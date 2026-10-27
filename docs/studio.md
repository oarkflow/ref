# REF Studio

Studio is the visual editor for a REF application. It edits the BCL configuration
and the page templates of an app without anyone writing BCL by hand, previews the
result in a sandboxed copy, and ships changes through a review-and-activate flow
with no restart.

## Run it on any app

```
ref -studio :8081 ./path/to/app      # app on :8080, Studio on :8081
```

Studio needs a `config/`, `bcl/` or `resources/config/` directory. The access
token is `$STUDIO_TOKEN` (16+ characters) or one generated into
`<app>/.data/studio/token` (mode 0600; keep `.data/` out of version control).
*Making a version live* writes its BCL files and templates back into the app
directory (files the version no longer has are removed) and restarts the app,
so the files on disk are always what runs. There is no approval step in this
mode, and live preview is not available (those endpoints answer 501); use
the starter's supervised setup when you need reviews or preview.

### Starter example

```
cd examples/starter
./scripts/run-studio.sh
# App     http://127.0.0.1:8080
# Studio  http://127.0.0.1:8081/studio/
```

The script generates development secrets on first run (in the git-ignored
`.data/studio/`), applies migrations, builds and starts the starter with Studio
mounted. It prints three development tokens; the token you sign in with decides
your role:

| Role | Can |
|---|---|
| viewer | look |
| editor | edit working copies, preview, submit for review |
| reviewer | approve, reject and activate versions |
| admin | everything, including rollback and the audit log |

The author of a version can never approve it. Use your own tokens outside
development (`STARTER_ADMIN_TOKEN`, `STARTER_REVIEWER_TOKEN`,
`STARTER_EDITOR_TOKEN`, each at least 16 characters). Every variable is listed in
`examples/starter/CONFIGURATION.md` under "Studio".

## What you can do

- **Pages & APIs, Logic flows, Connections, Data, Automation, Access & security,
  Settings** — schema-driven forms for every BCL block, in plain language, with
  Basics / Security / Performance / Advanced sections. Every value can be a fixed
  value, taken from the environment, or a formula. A *Developer view* toggle shows
  the raw names, paths and source.
