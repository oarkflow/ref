package process

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"strings"
	"time"
)

// Edge resolution: what happens after a step finishes.
//
// One contract holds across every edge type without exception: the guard is
// evaluated before the edge does anything. A fan-out, a manual gate and a
// compensation edge all honour a `when` condition exactly as a simple edge does.
// DAGFlow originally let several types bypass their own conditions, which meant a
// graph's behaviour depended on which edge type an author happened to pick; one
// contract, checked in one place, removes that whole class of surprise.
//
// The second rule: success-path and error-path edges never mix. A failed step
// resolves only error, fallback and compensate edges; a successful one resolves
// only the rest. There is no arrangement of conditions that can run a compensation
// on the happy path.

// edgeFault is an error that comes from how a run's own graph resolved — a
// condition that could not be evaluated, a loop past its cycle cap, an edge the
// engine is not configured to honour — rather than from the store.
//
// The distinction matters because the two need opposite handling. A store error
// is transient: the advance returns it, nothing is saved, and a later advance
// retries. A graph fault is not: retrying re-executes the step that produced it,
// hits the same fault, and wedges the run in "running" forever while re-running
// its body on every advance. So a fault fails the run (through compensation, like
// any failure) instead of being returned.
type edgeFault struct{ err error }

func (f *edgeFault) Error() string { return f.err.Error() }
func (f *edgeFault) Unwrap() error { return f.err }

func faultf(format string, args ...any) error {
	return &edgeFault{err: fmt.Errorf(format, args...)}
}

// resolve resolves one outcome, turning a graph fault into a failed run.
func (e *Engine) resolve(ctx context.Context, definition *Definition, run *Run, outcome stepOutcome) error {
	err := e.resolveOutcome(ctx, definition, run, outcome)
	var fault *edgeFault
	if errors.As(err, &fault) {
		return e.beginCompensation(ctx, definition, run, outcome.frame.Step, fault.err)
	}
	return err
}

// resolveOutcome turns one executed step into cursor changes.
func (e *Engine) resolveOutcome(ctx context.Context, definition *Definition, run *Run, outcome stepOutcome) error {
	if outcome.step == nil {
		return e.failRun(ctx, run, outcome.frame.Step, outcome.err)
	}
	if outcome.frame.Compensate {
		return e.afterCompensation(ctx, run, outcome)
	}
	// A parked step contributed no result and no traversal. Its continuation is
	// held by the task, timer or subscription that parked it.
	if outcome.parked != nil {
		return e.resolvePark(ctx, definition, run, outcome)
	}

	if !outcome.skipped {
		run.Steps++
		if run.Visits == nil {
			run.Visits = map[string]int{}
		}
		run.Visits[outcome.frame.Step]++
	}

	// The step has finished for good unless it is about to retry. Whatever was
	// watching it — a timeout edge's deadline, a task step's escalation — has
	// nothing left to watch. Leaving those timers would hold a finished run open
	// until they fired, and then fire a timeout on work that completed in time.
	if outcome.err == nil || !e.willRetry(definition, outcome) {
		if err := e.disarmStepWatchers(ctx, definition, run, outcome); err != nil {
			return err
		}
	}

	// A race entrant is judged by its race before its own edges: only the winner
	// continues the graph.
	if race := e.raceOf(definition, outcome.frame); race != nil {
		suppressed, err := e.resolveRaceEntrant(ctx, definition, run, outcome, race)
		if err != nil || suppressed {
			return err
		}
	}

	if outcome.err != nil {
		return e.resolveFailure(ctx, definition, run, outcome)
	}

	scope, err := e.scope(ctx, run, outcome.frame, nil, outcome.result)
	if err != nil {
		return err
	}
	edges := definition.Outgoing(outcome.frame.Step)
	traversed, err := e.traverse(ctx, definition, run, outcome, edges, scope, false)
	if err != nil {
		return err
	}

	// A step marked terminal may end its branch without traversing anything; the
	// run completes when nothing else is outstanding (see settle). It does not
	// complete the run on the spot: in a fan-out, a short terminal branch must not
	// drop the work still queued on the others, and a terminal step whose own
	// edges did traverse has more to do.
	//
	// A step that is not terminal and traversed nothing has reached a dead end the
	// compiler could not see — every edge's condition was false — and saying so
	// beats a run that silently stops.
	if traversed == 0 && !outcome.step.Terminal && !e.hasRemainingWork(run) {
		return e.failRun(ctx, run, outcome.frame.Step,
			fmt.Errorf("step %q finished but none of its %d outgoing edges' conditions matched, so the run cannot continue",
				outcome.frame.Step, len(edges)))
	}
	return nil
}

func (e *Engine) hasRemainingWork(run *Run) bool { return len(run.Frames) > 0 }

// resolvePark records why a step parked and arms whatever the park needs.
func (e *Engine) resolvePark(ctx context.Context, definition *Definition, run *Run, outcome stepOutcome) error {
	wait := *outcome.parked
	switch wait.Reason {
	case "rate_limit", "lock":
		// The step could not start yet. Nothing durable holds a lock or rate-limit
		// park — no task, no timer, no subscription — so the frame itself must go
		// back on the cursor, dated for when it may try again. Dropping it (as a
		// bare recordPark did) lost the step: the run then settled and completed
		// without ever running it.
		frame := outcome.frame
		when := e.now().Add(time.Second)
		if wait.Until != nil {
			when = *wait.Until
		}
		frame.NotBefore = &when
		run.Frames = append(run.Frames, frame)
	case "task":
		if outcome.state != nil {
			if err := e.armTaskEscalations(ctx, definition, run, outcome, wait.TaskID); err != nil {
				return err
			}
		}
	}
	return e.recordPark(ctx, run, wait)
}

// retryPolicyFor picks the policy governing a failed frame: the step's own, then
// the retry edge that reached it, then the process default.
func (e *Engine) retryPolicyFor(definition *Definition, outcome stepOutcome) *RetryPolicy {
	if outcome.step.Retry != nil {
		return outcome.step.Retry
	}
	if edge := edgeReaching(definition, outcome.frame, EdgeRetry); edge != nil {
		// A retry edge's timeout is its backoff: the first retry waits that long and
		// each later one doubles it.
		return &RetryPolicy{MaxAttempts: edge.Attempts, Strategy: "exponential", InitialDelay: edge.Timeout}
	}
	return definition.Retry
}

// willRetry reports whether a failed outcome is about to be retried.
func (e *Engine) willRetry(definition *Definition, outcome stepOutcome) bool {
	if outcome.err == nil || outcome.step == nil {
		return false
	}
	// Overrunning a timeout edge's bound is not retried: the bound is on the
	// target's duration, and another attempt would only overrun it again.
	if e.timedOutByEdge(definition, outcome) != nil {
		return false
	}
	policy := e.retryPolicyFor(definition, outcome)
	return policy != nil && outcome.frame.Attempt < max(policy.MaxAttempts, 1) && retriable(policy, outcome.err)
}

// timedOutByEdge returns the timeout edge whose bound a failed step overran.
func (e *Engine) timedOutByEdge(definition *Definition, outcome stepOutcome) *Edge {
	if outcome.err == nil || !errors.Is(outcome.err, context.DeadlineExceeded) {
		return nil
	}
	return edgeReaching(definition, outcome.frame, EdgeTimeout)
}

