package execution

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/oarkflow/ref/fact"
	"github.com/oarkflow/ref/graph"
	"github.com/oarkflow/ref/invocation"
	"github.com/oarkflow/ref/observer"
)

var (
	errNilPlan = errors.New("ref: cannot execute nil plan")
)

// nodePanicError is a pre-allocated error type for panics (avoids fmt.Errorf).
type nodePanicError struct {
	node   string
	reason any
}

func (e *nodePanicError) Error() string {
	return "ref: panic in node \"" + e.node + "\""
}

var closedDoneChan = make(chan struct{})

func init() {
	close(closedDoneChan)
}

// execCancelCtx is a lightweight cancellable context that avoids heap allocation.
// Uses an atomic flag instead of the full context.WithCancel machinery.
type execCancelCtx struct {
	context.Context
	canceled atomic.Bool
	doneCtx  context.Context
	cancel   context.CancelFunc
	doneMu   sync.Mutex
}

func (c *execCancelCtx) reset(parent context.Context) {
	c.doneMu.Lock()
	c.Context = parent
	c.doneCtx = nil
	c.cancel = nil
	c.doneMu.Unlock()
	c.canceled.Store(false)
}

func (c *execCancelCtx) Done() <-chan struct{} {
	c.doneMu.Lock()
	if c.doneCtx == nil {
		if c.Context == nil {
			c.doneMu.Unlock()
			return closedDoneChan
		}
		c.doneCtx, c.cancel = context.WithCancel(c.Context)
		if c.canceled.Load() {
			c.cancel()
		}
	}
	done := c.doneCtx.Done()
	c.doneMu.Unlock()
	return done
}

func (c *execCancelCtx) cancelExecution() {
	c.doneMu.Lock()
	canceled := c.canceled.Swap(true)
	if !canceled && c.cancel != nil {
		c.cancel()
	}
	c.doneMu.Unlock()
}

func (c *execCancelCtx) Err() error {
	if c.canceled.Load() {
		return context.Canceled
	}
	c.doneMu.Lock()
	parent := c.Context
	c.doneMu.Unlock()
	if parent == nil {
		return context.Canceled
	}
	return parent.Err()
}

// ExecutionState describes how execution completed.
type ExecutionState uint8

const (
	StateCompleted ExecutionState = iota
	StateShortCircuited
	StateDenied
	StateDeferred
	StateFailed
	StateCanceled
)

func (s ExecutionState) String() string {
	switch s {
	case StateCompleted:
		return "completed"
	case StateShortCircuited:
		return "short_circuited"
	case StateDenied:
		return "denied"
	case StateDeferred:
		return "deferred"
	case StateFailed:
		return "failed"
	case StateCanceled:
		return "canceled"
	default:
		return "unknown"
	}
}

// ExecutionOutcome carries the final result of execution.
type ExecutionOutcome struct {
	State   ExecutionState
	Value   any
	Err     error
	Effects []any
	Meta    any
}

// NodeExecutor is the executable callback for a graph node.
// It directly receives an isolated NodeContext, decoupling execution from the graph layer.
type NodeExecutor func(nc *NodeContext) error

// CompiledNode pairs a descriptive graph.Node with its runtime executor.
type CompiledNode struct {
	Spec *graph.Node
	Run  NodeExecutor
}

// Program pairs a compiled graph.Plan with its executable node callbacks.
type Program struct {
	Plan    *graph.Plan
	Runners []NodeExecutor

	// Resilience holds per-node fault-tolerance policy, indexed by
	// graph.NodeID exactly like Runners. It may be nil or shorter than
	// Runners — any node without an entry is treated as the zero value
	// (no timeout, no retry, no bulkhead), identical to today's behavior.
	Resilience []Resilience
}

// nodeState tracks per-node runtime state.
type nodeState struct {
	remaining atomic.Int32 // dependency counter
	state     atomic.Uint32
}

const (
	nsBlocked  uint32 = 0
	nsReady    uint32 = 1
	nsRunning  uint32 = 2
	nsComplete uint32 = 3
	nsCanceled uint32 = 4
)

