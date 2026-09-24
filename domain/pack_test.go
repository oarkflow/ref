package domain

import (
	"testing"

	"github.com/oarkflow/ref/capability"
	"github.com/oarkflow/ref/execution"
	"github.com/oarkflow/ref/fact"
	"github.com/oarkflow/ref/graph"
)

func TestPackRegistersCapabilitiesAtomically(t *testing.T) {
	key := fact.NewKey[string]("pack.value")
	pack, err := NewPack("billing", "1")
	if err != nil {
		t.Fatal(err)
	}
	reg := capability.Registration{Name: "pack.read", Kind: graph.ReadNode, Provides: []fact.AnyKey{key.Any()}, Run: func(*execution.NodeContext) error { return nil }}
	if err := pack.Add(reg); err != nil {
		t.Fatal(err)
	}
	registry := capability.NewRegistry()
	if err := pack.Register(registry); err != nil {
		t.Fatal(err)
	}
	if _, ok := registry.Lookup("pack.read"); !ok {
		t.Fatal("expected registered capability")
	}
	reg.Provides[0] = fact.AnyKey{}
	if pack.Capabilities()[0].Provides[0].DefID == 0 {
		t.Fatal("pack exposed mutable registration")
	}
}

func TestResourceValidation(t *testing.T) {
	if !(Resource[string]{ID: "r1", TenantID: "t1"}).Valid() {
		t.Fatal("expected valid resource")
	}
	if (Resource[string]{ID: "r1"}).Valid() {
		t.Fatal("expected missing tenant to be invalid")
	}
}
