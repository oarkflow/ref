package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// A grant application exercises the work-management features: routing,
// claims, SLAs on a business calendar with escalation, holds, votes, rules,
// computed and sealed inputs, external links, notes, erasure and analytics.

type clock struct{ t time.Time }

func (c *clock) now() time.Time         { return c.t }
func (c *clock) add(d time.Duration)    { c.t = c.t.Add(d) }
func (c *clock) set(s string) time.Time { c.t, _ = time.Parse(time.RFC3339, s); return c.t }

func grantDef() *Definition {
	return &Definition{
		Name:         "grant",
		NumberFormat: "GR-{seq:4}",
		Subject:      []string{"applicant.email"},
		Calendars: []Calendar{{Name: "office", Timezone: "Asia/Kathmandu",
			Hours: "sun-thu 10:00-17:00; fri 10:00-15:00", Holidays: []string{"2026-03-03", "01-01"}}},
		Workers: []Worker{
			{ID: "ana", Roles: []string{"officer"}, Skills: []string{"health"}, Capacity: 2},
			{ID: "bo", Roles: []string{"officer"}, Skills: []string{"health", "education"}},
			{ID: "cy", Roles: []string{"officer"}, Skills: []string{"education"},
				Away: []Absence{{Name: "leave", From: "2026-03-01", Until: "2026-03-10", Reason: "annual leave"}}},
			{ID: "sam", Roles: []string{"supervisor"}},
			{ID: "sue", Roles: []string{"supervisor"}},
		},
		Seal:      &SealPolicy{OpenRoles: []string{"treasurer"}, Quorum: 2, OpenAfter: "call.closes_at"},
		Retention: &Retention{After: "30d"},
		Notes:     &NotesPolicy{ApplicantMayWrite: true},
		Forms: []Form{
			{Name: "applicant", Inputs: []Input{
				{Name: "name", Kind: KindText, Required: true, PII: true},
				{Name: "email", Kind: KindEmail, Required: true},
			}},
			{Name: "budget", Inputs: []Input{
				{Name: "staff", Kind: KindNumber, Required: true},
				{Name: "equipment", Kind: KindNumber, Required: true},
				{Name: "total", Kind: KindNumber, Compute: "budget.staff + budget.equipment"},
				{Name: "offer", Kind: KindNumber, Sealed: true},
			}, Rules: []Rule{{Name: "cap", Check: "budget.staff + budget.equipment <= 100000", Message: "the total may not exceed 100000", Path: "budget.total"}}},
			{Name: "call", Inputs: []Input{{Name: "closes_at", Kind: KindDateTime}}},
			{Name: "reference", Inputs: []Input{{Name: "opinion", Kind: KindTextarea, Required: true}, {Name: "score", Kind: KindInteger}}},
		},
		Stages: []Stage{
			{Name: "apply", Public: true, Page: &Page{Groups: []Group{{Name: "all", Forms: []string{"applicant", "budget"}}}}},
			{Name: "screen", Roles: []string{"officer"}, AssignRoles: []string{"supervisor"}, SuspendRoles: []string{"officer", "supervisor"},
				ExternalRoles: []string{"officer"},
				Routing:       &Routing{Strategy: RouteSkills, SkillsFrom: "focus", PreferredSkills: []string{"education"}, Sticky: true},
				SLA: &SLA{Duration: "1d", WarnBefore: "2h", Calendar: "office", OnBreach: "reassign",
					Escalate: []Escalation{{Name: "supervisor", After: "4h", AssignRoles: []string{"supervisor"}, Notify: []string{"director"}}}},
				Page: &Page{Groups: []Group{
					{Name: "submitted", Mode: ModeReadonly, Forms: []string{"applicant", "budget"}},
					{Name: "ref", Title: "Reference", Forms: []string{"reference"}, Roles: []string{"referee"}},
				}},
				Rules: []Rule{{Name: "has_email", Check: "applicant.email != ''", Message: "no email"}},
				Actions: []ActionSpec{
					{Name: "accept", Outcome: OutcomeAdvance},
					{Name: "return", Outcome: OutcomeReturn, ReturnTo: "apply", CommentRequired: true},
				},
			},
			{Name: "panel", Roles: []string{"panelist"},
				Nodes: []Node{{Name: "decision", Kind: NodeVote, Voters: 3, Consensus: ConsensusMajority}}, AutoAdvance: true},
		},
	}
}

