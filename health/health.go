package health

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"
)

// Status is the outcome of a health check or an aggregated report.
type Status uint8

const (
	// StatusUp means the check succeeded / the process can serve traffic.
	StatusUp Status = iota
	// StatusDegraded means the check succeeded in a reduced-capacity mode
	// (e.g. an open circuit breaker, a failed-over replica). Requests can
	// still be served, but not at full health.
	StatusDegraded
	// StatusDown means the check failed outright.
	StatusDown
)

// String implements fmt.Stringer.
func (s Status) String() string {
	switch s {
	case StatusUp:
		return "up"
	case StatusDegraded:
		return "degraded"
	case StatusDown:
		return "down"
	default:
		return "unknown"
	}
}

// MarshalJSON encodes Status as its string form.
func (s Status) MarshalJSON() ([]byte, error) {
	return []byte(`"` + s.String() + `"`), nil
}

// CheckResult is the outcome a Checker reports for a single invocation.
type CheckResult struct {
	Status Status
	// Error is a human-readable failure reason. Empty when Status is
	// StatusUp.
	Error string
}

// Checker is a single named health check. Implementations should honor
// ctx cancellation/deadline where practical, though the Registry enforces
// a per-check timeout regardless.
//
// This mirrors the codebase's existing convention of a single-method
// interface plus a func adapter (see capability.Producer / ProducerFunc).
type Checker interface {
	Check(ctx context.Context) CheckResult
}

// CheckFunc adapts a plain function into a Checker.
type CheckFunc func(ctx context.Context) CheckResult

// Check implements Checker.
func (f CheckFunc) Check(ctx context.Context) CheckResult {
	return f(ctx)
}

// Simple adapts the common case — a function that just returns an error —
// into a CheckFunc. A nil error means StatusUp; a non-nil error means
// StatusDown with the error's message as the reason.
func Simple(fn func(ctx context.Context) error) CheckFunc {
	return func(ctx context.Context) CheckResult {
		if err := fn(ctx); err != nil {
			return CheckResult{Status: StatusDown, Error: err.Error()}
		}
		return CheckResult{Status: StatusUp}
	}
}

// Entry is the reported outcome of a single named check within a Report.
type Entry struct {
	Name     string        `json:"name"`
	Status   Status        `json:"status"`
	Error    string        `json:"error,omitempty"`
	Duration time.Duration `json:"durationMs"`
}

// MarshalJSON reports Duration in milliseconds for readability.
func (e Entry) durationMs() float64 {
	return float64(e.Duration) / float64(time.Millisecond)
}

// Report is the aggregated outcome of running a set of checks.
type Report struct {
	Status   Status        `json:"status"`
	Checks   []Entry       `json:"checks"`
	Duration time.Duration `json:"durationMs"`
	Time     time.Time     `json:"time"`
}

// reportJSON is the wire representation of Report (Duration/Entry.Duration
// rendered as milliseconds rather than Go's default nanosecond int).
type reportJSON struct {
	Status   Status      `json:"status"`
	Checks   []entryJSON `json:"checks"`
	Duration float64     `json:"durationMs"`
	Time     time.Time   `json:"time"`
}

type entryJSON struct {
	Name       string  `json:"name"`
	Status     Status  `json:"status"`
	Error      string  `json:"error,omitempty"`
	DurationMs float64 `json:"durationMs"`
}

// MarshalJSON renders durations in milliseconds.
func (r Report) MarshalJSON() ([]byte, error) {
	rj := reportJSON{
		Status:   r.Status,
		Checks:   make([]entryJSON, len(r.Checks)),
		Duration: float64(r.Duration) / float64(time.Millisecond),
		Time:     r.Time,
	}
	for i, c := range r.Checks {
		rj.Checks[i] = entryJSON{
			Name:       c.Name,
			Status:     c.Status,
			Error:      c.Error,
			DurationMs: c.durationMs(),
		}
	}
	return json.Marshal(rj)
}

// DefaultCheckTimeout is the per-check timeout used when none is configured.
const DefaultCheckTimeout = 2 * time.Second

// Option configures a Registry.
type Option func(*Registry)

// WithTimeout sets the per-check timeout enforced by the Registry. Checks
// that exceed it are reported as StatusDown with a timeout error, without
// blocking the rest of the report.
func WithTimeout(d time.Duration) Option {
	return func(r *Registry) {
		if d > 0 {
			r.timeout = d
		}
	}
}

// Registry is a thread-safe collection of named health checks.
type Registry struct {
	mu        sync.RWMutex
	liveness  map[string]Checker
	readiness map[string]Checker
	timeout   time.Duration
}