// edgeReaching returns the edge of one of the given types that pushed a frame,
// if the frame is one of that edge's direct targets.
func edgeReaching(definition *Definition, frame Frame, kinds ...EdgeType) *Edge {
	if frame.Edge == "" || frame.Compensate {
		return nil
	}
	edge, ok := definition.Edge(frame.Edge)
	if !ok || !slices.Contains(kinds, edge.Type) {
		return nil
	}
	return edge
}

// multiTargetEdges are the edges whose per-target options (max_concurrency,
// fail_fast, continue_on_error) apply to the frames they push.
var multiTargetEdges = []EdgeType{EdgeFanOut, EdgeDynamicFanOut, EdgeParallel, EdgeIterator, EdgeBatchIterator}

// resolveFailure handles a failed step: retry it, take an error path, let a
// join or a continue_on_error edge absorb it, or compensate and fail.
func (e *Engine) resolveFailure(ctx context.Context, definition *Definition, run *Run, outcome stepOutcome) error {
	// Retry first. A retry is not a traversal: the same frame comes back with a
	// higher attempt number and a delay, so the step's own state row records the
	// attempt count rather than the graph recording a loop.
	if e.willRetry(definition, outcome) {
		policy := e.retryPolicyFor(definition, outcome)
		next := outcome.frame
		next.Attempt++
		delay := retryDelay(policy, outcome.frame.Attempt)
		when := e.now().Add(delay)
		next.NotBefore = &when
		run.Frames = append(run.Frames, next)
		if e.enqueuer != nil {
			_ = e.enqueuer.EnqueueAdvanceAt(ctx, run.ID, when)
		}
		return nil
	}

	// fail_fast: the siblings this failure's edge has not started yet are
	// dropped. Siblings already executing in this wave cannot be unstarted.
	if edge := edgeReaching(definition, outcome.frame, multiTargetEdges...); edge != nil && edge.FailFast {
		run.Frames = slices.DeleteFunc(run.Frames, func(frame Frame) bool {
			return !frame.Compensate && frame.Edge == edge.Name
		})
	}

	// A target that overran its timeout edge's bound takes on_timeout, exactly as
	// a parked target does when its deadline timer fires.
	if edge := e.timedOutByEdge(definition, outcome); edge != nil && edge.OnTimeout != "" {
		e.pushTimedOut(run, edge.Name, outcome.frame.Step, edge.OnTimeout)
		return nil
	}

	scope, err := e.scope(ctx, run, outcome.frame, nil, map[string]any{"error": outcome.err.Error()})
	if err != nil {
		return err
	}
	edges := definition.Outgoing(outcome.frame.Step)
	traversed, err := e.traverse(ctx, definition, run, outcome, edges, scope, true)
	if err != nil {
		return err
	}
	if traversed > 0 {
		return nil
	}

	// A join this step feeds may be able to live with the failure (any, quorum,
	// partial_success, first_failure). Recording it there — rather than failing
	// the run outright, as before — is what makes those strategies reachable at
	// all; and a join that can no longer be met falls through to failure here
	// instead of waiting forever for results that cannot complete it.
	absorbed, err := e.reportFailureToJoins(ctx, definition, run, outcome, scope)
	if err != nil || absorbed {
		return err
	}

	// continue_on_error: the failure is recorded on the step and the run goes on.
	if edge := edgeReaching(definition, outcome.frame, multiTargetEdges...); edge != nil && edge.ContinueOnError {
		return nil
	}

	// Nothing caught the failure. Undo what already committed, then fail.
	return e.beginCompensation(ctx, definition, run, outcome.frame.Step, outcome.err)
}

// pushTimedOut continues a run at an on_timeout step.
func (e *Engine) pushTimedOut(run *Run, edge, step, onTimeout string) {
	payload, _ := json.Marshal(map[string]any{"timed_out": true, "edge": edge, "step": step})
	run.Frames = append(run.Frames, Frame{Step: onTimeout, Input: payload, From: step, Edge: edge, Attempt: 1})
}

// reportFailureToJoins records a failed source on the joins it feeds, and
// reports whether one of them absorbed the failure.
func (e *Engine) reportFailureToJoins(ctx context.Context, definition *Definition, run *Run, outcome stepOutcome, scope Scope) (bool, error) {
	absorbed := false
	for _, edge := range definition.Outgoing(outcome.frame.Step) {
		switch edge.Type {
		case EdgeFanIn, EdgeJoin, EdgeQuorum:
		default:
			continue
		}
		if !slices.Contains(edge.Sources, outcome.frame.Step) {
			continue
		}
		ok, err := evalGuard(edge.Guard, scope)
		if err != nil {
			return false, faultf("edge %q condition: %w", edge.Name, err)
		}
		if !ok {
			continue
		}
		_, join, err := e.resolveJoin(ctx, run, outcome, edge, scope)
		if err != nil {
			return false, err
		}
		if join.Emitted || !join.Impossible(edge.Strategy, edge.Quorum) {
			absorbed = true
		}
	}
	return absorbed, nil
}

// traverse resolves every eligible edge and returns how many actually traversed.
//
// Eligibility is computed for all edges first, so weighted and priority selection
// choose among edges that really could fire rather than among all of them.
func (e *Engine) traverse(ctx context.Context, definition *Definition, run *Run, outcome stepOutcome, edges []*Edge, scope Scope, errorMode bool) (int, error) {
	eligible := make([]*Edge, 0, len(edges))
	looping := false
	for _, edge := range edges {
		if edge.Type.ErrorPath() != errorMode {
			continue
		}
		// An edge whose source is not this step is a fan-in waiting on it; those are
		// handled by the fan-in branch below, which needs to run even though this
		// step is only one of its sources.
		if !slices.Contains(edge.allSources(), outcome.frame.Step) {
			continue
		}
		// An escalation edge leaving a human task was armed when the task opened
		// (armTaskEscalations): it watches the task while it is outstanding. Once
		// the task is done there is nothing overdue to escalate.
		if edge.Type == EdgeEscalation && outcome.step != nil && outcome.step.Task != nil {
			continue
		}
		ok, err := evalGuard(edge.Guard, scope)
		if err != nil {
			return 0, faultf("edge %q condition: %w", edge.Name, err)
		}
		if edge.Type == EdgeLoopUntil {
			// A loop_until condition is the loop's exit: "repeat until it holds".
			// The edge loops back while it does not.
			ok = !ok
			looping = looping || ok
		}
		if ok {
			eligible = append(eligible, edge)
		}
	}
	if looping {
		// While a loop continues, the step's other success edges are the loop's
		// exit and wait for it to end; otherwise the code after the loop would run
		// once per iteration.
		eligible = slices.DeleteFunc(eligible, func(edge *Edge) bool { return edge.Type != EdgeLoopUntil })
	}

	// Weighted, priority and switch edges are mutually exclusive alternatives, so
	// exactly one of each group is chosen per resolution — and the draw happens
	// once, so every weighted case in this call agrees on the winner.
	chosenWeighted := pickWeighted(eligible)
	chosenPriority := pickPriority(eligible)
	chosenSwitch := pickSwitch(eligible)

	traversed := 0
	for _, edge := range eligible {
		switch edge.Type {
		case EdgeWeighted:
			if edge != chosenWeighted {
				continue
			}
		case EdgePriority:
			if edge != chosenPriority {
				continue
			}
		case EdgeSwitch:
			if edge != chosenSwitch {
				continue
			}
		}
		fired, err := e.resolveEdge(ctx, definition, run, outcome, edge, scope)
		if err != nil {
			return traversed, err
		}
		if fired {
			traversed++
		}
		if run.Status.Terminal() {
			return traversed, nil
		}
	}
	return traversed, nil
}

