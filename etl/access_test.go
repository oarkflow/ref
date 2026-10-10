package etl

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestCustomRolesAndSourceScopes(t *testing.T) {
	eachStore(t, func(t *testing.T, e *Engine, _ *clock) {
		ctx := context.Background()
		other := ordersSource()
		other.ID, other.Name = "crm", "CRM"
		if _, err := e.PutSource(ctx, admin, other); err != nil {
			t.Fatal(err)
		}
		role, err := e.PutRole(ctx, admin, Role{ID: "orders-ops", Name: "Orders operator", Permissions: []string{PermRead, PermIngest, PermReplay, PermMonitor}, Sources: []string{"orders"}})
		if err != nil {
			t.Fatal(err)
		}
		if role.System {
			t.Fatal("a custom role is not a system role")
		}
		ops := Actor{ID: "olu", Roles: []string{"orders-ops"}}
		if _, err := e.Ingest(ctx, ops, "orders", "k1", rows(t, goodCSV)); err != nil {
			t.Fatalf("own source: %v", err)
		}
		// Another source reads as not found: its existence is not disclosed.
		if _, err := e.Ingest(ctx, ops, "crm", "k1", rows(t, goodCSV)); !errors.Is(err, ErrNotFound) {
			t.Fatalf("other source: %v", err)
		}
		other1, _ := e.Ingest(ctx, admin, "crm", "c1", rows(t, goodCSV))
		if _, err := e.Trace(ctx, ops, other1.Batch.ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("trace of another source: %v", err)
		}
		list, _ := e.Batches(ctx, ops, Query{})
		if len(list) != 1 || list[0].SourceID != "orders" {
			t.Fatalf("scoped listing: %+v", list)
		}
		if l, _ := e.Batches(ctx, ops, Query{SourceID: "crm"}); len(l) != 0 {
			t.Fatalf("asked for another source: %+v", l)
		}
		sum, _ := e.Summary(ctx, ops)
		if sum.Batches != 1 {
			t.Fatalf("scoped summary: %+v", sum)
		}
		srcs, _ := e.Sources(ctx, ops)
		if len(srcs) != 1 || srcs[0].ID != "orders" {
			t.Fatalf("scoped sources: %+v", srcs)
		}
		// No manage, audit or role permissions.
		if _, err := e.PutSource(ctx, ops, ordersSource()); !errors.Is(err, ErrForbidden) {
			t.Fatalf("manage: %v", err)
		}
		if _, err := e.Audit(ctx, ops, 10, 0); !errors.Is(err, ErrForbidden) {
			t.Fatalf("audit: %v", err)
		}
		if _, err := e.PutRole(ctx, ops, Role{ID: "x1", Name: "x"}); !errors.Is(err, ErrForbidden) {
			t.Fatalf("roles: %v", err)
		}
		// Widening the role takes effect without a restart.
		if _, err := e.PutRole(ctx, admin, Role{ID: "orders-ops", Name: "Orders operator", Permissions: []string{PermRead}, Sources: []string{"*"}}); err != nil {
			t.Fatal(err)
		}
		e.rolesMu.Lock()
		e.roles = nil
		e.rolesMu.Unlock()
		if l, _ := e.Batches(ctx, ops, Query{}); len(l) != 2 {
			t.Fatalf("after widening: %d", len(l))
		}
		if _, err := e.Ingest(ctx, ops, "orders", "k2", rows(t, goodCSV)); !errors.Is(err, ErrForbidden) {
			t.Fatalf("lost ingest: %v", err)
		}
		if err := e.DeleteRole(ctx, admin, "orders-ops"); err != nil {
			t.Fatal(err)
		}
	})
}

func TestRoleRules(t *testing.T) {
	eachStore(t, func(t *testing.T, e *Engine, _ *clock) {
		ctx := context.Background()
		if _, err := e.PutRole(ctx, admin, Role{ID: "Bad Id", Name: "x"}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("bad id: %v", err)
		}
		if _, err := e.PutRole(ctx, admin, Role{ID: "ok-role", Name: "x", Permissions: []string{"fly"}}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("unknown permission: %v", err)
		}
		if _, err := e.PutRole(ctx, admin, Role{ID: "ok-role", Name: "x", Sources: []string{"nope"}}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("unknown source: %v", err)
		}
		// The administrator cannot be stripped of anything.
		r, err := e.PutRole(ctx, admin, Role{ID: RoleAdmin, Name: "Admin", Permissions: nil, Sources: []string{"orders"}})
		if err != nil || len(r.Permissions) != len(Permissions) || len(r.Sources) != 0 {
			t.Fatalf("admin role: %+v %v", r, err)
		}
		if err := e.DeleteRole(ctx, admin, "operate"); !errors.Is(err, ErrState) {
			t.Fatalf("built-in delete: %v", err)
		}
		if err := e.Require(ctx, Actor{ID: "x", Roles: []string{"read"}}, PermUsers); !errors.Is(err, ErrForbidden) {
			t.Fatalf("require: %v", err)
		}
		if err := e.Require(ctx, admin, PermUsers); err != nil {
			t.Fatal(err)
		}
		if err := e.Note(ctx, admin, PermUsers, "user.create", "bob"); err != nil {
			t.Fatal(err)
		}
		if bad, _, _ := e.VerifyAudit(ctx, admin); bad != 0 {
			t.Fatalf("audit broken at %d", bad)
		}
		// Scoped auditors cannot verify the whole chain.
		e.PutSource(ctx, admin, ordersSource())
		e.PutRole(ctx, admin, Role{ID: "aud", Name: "Auditor", Permissions: []string{PermAudit}, Sources: []string{"orders"}})
		e.rolesMu.Lock()
		e.roles = nil
		e.rolesMu.Unlock()
		if _, _, err := e.VerifyAudit(ctx, Actor{ID: "a", Roles: []string{"aud"}}); !errors.Is(err, ErrForbidden) {
			t.Fatalf("scoped verify: %v", err)
		}
		entries, _ := e.Audit(ctx, Actor{ID: "a", Roles: []string{"aud"}}, 50, 0)
		for _, en := range entries {
			if en.SourceID != "orders" {
				t.Fatalf("scoped audit leaked %+v", en)
			}
		}
	})
}

