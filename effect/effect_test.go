package effect_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oarkflow/ref/effect"
)

type mockEffect struct {
	name      string
	kind      effect.EffectKind
	committed atomic.Bool
	err       error
}

func (m *mockEffect) Name() string            { return m.name }
func (m *mockEffect) Kind() effect.EffectKind { return m.kind }
func (m *mockEffect) Commit(ctx context.Context) error {
	m.committed.Store(true)
	return m.err
}

type encodedMockEffect struct {
	mockEffect
	payload []byte
}

func (m *encodedMockEffect) EncodeEffect() ([]byte, string, bool, error) {
	return append([]byte(nil), m.payload...), "stable-key", true, nil
}

type mockCompensatingEffect struct {
	mockEffect
	compensated atomic.Bool
}

func (m *mockCompensatingEffect) Compensate(ctx context.Context) error {
	m.compensated.Store(true)
	return nil
}

func TestCommitPlanSuccess(t *testing.T) {
	store := effect.NewMemoryEffectStore()

	e1 := &mockEffect{name: "db-insert", kind: effect.LocalTransactional}
	e2 := &mockEffect{name: "webhook", kind: effect.DurableDelivery}
	e3 := &mockEffect{name: "metric", kind: effect.FireAndForget}

	err := effect.CommitPlan(context.Background(), store, "exec-1", []effect.Effect{e1, e2, e3})
	if err != nil {
		t.Fatalf("unexpected commit plan error: %v", err)
	}

	if !e1.committed.Load() || !e2.committed.Load() || !e3.committed.Load() {
		t.Errorf("all effects should have been committed")
	}
}

func TestCommitPlanCompensation(t *testing.T) {
	store := effect.NewMemoryEffectStore()

	e1 := &mockCompensatingEffect{mockEffect: mockEffect{name: "reserve-inventory", kind: effect.LocalTransactional}}
	e2 := &mockEffect{name: "charge-card", kind: effect.LocalTransactional, err: errors.New("insufficient funds")}

	err := effect.CommitPlan(context.Background(), store, "exec-2", []effect.Effect{e1, e2})
	if err == nil {
		t.Fatalf("expected error from charge-card failure, got nil")
	}

	if !e1.compensated.Load() {
		t.Errorf("expected e1 to be compensated when subsequent transactional effect failed")
	}
}

func TestCommitPlanErrorReporting(t *testing.T) {
	store := effect.NewMemoryEffectStore()

	var reported []effect.EffectError
	reporter := func(err effect.EffectError) {
		reported = append(reported, err)
	}

	// Durable effect whose Commit fails — error should be reported, not swallowed.
	e1 := &mockEffect{name: "webhook-fail", kind: effect.DurableDelivery, err: errors.New("connection refused")}
	e2 := &mockEffect{name: "metric", kind: effect.FireAndForget, err: errors.New("metric drop")}

	err := effect.CommitPlan(context.Background(), store, "exec-3", []effect.Effect{e1, e2}, reporter)
	if err != nil {
		t.Fatalf("non-fatal errors should not abort commit plan: %v", err)
	}

	if len(reported) != 2 {
		t.Fatalf("expected 2 reported errors, got %d", len(reported))
	}
	if reported[0].Phase != "durable_commit" || reported[0].Name != "webhook-fail" {
		t.Errorf("first report mismatch: %+v", reported[0])
	}
	if reported[1].Phase != "fire_and_forget" || reported[1].Name != "metric" {
		t.Errorf("second report mismatch: %+v", reported[1])
	}
}

func TestCommitPlanNoReporterSilent(t *testing.T) {
	store := effect.NewMemoryEffectStore()

	// Without a reporter, errors should be silently discarded (backward compat).
	e1 := &mockEffect{name: "webhook", kind: effect.DurableDelivery, err: errors.New("fail")}
	err := effect.CommitPlan(context.Background(), store, "exec-4", []effect.Effect{e1})
	if err != nil {
		t.Fatalf("non-fatal error should not abort commit plan: %v", err)
	}
}

