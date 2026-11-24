package pipeline

import (
	"context"
	"slices"
	"testing"
)

func controlsDef() *Definition {
	return &Definition{
		Name:         "controls",
		AllowRFI:     false,
		SuspendRoles: []string{"officer", "lead"},
		ResumeRoles:  []string{"lead"},
		InfoRoles:    []string{"officer"},
		Inputs:       []Input{{Name: "name", Kind: KindText, Required: true}, {Name: "ssn", Kind: KindText}, {Name: "note", Kind: KindText}},
		Forms:        []Form{{Name: "who", Fields: []string{"name", "ssn", "note"}}},
		Stages: []Stage{
			{Name: "intake", Public: true, Page: &Page{Groups: []Group{{Name: "g", Forms: []string{"who"}}}},
				Summary: &SummarySpec{Title: "Check your answers", Fields: []string{"who.name"}, Button: "Send it"},
				Actions: []ActionSpec{{Name: "submit", Outcome: OutcomeAdvance}}},
			{Name: "review", Roles: []string{"officer", "lead"}, Actions: []ActionSpec{{Name: "ok", Outcome: OutcomeAdvance}}},
			{Name: "other", Roles: []string{"officer"}, SuspendRoles: []string{"auditor"}, InfoRoles: []string{"auditor"},
				Actions: []ActionSpec{{Name: "ok", Outcome: OutcomeAdvance}}},
		},
	}
}

func TestStageControlsDefaultsAndOverrides(t *testing.T) {
	c, err := Compile(controlsDef())
	if err != nil {
		t.Fatal(err)
	}
	if !c.Def.AllowRFI {
		t.Fatal("info_roles must switch information requests on")
	}
	review, _ := c.Stage("review")
	if !slices.Equal(review.SuspendRoles, []string{"officer", "lead"}) || !slices.Equal(review.ResumeRoles, []string{"lead"}) {
		t.Fatalf("stage inherits the workflow defaults: %v %v", review.SuspendRoles, review.ResumeRoles)
	}
	other, _ := c.Stage("other")
	if !slices.Equal(other.SuspendRoles, []string{"auditor"}) || !slices.Equal(other.InfoRoles, []string{"auditor"}) {
		t.Fatalf("a stage's own roles win: %v %v", other.SuspendRoles, other.InfoRoles)
	}
}

func TestSuspendResumeAndInformationRolesAreSeparate(t *testing.T) {
	ctx := context.Background()
	c, _ := Compile(controlsDef())
	e := NewEngine(c)
	e.Eval = bclEval{}
	cs, _ := e.Start(ctx, applicant, StartOptions{})
	cs, err := e.Act(ctx, cs, applicant, "intake", "submit", ActInput{Data: map[string]any{"who": map[string]any{"name": "Asha"}}, ConfirmToken: ""})
	if err == nil {
		t.Fatal("a stage with a summary must need the review step first")
	}
	cs, _ = e.Start(ctx, applicant, StartOptions{})
	in := ActInput{Data: map[string]any{"who": map[string]any{"name": "Asha", "ssn": "123-45-6789", "note": "x"}}}
	rv, err := e.Preview(ctx, cs, applicant, "intake", "submit", in)
	if err != nil {
		t.Fatal(err)
	}
	if rv.Title != "Check your answers" || rv.Button != "Send it" || len(rv.Fields) != 1 || rv.Fields[0].Path != "who.name" {
		t.Fatalf("summary lists only the configured fields: %+v", rv)
	}
	in.ConfirmToken = rv.Token
	if cs, err = e.Act(ctx, cs, applicant, "intake", "submit", in); err != nil || cs.Stage != "review" {
		t.Fatalf("commit after summary: %v %v", err, cs)
	}

	officer, lead := Actor{ID: "o1", Roles: []string{"officer"}}, Actor{ID: "l1", Roles: []string{"lead"}}
	ops := func(a Actor) []string { v, _ := e.View(cs, a, "review"); return v.Work.Operations }
	if o := ops(officer); !slices.Contains(o, "suspend") || slices.Contains(o, "resume") || !slices.Contains(o, "request_information") {
		t.Fatalf("officer ops %v", o)
	}
	held, err := e.Suspend(ctx, cs, officer, "review", "waiting for a letter", nil)
	if err != nil {
		t.Fatal(err)
	}
	cs = held
	if _, err := e.Resume(ctx, cs, officer, "review", "back"); err == nil {
		t.Fatal("an officer may not resume: only resume_roles may")
	}
	if o := ops(lead); !slices.Contains(o, "resume") {
		t.Fatalf("lead ops %v", o)
	}
	if cs, err = e.Resume(ctx, cs, lead, "review", "got it"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.RequestInformation(ctx, cs, lead, "need more"); err == nil {
		t.Fatal("lead is not in info_roles")
	}
	asked, item, err := e.RequestInformation(ctx, cs, officer, "need your ID")
	if err != nil || item == nil || asked.Status != CaseRequiresInformation {
		t.Fatalf("officer may request information: %v", err)
	}
	if v, _ := e.View(asked, officer, "review"); v.Information == nil || v.Information.Reason != "need your ID" {
		t.Fatalf("the view carries the pending request: %+v", v.Information)
	}
}

func TestSummaryRejectsUnknownGroup(t *testing.T) {
	d := controlsDef()
	d.Stages[0].Summary.Groups = []string{"nope"}
	if _, err := Compile(d); err == nil {
		t.Fatal("a summary naming an unknown group must not compile")
	}
}
