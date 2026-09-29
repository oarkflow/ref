# Configuration reference

Everything below is scoped to what this starter actually uses. For the full
catalog of resource kinds, node actions and route fields, see
`docs/*.md` and `platform/catalog_builtins.go` at the repository root — there
is no single generated reference for all of it yet.

## Resources (`resources/config/01_resources.bcl`)

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
| `guard` | `security.tcpguard` | `path`, `mode`, `identity_fields` | Network/API abuse-detection perimeter guard — see "Network protection and business rules" below. |
| `policy_engine` | `rules.engine` | `dir` | Versioned business-rules engine — see the same section. |
| `delivery_breaker` | `circuit_breaker.store` | `cache`, `failure_threshold`, `reset_after` | Trips after 5 consecutive `notify.welcome`/`notification.password_reset` delivery failures; see "Circuit breaker" below. |

## Schema migrations (`resources/migrations/*.bcl`, `cmd/migrator`)

`AUTOINCREMENT` (SQLite) and `SERIAL` (PostgreSQL) have no spelling both
accept, so schema isn't declared inline on the `database` resource at all —
it's owned by `resources/migrations/*.bcl`, applied with a separate binary
(`go run ./cmd/migrator cli migrate`, once per schema change, before
starting the server) built on `github.com/oarkflow/migrate`, which compiles
one declarative `Migration { Up { CreateTable ... } }` block per dialect's
correct DDL. `resources/config/01_resources.bcl`'s `database` resource only has
`driver`/`dsn` — no `migrations` list — because the tool, not the app
process, owns table creation.

### `go run ./cmd/server` checks on every boot too

You don't strictly have to run the migrator first. `cmd/server/main.go`'s
`ensureMigrated` (`cmd/server/migrate.go`) checks `resources/migrations/*.bcl`
against the database it's about to open, every time it starts — a fresh
checkout used to fail here with a raw `no such table: users` from the
seeding step deep inside `LoadDir`; now it turns into one of three clear
outcomes:

- **An interactive terminal** (you, at a shell): it lists what's pending and
  asks — `4 pending migration(s) (1_create_users_table, ...). Run them now?
  [y/N]:` — and only touches the database on `y`/`yes`. Anything else
  refuses to boot, naming `go run ./cmd/migrator cli migrate` as the manual
  alternative.
- **`AUTO_MIGRATE=true`** (or `1`): applies them without asking — the shape
  a container entrypoint or CI job wants, where nothing can answer a
  prompt. Set it and `go run ./cmd/server` alone is enough for a fresh
  checkout, migration included.
- **Neither** (a service manager with no attached terminal, `AUTO_MIGRATE`
  unset): refuses to boot with a message naming exactly what's pending and
  what to run — never a raw driver error.

This check is read-only when nothing is pending (which is the common
case — an already-migrated database boots exactly as fast as before, no
prompt, no delay) and, when it does apply something, goes through the same
idempotent `Manager.ApplyMigration` the CLI itself uses
(`internal/migrations.Apply`) — so it can never re-apply what's already
there, and calling it on every boot is safe by construction, not just by
convention. It is deliberately narrower than the migrator binary itself:
forward-only, no rollback, no `--force`, no seeding — reach for
`go run ./cmd/migrator` directly for any of those.

