package pipeline

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

func entriesDef(spec *EntriesSpec) *Definition {
	return &Definition{
		Name:       "entries",
		AdminRoles: []string{"admin"},
		Inputs:     []Input{{Name: "title", Kind: KindText, Required: true}, {Name: "score", Kind: KindInteger}, {Name: "note", Kind: KindText}},
		Forms:      []Form{{Name: "e", Fields: []string{"title", "score", "note"}}, {Name: "agent", Inputs: []Input{{Name: "who", Kind: KindText}, {Name: "ok", Kind: KindBoolean}}}},
		Stages: []Stage{
			{Name: "enter", Roles: []string{"clerk"}, Entries: spec, Page: &Page{Groups: []Group{{Name: "g", Forms: []string{"e"}}}},
				Actions: []ActionSpec{{Name: "done", Outcome: OutcomeAdvance}},
				SLA:     &SLA{Duration: "2h", OnBreach: "notify"}},
			{Name: "after", Roles: []string{"clerk"}, Page: &Page{Groups: []Group{{Name: "g", Mode: ModeReadonly, Forms: []string{"e"}}}}, Actions: []ActionSpec{{Name: "ok", Outcome: OutcomeAdvance}}},
		},
	}
}

func entriesCase(t *testing.T, spec *EntriesSpec) (*Engine, *Case, *clock) {
	t.Helper()
	clk := &clock{}
	clk.set("2026-03-04T05:00:00Z")
	c, err := Compile(entriesDef(spec))
	if err != nil {
		t.Fatal(err)
	}
	e := NewEngine(c)
	e.Eval = bclEval{}
	e.Now = clk.now
	cs, err := e.Start(context.Background(), clerk1, StartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return e, cs, clk
}

func entry(title string) map[string]any {
	return map[string]any{"e": map[string]any{"title": title, "score": 3}}
}

func TestEntriesLimitsPerUserAndInTotal(t *testing.T) {
	ctx := context.Background()
	e, c, _ := entriesCase(t, &EntriesSpec{PerUser: 2, Min: 2, Max: 3, AllowDraft: true})
	var err error
	if c, _, err = e.SaveEntry(ctx, c, clerk1, "enter", "", entry("a"), false); err != nil {
		t.Fatal(err)
	}
	if c, _, err = e.SaveEntry(ctx, c, clerk1, "enter", "", entry("b"), true); err != nil {
		t.Fatal(err)
	}
	if _, _, err = e.SaveEntry(ctx, c, clerk1, "enter", "", entry("c"), true); err == nil || !strings.Contains(err.Error(), "at most 2") {
		t.Fatalf("per-person limit: %v", err)
	}
	if c, _, err = e.SaveEntry(ctx, c, clerk2, "enter", "", entry("d"), true); err != nil {
		t.Fatal(err)
	}
	if _, _, err = e.SaveEntry(ctx, c, clerk2, "enter", "", entry("e"), true); err == nil || !strings.Contains(err.Error(), "most entries") {
		t.Fatalf("overall cap: %v", err)
	}
	ev, _ := e.ViewEntry(c, clerk1, "enter", "")
	if ev.Entries.Remaining != 0 || ev.Entries.CanAdd || len(ev.Entries.Mine) != 2 || ev.Entries.Submitted != 2 || ev.Entries.Drafts != 1 {
		t.Fatalf("entries view: %+v", ev.Entries)
	}
}

func TestEntriesDraftEditAndSubmitRules(t *testing.T) {
	ctx := context.Background()
	// No drafts: an entry is submitted or not made.
	e, c, _ := entriesCase(t, &EntriesSpec{PerUser: 1})
	if _, _, err := e.SaveEntry(ctx, c, clerk1, "enter", "", entry("x"), false); err == nil || !strings.Contains(err.Error(), "drafts") {
		t.Fatalf("drafts off: %v", err)
	}
	if _, _, err := e.SaveEntry(ctx, c, clerk1, "enter", "", map[string]any{"e": map[string]any{"score": 1}}, true); err == nil {
		t.Fatal("a submitted entry must be complete")
	}
	c, en, err := e.SaveEntry(ctx, c, clerk1, "enter", "", entry("x"), true)
	if err != nil || en.Status != EntrySubmitted || en.SubmittedAt == nil {
		t.Fatalf("submit: %v %+v", err, en)
	}
	if _, _, err := e.SaveEntry(ctx, c, clerk1, "enter", en.ID, entry("y"), true); err == nil || !strings.Contains(err.Error(), "cannot be edited") {
		t.Fatalf("edit off: %v", err)
	}
	if _, err := e.Save(ctx, c, clerk1, "enter", entry("z")); err == nil || !strings.Contains(err.Error(), "collects entries") {
		t.Fatalf("the shared document is not editable on an entries stage: %v", err)
	}

	// Edits allowed: the owner may change a submitted entry, others may not.
	e, c, _ = entriesCase(t, &EntriesSpec{PerUser: 1, AllowEdit: true, AllowDraft: true})
	c, en, _ = e.SaveEntry(ctx, c, clerk1, "enter", "", entry("x"), true)
	c, en, err = e.SaveEntry(ctx, c, clerk1, "enter", en.ID, entry("x2"), false)
	if err != nil || en.Status != EntrySubmitted || en.Edits != 1 || en.Data["e"].(map[string]any)["title"] != "x2" {
		t.Fatalf("owner edit: %v %+v", err, en)
	}
	if _, _, err = e.SaveEntry(ctx, c, clerk2, "enter", en.ID, entry("hack"), true); !errors.Is(err, ErrForbidden) {
		t.Fatalf("only the owner may edit: %v", err)
	}
	if v, err := e.ViewEntry(c, clerk2, "enter", en.ID); err != nil || v.CanEdit {
		t.Fatalf("staff may read a submitted entry but not edit it: %v", err)
	}
	c, dr, _ := e.SaveEntry(ctx, c, clerk2, "enter", "", entry("private"), false)
	if _, err = e.ViewEntry(c, clerk1, "enter", dr.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a draft is private to its maker: %v", err)
	}
	if c, err = e.DeleteEntry(ctx, c, clerk1, "enter", en.ID); err != nil || len(c.Stages["enter"].Entries) != 1 {
		t.Fatalf("delete: %v", err)
	}
}

func TestEntriesStageNeedsEnoughSubmittedEntriesThenMergesOne(t *testing.T) {
	ctx := context.Background()
	e, c, _ := entriesCase(t, &EntriesSpec{Min: 2, AllowDraft: true})
	c, _, _ = e.SaveEntry(ctx, c, clerk1, "enter", "", entry("first"), true)
	if _, err := e.Act(ctx, c, clerk1, "enter", "done", ActInput{}); err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("one entry is not enough: %v", err)
	}
	c, _, _ = e.SaveEntry(ctx, c, clerk2, "enter", "", entry("second"), true)
	c, _, _ = e.SaveEntry(ctx, c, clerk2, "enter", "", entry("draft only"), false)
	c, err := e.Act(ctx, c, clerk1, "enter", "done", ActInput{})
	if err != nil || c.Stage != "after" {
		t.Fatalf("complete: %v", err)
	}
	if got, _ := c.Get("e.title"); got != "second" {
		t.Fatalf("the last submitted entry becomes the case data (drafts never do): %v", got)
	}
	o, _ := e.Overview(c, admin)
	if len(o.EntryRows) != 2 || o.EntryRows[0].Status != EntrySubmitted {
		t.Fatalf("staff see everyone's submitted entries: %+v", o.EntryRows)
	}
	mine, _ := e.Overview(c, clerk2)
	var drafts int
	for _, r := range mine.EntryRows {
		if r.Status == EntryDraft {
			drafts++
		}
	}
	if drafts != 1 {
		t.Fatalf("a person sees their own drafts: %+v", mine.EntryRows)
	}

	e, c, _ = entriesCase(t, &EntriesSpec{Min: 1, Merge: "first", AllowDraft: true})
	c, _, _ = e.SaveEntry(ctx, c, clerk1, "enter", "", entry("one"), true)
	c, _, _ = e.SaveEntry(ctx, c, clerk2, "enter", "", entry("two"), true)
	c, _ = e.Act(ctx, c, clerk1, "enter", "done", ActInput{})
	if got, _ := c.Get("e.title"); got != "one" {
		t.Fatalf("merge first: %v", got)
	}
}

