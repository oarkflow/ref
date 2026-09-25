package process

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"
)

// The engine: the loop that turns a persisted cursor into progress.
//
// One idea organises the whole thing. A run holds a queue of frames — "execute
// this step with this input" — and an advance pass drains that queue, resolving
// each executed step's outgoing edges into new frames. When the queue empties the
// run either completes or, if something external is outstanding, parks.
//
// Parking is not a stop. A wait-event edge removes its continuation from the
// queue and records a subscription that will put it back; a delayed edge records
// a timer. A run with one branch parked and another still working stays running,
// which is what makes a fan-out where one arm waits for an approval behave the way
// an author expects rather than freezing the other arm.
//
// Everything that could be lost on a crash is a row before the pass returns: the
// cursor, the step states, the timers, the subscriptions, the join accumulations.
// A replica that dies mid-pass loses at most the steps it had already executed
// but not yet recorded — and those are re-executed, which is why step bodies
// should be idempotent and why the platform gives them idempotency guards.

// Options configures an Engine.
type Options struct {
	// Store is required.
	Store Store
	// Runner executes step bodies. Required.
	Runner StepRunner
	// Enqueuer schedules advances on a durable queue. Without one the engine
	// advances synchronously in the caller's goroutine, which is correct for tests
	// and inadequate for production.
	Enqueuer Enqueuer
	// Owner identifies this replica in leases. Defaults to a random id, which is
	// right: two replicas sharing an owner id would each think they held the
	// other's leases.
	Owner string
	// LeaseTTL bounds how long a crashed replica can hold a run. Default 30s.
	LeaseTTL time.Duration
	// MaxPasses bounds one Advance call, so a run that keeps producing frames
	// yields rather than monopolising a worker.
	MaxPasses int
	// StepConcurrency caps concurrent step executions within one pass. Default 8.
	StepConcurrency int
	// Notifier delivers SLA breaches, escalations and task notifications.
	Notifier Notifier
	// Locker and Limiter back a step's lock and rate_limit.
	Locker  Locker
	Limiter RateLimiter
	// Observers are notified of lifecycle transitions.
	Observers []Observer
	// Clock overrides time.Now, for tests.
	Clock func() time.Time
}

// Engine runs durable processes.
type Engine struct {
	store    Store
	runner   StepRunner
	enqueuer Enqueuer
	notifier Notifier
	locker   Locker
	limiter  RateLimiter

	owner       string
	leaseTTL    time.Duration
	maxPasses   int
	concurrency int
	observers   []Observer
	now         func() time.Time

	router Router

	mu          sync.RWMutex
	definitions map[string]*Definition
	versions    map[string]map[int]*Definition
}

// New builds an engine.
func New(opts Options) (*Engine, error) {
	if opts.Store == nil {
		return nil, errors.New("ref/process: an engine needs a store")
	}
	if opts.Runner == nil {
		return nil, errors.New("ref/process: an engine needs a step runner")
	}
	engine := &Engine{
		store:       opts.Store,
		runner:      opts.Runner,
		enqueuer:    opts.Enqueuer,
		notifier:    opts.Notifier,
		locker:      opts.Locker,
		limiter:     opts.Limiter,
		owner:       opts.Owner,
		leaseTTL:    opts.LeaseTTL,
		maxPasses:   opts.MaxPasses,
		concurrency: opts.StepConcurrency,
		observers:   opts.Observers,
		now:         opts.Clock,
		definitions: map[string]*Definition{},
		versions:    map[string]map[int]*Definition{},
	}
	if engine.owner == "" {
		engine.owner = "replica-" + randomID()
	}
	if engine.leaseTTL <= 0 {
		engine.leaseTTL = 30 * time.Second
	}
	if engine.maxPasses <= 0 {
		engine.maxPasses = 100
	}
	if engine.concurrency <= 0 {
		engine.concurrency = 8
	}
	if engine.now == nil {
		engine.now = func() time.Time { return time.Now().UTC() }
	}
	return engine, nil
}

// Register installs a compiled definition. It compiles and validates first, so a
// broken definition never reaches a run.
func (e *Engine) Register(definition *Definition) error {
	if err := Compile(definition); err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	versions := e.versions[definition.Name]
	if versions == nil {
		versions = map[int]*Definition{}
		e.versions[definition.Name] = versions
	}
	if _, ok := versions[definition.Version]; ok {
		return fmt.Errorf("ref/process: process %q version %d is already registered", definition.Name, definition.Version)
	}
	versions[definition.Version] = definition
	if current := e.definitions[definition.Name]; current == nil || definition.Version > current.Version {
		e.definitions[definition.Name] = definition
	}
	return nil
}

// Definition returns a registered definition.
func (e *Engine) Definition(name string) (*Definition, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	definition, ok := e.definitions[name]
	return definition, ok
}

func (e *Engine) DefinitionVersion(name string, version int) (*Definition, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	versions := e.versions[name]
	if versions == nil {
		return nil, false
	}
	definition, ok := versions[version]
	return definition, ok
}