func TestMonitoringAlertsHealthMetricsAndLogs(t *testing.T) {
	eachStore(t, func(t *testing.T, e *Engine, clk *clock) {
		ctx := context.Background()
		src := ordersSource()
		src.ExpectEvery = Dur(time.Hour)
		if _, err := e.PutSource(ctx, admin, src); err != nil {
			t.Fatal(err)
		}
		e.Hooks.Deliver = func(context.Context, *Source, *Batch, []Row) (string, error) { return "", errors.New("billing down") }
		e.Beat()
		res, _ := e.Ingest(WithTrace(ctx, "trace-abc"), admin, "orders", "m1", rows(t, goodCSV+"5,-1,NPR,e@x.com\n"))
		if res.Batch.TraceID != "trace-abc" {
			t.Fatalf("trace id %q", res.Batch.TraceID)
		}
		b, _ := e.RunAll(ctx, admin, res.Batch.ID)
		for b.Status == StatusRetrying {
			clk.advance(time.Minute)
			e.Sweep(ctx, 10)
			b, _ = e.Store.GetBatch(ctx, b.ID)
		}
		if b.Status != StatusHeld {
			t.Fatalf("got %s", b.Status)
		}
		var spans int
		for _, ev := range b.Events {
			if ev.Attempt > 0 {
				spans++
			}
		}
		if spans < 5 {
			t.Fatalf("stage events should carry attempts: %+v", b.Events)
		}
		v, err := e.Monitor(ctx, admin, 24*time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		kinds := map[string]bool{}
		for _, a := range v.Alerts {
			kinds[a.Kind] = true
		}
		if !kinds["held"] {
			t.Fatalf("alerts %+v", v.Alerts)
		}
		if v.Alerts[0].Severity != "critical" {
			t.Fatalf("critical first: %+v", v.Alerts)
		}
		if len(v.Sources) != 1 || v.Sources[0].Held != 1 || v.Sources[0].Batches != 1 || v.Sources[0].Freshness != "ok" {
			t.Fatalf("source health %+v", v.Sources)
		}
		if len(v.Series) == 0 || len(v.Stages) != 6 || v.Stages[3].Failures == 0 {
			t.Fatalf("series/stages %+v %+v", v.Series, v.Stages)
		}
		total := 0
		for _, p := range v.Series {
			total += p.Batches
		}
		if total != 1 {
			t.Fatalf("series total %d", total)
		}
		// Quiet for two windows: the source is stale.
		clk.advance(3 * time.Hour)
		v, _ = e.Monitor(ctx, admin, 24*time.Hour)
		if v.Sources[0].Freshness != "stale" {
			t.Fatalf("freshness %s", v.Sources[0].Freshness)
		}
		status, checks := e.Health(ctx)
		if status != "degraded" || len(checks) != 7 {
			t.Fatalf("health %s %+v", status, checks)
		}
		text, err := e.MetricsText(ctx)
		if err != nil || !strings.Contains(text, `etl_batches{status="held"} 1`) || !strings.Contains(text, "etl_stage_duration_seconds_bucket") ||
			!strings.Contains(text, `etl_stage_runs_total{outcome="held",stage="4"} 1`) {
			t.Fatalf("metrics %v\n%s", err, text)
		}
		logs, _ := e.LogsFor(ctx, admin, LogFilter{TraceID: "trace-abc"})
		if len(logs) < 3 {
			t.Fatalf("logs %+v", logs)
		}
		if _, err := e.LogsFor(ctx, Actor{ID: "x"}, LogFilter{}); !errors.Is(err, ErrForbidden) {
			t.Fatal(err)
		}
		tr, _ := e.Trace(ctx, admin, b.ID)
		if len(tr.Logs) == 0 || len(tr.Audit) == 0 || tr.Audit[0].TraceID != "trace-abc" {
			t.Fatalf("trace %+v", tr.Audit)
		}
	})
}
