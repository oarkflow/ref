package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// cloneData deep-copies a case's data document.
func cloneData(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	raw, err := json.Marshal(in)
	if err != nil {
		return nil
	}
	var out map[string]any
	if json.Unmarshal(raw, &out) != nil {
		return nil
	}
	return out
}

// isAdmin reports whether the actor may take a stand on the stage.
func isAdmin(st *Stage, actor Actor) bool {
	return actor.ID == "system" || actor.HasAnyRole(st.AdminRoles)
}

// markStarted records who began the stage and when, on the stage and on the
// current visit.
func (e *Engine) markStarted(c *Case, st *Stage, ss *StageState, actor Actor, now time.Time) {
	ss.StartedAt, ss.StartedBy = &now, actor.ID
	if v := c.openVisit(st.Name); v != nil {
		v.StartedAt, v.StartedBy = &now, actor.ID
	}
	c.History = append(c.History, Entry{At: now, Actor: actor.ID, Stage: st.Name, Action: "start"})
	c.emit("stage.started", st.Name, actor.ID, now, nil)
}

// StartStage begins work on a stage: it records who started and when, and on a
// claimable stage it also claims the work for the person starting.
func (e *Engine) StartStage(_ context.Context, in *Case, actor Actor, stage string) (*Case, error) {
	c := in.Clone()
	st, ss, err := e.stageOpen(c, stage)
	if err != nil {
		return nil, err
	}
	if !e.CanAct(c, st, actor) || actor.Link != nil {
		return nil, forbidden("you may not work stage %q", stage)
	}
	if ss.StartedAt != nil {
		return nil, badState("stage %q was already started by %s", stage, ss.StartedBy)
	}
	if err := e.guardAssignee(c, st, ss, actor); err != nil {
		return nil, err
	}
	now := e.now()
	e.markStarted(c, st, ss, actor, now)
	c.UpdatedAt = now
	return c, nil
}

// breachCause says in words why the deadline was missed.
func breachCause(c *Case, st *Stage, ss *StageState, due, now time.Time) (cause, reason string) {
	who := ss.Assignee
	switch {
	case st.External != nil:
		cause = "external_no_response"
		party := st.External.Party
		if l := c.Links[ss.ExternalLink]; l != nil {
			party = l.Party
		}
		reason = fmt.Sprintf("Waiting for %s: they did not submit before the deadline.", orDefault(party, "the outside party"))
	case st.Public && !c.Terminal() && ss.StartedBy == "" && who == "":
		cause = "applicant_no_response"
		reason = "The applicant did not complete this stage in time."
	case ss.StartedAt != nil:
		cause = "not_completed"
		reason = fmt.Sprintf("%s started the stage at %s but it was not finished by the deadline.", orDefault(ss.StartedBy, "someone"), ss.StartedAt.UTC().Format("2 Jan 15:04 MST"))
	case who != "":
		cause = "not_started"
		reason = fmt.Sprintf("The stage was assigned to %s but never started.", who)
	default:
		cause = "not_picked_up"
		reason = "Nobody picked the stage up: it was never claimed or started."
	}
	if v := c.openVisitFor(ss); v != nil && v.SuspendedSeconds > 0 {
		reason += fmt.Sprintf(" It was on hold for %s (the clock was paused).", (time.Duration(v.SuspendedSeconds) * time.Second).Round(time.Minute))
	}
	if st.Entries != nil {
		_, submitted, drafts := ss.countEntries("")
		reason += fmt.Sprintf(" %d of %d required entries were submitted (%d still drafts).", submitted, st.Entries.need(), drafts)
	}
	over := now.Sub(due).Round(time.Minute)
	if over > 0 {
		reason += fmt.Sprintf(" It is %s overdue.", over)
	}
	return cause, reason
}

// openVisitFor finds the open visit of the stage the state belongs to.
func (c *Case) openVisitFor(ss *StageState) *Visit {
	for name, s := range c.Stages {
		if s == ss {
			return c.openVisit(name)
		}
	}
	return nil
}

