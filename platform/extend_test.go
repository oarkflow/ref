package platform

import (
	"strings"
	"testing"
)

func extDoc() Document {
	return Document{Intents: []IntentSpec{{Name: "flow", Response: "out", Nodes: []NodeSpec{
		{Name: "a", Provides: []string{"a"}},
		{Name: "gate", Requires: []string{"a"}, Provides: []string{"gate"}},
		{Name: "out", Requires: []string{"a", "gate"}, Provides: []string{"out"}},
	}}}}
}

func TestExtendAddsFeedsRemovesReplaces(t *testing.T) {
	doc := extDoc()
	doc.Extensions = []IntentExtensionSpec{
		{Name: "add", Intent: "flow", Nodes: []NodeSpec{{Name: "check", Requires: []string{"a"}, Provides: []string{"check"}}}, Feed: []string{"out"}},
		{Name: "drop", Intent: "flow", Remove: []string{"gate"}},
		{Name: "swap", Intent: "flow", Replace: []string{"a"}, Nodes: []NodeSpec{{Name: "a", Provides: []string{"a"}, Description: "new"}}},
	}
	got, err := expandExtensions(doc)
	if err != nil {
		t.Fatal(err)
	}
	in := got.Intents[0]
	names := map[string]NodeSpec{}
	for _, n := range in.Nodes {
		names[n.Name] = n
	}
	if _, ok := names["gate"]; ok {
		t.Error("gate was not removed")
	}
	if strings.Join(names["out"].Requires, ",") != "a,check" {
		t.Errorf("out requires %v, want the removed gate gone and check added", names["out"].Requires)
	}
	if names["a"].Description != "new" {
		t.Error("a was not replaced")
	}
	if len(doc.Intents[0].Nodes) != 3 || len(doc.Intents[0].Nodes[2].Requires) != 2 {
		t.Error("the original document was modified")
	}
	if got.Extensions != nil {
		t.Error("extensions must be consumed")
	}
}

func TestExtendDisabledAndErrors(t *testing.T) {
	doc := extDoc()
	doc.Extensions = []IntentExtensionSpec{{Name: "off", Intent: "nope", Disabled: true}}
	if _, err := expandExtensions(doc); err != nil {
		t.Errorf("a disabled extension must be ignored: %v", err)
	}
	for name, ext := range map[string]IntentExtensionSpec{
		"intent":    {Name: "x", Intent: "nope"},
		"duplicate": {Name: "x", Intent: "flow", Nodes: []NodeSpec{{Name: "a"}}},
		"remove":    {Name: "x", Intent: "flow", Remove: []string{"ghost"}},
		"feed":      {Name: "x", Intent: "flow", Feed: []string{"ghost"}},
		"replace":   {Name: "x", Intent: "flow", Replace: []string{"a"}},
	} {
		d := extDoc()
		d.Extensions = []IntentExtensionSpec{ext}
		if _, err := expandExtensions(d); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}
