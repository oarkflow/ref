package etl

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func counter(t *testing.T, st Store, name, labels string) float64 {
	t.Helper()
	rows, err := st.Counters(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	total := 0.0
	for _, r := range rows {
		if r.Name == name && (labels == "*" || r.Labels == labels) {
			total += r.Value
		}
	}
	return total
}

func sweepAll(e *Engine, max int) {
	for i := 0; i < max; i++ {
		if n, _ := e.Sweep(context.Background(), 100); n == 0 {
			return
		}
	}
}

func TestCountersSurviveARestart(t *testing.T) {
	path := "file:" + filepath.Join(t.TempDir(), "restart.db") + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	open := func() (*Engine, *sql.DB) {
		db, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		db.SetMaxOpenConns(1)
		e, _ := testEngine(t, migrated(t, db, "sqlite", "r_"))
		return e, db
	}
	ctx := context.Background()
	e1, db1 := open()
	e1.PutSource(ctx, admin, ordersSource())
	for i := 0; i < 3; i++ {
		res, _ := e1.Ingest(ctx, admin, "orders", fmt.Sprint("k", i), rows(t, goodCSV+fmt.Sprintf("%d,1,NPR,x@y.z\n", 100+i)))
		e1.RunAll(ctx, admin, res.Batch.ID)
	}
	db1.Close() // the process ends

	e2, db2 := open()
	defer db2.Close()
	if got := counter(t, e2.Store, "etl_batches_ingested_total", `source="orders"`); got != 3 {
		t.Fatalf("ingested after restart: %v", got)
	}
	if got := counter(t, e2.Store, "etl_rows_total", `kind="delivered",source="orders"`); got != 15 {
		t.Fatalf("delivered after restart: %v", got)
	}
	res, _ := e2.Ingest(ctx, admin, "orders", "after", rows(t, goodCSV))
	e2.RunAll(ctx, admin, res.Batch.ID)
	text, _ := e2.MetricsText(ctx)
	if !strings.Contains(text, `etl_batches_ingested_total{source="orders"} 4`) || !strings.Contains(text, `etl_stage_duration_seconds_count{stage="4"} 4`) ||
		!strings.Contains(text, `etl_stage_duration_seconds_bucket{stage="4",le="+Inf"} 4`) {
		t.Fatalf("metrics after restart:\n%s", text)
	}
}

func TestCountersMatchTheStateTheyCount(t *testing.T) {
	eachStore(t, func(t *testing.T, e *Engine, _ *clock) {
		ctx := context.Background()
		for i := 0; i < 4; i++ {
			res, _ := e.Ingest(ctx, admin, "orders", fmt.Sprint("c", i), rows(t, goodCSV+fmt.Sprintf("%d,-1,NPR,x@y.z\n", 50+i)))
			e.RunAll(ctx, admin, res.Batch.ID)
		}
		sum, _ := e.Summary(ctx, admin)
		if got := counter(t, e.Store, "etl_rows_total", `kind="delivered",source="orders"`); int(got) != sum.Delivered {
			t.Fatalf("counter %v, state %d", got, sum.Delivered)
		}
		if got := counter(t, e.Store, "etl_rows_total", `kind="quarantined",source="orders"`); int(got) != sum.Quarantined {
			t.Fatalf("counter %v, state %d", got, sum.Quarantined)
		}
	})
}

// Several workers share one store; each stage of each batch must run exactly once.
func TestWorkersNeverRunTheSameStageTwice(t *testing.T) {
	eachStore(t, func(t *testing.T, e0 *Engine, clk *clock) {
		ctx := context.Background()
		var mu sync.Mutex
		runs := map[string]int{}
		count := func(stage string, b *Batch) {
			mu.Lock()
			runs[stage+b.ID]++
			mu.Unlock()
		}
		hooks := Hooks{
			Transform: func(_ context.Context, _ *Source, b *Batch, r []Row) ([]Row, string, error) {
				count("t", b)
				time.Sleep(2 * time.Millisecond)
				return r, "v1", nil
			},
			Transfer: func(_ context.Context, _ *Source, b *Batch, r []Row) error { count("x", b); return nil },
			Deliver: func(_ context.Context, _ *Source, b *Batch, r []Row) (string, error) {
				count("d", b)
				return b.Key, nil
			},
		}
		const batches = 30
		ids := make([]string, 0, batches)
		for i := 0; i < batches; i++ {
			res, err := e0.Ingest(ctx, admin, "orders", fmt.Sprint("w", i), rows(t, goodCSV+fmt.Sprintf("%d,1,NPR,x@y.z\n", 200+i)))
			if err != nil {
				t.Fatal(err)
			}
			ids = append(ids, res.Batch.ID)
		}
		var wg sync.WaitGroup
		for w := 0; w < 4; w++ {
			e := &Engine{Store: e0.Store, Hooks: hooks, Now: clk.now, Owner: fmt.Sprint("worker-", w), Eval: e0.Eval, Logger: e0.Logger}
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := 0; i < 400; i++ {
					if n, _ := e.Sweep(ctx, 10); n == 0 {
						time.Sleep(time.Millisecond)
					}
				}
			}()
		}
		wg.Wait()
		for _, id := range ids {
			b, _ := e0.Store.GetBatch(ctx, id)
			if b.Status != StatusDelivered {
				t.Fatalf("%s is %s at stage %d", id, b.Status, b.Stage)
			}
			for _, st := range []string{"t", "x", "d"} {
				if runs[st+id] != 1 {
					t.Fatalf("stage %s of %s ran %d times", st, id, runs[st+id])
				}
			}
		}
		sum, _ := e0.Summary(ctx, admin)
		if sum.RowsLost != 0 || sum.Delivered != batches*5 {
			t.Fatalf("summary %+v", sum)
		}
	})
}

