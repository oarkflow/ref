package process

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Waking parked runs: events, timers and operator gates.
//
// Everything here shares one shape. A parked run left a durable marker — a
// subscription or a timer — recording which step to resume at and which edge
// parked it. Something external arrives, the marker is found, matched and
// consumed, a frame goes back on the run's cursor, and the run advances.
//
// Consuming the marker before advancing is what makes a signal at-most-once per
// subscription. If the advance then fails, the run keeps the frame on its cursor
// and a later advance picks it up: the work is not lost, and the event is not
// applied twice.

// Signal delivers an external event to whatever runs are waiting for it.
//
// It returns the run ids it woke. Delivering an event nobody is waiting for is not
// an error: a system emitting "payment.settled" should not have to know whether
// anything cares, and treating it as a failure would make every integration
// fragile.
func (e *Engine) Signal(ctx context.Context, name, correlation string, payload any) ([]string, error) {
	if name == "" {
		return nil, errors.New("ref/process: an event needs a name")
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("ref/process: the event payload cannot be serialised: %w", err)
	}

	if err := e.store.RecordEvent(ctx, &Event{
		ID: randomID(), Name: name, Correlation: correlation,
		Payload: encoded, ReceivedAt: e.now(),
	}); err != nil {
		return nil, err
	}

	subscriptions, err := e.store.ClaimSubscriptions(ctx, name, correlation, encoded, e.now(), 1000, e.leaseTTL)
	if err != nil {
		return nil, err
	}
	now := e.now()
	woken := make([]string, 0, len(subscriptions))
	var failures []error
	for _, subscription := range subscriptions {
		if subscription.ExpiresAt != nil && subscription.ExpiresAt.Before(now) {
			// The run has already given up on this event; its timeout timer will
			// clean the subscription up.
			continue
		}
		if err := e.deliver(ctx, subscription, encoded); err != nil {
			failures = append(failures, fmt.Errorf("subscription %s: %w", subscription.ID, err))
			continue
		}
		woken = append(woken, subscription.RunID)
	}
	return woken, errors.Join(failures...)
}

// deliver consumes one subscription and resumes its run.
//
// The order is: put the continuation on the cursor (durable), acknowledge the
// subscription, drop the deadline that was guarding it, then advance. Advancing
// before the acknowledgement — as this used to — let the advance see its own
// still-claimed subscription as outstanding work, so a run whose last step was
// the one the event resumed parked as "waiting" and, once the acknowledgement
// deleted the subscription, never woke again.
func (e *Engine) deliver(ctx context.Context, subscription *Subscription, payload json.RawMessage) error {
	if err := e.deliverClaimed(ctx, subscription, payload); err != nil {
		_ = e.store.ReleaseSubscription(context.WithoutCancel(ctx), subscription.ID, subscription.ClaimToken, err)
		return err
	}
	if err := e.store.AckSubscription(context.WithoutCancel(ctx), subscription.ID, subscription.ClaimToken); err != nil {
		return err
	}
	if subscription.Event != childCompletedEvent && subscription.Edge != "" {
		// Only this wait's own deadline: another branch waiting on another event
		// keeps its deadline. Deleting every wait_event timer of the run, as this
		// did, left a parallel wait with no timeout at all — it could wait forever.
		if err := e.deleteTimers(ctx, subscription.RunID, "wait_event", subscription.Edge, ""); err != nil {
			return err
		}
	}
	return e.advanceOrEnqueue(ctx, subscription.RunID)
}

func (e *Engine) deliverClaimed(ctx context.Context, subscription *Subscription, payload json.RawMessage) error {
	if subscription.Event == childCompletedEvent {
		return e.resumeAfterChild(ctx, subscription, payload)
	}
	return e.resume(ctx, subscription.RunID, Frame{
		Step: subscription.Step, Input: payload, Edge: subscription.Edge, Attempt: 1,
	})
}

// resume puts a frame back on a parked run's cursor.
//
// It takes the lease, so a resume cannot interleave with an advance, and it
// tolerates a revision conflict by retrying: the common cause is another signal
// arriving at the same instant, and both should land. It does not advance: the
// caller first consumes the marker (timer, subscription) that held the park, so
// the advance does not mistake it for outstanding work.
func (e *Engine) resume(ctx context.Context, runID string, frame Frame) error {
	return e.withRunLease(ctx, runID, func(ctx context.Context, run *Run) error {
		if run.Status == StatusCompensating {
			// The run is unwinding; forward work that arrives now is abandoned with
			// the rest of it.
			return nil
		}
		run.Frames = append(run.Frames, frame)
		run.Status = StatusRunning
		run.Waiting = nil
		return nil
	})
}

