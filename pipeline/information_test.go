package pipeline

import (
	"context"
	"testing"
)

func TestInformationRequestPausesAndResumesStage(t *testing.T) {
	compiled, err := Compile(&Definition{Name: "request", AllowRFI: true, Stages: []Stage{
		{Name: "application", Public: true, Actions: []ActionSpec{{Name: "submit", Outcome: OutcomeAdvance}}},
		{Name: "review", Roles: []string{"officer"}, Actions: []ActionSpec{{Name: "complete", Outcome: OutcomeAdvance}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	e := NewEngine(compiled)
	ctx := context.Background()
	applicant := Actor{ID: "person"}
	officer := Actor{ID: "staff", Roles: []string{"officer"}}
	c, err := e.Start(ctx, applicant, StartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	c, err = e.Act(ctx, c, applicant, "application", "submit", ActInput{})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.RequestInformation(ctx, c, applicant, "more detail"); err == nil {
		t.Fatal("applicant requested information")
	}
	pending, item, err := e.RequestInformation(ctx, c, officer, "Clarify the date")
	if err != nil {
		t.Fatal(err)
	}
	if pending.Status != CaseRequiresInformation || item.Reason != "Clarify the date" {
		t.Fatalf("pending: %+v", pending)
	}
	if _, err := e.Act(ctx, pending, officer, "review", "complete", ActInput{}); err == nil {
		t.Fatal("advanced while RFI pending")
	}
	if _, _, err := e.SubmitInformation(ctx, pending, officer, map[string]any{"date": "2026-10-27"}); err == nil {
		t.Fatal("officer answered applicant RFI")
	}
	answered, response, err := e.SubmitInformation(ctx, pending, applicant, map[string]any{"date": "2026-10-27"})
	if err != nil {
		t.Fatal(err)
	}
	if answered.Status != CaseInProgress || answered.Stage != "review" || response.Response["date"] != "2026-10-27" {
		t.Fatalf("answer: %+v", answered)
	}
	if _, err := e.Act(ctx, answered, officer, "review", "complete", ActInput{}); err != nil {
		t.Fatalf("resume: %v", err)
	}
}
