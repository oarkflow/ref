package pipeline

import (
	"context"
	"slices"
	"testing"
)

func resumeToDef() *Definition {
	return &Definition{
		Name:         "rt",
		SuspendRoles: []string{"lead"},
		ResumeRoles:  []string{"lead"},
		Workers: []Worker{
			{ID: "n1", Roles: []string{"nurse"}}, {ID: "n2", Roles: []string{"nurse"}},
			{ID: "c1", Roles: []string{"coder"}}, {ID: "c2", Roles: []string{"coder"}},
		},
		Inputs: []Input{{Name: "a", Kind: KindText}},
		Forms:  []Form{{Name: "f", Fields: []string{"a"}}},
		Stages: []Stage{
			{Name: "intake", Roles: []string{"nurse"}, Claimable: true, RequireStart: true, Page: &Page{Groups: []Group{{Name: "g", Forms: []string{"f"}}}},
				SLA:     &SLA{Duration: "2h"},
				Actions: []ActionSpec{{Name: "send", Outcome: OutcomeAdvance}}},
			{Name: "coding", Roles: []string{"coder"}, Claimable: true, RequireStart: true, Page: &Page{Groups: []Group{{Name: "g", Forms: []string{"f"}}}},
				SLA:     &SLA{Duration: "2h"},
				Actions: []ActionSpec{{Name: "done", Outcome: OutcomeAdvance}}},
			{Name: "end", Roles: []string{"coder"}, Actions: []ActionSpec{{Name: "ok", Outcome: OutcomeAdvance}}},
		},
	}
}

