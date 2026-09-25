package execution_test

// Larger-graph / high-concurrency scheduler coverage.
//
// The cheap-roots inline fast path (execution/scheduler.go) only applies to
// graphs with <=8 nodes whose initial roots are all Pure/Decision — the
// common small HTTP-intent shape. It does nothing for larger, higher-fanout
// graphs, which always go through execState.launchAsync -> sharedNodeWorkers
// (a fixed-size worker pool backed by a channel, with an unbounded raw
// goroutine spawn fallback when that channel is full). This file exercises
// that untouched path directly, at the execution-package level (bypassing
// HTTP/fh entirely), so we can:
//
//   - benchmark ns/op, B/op, allocs/op for a >=8 node fan-out graph under
//     parallel load (BenchmarkScaleFanoutParallel), and
//   - observe goroutine-count and GC behavior under sustained, saturating
//     concurrent load (TestScaleFanoutConcurrencyProfile, opt-in via
//     FH_SCALE_LOAD=1) to check whether the shared-worker-pool's unbounded
//     goroutine fallback is actually reached, and what it costs, on this
//     hardware.

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oarkflow/ref/execution"
	"github.com/oarkflow/ref/fact"
	"github.com/oarkflow/ref/graph"
	"github.com/oarkflow/ref/invocation"
)

// fanoutWidth independent read-like roots feed one join operation node.
// nodeCount = fanoutWidth + 1, kept >= 8 so execState.launchAsync routes
// every root through sharedNodeWorkers instead of the small-graph inline
// fast path.
const fanoutWidth = 8

// buildScaleFanoutPlan builds a plan with fanoutWidth independent ReadNode
// roots (kind ReadNode, so the cheap-roots Pure/Decision-only fast path never
// applies) each publishing a distinct fact slot, feeding a single
// OperationNode that requires all of them. workDur, when > 0, is a
// synchronous sleep inside each root to model non-trivial (e.g. blocking-IO
// shaped) per-node work — needed to build up a real backlog against the
// shared worker pool's bounded channel under concurrent load.
func buildScaleFanoutPlan(tb testing.TB, workDur time.Duration) (*graph.Plan, []execution.NodeExecutor) {
	tb.Helper()
	nodes := make([]*graph.Node, 0, fanoutWidth+1)
	runners := make([]execution.NodeExecutor, 0, fanoutWidth+1)
	joinRequires := make([]fact.PlanSlot, 0, fanoutWidth)

	for i := 0; i < fanoutWidth; i++ {
		slot := fact.PlanSlot(i)
		joinRequires = append(joinRequires, slot)
		nodes = append(nodes, &graph.Node{
			ID:       graph.NodeID(i),
			Name:     fmt.Sprintf("Read%d", i),
			Kind:     graph.ReadNode,
			Provides: []fact.PlanSlot{slot},
		})
		idx := i
		runners = append(runners, func(nc *execution.NodeContext) error {
			if workDur > 0 {
				time.Sleep(workDur)
			}
			execution.PublishFact(nc, fact.PlanSlot(idx), idx)
			return nil
		})
	}

	nodes = append(nodes, &graph.Node{
		ID:       graph.NodeID(fanoutWidth),
		Name:     "Join",
		Kind:     graph.OperationNode,
		Requires: joinRequires,
	})
	runners = append(runners, func(nc *execution.NodeContext) error {
		return nil
	})

	g, err := graph.Build(nodes, 0)
	if err != nil {
		tb.Fatalf("graph build failed: %v", err)
	}
	plan, err := graph.Compile(g, "ScaleFanout", 1)
	if err != nil {
		tb.Fatalf("graph compile failed: %v", err)
	}
	return plan, runners
}

func newScaleInvocation(id string) *invocation.Invocation {
	return &invocation.Invocation{
		ID:     invocation.ID(id),
		Intent: "ScaleFanout",
		Input:  invocation.NewInput([]byte("{}"), "application/json"),
	}
}

// BenchmarkScaleFanoutParallel measures the >=8 node fan-out shape (no
// artificial per-node delay: purely CPU-bound) under parallel load, the
// scenario the small-graph cheap-roots fix does not touch.
func BenchmarkScaleFanoutParallel(b *testing.B) {
	plan, runners := buildScaleFanoutPlan(b, 0)
	sched := execution.NewScheduler()
	budget := execution.NewBudget(5*time.Second, 100, 100, 1<<20, 100)
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		inv := newScaleInvocation("scale-bench")
		for pb.Next() {
			out, err := sched.Execute(ctx, inv, plan, runners, budget)
			if err != nil {
				b.Fatal(err)
			}
			if out.State != execution.StateCompleted {
				b.Fatalf("unexpected state: %v", out.State)
			}
			execution.ReleaseOutcome(out)
		}
	})
}