func TestACrashedWorkersBatchIsTakenOver(t *testing.T) {
	eachStore(t, func(t *testing.T, e *Engine, clk *clock) {
		ctx := context.Background()
		res, _ := e.Ingest(ctx, admin, "orders", "crash", rows(t, goodCSV))
		id := res.Batch.ID
		// A worker claims the batch and dies before committing.
		if _, err := e.Store.Claim(ctx, id, "dead-worker", clk.now(), time.Minute); err != nil {
			t.Fatal(err)
		}
		if n, _ := e.Sweep(ctx, 10); n != 0 {
			t.Fatalf("a leased batch must be left alone, advanced %d", n)
		}
		if _, err := e.Advance(ctx, admin, id); !errors.Is(err, ErrLeased) {
			t.Fatalf("manual advance of a leased batch: %v", err)
		}
		clk.advance(2 * time.Minute) // the lease runs out
		sweepAll(e, 20)
		b, _ := e.Store.GetBatch(ctx, id)
		if b.Status != StatusDelivered || b.Delivered != 4 {
			t.Fatalf("not recovered: %+v", b)
		}
		if counter(t, e.Store, "etl_lease_conflicts_total", "") < 1 {
			t.Fatal("the conflict should have been counted")
		}
	})
}

func TestAHookThatPanicsOrHangsDoesNotTakeTheWorkerDown(t *testing.T) {
	eachStore(t, func(t *testing.T, e *Engine, clk *clock) {
		ctx := context.Background()
		e.StageTimeout = 40 * time.Millisecond
		var tcalls, dcalls atomic.Int32
		e.Hooks.Transform = func(_ context.Context, _ *Source, _ *Batch, r []Row) ([]Row, string, error) {
			if tcalls.Add(1) == 1 {
				panic("nil map write")
			}
			return r, "v1", nil
		}
		e.Hooks.Deliver = func(ctx context.Context, _ *Source, b *Batch, r []Row) (string, error) {
			if dcalls.Add(1) == 1 {
				time.Sleep(300 * time.Millisecond) // ignores its context: a hung call
			}
			return b.Key, nil
		}
		res, _ := e.Ingest(ctx, admin, "orders", "p1", rows(t, goodCSV))
		b, _ := e.RunAll(ctx, admin, res.Batch.ID)
		if b.Status != StatusRetrying || !strings.Contains(b.LastError, "panicked: nil map write") {
			t.Fatalf("panic: %+v", b)
		}
		for i := 0; i < 8 && b.Status != StatusDelivered; i++ {
			clk.advance(time.Minute)
			sweepAll(e, 10)
			b, _ = e.Store.GetBatch(ctx, b.ID)
		}
		if b.Status != StatusDelivered {
			t.Fatalf("did not recover: %s %s", b.Status, b.LastError)
		}
		timedOut := false
		for _, f := range b.Failures {
			timedOut = timedOut || strings.Contains(f.Error, "did not answer within")
		}
		if !timedOut {
			t.Fatalf("the hung deliver should be a recorded failure: %+v", b.Failures)
		}
		if counter(t, e.Store, "etl_hook_panics_total", "*") != 1 || counter(t, e.Store, "etl_hook_timeouts_total", "*") != 1 {
			t.Fatal("panic and timeout should each be counted once")
		}
	})
}

