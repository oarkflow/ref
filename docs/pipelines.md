# Data verification pipelines

A **pipeline** is a multi-stage workflow in which a *case* moves between people until it is approved, rejected or withdrawn. A case might be an application, a claim, a registration or a licence. Each stage has its own:
- page of form groups,
- people and roles,
- nodes, each with its own status,
- rules, and
- status.

Data entered at one stage flows to the next. Reviewers can verify it field by field or send it back.

A pipeline is declared in BCL with a `pipeline` block and run by a `pipeline.cases` resource. The rules live in the transport-free [`pipeline`](../pipeline) package, so the same engine runs in a request, a test or a replay.

The working example is [`examples/passport`](../examples/passport). It is exercised end to end over HTTP in [`platform/passport_e2e_test.go`](../platform/passport_e2e_test.go).

## The model

```
pipeline "passport"
├── input "full_name" { … }                 shared field catalog
├── form "applicant" { fields [...] }       reusable snippets of inputs
├── form "addresses" { repeatable true … }  lists of entries
├── stage "application" { public true  page { layout wizard  group … } }
├── stage "verification" {
│     roles ["officer"]      form "officer_check" { … }   ← stage-local form
│     page { group "submitted" { mode readonly … } }
│     node "review_identity" { kind review  forms ["applicant"] }
│     node "watchlist" { kind automated  hook "passport.watchlist" }
│     action "return" { outcome return  return_to "application" }
│     assign "decision.verified_by" { value "actor.id" }
│   }
├── stage "approval" { node "sign_off" { kind approval  distinct_from [...] } }
├── stage "issuance" { node "issue" { kind certificate  auto true } }
└── certificate "passport_approval" { fields [...]  validity "4380h" }
```

> **BCL spelling:** `field` and `type` do not bind in BCL, so fields are `input` blocks and discriminators are spelled `kind`.

### Case data

A case's data is a document keyed by form and then by input: `data.applicant.full_name`. A repeatable form holds a list: `data.addresses[0].line`.

Forms are shared across stages. Two stages that show the `applicant` form show the same values, so data entered once is available everywhere. `assign` blocks derive new values when a stage completes.

### Inputs

| Attribute | Meaning |
|---|---|
| `kind` | One of `text`, `textarea`, `number`, `integer`, `boolean`, `date`, `datetime`, `email`, `phone`, `select`, `radio`, `multiselect` or `file`. |
| `required`, `required_if` | Required always, or when an expression holds. |
| `visible_if` | The input is hidden unless the expression holds. A hidden input is never accepted or required. An input may be submitted together with the field that controls its visibility. |
| `pattern`, `min_length`, `max_length`, `min`, `max` | Constraints. `min` and `max` are numbers, or dates for date kinds. |
| `options` / `lookup` | Fixed choices, or a reference-data set. With `org_resource` set, the set is resolved from `org.hierarchy` lookups for the case's org unit, so a district can add, relabel or disable choices. |
| `sensitive` | Masked (`••••1234`) for everyone except the applicant and the pipeline's `reveal_roles`. |
| `default`, `placeholder`, `help`, `span`, `accept` | Presentation hints. |

Every submitted field is validated before anything is written. Errors come back all at once, as `422` with `details: [{path, rule, message}]`.

### Pages, groups, modes and layouts

A stage's `page` lists `group`s. Each group shows one or more forms.

| | Values |
|---|---|
| page `layout` (how the groups are arranged) | `wizard`, `tabbed`, `accordion`, `stacked`, `grid` |
| group `layout` (how the forms inside a group are arranged) | same as above |
| group `mode` | `editable` (the default), `readonly`, `summary`, `hidden` |
| group `roles`, `visible_if`, `collapsed` | Who sees the group, when it is shown, and whether it starts collapsed. |

An input is editable only if all of these hold:
- its group is `editable`,
- the stage is open,
- the caller may act at the stage, and
- while the case is returned for correction, the input was flagged.

The **view** (`pipeline.view`) resolves all of this for the caller. It returns groups with their effective modes, inputs with `editable`, `value` (masked where needed), `choices`, `flag` and `verdict`, plus nodes with their allowed operations and the caller's actions. A UI renders it generically; [`examples/passport/static/index.html`](../examples/passport/static/index.html) is such a renderer.

### Stages

| Attribute | Meaning |
|---|---|
| `public` | The case's applicant (its creator) acts here. Anonymous starts are allowed when the resource sets `allow_anonymous`. |
| `roles` / `view_roles` | Roles that act at the stage, and roles that may only look. |
| `requires`, `entry_conditions`, `skip_if` | Gates the stage. A skipped stage is recorded as `skipped` and the case moves on. |
| `form` blocks | Forms that belong to this stage only (officer notes, capture details). |
| `node` blocks | Units of work, each with its own status (see below). |
| `complete` | `all` (the default), `any` or `quorum` (with `quorum N`) nodes must be satisfied. |
| `auto_advance` | Complete the stage as soon as its nodes are satisfied. |
| `action` blocks | The buttons: `outcome` is `advance`, `return` (`return_to`), `reject`, `approve`, `withdraw` or `hold`. An action can also set `roles`, `comment_required`, `condition`, `confirm`, `skip_nodes` and `next`. A stage without actions gets a default `submit`. |
| `assign` | `path { value "expr" }` sets data when the stage completes. |
| `certificate` | Issues a certificate when the stage completes. |
| `due` | The SLA (e.g. `"72h"`). Queue rows carry `due_at` and `overdue`. |
| `on_enter`, `on_complete` | Intents run as hooks. A failing hook aborts the operation, so nothing is saved. |