// TestScaleFanoutConcurrencyProfile drives the fan-out graph with many
// concurrent callers, each root doing a short synchronous sleep to model
// blocking-shaped work, and samples runtime.NumGoroutine() plus GC stats
// throughout. This is the direct test of the audit's open question: does
// sharedNodeWorkers' bounded channel + unbounded-goroutine-on-full-fallback
// actually let goroutine count run away under sustained high concurrency for
// a larger/higher-fanout graph, on this hardware?
//
// Opt-in: set FH_SCALE_LOAD=1 (and optionally FH_SCALE_CLIENTS,
// FH_SCALE_DURATION) to run.
func TestScaleFanoutConcurrencyProfile(t *testing.T) {
	if os.Getenv("FH_SCALE_LOAD") != "1" {
		t.Skip("set FH_SCALE_LOAD=1 to run")
	}
	clients := 300
	if v := os.Getenv("FH_SCALE_CLIENTS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			clients = n
		}
	}
	duration := 5 * time.Second
	if v := os.Getenv("FH_SCALE_DURATION"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			duration = d
		}
	}
	// Per-root synchronous "blocking IO" delay. Deliberately not trivial:
	// this is what lets fanoutWidth*clients in-flight jobs pile up faster
	// than sharedNodeWorkers' fixed pool can drain them, which is exactly
	// the condition needed to reach its channel-full fallback.
	const perNodeWork = 2 * time.Millisecond

	plan, runners := buildScaleFanoutPlan(t, perNodeWork)
	sched := execution.NewScheduler()
	budget := execution.NewBudget(30*time.Second, 1000, 1000, 1<<20, 1000)
	ctx := context.Background()

	baselineGoroutines := runtime.NumGoroutine()
	var peakGoroutines atomic.Int64
	var samples atomic.Int64
	var sampleSum atomic.Int64

	var memBefore, memAfter runtime.MemStats
	runtime.ReadMemStats(&memBefore)

	stop := make(chan struct{})
	var monitorWG sync.WaitGroup
	monitorWG.Add(1)
	go func() {
		defer monitorWG.Done()
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				n := int64(runtime.NumGoroutine())
				samples.Add(1)
				sampleSum.Add(n)
				for {
					cur := peakGoroutines.Load()
					if n <= cur || peakGoroutines.CompareAndSwap(cur, n) {
						break
					}
				}
			}
		}
	}()

	var requests atomic.Int64
	var failures atomic.Int64
	var wg sync.WaitGroup
	deadline := time.Now().Add(duration)
	for i := 0; i < clients; i++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			inv := newScaleInvocation(fmt.Sprintf("scale-load-%d", worker))
			for time.Now().Before(deadline) {
				out, err := sched.Execute(ctx, inv, plan, runners, budget)
				if err != nil || out.State != execution.StateCompleted {
					failures.Add(1)
				} else {
					requests.Add(1)
				}
				if out != nil {
					execution.ReleaseOutcome(out)
				}
			}
		}(i)
	}
	wg.Wait()
	close(stop)
	monitorWG.Wait()

	runtime.ReadMemStats(&memAfter)

	var avgGoroutines float64
	if s := samples.Load(); s > 0 {
		avgGoroutines = float64(sampleSum.Load()) / float64(s)
	}
	gcCount := memAfter.NumGC - memBefore.NumGC
	pauseNs := memAfter.PauseTotalNs - memBefore.PauseTotalNs

	t.Logf(
		"clients=%d requests=%d failures=%d throughput=%.0f req/s baselineGoroutines=%d peakGoroutines=%d avgGoroutines=%.1f gcRuns=%d gcPauseTotal=%v heapAllocDelta=%dKB",
		clients,
		requests.Load(),
		failures.Load(),
		float64(requests.Load())/duration.Seconds(),
		baselineGoroutines,
		peakGoroutines.Load(),
		avgGoroutines,
		gcCount,
		time.Duration(pauseNs),
		int64(memAfter.HeapAlloc-memBefore.HeapAlloc)/1024,
	)

	if failures.Load() > 0 {
		t.Errorf("unexpected failures: %d", failures.Load())
	}
}
