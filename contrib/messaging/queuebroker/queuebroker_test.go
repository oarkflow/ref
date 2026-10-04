package queuebroker

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oarkflow/fh"

	cfgdec "github.com/oarkflow/ref/contrib/messaging/internal/cfg"
)

func fast() Config {
	return Config{
		Concurrency: 2, PollInterval: cfgdec.Duration(5 * time.Millisecond),
		Retry: Retry{MaxAttempts: 3, Initial: cfgdec.Duration(10 * time.Millisecond), Max: cfgdec.Duration(40 * time.Millisecond), Factor: 2},
	}
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestEnqueueDeliversPayloadAndHeaders(t *testing.T) {
	q, err := Open(fast())
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	got := make(chan *fh.QueueJob, 1)
	q.Register("sms.test", func(_ context.Context, j *fh.QueueJob) error { got <- j; return nil })
	if err := q.Start(); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Enqueue("sms.test", map[string]any{"n": 1}, map[string]string{"x-tenant": "acme"}); err != nil {
		t.Fatal(err)
	}
	select {
	case j := <-got:
		var p map[string]any
		if err := json.Unmarshal(j.Payload, &p); err != nil || p["n"] != float64(1) {
			t.Fatalf("payload %s err %v", j.Payload, err)
		}
		if j.Headers["x-tenant"] != "acme" || j.Attempts != 1 || j.Type != "sms.test" {
			t.Fatalf("job = %+v", j)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("job was not delivered")
	}
}

func TestFailingHandlerRetriesThenDeadLetters(t *testing.T) {
	q, _ := Open(fast())
	defer q.Close()
	var calls atomic.Int32
	var maxAttempt atomic.Int32
	q.Register("sms.fail", func(_ context.Context, j *fh.QueueJob) error {
		calls.Add(1)
		maxAttempt.Store(int32(j.Attempts))
		return errors.New("boom")
	})
	q.Start()
	if _, err := q.Enqueue("sms.fail", map[string]any{"a": 1}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "dead letter", func() bool { return q.DLQDepth("sms.fail") == 1 })
	if calls.Load() != 3 || maxAttempt.Load() != 3 {
		t.Fatalf("calls=%d attempt=%d, want 3 and 3", calls.Load(), maxAttempt.Load())
	}
}

func TestDelayedJobWaits(t *testing.T) {
	q, _ := Open(fast())
	defer q.Close()
	got := make(chan time.Time, 1)
	q.Register("sms.later", func(context.Context, *fh.QueueJob) error { got <- time.Now(); return nil })
	q.Start()
	start := time.Now()
	if _, err := q.EnqueueAfter("sms.later", 1, 300*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	select {
	case at := <-got:
		if at.Sub(start) < 250*time.Millisecond {
			t.Fatalf("delivered after %v, before its delay", at.Sub(start))
		}
	case <-time.After(3 * time.Second):
		t.Fatal("delayed job never ran")
	}
}

func TestQueuedWorkSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	cfg := fast()
	cfg.Dir = dir
	q, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	// Publish with no consumer, then stop: the job must be in the log.
	if _, err := q.Enqueue("sms.durable", map[string]any{"id": "m1"}); err != nil {
		t.Fatal(err)
	}
	if err := q.Close(); err != nil {
		t.Fatal(err)
	}

	q2, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer q2.Close()
	got := make(chan string, 1)
	q2.Register("sms.durable", func(_ context.Context, j *fh.QueueJob) error {
		got <- string(j.Payload)
		return nil
	})
	q2.Start()
	select {
	case p := <-got:
		if p != `{"id":"m1"}` {
			t.Fatalf("payload after restart = %s", p)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("job did not survive the restart")
	}
}

func TestConsumerRulesTuneByGlob(t *testing.T) {
	cfg := fast()
	cfg.Consumers = []Consumer{{ID: "sms.dispatch.*", Concurrency: 9, MaxAttempts: 2}}
	q, _ := Open(cfg)
	defer q.Close()
	conc, attempts, _, _, _ := q.settings("sms.dispatch.np")
	if conc != 9 || attempts != 2 {
		t.Fatalf("dispatch settings = %d/%d", conc, attempts)
	}
	conc, attempts, _, _, _ = q.settings("sms.other")
	if conc != 2 || attempts != 3 {
		t.Fatalf("default settings = %d/%d", conc, attempts)
	}
}
