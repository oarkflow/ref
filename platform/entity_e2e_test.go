package platform

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
)

const entityApp = `
name "entities"

resource "db" {
  kind "database.sql"
  config {
    driver env("ENT_DRIVER", "sqlite")
    dsn env("ENT_DSN")
    migrations ["CREATE TABLE IF NOT EXISTS hook_log (entity TEXT, event TEXT, name TEXT)"]
  }
}

resource "jwt" {
  kind "auth.jwt"
  config {
    secret env.required("ENT_JWT_SECRET")
  }
}

entity "project" {
  title "Project"
  database "db"
  path "/api/projects"
  auth "jwt"
  tenant_scoped true
  soft_delete true
  versioned true
  search ["name", "code"]
  default_sort "name"
  export true
  aggregate true
  column "name" {
    kind text
    required true
    min_length 2
    max_length 100
  }
  column "code" {
    kind text
    unique true
    pattern "^[A-Z]{2,5}-[0-9]+$"
    immutable true
  }
  column "status" {
    kind text
    options ["draft", "active", "closed"]
    default "draft"
  }
  column "budget" {
    kind number
    min 0
  }
  column "priority" {
    kind integer
  }
  column "cost" {
    kind decimal
    min 0
  }
  column "public" {
    kind boolean
  }
  column "tags" {
    kind json
  }
  column "score" {
    kind number
    read_only true
  }
  column "secret_note" {
    kind text
    hidden true
  }
  allow "*" {
    roles ["staff", "admin"]
  }
  allow "delete" {
    roles ["admin"]
  }
  allow "update" {
    roles ["staff"]
    condition "record.status != 'closed'"
  }
  on "created" {
    hook "log.hook"
  }
  on "deleted" {
    hook "log.hook"
  }
}

entity "note" {
  database "db"
  auth "jwt"
  owner_scoped true
  owner_bypass_roles ["admin"]
  column "body" {
    kind text
    required true
  }
}

intent "log.hook" {
  response "n"
  node "n" {
    uses "database.exec"
    resource "db"
    kind effect
    requires [input]
    provides [n]
    config {
      statement "INSERT INTO hook_log (entity, event, name) VALUES ($1, $2, $3)"
      args ["input.entity", "input.event", "input.record.name"]
    }
  }
}

intent "log.list" {
  response "rows"
  node "rows" {
    uses "database.query"
    resource "db"
    provides [rows]
    config {
      statement "SELECT entity, event, name FROM hook_log"
    }
  }
}

route "log" {
  method GET
  path "/api/log"
  intent "log.list"
  auth "jwt"
}
`

func TestEntitiesEndToEnd(t *testing.T) {
	dir := t.TempDir()
	runEntities(t, "sqlite", "file:"+filepath.Join(dir, "e.db")+"?_pragma=busy_timeout(5000)")
}

// TestEntitiesPostgres runs the same journey on PostgreSQL when
// TEST_POSTGRES_DSN is set; the database should be empty (a fresh one per run).
func TestEntitiesPostgres(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN not set")
	}
	runEntities(t, "pgx", dsn)
}

