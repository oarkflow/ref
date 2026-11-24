package pipeline

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

var (
	clerk1 = Actor{ID: "clerk1", Roles: []string{"clerk"}}
	clerk2 = Actor{ID: "clerk2", Roles: []string{"clerk"}}
	admin  = Actor{ID: "boss", Roles: []string{"admin"}}
)

func standDef(onBreach string) *Definition {
	return &Definition{
		Name:       "stand",
		AdminRoles: []string{"admin"},
		Workers: []Worker{
			{ID: "clerk1", Roles: []string{"clerk"}}, {ID: "clerk2", Roles: []string{"clerk"}}, {ID: "boss", Roles: []string{"admin"}},
		},
		Inputs: []Input{{Name: "a", Kind: KindText}, {Name: "b", Kind: KindText}},
		Forms:  []Form{{Name: "f", Fields: []string{"a", "b"}}},
		Stages: []Stage{
			{Name: "intake", Public: true, Page: &Page{Groups: []Group{{Name: "g", Forms: []string{"f"}}}}, Actions: []ActionSpec{{Name: "go", Outcome: OutcomeAdvance}}},
			{Name: "work", Roles: []string{"clerk"}, Claimable: true, RequireStart: true,
				Page:    &Page{Groups: []Group{{Name: "g", Forms: []string{"f"}}}},
				Actions: []ActionSpec{{Name: "done", Outcome: OutcomeAdvance}},
				Nodes:   []Node{{Name: "check", Kind: NodeTask, Title: "Check", Optional: true}},
				SLA:     &SLA{Duration: "2h", OnBreach: onBreach, ExtendBy: "1h", ReassignRoles: []string{"clerk"}, Notify: []string{"owner"}}},
			{Name: "after", Roles: []string{"clerk"}, Actions: []ActionSpec{{Name: "ok", Outcome: OutcomeAdvance}}},
		},
	}
}

