# ETL engine: architecture, guarantees and failure modes

This describes what the engine promises, how it keeps the promise when parts fail, and which test proves each claim. Concepts and configuration are in [etl.md](etl.md).

## Shape

```
 ingest ──► checkpoint 1 ──► transform ──► checkpoint 2 ──► transfer ──► checkpoint 3 ──► deliver ──► reconcile ──► lineage
 (validate)                   (contract)                     (breaker)                    (breaker)   (books balance)
```

* **One writer of truth: the store.** Every state change is a single `Store.Commit`: the batch (revision-checked), its checkpoints, quarantined rows, lineage, audit entries and counter increments, atomically. There is no state in a process that cannot be rebuilt from the store, except circuit-breaker state and the log ring, which are advisory.
* **Workers are interchangeable.** Any number of processes may sweep one store. A worker *leases* a batch before running a stage (`Claim`), so a stage runs once at a time; the lease ends with the commit, or by expiring.
* **Every stage re-reads its input from a checkpoint** (hash-checked), never from memory, so a stage can be repeated from nothing but the store.

## Guarantees

| # | Guarantee | Mechanism |
|---|---|---|
| G1 | No row is lost. Every row in is quarantined, delivered, or still travelling in a batch that is in flight or held. | Checkpoints written before the next stage; `Summary.rows_lost` and the reconciliation stage |
| G2 | A failure never drops data: it is retried, then the batch is held at its checkpoint. | Retry policy, hold, replay |
| G3 | Nothing is delivered twice by the engine. Deliveries are at-least-once and must be idempotent on `batch.key`, which replay reuses. | Idempotency key; one lease per stage |
| G4 | A stage runs on one worker at a time. | Lease (`lease_owner`, `lease_until`), revision check on commit |
| G5 | A dead worker's batch is recovered without anyone's help. | Lease expiry; the batch is due again |
| G6 | A hook that panics, hangs or is slow cannot stop a worker. | `guard`: recover, timeout, run in its own goroutine |
| G7 | A failing destination is not hammered, and waiting for it costs no attempts. | Circuit breaker per destination |
| G8 | Wrong data does not travel on: a transform that changes the row count or breaks the output contract is held; a batch whose books do not balance cannot count as delivered. | Contract check after transform; reconciliation stage |
| G9 | The audit trail cannot be rewritten: not by the application, and not by an UPDATE or DELETE in the database. A change anywhere breaks the hash chain. | DB triggers; hash chain; `VerifyAudit` |
| G10 | Counters never disagree with the state they count, and survive restarts. | Counters are rows committed in the same transaction as the state |
| G11 | A stage in progress when the process stops still has its outcome recorded. | Hook not cancelled by shutdown; commit on a detached context |
| G12 | Bad input is refused before it costs anything: oversize uploads, duplicate content, unknown or paused sources, missing keys, bad roles. | `MaxRows`, `RejectDuplicates`, validation |
| G13 | Storage does not grow without bound. | Retention removes checkpoints of finished batches; held batches keep theirs |
| G14 | A person sees and does only what their roles allow, and sources they may not see do not reveal themselves. | Roles with source scopes; not-found instead of forbidden |
| G15 | The database rejects impossible states even if the code is wrong. | `CHECK` constraints on status, stage and row counts; unique `(source, key)` |
| G16 | Every process learns of a sick destination from the first one to find it: the breaker is in the store, not in a process. | `etl_breakers`, atomic `RecordBreaker` |
| G17 | Hooks that cannot be stopped do not pile up. | Calls still running after their timeout are counted and capped (`MaxAbandoned`, `etl_hooks_abandoned`); past the cap new calls fail at once |
| G18 | An alert has a life, not just a moment: it opens, may be acknowledged (who, why, until when), clears, and is announced once. | `etl_alerts`, `EvaluateAlerts`, `AckAlert`, `Notify` (retried until it succeeds) |
| G19 | Monitoring stays exact and cheap as data grows. | Source totals are SQL aggregates; stage totals are hourly counters; only percentiles use a bounded sample |
| G20 | Checkpoints are small and finished ones are removed. | gzip on write; retention deletes the checkpoints of delivered and failed batches (held keep theirs) |

