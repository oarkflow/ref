package pipeline

import (
	"context"
	"strings"
	"testing"
)

func uiDef(ui *UISpec, stageUI *LayoutSpec) *Definition {
	return &Definition{
		Name: "ui", UI: ui,
		Inputs: []Input{{Name: "a", Kind: KindText}},
		Forms:  []Form{{Name: "f", Fields: []string{"a"}}},
		Stages: []Stage{{Name: "s", Public: true, UI: stageUI, Page: &Page{Groups: []Group{{Name: "g", Forms: []string{"f"}}}}, Actions: []ActionSpec{{Name: "go", Outcome: OutcomeAdvance}}}},
	}
}

func TestUIConfigIsValidated(t *testing.T) {
	good := &UISpec{Brand: "Registry", Accent: "#0f766e", Density: "compact",
		Overview: &LayoutSpec{Mode: "console", Flow: "hide", Columns: []UIColumn{
			{Name: "left", Width: "280px", Panels: []string{"case", "stage"}},
			{Name: "main", Width: "1fr", Panels: []string{"next", "summary"}},
			{Name: "right", Width: "30%", Panels: []string{"log"}}}, Panels: []UIPanel{{ID: "log", Title: "History", Collapsed: true}}},
		Task: &LayoutSpec{Mode: "split", Columns: []UIColumn{{Name: "main", Panels: []string{"form", "checks"}}, {Name: "rail", Width: "320px", Panels: []string{"stage"}}}}}
	if _, err := Compile(uiDef(good, &LayoutSpec{Mode: "stacked"})); err != nil {
		t.Fatalf("a valid ui must compile: %v", err)
	}
	bad := map[string]*UISpec{
		"colour":        {Accent: "teal"},
		"density":       {Density: "tiny"},
		"mode":          {Overview: &LayoutSpec{Mode: "grid"}},
		"flow":          {Overview: &LayoutSpec{Flow: "maybe"}},
		"unknown panel": {Overview: &LayoutSpec{Columns: []UIColumn{{Name: "a", Panels: []string{"nope"}}}}},
		"task panel":    {Task: &LayoutSpec{Columns: []UIColumn{{Name: "a", Panels: []string{"log"}}}}},
		"twice":         {Overview: &LayoutSpec{Columns: []UIColumn{{Name: "a", Panels: []string{"log"}}, {Name: "b", Panels: []string{"log"}}}}},
		"width":         {Overview: &LayoutSpec{Columns: []UIColumn{{Name: "a", Width: "wide", Panels: []string{"log"}}}}},
		"empty column":  {Overview: &LayoutSpec{Columns: []UIColumn{{Name: "a"}}}},
		"dup column":    {Overview: &LayoutSpec{Columns: []UIColumn{{Name: "a", Panels: []string{"log"}}, {Name: "a", Panels: []string{"case"}}}}},
		"panel id":      {Overview: &LayoutSpec{Panels: []UIPanel{{ID: "sidebar"}}}},
	}
	for name, ui := range bad {
		if _, err := Compile(uiDef(ui, nil)); err == nil {
			t.Errorf("%s must not compile", name)
		}
	}
	if _, err := Compile(uiDef(nil, &LayoutSpec{Columns: []UIColumn{{Name: "a", Panels: []string{"log"}}}})); err == nil || !strings.Contains(err.Error(), "stage s ui") {
		t.Errorf("a stage layout is checked against the task panels: %v", err)
	}
}

func TestUIReachesTheOverviewAndTheTaskView(t *testing.T) {
	ui := &UISpec{Brand: "Registry", Accent: "#0f766e",
		Overview: &LayoutSpec{Mode: "stacked"}, Task: &LayoutSpec{Mode: "split"}}
	c, err := Compile(uiDef(ui, &LayoutSpec{Mode: "stacked"}))
	if err != nil {
		t.Fatal(err)
	}
	e := NewEngine(c)
	e.Eval = bclEval{}
	cs, _ := e.Start(context.Background(), applicant, StartOptions{})
	o, err := e.Overview(cs, applicant)
	if err != nil || o.UI == nil || o.UI.Brand != "Registry" || o.UI.Accent != "#0f766e" || o.UI.Layout.Mode != "stacked" {
		t.Fatalf("overview ui: %v %+v", err, o.UI)
	}
	v, _ := e.View(cs, applicant, "s")
	if v.UI == nil || v.UI.Layout.Mode != "stacked" {
		t.Fatalf("a stage's own layout wins on the task screen: %+v", v.UI)
	}
	// With no ui at all the host keeps its basic layout.
	plain, _ := Compile(uiDef(nil, nil))
	pe := NewEngine(plain)
	pe.Eval = bclEval{}
	pc, _ := pe.Start(context.Background(), applicant, StartOptions{})
	if po, _ := pe.Overview(pc, applicant); po.UI != nil {
		t.Fatalf("no ui configured, none sent: %+v", po.UI)
	}
}
