package platform

import (
	"context"
	"testing"
)

func TestAppManagerLifecycle(t *testing.T) {
	am := NewMemoryAppManager()
	ctx := context.Background()

	bclSource := `
app "fintech_service" {
  version "1.0.0"
}

resource "db" {
  kind "database.sql"
}

intent "wallet.transfer" {
  node "validate" {
    uses "constant"
    config { value true }
    provides [valid]
  }
}

route "api.transfer" {
  method POST
  path "/v1/transfer"
  intent "wallet.transfer"
}

worker "audit_worker" {
  queue "bus"
  job_type "audit.log"
  intent "audit.record"
}
`

	// 1. Create app
	created, err := am.CreateApp(ctx, AppDef{
		ID:          "fintech",
		Name:        "Fintech Service",
		Description: "Payment and transfer engine",
		SourceBCL:   bclSource,
		Status:      AppStatusDraft,
	})
	if err != nil {
		t.Fatalf("create app failed: %v", err)
	}
	if created.RevisionID != 1 || created.Status != AppStatusDraft {
		t.Fatalf("unexpected app creation: %+v", created)
	}
	if len(created.Intents) != 1 || len(created.Routes) != 1 || len(created.Resources) != 1 || len(created.Workers) != 1 {
		t.Fatalf("expected extracted structures: intents=%d, routes=%d, resources=%d, workers=%d",
			len(created.Intents), len(created.Routes), len(created.Resources), len(created.Workers))
	}

	// 2. Get app
	got, err := am.GetApp(ctx, "fintech")
	if err != nil || got.Name != "Fintech Service" {
		t.Fatalf("get app failed: %v", err)
	}

	// 3. List apps
	list, err := am.ListApps(ctx, AppFilter{})
	if err != nil || len(list) != 1 {
		t.Fatalf("list apps failed: %v", err)
	}
	if list[0].ID != "fintech" || list[0].Intents != 1 {
		t.Fatalf("unexpected summary: %+v", list[0])
	}

	// 4. Update app (stages revision 2)
	updated, err := am.UpdateApp(ctx, "fintech", AppDef{
		Name:      "Fintech Core",
		SourceBCL: bclSource,
	})
	if err != nil || updated.RevisionID != 2 {
		t.Fatalf("update app failed: %v, rev=%d", err, updated.RevisionID)
	}

	// 5. Activate app (revision 2 becomes active)
	if err := am.ActivateApp(ctx, "fintech", 2); err != nil {
		t.Fatalf("activate app failed: %v", err)
	}
	active, _ := am.GetApp(ctx, "fintech")
	if active.Status != AppStatusActive {
		t.Fatalf("expected status ACTIVE, got: %s", active.Status)
	}

	// 6. Check health
	health, err := am.AppHealth(ctx, "fintech")
	if err != nil || !health.Healthy || health.Status != AppStatusActive {
		t.Fatalf("unexpected health: %+v, err=%v", health, err)
	}

	// 7. Run intent
	res, err := am.RunIntent(ctx, "fintech", "wallet.transfer", map[string]any{"amount": 100.0})
	if err != nil || res["status"] != "completed" {
		t.Fatalf("run intent failed: %v, res=%+v", err, res)
	}

	// 8. Check metrics & logs
	metrics, err := am.AppMetrics(ctx, "fintech")
	if err != nil || metrics.RequestsTotal != 1 {
		t.Fatalf("expected 1 request in metrics, got: %+v", metrics)
	}

	logs, err := am.AppLogs(ctx, "fintech", 10)
	if err != nil || len(logs) < 2 {
		t.Fatalf("expected at least 2 logs, got: %d", len(logs))
	}

	// 9. Rollback to revision 1
	if err := am.RollbackApp(ctx, "fintech", 1); err != nil {
		t.Fatalf("rollback failed: %v", err)
	}
	active, _ = am.GetApp(ctx, "fintech")
	if active.RevisionID != 1 {
		t.Fatalf("expected rev 1 active after rollback, got: %d", active.RevisionID)
	}

	// 10. Clone app
	cloned, err := am.CloneApp(ctx, "fintech", "fintech_clone", "Fintech Cloned")
	if err != nil || cloned.ID != "fintech_clone" {
		t.Fatalf("clone app failed: %v", err)
	}

	// 11. Deactivate & Delete
	if err := am.DeactivateApp(ctx, "fintech"); err != nil {
		t.Fatalf("deactivate failed: %v", err)
	}
	deact, _ := am.GetApp(ctx, "fintech")
	if deact.Status != AppStatusInactive {
		t.Fatalf("expected INACTIVE, got: %s", deact.Status)
	}

	if err := am.DeleteApp(ctx, "fintech"); err != nil {
		t.Fatalf("delete failed: %v", err)
	}
	arch, _ := am.GetApp(ctx, "fintech")
	if arch.Status != AppStatusArchived {
		t.Fatalf("expected ARCHIVED, got: %s", arch.Status)
	}
}
