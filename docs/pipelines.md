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

## Work management

These features take DAGFlow's human-work layer and close the gaps it leaves open. Capacity is counted from the store (not a per-process ledger), approvers are enforced, escalation reassigns rather than only notifying, and sealed values carry a time lock.

### Workers, routing and claims

```bcl
calendar "office" { timezone "Asia/Kathmandu"  hours "sun-thu 10:00-17:00; fri 10:00-15:00"  holidays ["01-11", "2026-10-20"] }

worker "officer-1" {
  roles ["officer"]  skills ["health"]  org_units ["ktm"]  capacity 25  calendar "office"
  away "leave" { from "2026-04-01"  until "2026-04-08"  reason "annual leave" }
}

stage "verification" {
  roles ["officer", "senior_officer"]
  assign_roles ["supervisor"]          # may assign / reassign
  routing {
    strategy least_loaded              # manual | round_robin | least_loaded | skill_based
    roles ["officer"]  required_skills ["health"]  skills_from "request.skills"
    preferred_skills ["pediatrics"]  capacity 20  sticky true  same_org_unit true  respect_hours true
  }
}
```

Routing runs when the stage opens. A candidate is eligible only if all of the following hold:
- is active and holds a routing role;
- is not away;
- is within working hours (if `respect_hours`);
- has the required skills;
- covers the case's org unit (if `same_org_unit`);
- is under capacity.

The winner is the least loaded worker, or the least recently assigned (round robin), or the one with the best preferred-skill score (skill based). With `sticky`, the person who worked the stage before wins if they are still eligible. `stage.routing` on the view records every candidate and why they were or were not chosen. When nobody qualifies, the case is **queued**. With `manual`, it always is.

**Claims.** A stage with `routing`, or with `claimable true`, is worked by one person at a time. Only the assignee, or someone holding an `assign_roles` role, may save, act or operate nodes. Approval and vote nodes are exempt, since they are other people's decisions by design. The first eligible person to act on queued work claims it.

Work operations (`pipeline.work`, `POST …/stages/:stage/work/:op`):

| op | Who | Effect |
|---|---|---|
| `claim` | anyone who may act at the stage | Take queued work. |
| `release` | the holder, or an assigner | Give the work back. Routing re-runs and excludes the person who released it. |
| `assign` `{to}` | assigners | Give the work to an eligible worker. Capacity may be overruled; eligibility may not. |
| `delegate` `{to}` | the holder | Hand the work to an eligible colleague. |
| `suspend` `{reason, until?}` | `suspend_roles` | Put the stage on hold. Nothing can be done and the SLA clock stops. `until` resumes it automatically. |
| `resume` | `suspend_roles` | Lift the hold. The deadline moves by the working time spent on hold. |

Queues:
- `pipeline.list` with `scope=assigned` returns the caller's work.
- `scope=queue&assignee=unassigned|me|<id>` filters a stage queue.
- Queue rows carry `assignee`, `sla_status` and `on_hold`.

### SLAs, business calendars and escalation

```bcl
sla {
  duration "2d"                 # 2 working days; "16h" = 16 working hours
  warn_before "4h"
  calendar "office"
  on_breach "reassign"          # notify | reassign | return (return_to) | <action name>
  escalate "senior" { after "1d"  assign_roles ["senior_officer"]  notify ["director"] }
  escalate "director" { after "3d"  assign_roles ["director"] }
}
```

**Calendars.** A calendar has weekly windows, holidays and a time zone. Holidays are either fixed dates or `MM-DD` dates that repeat every year. With a calendar, SLA time is counted in working time, and `d` means working days.

**`pipeline.sweep`.** Run it on a schedule. It applies every time-based rule:
- the warning, with the `sla.warning` event;
- the breach, with the `sla.breached` event and the breach action;
- each escalation level after the breach, which re-routes to the level's roles, with the `sla.escalated` event;
- the end of holds whose `until` has passed;
- retention.

A case changed concurrently is skipped until the next run.

### Notes

Notes are threaded (`parent_id`) and are either **internal** or **public**:
- Internal notes are written and read only by staff, meaning holders of any role of the pipeline, or of `notes.internal_roles` when that is set.
- Public notes are seen by everyone who can see the case. The applicant may write them when `notes { applicant_may_write true }` is set.

The view returns only the notes the viewer may read. The endpoint is `pipeline.note` (`POST …/notes {body, internal, parent_id}`).

### Consensus votes

```bcl
node "panel" { kind vote  voters 3  consensus "majority" }   # unanimous (default) | majority | "2"
```

