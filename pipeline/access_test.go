package pipeline

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

// accessDef is a one-stage claim with every kind of access rule: a form shown
// only for one category, a form only auditors may see, an input only managers
// may edit, an input disabled by a condition, and a state that depends on a
// country.
func accessDef() *Definition {
	return &Definition{
		Name: "claims",
		Forms: []Form{
			{Name: "claim", Inputs: []Input{
				{Name: "category", Kind: KindSelect, Options: []string{"A", "B"}},
				{Name: "amount", Kind: KindNumber, EditRoles: []string{"manager"}},
				{Name: "reason", Kind: KindText, DisabledIf: "claim.category == 'B'"},
				{Name: "internal", Kind: KindText, ViewRoles: []string{"auditor"}},
			}},
			{Name: "extra", VisibleIf: "claim.category == 'A'", Inputs: []Input{{Name: "detail", Kind: KindText, Required: true}}},
			{Name: "audit", ViewRoles: []string{"auditor"}, Inputs: []Input{{Name: "finding", Kind: KindText}}},
			{Name: "geo", Inputs: []Input{
				{Name: "country", Kind: KindSelect, Lookup: "countries"},
				{Name: "state", Kind: KindSelect, Lookup: "states", DependsOn: []string{"country"}},
			}},
			{Name: "locked", EditRoles: []string{"manager"}, Inputs: []Input{{Name: "code", Kind: KindText}}},
		},
		Stages: []Stage{{
			Name: "work", Roles: []string{"coder", "manager", "auditor"},
			Page: &Page{Groups: []Group{
				{Name: "main", Forms: []string{"claim", "extra", "audit", "geo", "locked"}},
			}},
		}},
	}
}

func accessEngine(t *testing.T, def *Definition) (*Engine, *Case) {
	t.Helper()
	compiled, err := Compile(def)
	if err != nil {
		t.Fatal(err)
	}
	e := NewEngine(compiled)
	e.Eval = bclEval{}
	e.Lookup = func(set string, _ *Case) []Option {
		if set == "countries" {
			return []Option{{Value: "US", Label: "US"}, {Value: "CA", Label: "Canada"}}
		}
		return nil
	}
	e.LookupFor = func(set string, _ *Case, deps map[string]any) []Option {
		if set != "states" {
			return nil
		}
		switch deps["country"] {
		case "US":
			return []Option{{Value: "NY", Label: "New York"}, {Value: "CA", Label: "California"}}
		case "CA":
			return []Option{{Value: "ON", Label: "Ontario"}}
		}
		return nil
	}
	c, err := e.Start(context.Background(), Actor{ID: "starter", Roles: []string{"coder"}}, StartOptions{Number: "C-1"})
	if err != nil {
		t.Fatal(err)
	}
	c.ID = "case_1"
	return e, c
}

func inputsOf(v *View, form string) map[string]InputView {
	out := map[string]InputView{}
	for _, g := range v.Page.Groups {
		for _, f := range g.Forms {
			if f.Name == form {
				for _, in := range f.Inputs {
					out[in.Name] = in
				}
			}
		}
	}
	return out
}

func hasForm(v *View, form string) bool {
	for _, g := range v.Page.Groups {
		for _, f := range g.Forms {
			if f.Name == form {
				return true
			}
		}
	}
	return false
}

func TestViewRolesHideFormsAndInputs(t *testing.T) {
	e, c := accessEngine(t, accessDef())
	coder := Actor{ID: "c1", Roles: []string{"coder"}}
	auditor := Actor{ID: "a1", Roles: []string{"auditor"}}

	cv, err := e.View(c, coder, "work")
	if err != nil {
		t.Fatal(err)
	}
	if hasForm(cv, "audit") {
		t.Error("coder must not see the audit form")
	}
	if _, ok := inputsOf(cv, "claim")["internal"]; ok {
		t.Error("coder must not see the auditor-only input")
	}
	av, err := e.View(c, auditor, "work")
	if err != nil {
		t.Fatal(err)
	}
	if !hasForm(av, "audit") {
		t.Error("auditor sees the audit form")
	}
	if _, ok := inputsOf(av, "claim")["internal"]; !ok {
		t.Error("auditor sees the auditor-only input")
	}
}

