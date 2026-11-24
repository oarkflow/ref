package pipeline

import (
	"context"
	"slices"
	"testing"
)

var (
	sup    = Actor{ID: "sup", Roles: []string{"supervisor"}}
	adm    = Actor{ID: "adm", Roles: []string{"admin"}}
	clerkA = Actor{ID: "ca", Roles: []string{"clerk"}}
	mgr    = Actor{ID: "mg", Roles: []string{"manager"}}
)

func rulesDef() *Definition {
	return &Definition{
		Name:       "rules",
		AdminRoles: []string{"admin"},
		Inputs:     []Input{{Name: "name", Kind: KindText, Required: true}, {Name: "id_no", Kind: KindText}, {Name: "notes", Kind: KindText}, {Name: "internal", Kind: KindText}},
		Forms:      []Form{{Name: "p", Fields: []string{"name", "id_no", "notes", "internal"}}},
		Stages: []Stage{
			{Name: "s1", Public: true, Actions: []ActionSpec{{Name: "go", Outcome: OutcomeAdvance}},
				Page: &Page{Groups: []Group{{Name: "g", Forms: []string{"p"}, Fields: []FieldRule{
					{Path: "p.internal", Mode: ModeHidden},
					{Path: "p.id_no", RequiredIf: "true", Label: "ID number (required here)"}}}}}},
			{Name: "s2", Roles: []string{"clerk", "supervisor"}, ViewRoles: []string{"admin"}, Actions: []ActionSpec{{Name: "go", Outcome: OutcomeAdvance}},
				Page: &Page{Groups: []Group{{Name: "g", Mode: ModeReadonly, Forms: []string{"p"}, Fields: []FieldRule{
					{Path: "p.notes", Mode: ModeEditable},
					{Path: "p.id_no", Roles: []string{"admin"}},
					{Path: "p.internal", Mode: ModeEditable, EditRoles: []string{"supervisor"}}}}}}},
			{Name: "s3", Roles: []string{"manager"}, Actions: []ActionSpec{{Name: "go", Outcome: OutcomeAdvance}},
				Page: &Page{Groups: []Group{{Name: "g", Forms: []string{"p"}, Fields: []FieldRule{
					{Path: "p", Mode: ModeReadonly},
					{Path: "p.notes", Mode: ModeEditable, DisabledIf: "p.notes == 'locked'"}}}}}},
		},
	}
}

func inputOf(v *View, path string) (InputView, bool) {
	if v.Page == nil {
		return InputView{}, false
	}
	for _, g := range v.Page.Groups {
		for _, f := range g.Forms {
			for _, in := range f.Inputs {
				if in.Path == path {
					return in, true
				}
			}
		}
	}
	return InputView{}, false
}