// AdvanceManual releases an operator gate.
//
// It is the only thing that releases one: no timer, no event and no retry will.
// That is the point of a manual edge, and an engine that quietly released them
// after a while would be worse than not having them.
func (e *Engine) AdvanceManual(ctx context.Context, runID, edge string) error {
	subscriptions, err := e.store.ListSubscriptions(ctx, runID)
	if err != nil {
		return err
	}
	var target *Subscription
	for _, subscription := range subscriptions {
		if subscription.Event != manualEventName {
			continue
		}
		if edge == "" || subscription.Edge == edge {
			target = subscription
			break
		}
	}
	if target == nil {
		return fmt.Errorf("ref/process: run %s has no operator gate outstanding%s", runID, edgeSuffix(edge))
	}

	// The source step's result was parked alongside the gate so the resumed step
	// receives it rather than nothing. It is removed only once the gate has really
	// been released: a release that fails (the run was busy) must leave it for the
	// retry, which would otherwise resume the step with no input.
	payload := json.RawMessage(nil)
	payloadTimer := ""
	if timers, err := e.store.ListTimers(ctx, runID); err == nil {
		for _, timer := range timers {
			if timer.Kind == "manual_payload" && timer.Edge == target.Edge {
				payload, payloadTimer = timer.Payload, timer.ID
				break
			}
		}
	}
	claimed, err := e.store.ClaimSubscription(ctx, target.ID, nil, e.now(), e.leaseTTL)
	if err != nil {
		return err
	}
	if claimed == nil {
		return fmt.Errorf("ref/process: operator gate %s is no longer available", target.ID)
	}
	if err := e.resume(ctx, runID, Frame{Step: target.Step, Input: payload, Edge: target.Edge, Attempt: 1}); err != nil {
		_ = e.store.ReleaseSubscription(context.WithoutCancel(ctx), claimed.ID, claimed.ClaimToken, err)
		return err
	}
	if err := e.store.AckSubscription(context.WithoutCancel(ctx), claimed.ID, claimed.ClaimToken); err != nil {
		return err
	}
	if payloadTimer != "" {
		_ = e.store.DeleteTimer(ctx, payloadTimer)
	}
	return e.Advance(ctx, runID)
}

func edgeSuffix(edge string) string {
	if edge == "" {
		return ""
	}
	return fmt.Sprintf(" on edge %q", edge)
}

// ---------------------------------------------------------------------------
// Timers
// ---------------------------------------------------------------------------

// Tick fires every timer that is due and returns how many it handled.
//
// Call it from a scheduled job. It is safe to call from every replica at once: the
// store claims timers atomically, so each one fires once.
func (e *Engine) Tick(ctx context.Context, limit int) (int, error) {
	now := e.now()
	timers, err := e.store.ClaimDueTimers(ctx, now, limit, e.leaseTTL)
	if err != nil {
		return 0, err
	}
	handled := 0
	var failures []error
	for _, timer := range timers {
		if err := e.fireTimer(ctx, timer); err != nil {
			failures = append(failures, fmt.Errorf("timer %s: %w", timer.ID, err))
			_ = e.store.ReleaseTimer(context.WithoutCancel(ctx), timer.ID, timer.ClaimToken, e.now().Add(time.Second), err)
			continue
		}
		if err := e.store.AckTimer(context.WithoutCancel(ctx), timer.ID, timer.ClaimToken); err != nil {
			// A lost claim here means the handler itself re-armed a timer under the
			// same id — a retry's wake timer re-parking the run for its next
			// backoff. The new timer is the live one; this firing did its job.
			if !errors.Is(err, ErrClaimLost) {
				failures = append(failures, fmt.Errorf("ack timer %s: %w", timer.ID, err))
				continue
			}
		}
		handled++
	}
	if err := e.tickTasks(ctx, limit); err != nil {
		failures = append(failures, err)
	}
	return handled, errors.Join(failures...)
}

