# Data pipeline control room

A data-pipeline platform written only in BCL, SPL and static JS. The engine is the generic `etl.engine` resource in REF (package [`etl`](../../etl), docs in [docs/etl.md](../../docs/etl.md)); this directory configures it.

What it does:

* **Known origin.** Every source has an id, owner, format, destination and validation rules (`config/02_resources.bcl`). Bad rows are quarantined with the rule and the reason; a batch with too many is refused whole.
* **Controlled movement.** Six stages: validate, transform, transfer, deliver, audit, lineage. Each failed attempt is retried with backoff, then the batch is held at its last checkpoint.
* **Visible delivery.** The console shows what moved, where it went, when, and what needs attention, with the full history of every failed attempt.
* **Recoverable flow.** Replay a held batch from its checkpoint with the same idempotency key. Rows are never dropped; "rows unaccounted for" must stay at 0.
* **Control.** Users, service accounts and API keys; custom roles with per-source scope; an append-only audit trail with a verifiable hash chain; per-batch lineage; alerts, metrics and traces.

## Run

```sh
make wasm                                        # once: build the secure-transport client (see below)
make run dir=./examples/data-pipeline            # http://127.0.0.1:8080, SQLite in .data/
ETL_DB_DRIVER=pgx ETL_DSN='postgres://user:pass@host/db?sslmode=disable' ETL_DB_CONNECTIONS=5 make run dir=./examples/data-pipeline
```

Development accounts (password is `<name>-pass-123`). Roles are edited on the Access tab; these are the built-in ones:

| Account | Role | Can |
|---|---|---|
| `admin@example.com` | admin | everything, including users and roles |
| `operator@example.com` | operate | read, run, replay, edit and pause sources, monitoring, take destinations offline |
| `feeder@example.com` | ingest | send data and nothing else |
| `replayer@example.com` | replay | read and replay held batches |
| `analyst@example.com` | read | read everything, monitoring and the audit trail |

The tabs you see follow your permissions. Create a role limited to some sources (Access, Roles) and a person with it to see scoped access: they see, send to and monitor only those sources.

**API keys.** Service accounts (`svc-orders-feeder`, `svc-prometheus`) and people can hold keys, issued and revoked on the Access tab (a key is shown once; only its digest is stored). Send data with `POST /v1/sources/:source/batches`, header `X-API-Key`, body `{"key": "...", "data": "<file text>"}` or `{"key": "...", "rows": [...]}`; add `X-Trace-Id` to follow the batch through logs and audit. Repeating a key with the same content returns the original batch. Development keys: `dev-feeder-key-change-me-0123456` (feeder) and `dev-metrics-key-change-me-0123456` (metrics scraper). Replace both.

## Sign-in, accounts and sessions

**The account flow.** Everything below is on the sign-in page and works without an administrator, except approval.

| Step | What happens |
|---|---|
| Create an account (`/register`) | Name, email, password and a confirmation. The account is created *pending*: no roles, cannot sign in. The page gives the same answer whether or not the email is already known. |
| Approve | An administrator sees the request on Access, Users, gives it roles, and activates it. This is recorded in the audit trail. |
| Sign in (`/login`) | Wrong password, unknown email, pending, disabled and service accounts all get the same `401 invalid credentials`. Ten attempts a minute per address, then `429`. |
| Forgot password (`/forgot`) | Same answer for every email. For an active account a one-time link (30 minutes, hash stored) is queued to the mailbox. |
| Reset (`/reset?token=…`) | New password and confirmation. A link works once. |
| Change password (Account) | Current password, new password, confirmation; the new one must differ. |
| Admin: reset link, set password | Access, Users: *Reset link* makes a one-time link to hand over; *Password* sets one directly (with confirmation). Both are audited. |

Passwords are hashed with argon2id, need at least 12 characters with a letter and a digit, and are always entered twice; the page checks as you type and the server checks again. Reset emails are written to a *mailbox* (Access, Mailbox) because no email sender is wired in; connect `service.smtp` to `mail_outbox` to deliver them.

**Sessions.** Signed cookies (`HttpOnly`, `SameSite=Lax`; `secure true` in `config/02_resources.bcl` behind HTTPS), kept on the server. Signing out destroys the session, so a copy of the cookie stops working; a forged cookie is refused. Roles and status are read from the user table on every request (`principal_query`, cached two seconds), so disabling an account or changing a role takes effect in every open session within seconds and in every API key the account holds. Permissions are enforced on the server; the console only hides what you cannot do. You cannot disable yourself or remove your own administrator role.

**Not covered.** Changing or resetting a password does not end the person's other sessions (their roles and status are rechecked, but a signed-in session stays signed in until it expires or they sign out). There is no lockout beyond the rate limit, and no MFA.

## Secure transport (WebAssembly)