// codingOnHold takes a case through intake into a held coding stage.
func codingOnHold(t *testing.T) (*Engine, *Case) {
	t.Helper()
	ctx := context.Background()
	compiled, err := Compile(resumeToDef())
	if err != nil {
		t.Fatal(err)
	}
	e := NewEngine(compiled)
	e.Eval = bclEval{}
	nurse, coder, lead := Actor{ID: "n1", Roles: []string{"nurse"}}, Actor{ID: "c1", Roles: []string{"coder"}}, Actor{ID: "boss", Roles: []string{"lead"}}
	c, err := e.Start(ctx, nurse, StartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if c, err = e.Save(ctx, c, nurse, "intake", map[string]any{"f": map[string]any{"a": "entered by the nurse"}}); err != nil {
		t.Fatal(err)
	}
	if c, err = e.Act(ctx, c, nurse, "intake", "send", ActInput{}); err != nil || c.Stage != "coding" {
		t.Fatalf("to coding: %v %v", err, c)
	}
	if c, err = e.StartStage(ctx, c, coder, "coding"); err != nil {
		t.Fatal(err)
	}
	if c, err = e.Suspend(ctx, c, lead, "coding", "waiting for the chart", nil); err != nil {
		t.Fatal(err)
	}
	return e, c
}

func TestResumeTargetsAreOfferedToTheReleaser(t *testing.T) {
	e, c := codingOnHold(t)
	v, err := e.View(c, Actor{ID: "boss", Roles: []string{"lead"}}, "coding")
	if err != nil || v.Work == nil {
		t.Fatalf("view: %v", err)
	}
	var got []string
	for _, tg := range v.Work.ResumeTargets {
		got = append(got, tg.To+":"+tg.Stage)
	}
	if !slices.Equal(got, []string{"same:coding", "previous:intake"}) {
		t.Fatalf("targets: %v", got)
	}
}

func TestResumeInPlaceWithAndWithoutAPerson(t *testing.T) {
	ctx := context.Background()
	lead := Actor{ID: "boss", Roles: []string{"lead"}}
	e, c := codingOnHold(t)
	c2, err := e.ResumeTo(ctx, c, lead, "coding", "chart arrived", ResumeOptions{})
	if err != nil || c2.Stage != "coding" || c2.Stages["coding"].Suspended != nil || c2.Stages["coding"].StartedBy != "c1" || c2.Stages["coding"].Assignee != "c1" {
		t.Fatalf("same stage, nobody named, keeps the holder and the start: %v %+v", err, c2.Stages["coding"])
	}
	c3, err := e.ResumeTo(ctx, c, lead, "coding", "give it to c2", ResumeOptions{To: "same", Assignee: "c2"})
	if err != nil || c3.Stages["coding"].Assignee != "c2" || c3.Stages["coding"].StartedBy != "c1" {
		t.Fatalf("same stage with a person: %v %+v", err, c3.Stages["coding"])
	}
	if _, err := e.ResumeTo(ctx, c, lead, "coding", "", ResumeOptions{Assignee: "n1"}); err == nil {
		t.Fatal("a person who cannot work this stage cannot be named")
	}
}

func TestReleaseToPreviousStageStartsItAfresh(t *testing.T) {
	ctx := context.Background()
	lead := Actor{ID: "boss", Roles: []string{"lead"}}
	e, c := codingOnHold(t)
	was := c.Stages["intake"].Visits

	// Without a person: back in the previous stage's queue, nothing carried over but the data.
	c2, err := e.ResumeTo(ctx, c, lead, "coding", "wrong code set", ResumeOptions{To: "previous"})
	if err != nil {
		t.Fatal(err)
	}
	in, co := c2.Stages["intake"], c2.Stages["coding"]
	if c2.Stage != "intake" || in.Status != StageActive || in.Visits != was+1 {
		t.Fatalf("it is back at intake as a new visit: stage %s %+v", c2.Stage, in)
	}
	if in.StartedAt != nil || in.StartedBy != "" || in.Assignee != "" || in.SLA == nil || in.SLA.Status != SLAOnTrack || in.Flags != nil {
		t.Fatalf("the stage starts from nothing: %+v", in)
	}
	if co.Suspended != nil || co.StartedAt != nil || co.Assignee != "" || co.Status != StagePending {
		t.Fatalf("coding lets go completely: %+v", co)
	}
	if got, _ := c2.Get("f.a"); got != "entered by the nurse" {
		t.Fatalf("the case data is kept: %v", got)
	}
	nurse := Actor{ID: "n1", Roles: []string{"nurse"}}
	if _, err := e.Save(ctx, c2, nurse, "intake", map[string]any{"f": map[string]any{"a": "x"}}); err == nil {
		t.Fatal("a new start is required: nothing can be edited before Start")
	}
	last := c2.History[len(c2.History)-1]
	if last.Action != "resume" || last.From != "coding" || last.To != "intake" || last.Actor != "boss" {
		t.Fatalf("the history says what happened: %+v", last)
	}
	// The visit at coding is closed as released, with the time it was held.
	closed := false
	for _, v := range c2.Timeline {
		if v.Stage == "coding" && v.Outcome == "released" && v.LeftAt != nil {
			closed = true
		}
	}
	if !closed {
		t.Fatalf("the coding visit must end as released: %+v", c2.Timeline)
	}

	// With a person: they hold the previous stage, still needing to Start it.
	c3, err := e.ResumeTo(ctx, c, lead, "coding", "back to the nurse", ResumeOptions{To: "previous", Assignee: "n2"})
	if err != nil || c3.Stages["intake"].Assignee != "n2" || c3.Stages["intake"].StartedAt != nil {
		t.Fatalf("previous with a person: %v %+v", err, c3.Stages["intake"])
	}
	if _, err := e.ResumeTo(ctx, c, lead, "coding", "", ResumeOptions{To: "previous", Assignee: "c1"}); err == nil {
		t.Fatal("the person must be able to work the previous stage")
	}
	if _, err := e.ResumeTo(ctx, c, lead, "coding", "", ResumeOptions{To: "nowhere"}); err == nil {
		t.Fatal("unknown targets are refused")
	}
	if _, err := e.ResumeTo(ctx, c, Actor{ID: "c1", Roles: []string{"coder"}}, "coding", "", ResumeOptions{To: "previous"}); err == nil {
		t.Fatal("only a resume role may release it")
	}
}

func TestNoPreviousStageToReleaseTo(t *testing.T) {
	ctx := context.Background()
	compiled, _ := Compile(resumeToDef())
	e := NewEngine(compiled)
	e.Eval = bclEval{}
	nurse, lead := Actor{ID: "n1", Roles: []string{"nurse"}}, Actor{ID: "boss", Roles: []string{"lead"}}
	c, _ := e.Start(ctx, nurse, StartOptions{})
	c, err := e.Suspend(ctx, c, lead, "intake", "hold", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.ResumeTo(ctx, c, lead, "intake", "", ResumeOptions{To: "previous"}); err == nil {
		t.Fatal("the first stage has no earlier stage")
	}
}