// fireTimer dispatches one due timer.
//
// Every handler that resumes the run deletes its own timer once the resumed work
// is on the cursor and before it advances (consumeAndAdvance). A timer still
// present during that advance would count as outstanding work, parking a run that
// had actually finished.
func (e *Engine) fireTimer(ctx context.Context, timer *Timer) error {
	run, err := e.store.GetRun(ctx, timer.RunID)
	if err != nil {
		if errors.Is(err, ErrRunNotFound) {
			// The run was purged; its timer is just litter.
			return nil
		}
		return err
	}
	if run.Status.Terminal() {
		return nil
	}

	switch timer.Kind {
	case "run_timeout":
		definition, defErr := e.definitionFor(run)
		timeout := time.Duration(0)
		if defErr == nil {
			timeout = definition.Timeout
		}
		return e.failRun(ctx, run, "", fmt.Errorf("the run exceeded its %s timeout", timeout))

	case "sla":
		return e.handleSLABreach(ctx, run)

	case "wake":
		return e.advanceOrEnqueue(ctx, run.ID)

	case "delayed":
		// The park is over: put the continuation back and go.
		if err := e.resume(ctx, run.ID, Frame{Step: timer.Step, Input: timer.Payload, Edge: timer.Edge, Attempt: 1}); err != nil {
			return err
		}
		return e.consumeAndAdvance(ctx, timer)

	case "rate_limited":
		return e.handleRateLimited(ctx, run, timer)

	case "wait_event", "timeout":
		return e.handleDeadline(ctx, run, timer)

	case "race_timeout":
		return e.handleRaceTimeout(ctx, run, timer)

	case "escalation":
		return e.handleEscalation(ctx, run, timer)

	case "task_escalation":
		return e.handleTaskEscalation(ctx, run, timer)

	case "manual_payload":
		// Not a real timer: it carries a manual gate's payload and must never fire.
		// Reaching here means the far-future fire time was somehow met, so put it
		// back rather than acting on it.
		return e.store.AddTimer(ctx, timer)

	default:
		return fmt.Errorf("ref/process: run %s has a timer of unknown kind %q", run.ID, timer.Kind)
	}
}

// consumeAndAdvance deletes a fired timer whose work is now on the cursor, then
// advances the run.
func (e *Engine) consumeAndAdvance(ctx context.Context, timer *Timer) error {
	if err := e.store.DeleteTimer(ctx, timer.ID); err != nil {
		return err
	}
	return e.advanceOrEnqueue(ctx, timer.RunID)
}

// handleDeadline handles a wait-event deadline or a timeout edge's deadline
// expiring.
//
// A deadline is live only while what it guards is still outstanding: the
// subscription for a wait, or — for a timeout edge — the target's open task,
// running child process or queued frame. A deadline whose target already
// finished is stale and does nothing; one that is live stops the target, so the
// overrun work cannot resume the run later, and continues at on_timeout or fails
// the run.
func (e *Engine) handleDeadline(ctx context.Context, run *Run, timer *Timer) error {
	fired := false
	err := e.withRunLease(ctx, run.ID, func(ctx context.Context, run *Run) error {
		if run.Status == StatusCompensating {
			return nil
		}
		definition, err := e.definitionFor(run)
		if err != nil {
			return e.failRun(ctx, run, timer.Step, err)
		}
		live := false
		switch timer.Kind {
		case "wait_event":
			subscriptions, err := e.store.ListSubscriptions(ctx, run.ID)
			if err != nil {
				return err
			}
			for _, subscription := range subscriptions {
				if subscription.Edge != timer.Edge || subscription.Event == manualEventName || subscription.Event == childCompletedEvent {
					continue
				}
				claimed, err := e.store.ClaimSubscription(ctx, subscription.ID, nil, e.now(), e.leaseTTL)
				if errors.Is(err, ErrClaimLost) {
					// The event is being delivered right now. It arrived first; it wins.
					continue
				}
				if err != nil {
					return err
				}
				if claimed != nil {
					if err := e.store.AckSubscription(ctx, claimed.ID, claimed.ClaimToken); err != nil {
						return err
					}
					live = true
				}
			}
		case "timeout":
			if live, err = e.cancelOutstanding(ctx, run, timer.Step, timer.Edge); err != nil {
				return err
			}
		}
		if !live {
			return nil
		}
		fired = true
		if timer.OnFire != "" {
			// A configured on_timeout target is the author's answer to "what if it
			// never comes", and taking it is not a failure.
			e.pushTimedOut(run, timer.Edge, timer.Step, timer.OnFire)
			run.Status = StatusRunning
			run.Waiting = nil
			return nil
		}
		// An expiry with no configured alternative is a failure, and a failure runs
		// compensation like any other.
		return e.beginCompensation(ctx, definition, run, timer.Step,
			fmt.Errorf("the run waited for %q longer than allowed and the edge declares no on_timeout", timer.Edge))
	})
	if err != nil {
		return err
	}
	if !fired {
		return nil
	}
	return e.consumeAndAdvance(ctx, timer)
}

