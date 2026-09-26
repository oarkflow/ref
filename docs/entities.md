# Entities: declarative data resources

An `entity` block declares a table and gets a complete, validated REST API. There is no intent or route to write. It is DAGFlow's `resource` (CRUD) block, extended with the following:
- filters and full-text-style search;
- sorting, pagination and totals;
- optimistic versioning;
- per-operation access rules with row conditions;
- tenant, owner and organisational scoping;
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
  default_sort "name"
  export true                   # GET /api/projects/-/export  (CSV)
  aggregate true                # GET /api/projects/-/aggregate

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

List and export take these query parameters:
- **Filters** by column: `col=v`, `col__ne`, `__gt`, `__gte`, `__lt`, `__lte`, `__in=a,b`, `__like` (substring) and `__null=true|false`. Unknown filters are a `422`, so a mistyped filter never silently returns everything.
- **Search:** `q=` searches the `search` columns.
- **Sorting:** `sort=-budget,name`.
- **Paging:** `limit`, `offset`.

## Access and scoping

**`allow` blocks** govern each operation: `list`, `get`, `create`, `update`, `delete`, `export`, `aggregate`, or `*` as the default.
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
