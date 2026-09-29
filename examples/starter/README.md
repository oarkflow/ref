# starter — the seed for your next product

A minimal, production-shaped `ref` application meant to be copied out (its
own `go.mod`, its own `resources/config/`) and grown into a real product by editing
`resources/config/` only. `cmd/server/main.go` is bootstrapping — config, logging, load,
mount, listen — and never gets a second line of business logic; see "How to
add a product" below for what that promise actually means.

It ships with:

- **Session-cookie authentication.** Argon2id passwords, a signed HMAC
  cookie (`session.sql` — correct across replicas, not `session.file`), and
  protected routes that fail closed: `authz { roles [...] }` on a route
  denies before the intent ever runs. Register, sign in, sign out, change
  password (`resources/config/03_intents.bcl`).
- **A real RBAC example.** Two roles ("admin", "user"), an `authz.rbac`
  resource, and three concrete protected-route shapes: a page any signed-in
  account reaches (`/dashboard`), a page only "admin" reaches
  (`/dashboard/admin`), and a JSON API with the same rules
  (`/api/v1/...`) — see CONFIGURATION.md's note on `superuser_roles` before
  you assume "admin" bypasses every `roles [...]` gate for free.
- **Route groups.** `route_group "api"` in `resources/config/04_routes.bcl` declares one
  path prefix and one `session`/`auth`/`authz`/`cache_control` default for
  every route nested inside it; a nested route overrides what it needs to.
  This is a `ref` core feature added alongside this starter
  (`platform/route_group.go`) — it expands into ordinary routes at compile
  time, so nothing downstream (the planner, the mounter) knows groups exist.
- **Server-rendered HTML pages, side by side with a JSON API.** SPL
  templates (`resources/templates/`) for login/register/dashboard/admin, a JSON API
  under `/api/v1` sharing the exact same session cookie and RBAC rules — one
  auth mechanism, two ways to reach it.
- **One intent, four transports.** `notify.welcome` (`resources/config/03_intents.bcl`) is
  invoked by an HTTP route, a background queue worker, a daily schedule and
  an inbound webhook — the same business logic, chosen by the caller's
  transport, never duplicated. `starter_test.go` proves the route and the
  worker both reach the same place.
- **Multi-replica-safe infrastructure by default.** `cache.sql`,
  `queue.sql`, `session.sql`, `ratelimit.store` — not the `.memory`/`.file`
  variants — so this starter is correct the day you run two instances
  behind a load balancer, not just the day you first run `go run`.
- **Structured logging and observability for free.** Every HTTP request and
  every DAG node execution (decision, effect, retry, timeout, bulkhead)
  writes through one `zlog.Logger` — see `cmd/server/logging.go`.
- **Maintenance mode.** One shared toggle (`internal/ops/maintenance.go`)
  backs an HTTP middleware (503, HTML or JSON depending on the caller), a
  `/readyz` check, and a BCL admin route (`POST /api/v1/admin/maintenance`)
  to flip it at runtime — no redeploy.
