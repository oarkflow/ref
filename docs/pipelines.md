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
| `default`, `placeholder`, `help`, `span` | Presentation hints. |
| `accept`, `max_bytes`, `max_files` | For `file` inputs: allowed content types, the size limit of each file and how many files the input holds. See [File uploads](#file-uploads). |

Every submitted field is validated before anything is written. Errors come back all at once, as `422` with `details: [{path, rule, message}]`.

### Pages, groups, modes and layouts

A stage's `page` lists `group`s. Each group shows one or more forms.

| | Values |
|---|---|
| page `layout` (how the groups are arranged) | `wizard`, `tabbed`, `accordion`, `stacked`, `grid` |
| group `layout` (how the forms inside a group are arranged) | same as above |
| group `mode` | `editable` (the default), `readonly`, `summary`, `hidden` |
| group `roles`, `visible_if`, `collapsed` | Who sees the group, when it is shown, and whether it starts collapsed. |
| page `info` blocks | Informational content. See [Info blocks and acknowledgements](#info-blocks-and-acknowledgements). |
| page `acknowledge` blocks | Statements the submitter must accept. |

An input is editable only if all of these hold:
- its group is `editable`,
- the stage is open,
- the caller may act at the stage, and
- while the case is returned for correction, the input was flagged.

The **view** (`pipeline.view`) resolves all of this for the caller. It returns groups with their effective modes, inputs with `editable`, `value` (masked where needed), `choices`, `flag` and `verdict`, plus nodes with their allowed operations and the caller's actions. A UI renders it generically; [`examples/passport/static/index.html`](../examples/passport/static/index.html) is such a renderer.

### Access, conditions and dependent inputs

Who may see and change what is declared on the stage, group, form and input, and the engine enforces it on every view and save.

| Setting | On | Effect |
|---|---|---|
| `view_roles` | input, form | Only these roles see it. Others get no input, no value in the view, and a write to it is ignored. |
| `edit_roles` | input, form, group | Only these roles may change it; everyone else sees it read-only. |
| `visible_if` | input, form, group | Hidden while false: not shown, not validated, not accepted on save. |
| `disabled_if` | input, form, group | Shown read-only while true (`disabled: true` in the view); a write to it is ignored. |
| `required_if` | input | Required while true. |

**What conditions can read.** Form values (`billing.claim_status`), the case (`case.org_unit` is the workspace, `case.status`, `case.stage`), the user (`actor.roles has 'qa'`, `actor.is_applicant`) and stages (`stages.coding.status`). A stage's `skip_if` is checked when the case reaches it; `entry_conditions` must all hold or the stage cannot open (the transition fails with the failing condition); `requires` lists stages that must be completed or skipped first.

**Empty values in conditions.** Write `form.field empty` and `form.field not empty`. They are true for a value that was never entered, one that was cleared, and zero. Do not write `form.field != ''` to mean "filled in": a value that was never entered is not equal to `''`, so that is true before the person has answered. Studio's rule builder writes `empty` / `not empty` and flags the other pattern.

Role limits are strict: the applicant of a public stage is not exempt. Only an outside party holding a signed link is, because the link itself names what they may fill. Stage `roles` still decide who may act at all; these settings narrow what an actor who may act can touch.

```bcl
form "billing" {
  edit_roles ["data_entry"]
  input "claim_status" { kind select  options ["Ready", "Hold"] }
  input "hold_reason"  { kind textarea  visible_if "billing.claim_status == 'Hold'"  required_if "billing.claim_status == 'Hold'" }
}
```

**Dependent inputs.** `depends_on` lists inputs (a bare name in the same form, or `form.input`) whose values narrow a lookup input's choices:

```bcl
input "mdm_level" { kind select  options ["Low", "High"] }
input "em_code"   { kind select  lookup "em_codes"  depends_on ["mdm_level"] }
```

The host resolves the set with the dependencies' values (`Engine.LookupFor`). With an `org.hierarchy`, a lookup entry belongs to a value when its attribute named after the dependency holds it (a list may hold several): `{ set "em_codes" code "99283" attrs { mdm_level ["Low", "Moderate"] } }`. An entry without the attribute is not offered. A value outside the narrowed choices is refused on save, using the values submitted in the same request. `pipeline.choices` returns the choices for given dependency values before anything is saved, so a form can refresh a dependent select; the compiler rejects an unknown, self-referential or cyclic `depends_on`.

### Workspaces: settings, lookups and roles

With an `org.hierarchy` (for example company > facility > work item) a case belongs to a unit, and three things follow the tree:

- **Lookups** resolve for the case's unit: inline or stored entries at a node override those above it, and `disabled true` switches an inherited entry off for that subtree.
- **Settings** tune a pipeline per workspace without changing its definition. A unit's attrs hold an `Overrides` document under `pipeline:<name>`; a case reads them from its unit up to the root, the nearest unit winning per stage and per field.

  ```json
  { "stages": { "coding": { "due": "12h", "sla_duration": "6h", "sla_warn_before": "1h", "sla_calendar": "clinic_hours" } },
    "fields": { "patient.fin": { "required": true, "label": "Financial number" },
                "billing": { "disabled": true } } }
  ```

  Settings can tune and narrow; they never grant access. Roles, permissions and conditions are not overridable, and an input the pipeline requires cannot be hidden. `pipeline.workspace_get` and `pipeline.workspace_set` read and replace a unit's settings; `set` validates against the pipeline (`Compiled.CheckOverrides`) and limits the caller to units in its scope. Name the claim holding the units a caller *administers* with `scope_claim`; without it the org's assignment claim applies. A unit's own assignee may edit that unit, never the ones above.
- **Roles** held at a workspace count for cases at and below it. A principal claim (`workspace_roles_claim`, default `workspace_roles`) lists `"unit:role"` entries; a company-level `qa` is a QA role on every facility and work item under the company, a work item-level `coder` only there. They add to the principal's own roles. Work queues pick the stages a user could work from every role held anywhere, then judge each case with the roles held at its own unit.

Staff may start a case only in a unit inside their org scope (a public first stage is open to applicants anywhere).

**Running a workflow inside a workspace.** A pipeline can declare that its cases belong to a workspace:

```bcl
pipeline "encounter" {
  workspace { level "facility"  required true  label "Facility" }
  form "encounter" {
    input "specialty" { kind select  lookup "specialties" }
    input "provider"  { kind select  lookup "providers"  depends_on ["specialty"] }   # that facility's providers for the specialty
    input "nurse"     { kind select  lookup "nurses" }
    input "location"  { kind select  lookup "locations" }
  }
}
```

`pipeline.describe` returns the `workspace` block so a client can ask where to start; `pipeline.start` then takes `org_unit`, refuses a missing one when `required`, refuses a unit of another `level`, and (for staff) refuses a unit outside the caller's scope. Every `lookup` input renders the choices of the case's workspace (inherited from its group, overridden by the facility, switched off with `disabled`), forms refresh a dependent lookup through `pipeline.choices`, and a saved value outside the workspace's list is refused. A dependency written `form.input` matches a lookup attribute named after the input (`attrs { specialty [...] }`). Workspace settings (`pipeline.workspace_get` / `workspace_set`) name their workflow with `pipeline`, so one resource can serve several.

**Who may change workspace data.** `lookup.write` and `pipeline.workspace_set` accept `roles`: only principals holding one of them may write, and their org scope still limits which units they may touch (a facility's admin cannot edit another facility). Without `roles`, scope alone decides. A tenant that has written only reference data of its own keeps the shared workspace tree and the shared reference data beneath its own.

### File uploads

```bcl
resource "files" { kind "storage.fs"  config { dir ".data/uploads" } }
resource "cases" { kind "pipeline.cases"  config { database "db"  storage "files" } }

form "documents" {
  input "photo"    { kind file  required true  accept ["image/jpeg", "image/png"]  max_bytes 2097152 }
  input "evidence" { kind file  accept [".pdf", "image/*"]  max_files 3 }
}
```

When the `pipeline.cases` resource names a `storage` resource (`storage.fs`, `storage.sql`, or any `ObjectStore`), its file inputs take **uploads only**:

- **Upload** (`pipeline.upload`, e.g. `POST …/stages/:stage/files/:path` with `:path` = `documents.photo`). The body is `multipart/form-data` with a file part named `file`, or JSON `{"file": {"filename", "content_base64"}}`. Several files may be sent at once (a list, or repeated parts); they are all-or-nothing.
- **Checks.** The caller must be able to edit the input at the stage, as for a save. The file must be non-empty and at most `max_bytes` (default: the resource's `max_upload_bytes`, 10 MiB). Its type is **sniffed from its magic bytes**; the declared `Content-Type` and the file name are ignored. `accept` lists types (`image/png`), wildcards (`image/*`) or extensions (`.pdf`, meaning the type such content sniffs as). A multi-file input refuses a file beyond `max_files`; a single-file input replaces its file. A file whose SHA-256 is already on the case, at any input, is refused as a duplicate. Failures are `422` with rule `content_type`, `max_bytes`, `max_files`, `duplicate` or `required`.
- **Recorded on the case.** The input's value is the file's metadata (a list when `max_files` > 1): `{id, name, size, content_type, sha256, key, uploaded_by, uploaded_at}`. Expressions can read it (`documents.photo.content_type`). The content is stored under `storage_prefix` + `key`. The history records an `upload` entry, and a `file.uploaded` event is emitted.
- **Saves cannot forge files.** A save may clear a file input, keep it as it is, or drop some files of a multi-file input (matched by `id`; the recorded metadata is kept). Anything else is refused with rule `upload`. Files cannot be uploaded into repeatable forms.
- **Download** (`pipeline.file`, e.g. `GET …/files/:path`, with `?file_id=` for one file of a multi-file input). Only callers who can see the input may download: the applicant, or someone who may view a stage whose page (in a group they can see) or review nodes show the form. A sensitive input also needs a reveal role. The stored bytes are **checked against the recorded size and SHA-256 before anything is sent**. A mismatch or a missing object is `500` with code `INTEGRITY_FAILED` and is logged.
- **Clean-up.** A replaced file's object is deleted once the case is saved. Objects are written before the case is saved and removed again if the save fails. Erasure and retention delete the objects of files they purge or anonymise.

Without `storage`, a file input holds a plain reference (a string, or an object with an `id`), as before.

### Info blocks and acknowledgements

```bcl
page {
  info "privacy" { title "Your data"  text "We keep your documents for five years."  style warning  before "docs" }
  acknowledge "truthful" { title "Declaration"  text "I declare that the information I have given is true." }
  group "docs" { forms ["documents"] }
}
```

An info block's content is its `text`. BCL reserves `body` for a block's own contents, so a `body` attribute on a typed block such as `info` is never read (`body` is still accepted in old documents but stays empty); use `text`.

- **`info`** is content to show: `title`, `body`, `style` (`info`, `warning`, `success` or `danger`), and optionally `before` (the group it is shown above; default the top of the page), `visible_if` and `roles`. The view lists it under `page.info`.
- **`acknowledge`** is a statement the person submitting the stage must accept. It has `text`, and optionally `title`, `before` and `visible_if`. The view lists it under `page.acknowledgements` with the `sha256` of the exact wording and the latest acceptance of that wording.
- **Accepting.** An `advance` or `approve` action sends `acknowledgements`: a list of names, or an object of names to `true` or to the `sha256` the client displayed. A missing acknowledgement fails with `422` at `acknowledgements.<name>` (rule `acknowledgement`). A hash that no longer matches the wording fails with rule `wording_changed`, so nobody accepts text they were not shown.
- **Recorded.** Each acceptance is stored on the case as `{stage, name, text, sha256, by, at, revision}`. `pipeline.get` returns them as `acknowledgements`, so the exact wording accepted can be proven later. Actions taken by the system (an SLA breach) need no acknowledgement.

### Confirm before submit

`confirm` on an action is a question for the client to ask. `confirm_submit true` makes the server enforce it. Set it on a stage (for its `advance` and `approve` actions) or on a single action.

1. **Review.** The first request (no `confirm_token`) runs every check the action would: permission, the data sent, required inputs, acknowledgements, rules and nodes. It saves nothing and returns `{confirmation_required: true, confirm_token, review}`. The review holds:
   - the case header and the action;
   - the `confirm` text, as `prompt`;
   - the changed paths;
   - every answered field (`path`, `label`, `group`, masked `value`);
   - the accepted acknowledgements.
2. **Commit.** The same request again, with `confirm_token`, takes the action.

The token is HMAC-signed with the resource's `signing_secret` (or a per-process key without one). It is bound to the case, stage, action, caller, **case revision** and a **SHA-256 of the submission** (`data`, `comment`, `flags` and `acknowledgements`), and expires after `confirm_ttl` (default 30m). Any change to the case since the review, or different data in the commit, makes it invalid: `409` with code `CONFIRMATION_INVALID`, and the client reviews again. Once the action commits the revision moves on, so a token cannot be replayed. A confirm-submit action cannot be applied through `pipeline.bulk`.

### Intro page, hold/resume and information requests

- **`intro`** (on the pipeline) is the page shown before a case starts: `title`, `image`, `image_alt`, `content` (HTML, render it in a sandboxed iframe), `button`, and `document "name" { title url }` blocks. `pipeline.describe` returns it; the host renders it.
- **`suspend_roles`, `resume_roles`, `info_roles`** on the pipeline are defaults for every stage; a stage can set its own. `suspend_roles` may put the case on hold, `resume_roles` (else `suspend_roles`) may lift it, `info_roles` may ask the applicant for more information. Setting any `info_roles` enables information requests. These roles may also view the stage.
- **`summary`** (on a stage) is the page shown before submitting: `title`, `description`, `groups`, `fields` (form.input paths) and `button`. It makes the stage's advance and approve actions two-step (see Confirm before submit), and the review lists only the chosen groups or fields.

### Starting a stage, the activity log and missed deadlines

- **`require_start true`** (on a stage) makes the stage read-only, with no actions, until someone presses Start (`pipeline.work` op `start`). Starting records who began and when, and on a claimable stage it claims the work. Without `require_start`, the first save, node action or action counts as the start. Starting is also what separates *waiting* time from *working* time in the log.
- **`pipeline.overview`** is the case at a glance (not a stage screen): the case and its status, the current stage with who started it and its deadline, the data gathered so far from every stage the caller may see (`fields`, masked where the stage masks), the `log`, and what the caller can do next (`can_work`, `needs_start`, `work.operations`). Each `log` entry is one visit of a stage: `started_by`, `started_at`, `wait_seconds` (entered to started), `work_seconds` (started to left, minus time on hold), `hold_seconds`, `total_seconds`, `outcome`, `due_at`, `breached`. People are left out for callers who are not staff.
- **A missed deadline is explained.** When the sweep finds an SLA past due it records why (`not_picked_up`, `not_started` or `not_completed`, with a sentence, who held it, who started it, how overdue and any time on hold), keeps it permanently in the case's `breaches`, and emits `sla.breached` with `reason`, `cause`, `started_by`, `overdue_seconds` and `notify` (the SLA's `notify` list plus `role:<role>` for each admin role). `on "sla.breached" { hook "..." }` receives it in `input.event.detail`.
- **`on_breach`** decides what happens automatically: `notify` (the default; nothing changes, admins take the stand), `reassign` (to `reassign_roles`, never the current holder), `release`, `reset`, `extend` (by `extend_by`), `return`, or an action name. `pickup_timeout` with `on_pickup_timeout` does the same for work nobody claimed.
- **`admin_roles`** (on a stage, or on the pipeline as a default) may take a stand on a stage: `reset`, `assign` (to a listed worker), `extend` (`by`, defaulting to the SLA's `extend_by`), `waive` (a reason is required; the miss stays on record), or `release`. Each settles the open breach with who, what and why (`resolution`, `resolved_by`, `resolution_comment`) and emits `sla.resolved`. Admin roles may also view the stage.
- **`reset`** restores the case data to what it was when the stage opened, clears the assignee, the start, holds, flags, check results and acknowledgements of that stage, closes the visit as `reset`, opens a new one and restarts the SLA. The history keeps everything that happened. The copy used for this (`entry_data`) is never served to clients and is wiped by erasure.
- Events added: `stage.started`, `stage.reset`, `sla.resolved`. Run `pipeline.sweep` on a schedule or no deadline is ever noticed.

### Who handles a stage, and data entries

A stage is handled by one of four kinds of actor:
- **The applicant** (`public true`): the person who started the case, with or without an account. A public stage may sit anywhere in the workflow, not only first, so the applicant can be asked for more later and the case moves on to the next stage when they submit.
- **Workflow users** (`roles [...]`): staff holding a role; add `claimable`, `require_start` and a `routing` strategy to share the work.
- **An outside party** (`external { party "KYC agent"  assignee_from "request.agent_email"  scope [...]  ttl "5d" }`): someone who is not a workflow user, such as a KYC verifier or a referee. A single-use signed link is issued automatically when the stage opens (the `link.issued` event carries `token`, so a hook can email it). They fill the stage's forms (or `scope`) with no account, and their submission completes the stage. Staff see who it waits for (`party` in the view and the overview); only the stage's roles and admins get the token, and an admin may reissue the link (`pipeline.work` op `reissue`, which kills the old one). Give the stage `view_roles` for the staff who should see it.
- **The system**: automated nodes and `auto_advance`.

**`entries`** turns a stage into a data-entry stage, where each person enters their own record instead of editing one shared document:

| Setting | Meaning |
|---|---|
| `per_user` | Entries one person may make: `1` is a single entry, `N` a specific number, `0` (or unset) no limit. |
| `min`, `max` | Submitted entries (from anyone) needed to complete the stage (default 1), and a cap on all entries, drafts included (`0` is no cap). |
| `allow_draft` | An entry may be kept unsubmitted. Without it an entry is submitted or not made. |
| `allow_edit` | The maker may change a submitted entry until the stage completes. Drafts are always editable by their maker. |
| `merge` | Which submitted entry becomes the case data when the stage completes: `last` (default), `first` or `none`. The others stay on the stage. |

Entries use `pipeline.entry` (`{entry, data, submit}`; op `delete` removes one) and `pipeline.view` with `entry` to render one. Each entry is validated like an ordinary stage (types, required inputs, rules), and drafts are private to their maker; staff of the stage read everyone's submitted entries in the overview (`entry_rows`). A stage with entries cannot also be `multi_date` or `external`, and its case data is not edited directly.

**Deadlines apply to every kind of stage.** The SLA of an applicant stage or an outside-party stage explains itself (`applicant_no_response`, `external_no_response`) and an entries stage reports how many entries came in. `on_breach "expire"` closes the case as withdrawn when nobody answers in time (event `case.expired`).

### Forms across stages: access per stage, role and condition

A form is defined once and listed in the page groups of any stages. Its data lives in the case, so it moves with the case from stage to stage; what changes is how each stage presents it. Beyond the input, form and group settings above (`view_roles`, `edit_roles`, `visible_if`, `disabled_if`, group `mode`), a group can carry `access` blocks that adjust single inputs, or a whole form, for that stage only:

```bcl
group "request" {
  mode readonly                       # read-only at this stage...
  forms ["request"]
  access "request.address" {          # ...except one field, which officers may correct
    mode editable
    edit_roles ["officer"]
    label "Address to certify (officers may correct it)"
  }
  access "request.email"   { roles ["officer", "manager", "admin"] }   # only these roles see it here
  access "request.id_no"   { mode hidden }                              # not shown at this stage
  access "request.notes"   { disabled_if "case.status == 'returned'"  required_if "true" }
  access "request"         { mode readonly }                            # a bare form name covers every input of the form
}
```

| Setting | Meaning |
|---|---|
| `mode` | `editable` switches a field on inside a read-only group; `readonly` switches it off inside an editable one; `hidden` removes it for everyone. Empty keeps the group's mode. |
| `roles` / `edit_roles` | Who sees the field at this stage (others do not see it at all) and who may change it (others see it read-only). |
| `visible_if`, `disabled_if`, `required_if` | Show it only while true; show it read-only while true; make it required at this stage while true (`"true"` for always). |
| `label`, `help`, `placeholder` | Reword it for this stage. |

Hidden, read-only and disabled fields are never accepted on save, hidden fields are not required or validated, and their data is kept as is for the stages that do show them. The more specific `form.input` rule wins over a bare `form` rule. The rules narrow what a person who may act at the stage can touch; they never grant access to a stage. The compiler rejects a rule for a form that is not in the group, an input that does not exist, or an unknown mode. **The block is `access`, not `field`: BCL does not bind a block called `field`.**

`pipeline.matrix` (`GET /api/pipelines/:pipeline/matrix`) returns the **field access matrix**: for every stage page, each input's effective mode, who sees it, who may change it and the conditions that apply. casekit renders it at `/guide/:workflow/fields`.

### Layout and look (`ui`)

A workflow declares how it looks and how its screens are laid out. Everything is optional: with no `ui` block the host renders its basic layout (a stage strip, then three columns on the overview and a main column with a rail on the task screen). `pipeline.describe`, `pipeline.overview` and `pipeline.view` carry the resolved `ui`, so any client can render it.

```bcl
ui {
  brand   "Civil Registry"      # replaces the product name in the sidebar
  accent  "#0f766e"             # one flat brand colour (hex); tints and text colour are derived
  density compact               # comfortable (default) or compact
  overview {
    mode console                # console | split | stacked
    flow hide                   # hide the strip of stages
    column "facts" { width "280px"  panels ["case", "stage", "actions"] }
    column "work"  { width "1fr"    panels ["stand", "next", "summary"] }
    panel "log" { title "History"  collapsed true }
  }
  task { mode split }
}
stage "review" {
  ui { column "main" { width "1fr" panels ["form", "checks"] }  column "rail" { width "300px" panels ["stage"] } }
}
```

- **Panels.** Overview: `case`, `stage`, `actions`, `stand`, `next`, `certificate`, `party`, `entries`, `summary`, `log`, `missed`. Task: `form`, `checks`, `stage`, `case`.
- **Columns** place panels left to right; a panel left out of every column is not shown, and a panel may appear once. `width` is a CSS length (`320px`, `30%`, `24rem`) or a share (`1fr`). Without `column` blocks each `mode` uses the basic arrangement: `console` three columns, `split` a main column and a rail, `stacked` one column.
- **`panel "id"`** adjusts one panel: `title`, `hidden`, `collapsed`.
- **A stage's own `ui`** replaces the workflow's `task` layout for that stage.
- The compiler rejects an unknown panel, a panel used twice, a bad width, mode, density or colour.

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
| `action` blocks | The buttons: `outcome` is `advance`, `return` (`return_to`), `reject`, `approve`, `withdraw` or `hold`. An action can also set `roles`, `comment_required`, `condition`, `confirm`, `confirm_submit`, `skip_nodes` and `next`. A stage without actions gets a default `submit`. |
| `confirm_submit` | Makes the stage's `advance` and `approve` actions two-step. See [Confirm before submit](#confirm-before-submit). |
| `assign` | `path { value "expr" }` sets data when the stage completes. |
| `certificate` | Issues a certificate when the stage completes. |
| `due` | The SLA (e.g. `"72h"`). Queue rows carry `due_at` and `overdue`. |
| `on_enter`, `on_complete` | Intents (or `script:<id>`, see [Scripts](#scripts)) run as hooks. A failing hook aborts the operation, so nothing is saved. |
| `review` blocks | Human-in-the-loop review modes: `diff`, `gate`, `triage` and `sampling`. See [Review modes](#review-modes). |

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
| `gate` | `approve`, `reject` | `approvals` distinct reviewers approved. A `review "gate"` block declares it. See [Review modes](#review-modes). |
| `liveness` | `challenge`, `verify` | A fresh challenge was answered and the host's `Engine.Liveness` verifier scored it at or above `min_score` (and, with `match_with`, the face matched that data path at `min_match`). Challenges are random gestures (`challenges`, default 2), single use, bound to the actor who asked, and expire after `challenge_ttl` (default `2m`). `max_attempts` (default 3) bounds retries; a verifier outage does not use an attempt. The engine ships no biometric model: supply a `LivenessVerifier`, and capture frames in the client as file uploads referenced in `result`. |
| any | `waive` (for `waive_roles`) | — |

Node statuses are `pending`, `passed`, `failed`, `waived` and `skipped` (`applies_if` false). `optional` nodes never block their stage.

### The correction loop

1. A reviewer flags inputs, then takes a `return` action with a comment. `flags` on the action can add further inputs.
2. The target stage reopens as `returned`, and the case status becomes `returned`. **Only the flagged inputs are editable.** Anything else submitted is ignored.
3. The applicant resubmits. The case goes straight back to the stage that returned it, not through the stages in between.
4. Verified verdicts are kept. Only the flagged inputs need review again.

### Certificates

A `certificate` block names the data paths copied into the certificate, its number format (`PPA-{year}-{seq:4}`, `{case}`) and its `validity`.

An issued certificate is canonicalised and SHA-256 hashed. With the resource's `signing_secret`, it is also HMAC-signed. With a `signer` (a `crypto.signer` resource), it also carries a `key_signature` (`{alg, kid, sig}`, Ed25519 or RSA) over the same canonical content. That signature can be verified offline by anyone holding the published JWKS: `pipeline.VerifyCertificateSignature(cert, keys)` with `keys` from `signing.ParseJWKS`. The HMAC stays for backward compatibility: certificates issued before the signer was added keep verifying. Every certificate carries a short verification code.

`pipeline.verify` checks the hash, the signatures, expiry and revocation for a given number or code. Any edit to the content is detected. For a key-signed certificate, the response also includes everything the signature covers, so the holder can re-verify it offline.

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

### Where a stage is in its life, and releasing a hold

| State | How it is held |
|---|---|
| Not started | stage `active`, no `started_at` (with `require_start`, nothing can be edited, saved or acted on) |
| In progress | stage `active`, `started_at` and `started_by` set |
| Complete | an `advance` action ends the visit and moves the case on; a terminal outcome closes the case |
| On hold | `suspended` on the stage (reason, who, when, optional `until`); the SLA clock stops; every write is refused |
| Needs information | case status `requires_information`; advancing is blocked until the applicant answers |

The case data is one map; each stage keeps a snapshot of it from when the stage was entered, and the history records every start, hold, release and assignment.

A hold is lifted with `work/resume`, and the person lifting it chooses where the work goes (`to`, and optionally `assignee`):

- **`same`** (default): the hold ends and work carries on at this stage; the deadline moves by the time on hold. `assignee` hands it to a named person who can work the stage (otherwise whoever holds it keeps it).
- **`previous`**: the work goes back to the stage the case came from, as new work. That stage starts from nothing: it must be started again, its deadline starts afresh, checks, verdicts, flags and acknowledgements are made again, and nobody holds it unless `assignee` names someone who can work it (otherwise it takes its own routing or queue). This stage lets go completely: its visit ends as `released`, it is not started and has no holder. The case data is **not** rolled back; the history records who released it, from where to where, and why.

Only the roles in `resume_roles` (else `suspend_roles`) may do either. The overview lists the places a hold can go in `work.resume_targets`.

### Configuring holds, releases, starts and questions

Each of these has a workflow-wide block and the same block on a stage to override it; every setting has a default that keeps the behaviour described above.

```bcl
pipeline "encounter" {
  information {                      # questions put to people
    roles ["coder", "supervisor"]    # who may ask (same as info_roles)
    to ["creator", "previous", "user"]  # who may be asked: whoever created the case (default), the roles of the
                                        # stage the case came from, or a named person
    sla "pause"                      # pause (default): the stage's deadline clock stops until answered; run: it keeps going
    due "24h"                        # how long they have (empty: no limit)
    on_timeout "notify"              # notify (default): record it once and tell people; withdraw: close the case as withdrawn
  }
  release {                          # lifting a hold
    targets ["same", "previous"]     # where work may go (default: both)
    assignee "optional"              # may a person be named: optional (default), required, none
    restore_data false               # true: a release to the previous stage also puts the case data back to how that stage found it
  }
  start {                            # stages with require_start
    on_create "auto"                 # auto (default): whoever creates the case has started its first stage; manual: they press Start
    on_return "keep"                 # keep (default): a stage already started stays started when the case returns; restart: start again
  }
  stage "coding" {
    sla { duration "2h"  on_hold "pause" }   # on_hold: pause (default) stops the clock for holds and open questions; run keeps it going
    information { to ["creator"] }           # a stage may override any block
  }
}
```

**Asking for inputs.** With `information { fields true }` a question may also name inputs (`fields ["encounter.mrn"]`, plain inputs only: not repeatable, file, calculated or sealed). The person asked must fill in each one; the values are checked like any entry (type, pattern, lookup), written into the case data whatever their rights on the stage, and the history entry lists the paths they changed. The overview's `information.fields` says what was asked and `work.info_fields` whether the viewer may ask.

**Notifying people.** Notify rules accept the recipients `asked` (whoever the latest question was put to: the creator, everyone holding the previous stage's roles, or the named person) and `asker`, next to `assignee`, `applicant`, `role:<role>` and user ids, and the text token `{info.reason}` (the latest question). `info.reason` is a dotted token because the BCL parser drops a bare `{name}`. Write the text of a rule as `message` (the BCL parser never binds a key named `body`; `message` is the same field). Events: `case.information_requested`, `case.information_submitted`, `case.information_overdue`, `assigned`. A rule's channels are delivered by `notify_channels` on `pipeline.cases` (channel name to an intent); casekit maps `inbox` to a table, a per-person list scoped with `database.crud owner_column`, and a bell in the sidebar.

**Who the workers are.** `directory_resource "<identity.users resource>"` on `pipeline.cases` makes the directory's members the workers of every pipeline, so `role:<role>` recipients, assignment and routing find the people who actually sign in (without it only the workflow's own `worker` blocks count). A signed-in caller's workflow description lists them as `workers`. A role recipient is limited to the people of that role who work where the case is: workers tied to org units (an identity member's org unit) are skipped when those units do not cover the case's unit, so a nurse of another facility is not told about this facility's case; workers with no unit work anywhere. A host that provisions accounts itself (casekit's `-demo` accounts) can call `IdentityDirectory.EnsureMember(tenant, email, name, roles, orgUnit)` to make them real, idempotent directory members and issue their tokens for the returned user id.

A channel that stores one row per message (`database.insert_many` over `input.items`, with `set { user_id "input.user" }`) turns a digest of several cases into one row each; items carry flat `case_id`, `case_number` and `pipeline` keys for that.

An unanswered question shows as overdue in the overview and the work list as soon as its due time passes; the sweep adds the `information.overdue` history entry and `on_timeout` action.

Questions: `POST .../information-request` takes `reason`, `to`, `fields` and (for `user`) `assignee`; the case waits at its stage with status `requires_information` and the request records who was asked and when the answer is due. Only the person asked may answer (`.../information-response`): for `previous` anyone holding one of that stage's roles, for `user` the named person, for `creator` the creator. They can open the case and find it under `scope=answer` ("Questions for me"). An answer restarts the clock by the working time spent waiting. If the answer is not in by `due`, the sweep records `information.overdue` once and applies `on_timeout`. The overview carries `information`, `information_can_answer` and, for those who may ask, `work.info_targets`.

### Start, and limits on what a person creates

`require_start true` on a stage makes Start mandatory: nothing on the stage can be edited, saved, uploaded or acted on until someone presses Start (the platform refuses it, whatever the UI shows). Data is started **once**:

- Creating a case starts its first stage for the person who created it (no second Start on the stage they just created it in). A public applicant has no Start to press.
- A stage the data comes back to (returned for correction, or sent again) is still started; only a reset (`reset` on an SLA breach, or an administrator's reset) asks for a new Start.
- A stage that follows is different work for a different person, so it waits for its own Start.

The work list (`pipeline.list`) marks each row with `require_start` and `can_start`. The casekit "My work" page uses them to offer Start on a row, on the ticked rows, or on every row of a step ("Start all"), each through `POST .../stages/:stage/work/start`; the case's own page offers Start too.

```bcl
pipeline "encounter" {
  limits {
    per_user 50          # how many cases one person may create (0 = no limit)
    count "open"         # "open": only unfinished cases count, so finishing one frees a place; "all" (default)
    message "..."        # optional wording of the refusal
  }
}
```

A person at the limit is refused (409) with the limit in the message. The workflow description carries `limits`, so a start page can show "12 of 50 used". Applicants without an account are not counted.

### Lookups from any data source

A `lookup "name"` input takes its choices from a `lookup.sources` resource (named by `lookup_resource` on `pipeline.cases`), then from the `org.hierarchy` lookups for any set the source does not define.

```bcl
resource "catalog" {
  kind "lookup.sources"
  config {
    database "database"                 # for sql sets (read-only SELECTs)
    service  "payer_api"                # a service.http resource, for http sets
    sets [
      { name "sex" type "static" entries [ { value "F" label "Female" }, { value "M" label "Male" } ] },
      { name "em_codes" type "sql" args ["mdm_level"] ttl "10m"
        query "SELECT code AS value, label FROM em_codes WHERE mdm_level = ?" },
      { name "payers" type "http" path "/v1/payers" items_path "data" value_field "id" label_field "name" ttl "15m" }
    ]
  }
}
```

- **static**: `entries` of `{value label attrs{}}`; `attrs` lists the values of a `depends_on` field an entry applies to.
- **sql**: one `SELECT` returning `value` and `label`. `args` names the `depends_on` fields passed as positional parameters; with a parameter unset there are no choices yet.
- **http**: calls the `service` (so its host allowlist and private-network block apply). `params` may use `{field}` placeholders for dependencies; `items_path` is the dotted path to the list; each item is an object (`value_field`, `label_field`) or a plain value.
- **mapping** (every kind): `value_field` and `label_field` name the source columns or JSON fields for an option's value and label (SQL defaults to columns called `value`/`code` and `label`/`name`). `attrs { specialty "specialties" }` exposes a source field as an attribute, so `depends_on` and `lookup_where` can narrow a datasource set the way they narrow a static one; a text value such as `CARD|EM` becomes a list.
- **filters** (every kind): `filters [ { on "facility_id" op "eq" value "{facility}" } ]` keeps matching rows (`op`: eq, ne, in, not_in, contains, prefix, gt, ge, lt, le; a list-valued field matches if any element does). `sort "full_name"` orders the rows. `merge true` offers the datasource rows together with the workspace's own `org.hierarchy` entries instead of replacing them.
- **workspace context**: a case that runs in a workspace passes its unit to the datasource. `{workspace}` is that unit and there is one placeholder per hierarchy level, such as `{facility}` or `{organization}`. They work in filter values, `args`, `params` and `path`, next to `depends_on` values (`{specialty}`). A hospital that keeps its providers in a MySQL `tbl_providers` table with a `facility_id` column maps the columns and filters on `{facility}`; nothing about the rows lives in BCL. A placeholder that is not set yet (a dependency not chosen) means no choices yet.
- **ownership and writes** (sql): `owner_field "facility_id"` tells administrators which workspace a row belongs to. `upsert` and `remove` are single INSERT/REPLACE/UPDATE/DELETE statements with `upsert_args`/`remove_args` naming their values (`code`, `label`, `sort`, `workspace`, `attrs.<name>`), which lets a workspace admin maintain the rows from the Workspace page (`pipeline.lookup_source_write`, admin roles and org scope enforced). Placeholders written `{facility?}` are optional: unset, they expand to nothing, so `value "{organization?},{facility?},{workspace}"` with `op "in"` means "the group's rows, the facility's rows and this unit's".
- **seed files**: a `database.sql` resource takes `migration_files ["seed/reference.sql"]`, SQL files relative to the application, so tables and their starting data stay out of the BCL (casekit keeps all its reference data in `examples/casekit/seed/reference.sql`).
- **cache**: `ttl` (default 5m); when a refresh fails the last good list is served instead of nothing. `timeout` defaults to 3s.

**Searchable, clearable, taggable.** A `select` or `multiselect` backed by a lookup (or with many options) is rendered as a combobox: type to filter, a clear button (on by default unless the input is `required`, forced with `clearable true`), chips for several values, and keyboard and screen-reader support. `searchable true` and `taggable true` are the other switches. With `taggable true` free text becomes a value, so the input accepts values outside its lookup set or `options`. When a set is large the control asks the server as the user types: `choices` takes `q` (`input.q`, or `q_fact`), which a `lookup.sources` set applies to value and label before its `max_rows` cap, and the org lookups apply the same way.

An input can also fix an attribute with `lookup_where ["provider_type=physician"]`, so several inputs (attending, resident, billing, secondary provider) draw different kinds from one `providers` set. `pipeline.lookup_sources` shows an administrator what the datasource yields for a workspace.

### SLAs, business calendars and escalation

```bcl
sla {
  duration "2d"                 # 2 working days; "16h" = 16 working hours
  warn_before "4h"
  calendar "office"
  on_breach "reassign"          # notify | reassign | release | return (return_to) | <action name>
  pickup_timeout "1h"           # unclaimed work breaches after this; on_pickup_timeout notify | reassign
  escalate "senior" { after "1d"  assign_roles ["senior_officer"]  notify ["director"] }
  escalate "director" { after "3d"  assign_roles ["director"] }
}
```

**Calendars.** A calendar has weekly windows, holidays and a time zone. Holidays are either fixed dates or `MM-DD` dates that repeat every year. With a calendar, SLA time is counted in working time, and `d` means working days.

**`pipeline.sweep`.** Run it on a schedule. It applies every time-based rule:
- the warning, with the `sla.warning` event;
- the pickup timeout of unclaimed work, with the `sla.pickup_breached` event (once);
- the breach, with the `sla.breached` event and the breach action;
- each escalation level after the breach, which re-routes to the level's roles, with the `sla.escalated` event;
- the end of holds whose `until` has passed;
- retention.

A case changed concurrently is skipped until the next run.

Run the sweep from a `schedule` (through a queue) and, if you like, an admin-only route. A sweep restricted with `roles` still runs when the schedule starts it: work the platform starts itself is trusted.

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
- **Review:** `triaged`, `review.sampled`, `review.not_sampled`, `review.approved`, `review.rejected`, `review.gate_opened`
- **Other:** `note.added`, `file.uploaded`, `link.issued`, `link.submitted`, `sealed.opened`, `erased`, `retention.applied`

**Delivery is durable.** Events that some hook listens to are written to an outbox in the **same transaction** as the case change. An event exists exactly when its change was committed.

A background dispatcher (one per replica; leases stop two replicas delivering the same event) does the following:
1. Claims due events.
2. Runs their hooks with `{case, data, stage, event}`, where `event.attempt` counts attempts.
3. Acknowledges the event once every hook succeeds.
4. On failure, retries with exponential backoff. By default the first retry is after 2s, the delay doubles up to 10 minutes, and the event is **dead-lettered** after 10 attempts. `event_retry_base` and `event_max_attempts` on the resource change these defaults.

Because a hook may run again after a partial failure, **hooks should be idempotent**. `event.id` is stable across retries, which makes a good deduplication key.

`pipeline.events` lists dead-lettered events and requeues one for immediate delivery, once its cause is fixed.

The operations actions (`pipeline.events`, `pipeline.hold`, `pipeline.erase`, `pipeline.sweep`, `pipeline.analytics`, and `process.analytics`) always need an authenticated caller, even when `roles` is empty and the route has no `auth`; an anonymous call gets `401`. `roles`, when set, further restricts them to holders of one of those roles. Work the platform starts itself (a schedule, a queue worker, a process step) carries no principal and is allowed, so a scheduled `pipeline.sweep` keeps working; an `async` route enqueues with the caller's identity, so do not put these actions behind an anonymous async route.

### Review modes

A stage's `review` blocks add human-in-the-loop policies. A stage may combine several:

```bcl
review "diff"     { against submission }                 # or approved
review "gate"     { approvals 2  roles ["senior"] }
review "triage"   { bucket "urgent" { condition "claim.amount > 1000"  priority 1  queue "urgent" } }
review "sampling" { percent 10  always_review_if ["claim.amount > 10000"] }
```

| Mode | Effect |
|---|---|
| `diff` | The view shows field-level changes since the previous submission or the last approved version. Approval records the revision reviewed. |
| `gate` | A hard gate: the case cannot advance until N distinct reviewers (optionally with given roles) approve. Rejections are recorded. |
| `triage` | Buckets classify a case on arrival into a priority and queue. Work lists are ordered by priority. |
| `sampling` | Only a deterministic percentage (by a hash of the case id), or cases matching a condition, are reviewed. The rest pass with a `not_sampled` audit record. |

See [Review modes and notifications](pipeline-review.md) for the details.

### Notifications

`notify "event" { to [...]  channels [...]  severity …  subject "…" }` rules send notifications to people. Delivery goes through channel intents that are named in the resource's `notify_channels`. Each person's preferences decide what reaches them:
- channel opt-in and opt-out, per event pattern;
- quiet hours in their time zone (urgent and critical notifications bypass them);
- hourly or daily digests.

Preferences and pending notifications are stored durably and flushed by the resource's background loop. See [Review modes and notifications](pipeline-review.md#notifications).

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
    signer "keys"                   # optional crypto.signer: asymmetric certificate signatures
    seal_secret env("PASSPORT_SEAL_SECRET", "")   # required only with sealed inputs
    notify_channels { email "passport.send_email" }  # required only with notify rules
    storage "files"                 # storage resource for file uploads (optional)
    # storage_prefix "pipeline/"  max_upload_bytes 10485760  confirm_ttl "30m"
  }
}
```

The resource compiles each pipeline and every expression in it at load time, so a typo stops the deployment. With a database, it creates `<prefix>cases`, `<prefix>certificates`, `<prefix>sequences`, `<prefix>outbox`, `<prefix>notify_prefs` and `<prefix>notifications`.

The stores are verified by one conformance suite against memory, SQLite and PostgreSQL. To include PostgreSQL, set `TEST_POSTGRES_DSN`; the Passport example also runs end to end on it when that variable is set. Routing reads each worker's workload with a single indexed `GROUP BY` rather than loading every open case. Sweeps, analytics and erasure page through all cases, and analytics folds them one at a time, so no scan is capped or holds every case in memory.

| Action | Parameters | Result |
|---|---|---|
| `pipeline.describe` | — | The definition |
| `pipeline.start` | `input.org_unit`, `input.data` | The view, plus `access_key` for anonymous callers |
| `pipeline.view` | `id`, `stage?` | The view |
| `pipeline.save` | `id`, `stage`, `input.data` | The view |
| `pipeline.act` | `id`, `stage`, `action`, body `{comment, data, flags, acknowledgements, confirm_token, revision}` | The view, or `{confirmation_required, confirm_token, review}` for the first step of a `confirm_submit` action |
| `pipeline.node` | `id`, `stage`, `node`, `verb`, body `{comment, verdicts, result}` | The view |
| `pipeline.list` | `scope` = `queue` \| `mine` \| `assigned` \| `all`, `status`, `stage`, `queue`, `order` (`priority` \| `newest`), `limit`, `offset` | Case rows |
| `pipeline.get` | `id` | Header, stage states, history and certificates |
| `pipeline.verify` | `key` (a number or code) | `{valid, reason, certificate}` |
| `pipeline.notify_prefs` | — | The caller's notification preferences and pending notifications |
| `pipeline.notify_prefs_set` | body: the preferences | The same, after replacing them |
| `pipeline.upload` | `id`, `stage`, `path`, body `file` (multipart or base64 JSON) | The view, plus `files` |
| `pipeline.file` | `id`, `path`, `file_id?` | The file, checksum-verified |

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
| Invalid or stale confirmation token | `409`, code `CONFIRMATION_INVALID` |
| Stored file fails its checksum | `500`, code `INTEGRITY_FAILED` |
| Invalid fields | `422`, with `details` |

**Access.** When an org resource is configured, officers see only cases in their jurisdiction. A case outside it reads as not found, so its existence is not disclosed. An anonymous applicant returns to their case with the access key in the `X-Access-Key` header. Only a SHA-256 hash of the key is stored.

**Hooks.** An automation or stage hook intent receives `{case, data, stage, node}` as its input.

## Using the engine directly

```go
compiled, err := pipeline.Compile(&def)
e := pipeline.NewEngine(compiled)
e.Eval = myEvaluator          // expressions
e.Automation = myAutomation   // automated nodes
e.SigningKey = key            // certificates (HMAC)
e.Signer = keySet             // certificates (Ed25519/RSA, a *signing.KeySet)
c, _ := e.Start(ctx, applicant, pipeline.StartOptions{Number: "PP-1"})
c, err = e.Act(ctx, c, applicant, "application", "submit", pipeline.ActInput{Data: data})
v, _ := e.View(c, officer, "")
c, ref, _, err := e.Attach(ctx, c, applicant, "application", "documents.photo", pipeline.Upload{Name: "me.jpg", Content: b})
review, err := e.Preview(ctx, c, applicant, "application", "submit", in)   // confirm_submit, step 1
```

Every operation takes a case and returns a new one. The input case is never mutated, so a failed operation leaves nothing half-applied. Persist the result with a `pipeline.Store`: `MemoryStore`, or `SQLStore` for PostgreSQL, MySQL or SQLite. Its conformance suite, the outbox included, runs on SQLite, PostgreSQL 16 and MariaDB 10.11 (the `mysql` dialect; MySQL 8 itself has not been run).

## Scripts

Declare each script once as a top-level `script` block, then refer to it by id wherever it is needed:

```bcl
script "approval_limit" {
  description "Fails an approval over the facility limit"
  file "scripts/approval_limit.spl"      # or: source "..."
  timeout "1s"                           # optional; default 2s
  max_steps 500000                       # optional; default 1,000,000
  max_heap_mb 32                         # optional; heap growth allowed per run, default none
}

pipeline "claims" {
  on "case.completed" { hook "script:completion_note" }
  stage "review" {
    on_enter "script:stamp"
    node "limit" { kind automated  hook "script:approval_limit" }
  }
}
```

```spl
// scripts/approval_limit.spl: the last expression is the result
{"passed": data.amount <= 10000, "reason": "over the approval limit"}
```

Where a script id can be used:

| Where | How |
|---|---|
| Stage `on_enter` / `on_complete` | `"script:<id>"`. A failing script aborts the operation. |
| Event hook (`on "event" { hook ... }`) | `"script:<id>"`. Delivered through the outbox like an intent hook. |
| Automated node | `hook "script:<id>"`. The node passes unless the result has `passed: false`; the result is stored on the node. |
| Any intent | A node with `uses "script"` and `config { script "<id>" }`; `provides` takes the result, or an object split across several names. |

A script block names exactly one of `source` or `file`. `file` is an `.spl` file in the application, read from the application files (including a sealed Aegis package), never from the host disk. A duplicate id, a missing file, a syntax error, or a hook naming an undeclared script stops startup.

The script runs on [github.com/oarkflow/interpreter](https://github.com/oarkflow/interpreter). A hook script sees the hook input both whole as `input` and as variables: `kase` (the case; `case` is an SPL keyword), `data`, `stage`, `node`, `event`, plus `principal` and `tenant`. An intent node's script sees the node's facts and `principal`, `tenant`, `now`.

Scripts are sandboxed: they can compute over their input and nothing else. Filesystem, network, exec, environment, database and dynamic imports are all denied, and each run is capped on time and steps, with string, array and hash size caps. `max_heap_mb` limits how far the process heap may grow during one run (measured from the heap at the start of the run). The heap is shared with concurrent requests, so it guards against a runaway script rather than accounting exactly; without it there is no heap limit. The interpreter's security policy is process-wide, so scripts run one at a time; keep them short.
