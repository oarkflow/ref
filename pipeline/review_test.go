package pipeline

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// saver persists each step in a memory store so revisions advance as they
// do in a host.
type saver struct {
	t *testing.T
	s *MemoryStore
}

func (sv saver) create(c *Case, err error) *Case {
	sv.t.Helper()
	if err != nil {
		sv.t.Fatal(err)
	}
	if err := sv.s.Create(context.Background(), c); err != nil {
		sv.t.Fatal(err)
	}
	return c
}

func (sv saver) save(c *Case, err error) *Case {
	sv.t.Helper()
	if err != nil {
		sv.t.Fatal(err)
	}
	if err := sv.s.Update(context.Background(), c); err != nil {
		sv.t.Fatal(err)
	}
	return c
}

func reviewEngine(t *testing.T, def *Definition, clk *clock) *Engine {
	t.Helper()
	c, err := Compile(def)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	e := NewEngine(c)
	e.Eval = bclEval{}
	e.Now = clk.now
	return e
}

func diffDef(against string) *Definition {
	return &Definition{
		Name: "permit",
		Forms: []Form{{Name: "applicant", Inputs: []Input{
			{Name: "name", Kind: KindText, Required: true},
			{Name: "email", Kind: KindEmail},
			{Name: "national_id", Kind: KindText, Sensitive: true},
		}}},
		Stages: []Stage{
			{Name: "apply", Public: true, Page: &Page{Groups: []Group{{Name: "g", Forms: []string{"applicant"}}}}},
			{Name: "review", Roles: []string{"officer"}, Reviews: []Review{{Mode: ReviewDiff, Against: against}},
				Page: &Page{Groups: []Group{{Name: "g", Mode: ModeReadonly, Forms: []string{"applicant"}}}},
				Actions: []ActionSpec{
					{Name: "accept", Outcome: OutcomeAdvance},
					{Name: "return", Outcome: OutcomeReturn, ReturnTo: "apply"},
				}},
			{Name: "final", Roles: []string{"officer"}, Actions: []ActionSpec{
				{Name: "done", Outcome: OutcomeApprove},
				{Name: "amend", Outcome: OutcomeAdvance, Next: "apply"},
			}},
		},
	}
}

func changesOf(v *View) string {
	var parts []string
	for _, ch := range v.Review.Diff.Changes {
		parts = append(parts, fmt.Sprintf("%s:%s:%v->%v", ch.Path, ch.Change, ch.Before, ch.After))
	}
	return strings.Join(parts, " ")
}

func TestReviewDiffSinceSubmissionAndApproval(t *testing.T) {
	ctx := context.Background()
	clk := &clock{}
	clk.set("2026-05-01T09:00:00Z")
	sv := saver{t, NewMemoryStore()}
	app, officer := Actor{ID: "u1"}, Actor{ID: "o1", Roles: []string{"officer"}}

	e := reviewEngine(t, diffDef(""), clk)
	c := sv.create(e.Start(ctx, app, StartOptions{}))
	c = sv.save(e.Act(ctx, c, app, "apply", "submit", ActInput{Data: map[string]any{
		"applicant": map[string]any{"name": "Asha", "email": "a@example.com", "national_id": "1234567890"}}}))
	v, err := e.View(c, officer, "")
	if err != nil {
		t.Fatal(err)
	}
	if v.Review == nil || !v.Review.Diff.First || len(v.Review.Diff.Changes) != 3 || v.Review.Diff.Revision != 2 {
		t.Fatalf("first diff: %+v", v.Review)
	}
	if got := changesOf(v); !strings.Contains(got, "applicant.national_id:added:<nil>->••••") {
		t.Fatalf("sensitive values must be masked: %s", got)
	}
	if av, _ := e.View(c, app, "apply"); av.Review != nil {
		t.Fatal("the applicant must not see the review state")
	}

	// Returned for correction: only the changed field shows.
	c = sv.save(e.Act(ctx, c, officer, "review", "return", ActInput{Comment: "fix email", Flags: map[string]string{"applicant.email": "typo"}}))
	c = sv.save(e.Act(ctx, c, app, "apply", "submit", ActInput{Data: map[string]any{"applicant": map[string]any{"email": "b@example.com"}}}))
	v, _ = e.View(c, officer, "")
	if d := v.Review.Diff; d.First || d.BaseRevision != 2 || d.Revision != c.Revision {
		t.Fatalf("second diff header: %+v", d)
	}
	if got := changesOf(v); got != "applicant.email:changed:a@example.com->b@example.com" {
		t.Fatalf("second diff: %s", got)
	}

	// Approval records the revision reviewed.
	reviewed := c.Revision
	c = sv.save(e.Act(ctx, c, officer, "review", "accept", ActInput{}))
	ss := c.Stages["review"]
	if ss.Review == nil || ss.Review.ReviewedRevision != reviewed || ss.Review.ReviewedBy != "o1" {
		t.Fatalf("reviewed revision: %+v", ss.Review)
	}
	var entry Entry
	for _, h := range c.History {
		if h.Action == "accept" {
			entry = h
		}
	}
	if entry.Revision != reviewed {
		t.Fatalf("history entry revision: %+v", entry)
	}
	last := c.Snapshots[len(c.Snapshots)-1]
	if last.Kind != SnapshotApproved || last.Revision != reviewed {
		t.Fatalf("approved snapshot: %+v", last)
	}
}

