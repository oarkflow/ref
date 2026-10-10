# ETL pipelines (`etl.engine`)

Package `etl` is a control plane for data that moves between systems. The engine decides when, how often and what to remember; your intents decide what a transform, a transfer or a delivery does. Example: [examples/data-pipeline](../examples/data-pipeline).

## Model

* **Source**: id, owner, format (`csv`, `jsonl`, `json`), destination, rules, `max_reject_rate`, retry policy. Updating a source bumps its version.
* **Batch**: one transfer from a source, idempotent on `(source, key)`. A repeated key with the same content returns the original batch; with different content it is refused (`KEY_REUSED`).
* **Stages**: 1 validate, 2 transform, 3 transfer, 4 deliver, 5 audit, 6 lineage. A batch's `stage` is the next stage to run.
* **Checkpoint**: rows handed on by stages 1, 2 and 3, stored with their SHA-256 and checked when read back.
* **Quarantine**: each refused row with the rule, the column and the reason.
* **Failure**: each failed attempt (stage, error, attempt n of max, outcome, reason, retry time) kept on the batch for its whole life.
* **Audit**: append-only, hash-chained (`prev_hash`, `hash`); `VerifyAudit` returns the first bad sequence number or 0.
* **Lineage**: edges `source:x → ingest → batch:y → transform v → rows:y → deliver → dest:z/ref`.

Status: `in_flight`, `retrying`, `held`, `delivered`, `failed` (refused at intake; nothing moved).

## Guarantees

* A failure never drops rows. A failed stage is retried with backoff; after the attempt limit (or a permanent error) the batch is **held** at its last checkpoint. `Replay` resumes after that checkpoint with the same idempotency key, so deliveries must be idempotent on `batch.key`.
* `Summary.rows_lost` is rows in minus quarantined, delivered, still travelling and refused whole. It must be 0.
* Writes are one atomic `Store.Commit` (batch, checkpoints, quarantine, lineage, audit); a stale batch revision is `ErrConflict`, so two workers cannot both advance a batch.

## Users, roles and permissions

Roles live in the engine (`etl_roles`), not in configuration, and are edited at run time. A role is a set of permissions, optionally limited to some sources. A person or service account holds role ids; the engine resolves what they may do on every call.