// handleRaceTimeout ends a race nobody won in time: every entrant still running
// is stopped, and the run continues at on_timeout or fails.
func (e *Engine) handleRaceTimeout(ctx context.Context, run *Run, timer *Timer) error {
	fired := false
	err := e.withRunLease(ctx, run.ID, func(ctx context.Context, run *Run) error {
		if run.Status == StatusCompensating {
			return nil
		}
		definition, err := e.definitionFor(run)
		if err != nil {
			return e.failRun(ctx, run, timer.Step, err)
		}
		race, ok := definition.Edge(timer.Edge)
		if !ok {
			return nil
		}
		row, err := e.store.GetJoin(ctx, run.ID, raceRow(race))
		if err != nil {
			return err
		}
		if row == nil || row.Emitted {
			// Decided before the deadline (and the timer somehow survived): stale.
			return nil
		}
		row.Emitted = true
		if err := e.store.SaveJoin(ctx, row); err != nil {
			return err
		}
		for _, target := range race.allTargets() {
			if _, err := e.cancelOutstanding(ctx, run, target, race.Name); err != nil {
				return err
			}
		}
		fired = true
		if timer.OnFire != "" {
			e.pushTimedOut(run, race.Name, timer.Step, timer.OnFire)
			run.Status = StatusRunning
			run.Waiting = nil
			return nil
		}
		return e.beginCompensation(ctx, definition, run, timer.Step,
			fmt.Errorf("race %q had no winner within %s and declares no on_timeout", race.Name, race.Timeout))
	})
	if err != nil {
		return err
	}
	if !fired {
		return e.store.DeleteTimer(ctx, timer.ID)
	}
	return e.consumeAndAdvance(ctx, timer)
}

// handleRateLimited asks the limiter again when a rate_limited edge's park ends.
// Admitted, the run continues at the target; refused — the window reopened but
// other runs took it — the run parks again until the next reset.
func (e *Engine) handleRateLimited(ctx context.Context, run *Run, timer *Timer) error {
	definition, err := e.definitionFor(run)
	if err != nil {
		return err
	}
	edge, ok := definition.Edge(timer.Edge)
	if !ok || e.limiter == nil {
		return fmt.Errorf("ref/process: run %s is parked on rate_limited edge %q, which this engine can no longer evaluate", run.ID, timer.Edge)
	}
	allowed, _, resetAt, err := e.limiter.Allow(ctx, "edge:"+edge.Name, edge.Limit, edge.Window)
	if err != nil {
		return fmt.Errorf("edge %q: the rate limiter is unavailable: %w", edge.Name, err)
	}
	if !allowed {
		if resetAt.IsZero() || !resetAt.After(e.now()) {
			resetAt = e.now().Add(edge.Window)
		}
		again := *timer
		again.ID, again.Fire, again.CreatedAt = randomID(), resetAt, e.now()
		if err := e.store.AddTimer(ctx, &again); err != nil {
			return err
		}
		if e.enqueuer != nil {
			_ = e.enqueuer.EnqueueAdvanceAt(ctx, run.ID, resetAt)
		}
		return e.store.DeleteTimer(ctx, timer.ID)
	}
	if err := e.resume(ctx, run.ID, Frame{Step: timer.Step, Input: timer.Payload, Edge: timer.Edge, Attempt: 1}); err != nil {
		return err
	}
	return e.consumeAndAdvance(ctx, timer)
}

