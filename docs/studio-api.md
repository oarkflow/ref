# Studio architecture and API contract (v1)

Status: contract. Backend (`studio/server`), preview (`studio/preview`) and the web app (`studio/web`) are built against this document. Change it here first.

## Decisions (fixed)

- Canonical config is BCL. Edits are ops applied with `studio/model` (span splice + `bcl.Format`); the AST is never printed.
- A working copy is a **draft**: a `platform.Bundle` (flat set of `*.bcl` files, no subdirectories) plus a `model.History` per file. Publishing a draft creates a `deploy` bundle revision through the normal validate -> approve (by someone else) -> activate flow.
- Bundle paths are flat: `NewBundle` rejects any `/` in a path.
- Studio runs in-process: `studio.Handler(Config) http.Handler` is mounted by the host (the starter mounts it on the admin listener at `/studio` when `STARTER_STUDIO=1`). A standalone `cmd/studio` may be added later.
- Frontend: React + TypeScript + Vite + React Flow (canvas), built into `studio/web/dist` and embedded with `go:embed`. `dist` is committed so `go build` needs no Node.
- Auth: bearer token -> identity `{name, roles}`. Roles: `viewer`, `editor`, `reviewer`, `admin`. Editors edit drafts; reviewers approve; the author of a revision cannot approve it (enforced by `deploy`). Every mutating call is written to an audit log.
- All JSON, base path `/api/v1`. Errors: `{"error": {"code": "string", "message": "string", "details": any}}` with HTTP 400/401/403/404/409/413/422.

## Types

```ts
type Diagnostic = {
  severity: "error" | "warning" | "info"; code?: string; message: string;
  file?: string; line?: number; column?: number; offset?: number;
  path?: string;            // "route/web.todos_list/path"
};
type BlockNode = {          // from model.Node
  kind: "block" | "field";
  path: string;             // canonical model path
  type?: string; id?: string; name?: string;   // block type/id or field name
  raw?: string;             // field value source
  comment?: string; line: number; start: number; end: number;
  children?: BlockNode[];
};
type Op =
  | { op: "setField";    file: string; path: string; value: string }   // value = raw BCL
  | { op: "removeField"; file: string; path: string }
  | { op: "addBlock";    file: string; parent?: string; type: string; id?: string; body?: string }
  | { op: "removeBlock"; file: string; path: string }
  | { op: "renameBlock"; file: string; path: string; newId: string }
  | { op: "moveBlock";   file: string; path: string; index: number }
  | { op: "addFile";     file: string; content?: string }
  | { op: "renameFile";  file: string; newFile: string }
  | { op: "removeFile";  file: string };
type DraftSummary = {
  id: string; owner: string; name: string; baseRevision?: string; version: number; // version bumps on every change
  files: string[]; dirty: boolean; createdAt: string; updatedAt: string;
  diagnostics: { errors: number; warnings: number };
};
```

## Endpoints

Identity/meta
- `GET /meta` -> `{app, version, identity:{name,roles}, activeRevision?, features:{preview:boolean, pages:boolean}}`

Schema
- `GET /schema/blocks` -> `platform.BlockSchemas()` JSON (with docs attached when available).
- `GET /schema/catalog` -> `Registry.Catalog()` JSON (resource kinds, actions, node types, edge types).

