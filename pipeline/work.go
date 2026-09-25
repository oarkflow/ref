package pipeline

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
)

// Load is a worker's current workload, as the host counts it across cases.
type Load struct {
	Open         int       `json:"open"`
	LastAssigned time.Time `json:"last_assigned,omitempty"`
}

// ---------------------------------------------------------------------------
// Directory and eligibility
// ---------------------------------------------------------------------------

// workers merges the definition's worker blocks with the host directory
// (host entries win), sorted by id for deterministic routing.
func (e *Engine) workers(ctx context.Context) []Worker {
	byID := map[string]Worker{}
	for _, w := range e.C.Def.Workers {
		byID[w.ID] = w
	}
	if e.Directory != nil {
		for _, w := range e.Directory(ctx) {
			byID[w.ID] = w
		}
	}
	out := make([]Worker, 0, len(byID))
	for _, w := range byID {
		out = append(out, w)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Worker returns a directory entry.
func (e *Engine) Worker(ctx context.Context, id string) (Worker, bool) {
	for _, w := range e.workers(ctx) {
		if w.ID == id {
			return w, true
		}
	}
	return Worker{}, false
}

// AwayAt reports whether the worker is absent at t.
func (w Worker) AwayAt(t time.Time) (bool, string) {
	for _, a := range w.Away {
		from, ok1 := parseDay(a.From, false)
		until, ok2 := parseDay(a.Until, true)
		if ok1 && ok2 && !t.Before(from) && t.Before(until) {
			return true, orDefault(a.Reason, a.Name)
		}
	}
	return false, ""
}

// parseDay reads an RFC 3339 time or a date; an end date covers the whole day.
func parseDay(s string, end bool) (time.Time, bool) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, true
	}
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return time.Time{}, false
	}
	if end {
		t = t.AddDate(0, 0, 1)
	}
	return t, true
}

func (e *Engine) calendar(name string) *WorkCalendar {
	if name == "" {
		return nil
	}
	return e.C.calendars[name]
}

// eligible checks a worker against a stage's routing rules. It returns "" or
// the reason the worker cannot take the case.
func (e *Engine) eligible(c *Case, st *Stage, r *Routing, w Worker, roles []string, load Load, now time.Time) string {
	if w.Inactive {
		return "inactive"
	}
	if !slices.ContainsFunc(w.Roles, func(role string) bool { return slices.Contains(roles, role) }) {
		return "lacks role " + strings.Join(roles, "/")
	}
	if away, why := w.AwayAt(now); away {
		return "away: " + why
	}
	if r != nil && r.RespectHours && !e.calendar(w.Calendar).IsOpen(now) {
		return "outside working hours"
	}
	if r != nil {
		need := slices.Clone(r.RequiredSkills)
		if r.SkillsFrom != "" {
			switch v := getAny(c, r.SkillsFrom).(type) {
			case []any:
				for _, s := range v {
					need = append(need, fmt.Sprint(s))
				}
			case string:
				if v != "" {
					need = append(need, v)
				}
			}
		}
		for _, s := range need {
			if !slices.Contains(w.Skills, s) {
				return "lacks skill " + s
			}
		}
		if r.SameOrgUnit && c.OrgUnit != "" && !e.inOrg(w, c.OrgUnit) {
			return "outside org unit " + c.OrgUnit
		}
	}
	capacity := w.Capacity
	if capacity == 0 && r != nil {
		capacity = r.Capacity
	}
	if capacity > 0 && load.Open >= capacity {
		return fmt.Sprintf("at capacity (%d/%d)", load.Open, capacity)
	}
	return ""
}

func (e *Engine) inOrg(w Worker, unit string) bool {
	if e.OrgCovers != nil {
		return e.OrgCovers(w.OrgUnits, unit)
	}
	return slices.Contains(w.OrgUnits, unit)
}

func getAny(c *Case, path string) any {
	v, _ := c.Get(path)
	return v
}

// stageRoles are the roles that work a stage (routing roles if narrowed).
func stageRoles(st *Stage) []string {
	if st.Routing != nil && len(st.Routing.Roles) > 0 {
		return st.Routing.Roles
	}
	return st.Roles
}

func claimable(st *Stage) bool { return st.Claimable || st.Routing != nil }

// ---------------------------------------------------------------------------
// Routing
// ---------------------------------------------------------------------------

