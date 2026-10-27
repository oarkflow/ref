package pipeline

import (
	"context"
	"strings"
)

// RequestInformation pauses the current stage until the applicant answers.
func (e *Engine) RequestInformation(_ context.Context, in *Case, actor Actor, reason string) (*Case, *InformationRequest, error) {
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
	if !e.CanAct(in, st, actor) {
		return nil, nil, forbidden("you may not request information at this stage")
	}
	if err := e.guardClaim(in, st, ss, actor); err != nil {
		return nil, nil, err
	}
	if in.Status == CaseRequiresInformation {
		return nil, nil, badState("an information request is already pending")
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return nil, nil, badState("a reason is required")
	}
	c := in.Clone()
	now := e.now()
	item := InformationRequest{ID: e.newID("rfi"), Stage: c.Stage, Reason: reason, RequestedBy: actor.ID, RequestedAt: now, PreviousStatus: c.Status}
	c.InformationRequests = append(c.InformationRequests, item)
	c.Status = CaseRequiresInformation
	c.UpdatedAt = now
	c.History = append(c.History, Entry{At: now, Actor: actor.ID, Stage: c.Stage, Action: "information.requested", Comment: reason})
	c.emit("case.information_requested", c.Stage, actor.ID, now, map[string]any{"request_id": item.ID})
	return c, &c.InformationRequests[len(c.InformationRequests)-1], nil
}

// SubmitInformation records the applicant's answer and resumes the same stage.
func (e *Engine) SubmitInformation(_ context.Context, in *Case, actor Actor, response map[string]any) (*Case, *InformationRequest, error) {
	if actor.ID == "" || actor.ID != in.CreatedBy || actor.Link != nil {
		return nil, nil, forbidden("only the applicant may answer an information request")
	}
	if in.Status != CaseRequiresInformation || len(in.InformationRequests) == 0 {
		return nil, nil, badState("no information request is pending")
	}
	if len(response) == 0 {
		return nil, nil, badState("a response is required")
	}
	c := in.Clone()
	item := &c.InformationRequests[len(c.InformationRequests)-1]
	if item.RespondedAt != nil {
		return nil, nil, badState("the information request has already been answered")
	}
	now := e.now()
	item.Response = response
	item.RespondedBy = actor.ID
	item.RespondedAt = &now
	c.Status = item.PreviousStatus
	c.UpdatedAt = now
	c.History = append(c.History, Entry{At: now, Actor: actor.ID, Stage: item.Stage, Action: "information.submitted"})
	c.emit("case.information_submitted", item.Stage, actor.ID, now, map[string]any{"request_id": item.ID})
	return c, item, nil
}
