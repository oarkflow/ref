package pipeline

import (
	"context"
	"fmt"
	"time"
)

func (e *Engine) scheduleStage(c *Case, actor Actor, stage string) (*Stage, *StageState, error) {
	st, ss, err := e.stageOpen(c, stage)
	if err != nil {
		return nil, nil, err
	}
	if !st.MultiDate {
		return nil, nil, badState("stage %q does not allow multi-date schedules", stage)
	}
	if !e.CanAct(c, st, actor) {
		return nil, nil, forbidden("you may not edit stage %q", stage)
	}
	if err := e.guardClaim(c, st, ss, actor); err != nil {
		return nil, nil, err
	}
	return st, ss, nil
}

// Schedules returns only the active stage's schedule instances to an actor who
// may view the stage. The caller receives a deep copy.
func (e *Engine) Schedules(c *Case, actor Actor, stage string) ([]Schedule, error) {
	st, ok := e.C.Stage(stage)
	if !ok {
		return nil, fmt.Errorf("%w: stage %q", ErrNotFound, stage)
	}
	if !st.MultiDate || !e.CanView(c, st, actor) {
		return nil, forbidden("you may not view schedules of stage %q", stage)
	}
	ss := c.Stages[stage]
	if ss == nil {
		return []Schedule{}, nil
	}
	copy := c.Clone().Stages[stage].Schedules
	if copy == nil {
		copy = []Schedule{}
	}
	return copy, nil
}

// AddSchedule creates a dated instance while the stage is open.
func (e *Engine) AddSchedule(_ context.Context, in *Case, actor Actor, stage, date, label string) (*Case, *Schedule, error) {
	if _, _, err := e.scheduleStage(in, actor, stage); err != nil {
		return nil, nil, err
	}
	when, err := time.Parse(time.RFC3339, date)
	if err != nil {
		return nil, nil, badState("scheduled_date must be an RFC 3339 timestamp")
	}
	c := in.Clone()
	now := e.now()
	item := Schedule{ID: e.newID("schedule"), Date: when.UTC(), Label: label, CreatedBy: actor.ID, CreatedAt: now}
	ss := c.Stages[stage]
	ss.Schedules = append(ss.Schedules, item)
	c.UpdatedAt = now
	c.History = append(c.History, Entry{At: now, Actor: actor.ID, Stage: stage, Action: "schedule.created"})
	return c, &ss.Schedules[len(ss.Schedules)-1], nil
}

// SubmitSchedule validates form data under the stage's normal field and role
// rules, then records it on the dated instance without overwriting case data.
func (e *Engine) SubmitSchedule(_ context.Context, in *Case, actor Actor, stage, scheduleID, form string, data map[string]any) (*Case, *ScheduleSubmission, error) {
	if _, _, err := e.scheduleStage(in, actor, stage); err != nil {
		return nil, nil, err
	}
	if e.C.forms[form] == nil {
		return nil, nil, fmt.Errorf("%w: form %q", ErrNotFound, form)
	}
	if len(data) == 0 {
		return nil, nil, badState("schedule submission data is required")
	}
	temp := in.Clone()
	delete(temp.Data, form)
	if _, err := e.saveInto(temp, actor, stage, map[string]any{form: data}); err != nil {
		return nil, nil, err
	}
	validated, ok := temp.Data[form].(map[string]any)
	if !ok || len(validated) == 0 {
		return nil, nil, forbidden("form %q is not editable in this stage", form)
	}
	for _, field := range e.C.forms[form].inputs {
		if e.required(form, field, e.Env(temp, actor)) && missingValue(field, validated[field.Name]) {
			return nil, nil, &ValidationError{Message: "schedule form is incomplete", Fields: []FieldError{{Path: form + "." + field.Name, Rule: "required", Message: "a value is required"}}}
		}
	}
	c := in.Clone()
	ss := c.Stages[stage]
	for i := range ss.Schedules {
		if ss.Schedules[i].ID != scheduleID {
			continue
		}
		now := e.now()
		submission := ScheduleSubmission{ID: e.newID("submission"), Form: form, Data: validated, By: actor.ID, At: now}
		ss.Schedules[i].Submissions = append(ss.Schedules[i].Submissions, submission)
		c.UpdatedAt = now
		c.History = append(c.History, Entry{At: now, Actor: actor.ID, Stage: stage, Action: "schedule.submitted"})
		return c, &ss.Schedules[i].Submissions[len(ss.Schedules[i].Submissions)-1], nil
	}
	return nil, nil, fmt.Errorf("%w: schedule %q", ErrNotFound, scheduleID)
}
