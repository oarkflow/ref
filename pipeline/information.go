package pipeline

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"
)

// InfoOptions say who a request for information is put to. To is "creator" (the
// default), "previous" (the people who handle the stage the case came from) or
// "user" (the person named in Assignee); which of them a stage allows is its
// information policy.
type InfoOptions struct {
	To       string `json:"to,omitempty"`
	Assignee string `json:"assignee,omitempty"`
	// Fields are inputs (form.input) the person asked must fill in; allowed when
	// the stage's information policy sets fields.
	Fields []string `json:"fields,omitempty"`
}

// RequestInformation asks the person who created the case for more information.
func (e *Engine) RequestInformation(ctx context.Context, in *Case, actor Actor, reason string) (*Case, *InformationRequest, error) {
	return e.RequestInformationTo(ctx, in, actor, reason, InfoOptions{})
}

// RequestInformationTo pauses the current stage until the person asked answers. By
// default the stage's deadline clock stops meanwhile (information sla "run" keeps it
// going), and an information due time starts counting.
func (e *Engine) RequestInformationTo(_ context.Context, in *Case, actor Actor, reason string, opts InfoOptions) (*Case, *InformationRequest, error) {
	if !e.C.Def.AllowRFI {
		return nil, nil, badState("this pipeline does not allow information requests")
	}
	if actor.ID == "" || actor.ID == in.CreatedBy || actor.Link != nil {
		return nil, nil, forbidden("only a staff member may request information")
	}
	st, ss, err := e.stageOpen(in, in.Stage)
	if err != nil {
		return nil, nil, err
	}
	if len(st.InfoRoles) > 0 {
		if !actor.HasAnyRole(st.InfoRoles) {
			return nil, nil, notYours("You cannot ask for information", *st, actor)
		}
	} else if !e.CanAct(in, st, actor) {
		return nil, nil, notYours("You cannot ask for information", *st, actor)
	}
	if err := e.guardAssignee(in, st, ss, actor); err != nil {
		return nil, nil, err
	}
	if in.Status == CaseRequiresInformation {
		return nil, nil, badState("an information request is already pending")
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return nil, nil, badState("a reason is required")
	}
	pol := e.infoPolicy(st)
	to := strings.ToLower(strings.TrimSpace(opts.To))
	if to == "" {
		to = pol.To[0]
	}
	if !allows(pol.To, to) {
		return nil, nil, badState("information may be asked of %s at this stage, not of %q", strings.Join(pol.To, ", "), to)
	}
	item := InformationRequest{ID: e.newID("rfi"), Stage: in.Stage, Reason: reason, RequestedBy: actor.ID, PreviousStatus: in.Status, To: to}
	switch to {
	case "previous":
		prev, ok := e.previousStage(in, in.Stage)
		ps, found := e.C.Stage(prev)
		if !ok || !found || len(ps.Roles) == 0 {
			return nil, nil, badState("there is no earlier stage whose people can be asked")
		}
		item.AnswerRoles = ps.Roles
	case "user":
		item.Assignee = strings.TrimSpace(opts.Assignee)
		if item.Assignee == "" {
			return nil, nil, &ValidationError{Message: "choose who to ask", Fields: []FieldError{{Path: "assignee", Rule: "required", Message: "name the person who should answer"}}}
		}
		if item.Assignee == actor.ID {
			return nil, nil, badState("you cannot ask yourself")
		}
	}
	if len(opts.Fields) > 0 {
		if !pol.Fields {
			return nil, nil, badState("this stage does not let a question ask for specific inputs")
		}
		for _, path := range opts.Fields {
			form, name, _ := strings.Cut(path, ".")
			inp, cf, ok := e.C.input(form, name)
			switch {
			case !ok:
				return nil, nil, &ValidationError{Message: "unknown input", Fields: []FieldError{{Path: "fields", Rule: "unknown", Message: path + " is not an input of this workflow"}}}
			case cf.Repeatable || inp.Kind == KindFile || inp.Compute != "" || e.C.sealed[path]:
				return nil, nil, &ValidationError{Message: "cannot ask for this input", Fields: []FieldError{{Path: "fields", Rule: "unsupported", Message: path + " cannot be filled in as an answer (it is repeatable, a file, calculated or sealed)"}}}
			}
			if !slices.Contains(item.Fields, path) {
				item.Fields = append(item.Fields, path)
			}
		}
	}
	c := in.Clone()
	now := e.now()
	item.RequestedAt = now
	if d, _ := parseDuration(pol.Due); d > 0 {
		due := now.Add(d)
		item.DueAt = &due
	}
	if css := c.Stages[c.Stage]; css != nil && css.SLA != nil && pol.SLA != "run" && css.SLA.Status != SLABreached && css.SLA.Status != SLAPaused {
		css.SLA.Status = SLAPaused
		item.PausedAt = &now
	}
	c.InformationRequests = append(c.InformationRequests, item)
	c.Status = CaseRequiresInformation
	c.UpdatedAt = now
	c.History = append(c.History, Entry{At: now, Actor: actor.ID, Stage: c.Stage, Action: "information.requested", To: e.infoTarget(item), Comment: reason})
	c.emit("case.information_requested", c.Stage, actor.ID, now, map[string]any{"request_id": item.ID, "to": to})
	return c, &c.InformationRequests[len(c.InformationRequests)-1], nil
}