// resolveEdge applies one edge's semantics. It reports whether the edge did
// anything — traversed, parked or ended the run.
func (e *Engine) resolveEdge(ctx context.Context, definition *Definition, run *Run, outcome stepOutcome, edge *Edge, scope Scope) (bool, error) {
	switch edge.Type {

	// --- plain traversal -------------------------------------------------
	//
	// retry and timeout traverse like a simple edge; their semantics apply to the
	// frames they push (see retryPolicyFor, timedOutByEdge and executeFrame's
	// deadline), because the frame remembers the edge that reached it.
	case EdgeSimple, EdgeBranch, EdgeSwitch, EdgeFanOut, EdgeParallel, EdgeError, EdgeFallback,
		EdgeTransform, EdgeFilter, EdgeStreamPipe, EdgeRetry, EdgeTimeout:
		payload, err := e.edgePayload(edge, outcome, scope)
		if err != nil {
			if errors.Is(err, ErrFiltered) {
				return false, nil
			}
			return false, err
		}
		// A timeout edge's deadline on a target that parks is enforced by a timer
		// rather than by holding a goroutine: that is what makes it survive a
		// restart. A target that runs a body is bounded by its context instead.
		if edge.Type == EdgeTimeout {
			if err := e.armTargetTimeout(ctx, run, edge); err != nil {
				return false, err
			}
		}
		e.pushFrames(run, edge, outcome.frame.Step, edge.allTargets(), payload, 1)
		return true, nil

	case EdgeConditionalFork:
		// Every target whose own guard passes fires. The edge-level guard has
		// already been checked; per-target guards are expressed as separate
		// conditional_fork edges sharing a source, which is why this behaves like a
		// fan-out once it is eligible.
		payload, err := e.edgePayload(edge, outcome, scope)
		if err != nil {
			if errors.Is(err, ErrFiltered) {
				return false, nil
			}
			return false, err
		}
		e.pushFrames(run, edge, outcome.frame.Step, edge.allTargets(), payload, 1)
		return true, nil

	case EdgeWeighted, EdgePriority:
		payload, err := e.edgePayload(edge, outcome, scope)
		if err != nil {
			if errors.Is(err, ErrFiltered) {
				return false, nil
			}
			return false, err
		}
		e.pushFrames(run, edge, outcome.frame.Step, edge.allTargets(), payload, 1)
		return true, nil

	case EdgeThreshold:
		return e.resolveThreshold(ctx, run, outcome, edge, scope)

	case EdgeDynamicFanOut:
		targets := e.dynamicTargets(definition, edge, outcome.result)
		if len(targets) == 0 {
			return false, nil
		}
		payload, err := e.edgePayload(edge, outcome, scope)
		if err != nil {
			if errors.Is(err, ErrFiltered) {
				return false, nil
			}
			return false, err
		}
		e.pushFrames(run, edge, outcome.frame.Step, targets, payload, 1)
		return true, nil

	// --- joins -----------------------------------------------------------
	case EdgeFanIn, EdgeJoin, EdgeQuorum:
		fired, _, err := e.resolveJoin(ctx, run, outcome, edge, scope)
		return fired, err

	// --- race ------------------------------------------------------------
	case EdgeRace:
		return e.resolveRace(ctx, definition, run, outcome, edge, scope)

	// --- iteration -------------------------------------------------------
	case EdgeIterator, EdgeBatchIterator:
		return e.resolveIterator(ctx, run, outcome, edge, scope)

	case EdgeLoopUntil:
		return e.resolveLoopUntil(ctx, run, outcome, edge, scope)

	// --- suspension ------------------------------------------------------
	case EdgeDelayed:
		payload, err := e.edgePayload(edge, outcome, scope)
		if err != nil {
			if errors.Is(err, ErrFiltered) {
				return false, nil
			}
			return false, err
		}
		return true, e.parkOnTimer(ctx, run, edge, outcome.frame.Step, payload, e.now().Add(edge.Timeout), "delayed")

	case EdgeWaitEvent:
		return e.resolveWaitEvent(ctx, run, outcome, edge, scope)

	case EdgeManual:
		return true, e.parkManual(ctx, run, edge, outcome)

	case EdgeEscalation:
		return true, e.parkEscalation(ctx, run, edge, outcome)

	case EdgeRateLimited:
		return e.resolveRateLimited(ctx, run, outcome, edge, scope)

	// --- termination -----------------------------------------------------
	case EdgeCancel:
		now := e.now()
		run.Status = StatusCancelled
		run.CompletedAt = &now
		run.Frames = nil
		run.Waiting = nil
		if run.Error == "" {
			run.Error = fmt.Sprintf("cancelled after %s by edge %s", outcome.frame.Step, edge.Name)
		}
		if encoded, err := json.Marshal(outcome.result); err == nil {
			run.Output = encoded
		}
		_ = e.store.DeleteRunTimers(ctx, run.ID)
		_ = e.store.DeleteRunSubscriptions(ctx, run.ID)
		_ = e.cancelRunTasks(ctx, run)
		// A cancelled child must release its parent like every other terminal path
		// does (completeRun, failRun, Cancel); without this the parent waited
		// forever on a child that had already ended.
		if err := e.signalParent(ctx, run); err != nil {
			return true, err
		}
		for _, observer := range e.observers {
			observer.RunFinished(run)
		}
		return true, nil

	case EdgeCompensate:
		return true, e.beginCompensation(ctx, definition, run, outcome.frame.Step,
			fmt.Errorf("compensating after %s", outcome.frame.Step))

	default:
		return false, faultf("edge %q has unhandled type %q", edge.Name, edge.Type)
	}
}

// edgePayload shapes what the targets receive. A filter inside the edge's data
// block returns ErrFiltered, which is how a filter edge declines to traverse.
func (e *Engine) edgePayload(edge *Edge, outcome stepOutcome, scope Scope) (any, error) {
	payload := outcome.result
	if edge.Shaper == nil {
		return payload, nil
	}
	return edge.Shaper.Shape(payload, scope)
}

// pushFrames appends work for each target.
func (e *Engine) pushFrames(run *Run, edge *Edge, from string, targets []string, payload any, attempt int) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		// A payload that cannot be serialised cannot cross a durability boundary.
		// Passing nothing is better than corrupting the cursor, and the target
		// step's own validation will report the missing input.
		encoded = nil
	}
	for _, target := range targets {
		run.Frames = append(run.Frames, Frame{
			Step: target, Input: encoded, From: from, Edge: edge.Name, Attempt: attempt,
		})
	}
}

// ---------------------------------------------------------------------------
// Selection
// ---------------------------------------------------------------------------

