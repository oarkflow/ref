# Application patterns: hierarchies and dates of service

REF's platform layer has two families of building blocks that come up in almost every line-of-business application:

- **Hierarchical organisations.** Examples are a government running states, districts and municipalities; a hospital group with facilities and departments; a franchise with regions and stores. Users are assigned to a unit and may reach only that unit's subtree. Reference data (code tables, service catalogues, fee schedules) is configured high up and overridden lower down.
- **Dates of service.** Examples are medical coding, where an encounter falls on one date or spans several; field inspections; home-care visits; timesheets. The activity is recorded against the civil date or dates it happened, is validated against business rules, and is billed one line per day.

Both are generic. Nothing below is specific to government or healthcare. Two runnable examples show them end to end:

| Example | Shows |
|---|---|
| [`examples/gov-hierarchy`](../examples/gov-hierarchy) | Country → state → district → municipality. Officers are scoped to their jurisdiction, the service catalogue is inherited and overridden per unit and department, and applications are scoped automatically by the platform. |
| [`examples/medical-coding`](../examples/medical-coding) | Single- and multi-DOS encounters. Timely-filing, span and "no future date" rules; duplicate-billing detection; per-day claim lines saved atomically with their encounter; tenant isolation per billing client. |

The logic lives in two plain Go packages, which you can use without the platform:

- [`hierarchy`](../hierarchy): immutable org trees, scope coverage, and inherited lookups.
- [`dos`](../dos): civil `Date` and `Period`, rules, overlap and merge, per-day expansion, and month splitting.

---

## 1. Hierarchical organisations

### The `org.hierarchy` resource

```bcl
resource "org" {
  kind "org.hierarchy"
  config {
    levels ["country", "state", "district", "municipality"]  # top-down; optional
    strict_levels false        # true: a child must be exactly one level down
    database "db"              # optional: persist units and lookups (org_units / org_lookups)
    refresh_interval 30s       # optional: reload from the database (multi-replica)
    assignment_claim "org_units"  # principal claim listing the user's units
    global_roles ["admin"]        # roles that see the whole tree
    nodes [
      { id "np" level "country" name "Nepal" },
      { id "bagmati" parent "np" level "state" name "Bagmati Province" code "P3" }
    ]
    lookups [
      { set "service" code "tax" label "Property tax" },
      { set "service" code "tax" node "bagmati" label "Integrated property tax" }
    ]
  }
}
```

- **Assignment.** A user's units come from a claim: a string, a comma-separated list or an array. A user assigned to `bagmati` reaches Bagmati and everything below it. They can see the names of units above it (for breadcrumbs), but never a sibling province.
- **Levels.** When levels are set they are validated: a district cannot be created under a municipality. Without `strict_levels` a tier may be skipped.
- **Tenancy.** Units and lookups are stored per tenant. The empty tenant, which holds the inline config, is the shared tree. A tenant that writes gets its own tree.
- **Performance.** Reads use an immutable snapshot held in an atomic pointer, so a scope check costs a map lookup. Writes persist, then publish a new snapshot.

### Actions

| Action | Kind | What it does |
|---|---|---|
| `org.scope` | read | Publishes the caller's scope (`global`, `assigned`, `assigned_ids`, optionally every id). With `target "input.org_unit_id"` it returns 403 unless that unit is inside the scope, and adds the unit and its ancestors to the output. |
| `org.query` | read | `get`, `children`, `descendants`, `ancestors`, `tree` (nested, with `depth`), `level` (every unit of one tier) or `roots`. Results are scoped by default. |
| `org.write` | effect | `upsert`, `move` or `delete` (with `cascade`) a unit. A non-global user may change only units strictly inside its scope. It cannot edit its own scope root or create roots. |
| `lookup.resolve` | read | Returns the effective values of a lookup set for a unit, workspace and department. `require_code "input.service"` rejects a submitted value that is not one of them. |
| `lookup.write` | effect | Upserts or deletes a definition at a unit the caller administers. Global definitions require a global role. |

### Inherited reference data

A lookup entry is defined either globally or at a unit, optionally narrowed to a `workspace` and/or `department`. When a set is resolved for a context, the most specific definition of each code wins:

1. A deeper unit beats its ancestors, and a global definition ranks lowest.
2. On the same unit, a department match beats a workspace match, which beats neither.

A winning entry with `disabled true` removes the code for that subtree. This is how a district switches off a service the country offers. The workspace and department default to the `workspace` and `department` claims, and can be overridden with `workspace_fact` or `department_fact`.

### Row-level scoping with `database.crud`