func TestReviewDiffAgainstLastApproved(t *testing.T) {
	ctx := context.Background()
	clk := &clock{}
	clk.set("2026-05-01T09:00:00Z")
	sv := saver{t, NewMemoryStore()}
	app, officer := Actor{ID: "u1"}, Actor{ID: "o1", Roles: []string{"officer"}}
	e := reviewEngine(t, diffDef("approved"), clk)
	c := sv.create(e.Start(ctx, app, StartOptions{}))
	c = sv.save(e.Act(ctx, c, app, "apply", "submit", ActInput{Data: map[string]any{"applicant": map[string]any{"name": "Asha", "email": "a@example.com"}}}))
	approvedAt := c.Revision
	c = sv.save(e.Act(ctx, c, officer, "review", "accept", ActInput{}))
	// An amendment goes round again; the reviewer sees it against what
	// they approved last time.
	c = sv.save(e.Act(ctx, c, officer, "final", "amend", ActInput{}))
	if c.Stage != "apply" {
		t.Fatalf("amend: at %s", c.Stage)
	}
	c = sv.save(e.Act(ctx, c, app, "apply", "submit", ActInput{Data: map[string]any{"applicant": map[string]any{"name": "Asha Rai", "email": ""}}}))
	v, _ := e.View(c, officer, "")
	d := v.Review.Diff
	if d.Against != "approved" || d.First || d.BaseRevision != approvedAt {
		t.Fatalf("baseline: %+v", d)
	}
	if got := changesOf(v); got != "applicant.email:removed:a@example.com-><nil> applicant.name:changed:Asha->Asha Rai" {
		t.Fatalf("diff: %s", got)
	}
}

func gateDef() *Definition {
	return &Definition{
		Name:  "loan",
		Forms: []Form{{Name: "loan", Inputs: []Input{{Name: "amount", Kind: KindNumber}}}},
		Stages: []Stage{
			{Name: "apply", Public: true, Page: &Page{Groups: []Group{{Name: "g", Forms: []string{"loan"}}}}},
			{Name: "check", Roles: []string{"officer", "senior"},
				Reviews: []Review{{Mode: ReviewGate, Approvals: 2, Roles: []string{"senior"}}},
				Actions: []ActionSpec{
					{Name: "accept", Outcome: OutcomeAdvance, SkipNodes: true},
					{Name: "reject", Outcome: OutcomeReject},
				}},
			{Name: "done", Roles: []string{"officer"}},
		},
	}
}