// pickWeighted performs one weighted-random draw across the eligible weighted
// edges. One draw per resolution, so all weighted cases agree.
func pickWeighted(edges []*Edge) *Edge {
	var (
		group []*Edge
		total float64
	)
	for _, edge := range edges {
		if edge.Type != EdgeWeighted {
			continue
		}
		weight := edge.Weight
		if weight <= 0 {
			weight = 1
		}
		group = append(group, edge)
		total += weight
	}
	if len(group) == 0 {
		return nil
	}
	if total <= 0 {
		return group[0]
	}
	draw := rand.Float64() * total
	running := 0.0
	for _, edge := range group {
		weight := edge.Weight
		if weight <= 0 {
			weight = 1
		}
		running += weight
		if draw <= running {
			return edge
		}
	}
	return group[len(group)-1]
}

// pickPriority selects the lowest priority value, ties going to the earlier
// declaration so the choice is deterministic.
func pickPriority(edges []*Edge) *Edge {
	var best *Edge
	for _, edge := range edges {
		if edge.Type != EdgePriority {
			continue
		}
		if best == nil || edge.Priority < best.Priority {
			best = edge
		}
	}
	return best
}

// pickSwitch selects one switch case: the first eligible case with a condition,
// in declaration order, or — when none matched — the first case without one,
// which is the switch's default. A switch is a single choice; firing every case
// whose condition held (as a plain branch does) is not what "switch" means.
func pickSwitch(edges []*Edge) *Edge {
	var fallback *Edge
	for _, edge := range edges {
		if edge.Type != EdgeSwitch {
			continue
		}
		if edge.Guard != nil {
			return edge
		}
		if fallback == nil {
			fallback = edge
		}
	}
	return fallback
}

// ---------------------------------------------------------------------------
// Thresholds
// ---------------------------------------------------------------------------

func (e *Engine) resolveThreshold(ctx context.Context, run *Run, outcome stepOutcome, edge *Edge, scope Scope) (bool, error) {
	for _, band := range edge.Thresholds {
		if band.Value == nil {
			continue
		}
		raw, err := band.Value.Value(scope)
		if err != nil {
			return false, faultf("edge %q threshold %q: %w", edge.Name, band.Name, err)
		}
		number, ok := toFloat(raw)
		if !ok {
			return false, faultf("edge %q threshold %q: %v is not a number", edge.Name, band.Name, raw)
		}
		if !band.Contains(number) {
			continue
		}
		payload, err := e.edgePayload(edge, outcome, scope)
		if err != nil {
			if errors.Is(err, ErrFiltered) {
				return false, nil
			}
			return false, err
		}
		// The band that matched is carried forward, so an auditor can see which
		// rule applied rather than inferring it from the path taken.
		if object, ok := payload.(map[string]any); ok {
			enriched := make(map[string]any, len(object)+len(band.Data)+2)
			for key, value := range object {
				enriched[key] = value
			}
			for key, value := range band.Data {
				enriched[key] = value
			}
			enriched["threshold"] = band.Name
			if band.Reason != "" {
				enriched["threshold_reason"] = band.Reason
			}
			payload = enriched
		}
		e.pushFrames(run, edge, outcome.frame.Step, []string{band.Target}, payload, 1)
		return true, nil
	}
	// No band matched. That is a gap in the configuration rather than a decision,
	// and it is worth reporting: a threshold edge whose bands do not tile the
	// range will silently stall runs at whatever value falls through.
	return false, nil
}

// dynamicTargets reads the runtime target list, falling back to the static ones.
func (e *Engine) dynamicTargets(definition *Definition, edge *Edge, result any) []string {
	var candidates []string
	if edge.TargetsPath != "" {
		if object, ok := result.(map[string]any); ok {
			if value, found := lookup(object, edge.TargetsPath); found {
				candidates = toStrings(value)
			}
		}
	}
	if len(candidates) == 0 {
		return edge.allTargets()
	}
	// Only declared steps are accepted. Without this check a step's result could
	// name any step in the process — or a step that does not exist — and dispatch
	// to it, which is a control-flow injection.
	valid := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if _, ok := definition.Step(candidate); ok {
			valid = append(valid, candidate)
		}
	}
	return valid
}

// ---------------------------------------------------------------------------
// Joins
// ---------------------------------------------------------------------------

// resolveJoin accumulates one source's completion — or failure — and continues
// when the strategy is satisfied. It returns whether the edge did anything, and
// the join's state after this source reported.
func (e *Engine) resolveJoin(ctx context.Context, run *Run, outcome stepOutcome, edge *Edge, scope Scope) (bool, *Join, error) {
	join, err := e.store.GetJoin(ctx, run.ID, edge.Name)
	if err != nil {
		return false, nil, err
	}
	if join == nil {
		join = &Join{
			RunID:   run.ID,
			Edge:    edge.Name,
			Sources: slices.Clone(edge.Sources),
			Results: map[string]json.RawMessage{},
			Errors:  map[string]string{},
		}
	}
	if join.Emitted {
		// A late source arriving after the join already continued must not fire the
		// downstream step a second time.
		return false, join, nil
	}
	if join.Results == nil {
		join.Results = map[string]json.RawMessage{}
	}
	if join.Errors == nil {
		join.Errors = map[string]string{}
	}

	if outcome.err != nil {
		join.Errors[outcome.frame.Step] = outcome.err.Error()
	} else if encoded, err := json.Marshal(outcome.result); err == nil {
		join.Results[outcome.frame.Step] = encoded
	}

	if !join.Complete(edge.Strategy, edge.Quorum) {
		return true, join, e.store.SaveJoin(ctx, join)
	}
	framed, err := e.emitJoin(ctx, run, edge, join, scope, outcome.frame.Step, nil)
	return framed, join, err
}

// emitJoin continues past a satisfied join, once.
func (e *Engine) emitJoin(ctx context.Context, run *Run, edge *Edge, join *Join, scope Scope, from string, missing []string) (bool, error) {
	results := make(map[string]any, len(join.Results))
	for source, encoded := range join.Results {
		var value any
		if err := json.Unmarshal(encoded, &value); err != nil {
			return false, err
		}
		results[source] = value
	}
	payload := map[string]any{
		"results": results, "sources": join.Sources, "strategy": edge.Strategy, "errors": join.Errors,
	}
	if len(missing) > 0 {
		payload["missing"] = missing
	}
	framed := true
	shaped := any(payload)
	if edge.Shaper != nil {
		joinScope := make(Scope, len(scope)+1)
		for key, value := range scope {
			joinScope[key] = value
		}
		joinScope["result"] = payload
		var err error
		shaped, err = edge.Shaper.Shape(payload, joinScope)
		if err != nil {
			if !errors.Is(err, ErrFiltered) {
				return false, err
			}
			framed = false
			shaped = nil
		}
	}
	if framed {
		e.pushFrames(run, edge, from, edge.allTargets(), shaped, 1)
	}
	join.Emitted = true
	if err := e.store.SaveJoinAndRun(ctx, join, run); err != nil {
		return false, err
	}
	return framed, nil
}