| Permission | Allows | Can be limited to sources |
|---|---|---|
| `ingest` | send data in | yes |
| `read` | batches, quarantine, lineage | yes |
| `advance` | run a stage by hand | yes |
| `replay` | replay held batches | yes |
| `sources.manage` | create, edit, pause sources | yes |
| `monitor.read` | monitoring, alerts, logs, health detail, metrics | yes |
| `audit.read` | the audit trail (verifying the chain needs it on every source) | yes |
| `ops.manage` | operational switches (an application's own) | no |
| `users.manage` | users and API keys (the application checks it with `etl.require`) | no |
| `roles.manage` | roles | no |

Built-in roles (editable, not deletable): `admin` (always everything), `operate`, `ingest`, `replay`, `read`, `monitor`, `auditor`. A caller who holds a permission only on other sources gets *not found* for a source they cannot see, so its existence is not disclosed; listings, summaries, quarantine and audit are filtered the same way.

Applications keep their own user table and map `principal.roles` to role ids (`role_map` if the names differ). `etl.require` guards an application's own operations with the engine's permissions, and `etl.note` writes them to the audit trail. See `examples/data-pipeline/config/21_access.bcl`: people, service accounts, API keys (digest only, shown once), password reset and change.

## Monitoring

`etl.monitor` returns, for a window: the throughput series (batches, rows in, delivered, quarantined, held per bucket), each source's health (freshness against `expect_every`, last batch, rows, reject rate, held, average and p95 latency), per-stage runs, failures and timings, the queue (batches waiting, oldest wait, sweeper age) and the alerts.

Alerts are computed from current state, so they clear when the cause does, and every one is also recorded: when it opened and cleared, who acknowledged it and why, and whether anyone was told. `EvaluateAlerts` (run by the sweeper every `alert_poll`) keeps that history and calls the `notify` intent once per opening and once per clearing, retrying until it succeeds; an acknowledged alert is silenced until its time is up (`etl.alert_ack`, `etl.alert_history`).

| Alert | Raised when | Severity |
|---|---|---|
| `held` | a batch is held and needs a replay | critical |
| `stale` | a source misses its `expect_every` window (critical past twice the window) | warning, critical |
| `sweeper` | nothing has run batches for 30 s and work is waiting | critical |
| `retrying` | a batch is backing off | warning |
| `intake_failed` | a batch was refused at intake in the last hour | warning |
| `stuck` | an in-flight batch has not moved for minutes | warning |
| `rejects` | over a day, more rows are quarantined than half the source's limit | warning |
| `paused` | a source is paused | info |

## Observability

* **Trace id.** Every batch has one (`X-Trace-Id` or `X-Request-Id` on the upload, else generated). It is on the batch, in every audit entry and log line, and passed to the stage intents as `batch.trace_id`.
* **Spans.** Each stage run is recorded on the batch with its attempt and duration; the console draws them as a waterfall.
* **Logs.** Structured records (`slog`, plus a ring of the last 1000 for `etl.logs`, filterable by level, batch, trace and text).
* **Metrics.** `etl.metrics` renders Prometheus text. Counters and histograms are stored in the database (they survive restarts and add up across processes); see [etl-architecture.md](etl-architecture.md): `etl_batches_ingested_total`, `etl_rows_total`, `etl_stage_runs_total`, `etl_stage_duration_seconds`, `etl_batch_duration_seconds`, `etl_replays_total`, `etl_hook_panics_total`, `etl_hook_timeouts_total`, `etl_circuit_opened_total`, `etl_lease_conflicts_total`, `etl_contract_violations_total`, `etl_reconciliation_failures_total`, `etl_refused_total`, and gauges `etl_batches{status}`, `etl_due_batches`, `etl_oldest_due_seconds`, `etl_sweeper_heartbeat_seconds`, `etl_circuit_open`, `etl_db_retries_total`, `etl_rows_unaccounted`, `etl_alerts{severity}`.  Gauges come from the store at scrape time.
* **Health.** `etl.health` runs store, sweeper, queue, held batches and audit chain probes. Anyone gets the overall status (`down` is a 503); a caller with `monitor.read` also gets every check.

## BCL

```bcl
resource "flow" {
  kind "etl.engine"
  config {
    database "db"                 # omit for memory; SQLite, PostgreSQL (driver "pgx") or MySQL
    transform "etl.transform"     # intents: input {source, batch, rows}
    transfer  "etl.transfer"      # ok false / an error fails the attempt; permanent true holds at once
    deliver   "etl.deliver"       # output {ref}; must be idempotent on batch.key
    poll "500ms"
    stage_timeout "30s"       # one hook call
    lease_ttl "90s"            # recovery time after a worker dies mid-stage
    breaker_threshold 5       # failures in a row that open a destination's circuit
    breaker_cooldown "30s"
    retention "720h"          # delete finished batches' checkpoints after this
    notify "alerts.notify"    # intent told when an alert opens or clears
    alert_poll "10s"
    sources [ { id "orders" owner "Sales" format "csv" destination "billing" max_reject_rate 0.2
                retry { max_attempts 4 base "2s" cap "30s" }
                rules [ { name "id" column "id" check "required" } ] } ]
  }
}
intent "ingest" { response "r" node "r" { uses "etl.ingest" resource "flow" kind effect requires [input] provides [r] } }
```

Source options: `expect_every` ("1h": the freshness window), `max_reject_rate`, `retry`, `max_rows` (refuse bigger uploads, default 100000), `reject_duplicates` (refuse content already accepted under another key), `output_rules` (the contract a transform's output must meet; a break holds the batch), `allow_row_change` (a transform may add or drop rows; by default it may not). Rule `check`: `required`, `type` (`int`, `float`, `bool`, `date`, `string`), `regex` (`pattern`), `range` (`min`, `max`), `enum` (`values`), `unique`, `expr` (an expression over the row; numeric text counts as a number). `column` and `check` are the BCL spellings, because `field` and `kind` are reserved inside BCL objects.

Actions: `etl.ingest`, `etl.advance`, `etl.run`, `etl.replay`, `etl.sources`, `etl.source_put`, `etl.source_pause`, `etl.batches`, `etl.batch` (batch, resume point, lineage, quarantine, audit, logs), `etl.quarantine`, `etl.audit`, `etl.verify_audit`, `etl.summary`, `etl.me`, `etl.permissions`, `etl.roles`, `etl.role_put`, `etl.role_delete`, `etl.require`, `etl.note`, `etl.monitor`, `etl.alerts`, `etl.logs`, `etl.counters`, `etl.alert_ack`, `etl.alert_history`, `etl.health`, `etl.metrics`.

## Storage

`etl.Store` has a memory implementation and `SQLStore` (PostgreSQL, MySQL, SQLite; tables `etl_*`, created by `Migrate`). One suite (`etl/engine_test.go`) runs against every store: memory and SQLite always, PostgreSQL when `TEST_POSTGRES_DSN` is set.

## Browser transport and account flow

The example's console reaches the engine through `transport.secure` (fh's WebAssembly secure fetch) and offers registration with approval, forgot and reset, and change of password with confirmation. They are described, with the limits, in `examples/data-pipeline/README.md`.