func TestEditRolesLimitWrites(t *testing.T) {
	e, c := accessEngine(t, accessDef())
	ctx := context.Background()
	coder := Actor{ID: "c1", Roles: []string{"coder"}}
	manager := Actor{ID: "m1", Roles: []string{"manager"}}

	v, _ := e.View(c, coder, "work")
	if inputsOf(v, "claim")["amount"].Editable || inputsOf(v, "locked")["code"].Editable {
		t.Fatal("a coder sees manager-only inputs read-only")
	}
	if !inputsOf(v, "claim")["category"].Editable {
		t.Fatal("unrestricted inputs stay editable")
	}

	// A coder's write to a manager-only input is ignored, never stored.
	c1, err := e.Save(ctx, c, coder, "work", map[string]any{
		"claim":  map[string]any{"category": "B", "amount": 500},
		"locked": map[string]any{"code": "x"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := c1.Get("claim.category"); got != "B" {
		t.Errorf("category = %v", got)
	}
	if _, ok := c1.Get("claim.amount"); ok {
		t.Error("coder wrote a manager-only input")
	}
	if _, ok := c1.Get("locked.code"); ok {
		t.Error("coder wrote a manager-only form")
	}

	c2, err := e.Save(ctx, c, manager, "work", map[string]any{
		"claim":  map[string]any{"amount": 500},
		"locked": map[string]any{"code": "x"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := c2.Get("claim.amount"); got != float64(500) && got != 500 {
		t.Errorf("manager amount = %v", got)
	}
	if got, _ := c2.Get("locked.code"); got != "x" {
		t.Errorf("manager code = %v", got)
	}
}

func TestGroupEditRolesAndDisabledIf(t *testing.T) {
	def := accessDef()
	def.Stages[0].Page.Groups[0].EditRoles = []string{"manager"}
	e, c := accessEngine(t, def)
	coder := Actor{ID: "c1", Roles: []string{"coder"}}
	v, _ := e.View(c, coder, "work")
	if inputsOf(v, "claim")["category"].Editable {
		t.Error("group edit_roles should make the whole group read-only for a coder")
	}

	def = accessDef()
	def.Stages[0].Page.Groups[0].DisabledIf = "claim.category == 'B'"
	e, c = accessEngine(t, def)
	c, err := e.Save(context.Background(), c, coder, "work", map[string]any{"claim": map[string]any{"category": "B"}})
	if err != nil {
		t.Fatal(err)
	}
	v, _ = e.View(c, coder, "work")
	if in := inputsOf(v, "claim")["category"]; in.Editable || !in.Disabled {
		t.Errorf("group disabled_if should disable its inputs: %+v", in)
	}
}

func TestDisabledIfIsShownReadOnlyAndNotAccepted(t *testing.T) {
	e, c := accessEngine(t, accessDef())
	ctx := context.Background()
	coder := Actor{ID: "c1", Roles: []string{"coder"}}

	c, err := e.Save(ctx, c, coder, "work", map[string]any{"claim": map[string]any{"category": "A", "reason": "first"}})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := c.Get("claim.reason"); got != "first" {
		t.Fatalf("reason while enabled = %v", got)
	}
	// Switching to B disables reason; a same-request write to it is dropped.
	c, err = e.Save(ctx, c, coder, "work", map[string]any{"claim": map[string]any{"category": "B", "reason": "second"}})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := c.Get("claim.reason"); got != "first" {
		t.Errorf("disabled input changed to %v", got)
	}
	v, _ := e.View(c, coder, "work")
	in := inputsOf(v, "claim")["reason"]
	if in.Editable || !in.Disabled || in.Value != "first" {
		t.Errorf("disabled input should show its value read-only: %+v", in)
	}
}

func TestFormVisibleIfHidesValidatesAndRejects(t *testing.T) {
	e, c := accessEngine(t, accessDef())
	ctx := context.Background()
	coder := Actor{ID: "c1", Roles: []string{"coder"}}

	v, _ := e.View(c, coder, "work")
	if hasForm(v, "extra") {
		t.Fatal("extra form is hidden until the category is A")
	}
	// Hidden: a write is ignored and its required input does not block.
	c1, err := e.Save(ctx, c, coder, "work", map[string]any{"extra": map[string]any{"detail": "sneaky"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := c1.Get("extra.detail"); ok {
		t.Error("hidden form accepted a write")
	}
	if miss := e.missingRequired(c1, &e.C.Def.Stages[0], c1.Stages["work"], coder); len(miss) != 0 {
		t.Errorf("hidden required input blocked the stage: %v", miss)
	}

	// Parent and child in one request: the child is accepted.
	c2, err := e.Save(ctx, c, coder, "work", map[string]any{
		"claim": map[string]any{"category": "A"},
		"extra": map[string]any{"detail": "ok"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := c2.Get("extra.detail"); got != "ok" {
		t.Errorf("extra.detail = %v", got)
	}
	v, _ = e.View(c2, coder, "work")
	if !hasForm(v, "extra") {
		t.Error("extra form should appear for category A")
	}
}

func TestDependentLookupNarrowsAndValidates(t *testing.T) {
	e, c := accessEngine(t, accessDef())
	ctx := context.Background()
	coder := Actor{ID: "c1", Roles: []string{"coder"}}
	var ve *ValidationError

	// Together in one request: state is checked against the submitted country.
	c1, err := e.Save(ctx, c, coder, "work", map[string]any{"geo": map[string]any{"country": "CA", "state": "ON"}})
	if err != nil {
		t.Fatalf("matching pair: %v", err)
	}
	if got, _ := c1.Get("geo.state"); got != "ON" {
		t.Errorf("state = %v", got)
	}
	if _, err := e.Save(ctx, c, coder, "work", map[string]any{"geo": map[string]any{"country": "CA", "state": "NY"}}); !errors.As(err, &ve) || ve.Fields[0].Rule != "option" {
		t.Fatalf("a state of another country must be refused: %v", err)
	}
	if _, err := e.Save(ctx, c, coder, "work", map[string]any{"geo": map[string]any{"state": "NY"}}); !errors.As(err, &ve) {
		t.Fatalf("a state without a country must be refused: %v", err)
	}

	// The view offers only the states of the saved country.
	v, _ := e.View(c1, coder, "work")
	var values []string
	for _, o := range inputsOf(v, "geo")["state"].Choices {
		values = append(values, o.Value)
	}
	if !slices.Equal(values, []string{"ON"}) {
		t.Errorf("choices = %v", values)
	}
	if deps := inputsOf(v, "geo")["state"].DependsOn; !slices.Equal(deps, []string{"country"}) {
		t.Errorf("depends_on = %v", deps)
	}

	// Choices answers for a hypothetical parent value, before anything is saved.
	opts, err := e.Choices(c1, coder, "geo", "state", map[string]any{"country": "US"})
	if err != nil || len(opts) != 2 {
		t.Fatalf("Choices(US) = %v, %v", opts, err)
	}
	if _, err := e.Choices(c1, coder, "geo", "country", nil); err != nil {
		t.Fatalf("a lookup without dependencies still answers: %v", err)
	}
	if _, err := e.Choices(c1, coder, "claim", "category", nil); err == nil {
		t.Fatal("an input without a lookup has no choices")
	}
	if _, err := e.Choices(c1, coder, "audit", "finding", nil); err == nil {
		t.Fatal("a hidden form must not answer")
	}
}

func TestCompileChecksDependencies(t *testing.T) {
	bad := func(mutate func(*Definition)) error {
		def := accessDef()
		mutate(def)
		_, err := Compile(def)
		return err
	}
	if err := bad(func(d *Definition) { d.Forms[3].Inputs[1].DependsOn = []string{"nowhere"} }); err == nil {
		t.Error("an unknown dependency must fail the compile")
	}
	if err := bad(func(d *Definition) { d.Forms[3].Inputs[1].DependsOn = []string{"state"} }); err == nil {
		t.Error("a self dependency must fail the compile")
	}
	if err := bad(func(d *Definition) {
		d.Forms[3].Inputs[0].DependsOn = []string{"state"}
	}); err == nil {
		t.Error("a dependency cycle must fail the compile")
	}
	if err := bad(func(d *Definition) { d.Forms[0].Inputs[0].DependsOn = []string{"amount"} }); err == nil {
		t.Error("depends_on without a lookup must fail the compile")
	}
	expr := mustCompile(t, accessDef()).Expressions()
	for _, key := range []string{"form extra visible_if", "form claim input reason disabled_if"} {
		if _, ok := expr[key]; !ok {
			t.Errorf("Expressions() lacks %q", key)
		}
	}
}

func mustCompile(t *testing.T, def *Definition) *Compiled {
	t.Helper()
	c, err := Compile(def)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func boolp(b bool) *bool { return &b }

func TestWorkspaceOverridesTuneStagesAndNarrowFields(t *testing.T) {
	def := accessDef()
	def.Calendars = []Calendar{{Name: "night", Timezone: "UTC", Hours: "24x7"}}
	def.Stages[0].Due = "72h"
	def.Stages[0].SLA = &SLA{Duration: "48h", WarnBefore: "6h"}
	compiled := mustCompile(t, def)
	e := NewEngine(compiled)
	e.Eval = bclEval{}
	ws := &Overrides{
		Stages: map[string]StageOverride{"work": {Due: "12h", Duration: "10h", WarnBefore: "2h", Calendar: "night"}},
		Fields: map[string]FieldOverride{
			"claim.category": {Required: boolp(true), Label: "Claim type"},
			"claim.reason":   {Hidden: boolp(true)},
			"claim.amount":   {Disabled: boolp(true)},
			"audit":          {Hidden: boolp(true)},
			"extra.detail":   {Hidden: boolp(true)}, // base-required: must stay
		},
	}
	e.Overrides = func(c *Case) *Overrides {
		if c.OrgUnit == "ws2" {
			return ws
		}
		return nil
	}
	ctx := context.Background()
	manager := Actor{ID: "m1", Roles: []string{"manager", "auditor"}}

	base, err := e.Start(ctx, manager, StartOptions{Number: "C-1", OrgUnit: "ws1"})
	if err != nil {
		t.Fatal(err)
	}
	tuned, err := e.Start(ctx, manager, StartOptions{Number: "C-2", OrgUnit: "ws2"})
	if err != nil {
		t.Fatal(err)
	}
	baseSpan := base.Stages["work"].DueAt.Sub(*base.Stages["work"].EnteredAt)
	tunedSpan := tuned.Stages["work"].DueAt.Sub(*tuned.Stages["work"].EnteredAt)
	if tunedSpan.Hours() != 10 || baseSpan.Hours() != 48 {
		t.Errorf("sla spans: base %v tuned %v", baseSpan, tunedSpan)
	}
	if w := tuned.Stages["work"].SLA.WarnAt; w == nil || tuned.Stages["work"].SLA.DueAt.Sub(*w).Hours() != 2 {
		t.Errorf("warning not tuned: %+v", tuned.Stages["work"].SLA)
	}

	bv, _ := e.View(base, manager, "work")
	tv, _ := e.View(tuned, manager, "work")
	if in := inputsOf(bv, "claim")["category"]; in.Required || in.Label != "category" {
		t.Errorf("base category: %+v", in)
	}
	if in := inputsOf(tv, "claim")["category"]; !in.Required || in.Label != "Claim type" {
		t.Errorf("tuned category should be required and relabelled: %+v", in)
	}
	if _, ok := inputsOf(tv, "claim")["reason"]; ok {
		t.Error("workspace hid reason")
	}
	if in := inputsOf(tv, "claim")["amount"]; in.Editable || !in.Disabled {
		t.Errorf("workspace disabled amount: %+v", in)
	}
	if hasForm(tv, "audit") || !hasForm(bv, "audit") {
		t.Error("workspace hid the audit form only for itself")
	}

	// A base-required input stays visible even if a workspace tries to hide it.
	tuned, err = e.Save(ctx, tuned, manager, "work", map[string]any{"claim": map[string]any{"category": "A"}})
	if err != nil {
		t.Fatal(err)
	}
	tv, _ = e.View(tuned, manager, "work")
	if _, ok := inputsOf(tv, "extra")["detail"]; !ok {
		t.Error("a base-required input must not be hideable")
	}
	// The required override blocks the stage until filled.
	miss := e.missingRequired(base, &compiled.Def.Stages[0], base.Stages["work"], manager)
	tm := e.missingRequired(tuned, &compiled.Def.Stages[0], tuned.Stages["work"], manager)
	if len(tm) == 0 {
		t.Errorf("tuned workspace should still need extra.detail: base=%v tuned=%v", miss, tm)
	}

	problems := compiled.CheckOverrides(&Overrides{
		Stages: map[string]StageOverride{"nowhere": {Due: "1h"}, "work": {Duration: "soon", Calendar: "ghost"}},
		Fields: map[string]FieldOverride{"claim.ghost": {}, "ghostform": {}, "extra.detail": {Hidden: boolp(true)}},
	})
	for _, want := range []string{`stage "nowhere" does not exist`, `"soon" is not a duration`, `calendar "ghost" is not defined`, `input "claim.ghost" does not exist`, `form "ghostform" does not exist`, `cannot be hidden`} {
		if !slices.ContainsFunc(problems, func(p string) bool { return strings.Contains(p, want) }) {
			t.Errorf("CheckOverrides missed %q in %v", want, problems)
		}
	}
	if got := compiled.CheckOverrides(ws); len(got) != 1 {
		t.Errorf("only the hidden required input should be reported, got %v", got)
	}
}