```bcl
node "applications" {
  uses "database.crud"
  resource "db"
  config {
    operation "list"
    table "applications"
    columns ["id", "org_unit_id", "service", "status"]
    org_resource "org"          # the hierarchy
    org_column "org_unit_id"    # the row's unit
    org_path_column "org_path"  # optional, recommended for large trees
  }
}
```

- **Reads and changes to existing rows.** `list`, `get`, `update` and `delete` only reach rows whose unit is in the caller's subtree.
- **Checks on written units.** `create` and `update` reject a unit outside the subtree with 403.
- **Path column.** With `org_path_column`, the platform stamps each row with the unit's materialised path (`/np/bagmati/ktm/`) and scopes with one indexed `LIKE '/np/bagmati/%'` per assigned subtree. Without it, scoping uses an `IN (...)` list of every unit in scope, capped at 2000 units.
- **Composition.** Org scoping composes with `tenant_column`, `owner_column` and `soft_delete_column`.

> After a `move`, units in the `org_units` table get their new paths automatically. Rows in your own tables keep the path they were stamped with until they are next updated. If you move units in a table that uses `org_path_column`, re-stamp those rows (`UPDATE ... SET org_path = ...`) in the same maintenance window.

---

## 2. Dates of service

Every DOS action publishes a period in one shape, whatever the client sent (a single `dos`, a `dos_from`/`dos_to` pair, or a `"from..to"` string):

```json
{ "from": "2026-03-01", "to": "2026-03-03", "kind": "multi", "days": 3 }
```

The dates are civil dates. `2026-03-04` is the same date of service in every time zone and is never shifted by a timestamp conversion. Accepted input formats are `2006-01-02`, `01/02/2006`, `20060102`, `2006/01/02`, and the date part of an RFC 3339 timestamp.

| Action | What it does |
|---|---|
| `dos.period` | Normalises the input into a period. `dates true` also lists every date. `split_by_month true` also returns the period cut at month ends. |
| `dos.validate` | Checks the rules (below) and, with `lines "input.lines"`, that every line falls inside the encounter period and has non-negative units. It returns **every** violation in the error's `details` (422 `INVALID_DATE_OF_SERVICE`), or only reports them when `fail false`. |
| `dos.overlap` | Compares the period with existing rows (e.g. a `database.query` result) and returns 409 `DOS_OVERLAP`, naming the conflicting ids. Use `on_conflict "report"` to only report. `exclude_id` skips the record being edited. |
| `dos.expand` | Turns line items into one row per date. `per_day` repeats the units on every date. `distribute` spreads the total, with whole units and the remainder on the earliest dates. A line without its own date inherits the encounter period. |

Rules for `dos.validate`:

| Config | Meaning |
|---|---|
| `allow_multi` (default true) | Allow periods longer than one day |
| `max_span_days` | Longest allowed period |
| `allow_future` (default false) | Allow dates after "today" |
| `max_age_days` | Timely filing: reject service older than this |
| `same_month` | A period may not cross a month boundary |
| `weekdays [mon tue …]` | Only these days of the week are billable |
| `not_before "2026-01-01"` | Earliest allowed date (e.g. a contract start) |
| `timezone "America/Chicago"` | Decides what "today" is (default UTC) |

### Saving a header with its lines atomically

`database.insert_many` inserts a list of rows in one transaction. It splits the statements under the driver's bind-parameter limit, validates every identifier at load time and binds every value. With a `parent` block it first inserts the header row in the same transaction, then links every child row to it. This means an encounter never exists without its lines, and neither does an order without its items:

```bcl
node "saved" {
  uses "database.insert_many"
  resource "db"
  kind effect
  requires [input, period, valid, no_overlap, lines]
  provides [saved]
  config {
    parent {
      table "encounters"
      values { patient_id "input.patient_id" dos_from "period.from" dos_to "period.to" }
      key "id"              # parent key, returned and linked (default id)
      link "encounter_id"   # child column that receives it
      returning ["dos_from", "dos_to"]
    }
    table "encounter_lines"
    columns ["line_no", "dos", "cpt", "icd", "units"]
    rows "lines"            # e.g. dos.expand's output
    set { batch "input.batch_id" }  # optional per-row constants from facts
    tenant_column "tenant_id"       # stamped on parent and children
  }
}
```

The node publishes `{ "inserted": n, "parent": { "id": …, … } }`.

---

## 3. Error details

A 4xx failure may now carry a `details` array next to its code and message, for example every failed DOS rule or every overlapping encounter. 5xx responses still carry only a code and message.

```json
{ "error": { "code": "INVALID_DATE_OF_SERVICE",
             "message": "date of service 2026-10-01 is in the future (and 1 more)",
             "details": [ { "rule": "future", "line": -1, "message": "…" },
                          { "rule": "line_outside_period", "line": 0, "message": "…" } ] } }
```