- **Form designer** — open any pipeline and choose *Open form designer*: a field
  palette (click or drag to add), a canvas with one tab per form, drag-to-reorder,
  a properties panel (label, field name, type, required, width, help, placeholder,
  options, pattern, length limits, accepted file types) and a *Preview* of the
  form as the end user sees it. Every change is a minimal BCL edit in the working
  copy, so it is undoable, reviewed and versioned like any other change.
  *Page layout* does the same for a stage's page: pick the layout (stacked, tabs,
  wizard, accordion, grid), add, order and delete groups, set each group's title,
  mode (editable, read-only, summary, hidden), columns and collapsed state, drag
  forms between groups, and watch a live preview of the page.
  *SLA* edits each stage's deadline, warning, working calendar, breach action,
  pickup timeout and escalation levels beside a timeline. *Workspaces & lookups*
  shows the workspace tree, the lookup sets on it and which fields use them, and
  manages lookup sources: fixed lists, database queries or HTTP APIs. Field
  properties include lookup, `depends_on`, rules (`visible_if`, `required_if`,
  `disabled_if`), role limits and PII flags, and a form can include shared
  *Form settings* (select no field) cover repeatable forms (at least / at most
  entries; Preview shows several), columns, role limits, and conditions. Conditions
  (show / required / read-only when) are edited with a rule builder: pick a field,
  an operator and a value, join rows with and / or. A condition the builder cannot
  show (parentheses, mixed and/or, functions) stays as text and is never rewritten.
  The builder writes `field empty` / `field not empty`, and flags `field != ''`
  because a value nobody has entered yet is not equal to a blank string.
  Groups on the *Page layout* tab take the same conditions and role limits.
  Conditions can also read the **workspace** (`case.org_unit`, with a picker of your
  workspaces and *is one of*), the **user** (`actor.roles has 'qa'`, whether they are
  the applicant), the **case status** and any **stage's status**
  (`stages.coding.status == 'completed'`).
  *Stages & flow* edits, per stage: when it is skipped, the entry rules that must hold
  before it opens (a failing rule stops the transition with the rule's text), which
  stages must be completed first, where it goes next, when it counts as done, and its
  actions (button label, what it does, when it is offered, where it sends the case,
  roles, confirmation). The strip at the top shows each stage's conditions and where it
  can lead. Workspace conditions match the workspace exactly; there is no "inside this
  facility" test.
  *Snippets* manages reusable pieces. **Shared fields** (the pipeline's catalog): create,
  rename (every form that lists it is updated), delete (it is removed from those
  forms), and on the Design canvas *Detach into this form* or *Make shared*. **Forms**:
  each form with the stages and groups that show it, *Duplicate* (keeps shared fields
  shared and points the copy's own rules at the copy), *Delete* (removes it from the
  pages that show it, after asking), and *Copy a form from another pipeline* (shared
  fields become ordinary fields of the copy). Making a field shared moves it to the end
  of the form's fields.
  catalog fields (tick *Add as shared field* in the palette to create one; edit it
  once and every form that lists it changes).
  A lookup set can be a fixed list, a database query (choose the app's database
  connection) or an HTTP API; connecting a new API creates a `service.http`
  resource that may reach only the host you enter. Credentials are never typed
  into Studio: put them in the resource's environment settings. After a failed
  test, *Add set* becomes *Add anyway* with a warning, and a stale result is
  discarded when you edit the set.
  *Test this set* (on every set, and before adding one) runs it against the
  running app's own connections and shows the choices, how long it took, or the
  real error, such as a column that does not exist. It needs `ref -studio`
  (the app and Studio in one process), uses the same admin token, reads only,
  and shows at most 50 choices. A connection created in the working copy cannot
  be tested until it is live.
- **Logic flows and approval workflows** — a graph canvas. Every kind of step has
  its own card and its own settings form. Connections carry plain-language
  labels ("If approved", "After 48 hours", "On error") that you can click to edit.
  Select a step to see data flow along its connections.
- **Page designs** — every page, layout and reusable piece: customize a built-in
  template (your version carries only what you change), edit it with highlighting
  and problem markers, insert components, links, buttons and forms from a menu,
  and revert to the original.
- **User journeys** — a map of how the app behaves: pages, the buttons, forms
  and links on them, the requests they send, the logic flow each request runs,
  and where the user ends up. Broken paths (a button calling a URL no route
  serves) are flagged.
- **Live preview** — a sandboxed copy of the app built from your working copy:
  empty database, recorded outbound calls, background jobs off. It rebuilds when
  you edit.
- **Versions & reviews** — submit for review, compare files and blocks, discuss,
  approve, make live, go back to an earlier version. Every step is audited.
- **Ctrl/⌘+K** jumps to anything.

## How it fits together

```
studio/web        React + TypeScript app, built into studio/web/dist and embedded
studio/server     HTTP API: working copies, atomic edits, undo/redo, validation,
                  diffs, assets, journeys, revisions, roles, audit, preview wiring
studio/model      lossless BCL editing: splice at byte ranges, bcl.Format, reparse
studio/pages      template analysis: variables, includes, interactive elements
studio/preview    sandboxed preview generations and request recorder
deploy            versions: checksum/signing, bundles with assets, approval, swap
platform          diagnostics with paths and spans, block schemas, asset rules
```

Design rules worth knowing:

- BCL files stay canonical and readable. Edits splice text at byte ranges and
  reformat with `bcl.Format`; the AST is never printed, so comments and order
  survive. See `studio/model/README.md`.
- A version is a flat set of `*.bcl` files plus optional template/static assets,
  signed as a bundle (payload v3 when it has assets; older versions still
  verify). See `docs/deploy.md`.
- Durations are Go durations (`30s`, `15m`, `48h`). Days and weeks are not valid.
- Preview is a guard against accidents, not a security boundary: give the editor
  token only to people you would give the repository.

The HTTP contract is `docs/studio-api.md`. The node-type registry (how each kind
of step gets its form) is documented in `studio/web/src/canvas/registry/README.md`.

## Develop it

```
cd studio/web
npm install
npm run dev:mock     # Go mock API + Vite; sign in with editor-token-000001
npm test             # unit tests
npm run build        # writes studio/web/dist (commit it: go build embeds it)
```

Go side: `go test ./studio/... ./deploy ./platform`. The end-to-end flow against a
real boot of the starter: `STARTER_STUDIO_E2E=1 go test ./examples/starter/cmd/server
-run TestStudioEndToEnd` (use `GOTOOLCHAIN=go1.26.5` in `examples/starter`).

## Known limits

- Versions live in memory; a restart boots from the files in `resources/config/`.
  Drafts and the audit log persist with `STARTER_STUDIO_PERSIST=1`.
- Only SQLite has been exercised; the Postgres and MySQL paths are written but
  untested.
- Journeys is a static map: routes generated by `entity` blocks, redirects chosen
  by logic, and elements written by JavaScript are not drawn.
- Template variable checks are best effort and warn only.
