package execution_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oarkflow/ref/execution"
	"github.com/oarkflow/ref/graph"
	"github.com/oarkflow/ref/invocation"
)

func singleNodePlan(t *testing.T, name string, kind graph.NodeKind) *graph.Plan {
	t.Helper()
	node := &graph.Node{ID: 0, Name: name, Kind: kind}
	g, err := graph.Build([]*graph.Node{node}, 0)
	if err != nil {
		t.Fatalf("build error: %v", err)
	}
	plan, err := graph.Compile(g, name, 1)
	if err != nil {
		t.Fatalf("compile error: %v", err)
	}
	return plan
}

// TestResilienceZeroValueUnchanged is the most important regression test:
// a Resilience-indexed slice full of zero values must behave exactly like
// no Resilience at all — no timeout, no retry, no bulkhead.
func TestResilienceZeroValueUnchanged(t *testing.T) {
	plan := singleNodePlan(t, "plain", graph.PureNode)

	var calls int32
	runners := []execution.NodeExecutor{
		func(nc *execution.NodeContext) error {
			atomic.AddInt32(&calls, 1)
			return nil
		},
	}
	resilience := []execution.Resilience{{}} // explicit zero value

	sched := execution.NewScheduler()
	outcome, err := sched.ExecuteWithResilience(context.Background(), &invocation.Invocation{Intent: "plain"}, plan, runners, resilience, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if outcome.State != execution.StateCompleted {
		t.Fatalf("expected StateCompleted, got %v", outcome.State)
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("expected exactly 1 call, got %d", calls)
	}

	// Also verify a nil resilience slice (the normal unconfigured path)
	// behaves identically.
	atomic.StoreInt32(&calls, 0)
	outcome2, err := sched.Execute(context.Background(), &invocation.Invocation{Intent: "plain"}, plan, runners, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if outcome2.State != execution.StateCompleted {
		t.Fatalf("expected StateCompleted, got %v", outcome2.State)
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("expected exactly 1 call, got %d", calls)
	}
}

// TestResilienceTimeout confirms a node with WithTimeout-equivalent policy
// that legitimately runs long reports a timeout error through the normal
// error/outcome path.
func TestResilienceTimeout(t *testing.T) {
	plan := singleNodePlan(t, "slow", graph.PureNode)

	runners := []execution.NodeExecutor{
		func(nc *execution.NodeContext) error {
			time.Sleep(200 * time.Millisecond)
			return nil
		},
	}
	resilience := []execution.Resilience{{Timeout: 20 * time.Millisecond}}

	sched := execution.NewScheduler()
	outcome, err := sched.ExecuteWithResilience(context.Background(), &invocation.Invocation{Intent: "slow"}, plan, runners, resilience, nil)
	if !errors.Is(err, execution.ErrNodeTimeout) {
		t.Fatalf("expected ErrNodeTimeout, got %v", err)
	}
	if outcome.State != execution.StateFailed {
		t.Fatalf("expected StateFailed, got %v", outcome.State)
	}
	// Give the abandoned goroutine time to finish so it doesn't leak past
	// the test (it is intentionally not waited on by the scheduler).
	time.Sleep(250 * time.Millisecond)
}

// TestResilienceRetrySucceedsEventually confirms WithRetry-equivalent policy
// retries a failing Run exactly the right number of times before succeeding,
// and that jitter varies the delay across repeated invocations.
func TestResilienceRetrySucceedsEventually(t *testing.T) {
	var calls int32
	failUntil := int32(3) // fail attempts 1..3, succeed on attempt 4

	makeRunner := func() execution.NodeExecutor {
		return func(nc *execution.NodeContext) error {
			n := atomic.AddInt32(&calls, 1)
			if n <= failUntil {
				return errors.New("transient failure")
			}
			return nil
		}
	}

	plan := singleNodePlan(t, "flaky", graph.PureNode)
	resilience := []execution.Resilience{{MaxRetries: 5, Backoff: 5 * time.Millisecond}}

	sched := execution.NewScheduler()
	outcome, err := sched.ExecuteWithResilience(context.Background(), &invocation.Invocation{Intent: "flaky"}, plan, []execution.NodeExecutor{makeRunner()}, resilience, nil)
	if err != nil {
		t.Fatalf("expected eventual success, got error: %v", err)
	}
	if outcome.State != execution.StateCompleted {
		t.Fatalf("expected StateCompleted, got %v", outcome.State)
	}
	if got := atomic.LoadInt32(&calls); got != failUntil+1 {
		t.Fatalf("expected exactly %d calls (3 failures + 1 success), got %d", failUntil+1, got)
	}
}

// TestResilienceRetryExhausted confirms retries stop after MaxRetries and
// the final error surfaces through the normal error path.
func TestResilienceRetryExhausted(t *testing.T) {
	var calls int32
	plan := singleNodePlan(t, "always-fails", graph.PureNode)
	runners := []execution.NodeExecutor{
		func(nc *execution.NodeContext) error {
			atomic.AddInt32(&calls, 1)
			return errors.New("permanent failure")
		},
	}
	resilience := []execution.Resilience{{MaxRetries: 2, Backoff: time.Millisecond}}

	sched := execution.NewScheduler()
	outcome, err := sched.ExecuteWithResilience(context.Background(), &invocation.Invocation{Intent: "always-fails"}, plan, runners, resilience, nil)
	if err == nil {
		t.Fatalf("expected error after exhausting retries")
	}
	if outcome.State != execution.StateFailed {
		t.Fatalf("expected StateFailed, got %v", outcome.State)
	}
	// 1 initial attempt + 2 retries = 3 calls.
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Fatalf("expected exactly 3 calls, got %d", got)
	}
}

// TestResilienceRetryJitterVaries checks that the backoff delay used by
// retries is not always identical across repeated runs (i.e. jitter is
// actually applied), without asserting on exact timing.
func TestResilienceRetryJitterVaries(t *testing.T) {
	observe := func() time.Duration {
		var calls int32
		var firstRetryAt time.Time
		start := time.Now()
		plan := singleNodePlan(t, "jitter", graph.PureNode)
		runners := []execution.NodeExecutor{
			func(nc *execution.NodeContext) error {
				n := atomic.AddInt32(&calls, 1)
				if n == 1 {
					return errors.New("fail once")
				}
				firstRetryAt = time.Now()
				return nil
			},
		}
		resilience := []execution.Resilience{{MaxRetries: 1, Backoff: 40 * time.Millisecond}}
		sched := execution.NewScheduler()
		_, err := sched.ExecuteWithResilience(context.Background(), &invocation.Invocation{Intent: "jitter"}, plan, runners, resilience, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		return firstRetryAt.Sub(start)
	}

	delays := make(map[time.Duration]struct{})
	for i := 0; i < 8; i++ {
		delays[observe()/time.Millisecond] = struct{}{}
	}
	if len(delays) < 2 {
		t.Fatalf("expected jittered retry delays to vary across runs, got only distinct buckets: %v", delays)
	}
}

// TestResilienceBulkheadFailsFast confirms that when more concurrent
// callers share a bulkhead name than its configured concurrency, the
// excess calls fail fast with ErrBulkheadFull while up to the configured
// concurrency succeed concurrently.
func TestResilienceBulkheadFailsFast(t *testing.T) {
	bulkheadName := "test-bulkhead-unique-name-1"
	const concurrency = 2
	const callers = 5

	release := make(chan struct{})
	var inFlight int32
	var maxInFlight int32

	makePlanAndRun := func() (*graph.Plan, []execution.NodeExecutor, []execution.Resilience) {
		plan := singleNodePlan(t, "bulkheaded", graph.PureNode)
		runners := []execution.NodeExecutor{
			func(nc *execution.NodeContext) error {
				cur := atomic.AddInt32(&inFlight, 1)
				for {
					old := atomic.LoadInt32(&maxInFlight)
					if cur <= old || atomic.CompareAndSwapInt32(&maxInFlight, old, cur) {
						break
					}
				}
				<-release
				atomic.AddInt32(&inFlight, -1)
				return nil
			},
		}
		resilience := []execution.Resilience{{Bulkhead: bulkheadName, Concurrency: concurrency}}
		return plan, runners, resilience
	}

	sched := execution.NewScheduler()
	var wg sync.WaitGroup
	results := make([]error, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			plan, runners, resilience := makePlanAndRun()
			_, err := sched.ExecuteWithResilience(context.Background(), &invocation.Invocation{Intent: "bulkheaded"}, plan, runners, resilience, nil)
			results[idx] = err
		}(i)
	}

	// Give the goroutines a chance to reach the bulkhead before releasing.
	time.Sleep(150 * time.Millisecond)
	close(release)
	wg.Wait()

	var fullCount, okCount int
	for _, err := range results {
		if err == nil {
			okCount++
		} else if errors.Is(err, execution.ErrBulkheadFull) {
			fullCount++
		} else {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if okCount != concurrency {
		t.Fatalf("expected exactly %d successful calls, got %d (full=%d)", concurrency, okCount, fullCount)
	}
	if fullCount != callers-concurrency {
		t.Fatalf("expected exactly %d ErrBulkheadFull, got %d", callers-concurrency, fullCount)
	}
	if atomic.LoadInt32(&maxInFlight) > int32(concurrency) {
		t.Fatalf("bulkhead allowed %d concurrent executions, want <= %d", maxInFlight, concurrency)
	}
}
