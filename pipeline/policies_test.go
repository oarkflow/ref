package pipeline

import (
	"context"
	"strings"
	"testing"
	"time"
)

// A workflow: intake (nurse) -> coding (coder, SLA 2h) -> end. Policies are set per test.
func policyEngine(t *testing.T, tweak func(*Definition)) (*Engine, *Case, *clock) {
	t.Helper()
	d := resumeToDef()
	d.AllowRFI = true
	d.InfoRoles = []string{"coder", "lead"}
	if tweak != nil {
		tweak(d)
	}
	compiled, err := Compile(d)
	if err != nil {
		t.Fatal(err)
	}
	clk := &clock{}
	clk.set("2026-03-04T05:00:00Z")
	e := NewEngine(compiled)
	e.Eval = bclEval{}
	e.Now = clk.now
	ctx := context.Background()
	nurse, coder := Actor{ID: "n1", Roles: []string{"nurse"}}, Actor{ID: "c1", Roles: []string{"coder"}}
	c, err := e.Start(ctx, nurse, StartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if c, err = e.Save(ctx, c, nurse, "intake", map[string]any{"f": map[string]any{"a": "x"}}); err != nil {
		t.Fatal(err)
	}
	if c, err = e.Act(ctx, c, nurse, "intake", "send", ActInput{}); err != nil || c.Stage != "coding" {
		t.Fatalf("to coding: %v", err)
	}
	if c, err = e.StartStage(ctx, c, coder, "coding"); err != nil {
		t.Fatal(err)
	}
	return e, c, clk
}

var (
	pNurse  = Actor{ID: "n1", Roles: []string{"nurse"}}
	pNurse2 = Actor{ID: "n2", Roles: []string{"nurse"}}
	pCoder  = Actor{ID: "c1", Roles: []string{"coder"}}
	pLead   = Actor{ID: "boss", Roles: []string{"lead"}}
)

func TestInformationRequestStopsTheClockAndAnswerRestartsIt(t *testing.T) {
	ctx := context.Background()
	e, c, clk := policyEngine(t, nil)
	due0 := c.Stages["coding"].SLA.DueAt
	clk.add(30 * time.Minute)
	c, item, err := e.RequestInformation(ctx, c, pCoder, "which chart?")
	if err != nil || c.Status != CaseRequiresInformation || c.Stages["coding"].SLA.Status != SLAPaused || item.PausedAt == nil {
		t.Fatalf("a question stops the clock: %v %+v", err, c.Stages["coding"].SLA)
	}
	clk.add(3 * time.Hour) // far past the original deadline: nothing breaches while the question is open
	c2, res, err := e.Sweep(ctx, c)
	if err != nil || c2.Stages["coding"].SLA.Status == SLABreached || res.Changed {
		t.Fatalf("no breach while waiting for an answer: %+v", c2.Stages["coding"].SLA)
	}
	c, _, err = e.SubmitInformation(ctx, c, pNurse, map[string]any{"answer": "chart 7"})
	if err != nil {
		t.Fatal(err)
	}
	sla := c.Stages["coding"].SLA
	if sla.Status == SLAPaused || !sla.DueAt.Equal(due0.Add(3*time.Hour)) {
		t.Fatalf("the deadline moves by the time spent waiting: was %v now %v (%s)", due0, sla.DueAt, sla.Status)
	}
}

func TestInformationSLARunKeepsTheClockGoing(t *testing.T) {
	e, c, _ := policyEngine(t, func(d *Definition) { d.Information = &InfoPolicy{SLA: "run"} })
	c, _, err := e.RequestInformation(context.Background(), c, pCoder, "which chart?")
	if err != nil || c.Stages["coding"].SLA.Status == SLAPaused {
		t.Fatalf("sla run leaves the clock alone: %v %+v", err, c.Stages["coding"].SLA)
	}
}

func TestInformationCanBeAskedOfThePreviousStageOrAUser(t *testing.T) {
	ctx := context.Background()
	e, c, _ := policyEngine(t, func(d *Definition) { d.Information = &InfoPolicy{To: []string{"creator", "previous", "user"}} })

	// Asked of the previous stage's role: any nurse may answer, the creator is no longer the only one.
	asked, item, err := e.RequestInformationTo(ctx, c, pCoder, "recheck the dates", InfoOptions{To: "previous"})
	if err != nil || item.To != "previous" || len(item.AnswerRoles) != 1 || item.AnswerRoles[0] != "nurse" {
		t.Fatalf("ask previous: %v %+v", err, item)
	}
	if !e.IsInfoAnswerer(asked, pNurse2) || e.IsInfoAnswerer(asked, pCoder) {
		t.Fatal("any nurse is asked; the coder is not")
	}
	if v, err := e.View(asked, pNurse2, ""); err != nil || !v.InformationCanAnswer {
		t.Fatalf("a nurse who did not create the case can open it and answer: %v", err)
	}
	if _, _, err := e.SubmitInformation(ctx, asked, pCoder, map[string]any{"a": "b"}); err == nil || !strings.Contains(err.Error(), "nurse") {
		t.Fatalf("the coder cannot answer a question put to nurses, and is told who can: %v", err)
	}
	if _, _, err := e.SubmitInformation(ctx, asked, pNurse2, map[string]any{"a": "b"}); err != nil {
		t.Fatalf("another nurse answers: %v", err)
	}

	// Asked of a named person.
	asked, _, err = e.RequestInformationTo(ctx, c, pCoder, "can you check?", InfoOptions{To: "user", Assignee: "n2"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.SubmitInformation(ctx, asked, pNurse, map[string]any{"a": "b"}); err == nil {
		t.Fatal("only the named person may answer (not even the creator)")
	}
	if _, _, err := e.SubmitInformation(ctx, asked, pNurse2, map[string]any{"a": "b"}); err != nil {
		t.Fatalf("the named person answers: %v", err)
	}
	if _, _, err := e.RequestInformationTo(ctx, c, pCoder, "x", InfoOptions{To: "user"}); err == nil {
		t.Fatal("a person must be named")
	}
}

func TestInformationTargetsFollowThePolicy(t *testing.T) {
	e, c, _ := policyEngine(t, nil) // default: only the creator may be asked
	if _, _, err := e.RequestInformationTo(context.Background(), c, pCoder, "x", InfoOptions{To: "previous"}); err == nil {
		t.Fatal("asking the previous stage is refused unless the policy allows it")
	}
	if _, _, err := Compile2(t, func(d *Definition) { d.Information = &InfoPolicy{To: []string{"anyone"}} }); err == nil {
		t.Fatal("unknown targets are refused when compiling")
	}
}

// Compile2 compiles the policy workflow with a tweak, for validation tests.
func Compile2(t *testing.T, tweak func(*Definition)) (*Compiled, struct{}, error) {
	d := resumeToDef()
	tweak(d)
	c, err := Compile(d)
	return c, struct{}{}, err
}

func TestInformationDueAndTimeout(t *testing.T) {
	ctx := context.Background()
	e, c, clk := policyEngine(t, func(d *Definition) { d.Information = &InfoPolicy{Due: "24h", OnTimeout: "withdraw"} })
	c, item, err := e.RequestInformation(ctx, c, pCoder, "which chart?")
	if err != nil || item.DueAt == nil || !item.DueAt.Equal(clk.now().Add(24*time.Hour)) {
		t.Fatalf("due is set: %v %+v", err, item)
	}
	clk.add(25 * time.Hour)
	c, res, err := e.Sweep(ctx, c)
	if err != nil || !res.Changed || c.Status != CaseWithdrawn {
		t.Fatalf("no answer in time withdraws the case: %v %v", err, c.Status)
	}
	var seen bool
	for _, h := range c.History {
		seen = seen || h.Action == "information.overdue"
	}
	if !seen {
		t.Fatal("the history says the answer was overdue")
	}

	// Default on_timeout only notes it, once.
	e, c, clk = policyEngine(t, func(d *Definition) { d.Information = &InfoPolicy{Due: "1h"} })
	c, _, _ = e.RequestInformation(ctx, c, pCoder, "q")
	clk.add(2 * time.Hour)
	c, res, _ = e.Sweep(ctx, c)
	if !res.Changed || c.Status != CaseRequiresInformation || !c.InformationRequests[0].Overdue {
		t.Fatalf("overdue is recorded and the case waits on: %v %+v", c.Status, c.InformationRequests[0])
	}
	if _, res, _ = e.Sweep(ctx, c); res.Changed {
		t.Fatal("overdue is noted only once")
	}
}

func TestReleasePolicyLimitsTargetsPersonAndRestoresData(t *testing.T) {
	ctx := context.Background()
	hold := func(e *Engine, c *Case) *Case {
		c, err := e.Suspend(ctx, c, pLead, "coding", "waiting", nil)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	// Only "same" allowed, a person required.
	e, c, _ := policyEngine(t, func(d *Definition) { d.Release = &ReleasePolicy{Targets: []string{"same"}, Assignee: "required"} })
	c = hold(e, c)
	if v, _ := e.View(c, pLead, "coding"); len(v.Work.ResumeTargets) != 1 || v.Work.ResumeTargets[0].To != "same" || v.Work.ResumeTargets[0].Assignee != "required" {
		t.Fatalf("only the allowed target is offered: %+v", v.Work.ResumeTargets)
	}
	if _, err := e.ResumeTo(ctx, c, pLead, "coding", "", ResumeOptions{To: "previous", Assignee: "n1"}); err == nil {
		t.Fatal("previous is not allowed here")
	}
	if _, err := e.ResumeTo(ctx, c, pLead, "coding", "", ResumeOptions{To: "same"}); err == nil {
		t.Fatal("a person is required")
	}
	if _, err := e.ResumeTo(ctx, c, pLead, "coding", "", ResumeOptions{To: "same", Assignee: "c2"}); err != nil {
		t.Fatal(err)
	}

	// No person allowed.
	e, c, _ = policyEngine(t, func(d *Definition) { d.Release = &ReleasePolicy{Assignee: "none"} })
	c = hold(e, c)
	if _, err := e.ResumeTo(ctx, c, pLead, "coding", "", ResumeOptions{To: "same", Assignee: "c2"}); err == nil {
		t.Fatal("naming a person is not allowed")
	}

	// restore_data puts the case data back to how intake found it (empty), which is the destructive choice.
	e, c, _ = policyEngine(t, func(d *Definition) { d.Release = &ReleasePolicy{RestoreData: true} })
	c = hold(e, c)
	if got, _ := c.Get("f.a"); got != "x" {
		t.Fatalf("setup: %v", got)
	}
	c, err := e.ResumeTo(ctx, c, pLead, "coding", "redo", ResumeOptions{To: "previous"})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := c.Get("f.a"); got != nil && got != "" {
		t.Fatalf("restore_data puts the data back: %v", got)
	}
}

func TestStartPolicy(t *testing.T) {
	ctx := context.Background()
	// on_create manual: even the creator must press Start.
	compiled, _ := Compile(func() *Definition { d := resumeToDef(); d.Start = &StartPolicy{OnCreate: "manual"}; return d }())
	e := NewEngine(compiled)
	e.Eval = bclEval{}
	c, err := e.Start(ctx, pNurse, StartOptions{})
	if err != nil || c.Stages["intake"].StartedAt != nil {
		t.Fatalf("manual start on create: %v %+v", err, c.Stages["intake"])
	}

	// on_return restart: a returned stage needs a new Start.
	e, c, _ = policyEngine(t, func(d *Definition) { d.Start = &StartPolicy{OnReturn: "restart"} })
	c, err = e.Act(ctx, c, pCoder, "coding", "done", ActInput{})
	_ = c
	e2, c2, _ := policyEngine(t, func(d *Definition) {
		d.Start = &StartPolicy{OnReturn: "restart"}
		d.Stages[1].Actions = append(d.Stages[1].Actions, ActionSpec{Name: "back", Outcome: OutcomeReturn, ReturnTo: "intake"})
	})
	c2, err = e2.Act(ctx, c2, pCoder, "coding", "back", ActInput{Comment: "fix"})
	if err != nil || c2.Stage != "intake" || c2.Stages["intake"].StartedAt != nil {
		t.Fatalf("restart on return: %v %+v", err, c2.Stages["intake"])
	}
}

func TestHoldSLARunKeepsTheClockGoing(t *testing.T) {
	ctx := context.Background()
	e, c, clk := policyEngine(t, func(d *Definition) { d.Stages[1].SLA.OnHold = "run" })
	c, err := e.Suspend(ctx, c, pLead, "coding", "waiting", nil)
	if err != nil || c.Stages["coding"].SLA.Status == SLAPaused {
		t.Fatalf("on_hold run: the clock is not stopped: %v %+v", err, c.Stages["coding"].SLA)
	}
	clk.add(3 * time.Hour)
	c, _, err = e.Sweep(ctx, c)
	if err != nil || c.Stages["coding"].SLA.Status != SLABreached {
		t.Fatalf("a hold does not hide a missed deadline when the clock runs: %v %+v", err, c.Stages["coding"].SLA)
	}
}

func TestPolicyValidation(t *testing.T) {
	bad := []func(*Definition){
		func(d *Definition) { d.Information = &InfoPolicy{SLA: "later"} },
		func(d *Definition) { d.Information = &InfoPolicy{OnTimeout: "withdraw"} },
		func(d *Definition) { d.Information = &InfoPolicy{Due: "soon"} },
		func(d *Definition) { d.Release = &ReleasePolicy{Targets: []string{"first"}} },
		func(d *Definition) { d.Release = &ReleasePolicy{Assignee: "maybe"} },
		func(d *Definition) { d.Start = &StartPolicy{OnReturn: "sometimes"} },
		func(d *Definition) { d.Stages[1].SLA.OnHold = "freeze" },
	}
	for i, f := range bad {
		if _, _, err := Compile2(t, f); err == nil {
			t.Errorf("case %d must be refused", i)
		}
	}
}
