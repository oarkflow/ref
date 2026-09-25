package pipeline

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/oarkflow/bcl"
)

type bclEval struct{}

func (bclEval) Eval(expr string, env map[string]any) (any, error) {
	expr = strings.ReplaceAll(strings.ReplaceAll(expr, "&&", " and "), "||", " or ")
	prog, err := bcl.CompileExpression(expr)
	if err != nil {
		return nil, err
	}
	return prog.Eval(env, &bcl.EvalOptions{Variables: env})
}

type fakeAutomation struct{ pass bool }

func (f fakeAutomation) RunNode(_ context.Context, hook string, c *Case, _, _ string) (map[string]any, bool, error) {
	return map[string]any{"hook": hook, "watchlist": "clear"}, f.pass, nil
}

func passportDef() *Definition {
	return &Definition{
		Name:         "passport",
		Title:        "Passport Request",
		NumberFormat: "PP-{year}-{seq:6}",
		RevealRoles:  []string{"supervisor"},
		Inputs: []Input{
			{Name: "full_name", Label: "Full name", Kind: KindText, Required: true, MinLength: 3},
			{Name: "dob", Label: "Date of birth", Kind: KindDate, Required: true, Max: "2020-01-01"},
			{Name: "national_id", Label: "National ID", Kind: KindText, Required: true, Pattern: `^[0-9]{10}$`, Sensitive: true},
			{Name: "email", Kind: KindEmail, Required: true},
		},
		Forms: []Form{
			{Name: "applicant", Title: "Applicant", Fields: []string{"full_name", "dob", "national_id", "email"}},
			{Name: "request", Title: "Request", Inputs: []Input{
				{Name: "kind", Kind: KindSelect, Options: []string{"new", "renewal"}, Required: true},
				{Name: "old_passport", Kind: KindText, RequiredIf: "request.kind == 'renewal'", VisibleIf: "request.kind == 'renewal'"},
				{Name: "pages", Kind: KindInteger, Min: "32", Max: "64"},
			}},
			{Name: "addresses", Title: "Addresses", Repeatable: true, MinItems: 1, MaxItems: 3, Inputs: []Input{
				{Name: "line", Kind: KindText, Required: true},
				{Name: "city", Kind: KindText, Required: true},
			}},
		},
		Certificates: []CertificateSpec{{Name: "approval", Title: "Passport approval", NumberFormat: "PPA-{year}-{seq:4}", Fields: []string{"applicant.full_name", "applicant.dob", "request.kind"}, Validity: "87600h"}},
		Stages: []Stage{
			{Name: "submission", Title: "Application", Kind: "submission", Public: true,
				Page: &Page{Layout: LayoutWizard, Groups: []Group{
					{Name: "who", Title: "About you", Forms: []string{"applicant"}},
					{Name: "what", Title: "Your request", Forms: []string{"request", "addresses"}},
				}},
				Actions: []ActionSpec{{Name: "submit", Outcome: OutcomeAdvance}, {Name: "withdraw", Outcome: OutcomeWithdraw}},
			},
			{Name: "verification", Title: "Document verification", Kind: "review", Roles: []string{"officer"},
				Forms: []Form{{Name: "officer_notes", Inputs: []Input{{Name: "notes", Kind: KindTextarea}}}},
				Page: &Page{Layout: LayoutTabbed, Groups: []Group{
					{Name: "submitted", Title: "Submitted", Mode: ModeReadonly, Forms: []string{"applicant", "request", "addresses"}},
					{Name: "notes", Title: "Notes", Forms: []string{"officer_notes"}},
				}},
				Nodes: []Node{
					{Name: "review_identity", Kind: NodeReview, Forms: []string{"applicant"}},
					{Name: "age_check", Kind: NodeCheck, Check: "applicant.dob != ''"},
					{Name: "watchlist", Kind: NodeAutomated, Hook: "watchlist.screen"},
				},
				Actions: []ActionSpec{
					{Name: "accept", Outcome: OutcomeAdvance},
					{Name: "return", Outcome: OutcomeReturn, ReturnTo: "submission", CommentRequired: true},
					{Name: "reject", Outcome: OutcomeReject, CommentRequired: true},
				},
				Assign: []Assignment{{Path: "decision.verified_by", Value: "actor.id"}},
			},
			{Name: "approval", Title: "Approval", Kind: "approval", Roles: []string{"supervisor"},
				Nodes:   []Node{{Name: "sign_off", Kind: NodeApproval, Approvals: 2, DistinctFrom: []string{"review_identity", "applicant"}}},
				Actions: []ActionSpec{{Name: "approve", Outcome: OutcomeAdvance}},
			},
			{Name: "issuance", Title: "Issuance", Kind: "issuance", Roles: []string{"officer"}, SkipIf: "request.pages == 99",
				Nodes: []Node{{Name: "issue", Kind: NodeCertificate, Certificate: "approval"}}, AutoAdvance: true},
		},
	}
}