// route assigns the stage's work under its routing strategy. roles overrides
// the candidate roles (escalation); exclude drops people (who just released).
func (e *Engine) route(ctx context.Context, c *Case, st *Stage, ss *StageState, roles []string, exclude []string, reason string) error {
	now := e.now()
	r := st.Routing
	strategy := RouteManual
	if r != nil && r.Strategy != "" {
		strategy = r.Strategy
	}
	if len(roles) == 0 {
		roles = stageRoles(st)
	} else {
		// Escalation targets other roles (supervisors): the stage's skill
		// and org requirements are for the people who normally work it.
		if strategy == RouteManual || strategy == RouteSkills {
			strategy = RouteLeastLoaded
		}
		r = &Routing{Strategy: strategy}
	}
	loads := map[string]Load{}
	if e.Workload != nil && strategy != RouteManual {
		l, err := e.Workload(ctx, c.Pipeline)
		if err != nil {
			return fmt.Errorf("pipeline: workload: %w", err)
		}
		loads = l
	}
	decision := &RoutingDecision{Strategy: strategy, At: now}
	prev := ss.Assignee
	if prev == "" {
		prev = ss.PreviousAssignee
	}
	var eligibleIDs []Candidate
	if strategy != RouteManual {
		for _, w := range e.workers(ctx) {
			load := loads[w.ID]
			cand := Candidate{ID: w.ID, Load: load.Open}
			if slices.Contains(exclude, w.ID) {
				cand.Reason = "excluded"
			} else {
				cand.Reason = e.eligible(c, st, r, w, roles, load, now)
			}
			cand.Eligible = cand.Reason == ""
			if cand.Eligible && r != nil {
				for _, s := range r.PreferredSkills {
					if slices.Contains(w.Skills, s) {
						cand.Score++
					}
				}
			}
			decision.Candidates = append(decision.Candidates, cand)
			if cand.Eligible {
				eligibleIDs = append(eligibleIDs, cand)
			}
		}
	}
	winner := ""
	switch {
	case strategy == RouteManual:
		decision.Reason = "queued: the first eligible person to claim it takes it"
	case len(eligibleIDs) == 0:
		decision.Reason = "queued: nobody is eligible right now"
	default:
		if r != nil && r.Sticky && prev != "" {
			for _, cand := range eligibleIDs {
				if cand.ID == prev {
					winner, decision.Reason = prev, "sticky: "+prev+" worked this stage before"
				}
			}
		}
		if winner == "" {
			sort.SliceStable(eligibleIDs, func(i, j int) bool {
				a, b := eligibleIDs[i], eligibleIDs[j]
				if strategy == RouteSkills && a.Score != b.Score {
					return a.Score > b.Score
				}
				if strategy != RouteRoundRobin && a.Load != b.Load {
					return a.Load < b.Load
				}
				la, lb := loads[a.ID].LastAssigned, loads[b.ID].LastAssigned
				if !la.Equal(lb) {
					return la.Before(lb)
				}
				return a.ID < b.ID
			})
			winner = eligibleIDs[0].ID
			switch strategy {
			case RouteRoundRobin:
				decision.Reason = "round robin: " + winner + " was assigned least recently"
			case RouteSkills:
				decision.Reason = fmt.Sprintf("skills: %s matches %d preferred skills with load %d", winner, eligibleIDs[0].Score, eligibleIDs[0].Load)
			default:
				decision.Reason = fmt.Sprintf("least loaded: %s has %d open", winner, eligibleIDs[0].Load)
			}
		}
	}
	if reason != "" {
		decision.Reason = reason + "; " + decision.Reason
	}
	decision.Assignee = winner
	ss.Routing = decision
	if winner == "" {
		ss.Assignee, ss.AssignedAt = "", nil
		c.emit("queued", st.Name, "", now, map[string]any{"reason": decision.Reason})
		return nil
	}
	e.setAssignee(c, st.Name, ss, winner, now)
	c.emit("assigned", st.Name, "", now, map[string]any{"assignee": winner, "reason": decision.Reason})
	return nil
}

func (e *Engine) setAssignee(c *Case, stage string, ss *StageState, who string, now time.Time) {
	ss.Assignee = who
	if who == "" {
		ss.AssignedAt = nil
	} else {
		ss.AssignedAt = &now
	}
	if v := c.openVisit(stage); v != nil && who != "" {
		v.Assignee = who
	}
}