// Definitions returns every registered definition name.
func (e *Engine) Definitions() []string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	names := make([]string, 0, len(e.definitions))
	for name := range e.definitions {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// Migrate prepares the store.
func (e *Engine) Migrate(ctx context.Context) error { return e.store.Migrate(ctx) }

// Store exposes the store, for the admin surface ref/platform mounts.
func (e *Engine) Store() Store { return e.store }

// StartOptions configures one run's creation.
type StartOptions struct {
	// ID overrides the generated run id, for a caller that wants its own.
	ID string
	// IdempotencyKey deduplicates run creation. Starting twice with the same key
	// returns the existing run instead of creating a second one.
	IdempotencyKey string
	TenantID       string
	PrincipalID    string
	Identity       *IdentitySnapshot
	CorrelationID  string
	ParentRunID    string
	// Detached skips the initial advance, leaving the run pending for a worker to
	// pick up. Use it when the caller must not wait for the first step.
	Detached  bool
	DeferWake bool
}

// Start creates a run and, unless detached, advances it immediately.
//
// The immediate advance matters for the common case: a caller placing an order
// wants the reservation attempted now and the approval to wait, not both to wait
// for a worker to notice.
func (e *Engine) Start(ctx context.Context, process string, input any, opts StartOptions) (*Run, error) {
	definition, ok := e.Definition(process)
	if !ok {
		return nil, fmt.Errorf("ref/process: process %q is not registered", process)
	}

	if opts.IdempotencyKey != "" {
		existing, err := e.store.FindRunByIdempotency(ctx, opts.TenantID, process, opts.IdempotencyKey)
		switch {
		case err == nil:
			if opts.Detached && !opts.DeferWake && existing.Status.Active() && e.enqueuer != nil {
				if err := e.enqueuer.EnqueueAdvance(ctx, existing.ID); err != nil {
					return existing, err
				}
			}
			return existing, nil
		case !errors.Is(err, ErrRunNotFound):
			return nil, err
		}
	}

	shaped := input
	if definition.InputShaper != nil {
		var err error
		shaped, err = definition.InputShaper.Shape(input, Scope{"input": input})
		if err != nil {
			return nil, err
		}
	}
	encoded, err := json.Marshal(shaped)
	if err != nil {
		return nil, fmt.Errorf("ref/process: the run input cannot be serialised: %w", err)
	}

	now := e.now()
	run := &Run{
		ID:             orRandomID(opts.ID, "run"),
		Process:        process,
		Version:        definition.Version,
		Status:         StatusPending,
		Input:          encoded,
		TenantID:       opts.TenantID,
		PrincipalID:    opts.PrincipalID,
		Identity:       cloneIdentity(opts.Identity),
		IdempotencyKey: opts.IdempotencyKey,
		CorrelationID:  opts.CorrelationID,
		ParentRunID:    opts.ParentRunID,
		Visits:         map[string]int{},
		CreatedAt:      now,
		UpdatedAt:      now,
		Frames:         []Frame{{Step: definition.Start, Input: encoded, Attempt: 1}},
	}
	if definition.Timeout > 0 {
		deadline := now.Add(definition.Timeout)
		run.DeadlineAt = &deadline
	}
	if definition.SLA != nil {
		if definition.SLA.Target > 0 {
			target := now.Add(definition.SLA.Target)
			run.SLATargetAt = &target
		}
		if definition.SLA.Breach > 0 {
			breach := now.Add(definition.SLA.Breach)
			run.SLABreachAt = &breach
		}
	}

	// A concurrency cap is checked before creating the run rather than before
	// advancing it, because a queue of created-but-blocked runs is harder to
	// reason about than a rejected start.
	if definition.Concurrency > 0 {
		active, err := e.store.CountRuns(ctx, RunFilter{Process: process, Status: StatusRunning})
		if err != nil {
			return nil, err
		}
		if active >= definition.Concurrency {
			return nil, fmt.Errorf("ref/process: process %q already has %d runs in flight, which is its concurrency limit", process, active)
		}
	}

	stored, created, err := e.store.CreateRunOrGet(ctx, run)
	if err != nil {
		return nil, err
	}
	if !created {
		return stored, nil
	}
	run = stored
	if run.DeadlineAt != nil {
		if err := e.store.AddTimer(ctx, &Timer{
			ID: randomID(), RunID: run.ID, Fire: *run.DeadlineAt, Kind: "run_timeout", CreatedAt: now,
		}); err != nil {
			return run, err
		}
	}
	if run.SLABreachAt != nil {
		if err := e.store.AddTimer(ctx, &Timer{
			ID: randomID(), RunID: run.ID, Fire: *run.SLABreachAt, Kind: "sla", CreatedAt: now,
		}); err != nil {
			return run, err
		}
	}
	for _, observer := range e.observers {
		observer.RunStarted(run)
	}

	if opts.DeferWake {
		return run, nil
	}
	if opts.Detached {
		if e.enqueuer != nil {
			if err := e.enqueuer.EnqueueAdvance(ctx, run.ID); err != nil {
				return run, err
			}
		}
		return run, nil
	}
	if err := e.Advance(ctx, run.ID); err != nil {
		// The run exists and is recorded; returning it alongside the error lets a
		// caller report the id even when the first pass failed.
		latest, loadErr := e.store.GetRun(ctx, run.ID)
		if loadErr == nil {
			return latest, err
		}
		return run, err
	}
	return e.store.GetRun(ctx, run.ID)
}

// Advance drives a run as far as it will go right now.
//
// It is safe to call concurrently from any number of replicas: the lease makes
// exactly one of them the driver, and the others return without doing anything.
func (e *Engine) Advance(ctx context.Context, runID string) error {
	acquired, err := e.store.AcquireLease(ctx, runID, e.owner, e.leaseTTL)
	if err != nil {
		return err
	}
	if !acquired {
		// Another replica is advancing this run. Doing nothing is correct — it will
		// finish or its lease will lapse, and either way somebody advances the run.
		return nil
	}
	defer func() {
		// Use a background context: the request that triggered this may already be
		// cancelled, and failing to release the lease would strand the run until
		// the TTL expires.
		_ = e.store.ReleaseLease(context.WithoutCancel(ctx), runID, e.owner)
	}()
	ctx = withLeaseHeld(ctx, runID)

	for pass := 0; pass < e.maxPasses; pass++ {
		run, err := e.store.GetRun(ctx, runID)
		if err != nil {
			return err
		}
		if run.Status.Terminal() {
			return nil
		}
		definition, err := e.definitionFor(run)
		if err != nil {
			return e.failRun(ctx, run, "", err)
		}

		done, err := e.pass(ctx, definition, run)
		switch {
		case errors.Is(err, ErrRevisionConflict):
			// Somebody else changed the run underneath us — most likely a signal or
			// a task completion. Re-read and continue rather than overwriting.
			continue
		case err != nil:
			return err
		case done:
			return nil
		}

		if err := e.refreshLease(ctx, runID); err != nil {
			return err
		}
	}
	// Out of passes rather than out of work: hand the run back to the queue so
	// another worker continues it, keeping one long-running process from
	// monopolising this one.
	if e.enqueuer != nil {
		return e.enqueuer.EnqueueAdvance(ctx, runID)
	}
	return nil
}

func (e *Engine) refreshLease(ctx context.Context, runID string) error {
	held, err := e.store.RefreshLease(ctx, runID, e.owner, e.leaseTTL)
	if err != nil {
		return err
	}
	if !held {
		// The lease lapsed while we worked, and somebody else may now hold it. The
		// optimistic revision has kept the data safe; stopping here keeps it that
		// way.
		return errLeaseLost
	}
	return nil
}

var errLeaseLost = errors.New("ref/process: the run lease was lost")

// definitionFor resolves the definition a run should execute, honouring the
// migration policy.
func (e *Engine) definitionFor(run *Run) (*Definition, error) {
	if pinned, ok := e.DefinitionVersion(run.Process, run.Version); ok {
		return pinned, nil
	}
	definition, ok := e.Definition(run.Process)
	if !ok {
		return nil, fmt.Errorf("process %q is no longer registered, so run %s cannot advance", run.Process, run.ID)
	}
	if definition.Version == run.Version {
		return definition, nil
	}
	switch definition.MigrationPolicy {
	case "migrate":
		// Adopting the new definition is the author's explicit choice.
		run.Version = definition.Version
		return definition, nil
	case "fail":
		return nil, fmt.Errorf("run %s started on version %d and this deployment runs version %d; its migration policy is fail",
			run.ID, run.Version, definition.Version)
	default:
		// "pin": there is only one registered version per name in this process, so
		// a pinned run cannot find its own definition. Surfacing that is better
		// than silently running a graph the run never agreed to.
		return nil, fmt.Errorf("run %s is pinned to version %d, which this deployment no longer has (it runs version %d). Deploy the old version alongside, or set the process migration_policy to migrate",
			run.ID, run.Version, definition.Version)
	}
}

// pass executes one wave of ready frames and resolves their edges.
//
// It returns done=true when the run needs nothing more right now: it finished,
// parked, or has only future-dated frames.
func (e *Engine) pass(ctx context.Context, definition *Definition, run *Run) (bool, error) {
	now := e.now()

	if run.DeadlineAt != nil && now.After(*run.DeadlineAt) && !run.Status.Terminal() {
		return true, e.failRun(ctx, run, "", fmt.Errorf("the run exceeded its %s timeout", definition.Timeout))
	}
	if run.Status == StatusPending {
		run.Status = StatusRunning
		run.StartedAt = &now
	}

	ready, future := splitFrames(run.Frames, now)
	if len(ready) == 0 {
		if len(future) > 0 {
			// Only future work remains: park until the earliest of it, so a retry
			// backoff does not need a timer row of its own.
			return true, e.parkUntilFrame(ctx, run, future)
		}
		more, err := e.settle(ctx, definition, run)
		return !more, err
	}
	ready, held := throttle(definition, ready)

	// The remaining frames are whatever we are not executing in this wave. They
	// are written back alongside whatever this wave produces.
	run.Frames = append(held, future...)

	outcomes := e.executeWave(ctx, definition, run, ready)
	e.orderRaceEntrants(definition, outcomes)

	// Resolution is sequential even though execution was concurrent. Edge
	// resolution mutates the cursor, the visit counts and the join rows, and doing
	// that from several goroutines would need a lock around everything that
	// matters — at which point the concurrency buys nothing.
	for _, outcome := range outcomes {
		// Once a failure in this wave has started compensation, the wave's other
		// forward outcomes must not push more forward work onto a run that is
		// unwinding: their committed effects are already on the compensation list,
		// and anything they would continue into would only need undoing too.
		if run.Status == StatusCompensating && !outcome.frame.Compensate {
			continue
		}
		if err := e.resolve(ctx, definition, run, outcome); err != nil {
			return false, err
		}
		if run.Status.Terminal() {
			return true, e.store.SaveRun(ctx, run)
		}
	}

	if err := e.store.SaveRun(ctx, run); err != nil {
		return false, err
	}
	// More frames means another pass; none means settle on the next iteration,
	// which keeps the "is it finished" decision in one place.
	return false, nil
}

// throttle enforces max_concurrency on the frames a fan-out, parallel or
// iterator edge pushed: at most that many of one edge's frames execute in a wave,
// and the rest stay queued for the next. Without it the field was accepted and
// ignored, so a 10,000-item iterator ran as wide as the step concurrency allowed.
func throttle(definition *Definition, ready []Frame) (run, held []Frame) {
	var counts map[string]int
	for _, frame := range ready {
		if edge := edgeReaching(definition, frame, multiTargetEdges...); edge != nil && edge.MaxConcurrency > 0 {
			if counts == nil {
				counts = map[string]int{}
			}
			if counts[edge.Name] >= edge.MaxConcurrency {
				held = append(held, frame)
				continue
			}
			counts[edge.Name]++
		}
		run = append(run, frame)
	}
	return run, held
}

// splitFrames divides the cursor into what can run now and what cannot yet.
func splitFrames(frames []Frame, now time.Time) (ready, future []Frame) {
	for _, frame := range frames {
		if frame.NotBefore != nil && frame.NotBefore.After(now) {
			future = append(future, frame)
			continue
		}
		ready = append(ready, frame)
	}
	return ready, future
}

// stepOutcome is one executed frame's result.
type stepOutcome struct {
	frame  Frame
	step   *Step
	state  *StepState
	result any
	err    error
	// skipped records that a skip_when condition matched, so edge resolution uses
	// the skip result rather than treating it as a real execution.
	skipped bool
	// parked records that the step itself parked — a human task waiting for
	// somebody, or a rate limit not yet admitting the run.
	parked *WaitState
}

// executeWave runs the ready frames with bounded concurrency.
func (e *Engine) executeWave(ctx context.Context, definition *Definition, run *Run, frames []Frame) []stepOutcome {
	outcomes := make([]stepOutcome, len(frames))
	limit := min(e.concurrency, len(frames))
	semaphore := make(chan struct{}, limit)
	var wg sync.WaitGroup

	for index, frame := range frames {
		wg.Add(1)
		go func(index int, frame Frame) {
			defer wg.Done()
			semaphore <- struct{}{}
			defer func() { <-semaphore }()
			outcomes[index] = e.executeFrame(ctx, definition, run, frame)
		}(index, frame)
	}
	wg.Wait()
	return outcomes
}

// runStepSafely calls the step runner with panic recovery.
// A panic in a step body must not crash the process engine.
func (e *Engine) runStepSafely(ctx context.Context, run *Run, step *Step, frame Frame, input any, scope Scope) (any, error) {
	var result any
	var err error
	func() {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("ref: panic in step %q: %v", step.Name, r)
			}
		}()
		result, err = e.runner.RunStep(ctx, StepCall{
			Run: run, Step: step, Frame: frame, Intent: step.Intent, Input: input, Scope: scope,
		})
	}()
	return result, err
}

