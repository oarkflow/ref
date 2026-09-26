# Entities: declarative data resources

An `entity` block declares a table and gets a complete, validated REST API. There is no intent or route to write. It is DAGFlow's `resource` (CRUD) block, extended with the following:
- filters and full-text-style search, with an optional token index;
- sorting, pagination and totals;
- optimistic versioning;
- per-operation access rules with row conditions;
- tenant, owner and organisational scoping;
- bulk creates, updates and deletes;
- CSV export and aggregates;
- post-commit hooks.

When the document loads, each entity is expanded into ordinary intents (`entity.<name>.<op>`) and routes, and its table is added to the database's migrations. It therefore composes with everything else:
- a flow or process step can call `entity.project.create`;
- a hand-written route can replace a generated one;
- a pipeline hook can update a record.

```bcl
entity "project" {
  database "db"                 # a database.sql resource
  path "/api/projects"          # default /api/<name>s
  auth "jwt"                    # route authenticator; allow_anonymous true for public reads
  tenant_scoped true            # stamps and filters tenant_id
  soft_delete true              # delete stamps deleted_at
  versioned true                # updates must send the version they read (409 otherwise)
  search ["name", "code"]       # ?q=
  search_index true             # ?q= by word prefix through a token index
  default_sort "name"
  export true                   # GET /api/projects/-/export  (CSV)
  aggregate true                # GET /api/projects/-/aggregate
  bulk true                     # POST /api/projects/-/bulk

  column "name"   { kind text  required true  min_length 2  max_length 100 }
  column "code"   { kind text  unique true  pattern "^[A-Z]{2,5}-[0-9]+$"  immutable true }
  column "status" { kind text  options ["draft", "active", "closed"]  default "draft" }
  column "budget" { kind number  min 0 }
  column "unit"   { kind text  index true }
  column "score"  { kind number  read_only true }      # set by hooks, never by clients
  column "notes"  { kind text  hidden true }            # stored, never returned

  allow "*"      { roles ["staff", "admin"] }
  allow "delete" { roles ["admin"] }
  allow "update" { roles ["staff"]  condition "record.status != 'closed'" }

  on "created" { hook "project.notify" }
  on "*"       { hook "project.audit" }
}
```

## Columns

| Attribute | Meaning |
|---|---|
| `kind` | `text` (default), `integer`, `number`, `decimal`, `boolean`, `date`, `datetime`, `email` or `json`. JSON values round-trip as structured data. `decimal` (with `scale`, default 2) is exact money: stored as integer minor units and returned as a string such as `"1250.50"`. |
| `required`, `min_length`, `max_length`, `min`, `max`, `pattern`, `options`, `default` | Validation and defaults. |
| `unique`, `index` | Indexes. Uniqueness is per tenant for tenant-scoped entities; a duplicate returns `409`. |
| `read_only` | Never writable by clients. |
| `immutable` | Settable on create only. |
| `hidden` | Stored but never returned, filtered or sorted on. |

The server manages these columns:
- **always:** `id`, `created_by`, `created_at` and `updated_at`;
- **when enabled:** `tenant_id`, `deleted_at` and `version`.

Writes are validated as a whole. Every problem is reported at once as a `422` with `details: [{path, rule, message}]`. That includes unknown fields, which are rejected rather than ignored, so a typo in a client is caught.

## API