// guardClaim enforces claims: on a claimable stage only the assignee (or an
// assigner) acts; an unassigned case is claimed by whoever acts first.
func (e *Engine) guardClaim(c *Case, st *Stage, ss *StageState, actor Actor) error {
	if ss.Suspended != nil {
		return badState("stage %q is on hold: %s", st.Name, ss.Suspended.Reason)
	}
	if !claimable(st) || actor.Link != nil || (st.Public && isApplicant(c, actor)) {
		return nil
	}
	switch ss.Assignee {
	case actor.ID:
		return nil
	case "":
		now := e.now()
		e.setAssignee(c, st.Name, ss, actor.ID, now)
		c.History = append(c.History, Entry{At: now, Actor: actor.ID, Stage: st.Name, Action: "claim"})
		c.emit("claimed", st.Name, actor.ID, now, nil)
		return nil
	}
	if actor.HasAnyRole(st.AssignRoles) {
		return nil
	}
	return forbidden("stage %q is assigned to %s", st.Name, ss.Assignee)
}

// ---------------------------------------------------------------------------
// Claim, release, assign, delegate
// ---------------------------------------------------------------------------

// Claim takes an open stage's work.
func (e *Engine) Claim(ctx context.Context, in *Case, actor Actor, stage string) (*Case, error) {
	c := in.Clone()
	st, ss, err := e.stageOpen(c, stage)
	if err != nil {
		return nil, err
	}
	if !claimable(st) {
		return nil, badState("stage %q is not claimable", stage)
	}
	if !e.CanAct(c, st, actor) {
		return nil, forbidden("you may not work stage %q", stage)
	}
	if ss.Assignee == actor.ID {
		return nil, badState("you already hold this case")
	}
	if ss.Assignee != "" {
		return nil, badState("stage %q is assigned to %s", stage, ss.Assignee)
	}
	now := e.now()
	e.setAssignee(c, stage, ss, actor.ID, now)
	c.History = append(c.History, Entry{At: now, Actor: actor.ID, Stage: stage, Action: "claim"})
	c.emit("claimed", stage, actor.ID, now, nil)
	c.UpdatedAt = now
	return c, nil
}

// Release gives the work back: to the queue, or to routing (excluding the
// person releasing it).
func (e *Engine) Release(ctx context.Context, in *Case, actor Actor, stage, comment string) (*Case, error) {
	c := in.Clone()
	st, ss, err := e.stageOpen(c, stage)
	if err != nil {
		return nil, err
	}
	if ss.Assignee == "" {
		return nil, badState("nobody holds stage %q", stage)
	}
	if ss.Assignee != actor.ID && !actor.HasAnyRole(st.AssignRoles) {
		return nil, forbidden("only %s or an assigner may release this case", ss.Assignee)
	}
	now := e.now()
	held := ss.Assignee
	e.setAssignee(c, stage, ss, "", now)
	c.History = append(c.History, Entry{At: now, Actor: actor.ID, Stage: stage, Action: "release", From: held, Comment: comment})
	c.emit("released", stage, actor.ID, now, map[string]any{"from": held})
	if st.Routing != nil && st.Routing.Strategy != "" && st.Routing.Strategy != RouteManual {
		if err := e.route(ctx, c, st, ss, nil, []string{held}, "released by "+held); err != nil {
			return nil, err
		}
	}
	c.UpdatedAt = now
	return c, nil
}

// Assign gives the work to someone (an assigner's decision).
func (e *Engine) Assign(ctx context.Context, in *Case, actor Actor, stage, to, comment string) (*Case, error) {
	c := in.Clone()
	st, ss, err := e.stageOpen(c, stage)
	if err != nil {
		return nil, err
	}
	if !actor.HasAnyRole(st.AssignRoles) {
		return nil, forbidden("you may not assign stage %q", stage)
	}
	if err := e.assignTo(ctx, c, st, ss, actor, to, "assign", comment); err != nil {
		return nil, err
	}
	return c, nil
}

// Delegate hands work the actor holds to a colleague.
func (e *Engine) Delegate(ctx context.Context, in *Case, actor Actor, stage, to, comment string) (*Case, error) {
	c := in.Clone()
	st, ss, err := e.stageOpen(c, stage)
	if err != nil {
		return nil, err
	}
	if ss.Assignee != actor.ID {
		return nil, forbidden("you can only delegate work you hold")
	}
	if err := e.assignTo(ctx, c, st, ss, actor, to, "delegate", comment); err != nil {
		return nil, err
	}
	return c, nil
}

