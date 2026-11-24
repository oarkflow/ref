package pipeline

import (
	"context"
	"testing"
)

// Data is started once: creating it starts its first step, and a step it comes
// back to (returned for correction) is still started.
func startOnceDef() *Definition {
	return &Definition{
		Name:   "once",
		Inputs: []Input{{Name: "a", Kind: KindText}},
		Forms:  []Form{{Name: "f", Fields: []string{"a"}}},
		Stages: []Stage{
			{Name: "intake", Roles: []string{"nurse"}, RequireStart: true, Page: &Page{Groups: []Group{{Name: "g", Forms: []string{"f"}}}},
				Actions: []ActionSpec{{Name: "send", Outcome: OutcomeAdvance}}},
			{Name: "coding", Roles: []string{"coder"}, RequireStart: true, Page: &Page{Groups: []Group{{Name: "g", Forms: []string{"f"}}}},
				Actions: []ActionSpec{{Name: "back", Outcome: OutcomeReturn, ReturnTo: "intake"}, {Name: "done", Outcome: OutcomeAdvance}}},
			{Name: "after", Roles: []string{"coder"}, Actions: []ActionSpec{{Name: "ok", Outcome: OutcomeAdvance}}},
		},
	}
}

func TestCreatingDataStartsItsFirstStepOnce(t *testing.T) {
	ctx := context.Background()
	compiled, err := Compile(startOnceDef())
	if err != nil {
		t.Fatal(err)
	}
	e := NewEngine(compiled)
	e.Eval = bclEval{}
	nurse, coder := Actor{ID: "n1", Roles: []string{"nurse"}}, Actor{ID: "c1", Roles: []string{"coder"}}

	c, err := e.Start(ctx, nurse, StartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if c.Stages["intake"].StartedAt == nil || c.Stages["intake"].StartedBy != "n1" {
		t.Fatalf("the person who created the data has started it: %+v", c.Stages["intake"])
	}
	// No second Start: they can enter data straight away.
	if c, err = e.Save(ctx, c, nurse, "intake", map[string]any{"f": map[string]any{"a": "x"}}); err != nil {
		t.Fatalf("creating it was the start: %v", err)
	}
	if c, err = e.Act(ctx, c, nurse, "intake", "send", ActInput{}); err != nil || c.Stage != "coding" {
		t.Fatalf("send: %v %v", err, c)
	}

	// The next step is different data for a different person: it still waits for Start.
	if c.Stages["coding"].StartedAt != nil {
		t.Fatal("the coding step must wait for someone to press Start")
	}
	if _, err := e.Save(ctx, c, coder, "coding", map[string]any{"f": map[string]any{"a": "y"}}); err == nil {
		t.Fatal("editing before Start is refused")
	}
	if c, err = e.StartStage(ctx, c, coder, "coding"); err != nil {
		t.Fatal(err)
	}

	// Returned for correction: the nurse fixes it without pressing Start again.
	if c, err = e.Act(ctx, c, coder, "coding", "back", ActInput{Comment: "fix it"}); err != nil || c.Stage != "intake" {
		t.Fatalf("return: %v %v", err, c)
	}
	if c.Stages["intake"].StartedAt == nil {
		t.Fatal("data that was started stays started when it is returned")
	}
	if c, err = e.Save(ctx, c, nurse, "intake", map[string]any{"f": map[string]any{"a": "fixed"}}); err != nil {
		t.Fatalf("no second Start after a return: %v", err)
	}
	if c, err = e.Act(ctx, c, nurse, "intake", "send", ActInput{}); err != nil || c.Stage != "coding" {
		t.Fatalf("resend: %v %v", err, c)
	}
	if c.Stages["coding"].StartedAt == nil {
		t.Fatal("coding was started before and stays started when the data comes back to it")
	}
}

func TestLimitsAreValidated(t *testing.T) {
	d := startOnceDef()
	d.Limits = &Limits{PerUser: -1}
	if _, err := Compile(d); err == nil {
		t.Fatal("a negative limit is refused")
	}
	d.Limits = &Limits{PerUser: 3, Count: "sometimes"}
	if _, err := Compile(d); err == nil {
		t.Fatal("count must be all or open")
	}
	d.Limits = &Limits{PerUser: 3, Count: "open"}
	if _, err := Compile(d); err != nil {
		t.Fatal(err)
	}
}