func TestReviewGateNeedsDistinctApprovals(t *testing.T) {
	ctx := context.Background()
	clk := &clock{}
	clk.set("2026-05-01T09:00:00Z")
	sv := saver{t, NewMemoryStore()}
	def := gateDef()
	e := reviewEngine(t, def, clk)
	if len(def.Stages[1].Nodes) != 0 {
		t.Fatal("compiling must not change the caller's definition")
	}
	app, officer := Actor{ID: "u1", Roles: []string{"senior"}}, Actor{ID: "o1", Roles: []string{"officer"}}
	s1, s2, s3 := Actor{ID: "s1", Roles: []string{"senior"}}, Actor{ID: "s2", Roles: []string{"senior"}}, Actor{ID: "s3", Roles: []string{"senior"}}
	c := sv.create(e.Start(ctx, app, StartOptions{}))
	c = sv.save(e.Act(ctx, c, app, "apply", "submit", ActInput{Data: map[string]any{"loan": map[string]any{"amount": 5000}}}))

	if _, err := e.Act(ctx, c, officer, "check", "accept", ActInput{}); !errors.Is(err, ErrState) || !strings.Contains(err.Error(), "0 of 2") {
		t.Fatalf("skip_nodes must not pass a gate: %v", err)
	}
	if _, err := e.NodeAct(ctx, c, officer, "check", "gate", "approve", NodeInput{}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("officer lacks the gate role: %v", err)
	}
	if _, err := e.NodeAct(ctx, c, app, "check", "gate", "approve", NodeInput{}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("the applicant cannot approve their own case: %v", err)
	}
	if _, err := e.NodeAct(ctx, c, s1, "check", "gate", "waive", NodeInput{}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a gate cannot be waived: %v", err)
	}
	var ve *ValidationError
	if _, err := e.NodeAct(ctx, c, s1, "check", "gate", "reject", NodeInput{}); !errors.As(err, &ve) {
		t.Fatalf("a rejection needs a reason: %v", err)
	}
	c = sv.save(e.NodeAct(ctx, c, s1, "check", "gate", "reject", NodeInput{Comment: "income unverified"}))
	if _, err := e.NodeAct(ctx, c, s1, "check", "gate", "approve", NodeInput{}); !errors.Is(err, ErrState) {
		t.Fatalf("one decision per reviewer: %v", err)
	}
	c = sv.save(e.NodeAct(ctx, c, s2, "check", "gate", "approve", NodeInput{}))
	if _, err := e.Act(ctx, c, officer, "check", "accept", ActInput{}); !errors.Is(err, ErrState) {
		t.Fatalf("1 of 2 must not pass: %v", err)
	}
	rev := c.Revision
	c = sv.save(e.NodeAct(ctx, c, s3, "check", "gate", "approve", NodeInput{}))
	ns := c.Stages["check"].Nodes["gate"]
	if ns.Status != NodePassed || len(ns.Approvals) != 3 || ns.Approvals[0].Decision != "reject" || ns.Approvals[2].Revision != rev {
		t.Fatalf("gate state: %+v", ns)
	}
	v, _ := e.View(c, officer, "")
	if g := v.Review.Gate; g == nil || !g.Open || g.Approvals != 2 || g.Rejections != 1 || g.Required != 2 {
		t.Fatalf("gate view: %+v", v.Review)
	}
	if !strings.Contains(eventNames(c), "review.gate_opened") {
		t.Fatalf("events: %s", eventNames(c))
	}
	c = sv.save(e.Act(ctx, c, officer, "check", "accept", ActInput{}))
	if c.Stage != "done" {
		t.Fatalf("after the gate opened: %s", c.Stage)
	}
}

func triageDef() *Definition {
	return &Definition{
		Name:    "claims",
		Workers: []Worker{{ID: "o1", Roles: []string{"officer"}}, {ID: "sr1", Roles: []string{"senior"}}},
		Forms:   []Form{{Name: "claim", Inputs: []Input{{Name: "amount", Kind: KindNumber}}}},
		Stages: []Stage{
			{Name: "apply", Public: true, Page: &Page{Groups: []Group{{Name: "g", Forms: []string{"claim"}}}}},
			{Name: "assess", Roles: []string{"officer", "senior"}, Routing: &Routing{Strategy: RouteLeastLoaded, Roles: []string{"officer"}},
				Reviews: []Review{{Mode: ReviewTriage, DefaultQueue: "standard", Buckets: []TriageBucket{
					{Name: "urgent", When: "claim.amount > 1000", Priority: 1, Queue: "urgent", Roles: []string{"senior"}},
					{Name: "normal", When: "claim.amount > 100", Priority: 2, Queue: "standard"},
				}}}},
		},
	}
}

