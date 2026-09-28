# Configuration reference

Everything below is scoped to what this starter actually uses. For the full
catalog of resource kinds, node actions and route fields, see
`docs/*.md` and `platform/catalog_builtins.go` at the repository root — there
is no single generated reference for all of it yet.

## Resources (`bcl/01_resources.bcl`)

| Resource | Kind | Key config | Why this variant |
|---|---|---|---|
| `database` | `database.sql` | `driver`, `dsn`, `ping` (defaults **true**) | SQLite for development, PostgreSQL (`pgx`) in production — see the README's "Switching to PostgreSQL". `ping true` makes an unreachable database a startup failure, not a surprise on the first request. |
| `cache` | `cache.sql` | `database` | Shared across replicas. `cache.memory` would be per-process — correct on your laptop, silently wrong the moment you run two instances. |
| `limits` | `ratelimit.store` | `cache` | Layers over `cache`, so a rate limit is shared, not reset per replica. |
| `jobs` | `queue.sql` | `database`, `workers`, `max_attempts` | Durable and multi-replica-safe; `queue.file` would only be visible to processes sharing a filesystem. |
| `sessions` | `session.sql` | `database`, `secret`, `cookie`, `max_age`, `secure` | Signed session cookies. `session.sql`, not `session.file`, so every replica behind a load balancer sees the same session. |
| `session_auth` | `auth.session` | `session`, `roles_claim` | Resolves the caller's principal (id, roles) from the cookie `sessions` verified. Every protected route names this as its `auth`. |
| `authorization` | `authz.rbac` | `superuser_roles`, `default_roles` | The RBAC engine every route's `authz { authorizer "authorization" }` checks against. **`superuser_roles` does not bypass a route's `roles [...]` gate** — see below. |
| `notifications` | `service.http` | `allowed_hosts`, `allow_private_networks` | Outbound target for `notify.welcome`. The host allowlist is an SSRF guard: a URL outside it is refused, not attempted. |

## Schema migrations (`migrations/*.bcl`, `cmd/migrator`)

`AUTOINCREMENT` (SQLite) and `SERIAL` (PostgreSQL) have no spelling both
accept, so schema isn't declared inline on the `database` resource at all —
it's owned by `migrations/*.bcl`, applied with a separate binary
(`go run ./cmd/migrator cli migrate`, once per schema change, before
starting the server) built on `github.com/oarkflow/migrate`, which compiles
one declarative `Migration { Up { CreateTable ... } }` block per dialect's
correct DDL. `bcl/01_resources.bcl`'s `database` resource only has
`driver`/`dsn` — no `migrations` list — because the tool, not the app
process, owns table creation.

Two things worth knowing about `oarkflow/migrate` (pin at least v0.0.27),
found while wiring this up — confirmed by reading the generated DDL
directly with `sqlite3`, not just by reading the docs:

- **v0.0.26 briefly validated `Field` names against a reserved-SQL-keyword
  list and rejected `action`** (it appears in `... ON DELETE ACTION`-style
  FK grammar) — reported upstream as a false positive, since `action` is an
  ordinary, unreserved column identifier in every dialect this tool
  targets. **Fixed in v0.0.27**: that check was removed from identifier
  validation (kept only as an opt-in `IsReservedKeyword` helper for callers
  who want it), so the audit log table's event-type column is named
  `action` again, as originally intended.
- **SQLite's rowid-alias auto-increment does *not* require an inline
  `id INTEGER PRIMARY KEY` column declaration** — a table-level
  `PRIMARY KEY ("id")` constraint on a single `INTEGER`-typed column (which
  is exactly what `oarkflow/migrate` emits for
  `Field "id" { type = "integer" primary_key = true auto_increment = true }`)
  gets the same treatment per SQLite's own docs. `auto_increment` on a
  SQLite integer primary key works correctly in this tool as-is; no
  app-side id generation workaround is needed.

