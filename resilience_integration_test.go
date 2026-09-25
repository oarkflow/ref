package ref_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oarkflow/ref"
	"github.com/oarkflow/ref/capability"
	"github.com/oarkflow/ref/execution"
	"github.com/oarkflow/ref/fact"
	"github.com/oarkflow/ref/intent"
	"github.com/oarkflow/ref/invocation"
)

// This file verifies end to end, through the real capability.Registration ->
// runtime.Engine.compileIntent -> execution.Program -> Scheduler path, that
// capability.Resilience (WithTimeout / WithRetry) is (a) preserved by graph
// compilation and (b) actually enforced at dispatch time — not merely
// declared and ignored.

type slowFactInput struct{}
type slowFactOutput struct{ Value string }

var slowFactKey = ref.NewKey[string]("test.resilience.slow_fact")

type slowFactIntent struct{}

func (slowFactIntent) Name() intent.Name { return "test.resilience.slow" }
func (slowFactIntent) Spec() intent.Spec {
	return intent.Spec{Requires: []fact.AnyKey{slowFactKey.Any()}}
}
func (slowFactIntent) Run(nc *ref.NodeContext, in slowFactInput) (ref.Outcome[slowFactOutput], error) {
	v, err := ref.Require(nc, slowFactKey)
	if err != nil {
		return ref.Outcome[slowFactOutput]{}, err
	}
	return ref.Outcome[slowFactOutput]{Value: slowFactOutput{Value: v}}, nil
}

// TestResilienceTimeoutEnforcedEndToEnd proves a capability.Registration
// built with capability.WithTimeout survives compilation into the Plan and
// is actually enforced by the scheduler when Run legitimately overruns.
func TestResilienceTimeoutEnforcedEndToEnd(t *testing.T) {
	engine := ref.NewEngine()

	slowCap := capability.Pure("slow.fact", capability.WithTimeout(20*time.Millisecond))
	slowCap.Provides = []fact.AnyKey{slowFactKey.Any()}
	slowCap.Run = func(nc *execution.NodeContext) error {
		time.Sleep(200 * time.Millisecond)
		execution.Publish(nc, slowFactKey, "too-late")
		return nil
	}

	if err := ref.RegisterCapability(engine, slowCap); err != nil {
		t.Fatalf("register capability: %v", err)
	}
	if err := ref.Register(engine, slowFactIntent{}); err != nil {
		t.Fatalf("register intent: %v", err)
	}
	if err := engine.Compile(); err != nil {
		t.Fatalf("compile: %v", err)
	}

	// Prerequisite check: Resilience must have survived compilation into
	// the Program the scheduler actually runs.
	plan, ok := engine.Plan(slowFactIntent{}.Name())
	if !ok {
		t.Fatalf("expected plan for intent")
	}
	_ = plan // Plan itself is resilience-agnostic; enforcement lives in Program.Resilience.

	_, err := engine.Dispatch(context.Background(), &invocation.Invocation{
		ID:     "inv-timeout",
		Intent: invocation.IntentID(slowFactIntent{}.Name()),
	})
	if err == nil {
		t.Fatalf("expected dispatch to fail due to node timeout")
	}
	if !errors.Is(err, execution.ErrNodeTimeout) {
		t.Fatalf("expected error to wrap execution.ErrNodeTimeout, got: %v", err)
	}

	// Let the abandoned slow goroutine finish so it doesn't outlive the test.
	time.Sleep(250 * time.Millisecond)
}

// TestResilienceRetryEnforcedEndToEnd proves capability.WithRetry causes a
// transiently failing capability to be retried the configured number of
// times before the intent ultimately succeeds.
func TestResilienceRetryEnforcedEndToEnd(t *testing.T) {
	engine := ref.NewEngine()

	var calls int32
	flakyCap := capability.Pure("flaky.fact", capability.WithRetry(3, 2*time.Millisecond))
	flakyCap.Provides = []fact.AnyKey{slowFactKey.Any()}
	flakyCap.Run = func(nc *execution.NodeContext) error {
		n := atomic.AddInt32(&calls, 1)
		if n <= 2 {
			return errors.New("transient")
		}
		execution.Publish(nc, slowFactKey, "ok")
		return nil
	}

	if err := ref.RegisterCapability(engine, flakyCap); err != nil {
		t.Fatalf("register capability: %v", err)
	}
	if err := ref.Register(engine, slowFactIntent{}); err != nil {
		t.Fatalf("register intent: %v", err)
	}
	if err := engine.Compile(); err != nil {
		t.Fatalf("compile: %v", err)
	}

	res, err := engine.Dispatch(context.Background(), &invocation.Invocation{
		ID:     "inv-retry",
		Intent: invocation.IntentID(slowFactIntent{}.Name()),
	})
	if err != nil {
		t.Fatalf("expected eventual success via retry, got error: %v", err)
	}
	out, ok := res.Value.(slowFactOutput)
	if !ok || out.Value != "ok" {
		t.Fatalf("unexpected result: %#v", res.Value)
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Fatalf("expected exactly 3 calls (2 failures + 1 success), got %d", got)
	}
}