func TestReviewTriageClassifiesRoutesAndOrders(t *testing.T) {
	ctx := context.Background()
	clk := &clock{}
	clk.set("2026-05-01T09:00:00Z")
	store := NewMemoryStore()
	sv := saver{t, store}
	e := reviewEngine(t, triageDef(), clk)
	submit := func(id string, amount float64) *Case {
		c := sv.create(e.Start(ctx, Actor{ID: "u-" + id}, StartOptions{ID: id}))
		clk.add(1)
		return sv.save(e.Act(ctx, c, Actor{ID: "u-" + id}, "apply", "submit", ActInput{Data: map[string]any{"claim": map[string]any{"amount": amount}}}))
	}
	small := submit("small", 50)
	big := submit("big", 5000)
	mid := submit("mid", 500)
	if tr := big.Triage; tr == nil || tr.Bucket != "urgent" || tr.Priority != 1 || tr.Queue != "urgent" {
		t.Fatalf("big: %+v", big.Triage)
	}
	if big.Stages["assess"].Assignee != "sr1" || !strings.Contains(big.Stages["assess"].Routing.Reason, "triage bucket urgent") {
		t.Fatalf("urgent work goes to seniors: %+v", big.Stages["assess"].Routing)
	}
	if tr := small.Triage; tr.Bucket != "" || tr.Priority != 3 || tr.Queue != "standard" || small.Stages["assess"].Assignee != "o1" {
		t.Fatalf("small: %+v / %s", small.Triage, small.Stages["assess"].Assignee)
	}
	if mid.Triage.Priority != 2 || !strings.Contains(eventNames(mid), "triaged") {
		t.Fatalf("mid: %+v %s", mid.Triage, eventNames(mid))
	}
	list, _ := store.List(ctx, Query{Pipeline: "claims", Order: OrderPriority})
	if got := list[0].ID + "," + list[1].ID + "," + list[2].ID; got != "big,mid,small" {
		t.Fatalf("priority order: %s", got)
	}
	list, _ = store.List(ctx, Query{Pipeline: "claims", Queues: []string{"standard"}, Order: OrderPriority})
	if len(list) != 2 || list[0].ID != "mid" {
		t.Fatalf("standard queue: %d", len(list))
	}
}

func samplingDef(percent float64) *Definition {
	return &Definition{
		Name:  "claims",
		Forms: []Form{{Name: "claim", Inputs: []Input{{Name: "amount", Kind: KindNumber}}}},
		Stages: []Stage{
			{Name: "apply", Public: true, Page: &Page{Groups: []Group{{Name: "g", Forms: []string{"claim"}}}}},
			{Name: "audit", Roles: []string{"auditor"}, Nodes: []Node{{Name: "look", Kind: NodeTask}},
				Reviews: []Review{{Mode: ReviewSampling, Percent: percent, Salt: "2026", AlwaysReviewIf: []string{"claim.amount > 10000"}}}},
			{Name: "pay", Roles: []string{"cashier"}},
		},
	}
}