func TestStageFieldRulesEnableDisableHideAndShowByRoleWhileDataMovesOn(t *testing.T) {
	ctx := context.Background()
	comp, err := Compile(rulesDef())
	if err != nil {
		t.Fatal(err)
	}
	e := NewEngine(comp)
	e.Eval = bclEval{}
	c, _ := e.Start(ctx, applicant, StartOptions{})

	// Stage 1: the applicant never sees the internal field, and id_no is required here by the stage rule.
	v, _ := e.View(c, applicant, "s1")
	if _, ok := inputOf(v, "p.internal"); ok {
		t.Fatal("a hidden field is not rendered")
	}
	if in, _ := inputOf(v, "p.id_no"); !in.Required || in.Label != "ID number (required here)" || !in.Editable {
		t.Fatalf("a stage rule requires and rewords the field: %+v", in)
	}
	if _, err := e.Act(ctx, c, applicant, "s1", "go", ActInput{Data: map[string]any{"p": map[string]any{"name": "Asha"}}}); err == nil {
		t.Fatal("the stage's required_if must block submitting without an ID number")
	}
	c, err = e.Act(ctx, c, applicant, "s1", "go", ActInput{Data: map[string]any{"p": map[string]any{"name": "Asha", "id_no": "N-77", "internal": "sneaky"}}})
	if err != nil || c.Stage != "s2" {
		t.Fatalf("advance: %v", err)
	}
	if got, _ := c.Get("p.internal"); got != nil {
		t.Fatalf("a hidden field is never accepted on save: %v", got)
	}

	// Stage 2: the form moved with its data; one field is switched on in a read-only group, one is limited by role.
	v, _ = e.View(c, clerkA, "s2")
	if in, _ := inputOf(v, "p.name"); in.Editable || in.Value != "Asha" {
		t.Fatalf("data moved on and is read-only here: %+v", in)
	}
	if in, _ := inputOf(v, "p.notes"); !in.Editable {
		t.Fatalf("a rule enables one field in a read-only group: %+v", in)
	}
	if _, ok := inputOf(v, "p.id_no"); ok {
		t.Fatal("id_no is only shown to admins at this stage")
	}
	if in, ok := inputOf(v, "p.internal"); !ok || in.Editable {
		t.Fatalf("internal is read-only for a clerk: %+v %v", in, ok)
	}
	av, _ := e.View(c, adm, "s2")
	if in, ok := inputOf(av, "p.id_no"); !ok || in.Value != "N-77" || in.Editable {
		t.Fatalf("an admin sees the id number, read-only: %+v %v", in, ok)
	}
	sv, _ := e.View(c, sup, "s2")
	if in, _ := inputOf(sv, "p.internal"); !in.Editable {
		t.Fatalf("a supervisor may edit internal: %+v", in)
	}
	c, err = e.Save(ctx, c, clerkA, "s2", map[string]any{"p": map[string]any{"name": "Hacked", "notes": "checked", "internal": "no"}})
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := c.Get("p.name"); n != "Asha" {
		t.Fatalf("a read-only field ignores writes: %v", n)
	}
	if n, _ := c.Get("p.notes"); n != "checked" {
		t.Fatalf("the enabled field was saved: %v", n)
	}
	if n, _ := c.Get("p.internal"); n != nil {
		t.Fatalf("an edit role limits who may write: %v", n)
	}
	if c, err = e.Save(ctx, c, sup, "s2", map[string]any{"p": map[string]any{"internal": "watch"}}); err != nil {
		t.Fatal(err)
	}
	if c, err = e.Act(ctx, c, clerkA, "s2", "go", ActInput{}); err != nil || c.Stage != "s3" {
		t.Fatalf("advance to s3: %v", err)
	}

	// Stage 3: the whole form is read-only except notes, which a condition can lock.
	v, _ = e.View(c, mgr, "s3")
	for _, path := range []string{"p.name", "p.id_no", "p.internal"} {
		if in, ok := inputOf(v, path); !ok || in.Editable {
			t.Fatalf("%s is read-only at s3: %+v %v", path, in, ok)
		}
	}
	if in, _ := inputOf(v, "p.notes"); !in.Editable || in.Disabled {
		t.Fatalf("notes are editable at s3: %+v", in)
	}
	if c, err = e.Save(ctx, c, mgr, "s3", map[string]any{"p": map[string]any{"notes": "locked"}}); err != nil {
		t.Fatal(err)
	}
	v, _ = e.View(c, mgr, "s3")
	if in, _ := inputOf(v, "p.notes"); in.Editable || !in.Disabled {
		t.Fatalf("a rule's disabled_if shows the field read-only: %+v", in)
	}
	c2, _ := e.Save(ctx, c, mgr, "s3", map[string]any{"p": map[string]any{"notes": "changed"}})
	if n, _ := c2.Get("p.notes"); n != "locked" {
		t.Fatalf("a disabled field ignores writes: %v", n)
	}
	for path, want := range map[string]any{"p.name": "Asha", "p.id_no": "N-77", "p.internal": "watch"} {
		if got, _ := c2.Get(path); got != want {
			t.Fatalf("%s did not travel with the case: %v", path, got)
		}
	}
}

func TestFieldRulesAreValidatedAtCompile(t *testing.T) {
	for name, rule := range map[string]FieldRule{
		"form not in group": {Path: "other.x"},
		"unknown input":     {Path: "p.nope"},
		"bad mode":          {Path: "p.name", Mode: "invisible"},
	} {
		d := rulesDef()
		d.Stages[0].Page.Groups[0].Fields = []FieldRule{rule}
		if _, err := Compile(d); err == nil {
			t.Errorf("%s must not compile", name)
		}
	}
	// Forms a stage's field rules and the matrix agree on.
	if m := slices.Contains([]string{ModeEditable, ModeReadonly, ModeHidden}, ModeHidden); !m {
		t.Fatal("modes")
	}
}