## Failure modes

| What fails | What happens | How it is noticed | Recovery | Proven by |
|---|---|---|---|---|
| Destination down | Attempts fail with backoff; after the limit the batch is held at its checkpoint | `held` alert, failures panel, `etl_stage_runs_total{outcome}` | Fix, then replay | `TestRetryThenHoldThenReplayLosesNothing` |
| Destination down for many batches | After N failures the circuit opens (for every process); other batches wait without using attempts | `circuit` alert, `etl_circuit_open`, health `circuits` | Automatic: one probe after the cool-down, then flow | `TestACircuitBreakerSparesAFailingDestination`, `TestOneProcessLearnsFromAnothersFailures` |
| Worker dies mid-stage | Its lease expires; the batch is due again; delivery is idempotent | `stuck` alert if nothing picks it up | Automatic after `lease_ttl` | `TestACrashedWorkersBatchIsTakenOver` |
| Two workers race for a batch | One wins the lease; the other skips it | `etl_lease_conflicts_total` | None needed | `TestWorkersNeverRunTheSameStageTwice` (4 workers, 30 batches, every stage exactly once; run with `-race` on SQLite and PostgreSQL) |
| Hook panics | Contained, counted, retried | `etl_hook_panics_total`, failure text | Automatic retry | `TestAHookThatPanicsOrHangsDoesNotTakeTheWorkerDown` |
| Hook hangs | Abandoned at `stage_timeout`, retried; past `MaxAbandoned` such calls are refused until they return | `etl_hook_timeouts_total`, `etl_hooks_abandoned` | Automatic retry | same, `TestHooksStillRunningAfterTheirTimeoutAreCountedAndCapped` |
| Process killed | Uncommitted stages repeat from their checkpoint; counters and batches are intact | Sweeper heartbeat gap; health `sweeper` | Restart | live drill (kill -9) in the example; `TestCountersSurviveARestart` |
| Shutdown during a stage | The stage finishes and its outcome is committed | none needed | none needed | `TestAStageInProgressFinishesWhenShutdownBegins` |
| Database busy, deadlock, dropped connection | The write is retried with a growing pause | `etl_db_retries_total` | Automatic | `TestTransientErrorsAreRetriedAndOthersAreNot` |
| Database down | Requests fail with 503; batches stay where they were; the sweeper keeps trying | health `store` is `down`, `/healthz` 503 | Restore the database | health probe tests |
| Transform returns wrong data | Batch held (permanent), nothing delivered | `held` alert, `etl_contract_violations_total` | Fix the transform, replay | `TestBusinessRulesAreEnforcedBetweenStages` |
| Delivery acknowledged fewer rows than sent | Reconciliation holds the batch | `etl_reconciliation_failures_total`, `held` | Investigate, replay | same |
| Same file sent twice | Same key: the original batch is returned. New key, same content: refused if the source says so | `etl_duplicates_rejected_total` | none needed | `TestIdempotentIngest`, `TestUploadLimitsAndDuplicateContent` |
| Huge upload | Refused before any row is read | `etl_refused_total{reason}` | Split it | `TestUploadLimitsAndDuplicateContent` |
| Source goes quiet | Freshness alert at one window, critical at two | `stale` alert, emailed once | Chase the owner; acknowledge to silence | `TestMonitoringAlertsHealthMetricsAndLogs`, `TestAlertsHaveAHistoryCanBeAcknowledgedAndAreAnnouncedOnce` |
| Someone edits the audit table | The database refuses; if the guard is dropped, the chain breaks | health `audit chain`, verify button | Investigate | `TestSQLAuditChainDetectsTampering` |
| Checkpoint corrupted | Hash mismatch: the stage refuses to run on it | error log | Re-ingest | `etl_test`: checkpoint hash check |
| Role or source changed while running | Takes effect within two seconds; in-flight work is unaffected | audit entry | none needed | `TestCustomRolesAndSourceScopes` |

## Durable counters