var (
	statesPool = sync.Pool{
		New: func() any {
			s := make([]nodeState, 32)
			return &s
		},
	}

	chanPool = sync.Pool{
		New: func() any {
			return make(chan graph.NodeID, 32)
		},
	}

	execStatePool = sync.Pool{
		New: func() any {
			return &execState{}
		},
	}
)

// execState holds shared mutable state for one execution.
// All helper functions are methods on this struct to avoid closure heap escapes.
type execState struct {
	// Immutable for this execution (set once in init, read everywhere)
	ctx        context.Context
	inv        *invocation.Invocation
	plan       *graph.Plan
	runners    []NodeExecutor
	resilience []Resilience
	budget     *Budget
	scheduler  *Scheduler
	trace      *Trace
	nodeCount  int

	// Per-execution state
	states    []nodeState
	facts     *fact.Store
	decisions *DecisionSet
	specCtx   execCancelCtx // embedded — no pointer allocation

	// Completion tracking
	completed chan graph.NodeID
	wg        sync.WaitGroup
	inFlight  atomic.Int32
	start     time.Time

	// leaked is set when a node timed out (Resilience.Timeout) and its
	// run(nc) goroutine could not be waited on or killed. In that case nc
	// — and everything it references (es.facts, es.decisions, and es
	// itself via its embedded specCtx) — may still be mutated by that
	// abandoned goroutine after this execution returns. Recycling any of
	// that shared, pooled state into a future, unrelated execution while
	// the old goroutine is still writing to it would be a data race, so
	// when leaked is set the defer in execute() intentionally does not
	// return es/es.facts/es.decisions to their pools; they are left for
	// the garbage collector once the abandoned goroutine eventually exits.
	leaked atomic.Bool

	// Outcome state (protected by outcomeMu)
	outcomeMu  sync.Mutex
	hasSC      bool
	scOut      ExecutionOutcome
	allEffects []any
	errOnce    sync.Once
	firstErr   error

	// Gate tracking
	queueMu       sync.Mutex
	gatedMask     uint64
	gatedOverflow map[int]struct{}
	readyStorage  [32]graph.NodeID

	// Pooled state ownership
	statesPtr *[]nodeState
}

var outcomePool = sync.Pool{
	New: func() any {
		return &ExecutionOutcome{}
	},
}

// AcquireOutcome gets a pooled ExecutionOutcome.
func AcquireOutcome() *ExecutionOutcome {
	return outcomePool.Get().(*ExecutionOutcome)
}

// ReleaseOutcome returns an ExecutionOutcome to the pool.
func ReleaseOutcome(out *ExecutionOutcome) {
	if out == nil {
		return
	}
	out.State = StateCompleted
	out.Value = nil
	out.Err = nil
	out.Effects = nil
	out.Meta = nil
	outcomePool.Put(out)
}

// isGateEligible checks whether a dependency-ready node can run now.
func (es *execState) isGateEligible(node *graph.Node) bool {
	if es.decisions.Verdict() == VerdictDeny {
		return false
	}
	if node.Kind == graph.DecisionNode {
		return true
	}
	if node.Kind == graph.OperationNode || node.Kind == graph.EffectNode || node.Kind == graph.AsyncEffect {
		if es.plan.HasDecisions && es.decisions.Verdict() != VerdictAllow {
			return false
		}
		return true
	}
	switch node.Speculation {
	case graph.NoSpeculation:
		if es.plan.HasDecisions && es.decisions.Verdict() != VerdictAllow {
			return false
		}
		return true
	case graph.PreAuthSafe:
		return true
	case graph.PostIdentitySafe:
		for _, slot := range node.Requires {
			if !es.facts.Has(slot) {
				return false
			}
		}
		return true
	case graph.PostPolicySafe:
		if es.plan.HasDecisions && es.decisions.Verdict() != VerdictAllow {
			return false
		}
		return true
	}
	return false
}

