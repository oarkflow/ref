package execution

import (
	"errors"
	"sync"
	"time"

	"github.com/oarkflow/ref/backoff"
)

// Resilience describes per-node operational fault-tolerance policy that the
// scheduler enforces around a node's NodeExecutor. It mirrors
// capability.Resilience but lives in the execution package (which
// capability already imports) to avoid an import cycle; runtime/engine.go
// maps capability.Registration.Resilience into this type when building a
// Program.
//
// The zero value means "no timeout, no retry, no bulkhead" — identical to
// today's unconditional, unguarded execution. That zero-value fast path is
// checked with a single struct comparison in safeRun and adds no
// measurable overhead for registrations that don't opt in.
type Resilience struct {
	Timeout     time.Duration
	MaxRetries  int
	Backoff     time.Duration
	Bulkhead    string
	Concurrency int
}

// Sentinel errors surfaced by resilience enforcement. They flow through the
// exact same es.setError/outcome path as any other node error — there is no
// parallel error-handling mechanism.
var (
	// ErrBulkheadFull is returned when a node's configured bulkhead has no
	// free concurrency slots. The scheduler fails fast rather than queuing:
	// queuing risks priority inversion and deadlock across the shared
	// worker pool, whereas failing fast keeps behavior simple and
	// predictable and lets the caller's own retry/backoff policy (if any)
	// handle recovery.
	ErrBulkheadFull = errors.New("ref: bulkhead full")

	// ErrNodeTimeout is returned when a node's Run did not complete within
	// its configured Resilience.Timeout.
	ErrNodeTimeout = errors.New("ref: node execution timed out")
)

// bulkheadSem is a fail-fast counting semaphore for one named bulkhead.
type bulkheadSem struct {
	slots chan struct{}
}

func (b *bulkheadSem) tryAcquire() bool {
	select {
	case b.slots <- struct{}{}:
		return true
	default:
		return false
	}
}

func (b *bulkheadSem) release() {
	select {
	case <-b.slots:
	default:
	}
}

// bulkheadRegistry is a global, lazily-populated registry of named
// bulkheads shared across the whole scheduler (all concurrent executions,
// all goroutines). It is safe for concurrent access via sync.Map.
//
// The first Resilience.Concurrency value seen for a given bulkhead name
// wins for the lifetime of the process; later registrations that reuse the
// same name keep the original capacity. This matches the common case where
// a bulkhead name identifies one logical resource with one fixed limit.
var bulkheadRegistry sync.Map // map[string]*bulkheadSem

func bulkheadFor(name string, concurrency int) *bulkheadSem {
	if v, ok := bulkheadRegistry.Load(name); ok {
		return v.(*bulkheadSem)
	}
	if concurrency <= 0 {
		concurrency = 1
	}
	sem := &bulkheadSem{slots: make(chan struct{}, concurrency)}
	actual, _ := bulkheadRegistry.LoadOrStore(name, sem)
	return actual.(*bulkheadSem)
}

// runWithTimeout runs run(nc) with a deadline of d. Because NodeExecutor is
// a synchronous, blocking call and node code is not required to cooperate
// with context cancellation, the only correct way to bound its wall time is
// to run it on a separate goroutine and race it against a timer.
//
// IMPORTANT: if the timer fires first, the run(nc) goroutine is NOT killed
// (Go cannot forcibly stop a goroutine) — it keeps running in the
// background and may still be mutating nc when this function returns.
// Callers MUST treat nc as poisoned after a timeout: do not read any
// derived state from it (effects, short-circuit, etc.) and do not return it
// to a pool for reuse, or the still-running goroutine can race with a
// future, unrelated execution that reuses the same pooled NodeContext.
// executeNode enforces this by skipping ReleaseNodeContext when the error
// is ErrNodeTimeout.
func runWithTimeout(nc *NodeContext, run NodeExecutor, nodeName string, d time.Duration) error {
	done := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				done <- &nodePanicError{node: nodeName, reason: r}
			}
		}()
		done <- run(nc)
	}()

	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case err := <-done:
		return err
	case <-timer.C:
		return ErrNodeTimeout
	}
}

// runResilient executes run(nc) under the policy described by res. It is
// only called when res is non-zero — the zero-value fast path is handled
// by the caller (safeRun) so the common, unconfigured case never pays for
// any of this.
func runResilient(nc *NodeContext, run NodeExecutor, nodeName string, res Resilience) error {
	if res.Bulkhead != "" && res.Concurrency > 0 {
		sem := bulkheadFor(res.Bulkhead, res.Concurrency)
		if !sem.tryAcquire() {
			return ErrBulkheadFull
		}
		defer sem.release()
	}

	maxAttempts := res.MaxRetries + 1
	if maxAttempts < 1 {
		maxAttempts = 1
	}

	var err error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if res.Timeout > 0 {
			err = runWithTimeout(nc, run, nodeName, res.Timeout)
		} else {
			err = run(nc)
		}

		if err == nil {
			return nil
		}
		if errors.Is(err, ErrNodeTimeout) {
			// A timed-out attempt leaves an abandoned goroutine that may
			// still hold a reference to nc (see runWithTimeout). Retrying
			// would mean handing that same nc to a second concurrent
			// caller of run(nc), which is unsafe. Timeouts are therefore
			// terminal regardless of MaxRetries.
			return err
		}
		if attempt == maxAttempts-1 {
			return err
		}
		if d := backoff.FullJitter(res.Backoff, attempt, 0); d > 0 {
			time.Sleep(d)
		}
	}
	return err
}