// breachRecord returns the unresolved record for a stage, if any.
func (c *Case) breachRecord(stage string) *BreachRecord {
	for i := len(c.Breaches) - 1; i >= 0; i-- {
		if c.Breaches[i].Stage == stage && c.Breaches[i].ResolvedAt == nil {
			return &c.Breaches[i]
		}
	}
	return nil
}

// resolveBreach closes the open breach record of a stage.
func (e *Engine) resolveBreach(c *Case, stage, by, resolution, comment string, now time.Time) {
	rec := c.breachRecord(stage)
	if rec == nil {
		return
	}
	rec.ResolvedAt, rec.ResolvedBy, rec.Resolution, rec.Comment = &now, by, resolution, comment
	c.emit("sla.resolved", stage, by, now, map[string]any{"action": resolution, "comment": comment, "breach": rec.ID, "reason": rec.Reason})
}

// ResetStage puts a stage back to how it was when it opened: the case data is
// restored, the assignee, the start, holds, flags and check results are
// cleared, the visit is closed as reset, and a fresh SLA window begins. The
// history keeps everything that happened. Only the stage's admins (and the
// system, on an SLA breach policy) may do it.
func (e *Engine) ResetStage(ctx context.Context, in *Case, actor Actor, stage, comment string) (*Case, error) {
	c := in.Clone()
	st, ss, err := e.stageOpen(c, stage)
	if err != nil {
		return nil, err
	}
	if !isAdmin(st, actor) {
		return nil, forbidden("you may not reset stage %q", stage)
	}
	now := e.now()
	if err := e.resetStage(ctx, c, st, ss, actor, comment, now); err != nil {
		return nil, err
	}
	e.resolveBreach(c, stage, actor.ID, "reset", comment, now)
	return c, nil
}

func (e *Engine) resetStage(ctx context.Context, c *Case, st *Stage, ss *StageState, actor Actor, comment string, now time.Time) error {
	if ss.EntryData == nil {
		return badState("stage %q has no saved starting point to reset to", st.Name)
	}
	prev := ss.Assignee
	was := map[string]any{"started_by": ss.StartedBy, "assignee": prev}
	c.Data = cloneData(ss.EntryData)
	if c.Data == nil {
		c.Data = map[string]any{}
	}
	if prev != "" {
		ss.PreviousAssignee = prev
	}
	ss.Assignee, ss.AssignedAt = "", nil
	ss.StartedAt, ss.StartedBy = nil, ""
	ss.Nodes, ss.Flags, ss.Suspended, ss.Review, ss.Routing = nil, nil, nil, nil, nil
	ss.Entries = nil
	e.initNodes(c, st, ss, e.Env(c, actor)) // fresh check results, as on entry
	ss.Status = StageActive
	ss.EnteredAt = &now
	ss.CompletedAt, ss.CompletedBy = nil, ""
	ss.Resets++
	// Acknowledgements given on this stage must be given again.
	kept := c.Acknowledgements[:0]
	for _, a := range c.Acknowledgements {
		if a.Stage != st.Name {
			kept = append(kept, a)
		}
	}
	c.Acknowledgements = kept
	c.closeVisit(st.Name, "reset", now)
	c.Timeline = append(c.Timeline, Visit{Stage: st.Name, EnteredAt: now})
	c.Stage, c.Status = st.Name, CaseInProgress
	eff := e.stageFor(c, st)
	ss.SLA, ss.DueAt = nil, nil
	e.startSLA(eff, ss, now)
	if v := c.openVisit(st.Name); v != nil && ss.SLA != nil {
		due := ss.SLA.DueAt
		v.DueAt = &due
	}
	c.History = append(c.History, Entry{At: now, Actor: actor.ID, Stage: st.Name, Action: "reset", Comment: comment})
	c.emit("stage.reset", st.Name, actor.ID, now, map[string]any{"reason": comment, "was": was})
	if st.Routing != nil && st.Routing.Strategy != "" && st.Routing.Strategy != RouteManual {
		if err := e.route(ctx, c, st, ss, nil, nil, "reset"); err != nil {
			return err
		}
	}
	c.UpdatedAt = now
	return nil
}