// executeNode executes a single node synchronously.
func (es *execState) executeNode(nodeID graph.NodeID) {
	node := es.plan.Nodes[nodeID]
	if es.trace != nil {
		es.trace.NodeStarted(nodeID, node.Name)
	}

	es.outcomeMu.Lock()
	hasSC := es.hasSC
	es.outcomeMu.Unlock()
	if hasSC || es.specCtx.Err() != nil {
		es.states[nodeID].state.Store(nsCanceled)
		if es.trace != nil {
			es.trace.NodeFinished(nodeID, es.specCtx.Err())
		}
		return
	}

	nc := AcquireNodeContext(
		&es.specCtx,
		es.inv,
		es.facts,
		es.budget,
		es.decisions,
		nodeID,
		es.plan.DefToSlot,
		es.plan.DefSlots,
		es.plan.MaxDefID,
	)
	nc.SetTrace(es.trace)

	hasObs := len(es.scheduler.observers) > 0
	var info graph.NodeInfo
	if hasObs {
		info = node.Info()
		for _, obs := range es.scheduler.observers {
			obs.NodeStarted(info)
		}
	}

	runErr := es.safeRun(nodeID, nc)
	if es.trace != nil {
		es.trace.NodeFinished(nodeID, runErr)
		if node.Kind == graph.DecisionNode {
			es.trace.RecordDecision(nodeID, es.decisions)
		}
	}

	if hasObs {
		for _, obs := range es.scheduler.observers {
			obs.NodeFinished(info, runErr)
		}
	}

	// A timed-out node may still have a goroutine running in the
	// background holding a reference to nc (see runWithTimeout's doc
	// comment). Reading nc's effects/short-circuit state here, or
	// returning it to the pool, would race with that goroutine, so both
	// are skipped for this outcome — nc is intentionally abandoned rather
	// than reused.
	timedOut := errors.Is(runErr, ErrNodeTimeout)
	if timedOut {
		// Mark this whole execution's pooled state as unsafe to recycle —
		// see the leaked field's doc comment.
		es.leaked.Store(true)
	} else {
		es.outcomeMu.Lock()
		// Direct iteration avoids the defensive copy in nc.Effects()
		nc.mu.Lock()
		for _, fx := range nc.effects {
			es.allEffects = append(es.allEffects, fx)
		}
		nc.mu.Unlock()
		if sc, ok := nc.GetShortCircuit(); ok && !es.hasSC && (!es.plan.HasDecisions || es.decisions.Verdict() == VerdictAllow) {
			es.hasSC = true
			es.scOut = *sc
			es.specCtx.cancelExecution()
		}
		es.outcomeMu.Unlock()

		ReleaseNodeContext(nc)
	}

	if runErr != nil {
		es.setError(runErr)
		es.states[nodeID].state.Store(nsCanceled)

		if node.Kind == graph.DecisionNode && es.decisions.Verdict() == VerdictDeny {
			es.specCtx.cancelExecution()
		}
	} else {
		es.states[nodeID].state.Store(nsComplete)
	}
}

// safeRun runs a runner callback under panic recovery.
func (es *execState) safeRun(nodeID graph.NodeID, nc *NodeContext) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = &nodePanicError{node: es.plan.Nodes[nodeID].Name, reason: r}
		}
	}()
	if int(nodeID) >= len(es.runners) || es.runners[nodeID] == nil {
		return nil
	}
	run := es.runners[nodeID]

	var res Resilience
	if int(nodeID) < len(es.resilience) {
		res = es.resilience[nodeID]
	}
	if res == (Resilience{}) {
		// Fast path: no Resilience configured for this node — identical
		// to pre-enforcement behavior, no extra work.
		return run(nc)
	}
	return runResilient(nc, run, es.plan.Nodes[nodeID].Name, res)
}

func (es *execState) setError(err error) {
	if err == nil {
		return
	}
	es.outcomeMu.Lock()
	es.errOnce.Do(func() { es.firstErr = err })
	es.outcomeMu.Unlock()
}

func (es *execState) errorValue() error {
	es.outcomeMu.Lock()
	defer es.outcomeMu.Unlock()
	return es.firstErr
}

// launchAsync launches a node for async execution in a goroutine.
func (es *execState) launchAsync(nodeID graph.NodeID) {
	if !es.states[nodeID].state.CompareAndSwap(nsReady, nsRunning) {
		return
	}

	es.inFlight.Add(1)
	es.wg.Add(1)
	job := nodeJob{state: es, nodeID: nodeID}
	if es.nodeCount >= 8 && sharedNodeWorkers.submit(job) {
		return
	}
	go job.run()
}