`MIGRATIONS_DIR`/`MIGRATIONS_SEED_DIR` override where it looks, the same
way `SECRETS_DIR` overrides the secrets-file lookup (see "Secrets in
production" below) — useful for a nonstandard layout, or a test that can't
rely on the default resolution matching its own working directory.

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

## Route groups (`resources/config/04_routes.bcl`)

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
`roles ["user", "admin"]` explicitly, the way `resources/config/04_routes.bcl`'s
`web.dashboard` route does. This starter's own `/dashboard` briefly had this
exact bug (`roles ["user"]` only) while it was being built — an admin
account got 403 there until the role list was made explicit.

## Business logic (`resources/config/03_intents.bcl` → routes/workers/schedules/triggers)

Add a feature by adding an `intent` and, if it needs one, a `route`. To
reuse it from a background job add a `worker` (consumes `queue.publish`
jobs by `job_type`) or a `schedule` (fires on a cadence); to reach it from
outside add a `trigger` (an inbound webhook, HMAC-verified). `notify.welcome`
uses all four — read it as the template.

A node's `bulkhead { name "..." limit N }` caps concurrent calls to that
node; every node across every intent naming the same `name` shares one
limiter. Use it for a slow or flaky external dependency; it fails fast
(`BULKHEAD_FULL`, HTTP 503) rather than queuing unboundedly.

## Network protection and business rules (`resources/security/`, `resources/rules/`)

Two capabilities live in core `ref/platform`, not in this starter's own Go
code — declaring the resource is the whole integration, the same as every
other resource kind in this document.

**`security.tcpguard`** (`guard` resource, `resources/security/tcpguard.bcl`,
`platform/resources_security.go`) is a deployment-wide perimeter guard —
credential-stuffing/endpoint-scanning/injection-shape abuse detection with
graduated allow/monitor/challenge/throttle/block decisions
(github.com/oarkflow/tcpguard), evaluated on *every* request before
authentication runs. This is a different layer from a route's own
`rate_limit`: `rate_limit` counts one route by a fixed window;
`security.tcpguard` inspects the whole request's shape across every route
against a policy pack written in tcpguard's own BCL dialect (not this
one). `path` (`resources/config/01_resources.bcl`) names a *directory*, not a file:
`tcpguard.LoadTCPGuardBundleDir` walks it recursively, merging every
`.bcl` file it finds — `resources/security/` splits the pack/guard/
policy_safety declaration (`resources/security/tcpguard.bcl`) from each
rule (`resources/security/rules/*.bcl`) into a nested subdirectory, the
way a real policy pack (tcpguard's own examples ship one file per
detector/rule/threat_model) actually organizes itself.

`resources/security/rules/` ships tcpguard's **full reference-pack rule
catalog** — 8 rules, one `status` each. `status` is a literal
(`active`/`paused`/...); `env(...)` does **not** work inside it — confirmed
empirically (a scratch core test with a live `env()`-driven `status` field
never flipped the effective decision) — so there is no runtime toggle
short of editing the file and redeploying.

| Rule file | Status | Why |
|---|---|---|
| `application-attack-probe.bcl` | `active` | XSS/SQLi/path-traversal/SSRF shape detection needs only the request itself — no extra wiring. |
| `auth-abuse-velocity.bcl` | `active` | Brute-force login detection — needs `security_event` (below) and `identity_fields` to know a login failed; both are wired. |
| `account-takeover.bcl` | `paused` | Needs a session-continuity/GeoIP `Enricher`; no config surface for one yet. |
| `api-key-abuse.bcl` (2 rules) | `paused` | Needs `X-API-Key`-authenticated traffic; this starter only has session cookies. |
| `data-export-abuse.bcl` | `paused` | Needs a bulk-export/download endpoint; `GET /api/v1/users` returns a small fixed page, not an export. |
| `function-invocation-abuse.bcl` | `paused` | Needs a FaaS-style "invoke this named function" surface; every route here is its own fixed intent. |
| `payment-fraud.bcl` (2 rules) | `paused` | Needs a `BusinessExtractor` parsing a payment amount off the request; `orders.create`'s `amount` field is a distant analog, not payment processing. |
| `destructive-admin-abuse.bcl` | `paused` | Needs a "destructive"/"outside business hours" admin action; the one admin action here (maintenance toggle) is reversible and has no business-hours concept. |

Each `paused` file's own comment states exactly what integration point is
missing and what shape to build to flip it `active` — a rule that looks
real but can never fire is worse than not shipping it, so nothing here is
silent dead weight.