func (e *Engine) assignTo(ctx context.Context, c *Case, st *Stage, ss *StageState, actor Actor, to, verb, comment string) error {
	if to == "" || to == ss.Assignee {
		return badState("%s to whom?", verb)
	}
	w, ok := e.Worker(ctx, to)
	if !ok {
		return fmt.Errorf("%w: worker %q", ErrNotFound, to)
	}
	now := e.now()
	var load Load
	if e.Workload != nil {
		if loads, err := e.Workload(ctx, c.Pipeline); err == nil {
			load = loads[to]
		}
	}
	// An assigner may overrule capacity; everything else still applies.
	r := st.Routing
	if why := e.eligible(c, st, r, w, stageRoles(st), Load{}, now); why != "" {
		return &ValidationError{Message: to + " cannot take this case", Fields: []FieldError{{Path: "to", Rule: "eligibility", Message: why}}}
	}
	from := ss.Assignee
	e.setAssignee(c, st.Name, ss, to, now)
	ss.Routing = &RoutingDecision{Strategy: verb, Assignee: to, At: now, Reason: fmt.Sprintf("%s by %s (load %d)", verb, actor.ID, load.Open)}
	c.History = append(c.History, Entry{At: now, Actor: actor.ID, Stage: st.Name, Action: verb, From: from, To: to, Comment: comment})
	name := "assigned"
	if verb == "delegate" {
		name = "delegated"
	}
	c.emit(name, st.Name, actor.ID, now, map[string]any{"assignee": to, "from": from})
	c.UpdatedAt = now
	return nil
}

// ---------------------------------------------------------------------------
// Suspension
// ---------------------------------------------------------------------------

// Suspend puts the current stage on hold (awaiting documents, an inspection).
// Nothing can be done at the stage and its SLA clock stops until Resume, or
// until `until` passes (Sweep resumes it).
func (e *Engine) Suspend(ctx context.Context, in *Case, actor Actor, stage, reason string, until *time.Time) (*Case, error) {
	c := in.Clone()
	st, ss, err := e.stageOpen(c, stage)
	if err != nil {
		return nil, err
	}
	if !actor.HasAnyRole(st.SuspendRoles) {
		return nil, forbidden("you may not put stage %q on hold", stage)
	}
	if ss.Suspended != nil {
		return nil, badState("stage %q is already on hold", stage)
	}
	if strings.TrimSpace(reason) == "" {
		return nil, &ValidationError{Message: "a reason is required", Fields: []FieldError{{Path: "reason", Rule: "required", Message: "say why the case is on hold"}}}
	}
	now := e.now()
	ss.Suspended = &Suspension{Reason: reason, By: actor.ID, At: now, Until: until}
	if ss.SLA != nil && ss.SLA.Status != SLABreached {
		ss.SLA.Status = SLAPaused
	}
	c.History = append(c.History, Entry{At: now, Actor: actor.ID, Stage: stage, Action: "suspend", Comment: reason})
	c.emit("suspended", stage, actor.ID, now, map[string]any{"reason": reason})
	c.UpdatedAt = now
	return c, nil
}

// Resume lifts a hold; the SLA deadline moves by the working time spent on hold.
func (e *Engine) Resume(ctx context.Context, in *Case, actor Actor, stage, comment string) (*Case, error) {
	c := in.Clone()
	st, ss, err := e.stageOpen(c, stage)
	if err != nil {
		return nil, err
	}
	if actor.ID != "system" && !actor.HasAnyRole(st.SuspendRoles) {
		return nil, forbidden("you may not resume stage %q", stage)
	}
	if ss.Suspended == nil {
		return nil, badState("stage %q is not on hold", stage)
	}
	e.resume(c, st, ss, actor.ID, comment)
	return c, nil
}

func (e *Engine) resume(c *Case, st *Stage, ss *StageState, actor, comment string) {
	now := e.now()
	held := ss.Suspended
	ss.Suspended = nil
	if v := c.openVisit(st.Name); v != nil {
		v.SuspendedSeconds += int64(now.Sub(held.At).Seconds())
	}
	if ss.SLA != nil && ss.SLA.Status != SLABreached {
		var cal *WorkCalendar
		if st.SLA != nil {
			cal = e.calendar(st.SLA.Calendar)
		}
		shift := cal.Between(held.At, now)
		ss.SLA.DueAt = cal.Add(ss.SLA.DueAt, shift)
		if ss.SLA.WarnAt != nil {
			w := cal.Add(*ss.SLA.WarnAt, shift)
			ss.SLA.WarnAt = &w
		}
		ss.SLA.Status = SLAOnTrack
		if ss.SLA.WarnAt != nil && !now.Before(*ss.SLA.WarnAt) {
			ss.SLA.Status = SLAWarning
		}
		due := ss.SLA.DueAt
		ss.DueAt = &due
	}
	c.History = append(c.History, Entry{At: now, Actor: actor, Stage: st.Name, Action: "resume", Comment: comment})
	c.emit("resumed", st.Name, actor, now, nil)
	c.UpdatedAt = now
}

