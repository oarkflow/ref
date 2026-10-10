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

Development accounts. Roles are edited on the Access tab; these are the built-in ones:

| Account / password | Role | Can |
|---|---|---|
| `admin@example.com` / `admin-pass-123` | admin | everything, including users and roles |
| `operator@example.com` / `operator-pass-123` | operate | read, run, replay, edit and pause sources, monitoring, take destinations offline |
| `feeder@example.com` / `feeder-pass-123` | ingest | send data and nothing else |
| `replayer@example.com` / `replayer-pass-123` | replay | read and replay held batches |
| `analyst@example.com` / `analyst-pass-123` | read | read everything, monitoring and the audit trail |

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

Passwords are hashed with argon2id, need at least 12 characters with a letter and a digit, and are always entered twice; the page checks as you type and the server checks again. Reset emails are written to a *mailbox* (Access, Mailbox); they are sent by email once `ETL_SMTP_HOST` is set.

**Sessions.** Signed cookies (`HttpOnly`, `SameSite=Lax`; `secure true` in `config/02_resources.bcl` behind HTTPS), kept on the server. Signing out destroys the session, so a copy of the cookie stops working; a forged cookie is refused. Roles and status are read from the user table on every request (`principal_query`, cached two seconds), so disabling an account or changing a role takes effect in every open session within seconds and in every API key the account holds. Permissions are enforced on the server; the console only hides what you cannot do. You cannot disable yourself or remove your own administrator role.

**Lockout.** Five wrong passwords (or codes) lock the account for fifteen minutes. A locked account, an unknown email and a pending account all get the same refusal. An administrator can unlock it at once (Access, Users, *Unlock*); the lock does not extend while it is on, so it cannot be used to keep someone out forever.

**Password change ends every session.** Changing a password, resetting it, or an administrator setting it ends all of that person's sessions, the current one too: the account's password version (`pw_changed_ms`) is checked against the one the session was created with on every request. API keys are not affected.

**Two-step sign-in (authenticator app).** Account, *Two-step sign-in*: the page shows a setup key; entering the code the app shows turns it on and shows eight single-use recovery codes once. After that, signing in with the password starts a session that can do nothing but enter a code (`MFA_REQUIRED` for everything else); a code is accepted once (its time step is remembered), wrong codes count towards the lockout, a recovery code works once. The secret is sealed (AES-GCM, `ETL_MFA_KEY`) before it is stored. Turning it off needs the password and a code. A person who lost their device is reset by an administrator (*Reset 2-step*), which also ends their sessions.

**Email.** Reset links and alert notices are written to a mail outbox in the step that caused them, and a scheduled job sends them through `service.smtp` (`ETL_SMTP_HOST`, `_PORT`, `_USERNAME`, `_PASSWORD`, `_FROM`, `ETL_SMTP_TLS`; sending stays off until `ETL_SMTP_HOST` is set, so no mail server is needed to run the example; for a local test run Mailpit and set `ETL_SMTP_HOST=127.0.0.1 ETL_SMTP_PORT=1025`). A message is tried ten times and stays visible in Access, Mailbox with its attempts. `ETL_ALERT_EMAIL` is who is told about alerts.

## Alerts

Open alerts appear on Monitoring. **Acknowledge** silences one for an hour to a week and records who and why; it stays listed, marked acknowledged, and is not announced again. Every alert's history (opened, cleared, acknowledged by) is kept (*Alert history*), and each opening and clearing is emailed once. Three things are checked by the engine itself and shown here: a held batch, a source that missed its schedule, and a destination whose circuit is open (the circuit state is shared by every process).

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

HTTPS is served by the runner itself when `TLS_CERT_FILE` and `TLS_KEY_FILE` are set (TLS 1.3 only; plain HTTP and TLS 1.2 are refused). The origin must match the address you open: `http://127.0.0.1:8080` and `http://localhost:8080` are allowed by default, and `ETL_ORIGIN` adds one (for another port, or the https address). In development the keys are created under `.data/secure` so pins survive restarts. Registered devices and replay records are kept in files under `.data/secure/state` (`state_dir`), so they survive a restart and a recorded handshake cannot be replayed after one; with `persist_sessions true` the sessions do too. For several server processes behind a balancer use a shared store. The transport is tamper-resistant, not tamperproof: a script injected into the page can still read data before it is encrypted, which is why the policy above matters and why the server enforces every permission itself.

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


## Dependencies

* Nothing outside Go is needed to run the example. The secure-fetch WASM client is committed under `static/wasm`; rebuild it only to embed trust keys, with `make wasm` (TinyGo and a Go 1.26 toolchain) or `make wasm-docker` (Docker only).
* Email is off until `ETL_SMTP_HOST` is set; no SMTP server is needed otherwise.
* Large batches are kept in `ETL_BLOB_DIR` (default `.data/blobs`). Put a file in `.data/blobs/inbox/` and ingest it with `{"key": "...", "object": "inbox/file.csv"}`.
* Delivery is exactly-once in effect: the example's `etl.deliver` is keyed on `delivery_key`, fenced by `epoch`, and `etl.verify_delivery` answers the engine when a call was cut off.