**Reporting an outcome the automatic per-request check can't see.** The
guard's automatic check (every route gets it for free) only ever evaluates
`request.received` — it cannot know whether *this* login will succeed,
because that isn't decided yet. A route's `security_event { on_success
"..." on_failure "..." }` (`resources/config/04_routes.bcl`'s
`web.login_action`) reports the real outcome once it's known, via
`c.OnBeforeResponse` (`platform/routes.go`) calling the `guard` resource's
`report()` method — this is what actually increments
`abuse.auth.ip_failures`/`.user_failures` and lets `auth-abuse-velocity`
fire. `identity_fields ["email"]` (the `guard` resource's config) tells the
guard how to read an account identifier out of a JSON request body
(`platform/resources_security.go`'s `jsonIdentityExtractor`) so
`.user_failures` tracks a specific account, not just an IP — without it,
`.user_failures` stays permanently zero and only the IP-keyed signal can
ever fire. Both the automatic check and `security_event` reports share the
same identity extraction, so a rule never sees a different account
identity depending on which path triggered it.

**`rules.engine`** (`policy_engine` resource, `resources/rules/orders/policy.bcl`,
core `platform/resources_rules_engine.go`) is a second decision engine
(github.com/oarkflow/rules) alongside the built-in `decision.table` —
reach for it, as `orders.create`'s `policy` node does, when a business
rule needs independent versioning/rollback (`rules.Service.Activate` /
`Rollback`) on its own schedule, not `decision.table`'s (redeployed with
the rest of the application document). `dir "./resources/rules"`
(`resources/config/01_resources.bcl`) scans recursively and publishes one
definition per `.bcl` file, named by its path relative to `dir` —
`orders/policy.bcl` becomes definition `"orders.policy"`, which is what
the `policy` node's `config { definition "orders.policy" }` names. A file
whose name starts with `_` (`resources/rules/orders/_schema.bcl`) is a
fragment, not a definition of its own — `policy.bcl` pulls it in with
`import "./_schema.bcl"` (`github.com/oarkflow/bcl`'s own directive, not
this dialect's); a large ruleset splits into fragments the same way, and
`dir`'s job is finding every independent definition, not composing one
across files — `import` does that, inside whichever file needs it.
`resources/rules/orders/policy.bcl` is a different BCL dialect too (bare
`decision_schema`/`decision_table` blocks — no `module` wrapper needed —
not `resource`/`intent`/`route`) — see that file's own comment for why it
exists as a second layer next to `orders.create`'s existing
`flow.branch`/`decision.table` rather than folded into them.

A `rules.evaluate` node with `kind decision` denies the same way
`auth.require_session` does — `!Allowed` fails the node, and it must
`requires` **only the facts the rule itself reads**, never a fact that
another plain node needs to produce first: a plain node without
`speculation` cannot run until every decision node in the intent has
resolved (`execution/scheduler.go`'s `isGateEligible`), so a decision node
requiring that plain node's output deadlocks the whole intent — every
request denied, generically, with no node past the deadlock ever running.
`orders.create`'s `policy` node hit exactly this requiring `[input,
validated]` instead of `[input]`; its own comment is the postmortem.

## Circuit breaker (`delivery_breaker` resource, `notify.welcome` / `notification.password_reset`)

`circuit_breaker.store`/`.guard`/`.record` (core `platform/resources_coord.go`,
`platform/actions_coord.go`) is a pre-existing, previously-unused core
capability — a consecutive-failure-threshold + reset-window + half-open-probe
breaker, backed by the same `cache` resource everything else shares (so it's
multi-replica-consistent, not per-process). `01_resources.bcl`'s
`delivery_breaker` resource sets `failure_threshold 5 reset_after 30s`. Both
async-delivery intents insert the same four-node chain between validation and
the final `response` node:

```text
breaker-guard (circuit_breaker.guard, key "'notifications'")
  -> deliver (service.http, on_error "continue", fallback { delivery { ok false } })
  -> delivery-ok (expression: "delivery.ok != false")
  -> breaker-record (circuit_breaker.record, key "'notifications'", success_fact "delivery_ok")
  -> delivery-succeeded (validate.expression: "delivery_ok == true")
```

**Why `deliver` needs `on_error "continue"` *and* a separate
`delivery-succeeded` re-check, rather than just letting the delivery failure
propagate directly**: `circuit_breaker.record` must run (and see the real
outcome) on every attempt, including a failed one, or the breaker never
learns anything and never opens. `on_error "continue"` lets the intent keep
running past a failed delivery so `breaker-record` gets to see it — but that
node config alone would make the *whole intent* report success even when
delivery genuinely failed, silently breaking a worker's retry-on-failure
behavior. `delivery-ok` → `breaker-record` → `delivery-succeeded` closes that
gap: the breaker observes every outcome, but the intent's own final status
still reflects reality — `INVALID_INPUT` (422, worker retries) for a real
delivery failure below the breaker's threshold, and a distinct
`UNAVAILABLE` (503) once the breaker itself is open. Verified live against
both codes while building this, not just read for plausibility. Copy this
exact node chain, not just the `circuit_breaker.guard`/`.record` actions in
isolation, when wiring a breaker around a new flaky dependency.

## Tracing (`internal/telemetry`, `OTEL_EXPORTER_OTLP_ENDPOINT`)

Zero-cost when unset: `cmd/server/main.go` only builds an OTel
`TracerProvider` (`internal/telemetry.NewTracerProvider`, OTLP/HTTP
exporter) and appends `otelobserver.New(tp.Tracer(...))`
(core `observer/otel`, another pre-existing, previously-unused capability)
to `platform.LoadOptions.Observers` when `OTEL_EXPORTER_OTLP_ENDPOINT` is
set — no collector configured means no exporter, no extra goroutine, no
behavior change from today. Set it to a running OTLP/HTTP collector
(`http://localhost:4318` for a local Jaeger/Tempo/Collector) to get one span
per DAG node execution (decision/effect/retry/timeout/bulkhead), alongside
whatever `zlog`/`LOG_WEBHOOK_URL` already ships — see `observer/otel`'s own
doc comment for its one documented limitation (no incoming-context
linkage: a trace starts fresh per intent execution, it does not continue a
trace ID from an inbound request header yet). `APP_VERSION`/`REPLICA_ID`
(already-existing `Bootstrap` fields) tag every span's resource attributes
so traces from different replicas or releases are distinguishable in the
collector.

## Pages (`resources/templates/`)

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

## Maintenance mode (`internal/ops/maintenance.go`, `resources/config/10_maintenance.bcl`)

A process-wide `atomic.Bool` + `atomic.Pointer[string]`, behind three
consumers: `app.Use(gate.Middleware())` in `cmd/server/main.go` (must run
before `p.Mount`, so it sees a request before any BCL route does),
`healthRegistry.Register("maintenance", health.Simple(gate.HealthCheck))`
(readiness only — the process itself is fine, so `/livez` stays up), and
`gate.RegisterAction()` (installs `ops.maintenance_set`, which
`resources/config/10_maintenance.bcl`'s admin route calls).

Exempt paths (`internal/ops.exemptPrefixes`) must include the toggle route
itself — `/api/v1/admin/maintenance` — or an admin who turns maintenance on
can never reach the route that turns it back off without a restart. If you
move that route, update the prefix list to match.

`MaintenancePage`/`MaintenanceLayout` (package-level vars, not consts) name
the SPL template the HTML branch renders — repoint them in `main()` if you
reorganise `resources/templates/`.

## Environments (`resources/config/11_environments.bcl`)

`profile "production" { override "resource.sessions" { secure "true" } }`
merges only the listed keys into that resource's existing config; anything
`resources/config/01_resources.bcl` already set that the profile doesn't mention is
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
| A webhook `trigger` with no `secret` | Refused at compile time — an unauthenticated public mutation endpoint is never the intent | Declare a `secret` block (see `resources/config/00_app.bcl`'s `webhook_secret`) and reference it. |
| `roles [...]` on a route excludes a role you expected to pass via `superuser_roles` | No error — a silent 403 at request time | See the RBAC section above; list the role explicitly. |
| `RegisterActionDriver`/`RegisterResourceDriver` called after `platform.DefaultLoadOptions()` (or any `NewRegistry()`) already ran | `uses unregistered action "..."`/`uses unregistered kind "..."` at compile time — reads like a typo | Register before the `Registry` is constructed; see "Plugin points" above. |
| The maintenance toggle route's own path isn't in `internal/ops.exemptPrefixes` | Turning maintenance on locks out the route that turns it back off; only a restart (or editing the env var) recovers | Keep the two in sync, or don't move the route. |
| `.env` sets a variable that's already set in the real environment | No error — the real value silently wins, `.env`'s is ignored | Expected behavior (`cmd/server/dotenv.go`), not a bug; unset the real one or edit it directly if you meant to override it locally. |
| A BCL field does `env(...) == "x" ? A : B` all inline | No error — silently picks the wrong branch | Bind the `env()` call to its own field first, then compare that field, e.g. `db_driver env(...)` then `db_driver == "x" ? A : B`. A real parser quirk in the pinned `github.com/oarkflow/bcl` version, found while this starter still branched migrations by dialect in BCL (since replaced by `cmd/migrator` — see "Schema migrations" above — but the parser behavior itself is unchanged). |
| A BCL field is a bare reference to another top-level field, inside a nested block or a list element | No error — the field's own *name* becomes its string value | Bare references only resolve reliably as a ternary's condition at the top level; use `env(...)` directly anywhere else. |
| A `kind decision` node's `requires` names a fact that a plain (non-speculative) node produces | No error — every request silently denied, generically ("access denied"), no node past the deadlock ever runs | The decision node must `requires` only facts that are themselves speculation-free of every other decision node — see "Network protection and business rules" above's `rules.evaluate` note. Give the plain node `speculation pre_auth_safe`, or drop the fact from the decision node's `requires` if it doesn't actually need it. |

## Load-test baseline

Measured with [`hey`](https://github.com/rakyll/hey) against a locally
running `go run ./cmd/server` (Apple M-series laptop, SQLite, single
process, no PostgreSQL) — a baseline to notice a regression against, not a
production capacity number:

| Route | Concurrency | Duration | Result |
|---|---|---|---|
| `GET /health` (no auth, no DB) | 50 | 15s | 306,418 requests, 100% 200, p50 1.5ms, p99 11.7ms |
| `GET /api/v1/me` (session lookup + RBAC + DB, **before** WAL) | 50 | 15s | 40,619 requests, 3 requests failed with `500`/`disk I/O error` |
| `GET /api/v1/me` (same route, **after** enabling `journal_mode(WAL)`) | 50 | 15s | 95,248 requests, 100% 200, p50 7.3ms, p99 17.4ms |
| `POST /api/v1/orders` (write path, WAL) | 25 | 10s | 37,289 requests, 100% 201, p50 6.2ms, p99 16.2ms |

The middle row is the reason `01_resources.bcl`'s `dsn` default now includes
`_pragma=journal_mode(WAL)` alongside `busy_timeout(5000)`: SQLite's default
rollback-journal mode produced sporadic `disk I/O error` 500s under
concurrent authenticated traffic that `busy_timeout` alone did not fully
absorb; WAL mode eliminated the errors and roughly doubled throughput while
cutting p99 latency by more than half in the same run. This is SQLite-only —
irrelevant, and harmless to leave in the DSN, once `DB_DRIVER=pgx`.

Re-run it yourself against your own hardware before trusting these numbers
for capacity planning:

```sh
export SESSION_SECRET="$(openssl rand -hex 32)" WEBHOOK_SECRET="$(openssl rand -hex 32)"
go run ./cmd/migrator cli migrate
go run ./cmd/server & SERVER_PID=$!
until curl -sf -o /dev/null http://localhost:8080/health; do sleep 0.2; done

# A cookie jar file, not a manual header parse: curl writes the exact
# Set-Cookie value here in a format awk can pull a "name=value" pair out of
# on any platform — grep -P (Perl-compatible regex) is a GNU extension that
# does not exist on macOS/BSD grep, so a `grep -oP` one-liner here would
# work on Linux and fail everywhere else with "invalid option -- P".
JAR=$(mktemp)
curl -s -c "$JAR" -X POST http://localhost:8080/register \
  -H 'Content-Type: application/json' \
  -d '{"email":"loadtest@example.com","password":"L0adTest!2345","name":"Load Test"}' >/dev/null
COOKIE=$(awk '/starter_sid/{print $6"="$7}' "$JAR")

hey -z 15s -c 50 http://localhost:8080/health
hey -z 15s -c 50 -H "Cookie: $COOKIE" http://localhost:8080/api/v1/me

kill $SERVER_PID
rm -f "$JAR"
```

## Secrets in production

`secret "x" { env "X" required true }` (`resources/config/00_app.bcl`) is the
default and is fine for a single host or a platform that injects secrets as
environment variables (most PaaS/container schedulers) — but a real secrets
manager (Vault, AWS/GCP/Azure Secrets Manager, Doppler, 1Password Connect,
...) more often hands you a *file* than sets your process's environment
directly.

**Two different resolution paths, not one** — found while wiring this up,
and worth knowing before you assume both secrets behave the same way:

- **`webhook_secret`** is read through `SecretSpec` itself: `07_triggers.bcl`'s
  `secret "webhook_secret"` names it by reference, and `platform/trigger.go`
  resolves it via `Platform.Secret("webhook_secret")` — the declarative
  block's own resolved value. `SecretSpec.file` (`platform/spec_app.go`)
  genuinely feeds this one: `resources/config/00_app.bcl` sets
  `file env("WEBHOOK_SECRET_FILE", "./.data/secrets/webhook_secret")`
  alongside `env "WEBHOOK_SECRET"`, and `SecretSpec` tries `env` first,
  falling back to `file` only when the variable is unset.
- **`session_secret`** is *not* read this way. `01_resources.bcl`'s session
  resource has its own `secret env.required("SESSION_SECRET")` — a BCL-parse-time
  call, evaluated before `SecretSpec` resolution ever runs — so a `file`
  field on `00_app.bcl`'s `secret "session_secret"` block would silently do
  nothing. This is resolved one level up instead: `cmd/server/main.go` calls
  `bootstrap.LoadSecretFile("SESSION_SECRET", ...)` right after loading
  `.env` and before `platform.LoadDir` ever parses the document — the same
  "populate the real environment before anything else runs" mechanism
  `.env` itself uses (`internal/bootstrap/dotenv.go`), just from a file a
  secrets manager renders instead of one a person edits.

Both end up configurable the same way: `SECRETS_DIR` (default
`./.data/secrets`) names the directory, and `SESSION_SECRET_FILE`/
`WEBHOOK_SECRET_FILE` override an individual path. Whichever variable is
already set in the real environment always wins — a file that exists is
only ever a fallback, never a silent override of production configuration.

The common integration shape is a sidecar or init container that
authenticates to the secrets manager and writes each secret to a file under
a `tmpfs`-backed path (e.g. `/run/secrets/`, or this starter's default
`./.data/secrets/`) before this process starts — the Vault Agent Injector,
the AWS/GCP Secrets Manager CSI driver, and Doppler's `doppler run` all work
this way. Prefer a file over `env` in this setup even though both work for
`webhook_secret` — a secret value never appearing in `docker inspect`/
`kubectl describe pod`/process-environment dumps (which `env` values do) is
the whole reason to use a secrets manager in the first place.

Either way, `SecretSpec` values are never included in the redacted
`Platform.Document` this app exposes (e.g. to `/metrics` or any future admin
introspection route) and are never logged — see its doc comment.

### Verified end to end against a real Vault

Every command below actually ran, in this order, against an actual local
HashiCorp Vault (`hashicorp/vault:1.17`, dev mode, real KV v2 — not
simulated). `scripts/render-secrets-from-vault.sh` and
`scripts/rotate-secret.sh` talk to Vault's HTTP API directly with `curl`+
`jq`; neither needs the `vault` CLI installed. Run this from
`examples/starter/`, with `jq` and Docker available.

**If you already have a `.env` with `SESSION_SECRET`/`WEBHOOK_SECRET` set**
(from the Quick Start), move it aside first —
`mv .env .env.disabled-for-vault-walkthrough`. `LoadDotenv` runs before the
file fallback below and, correctly, never overrides a variable that is
already set (see "Two different resolution paths, not one" above) — which
means a `.env` supplying these two specifically will make every step below
*look* like it did nothing: the server keeps using `.env`'s fixed values no
matter what Vault says, silently. This is not a bug in either mechanism,
just two real env-var sources that both apply here; restore the file
(`mv .env.disabled-for-vault-walkthrough .env`) once you're done.

**1. Start Vault and seed the two secrets it doesn't have yet:**

```sh
docker run -d --name starter-vault -p 8200:8200 \
  -e VAULT_DEV_ROOT_TOKEN_ID=root hashicorp/vault:1.17 server -dev \
  -dev-listen-address=0.0.0.0:8200
sleep 2   # give the dev server a moment to finish unsealing

export VAULT_ADDR=http://127.0.0.1:8200 VAULT_TOKEN=root
curl -s -H "X-Vault-Token: $VAULT_TOKEN" -X POST \
  -d '{"data":{"session_secret":"'"$(openssl rand -hex 32)"'","webhook_secret":"'"$(openssl rand -hex 32)"'"}}' \
  "$VAULT_ADDR/v1/secret/data/starter" >/dev/null
```

**2. Render the secrets to disk and boot the server on them alone** — no
`SESSION_SECRET`/`WEBHOOK_SECRET` in the shell at all, which is the point:

```sh
rm -rf .data
./scripts/render-secrets-from-vault.sh ./.data/secrets
export DB_DRIVER=sqlite DB_DSN="file:.data/starter/app.db?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
go build -o ./bin/server ./cmd/server   # a real binary, not `go run`: step 5 needs to kill and
                                         # restart the exact process listening on :8080, and `go
                                         # run`'s own PID is a wrapper around a separately-forked
                                         # child — killing it does not reliably kill the server,
                                         # so a "restart" can silently no-op and keep serving the
                                         # generation from before the rotation.
go run ./cmd/migrator cli migrate

restart_server() {
  lsof -ti:8080 | xargs -r kill -9 2>/dev/null
  while lsof -ti:8080 >/dev/null 2>&1; do sleep 0.2; done
  ./bin/server & SERVER_PID=$!
  until curl -sf -o /dev/null http://localhost:8080/health; do sleep 0.2; done
}
restart_server
```

**3. Prove it authenticates, and keep the cookie for later:**

```sh
JAR=$(mktemp)
curl -s -c "$JAR" -X POST http://localhost:8080/register -H 'Content-Type: application/json' \
  -d '{"email":"vaultuser@example.com","password":"VaultTest!2345","name":"Vault User"}' -o /dev/null
COOKIE=$(awk '/starter_sid/{print $6"="$7}' "$JAR")
curl -s -H "Cookie: $COOKIE" http://localhost:8080/api/v1/me -w '\nHTTP:%{http_code}\n'   # 200
```

**4. Rotate `session_secret` in Vault and re-render — without stopping the
server:**

```sh
./scripts/rotate-secret.sh session_secret ./.data/secrets
curl -s -H "Cookie: $COOKIE" http://localhost:8080/api/v1/me -w '\nHTTP:%{http_code}\n'   # still 200
```

Still `200` here is expected, not a bug: `SESSION_SECRET` was read into the
process environment once, at boot (see "Two different resolution paths,
not one" above) — a still-running generation has no way to notice the file
on disk changed underneath it. Rotation takes effect on the *next* boot,
which is exactly what step 5 forces:

**5. Restart, and confirm the rotation actually took effect:**

```sh
restart_server

echo "old cookie after rotation + restart:"
curl -s -H "Cookie: $COOKIE" http://localhost:8080/api/v1/me -w '\nHTTP:%{http_code}\n'   # 401

echo "fresh login with the rotated secret:"
curl -s -c "$JAR" -X POST http://localhost:8080/login -H 'Content-Type: application/json' \
  -d '{"email":"vaultuser@example.com","password":"VaultTest!2345"}' -o /dev/null
NEW_COOKIE=$(awk '/starter_sid/{print $6"="$7}' "$JAR")
curl -s -H "Cookie: $NEW_COOKIE" http://localhost:8080/api/v1/me -w '\nHTTP:%{http_code}\n'   # 200
```

**6. The same rotation, for `webhook_secret`** — signing a request the way
`platform/trigger.go`'s `verify()` checks it, `HMAC-SHA256(secret,
timestamp + "." + body)`, hex-encoded, in `X-Signature-256`/`X-Timestamp`:

```sh
OLD_WEBHOOK_SECRET=$(cat ./.data/secrets/webhook_secret)
./scripts/rotate-secret.sh webhook_secret ./.data/secrets
restart_server
NEW_WEBHOOK_SECRET=$(cat ./.data/secrets/webhook_secret)

TS=$(date +%s)
BODY='{"email":"webhook-test@example.com","name":"Webhook Test"}'
OLD_SIG=$(printf '%s.%s' "$TS" "$BODY" | openssl dgst -sha256 -hmac "$OLD_WEBHOOK_SECRET" | sed 's/^.* //')
NEW_SIG=$(printf '%s.%s' "$TS" "$BODY" | openssl dgst -sha256 -hmac "$NEW_WEBHOOK_SECRET" | sed 's/^.* //')

echo "old signature after rotation + restart (expect 403, signature rejected):"
curl -s -X POST http://localhost:8080/webhooks/welcome -H 'Content-Type: application/json' \
  -H "X-Signature-256: $OLD_SIG" -H "X-Timestamp: $TS" -d "$BODY" -w '\nHTTP:%{http_code}\n'

echo "new signature (expect 422 — signature accepted, delivery itself fails with no real notification service running):"
curl -s -X POST http://localhost:8080/webhooks/welcome -H 'Content-Type: application/json' \
  -H "X-Signature-256: $NEW_SIG" -H "X-Timestamp: $TS" -d "$BODY" -w '\nHTTP:%{http_code}\n'
```

**7. Clean up:**

```sh
lsof -ti:8080 | xargs -r kill -9
docker rm -f starter-vault
rm -rf .data bin "$JAR"
```

This is the exact sequence run to verify this feature, not a paraphrase of
it — every status code in the comments above is a real response, captured
while writing this section, not a prediction.

**What this establishes, and what it doesn't**: file-based secret injection
from a real secrets manager works end to end, and rotation is real —
rejection of the old credential is not simulated, it comes from the session
resource's own HMAC verification failing against a genuinely different key.
What it does *not* give you is zero-downtime rotation: this starter reads
every secret once, at `platform.LoadDir` time, so today's rotation step is
"update the secret, then restart the process" — a brief window (however
long your process supervisor takes to cycle it) where requests fail rather
than degrade gracefully. Two ways to close that gap, neither wired in here
because they're bigger commitments than a starter should make by default:

- `session.sql`'s `secret` config field has a sibling, `previous_secrets`
  (`platform/resources_session.go`) — a list of prior keys still accepted
  for verification (not for signing new cookies), which turns a rotation
  into a grace window instead of a hard cut, at the cost of the rotation
  script also needing to shift the old value into that list rather than
  discard it.
- `docs/deploy.md`'s `deploy.Supervisor` hot-swap (already used nowhere in
  this starter — see its "What's deliberately not here" entry below) builds
  a new `Platform` generation and cuts connections over to it with zero
  refused connections; a rotation would re-render the secret files and
  propose+activate a new (functionally, if not textually, different)
  revision instead of restarting the process.

## What's deliberately not here

Documented, tested, and available in the wider `ref` repository, but not
wired into this starter because it doesn't belong in every product by
default:

- **`entity` blocks** (`docs/entities.md`) — a full CRUD REST resource from
  one declarative block, including tenant/owner scoping, soft delete,
  optimistic versioning and CSV export. See the commented example at the
  bottom of `resources/config/03_intents.bcl`.
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
  adopting once more than one person edits `resources/config/` in production.
- **The `capability` package's Go-level circuit breaker** (distinct from the
  BCL-declared `circuit_breaker.store`/`.guard`/`.record` this starter *does*
  use — see "Circuit breaker" above). `capability.NewRedisCircuitBreakerCapability`/
  `NewInMemoryCircuitBreakerCapability` exist for a Go-registered SPI adapter
  wrapping a call this starter doesn't make (an adapter's own outbound client
  call, not a BCL node) — a different layer, not a more-capable version of
  what's already wired in.