type loads map[string]Load

func newGrantEngine(t *testing.T, clk *clock, work loads) *Engine {
	t.Helper()
	c, err := Compile(grantDef())
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	e := NewEngine(c)
	e.Eval = bclEval{}
	e.Now = clk.now
	e.SigningKey = []byte("grant-signing-key-0123456789abcdef")
	e.SealKey = []byte("grant-seal-key-0123456789abcdef!")
	e.Workload = func(context.Context, string) (map[string]Load, error) { return work, nil }
	return e
}

func submitGrant(t *testing.T, e *Engine, focus string) *Case {
	t.Helper()
	ctx := context.Background()
	c, err := e.Start(ctx, Actor{ID: "u-app"}, StartOptions{Number: "GR-1"})
	if err != nil {
		t.Fatal(err)
	}
	c.Set("focus", focus)
	c, err = e.Act(ctx, c, Actor{ID: "u-app"}, "apply", "submit", ActInput{Data: map[string]any{
		"applicant": map[string]any{"name": "Asha Rai", "email": "asha@example.com"},
		"budget":    map[string]any{"staff": 40000, "equipment": 10000, "offer": 48000},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestCalendarWorkingTime(t *testing.T) {
	wc, err := CompileCalendar(Calendar{Name: "office", Timezone: "Asia/Kathmandu", Hours: "sun-thu 10:00-17:00; fri 10:00-15:00", Holidays: []string{"2026-03-03"}})
	if err != nil {
		t.Fatal(err)
	}
	ktm, _ := time.LoadLocation("Asia/Kathmandu")
	at := func(s string) time.Time { t, _ := time.ParseInLocation("2006-01-02 15:04", s, ktm); return t }
	// Friday 14:00 + 3h: 1h Friday, Saturday off, 2h Sunday -> Sunday 12:00.
	if got := wc.Add(at("2026-03-06 14:00"), 3*time.Hour); !got.Equal(at("2026-03-08 12:00")) {
		t.Fatalf("add over weekend: %s", got)
	}
	// Monday 16:00 + 2h: 1h Monday, Tuesday 3rd is a holiday -> Wednesday 11:00.
	if got := wc.Add(at("2026-03-02 16:00"), 2*time.Hour); !got.Equal(at("2026-03-04 11:00")) {
		t.Fatalf("add over holiday: %s", got)
	}
	// 1 working day from Thursday 12:00 -> Friday 12:00; 2 -> Sunday 12:00.
	if got := wc.AddDays(at("2026-03-05 12:00"), 2); !got.Equal(at("2026-03-08 12:00")) {
		t.Fatalf("add days: %s", got)
	}
	// Friday 14:00 is before the 15:00 close; Saturday is shut.
	if !wc.IsOpen(at("2026-03-06 14:00")) || wc.IsOpen(at("2026-03-07 12:00")) || wc.IsOpen(at("2026-03-03 12:00")) {
		t.Fatal("IsOpen")
	}
	if got := wc.Between(at("2026-03-06 14:00"), at("2026-03-08 12:00")); got != 3*time.Hour {
		t.Fatalf("between: %s", got)
	}
	if _, err := CompileCalendar(Calendar{Name: "bad", Hours: "funday 09:00-17:00"}); err == nil {
		t.Fatal("bad day accepted")
	}
	if sp, err := ParseSpan("1d12h"); err != nil || sp.Days != 1 || sp.Dur != 12*time.Hour {
		t.Fatalf("span: %+v %v", sp, err)
	}
}

func TestRoutingClaimsAndDelegation(t *testing.T) {
	ctx := context.Background()
	clk := &clock{}
	clk.set("2026-03-04T05:00:00Z") // Wednesday 10:45 in Kathmandu
	e := newGrantEngine(t, clk, loads{"ana": {Open: 2}, "bo": {Open: 1}})

	// Needs "health": cy lacks it (and is away); ana is at capacity; bo wins.
	c := submitGrant(t, e, "health")
	ss := c.Stages["screen"]
	if ss.Assignee != "bo" || ss.Routing == nil || !strings.Contains(ss.Routing.Reason, "bo") {
		t.Fatalf("routing: %+v", ss.Routing)
	}
	reasons := map[string]string{}
	for _, cand := range ss.Routing.Candidates {
		reasons[cand.ID] = cand.Reason
	}
	if !strings.Contains(reasons["ana"], "capacity") || !strings.Contains(reasons["cy"], "away") || !strings.Contains(reasons["sam"], "role") {
		t.Fatalf("candidate reasons: %v", reasons)
	}
	if ev := eventNames(c); !strings.Contains(ev, "assigned") || !strings.Contains(ev, "stage.entered") {
		t.Fatalf("events: %s", ev)
	}

	// Another officer cannot act on bo's case; a supervisor may reassign it.
	ana := Actor{ID: "ana", Roles: []string{"officer"}}
	bo := Actor{ID: "bo", Roles: []string{"officer"}}
	sam := Actor{ID: "sam", Roles: []string{"supervisor"}}
	if _, err := e.Act(ctx, c, ana, "screen", "accept", ActInput{}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("non-assignee act: %v", err)
	}
	if _, err := e.Assign(ctx, c, ana, "screen", "ana", ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("officer assign: %v", err)
	}
	if _, err := e.Assign(ctx, c, sam, "screen", "cy", ""); err == nil || !strings.Contains(err.Error(), "cannot take") {
		t.Fatalf("assign to an absent worker: %v", err)
	}
	c2, err := e.Assign(ctx, c, sam, "screen", "ana", "rebalancing")
	if err != nil || c2.Stages["screen"].Assignee != "ana" {
		t.Fatalf("assign: %v", err)
	}
	// Delegation: only the holder, only to an eligible colleague.
	if _, err := e.Delegate(ctx, c2, bo, "screen", "bo", ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("delegate by non-holder: %v", err)
	}
	c3, err := e.Delegate(ctx, c2, ana, "screen", "bo", "conflict of interest")
	if err != nil || c3.Stages["screen"].Assignee != "bo" {
		t.Fatalf("delegate: %v", err)
	}
	// Release re-routes, excluding whoever released it.
	c4, err := e.Release(ctx, c3, bo, "screen", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := c4.Stages["screen"].Assignee; got != "" {
		// Only bo has "health" and ana is at capacity: nobody else qualifies.
		t.Fatalf("release should queue: %q %+v", got, c4.Stages["screen"].Routing)
	}
	// Queued work is claimed by the first eligible person to act.
	c5, err := e.Claim(ctx, c4, ana, "screen")
	if err != nil || c5.Stages["screen"].Assignee != "ana" {
		t.Fatalf("claim: %v", err)
	}
	if _, err := e.Claim(ctx, c5, bo, "screen"); !errors.Is(err, ErrState) {
		t.Fatalf("double claim: %v", err)
	}

	// Sticky: a returned-and-resubmitted case goes back to its last officer.
	c6, err := e.Act(ctx, c5, ana, "screen", "return", ActInput{Comment: "wrong budget"})
	if err != nil {
		t.Fatal(err)
	}
	c7, err := e.Act(ctx, c6, Actor{ID: "u-app"}, "apply", "submit", ActInput{})
	if err != nil {
		t.Fatal(err)
	}
	if c7.Stage != "screen" {
		t.Fatalf("resubmit stage: %s", c7.Stage)
	}
	if ss := c7.Stages["screen"]; ss.Assignee != "ana" && ss.Assignee != "bo" {
		t.Fatalf("sticky routing: %+v", ss.Routing)
	}
}

func TestSLAWarningBreachEscalationAndHold(t *testing.T) {
	ctx := context.Background()
	clk := &clock{}
	clk.set("2026-03-04T05:00:00Z") // Wed 10:45 NPT
	e := newGrantEngine(t, clk, loads{})
	c := submitGrant(t, e, "education")
	ss := c.Stages["screen"]
	if ss.Assignee != "bo" { // skill_based: bo has both skills... cy is away
		t.Fatalf("assignee: %s (%s)", ss.Assignee, ss.Routing.Reason)
	}
	// 1 working day from Wed 10:45 NPT -> Thu 10:45 NPT = Thu 05:00Z.
	if want, _ := time.Parse(time.RFC3339, "2026-03-05T05:00:00Z"); !ss.SLA.DueAt.Equal(want) {
		t.Fatalf("due: %s", ss.SLA.DueAt)
	}

	// Put it on hold for a working day: the deadline moves by that time.
	bo := Actor{ID: "bo", Roles: []string{"officer"}}
	c, err := e.Suspend(ctx, c, bo, "screen", "awaiting bank letter", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Act(ctx, c, bo, "screen", "accept", ActInput{}); !errors.Is(err, ErrState) {
		t.Fatalf("act while on hold: %v", err)
	}
	clk.add(24 * time.Hour) // Thu 10:45 NPT
	c, err = e.Resume(ctx, c, bo, "screen", "letter received")
	if err != nil {
		t.Fatal(err)
	}
	due := c.Stages["screen"].SLA.DueAt
	if want, _ := time.Parse(time.RFC3339, "2026-03-06T05:00:00Z"); !due.Equal(want) {
		t.Fatalf("due after hold: %s", due) // Fri 10:45 NPT
	}

	// Warning 2 working hours before, breach at the deadline.
	clk.set("2026-03-06T03:30:00Z") // Fri 09:15 NPT: office shut; warn at 08:45 NPT-equivalent working time
	c, res, err := e.Sweep(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Changed || c.Stages["screen"].SLA.Status != SLAWarning || !strings.Contains(eventNames(c), "sla.warning") {
		t.Fatalf("warning: %+v %s", c.Stages["screen"].SLA, eventNames(c))
	}
	clk.set("2026-03-06T05:30:00Z")
	c, _, err = e.Sweep(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	ss = c.Stages["screen"]
	if ss.SLA.Status != SLABreached || !strings.Contains(eventNames(c), "sla.breached") {
		t.Fatalf("breach: %+v", ss.SLA)
	}
	// on_breach reassign: bo is excluded; ana lacks "education"; nobody -> queued.
	if ss.Assignee == "bo" {
		t.Fatalf("breach should move the case off bo: %+v", ss.Routing)
	}
	// Escalation 4 working hours after the breach: Fri 11:15 + 4h = Sun 11:00 (Fri closes 15:00).
	clk.set("2026-03-06T08:00:00Z") // Fri 13:45 NPT
	c, res, _ = e.Sweep(ctx, c)
	if res.Changed {
		t.Fatalf("escalated too early: %+v", c.Stages["screen"].SLA)
	}
	clk.set("2026-03-08T05:30:00Z") // Sun 11:15 NPT
	c, _, _ = e.Sweep(ctx, c)
	ss = c.Stages["screen"]
	if ss.SLA.Level != 1 || (ss.Assignee != "sam" && ss.Assignee != "sue") || !strings.Contains(eventNames(c), "sla.escalated") {
		t.Fatalf("escalation: level=%d assignee=%s %s", ss.SLA.Level, ss.Assignee, eventNames(c))
	}
	// Timeline records the breach for analytics.
	if v := c.openVisit("screen"); v == nil || !v.Breached || v.SuspendedSeconds < 86000 {
		t.Fatalf("visit: %+v", v)
	}
}

func TestVotesRulesComputedAndSealed(t *testing.T) {
	ctx := context.Background()
	clk := &clock{}
	clk.set("2026-03-04T05:00:00Z")
	e := newGrantEngine(t, clk, loads{})
	app := Actor{ID: "u-app"}

	c, err := e.Start(ctx, app, StartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// Rule: the total may not exceed the cap. Computed: total = staff + equipment.
	_, err = e.Act(ctx, c, app, "apply", "submit", ActInput{Data: map[string]any{
		"applicant": map[string]any{"name": "Asha Rai", "email": "asha@example.com"},
		"budget":    map[string]any{"staff": 90000, "equipment": 20000, "total": 1},
	}})
	var ve *ValidationError
	if !errors.As(err, &ve) || ve.Fields[0].Path != "budget.total" {
		t.Fatalf("rule: %v", err)
	}
	c, err = e.Save(ctx, c, app, "apply", map[string]any{"budget": map[string]any{"staff": 40000, "equipment": 10000, "total": 1, "offer": 48000}})
	if err != nil {
		t.Fatal(err)
	}
	if total, _ := c.Get("budget.total"); total != float64(50000) {
		t.Fatalf("computed total: %v", total)
	}
	// Sealed: only a marker and ciphertext are stored.
	if v, _ := c.Get("budget.offer"); v != SealedMarker || c.Sealed["budget.offer"].Ciphertext == "" {
		t.Fatalf("sealed: %v %+v", v, c.Sealed)
	}
	if strings.Contains(mustJSON(c), "48000") {
		t.Fatal("the sealed value leaked into the stored case")
	}
	v, _ := e.View(c, app, "")
	for _, g := range v.Page.Groups {
		for _, f := range g.Forms {
			for _, in := range f.Inputs {
				if in.Path == "budget.offer" && (!in.Sealed || in.Value != nil) {
					t.Fatalf("sealed view: %+v", in)
				}
				if in.Path == "budget.total" && (!in.Computed || in.Editable) {
					t.Fatalf("computed view: %+v", in)
				}
			}
		}
	}

	// Opening: not before the call closes, two distinct treasurers.
	c.Set("call.closes_at", "2026-03-10T00:00:00Z")
	t1 := Actor{ID: "t1", Roles: []string{"treasurer"}}
	t2 := Actor{ID: "t2", Roles: []string{"treasurer"}}
	if _, err := e.ApproveOpening(ctx, c, t1, ""); !errors.Is(err, ErrState) {
		t.Fatalf("opening before the deadline: %v", err)
	}
	clk.set("2026-03-10T01:00:00Z")
	if _, err := e.ApproveOpening(ctx, c, Actor{ID: "x", Roles: []string{"officer"}}, ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("opening without the role: %v", err)
	}
	c, err = e.ApproveOpening(ctx, c, t1, "")
	if err != nil || c.opened() {
		t.Fatalf("first approval: %v opened=%v", err, c.opened())
	}
	if _, err := e.ApproveOpening(ctx, c, t1, ""); !errors.Is(err, ErrState) {
		t.Fatalf("same approver twice: %v", err)
	}
	c, err = e.ApproveOpening(ctx, c, t2, "")
	if err != nil || !c.opened() {
		t.Fatalf("second approval: %v", err)
	}
	if v, _ := c.Get("budget.offer"); v != float64(48000) {
		t.Fatalf("opened value: %v", v)
	}

	// Vote: majority of 3.
	c.Stage = "panel"
	c.Status = CaseInProgress
	c.Stages["panel"] = &StageState{Status: StageActive, Nodes: map[string]*NodeState{"decision": {Status: NodePending}}}
	p := func(id string) Actor { return Actor{ID: id, Roles: []string{"panelist"}} }
	c, err = e.NodeAct(ctx, c, p("p1"), "panel", "decision", "vote", NodeInput{Result: map[string]any{"decision": "approve"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.NodeAct(ctx, c, p("p1"), "panel", "decision", "vote", NodeInput{Result: map[string]any{"decision": "approve"}}); !errors.Is(err, ErrState) {
		t.Fatalf("double vote: %v", err)
	}
	if _, err := e.NodeAct(ctx, c, p("p2"), "panel", "decision", "vote", NodeInput{Result: map[string]any{"decision": "reject"}}); err == nil {
		t.Fatal("reject without a reason accepted")
	}
	c, err = e.NodeAct(ctx, c, p("p2"), "panel", "decision", "vote", NodeInput{Result: map[string]any{"decision": "reject"}, Comment: "weak plan"})
	if err != nil || c.Stages["panel"].Nodes["decision"].Status != NodeInProgress {
		t.Fatalf("1-1: %v", err)
	}
	c, err = e.NodeAct(ctx, c, p("p3"), "panel", "decision", "vote", NodeInput{Result: map[string]any{"decision": "approve"}})
	if err != nil || c.Status != CaseCompleted {
		t.Fatalf("2-1 majority should pass and auto-advance: %v %s", err, c.Status)
	}
}

func TestTallyPolicies(t *testing.T) {
	v := func(ds ...string) []Approval {
		var out []Approval
		for _, d := range ds {
			out = append(out, Approval{Decision: d})
		}
		return out
	}
	for _, tc := range []struct {
		policy string
		voters int
		votes  []Approval
		want   string
	}{
		{"", 3, v("approve", "approve"), NodeInProgress},
		{"", 3, v("approve", "reject"), NodeFailed},
		{"", 2, v("approve", "approve"), NodePassed},
		{"majority", 4, v("approve", "approve"), NodeInProgress},
		{"majority", 4, v("reject", "reject"), NodeFailed},
		{"majority", 3, v("approve", "approve"), NodePassed},
		{"2", 5, v("approve", "approve"), NodePassed},
		{"4", 5, v("reject", "reject"), NodeFailed},
	} {
		got, _ := tallyVotes(&Node{Voters: tc.voters, Consensus: tc.policy}, tc.votes)
		if got != tc.want {
			t.Errorf("%s/%d %v: got %s want %s", tc.policy, tc.voters, tc.votes, got, tc.want)
		}
	}
}

func TestExternalLinksNotesErasureAndAnalytics(t *testing.T) {
	ctx := context.Background()
	clk := &clock{}
	clk.set("2026-03-04T05:00:00Z")
	e := newGrantEngine(t, clk, loads{})
	c := submitGrant(t, e, "education")
	bo := Actor{ID: "bo", Roles: []string{"officer"}}

	// A referee fills only the reference form, once.
	if _, _, err := e.IssueLink(ctx, c, Actor{ID: "u-app"}, "screen", "referee", []string{"reference"}, time.Hour); !errors.Is(err, ErrForbidden) {
		t.Fatalf("applicant issuing a link: %v", err)
	}
	c, token, err := e.IssueLink(ctx, c, bo, "screen", "Dr Joshi", []string{"reference"}, 48*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.LinkActor(c, token+"x"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("tampered token: %v", err)
	}
	ref, err := e.LinkActor(c, token)
	if err != nil {
		t.Fatal(err)
	}
	v, err := e.View(c, ref, "")
	if err != nil || v.External == nil || len(v.Page.Groups) != 1 || v.Page.Groups[0].Name != "ref" || len(v.Nodes) != 0 {
		t.Fatalf("link view: %v %+v", err, v)
	}
	if _, err := e.Act(ctx, c, ref, "screen", "accept", ActInput{}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("link act: %v", err)
	}
	if _, err := e.LinkSubmit(ctx, c, ref, map[string]any{"reference": map[string]any{"score": 4}}); err == nil {
		t.Fatal("link submit without the required opinion accepted")
	}
	c, err = e.LinkSubmit(ctx, c, ref, map[string]any{
		"reference": map[string]any{"opinion": "Strong candidate", "score": 5},
		"applicant": map[string]any{"name": "Mallory"}, // out of scope: ignored
	})
	if err != nil {
		t.Fatal(err)
	}
	if name, _ := c.Get("applicant.name"); name != "Asha Rai" {
		t.Fatalf("out-of-scope write: %v", name)
	}
	if _, err := e.LinkActor(c, token); !errors.Is(err, ErrForbidden) {
		t.Fatalf("reused link: %v", err)
	}

	// Notes: internal notes are invisible to the applicant.
	app := Actor{ID: "u-app"}
	c, err = e.AddNote(ctx, c, bo, "bank letter looks forged: call the bank, Asha Rai", true, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.AddNote(ctx, c, app, "secret", true, ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("applicant internal note: %v", err)
	}
	c, err = e.AddNote(ctx, c, app, "I sent the bank letter again", false, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(e.NotesFor(c, app)) != 1 || len(e.NotesFor(c, bo)) != 2 {
		t.Fatalf("note visibility: %d %d", len(e.NotesFor(c, app)), len(e.NotesFor(c, bo)))
	}

	// Subject matching and erasure; a legal hold blocks it.
	if ok, _ := e.MatchesSubject(c, map[string]string{"applicant.email": "ASHA@example.com"}); !ok {
		t.Fatal("subject match")
	}
	if _, err := e.MatchesSubject(c, map[string]string{"applicant.name": "Asha Rai"}); err == nil {
		t.Fatal("non-subject identifier accepted")
	}
	held, _ := e.PlaceHold(ctx, c, Actor{ID: "legal"}, "litigation 42")
	if _, err := e.Anonymize(ctx, held, Actor{ID: "dpo"}, "request 7"); !errors.Is(err, ErrState) {
		t.Fatalf("erase under hold: %v", err)
	}
	erased, err := e.Anonymize(ctx, c, Actor{ID: "dpo"}, "request 7")
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := erased.Get("applicant.name"); n != ErasedMarker {
		t.Fatalf("pii not erased: %v", n)
	}
	if strings.Contains(mustJSON(erased), "Asha Rai") || strings.Contains(mustJSON(erased), "asha@example.com") {
		t.Fatal("personal data survived erasure")
	}

	// Retention: a finished case is anonymised 30 days after closing.
	done := c.Clone()
	done.Status = CaseCompleted
	e.finish(done, clk.t)
	clk.add(29 * 24 * time.Hour)
	if _, res, _ := e.Sweep(ctx, done); res.Changed {
		t.Fatal("retention applied early")
	}
	clk.add(2 * 24 * time.Hour)
	after, res, _ := e.Sweep(ctx, done)
	if !res.Changed || after.Erased == nil {
		t.Fatal("retention not applied")
	}

	// Analytics over a few cases.
	clk.set("2026-03-04T05:00:00Z")
	a1 := submitGrant(t, e, "education")
	clk.add(3 * time.Hour)
	a1, err = e.Act(ctx, a1, Actor{ID: a1.Stages["screen"].Assignee, Roles: []string{"officer"}}, "screen", "return", ActInput{Comment: "fix"})
	if err != nil {
		t.Fatal(err)
	}
	a2 := submitGrant(t, e, "health")
	rep := e.Analyze([]*Case{a1, a2}, clk.t)
	var screen StageAnalytics
	for _, s := range rep.Stages {
		if s.Stage == "screen" {
			screen = s
		}
	}
	if rep.Cases != 2 || rep.Open != 2 || screen.Returned != 1 || screen.OpenNow != 1 || screen.DwellHours.Count != 1 || screen.DwellHours.Max != 3 || screen.ReworkRate != 0.5 {
		t.Fatalf("analytics: %+v screen=%+v", rep, screen)
	}
	if len(rep.Bottlenecks) == 0 {
		t.Fatalf("bottlenecks: %+v", rep)
	}
}

func eventNames(c *Case) string {
	var names []string
	for _, ev := range c.Events() {
		names = append(names, ev.Name)
	}
	return strings.Join(names, ",")
}

func mustJSON(c *Case) string {
	raw, _ := json.Marshal(c)
	return string(raw)
}
