package etl

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
)

var (
	admin  = Actor{ID: "ada", Roles: []string{RoleAdmin}}
	reader = Actor{ID: "rita", Roles: []string{"read"}}
)

// storeFactories lists the stores every test runs against. sql_test.go adds SQLite and Postgres.
var storeFactories = map[string]func(t *testing.T) Store{
	"memory": func(t *testing.T) Store { return NewMemoryStore() },
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

func testEngine(t *testing.T, st Store) (*Engine, *clock) {
	t.Helper()
	clk := &clock{t: time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC)}
	e := &Engine{Store: st, Now: clk.now, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Backoff: func(base time.Duration, attempt int, cap time.Duration) time.Duration {
		return min(base<<(attempt-1), cap)
	}}
	if err := e.EnsureRoles(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.Eval = func(expr string, row Row) (bool, error) {
		if expr == "amount > 0" {
			f, _ := toFloat(row["amount"])
			return f > 0, nil
		}
		return false, fmt.Errorf("unknown expression %q", expr)
	}
	return e, clk
}

func ordersSource() Source {
	lo := 0.0
	return Source{ID: "orders", Name: "Orders", Owner: "sales-data", Format: "csv", Destination: "billing-api", MaxRejectRate: 0.25,
		Retry: RetryPolicy{MaxAttempts: 3, Base: 2 * time.Second, Cap: time.Minute},
		Rules: []Rule{
			{Name: "id", Field: "id", Kind: "required"},
			{Name: "unique id", Field: "id", Kind: "unique"},
			{Name: "amount type", Field: "amount", Kind: "type", Type: "float"},
			{Name: "amount range", Field: "amount", Kind: "range", Min: &lo},
			{Name: "currency", Field: "currency", Kind: "enum", Values: []string{"NPR", "USD"}},
			{Name: "email", Field: "email", Kind: "regex", Pattern: `^[^@\s]+@[^@\s]+$`},
		}}
}

const goodCSV = "id,amount,currency,email\n1,10.5,NPR,a@x.com\n2,20,USD,b@x.com\n3,5,NPR,c@x.com\n4,7,USD,d@x.com\n"

func eachStore(t *testing.T, fn func(t *testing.T, e *Engine, clk *clock)) {
	for name, mk := range storeFactories {
		t.Run(name, func(t *testing.T) {
			e, clk := testEngine(t, mk(t))
			if _, err := e.PutSource(context.Background(), admin, ordersSource()); err != nil {
				t.Fatal(err)
			}
			fn(t, e, clk)
		})
	}
}

func rows(t *testing.T, s string) []Row {
	t.Helper()
	r, err := ParseRows("csv", []byte(s))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestHappyPathDeliversEverythingWithLineage(t *testing.T) {
	eachStore(t, func(t *testing.T, e *Engine, _ *clock) {
		ctx := context.Background()
		res, err := e.Ingest(ctx, admin, "orders", "k1", rows(t, goodCSV))
		if err != nil {
			t.Fatal(err)
		}
		b, err := e.RunAll(ctx, admin, res.Batch.ID)
		if err != nil {
			t.Fatal(err)
		}
		if b.Status != StatusDelivered || b.Delivered != 4 || b.Stage != StageDone {
			t.Fatalf("got %+v", b)
		}
		tr, _ := e.Trace(ctx, admin, b.ID)
		if len(tr.Lineage) < 3 || !strings.HasPrefix(tr.Lineage[len(tr.Lineage)-1].Child, "dest:billing-api/") {
			t.Fatalf("lineage %+v", tr.Lineage)
		}
		sum, _ := e.Summary(ctx, admin)
		if sum.Delivered != 4 || sum.RowsLost != 0 {
			t.Fatalf("summary %+v", sum)
		}
		if bad, total, _ := e.VerifyAudit(ctx, admin); bad != 0 || total < 3 {
			t.Fatalf("audit bad=%d total=%d", bad, total)
		}
	})
}

func TestValidationQuarantinesBadRowsAndKeepsReasons(t *testing.T) {
	eachStore(t, func(t *testing.T, e *Engine, _ *clock) {
		ctx := context.Background()
		csv := goodCSV + "5,-3,NPR,e@x.com\n"
		res, err := e.Ingest(ctx, admin, "orders", "k2", rows(t, csv))
		if err != nil {
			t.Fatal(err)
		}
		b := res.Batch
		if b.RowsIn != 5 || b.Quarantined != 1 || b.Status != StatusInFlight {
			t.Fatalf("got %+v", b)
		}
		q, _ := e.Store.ListQuarantine(ctx, b.ID, nil, 10)
		if len(q) != 1 || q[0].Rule != "amount range" || q[0].RowNo != 5 || !strings.Contains(q[0].Reason, "below") {
			t.Fatalf("quarantine %+v", q)
		}
		done, _ := e.RunAll(ctx, admin, b.ID)
		if done.Delivered != 4 {
			t.Fatalf("delivered %d", done.Delivered)
		}
		if sum, _ := e.Summary(ctx, admin); sum.RowsLost != 0 {
			t.Fatalf("lost %d", sum.RowsLost)
		}
	})
}

func TestTooManyRefusedRowsFailsTheBatch(t *testing.T) {
	eachStore(t, func(t *testing.T, e *Engine, _ *clock) {
		ctx := context.Background()
		csv := "id,amount,currency,email\n1,1,XXX,a@x.com\n2,1,XXX,b@x.com\n3,1,NPR,c@x.com\n"
		res, _ := e.Ingest(ctx, admin, "orders", "k3", rows(t, csv))
		if res.Batch.Status != StatusFailed || res.Batch.LastError == "" {
			t.Fatalf("got %+v", res.Batch)
		}
		if _, err := e.Advance(ctx, admin, res.Batch.ID); !errors.Is(err, ErrState) {
			t.Fatalf("err %v", err)
		}
		if sum, _ := e.Summary(ctx, admin); sum.RowsLost != 0 {
			t.Fatalf("lost %d", sum.RowsLost)
		}
	})
}

func TestIdempotentIngest(t *testing.T) {
	eachStore(t, func(t *testing.T, e *Engine, _ *clock) {
		ctx := context.Background()
		a, _ := e.Ingest(ctx, admin, "orders", "same", rows(t, goodCSV))
		b, err := e.Ingest(ctx, admin, "orders", "same", rows(t, goodCSV))
		if err != nil || !b.Duplicate || b.Batch.ID != a.Batch.ID {
			t.Fatalf("dup %+v %v", b, err)
		}
		if _, err := e.Ingest(ctx, admin, "orders", "same", rows(t, goodCSV+"9,1,NPR,z@x.com\n")); !errors.Is(err, ErrKeyReused) {
			t.Fatalf("err %v", err)
		}
	})
}

func TestRetryThenHoldThenReplayLosesNothing(t *testing.T) {
	eachStore(t, func(t *testing.T, e *Engine, clk *clock) {
		ctx := context.Background()
		down, delivered := true, 0
		e.Hooks.Deliver = func(ctx context.Context, src *Source, b *Batch, rows []Row) (string, error) {
			if down {
				return "", errors.New("billing-api: connection refused")
			}
			delivered += len(rows)
			return "ref-" + b.Key, nil
		}
		res, _ := e.Ingest(ctx, admin, "orders", "k4", rows(t, goodCSV))
		id := res.Batch.ID
		b, _ := e.RunAll(ctx, admin, id) // transform, transfer, then delivery fails
		if b.Status != StatusRetrying || b.Attempts != 1 || b.Stage != StageDelivery {
			t.Fatalf("got %+v", b)
		}
		if _, err := e.Advance(ctx, admin, id); !errors.Is(err, ErrNotDue) {
			t.Fatalf("expected ErrNotDue, got %v", err)
		}
		clk.advance(2 * time.Second)
		if n, _ := e.Sweep(ctx, 10); n != 1 {
			t.Fatalf("sweep %d", n)
		}
		b, _ = e.Store.GetBatch(ctx, id)
		if b.Attempts != 2 || !b.NextAttemptAt.Equal(clk.now().Add(4*time.Second)) {
			t.Fatalf("backoff %+v", b)
		}
		clk.advance(4 * time.Second)
		e.Sweep(ctx, 10)
		b, _ = e.Store.GetBatch(ctx, id)
		if b.Status != StatusHeld || b.Attempts != 3 {
			t.Fatalf("expected held, got %+v", b)
		}
		if sum, _ := e.Summary(ctx, admin); sum.ByStatus[StatusHeld] != 1 || sum.RowsLost != 0 {
			t.Fatalf("summary %+v", sum)
		}
		if _, err := e.Replay(ctx, Actor{ID: "ingest-bot", Roles: []string{"ingest"}}, id); !errors.Is(err, ErrForbidden) {
			t.Fatalf("replay without role: %v", err)
		}
		down = false
		if _, err := e.Replay(ctx, admin, id); err != nil {
			t.Fatal(err)
		}
		b, err := e.RunAll(ctx, admin, id)
		if err != nil || b.Status != StatusDelivered || b.Delivered != 4 || delivered != 4 {
			t.Fatalf("after replay %+v err=%v delivered=%d", b, err, delivered)
		}
		if bad, _, _ := e.VerifyAudit(ctx, admin); bad != 0 {
			t.Fatalf("audit broken at %d", bad)
		}
		tr, _ := e.Trace(ctx, admin, id)
		f := tr.Batch.Failures
		if len(f) != 3 || f[0].Outcome != "retry" || f[2].Outcome != "held" || f[2].Attempt != 3 || f[2].StageName != StageNames[StageDelivery] ||
			!strings.Contains(f[2].Reason, "3 attempts") || !strings.Contains(f[0].Error, "connection refused") || f[0].RetryAt.IsZero() {
			t.Fatalf("failure history %+v", f)
		}
		if tr.Resume == nil || tr.Resume.Stage != StageTransfer || tr.Resume.Rows != 4 {
			t.Fatalf("resume %+v", tr.Resume)
		}
	})
}

func TestSweepDrivesBatchesAndHonoursStageDelay(t *testing.T) {
	eachStore(t, func(t *testing.T, e *Engine, clk *clock) {
		ctx := context.Background()
		e.StageDelay = 3 * time.Second
		res, _ := e.Ingest(ctx, admin, "orders", "sw", rows(t, goodCSV))
		for i := 0; i < 8; i++ {
			n, _ := e.Sweep(ctx, 10)
			if n == 0 {
				break
			}
		}
		b, _ := e.Store.GetBatch(ctx, res.Batch.ID)
		if b.Stage != StageTransfer {
			t.Fatalf("one stage per delay expected, got stage %d", b.Stage)
		}
		for i := 0; i < 8; i++ {
			clk.advance(3 * time.Second)
			e.Sweep(ctx, 10)
		}
		b, _ = e.Store.GetBatch(ctx, res.Batch.ID)
		if b.Status != StatusDelivered {
			t.Fatalf("got %+v", b)
		}
	})
}

func TestPermanentErrorHoldsImmediately(t *testing.T) {
	eachStore(t, func(t *testing.T, e *Engine, _ *clock) {
		ctx := context.Background()
		e.Hooks.Transfer = func(context.Context, *Source, *Batch, []Row) error {
			return Permanent(errors.New("certificate rejected"))
		}
		res, _ := e.Ingest(ctx, admin, "orders", "k5", rows(t, goodCSV))
		b, _ := e.RunAll(ctx, admin, res.Batch.ID)
		if b.Status != StatusHeld || b.Attempts != 1 {
			t.Fatalf("got %+v", b)
		}
	})
}

func TestRolesAreEnforced(t *testing.T) {
	eachStore(t, func(t *testing.T, e *Engine, _ *clock) {
		ctx := context.Background()
		if _, err := e.Ingest(ctx, reader, "orders", "x", rows(t, goodCSV)); !errors.Is(err, ErrForbidden) {
			t.Fatalf("reader ingest: %v", err)
		}
		if _, err := e.Summary(ctx, Actor{ID: "x"}); !errors.Is(err, ErrForbidden) {
			t.Fatalf("no roles: %v", err)
		}
		if _, err := e.PutSource(ctx, reader, ordersSource()); !errors.Is(err, ErrForbidden) {
			t.Fatalf("reader put: %v", err)
		}
		if _, err := e.Summary(ctx, reader); err != nil {
			t.Fatal(err)
		}
	})
}

func TestPausedSourceRefusesBatches(t *testing.T) {
	eachStore(t, func(t *testing.T, e *Engine, _ *clock) {
		ctx := context.Background()
		if _, err := e.SetPaused(ctx, admin, "orders", true); err != nil {
			t.Fatal(err)
		}
		if _, err := e.Ingest(ctx, admin, "orders", "p", rows(t, goodCSV)); !errors.Is(err, ErrState) {
			t.Fatalf("err %v", err)
		}
	})
}

func TestStaleRevisionIsRefused(t *testing.T) {
	eachStore(t, func(t *testing.T, e *Engine, _ *clock) {
		ctx := context.Background()
		res, _ := e.Ingest(ctx, admin, "orders", "k6", rows(t, goodCSV))
		a, _ := e.Store.GetBatch(ctx, res.Batch.ID)
		b, _ := e.Store.GetBatch(ctx, res.Batch.ID)
		a.Status = StatusHeld
		if err := e.Store.Commit(ctx, Change{Batch: a}); err != nil {
			t.Fatal(err)
		}
		if err := e.Store.Commit(ctx, Change{Batch: b}); !errors.Is(err, ErrConflict) {
			t.Fatalf("expected conflict, got %v", err)
		}
	})
}

func TestAuditChainDetectsTampering(t *testing.T) {
	m := NewMemoryStore()
	e, _ := testEngine(t, m)
	ctx := context.Background()
	e.PutSource(ctx, admin, ordersSource())
	res, _ := e.Ingest(ctx, admin, "orders", "k7", rows(t, goodCSV))
	e.RunAll(ctx, admin, res.Batch.ID)
	if bad, _, _ := e.VerifyAudit(ctx, admin); bad != 0 {
		t.Fatalf("bad %d before tampering", bad)
	}
	m.audit[1].Detail = "nothing happened here"
	if bad, _, _ := e.VerifyAudit(ctx, admin); bad != 2 {
		t.Fatalf("expected seq 2, got %d", bad)
	}
}

func TestParseFormats(t *testing.T) {
	for format, data := range map[string]string{
		"jsonl": "{\"a\":1}\n\n{\"a\":2}\n",
		"json":  `[{"a":1},{"a":2}]`,
		"csv":   "a\n1\n2\n",
	} {
		r, err := ParseRows(format, []byte(data))
		if err != nil || len(r) != 2 {
			t.Fatalf("%s: %v %v", format, r, err)
		}
	}
	if _, err := ParseRows("json", []byte("{")); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
}
