# starter — the seed for your next product

A minimal, production-shaped `ref` application meant to be copied out (its
own `go.mod`, its own `bcl/`) and grown into a real product by editing
`bcl/` only. `cmd/server/main.go` is bootstrapping — config, logging, load,
mount, listen — and never gets a second line of business logic; see "How to
add a product" below for what that promise actually means.

It ships with:

- **Session-cookie authentication.** Argon2id passwords, a signed HMAC
  cookie (`session.sql` — correct across replicas, not `session.file`), and
  protected routes that fail closed: `authz { roles [...] }` on a route
  denies before the intent ever runs. Register, sign in, sign out, change
  password (`bcl/03_intents.bcl`).
- **A real RBAC example.** Two roles ("admin", "user"), an `authz.rbac`
  resource, and three concrete protected-route shapes: a page any signed-in
  account reaches (`/dashboard`), a page only "admin" reaches
  (`/dashboard/admin`), and a JSON API with the same rules
  (`/api/v1/...`) — see CONFIGURATION.md's note on `superuser_roles` before
  you assume "admin" bypasses every `roles [...]` gate for free.
- **Route groups.** `route_group "api"` in `bcl/04_routes.bcl` declares one
  path prefix and one `session`/`auth`/`authz`/`cache_control` default for
  every route nested inside it; a nested route overrides what it needs to.
  This is a `ref` core feature added alongside this starter
  (`platform/route_group.go`) — it expands into ordinary routes at compile
  time, so nothing downstream (the planner, the mounter) knows groups exist.
- **Server-rendered HTML pages, side by side with a JSON API.** SPL
  templates (`templates/`) for login/register/dashboard/admin, a JSON API
  under `/api/v1` sharing the exact same session cookie and RBAC rules — one
  auth mechanism, two ways to reach it.
- **One intent, four transports.** `notify.welcome` (`bcl/03_intents.bcl`) is
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
- **A bulkhead.** `notify.welcome`'s delivery node caps concurrent calls at
  5, shared by name across every one of its four entry points, so a slow
  notification service cannot exhaust every worker goroutine.

## Quick start

```sh
export SESSION_SECRET="$(openssl rand -hex 32)"
export WEBHOOK_SECRET="$(openssl rand -hex 32)"

go run ./cmd/server
```

It listens on `:8080` against an embedded SQLite database at
`.data/starter/app.db` (created on first run, seeded with a development
admin account — see "Production checklist" before you deploy this). In a
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
curl -X POST $BASE/api/v1/notify/welcome-async -d '{"email":"someone-else@example.com"}'
# 202 immediately; bcl/05_workers.bcl's worker delivers it in the background.