Stage statuses are `pending`, `active`, `returned`, `completed`, `rejected` and `skipped`. Case statuses are `draft`, `in_progress`, `returned`, `approved`, `rejected`, `withdrawn` and `completed`.

### Nodes

| `kind` | Operations (verbs) | Satisfied when |
|---|---|---|
| `review` | `verify` (with `verdicts: {"form.input": {status: verified\|flagged, comment}}`), `complete` | Every input of its `forms` is verified. A flag fails the node. |
| `approval` | `approve`, `reject` | `approvals` distinct people approved. `distinct_from` names nodes (or `applicant`) whose actors may not approve, which enforces the four-eyes rule. |
| `check` | `recheck` | Its `check` expression holds. It is re-evaluated on every save. |
| `automated` | `run` | Its `hook` intent returned `true` (or a map without `passed: false`). It runs on stage entry. |
| `task` | `complete`, `fail` | Someone completed it (with an optional `result`). |
| `form` | `complete` | Its forms validate. |
| `certificate` | `issue` | The certificate was issued. With `auto true` this happens on stage entry. |
| any | `waive` (for `waive_roles`) | — |

Node statuses are `pending`, `passed`, `failed`, `waived` and `skipped` (`applies_if` false). `optional` nodes never block their stage.

### The correction loop

1. A reviewer flags inputs, then takes a `return` action with a comment. `flags` on the action can add further inputs.
2. The target stage reopens as `returned`, and the case status becomes `returned`. **Only the flagged inputs are editable.** Anything else submitted is ignored.
3. The applicant resubmits. The case goes straight back to the stage that returned it, not through the stages in between.
4. Verified verdicts are kept. Only the flagged inputs need review again.

### Certificates

A `certificate` block names the data paths copied into the certificate, its number format (`PPA-{year}-{seq:4}`, `{case}`) and its `validity`.

An issued certificate is canonicalised and SHA-256 hashed. With the resource's `signing_secret`, it is also HMAC-signed. It carries a short verification code.

`pipeline.verify` checks the hash, the signature, expiry and revocation for a given number or code. Any edit to the content is detected.

### Concurrency

Every change bumps the case's `revision`, and the store updates with `WHERE revision = ?`. Two officers acting at once cannot overwrite each other: the second gets `409`. A client can also send the `revision` it rendered, to get a clean conflict instead of acting on a case that moved under it.

The engine holds no locks. Automation and stage hooks run within the request, before the store write. If the write then conflicts, a hook with external side effects may have run for an operation that was not saved, so make such hooks idempotent. Alternatively, have the hook enqueue work rather than do it.

## Running a pipeline

```bcl
resource "cases" {
  kind "pipeline.cases"
  config {
    pipelines ["passport"]          # default: every pipeline block
    database "db"                   # omit for in-memory
    org_resource "org"              # lookups + jurisdiction-scoped queues
    allow_anonymous true            # public stages without an account
    signing_secret env.required("PASSPORT_SIGNING_SECRET")
  }
}
```

The resource compiles each pipeline and every expression in it at load time, so a typo stops the deployment. With a database, it creates `<prefix>cases`, `<prefix>certificates` and `<prefix>sequences`.

| Action | Parameters | Result |
|---|---|---|
| `pipeline.describe` | — | The definition |
| `pipeline.start` | `input.org_unit`, `input.data` | The view, plus `access_key` for anonymous callers |
| `pipeline.view` | `id`, `stage?` | The view |
| `pipeline.save` | `id`, `stage`, `input.data` | The view |
| `pipeline.act` | `id`, `stage`, `action`, body `{comment, data, flags, revision}` | The view |
| `pipeline.node` | `id`, `stage`, `node`, `verb`, body `{comment, verdicts, result}` | The view |
| `pipeline.list` | `scope` = `queue` \| `mine` \| `all`, `status`, `stage`, `limit`, `offset` | Case rows |
| `pipeline.get` | `id` | Header, stage states, history and certificates |
| `pipeline.verify` | `key` (a number or code) | `{valid, reason, certificate}` |

Each action looks for a parameter such as `id` in this order, so routes need no plumbing nodes:
1. `config.id_fact` (a fact path)
2. a literal `config.id`
3. the `:id` path parameter
4. the `?id=` query parameter
5. an `id` fact, or `input.id`

Nodes that take a body declare `requires [input]`.

Failures map to HTTP statuses as follows:

| Failure | Status |
|---|---|
| Not permitted | `403` |
| Wrong state | `409` |
| Unknown case, or out of jurisdiction | `404` |
| Stale revision | `409` |
| Invalid fields | `422`, with `details` |

**Access.** When an org resource is configured, officers see only cases in their jurisdiction. A case outside it reads as not found, so its existence is not disclosed. An anonymous applicant returns to their case with the access key in the `X-Access-Key` header. Only a SHA-256 hash of the key is stored.

**Hooks.** An automation or stage hook intent receives `{case, data, stage, node}` as its input.

## Using the engine directly

```go
compiled, err := pipeline.Compile(&def)
e := pipeline.NewEngine(compiled)
e.Eval = myEvaluator          // expressions
e.Automation = myAutomation   // automated nodes
e.SigningKey = key            // certificates
c, _ := e.Start(ctx, applicant, pipeline.StartOptions{Number: "PP-1"})
c, err = e.Act(ctx, c, applicant, "application", "submit", pipeline.ActInput{Data: data})
v, _ := e.View(c, officer, "")
```

Every operation takes a case and returns a new one. The input case is never mutated, so a failed operation leaves nothing half-applied. Persist the result with a `pipeline.Store`: `MemoryStore`, or `SQLStore` for PostgreSQL, MySQL or SQLite.