// executeFrame runs one frame: a step, or a step's compensation.
func (e *Engine) executeFrame(ctx context.Context, definition *Definition, run *Run, frame Frame) stepOutcome {
	outcome := stepOutcome{frame: frame}

	step, ok := definition.Step(frame.Step)
	if !ok {
		outcome.err = fmt.Errorf("step %q is not declared in process %q", frame.Step, definition.Name)
		return outcome
	}
	outcome.step = step

	if frame.Compensate {
		return e.executeCompensation(ctx, run, step, frame)
	}

	if run.Steps >= definition.MaxSteps {
		outcome.err = fmt.Errorf("the run executed %d steps, which is its max_steps limit — there is probably a loop that does not converge", run.Steps)
		return outcome
	}
	if run.Visits[frame.Step] >= definition.MaxVisits {
		outcome.err = fmt.Errorf("step %q ran %d times, which is its max_visits limit", frame.Step, run.Visits[frame.Step])
		return outcome
	}

	var input any
	if len(frame.Input) > 0 {
		if err := json.Unmarshal(frame.Input, &input); err != nil {
			outcome.err = fmt.Errorf("step %q received unreadable input: %w", frame.Step, err)
			return outcome
		}
	}
	scope, err := e.scope(ctx, run, frame, input, nil)
	if err != nil {
		outcome.err = err
		return outcome
	}

	if step.SkipWhen != nil {
		skip, err := step.SkipWhen.Eval(scope)
		if err != nil {
			outcome.err = fmt.Errorf("step %q skip_when: %w", frame.Step, err)
			return outcome
		}
		if skip {
			state, recordErr := e.recordSkip(ctx, run, frame, step, input)
			if recordErr != nil {
				outcome.err = recordErr
				return outcome
			}
			outcome.skipped = true
			outcome.result = step.SkipResult
			outcome.state = state
			return outcome
		}
	}

	// A human task step does not execute a body: it creates a work item and parks.
	if step.Task != nil {
		wait, state, err := e.openTask(ctx, run, step, frame, scope, input)
		outcome.parked, outcome.state, outcome.err = wait, state, err
		return outcome
	}

	if step.RateLimit != "" && e.limiter != nil {
		allowed, wait, err := e.checkStepRateLimit(ctx, step, scope)
		if err != nil {
			outcome.err = err
			return outcome
		}
		if !allowed {
			outcome.parked = wait
			return outcome
		}
	}

	var lockToken string
	if step.Lock != "" && e.locker != nil {
		token, acquired, err := e.acquireStepLock(ctx, step, scope)
		if err != nil {
			outcome.err = err
			return outcome
		}
		if !acquired {
			// Somebody else holds the lock. Parking and retrying is right: the work
			// is still wanted, just not right now.
			until := e.now().Add(max(step.LockWait, time.Second))
			outcome.parked = &WaitState{Reason: "lock", Step: frame.Step, Until: &until,
				Detail: fmt.Sprintf("waiting for the %s lock", step.Lock)}
			return outcome
		}
		lockToken = token
		defer func() {
			if lockToken != "" {
				key, _ := step.LockKey.Value(scope)
				_ = e.locker.Release(context.WithoutCancel(ctx), fmt.Sprint(key), lockToken)
			}
		}()
	}

	shaped := input
	if step.InputShaper != nil {
		shaped, err = step.InputShaper.Shape(input, scope)
		if err != nil {
			if errors.Is(err, ErrFiltered) {
				state, recordErr := e.recordSkip(ctx, run, frame, step, input)
				if recordErr != nil {
					outcome.err = recordErr
					return outcome
				}
				outcome.skipped = true
				outcome.state = state
				return outcome
			}
			outcome.err = fmt.Errorf("step %q input shaping: %w", frame.Step, err)
			return outcome
		}
	}

	state := &StepState{
		RunID:     run.ID,
		Step:      frame.Step,
		Key:       frame.StateKey(),
		Status:    StepRunning,
		Attempt:   max(frame.Attempt, 1),
		StartedAt: e.now(),
	}
	encodedInput, err := json.Marshal(shaped)
	if err != nil {
		outcome.err = err
		return outcome
	}
	state.Input = encodedInput
	if err := e.store.SaveStep(ctx, state); err != nil {
		outcome.err = err
		return outcome
	}

	runCtx := ctx
	limit := step.Timeout
	// A timeout edge bounds the duration of the step it reached. For a step with a
	// body that bound is the body's context deadline; overrunning it takes the
	// edge's on_timeout (resolveFailure). A parked target is bounded by the
	// edge's timer instead.
	if edge := edgeReaching(definition, frame, EdgeTimeout); edge != nil && (limit <= 0 || edge.Timeout < limit) {
		limit = edge.Timeout
	}
	if limit > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, limit)
		defer cancel()
	}

	var result any
	if step.Process != "" {
		// A child process is started rather than called: the parent parks on its
		// completion event, so the child's own durability applies — unless the
		// child finished within the call, in which case its outcome is the step's.
		wait, childResult, childErr := e.startChild(ctx, run, step, frame, shaped)
		if childErr == nil && wait != nil {
			outcome.parked, outcome.state = wait, state
			return outcome
		}
		result, err = childResult, childErr
		if err == nil {
			err = childOutcomeError(childResult)
		}
	} else {
		result, err = e.runStepSafely(runCtx, run, step, frame, shaped, scope)
	}
	finished := e.now()
	state.FinishedAt = &finished

	if err != nil {
		state.Status, state.Error = StepFailed, err.Error()
		if saveErr := e.store.SaveStep(ctx, state); saveErr != nil {
			outcome.state, outcome.err = state, saveErr
			return outcome
		}
		outcome.state, outcome.err = state, err
		return outcome
	}

	if step.OutputShaper != nil {
		resultScope, scopeErr := e.scope(ctx, run, frame, input, result)
		if scopeErr != nil {
			outcome.err = scopeErr
			return outcome
		}
		shapedResult, shapeErr := step.OutputShaper.Shape(result, resultScope)
		if errors.Is(shapeErr, ErrFiltered) {
			state.Status = StepSkipped
			if saveErr := e.store.SaveStep(ctx, state); saveErr != nil {
				outcome.state, outcome.err = state, saveErr
				return outcome
			}
			outcome.state, outcome.skipped = state, true
			return outcome
		}
		if shapeErr != nil {
			state.Status, state.Error = StepFailed, shapeErr.Error()
			if saveErr := e.store.SaveStep(ctx, state); saveErr != nil {
				outcome.state, outcome.err = state, saveErr
				return outcome
			}
			outcome.state, outcome.err = state, shapeErr
			return outcome
		}
		result = shapedResult
	}

	sequence, err := e.store.NextStepSequence(ctx, run.ID)
	if err != nil {
		outcome.err = err
		return outcome
	}
	state.Status, state.Sequence = StepCompleted, sequence
	encodedResult, err := json.Marshal(result)
	if err != nil {
		outcome.err = err
		return outcome
	}
	state.Result = encodedResult
	if err := e.store.SaveStep(ctx, state); err != nil {
		outcome.err = err
		return outcome
	}

	outcome.state, outcome.result = state, result
	for _, observer := range e.observers {
		observer.StepFinished(run, state)
	}
	return outcome
}

