package pipeline

import "testing"

// A taggable input takes values beyond its lookup set; an ordinary lookup input does not.
func TestTaggableInputSkipsLookupCheck(t *testing.T) {
	e := NewEngine(&Compiled{})
	e.Lookup = func(string, *Case) []Option { return []Option{{Value: "a", Label: "A"}} }
	c := &Case{}
	plain := Input{Name: "x", Kind: KindMultiSelect, Lookup: "s"}
	if rule, _ := e.checkLookup(c, "f", plain, []any{"a", "new-tag"}); rule == "" {
		t.Fatal("a value outside the set must be refused")
	}
	tagged := plain
	tagged.Taggable = true
	if rule, msg := e.checkLookup(c, "f", tagged, []any{"a", "new-tag"}); rule != "" {
		t.Fatalf("a taggable input accepts new tags: %s", msg)
	}
}

func TestChoicesPassTypeAheadText(t *testing.T) {
	var seen map[string]any
	compiled, err := Compile(&Definition{Name: "d", Forms: []Form{{Name: "f", Inputs: []Input{{Name: "p", Kind: KindSelect, Lookup: "s"}}}},
		Stages: []Stage{{Name: "w", Roles: []string{"r"}, Page: &Page{Groups: []Group{{Name: "g", Forms: []string{"f"}}}}}}})
	if err != nil {
		t.Fatal(err)
	}
	e := NewEngine(compiled)
	e.Lookup = func(string, *Case) []Option { return nil }
	e.LookupFor = func(_ string, _ *Case, deps map[string]any) []Option { seen = deps; return nil }
	if _, err := e.Choices(&Case{}, Actor{ID: "u", Roles: []string{"r"}}, "f", "p", map[string]any{"@q": "shar"}); err != nil {
		t.Fatal(err)
	}
	if seen["@q"] != "shar" {
		t.Fatalf("the host must receive the typed text: %v", seen)
	}
}