curl $BASE/livez
curl $BASE/readyz
```

`go test ./...` runs the same walkthrough (login, RBAC denial for the wrong
role, route_group inheritance, both notify.welcome transports) against an
in-process SQLite database and a recording HTTP server standing in for the
real notification service.

## How to add a product

The contract this starter exists to demonstrate:

1. **Add or edit a file under `bcl/`.** A new feature is a new intent (and,
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
   it never changes when you add a page — only `templates/` and `bcl/`
   change.
4. **Never fork the fact-DAG engine.** If a feature seems to need a change
   to `platform/` itself, it almost certainly needs a resource or action this
   starter doesn't wire up yet, not a code change — check
   `platform/catalog_builtins.go`'s registered kinds first.

See `CONFIGURATION.md` for what each `bcl/` file configures, how route
groups and RBAC actually behave, and the specific error you get for the
common ways to misconfigure either.

## Repository layout

```
starter/
├── bcl/
│   ├── 00_app.bcl          # name, environment, roles, secrets
│   ├── 01_resources.bcl    # database, cache, queue, sessions, RBAC, notifications
│   ├── 02_shapes.bcl       # validated input contracts (register, login, ...)
│   ├── 03_intents.bcl      # business logic — auth.*, dashboard.*, notify.welcome
│   ├── 04_routes.bcl       # web pages + route_group "api"
│   ├── 05_workers.bcl      # notify.welcome's async entry point
│   ├── 06_schedules.bcl    # notify.welcome's cron entry point
│   ├── 07_triggers.bcl     # notify.welcome's webhook entry point
│   ├── 08_static.bcl       # /static → static/
│   └── 09_workflow_example.bcl  # GUIDE: branching, switch/case, flags, route params
├── templates/               # SPL pages: auth/, dashboard/, errors/, layouts, components
├── static/css/app.css       # the one stylesheet every page shares
├── internal/web/
│   └── renderer.go          # the SPL template engine adapter (Go glue, not business logic)
├── cmd/server/
│   ├── main.go              # bootstrap: config, logging, LoadDir, mount, listen
│   ├── logging.go           # the one zlog.Logger every log line goes through
│   └── http_adapter.go      # bridges /livez, /readyz onto *fh.App
├── starter_test.go          # compiles bcl/, proves auth + RBAC + route groups + transport reuse
├── README.md                # this file
└── CONFIGURATION.md         # BCL reference + misconfiguration hints
```

## Conditional flows, branching, switch/case, flags and parameters

`bcl/09_workflow_example.bcl` is a self-contained teaching feature ("orders")
covering the five mechanics an auth/dashboard starter doesn't otherwise need:
a conditional gate node, a compiled if/elseif branch (`flow.branch`), a
value-keyed switch/case (`decision.table` + `flow.switch`), a feature flag
read inside an expression (`flags.priority_shipping`), and an HTTP path
parameter (`request.param`, plus a documenting `parameter` block). It has its
own routes under `/api/v1/orders`, its own migration, and its own test
(`TestOrdersWorkflowExample` in `starter_test.go`) — delete the file, its
migration line in `bcl/01_resources.bcl`, and that test once you've read it;
nothing else in this starter depends on it.

```sh
curl -X POST $BASE/api/v1/orders -d '{"amount": 900}'                       # flow.branch -> tier "large"
curl -X POST $BASE/api/v1/orders/1/transition -d '{"status": "shipped"}'    # 422: not a legal transition from "pending"
curl -X POST $BASE/api/v1/orders/1/transition -d '{"status": "paid"}'       # decision.table + flow.switch
curl -X POST $BASE/api/v1/orders/1/transition -d '{"status": "cancelled"}'  # the "cancel" case, from any non-delivered status
```

## Switching to PostgreSQL

Set `DB_DRIVER=pgx` and `DB_DSN=postgres://...`, and uncomment the
`jackc/pgx/v5/stdlib` blank import in `cmd/server/main.go`. That's the whole
change — `bcl/01_resources.bcl`'s `driver`/`dsn` already read from the
environment.

## Production checklist

- **Remove or change the seeded admin account.** `bcl/01_resources.bcl`'s
  migrations insert `admin@example.com` / `Password123!` so a fresh clone
  has a way in. Change its password immediately after first login in a real
  deployment, or delete that migration line and create your first admin a
  different way (register normally, then `UPDATE users SET roles='admin'
  WHERE email=...` once, by hand).
- `APP_ENV=production` — recorded on the document; nothing in this starter
  currently hard-fails on it the way `crypto.signer`'s `ephemeral true`
  does elsewhere in `ref`, but keep it accurate for your own future checks
  and for anyone reading logs.
- `SESSION_SECRET` — `openssl rand -hex 32`; at least 32 bytes, enforced
  when the `sessions` resource opens.
- `SESSION_COOKIE_SECURE=true` behind HTTPS — the cookie is otherwise sent
  over plain HTTP too.
- `WEBHOOK_SECRET` — a real random value; the webhook trigger verifies
  every inbound request's HMAC against it.
- `DB_DRIVER` / `DB_DSN` — PostgreSQL for anything beyond a single-node
  deployment.
