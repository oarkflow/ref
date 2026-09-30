package ops

import (
	"context"
	"testing"

	"github.com/oarkflow/ref/platform"
)

// A preview registry rebinds ops.maintenance_set to a gate of its own: running
// the rebound action flips that gate and leaves the live one alone.
func TestReplaceActionsOnBindsTheOtherGate(t *testing.T) {
	live, sandbox := NewMaintenanceGate(false, ""), NewMaintenanceGate(false, "")

	r := platform.NewRegistry()
	for name, f := range map[string]platform.ActionFactory{
		"ops.maintenance_set": live.setFactory(), "ops.maintenance_status": live.statusFactory(),
	} {
		if err := r.RegisterAction(name, f); err != nil {
			t.Fatal(err)
		}
	}
	if err := sandbox.ReplaceActionsOn(r); err != nil {
		t.Fatalf("ReplaceActionsOn: %v", err)
	}

	// What the registry now builds for the set action is sandbox.setFactory().
	act, err := sandbox.setFactory().Build(platform.BuildContext{}, platform.NodeSpec{Provides: []string{"out"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := act.Run(&platform.ActionContext{
		Context: context.Background(),
		Inputs:  map[string]any{"input": map[string]any{"on": true, "message": "preview only"}},
	}); err != nil {
		t.Fatal(err)
	}
	if !sandbox.On() || live.On() {
		t.Fatalf("sandbox on=%v live on=%v, want true/false", sandbox.On(), live.On())
	}

	// A registry that lacks the actions is an error, not a silent no-op.
	if err := sandbox.ReplaceActionsOn(platform.NewRegistry()); err == nil {
		t.Error("ReplaceActionsOn on a registry without the actions succeeded")
	}
}