func TestACircuitBreakerSparesAFailingDestination(t *testing.T) {
	eachStore(t, func(t *testing.T, e *Engine, clk *clock) {
		ctx := context.Background()
		e.BreakerThreshold, e.BreakerCooldown = 3, time.Minute
		var calls atomic.Int32
		var failing atomic.Bool
		failing.Store(true)
		e.Hooks.Deliver = func(_ context.Context, _ *Source, b *Batch, _ []Row) (string, error) {
			calls.Add(1)
			if failing.Load() {
				return "", errors.New("connection refused")
			}
			return b.Key, nil
		}
		var ids []string
		for i := 0; i < 6; i++ {
			res, _ := e.Ingest(ctx, admin, "orders", fmt.Sprint("cb", i), rows(t, goodCSV+fmt.Sprintf("%d,1,NPR,x@y.z\n", 300+i)))
			ids = append(ids, res.Batch.ID)
		}
		sweepAll(e, 30) // the first three fail and open the circuit; the rest wait
		if calls.Load() != 3 {
			t.Fatalf("the destination was called %d times; the circuit should have stopped it at 3", calls.Load())
		}
		waiting := 0
		for _, id := range ids {
			b, _ := e.Store.GetBatch(ctx, id)
			if b.Status == StatusInFlight && b.Stage == StageDelivery {
				waiting++
				if b.Attempts != 0 {
					t.Fatalf("waiting for an open circuit must not use attempts: %+v", b)
				}
			}
		}
		if waiting != 3 {
			t.Fatalf("%d batches waiting, want 3", waiting)
		}
		if c := e.Circuits(ctx); len(c) != 1 || c[0].State != "open" {
			t.Fatalf("circuits %+v", c)
		}
		v, _ := e.Monitor(ctx, admin, time.Hour)
		kinds := map[string]bool{}
		for _, a := range v.Alerts {
			kinds[a.Kind] = true
		}
		if !kinds["circuit"] {
			t.Fatalf("alerts %+v", v.Alerts)
		}
		// The destination recovers; after the cool-down everything flows.
		failing.Store(false)
		clk.advance(2 * time.Minute)
		for i := 0; i < 10; i++ {
			sweepAll(e, 30)
			clk.advance(time.Minute)
		}
		for _, id := range ids {
			if b, _ := e.Store.GetBatch(ctx, id); b.Status != StatusDelivered {
				t.Fatalf("%s is %s: %s", id, b.Status, b.LastError)
			}
		}
		if c := e.Circuits(ctx); len(c) != 0 {
			t.Fatalf("circuit should have closed: %+v", c)
		}
		if counter(t, e.Store, "etl_circuit_opened_total", "*") < 1 || counter(t, e.Store, "etl_circuit_waits_total", "*") < 3 {
			t.Fatal("circuit events should be counted")
		}
	})
}

