package process

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// Harness for the edge-type tests.
//
// Two properties matter more than convenience here:
//
//   - Every call into the engine is bounded. A deadlock in the engine or a store
//     must fail the test with a goroutine dump, not hang `go test` until its
//     global timeout, so each call runs under boundedCall.
//   - Time is a fake clock. Timers, retry backoff, deadlines and rate-limit
//     windows are driven by moving the clock, so the tests assert exact
//     boundaries ("not yet at 999ms, yes at 1s") instead of sleeping and hoping.

const edgeTestBound = 5 * time.Second

// fakeClock is a settable clock for Options.Clock.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 1, 5, 9, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// edgeCall is one recorded step invocation.
type edgeCall struct {
	Intent       string
	Step         string
	Key          string
	Edge         string
	Attempt      int
	Input        any
	Compensating bool
}

// edgeRunner executes steps from per-intent behaviours and records every call,
// including how many ran at once.
type edgeRunner struct {
	mu        sync.Mutex
	calls     []edgeCall
	counts    map[string]int
	behaviour map[string]func(call StepCall, n int) (any, error)
	delay     map[string]time.Duration
	inflight  int
	peak      int
	finished  []string
}

func newEdgeRunner() *edgeRunner {
	return &edgeRunner{
		counts:    map[string]int{},
		behaviour: map[string]func(StepCall, int) (any, error){},
		delay:     map[string]time.Duration{},
	}
}

func (r *edgeRunner) on(intent string, fn func(call StepCall, n int) (any, error)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.behaviour[intent] = fn
}

func (r *edgeRunner) returns(intent string, value any) {
	r.on(intent, func(StepCall, int) (any, error) { return value, nil })
}

func (r *edgeRunner) fails(intent, message string) {
	r.on(intent, func(StepCall, int) (any, error) { return nil, errors.New(message) })
}

func (r *edgeRunner) slow(intent string, d time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.delay[intent] = d
}

// echo returns the step's input as its result.
func echo(call StepCall, _ int) (any, error) { return call.Input, nil }

func (r *edgeRunner) RunStep(ctx context.Context, call StepCall) (any, error) {
	r.mu.Lock()
	r.counts[call.Intent]++
	n := r.counts[call.Intent]
	r.calls = append(r.calls, edgeCall{
		Intent: call.Intent, Step: call.Step.Name, Key: call.Frame.StateKey(), Edge: call.Frame.Edge,
		Attempt: call.Frame.Attempt, Input: call.Input, Compensating: call.Compensating,
	})
	r.inflight++
	r.peak = max(r.peak, r.inflight)
	fn := r.behaviour[call.Intent]
	delay := r.delay[call.Intent]
	r.mu.Unlock()

	defer func() {
		r.mu.Lock()
		r.inflight--
		r.finished = append(r.finished, call.Intent)
		r.mu.Unlock()
	}()

	if delay > 0 {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
		}
	}
	if fn != nil {
		return fn(call, n)
	}
	return map[string]any{"step": call.Step.Name, "ok": true}, nil
}

func (r *edgeRunner) count(intent string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.counts[intent]
}

func (r *edgeRunner) intents() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.calls))
	for _, call := range r.calls {
		out = append(out, call.Intent)
	}
	return out
}

func (r *edgeRunner) callsTo(intent string) []edgeCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []edgeCall
	for _, call := range r.calls {
		if call.Intent == intent {
			out = append(out, call)
		}
	}
	return out
}

func (r *edgeRunner) peakConcurrency() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.peak
}

// gateLimiter admits while open and refuses while closed, reporting a reset at
// the end of the window on the fake clock.
type gateLimiter struct {
	mu    sync.Mutex
	open  bool
	calls int
	clock *fakeClock
}

func (l *gateLimiter) set(open bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.open = open
}

func (l *gateLimiter) Allow(_ context.Context, _ string, _ int, window time.Duration) (bool, int, time.Time, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls++
	if l.open {
		return true, 1, time.Time{}, nil
	}
	return false, 0, l.clock.Now().Add(window), nil
}