func (es *execState) runInline(nodeID graph.NodeID) ([]graph.NodeID, bool) {
	es.states[nodeID].state.Store(nsRunning)
	es.executeNode(nodeID)
	es.outcomeMu.Lock()
	hasSC := es.hasSC
	es.outcomeMu.Unlock()
	if hasSC || es.errorValue() != nil || (es.plan.Nodes[nodeID].Kind == graph.DecisionNode && es.decisions.Verdict() == VerdictDeny) {
		return nil, true
	}
	nextReady := es.propagateDownstream(nodeID, es.readyStorage[:0])
	nextReady = es.checkGatedWait(nextReady)
	if len(nextReady) > 1 {
		for _, nid := range nextReady {
			es.launchAsync(nid)
		}
		return nil, false
	}
	return nextReady, false
}

// checkGatedWait evaluates all waiting nodes and promotes eligible ones.
func (es *execState) checkGatedWait(dst []graph.NodeID) []graph.NodeID {
	es.queueMu.Lock()
	defer es.queueMu.Unlock()
	if es.gatedMask == 0 && len(es.gatedOverflow) == 0 {
		return dst
	}
	for nid := 0; nid < es.nodeCount && nid < 64; nid++ {
		bit := uint64(1) << nid
		if es.gatedMask&bit != 0 && es.isGateEligible(es.plan.Nodes[nid]) {
			es.gatedMask &^= bit
			es.states[nid].state.Store(nsReady)
			dst = append(dst, graph.NodeID(nid))
		}
	}
	for nid := range es.gatedOverflow {
		if nid >= 64 && es.isGateEligible(es.plan.Nodes[nid]) {
			delete(es.gatedOverflow, nid)
			es.states[nid].state.Store(nsReady)
			dst = append(dst, graph.NodeID(nid))
		}
	}
	return dst
}

// propagateDownstream decrements downstream dependency counters and collects newly ready nodes.
func (es *execState) propagateDownstream(nodeID graph.NodeID, dst []graph.NodeID) []graph.NodeID {
	es.queueMu.Lock()
	defer es.queueMu.Unlock()
	for _, downstream := range es.plan.Adjacency[nodeID] {
		remaining := es.states[downstream].remaining.Add(-1)
		if remaining == 0 {
			dn := es.plan.Nodes[downstream]
			if es.isGateEligible(dn) {
				es.states[downstream].state.Store(nsReady)
				dst = append(dst, downstream)
			} else {
				es.states[downstream].state.Store(nsBlocked)
				if downstream < 64 {
					es.gatedMask |= 1 << downstream
				} else {
					es.gatedOverflow[int(downstream)] = struct{}{}
				}
			}
		}
	}
	return dst
}

// reportFinish notifies observers of execution completion.
func (es *execState) reportFinish(err error) {
	durationMs := float64(time.Since(es.start).Nanoseconds()) / 1e6
	for _, obs := range es.scheduler.observers {
		obs.ExecutionFinished(string(es.inv.Intent), durationMs, err)
	}
}

// reset clears the execState for reuse via the pool.
func (es *execState) reset() {
	es.ctx = nil
	es.inv = nil
	es.plan = nil
	es.runners = nil
	es.resilience = nil
	es.budget = nil
	es.scheduler = nil
	es.trace = nil
	es.states = nil
	es.facts = nil
	es.decisions = nil
	es.specCtx.reset(nil)
	es.hasSC = false
	es.scOut = ExecutionOutcome{}
	if len(es.allEffects) > 0 {
		es.allEffects = nil
	} else if es.allEffects != nil {
		es.allEffects = es.allEffects[:0]
	}
	es.firstErr = nil
	es.errOnce = sync.Once{}
	es.inFlight.Store(0)
	es.completed = nil
	es.gatedMask = 0
	es.gatedOverflow = nil
	es.statesPtr = nil
	es.nodeCount = 0
	es.leaked.Store(false)
}

type nodeJob struct {
	state  *execState
	nodeID graph.NodeID
}