func TestReviewSamplingIsDeterministicAndAudited(t *testing.T) {
	ctx := context.Background()
	clk := &clock{}
	clk.set("2026-05-01T09:00:00Z")
	e := reviewEngine(t, samplingDef(30), clk)
	run := func(id string, amount float64) *Case {
		c, err := e.Start(ctx, Actor{ID: "u"}, StartOptions{ID: id})
		if err != nil {
			t.Fatal(err)
		}
		c, err = e.Act(ctx, c, Actor{ID: "u"}, "apply", "submit", ActInput{Data: map[string]any{"claim": map[string]any{"amount": amount}}})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	sampled, skippedID := 0, ""
	for i := range 400 {
		id := fmt.Sprintf("case-%d", i)
		c := run(id, 10)
		d := c.Stages["audit"].Review.Sampling
		if d.Bucket != SampleBucket("2026", "claims", "audit", id) || d.Sampled != (d.Bucket < 3000) {
			t.Fatalf("%s: decision %+v is not reproducible", id, d)
		}
		if again := run(id, 10); again.Stages["audit"].Review.Sampling.Sampled != d.Sampled {
			t.Fatalf("%s: a second run decided differently", id)
		}
		if d.Sampled {
			sampled++
			if c.Stage != "audit" || c.Stages["audit"].Status != StageActive {
				t.Fatalf("a sampled case waits for review: %s", c.Stage)
			}
			continue
		}
		skippedID = id
		ss := c.Stages["audit"]
		if c.Stage != "pay" || ss.Status != StageCompleted || ss.CompletedBy != "system" || ss.Nodes["look"].Status != NodeSkipped {
			t.Fatalf("an unsampled case passes: %s %+v", c.Stage, ss)
		}
		if !strings.Contains(eventNames(c), "review.not_sampled") || !slicesContainsAction(c.History, "not_sampled", "outside the 30% sample") {
			t.Fatalf("no audit record: %+v", c.History)
		}
	}
	if sampled < 90 || sampled > 150 {
		t.Fatalf("sampled %d of 400 at 30%%", sampled)
	}
	// Always-review overrides the sample.
	c := run(skippedID, 20000)
	if d := c.Stages["audit"].Review.Sampling; !d.Sampled || !strings.HasPrefix(d.Reason, "always reviewed") || c.Stage != "audit" {
		t.Fatalf("override: %+v", d)
	}
	// 0% passes everything, 100% reviews everything.
	e = reviewEngine(t, samplingDef(0), clk)
	if c := run("x", 1); c.Stage != "pay" {
		t.Fatalf("0%%: %s", c.Stage)
	}
	e = reviewEngine(t, samplingDef(100), clk)
	if c := run("y", 1); c.Stage != "audit" {
		t.Fatalf("100%%: %s", c.Stage)
	}
}

func slicesContainsAction(h []Entry, action, comment string) bool {
	for _, e := range h {
		if e.Action == action && strings.Contains(e.Comment, comment) {
			return true
		}
	}
	return false
}

func TestReviewCompileErrors(t *testing.T) {
	for name, mutate := range map[string]func(*Definition){
		"unknown mode":   func(d *Definition) { d.Stages[1].Reviews = []Review{{Mode: "vibes"}} },
		"duplicate mode": func(d *Definition) { d.Stages[1].Reviews = append(d.Stages[1].Reviews, d.Stages[1].Reviews[0]) },
		"bad percent":    func(d *Definition) { d.Stages[1].Reviews[0].Percent = 101 },
		"no bucket":      func(d *Definition) { d.Stages[1].Reviews = []Review{{Mode: ReviewTriage}} },
		"zero priority": func(d *Definition) {
			d.Stages[1].Reviews = []Review{{Mode: ReviewTriage, Buckets: []TriageBucket{{Name: "a"}}}}
		},
		"bad against":     func(d *Definition) { d.Stages[1].Reviews = []Review{{Mode: ReviewDiff, Against: "yesterday"}} },
		"gate on a task":  func(d *Definition) { d.Stages[1].Reviews = []Review{{Mode: ReviewGate, Node: "look"}} },
		"notify severity": func(d *Definition) { d.Notify = []NotifyRule{{Event: "*", To: []string{"applicant"}, Severity: "meh"}} },
		"notify no to":    func(d *Definition) { d.Notify = []NotifyRule{{Event: "*"}} },
	} {
		d := samplingDef(10)
		mutate(d)
		if _, err := Compile(d); err == nil {
			t.Errorf("%s: compiled", name)
		}
	}
}