// ExtendSLA moves a stage's deadline out. by is a span such as "24h"; empty
// uses the SLA's own extend_by. The clock is re-armed so warnings and
// escalation start over.
func (e *Engine) ExtendSLA(_ context.Context, in *Case, actor Actor, stage, by, comment string) (*Case, error) {
	c := in.Clone()
	st, ss, err := e.stageOpen(c, stage)
	if err != nil {
		return nil, err
	}
	if !isAdmin(st, actor) {
		return nil, forbidden("you may not extend the deadline of stage %q", stage)
	}
	if err := e.extendSLA(c, st, ss, actor.ID, by, comment, e.now()); err != nil {
		return nil, err
	}
	return c, nil
}

func (e *Engine) extendSLA(c *Case, st *Stage, ss *StageState, by, span, comment string, now time.Time) error {
	if st.SLA == nil || ss.SLA == nil {
		return badState("stage %q has no SLA", st.Name)
	}
	span = strings.TrimSpace(orDefault(span, st.SLA.ExtendBy))
	sp, err := ParseSpan(span)
	if err != nil || sp.Zero() {
		return &ValidationError{Message: "say how much time to add, e.g. 24h", Fields: []FieldError{{Path: "by", Rule: "required", Message: "an extension such as 24h or 2d is required"}}}
	}
	cal := e.calendar(st.SLA.Calendar)
	sla := ss.SLA
	from := now
	if sla.DueAt.After(now) {
		from = sla.DueAt
	}
	due := sp.From(cal, from)
	e.resolveBreach(c, st.Name, by, "extend", strings.TrimSpace(comment+" (+"+span+")"), now)
	sla.DueAt, sla.Status, sla.BreachedAt, sla.Level, sla.WarnAt, sla.Breach = due, SLAOnTrack, nil, 0, nil, nil
	ss.DueAt = &due
	if v := c.openVisit(st.Name); v != nil {
		v.DueAt, v.Breached = &due, false
	}
	c.History = append(c.History, Entry{At: now, Actor: by, Stage: st.Name, Action: "sla_extended", Comment: strings.TrimSpace(comment + " (+" + span + ")")})
	c.UpdatedAt = now
	return nil
}

// WaiveSLA accepts a missed deadline: nothing changes on the stage, but the
// breach is closed as waived with the admin's reason.
func (e *Engine) WaiveSLA(_ context.Context, in *Case, actor Actor, stage, comment string) (*Case, error) {
	c := in.Clone()
	st, _, err := e.stageOpen(c, stage)
	if err != nil {
		return nil, err
	}
	if !isAdmin(st, actor) {
		return nil, forbidden("you may not waive the deadline of stage %q", stage)
	}
	if strings.TrimSpace(comment) == "" {
		return nil, &ValidationError{Message: "a reason is required", Fields: []FieldError{{Path: "comment", Rule: "required", Message: "say why the missed deadline is accepted"}}}
	}
	now := e.now()
	if c.breachRecord(stage) == nil {
		return nil, badState("stage %q has no open SLA breach", stage)
	}
	e.resolveBreach(c, stage, actor.ID, "waive", comment, now)
	c.History = append(c.History, Entry{At: now, Actor: actor.ID, Stage: stage, Action: "sla_waived", Comment: comment})
	c.UpdatedAt = now
	return c, nil
}

// breachRecordReason is the reason of the stage's latest breach record.
func (c *Case) breachRecordReason(stage string) string {
	for i := len(c.Breaches) - 1; i >= 0; i-- {
		if c.Breaches[i].Stage == stage {
			return c.Breaches[i].Reason
		}
	}
	return ""
}