func (e *Engine) recordSkip(ctx context.Context, run *Run, frame Frame, step *Step, input any) (*StepState, error) {
	now := e.now()
	sequence, err := e.store.NextStepSequence(ctx, run.ID)
	if err != nil {
		return nil, err
	}
	state := &StepState{
		RunID: run.ID, Step: frame.Step, Key: frame.StateKey(),
		Status: StepSkipped, Attempt: max(frame.Attempt, 1),
		Sequence: sequence, StartedAt: now, FinishedAt: &now,
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	state.Input = encoded
	if step.SkipResult != nil {
		encoded, err = json.Marshal(step.SkipResult)
		if err != nil {
			return nil, err
		}
		state.Result = encoded
	}
	if err := e.store.SaveStep(ctx, state); err != nil {
		return nil, err
	}
	return state, nil
}

func (e *Engine) checkStepRateLimit(ctx context.Context, step *Step, scope Scope) (bool, *WaitState, error) {
	key := step.Name
	if step.RateLimitKey != nil {
		value, err := step.RateLimitKey.Value(scope)
		if err != nil {
			return false, nil, fmt.Errorf("step %q rate_limit_key: %w", step.Name, err)
		}
		key = fmt.Sprint(value)
	}
	allowed, _, resetAt, err := e.limiter.Allow(ctx, "step:"+step.Name+":"+key, step.Limit, step.Window)
	if err != nil {
		// Fail closed on a limiter outage: the limit exists to protect something,
		// and this is when it matters most.
		return false, nil, fmt.Errorf("step %q: the rate limiter is unavailable: %w", step.Name, err)
	}
	if allowed {
		return true, nil, nil
	}
	if resetAt.IsZero() {
		resetAt = e.now().Add(step.Window)
	}
	return false, &WaitState{
		Reason: "rate_limit", Step: step.Name, Until: &resetAt,
		Detail: fmt.Sprintf("the %s rate limit is exhausted", step.RateLimit),
	}, nil
}

func (e *Engine) acquireStepLock(ctx context.Context, step *Step, scope Scope) (string, bool, error) {
	value, err := step.LockKey.Value(scope)
	if err != nil {
		return "", false, fmt.Errorf("step %q lock_key: %w", step.Name, err)
	}
	key := fmt.Sprint(value)
	if key == "" {
		return "", false, fmt.Errorf("step %q: the lock key evaluated to empty", step.Name)
	}
	ttl := step.LockTTL
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	token, acquired, err := e.locker.Acquire(ctx, key, ttl)
	if err != nil {
		return "", false, fmt.Errorf("step %q: the lock service is unavailable: %w", step.Name, err)
	}
	return token, acquired, nil
}

// ---------------------------------------------------------------------------
// Settling
// ---------------------------------------------------------------------------

// settle decides what an empty cursor means: finished, parked, or the end of a
// compensation. It reports whether settling produced more work for another pass
// (a join that could only now be resolved).
func (e *Engine) settle(ctx context.Context, definition *Definition, run *Run) (bool, error) {
	if run.Status == StatusCompensating {
		return false, e.finishCompensation(ctx, run)
	}

	outstanding, err := e.hasOutstandingWait(ctx, run)
	if err != nil {
		return false, err
	}
	if outstanding {
		if run.Status != StatusWaiting {
			run.Status = StatusWaiting
			if run.Waiting == nil {
				run.Waiting = &WaitState{Reason: "external", Detail: "waiting for a timer, an event or a person"}
			}
			for _, observer := range e.observers {
				observer.RunParked(run, *run.Waiting)
			}
		}
		return false, e.store.SaveRun(ctx, run)
	}
	more, err := e.settleJoins(ctx, definition, run)
	if err != nil || run.Status.Terminal() {
		return false, err
	}
	if more {
		return true, e.store.SaveRun(ctx, run)
	}
	return false, e.completeRun(ctx, definition, run)
}

// hasOutstandingWait reports whether anything external will wake this run. It is
// the difference between "finished" and "stuck", and getting it wrong in either
// direction is bad: a completed run that stays waiting never reports success, and
// a waiting run marked complete drops an approval on the floor.
func (e *Engine) hasOutstandingWait(ctx context.Context, run *Run) (bool, error) {
	timers, err := e.store.ListTimers(ctx, run.ID)
	if err != nil {
		return false, err
	}
	for _, timer := range timers {
		// The run's own timeout and SLA timers are housekeeping, not work: a run
		// whose only pending timer is its deadline has actually finished. A wake
		// timer exists only to advance future-dated frames, and settle is reached
		// only when there are none — so a leftover wake (from a retry that was
		// advanced directly rather than by its timer) is not work either. Counting
		// it parked a finished run as waiting, and when the wake then fired the run
		// settled onto its own claimed wake row and stayed waiting for good.
		switch timer.Kind {
		case "run_timeout", "sla", "wake":
		default:
			return true, nil
		}
	}
	subscriptions, err := e.store.ListSubscriptions(ctx, run.ID)
	if err != nil {
		return false, err
	}
	if len(subscriptions) > 0 {
		return true, nil
	}
	tasks, err := e.store.ListTasks(ctx, TaskFilter{RunID: run.ID, Limit: 1})
	if err != nil {
		return false, err
	}
	return len(tasks) > 0, nil
}

// parkUntilFrame parks a run whose only remaining work is future-dated.
func (e *Engine) parkUntilFrame(ctx context.Context, run *Run, future []Frame) error {
	earliest := future[0].NotBefore
	for _, frame := range future[1:] {
		if frame.NotBefore != nil && (earliest == nil || frame.NotBefore.Before(*earliest)) {
			earliest = frame.NotBefore
		}
	}
	run.Frames = future
	run.Status = StatusWaiting
	run.Waiting = &WaitState{Reason: "timer", Until: earliest, Detail: "waiting before the next attempt"}
	if earliest != nil {
		if err := e.store.AddTimer(ctx, &Timer{
			ID: "wake:" + run.ID, RunID: run.ID, Fire: *earliest, Kind: "wake", CreatedAt: e.now(),
		}); err != nil {
			return err
		}
	}
	if err := e.store.SaveRun(ctx, run); err != nil {
		return err
	}
	for _, observer := range e.observers {
		observer.RunParked(run, *run.Waiting)
	}
	if earliest != nil && e.enqueuer != nil {
		_ = e.enqueuer.EnqueueAdvanceAt(ctx, run.ID, *earliest)
	}
	return nil
}

// completeRun marks a run finished and publishes its output.
func (e *Engine) completeRun(ctx context.Context, definition *Definition, run *Run) error {
	now := e.now()
	run.Status = StatusCompleted
	run.CompletedAt = &now
	run.Waiting = nil
	run.Frames = nil

	// The run's output is the last completed step's result, shaped by the
	// definition's output pipeline. "Last completed" is by sequence, which is the
	// completion order rather than the declaration order — what an author means by
	// "the result" of a graph that ended on one of several branches.
	states, err := e.store.ListSteps(ctx, run.ID)
	if err != nil {
		return err
	}
	for i := len(states) - 1; i >= 0; i-- {
		if states[i].Status == StepCompleted && len(states[i].Result) > 0 {
			run.Output = states[i].Result
			break
		}
	}
	if definition.OutputShaper != nil && len(run.Output) > 0 {
		var value any
		if err := json.Unmarshal(run.Output, &value); err != nil {
			return err
		}
		scope, err := e.scope(ctx, run, Frame{}, nil, value)
		if err != nil {
			return err
		}
		shaped, shapeErr := definition.OutputShaper.Shape(value, scope)
		if errors.Is(shapeErr, ErrFiltered) {
			run.Output = nil
		} else if shapeErr != nil {
			return shapeErr
		} else {
			encoded, err := json.Marshal(shaped)
			if err != nil {
				return err
			}
			run.Output = encoded
		}
	}

	if err := e.store.SaveRun(ctx, run); err != nil {
		return err
	}
	// Housekeeping timers are only useful while the run is live.
	if err := e.store.DeleteRunTimers(ctx, run.ID); err != nil {
		return err
	}
	if err := e.store.DeleteRunSubscriptions(ctx, run.ID); err != nil {
		return err
	}
	if err := e.signalParent(ctx, run); err != nil {
		return err
	}
	for _, observer := range e.observers {
		observer.RunFinished(run)
	}
	return nil
}

// failRun ends a run in failure. Compensation, if there is any to do, has already
// run by the time this is called.
func (e *Engine) failRun(ctx context.Context, run *Run, step string, cause error) error {
	now := e.now()
	run.Status = StatusFailed
	run.CompletedAt = &now
	run.Error = cause.Error()
	run.FailedStep = step
	run.Waiting = nil
	run.Frames = nil
	if err := e.store.SaveRun(ctx, run); err != nil {
		return err
	}
	if err := e.store.DeleteRunTimers(ctx, run.ID); err != nil {
		return err
	}
	if err := e.store.DeleteRunSubscriptions(ctx, run.ID); err != nil {
		return err
	}
	if err := e.cancelRunTasks(ctx, run); err != nil {
		return err
	}
	if e.notifier != nil {
		_ = e.notifier.NotifyProcess(ctx, NotifyEvent{Kind: "run_failed", Run: run, Step: step, Detail: cause.Error()})
	}
	if err := e.signalParent(ctx, run); err != nil {
		return err
	}
	for _, observer := range e.observers {
		observer.RunFinished(run)
	}
	return nil
}

// Cancel ends a run by decision. An already-terminal run is left alone rather
// than reported as an error: the caller's goal is met.
func (e *Engine) Cancel(ctx context.Context, runID, reason string) (*Run, error) {
	acquired, err := e.store.AcquireLease(ctx, runID, e.owner, e.leaseTTL)
	if err != nil {
		return nil, err
	}
	if !acquired {
		return nil, fmt.Errorf("ref/process: run %s is being advanced right now; try again in a moment", runID)
	}
	defer func() { _ = e.store.ReleaseLease(context.WithoutCancel(ctx), runID, e.owner) }()
	ctx = withLeaseHeld(ctx, runID)

	run, err := e.store.GetRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	if run.Status.Terminal() {
		return run, nil
	}
	now := e.now()
	run.Status = StatusCancelled
	run.CompletedAt = &now
	run.Waiting = nil
	run.Frames = nil
	if reason != "" {
		run.Error = reason
	}
	if err := e.store.SaveRun(ctx, run); err != nil {
		return nil, err
	}
	if err := e.store.DeleteRunTimers(ctx, runID); err != nil {
		return nil, err
	}
	if err := e.store.DeleteRunSubscriptions(ctx, runID); err != nil {
		return nil, err
	}
	if err := e.cancelRunTasks(ctx, run); err != nil {
		return nil, err
	}
	if err := e.signalParent(ctx, run); err != nil {
		return nil, err
	}
	for _, observer := range e.observers {
		observer.RunFinished(run)
	}
	return run, nil
}

// cancelRunTasks closes the work items of a run that has ended, so nobody is
// asked to approve something that no longer exists.
func (e *Engine) cancelRunTasks(ctx context.Context, run *Run) error {
	tasks, err := e.store.ListTasks(ctx, TaskFilter{RunID: run.ID, Limit: 500})
	if err != nil {
		return err
	}
	for _, task := range tasks {
		if !task.Open() {
			continue
		}
		task.Status = TaskCancelled
		if err := e.store.SaveTask(ctx, task); err != nil {
			return err
		}
	}
	return nil
}

// Snapshot returns the operator view of a run.
func (e *Engine) Snapshot(ctx context.Context, runID string) (*Snapshot, error) {
	run, err := e.store.GetRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	snapshot := &Snapshot{Run: run}

	// Collect errors from sub-queries — partial data is worse than no data
	// for operators debugging production incidents.
	var errs []error
	snapshot.Steps, err = e.store.ListSteps(ctx, runID)
	if err != nil {
		errs = append(errs, fmt.Errorf("list steps: %w", err))
	}
	snapshot.Tasks, err = e.store.ListTasks(ctx, TaskFilter{RunID: runID, Limit: 100})
	if err != nil {
		errs = append(errs, fmt.Errorf("list tasks: %w", err))
	}
	snapshot.Timers, err = e.store.ListTimers(ctx, runID)
	if err != nil {
		errs = append(errs, fmt.Errorf("list timers: %w", err))
	}
	snapshot.Subscriptions, err = e.store.ListSubscriptions(ctx, runID)
	if err != nil {
		errs = append(errs, fmt.Errorf("list subscriptions: %w", err))
	}

	if len(errs) > 0 {
		return snapshot, fmt.Errorf("ref: snapshot partial: %v", errs)
	}
	return snapshot, nil
}

// ListRuns exposes the store's listing for an operations view.
func (e *Engine) ListRuns(ctx context.Context, filter RunFilter) ([]*Run, error) {
	return e.store.ListRuns(ctx, filter)
}

// ---------------------------------------------------------------------------
// Scope
// ---------------------------------------------------------------------------

// scope builds what guards, shapers and renderers see.
//
// Every step's result is exposed under results.<step>, which is what lets a later
// edge test something an earlier step produced rather than only the immediately
// preceding one. That lookup is the difference between a graph you can express
// and one you have to thread data through manually.
func (e *Engine) scope(ctx context.Context, run *Run, frame Frame, input, result any) (Scope, error) {
	var runInput any
	if len(run.Input) > 0 {
		if err := json.Unmarshal(run.Input, &runInput); err != nil {
			return nil, fmt.Errorf("run %s has unreadable input: %w", run.ID, err)
		}
	}
	results := map[string]any{}
	states, err := e.store.ListSteps(ctx, run.ID)
	if err != nil {
		return nil, err
	}
	for _, state := range states {
		if len(state.Result) == 0 {
			continue
		}
		var value any
		if err := json.Unmarshal(state.Result, &value); err != nil {
			return nil, fmt.Errorf("step state %s/%s is unreadable: %w", state.Step, state.Key, err)
		}
		results[state.Step] = value
		if state.Key != "" {
			results[state.Key] = value
		}
	}
	scope := Scope{
		"run": map[string]any{
			"id":         run.ID,
			"process":    run.Process,
			"status":     string(run.Status),
			"input":      runInput,
			"tenant_id":  run.TenantID,
			"principal":  run.PrincipalID,
			"created_at": run.CreatedAt.Format(time.RFC3339),
			"steps":      run.Steps,
		},
		"step": map[string]any{
			"name":    frame.Step,
			"from":    frame.From,
			"attempt": max(frame.Attempt, 1),
			"key":     frame.StateKey(),
		},
		"input":     input,
		"result":    result,
		"results":   results,
		"tenant":    run.TenantID,
		"principal": map[string]any{"id": run.PrincipalID},
		"now":       e.now().Format(time.RFC3339),
	}
	if result == nil {
		// Before a step runs, `result` reads as its input. An edge guard written
		// against `result` on the way in and on the way out then behaves the same,
		// which is what an author expects from one name.
		scope["result"] = input
	}
	return scope, nil
}

// ErrFiltered reports that a shaper's filter rejected a payload. On an edge it
// means the edge does not traverse; on a step input it means the step is skipped.
var ErrFiltered = errors.New("ref/process: payload filtered")

// continueFromStep resolves one step's outgoing edges against a result the step
// produced outside an ordinary execution, then advances the run.
//
// Two things reach the engine this way: a human task somebody completed, and a
// child process that finished. Both have already "run" — the step's work is done —
// so pushing a frame for the step would re-run it (opening a second task, starting
// a second child). This resolves off the result instead, which is why it exists
// rather than the two callers each having their own version to drift apart.
func (e *Engine) continueFromStep(ctx context.Context, runID, stepName, stateKey string, result map[string]any, stepErr error) error {
	if err := e.applyStepCompletion(ctx, runID, stepName, stateKey, result, stepErr, nil); err != nil {
		return err
	}
	return e.advanceOrEnqueue(ctx, runID)
}

// applyStepCompletion is continueFromStep without the advance, for a caller that
// must consume its own durable marker (a child's completion subscription) after
// the continuation is on the cursor and before the run advances past it.
//
// It waits for the lease rather than giving up on it. The previous version, on
// finding the run busy, returned success after queueing an advance — but nothing
// in an advance looks for a completed-but-unresolved step, so the task's decision
// or the child's result was silently lost (and a child's completion subscription
// acknowledged). Now a run that stays busy is an error the caller can retry: a
// signal delivery releases its subscription for redelivery.
//
// wanted, when given, is checked under the lease: it lets a caller confirm the
// run still wants this completion — a child whose parent timed out or lost a
// race was abandoned while its completion waited for the lease, and must not
// continue the graph a second time.
func (e *Engine) applyStepCompletion(ctx context.Context, runID, stepName, stateKey string, result map[string]any, stepErr error,
	wanted func(ctx context.Context) (bool, error)) error {
	if stateKey == "" {
		stateKey = stepName
	}
	return e.withRunLease(ctx, runID, func(ctx context.Context, run *Run) error {
		if run.Status == StatusCompensating {
			// The run is unwinding. Its forward work — this step's continuation
			// included — was abandoned when the rollback began.
			return nil
		}
		if wanted != nil {
			ok, err := wanted(ctx)
			if err != nil || !ok {
				return err
			}
		}
		definition, err := e.definitionFor(run)
		if err != nil {
			return e.failRun(ctx, run, stepName, err)
		}
		step, ok := definition.Step(stepName)
		if !ok {
			return e.failRun(ctx, run, stepName,
				fmt.Errorf("step %q is no longer declared in process %q", stepName, run.Process))
		}

		run.Status = StatusRunning
		run.Waiting = nil

		// Record the completion so a later edge reading results.<step> sees it, and
		// so compensation knows this step committed.
		now := e.now()
		state, err := e.stepStateByKey(ctx, runID, stateKey)
		if err != nil {
			return err
		}
		if state == nil {
			state = &StepState{RunID: runID, Step: stepName, Key: stateKey, Attempt: 1, StartedAt: now}
		}
		state.FinishedAt = &now
		if stepErr != nil {
			state.Status, state.Error = StepFailed, stepErr.Error()
		} else {
			sequence, err := e.store.NextStepSequence(ctx, runID)
			if err != nil {
				return err
			}
			state.Status, state.Sequence = StepCompleted, sequence
			encoded, err := json.Marshal(result)
			if err != nil {
				return err
			}
			state.Result = encoded
		}
		if err := e.store.SaveStep(ctx, state); err != nil {
			return err
		}

		// From here the step resolves exactly as an executed one does — retry,
		// error edges, joins, races, terminal handling — through the same code.
		key := stateKey
		if key == stepName {
			key = ""
		}
		return e.resolve(ctx, definition, run, stepOutcome{
			frame:  Frame{Step: stepName, Key: key, Attempt: 1},
			step:   step,
			state:  state,
			result: result,
			err:    stepErr,
		})
	})
}

// ---------------------------------------------------------------------------
// Leases outside an advance
// ---------------------------------------------------------------------------

// leaseHeldKey marks, on a context, a run whose lease this call chain holds.
type leaseHeldKey struct{ runID string }

func withLeaseHeld(ctx context.Context, runID string) context.Context {
	return context.WithValue(ctx, leaseHeldKey{runID: runID}, true)
}

// leaseHeld reports whether the lease on a run is held further up this call
// chain. Leases are not re-entrant, so code that would otherwise wait for one
// its own caller holds — a child run completing synchronously inside its
// parent's advance, a step body signalling its own run — must not wait: the
// wait can only end in a timeout.
func leaseHeld(ctx context.Context, runID string) bool {
	held, _ := ctx.Value(leaseHeldKey{runID: runID}).(bool)
	return held
}

// errRunBusy reports that a run's lease could not be had.
var errRunBusy = errors.New("ref/process: the run is being advanced elsewhere")

// acquireLeaseWaiting takes a run's lease, waiting briefly for a concurrent
// advance to finish.
func (e *Engine) acquireLeaseWaiting(ctx context.Context, runID string) error {
	if leaseHeld(ctx, runID) {
		return fmt.Errorf("%w: run %s is being advanced by this very call", errRunBusy, runID)
	}
	for attempt := range 3 {
		acquired, err := e.store.AcquireLease(ctx, runID, e.owner, e.leaseTTL)
		if err != nil {
			return err
		}
		if acquired {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(attempt+1) * 100 * time.Millisecond):
		}
	}
	return fmt.Errorf("%w: run %s stayed busy across three attempts", errRunBusy, runID)
}

// withRunLease loads a run under its lease, lets fn change it, and saves it.
//
// It is how everything outside an advance — a signal, a timer, a task, an
// operator — changes a run without interleaving with an advance. A terminal run
// is left alone (fn is not called); a run fn itself finished is not saved again.
// A revision conflict re-reads and retries.
func (e *Engine) withRunLease(ctx context.Context, runID string, fn func(ctx context.Context, run *Run) error) error {
	for range 3 {
		if err := e.acquireLeaseWaiting(ctx, runID); err != nil {
			return err
		}
		err := func() error {
			defer func() { _ = e.store.ReleaseLease(context.WithoutCancel(ctx), runID, e.owner) }()
			leased := withLeaseHeld(ctx, runID)
			run, err := e.store.GetRun(leased, runID)
			if err != nil {
				return err
			}
			if run.Status.Terminal() {
				return nil
			}
			if err := fn(leased, run); err != nil {
				return err
			}
			if run.Status.Terminal() {
				return nil
			}
			return e.store.SaveRun(leased, run)
		}()
		if errors.Is(err, ErrRevisionConflict) {
			continue
		}
		return err
	}
	return fmt.Errorf("%w: run %s kept changing underneath three attempts", errRunBusy, runID)
}