func newEngine(t *testing.T, pass bool) *Engine {
	t.Helper()
	c, err := Compile(passportDef())
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	e := NewEngine(c)
	e.Eval = bclEval{}
	e.Automation = fakeAutomation{pass: pass}
	e.SigningKey = []byte("test-signing-key-0123456789abcdef")
	clock := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	e.Now = func() time.Time { clock = clock.Add(time.Minute); return clock }
	return e
}

var (
	applicant = Actor{ID: "u-applicant"}
	officer   = Actor{ID: "u-officer", Roles: []string{"officer"}}
	super1    = Actor{ID: "u-super1", Roles: []string{"supervisor"}}
	super2    = Actor{ID: "u-super2", Roles: []string{"supervisor"}}
)

func goodData() map[string]any {
	return map[string]any{
		"applicant": map[string]any{"full_name": "Asha Rai", "dob": "1990-05-01", "national_id": "1234567890", "email": "asha@example.com"},
		"request":   map[string]any{"kind": "renewal", "old_passport": "P1234567", "pages": 32},
		"addresses": []any{map[string]any{"line": "Ward 4", "city": "Kathmandu"}},
	}
}

func mustFields(t *testing.T, err error, want ...string) {
	t.Helper()
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected a ValidationError, got %v", err)
	}
	got := map[string]bool{}
	for _, f := range ve.Fields {
		got[f.Path] = true
	}
	for _, w := range want {
		if !got[w] {
			t.Fatalf("expected error on %s, got %+v", w, ve.Fields)
		}
	}
}

func TestCompileRejectsBadDefinitions(t *testing.T) {
	bad := func(mutate func(*Definition)) error {
		d := passportDef()
		mutate(d)
		_, err := Compile(d)
		return err
	}
	for name, mutate := range map[string]func(*Definition){
		"unknown catalog input": func(d *Definition) { d.Forms[0].Fields = append(d.Forms[0].Fields, "nope") },
		"group unknown form":    func(d *Definition) { d.Stages[0].Page.Groups[0].Forms = []string{"ghost"} },
		"bad mode":              func(d *Definition) { d.Stages[0].Page.Groups[0].Mode = "sideways" },
		"bad layout":            func(d *Definition) { d.Stages[0].Page.Layout = "spiral" },
		"return to unknown":     func(d *Definition) { d.Stages[1].Actions[1].ReturnTo = "nowhere" },
		"unknown certificate":   func(d *Definition) { d.Stages[3].Nodes[0].Certificate = "x" },
		"select without options": func(d *Definition) {
			d.Forms[1].Inputs[0].Options = nil
		},
		"bad pattern":         func(d *Definition) { d.Inputs[2].Pattern = "([" },
		"duplicate stage":     func(d *Definition) { d.Stages[1].Name = "submission" },
		"review without form": func(d *Definition) { d.Stages[1].Nodes[0].Forms = nil },
	} {
		if err := bad(mutate); err == nil {
			t.Errorf("%s: expected a compile error", name)
		}
	}
}

