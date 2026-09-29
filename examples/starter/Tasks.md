# TODO example: workflow, review & approval — implementation plan

Status: **implemented and verified** (see the checklist at the bottom —
every item checked, plus two things found only by actually building this:
a real platform RBAC-status bug, fixed in core, and one open question
below resolved in a way worth reading if you're adding a third role/stage
later). Kept as-is, not rewritten past tense, since it's still the right
design reference for the code it describes. This file is the task
breakdown for a new, self-contained teaching example (same convention
as `resources/config/09_workflow_example.bcl`): a TODO item that moves
through **draft → review → approval → done** (with a revision loop and two
terminal failure paths), demonstrating the `process`/`step`/`task`/`edge`
durable-workflow DSL, role-gated human tasks, multi-group forms, and
stage-specific SSR template snippets (field / row / form) — none of which
this starter currently has a worked example of. `09_workflow_example.bcl`
teaches synchronous branching; this teaches the asynchronous, human-in-the-
loop side of the same platform.

Delete-ability: like the orders example, this is meant to be read once and
deleted. Unlike it, this example **cannot be removed by deleting one file**
— it adds two roles to `00_app.bcl` (the canonical role registry) because
demonstrating role-gated multi-stage review genuinely needs more than
`user`/`admin`. The final task in every phase below is the exact list of
what to delete to remove the whole example cleanly.

## Design

### State machine

```text
 POST /todos                    POST /todos/:id/submit
      │                                  │
      ▼                                  ▼
 ┌─────────┐   submit    ┌────────────────────────┐
 │  draft  │ ───────────▶│  review (role:reviewer) │
 └─────────┘             └────────────────────────┘
      ▲                     │                   │
      │ resubmit            │ approve           │ request_changes
      │                     ▼                   ▼
 ┌───────────────────┐  ┌─────────────────────────┐
 │ revise (owner only)│  │ approval (role:approver) │
 └───────────────────┘  └─────────────────────────┘
      ▲   cancel              │              │
      │                       │ approve      │ reject
      ▼                       ▼              ▼
 ┌───────────┐          ┌─────────┐    ┌──────────┐
 │ cancelled │          │  done   │    │ rejected │
 └───────────┘          └─────────┘    └──────────┘
   (terminal)            (terminal)     (terminal)
```

- **draft** is *not* part of the durable process — a plain row insert
  (`todo.create`). A draft is cheap, editable, and abandonable with no
  process/task/queue overhead; nothing durable starts until the owner
  explicitly submits it. This mirrors a real product decision (don't pay
  for a workflow engine on data nobody has committed to yet) and is worth
  the file's own comment explaining it.
- **review**, **approval**, **revise** are `family approval` task steps —
  three separate human-in-the-loop stages, two distinct roles, one step
  (`revise`) assigned back to the specific owner (`assignee` render field,
  not `role`) rather than a role — demonstrating both assignment
  mechanisms `process.TaskDefinition` supports (confirmed in
  `process/definition.go`: `Role string` vs `Assignee Renderer`).
- **done / rejected / cancelled** are `terminal true` steps, each running
  its own tiny intent (`todo.complete` / `todo.reject` / `todo.cancel`) that
  updates the denormalized `status` column so list/row queries never need
  to join the process store.
