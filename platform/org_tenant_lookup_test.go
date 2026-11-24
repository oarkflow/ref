package platform

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const tenantLookupApp = `
name "tl"

resource "db" {
  kind "database.sql"
  config { driver "sqlite"  dsn "file:%DB%?_pragma=busy_timeout(5000)" }
}
resource "jwt" {
  kind "auth.jwt"
  config { secret env.required("TL_JWT_SECRET") }
}
resource "org" {
  kind "org.hierarchy"
  config {
    levels ["group", "facility"]
    database "db"
    assignment_claim "org_units"
    nodes [
      { id "g" level "group" name "Group" },
      { id "a" parent "g" level "facility" name "Hospital A" }
    ]
    lookups [ { set "providers" node "a" code "dr-a" label "Dr A" } ]
  }
}
resource "cases" {
  kind "pipeline.cases"
  config { org_resource "org" }
}
pipeline "enc" {
  workspace { level "facility"  required true }
  form "f" { input "provider" { kind select  lookup "providers" } }
  stage "intake" {
    roles ["nurse"]
    page { group "g" { forms ["f"] } }
    action "go" { outcome advance }
  }
}
intent "start" { response "r"  node "r" { uses "pipeline.start"  resource "cases"  kind effect  requires [input]  provides [r] } }
intent "write" { response "r"  node "r" { uses "lookup.write"  resource "org"  kind effect  requires [input]  provides [r]  config { operation "upsert"  roles ["admin"] } } }
intent "tree"  { response "r"  node "r" { uses "org.query"  resource "org"  provides [r]  config { operation "tree" } } }
route "start" { method POST  path "/cases"  intent "start"  auth "jwt"  status 201 }
route "write" { method POST  path "/lookup"  intent "write"  auth "jwt" }
route "tree"  { method GET   path "/tree"  intent "tree"  auth "jwt" }
`

// A tenant that has only written reference data of its own must keep working
// inside the shared tree, with the shared reference data beneath its own.
func TestTenantWithOnlyLookupRowsKeepsTheSharedWorkspaces(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.bcl")
	if err := os.WriteFile(path, []byte(strings.ReplaceAll(tenantLookupApp, "%DB%", filepath.Join(dir, "tl.db"))), 0o600); err != nil {
		t.Fatal(err)
	}
	h := newAppHarness(t, path, map[string]string{"TL_JWT_SECRET": strings.Repeat("k", 48)})
	claims := map[string]any{"org_units": []string{"a"}, "tenant_id": "t1"}
	admin := h.token("jwt", "adm", []string{"admin"}, claims)
	nurse := h.token("jwt", "nur", []string{"nurse"}, claims)

	if status, r := h.call("POST", "/lookup", admin, map[string]any{"set": "providers", "node_id": "a", "code": "dr-t1", "label": "Dr T1"}); status != 200 {
		t.Fatalf("write: %d %v", status, r)
	}
	// A restart rebuilds every tenant's snapshot from the database.
	h = newAppHarness(t, path, map[string]string{"TL_JWT_SECRET": strings.Repeat("k", 48)})
	nurse = h.token("jwt", "nur", []string{"nurse"}, claims)
	if status, r := h.call("GET", "/tree", nurse, nil); status != 200 || r == nil {
		t.Fatalf("tree: %d %v", status, r)
	}
	if status, r := h.call("POST", "/cases", nurse, map[string]any{"data": map[string]any{}, "org_unit": "a"}); status != 201 {
		t.Fatalf("after writing its own reference data the tenant must still see the shared workspaces: %d %v", status, r)
	}
}
