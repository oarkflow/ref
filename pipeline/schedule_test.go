package pipeline

import (
	"context"
	"testing"
)

func TestMultiDateSchedulesKeepValidatedSubmissionsSeparate(t *testing.T) {
	def := &Definition{Name: "activity", Forms: []Form{{Name: "log", Inputs: []Input{{Name: "activity_date", Kind: KindDate, Required: true}, {Name: "summary", Kind: KindText, Required: true}}}}, Stages: []Stage{{Name: "daily", Public: true, MultiDate: true, Page: &Page{Groups: []Group{{Name: "logs", Forms: []string{"log"}}}}}}}
	compiled, err := Compile(def)
	if err != nil {
		t.Fatal(err)
	}
	engine := NewEngine(compiled)
	actor := Actor{ID: "applicant-1"}
	ctx := context.Background()
	original, err := engine.Start(ctx, actor, StartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	current, schedule, err := engine.AddSchedule(ctx, original, actor, "daily", "2026-10-28T00:00:00Z", "Day one")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := engine.SubmitSchedule(ctx, current, actor, "daily", schedule.ID, "log", map[string]any{"summary": "missing date"}); err == nil {
		t.Fatal("accepted incomplete schedule submission")
	}
	next, submission, err := engine.SubmitSchedule(ctx, current, actor, "daily", schedule.ID, "log", map[string]any{"activity_date": "2026-10-28", "summary": "Field visit"})
	if err != nil {
		t.Fatal(err)
	}
	if submission.Form != "log" || submission.Data["summary"] != "Field visit" {
		t.Fatalf("submission: %+v", submission)
	}
	if _, present := next.Data["log"]; present {
		t.Fatal("dated submission overwrote the case form")
	}
	if len(original.Stages["daily"].Schedules) != 0 || len(current.Stages["daily"].Schedules[0].Submissions) != 0 {
		t.Fatal("operation mutated its input case")
	}
	found, err := engine.Schedules(next, actor, "daily")
	if err != nil || len(found) != 1 || len(found[0].Submissions) != 1 {
		t.Fatalf("schedules: %+v %v", found, err)
	}
}