// handleEscalation raises overdue work and continues at the escalation target:
// it notifies the edge's channel, reassigns the run's open tasks to the edge's
// escalate role, and resumes at the target.
func (e *Engine) handleEscalation(ctx context.Context, run *Run, timer *Timer) error {
	var edge *Edge
	if definition, err := e.definitionFor(run); err == nil {
		edge, _ = definition.Edge(timer.Edge)
	}
	if e.notifier != nil {
		channel := ""
		if edge != nil {
			channel = edge.Notify
		}
		_ = e.notifier.NotifyProcess(ctx, NotifyEvent{
			Kind: "escalation", Channel: channel, Run: run, Step: timer.Step,
			Detail: fmt.Sprintf("escalating after %s", timer.Edge),
		})
	}
	if edge != nil && edge.Escalate != "" {
		// The edge's escalate role was accepted and ignored before: the work was
		// "escalated" without anybody new being able to see it.
		if err := e.escalateRunTasks(ctx, run, edge.Escalate); err != nil {
			return err
		}
	}
	if err := e.resume(ctx, run.ID, Frame{Step: timer.Step, Input: timer.Payload, Edge: timer.Edge, Attempt: 1}); err != nil {
		return err
	}
	return e.consumeAndAdvance(ctx, timer)
}

// handleTaskEscalation escalates a human task that is still open when an
// escalation edge leaving its step times out: the task moves to the edge's
// escalate role, the edge's channel is notified, and the run continues at the
// edge's target while the task stays open for its new owners.
func (e *Engine) handleTaskEscalation(ctx context.Context, run *Run, timer *Timer) error {
	var watched taskEscalation
	if err := json.Unmarshal(timer.Payload, &watched); err != nil {
		return e.store.DeleteTimer(ctx, timer.ID)
	}
	task, err := e.store.GetTask(ctx, watched.TaskID)
	if errors.Is(err, ErrTaskNotFound) || (err == nil && !task.Open()) {
		// Done in time (or gone): nothing is overdue.
		return e.store.DeleteTimer(ctx, timer.ID)
	}
	if err != nil {
		return err
	}
	definition, err := e.definitionFor(run)
	if err != nil {
		return err
	}
	edge, ok := definition.Edge(timer.Edge)
	if !ok {
		return e.store.DeleteTimer(ctx, timer.ID)
	}
	if edge.Escalate != "" && task.Role != edge.Escalate {
		task.Role = edge.Escalate
		task.Assignee = ""
		task.ClaimedBy = ""
		task.ClaimedAt = nil
		task.Status = TaskEscalated
		if err := e.store.SaveTask(ctx, task); err != nil {
			return err
		}
	}
	if e.notifier != nil {
		_ = e.notifier.NotifyProcess(ctx, NotifyEvent{
			Kind: "escalation", Channel: edge.Notify, Run: run, Task: task, Step: task.Step,
			Detail: fmt.Sprintf("task %s is overdue; escalated by %s", task.ID, edge.Name),
		})
	}
	payload, _ := json.Marshal(map[string]any{
		"task_id": task.ID, "step": task.Step, "overdue": true, "escalated_to": task.Role,
	})
	if err := e.resume(ctx, run.ID, Frame{Step: timer.Step, Input: payload, From: watched.Step, Edge: timer.Edge, Attempt: 1}); err != nil {
		return err
	}
	return e.consumeAndAdvance(ctx, timer)
}

// handleSLABreach applies the definition's breach action, once.
func (e *Engine) handleSLABreach(ctx context.Context, run *Run) error {
	if run.SLABreached {
		return nil
	}
	definition, ok := e.Definition(run.Process)
	if !ok || definition.SLA == nil {
		return nil
	}
	run.SLABreached = true
	if err := e.store.SaveRun(ctx, run); err != nil && !errors.Is(err, ErrRevisionConflict) {
		return err
	}
	if e.notifier != nil {
		_ = e.notifier.NotifyProcess(ctx, NotifyEvent{
			Kind: "sla_breach", Channel: definition.SLA.Notify, Run: run,
			Detail: fmt.Sprintf("the run passed its %s SLA", definition.SLA.Breach),
		})
	}

	switch definition.SLA.OnBreach {
	case "cancel":
		_, err := e.Cancel(ctx, run.ID, "cancelled after breaching its SLA")
		return err
	case "fail":
		latest, err := e.store.GetRun(ctx, run.ID)
		if err != nil {
			return err
		}
		return e.beginCompensation(ctx, definition, latest, "", fmt.Errorf("the run breached its %s SLA", definition.SLA.Breach))
	case "escalate":
		// Reassign the run's open tasks to the escalation role so somebody with the
		// authority to unblock it sees them.
		return e.escalateRunTasks(ctx, run, definition.SLA.Escalate)
	default:
		// "notify": the notification above was the action.
		return nil
	}
}