- **Environment overlays.** `resources/config/11_environments.bcl`'s `profile "production"
  { override "resource.x" { ... } }` changes resource config per
  environment, natively, with zero code and zero filename convention.
- **An easy plugin point for a third-party tool.** Set `LOG_WEBHOOK_URL` and
  every log line — HTTP access logs and every DAG event — ships to any HTTP
  log collector (Datadog, Loki, Logtail, ...), with no code change. The
  maintenance toggle above is the second example of the same underlying
  mechanism (`platform.RegisterActionDriver`), for anyone building their own.
- **A bulkhead.** `notify.welcome`'s delivery node caps concurrent calls at
  5, shared by name across every one of its four entry points, so a slow
  notification service cannot exhaust every worker goroutine.
- **A circuit breaker.** `notify.welcome` and `notification.password_reset`
  both trip `delivery_breaker` after 5 consecutive delivery failures and fail
  fast (503) rather than keep hammering a downed dependency — while still
  reporting the real failure (422, worker retries) on every attempt below
  that threshold. See CONFIGURATION.md's "Circuit breaker".
- **Brute-force login protection.** 5 failed logins from the same IP or
  against the same account bans it (`resources/security/rules/auth-abuse-velocity.bcl`)
  for a window, via a route reporting its real outcome
  (`security_event { on_success on_failure }`) back to the perimeter guard —
  see "Network protection and business rules" below.
- **Optional distributed tracing.** Set `OTEL_EXPORTER_OTLP_ENDPOINT` and every
  DAG node execution gets a span, shipped to any OTLP/HTTP collector; unset,
  there's zero tracing overhead. See CONFIGURATION.md's "Tracing".
- **Password reset**, asynchronous like `notify.welcome`: a one-time token,
  delivered off the request path, that never reveals whether an email
  address exists (`resources/config/03_intents.bcl`'s `auth.forgot_password`).
- **Metrics.** `/metrics` (Prometheus) counts and times every DAG
  node/decision/effect/execution, verified with real traffic while building
  this starter, not just wired and left untested.
- **Verified against real PostgreSQL**, not just SQLite — see "Switching to
  PostgreSQL" below, including a real BCL parser gotcha found doing that
  verification (CONFIGURATION.md).
- **A load-tested SQLite default.** `journal_mode(WAL)` is in the DSN
  because a `hey -c 50` burst against an authenticated route reliably
  produced `disk I/O error` 500s without it — see CONFIGURATION.md's
  "Load-test baseline" for the before/after numbers.
- **Docker, and CI.** A working multi-stage `Dockerfile` and a three-job
  GitHub Actions workflow (`.github/workflows/ci.yml`) — both actually
  run, not just present.
- **A boot-time migration check, not a boot-time migration surprise.**
  `go run ./cmd/server` on a fresh checkout notices nothing has been
  migrated and either asks (an interactive terminal) or applies them
  automatically (`AUTO_MIGRATE=true`) — never a raw `no such table` from
  deep inside `LoadDir`. See "Quick start" below and CONFIGURATION.md's
  "Schema migrations".

## Quick start

Either export the required variables directly:

```sh
export SESSION_SECRET="$(openssl rand -hex 32)"
export WEBHOOK_SECRET="$(openssl rand -hex 32)"