// edgeHarness bundles an engine over the memory store with a fake clock.
type edgeHarness struct {
	t       *testing.T
	engine  *Engine
	store   *MemoryStore
	runner  *edgeRunner
	clock   *fakeClock
	limiter *gateLimiter

	mu      sync.Mutex
	notices []NotifyEvent
}

func (h *edgeHarness) NotifyProcess(_ context.Context, event NotifyEvent) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.notices = append(h.notices, event)
	return nil
}

func (h *edgeHarness) noticesOf(kind string) []NotifyEvent {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []NotifyEvent
	for _, notice := range h.notices {
		if notice.Kind == kind {
			out = append(out, notice)
		}
	}
	return out
}

type harnessOption func(*Options)

func withoutLimiter() harnessOption { return func(o *Options) { o.Limiter = nil } }

func newEdgeHarness(t *testing.T, definitions []*Definition, options ...harnessOption) *edgeHarness {
	t.Helper()
	checkGoroutineLeaks(t)
	clock := newFakeClock()
	h := &edgeHarness{
		t: t, store: NewMemoryStore(), runner: newEdgeRunner(), clock: clock,
		limiter: &gateLimiter{open: true, clock: clock},
	}
	opts := Options{
		Store: h.store, Runner: h.runner, Notifier: h, Limiter: h.limiter,
		Owner: "edge-test", Clock: clock.Now,
	}
	for _, option := range options {
		option(&opts)
	}
	engine, err := New(opts)
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}
	for _, definition := range definitions {
		if err := engine.Register(definition); err != nil {
			t.Fatalf("register %s: %v", definition.Name, err)
		}
	}
	h.engine = engine
	return h
}

func newEdgeHarnessFor(t *testing.T, definition *Definition, options ...harnessOption) *edgeHarness {
	t.Helper()
	return newEdgeHarness(t, []*Definition{definition}, options...)
}