Every request the console makes, sign-in included, goes through [fh's secure transport](https://github.com/oarkflow/fh/blob/main/docs/secure-wasm-transport.md): the browser loads fh's WebAssembly client, which keeps a non-extractable device key, negotiates an AES-GCM session with the server, and sends each request as an encrypted, replay-protected envelope; replies are signed (RFC 9421) and verified before they are decrypted. It sits on top of TLS, not instead of it.

* `static/js/secure.js` starts it, checks the client's files against integrity pins, and replaces the page's `fetch`; `core.js` waits for it and sends nothing in plain. If it cannot start, the page shows why and makes no request.
* The server side is the `transport.secure` resource (`config/02_resources.bcl`). All of `/ui/` and the writes to `/login /logout /register /forgot /reset` must arrive encrypted; a plain request gets `426`. Machines keep using `/v1` with API keys, which are not wrapped.
* A secure session is tied to the web session. Before sign-in a device registers under an anonymous principal and may only call the sign-in paths; after sign-in the page registers again as the user. Signing out, or a different person signing in, ends the secure session. Registration needs a single-use grant that is bound to the cookie that asked for it and expires in 90 seconds.
* Pages carry a strict content security policy (scripts from this origin and WebAssembly only, connections back to this origin only, no framing).

Build the client with `make wasm` (needs TinyGo and a Go 1.26 toolchain: `go install golang.org/dl/go1.26.5@latest && go1.26.5 download`). It builds fh's own client and installs it, with its integrity manifest, into `static/wasm/` (about 600 KB). Without trust settings the client works only on loopback. **For a real deployment**:

```sh
# keys from a secret manager, an https origin, and the public halves built into the client
make wasm WASM_TRUSTED_ORIGIN=https://app.example.com \
  WASM_TRUSTED_TRANSPORT_KEY=... WASM_TRUSTED_TRANSPORT_KEY_ID=... \
  WASM_TRUSTED_RESPONSE_KEY=... WASM_TRUSTED_RESPONSE_KEY_ID=...
ETL_ORIGIN=https://app.example.com ETL_REQUIRE_EMBEDDED_TRUST=true ETL_DEV_KEYS=false \
  ETL_TRANSPORT_KEY_FILE=/run/secrets/etl-transport-key ETL_SIGNING_KEY_FILE=/run/secrets/etl-signing-key  make run dir=./examples/data-pipeline
```

The origin must match the address you open: `http://127.0.0.1:8080` and `http://localhost:8080` are allowed by default, and `ETL_ORIGIN` adds one (for another port, or the https address). In development the keys are created under `.data/secure` so pins survive restarts. Device registrations are kept in memory, so after a server restart the page registers again by itself; use a durable device store for production. The transport is tamper-resistant, not tamperproof: a script injected into the page can still read data before it is encrypted, which is why the policy above matters and why the server enforces every permission itself.

## Monitoring and observability

* **Reliability.** Workers lease batches (several processes can share the database), a crashed worker's batch is picked up after its lease expires, hooks are timed out and contained, a failing destination trips a circuit breaker, transforms must keep the row count and meet the source's output rules, books must balance before a batch counts as delivered, and the audit table refuses updates and deletes. See [docs/etl-architecture.md](../../docs/etl-architecture.md) for every failure mode and the test that proves it.
* **Monitoring tab:** throughput, source freshness against `expect_every`, per-source reject rate and latency, stage timings, queue and sweeper state, and alerts (held batch, stale source, retry, stuck, many rejects, sweeper stopped).
* **Observability tab:** health checks, lifetime counters (stored in the database, so they survive restarts), and the structured logs filtered by level, text, batch or trace id. A batch's detail shows its trace as a waterfall of stage runs and attempts.
* `GET /healthz` (no sign-in, 503 when down) and `GET /metrics` (Prometheus text; API key with `monitor.read`):

```sh
curl -H 'X-API-Key: dev-metrics-key-change-me-0123456' http://127.0.0.1:8080/metrics
```

## Try the failure drill

1. Sign in as admin. On Overview, take **Billing API** offline.
2. On Sources, **Send sample** for Orders. Open **Failures**: delivery retries 4 times with backoff, then the batch is held. The panel shows every attempt, the error, the decision, the checkpoint a replay resumes from, and what to do.
3. Bring Billing API online and **Replay from checkpoint**. The batch delivers; the audit trail records the hold and the replay.

## Where things are

| Question | Answer lives in |
|---|---|
| Which sources exist, their rules, retry policy and freshness window | `config/02_resources.bcl` (`sources`), editable at runtime from the Sources tab |
| What transform, transfer and delivery do | `config/10_stages.bcl` (intents the engine calls) |
| Routes and sign-in | `config/20_api.bcl` |
| Users, service accounts, keys, roles | `config/21_access.bcl` |
| Monitoring, logs, health, metrics routes | `config/22_observe.bcl` |
| Console | `templates/`, `static/` |
