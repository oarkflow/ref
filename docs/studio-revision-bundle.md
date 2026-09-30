# Revisions as bundles (design note)

Status: **implemented — Option B** (first-class bundle revisions). The user chose the multi-file bundle. The reference for operators is [deploy.md](deploy.md#bundle-revisions); this note keeps the design reasoning and records the final format.

## Final format

- `platform.Bundle` / `platform.BundleFile{Path, Content}` (`platform/bundle.go`): sorted, validated files. Bundles are **flat**: a path is a plain file name (no `/`), matching `LoadDir` and `ReadBundleDir`, which read only the top level of a directory. `Bundle.Source()` is the `LoadDir` concatenation (`platform.JoinBundle`, shared with `LoadFiles`). `Bundle.Locate(line)` and `LocateOffset` map the joined document back to `(file, line)`.
- `platform.ValidateBundle` and `platform.CompileBundle` fill `Diagnostic.Span.File`/`Line`/`Offset` per file. `platform.Validate(ctx, src, …)` is unchanged.
- `deploy.Revision.Files []platform.BundleFile` (`json:"files,omitempty"`) next to the derived `Source`; `Manager.ProposeBundle`; `Revision.ChangedFiles`.
- Checksum: `sha256` over sorted records `path ␀ decimal-length ␀ content` (`deploy.BundleChecksum`). Payloads: `ref-revision/v2|…`, `ref-revision-approval/v2|…`, HMAC over `bundle/v2|app|seq|checksum`. Revisions without `Files` keep the v1 formats byte for byte (pinned by `deploy/testdata/legacy_revision.json`).
- `SQLStore`: a nullable `files` column, added by `Migrate` to tables that predate it (dialect-aware catalog check, `ALTER` only when the column is missing). Files are stored in the column and left out of the `doc` JSON. Old rows read back without files.
- Admin: `POST /validate` and `POST /revisions` accept `{"files": {path: content}}` xor `{"source": …}`.
- Starter: `STARTER_SUPERVISOR=1` boots from `platform.ReadBundleDir` (one file per `.bcl`); `STARTER_REVIEWER_TOKEN` adds a second admin identity.

## Assets (implemented after bundles)

The trigger named at the bottom of this note ("revisions must include non-BCL files") arrived with the page tools, so a revision can now carry templates and static files next to its BCL:

- `platform.Assets` (`platform/assets.go`): `[]BundleFile` under `templates/` or `static/`, extension allowlist `.html .css .js .json .txt .svg`, UTF-8 text only, 1024 files / 1 MiB each / 8 MiB in all. `Assets.FS()` and `Assets.Overlay(base)` expose them as an `fs.FS`.
- `deploy.Revision.Assets` and `ChangedAssets`; `Manager.ProposeBundleAssets`; `Revision.AssetsFS(base)`.
- Payload **v3**, used only by revisions that have assets: `AssetsChecksum` over labelled, counted `bcl` and `assets` sections; `ref-revision/v3|…`, `ref-revision-approval/v3|…`, HMAC over `bundle/v3|…`. Legacy (v1) and bundle (v2) revisions are unchanged and pinned by fixtures (`legacy_revision.json`, `v2_revision.json`).
- `SQLStore`: a nullable `assets` column beside `files`, added by `Migrate` the same way (catalog check, `ALTER` only when missing).
- Admin: `assets` accepted with `files`; `changed_assets` in listings.
- Assets are **overrides**: a host reads them over its own files on disk, so a revision carries only what it changes.
- Hosts: `deploy.Supervisor.NewAppFor/MountFor/Closed`; `studio.AssetDraft` and `preview.Options.NewAppFor/MountFor` (which also receive a per-generation scratch directory); `studio/pages` reports which variables, layouts, includes and blocks a template uses.

The sections below are the original analysis.

## The problem

A `deploy.Revision` holds one `Source string`. The manager checksums it, signs it, validates it with `platform.Validate(ctx, src, baseDir, opts)` and the supervisor builds it with `platform.Compile(ctx, src, baseDir, opts)`.

An application, though, is a directory of numbered files (`examples/starter/resources/config/00_app.bcl` … `13_todo_pages.bcl`). `platform.LoadDir` reads them in name order and concatenates them, each followed by `"\n"`, into one buffer, then calls `Compile` with `baseDir = dir`. So the deployable unit is already one source: the concatenation.

A visual editor needs the opposite direction. It edits per file (a new route goes in `04_routes.bcl`), shows per-file diffs, and keeps a git-friendly layout. It must still hand the deploy machinery something it can checksum, sign and build.

Things any option has to preserve:

- The checksum and signatures cover exactly what gets compiled.
- `Validate` and `Compile` see identical input.
- Diagnostics can name a file and line. Today `bcl.ParseFile("", src)` gives spans into the concatenation, not into a file.
- `import` resolution and relative paths (`baseDir`) keep working.

## Option A: keep the single-blob revision; the bundle is a convention

The studio keeps files as its working model. To propose a revision it concatenates them exactly as `LoadDir` does and sends that string. Nothing changes in `deploy`.

To recover file identity, the studio inserts a marker comment between files, for example `# ---- file: 04_routes.bcl ----`. A comment does not change what the source means. The studio splits on those markers to rebuild the per-file view, and maps a diagnostic's concatenated line to `(file, line)` with a line table it keeps next to the revision (or recomputes from the markers).

- No schema or API change. Signatures, checksum, `Supervisor` and `Admin` work unchanged.
- The revision stores exactly what compiled. Rollback is trivial.
- File identity lives in a convention, not in the type. A hand-proposed revision without markers is just one anonymous file. That degrades gracefully but loses per-file diffs.
- Line mapping is the studio's job. Any other consumer (CLI, CI) needs the same marker logic.

## Option B: a first-class bundle in the revision

`Revision` gains `Files []File` (`{Path, Content}`, sorted by path) next to, or instead of, `Source`. The canonical source is derived: `Bundle.Source()` concatenates in path order, exactly like `LoadDir`.

- The checksum and both signatures cover a canonical encoding of the file list: `sha256` over `path\0len\0content` records in sorted order, so a rename or a moved byte changes it. `SigningPayload` and `ApprovalPayload` include that hash.
- `Validate` and `Compile` take the bundle (or the derived `Source` plus a line table), so diagnostics carry `File` in `SourceSpan` natively.
- `Admin` accepts `{"files": {"04_routes.bcl": "..."}}` in addition to `{"source": ...}`; a plain `source` becomes a one-file bundle.
- Store changes: `SQLStore` needs a `files` JSON column and a migration. Old rows read back as a one-file bundle, so existing revisions stay valid.
- Signature compatibility needs care: a revision signed before the change must still verify. Keep the old payload format for revisions with no `Files`, and add a versioned payload for bundles. This is the same upgrade path `docs/deploy.md` already describes for approval signatures.
- More work and a wider blast radius (`revision.go`, `store.go`, `admin.go`, `supervisor.go`, `platform.Validate`, and every signing test).

## Recommendation (superseded: Option B was chosen)

Start with **Option A** and design so that **Option B** is a later, mechanical upgrade.

Reasons:

1. The studio's first milestones (form editing, splice engine, live preview) do not depend on per-file revisions. They only need a faithful `LoadDir`-equivalent concatenation, which is what A gives with zero changes to the security-sensitive signing code.
2. Reviewers get per-file diffs anyway: the studio can render them from its markers, and `deploy.DiffDocuments` already gives block-level change lists.
3. Changing what a signature covers is the riskiest edit in the deploy package. It is better made once, later, with the real needs known (for example, whether the store should hold non-BCL assets such as templates).

To keep the upgrade cheap, do these now:

- Put the concatenation in one exported function (`platform.JoinBundle(files) []byte`, used by `LoadFiles` too) so the studio and `LoadDir` cannot drift.
- Use a fixed, documented marker format, and make the studio's splitter reject a source that does not round-trip through it.
- Have `Validate` accept an optional file table so `Diagnostic.Span.File` and `Line` can be filled per file without changing the signature of anything else.

Move to Option B when any of these becomes true: revisions must include non-BCL files (templates, static assets), multiple people edit different files of one revision concurrently, or external tooling needs file-level provenance in the signed record.