func (e *Engine) escalateRunTasks(ctx context.Context, run *Run, role string) error {
	if role == "" {
		return nil
	}
	tasks, err := e.store.ListTasks(ctx, TaskFilter{RunID: run.ID, Limit: 100})
	if err != nil {
		return err
	}
	for _, task := range tasks {
		if !task.Open() {
			continue
		}
		task.Role = role
		task.Assignee = ""
		task.ClaimedBy = ""
		task.ClaimedAt = nil
		task.Status = TaskEscalated
		if err := e.store.SaveTask(ctx, task); err != nil {
			continue
		}
		if e.notifier != nil {
			_ = e.notifier.NotifyProcess(ctx, NotifyEvent{Kind: "escalation", Run: run, Task: task, Step: task.Step,
				Detail: "reassigned to " + role})
		}
	}
	return nil
}

// advanceOrEnqueue prefers the queue when there is one, so a timer firing on a
// scheduler replica does not do the work on that replica.
func (e *Engine) advanceOrEnqueue(ctx context.Context, runID string) error {
	if e.enqueuer != nil {
		return e.enqueuer.EnqueueAdvance(ctx, runID)
	}
	return e.Advance(ctx, runID)
}

// ---------------------------------------------------------------------------
// Retention
// ---------------------------------------------------------------------------

// Purge removes terminal runs past their process's retention. Call it from a
// scheduled job.
//
// Retention is per process, and a process with none keeps its runs forever — which
// is a deliberate choice an author makes rather than a default that quietly grows
// a table until somebody notices.
func (e *Engine) Purge(ctx context.Context, limit int) (int, error) {
	total := 0
	for _, name := range e.Definitions() {
		definition, ok := e.Definition(name)
		if !ok || definition.Retention <= 0 {
			continue
		}
		purged, err := e.store.PurgeRuns(ctx, name, e.now().Add(-definition.Retention), limit)
		total += purged
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

// RecoverStalled re-enqueues runs that should be advancing but are not.
//
// It is the backstop for a replica that died holding a lease: the run stays in
// "running" with nobody driving it until the lease lapses, and nothing would
// otherwise notice. Call it from a scheduled job on a longer interval than Tick.
func (e *Engine) RecoverStalled(ctx context.Context, olderThan time.Duration, limit int) (int, error) {
	if olderThan <= 0 {
		olderThan = 5 * time.Minute
	}
	recovered := 0
	allRuns, err := e.store.ListRuns(ctx, RunFilter{Limit: limit})
	if err != nil {
		return 0, err
	}
	for _, run := range allRuns {
		if run.Status.Terminal() && run.ParentRunID != "" && !run.ParentNotified {
			if err := e.signalParent(ctx, run); err == nil {
				recovered++
			}
		}
	}
	subscriptions, err := e.store.ClaimExpiredSubscriptions(ctx, e.now(), limit, e.leaseTTL)
	if err != nil {
		return 0, err
	}
	for _, subscription := range subscriptions {
		if err := e.deliver(ctx, subscription, subscription.PendingPayload); err == nil {
			recovered++
		}
	}
	cutoff := e.now().Add(-olderThan)
	for _, status := range []Status{StatusPending, StatusWaiting, StatusRunning} {
		runs, err := e.store.ListRuns(ctx, RunFilter{Status: status, Limit: limit})
		if err != nil {
			return recovered, err
		}
		for _, run := range runs {
			if run.UpdatedAt.After(cutoff) {
				continue
			}
			if status == StatusWaiting && run.Waiting != nil && run.Waiting.Reason == "task" {
				tasks, err := e.store.ListTasks(ctx, TaskFilter{RunID: run.ID, Step: run.Waiting.Step, Status: TaskCompleted, Limit: 10})
				if err != nil {
					return recovered, err
				}
				for _, task := range tasks {
					var result map[string]any
					if err := json.Unmarshal(task.Result, &result); err != nil {
						continue
					}
					if err := e.resumeAfterTask(ctx, task, result); err == nil {
						recovered++
						break
					}
				}
				continue
			}
			if err := e.advanceOrEnqueue(ctx, run.ID); err == nil {
				recovered++
			}
		}
	}
	return recovered, nil
}