// settleJoins deals with joins still accumulating when a run has nothing left
// to do.
//
// At that point every source that has not reported never will: its branch was
// not taken, a condition skipped it, or a filter dropped it. Before this check
// such a run simply completed — reporting success for a process whose join step
// never ran. Now a partial_success join continues with what arrived (that is
// what the strategy promises), and any other strategy fails the run, naming the
// join and the sources it was still waiting for.
func (e *Engine) settleJoins(ctx context.Context, definition *Definition, run *Run) (bool, error) {
	more := false
	for _, edge := range definition.Edges {
		switch edge.Type {
		case EdgeFanIn, EdgeJoin, EdgeQuorum:
		default:
			continue
		}
		join, err := e.store.GetJoin(ctx, run.ID, edge.Name)
		if err != nil {
			return false, err
		}
		if join == nil || join.Emitted {
			continue
		}
		missing := join.Missing()
		if len(missing) == 0 {
			// Every source reported and the strategy said no — a first_failure join
			// when nothing failed. That is a decision, not a stall.
			continue
		}
		if edge.Strategy == "partial_success" && len(join.Results) > 0 {
			scope, err := e.scope(ctx, run, Frame{}, nil, nil)
			if err != nil {
				return false, err
			}
			framed, err := e.emitJoin(ctx, run, edge, join, scope, "", missing)
			if err != nil {
				return false, err
			}
			more = more || framed
			continue
		}
		cause := fmt.Errorf("join %q is still waiting for %s, which can no longer run, so its %q strategy can never be met",
			edge.Name, strings.Join(missing, ", "), edge.Strategy)
		if err := e.beginCompensation(ctx, definition, run, "", cause); err != nil {
			return false, err
		}
		return !run.Status.Terminal(), nil
	}
	return more, nil
}

// ---------------------------------------------------------------------------
// Race
// ---------------------------------------------------------------------------

// resolveRace pushes every target and opens the race row that decides it.
//
// The race is decided durably, by a row, rather than by a goroutine that returns
// first: that is how it survives a restart. The first entrant to finish
// successfully wins and only its outgoing edges continue the graph; every other
// entrant's result is recorded and its continuation suppressed, so the step after
// a race runs once. CancelLosers additionally drops the entrants that have not
// started and closes the ones parked on a person or a child process — a step
// already executing cannot be unstarted. A failing entrant is out of the race; the
// race fails only when every entrant has failed.
func (e *Engine) resolveRace(ctx context.Context, definition *Definition, run *Run, outcome stepOutcome, edge *Edge, scope Scope) (bool, error) {
	payload, err := e.edgePayload(edge, outcome, scope)
	if err != nil {
		if errors.Is(err, ErrFiltered) {
			return false, nil
		}
		return false, err
	}
	// A fresh row every time the race starts: a race inside a loop is a new race
	// on each iteration, not a replay of the first one's verdict.
	join := &Join{
		RunID:   run.ID,
		Edge:    raceRow(edge),
		Sources: slices.Clone(edge.allTargets()),
		Results: map[string]json.RawMessage{},
		Errors:  map[string]string{},
	}
	if err := e.store.SaveJoin(ctx, join); err != nil {
		return false, err
	}
	if edge.Timeout > 0 {
		when := e.now().Add(edge.Timeout)
		if err := e.store.AddTimer(ctx, &Timer{
			ID: randomID(), RunID: run.ID, Fire: when,
			Kind: "race_timeout", Step: outcome.frame.Step, Edge: edge.Name,
			OnFire: edge.OnTimeout, CreatedAt: e.now(),
		}); err != nil {
			return false, err
		}
		if e.enqueuer != nil {
			_ = e.enqueuer.EnqueueAdvanceAt(ctx, run.ID, when)
		}
	}
	e.pushFrames(run, edge, outcome.frame.Step, edge.allTargets(), payload, 1)
	return true, nil
}

func raceRow(edge *Edge) string { return edge.Name + ":race" }

// raceOf returns the race a frame is an entrant of.
func (e *Engine) raceOf(definition *Definition, frame Frame) *Edge {
	if frame.Compensate {
		return nil
	}
	if frame.Edge != "" {
		edge, ok := definition.Edge(frame.Edge)
		if ok && edge.Type == EdgeRace && slices.Contains(edge.allTargets(), frame.Step) {
			return edge
		}
		return nil
	}
	// An entrant resumed from outside an advance — a task somebody completed, a
	// child run that finished — comes back without the edge that reached it. The
	// race row (checked by the caller) says whether it is still racing.
	for _, edge := range definition.Incoming(frame.Step) {
		if edge.Type == EdgeRace {
			return edge
		}
	}
	return nil
}

// resolveRaceEntrant records one entrant's outcome and reports whether its
// continuation is suppressed.
func (e *Engine) resolveRaceEntrant(ctx context.Context, definition *Definition, run *Run, outcome stepOutcome, race *Edge) (bool, error) {
	row, err := e.store.GetJoin(ctx, run.ID, raceRow(race))
	if err != nil || row == nil {
		return false, err
	}
	entrant := outcome.frame.Step
	if !slices.Contains(row.Sources, entrant) {
		return false, nil
	}
	if _, done := row.Results[entrant]; done {
		// Already reported: the step is running again for some other reason.
		return false, nil
	}
	if _, done := row.Errors[entrant]; done {
		return false, nil
	}
	if row.Results == nil {
		row.Results = map[string]json.RawMessage{}
	}
	if row.Errors == nil {
		row.Errors = map[string]string{}
	}

	if outcome.err != nil {
		if e.willRetry(definition, outcome) {
			// Still in the race: the retry is its next attempt.
			return false, nil
		}
		row.Errors[entrant] = outcome.err.Error()
		if !row.Emitted && len(row.Errors) >= len(row.Sources) {
			// Every entrant failed: nobody can win. This last failure is the race's,
			// and it takes the ordinary failure path — error edges, compensation.
			row.Emitted = true
			if err := e.store.SaveJoin(ctx, row); err != nil {
				return false, err
			}
			return false, e.deleteTimers(ctx, run.ID, "race_timeout", race.Name, "")
		}
		// Out of the race, but somebody else may still win it.
		return true, e.store.SaveJoin(ctx, row)
	}

	encoded, err := json.Marshal(outcome.result)
	if err != nil {
		return false, err
	}
	row.Results[entrant] = encoded
	if row.Emitted {
		// Decided already: a loser, whose work is recorded but goes no further.
		return true, e.store.SaveJoin(ctx, row)
	}
	row.Emitted = true
	if err := e.store.SaveJoin(ctx, row); err != nil {
		return false, err
	}
	if err := e.deleteTimers(ctx, run.ID, "race_timeout", race.Name, ""); err != nil {
		return false, err
	}
	if race.CancelLosers {
		for _, target := range race.allTargets() {
			if target == entrant {
				continue
			}
			if _, err := e.cancelOutstanding(ctx, run, target, race.Name); err != nil {
				return false, err
			}
		}
	}
	return false, nil
}