| Method and path | Operation |
|---|---|
| `GET {path}` | `list`: returns `{items, total, limit, offset}` |
| `POST {path}` | `create`: returns `201` and the record |
| `GET {path}/:id` | `get` |
| `PATCH` or `PUT {path}/:id` | `update`: partial; send `version` when versioned |
| `DELETE {path}/:id` | `delete`: soft or hard |
| `GET {path}/-/export` | CSV: up to 10,000 rows, same filters; spreadsheet formula injection neutralised |
| `GET {path}/-/aggregate?group_by=status&agg=sum&field=budget` | `count`, `sum`, `avg`, `min` or `max`, same filters |
| `POST {path}/-/bulk` | many creates, updates and deletes in one request (see [Bulk operations](#bulk-operations)) |

List and export take these query parameters:
- **Filters** by column: `col=v`, `col__ne`, `__gt`, `__gte`, `__lt`, `__lte`, `__in=a,b`, `__like` (substring) and `__null=true|false`. Unknown filters are a `422`, so a mistyped filter never silently returns everything.
- **Search:** `q=` searches the `search` columns: a case-insensitive substring match, or a word-prefix match with `search_index true` (see [Search index](#search-index)).
- **Sorting:** `sort=-budget,name`.
- **Paging:** `limit`, `offset`.

## Bulk operations

`bulk true` adds `POST {path}/-/bulk`, which carries many changes in one request:

```json
{
  "create": [{"name": "Alpha", "code": "AL-1"}, {"name": "Beta", "code": "BE-2"}],
  "update": [{"id": "…", "version": 3, "status": "active"}],
  "delete": ["…", {"id": "…"}],
  "atomic": true
}
```

- Each item is validated and access-checked exactly as the single create, update or delete it stands for: the same `422` details, `version` rule, row conditions, scoping and org checks. The op-level `allow` rules (roles) of each op present apply to the whole request, so a caller who may not delete gets a `403` for a request with any delete.
- Items run in order: creates, then updates, then deletes. A record may appear only once among the updates and deletes.
- `bulk_max` (default 500) caps the items of a request.
- **`atomic` (default `true`)** is all or nothing. Every item is prepared inside one transaction, and if any is invalid, missing or forbidden, nothing is written. The response is an error with code `BULK_REJECTED`, whose status is that of the first failed item, and `details` lists every failed item as `{op, index, id, status, code, message, details}`. If a statement then fails, for example on a duplicate, that item is reported and the whole batch rolls back. Search tokens and durable hook events are written in the same transaction, so a rejected batch leaves neither behind. Plain hooks run after the commit.
- With **`atomic: false`**, each item commits on its own, as a single request would.

A successful response has the counts and one result per item, in execution order:

```json
{"atomic": true, "created": 2, "updated": 1, "deleted": 1, "failed": 0,
 "results": [{"op": "create", "index": 0, "id": "…", "ok": true, "record": {…}}, …]}
```

With `atomic: false`, a failed item has `ok: false` and the error fields instead of `record`.

## Search index

Without an index, `q=` scans the `search` columns with `LOWER(col) LIKE '%q%'`, which reads every row. Add `search_index true` to keep a token index instead:

```bcl
entity "place" {
  search ["name", "city"]
  search_index true
  ...
}
```

- The migration adds a `<table>_search (record_id, token)` table with an index on `token`.
- Each create, update and delete rewrites the record's tokens **in the same transaction as the change**, so the index never disagrees with a committed record. A soft delete drops the tokens.
- A token is a word of the search columns: compatibility-decomposed, accents dropped, lower-cased (`ß` becomes `ss`), split on anything that is not a letter or digit. Words are cut at 64 characters, and a record indexes at most 512 distinct words.
- `q=` is tokenised the same way (at most 8 words). **Every** query word must be the prefix of some word of the row: `q=bri rep` finds "Bridge repair", `q=zurich` finds "Zürich", and `q=ridge` finds nothing. A query with no letters or digits does not filter.
- The match is an indexed `token LIKE 'word%'` on SQLite, PostgreSQL and MySQL.

When the application starts and a token table is empty (because the index was just turned on for an existing table, for example), every live record is indexed in the background. To rebuild on demand, for example after rows were changed with raw SQL, use an `entity.reindex` node:

```bcl
intent "places.reindex" {
  response "r"
  node "r" {
    uses "entity.reindex"
    resource "db"          # the entity's database
    kind effect
    provides [r]
    config {
      entity "place"
      roles ["ops"]        # optional: only these roles may call it
    }
  }
}
```

It re-derives the tokens of every live record, a page per transaction, drops tokens of deleted records and returns `{entity, indexed}`.

## Access and scoping

**`allow` blocks** govern each operation: `list`, `get`, `create`, `update`, `delete`, `export`, `aggregate`, or `*` as the default. A bulk request has no block of its own: each item is checked as the create, update or delete it is.
- An op's own blocks **replace** the `*` blocks.
- With no `allow` blocks at all, every operation is open to whoever passes the route's authentication.
- With some blocks, an op without a matching block is denied.
- `roles` restricts a block to holders of those roles.
- `condition` is an expression over `record` (and `principal`, `input`, …). It is checked against the stored row for get, update and delete, and against the submitted row for create. A get that fails its condition reads as `404`, so the record's existence is not disclosed.

**Scoping** is applied to every query, count, export and aggregate:
- `tenant_scoped`: rows of the caller's tenant only. The tenant is stamped on create.
- `owner_scoped`: rows the caller created, unless they hold one of `owner_bypass_roles`.
- `org_resource` + `org_column`: rows inside the caller's organisational units (an `org.hierarchy` resource). Writes outside the caller's units are refused.

## Hooks

`on "created" | "updated" | "deleted" | "*" { hook "<intent>" }` runs an intent **after** the change is committed, with `{entity, event, record, previous}` as its input. A failing hook is logged; it never undoes the change.

That plain hook runs once, in the request. If it fails, or the process dies first, it is lost. For anything that must happen, such as syncing to an ERP, sending a receipt or updating a ledger, mark the hook `durable`:

```bcl
on "created" {
  hook "invoice.sync"
  durable true
  max_attempts 8      # default 10
  retry_base "5s"     # doubling up to 10m; default 2s
}
```

A durable hook is written to a `ref_entity_events` table **in the same transaction as the change**. So a committed change always has its hook, and a change that rolled back (a duplicate or a version conflict, for example) never does. The migration creates the table on the entity's database.

A background dispatcher then calls the hook intent. Its input is `{entity, event, record, previous, actor, tenant_id, event_id, attempt}`. On failure it retries with backoff. After `max_attempts` failures the event is dead-lettered. Replicas share the table: a claimed event is leased for a minute, so each event goes to one replica at a time.

Delivery is **at least once**, so make the hook idempotent on `event_id`, for example with `INSERT … ON CONFLICT (event_id) DO NOTHING`.

To operate the outbox, use an `entity.events` node on the database:
- By default it lists dead-lettered events: `{dead: [...]}`, each with its input, attempts and last error.
- With op `requeue` (from config, a `:op` path parameter or `?op=`) it gives the event `:event_id` a fresh set of attempts.
- `entity` limits it to one entity, and `roles` restricts who may call it.

## Raw responses

The CSV export uses a general platform mechanism: any action may return a `platform.RawResponse{ContentType, Filename, Body}`. A route then sends those bytes as-is, with the given content type and a download filename, instead of JSON. PDF certificates, images and reports use the same path.