Each voter sends `vote` with `result.decision` set to `approve` or `reject`; a reject needs a comment. Voters must be distinct people, and `distinct_from` applies. The node decides as soon as the outcome can no longer change.

### Rules and computed inputs

```bcl
input "fee" { kind number  compute "(request.pages == '66' ? 10000 : 5000) * (request.service == 'fast_track' ? 2 : 1)" }
rule "fast_track_office" { check "request.service != 'fast_track' or request.office == 'dop'"
                           message "Fast track is only available at the Department of Passports"  path "request.office" }
```

**Computed inputs** are re-evaluated after every change. They are never editable and never need review.

**Rules** can sit on a form (checked when a stage that edits the form advances) or on a stage. They report `422` errors at their `path`.

### Sealed inputs

```bcl
input "offer" { kind number  sealed true }
seal { open_roles ["treasurer", "auditor"]  quorum 2  open_after "tender.closes_at" }
```

**Encryption.** A sealed value is encrypted as soon as it is saved: AES-256-GCM under the resource's `seal_secret`, bound to the case and path. Case data holds only `[sealed]` until the value is opened.

**Opening** (`pipeline.seal_open`):
- It needs `quorum` distinct approvers holding an `open_roles` role; the applicant can't be one of them.
- It cannot happen before `open_after`, which is either an RFC 3339 time or a data path such as a tender deadline.
- Each value's SHA-256 digest is disclosed at opening, so anyone can check the opened value is the one that was submitted.

### External-party links

A stage's `external_roles` may issue a link with `pipeline.link` `{party, scope: ["ward_recommendation"], ttl}`. The link lets an outside party (a ward office, a referee, an employer) fill exactly the scoped forms or inputs, with no account:
- The token is HMAC-signed and expires, and it is **single-use**: submitting consumes it.
- The party sees only the groups showing its scope. It cannot act or operate nodes.
- Its submission is recorded as `link.submitted`, and staff decide what happens next.
- Routes: `GET /links/:token` renders the page; `POST /links/:token {data}` submits it.

### Events and hooks

```bcl
on "sla.*" { hook "passport.notify" }
on "case.completed" { hook "passport.notify"  stage "issuance"  when "request.service == 'fast_track'" }
```

Every operation emits events. The names are:
- **Case:** `case.started`, `case.returned`, `case.rejected`, `case.withdrawn`, `case.completed`, `case.approved`
- **Stage:** `stage.entered`, `stage.completed`, `stage.skipped`
- **Work:** `assigned`, `queued`, `claimed`, `released`, `delegated`, `suspended`, `resumed`
- **SLA:** `sla.warning`, `sla.breached`, `sla.escalated`
- **Other:** `note.added`, `link.issued`, `link.submitted`, `sealed.opened`, `erased`, `retention.applied`

Hooks run **after the change is saved**, receiving `{case, data, stage, event}`, so a notification never announces something that was rolled back. A failing hook is logged; it never undoes the change.

### Analytics

`pipeline.analytics` computes figures from every case's stage timeline:
- case counts by status, and cycle time;
- for each stage:
  - visits, completions and returns;
  - dwell time (p50/p90/avg/max, excluding time on hold);
  - rework rate;
  - open, unassigned and on-hold counts;
  - SLA met or breached, attainment, and at-risk count;
- **bottlenecks**: open work weighted by p90 dwell;
- daily throughput;
- per-assignee load.

It is scoped to the caller's jurisdiction, and accepts `?from=&to=`.

### Bulk operations

`pipeline.bulk` `{ids, op: act|node|work|note|hold, stage?, action?, node?, verb?, work_op?, input}` applies one operation to up to `max_items` cases. Each case is loaded, authorised, claim-checked and saved on its own, and returns `applied` or `failed` with a reason. One failure never blocks the rest, and nothing is applied that the caller could not do case by case.

### Data protection

- **`pii true`:** marks personal inputs.
- **`subject [...]`:** names the paths that identify the data subject.
- **`pipeline.erase`** `{identifiers, mode: anonymize|purge, dry_run, reason}`: finds the subject's cases and anonymises or deletes them.
  - Anonymisation replaces personal values and scrubs them from notes, history comments and verdicts.
  - It drops sealed values, links and access keys.
  - It keeps certificates: they are legal records, and altering them would break verification.
  - Receipts identify the subject by a SHA-256, never by the data itself.
- **`pipeline.hold`** (`place` / `release`): a legal hold blocks erasure and retention. A blocked case is reported, not silently skipped.
- **`retention { after "3650d" action anonymize|purge }`:** is applied by the sweep to finished cases.

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
    seal_secret env("PASSPORT_SEAL_SECRET", "")   # required only with sealed inputs
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