Drafts (editor+)
- `GET /drafts` -> `DraftSummary[]` (own drafts; admin sees all)
- `POST /drafts` `{name?, from: "active" | "revision:<id>" | "dir"}` -> `DraftSummary` (`dir` = the host-configured config directory)
- `GET /drafts/{id}` -> `DraftSummary & {diagnostics: Diagnostic[]}`
- `DELETE /drafts/{id}`
- `GET /drafts/{id}/files` -> `[{path, size, diagnostics:{errors,warnings}}]`
- `GET /drafts/{id}/files/{file}` -> `{path, content, version}` (raw source, for the escape-hatch text view)
- `PUT /drafts/{id}/files/{file}` `{content, ifVersion}` -> raw replace (must parse; else 422 with diagnostics)
- `GET /drafts/{id}/files/{file}/tree` -> `BlockNode[]`
- `GET /drafts/{id}/tree` -> `{[file]: BlockNode[]}` (top-level blocks only, `children` omitted; for the navigator)
- `POST /drafts/{id}/ops` `{ops: Op[], ifVersion: number, dryRun?: boolean}` -> `{version, applied: number, diagnostics: Diagnostic[], changed: string[]}`. Atomic: all ops or none. 409 `stale` if `ifVersion` != current.
- `POST /drafts/{id}/undo` / `redo` -> same result shape as ops
- `POST /drafts/{id}/format` -> same result shape (reformat all files)
- `POST /drafts/{id}/validate` -> `{valid, diagnostics: Diagnostic[], summary}` (`platform.ValidateBundle`)
- `GET /drafts/{id}/diff` -> `{files: [{path, status:"added"|"modified"|"removed", unified?: string}], changes: platform.DocumentChange[]}` against its base
- `GET /drafts/{id}/events` (SSE) -> events `changed {version, changed[]}`, `diagnostics {diagnostics[]}`, `preview {status, message?}`
- `POST /drafts/{id}/propose` `{message}` (editor+) -> `Revision` (validates; 422 with diagnostics on error)

Revisions (delegate to `deploy.Manager`, identity from token)
- `GET /revisions?limit=` , `GET /revisions/{id}` (includes `files`, `changed_files`, `changes`)
- `POST /revisions/{id}/approve|reject` `{comment}` (reviewer+), `POST /revisions/{id}/activate` (reviewer+), `POST /rollback` `{to?}` (admin)
- `GET /audit?limit=` (admin) -> `[{at, who, action, target, detail}]`

Preview (editor+; see `studio/preview`)
- `POST /drafts/{id}/preview` -> `{status:"starting"|"ready"|"failed", url:"/preview/{id}/", error?: Diagnostic[]}` builds a preview generation from the draft; idempotent, rebuilds if the version changed
- `DELETE /drafts/{id}/preview`
- `GET /preview/{id}/...` -> served by the preview generation (proxied to it). The generation is built with the `preview` profile: data-bearing resources overridden to sqlite-in-memory/memory/noop, workers, schedules and triggers disabled, outbound service/smtp resources replaced by a recording stub. `GET /drafts/{id}/preview/requests` -> the last N recorded requests and stubbed outbound calls.

## Go seams (so packages build independently)

`studio` (package `studio`, files `types.go`, `studio.go`, `assets.go`) holds every type the packages share, so no package imports another's internals:

```go
package studio

type Identity struct{ Name string; Roles []string }            // types.go
type Draft interface {                                          // types.go
    ID() string; Version() int64
    Bundle() platform.Bundle           // snapshot
    Subscribe() (<-chan int64, func()) // version after each change; closed on delete
}
type DraftSource interface{ Get(id string) (Draft, bool) }      // types.go; *server.Server implements it

type Role string // RoleViewer < RoleEditor < RoleReviewer < RoleAdmin; Identity.Has(min)
type Diagnostic struct{ Severity, Code, Message, File string; Line, Column, Offset int; Path string }
func FromPlatform([]platform.Diagnostic) []Diagnostic

type PreviewStatus struct {
    Status  string       // "starting" | "ready" | "failed" (the server also publishes "stopped")
    URL     string       // handler-relative "/preview/{id}/"; the server prefixes Config.BasePath
    Version int64        // draft version the generation was built from
    Error   []Diagnostic // build errors when failed
    Message string
}
type RecordedRequest struct {
    At time.Time; Kind string // "request" | "outbound"
    Method, URL string; Status int; DurationMS float64; Detail map[string]any
}
// PreviewManager is what studio/server needs from studio/preview.
type PreviewManager interface {
    Ensure(ctx context.Context, d Draft) (PreviewStatus, error) // idempotent; rebuilds if d.Version() changed
    Stop(id string)
    Handler() http.Handler // serves /preview/{id}/... (the full request path is passed through)
    Requests(id string) []RecordedRequest
}
func WebAssets() fs.FS // embedded studio/web/dist, rooted at index.html
```