go run ./cmd/server
```

That's it — on a fresh checkout, `go run ./cmd/server` notices the
database has never been migrated and asks:

```text
4 pending migration(s) (1_create_users_table, 2_create_audit_log_table,
3_create_password_resets_table, 4_create_orders_table). Run them now? [y/N]:
```

Answer `y` and it migrates, then boots. Prefer the two deliberate steps
instead (a real deployment usually should — see CONFIGURATION.md's "Schema
migrations" for why cmd/migrator is still the tool for anything beyond a
forward migration, such as a rollback)? Run the migrator yourself first and
the server's own check finds nothing pending and boots straight through,
with no prompt at all:

```sh
go run ./cmd/migrator cli migrate   # applies resources/migrations/*.bcl — once per schema change
go run ./cmd/server
```

Scripting this (CI, a container entrypoint, anywhere nothing can answer a
prompt)? Set `AUTO_MIGRATE=true` and skip the migrator step entirely —
`go run ./cmd/server` alone is enough, migration included:

```sh
AUTO_MIGRATE=true go run ./cmd/server
```

...or copy `.env.example` to `.env` and fill it in — `cmd/server/main.go`
(and `cmd/migrator/main.go`) load it automatically (`internal/bootstrap`)
before anything else runs, including before the BCL document's own
`env()`/`env.required()` calls:

```sh
cp .env.example .env
# edit .env: set real values for SESSION_SECRET and WEBHOOK_SECRET
go run ./cmd/server
```

A real environment variable always wins over `.env` — set one in your
shell, Docker, or CI and the file's value for that key is ignored, so `.env`
is safe to use in development without it ever fighting a deployment's real
configuration. `.env` is gitignored; `ENV_FILE=path/to/other.env` points at
a different file if you don't want `./.env`.

Schema is still owned by `resources/migrations/*.bcl`, applied through the
same `github.com/oarkflow/migrate` machinery either way — `cmd/server`
checking and applying it on boot (above) is not a second, competing way
schema gets created, just the same one steps automatically when nothing
answers a prompt for it or you've said `AUTO_MIGRATE=true`. Add or edit a
migration file and either binary picks it up: run `go run ./cmd/migrator
cli migrate` yourself, or just start the server again and answer its
prompt. `cmd/server` seeds a development admin account into that database
on boot — see "Production checklist" before you deploy this. In a
browser: `http://localhost:8080/login`, sign in with
`admin@example.com` / `Password123!`, and you land on `/dashboard`;
`/dashboard/admin` is the admin-only page.

From another terminal, the same session cookie drives the JSON API:

```sh
BASE=http://localhost:8080
J=/tmp/starter.cookies
curl() { command curl -sS -c $J -b $J -H 'Content-Type: application/json' "$@"; }

curl $BASE/health
curl -X POST $BASE/login -d '{"email":"admin@example.com","password":"Password123!"}'

curl $BASE/api/v1/me                       # any signed-in account
curl $BASE/api/v1/users                    # admin only
curl -X POST $BASE/api/v1/notify/welcome -d '{"email":"someone@example.com","name":"Someone"}'
# 422 "delivery to the notification service failed" by default — expected,
# not a bug: NOTIFY_URL (.env.example) points at 127.0.0.1:8099, and nothing
# listens there unless you run one. The point of this line is the 422 itself:
# a synchronous delivery failure reaches the caller as a real error, not a
# silent 200 — set NOTIFY_URL at something that answers to see the success path.
curl -X POST $BASE/api/v1/notify/welcome-async -d '{"email":"someone-else@example.com"}'
# 202 immediately; resources/config/05_workers.bcl's worker delivers it in the
# background, retrying on the same schedule, so this one never surfaces that
# failure to the caller at all — compare the two.

curl $BASE/livez
curl $BASE/readyz
```

`go test ./...` runs the same walkthrough (login, RBAC denial for the wrong
role, route_group inheritance, both notify.welcome transports) against an
in-process SQLite database and a recording HTTP server standing in for the
real notification service.

## How to add a product

The contract this starter exists to demonstrate:

1. **Add or edit a file under `resources/config/`.** A new feature is a new intent (and,
   if it's HTTP-reachable, a new route, possibly inside a `route_group`); a
   new backend is a new resource. Run `go test ./...` — a typo in an action
   name, a fact nobody provides, or a route pointing at an intent that
   doesn't exist is a test failure here, with a message naming the intent
   and the node, never a 500 in production.
2. **Touch `cmd/server/main.go` only for**: a new SQL driver's blank import
   (there's a commented one for `pgx`/PostgreSQL already), or a
   `platform.RegisterResourceDriver`/`RegisterActionDriver` call to add a
   backend `ref` doesn't ship (a Redis cache, a Kafka queue — see
   `docs/integrations-and-streaming.md` and the driver SPI in
   `platform/spi/spi.go` at the repository root). Never for business logic.
3. **Add a page by adding a template plus a route.** `internal/web/`'s SPL
   renderer is the one piece of Go wiring a BCL document has no way to name;
   it never changes when you add a page — only `resources/templates/` and `resources/config/`
   change.
4. **Never fork the fact-DAG engine.** If a feature seems to need a change
   to `platform/` itself, it almost certainly needs a resource or action this
   starter doesn't wire up yet, not a code change — check
   `platform/catalog_builtins.go`'s registered kinds first.

See `CONFIGURATION.md` for what each `resources/config/` file configures, how route
groups and RBAC actually behave, and the specific error you get for the
common ways to misconfigure either.

## Repository layout

Every non-Go input this starter reads lives under `resources/` — one
directory a deployment copies, mounts or bakes into an image, instead of
five scattered at the module root:

```
starter/
├── resources/
│   ├── config/               # ref's own BCL dialect (resource/intent/route/...) — the application document
│   │   ├── 00_app.bcl          # name, environment, roles, secrets
│   │   ├── 01_resources.bcl    # database, cache, queue, sessions, RBAC, notifications, guard, policy_engine
│   │   ├── 02_shapes.bcl       # validated input contracts (register, login, ...)
│   │   ├── 03_intents.bcl      # business logic — auth.*, dashboard.*, notify.welcome
│   │   ├── 04_routes.bcl       # web pages + route_group "api"
│   │   ├── 05_workers.bcl      # notify.welcome's async entry point
│   │   ├── 06_schedules.bcl    # notify.welcome's cron entry point
│   │   ├── 07_triggers.bcl     # notify.welcome's webhook entry point
│   │   ├── 08_static.bcl       # /static → resources/static/
│   │   ├── 09_workflow_example.bcl  # GUIDE: branching, switch/case, flags, route params, rules.evaluate
│   │   ├── 10_maintenance.bcl  # the admin route that flips maintenance mode
│   │   └── 11_environments.bcl # profile "production" { override ... } example
│   ├── security/              # the "guard" resource's policy — tcpguard's own BCL dialect, not config/'s;
│   │   ├── tcpguard.bcl          #   pack/guard/detector — LoadTCPGuardBundleDir walks this dir recursively
│   │   └── rules/                #   the full 8-rule reference-pack catalog, one status each — see CONFIGURATION.md
│   ├── rules/                 # the "policy_engine" resource's policies — rules' own BCL dialect, not config/'s;
│   │   └── orders/
│   │       ├── _schema.bcl       #   a fragment ("orders/*.bcl" auto-discovers definitions; "_"-prefixed = not one)
│   │       └── policy.bcl        #   imports _schema.bcl — becomes definition "orders.policy"
│   ├── migrations/*.bcl       # schema, applied by cmd/migrator — see "Schema migrations" in CONFIGURATION.md
│   ├── templates/             # SPL pages: auth/, dashboard/, errors/ (incl. maintenance.html), layouts, components
│   └── static/css/app.css     # the one stylesheet every page shares
├── internal/web/
│   └── renderer.go          # the SPL template engine adapter (Go glue, not business logic)
├── internal/bootstrap/      # .env/secret-file loading and CWD-independent directory resolution, shared by both binaries below
├── internal/migrations/     # shared Manager construction + Pending/Apply — cmd/server's boot-time check and cmd/migrator both build on this
├── internal/ops/
│   ├── maintenance.go       # the maintenance gate: middleware, health check, BCL action
│   └── seed.go              # the dev-admin seeder — env-gated, never runs in production
├── cmd/server/
│   ├── main.go              # bootstrap: .env, config, logging, metrics, migration check, LoadDir, mount, listen
│   ├── migrate.go           # ensureMigrated: checks + asks/applies pending migrations on every boot
│   └── logging.go           # the one zlog.Logger every log line goes through (+ webhook plugin)
├── cmd/migrator/main.go     # applies resources/migrations/*.bcl — see "Schema migrations" in CONFIGURATION.md
├── scripts/
│   ├── render-secrets-from-vault.sh  # renders session_secret/webhook_secret from Vault KV to ./.data/secrets/
│   └── rotate-secret.sh              #   rotates one in Vault, then re-renders — see "Secrets in production"
├── starter_test.go          # compiles resources/config/, proves auth + RBAC + route groups + transport reuse +
│                            # password reset + maintenance-guarded background delivery +
│                            # the business rule + the network guard
├── Dockerfile               # multi-stage build; run from the repository root — COPYs resources/ as one directory
├── .env.example             # copy to .env for local development
├── README.md                # this file
└── CONFIGURATION.md         # BCL reference + misconfiguration hints
```

`.github/workflows/ci.yml` and `.dockerignore` live at the repository root,
not here, since CI covers both this module and the `ref` core module.

## Conditional flows, branching, switch/case, flags and parameters

`resources/config/09_workflow_example.bcl` is a self-contained teaching feature ("orders")
covering the five mechanics an auth/dashboard starter doesn't otherwise need:
a conditional gate node, a compiled if/elseif branch (`flow.branch`), a
value-keyed switch/case (`decision.table` + `flow.switch`), a feature flag
read inside an expression (`flags.priority_shipping`), and an HTTP path
parameter (`request.param`, plus a documenting `parameter` block). It has its
own routes under `/api/v1/orders`, its own migration
(`resources/migrations/4_create_orders_table.bcl`), and its own test
(`TestOrdersWorkflowExample` in `starter_test.go`) — delete the file, its
migration, and that test once you've read it; nothing else in this starter
depends on it.

```sh
curl -X POST $BASE/api/v1/orders -d '{"amount": 900}'                       # flow.branch -> tier "large"
curl -X POST $BASE/api/v1/orders/1/transition -d '{"status": "shipped"}'    # 422: not a legal transition from "pending"
curl -X POST $BASE/api/v1/orders/1/transition -d '{"status": "paid"}'       # decision.table + flow.switch
curl -X POST $BASE/api/v1/orders/1/transition -d '{"status": "cancelled"}'  # the "cancel" case, from any non-delivered status
curl -X POST $BASE/api/v1/orders -d '{"amount": 9000}'                      # rules.evaluate: over the $5,000 self-service cap -> 403
```

## Network protection and business rules

Two capabilities that live in core `ref/platform`, wired in by declaring a
resource — no Go code in this starter or anywhere else:

- **`security.tcpguard`** (the `guard` resource in `resources/config/01_resources.bcl`,
  policy in `resources/security/tcpguard.bcl` + `resources/security/rules/*.bcl`):
  a deployment-wide abuse-detection perimeter guard, evaluated on every
  request before authentication runs — a different layer from a route's own
  `rate_limit`, which only counts one route by a fixed window.
  `resources/security/rules/` ships tcpguard's full 8-rule reference-pack
  catalog: 2 `active` (request-shape probing, brute-force login velocity),
  6 `paused` with a comment on each explaining exactly what's missing to
  activate it (an API-key surface, a bulk-export endpoint, a payment
  amount extractor, ...) — see CONFIGURATION.md's "Network protection and
  business rules" for the full table.
- **`rules.engine`** (`policy_engine`, policy in `resources/rules/orders/policy.bcl`):
  a second, independently versioned decision engine alongside the built-in
  `decision.table`, evaluated by `orders.create`'s `policy` node.

```sh
curl "$BASE/health?x=%27%20or%20%271%27%3D%271"   # a SQLi-shaped query -> 403, blocked before health.ping ever runs

# Brute-force login protection: 5 failed logins ban the IP/account for a window
for i in 1 2 3 4 5; do curl -s -o /dev/null -w '%{http_code}\n' -X POST $BASE/login -d '{"email":"admin@example.com","password":"wrong"}'; done
curl -X POST $BASE/login -d '{"email":"admin@example.com","password":"Password123!"}'   # 403 REQUEST_BLOCKED, even with the right password
```

See CONFIGURATION.md's "Network protection and business rules" for what
each policy file does (and deliberately doesn't), how `security_event` and
`identity_fields` feed the brute-force detector, and a real deadlock this
starter hit and fixed while wiring `rules.evaluate` in as a `kind decision`
node — worth reading before adding a second one.

## Maintenance mode, environments, and plugging in a third-party tool

**Maintenance mode.** `internal/ops/maintenance.go`'s `MaintenanceGate` is one
shared toggle behind three things: an `app.Use` middleware (503 to everyone
except `/health`, `/livez`, `/readyz`, `/static/*` and the toggle route
itself), a `/readyz` check, and the `ops.maintenance_set` action
`resources/config/10_maintenance.bcl`'s admin-only route calls. A browser gets a real SPL
page (`resources/templates/pages/errors/maintenance.html`, its own layout, its own
variables — `message`, `since`, `retryAfterSeconds`); anything else gets the
same information as JSON.

```sh
curl -X POST $BASE/api/v1/admin/maintenance \
  -d '{"on": true, "message": "Upgrading the database", "retry_after_seconds": 120}'
curl $BASE/dashboard          # 503 — the HTML page, if the client accepts it
curl $BASE/readyz             # {"status":"down", ...}
curl -X POST $BASE/api/v1/admin/maintenance -d '{"on": false}'
```

Boot straight into maintenance mode with `MAINTENANCE=true` (a maintenance
window that starts before the process does); flip it at runtime through the
route above, no restart.

**Environments.** See "Conditional flows..." above's neighbour,
`resources/config/11_environments.bcl` — BCL's native `profile "name" { override "type.id"
{ ... } }`, applied when `platform.LoadOptions.Profile` matches (set from
`APP_ENV` in `cmd/server/main.go`). No filename convention, no code change.

**A plugin point for a third-party tool.** `LOG_WEBHOOK_URL` (optionally with
`LOG_WEBHOOK_AUTH` for an API key) ships every log line — HTTP access logs
and every DAG node/decision/effect event — to any HTTP log collector, as
JSON, with no code change (`cmd/server/logging.go`'s `webhookWriter`). For a
deeper integration than "post JSON somewhere," the underlying mechanism is
`platform.RegisterActionDriver`/`RegisterResourceDriver` — the exact thing
`internal/ops.MaintenanceGate.RegisterAction` uses to add `ops.maintenance_set`
as a BCL-reachable action. Follow that file's shape for your own.

## Switching to PostgreSQL

Set `DB_DRIVER=pgx` and `DB_DSN=postgres://...`, uncomment the
`jackc/pgx/v5/stdlib` blank import in `cmd/server/main.go`, and re-run
`go run ./cmd/migrator cli migrate` against the new DSN before starting the
server — `resources/migrations/*.bcl` is dialect-neutral; `cmd/migrator`, built on
`github.com/oarkflow/migrate`, compiles it to `SERIAL` for PostgreSQL or
`AUTOINCREMENT`-equivalent DDL for SQLite from the same files. See
CONFIGURATION.md's "Schema migrations" section for the two things about that
tool worth knowing before you add your own table.

Both paths were verified end to end against actual running servers, not
just read for plausibility: registration, login, the orders workflow
(`flow.branch`, `decision.table` + `flow.switch`, and each dialect's own
primary-key form — SQLite's rowid alias, PostgreSQL's `SERIAL`), and the
password-reset flow all run identically on SQLite and PostgreSQL 16.

```sh
docker run -d --name starter-pg -e POSTGRES_PASSWORD=postgres -e POSTGRES_DB=starter -p 5432:5432 postgres:16-alpine
export DB_DRIVER=pgx DB_DSN="postgres://postgres:postgres@127.0.0.1:5432/starter?sslmode=disable"
export SESSION_SECRET=... WEBHOOK_SECRET=...
go run ./cmd/migrator cli migrate
go run ./cmd/server
```

## Docker

```sh
# from the repository root — the Dockerfile's build context must include
# ../../ because examples/starter/go.mod replaces github.com/oarkflow/ref
# with a relative path
docker build -f examples/starter/Dockerfile -t starter .

# The image ships both binaries; CMD's default is the server. A detached
# container has no terminal to answer cmd/server's own migration prompt
# (see "Schema migrations" in CONFIGURATION.md), so either run the migrator
# once yourself, against the same volume/DSN the server will use, and again
# after any schema change:
docker run --rm -v starter-data:/app/.data -e DB_DSN=... starter /usr/local/bin/migrator cli migrate

docker run -p 8080:8080 -v starter-data:/app/.data -e SESSION_SECRET=... -e WEBHOOK_SECRET=... starter

# ...or skip that step and let the server migrate itself on every start:
docker run -p 8080:8080 -v starter-data:/app/.data -e AUTO_MIGRATE=true \
  -e SESSION_SECRET=... -e WEBHOOK_SECRET=... starter
```

## CI

`.github/workflows/ci.yml` (at the repository root) runs three jobs on
every push/PR: the `ref` core module's own build/vet/test/gofmt (plus
`govulncheck` against its dependency graph), this starter's the same way
(a separate Go module, its own `govulncheck` run), and a Docker image
build.

## Production checklist

- **The seeded admin account is development-only, and enforced as such in
  Go, not just by convention.** `internal/ops.SeedDevAdmin` refuses to run
  at all when `APP_ENV=production`, and even outside production it only
  ever seeds once, when the `users` table is completely empty. There is no
  migration line to remember to delete.
- `APP_ENV=production` — checked by the seeding guard above, by
  `resources/config/11_environments.bcl`'s profile override, and by `crypto.signer`'s
  `ephemeral true` refusal elsewhere in `ref`, if you ever add one.
- `SESSION_SECRET` / `WEBHOOK_SECRET` — real random values
  (`openssl rand -hex 32`); at least 32 bytes, enforced when the `sessions`
  resource opens. Behind a real secrets manager (Vault, AWS/GCP Secrets
  Manager, Doppler, ...)? Leave them unset and let a sidecar render
  `./.data/secrets/session_secret`/`webhook_secret` instead —
  `scripts/render-secrets-from-vault.sh` and `scripts/rotate-secret.sh` do
  exactly this against a real Vault, verified end to end (old session
  cookies and webhook signatures rejected after rotation, new ones
  accepted) — see CONFIGURATION.md's "Secrets in production".
- `SESSION_COOKIE_SECURE=true` behind HTTPS — the cookie is otherwise sent
  over plain HTTP too. `resources/config/11_environments.bcl`'s `profile "production"`
  already forces this regardless of the env var, as a backstop.
- `DB_DRIVER` / `DB_DSN` — PostgreSQL for anything beyond a single-node
  deployment; see "Switching to PostgreSQL" above.
- **Log delivery, if `LOG_WEBHOOK_URL` is set, is best-effort.** A struggling
  collector gets its lines dropped rather than blocking request handling;
  `cmd/server/logging.go`'s `webhookWriter` counts drops and failures and
  reports the first one and every 100th one after to stderr — watch for
  that line if a collector goes down.
- **Maintenance mode pauses more than HTTP.** `notify.welcome` and
  `notification.password_reset` both check `ops.maintenance_status` before
  their delivery effect runs, so a worker/schedule/webhook-triggered job
  queued during a maintenance window waits (and retries) rather than
  running against a database that might be mid-migration. Apply the same
  `ops.maintenance_status` + `validate.expression` pattern to any new
  worker-driven intent that should honor it too.