func TestBusinessRulesAreEnforcedBetweenStages(t *testing.T) {
	eachStore(t, func(t *testing.T, e *Engine, _ *clock) {
		ctx := context.Background()
		src := ordersSource()
		lo := 1.0
		src.OutputRules = []Rule{{Name: "amount_npr present", Field: "amount_npr", Kind: "range", Min: &lo}}
		e.PutSource(ctx, admin, src)

		// 1. a transform that drops rows
		e.Hooks.Transform = func(_ context.Context, _ *Source, _ *Batch, r []Row) ([]Row, string, error) {
			return r[:len(r)-1], "bad", nil
		}
		res, _ := e.Ingest(ctx, admin, "orders", "r1", rows(t, goodCSV))
		b, _ := e.RunAll(ctx, admin, res.Batch.ID)
		if b.Status != StatusHeld || !strings.Contains(b.LastError, "may not add or drop rows") || !b.Failures[0].Permanent {
			t.Fatalf("row count: %+v", b)
		}
		// 2. a transform whose output breaks the contract
		e.Hooks.Transform = func(_ context.Context, _ *Source, _ *Batch, r []Row) ([]Row, string, error) {
			out := make([]Row, len(r))
			for i, row := range r {
				out[i] = Row{"id": row["id"], "amount_npr": 5.0}
			}
			out[2]["amount_npr"] = 0.0
			return out, "bad2", nil
		}
		res, _ = e.Ingest(ctx, admin, "orders", "r2", rows(t, goodCSV))
		b, _ = e.RunAll(ctx, admin, res.Batch.ID)
		if b.Status != StatusHeld || !strings.Contains(b.LastError, "broke the output contract") || !strings.Contains(b.LastError, "row 3") {
			t.Fatalf("contract: %+v", b)
		}
		// 3. a good transform passes, and the books balance
		e.Hooks.Transform = func(_ context.Context, _ *Source, _ *Batch, r []Row) ([]Row, string, error) {
			out := make([]Row, len(r))
			for i, row := range r {
				out[i] = Row{"id": row["id"], "amount_npr": 5.0}
			}
			return out, "good", nil
		}
		res, _ = e.Ingest(ctx, admin, "orders", "r3", rows(t, goodCSV))
		b, _ = e.RunAll(ctx, admin, res.Batch.ID)
		if b.Status != StatusDelivered || b.RowsOut != 4 {
			t.Fatalf("good transform: %+v", b)
		}
		// 4. books that do not balance cannot count as delivered
		e.Hooks.Transform = nil
		res, _ = e.Ingest(ctx, admin, "orders", "r4", rows(t, goodCSV))
		id := res.Batch.ID
		for {
			b, _ = e.Store.GetBatch(ctx, id)
			if b.Stage == StageAudit {
				break
			}
			if _, err := e.Advance(ctx, admin, id); err != nil {
				t.Fatal(err)
			}
		}
		b.Delivered-- // simulate a destination that took one row fewer than it acknowledged
		if err := e.Store.Commit(ctx, Change{Batch: b}); err != nil {
			t.Fatal(err)
		}
		b, _ = e.Advance(ctx, admin, id)
		if b.Status != StatusHeld || !strings.Contains(b.LastError, "reconciliation failed") {
			t.Fatalf("reconciliation: %+v", b)
		}
		if counter(t, e.Store, "etl_contract_violations_total", "*") < 2 || counter(t, e.Store, "etl_reconciliation_failures_total", "*") != 1 {
			t.Fatal("violations should be counted")
		}
	})
}

func TestUploadLimitsAndDuplicateContent(t *testing.T) {
	eachStore(t, func(t *testing.T, e *Engine, _ *clock) {
		ctx := context.Background()
		src := ordersSource()
		src.MaxRows, src.RejectDuplicates = 5, true
		e.PutSource(ctx, admin, src)
		if _, err := e.Ingest(ctx, admin, "orders", "big", rows(t, goodCSV+"5,1,NPR,a@b.c\n6,1,NPR,a@b.c\n")); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "6 rows is more than the 5") {
			t.Fatalf("size: %v", err)
		}
		if _, err := e.Ingest(ctx, admin, "orders", "one", rows(t, goodCSV)); err != nil {
			t.Fatal(err)
		}
		if _, err := e.Ingest(ctx, admin, "orders", "two", rows(t, goodCSV)); !errors.Is(err, ErrDuplicate) {
			t.Fatalf("duplicate under a new key: %v", err)
		}
		if res, err := e.Ingest(ctx, admin, "orders", "one", rows(t, goodCSV)); err != nil || !res.Duplicate {
			t.Fatalf("the same key is still idempotent: %v", err)
		}
		if counter(t, e.Store, "etl_refused_total", `reason="too_many_rows",source="orders"`) != 1 || counter(t, e.Store, "etl_duplicates_rejected_total", "*") != 1 {
			t.Fatal("refusals should be counted")
		}
	})
}

