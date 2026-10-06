package platform

import (
	"context"
	"testing"
)

func TestGenerationStore(t *testing.T) {
	store := NewMemoryGenerationStore()
	ctx := context.Background()

	// Initial active generation
	gen1 := GenerationRecord{
		AppID:  "my_app",
		Source: []byte(`intent "test1" {}`),
		Active: true,
	}
	if err := store.Save(ctx, gen1); err != nil {
		t.Fatalf("save gen1 failed: %v", err)
	}

	active, ok, err := store.GetActive(ctx, "my_app")
	if err != nil || !ok || active.RevisionID != 1 {
		t.Fatalf("expected revision 1 active, got ok=%v, rev=%d, err=%v", ok, active.RevisionID, err)
	}

	// Save gen2 as active
	gen2 := GenerationRecord{
		AppID:  "my_app",
		Source: []byte(`intent "test2" {}`),
		Active: true,
	}
	if err := store.Save(ctx, gen2); err != nil {
		t.Fatalf("save gen2 failed: %v", err)
	}

	active, ok, err = store.GetActive(ctx, "my_app")
	if err != nil || !ok || active.RevisionID != 2 {
		t.Fatalf("expected revision 2 active, got: %d", active.RevisionID)
	}

	revs, err := store.ListRevisions(ctx, "my_app", 10)
	if err != nil || len(revs) != 2 {
		t.Fatalf("expected 2 revisions, got: %d", len(revs))
	}

	// Rollback to rev 1
	if err := store.Rollback(ctx, "my_app", 1); err != nil {
		t.Fatalf("rollback failed: %v", err)
	}

	active, ok, err = store.GetActive(ctx, "my_app")
	if err != nil || !ok || active.RevisionID != 1 {
		t.Fatalf("expected revision 1 active after rollback, got: %d", active.RevisionID)
	}
}

func TestWorkflowManagerLifecycle(t *testing.T) {
	wm := &workflowManagerResource{
		name:      "wf_mgr",
		store:     NewMemoryGenerationStore(),
		hotReload: true,
		workflows: make(map[string]WorkflowDef),
		history:   make(map[string][]WorkflowDef),
	}
	ctx := context.Background()

	// 1. Validation test
	invalidBCL := `intent "bad" { unclosed block`
	diags, err := wm.ValidateWorkflow(ctx, WorkflowDef{SourceBCL: invalidBCL})
	if err != nil || len(diags) == 0 || diags[0].Severity != SeverityError {
		t.Fatalf("expected validation error, got: %+v, err=%v", diags, err)
	}

	validBCL := `
intent "order.checkout" {
  node "validate" {
    uses "constant"
    config { value true }
    provides [valid]
  }
}
`
	diags, err = wm.ValidateWorkflow(ctx, WorkflowDef{SourceBCL: validBCL})
	if err != nil || (len(diags) > 0 && diags[0].Severity == SeverityError) {
		t.Fatalf("expected valid BCL, got diags: %+v, err=%v", diags, err)
	}

	// 2. Create workflow
	created, err := wm.CreateWorkflow(ctx, WorkflowDef{
		ID:        "order_flow",
		Name:      "Order Checkout Flow",
		SourceBCL: validBCL,
		Status:    "draft",
	})
	if err != nil {
		t.Fatalf("create workflow failed: %v", err)
	}
	if created.RevisionID != 1 || len(created.Intents) != 1 || created.Intents[0] != "order.checkout" {
		t.Fatalf("unexpected created workflow: %+v", created)
	}

	// 3. Update workflow
	updatedBCL := `
intent "order.checkout" {
  node "validate" {
    uses "constant"
    config { value true }
    provides [valid]
  }
}

intent "order.cancel" {
  node "notify" {
    uses "constant"
    config { value "cancelled" }
    provides [status]
  }
}
`
	updated, err := wm.UpdateWorkflow(ctx, "order_flow", WorkflowDef{
		Name:      "Order Checkout & Cancel Flow",
		SourceBCL: updatedBCL,
	})
	if err != nil {
		t.Fatalf("update workflow failed: %v", err)
	}
	if updated.RevisionID != 2 || len(updated.Intents) != 2 {
		t.Fatalf("expected rev 2 with 2 intents, got: %+v", updated)
	}

	// 4. Activate workflow
	if err := wm.ActivateWorkflow(ctx, "order_flow", 2); err != nil {
		t.Fatalf("activate workflow failed: %v", err)
	}

	got, err := wm.GetWorkflow(ctx, "order_flow")
	if err != nil || got.Status != "active" {
		t.Fatalf("expected active status, got: %+v, err=%v", got, err)
	}

	// 5. Clone workflow
	cloned, err := wm.CloneWorkflow(ctx, "order_flow", "order_flow_v2", "Order Flow V2")
	if err != nil {
		t.Fatalf("clone workflow failed: %v", err)
	}
	if cloned.ID != "order_flow_v2" || cloned.Status != "draft" {
		t.Fatalf("unexpected cloned workflow: %+v", cloned)
	}

	// 6. List workflows
	list, err := wm.ListWorkflows(ctx, WorkflowFilter{})
	if err != nil || len(list) != 2 {
		t.Fatalf("expected 2 workflows listed, got: %d", len(list))
	}

	// 7. Rollback workflow to rev 1
	if err := wm.RollbackWorkflow(ctx, "order_flow", 1); err != nil {
		t.Fatalf("rollback failed: %v", err)
	}
	got, _ = wm.GetWorkflow(ctx, "order_flow")
	if got.RevisionID != 1 {
		t.Fatalf("expected rev 1 active after rollback, got: %d", got.RevisionID)
	}
}

func TestDynamicRegistration(t *testing.T) {
	r := NewEmptyRegistry()

	// Register dynamic action
	called := false
	err := r.RegisterDynamicAction("custom.test", ActionFactoryFunc(func(_ BuildContext, spec NodeSpec) (Action, error) {
		return ActionFunc(func(ctx *ActionContext) (ActionResult, error) {
			called = true
			return ActionResult{}, nil
		}), nil
	}))
	if err != nil {
		t.Fatalf("dynamic register failed: %v", err)
	}

	factory, ok := r.action("custom.test")
	if !ok || factory == nil {
		t.Fatalf("expected custom.test to be found in registry")
	}

	act, err := factory.Build(BuildContext{}, NodeSpec{Name: "test"})
	if err != nil {
		t.Fatalf("build action failed: %v", err)
	}
	_, _ = act.Run(&ActionContext{})
	if !called {
		t.Fatalf("expected dynamic action to execute")
	}
}