func runEntities(t *testing.T, driver, dsn string) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.bcl")
	if err := os.WriteFile(path, []byte(entityApp), 0o600); err != nil {
		t.Fatal(err)
	}
	h := newAppHarness(t, path, map[string]string{
		"ENT_DRIVER":     driver,
		"ENT_DSN":        dsn,
		"ENT_JWT_SECRET": "entity-test-secret-0123456789abcdef-xyz",
	})
	staff := h.token("jwt", "u1", []string{"staff"}, map[string]any{"tenant_id": "acme"})
	staff2 := h.token("jwt", "u2", []string{"staff"}, map[string]any{"tenant_id": "acme"})
	other := h.token("jwt", "u9", []string{"staff"}, map[string]any{"tenant_id": "globex"})
	admin := h.token("jwt", "root", []string{"admin"}, map[string]any{"tenant_id": "acme"})
	viewer := h.token("jwt", "v1", []string{"viewer"}, map[string]any{"tenant_id": "acme"})

	// Validation: every problem at once, unknown and read-only fields refused.
	status, body := h.call("POST", "/api/projects", staff, map[string]any{
		"name": "X", "code": "bad", "status": "open", "budget": -5, "score": 9, "colour": "red",
	})
	details := fmt.Sprint(body)
	if status != 422 {
		t.Fatalf("invalid create: %d %v", status, body)
	}
	for _, want := range []string{"name", "code", "status", "budget", "score", "colour"} {
		if !strings.Contains(details, "path:"+want) {
			t.Fatalf("missing error for %s: %v", want, body)
		}
	}

	// Create a few.
	mk := func(tok, name, code string, budget float64, extra map[string]any) map[string]any {
		t.Helper()
		rec := map[string]any{"name": name, "code": code, "budget": budget, "tags": []any{"a", "b"}, "public": true, "priority": 2, "secret_note": "hush"}
		for k, v := range extra {
			rec[k] = v
		}
		status, body := h.call("POST", "/api/projects", tok, rec)
		if status != 201 {
			t.Fatalf("create %s: %d %v", name, status, body)
		}
		return body.(map[string]any)
	}
	p1 := mk(staff, "Bridge repair", "BR-1", 5000, map[string]any{"status": "active"})
	if p1["status"] != "active" || p1["version"] != float64(1) || p1["created_by"] != "u1" || p1["public"] != true || p1["secret_note"] != nil {
		t.Fatalf("created: %v", p1)
	}
	if tags, _ := p1["tags"].([]any); len(tags) != 2 {
		t.Fatalf("json column: %v", p1["tags"])
	}
	mk(staff, "Road survey", "RS-2", 12000, nil)
	mk(staff2, "School roof", "SR-3", 8000, map[string]any{"status": "closed"})
	mk(other, "Other tenant", "OT-1", 1, nil)
	if status, _ := h.call("POST", "/api/projects", staff, map[string]any{"name": "Dup", "code": "BR-1"}); status != 409 {
		t.Fatalf("unique: %d", status)
	}

	// List: tenant-scoped, sorted, totals, filters, search, pagination.
	status, body = h.call("GET", "/api/projects", staff, nil)
	items := dig(body, "items").([]any)
	if status != 200 || dig(body, "total") != float64(3) || len(items) != 3 || dig(items, 0, "name") != "Bridge repair" {
		t.Fatalf("list: %d %v", status, body)
	}
	for q, want := range map[string]float64{
		"status=draft": 1, "budget__gte=6000": 2, "status__in=draft,closed": 2, "q=ROAD": 1, "budget__lt=100": 0,
		"public=true": 3, "created_by=u2": 1, "name__like=roo": 1,
	} {
		_, body := h.call("GET", "/api/projects?"+q, staff, nil)
		if dig(body, "total") != want {
			t.Fatalf("filter %s: %v", q, body)
		}
	}
	_, body = h.call("GET", "/api/projects?sort=-budget&limit=1&offset=1", staff, nil)
	if dig(body, "items", 0, "name") != "School roof" || dig(body, "total") != float64(3) {
		t.Fatalf("sort/page: %v", body)
	}
	if status, _ := h.call("GET", "/api/projects?colour=red", staff, nil); status != 422 {
		t.Fatalf("unknown filter: %d", status)
	}
	if status, _ := h.call("GET", "/api/projects?sort=secret_note", staff, nil); status != 422 {
		t.Fatalf("sort by hidden: %d", status)
	}

	// Access rules: viewers may not list; other tenants cannot see acme's rows.
	if status, _ := h.call("GET", "/api/projects", viewer, nil); status != 403 {
		t.Fatalf("viewer list: %d", status)
	}
	id := fmt.Sprint(p1["id"])
	if status, _ := h.call("GET", "/api/projects/"+id, other, nil); status != 404 {
		t.Fatalf("cross-tenant get: %d", status)
	}

	// Update: versioned, immutable fields, row condition.
	if status, _ := h.call("PATCH", "/api/projects/"+id, staff, map[string]any{"budget": 6000}); status != 422 {
		t.Fatalf("update without version: %d", status)
	}
	status, body = h.call("PATCH", "/api/projects/"+id, staff, map[string]any{"budget": 6000, "version": 1})
	if status != 200 || dig(body, "budget") != float64(6000) || dig(body, "version") != float64(2) {
		t.Fatalf("update: %d %v", status, body)
	}
	if status, _ := h.call("PATCH", "/api/projects/"+id, staff2, map[string]any{"budget": 7000, "version": 1}); status != 409 {
		t.Fatalf("stale version: %d", status)
	}
	if status, _ := h.call("PATCH", "/api/projects/"+id, staff, map[string]any{"code": "XX-9", "version": 2}); status != 422 {
		t.Fatalf("immutable: %d", status)
	}
	_, list := h.call("GET", "/api/projects?status=closed", staff, nil)
	closedID := fmt.Sprint(dig(list, "items", 0, "id"))
	if status, _ := h.call("PATCH", "/api/projects/"+closedID, staff, map[string]any{"name": "Reopen", "version": 1}); status != 403 {
		t.Fatalf("row condition: %d", status)
	}

	// Aggregates and CSV export.
	_, body = h.call("GET", "/api/projects/-/aggregate?group_by=status&agg=sum&field=budget", staff, nil)
	sums := map[string]any{}
	for _, r := range dig(body, "rows").([]any) {
		sums[fmt.Sprint(dig(r, "group"))] = dig(r, "value")
	}
	if sums["active"] != float64(6000) || sums["draft"] != float64(12000) || sums["closed"] != float64(8000) {
		t.Fatalf("aggregate: %v", body)
	}
	req, _ := http.NewRequest("GET", h.base+"/api/projects/-/export?sort=name", nil)
	req.Header.Set("Authorization", "Bearer "+staff)
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	csvBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	lines := strings.Split(strings.TrimSpace(string(csvBody)), "\n")
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/csv") || len(lines) != 4 ||
		!strings.Contains(lines[1], "Bridge repair") || strings.Contains(string(csvBody), "hush") {
		t.Fatalf("export: %d %s\n%s", resp.StatusCode, resp.Header.Get("Content-Type"), csvBody)
	}

	// Decimals are exact: 0.10 + 0.20 sums to exactly 0.30.
	for _, c := range []struct{ name, code, cost string }{{"Dec one", "DA-1", "0.10"}, {"Dec two", "DA-2", "0.2"}} {
		status, body := h.call("POST", "/api/projects", staff2, map[string]any{"name": c.name, "code": c.code, "cost": c.cost, "status": "draft"})
		if status != 201 {
			t.Fatalf("decimal create: %d %v", status, body)
		}
	}
	_, body = h.call("GET", "/api/projects?code__in=DA-1,DA-2&sort=code", staff, nil)
	if dig(body, "items", 0, "cost") != "0.10" || dig(body, "items", 1, "cost") != "0.20" {
		t.Fatalf("decimal values: %v", body)
	}
	_, body = h.call("GET", "/api/projects/-/aggregate?agg=sum&field=cost&code__in=DA-1,DA-2", staff, nil)
	if dig(body, "rows", 0, "value") != "0.30" {
		t.Fatalf("decimal sum: %v", body)
	}
	if _, body := h.call("GET", "/api/projects?cost__gte=0.15", staff, nil); dig(body, "total") != float64(1) {
		t.Fatalf("decimal filter: %v", body)
	}
	if status, _ := h.call("POST", "/api/projects", staff, map[string]any{"name": "Too precise", "cost": "1.234"}); status != 422 {
		t.Fatalf("decimal precision: %d", status)
	}

	// Delete: admins only, soft.
	if status, _ := h.call("DELETE", "/api/projects/"+id, staff, nil); status != 403 {
		t.Fatalf("staff delete: %d", status)
	}
	if status, _ := h.call("DELETE", "/api/projects/"+id, admin, nil); status != 200 {
		t.Fatalf("admin delete: %d", status)
	}
	if status, _ := h.call("GET", "/api/projects/"+id, staff, nil); status != 404 {
		t.Fatalf("get after soft delete: %d", status)
	}
	if _, body := h.call("GET", "/api/projects", staff, nil); dig(body, "total") != float64(4) { // 3 + 2 decimal rows - 1 deleted
		t.Fatalf("list after delete: %v", body)
	}

	// Owner scoping: each user sees their own notes; admins see all.
	h.call("POST", "/api/notes", staff, map[string]any{"body": "mine"})
	h.call("POST", "/api/notes", staff2, map[string]any{"body": "theirs"})
	if _, body := h.call("GET", "/api/notes", staff, nil); dig(body, "total") != float64(1) {
		t.Fatalf("owner scope: %v", body)
	}
	if _, body := h.call("GET", "/api/notes", admin, nil); dig(body, "total") != float64(2) {
		t.Fatalf("owner bypass: %v", body)
	}

	// Hooks ran after each committed create and delete.
	_, body = h.call("GET", "/api/log", staff, nil)
	logs := fmt.Sprint(body)
	if strings.Count(logs, "created") != 6 || !strings.Contains(logs, "deleted") {
		t.Fatalf("hooks: %v", body)
	}
}

func TestDecimalParseFormat(t *testing.T) {
	for _, tc := range []struct {
		in    any
		scale int
		want  int64
		ok    bool
	}{
		{"1250.5", 2, 125050, true}, {1250.5, 2, 125050, true}, {"-3", 2, -300, true}, {int64(7), 0, 7, true},
		{".5", 2, 50, true}, {"1.234", 2, 0, false}, {"12a", 2, 0, false}, {"1e5", 2, 0, false},
		{"99999999999999999999", 2, 0, false}, {true, 2, 0, false},
	} {
		got, err := parseDecimal(tc.in, tc.scale)
		if (err == nil) != tc.ok || (tc.ok && got != tc.want) {
			t.Errorf("parseDecimal(%v, %d) = %d, %v", tc.in, tc.scale, got, err)
		}
	}
	for minor, want := range map[int64]string{125050: "1250.50", 5: "0.05", -300: "-3.00", 0: "0.00"} {
		if got := formatDecimal(minor, 2); got != want {
			t.Errorf("formatDecimal(%d) = %s, want %s", minor, got, want)
		}
	}
	if formatDecimal(7, 0) != "7" {
		t.Error("scale 0")
	}
}