// ---------------------------------------------------------------------------
// Notes
// ---------------------------------------------------------------------------

// staff reports whether the actor holds any role of the pipeline.
func (e *Engine) staff(actor Actor) bool {
	if actor.HasAnyRole(e.C.Def.RevealRoles) {
		return true
	}
	if n := e.C.Def.Notes; n != nil && actor.HasAnyRole(n.InternalRoles) {
		return true
	}
	for _, st := range e.C.Def.Stages {
		if actor.HasAnyRole(st.Roles) || actor.HasAnyRole(st.ViewRoles) || actor.HasAnyRole(st.AssignRoles) || actor.HasAnyRole(st.SuspendRoles) {
			return true
		}
		for _, n := range st.Nodes {
			if actor.HasAnyRole(n.Roles) {
				return true
			}
		}
	}
	return false
}

func (e *Engine) internalReader(actor Actor) bool {
	if n := e.C.Def.Notes; n != nil && len(n.InternalRoles) > 0 {
		return actor.HasAnyRole(n.InternalRoles)
	}
	return e.staff(actor)
}

// AddNote adds a note. Staff may write internal notes (seen only by staff)
// and public notes; the applicant may write public notes when allowed.
func (e *Engine) AddNote(ctx context.Context, in *Case, actor Actor, body string, internal bool, parentID string) (*Case, error) {
	c := in.Clone()
	body = strings.TrimSpace(body)
	if body == "" {
		return nil, &ValidationError{Message: "the note is empty", Fields: []FieldError{{Path: "body", Rule: "required", Message: "write something"}}}
	}
	if len(body) > 10000 {
		return nil, &ValidationError{Message: "the note is too long", Fields: []FieldError{{Path: "body", Rule: "max_length", Message: "at most 10000 characters"}}}
	}
	applicant := isApplicant(c, actor)
	switch {
	case internal && !e.internalReader(actor):
		return nil, forbidden("you may not write internal notes")
	case !internal && !e.staff(actor) && !(applicant && e.C.Def.Notes != nil && e.C.Def.Notes.ApplicantMayWrite):
		return nil, forbidden("you may not add notes to this case")
	}
	if parentID != "" {
		found := false
		for _, n := range c.Notes {
			if n.ID == parentID {
				found = true
				if n.Internal && !internal {
					return nil, badState("a reply to an internal note must be internal")
				}
			}
		}
		if !found {
			return nil, fmt.Errorf("%w: note %q", ErrNotFound, parentID)
		}
	}
	now := e.now()
	note := Note{ID: e.newID("note"), Stage: c.Stage, Author: actor.ID, Body: body, Internal: internal, ParentID: parentID, At: now}
	c.Notes = append(c.Notes, note)
	c.History = append(c.History, Entry{At: now, Actor: actor.ID, Stage: c.Stage, Action: "note", To: note.ID})
	c.emit("note.added", c.Stage, actor.ID, now, map[string]any{"note_id": note.ID, "internal": internal})
	c.UpdatedAt = now
	return c, nil
}

// NotesFor returns the notes the actor may read.
func (e *Engine) NotesFor(c *Case, actor Actor) []Note {
	internal := e.internalReader(actor)
	var out []Note
	for _, n := range c.Notes {
		if !n.Internal || internal {
			out = append(out, n)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Timeline
// ---------------------------------------------------------------------------

func (c *Case) openVisit(stage string) *Visit {
	for i := len(c.Timeline) - 1; i >= 0; i-- {
		if c.Timeline[i].Stage == stage && c.Timeline[i].LeftAt == nil {
			return &c.Timeline[i]
		}
	}
	return nil
}

func (c *Case) closeVisit(stage, outcome string, at time.Time) {
	if v := c.openVisit(stage); v != nil {
		v.LeftAt = &at
		v.Outcome = outcome
	}
}
