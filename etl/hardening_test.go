package etl

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestOneProcessLearnsFromAnothersFailures(t *testing.T) {
	eachStore(t, func(t *testing.T, e1 *Engine, clk *clock) {
		ctx := context.Background()
		e1.BreakerThreshold, e1.BreakerCooldown = 3, time.Minute
		fail := func(context.Context, *Source, *Batch, []Row) (string, error) { return "", errors.New("down") }
		e1.Hooks.Deliver = fail
		// A second process on the same store, which never saw a failure itself.
		e2 := &Engine{Store: e1.Store, Now: clk.now, Owner: "other", Eval: e1.Eval, Logger: e1.Logger, BreakerThreshold: 3, BreakerCooldown: time.Minute}
		var calls2 atomic.Int32
		e2.Hooks.Deliver = func(context.Context, *Source, *Batch, []Row) (string, error) {
			calls2.Add(1)
			return "", errors.New("down")
		}
		for i := 0; i < 3; i++ {
			res, _ := e1.Ingest(ctx, admin, "orders", fmt.Sprint("s", i), rows(t, goodCSV+fmt.Sprintf("%d,1,NPR,a@b.c\n", 400+i)))
			e1.RunAll(ctx, admin, res.Batch.ID)
		}
		res, _ := e2.Ingest(ctx, admin, "orders", "late", rows(t, goodCSV+"999,1,NPR,a@b.c\n"))
		b, _ := e2.RunAll(ctx, admin, res.Batch.ID)
		if calls2.Load() != 0 {
			t.Fatalf("the second process called a destination the first had found down (%d calls)", calls2.Load())
		}
		if b.Status != StatusInFlight || b.Attempts != 0 {
			t.Fatalf("it should wait without using attempts: %+v", b)
		}
		if c := e2.Circuits(ctx); len(c) != 1 || c[0].State != "open" {
			t.Fatalf("circuits %+v", c)
		}
	})
}

func TestHooksStillRunningAfterTheirTimeoutAreCountedAndCapped(t *testing.T) {
	eachStore(t, func(t *testing.T, e *Engine, _ *clock) {
		ctx := context.Background()
		e.StageTimeout, e.MaxAbandoned = 20*time.Millisecond, 2
		release := make(chan struct{})
		var calls atomic.Int32
		e.Hooks.Transform = func(context.Context, *Source, *Batch, []Row) ([]Row, string, error) {
			calls.Add(1)
			<-release // a receiver that never answers
			return nil, "", nil
		}
		var ids []string
		for i := 0; i < 4; i++ {
			res, _ := e.Ingest(ctx, admin, "orders", fmt.Sprint("h", i), rows(t, goodCSV+fmt.Sprintf("%d,1,NPR,a@b.c\n", 500+i)))
			ids = append(ids, res.Batch.ID)
			e.Advance(ctx, admin, res.Batch.ID) // transform
		}
		if got := calls.Load(); got != 2 {
			t.Fatalf("only 2 hung calls may pile up; the destination was called %d times", got)
		}
		b, _ := e.Store.GetBatch(ctx, ids[3])
		if !strings.Contains(b.LastError, "still running after their timeout") {
			t.Fatalf("the refused call should say why: %q", b.LastError)
		}
		text, _ := e.MetricsText(ctx)
		if !strings.Contains(text, "etl_hooks_abandoned 2") {
			t.Fatalf("abandoned hooks should be a gauge:\n%s", text)
		}
		close(release)
		time.Sleep(50 * time.Millisecond)
		if e.abandon.Load() != 0 {
			t.Fatalf("late hooks that returned should stop counting: %d", e.abandon.Load())
		}
	})
}

func TestCheckpointsAreStoredCompressed(t *testing.T) {
	eachStore(t, func(t *testing.T, e *Engine, _ *clock) {
		ctx := context.Background()
		var csv strings.Builder
		csv.WriteString("id,amount,currency,email\n")
		for i := 0; i < 2000; i++ {
			fmt.Fprintf(&csv, "%d,10.5,NPR,customer%d@example.com\n", i, i%7)
		}
		src := ordersSource()
		src.MaxRejectRate = 1
		e.PutSource(ctx, admin, src)
		res, err := e.Ingest(ctx, admin, "orders", "big", rows(t, csv.String()))
		if err != nil {
			t.Fatal(err)
		}
		cp, err := e.Store.LatestCheckpoint(ctx, res.Batch.ID)
		if err != nil || len(cp.Rows) != 2000 || RowsHash(cp.Rows) != cp.Hash {
			t.Fatalf("round trip: %v %d", err, len(cp.Rows))
		}
		if s, ok := e.Store.(*SQLStore); ok {
			var stored int
			if err := s.db.QueryRow(s.q(`SELECT LENGTH(doc) FROM {p}checkpoints WHERE batch_id = ?`), res.Batch.ID).Scan(&stored); err != nil {
				t.Fatal(err)
			}
			if raw := len(fmt.Sprint(cp.Rows)); stored*4 > raw {
				t.Fatalf("checkpoint takes %d bytes for about %d of rows: not compressed", stored, raw)
			}
		}
		if b, err := e.RunAll(ctx, admin, res.Batch.ID); err != nil || b.Delivered != 2000 {
			t.Fatalf("a compressed checkpoint must still run: %v %+v", err, b)
		}
	})
}

type notice struct{ event, id string }