// orderRaceEntrants reorders a wave's race entrants by when they finished, so
// "first to finish" means that rather than "first declared". Successful
// entrants are ordered by their completion sequence; failed and parked ones
// follow. Only the entrants' own slots are permuted: the rest of the wave keeps
// its resolution order.
func (e *Engine) orderRaceEntrants(definition *Definition, outcomes []stepOutcome) {
	var slots []int
	for index, outcome := range outcomes {
		if outcome.frame.Edge != "" && e.raceOf(definition, outcome.frame) != nil {
			slots = append(slots, index)
		}
	}
	if len(slots) < 2 {
		return
	}
	finished := func(outcome stepOutcome) int64 {
		if outcome.err == nil && outcome.parked == nil && outcome.state != nil && outcome.state.Sequence > 0 {
			return outcome.state.Sequence
		}
		return math.MaxInt64
	}
	entrants := make([]stepOutcome, len(slots))
	for i, slot := range slots {
		entrants[i] = outcomes[slot]
	}
	slices.SortStableFunc(entrants, func(a, b stepOutcome) int { return cmp.Compare(finished(a), finished(b)) })
	for i, slot := range slots {
		outcomes[slot] = entrants[i]
	}
}

// cancelOutstanding stops a step that has not finished: its queued frames (those
// pushed by viaEdge, or any when viaEdge is empty), its open human task, and its
// running child process. It reports whether there was anything to stop.
func (e *Engine) cancelOutstanding(ctx context.Context, run *Run, step, viaEdge string) (bool, error) {
	found := false
	before := len(run.Frames)
	run.Frames = slices.DeleteFunc(run.Frames, func(frame Frame) bool {
		return !frame.Compensate && frame.Step == step && (viaEdge == "" || frame.Edge == viaEdge)
	})
	found = len(run.Frames) != before

	tasks, err := e.store.ListTasks(ctx, TaskFilter{RunID: run.ID, Step: step, Limit: 500})
	if err != nil {
		return found, err
	}
	for _, task := range tasks {
		if !task.Open() {
			continue
		}
		task.Status = TaskCancelled
		if err := e.store.SaveTask(ctx, task); err != nil {
			return found, err
		}
		found = true
	}

	subscriptions, err := e.store.ListSubscriptions(ctx, run.ID)
	if err != nil {
		return found, err
	}
	for _, subscription := range subscriptions {
		if subscription.Event != childCompletedEvent || subscription.Step != step {
			continue
		}
		if err := e.store.DeleteSubscription(ctx, subscription.ID); err != nil {
			return found, err
		}
		found = true
		// The child is abandoned with its parent's interest in it. Cancelling it is
		// best effort: a child that is busy right now ends on its own, and its
		// completion signal then finds nobody subscribed, which is harmless.
		_, _ = e.Cancel(ctx, subscription.Correlation, fmt.Sprintf("cancelled by its parent run %s", run.ID))
	}
	return found, nil
}

// deleteTimers removes a run's timers of one kind, narrowed to an edge and/or a
// step when those are given.
func (e *Engine) deleteTimers(ctx context.Context, runID, kind, edge, step string) error {
	timers, err := e.store.ListTimers(ctx, runID)
	if err != nil {
		return err
	}
	for _, timer := range timers {
		if timer.Kind != kind || (edge != "" && timer.Edge != edge) || (step != "" && timer.Step != step) {
			continue
		}
		if err := e.store.DeleteTimer(ctx, timer.ID); err != nil {
			return err
		}
	}
	return nil
}