func TestEffectErrorMessage(t *testing.T) {
	e := effect.EffectError{Phase: "schedule_delivery", Err: errors.New("db down")}
	if e.Error() != "ref: effect schedule_delivery: db down" {
		t.Errorf("unexpected error message: %s", e.Error())
	}
	if e.Unwrap() == nil {
		t.Error("expected Unwrap to return the underlying error")
	}

	e2 := effect.EffectError{Phase: "durable_commit", Name: "send-email", Err: errors.New("timeout")}
	if e2.Error() != `ref: effect durable_commit "send-email": timeout` {
		t.Errorf("unexpected error message: %s", e2.Error())
	}
}

func TestEffectPlanRejectsMismatchedGroup(t *testing.T) {
	plan := effect.EffectPlan{LocalTx: []effect.Effect{&mockEffect{name: "webhook", kind: effect.DurableDelivery}}}
	if err := plan.Validate(); err == nil {
		t.Fatal("expected mismatched effect group error")
	}
}

func TestMemoryEffectStoreReclaimsExpiredDeliveryLease(t *testing.T) {
	store := effect.NewMemoryEffectStore()
	txID, err := store.Begin(context.Background(), "exec-lease")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Record(context.Background(), txID, effect.EffectRecord{Name: "webhook", Kind: effect.DurableDelivery}); err != nil {
		t.Fatal(err)
	}
	if err := store.Commit(context.Background(), txID); err != nil {
		t.Fatal(err)
	}
	first, err := store.ClaimDeliveries(context.Background(), "owner-a", 1, time.Millisecond)
	if err != nil || len(first) != 1 {
		t.Fatalf("first claim failed: %+v err=%v", first, err)
	}
	time.Sleep(3 * time.Millisecond)
	second, err := store.ClaimDeliveries(context.Background(), "owner-b", 1, time.Second)
	if err != nil || len(second) != 1 {
		t.Fatalf("expired lease was not reclaimed: %+v err=%v", second, err)
	}
	if second[0].ClaimToken == first[0].ClaimToken {
		t.Fatal("expected a new claim token")
	}
}

func TestRunnerDeliversDurableEffect(t *testing.T) {
	store := effect.NewMemoryEffectStore()
	delivered := make(chan struct{}, 1)
	runner := effect.NewRunner(store, effect.WithEffectResolver("webhook", func(record effect.EffectRecord) (effect.Effect, error) {
		return &callbackEffect{done: delivered}, nil
	}))
	defer runner.Close()

	plan := effect.EffectPlan{Durable: []effect.Effect{&mockEffect{name: "webhook", kind: effect.DurableDelivery}}}
	if err := runner.Run(context.Background(), "exec-delivery", plan); err != nil {
		t.Fatalf("run failed: %v", err)
	}
	select {
	case <-delivered:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for durable delivery")
	}
}

func TestRunnerRecoversUnscheduledTransaction(t *testing.T) {
	store := effect.NewMemoryEffectStore()
	txID, err := store.Begin(context.Background(), "exec-recovery")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Record(context.Background(), txID, effect.EffectRecord{Name: "webhook", Kind: effect.DurableDelivery}); err != nil {
		t.Fatal(err)
	}
	if err := store.Commit(context.Background(), txID); err != nil {
		t.Fatal(err)
	}
	delivered := make(chan struct{}, 1)
	runner := effect.NewRunner(store, effect.WithEffectResolver("webhook", func(effect.EffectRecord) (effect.Effect, error) {
		return &callbackEffect{done: delivered}, nil
	}))
	defer runner.Close()
	if err := runner.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-delivered:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for recovered delivery")
	}
}

type callbackEffect struct {
	done chan<- struct{}
}

func (e *callbackEffect) Name() string            { return "webhook" }
func (e *callbackEffect) Kind() effect.EffectKind { return effect.DurableDelivery }
func (e *callbackEffect) Commit(context.Context) error {
	select {
	case e.done <- struct{}{}:
	default:
	}
	return nil
}