func TestPassportHappyPathWithCorrectionLoop(t *testing.T) {
	ctx := context.Background()
	e := newEngine(t, true)
	store := NewMemoryStore()

	seq, _ := store.NextSeq(ctx, "passport")
	c, err := e.Start(ctx, applicant, StartOptions{Number: FormatNumber(e.C.Def.NumberFormat, seq, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))})
	if err != nil {
		t.Fatal(err)
	}
	if c.Number != "PP-2026-000001" || c.Status != CaseDraft || c.Stage != "submission" {
		t.Fatalf("start: %+v", c)
	}
	if err := store.Create(ctx, c); err != nil {
		t.Fatal(err)
	}

	// Invalid data is rejected field by field and nothing is written.
	_, err = e.Save(ctx, c, applicant, "submission", map[string]any{
		"applicant": map[string]any{"full_name": "A", "dob": "2025-01-01", "national_id": "12", "email": "nope"},
		"request":   map[string]any{"kind": "express", "pages": 12},
	})
	mustFields(t, err, "applicant.full_name", "applicant.dob", "applicant.national_id", "applicant.email", "request.kind", "request.pages")

	// Submitting an empty application lists every missing field.
	_, err = e.Act(ctx, c, applicant, "submission", "submit", ActInput{})
	mustFields(t, err, "applicant.full_name", "request.kind", "addresses")

	// Officers cannot write the applicant's page; a stranger cannot either.
	if _, err := e.Save(ctx, c, officer, "submission", goodData()); !errors.Is(err, ErrForbidden) {
		t.Fatalf("officer edit of submission: %v", err)
	}

	c, err = e.Act(ctx, c, applicant, "submission", "submit", ActInput{Data: goodData()})
	if err != nil {
		t.Fatal(err)
	}
	if c.Stage != "verification" || c.Status != CaseInProgress {
		t.Fatalf("after submit: stage=%s status=%s", c.Stage, c.Status)
	}
	vs := c.Stages["verification"]
	if vs.Nodes["watchlist"].Status != NodePassed || vs.Nodes["age_check"].Status != NodePassed || vs.Nodes["review_identity"].Status != NodePending {
		t.Fatalf("nodes on entry: %+v %+v %+v", vs.Nodes["watchlist"], vs.Nodes["age_check"], vs.Nodes["review_identity"])
	}

	// The applicant can no longer edit anything once submitted.
	if _, err := e.Save(ctx, c, applicant, "submission", goodData()); !errors.Is(err, ErrState) {
		t.Fatalf("edit after submit: %v", err)
	}

	// Officer view: submitted group is readonly, sensitive id masked, notes editable.
	v, err := e.View(c, officer, "")
	if err != nil {
		t.Fatal(err)
	}
	if v.Page.Layout != LayoutTabbed || v.Page.Groups[0].Mode != ModeReadonly || v.Page.Groups[1].Mode != ModeEditable {
		t.Fatalf("officer page modes: %+v", v.Page.Groups)
	}
	var idView InputView
	for _, in := range v.Page.Groups[0].Forms[0].Inputs {
		if in.Name == "national_id" {
			idView = in
		}
	}
	if !idView.Masked || idView.Value != "••••••7890" || idView.Editable {
		t.Fatalf("masking: %+v", idView)
	}

	// The officer cannot accept while the review is open.
	if _, err := e.Act(ctx, c, officer, "verification", "accept", ActInput{}); !errors.Is(err, ErrState) {
		t.Fatalf("accept with open review: %v", err)
	}
	// Flag the date of birth, verify the rest.
	c, err = e.NodeAct(ctx, c, officer, "verification", "review_identity", "verify", NodeInput{Verdicts: map[string]VerdictInput{
		"applicant.full_name": {Status: VerdictVerified}, "applicant.national_id": {Status: VerdictVerified},
		"applicant.email": {Status: VerdictVerified}, "applicant.dob": {Status: VerdictFlagged, Comment: "does not match the ID"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	c, err = e.NodeAct(ctx, c, officer, "verification", "review_identity", "complete", NodeInput{})
	if err != nil {
		t.Fatal(err)
	}
	if c.Stages["verification"].Nodes["review_identity"].Status != NodeFailed {
		t.Fatalf("review with a flag should fail")
	}
	if _, err := e.Act(ctx, c, officer, "verification", "return", ActInput{}); err == nil {
		t.Fatal("return without comment must fail")
	}
	c, err = e.Act(ctx, c, officer, "verification", "return", ActInput{Comment: "please fix your date of birth"})
	if err != nil {
		t.Fatal(err)
	}
	if c.Stage != "submission" || c.Status != CaseReturned || len(c.Stages["submission"].Flags) != 1 {
		t.Fatalf("after return: %s %s %+v", c.Stage, c.Status, c.Stages["submission"].Flags)
	}

	// Correction mode: only the flagged input is editable.
	v, _ = e.View(c, applicant, "")
	editable := []string{}
	for _, g := range v.Page.Groups {
		for _, f := range g.Forms {
			for _, in := range f.Inputs {
				if in.Editable {
					editable = append(editable, in.Path)
				}
			}
		}
	}
	if len(editable) != 1 || editable[0] != "applicant.dob" || len(v.Corrections) != 1 {
		t.Fatalf("correction editable = %v corrections=%v", editable, v.Corrections)
	}
	// An attempt to sneak in other changes is ignored.
	c, err = e.Act(ctx, c, applicant, "submission", "submit", ActInput{Data: map[string]any{
		"applicant": map[string]any{"dob": "1990-06-01", "full_name": "Someone Else"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if name, _ := c.Get("applicant.full_name"); name != "Asha Rai" {
		t.Fatalf("non-flagged field changed during correction: %v", name)
	}
	if dob, _ := c.Get("applicant.dob"); dob != "1990-06-01" {
		t.Fatalf("flagged field not updated: %v", dob)
	}
	// Straight back to verification; verified verdicts kept, flagged one reset.
	rv := c.Stages["verification"].Nodes["review_identity"]
	if c.Stage != "verification" || len(rv.Verdicts) != 3 || rv.Status != NodePending {
		t.Fatalf("re-review state: stage=%s %+v", c.Stage, rv)
	}
	c, err = e.NodeAct(ctx, c, officer, "verification", "review_identity", "verify", NodeInput{Verdicts: map[string]VerdictInput{"applicant.dob": {Status: VerdictVerified}}})
	if err != nil {
		t.Fatal(err)
	}
	if c, err = e.NodeAct(ctx, c, officer, "verification", "review_identity", "complete", NodeInput{}); err != nil {
		t.Fatal(err)
	}
	c, err = e.Act(ctx, c, officer, "verification", "accept", ActInput{Data: map[string]any{"officer_notes": map[string]any{"notes": "documents match"}}})
	if err != nil {
		t.Fatal(err)
	}
	if c.Stage != "approval" {
		t.Fatalf("stage after accept: %s", c.Stage)
	}
	if by, _ := c.Get("decision.verified_by"); by != officer.ID {
		t.Fatalf("assign did not run: %v", by)
	}

	// Four-eyes and two distinct approvers.
	if _, err := e.NodeAct(ctx, c, Actor{ID: officer.ID, Roles: []string{"supervisor"}}, "approval", "sign_off", "approve", NodeInput{}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("reviewer approving own case: %v", err)
	}
	c, err = e.NodeAct(ctx, c, super1, "approval", "sign_off", "approve", NodeInput{Comment: "ok"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.NodeAct(ctx, c, super1, "approval", "sign_off", "approve", NodeInput{}); !errors.Is(err, ErrState) {
		t.Fatalf("double approval: %v", err)
	}
	if _, err := e.Act(ctx, c, super1, "approval", "approve", ActInput{}); !errors.Is(err, ErrState) {
		t.Fatalf("stage approve with 1/2 approvals: %v", err)
	}
	if c, err = e.NodeAct(ctx, c, super2, "approval", "sign_off", "approve", NodeInput{}); err != nil {
		t.Fatal(err)
	}
	c, err = e.Act(ctx, c, super2, "approval", "approve", ActInput{})
	if err != nil {
		t.Fatal(err)
	}
	// Issuance auto-advances once the officer issues the certificate.
	if c.Stage != "issuance" {
		t.Fatalf("stage after approval: %s", c.Stage)
	}
	c, err = e.NodeAct(ctx, c, officer, "issuance", "issue", "issue", NodeInput{})
	if err != nil {
		t.Fatal(err)
	}
	if c.Status != CaseCompleted || len(c.Certificates) != 1 {
		t.Fatalf("final: status=%s certs=%d", c.Status, len(c.Certificates))
	}
	cert := c.Certificates[0]
	if !strings.HasPrefix(cert.Number, "PPA-2026-") || cert.Subject["applicant.full_name"] != "Asha Rai" || !e.Verify(cert).Valid {
		t.Fatalf("certificate: %+v %+v", cert, e.Verify(cert))
	}
	tampered := cert
	tampered.Subject = map[string]any{"applicant.full_name": "Mallory"}
	if e.Verify(tampered).Valid {
		t.Fatal("tampered certificate verified")
	}
	other := NewEngine(e.C)
	other.SigningKey = []byte("some-other-key-0000000000000000")
	if other.Verify(cert).Valid {
		t.Fatal("certificate verified under the wrong key")
	}

	// History tells the whole story.
	var actions []string
	for _, h := range c.History {
		actions = append(actions, h.Action)
	}
	joined := strings.Join(actions, ",")
	for _, want := range []string{"start", "submit", "verify", "return", "approve", "certificate_issued"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("history missing %s: %s", want, joined)
		}
	}
}

func TestSkipConditionalAndRejectAndWithdraw(t *testing.T) {
	ctx := context.Background()
	e := newEngine(t, false)
	c, _ := e.Start(ctx, applicant, StartOptions{})
	data := goodData()
	data["request"] = map[string]any{"kind": "new"} // old_passport hidden & not required
	c, err := e.Act(ctx, c, applicant, "submission", "submit", ActInput{Data: data})
	if err != nil {
		t.Fatal(err)
	}
	// Automation failed: the stage cannot be accepted, but it can be rejected.
	if c.Stages["verification"].Nodes["watchlist"].Status != NodeFailed {
		t.Fatal("automation should fail")
	}
	c, err = e.Act(ctx, c, officer, "verification", "reject", ActInput{Comment: "watchlist hit"})
	if err != nil || c.Status != CaseRejected {
		t.Fatalf("reject: %v %s", err, c.Status)
	}
	if _, err := e.Save(ctx, c, applicant, "submission", data); !errors.Is(err, ErrState) {
		t.Fatalf("terminal case edit: %v", err)
	}

	c2, _ := e.Start(ctx, applicant, StartOptions{})
	if _, err := e.Act(ctx, c2, officer, "submission", "withdraw", ActInput{}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("stranger withdraw: %v", err)
	}
	c2, err = e.Act(ctx, c2, applicant, "submission", "withdraw", ActInput{})
	if err != nil || c2.Status != CaseWithdrawn {
		t.Fatalf("withdraw: %v", err)
	}
}

func TestStoresOptimisticConcurrency(t *testing.T) {
	ctx := context.Background()
	e := newEngine(t, true)
	store := NewMemoryStore()
	c, _ := e.Start(ctx, applicant, StartOptions{})
	_ = store.Create(ctx, c)
	a, _ := store.Get(ctx, c.ID)
	b, _ := store.Get(ctx, c.ID)
	a2, _ := e.Save(ctx, a, applicant, "submission", goodData())
	if err := store.Update(ctx, a2); err != nil {
		t.Fatal(err)
	}
	b2, _ := e.Save(ctx, b, applicant, "submission", goodData())
	if err := store.Update(ctx, b2); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale update: %v", err)
	}
	list, _ := store.List(ctx, Query{Pipeline: "passport", Stages: []string{"submission"}})
	if len(list) != 1 {
		t.Fatalf("list = %d", len(list))
	}
}

func TestFormatNumber(t *testing.T) {
	now := time.Date(2026, 7, 4, 0, 0, 0, 0, time.UTC)
	if got := FormatNumber("PP-{year}{month}-{seq:5}", 42, now); got != "PP-202607-00042" {
		t.Fatal(got)
	}
	if got := FormatNumber("", 7, now); got != "2026-000007" {
		t.Fatal(got)
	}
}