// run executes the job and reports completion. Shared by pool workers,
// bounded overflow goroutines, and the cancellation-only raw-spawn fallback
// so the three paths cannot drift out of sync.
func (job nodeJob) run() {
	defer job.state.wg.Done()
	job.state.executeNode(job.nodeID)
	select {
	case job.state.completed <- job.nodeID:
	default:
	}
}

// nodePoolOverflowCap bounds the number of *additional* goroutines
// nodeWorkerPool.submit may spawn once its fixed pool's channel is full.
// Before this bound existed, a full channel fell straight through to an
// unconditional raw goroutine spawn in launchAsync: under sustained,
// saturating concurrency against a larger/higher-fanout graph (the shape
// the small-graph cheap-roots fast path does not cover), that path is taken
// continuously and goroutine count grows without any ceiling, tracking
// submitted work rather than any bound.
//
// TestScaleFanoutConcurrencyProfile (execution package, FH_SCALE_LOAD=1)
// measured both the unboundedness and where it actually matters, on an
// Apple M2 Pro / Go 1.27:
//   - At the concurrency the original 32-client TCP load test exercises,
//     goroutine growth never leaves the fixed pool's channel capacity —
//     this path isn't reached at all, which is why that test shows no
//     regression.
//   - At moderate sustained concurrency (hundreds of concurrent DAG
//     executions, thousands of concurrent fan-out jobs), unbounded growth
//     costs little: goroutines stay in the low thousands and throughput is
//     comparable with or without a cap.
//   - At extreme sustained concurrency (thousands of concurrent DAG
//     executions), unbounded growth is actively harmful, not just risky:
//     8,000 concurrent callers against this fan-out shape drove peak
//     goroutines to ~54,000 and *cut* throughput nearly in half versus the
//     3,000-caller case (Go's scheduler and GC start thrashing). Capping
//     overflow at this value in the same scenario held peak goroutines to
//     ~16,000 and *raised* throughput by roughly 2x versus the unbounded
//     run, with GC pause time also down by more than 4x.
//
// This value is deliberately generous — large enough that it does not bind
// at the concurrency levels this codebase's own benchmarks and load tests
// exercise (so no measured throughput cost there), while still being a
// real, finite ceiling instead of none, for the pathological tail. Once
// both the fixed pool and this overflow budget are saturated, submit blocks
// on the pool channel (real backpressure) instead of spawning further.
const nodePoolOverflowCap = 8192

type nodeWorkerPool struct {
	once     sync.Once
	jobs     chan nodeJob
	overflow atomic.Int32
}

var sharedNodeWorkers = &nodeWorkerPool{jobs: make(chan nodeJob, 1024)}

func (p *nodeWorkerPool) submit(job nodeJob) bool {
	p.once.Do(func() {
		count := runtime.GOMAXPROCS(0) * 2
		if count < 8 {
			count = 8
		}
		if count > 64 {
			count = 64
		}
		for i := 0; i < count; i++ {
			go p.worker()
		}
	})

	// Fast path: the fixed pool has room.
	select {
	case p.jobs <- job:
		return true
	default:
	}

	// Pool saturated: grow with a bounded number of overflow goroutines
	// rather than spawning unconditionally.
	for {
		cur := p.overflow.Load()
		if cur >= nodePoolOverflowCap {
			break
		}
		if p.overflow.CompareAndSwap(cur, cur+1) {
			go func() {
				defer p.overflow.Add(-1)
				job.run()
			}()
			return true
		}
	}

	// Pool and overflow budget both saturated: apply real backpressure
	// instead of spawning another goroutine. Block on the pool channel,
	// but give up if this execution has already been canceled so a
	// short-circuited or failed request can't deadlock here.
	select {
	case p.jobs <- job:
		return true
	case <-job.state.specCtx.Done():
		return false
	}
}

func (p *nodeWorkerPool) worker() {
	for job := range p.jobs {
		job.run()
	}
}

// Scheduler executes a compiled Plan using dependency readiness and explicit gate queues.
// Dependencies determine readiness — not stage indices.
type Scheduler struct {
	observers []observer.Observer
}

// NewScheduler creates a Scheduler with the given observers.
func NewScheduler(observers ...observer.Observer) *Scheduler {
	return &Scheduler{observers: observers}
}