// infoTarget names who a request was put to, for the history.
func (e *Engine) infoTarget(r InformationRequest) string {
	switch r.To {
	case "user":
		return r.Assignee
	case "previous":
		return strings.Join(r.AnswerRoles, " or ")
	}
	return "creator"
}

// mayAnswer reports whether actor is the one a pending request was put to.
func mayAnswer(r InformationRequest, c *Case, actor Actor) bool {
	if actor.ID == "" || actor.Link != nil || r.RespondedAt != nil {
		return false
	}
	switch r.To {
	case "user":
		return actor.ID == r.Assignee
	case "previous":
		return actor.HasAnyRole(r.AnswerRoles)
	}
	return actor.ID == c.CreatedBy
}

// IsInfoAnswerer reports whether actor is the person (or one of the people) the
// case's pending information request was put to.
func (e *Engine) IsInfoAnswerer(c *Case, actor Actor) bool {
	if c.Status != CaseRequiresInformation || len(c.InformationRequests) == 0 {
		return false
	}
	return mayAnswer(c.InformationRequests[len(c.InformationRequests)-1], c, actor)
}

// mayRequestInfo reports whether the actor may ask for more information at this
// open stage right now.
func (e *Engine) mayRequestInfo(c *Case, st *Stage, actor Actor) bool {
	if !e.C.Def.AllowRFI || actor.ID == "" || actor.ID == c.CreatedBy || actor.Link != nil || c.Status == CaseRequiresInformation {
		return false
	}
	if len(st.InfoRoles) > 0 {
		return actor.HasAnyRole(st.InfoRoles)
	}
	return e.CanAct(c, st, actor)
}

// SubmitInformation records the answer, restarts the deadline clock if the request
// stopped it, and resumes the same stage.
func (e *Engine) SubmitInformation(_ context.Context, in *Case, actor Actor, response map[string]any) (*Case, *InformationRequest, error) {
	if in.Status != CaseRequiresInformation || len(in.InformationRequests) == 0 {
		return nil, nil, badState("no information request is pending")
	}
	last := in.InformationRequests[len(in.InformationRequests)-1]
	if last.RespondedAt != nil {
		return nil, nil, badState("the information request has already been answered")
	}
	if !mayAnswer(last, in, actor) {
		return nil, nil, forbidden("this question is for %s to answer", e.askedOf(last))
	}
	if len(response) == 0 {
		return nil, nil, badState("a response is required")
	}
	c := in.Clone()
	item := &c.InformationRequests[len(c.InformationRequests)-1]
	now := e.now()
	var changed []string
	if len(item.Fields) > 0 {
		var err error
		if changed, err = e.applyAnswerFields(c, actor, item.Fields, response); err != nil {
			return nil, nil, err
		}
	}
	item.Response = response
	item.RespondedBy = actor.ID
	item.RespondedAt = &now
	if item.PausedAt != nil {
		if st, ok := e.C.Stage(item.Stage); ok {
			if ss := c.Stages[item.Stage]; ss != nil {
				e.unpauseSLA(st, ss, *item.PausedAt, now)
			}
		}
		if v := c.openVisit(item.Stage); v != nil {
			v.SuspendedSeconds += int64(now.Sub(*item.PausedAt).Seconds())
		}
	}
	c.Status = item.PreviousStatus
	c.UpdatedAt = now
	c.History = append(c.History, Entry{At: now, Actor: actor.ID, Stage: item.Stage, Action: "information.submitted", Changes: changed})
	c.emit("case.information_submitted", item.Stage, actor.ID, now, map[string]any{"request_id": item.ID})
	return c, item, nil
}