// standCase opens a case on the "work" stage at t0.
func standCase(t *testing.T, onBreach string) (*Engine, *Case, *clock) {
	t.Helper()
	clk := &clock{}
	clk.set("2026-03-04T05:00:00Z")
	c, err := Compile(standDef(onBreach))
	if err != nil {
		t.Fatal(err)
	}
	e := NewEngine(c)
	e.Eval = bclEval{}
	e.Now = clk.now
	ctx := context.Background()
	cs, err := e.Start(ctx, applicant, StartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	cs, err = e.Act(ctx, cs, applicant, "intake", "go", ActInput{Data: map[string]any{"f": map[string]any{"a": "original"}}})
	if err != nil || cs.Stage != "work" {
		t.Fatalf("to work: %v %v", err, cs)
	}
	return e, cs, clk
}

func TestStageMustBeStartedAndLogsWhoAndHowLong(t *testing.T) {
	ctx := context.Background()
	e, c, clk := standCase(t, "notify")
	if _, err := e.Save(ctx, c, clerk1, "work", map[string]any{"f": map[string]any{"b": "x"}}); err == nil || !strings.Contains(err.Error(), "start stage") {
		t.Fatalf("editing before Start must be refused: %v", err)
	}
	v, _ := e.View(c, clerk1, "work")
	if v.CanEdit || len(v.Actions) != 0 || !slices.Contains(v.Work.Operations, "start") {
		t.Fatalf("an unstarted stage is read-only with a start operation: edit=%v actions=%v ops=%v", v.CanEdit, v.Actions, v.Work.Operations)
	}
	if _, err := e.StartStage(ctx, c, applicant, "work"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("only people who may work the stage can start it: %v", err)
	}
	clk.add(30 * time.Minute) // 30 minutes waiting in the queue
	c, err := e.StartStage(ctx, c, clerk1, "work")
	if err != nil {
		t.Fatal(err)
	}
	if ss := c.Stages["work"]; ss.StartedBy != "clerk1" || ss.Assignee != "" && ss.Assignee != "clerk1" {
		t.Fatalf("start state: %+v", ss)
	}
	if _, err := e.StartStage(ctx, c, clerk2, "work"); err == nil {
		t.Fatal("a started stage cannot be started twice")
	}
	clk.add(45 * time.Minute) // 45 minutes of work
	if c, err = e.Save(ctx, c, clerk1, "work", map[string]any{"f": map[string]any{"b": "x"}}); err != nil {
		t.Fatal(err)
	}
	if c, err = e.Act(ctx, c, clerk1, "work", "done", ActInput{}); err != nil {
		t.Fatal(err)
	}
	o, err := e.Overview(c, clerk1)
	if err != nil {
		t.Fatal(err)
	}
	var work LogEntry
	for _, l := range o.Log {
		if l.Stage == "work" {
			work = l
		}
	}
	if work.StartedBy != "clerk1" || work.WaitSeconds != 1800 || work.WorkSeconds != 2700 || work.TotalSeconds != 4500 || work.Outcome == "" {
		t.Fatalf("log of the stage: %+v", work)
	}
	anon, _ := e.Overview(c, applicant)
	for _, l := range anon.Log {
		if l.StartedBy != "" || l.Assignee != "" {
			t.Fatalf("people are not shown to the applicant: %+v", l)
		}
	}
	if len(o.Fields) == 0 || o.Fields[0].Stage == "" {
		t.Fatalf("the overview summarises the data: %+v", o.Fields)
	}
}

func TestMissedSLARecordsWhyAndNotifies(t *testing.T) {
	ctx := context.Background()
	e, c, clk := standCase(t, "notify")
	c, _ = e.StartStage(ctx, c, clerk1, "work")
	clk.add(3 * time.Hour)
	c, res, err := e.Sweep(ctx, c)
	if err != nil || !res.Changed {
		t.Fatalf("sweep: %v %+v", err, res)
	}
	b := c.Stages["work"].SLA.Breach
	if b == nil || b.Cause != "not_completed" || b.StartedBy != "clerk1" || !strings.Contains(b.Reason, "clerk1 started the stage") || !strings.Contains(b.Reason, "overdue") {
		t.Fatalf("breach info: %+v", b)
	}
	if len(c.Breaches) != 1 || c.Breaches[0].ResolvedAt != nil {
		t.Fatalf("an open breach record: %+v", c.Breaches)
	}
	var detail map[string]any
	for _, ev := range c.Events() {
		if ev.Name == "sla.breached" {
			detail = ev.Detail
		}
	}
	notify, _ := detail["notify"].([]string)
	if detail["reason"] == "" || detail["cause"] != "not_completed" || !slices.Contains(notify, "owner") || !slices.Contains(notify, "role:admin") {
		t.Fatalf("the event explains the breach and names who to tell: %v", detail)
	}

	// Nobody picked it up.
	e2, c2, clk2 := standCase(t, "notify")
	clk2.add(3 * time.Hour)
	c2, _, _ = e2.Sweep(ctx, c2)
	if got := c2.Stages["work"].SLA.Breach.Cause; got != "not_picked_up" {
		t.Fatalf("cause: %s", got)
	}
}

func TestAdminResetRestoresTheStageAsItWasOnEntry(t *testing.T) {
	ctx := context.Background()
	e, c, clk := standCase(t, "notify")
	c, _ = e.StartStage(ctx, c, clerk1, "work")
	c, _ = e.Save(ctx, c, clerk1, "work", map[string]any{"f": map[string]any{"a": "changed", "b": "junk"}})
	clk.add(3 * time.Hour)
	c, _, _ = e.Sweep(ctx, c)

	if _, err := e.ResetStage(ctx, c, clerk1, "work", "no"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("clerks may not reset: %v", err)
	}
	v, _ := e.View(c, admin, "work")
	if !slices.Contains(v.Work.Operations, "reset") || !slices.Contains(v.Work.Operations, "extend") || !slices.Contains(v.Work.Operations, "waive") || !slices.Contains(v.Work.Operations, "assign") {
		t.Fatalf("admin operations: %v", v.Work.Operations)
	}
	c, err := e.ResetStage(ctx, c, admin, "work", "clerk went silent")
	if err != nil {
		t.Fatal(err)
	}
	ss := c.Stages["work"]
	if got, _ := c.Get("f.a"); got != "original" {
		t.Fatalf("data restored: %v", got)
	}
	if got, _ := c.Get("f.b"); got != nil {
		t.Fatalf("edits made in the stage are gone: %v", got)
	}
	if ss.StartedBy != "" || ss.StartedAt != nil || ss.Assignee != "" || ss.Resets != 1 || ss.SLA.Status != SLAOnTrack || ss.SLA.Breach != nil || !ss.SLA.DueAt.After(clk.now()) {
		t.Fatalf("stage state after reset: %+v sla=%+v", ss, ss.SLA)
	}
	if rec := c.Breaches[0]; rec.Resolution != "reset" || rec.ResolvedBy != "boss" || rec.Comment != "clerk went silent" {
		t.Fatalf("breach record: %+v", rec)
	}
	last := c.Timeline[len(c.Timeline)-1]
	if last.Stage != "work" || last.LeftAt != nil || last.StartedAt != nil || c.Timeline[len(c.Timeline)-2].Outcome != "reset" {
		t.Fatalf("timeline keeps the reset visit and opens a fresh one: %+v", c.Timeline)
	}
	if !strings.Contains(eventNames(c), "stage.reset") || !strings.Contains(eventNames(c), "sla.resolved") {
		t.Fatalf("events: %s", eventNames(c))
	}
	if _, err := e.Save(ctx, c, clerk2, "work", map[string]any{"f": map[string]any{"b": "z"}}); err == nil {
		t.Fatal("after a reset the stage must be started again")
	}
	// Check results start fresh and the stage works again.
	if c, err = e.StartStage(ctx, c, clerk2, "work"); err != nil {
		t.Fatal(err)
	}
	if c, err = e.NodeAct(ctx, c, clerk2, "work", "check", "complete", NodeInput{}); err != nil || c.Stages["work"].Nodes["check"].Status != NodePassed {
		t.Fatalf("a node can be worked after a reset: %v", err)
	}
}

func TestAdminExtendWaiveAndReassign(t *testing.T) {
	ctx := context.Background()
	e, c, clk := standCase(t, "notify")
	c, _ = e.StartStage(ctx, c, clerk1, "work")
	clk.add(3 * time.Hour)
	c, _, _ = e.Sweep(ctx, c)

	ext, err := e.ExtendSLA(ctx, c, admin, "work", "", "more time")
	if err != nil {
		t.Fatal(err)
	}
	if sla := ext.Stages["work"].SLA; sla.Status != SLAOnTrack || !sla.DueAt.After(clk.now()) || ext.Breaches[0].Resolution != "extend" {
		t.Fatalf("extend: %+v %+v", sla, ext.Breaches[0])
	}
	if _, err := e.ExtendSLA(ctx, c, clerk1, "work", "1h", ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("extend is for admins: %v", err)
	}

	if _, err := e.WaiveSLA(ctx, c, admin, "work", " "); err == nil {
		t.Fatal("waiving needs a reason")
	}
	w, err := e.WaiveSLA(ctx, c, admin, "work", "holiday backlog")
	if err != nil || w.Breaches[0].Resolution != "waive" || w.Stages["work"].SLA.Status != SLABreached {
		t.Fatalf("waive keeps the miss on record: %v %+v", err, w.Breaches)
	}

	as, err := e.Assign(ctx, c, admin, "work", "clerk2", "needs a fresh pair of hands")
	if err != nil || as.Stages["work"].Assignee != "clerk2" || as.Breaches[0].Resolution != "reassign" {
		t.Fatalf("reassign: %v %+v", err, as.Breaches)
	}
}

func TestBreachPolicyResetAndReassignRunAutomatically(t *testing.T) {
	ctx := context.Background()
	e, c, clk := standCase(t, "reset")
	c, _ = e.StartStage(ctx, c, clerk1, "work")
	c, _ = e.Save(ctx, c, clerk1, "work", map[string]any{"f": map[string]any{"b": "junk"}})
	clk.add(3 * time.Hour)
	c, _, _ = e.Sweep(ctx, c)
	ss := c.Stages["work"]
	if got, _ := c.Get("f.b"); got != nil || ss.StartedBy != "" || ss.Resets != 1 || ss.SLA.Status != SLAOnTrack || c.Breaches[0].Resolution != "reset" || c.Breaches[0].ResolvedBy != "system" {
		t.Fatalf("automatic reset: %+v %+v", ss, c.Breaches)
	}

	e, c, clk = standCase(t, "reassign")
	c, _ = e.StartStage(ctx, c, clerk1, "work")
	clk.add(3 * time.Hour)
	c, _, _ = e.Sweep(ctx, c)
	if a := c.Stages["work"].Assignee; a == "clerk1" || c.Breaches[0].Resolution != "reassign" {
		t.Fatalf("automatic reassign: assignee=%q %+v", a, c.Breaches)
	}

	e, c, clk = standCase(t, "extend")
	clk.add(3 * time.Hour)
	c, _, _ = e.Sweep(ctx, c)
	if sla := c.Stages["work"].SLA; sla.Status != SLAOnTrack || c.Breaches[0].Resolution != "extend" {
		t.Fatalf("automatic extend: %+v", sla)
	}
}
