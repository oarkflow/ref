# REF Complete Configuration Example

This example is configuration-first. `app.bcl` defines the application, resources, identity, authorization, policies, effects, durable process, human task, worker, idempotency, rate limiting, and HTTP routes. `main.go` is only the host: it prepares local paths, loads the BCL document, mounts the generated application, and shuts it down cleanly.

The host also runs an optional in-process kernel demonstration before serving. That demonstration exercises APIs which are intentionally host concerns rather than BCL resources:

- typed fact scheduling and immutable plan generation;
- verified identity, scoped source caching/coalescing, and an adaptive circuit breaker;
- transactional local effects plus leased durable delivery and replay;
- fact provenance, plan simulation, trace comparison, and deterministic replay;
- grounded prompt construction/citation validation;
- NDJSON/SSE streaming helpers;
- a versioned domain capability pack.

## Run

From the repository root:

```sh
go run ./examples/ref-complete
```

The default listener is `:8092`. Useful flags:

```sh
go run ./examples/ref-complete -addr 127.0.0.1:8092
go run ./examples/ref-complete -kernel-demo=false
go run ./examples/ref-complete -config examples/ref-complete/app.bcl
```

The default database is `.data/ref-complete/ref-complete.db` and the default queue directory is `.data/ref-complete/queue`. Override them without editing the BCL file:

```sh
COMPLETE_DATABASE_URL='file:/tmp/ref-complete.db?cache=shared&mode=rwc' \
COMPLETE_QUEUE_DIR=/tmp/ref-complete-queue \
go run ./examples/ref-complete
```

`COMPLETE_SESSION_SECRET` is optional for local development because the BCL document has a development fallback. Set it to a long random value outside local development.

## BCL walkthrough

Create a session with a role, then use the returned cookie for authenticated routes:

```sh
BASE=http://127.0.0.1:8092
curl -i -c /tmp/ref-complete.cookies -H 'Content-Type: application/json' \
  -d '{"user":"alice","roles":["customer"]}' "$BASE/session"

curl -i -b /tmp/ref-complete.cookies "$BASE/session"
```

Exercise the compiled branch and the BCL stream projection:

```sh
curl -sS -H 'Content-Type: application/json' \
  -d '{"subject":"large-ticket","amount":250}' "$BASE/flow/route"

curl -sS -H 'Content-Type: application/json' \
  -d '{"subject":"stream-ticket","amount":25}' "$BASE/flow/stream"
```

Exercise cache and route idempotency. The second request with the same key returns the stored response rather than executing the graph again:

```sh
curl -i -H 'Content-Type: application/json' -H 'Idempotency-Key: cache-1' \
  -d '{"key":"configuration"}' "$BASE/cache"
curl -i -H 'Content-Type: application/json' -H 'Idempotency-Key: cache-1' \
  -d '{"key":"configuration"}' "$BASE/cache"
```

Create an order. The BCL intent validates the shape, checks the session and RBAC, writes SQLite, publishes an outbox notification, and starts the durable `order.fulfil` process:

```sh
curl -i -b /tmp/ref-complete.cookies \
  -H 'Content-Type: application/json' -H 'Idempotency-Key: order-1' \
  -d '{"id":"order-1","amount":2500,"note":"needs review"}' \
  "$BASE/orders"
```

An order over `1000` parks at the BCL human task. A second user with the `approver` role can list and decide it:

```sh
curl -i -b /tmp/ref-complete.cookies "$BASE/tasks"
curl -i -b /tmp/ref-complete.cookies -H 'Content-Type: application/json' \
  -d '{"action":"approve","note":"reviewed"}' \
  "$BASE/tasks/<task-id>/decide"
```

Use `/runs/:id` to inspect the persisted cursor, step states, timers, subscriptions, and task state. The process store is `store.sql`; replacing it with `store.memory` is useful for a disposable test, while the SQL form survives restarts.

## What is configuration and what is host wiring?

The BCL owns:

- `database.sql`, `cache.memory`, `queue.file`, `outbox.memory`, and `store.sql` resources;
- `auth.session`, `authz.rbac`, `rules.engine`, rate-limit, and lock resources;
- typed shapes, roles, intents, effect barriers, process edges, task forms, worker, routes, idempotency, and route authorization;
- the `stream` route mode and the complete request/response contract.

The Go host owns:

- the SQLite driver import;
- local directory creation and signal-driven shutdown;
- the optional kernel demonstration for APIs that are libraries rather than BCL resource kinds.

The application is intentionally usable without PostgreSQL, Redis, Kafka, or a model provider. Replace the resource kinds in `app.bcl` when deploying against shared infrastructure.