// ExecuteProgram executes a compiled Program.
func (s *Scheduler) ExecuteProgram(
	ctx context.Context,
	inv *invocation.Invocation,
	prog *Program,
	budget *Budget,
) (*ExecutionOutcome, error) {
	if prog == nil {
		return nil, fmt.Errorf("ref: cannot execute nil program")
	}
	return s.execute(ctx, inv, prog.Plan, prog.Runners, prog.Resilience, budget, nil)
}

func (s *Scheduler) ExecuteProgramTraced(ctx context.Context, inv *invocation.Invocation, prog *Program, budget *Budget, trace *Trace) (*ExecutionOutcome, error) {
	if prog == nil {
		return nil, fmt.Errorf("ref: cannot execute nil program")
	}
	return s.execute(ctx, inv, prog.Plan, prog.Runners, prog.Resilience, budget, trace)
}

func (s *Scheduler) ExecuteProgramReplay(ctx context.Context, inv *invocation.Invocation, prog *Program, budget *Budget, expected *Trace) (*ExecutionOutcome, *Trace, error) {
	if expected == nil {
		return nil, nil, errors.New("ref: expected replay trace is required")
	}
	if inv == nil {
		return nil, nil, errors.New("ref: replay invocation is required")
	}
	if prog == nil {
		return nil, nil, errors.New("ref: cannot execute nil program")
	}
	actual := NewTrace(string(inv.ID), string(inv.Intent), expected.PlanVersion)
	out, err := s.execute(ctx, inv, prog.Plan, prog.Runners, prog.Resilience, budget, actual)
	if err != nil {
		return out, actual, err
	}
	if err := CompareTraces(expected, actual); err != nil {
		return out, actual, err
	}
	return out, actual, nil
}

func (s *Scheduler) ExecuteTraced(ctx context.Context, inv *invocation.Invocation, plan *graph.Plan, runners []NodeExecutor, budget *Budget, trace *Trace) (*ExecutionOutcome, error) {
	return s.execute(ctx, inv, plan, runners, nil, budget, trace)
}

// Execute runs a plan with node executors to completion.
func (s *Scheduler) Execute(
	ctx context.Context,
	inv *invocation.Invocation,
	plan *graph.Plan,
	runners []NodeExecutor,
	budget *Budget,
) (*ExecutionOutcome, error) {
	return s.execute(ctx, inv, plan, runners, nil, budget, nil)
}

// ExecuteWithResilience runs a plan with node executors and an explicit,
// NodeID-indexed Resilience policy slice (see Program.Resilience). It is
// additive: Execute/ExecuteTraced continue to run with no enforcement, and
// this method with a nil/empty resilience slice behaves identically to
// Execute.
func (s *Scheduler) ExecuteWithResilience(
	ctx context.Context,
	inv *invocation.Invocation,
	plan *graph.Plan,
	runners []NodeExecutor,
	resilience []Resilience,
	budget *Budget,
) (*ExecutionOutcome, error) {
	return s.execute(ctx, inv, plan, runners, resilience, budget, nil)
}

