# Configuration lifecycle: revisions, review, hot swap, rollback

Every change to an application's BCL document is a **revision**. The `deploy` package takes each revision through the same steps:
1. it is validated without touching any database;
2. it is diffed against what is live;
3. it is approved by someone other than its author;
4. it is activated;
5. if needed, it is rolled back.

A **Supervisor** serves the active revision. When a new revision is activated, the swap refuses no connection. A revision that fails to build never replaces the one being served.

```
propose ─▶ pending ─▶ approved ─▶ active ─▶ superseded ─(rollback)─▶ active
              │            │          │
              ▼            ▼          ▼
          rejected     rejected    failed  (did not build; the previous revision stays live)
```

## Static validation

`platform.Validate(ctx, src, baseDir, opts)` runs every check `Compile` does before opening a resource:
- parsing, shapes, roles and entity expansion;
- document cross-references, such as routes that name unknown intents;
- that every resource kind is registered;
- every pipeline and its expressions, and every flag.

It opens nothing: no database connection, no migration, no listener. Secrets that aren't set in the validating process are reported as **warnings**, because the deployment that activates the revision may have them.

`platform.DiffDocuments(before, after)` lists every named block (resource, shape, role, intent, route, process, pipeline, entity, flag) that was added, removed or changed. Reviewers see this on each revision.

> Expressions are checked during validation: an unclosed bracket, a dangling operator, leftover tokens (`(a`, `a ==`, `"USD" "NPR"`) or a call to a function that does not exist (`uper(name)`) makes the revision invalid. Errors that depend on data, such as dividing text, still appear only at run time.

## The workflow

```go
m := &deploy.Manager{
	Store:     store,                          // deploy.NewSQLStore(db, "postgres", "") or NewMemoryStore()
	App:       "passport",
	Secret:    []byte(os.Getenv("DEPLOY_SIGNING_SECRET")),
	Signer:    keys,                           // optional: a *signing.KeySet with an Ed25519 key
	Approvals: 1,                              // distinct reviewers required; 0 = no review (development)
	Validate: func(ctx context.Context, src []byte) platform.ValidationReport {
		return platform.Validate(ctx, src, baseDir, platform.DefaultLoadOptions())
	},
}
```

`deploy.NewSQLStore` takes the dialect `"postgres"`, `"mysql"` or `"sqlite"`. Its tests run on SQLite and on MariaDB 10.11 for the `mysql` dialect; MySQL 8 itself has not been run.

- **Propose** validates the document and refuses an invalid one, returning the full report. It records:
  - the source and its SHA-256 checksum;
  - an HMAC signature (with `Secret`) and an Ed25519 or RSA key signature (with `Signer`);
  - the author and message;
  - the diff against the active revision.