Counters and histograms live in the `etl_counters` table (name, labels, value). A step that changes state also lists the increments it caused (`Change.Counters`), and the store applies them in the same transaction with an upsert (`value = value + ?`). Consequences:

* they survive restarts and crashes, and add up across processes;
* a counter cannot say a batch was delivered when the batch is not (or the reverse);
* increments are written in a fixed order, so concurrent writers cannot deadlock on counter rows;
* histograms are stored as `_bucket{le}`, `_sum` and `_count` counters and rendered in the Prometheus format by `etl.metrics`.

Gauges (batches by status, queue depth, oldest wait, sweeper age, open circuits, alerts, rows unaccounted for) are computed from the store at scrape time. Counters that are not part of a state change (a refused upload, a lease conflict) are written best effort in their own small transaction.

## Operating it

* **Scale out** by starting more processes against the same database. Use PostgreSQL for more than one writer; SQLite is for one process.
* **Tune** `stage_timeout` to the slowest legitimate hook, `lease_ttl` to a little more than twice that (recovery time after a crash), `breaker_threshold` and `breaker_cooldown` to how quickly a destination usually recovers, `retention` to how long finished checkpoints are useful.
* **Alert on** `etl_rows_unaccounted > 0` (page), `etl_batches{status="held"} > 0` (ticket), `etl_sweeper_heartbeat_seconds > 30` (page), `etl_alerts{severity="critical"} > 0`.

## Stores

One suite (`etl/*_test.go`) runs against every store: memory and SQLite always, PostgreSQL with `TEST_POSTGRES_DSN`, MySQL with `TEST_MYSQL_DSN`. All four pass, including the multi-worker, crash-recovery and circuit-breaker tests (run with `-race`). Use PostgreSQL or MySQL for more than one writer process.

## What cannot be fixed, and what is done about it

* **Exactly-once is an effect, not a transport guarantee.** A call can be cut off after the destination acted and before the engine wrote that down; no sender can see that. The engine makes the effect happen once anyway: just before each delivery call the batch is committed as *delivering* with a new **epoch** (a fencing token that only rises); the call carries `delivery_key` (the idempotency key) and `epoch`. If the outcome is never recorded, the next run finds the batch still marked, asks the destination through the `verify` hook whether it has `delivery_key`, and records it as delivered *without sending again* (`etl_deliveries_recovered_total`). If the destination does not have it, the call is repeated with the next epoch, and a destination that stores the highest epoch per key refuses a late call from a worker that lost its lease. What is still required of the destination: store `delivery_key` atomically with the data and answer `verify`. Without a `verify` hook an interrupted call is simply repeated and the destination must deduplicate on `delivery_key`.
* **A Go function cannot be forced to stop, only asked.** On timeout the hook's context is cancelled and it gets `stage_grace` (2s) to return; anything that honours its context (database, HTTP, `select` on `ctx.Done()`) stops and is not counted as abandoned. One that ignores it keeps running: it is counted, too many stop new calls, and once one has been stuck for `wedge_after` (2m) health reports `hooks: down` and `OnWedged` fires, so a supervisor restarts the process, which is the only thing that reclaims such a goroutine. Hooks must pass their context to everything that blocks.
* **Large batches live in object storage.** With `blobs` set (any `storage.*` resource), a batch is validated, transformed, transferred and delivered a **chunk** (`chunk_rows`, 5000) at a time: the database holds only chunk references and hashes (a few KB however large the batch), a hook never sees more than one chunk, and memory is one chunk. A file can be streamed in from the request or ingested by name from the `inbox/` prefix of the blob store without being read whole; its content hash is the SHA-256 of the bytes. Each chunk is delivered under its own key (`key#n`) with progress recorded, so an interruption resumes at the next chunk. Chunks are checked against their hash when read, and deleted by retention. Limits that remain: `unique` rules remember the values they have seen (memory grows with distinct values, not rows), at most 2000 refused-row details are kept per batch (the count is exact), and `reject_duplicates` on a streamed file is checked after it is read.
* **Percentiles** (p95) come from a sample of recent batches, not all of them; totals and averages are exact.
