package pipeline

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeLiveness struct {
	res   LivenessResult
	err   error
	calls int
	last  LivenessRequest
}

func (f *fakeLiveness) VerifyLiveness(_ context.Context, r LivenessRequest) (LivenessResult, error) {
	f.calls++
	f.last = r
	return f.res, f.err
}

func kycDef(node Node) *Definition {
	return &Definition{
		Name: "kyc",
		Inputs: []Input{
			{Name: "name", Kind: KindText, Required: true},
			{Name: "id_photo", Kind: KindText},
		},
		Forms: []Form{{Name: "who", Fields: []string{"name", "id_photo"}}},
		Stages: []Stage{
			{Name: "details", Public: true, Page: &Page{Groups: []Group{{Name: "g", Forms: []string{"who"}}}},
				Actions: []ActionSpec{{Name: "submit", Outcome: OutcomeAdvance}}},
			{Name: "liveness", Public: true, Nodes: []Node{node}, AutoAdvance: true,
				Actions: []ActionSpec{{Name: "done", Outcome: OutcomeApprove}}},
		},
	}
}

func kycCase(t *testing.T, node Node, v LivenessVerifier) (*Engine, *Case, *time.Time) {
	t.Helper()
	c, err := Compile(kycDef(node))
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	e := NewEngine(c)
	e.Eval = bclEval{}
	e.Liveness = v
	now := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	e.Now = func() time.Time { return now }
	cs, err := e.Start(context.Background(), applicant, StartOptions{Number: "K-1"})
	if err != nil {
		t.Fatal(err)
	}
	cs, err = e.Act(context.Background(), cs, applicant, "details", "submit", ActInput{Data: map[string]any{
		"who": map[string]any{"name": "Asha Rai", "id_photo": "file:abc"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return e, cs, &now
}

func TestLivenessPassesWithFreshChallenge(t *testing.T) {
	v := &fakeLiveness{res: LivenessResult{Live: true, Score: 0.97}}
	e, c, _ := kycCase(t, Node{Name: "live", Kind: NodeLiveness, Challenges: 3}, v)
	ctx := context.Background()

	if _, err := e.NodeAct(ctx, c, applicant, "liveness", "live", "verify", NodeInput{Result: map[string]any{"nonce": "x"}}); err == nil {
		t.Fatal("verify without a challenge must fail")
	}
	c, err := e.NodeAct(ctx, c, applicant, "liveness", "live", "challenge", NodeInput{})
	if err != nil {
		t.Fatal(err)
	}
	ch := c.Stages["liveness"].Nodes["live"].Challenge
	if ch == nil || len(ch.Actions) != 3 || ch.Nonce == "" {
		t.Fatalf("bad challenge %+v", ch)
	}
	seen := map[string]bool{}
	for _, a := range ch.Actions {
		if seen[a] {
			t.Fatalf("challenge repeats %q", a)
		}
		seen[a] = true
	}
	c, err = e.NodeAct(ctx, c, applicant, "liveness", "live", "verify", NodeInput{Result: map[string]any{"nonce": ch.Nonce, "frames": []any{"f1", "f2"}}})
	if err != nil {
		t.Fatal(err)
	}
	ns := c.Stages["liveness"].Nodes["live"]
	if ns.Status != NodePassed || ns.Challenge != nil {
		t.Fatalf("want passed and spent challenge, got %s %+v", ns.Status, ns.Challenge)
	}
	if _, leaked := v.last.Evidence["nonce"]; leaked || v.last.Evidence["frames"] == nil || len(v.last.Actions) != 3 {
		t.Fatalf("verifier saw %+v", v.last)
	}
	if _, err := e.NodeAct(ctx, c, applicant, "liveness", "live", "challenge", NodeInput{}); err == nil {
		t.Fatal("a passed check cannot be re-run")
	}
}

func TestLivenessChallengeIsSingleUseExpiringAndBoundToActor(t *testing.T) {
	v := &fakeLiveness{res: LivenessResult{Live: false, Score: 0.2}}
	e, c, now := kycCase(t, Node{Name: "live", Kind: NodeLiveness, MaxAttempts: 2, ChallengeTTL: "1m"}, v)
	ctx := context.Background()
	issue := func() string {
		var err error
		c, err = e.NodeAct(ctx, c, applicant, "liveness", "live", "challenge", NodeInput{})
		if err != nil {
			t.Fatal(err)
		}
		return c.Stages["liveness"].Nodes["live"].Challenge.Nonce
	}

	nonce := issue()
	if _, err := e.NodeAct(ctx, c, applicant, "liveness", "live", "verify", NodeInput{Result: map[string]any{"nonce": "wrong"}}); err == nil || v.calls != 0 {
		t.Fatalf("wrong nonce must be rejected without scoring: %v", err)
	}
	// A failed attempt spends the challenge: replaying it is refused.
	c, err := e.NodeAct(ctx, c, applicant, "liveness", "live", "verify", NodeInput{Result: map[string]any{"nonce": nonce}})
	if err != nil {
		t.Fatal(err)
	}
	if ns := c.Stages["liveness"].Nodes["live"]; ns.Status != NodeInProgress || ns.Attempts != 1 {
		t.Fatalf("after one miss: %s attempts=%d", ns.Status, ns.Attempts)
	}
	if _, err := e.NodeAct(ctx, c, applicant, "liveness", "live", "verify", NodeInput{Result: map[string]any{"nonce": nonce}}); err == nil {
		t.Fatal("a spent challenge must not verify again")
	}

	// Expiry.
	nonce = issue()
	*now = now.Add(2 * time.Minute)
	if _, err := e.NodeAct(ctx, c, applicant, "liveness", "live", "verify", NodeInput{Result: map[string]any{"nonce": nonce}}); err == nil {
		t.Fatal("an expired challenge must not verify")
	}

	// Another person cannot use the applicant's challenge.
	nonce = issue()
	other := Actor{ID: "u-other", Roles: []string{"officer"}}
	if _, err := e.NodeAct(ctx, c, other, "liveness", "live", "verify", NodeInput{Result: map[string]any{"nonce": nonce}}); err == nil {
		t.Fatal("someone else must not verify the challenge")
	}

	// Second failed attempt exhausts MaxAttempts.
	c, err = e.NodeAct(ctx, c, applicant, "liveness", "live", "verify", NodeInput{Result: map[string]any{"nonce": nonce}})
	if err != nil {
		t.Fatal(err)
	}
	if ns := c.Stages["liveness"].Nodes["live"]; ns.Status != NodeFailed {
		t.Fatalf("want failed after max attempts, got %s", ns.Status)
	}
	if _, err := e.NodeAct(ctx, c, applicant, "liveness", "live", "challenge", NodeInput{}); err == nil {
		t.Fatal("no more challenges after the attempt limit")
	}
}

func TestLivenessThresholdsMatchAndVerifierOutage(t *testing.T) {
	low := 0.4
	v := &fakeLiveness{res: LivenessResult{Live: true, Score: 0.99, MatchScore: &low}}
	e, c, _ := kycCase(t, Node{Name: "live", Kind: NodeLiveness, MatchWith: "who.id_photo", MinMatch: 0.8}, v)
	ctx := context.Background()
	ask := func() string {
		var err error
		c, err = e.NodeAct(ctx, c, applicant, "liveness", "live", "challenge", NodeInput{})
		if err != nil {
			t.Fatal(err)
		}
		return c.Stages["liveness"].Nodes["live"].Challenge.Nonce
	}

	c, err := e.NodeAct(ctx, c, applicant, "liveness", "live", "verify", NodeInput{Result: map[string]any{"nonce": ask()}})
	if err != nil {
		t.Fatal(err)
	}
	if v.last.Reference != "file:abc" {
		t.Fatalf("verifier should get the reference portrait, got %v", v.last.Reference)
	}
	if ns := c.Stages["liveness"].Nodes["live"]; ns.Status == NodePassed {
		t.Fatalf("a face that does not match must not pass: %+v", ns.Result)
	}

	// An outage refunds the attempt and leaves the state retryable.
	v.err = errors.New("model down")
	before := c.Stages["liveness"].Nodes["live"].Attempts
	if _, err := e.NodeAct(ctx, c, applicant, "liveness", "live", "verify", NodeInput{Result: map[string]any{"nonce": ask()}}); err == nil {
		t.Fatal("expected the outage to surface")
	}
	if got := c.Stages["liveness"].Nodes["live"].Attempts; got != before {
		t.Fatalf("outage consumed an attempt: %d -> %d", before, got)
	}

	// Good match passes.
	v.err = nil
	high := 0.93
	v.res.MatchScore = &high
	c, err = e.NodeAct(ctx, c, applicant, "liveness", "live", "verify", NodeInput{Result: map[string]any{"nonce": ask()}})
	if err != nil {
		t.Fatal(err)
	}
	if ns := c.Stages["liveness"].Nodes["live"]; ns.Status != NodePassed {
		t.Fatalf("want passed, got %s %+v", ns.Status, ns.Result)
	}
}

func TestLivenessCompileValidation(t *testing.T) {
	if _, err := Compile(kycDef(Node{Name: "live", Kind: NodeLiveness, MinScore: 1.5})); err == nil {
		t.Fatal("min_score above 1 must be rejected")
	}
	if _, err := Compile(kycDef(Node{Name: "live", Kind: NodeLiveness, ChallengeTTL: "soon"})); err == nil {
		t.Fatal("bad challenge_ttl must be rejected")
	}
}

func routedDef() *Definition {
	return &Definition{
		Name:   "routed",
		Inputs: []Input{{Name: "amount", Kind: KindInteger, Required: true}},
		Forms:  []Form{{Name: "req", Fields: []string{"amount"}}},
		Stages: []Stage{
			{Name: "intake", Public: true, Page: &Page{Groups: []Group{{Name: "g", Forms: []string{"req"}}}},
				Actions: []ActionSpec{{Name: "submit", Outcome: OutcomeAdvance}},
				Routes: []Route{
					{Name: "tiny", When: "req.amount < 100", To: RouteEnd},
					{Name: "big", When: "req.amount >= 10000", To: "director"},
				}},
			{Name: "officer", Roles: []string{"officer"}, Actions: []ActionSpec{{Name: "ok", Outcome: OutcomeAdvance}}},
			{Name: "director", Roles: []string{"director"}, Actions: []ActionSpec{{Name: "ok", Outcome: OutcomeAdvance}}},
		},
	}
}

func TestStageRoutesSwitchOnData(t *testing.T) {
	c, err := Compile(routedDef())
	if err != nil {
		t.Fatal(err)
	}
	e := NewEngine(c)
	e.Eval = bclEval{}
	run := func(amount int) *Case {
		cs, err := e.Start(context.Background(), applicant, StartOptions{})
		if err != nil {
			t.Fatal(err)
		}
		cs, err = e.Act(context.Background(), cs, applicant, "intake", "submit", ActInput{Data: map[string]any{"req": map[string]any{"amount": amount}}})
		if err != nil {
			t.Fatal(err)
		}
		return cs
	}
	if cs := run(50); cs.Status != CaseCompleted || cs.Stages["officer"].Status != StageSkipped || cs.Stages["director"].Status != StageSkipped {
		t.Fatalf("route to end skips every later stage: status %s officer=%s director=%s", cs.Status, cs.Stages["officer"].Status, cs.Stages["director"].Status)
	}
	if cs := run(20000); cs.Stages["director"].Status != StageActive || cs.Stages["officer"].Status != StageSkipped {
		t.Fatalf("big amounts skip the officer: director=%s officer=%s", cs.Stages["director"].Status, cs.Stages["officer"].Status)
	}
	if cs := run(500); cs.Stages["officer"].Status != StageActive {
		t.Fatalf("no route matches, so the default next stage applies: %s", cs.Stages["officer"].Status)
	}
	bad := routedDef()
	bad.Stages[0].Routes[1].To = "nowhere"
	if _, err := Compile(bad); err == nil {
		t.Fatal("a route to an unknown stage must not compile")
	}
}