// disarmStepWatchers removes the timers that were watching a step which has now
// finished: a timeout edge's deadline on it, and the escalations of its task.
func (e *Engine) disarmStepWatchers(ctx context.Context, definition *Definition, run *Run, outcome stepOutcome) error {
	step := outcome.frame.Step
	if slices.ContainsFunc(definition.Incoming(step), func(edge *Edge) bool { return edge.Type == EdgeTimeout }) {
		if err := e.deleteTimers(ctx, run.ID, "timeout", "", step); err != nil {
			return err
		}
	}
	if outcome.step.Task == nil {
		return nil
	}
	hasEscalation := slices.ContainsFunc(definition.Outgoing(step), func(edge *Edge) bool { return edge.Type == EdgeEscalation })
	if !hasEscalation {
		return nil
	}
	timers, err := e.store.ListTimers(ctx, run.ID)
	if err != nil {
		return err
	}
	for _, timer := range timers {
		if timer.Kind != "task_escalation" {
			continue
		}
		var watched taskEscalation
		if json.Unmarshal(timer.Payload, &watched) != nil || watched.Step != step || watched.Key != outcome.frame.StateKey() {
			continue
		}
		if err := e.store.DeleteTimer(ctx, timer.ID); err != nil {
			return err
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Iteration
// ---------------------------------------------------------------------------

// resolveIterator pushes one frame per element or per batch.
//
// Each iteration gets its own state key, so the twelfth item's failure is
// recorded against `notify[11]` rather than overwriting the eleventh's record —
// which is what makes an iterator's partial failure diagnosable.
func (e *Engine) resolveIterator(ctx context.Context, run *Run, outcome stepOutcome, edge *Edge, scope Scope) (bool, error) {
	payload, err := e.edgePayload(edge, outcome, scope)
	if err != nil {
		if errors.Is(err, ErrFiltered) {
			return false, nil
		}
		return false, err
	}
	items := e.iteratorItems(edge, payload)
	if len(items) == 0 {
		// An empty collection is not a failure; it is a loop that runs zero times.
		// The run continues past the iterator through whatever the target's own
		// edges would have led to, which the author expresses with a separate edge
		// from the source.
		return false, nil
	}

	targets := edge.allTargets()
	target := targets[0]
	if edge.Type == EdgeBatchIterator {
		batches := make([]any, 0, len(items)/max(edge.BatchSize, 1)+1)
		for start := 0; start < len(items); start += edge.BatchSize {
			end := min(start+edge.BatchSize, len(items))
			batches = append(batches, items[start:end])
		}
		items = batches
	}

	for index, item := range items {
		encoded, err := json.Marshal(map[string]any{
			"item":  item,
			"index": index,
			"total": len(items),
		})
		if err != nil {
			return false, err
		}
		run.Frames = append(run.Frames, Frame{
			Step:    target,
			Input:   encoded,
			From:    outcome.frame.Step,
			Edge:    edge.Name,
			Key:     fmt.Sprintf("%s[%d]", target, index),
			Attempt: 1,
		})
	}
	return true, nil
}

func (e *Engine) iteratorItems(edge *Edge, payload any) []any {
	value := payload
	if edge.ItemsPath != "" {
		if object, ok := payload.(map[string]any); ok {
			found, ok := lookup(object, edge.ItemsPath)
			if !ok {
				return nil
			}
			value = found
		}
	}
	switch typed := value.(type) {
	case []any:
		return typed
	case []map[string]any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = item
		}
		return out
	default:
		return nil
	}
}

// resolveLoopUntil sends the run back to the target until the condition holds or
// the cycle cap is reached.
func (e *Engine) resolveLoopUntil(ctx context.Context, run *Run, outcome stepOutcome, edge *Edge, scope Scope) (bool, error) {
	targets := edge.allTargets()
	if len(targets) == 0 {
		return false, nil
	}
	target := targets[0]

	// The guard on a loop_until edge is the *exit* condition, and traverse only
	// makes the edge eligible while it does not hold — so reaching here means the
	// loop continues. The cycle cap (max_concurrency) is the backstop for a
	// condition that never holds; reaching it fails the run rather than returning
	// an error that would leave it wedged, re-running the body on every advance.
	if run.Visits[target] >= edge.MaxConcurrency {
		return false, faultf("edge %q looped back to %q %d times without its condition holding (its cycle cap is %d)",
			edge.Name, target, run.Visits[target], edge.MaxConcurrency)
	}
	payload, err := e.edgePayload(edge, outcome, scope)
	if err != nil {
		if errors.Is(err, ErrFiltered) {
			return false, nil
		}
		return false, err
	}
	e.pushFrames(run, edge, outcome.frame.Step, []string{target}, payload, 1)
	return true, nil
}

// ---------------------------------------------------------------------------
// Suspension
// ---------------------------------------------------------------------------

// parkOnTimer records a timer that will resume the run at the target.
func (e *Engine) parkOnTimer(ctx context.Context, run *Run, edge *Edge, from string, payload any, when time.Time, kind string) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	targets := edge.allTargets()
	if len(targets) == 0 {
		return faultf("edge %q has no target to resume at", edge.Name)
	}
	if err := e.store.AddTimer(ctx, &Timer{
		ID: randomID(), RunID: run.ID, Fire: when, Kind: kind,
		Step: targets[0], Edge: edge.Name, Payload: encoded, CreatedAt: e.now(),
	}); err != nil {
		return err
	}
	if e.enqueuer != nil {
		_ = e.enqueuer.EnqueueAdvanceAt(ctx, run.ID, when)
	}
	return e.recordPark(ctx, run, WaitState{
		Reason: "timer", Step: targets[0], Edge: edge.Name, Until: &when,
		Detail: fmt.Sprintf("waiting until %s", when.Format(time.RFC3339)),
	})
}

// armTargetTimeout records the deadline a timeout edge imposes on its targets.
// The timer is removed when the target finishes (disarmStepWatchers); if it fires
// first, handleDeadline stops the target and takes on_timeout.
func (e *Engine) armTargetTimeout(ctx context.Context, run *Run, edge *Edge) error {
	when := e.now().Add(edge.Timeout)
	for _, target := range edge.allTargets() {
		if err := e.store.AddTimer(ctx, &Timer{
			ID: randomID(), RunID: run.ID, Fire: when, Kind: "timeout",
			Step: target, Edge: edge.Name, OnFire: edge.OnTimeout, CreatedAt: e.now(),
		}); err != nil {
			return err
		}
	}
	if e.enqueuer != nil {
		_ = e.enqueuer.EnqueueAdvanceAt(ctx, run.ID, when)
	}
	return nil
}

// resolveWaitEvent records a subscription and, when the edge has a timeout, the
// deadline that gives up on it.
func (e *Engine) resolveWaitEvent(ctx context.Context, run *Run, outcome stepOutcome, edge *Edge, scope Scope) (bool, error) {
	correlation, err := edge.Correlation.Value(scope)
	if err != nil {
		return false, faultf("edge %q correlation: %w", edge.Name, err)
	}
	key := fmt.Sprint(correlation)
	if correlation == nil || key == "" {
		// An empty correlation would make this subscription match every event of
		// its name, waking runs that have nothing to do with the signal.
		return false, faultf("edge %q: the correlation expression %q evaluated to empty", edge.Name, edge.Correlation.Source())
	}
	targets := edge.allTargets()
	if len(targets) == 0 {
		return false, faultf("edge %q has no target to resume at", edge.Name)
	}

	var expires *time.Time
	if edge.Timeout > 0 {
		deadline := e.now().Add(edge.Timeout)
		expires = &deadline
		if err := e.store.AddTimer(ctx, &Timer{
			ID: randomID(), RunID: run.ID, Fire: deadline, Kind: "wait_event",
			Step: targets[0], Edge: edge.Name, OnFire: edge.OnTimeout, CreatedAt: e.now(),
		}); err != nil {
			return false, err
		}
		if e.enqueuer != nil {
			_ = e.enqueuer.EnqueueAdvanceAt(ctx, run.ID, deadline)
		}
	}
	if err := e.store.Subscribe(ctx, &Subscription{
		ID: randomID(), RunID: run.ID, Event: edge.Event, Correlation: key,
		Step: targets[0], Key: outcome.frame.StateKey(), Edge: edge.Name, ExpiresAt: expires, CreatedAt: e.now(),
	}); err != nil {
		return false, err
	}
	return true, e.recordPark(ctx, run, WaitState{
		Reason: "event", Step: targets[0], Edge: edge.Name, Event: edge.Event, Until: expires,
		Detail: fmt.Sprintf("waiting for %s (%s)", edge.Event, key),
	})
}

// parkManual holds the run until an operator advances it. Nothing automatic ever
// releases a manual gate — that is its entire purpose.
func (e *Engine) parkManual(ctx context.Context, run *Run, edge *Edge, outcome stepOutcome) error {
	targets := edge.allTargets()
	if len(targets) == 0 {
		return faultf("edge %q has no target to resume at", edge.Name)
	}
	payload, _ := json.Marshal(outcome.result)
	if err := e.store.Subscribe(ctx, &Subscription{
		ID: randomID(), RunID: run.ID, Event: manualEventName, Correlation: edge.Name,
		Step: targets[0], Key: outcome.frame.StateKey(), Edge: edge.Name, CreatedAt: e.now(),
	}); err != nil {
		return err
	}
	// The payload rides on a timer-shaped record with no fire time so the resumed
	// frame carries the source step's result; the subscription alone would lose it.
	if err := e.store.AddTimer(ctx, &Timer{
		ID: randomID(), RunID: run.ID, Fire: farFuture, Kind: "manual_payload",
		Step: targets[0], Edge: edge.Name, Payload: payload, CreatedAt: e.now(),
	}); err != nil {
		return err
	}
	return e.recordPark(ctx, run, WaitState{
		Reason: "manual", Step: targets[0], Edge: edge.Name,
		Detail: fmt.Sprintf("waiting for an operator to release the gate after %s", outcome.frame.Step),
	})
}

// parkEscalation waits, then raises the work to somebody else: when the timer
// fires the engine notifies edge.Notify, reassigns the run's open tasks to the
// edge.Escalate role, and continues at the target (see handleEscalation).
//
// This is the form for an escalation leaving an ordinary step. One leaving a human
// task is armed when the task opens instead (armTaskEscalations), because what it
// escalates is that task being overdue — which it can only be while it is open.
func (e *Engine) parkEscalation(ctx context.Context, run *Run, edge *Edge, outcome stepOutcome) error {
	when := e.now().Add(edge.Timeout)
	payload, _ := json.Marshal(outcome.result)
	targets := edge.allTargets()
	if len(targets) == 0 {
		return faultf("edge %q has no target to escalate to", edge.Name)
	}
	if err := e.store.AddTimer(ctx, &Timer{
		ID: randomID(), RunID: run.ID, Fire: when, Kind: "escalation",
		Step: targets[0], Edge: edge.Name, Payload: payload, CreatedAt: e.now(),
	}); err != nil {
		return err
	}
	if e.enqueuer != nil {
		_ = e.enqueuer.EnqueueAdvanceAt(ctx, run.ID, when)
	}
	return e.recordPark(ctx, run, WaitState{
		Reason: "timer", Step: targets[0], Edge: edge.Name, Until: &when,
		Detail: "waiting before escalating",
	})
}

// resolveRateLimited parks until the limiter admits the run, rather than sleeping
// a worker. DAGFlow slept a fraction of the window in the resolver; a durable
// engine can simply come back later, which costs nothing and survives a restart.
func (e *Engine) resolveRateLimited(ctx context.Context, run *Run, outcome stepOutcome, edge *Edge, scope Scope) (bool, error) {
	payload, err := e.edgePayload(edge, outcome, scope)
	if err != nil {
		if errors.Is(err, ErrFiltered) {
			return false, nil
		}
		return false, err
	}
	targets := edge.allTargets()
	if len(targets) == 0 {
		return false, faultf("edge %q has no target", edge.Name)
	}
	if e.limiter == nil {
		// Without a limiter the edge cannot honour its contract. Traversing anyway
		// would silently remove the limit the author asked for.
		return false, faultf("edge %q is rate_limited but this engine has no rate limiter configured", edge.Name)
	}
	allowed, _, resetAt, err := e.limiter.Allow(ctx, "edge:"+edge.Name, edge.Limit, edge.Window)
	if err != nil {
		return false, fmt.Errorf("edge %q: the rate limiter is unavailable: %w", edge.Name, err)
	}
	if allowed {
		e.pushFrames(run, edge, outcome.frame.Step, targets, payload, 1)
		return true, nil
	}
	if resetAt.IsZero() || !resetAt.After(e.now()) {
		resetAt = e.now().Add(edge.Window)
	}
	// A "rate_limited" timer rather than a "delayed" one: when it fires it asks
	// the limiter again (handleRateLimited) instead of traversing unconditionally,
	// which let every parked run through at the end of the window regardless of
	// how many the limit admits.
	return true, e.parkOnTimer(ctx, run, edge, outcome.frame.Step, payload, resetAt, "rate_limited")
}

// taskEscalation is the payload of a timer watching an open human task.
type taskEscalation struct {
	TaskID string `json:"task_id"`
	Step   string `json:"step"`
	Key    string `json:"key"`
}

// armTaskEscalations arms the escalation edges leaving a human task step when the
// task opens. Each becomes a timer that, if the task is still open when it fires,
// reassigns the task to the edge's escalate role, notifies, and continues at the
// edge's target (handleTaskEscalation). Completing the task first disarms it.
func (e *Engine) armTaskEscalations(ctx context.Context, definition *Definition, run *Run, outcome stepOutcome, taskID string) error {
	var scope Scope
	for _, edge := range definition.Outgoing(outcome.frame.Step) {
		if edge.Type != EdgeEscalation || !slices.Contains(edge.allSources(), outcome.frame.Step) {
			continue
		}
		if scope == nil {
			var input any
			if len(outcome.frame.Input) > 0 {
				_ = json.Unmarshal(outcome.frame.Input, &input)
			}
			var err error
			if scope, err = e.scope(ctx, run, outcome.frame, input, nil); err != nil {
				return err
			}
		}
		ok, err := evalGuard(edge.Guard, scope)
		if err != nil {
			return faultf("edge %q condition: %w", edge.Name, err)
		}
		targets := edge.allTargets()
		if !ok || len(targets) == 0 {
			continue
		}
		payload, err := json.Marshal(taskEscalation{TaskID: taskID, Step: outcome.frame.Step, Key: outcome.frame.StateKey()})
		if err != nil {
			return err
		}
		when := e.now().Add(edge.Timeout)
		if err := e.store.AddTimer(ctx, &Timer{
			ID: randomID(), RunID: run.ID, Fire: when, Kind: "task_escalation",
			Step: targets[0], Edge: edge.Name, Payload: payload, CreatedAt: e.now(),
		}); err != nil {
			return err
		}
		if e.enqueuer != nil {
			_ = e.enqueuer.EnqueueAdvanceAt(ctx, run.ID, when)
		}
	}
	return nil
}

// recordPark notes why the run is waiting. It does not itself set the status:
// a run with other frames still in flight keeps running, and only settle decides
// that the run as a whole is parked.
func (e *Engine) recordPark(ctx context.Context, run *Run, wait WaitState) error {
	run.Waiting = &wait
	return nil
}

// manualEventName is the reserved event a manual gate waits for. It is namespaced
// so an application cannot collide with it.
const manualEventName = "__manual__"

// farFuture is the fire time of a record that is not really a timer — a manual
// gate's payload carrier, which must never fire on its own.
var farFuture = time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)