`studio/preview` implements `studio.PreviewManager` and uses the `studio` types above for status and recorded requests (it must not define its own copies). It takes the same `NewApp`/`Mount` hooks as `deploy.Supervisor`, plus `Profile` ("preview"). `studio/server` talks to previews through `studio.PreviewManager`; the host may pass a ready one (`server.Config{Preview: m}`) or let the server build and own a `studio/preview` service (`server.Config{PreviewOptions: &preview.Options{...}}`, so `studio/server` does import `studio/preview` for that and for its status events).

## Frontend layout (`studio/web`)

`src/api/` typed client generated by hand from this file; `src/state/` draft store (version, optimistic ops, undo/redo, SSE); `src/forms/` schema-driven inspector (field widgets: string, int, bool, duration, enum, list, map, block, plus a "literal / env() / expression" switch on every scalar); `src/nav/` file and block navigator; `src/diag/` diagnostics panel; `src/preview/` iframe + request console; `src/canvas/` React Flow editors for `intent`, `process`, `pipeline`; `src/revisions/` history, diff, approval; `src/pages/` template/page tooling.

## Implementation notes (studio/server)

Behaviour of the implemented server where the sections above leave room, or where it differs. This section wins over the text above.

**Roles and auth**
- Roles are hierarchical: `viewer < editor < reviewer < admin`; an identity holds the highest of its roles. A reviewer may also edit drafts (the author of a revision still cannot approve it: `deploy.Manager` refuses, surfaced as 403 `forbidden`).
- Tokens must be 16+ characters; `server.New` fails on a short token, an empty name, no roles or an unknown role.
- A draft belongs to its creator; only they and admins can read or change it (403 otherwise; 404 if it does not exist). `GET /drafts` lists your drafts, admins see all. A user may hold `Config.MaxDraftsPerOwner` (default 50) drafts, then 409 `too_many_drafts`.
- `GET /drafts/{id}/events` also accepts `?access_token=<token>` because `EventSource` cannot set headers. Every other endpoint needs the `Authorization: Bearer` header.

**Errors** are `{"error":{"code","message","details?"}}`. Codes: `bad_request` 400, `unauthorized` 401, `forbidden` 403, `not_found` 404, `stale` 409 (details `{version}`), `conflict` 409, `nothing_to_undo`/`nothing_to_redo` 409, `too_many_drafts` 409, `too_large` 413, `invalid` 422 (details `{diagnostics, errors, warnings}`), `invalid_bcl` 422 (details `{diagnostics}`), `op_failed` 422 (details `{index, op}`), `invalid_bundle` 422, `not_implemented` 501, `internal` 500.