- The `review ⇄ revise` loop is bounded by the process's own `max_visits`
  (matching `order.fulfil`'s `max_visits 10` in `examples/ref-complete`) —
  a runaway back-and-forth fails the run instead of looping forever.

### Roles

Add to `resources/config/00_app.bcl` (existing `user`/`admin` stay
unchanged):

| Role | Can |
|---|---|
| `user` (existing) | Create a draft, submit it, resubmit after changes requested, cancel their own item |
| `reviewer` (new) | Decide the `review` task: approve → approval stage, or request changes → back to the owner |
| `approver` (new) | Decide the `approval` task: approve → done, or reject → rejected |
| `admin` (existing) | Sees and can act on everything — every gate below lists `admin` explicitly alongside the role it's actually for, matching this starter's established convention (see `CONFIGURATION.md`'s "RBAC: what `superuser_roles` actually does" — `admin` never gets a free pass, every route/task that should include it lists it) |

### Data model

New table `todos` (migration `resources/migrations/5_create_todos_table.bcl`,
same `oarkflow/migrate` BCL dialect as the existing four):

| Column | Type | Notes |
|---|---|---|
| `id` | integer, pk, auto_increment | |
| `owner_id` | string, indexed | the creator, FK-by-convention to `users.id` |
| `title` | string(200) | group 1 field — see shapes below |
| `description` | text, nullable | group 1 field |
| `assignee_email` | string, nullable | group 2 field |
| `priority` | string(10), default `'medium'` | group 2 field — `low`/`medium`/`high` |
| `due_date` | date, nullable | group 2 field |
| `status` | string(20), default `'draft'` | denormalized from the process run — `draft`, `in_review`, `pending_approval`, `done`, `rejected`, `cancelled` |
| `run_id` | string, nullable | set once `todo.submit` starts the durable process; null while still a draft |
| `created_at` / `updated_at` | datetime | |

### Shapes — multiple form groups, two independent shapes over one flat body

**Revised from the original plan.** The plan below originally called for one
composed shape (`todo_input`, with `details`/`assignment` as nested `kind
object` props) checked by a single `validate.schema` node. That doesn't work
with a real HTML form: a native `<form method="POST">` always posts a FLAT
`application/x-www-form-urlencoded` body (`platform/routes.go`'s
`url.ParseQuery`-based parsing has no nested-key support at all), and every
`data.transform` mechanism available for reshaping flat → nested turned out
to have a real gap (dotted `extract` keys and nested `set` blocks — see the
long comment in `12_todo_workflow_example.bcl`'s `todo.create` intent for the
full root-cause chain through `platform/dataspec.go` and
`platform/config.go`). `validate.schema` empirically tolerates extra/
undeclared fields on the object it checks, so the actual design runs TWO
independent `validate.schema` nodes against the SAME flat body instead of
composing one nested shape:

```bcl
shape "todo_details" {          # form group 1: "What"
  kind object
  prop "title" { kind string required true min_length 1 max_length 200 }
  prop "description" { kind string max_length 4000 }
}

shape "todo_assignment" {       # form group 2: "Who / when / how urgent"
  kind object
  prop "assignee_email" { kind string format email }
  prop "priority" { kind string enum [low, medium, high] default "medium" }
  prop "due_date" { kind string format date }
}

shape "review_decision" {       # task form_schema for the "review" step
  kind object
  prop "action" { kind string required true enum [approve, request_changes] }
  prop "notes" { kind string max_length 2000 }
}

shape "approval_decision" {     # task form_schema for the "approval" step
  kind object
  prop "action" { kind string required true enum [approve, reject] }
  prop "notes" { kind string max_length 2000 }
}

shape "revise_decision" {       # task form_schema for the "revise" step
  kind object
  prop "action" { kind string required true enum [resubmit, cancel] }
}
```

`todo.create`'s `validate-details`/`validate-assignment` nodes, each checking
the same flat body against its own shape, are the mechanism: this is what
"multiple form groups" *is* at the data layer — one request body, two
independently-reusable, independently-validated groups, no nesting required.
The create/edit HTML form mirrors this exactly with two `<fieldset>` blocks
(see the template plan below) — the grouping is visual/organizational only,
not a data-shape requirement.

One more real-world gap this surfaced: an unfilled optional `<input>`
(`assignee_email`, `due_date`) posts as `""`, not an absent key — and
`format email`/`format date` reject `""` even on a non-`required` prop
(`DataSpec.Defaults` doesn't help either: it only fills absent/null keys,
and its own doc comment says `""` and `0` are values, not absences). A
`node "clean"` (`data.transform`, flat ternary `extract` keys —
`input.assignee_email != '' ? input.assignee_email : null`) runs before both
validation nodes to turn those empty strings into `null` first.

### Resources (add to `resources/config/01_resources.bcl`)

```bcl
resource "todo_runs" {
  kind "store.sql"
  description "Durable process rows, steps, timers and tasks for the todo workflow example"
  config { database "database" table_prefix "todo_process" }
}
```

Reuses the *existing* `jobs` queue resource (`queue.sql`, already declared
for `notify.welcome`) — no new queue needed; the process's `queue "jobs"`
field just names it, the same way `order.fulfil` does in
`examples/ref-complete`.

## File-by-file plan

| File | Action | Contents |
|---|---|---|
| `resources/migrations/5_create_todos_table.bcl` | new | the `todos` table |
| `resources/config/00_app.bcl` | edit | add `role "reviewer"`, `role "approver"` |
| `resources/config/01_resources.bcl` | edit | add `resource "todo_runs"` |
| `resources/config/12_todo_workflow_example.bcl` | new | shapes, the `process "todo.workflow"` block, every intent (`todo.create`, `todo.submit`, `todo.persist`-equivalent already covered by `todo.create`, `todo.complete`, `todo.reject`, `todo.cancel`, `todo.list`, `todo.get`), `route_group "todos"` (JSON) + SSR page routes |
| `resources/templates/pages/todos/list.html` | new | list page — uses the **row** snippet per item |
| `resources/templates/pages/todos/new.html` | new | create form — uses the **form** snippet, two fieldsets |
| `resources/templates/pages/todos/show.html` | new | detail page — uses the **field** snippets, stage-specific action buttons |
| `resources/templates/components/todos/row.html` | new | the **row** snippet (see below) |
| `resources/templates/components/todos/field.html` | new | the **field** snippet (see below) |
| `resources/templates/components/todos/status-badge.html` | new | small shared partial: status string → `<span class="badge badge-...">`, reused by both row and show |
| `starter_test.go` | edit | `TestTodoWorkflowExample` — the full happy path plus one `request_changes` loop, mirroring `TestOrdersWorkflowExample`'s existing shape |
| `README.md` | edit | one bullet + a short "Durable workflow, review and approval" section, mirroring the existing "Conditional flows..." section for the orders example |
| `CONFIGURATION.md` | edit | a short section on the `process`/`task` DSL as actually used here (the platform docs cover the DSL in the abstract; this starter had no worked example to point at before) |

## Template snippet plan (field / row / form)

This is the part that doesn't exist anywhere else in the starter yet — the
other examples (orders, dashboard) don't need per-stage rendering because
nothing about them changes shape based on workflow state. A TODO does.

### `components/todos/status-badge.html`

```html
<span class="badge badge-status-${todo.status}">${todo.status}</span>
```

New badge color variants for `12_todo_workflow_example.bcl`'s statuses,
added to `resources/static/css/app.css` alongside the existing
`badge-admin`/`badge-user`/etc. — flat solid colors, no gradients (per this
starter's own design system), reusing `--accent-soft`/`--success-soft`/
`--error-soft`/`--bg-inset` tokens already defined rather than inventing new
ones: `draft`→dim/neutral, `in_review`→accent, `pending_approval`→accent
(stronger), `done`→success, `rejected`/`cancelled`→error.

### `components/todos/row.html` — one list row, stage-aware actions

```html
<tr>
  <td>${todo.title}</td>
  <td>${todo.owner_email}</td>
  <td>@include("components/todos/status-badge.html")</td>
  <td class="font-mono">${todo.due_date}</td>
  <td>
    @if(todo.status == "draft" && todo.owner_id == user.id) {
      <a href="/todos/${todo.id}" class="btn btn-sm btn-secondary">Submit…</a>
    }
    @if(todo.status == "in_review" && (user.roles == "reviewer" || user.roles == "admin")) {
      <a href="/todos/${todo.id}" class="btn btn-sm btn-primary">Review</a>
    }
    @if(todo.status == "pending_approval" && (user.roles == "approver" || user.roles == "admin")) {
      <a href="/todos/${todo.id}" class="btn btn-sm btn-primary">Approve/Reject</a>
    }
  </td>
</tr>
```

The row *reveals* the right next action per viewer per stage instead of
showing every action to everyone and relying on the destination page (or
worse, the button click itself) to fail closed — the real gate is still
each task's own `role`/`authz` server-side, exactly like
`CONFIGURATION.md`'s dashboard note already explains for the admin-portal
link ("its own `authz { roles [...] }` is the real gate: ... not a hidden
link here"). This row is that same pattern, now stage-conditional as well
as role-conditional.

### `components/todos/field.html` — one field, read-only vs editable by stage

A single reusable partial parameterized by the field's label/value/whether
this viewer may currently change it — the "field" snippet the request
asked for specifically:

```html
@if(editable) {
  <div class="form-field">
    <label class="form-label" for="${name}">${label}</label>
    <input type="${type}" id="${name}" name="${name}" class="form-control" value="${value}">
  </div>
} @else {
  <div class="kv-row">
    <span class="kv-key">${label}</span>
    <span class="kv-value">${value}</span>
  </div>
}
```

`show.html` includes this once per field, passing `editable` as
`todo.status == "draft" && todo.owner_id == user.id` — a draft is
editable by its owner; everything from `in_review` onward is read-only
(the *decision* is editable via the task's own form, the *item* itself is
not) — the field snippet is what makes that one rule apply uniformly
across every field instead of six copy-pasted `@if`s.

### `pages/todos/new.html` — the form, two groups

```html
@extends("layouts/base.html")
@define("content") {
  <form method="POST" action="/todos">
    <fieldset class="panel">
      <legend class="panel-label">Details</legend>
      <div class="form-field"> ... title ... </div>
      <div class="form-field"> ... description ... </div>
    </fieldset>
    <fieldset class="panel">
      <legend class="panel-label">Assignment</legend>
      <div class="form-field"> ... assignee_email ... </div>
      <div class="form-field"> ... priority (select) ... </div>
      <div class="form-field"> ... due_date ... </div>
    </fieldset>
    <button type="submit" class="btn btn-primary">Save draft</button>
  </form>
}
```

Two `<fieldset>`s, matching `todo_details`/`todo_assignment` one-to-one —
the point being that the shape grouping and the form grouping are the same
grouping, not independently maintained.

### `pages/todos/show.html` — detail page, task decision form inline

Renders every field via the **field** snippet, the status badge, and —
only when `user` currently has an open task on this run (server-computed,
not guessed client-side) — the relevant decision form
(`review_decision`/`approval_decision`/`revise_decision`'s fields, POSTing
to the existing generic `/tasks/:id/decide` action pattern from
`examples/ref-complete`, adopted here rather than reinvented).

## Routes & RBAC

```bcl
route_group "todos" {
  prefix "/api/v1/todos"
  session "sessions"
  auth "session_auth"
  authz { roles ["user", "reviewer", "approver", "admin"] authorizer "authorization" }
  cache_control "no-store"

  route "todos.create" { method POST path "" intent "todo.create" status 201 }
  route "todos.list"   { method GET  path "" intent "todo.list" }
  route "todos.get"    { method GET  path "/:id" intent "todo.get" }
  route "todos.submit" { method POST path "/:id/submit" intent "todo.submit" }
}
```

Plus SSR page routes (`web.todos_list`, `web.todos_new`, `web.todos_show`)
under the existing session-cookie web pages, same shape as
`web.dashboard`/`web.admin` in `04_routes.bcl`. Task decisions reuse
`task.list`/`task.decide`-shaped intents (new copies here, since this
starter doesn't already have them — `examples/ref-complete`'s
`intent "task.decide"` is the reference to copy from, not import).

**RBAC gates, explicitly**, so the eventual `authz`/task `role` lines in
the real BCL are traceable back to this table:

| Action | Who |
|---|---|
| Create / edit / submit a draft | the owner (`user`) |
| Decide `review` | `reviewer`, `admin` |
| Decide `approval` | `approver`, `admin` |
| Decide `revise` | the specific owner only (`assignee`, not a role) |
| List all todos | any signed-in role (`user`, `reviewer`, `approver`, `admin`) — everyone can *see* the queue, only the right role can *act* |

## Implementation task checklist

Ordered so each step is runnable/testable before the next depends on it.

- [x] 1. Migration: `resources/migrations/5_create_todos_table.bcl`. Verify
      with `go run ./cmd/migrator cli migrate` against a scratch SQLite db.
- [x] 2. Roles: add `reviewer`/`approver` to `00_app.bcl`.
- [x] 3. Resource: add `todo_runs` (`store.sql`) to `01_resources.bcl`.
- [x] 4. Shapes: all five, in the new `12_todo_workflow_example.bcl`.
- [x] 5. `todo.create` intent (plain insert, no process) + its JSON route.
      Test: `curl -X POST /api/v1/todos` creates a `status=draft` row.
- [x] 6. `process "todo.workflow"` block: steps `review`/`approval`/`revise`
      + terminal steps `done`/`rejected`/`cancelled`, their tiny intents,
      and the branch edges between them. Confirm the `assignee` field name
      and `Renderer` templating syntax against `process/definition.go` /
      an existing `title "... {{ run.input.id }}"` example while writing
      this — flagged here because it's the one piece of syntax this plan
      infers from the Go struct rather than copying verbatim from a working
      BCL example.
- [x] 7. `todo.submit` intent: starts the process (`process.start`,
      `input_fact`, `idempotency_fact`), updates `status` to `in_review`.
      Test: submit a draft, confirm a `review`-role task appears for a
      reviewer account (`GET /tasks`).
- [x] 8. `todo.list`/`todo.get` intents + JSON routes.
- [x] 9. Task-decision intents (`todo.task_list`/`todo.task_decide`, or
      reuse a shared name if one already fits) + routes, adapted from
      `examples/ref-complete`'s `task.list`/`task.decide`.
- [x] 10. SSR routes + `pages/todos/{list,new,show}.html` +
      `components/todos/{row,field,status-badge}.html`.
- [x] 11. CSS: status badge color variants in `resources/static/css/app.css`
      (flat colors only, reusing existing tokens — see the template plan
      above).
- [x] 12. `starter_test.go`: `TestTodoWorkflowExample` — create → submit →
      reviewer requests changes → owner resubmits → reviewer approves →
      approver approves → `status == "done"`; a second short test for the
      reject path.
- [x] 13. End-to-end smoke test: create, submit, decide as each role (owner/
      reviewer/approver), the `request_changes` → revise → resubmit loop,
      and the RBAC probe — all against a real running server via `curl`,
      not just `starter_test.go`'s in-process harness. The browser
      extension was unavailable both times it was attempted in this
      session, so `/todos`/`/todos/new`/`/todos/:id` were verified by
      inspecting the rendered HTML `curl` returned (row markup, form
      fieldsets, the inline decision form) rather than by actually
      clicking through in Chrome — flagged here rather than silently
      claimed as a full browser test it wasn't.
- [x] 14. Docs: README.md bullet + section, CONFIGURATION.md section.
- [x] 15. Final full-repo `go build`/`go vet`/`gofmt -l`/`go test ./...`
      across both `examples/starter` and core `ref` — **not** unchanged:
      building this surfaced one genuine core bug (see "What this
      surfaced" below), fixed in `platform/actions_process.go`, covered by
      a regression assertion in `TestTodoWorkflowExample` and by core's
      own `go test ./...` staying green.

## What to delete to remove this example

`resources/migrations/5_create_todos_table.bcl`,
`resources/config/12_todo_workflow_example.bcl`,
`resources/templates/pages/todos/`,
`resources/templates/components/todos/`,
the `todos` table it created, the `TestTodoWorkflowExample` test, the
`role "reviewer"`/`role "approver"` blocks in `00_app.bcl`, the
`todo_runs` resource in `01_resources.bcl`, and the badge CSS variants in
`app.css` — listed here so removing the teaching example is a checklist,
not an archaeology exercise, the same reasoning `09_workflow_example.bcl`
already gives for itself.

## Open questions — resolved

- **Assignee's exact BCL field name**: `assignee`, confirmed against
  `process/definition.go`'s `TaskDefinition.Assignee Renderer` field and
  used as `assignee "{{ run.input.owner_id }}"` — same `{{ }}` templating
  `title`/`instructions` already use elsewhere in the DSL.
- **`task.list`'s `scope "mine"`**: already covers "everything my role
  could claim" as well as "my own claims" (confirmed against
  `taskListAction`'s implementation — `scope "mine"` sets both
  `filter.Assignee` and `filter.Roles` from the caller's principal). No
  separate role-scoped view was needed; the review queue and the approval
  queue are the same `todo.task_list` intent, naturally filtered per
  caller.
- **`authorization` resource**: needed nothing beyond listing all four
  roles explicitly in each route/route_group's `authz { roles [...] }` —
  confirmed no `superuser_roles`-related surprise repeated itself.

## What this surfaced (beyond the plan)

Two things only building this — not just writing this plan — found:

- **`todo.get` needed a node chain the original plan didn't spell out**:
  computing "does the caller have an open task on *this* todo's run"
  turned out to need three nodes (`task.list { scope "mine" }` →
  `data.filter` on `item.run_id == todo.run_id` → `data.first { optional
  true }`), because `task.list`'s BCL config has no `run_id` scoping of
  its own (`TaskFilter.RunID` exists in Go, the BCL action just never
  wires it). Documented in `12_todo_workflow_example.bcl`'s `todo.get`
  intent and in `CONFIGURATION.md`'s new section.
- **A genuine platform bug**: `platform/actions_process.go`'s
  `taskFailure` didn't recognize `ClaimTaskAs`'s role-mismatch error text
  (`role %q is required to claim task %s`), so a correct RBAC denial
  surfaced as 500 instead of 403. Fixed in core (`strings.Contains(text,
  "is required to claim task")` added alongside the existing cases);
  `TestTodoWorkflowExample` asserts the 403 directly.
- **One small correctness fix mid-integration**: `row.html`'s
  `todo.owner_email` doesn't exist as a fact (`todo.list`'s query never
  joins `users`) — changed to `todo.owner_id`, which in this starter is
  already the account's email (`auth.register`'s own `INSERT ... VALUES
  (LOWER($1), LOWER($1), ...)` — id and email are the same value), so the
  column reads correctly with no query change needed.