// askedOf says who a request was put to, in words.
func (e *Engine) askedOf(r InformationRequest) string {
	switch r.To {
	case "user":
		return r.Assignee
	case "previous":
		return "the " + strings.Join(r.AnswerRoles, " or ") + " role"
	}
	return "the person who created the case"
}

// sweepInformation notices a request whose answer is overdue, once, and applies
// the stage's on_timeout: tell people (the default) or close the case as withdrawn.
func (e *Engine) sweepInformation(c *Case, now time.Time) bool {
	if c.Status != CaseRequiresInformation || len(c.InformationRequests) == 0 {
		return false
	}
	req := &c.InformationRequests[len(c.InformationRequests)-1]
	if req.RespondedAt != nil || req.DueAt == nil || req.Overdue || now.Before(*req.DueAt) {
		return false
	}
	req.Overdue = true
	c.History = append(c.History, Entry{At: now, Actor: "system", Stage: req.Stage, Action: "information.overdue", To: e.infoTarget(*req), Comment: req.Reason})
	c.emit("case.information_overdue", req.Stage, "system", now, map[string]any{"request_id": req.ID, "to": req.To})
	if st, ok := e.C.Stage(req.Stage); ok && e.infoPolicy(st).OnTimeout == "withdraw" {
		c.closeVisit(req.Stage, "expired", now)
		c.Status = CaseWithdrawn
		c.History = append(c.History, Entry{At: now, Actor: "system", Stage: req.Stage, Action: "expire", Comment: fmt.Sprintf("no answer to %q", req.Reason)})
		c.emit("case.expired", req.Stage, "system", now, map[string]any{"reason": "no answer to the information request"})
		e.finish(c, now)
	}
	return true
}

// InfoTarget is one place a question may be put, for the screen.
type InfoTarget struct {
	To    string `json:"to"`
	Label string `json:"label"`
	// Person says a person must be named.
	Person bool `json:"person,omitempty"`
	// Due is how long they will have, when the policy sets a limit.
	Due string `json:"due,omitempty"`
}

func (e *Engine) infoTargets(c *Case, st *Stage) []InfoTarget {
	pol := e.infoPolicy(st)
	var out []InfoTarget
	for _, to := range pol.To {
		t := InfoTarget{To: to, Due: pol.Due}
		switch to {
		case "creator":
			t.Label = "The person who created it"
		case "previous":
			prev, ok := e.previousStage(c, st.Name)
			ps, found := e.C.Stage(prev)
			if !ok || !found || len(ps.Roles) == 0 {
				continue
			}
			t.Label = orDefault(ps.Title, ps.Name) + " (" + strings.Join(ps.Roles, " or ") + ")"
		case "user":
			t.Label, t.Person = "A named person", true
		}
		out = append(out, t)
	}
	return out
}

// applyAnswerFields writes the inputs a question asked for into the case data.
// Each must be answered; values are coerced and checked like any entry (types,
// patterns, lookups), whatever the person's rights on the stage, because the
// question itself is what entitles them. The response carries them as
// data.<form>.<input> or as "<form>.<input>" keys under data.
func (e *Engine) applyAnswerFields(c *Case, actor Actor, fields []string, response map[string]any) ([]string, error) {
	data, _ := response["data"].(map[string]any)
	var errs []FieldError
	type write struct {
		path string
		v    any
	}
	var writes []write
	for _, path := range fields {
		form, name, _ := strings.Cut(path, ".")
		inp, _, ok := e.C.input(form, name)
		if !ok {
			continue
		}
		var raw any
		if v, found := data[path]; found {
			raw = v
		} else if f, ok := data[form].(map[string]any); ok {
			raw = f[name]
		}
		label := orDefault(inp.Label, inp.Name)
		if isEmpty(raw) {
			errs = append(errs, FieldError{Path: path, Rule: "required", Message: label + " is required"})
			continue
		}
		v, rule, msg := e.C.coerce(form, inp, raw)
		if msg == "" {
			rule, msg = e.checkLookup(c, form, inp, v)
		}
		if msg != "" {
			errs = append(errs, FieldError{Path: path, Rule: rule, Message: msg})
			continue
		}
		writes = append(writes, write{path, v})
	}
	if len(errs) > 0 {
		return nil, &ValidationError{Message: "the answer is incomplete", Fields: errs}
	}
	var changed []string
	for _, w := range writes {
		if old, had := c.Get(w.path); !had || fmt.Sprint(old) != fmt.Sprint(w.v) {
			changed = append(changed, w.path)
		}
		c.Set(w.path, w.v)
	}
	e.recompute(c, actor)
	return changed, nil
}