**Drafts**
- `from` defaults to `"active"`. A legacy single-document revision becomes one file, `main.bcl`. `baseRevision` is set for `active` and `revision:<id>`; the diff base is what the draft started from.
- Bundles are flat: `platform.NewBundle` rejects any `/` in a path, so `addFile`/`renameFile` with `sub/x.bcl` fail (`op_failed`). A draft always keeps at least one file.
- `ifVersion` is optional everywhere; omitted means "do not check". A stale value gives 409 `stale` and changes nothing.
- `POST .../ops` is atomic: any failing op rolls the whole batch back (422 `op_failed`, `details.index`). `dryRun` returns the diagnostics of the would-be result and commits nothing. A batch that changes no bytes leaves `version` unchanged and `changed` empty. Ops do not need to leave the draft *semantically* valid; the result carries diagnostics instead. Syntax must stay valid (`model` re-verifies every op).
- Undo/redo is draft-level: one step per committed batch (including file add/rename/remove and `PUT` file), up to 200 steps, and it bumps `version` (versions only increase). This replaces the per-file `model.History` idea so a batch across files undoes as one step.
- `PUT /files/{file}` replaces an existing file's text (404 if absent; use `addFile` to create). Content must parse as BCL (422 `invalid_bcl` with syntax diagnostics). A file that parses but that the structural editor cannot open (e.g. `schema` bodies the scanner disagrees with) is kept as text: its tree is empty, structural ops on it fail, and validation reports an `info` diagnostic `studio.text_only`.
- Result shape of `ops`, `undo`, `redo`, `format`, `PUT file`: `{version, applied, diagnostics, changed}`. `diagnostics` is never null.
- `GET /drafts/{id}` returns the summary fields plus `diagnostics: Diagnostic[]` (the array replaces the summary's `{errors, warnings}` counts, which appear in `POST/GET /drafts` list and create responses).
- `GET /drafts/{id}/tree` lists each file's top-level statements (fields and blocks) without children. `GET .../files/{file}/tree` is the full tree (depth-capped at 64). A `BlockNode` has an extra `opaque?: boolean` (a header the editor cannot rename).
- `GET .../diff` -> `{version, files: [{path, status, unified}], changes}`; only changed files are listed; `unified` is a 3-line-context unified diff (for a very large differing region the middle is shown as one replaced block).
- Validation uses `Manager.ValidateFiles` when a manager is configured (identical to what `propose` checks), else `platform.ValidateBundle` with `Config.LoadOptions`. Diagnostics are cached per draft version.
- `POST .../propose` -> 201 with the full `deploy.Revision`; an invalid draft gives 422 `invalid` with diagnostics.

**Events (SSE)**: on connect a `changed {version, changed: []}` event; then `changed {version, changed[]}`, `diagnostics {version, diagnostics[]}` after each change, `preview {PreviewStatus}` when the preview starts or stops (`stopped`). A `: ping` comment every 15s. The stream ends when the draft is deleted. `server.Server.Notify(draftID, event, data)` lets a host publish more (the preview manager publishes build progress this way).

**Revisions**: `GET /revisions` returns summaries (no source or file contents; `files` is a list of paths, plus `changed_files` and a `changes` count); `GET /revisions/{id}` returns the full revision. `approve`/`reject` take `{comment}`; `activate` and `rollback` (`{to?, reason}`) call `Config.OnActivate` so the host can tell the supervisor to swap. `GET /audit?limit=` (admin) returns `[{at, who, action, target, detail}]` newest first; actions: `draft.create|delete|ops|undo|redo|format|put_file`, `revision.propose|approve|reject|activate|rollback`, `preview.start|stop`.

**Preview endpoints**: without a `PreviewManager` all four answer 501 `not_implemented` and `/meta.features.preview` is false. `POST /drafts/{id}/preview` sets an HttpOnly cookie `studio_preview` scoped to `Config.BasePath + /preview/{id}/` (an iframe cannot send a bearer header); `/preview/{id}/...` accepts that cookie for that draft or a bearer token of the draft's owner/an admin, and is otherwise 401.

**Serving**: mount `server.Handler()` under a prefix with `http.StripPrefix` and set `Config.BasePath` to it. The web app is served from `Config.Web` (default `studio.WebAssets()`); unknown paths without a file extension get `index.html` (client-side routes). The web build must use relative asset URLs (Vite `base: './'`) and call the API at `api/v1` relative to the page.

**Starter**: `STARTER_STUDIO=1` (with `STARTER_SUPERVISOR=1`) mounts Studio on the admin listener at `/studio/`. `STARTER_ADMIN_TOKEN` is `admin`, `STARTER_REVIEWER_TOKEN` is `reviewer`, optional `STARTER_EDITOR_TOKEN` (16+ chars) is `editor`. `STARTER_STUDIO_SRC=<repo checkout>` attaches Go doc comments to the block schemas.

### Hardening, persistence, governance (studio/server)

**Persistence** (`server.OpenSQL(ctx, db, dialect, prefix)` -> `(*SQLStore, *SQLSink, error)`; dialects `sqlite`, `postgres`, `mysql`; tables `<prefix>drafts|blobs|audit|comments`, prefix default `studio_`). Pass them as `Config.Store`, and the sink as `Config.AuditStore` and `Config.Comments`. The defaults stay in memory.
- Drafts are written through after every committed edit (ops, undo, redo, format, file replace, create; deleted with their rows). The store loads every draft at start, so drafts survive a restart with their undo/redo history and diff base intact. File contents are stored once per distinct SHA-256 per draft; contents no history step or base file refers to any more are deleted. A draft that cannot be loaded is left in the database and listed in `SQLStore.Skipped`. One server owns a set of tables (the store caches all drafts); two servers on the same tables would not see each other's edits.
- A failed save is logged (`Config.Logf`), not returned: the edit is already applied in memory and the next save writes the whole state.
- Audit entries get an `id` (a nanosecond timestamp forced to increase) that orders them across restarts. With an `AuditStore` reads come from SQL (the ring still serves if the store fails). `GET /audit?limit=&before=` (admin): newest first, entries with `id < before`; a full page sets the response header `X-Next-Before` to the cursor for the next page. `limit` defaults to 100, capped at 1000. `before` must be a positive integer (400 otherwise).

**Preview wiring**: `Config.PreviewOptions` builds and owns a `preview.Service` (closed by `Server.Close`); its `Prefix` defaults to `BasePath + "/preview/"`, and the server puts `BasePath` back on the path it hands the service, so redirects, cookies and links inside the preview carry the mount prefix. A ready `Config.Preview` is used as given (build it with the matching prefix and mount it without stripping, or leave BasePath empty). When the manager offers `Events()` (the studio/preview service does) every status change is relayed as a `preview` event on that draft's stream (`starting`, `ready`, `failed`, `stopped`), otherwise the server publishes only its own start/stop events. `GET /drafts/{id}/preview/requests` returns the recorded requests and stubbed outbound calls. The iframe authenticates with the `studio_preview` cookie set by `POST .../preview` (HttpOnly, path `BasePath/preview/{id}/`, 12h).

**Revisions**
- `GET /revisions/{id}` and the list/`/meta.activeRevision` summaries add `required_approvals` (`deploy.Manager.Approvals`; 0 means a revision needs none).
- `GET /revisions/{id}/diff?against=<revision id>|active` (viewer+) -> `{from, to, files: [{path, status, unified}], changes}`: the change from `against` to `{id}`. Without `against`, the revision it was proposed against (`base_id`); `from` is `""` when there is none, and then every file is `added`. Block-level `changes` come from `platform.DiffDocuments` over the two validated bundles (empty if either document does not load). A legacy single-document revision counts as one file, `main.bcl`.
- `GET /revisions/{id}/comments` (viewer+) -> `Comment[]` oldest first. `POST` (editor+) `{body, replyTo?}` -> 201 `Comment` `{id, revisionId, author, at, body, replyTo?}`. `body` is trimmed, 1..8000 characters (422 `invalid_comment`); `replyTo` must be a comment of the same revision (422); a revision holds `Config.MaxCommentsPerRevision` (500) comments (409 `too_many_comments`). Comments are audited as `revision.comment`. Unknown revision: 404. Stored in the SQL sink when configured, else in memory.

**Limits**
- Mutating requests (POST, PUT, PATCH, DELETE) are rate limited per identity with a token bucket (`Config.RateLimit{Rate: 30/s, Burst: 60}`; `Rate < 0` disables); reads are not limited. Over the limit: 429 `rate_limited` with `Retry-After` (seconds). Failed authentication does not consume tokens.
- Body size: `Config.MaxBody` (24 MiB) applies to `POST .../ops` and `PUT .../files/{file}`; every other body is limited to `Config.MaxSmallBody` (1 MiB). Both give 413 `too_large`; a declared `Content-Length` above `MaxBody` is refused before the body is read. `POST .../ops` accepts at most `Config.MaxOps` (1000) ops (422 `too_many_ops`).
- Every response carries `X-Request-Id`: the client's value if it is 1..64 characters of `[A-Za-z0-9._-]`, else a generated `req_...`. Error bodies include it as `error.requestId`, and internal (500) errors are logged with it.
- CORS: none by default (same-origin only). `Config.AllowedOrigins` (exact origins, or `"*"`) lets those browser origins call `/api/`; a preflight from them gets 204 with `Allow-Methods/Headers` (no token needed), a preflight from any other origin gets 403 `forbidden_origin`, and other cross-origin responses simply carry no CORS headers. `X-Request-Id`, `X-Next-Before` and `Retry-After` are exposed. The web app and `/preview/` paths are not covered.
- `Server.Close()` ends every open event stream (each receives a final `shutdown` event), stops the preview relay and closes an owned preview service; it is idempotent. Call it before `http.Server.Shutdown`, which otherwise waits on the streams.

### Assets and page templates (studio/server)

A draft carries **assets** next to its BCL files: page templates and static files, the same `platform.Assets` a revision holds (paths under `templates/` or `static/`, extensions `.html .css .js .json .txt .svg`, size limits from `platform.NewAssets`). They live in the draft's snapshot (a key containing `/` is an asset; BCL names are flat), so they share the draft's `version`, undo/redo, persistence and diff base. **Every asset change bumps `version`**, so a preview built from the draft rebuilds. `Draft` implements `studio.AssetDraft`.

**Config**: `Resources fs.FS` (or `ResourcesDir string`) is the host's resources directory (the tree holding `templates/` and `static/`). Drafts override its files; it is read for the template catalog, `import-from-disk` and the disk fallback of `GET .../assets/{path}`. Only paths that pass the asset rules are ever read from it (root, extension, clean path), so `config/`, keys and other files in that directory are not reachable. `TemplateGlobals []string` adds variables the host renderer supplies to every template (built-in list: `appName`, `title`, `user`, `csrf`, `error`, `success`, `appVersion`, `currentYear`, ...), so the linkage check does not ask a route's intent for them.

**Draft summaries** add `assets: string[]` (paths). `files` stays BCL-only; `dirty`, `diff` and `changed` cover both, so `changed[]` in an edit result, in a `changed` SSE event and in `diff.files[]` can hold asset paths (a slash tells them apart).

**Ops** (in `POST /drafts/{id}/ops`, atomic with any BCL ops in the batch and one undo step): `putAsset {file: <asset path>, content}` (create or replace), `removeAsset {file}`, `renameAsset {file, newFile}`. A batch whose assets are invalid (bad path or extension, too big, a path that is also a directory) fails whole with 422 `invalid_assets`.

**Endpoints** (editor+, owner or admin)
- `GET /drafts/{id}/assets` -> `{version, assets: [{path, size, kind, status, overridesDisk}]}`. `kind`: `template` (a page), `layout`, `component`, `static` or `other`. `status` against the draft's base: `added | modified | removed | unchanged` (a removed asset is listed with size 0). `overridesDisk`: the host has a file at that path.
- `GET /drafts/{id}/assets/{path...}` -> `{path, content, version, kind, source}`; `source` is `draft`, or `disk` when the draft does not override it and the host has it. 404 otherwise.
- `PUT /drafts/{id}/assets/{path...}` `{content, ifVersion?}` -> `{version, applied, diagnostics, changed, template?}`. For `.html` templates the SPL parser checks the syntax first: an error is refused with 422 `invalid_template` (`details.diagnostics`, code `studio.pages.syntax`) unless the request has `?force=1`; the findings of a saved template come back in `template`. 400 for a path that is not an asset path; 409 `stale`; 422 `invalid_assets`. Body limit `MaxBody`.
- `DELETE /drafts/{id}/assets/{path...}[?ifVersion=n]`; 422 `op_failed` if it is not in the draft.
- `POST /drafts/{id}/assets/rename` `{from, to, ifVersion?}`.
- `POST /drafts/{id}/assets/import-from-disk` `{paths: string[], ifVersion?}`: copy host files into the draft as overrides. A path is an asset path (`templates/pages/x.html`) or a template name (`pages/x`, meaning `templates/pages/x.html`). One batch, one undo step. Already-overridden paths are skipped. -> `{version, imported[], skipped[], ...}`. 404 (`details.missing`, nothing imported) if any is not on disk; 400 for an invalid path; 422 `no_resources` if no resources directory is configured.
- `GET /drafts/{id}/templates` -> `{version, templates: TemplateInfo[], missing: MissingRef[], globals: string[]}`. `TemplateInfo`: `name` (`pages/todos/list`), `path`, `kind` (`page | layout | component`, from `layouts/`, `components/`, `partials/`), `source` (`disk | draft | override`), `extends`, `layouts` (chain, nearest first), `includes`, `vars` (free variables the template and everything it extends and includes read; best effort, see `studio/pages`), `blocks`, `unfilled`, `unknown`, `missing` (includes that do not exist), `diagnostics` (syntax), `routes` (`[{file, path, route, method, url, as: "template"|"layout", line}]`, from the draft's `route` blocks, including inside `route_group` with its `prefix`), and `unused` (no route names it and no template extends or includes it; the host may still render it from Go code, so it is a hint). `missing[]` lists route references to templates that do not exist. Only routes whose `template`/`layout` are plain strings are followed (not `env()` or expressions).
- `GET /drafts/{id}/templates/{name...}` -> one `TemplateInfo`. With the suffix `/preview-data` (`.../templates/pages/todos/list/preview-data`) -> `{name, version, guessed: true, vars, data}`, a sample object built from the variables and dotted paths the template reads, with types guessed from names (`id`, `count` -> numbers, `isX`/`enabled` -> booleans, `*At`/`date` -> ISO timestamps, plurals -> two sample items, `email`, `url`, `name`, `title` -> sample strings). It is a starting point for a sample render, not a contract.

**Linkage diagnostics** (warnings, code prefix `studio.pages.`, in every draft diagnostics list and `validate` result; `path` is `route/<id>/template|layout|intent`, with `file` and `line` of the field): `template_missing`, `layout_missing`, `include_missing`, `include_cycle`, and `vars_unprovided`. The last is deliberately shy: a variable counts as provided if its name appears anywhere in the route's intent block (or is a host global), so it fires only when the intent never mentions something the template reads. `studio.pages.syntax` (severity as the parser reports, usually error) is attached to a template asset the draft carries (`file` is its asset path).

**Propose and revisions**: `POST /drafts/{id}/propose` calls `Manager.ProposeBundleAssets` when the draft has assets (v3 revision), else `ProposeBundle`. Revision summaries add `assets` (paths) and `changed_assets`; the detail carries `assets` with content. `GET /revisions/{id}/diff` includes asset files (with unified diffs) next to the BCL files. A draft created from `active` or `revision:<id>` starts with that revision's assets, as its base.

### Journeys (studio/server, studio/pages)

A journey graph shows how the app behaves for a visitor: the links, forms, buttons and script requests on each page, the routes they reach, the intents those routes run, the resources the intents use, and where the visitor lands next. It is derived on demand from the draft's BCL files and templates (with the host's templates and scripts as the base) and cached per draft version, so it always reflects the draft, not the live app.

**`GET /drafts/{id}/flows`** (viewer+, and the draft's owner or an admin) `[?focus=<node id>&depth=N]`

Without `focus` it returns the whole app. With `focus` (a node id such as `page:pages/todos/list`, `route:web.todos_create` or `intent:todo.create`) it returns the nodes within `depth` edges of it (default 2, 0-8), following edges in both directions. A shared element does not lead back out to the other pages that hold it. 400 `bad_focus` / `bad_depth`; 404 if the node does not exist.

```ts
type FlowGraph = {
  version: number;            // the draft version it was built from
  focus?: string; depth?: number;
  nodes: FlowNode[]; edges: FlowEdge[];
  stats: { nodes: {[kind]: number}; edges: {[kind]: number}; pages: number; routes: number;
           elements: number; sharedElements: number; unresolved: number; unusedPages: number };
  warnings: { code: string; severity: "warning" | "info"; message: string; node?: string; file?: string; line?: number }[];
};
type FlowNode = {
  id: string;                 // stable: derived from names and content, not from order
  kind: "page" | "element" | "route" | "intent" | "resource" | "external" | "unresolved";
  subkind?: string;           // element: link|form|button|fetch; intent: "intent"|"process";
                              // route: lower-case method; resource: database|email|http|files|...
  label: string;              // "Save draft", "POST /todos", "todo.create", "Todos"
  group?: string;             // container to draw it in: "page:<template>" or "shared:components/navbar"
  shared?: boolean;           // element that comes from a layout or component
  file?: string; line?: number; path?: string;   // where to jump: BCL file + model path, or template/script asset path
  data?: {...};               // kind-specific, below
};
type FlowEdge = { id: string; kind: "contains"|"navigates"|"calls"|"runs"|"renders"|"redirects"|"uses";
                  from: string; to: string; label?: string; shared?: boolean; data?: {...} };
```

**Node ids**: `page:<template name>`, `element:page:<template>#<hash>` (a repeat on one page gets `.1`, `.2`), `element:shared:<component file>#<hash>` (one node however many pages include the component), `route:<route id>`, `intent:<name>`, `process:<name>`, `resource:<name>`, `external:<hash>`, `unresolved:<hash>`. The hash covers the element's kind, method, URL, label, redirect and fields, so editing one page does not renumber the others.

**`data` by kind**
- `page`: `template`, `routes` (ids of the routes that render it), `protected`, `public`, `unused` (no route renders it), `heading`. The label is the page's `<h1>`/`<title>`, else a humanised file name.
- `element`: `method`, `url` (normalised, `:param` for `${...}` and numeric segments), `rawUrl`, `redirect`, `fields` (form input names, CSRF fields left out), `hints` (`data-*`, `onclick`, `script`), `source` (template or script), `external`, `dynamic`, `via` (the template that loads the script).
- `route`: `name`, `method`, `path` (with its `route_group` prefix), `public`/`protected`, `auth`, `session`, `roles`, `rateLimited`, `intent`, `process`, `template`, `layout`, `group`. Group settings (`auth`, `session`, `authz`, `rate_limit`) are inherited.
- `intent`: `name`, `steps`, `stepKinds` (`{decision: 2, data: 1, ...}` by action family), `resources`, `description`. A process has `subkind: "process"`.
- `resource`: `name`, `kind` (`database.sql`), `category`.

**Edges**: `contains` page -> element (`shared: true` for elements inherited from a layout or component); `navigates` link or GET route -> route; `calls` form, button or fetch -> route (label is the method); `renders` route -> page (`shows`); `runs` route -> intent or process, and intent -> a child intent it invokes; `uses` intent -> resource (label is the category); `redirects` route -> the page or route it sends the visitor to, labelled `on success`, from a `redirect`/`next`/`return_to` query parameter on the element that calls it (`data.source: "query"`, `data.via`: the element ids) or from the route's own `redirect` field (`data.source: "route"`). Failure paths (`fails-to`) are not derived.

**Matching**: an element's method and normalised URL are matched to routes segment by segment (`:id`, `{id}` and `:param` match any segment; a literal segment beats a parameter, so `/todos/new` reaches `web.todos_new` and not `/todos/:id`; a trailing `*` matches the rest). A form without an `action` posts to the URL of the page's own GET route. Absolute URLs become `external` nodes. Static files (`/static/...`, the `static` blocks' prefixes, asset extensions) are skipped. A URL no route serves becomes an `unresolved` node and a `flows.unresolved` warning pointing at the template and line.

**Warnings** (`code`): `flows.unresolved`, `flows.redirect_unresolved`, `flows.intent_missing`, `flows.process_missing`, `flows.template_missing`, `flows.resource_missing` (warning); `flows.dynamic_target`, `flows.relative_target`, `flows.script_missing`, `flows.dynamic_route`, `flows.entities` (info).

**Limits**
- Routes come from explicit `route` and `route_group` blocks. Routes generated by `entity` blocks are not drawn (a `flows.entities` warning says how many entity blocks there are). Routes with a computed `path` are skipped.
- Elements are found by parsing the template's HTML, so links written by JavaScript (`el.innerHTML = ...`), URLs assembled from several variables, and buttons wired only by an event listener show as UI-only buttons or not at all. Scripts are read only from `<script src="/static/...js">` files that exist in the draft or on the host; inline scripts are scanned too. `fetch`, `XMLHttpRequest.open`, `axios`, `$.get/$.post` and `location` assignments are recognised.
- Template directives are opaque: an element inside `@if` or `@for` is drawn once, whether or not it is rendered for a given user, and a `${...}` URL segment is a `:param`.
- `redirects` is read from the query parameter the app's own convention uses (`?redirect=`); a redirect chosen by the intent's logic at run time is not visible.
- Which intent step reads or writes which table is not derived; `uses` stops at the connection.

**`studio/pages` additions**: `Elements(src) []Element`, `Scripts(src)`, `Heading(src)`, `ScriptElements(js, baseLine)`, `NormalizeURL(raw) URLInfo`, `IsAssetURL(path)`, and `ElementsFor(templates, resources fs.FS, name) (*PageElements, error)`, which resolves a page's layout chain and includes, attributes elements of other files as `Shared`, and reads the scripts it loads.