- **Approve** records a reviewer. The author can't approve their own revision unless `AllowSelfApproval` is set, the same reviewer can't approve twice, and `Approvals` distinct reviewers are required.
- **Reject** needs a reason.
- **Activate** applies only to an approved revision, after verifying its checksum, its signature and its signed approvals (see [Signing revisions](#signing-revisions)). A source edited directly in the store can't be activated. The previously active revision becomes `superseded`.
- **Rollback** re-activates a superseded revision: the one named, or the most recent. Its approval signature is verified too (see [Signed approvals](#signed-approvals)).
- Every step is recorded in the revision's `history`.

## Bundle revisions

An application is usually several BCL files (`resources/config/00_app.bcl` … `13_todo_pages.bcl`). A **bundle revision** keeps them apart:

```go
files, _ := platform.ReadBundleDir(dir)          // or platform.NewBundle([]platform.BundleFile{{Path, Content}, …})
rev, err := m.ProposeBundle(ctx, files, "alice", "add /probe")
```

- `platform.NewBundle` validates and sorts the files. A path must be a plain file name (bundles are flat: no `/`, no directories, matching what `LoadDir` reads), contain no control characters, end in `.bcl` and be unique. Limits: 512 files, 4 MiB per file, 16 MiB in all.
- `Bundle.Source()` is the document the compiler reads: the files in path order, each followed by `"\n"`. This is byte for byte what `platform.LoadDir` compiles for the same directory, and `LoadDir`/`LoadFiles` use the same `platform.JoinBundle`, so the two cannot drift.
- A revision stores `files` and the derived `source`. Old readers that only know `source` keep working. `Source` is re-derived from the files whenever the revision is verified, so editing one but not the other is detected.
- Validation runs over the joined source, and every diagnostic's `span` names the file and the line within it. Set `Manager.ValidateBundle` (typically `platform.ValidateBundle` with the deployment's options) to get that; without it, the bundle is checked with `Validate` over its joined source and spans refer to the joined text. `platform.CompileBundle` does the same for compile errors.
- `changed_files` lists the files that differ from the active revision (`added`, `modified`, `removed`), in path order. When the active revision is a legacy one (no files) every file counts as `added`. It is review metadata and is not signed.

`Propose(ctx, src, …)` still records a single-document revision exactly as before.

### Signing formats

Three formats, told apart by what the revision carries. Each is frozen once shipped: revisions without files are **legacy** (v1) and bundles without assets are **v2**, and nothing about either changed when assets arrived. Their checksum, HMAC and payloads are byte-for-byte what earlier versions produced. `deploy/testdata/legacy_revision.json` pins a revision signed before bundles existed and `deploy/testdata/v2_revision.json` one signed before assets existed; tests verify both, under the HMAC, the key, both, and with neither configured.

| | Legacy v1 (no files) | Bundle v2 (`files`) | Bundle with assets v3 (`files` and `assets`) |
|---|---|---|---|
| `checksum` | `sha256(source)` | `sha256` over the sorted records `path ␀ len ␀ content` (`deploy.BundleChecksum`) | `sha256` of `ref-bundle/v3␀`, then the section `bcl␀count␀` and its records, then `assets␀count␀` and its records (`deploy.AssetsChecksum`) |
| `SigningPayload` | `ref-revision/v1\|app\|seq\|checksum` | `ref-revision/v2\|app\|seq\|checksum` | `ref-revision/v3\|app\|seq\|checksum` |
| `ApprovalPayload` | `ref-revision-approval/v1\|app\|seq\|checksum\|approved\|[approvers]` | `ref-revision-approval/v2\|…` (same fields) | `ref-revision-approval/v3\|…` (same fields) |
| HMAC of the proposal | over `app\|seq\|checksum` | over `bundle/v2\|app\|seq\|checksum` | over `bundle/v3\|app\|seq\|checksum` |

In a record, `len` is the decimal byte length of the content and `␀` is a NUL byte, so the encoding is unambiguous. Because the checksum covers the layout, renaming a file, or moving bytes from one file to the next so that the joined source stays identical, changes the checksum and fails verification. In v3 the labelled, counted sections make the boundary between BCL files and assets unambiguous too: moving a file's bytes into an asset (or the reverse), renaming an asset, editing, adding or removing one, all change the checksum. Stripping the assets from a v3 revision (or attaching them to a v2 one), stripping the files from a bundle revision, or attaching files to a legacy one also fails: each format's checksum can only be produced by its own rules, and a v3 checksum with no assets is not the v2 checksum. `Verify` additionally requires the files (and assets) to be valid, in canonical (sorted) order, and the files to join into `source` exactly; assets without files are refused.

## Assets: templates and static files in a revision

A bundle revision may also carry **assets**: page templates and static files that override the ones an application ships on disk.

```go
rev, err := m.ProposeBundleAssets(ctx, files, []platform.BundleFile{
    {Path: "templates/pages/todos/list.html", Content: "…"},
    {Path: "static/css/app.css", Content: "…"},
}, "alice", "new todo list page")
```

- `platform.NewAssets` validates and sorts them (`Assets`, a `[]BundleFile`). A path is slash-separated, clean (no `.`, `..`, empty segments, backslashes or control characters), relative, and under `templates/` or `static/`. The extension must be one of `.html .css .js .json .txt .svg` (compared case-insensitively), so a `.bcl` file can never be smuggled in as an asset. No hidden path segments (`.git/…`), no duplicates, and no asset may be a directory of another. Content must be UTF-8 text without NUL bytes. Limits: 1024 assets, 1 MiB each, 8 MiB in all.
- Assets are **overrides**. A host reads them over its own files on disk, path for path, so a revision carries only what it changes and every other page keeps coming from disk. `platform.Assets.Overlay(base fs.FS)` (or `Revision.AssetsFS(base)`) is that layering as an `fs.FS`; `Assets.FS()` is the assets alone. A revision with no assets hands back `base` unchanged.
- Assets need files: a revision cannot have assets without BCL files.
- `changed_assets` lists what differs from the active revision (`added`, `modified`, `removed`), like `changed_files`. Against an active revision with no assets, every asset counts as `added`. It is review metadata and is not signed.
- `Manager.ProposeBundle` is `ProposeBundleAssets` with no assets. A revision with no assets is exactly the v2 revision it always was.

### Host wiring

`deploy.Supervisor` offers per-revision hooks for a host that renders a revision's assets:

- `NewAppFor(rev)` and `MountFor(app, p, rev)` replace `NewApp`/`Mount` when set, and receive the `*Revision` being built.
- `Closed(rev)` runs once a generation has drained and its resources are closed (also when mounting fails), so anything made for that revision can be released.

The starter's `STARTER_SUPERVISOR=1` mode shows the pattern. Its template engine reads templates from a directory, so for a revision with template assets it creates a scratch directory in `NewAppFor`, fills it in `MountFor` (`web.MaterializeTemplates`: the templates on disk, then the revision's on top; a failure fails the build and the running revision keeps serving), and removes it in `Closed`. Static assets are served by `web.StaticOverlay("/static", assets)`, registered before the document's routes, so an override wins over the file on disk and any path the assets don't define falls through to the `static` block. A revision without template assets uses the directory on disk directly.

## Signing revisions

A revision can carry two signatures, and `Verify` (which `Activate` and `Rollback` run) accepts either one.

- **HMAC** (`Secret`): HMAC-SHA256 over the app, sequence and checksum, stored in `signature`. Anyone who can check it can also forge it, so it only protects against edits by someone without the secret.
- **Key signature** (`Signer`, optionally `Verifier`): an asymmetric signature over `deploy.SigningPayload(r)` (`ref-revision/v1|app|seq|checksum`, `v2` for a bundle revision, `v3` when it has assets; see [Signing formats](#signing-formats)), stored in `key_signature` as `{alg, kid, sig}`. Ed25519 is the intended key; RSA (RS256, PS256) also works. The private key can live on the machine that proposes revisions, while the machines that activate them hold only the public key.

```go
key, _ := signing.ParsePrivateKeyPEM("deploy-2026", signing.EdDSA, pemBytes) // or signing.GenerateEd25519
keys, _ := signing.NewKeySet(key)                                           // add old public keys to rotate
m.Signer = keys                                                              // signs and, by default, verifies

// A host that only activates: public keys from a JWKS document.
public, _ := signing.ParseJWKS(jwksJSON)
m.Verifier = public
```

Verification rules:
- The content must always match its checksum: `source` for a legacy revision, the files (well-formed, sorted, joining into `source`) for a bundle revision, and the files and the assets (well-formed, sorted) for a revision with assets.
- With neither `Secret` nor a verifier, only the checksum is checked.
- Otherwise, a valid key signature **or** a valid HMAC is required. Revisions signed before a key was added keep verifying through their HMAC, so a key can be introduced without re-signing history.
- A tampered source, a dropped signature or a signature by another key fails with `ErrTampered`.
- When `Verifier` is nil and `Signer` can verify (a `*signing.KeySet` can), `Signer` is used.

### Signed approvals

The proposal signature does not cover `status` or `approvals`, so when a revision becomes approved (the last required approval, or at proposal when `Approvals` is 0) it is signed a second time, with the same `Secret` and `Signer`, over `deploy.ApprovalPayload(r)`: `ref-revision-approval/v1|app|seq|checksum|approved|["sorted","approvers"]` (`v2` for a bundle revision, `v3` when it has assets). The signatures are stored in `approval_signature` (HMAC) and `approval_key_signature`.

`VerifyActivation` runs on `Activate`, `Rollback` and whenever the supervisor starts a revision. It runs `Verify`, then requires a valid approval signature (key or HMAC, by the same rules) and an approvals list of distinct approvers, none of them the author unless `AllowSelfApproval`, meeting the Manager's `Approvals` threshold. A status or approvals list edited in the store therefore fails with `ErrTampered`.

**Upgrading:** revisions approved or activated before approval signatures existed have none and can no longer be activated, rolled back to or served. Re-propose the document and approve it again (pending revisions just need their approvals recorded as usual). Plan the upgrade so the supervisor restarts onto a re-approved revision.

The key types, JWKS format and rotation are described in [identity-and-signing.md](identity-and-signing.md).

## Serving and hot swap

```go
sup := &deploy.Supervisor{Manager: m, Build: func(ctx context.Context, src []byte) (*platform.Platform, error) {
	return platform.Compile(ctx, src, baseDir, platform.DefaultLoadOptions())
}}
ln, _ := net.Listen("tcp", ":8080")
go sup.Serve(ctx, ln)
```

The supervisor owns the listener. Each accepted connection is handed to the current generation. On activation (checked every `Poll`, or immediately via `Notify`), the swap works like this:
1. The new revision is built.
2. New connections go to it.
3. The old generation keeps serving for `Grace` (default 2s), so connections handed to it just before the swap are answered rather than closed as idle.
4. It then drains: in-flight requests finish (bounded by `Drain`, default 30s) and answer `Connection: close`, so clients reconnect to the new generation. Its resources close last.

If the build fails, the supervisor marks the revision `failed` with the error, restores the previous revision as active, and keeps serving it.

In the test suite, thousands of concurrent requests cross a swap. Requests on fresh connections never fail. As with any HTTP server that shuts down, a keep-alive client can occasionally send a request on an idle connection at the moment the old generation closes it. Standard clients retry idempotent requests in that case, and the test does the same (one retry).

## Admin API

`(&deploy.Admin{Manager: m, Supervisor: sup, Tokens: map[string]string{token: "alice", …}}).Handler()` is a `net/http` handler. Mount it on an internal port.

| Method and path | Purpose |
|---|---|
| `GET /revisions`, `GET /revisions/{id}`, `GET /active` | Read revisions; `/active` also says which revision is actually being served |
| `POST /validate {source}` or `{files[, assets]}` | Static validation report; with `files`, diagnostics name the file |
| `POST /revisions {source \| files[, assets], message}` | Propose; `422` with the report when invalid |
| `POST /revisions/{id}/approve \| reject \| activate {comment}` | Decide |
| `POST /rollback {to?, reason}` | Roll back |

A document is sent as `source` (one string) or as `files` (an object of relative path to content), exactly one of the two. `files` creates a bundle revision: `400` if both or neither are sent, `422` for a bad path or an empty set, `413` when the total exceeds `Admin.MaxSource` (default 4 MiB). `GET /revisions/{id}` returns `files` as `[{path, content}]` in path order alongside `source`; the listing and `/active` return `files` (paths) and `changed_files` for bundle revisions.

`files` may be accompanied by `assets`, an object of path (`templates/…` or `static/…`) to content: `422` for an invalid asset path, extension or content, `400` for `assets` sent with `source`, and the `413` limit counts files and assets together. `GET /revisions/{id}` returns `assets` as `[{path, content}]`; the listing and `/active` return `assets` (paths) and `changed_assets` for a revision that has them.

Each request authenticates with `Authorization: Bearer <token>`. The token identifies the person, which is what makes the four-eyes rule enforceable. Tokens are looked up by hash, so the comparison doesn't leak timing.