func (s *Scheduler) execute(
	ctx context.Context,
	inv *invocation.Invocation,
	plan *graph.Plan,
	runners []NodeExecutor,
	resilience []Resilience,
	budget *Budget,
	trace *Trace,
) (out *ExecutionOutcome, err error) {
	if plan == nil {
		return nil, errNilPlan
	}
	if ctx == nil {
		return nil, errors.New("ref: nil execution context")
	}
	if err := ctx.Err(); err != nil {
		out := AcquireOutcome()
		out.State = StateCanceled
		out.Err = err
		return out, err
	}

	nodeCount := len(plan.Nodes)

	// Acquire pooled execState — all mutable state lives here,
	// eliminating closure heap escapes.
	es := execStatePool.Get().(*execState)
	es.ctx = ctx
	es.inv = inv
	es.plan = plan
	es.runners = runners
	es.resilience = resilience
	es.budget = budget
	es.scheduler = s
	es.trace = trace
	es.nodeCount = nodeCount
	es.start = time.Now()
	es.leaked.Store(false)
	es.specCtx.reset(ctx)
	es.firstErr = nil
	es.errOnce = sync.Once{}
	es.hasSC = false
	es.scOut = ExecutionOutcome{}
	if cap(es.allEffects) > 0 {
		es.allEffects = es.allEffects[:0]
	} else {
		es.allEffects = nil
	}
	es.gatedMask = 0
	if nodeCount > 64 {
		es.gatedOverflow = make(map[int]struct{})
	} else {
		es.gatedOverflow = nil
	}
	es.inFlight.Store(0)

	defer func() {
		leaked := es.leaked.Load()
		if trace != nil {
			if out != nil {
				trace.Finalize(out.State, out.Value, err)
			} else {
				trace.Finalize(StateFailed, nil, err)
			}
			if !leaked {
				trace.SetFacts(es.facts)
			}
		}
		es.specCtx.cancelExecution()
		if leaked {
			// A node timed out and its run(nc) goroutine is still running
			// in the background (see the leaked field's doc comment).
			// Skip every pool return below: es.facts, es.decisions, and es
			// itself (via its embedded specCtx, which nc.Context aliases)
			// may still be read or written by that abandoned goroutine, so
			// handing any of them to a future, unrelated execution would
			// be unsafe. They are intentionally left for the garbage
			// collector once the abandoned goroutine eventually exits.
			return
		}
		// Return pooled resources
		if es.statesPtr != nil {
			st := es.states
			for i := range st {
				st[i].remaining.Store(0)
				st[i].state.Store(0)
			}
			statesPool.Put(es.statesPtr)
		}
		if es.completed != nil && nodeCount <= 32 {
			for len(es.completed) > 0 {
				<-es.completed
			}
			chanPool.Put(es.completed)
		}
		fact.ReleaseStore(es.facts)
		ReleaseDecisionSet(es.decisions)
		es.reset()
		execStatePool.Put(es)
	}()

	// Acquire pooled nodeState slice
	if nodeCount <= 32 {
		es.statesPtr = statesPool.Get().(*[]nodeState)
		es.states = (*es.statesPtr)[:nodeCount]
	} else {
		es.statesPtr = nil
		es.states = make([]nodeState, nodeCount)
	}

	for i, deps := range plan.InitialDeps {
		es.states[i].remaining.Store(deps)
		es.states[i].state.Store(nsBlocked)
	}

	es.facts = fact.AcquireStore(plan.SlotCount)
	if trace != nil {
		es.facts.EnableProvenance()
	}
	es.decisions = AcquireDecisionSet(int32(plan.DecisionCount))

	if nodeCount <= 32 {
		es.completed = chanPool.Get().(chan graph.NodeID)
	} else {
		es.completed = make(chan graph.NodeID, nodeCount)
	}

	// Seed: collect initial nodes with zero dependencies
	es.queueMu.Lock()
	initialReady := es.readyStorage[:0]
	for i := range es.states {
		if es.states[i].remaining.Load() == 0 {
			node := plan.Nodes[i]
			if es.isGateEligible(node) {
				es.states[i].state.Store(nsReady)
				initialReady = append(initialReady, graph.NodeID(i))
			} else {
				es.states[i].state.Store(nsBlocked)
				if i < 64 {
					es.gatedMask |= 1 << i
				}
			}
		}
	}
	es.queueMu.Unlock()

	cheapRoots := len(initialReady) > 1 && nodeCount <= 8
	if cheapRoots {
		for _, id := range initialReady {
			kind := plan.Nodes[id].Kind
			if kind != graph.PureNode && kind != graph.DecisionNode {
				cheapRoots = false
				break
			}
		}
	}
	if cheapRoots {
		// All initial roots are Pure/Decision nodes: fast, synchronous,
		// dependency-free work with no reason to pay for a goroutine spawn
		// and a channel handoff. Run every one of them inline, sequentially,
		// instead of running only the first inline and dispatching the rest
		// as raw goroutines. This is the dominant path for small (<8 node)
		// HTTP-style graphs such as "auth decision" + "decode" feeding a
		// single downstream operation node — profiling showed the async
		// dispatch of that second root (goroutine create + channel send/
		// receive) was pure overhead for work that never blocks.
		//
		// initialReady aliases es.readyStorage, which runInline reuses as
		// its append destination, so snapshot the roots into a local copy
		// before iterating.
		var rootsBuf [32]graph.NodeID
		roots := rootsBuf[:copy(rootsBuf[:], initialReady)]
		initialReady = nil
		var merged []graph.NodeID
		for _, id := range roots {
			nextReady, terminal := es.runInline(id)
			if terminal {
				goto finished
			}
			merged = append(merged, nextReady...)
		}
		switch len(merged) {
		case 1:
			initialReady = merged
		default:
			for _, nid := range merged {
				es.launchAsync(nid)
			}
		}
	}

	for len(initialReady) == 1 && es.inFlight.Load() == 0 {
		curr := initialReady[0]
		initialReady = nil
		nextReady, terminal := es.runInline(curr)
		if terminal {
			goto finished
		}
		if len(nextReady) == 1 {
			initialReady = nextReady
		}
	}

	if es.inFlight.Load() == 0 && len(initialReady) == 0 {
		goto finished
	}

	// Launch any remaining initial nodes concurrently
	for _, nid := range initialReady {
		es.launchAsync(nid)
	}

	for {
		select {
		case <-ctx.Done():
			es.specCtx.cancelExecution()
			es.wg.Wait()
			es.reportFinish(ctx.Err())
			out := AcquireOutcome()
			out.State = StateCanceled
			out.Err = ctx.Err()
			return out, ctx.Err()

		case nodeID := <-es.completed:
			es.inFlight.Add(-1)

			es.outcomeMu.Lock()
			hasSC := es.hasSC
			es.outcomeMu.Unlock()

			if hasSC {
				es.specCtx.cancelExecution()
				es.wg.Wait()
				es.outcomeMu.Lock()
				out := AcquireOutcome()
				*out = es.scOut
				out.Effects = es.allEffects
				es.outcomeMu.Unlock()
				es.reportFinish(nil)
				return out, nil
			}

			firstErr := es.errorValue()
			if firstErr != nil {
				es.specCtx.cancelExecution()
				es.wg.Wait()
				firstErr = es.errorValue()
				es.reportFinish(firstErr)
				out := AcquireOutcome()
				if es.decisions.Verdict() == VerdictDeny {
					out.State = StateDenied
				} else {
					out.State = StateFailed
				}
				out.Err = firstErr
				out.Effects = es.allEffects
				return out, firstErr
			}

			if plan.Nodes[nodeID].Kind == graph.DecisionNode && es.decisions.Verdict() == VerdictDeny {
				es.specCtx.cancelExecution()
				es.wg.Wait()
				es.reportFinish(nil)
				out := AcquireOutcome()
				out.State = StateDenied
				out.Effects = es.allEffects
				return out, nil
			}

			// Propagate to downstream
			downstreamReady := es.propagateDownstream(nodeID, es.readyStorage[:0])
			downstreamReady = es.checkGatedWait(downstreamReady)

			for _, nid := range downstreamReady {
				es.launchAsync(nid)
			}

			if es.inFlight.Load() == 0 {
				goto finished
			}
		}
	}

finished:

	es.wg.Wait()

	firstErr := es.errorValue()
	if firstErr != nil {
		es.reportFinish(firstErr)
		out := AcquireOutcome()
		if es.decisions.Verdict() == VerdictDeny {
			out.State = StateDenied
		} else {
			out.State = StateFailed
		}
		out.Err = firstErr
		out.Effects = es.allEffects
		return out, firstErr
	}

	es.queueMu.Lock()
	stuckGated := es.gatedMask != 0
	es.queueMu.Unlock()

	if stuckGated || es.decisions.Verdict() == VerdictDeny {
		outcome := AcquireOutcome()
		outcome.State = StateDenied
		outcome.Effects = es.allEffects
		es.reportFinish(nil)
		return outcome, nil
	}

	es.outcomeMu.Lock()
	if es.hasSC {
		out := AcquireOutcome()
		*out = es.scOut
		out.Effects = es.allEffects
		es.outcomeMu.Unlock()
		es.reportFinish(nil)
		return out, nil
	}
	es.outcomeMu.Unlock()

	finalOutcome := AcquireOutcome()
	finalOutcome.State = StateCompleted
	finalOutcome.Effects = es.allEffects

	es.reportFinish(nil)

	return finalOutcome, nil
}