// checkGoroutineLeaks fails the test when it leaves goroutines behind: an engine
// call that returned while something it started is still blocked is a leak even
// if the test's own assertions passed.
func checkGoroutineLeaks(t *testing.T) {
	t.Helper()
	before := runtime.NumGoroutine()
	t.Cleanup(func() {
		deadline := time.Now().Add(2 * time.Second)
		for {
			after := runtime.NumGoroutine()
			if after <= before {
				return
			}
			if time.Now().After(deadline) {
				buf := make([]byte, 1<<20)
				n := runtime.Stack(buf, true)
				t.Errorf("goroutine leak: %d goroutines before the test, %d after\n%s", before, after, buf[:n])
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	})
}

// boundedCall runs fn and fails the test if it does not return within the bound —
// the difference between a red test and a hung test binary.
func boundedCall[T any](t *testing.T, what string, fn func() (T, error)) (T, error) {
	t.Helper()
	type result struct {
		value T
		err   error
	}
	done := make(chan result, 1)
	go func() {
		value, err := fn()
		done <- result{value, err}
	}()
	timer := time.NewTimer(edgeTestBound)
	defer timer.Stop()
	select {
	case r := <-done:
		return r.value, r.err
	case <-timer.C:
		buf := make([]byte, 1<<20)
		n := runtime.Stack(buf, true)
		t.Fatalf("%s did not return within %s — probable deadlock or livelock:\n%s", what, edgeTestBound, buf[:n])
		var zero T
		return zero, nil
	}
}

func (h *edgeHarness) start(process string, input any) *Run {
	h.t.Helper()
	run, err := boundedCall(h.t, "Start", func() (*Run, error) {
		return h.engine.Start(context.Background(), process, input, StartOptions{})
	})
	if err != nil {
		h.t.Fatalf("start %s: %v", process, err)
	}
	return run
}

func (h *edgeHarness) get(id string) *Run {
	h.t.Helper()
	run, err := h.store.GetRun(context.Background(), id)
	if err != nil {
		h.t.Fatalf("get run: %v", err)
	}
	return run
}

func (h *edgeHarness) advance(id string) {
	h.t.Helper()
	if _, err := boundedCall(h.t, "Advance", func() (struct{}, error) {
		return struct{}{}, h.engine.Advance(context.Background(), id)
	}); err != nil {
		h.t.Fatalf("advance: %v", err)
	}
}

func (h *edgeHarness) tick() int {
	h.t.Helper()
	handled, err := boundedCall(h.t, "Tick", func() (int, error) {
		return h.engine.Tick(context.Background(), 100)
	})
	if err != nil {
		h.t.Fatalf("tick: %v", err)
	}
	return handled
}

// passTime moves the fake clock and fires whatever became due.
func (h *edgeHarness) passTime(d time.Duration) {
	h.t.Helper()
	h.clock.Add(d)
	h.tick()
}

func (h *edgeHarness) signal(name, correlation string, payload any) []string {
	h.t.Helper()
	woken, err := boundedCall(h.t, "Signal", func() ([]string, error) {
		return h.engine.Signal(context.Background(), name, correlation, payload)
	})
	if err != nil {
		h.t.Fatalf("signal: %v", err)
	}
	return woken
}

func (h *edgeHarness) openTasks(runID string) []*Task {
	h.t.Helper()
	tasks, err := h.store.ListTasks(context.Background(), TaskFilter{RunID: runID, Limit: 100})
	if err != nil {
		h.t.Fatalf("list tasks: %v", err)
	}
	return tasks
}

func (h *edgeHarness) taskFor(runID, step string) *Task {
	h.t.Helper()
	for _, task := range h.openTasks(runID) {
		if task.Step == step {
			return task
		}
	}
	h.t.Fatalf("no open task for step %q", step)
	return nil
}

// completeTask claims and completes a task as a principal holding its role.
func (h *edgeHarness) completeTask(task *Task, action string) error {
	h.t.Helper()
	_, err := boundedCall(h.t, "CompleteTask", func() (*Task, error) {
		ctx := context.Background()
		actor := TaskActor{ID: "worker-1", Roles: []string{task.Role}}
		if _, err := h.engine.ClaimTaskAs(ctx, task.ID, actor); err != nil {
			return nil, err
		}
		return h.engine.CompleteTask(ctx, task.ID, actor.ID, action, nil)
	})
	return err
}

func (h *edgeHarness) timers(runID string) []*Timer {
	h.t.Helper()
	timers, err := h.store.ListTimers(context.Background(), runID)
	if err != nil {
		h.t.Fatalf("list timers: %v", err)
	}
	return timers
}

func expectStatus(t *testing.T, run *Run, want Status) {
	t.Helper()
	if run.Status != want {
		t.Fatalf("run status = %s, want %s (error: %q, waiting: %+v)", run.Status, want, run.Error, run.Waiting)
	}
}

func expectCount(t *testing.T, h *edgeHarness, intent string, want int) {
	t.Helper()
	if got := h.runner.count(intent); got != want {
		t.Fatalf("%s ran %d times, want %d (calls: %v)", intent, got, want, h.runner.intents())
	}
}

// field reads a nested key from a decoded payload.
func field(value any, path string) any {
	current := value
	for _, segment := range strings.Split(path, ".") {
		object, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current = object[segment]
	}
	return current
}

// guardFunc adapts a function to Guard.
type guardFunc func(Scope) bool

func (g guardFunc) Eval(scope Scope) (bool, error) { return g(scope), nil }
func (g guardFunc) Source() string                 { return "func" }

// never and always are fixed guards.
var (
	never  Guard = guardFunc(func(Scope) bool { return false })
	always Guard = guardFunc(func(Scope) bool { return true })
)

// shaperFunc adapts a function to Shaper.
type shaperFunc func(any, Scope) (any, error)

func (s shaperFunc) Shape(value any, scope Scope) (any, error) { return s(value, scope) }

func intentsContain(h *edgeHarness, intent string) bool {
	return slices.Contains(h.runner.intents(), intent)
}

func describeTimers(timers []*Timer) string {
	parts := make([]string, 0, len(timers))
	for _, timer := range timers {
		parts = append(parts, fmt.Sprintf("%s(%s/%s)", timer.Kind, timer.Edge, timer.Step))
	}
	return strings.Join(parts, ", ")
}