func TestRetentionKeepsWhatAReplayNeeds(t *testing.T) {
	eachStore(t, func(t *testing.T, e *Engine, clk *clock) {
		ctx := context.Background()
		ok, _ := e.Ingest(ctx, admin, "orders", "old-ok", rows(t, goodCSV))
		e.RunAll(ctx, admin, ok.Batch.ID)
		e.Hooks.Deliver = func(context.Context, *Source, *Batch, []Row) (string, error) { return "", Permanent(errors.New("no")) }
		held, _ := e.Ingest(ctx, admin, "orders", "old-held", rows(t, goodCSV+"9,1,NPR,a@b.c\n"))
		e.RunAll(ctx, admin, held.Batch.ID)
		clk.advance(40 * 24 * time.Hour)
		n, err := e.Prune(ctx, 30*24*time.Hour)
		if err != nil || n == 0 {
			t.Fatalf("pruned %d: %v", n, err)
		}
		if _, err := e.Store.LatestCheckpoint(ctx, ok.Batch.ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("a delivered batch's checkpoints should be gone: %v", err)
		}
		if _, err := e.Store.LatestCheckpoint(ctx, held.Batch.ID); err != nil {
			t.Fatalf("a held batch keeps its checkpoints: %v", err)
		}
		if b, _ := e.Store.GetBatch(ctx, ok.Batch.ID); b.Status != StatusDelivered {
			t.Fatal("the batch record stays")
		}
		if bad, _, _ := e.VerifyAudit(ctx, admin); bad != 0 {
			t.Fatal("audit untouched")
		}
	})
}

func TestAStageInProgressFinishesWhenShutdownBegins(t *testing.T) {
	eachStore(t, func(t *testing.T, e *Engine, _ *clock) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		e.Hooks.Deliver = func(_ context.Context, _ *Source, b *Batch, _ []Row) (string, error) {
			cancel() // the process is told to stop while delivering
			time.Sleep(20 * time.Millisecond)
			return "ref-" + b.Key, nil
		}
		res, _ := e.Ingest(context.Background(), admin, "orders", "sd", rows(t, goodCSV))
		var b *Batch
		for i := 0; i < 3; i++ {
			b, _ = e.Advance(context.Background(), admin, res.Batch.ID)
			if b.Stage == StageDelivery {
				break
			}
		}
		b, err := e.Advance(ctx, admin, res.Batch.ID)
		if err != nil || b.Delivered != 4 || b.Stage != StageAudit {
			t.Fatalf("the outcome must be recorded: %+v %v", b, err)
		}
		got, _ := e.Store.GetBatch(context.Background(), res.Batch.ID)
		if got.Ref != "ref-sd" {
			t.Fatalf("stored: %+v", got)
		}
	})
}

func TestTransientErrorsAreRetriedAndOthersAreNot(t *testing.T) {
	for msg, want := range map[string]bool{
		"database is locked (5) (SQLITE_BUSY)": true, "ERROR: deadlock detected (SQLSTATE 40P01)": true, "could not serialize access (SQLSTATE 40001)": true,
		"read tcp: connection reset by peer": true, "UNIQUE constraint failed: x.id": false, "syntax error": false,
	} {
		if transient(errors.New(msg)) != want {
			t.Errorf("transient(%q) should be %v", msg, want)
		}
	}
	s := &SQLStore{}
	calls := 0
	err := s.withRetry(context.Background(), func() error {
		calls++
		if calls < 3 {
			return errors.New("database is locked")
		}
		return nil
	})
	if err != nil || calls != 3 || s.Retries() != 2 {
		t.Fatalf("retry: %v calls=%d retries=%d", err, calls, s.Retries())
	}
	calls = 0
	if err := s.withRetry(context.Background(), func() error { calls++; return errors.New("syntax error") }); err == nil || calls != 1 {
		t.Fatalf("a permanent error must not be retried: %d", calls)
	}
}
