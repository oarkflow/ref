package pipeline

import (
	"context"
	"fmt"
	"time"
)

// startSLA arms a stage's SLA on entry.
func (e *Engine) startSLA(st *Stage, ss *StageState, now time.Time) {
	if st.SLA == nil {
		return
	}
	cal := e.calendar(st.SLA.Calendar)
	span, _ := ParseSpan(st.SLA.Duration)
	state := &SLAState{Status: SLAOnTrack, StartedAt: now, DueAt: span.From(cal, now)}
	if warn, _ := ParseSpan(st.SLA.WarnBefore); !warn.Zero() {
		var at time.Time
		if span.Days == 0 && warn.Days == 0 && warn.Dur < span.Dur {
			at = cal.Add(now, span.Dur-warn.Dur) // warning in working time
		} else {
			at = warn.Before(state.DueAt)
		}
		if at.Before(now) {
			at = now
		}
		state.WarnAt = &at
	}
	if pick, _ := ParseSpan(st.SLA.PickupTimeout); !pick.Zero() {
		at := pick.From(cal, now)
		state.PickupDueAt = &at
	}
	ss.SLA = state
	due := state.DueAt
	ss.DueAt = &due
}

// SweepResult reports what a sweep did.
type SweepResult struct {
	Changed bool
	// Purge asks the host to delete the case (retention action purge).
	Purge bool
}

// Sweep applies time-based rules to a case as of now: resuming holds whose
// time is up, SLA warnings, breaches and their actions, escalation levels,
// and retention of finished cases. The host calls it periodically for open
// cases (in REF: the pipeline.sweep action on a schedule) and saves the
// result when Changed.
func (e *Engine) Sweep(ctx context.Context, in *Case) (*Case, SweepResult, error) {
	c := in.Clone()
	now := e.now()
	var res SweepResult
	if c.Terminal() {
		purge, changed := e.applyRetention(c, now)
		res.Changed, res.Purge = changed, purge
		return c, res, nil
	}
	st, ok := e.C.Stage(c.Stage)
	ss := c.Stages[c.Stage]
	if !ok || ss == nil || (ss.Status != StageActive && ss.Status != StageReturned) {
		return c, res, nil
	}
	if ss.Suspended != nil {
		if ss.Suspended.Until != nil && !now.Before(*ss.Suspended.Until) {
			e.resume(c, st, ss, "system", "hold period ended")
			res.Changed = true
		} else {
			return c, res, nil
		}
	}
	if st.SLA == nil || ss.SLA == nil {
		return c, res, nil
	}
	sla := ss.SLA
	if sla.Status == SLAOnTrack && sla.WarnAt != nil && !now.Before(*sla.WarnAt) && now.Before(sla.DueAt) {
		sla.Status = SLAWarning
		c.emit("sla.warning", st.Name, "system", now, map[string]any{"due_at": sla.DueAt, "assignee": ss.Assignee})
		c.History = append(c.History, Entry{At: now, Actor: "system", Stage: st.Name, Action: "sla_warning"})
		res.Changed = true
	}
	if sla.PickupDueAt != nil && !sla.PickupBreached && ss.Assignee == "" && !now.Before(*sla.PickupDueAt) {
		sla.PickupBreached = true
		action := orDefault(st.SLA.OnPickupTimeout, "notify")
		c.History = append(c.History, Entry{At: now, Actor: "system", Stage: st.Name, Action: "sla_pickup_breached", To: action})
		c.emit("sla.pickup_breached", st.Name, "system", now, map[string]any{"due_at": *sla.PickupDueAt, "action": action})
		res.Changed = true
		if action == "reassign" {
			if err := e.route(ctx, c, st, ss, st.SLA.ReassignRoles, nil, "pickup timeout"); err != nil {
				return nil, res, err
			}
		}
	}
	if sla.Status != SLABreached && !now.Before(sla.DueAt) {
		sla.Status = SLABreached
		sla.BreachedAt = &now
		if v := c.openVisit(st.Name); v != nil {
			v.Breached = true
		}
		cause, reason := breachCause(c, st, ss, sla.DueAt, now)
		action := orDefault(st.SLA.OnBreach, "notify")
		info := BreachInfo{Cause: cause, Reason: reason, DueAt: sla.DueAt, BreachedAt: now, OverdueSeconds: int64(now.Sub(sla.DueAt).Seconds()),
			Assignee: ss.Assignee, StartedBy: ss.StartedBy, StartedAt: ss.StartedAt, Action: action}
		sla.Breach = &info
		rec := BreachRecord{ID: fmt.Sprintf("br_%d_%s", len(c.Breaches)+1, st.Name), Stage: st.Name, BreachInfo: info}
		c.Breaches = append(c.Breaches, rec)
		c.History = append(c.History, Entry{At: now, Actor: "system", Stage: st.Name, Action: "sla_breached", To: action, Comment: reason})
		notify := append(append([]string{}, st.SLA.Notify...), adminRecipients(st)...)
		c.emit("sla.breached", st.Name, "system", now, map[string]any{"breach": rec.ID, "due_at": sla.DueAt, "assignee": ss.Assignee, "action": action,
			"cause": cause, "reason": reason, "overdue_seconds": info.OverdueSeconds, "started_by": ss.StartedBy, "notify": notify})
		res.Changed = true
		if err := e.onBreach(ctx, c, st, ss); err != nil {
			return nil, res, err
		}
		if c.Stage != st.Name || c.Terminal() {
			return c, res, nil // the breach action moved the case on
		}
	}
	// A reset starts a fresh SLA, so the old one has nothing left to escalate.
	// Escalation levels are measured from the breach.
	if ss.SLA == sla && sla.Status == SLABreached && sla.BreachedAt != nil {
		cal := e.calendar(st.SLA.Calendar)
		for sla.Level < len(st.SLA.Escalate) {
			level := st.SLA.Escalate[sla.Level]
			after, _ := ParseSpan(level.After)
			if now.Before(after.From(cal, *sla.BreachedAt)) {
				break
			}
			sla.Level++
			detail := map[string]any{"level": sla.Level, "name": level.Name, "notify": level.Notify}
			if len(level.AssignRoles) > 0 {
				if err := e.route(ctx, c, st, ss, level.AssignRoles, nil, "escalation "+level.Name); err != nil {
					return nil, res, err
				}
				detail["assignee"] = ss.Assignee
			}
			c.History = append(c.History, Entry{At: now, Actor: "system", Stage: st.Name, Action: "escalate", To: level.Name})
			c.emit("sla.escalated", st.Name, "system", now, detail)
			res.Changed = true
		}
	}
	if res.Changed {
		c.UpdatedAt = now
		e.finish(c, now)
	}
	return c, res, nil
}