func TestEntriesStageCompileChecks(t *testing.T) {
	for name, spec := range map[string]*EntriesSpec{
		"max below min": {Min: 3, Max: 2}, "bad merge": {Merge: "newest"}, "negative": {PerUser: -1},
	} {
		if _, err := Compile(entriesDef(spec)); err == nil {
			t.Errorf("%s must not compile", name)
		}
	}
	d := entriesDef(&EntriesSpec{})
	d.Stages[0].Page = nil
	if _, err := Compile(d); err == nil {
		t.Error("entries need a page to enter data in")
	}
}

func TestSLAAppliesToEntriesAndPublicStages(t *testing.T) {
	ctx := context.Background()
	e, c, clk := entriesCase(t, &EntriesSpec{Min: 3, AllowDraft: true})
	c, _, _ = e.SaveEntry(ctx, c, clerk1, "enter", "", entry("one"), true)
	c, _, _ = e.SaveEntry(ctx, c, clerk1, "enter", "", entry("draft"), false)
	clk.add(3 * time.Hour)
	c, res, _ := e.Sweep(ctx, c)
	b := c.Stages["enter"].SLA.Breach
	if !res.Changed || b == nil || !strings.Contains(b.Reason, "1 of 3 required entries were submitted (1 still drafts)") {
		t.Fatalf("an entries stage explains how far it got: %+v", b)
	}

	// An applicant stage with on_breach expire closes the case when nobody answers.
	d := &Definition{Name: "pub", Inputs: []Input{{Name: "a", Kind: KindText}}, Forms: []Form{{Name: "f", Fields: []string{"a"}}},
		Stages: []Stage{{Name: "ask", Public: true, Page: &Page{Groups: []Group{{Name: "g", Forms: []string{"f"}}}}, Actions: []ActionSpec{{Name: "go", Outcome: OutcomeAdvance}},
			SLA: &SLA{Duration: "1d", OnBreach: "expire"}}}}
	cp, err := Compile(d)
	if err != nil {
		t.Fatal(err)
	}
	pe := NewEngine(cp)
	pe.Eval = bclEval{}
	pe.Now = clk.now
	pc, _ := pe.Start(ctx, applicant, StartOptions{})
	clk.add(25 * time.Hour)
	pc, _, err = pe.Sweep(ctx, pc)
	if err != nil || pc.Status != CaseWithdrawn || pc.ClosedAt == nil {
		t.Fatalf("expire: %v %s", err, pc.Status)
	}
	rec := pc.Breaches[0]
	if rec.Cause != "applicant_no_response" || rec.Resolution != "expire" || !strings.Contains(eventNames(pc), "case.expired") {
		t.Fatalf("expiry record: %+v events=%s", rec, eventNames(pc))
	}
}