func TestAlertsHaveAHistoryCanBeAcknowledgedAndAreAnnouncedOnce(t *testing.T) {
	eachStore(t, func(t *testing.T, e *Engine, clk *clock) {
		ctx := context.Background()
		var sent []notice
		var failNext atomic.Bool
		e.Notify = func(_ context.Context, event string, a AlertRecord) error {
			if failNext.Load() {
				return errors.New("mail server down")
			}
			sent = append(sent, notice{event, a.ID})
			return nil
		}
		e.Hooks.Deliver = func(context.Context, *Source, *Batch, []Row) (string, error) {
			return "", Permanent(errors.New("nope"))
		}
		res, _ := e.Ingest(ctx, admin, "orders", "al", rows(t, goodCSV))
		e.RunAll(ctx, admin, res.Batch.ID)
		held := "held:orders:" + res.Batch.ID

		failNext.Store(true)
		if err := e.EvaluateAlerts(ctx); err != nil {
			t.Fatal(err)
		}
		if len(sent) != 0 {
			t.Fatalf("a failed notification must not count as sent: %v", sent)
		}
		failNext.Store(false)
		e.EvaluateAlerts(ctx) // retried
		e.EvaluateAlerts(ctx) // and not again
		var heldNotices int
		for _, n := range sent {
			if n.event == "opened" && n.id == held {
				heldNotices++
			}
		}
		if heldNotices != 1 {
			t.Fatalf("the held alert should be announced exactly once, got %d: %v", heldNotices, sent)
		}

		// A person acknowledges it: it stays, silenced, and says who.
		if err := e.AckAlert(ctx, reader, held, "x", time.Hour); !errors.Is(err, ErrForbidden) {
			t.Fatalf("an analyst cannot acknowledge: %v", err)
		}
		if err := e.AckAlert(ctx, admin, held, "vendor outage, ticket 4412", time.Hour); err != nil {
			t.Fatal(err)
		}
		v, _ := e.Monitor(ctx, admin, time.Hour)
		var found bool
		for _, a := range v.Alerts {
			if a.ID == held {
				found = true
				if a.AckedBy != "ada" || !strings.Contains(a.AckedNote, "4412") {
					t.Fatalf("acknowledgement not shown: %+v", a)
				}
			}
		}
		if !found {
			t.Fatal("an acknowledged alert stays listed")
		}
		if err := e.AckAlert(ctx, admin, "held:orders:b_nonexistent", "x", time.Hour); !errors.Is(err, ErrNotFound) {
			t.Fatalf("acknowledging nothing: %v", err)
		}

		// Fix the cause, replay: the alert clears and the clearing is announced and kept.
		e.Hooks.Deliver = nil
		if _, err := e.Replay(ctx, admin, res.Batch.ID); err != nil {
			t.Fatal(err)
		}
		e.RunAll(ctx, admin, res.Batch.ID)
		e.EvaluateAlerts(ctx)
		hist, err := e.AlertHistory(ctx, admin, 50)
		if err != nil {
			t.Fatal(err)
		}
		var rec *AlertRecord
		for i := range hist {
			if hist[i].ID == held {
				rec = &hist[i]
			}
		}
		if rec == nil || rec.ClearedAt.IsZero() || rec.OpenedAt.IsZero() || rec.AckedBy != "ada" {
			t.Fatalf("history of the held alert: %+v", rec)
		}
		var cleared bool
		for _, n := range sent {
			cleared = cleared || (n.event == "cleared" && n.id == held)
		}
		if !cleared {
			t.Fatalf("the clearing should be announced: %v", sent)
		}
	})
}

func TestMonitoringNumbersComeFromTheStoreNotAScanOfRecentBatches(t *testing.T) {
	eachStore(t, func(t *testing.T, e *Engine, _ *clock) {
		ctx := context.Background()
		src := ordersSource()
		src.MaxRejectRate = 1
		e.PutSource(ctx, admin, src)
		// More batches than any recent-batches scan would look at.
		for i := 0; i < 320; i++ {
			res, err := e.Ingest(ctx, admin, "orders", fmt.Sprint("m", i), rows(t, fmt.Sprintf("id,amount,currency,email\n%d,1,NPR,a@b.c\n", 1000+i)))
			if err != nil {
				t.Fatal(err)
			}
			if i < 300 {
				e.RunAll(ctx, admin, res.Batch.ID)
			}
		}
		v, err := e.Monitor(ctx, admin, 24*time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		h := v.Sources[0]
		if h.Batches != 320 || h.RowsIn != 320 || h.Delivered != 300 {
			t.Fatalf("source totals: %+v", h)
		}
		if v.Stages[3].Runs != 300 || v.Stages[1].Runs != 300 {
			t.Fatalf("stage totals should be exact: %+v", v.Stages)
		}
		if h.AvgSeconds < 0 || h.P95Seconds < 0 {
			t.Fatalf("latency: %+v", h)
		}
		// Old hourly counters are removed by retention; recent ones stay.
		before, _ := e.Store.Counters(ctx)
		e.Store.PruneCounters(ctx, "2099010100")
		after, _ := e.Store.Counters(ctx)
		hourly := func(rows []CounterRow) (n int) {
			for _, r := range rows {
				if strings.HasPrefix(r.Name, HourlyPrefix) {
					n++
				}
			}
			return
		}
		if hourly(before) == 0 || hourly(after) != 0 || len(after) != len(before)-hourly(before) {
			t.Fatalf("hourly counters: before %d after %d", hourly(before), hourly(after))
		}
	})
}
