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
	if sla.Status != SLABreached && !now.Before(sla.DueAt) {
		sla.Status = SLABreached
		sla.BreachedAt = &now
		if v := c.openVisit(st.Name); v != nil {
			v.Breached = true
		}
		c.History = append(c.History, Entry{At: now, Actor: "system", Stage: st.Name, Action: "sla_breached", To: st.SLA.OnBreach})
		c.emit("sla.breached", st.Name, "system", now, map[string]any{"due_at": sla.DueAt, "assignee": ss.Assignee, "action": orDefault(st.SLA.OnBreach, "notify")})
		res.Changed = true
		if err := e.onBreach(ctx, c, st, ss); err != nil {
			return nil, res, err
		}
		if c.Stage != st.Name || c.Terminal() {
			return c, res, nil // the breach action moved the case on
		}
	}
	// Escalation levels, measured from the breach.
	if sla.Status == SLABreached && sla.BreachedAt != nil {
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
	switch action := st.SLA.OnBreach; action {
	case "", "notify":
		return nil
	case "reassign":
		held := ss.Assignee
		var exclude []string
		if held != "" {
			exclude = []string{held}
		}
		return e.route(ctx, c, st, ss, nil, exclude, "SLA breached")
	case "return":
		return e.returnTo(c, st, ss, st.SLA.ReturnTo, system, "SLA breached", nil, e.now())
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
		return nil
	}
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
