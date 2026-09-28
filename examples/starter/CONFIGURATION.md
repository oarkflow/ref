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