// ---------------------------------------------------------------------------
// Retry helpers
// ---------------------------------------------------------------------------

func retriable(policy *RetryPolicy, err error) bool {
	if policy.Retriable != nil {
		return policy.Retriable(err)
	}
	// Without a classifier from the host, retry everything except a cancelled
	// context — which cannot succeed on another attempt.
	return !errors.Is(err, context.Canceled)
}

// retryDelay computes the wait before an attempt, capped and optionally jittered.
func retryDelay(policy *RetryPolicy, attempt int) time.Duration {
	initial := policy.InitialDelay
	if initial <= 0 {
		initial = 250 * time.Millisecond
	}
	maxDelay := policy.MaxDelay
	if maxDelay <= 0 {
		maxDelay = 30 * time.Second
	}
	var wait time.Duration
	switch policy.Strategy {
	case "fixed":
		wait = initial
	case "linear":
		wait = time.Duration(attempt) * initial
	default:
		wait = initial << min(attempt-1, 16)
	}
	if wait > maxDelay || wait <= 0 {
		wait = maxDelay
	}
	if policy.Jitter || policy.Strategy == "exponential_jitter" || policy.Strategy == "decorrelated_jitter" {
		half := wait / 2
		wait = half + time.Duration(rand.Int64N(int64(half)+1))
	}
	return wait
}

// ---------------------------------------------------------------------------
// Small helpers
// ---------------------------------------------------------------------------

func evalGuard(guard Guard, scope Scope) (bool, error) {
	if guard == nil {
		return true, nil
	}
	return guard.Eval(scope)
}

func lookup(root map[string]any, path string) (any, bool) {
	var current any = root
	for _, segment := range splitPath(path) {
		object, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		next, found := object[segment]
		if !found {
			return nil, false
		}
		current = next
	}
	return current, true
}

func splitPath(path string) []string {
	if path == "" {
		return nil
	}
	segments := make([]string, 0, 4)
	start := 0
	for i := 0; i < len(path); i++ {
		if path[i] == '.' {
			segments = append(segments, path[start:i])
			start = i + 1
		}
	}
	return append(segments, path[start:])
}

func toStrings(value any) []string {
	switch typed := value.(type) {
	case []string:
		return typed
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if text, ok := item.(string); ok && text != "" {
				out = append(out, text)
			}
		}
		return out
	case string:
		if typed == "" {
			return nil
		}
		return []string{typed}
	default:
		return nil
	}
}

func toFloat(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case json.Number:
		number, err := typed.Float64()
		return number, err == nil
	case string:
		var number float64
		if _, err := fmt.Sscanf(typed, "%g", &number); err == nil {
			return number, true
		}
		return 0, false
	default:
		return 0, false
	}
}
