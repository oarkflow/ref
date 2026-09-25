package process

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
)

// Child processes.
//
// A step whose body is a process starts a sub-run and parks the parent until it
// finishes. Composing durable processes rather than nesting them in one graph is
// what keeps each one legible: a fulfilment process that calls a refund process
// does not have to inline the refund's own retries, approvals and compensation.
//
// The mechanism is deliberately the same one every other park uses. The parent
// subscribes to a reserved completion event correlated on the child's run id; the
// child, when it finishes, signals it. Nothing special, which means nothing extra
// to go wrong: the parent survives a restart the same way a wait-event park does.

// childCompletedEvent is the reserved event a child run emits when it ends. It is
// namespaced so an application's own events cannot collide with it.
const childCompletedEvent = "__child_completed__"

// startChild launches a sub-run and parks the parent on its completion. When
// the child finishes within the call — which it does in synchronous mode, where
// there is no queue and the child is advanced right here — the child's outcome is
// returned instead of a park, and becomes the step's own result.
func (e *Engine) startChild(ctx context.Context, run *Run, step *Step, frame Frame, input any) (*WaitState, map[string]any, error) {
	if _, ok := e.Definition(step.Process); !ok {
		return nil, nil, fmt.Errorf("step %q calls process %q, which is not registered", step.Name, step.Process)
	}

	// The parent step's own resumption target is itself: when the child finishes,
	// the parent re-enters at this step with the child's result, and the step's
	// outgoing edges resolve off that.

	child, err := e.Start(ctx, step.Process, input, StartOptions{
		// The child inherits identity so its own tenant-scoped steps and
		// authorization gates see the same caller the parent did.
		TenantID:    run.TenantID,
		PrincipalID: run.PrincipalID,
		Identity:    cloneIdentity(run.Identity),
		// Correlating on the parent's run and step makes a repeated advance of the
		// parent return the existing child rather than starting a second one — the
		// same idempotency that protects a queue retry.
		IdempotencyKey: run.ID + ":" + frame.StateKey(),
		CorrelationID:  run.CorrelationID,
		ParentRunID:    run.ID,
		Detached:       true,
		DeferWake:      true,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("step %q could not start child process %q: %w", step.Name, step.Process, err)
	}

	subscriptionID := "child:" + run.ID + ":" + frame.StateKey()
	if err := e.store.Subscribe(ctx, &Subscription{
		ID:          subscriptionID,
		RunID:       run.ID,
		Event:       childCompletedEvent,
		Correlation: child.ID,
		Step:        step.Name,
		Key:         frame.StateKey(),
		CreatedAt:   e.now(),
	}); err != nil {
		return nil, nil, err
	}
	// Advance the child now that the parent's subscription exists. Doing it in this
	// order matters: a fast child that finished first would otherwise signal into
	// nothing and strand the parent.
	if err := e.advanceOrEnqueue(ctx, child.ID); err != nil {
		return nil, nil, err
	}

	// A child that already ended — in synchronous mode, inside the call above —
	// could not signal this run: this run's lease is held by the very pass that is
	// starting the child (signalParent sees that and stands down). Its outcome is
	// read here instead. Parking would wait for a signal that is never coming;
	// before this, the child's completion either missed the subscription or was
	// consumed by a resume that could not take the lease, and the parent waited
	// forever or completed without its child's result.
	if latest, err := e.store.GetRun(ctx, child.ID); err == nil && latest.Status.Terminal() {
		if err := e.store.DeleteSubscription(ctx, subscriptionID); err != nil {
			return nil, nil, err
		}
		if !latest.ParentNotified {
			latest.ParentNotified = true
			_ = e.store.SaveRun(context.WithoutCancel(ctx), latest)
		}
		return nil, childPayload(latest), nil
	}

	return &WaitState{
		Reason: "child", Step: step.Name,
		Detail: fmt.Sprintf("waiting for child process %s (run %s)", step.Process, child.ID),
	}, nil, nil
}

// signalParent tells a waiting parent that this run has ended. It is called from
// the run's own finalisation, so every terminal path — completion, failure,
// cancellation — releases the parent.
func (e *Engine) signalParent(ctx context.Context, run *Run) error {
	if run.ParentRunID == "" || run.ParentNotified {
		return nil
	}
	if leaseHeld(ctx, run.ParentRunID) {
		// The parent is our caller: it is advancing right now and started (or is
		// cancelling) this child inside that advance, so it reads the outcome
		// itself (startChild). Signalling would try to resume a run whose lease this
		// very call chain holds.
		return nil
	}
	// The parent subscribed with the child's run id as the correlation, so that is
	// what the signal carries. Signalling with the parent's id — as this did —
	// matched no subscription at all, and every parent waited forever.
	woken, err := e.Signal(ctx, childCompletedEvent, run.ID, childPayload(run))
	if err != nil {
		return err
	}
	if len(woken) == 0 {
		// Nobody is subscribed. When the parent has already ended there is nothing
		// to tell, and failing the child over it would be wrong — the child
		// genuinely finished. When it is still active, RecoverStalled retries while
		// ParentNotified stays false.
		parent, err := e.store.GetRun(ctx, run.ParentRunID)
		if err == nil && parent.Status.Active() {
			return nil
		}
	}
	run.ParentNotified = true
	return e.store.SaveRun(context.WithoutCancel(ctx), run)
}

// childPayload is what a parent step receives as the result of a child run.
func childPayload(run *Run) map[string]any {
	var output any
	if len(run.Output) > 0 {
		_ = json.Unmarshal(run.Output, &output)
	}
	payload := map[string]any{
		"run_id":  run.ID,
		"process": run.Process,
		"status":  string(run.Status),
		"output":  output,
	}
	if run.Error != "" {
		payload["error"] = run.Error
	}
	return payload
}

// ChildFailurePolicy decides what a parent does when its child fails.
//
// The default is to fail the parent step, which then takes the parent's own error
// edges or compensates. That is almost always right: a fulfilment that could not
// refund has not finished successfully. An author who wants to continue regardless
// puts an error edge on the calling step.
func childOutcomeError(payload map[string]any) error {
	status, _ := payload["status"].(string)
	switch Status(status) {
	case StatusCompleted:
		return nil
	case StatusCancelled:
		return fmt.Errorf("the child process was cancelled")
	default:
		message, _ := payload["error"].(string)
		if message == "" {
			message = "the child process failed"
		}
		return fmt.Errorf("%s", message)
	}
}

// resumeAfterChild re-enters the parent at the calling step with the child's
// outcome, resolving that step's edges off it.
//
// A failed child is surfaced as a step failure, so the parent's error edges, retry
// policy and compensation all apply to it exactly as they would to a failed
// intent. That uniformity is the point: a caller does not need a separate mechanism
// to handle "the sub-process went wrong".
func (e *Engine) resumeAfterChild(ctx context.Context, subscription *Subscription, payload json.RawMessage) error {
	var outcome map[string]any
	if err := json.Unmarshal(payload, &outcome); err != nil {
		outcome = map[string]any{"status": string(StatusFailed), "error": "the child process reported an unreadable result"}
	}
	stateKey := subscription.Key
	if stateKey == "" {
		stateKey = subscription.Step
	}
	// No advance here: deliver acknowledges the subscription first, so the advance
	// that follows does not find it outstanding and park a finished run.
	stillSubscribed := func(ctx context.Context) (bool, error) {
		subscriptions, err := e.store.ListSubscriptions(ctx, subscription.RunID)
		if err != nil {
			return false, err
		}
		return slices.ContainsFunc(subscriptions, func(s *Subscription) bool { return s.ID == subscription.ID }), nil
	}
	return e.applyStepCompletion(ctx, subscription.RunID, subscription.Step, stateKey, outcome, childOutcomeError(outcome), stillSubscribed)
}