func externalDef() *Definition {
	return &Definition{
		Name: "kyc", AdminRoles: []string{"admin"},
		Inputs: []Input{{Name: "agent", Kind: KindText}, {Name: "verdict", Kind: KindText, Required: true}},
		Forms:  []Form{{Name: "req", Fields: []string{"agent"}}, {Name: "kyc", Fields: []string{"verdict"}}},
		Stages: []Stage{
			{Name: "request", Public: true, Page: &Page{Groups: []Group{{Name: "g", Forms: []string{"req"}}}}, Actions: []ActionSpec{{Name: "go", Outcome: OutcomeAdvance}}},
			{Name: "verify", ViewRoles: []string{"clerk"}, Page: &Page{Groups: []Group{{Name: "g", Forms: []string{"kyc"}}}},
				External: &ExternalSpec{Party: "KYC agent", AssigneeFrom: "req.agent", TTL: "3d"},
				SLA:      &SLA{Duration: "1d", OnBreach: "notify"}},
			{Name: "done", Roles: []string{"clerk"}, Actions: []ActionSpec{{Name: "ok", Outcome: OutcomeAdvance}}},
		},
	}
}

func TestExternalStageIssuesALinkAndCompletesWhenThePartySubmits(t *testing.T) {
	ctx := context.Background()
	clk := &clock{}
	clk.set("2026-03-04T05:00:00Z")
	comp, err := Compile(externalDef())
	if err != nil {
		t.Fatal(err)
	}
	e := NewEngine(comp)
	e.Eval, e.Now, e.SigningKey = bclEval{}, clk.now, []byte("0123456789abcdef0123456789abcdef")
	c, _ := e.Start(ctx, applicant, StartOptions{})
	c, err = e.Act(ctx, c, applicant, "request", "go", ActInput{Data: map[string]any{"req": map[string]any{"agent": "Hari Shrestha"}}})
	if err != nil || c.Stage != "verify" {
		t.Fatalf("enter the external stage: %v", err)
	}
	var token string
	for _, ev := range c.Events() {
		if ev.Name == "link.issued" {
			token, _ = ev.Detail["token"].(string)
			if ev.Detail["party"] != "Hari Shrestha" {
				t.Fatalf("the person named in the data is the party: %v", ev.Detail)
			}
		}
	}
	if token == "" {
		t.Fatal("link.issued must carry the token so a hook can send it")
	}
	// Staff see who it waits for; only the people who run the stage get the token.
	clerk := Actor{ID: "k", Roles: []string{"clerk"}}
	if v, _ := e.View(c, clerk, "verify"); v.Party != nil && v.Party.Token != "" {
		t.Fatalf("a viewer with no part in the stage does not get the link: %+v", v.Party)
	}
	v, err := e.View(c, admin, "verify")
	if err != nil || v.Party == nil || v.Party.Status != "waiting" || v.Party.Token != token || !slices.Contains(v.Work.Operations, "reissue") {
		t.Fatalf("admin view of the party: %v %+v", err, v)
	}
	if o, _ := e.Overview(c, admin); o.Party == nil || o.Party.Party != "Hari Shrestha" {
		t.Fatalf("overview party: %+v", o.Party)
	}

	// Reissuing kills the old link.
	c2, err := e.ReissueExternal(ctx, c, admin, "verify")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.LinkActor(c2, token); err == nil {
		t.Fatal("the old link must stop working")
	}
	var fresh string
	for _, ev := range c2.Events() {
		if ev.Name == "link.issued" {
			fresh, _ = ev.Detail["token"].(string)
		}
	}
	actor, err := e.LinkActor(c2, fresh)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.LinkSubmit(ctx, c2, actor, map[string]any{"kyc": map[string]any{}}); err == nil {
		t.Fatal("required inputs apply to the outside party too")
	}
	done, err := e.LinkSubmit(ctx, c2, actor, map[string]any{"kyc": map[string]any{"verdict": "verified"}})
	if err != nil || done.Stage != "done" || done.Stages["verify"].Status != StageCompleted {
		t.Fatalf("the party's submission completes the stage: %v stage=%s", err, done.Stage)
	}
	if got, _ := done.Get("kyc.verdict"); got != "verified" {
		t.Fatalf("data: %v", got)
	}
	if _, err := e.LinkActor(done, fresh); err == nil {
		t.Fatal("a link is single-use")
	}

	// SLA on the external stage: it says who never answered.
	clk.add(25 * time.Hour)
	c3, _, _ := e.Sweep(ctx, c2)
	if b := c3.Stages["verify"].SLA.Breach; b == nil || b.Cause != "external_no_response" || !strings.Contains(b.Reason, "Hari Shrestha") {
		t.Fatalf("external breach: %+v", b)
	}
}