`cmd/migrator/main.go` translates this starter's `DB_DRIVER` value
(`"pgx"`, matching `database/sql`'s driver name) to the dialect name
`oarkflow/migrate` expects (`"postgres"`) — the two libraries name
PostgreSQL differently on purpose (driver name vs. dialect family), so this
translation is intentional, not a workaround.

If you add your own dialect-sensitive DDL, verify it the same way this
schema was: `docker run -e POSTGRES_PASSWORD=... -p 5432:5432 postgres:16-alpine`,
then `DB_DRIVER=pgx DB_DSN=postgres://...` and an actual
`go run ./cmd/migrator cli migrate`, not just a read of the BCL.

## Route groups (`bcl/04_routes.bcl`)

```bcl
route_group "api" {
  prefix "/api/v1"
  session "sessions"
  auth "session_auth"
  authz { roles ["user"] authorizer "authorization" }
  cache_control "no-store"

  route "me" { method GET path "/me" intent "api.me" }               # inherits the group default
  route "users" { method GET path "/users" intent "api.users" authz { roles ["admin"] authorizer "authorization" } }  # overrides it
}
```

A route nested in a group inherits `session`/`auth`/`authz`/`rate_limit`/
`cache_control` from the group **only for fields it leaves unset**; its own
value always wins. `expandRouteGroups` (`platform/route_group.go`) turns
every nested route into an ordinary `RouteSpec` — with its path already
joined to the group's `prefix` — before the compiler, the validator or the
mounter ever run, so a group is an authoring convenience, not a separate
runtime concept. `allow_anonymous` is never inherited: a route that needs it
(a login endpoint inside an otherwise-authenticated group, say) must set it
itself, so a group can never make a route more open than the route asked to
be.

## RBAC: what `superuser_roles` actually does

This tripped us up building this starter, so it's worth stating plainly:
**`superuser_roles` on an `authz.rbac` resource does not make that role pass
every `authz { roles [...] }` check for free.** A route's `roles [...]` is a
literal membership check — it lists every role allowed to reach that route.
If you want "admin" to reach a page that also allows "user", write
`roles ["user", "admin"]` explicitly, the way `bcl/04_routes.bcl`'s
`web.dashboard` route does. This starter's own `/dashboard` briefly had this
exact bug (`roles ["user"]` only) while it was being built — an admin
account got 403 there until the role list was made explicit.

## Business logic (`bcl/03_intents.bcl` → routes/workers/schedules/triggers)

Add a feature by adding an `intent` and, if it needs one, a `route`. To
reuse it from a background job add a `worker` (consumes `queue.publish`
jobs by `job_type`) or a `schedule` (fires on a cadence); to reach it from
outside add a `trigger` (an inbound webhook, HMAC-verified). `notify.welcome`
uses all four — read it as the template.

A node's `bulkhead { name "..." limit N }` caps concurrent calls to that
node; every node across every intent naming the same `name` shares one
limiter. Use it for a slow or flaky external dependency; it fails fast
(`BULKHEAD_FULL`, HTTP 503) rather than queuing unboundedly.

## Pages (`templates/`)

A route with `template`/`layout` set renders through `internal/web`'s SPL
adapter instead of returning JSON. The intent's response *is* the template's
data — `@extends`/`@define`/`@include`/`@if`/`@for`/`${...}` read directly
from whatever facts the intent's `collect` node returned, plus the
`Globals` fallbacks in `internal/web/renderer.go` for fields no intent
supplied (so `components/navbar.html`, included on every page, still
renders sanely on a public page that has no `user` fact at all).

**One SPL gotcha worth knowing**: an HTML comment containing a literal `{`
and `}` (e.g. `<!-- see authz {roles [...]} -->`) can confuse the block
parser into reporting an unrelated `unclosed block` error elsewhere in the
same file. Keep explanatory prose out of inline template comments; put it
in this file or the README instead.

**Another one**: `c.Accepts(...)` (used by the maintenance middleware below
to choose HTML vs JSON) matches offers against Accept header tokens
literally — offer `"text/html"`, not the bare word `"html"`; the latter
silently never matches and you always get the JSON branch.

## Maintenance mode (`internal/ops/maintenance.go`, `bcl/10_maintenance.bcl`)

A process-wide `atomic.Bool` + `atomic.Pointer[string]`, behind three
consumers: `app.Use(gate.Middleware())` in `cmd/server/main.go` (must run
before `p.Mount`, so it sees a request before any BCL route does),
`healthRegistry.Register("maintenance", health.Simple(gate.HealthCheck))`
(readiness only — the process itself is fine, so `/livez` stays up), and
`gate.RegisterAction()` (installs `ops.maintenance_set`, which
`bcl/10_maintenance.bcl`'s admin route calls).

Exempt paths (`internal/ops.exemptPrefixes`) must include the toggle route
itself — `/api/v1/admin/maintenance` — or an admin who turns maintenance on
can never reach the route that turns it back off without a restart. If you
move that route, update the prefix list to match.

`MaintenancePage`/`MaintenanceLayout` (package-level vars, not consts) name
the SPL template the HTML branch renders — repoint them in `main()` if you
reorganise `templates/`.

## Environments (`bcl/11_environments.bcl`)

`profile "production" { override "resource.sessions" { secure "true" } }`
merges only the listed keys into that resource's existing config; anything
`bcl/01_resources.bcl` already set that the profile doesn't mention is
untouched. It applies only when `platform.LoadOptions.Profile` equals the
profile's name — `cmd/server/main.go` sets `opts.Profile = boot.Env`, so
this fires under `APP_ENV=production` and nothing else. There's no
`*.production.bcl` filename convention; `LoadDir` loads every `*.bcl` file
unconditionally and profile blocks decide what actually applies once parsed.

## Plugin points: `RegisterActionDriver` / `RegisterResourceDriver` / `Observers`

Three real extension points, all Go-level, all process-wide (not
per-`Platform`):

- **`platform.RegisterActionDriver(name, factory, info...)`** — adds a new
  BCL-reachable node action. `internal/ops.MaintenanceGate.RegisterAction`
  is the worked example. **Must run before the `Registry` is constructed**
  — before `platform.DefaultLoadOptions()` (which calls `NewRegistry()`
  internally), or before your own `platform.NewRegistry()` call if you
  don't use the default. Calling it after `LoadDir` compiled once, or even
  just after `DefaultLoadOptions()` ran, silently does nothing until the
  *next* registry is built — the failure is `uses unregistered action
  "..."` at compile time, which looks like a typo, not an ordering bug.
- **`platform.RegisterResourceDriver(kind, factory, info...)`** — adds a new
  resource kind (a Redis cache, a Kafka queue, ...), same ordering rule.
  Nothing in this starter uses it yet; see `platform/spi/spi.go` at the
  repository root for the interfaces a driver typically implements.
- **`platform.LoadOptions.Observers []observer.Observer`** — DAG execution
  telemetry (node/decision/effect/execution events), set directly on the
  options struct (no global registration, no ordering trap). This starter
  wires exactly one: `observer/slog` fed by the same `zlog.Logger`
  everything else logs through.

For logging specifically, `cmd/server/logging.go`'s `LOG_WEBHOOK_URL`
plugs into `zlog.NewMultiSink`, one level below all three of the above —
see the README's "Maintenance mode, environments, and plugging in a
third-party tool".

## Misconfiguration hints

Every one of these is caught before the server accepts a request — most at
`go run` (a startup failure), a few at `go test` (via `platform.Validate`,
which runs the same checks without opening a database or a listener).

| You did | You get | Fix |
|---|---|---|
| A `route_group` with no nested routes | `ref/platform: route_group "x" declares no routes` | Add at least one `route` block, or remove the group. |
| Two routes (in or out of a group) end up with the same name | `ref/platform: route_group "x": duplicate route "y"` | Rename one. Route names must be unique across the whole document, groups included. |
| A node's `bulkhead { limit 0 }` (or negative) | `intent "x" node "y" bulkhead: limit must be at least 1, got 0` | Use a positive limit. |
| Ran two replicas (`REPLICA_ID` set, or any `process` block declared) with a `cache.memory`/`lock.memory`/`ratelimit.memory` resource | A startup warning: `resource "x" is process-local (...): ...; two replicas will not share its state — use ... instead` | Switch to the `.sql`/`.store` variant, as this starter already does by default. |
| A typo in a node's `uses` | `ref/platform: intent "x" node "y" uses unregistered action "z". Registered actions: ...` (the full list follows) | Check the spelling against the printed list. |
| A node's `resource` names something not declared | `ref/platform: intent "x" node "y" references undeclared resource "z"` | Add the `resource` block, or fix the name. |
| Two nodes both `provides` the same fact | `ref/platform: intent "x" fact "y" is provided by both "a" and "b" — a fact needs exactly one producer` | Rename one, or remove the duplicate node. |
| A node `requires` a fact nothing provides | `ref/platform: intent "x" node "y" requires fact "z", which no node provides` | Check for a typo, or add the producing node. |
| A required secret unresolved at `go run` | `ref/platform: secret "x" is required but could not be resolved (env "Y", file "")` | Export the environment variable. At `platform.Validate` time (e.g. in CI) the same condition is a warning, not an error — deliberately, so review doesn't require production secrets. |
| An `authz` block with no `roles`/`permissions`/`condition`, or no `auth`/`session` resource on the route | Caught at `platform.Validate`/`Compile`, naming the route | Add at least one rule, and an `auth` (or `session`) resource for it to check against. |
| A webhook `trigger` with no `secret` | Refused at compile time — an unauthenticated public mutation endpoint is never the intent | Declare a `secret` block (see `bcl/00_app.bcl`'s `webhook_secret`) and reference it. |
| `roles [...]` on a route excludes a role you expected to pass via `superuser_roles` | No error — a silent 403 at request time | See the RBAC section above; list the role explicitly. |
| `RegisterActionDriver`/`RegisterResourceDriver` called after `platform.DefaultLoadOptions()` (or any `NewRegistry()`) already ran | `uses unregistered action "..."`/`uses unregistered kind "..."` at compile time — reads like a typo | Register before the `Registry` is constructed; see "Plugin points" above. |
| The maintenance toggle route's own path isn't in `internal/ops.exemptPrefixes` | Turning maintenance on locks out the route that turns it back off; only a restart (or editing the env var) recovers | Keep the two in sync, or don't move the route. |
| `.env` sets a variable that's already set in the real environment | No error — the real value silently wins, `.env`'s is ignored | Expected behavior (`cmd/server/dotenv.go`), not a bug; unset the real one or edit it directly if you meant to override it locally. |
| A BCL field does `env(...) == "x" ? A : B` all inline | No error — silently picks the wrong branch | Bind the `env()` call to its own field first, then compare that field, e.g. `db_driver env(...)` then `db_driver == "x" ? A : B`. A real parser quirk in the pinned `github.com/oarkflow/bcl` version, found while this starter still branched migrations by dialect in BCL (since replaced by `cmd/migrator` — see "Schema migrations" above — but the parser behavior itself is unchanged). |
| A BCL field is a bare reference to another top-level field, inside a nested block or a list element | No error — the field's own *name* becomes its string value | Bare references only resolve reliably as a ternary's condition at the top level; use `env(...)` directly anywhere else. |

## What's deliberately not here

Documented, tested, and available in the wider `ref` repository, but not
wired into this starter because it doesn't belong in every product by
default:

- **`entity` blocks** (`docs/entities.md`) — a full CRUD REST resource from
  one declarative block, including tenant/owner scoping, soft delete,
  optimistic versioning and CSV export. See the commented example at the
  bottom of `bcl/03_intents.bcl`.
- **JWT bearer tokens** (`auth.jwt`, `identity.users`) — a separate,
  independent auth stack from the session cookies this starter uses, with
  its own hardened user directory (tenants, memberships, invitations,
  lockout, audit trail). Reach for it instead of (not alongside — they don't
  share a principal store) session cookies for a mobile app or a public API
  with no browser client. See `docs/identity-and-signing.md` and
  `examples/identity`.
- **Feature flags, exact-decimal/money columns, and data residency**
  (`docs/flags-and-documents.md`, `docs/localization-and-money.md`).
- **Hierarchical organisations and dates-of-service** (`docs/app-patterns.md`)
  — vertical patterns (government, healthcare, field service) that don't
  apply to most products.
- **The config-revision lifecycle** (`docs/deploy.md`) — validate → diff →
  four-eyes approve → sign → zero-downtime activate → rollback. Worth
  adopting once more than one person edits `bcl/` in production.
- **The distributed circuit breaker in `capability/`** — this starter's
  `ratelimit.store`/`cache.sql` cover the common case declaratively; the
  `capability.NewRedisCircuitBreakerCapability`/`NewInMemoryCircuitBreakerCapability`
  helpers exist for a Go-registered SPI adapter calling a genuinely external,
  flaky service, which nothing in this starter does yet.
