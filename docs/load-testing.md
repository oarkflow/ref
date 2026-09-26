# Load and soak testing

`loadtest/soak_test.go` runs an app with a versioned, search-indexed, bulk
entity with durable hooks and a pipeline with an outbox hook, triage and a
notify rule, on a fresh PostgreSQL database, behind two replicas (two
platforms on one database). Both hooks fail 10% of the time (fault injection
for retry and dead-letter). It is skipped unless opted in:

    TEST_POSTGRES_DSN=postgres://postgres@127.0.0.1:55432/postgres REF_SOAK=1 \
      go test ./loadtest -run TestSoak -v -count=1

`REF_SOAK_DURATION` (default 30s; e.g. 8s for a smoke run) and
`REF_SOAK_WORKERS` (default 32) size it. It prints throughput and p50/p95/p99
and status codes per operation, then checks: both outboxes and the
notification queue drain; every committed entity change is delivered once
(duplicate hook runs are counted); every started case's `case.started` is
delivered; one notification per case; search index matches the rows; no 5xx;
stale versions get 409; heap does not grow monotonically; goroutines return to
baseline; no connections to the database remain after shutdown.

## Findings (32 workers, 30s, shared machine: numbers are indicative)

~650 req/s overall; p50 10-80ms, p99 < 250ms for every operation; no 5xx;
version and concurrent-action conflicts are 409; heap flat (~6-10MB after GC);
goroutines and connections released.

Fixed: two dispatchers could lease the same outbox row on PostgreSQL. The
claiming `UPDATE ... WHERE id IN (SELECT ... LIMIT n)` re-checks only its
outer condition on a row locked by a concurrent claim, and that was just the
id. Before the fix a 30s run delivered 45 entity hooks and 370 pipeline hooks
twice and sent 3038 notifications for 2096 cases (notifications have no
idempotency key, so users got duplicates); the outboxes took 36s to drain.
The claims (entity events, pipeline outbox, notifications) now repeat the
lease test in the outer condition: zero duplicates, 13s drain.

Remaining: each replica's dispatcher delivers one event at a time, so under
sustained writes the pipeline outbox builds a backlog that drains only after
load stops.