func (e *Engine) onBreach(ctx context.Context, c *Case, st *Stage, ss *StageState) error {
	system := Actor{ID: "system"}
	now := e.now()
	switch action := st.SLA.OnBreach; action {
	case "", "notify":
		return nil
	case "reassign":
		held := ss.Assignee
		var exclude []string
		if held != "" {
			exclude = []string{held}
		}
		if err := e.route(ctx, c, st, ss, st.SLA.ReassignRoles, exclude, "SLA breached"); err != nil {
			return err
		}
		e.resolveBreach(c, st.Name, "system", "reassign", "automatic: "+orDefault(ss.Assignee, "released to the queue"), now)
		return nil
	case "release":
		if held := ss.Assignee; held != "" {
			e.setAssignee(c, st.Name, ss, "", now)
			c.History = append(c.History, Entry{At: now, Actor: "system", Stage: st.Name, Action: "release", From: held, Comment: "SLA breached"})
			c.emit("released", st.Name, "system", now, map[string]any{"from": held})
		}
		e.resolveBreach(c, st.Name, "system", "release", "automatic", now)
		return nil
	case "reset":
		if err := e.resetStage(ctx, c, st, ss, system, "SLA breached: "+c.breachRecord(st.Name).Reason, now); err != nil {
			c.History = append(c.History, Entry{At: now, Actor: "system", Stage: st.Name, Action: "sla_action_failed", Comment: err.Error()})
			return nil
		}
		e.resolveBreach(c, st.Name, "system", "reset", "automatic", now)
		return nil
	case "extend":
		if err := e.extendSLA(c, st, ss, "system", "", "automatic", now); err != nil {
			c.History = append(c.History, Entry{At: now, Actor: "system", Stage: st.Name, Action: "sla_action_failed", Comment: err.Error()})
		}
		return nil
	case "expire":
		// Nobody answered in time: the case is closed as withdrawn.
		e.resolveBreach(c, st.Name, "system", "expire", "automatic", now)
		c.closeVisit(st.Name, "expired", now)
		c.Status = CaseWithdrawn
		c.History = append(c.History, Entry{At: now, Actor: "system", Stage: st.Name, Action: "expire", Comment: c.breachRecordReason(st.Name)})
		c.emit("case.expired", st.Name, "system", now, map[string]any{"reason": c.breachRecordReason(st.Name)})
		e.finish(c, now)
		return nil
	case "return":
		e.resolveBreach(c, st.Name, "system", "return", "automatic", now)
		return e.returnTo(c, st, ss, st.SLA.ReturnTo, system, "SLA breached", nil, now)
	default:
		spec, ok := findAction(st, action)
		if !ok {
			return fmt.Errorf("pipeline: stage %q on_breach names unknown action %q", st.Name, action)
		}
		next, err := e.act(ctx, c, system, st.Name, spec, ActInput{Comment: "SLA breached"}, true)
		if err != nil {
			// The automatic action could not run (e.g. required data is
			// missing): the breach stays recorded for people to act on.
			c.History = append(c.History, Entry{At: e.now(), Actor: "system", Stage: st.Name, Action: "sla_action_failed", Comment: err.Error()})
			return nil
		}
		*c = *next
		e.resolveBreach(c, st.Name, "system", action, "automatic", now)
		return nil
	}
}

// adminRecipients names the stage's admins as event recipients.
func adminRecipients(st *Stage) []string {
	var out []string
	for _, r := range st.AdminRoles {
		out = append(out, "role:"+r)
	}
	return out
}

// applyRetention anonymises or flags for purge a finished case whose
// retention period has passed.
func (e *Engine) applyRetention(c *Case, now time.Time) (purge, changed bool) {
	r := e.C.Def.Retention
	if r == nil || c.ClosedAt == nil || c.Hold != nil || c.Erased != nil {
		return false, false
	}
	span, _ := ParseSpan(r.After)
	if now.Before(span.From(nil, *c.ClosedAt)) {
		return false, false
	}
	if r.Action == "purge" {
		return true, false
	}
	e.anonymize(c, "system", "retention period ended", now)
	c.emit("retention.applied", "", "system", now, map[string]any{"action": "anonymize"})
	return false, true
}