// NewRegistry creates an empty Registry.
func NewRegistry(opts ...Option) *Registry {
	r := &Registry{
		liveness:  make(map[string]Checker),
		readiness: make(map[string]Checker),
		timeout:   DefaultCheckTimeout,
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// Register adds a named readiness check (one that may reach external
// dependencies). It participates in Readiness reports only.
func (r *Registry) Register(name string, check CheckFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.readiness[name] = check
}

// RegisterChecker is like Register but accepts any Checker implementation.
func (r *Registry) RegisterChecker(name string, check Checker) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.readiness[name] = check
}

// RegisterLiveness adds a named liveness check. By convention these should
// be cheap and free of external dependencies (network calls, disk I/O,
// etc.) — they exist to answer "is this process fundamentally alive",
// not "can it serve requests". Liveness checks also participate in
// Readiness reports, since a process that fails its own liveness checks
// cannot be ready either.
func (r *Registry) RegisterLiveness(name string, check CheckFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.liveness[name] = check
}

// Unregister removes a check (from both categories) by name.
func (r *Registry) Unregister(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.liveness, name)
	delete(r.readiness, name)
}

// Liveness runs only the liveness-registered checks. If none have been
// registered, it reports StatusUp trivially — the process executing this
// call is itself proof of life.
func (r *Registry) Liveness(ctx context.Context) Report {
	r.mu.RLock()
	checks := cloneCheckers(r.liveness)
	timeout := r.timeout
	r.mu.RUnlock()

	if len(checks) == 0 {
		now := time.Now()
		return Report{Status: StatusUp, Checks: nil, Duration: 0, Time: now}
	}
	return runChecks(ctx, checks, timeout)
}

// Readiness runs every registered check (liveness + readiness) in
// parallel, bounded by the configured per-check timeout, and aggregates
// the overall Status.
func (r *Registry) Readiness(ctx context.Context) Report {
	r.mu.RLock()
	checks := make(map[string]Checker, len(r.liveness)+len(r.readiness))
	for name, c := range r.liveness {
		checks[name] = c
	}
	for name, c := range r.readiness {
		checks[name] = c
	}
	timeout := r.timeout
	r.mu.RUnlock()

	return runChecks(ctx, checks, timeout)
}

func cloneCheckers(m map[string]Checker) map[string]Checker {
	out := make(map[string]Checker, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// runChecks executes every checker in parallel, each bounded by timeout,
// recovering from panics, and returns the aggregated Report.
func runChecks(ctx context.Context, checks map[string]Checker, timeout time.Duration) Report {
	start := time.Now()

	names := make([]string, 0, len(checks))
	for name := range checks {
		names = append(names, name)
	}
	sort.Strings(names)

	entries := make([]Entry, len(names))
	var wg sync.WaitGroup
	for i, name := range names {
		wg.Add(1)
		go func(i int, name string, checker Checker) {
			defer wg.Done()
			entries[i] = runOne(ctx, name, checker, timeout)
		}(i, name, checks[name])
	}
	wg.Wait()

	return Report{
		Status:   aggregate(entries),
		Checks:   entries,
		Duration: time.Since(start),
		Time:     start,
	}
}

// runOne runs a single checker with a timeout and panic recovery. The
// checker itself keeps running in the background if it does not honor
// ctx cancellation, but runOne returns as soon as the timeout elapses so
// one slow/misbehaving check never blocks the overall report.
func runOne(ctx context.Context, name string, checker Checker, timeout time.Duration) Entry {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	resultCh := make(chan CheckResult, 1)
	start := time.Now()
	go func() {
		defer func() {
			if rec := recover(); rec != nil {
				resultCh <- CheckResult{Status: StatusDown, Error: fmt.Sprintf("panic: %v", rec)}
			}
		}()
		resultCh <- checker.Check(cctx)
	}()

	select {
	case res := <-resultCh:
		return Entry{Name: name, Status: res.Status, Error: res.Error, Duration: time.Since(start)}
	case <-cctx.Done():
		return Entry{
			Name:     name,
			Status:   StatusDown,
			Error:    fmt.Sprintf("check timed out after %s", timeout),
			Duration: time.Since(start),
		}
	}
}

// aggregate reduces a set of check entries down to a single overall
// Status: Down if any check is Down, else Degraded if any is Degraded,
// else Up.
func aggregate(entries []Entry) Status {
	status := StatusUp
	for _, e := range entries {
		switch e.Status {
		case StatusDown:
			return StatusDown
		case StatusDegraded:
			status = StatusDegraded
		}
	}
	return status
}
