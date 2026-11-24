package platform

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

const membersApp = `
name "mem"
resource "jwt" {
  kind "auth.jwt"
  config { secret env.required("WS_JWT_SECRET") }
}
resource "db" {
  kind "database.sql"
  config {
    driver "sqlite"
    dsn "file:%DB%?_pragma=busy_timeout(5000)"
    migrations [
      "CREATE TABLE inbox (id INTEGER PRIMARY KEY AUTOINCREMENT, user_id TEXT NOT NULL, subject TEXT NOT NULL, case_id TEXT NOT NULL DEFAULT '', case_number TEXT NOT NULL DEFAULT '')"
    ]
  }
}
resource "people" {
  kind "identity.users"
  config {
    database "db"
    token_issuer "jwt"
  }
}
resource "cases" {
  kind "pipeline.cases"
  config { directory_resource "people" }
}
pipeline "p" {
  form "f" { input "a" { kind text } }
  stage "s" { roles ["nurse"]  page { group "g" { forms ["f"] } }  action "go" { outcome advance } }
  stage "e" { roles ["nurse"] }
}
intent "deliver" {
  response "r"
  node "r" {
    uses "database.insert_many"
    resource "db"
    kind effect
    requires [input]
    provides [r]
    config {
      table "inbox"
      columns ["subject", "case_id", "case_number"]
      rows "input.items"
      set { user_id "input.user" }
    }
  }
}
`

// Accounts a host provisions itself become real directory members (idempotently), so role
// recipients and assignment find them.
func TestEnsureMemberMakesAccountsDirectoryMembers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.bcl")
	src := []byte(replaceAll(membersApp, "%DB%", filepath.Join(dir, "m.db")))
	if err := os.WriteFile(path, src, 0o600); err != nil {
		t.Fatal(err)
	}
	h := newAppHarness(t, path, map[string]string{"WS_JWT_SECRET": "ws-test-secret-0123456789abcdef-xyz"})
	r, ok := h.platform.Resource("people")
	d, isDir := r.(*IdentityDirectory)
	if !ok || !isDir {
		t.Fatal("no directory")
	}
	ctx := context.Background()
	id1, err := d.EnsureMember(ctx, "t", "nina@demo.local", "Nina", []string{"nurse"}, "city")
	if err != nil || id1 == "" {
		t.Fatalf("create: %v", err)
	}
	id2, err := d.EnsureMember(ctx, "t", "NINA@demo.local", "Nina", []string{"nurse", "coder"}, "river")
	if err != nil || id2 != id1 {
		t.Fatalf("the same account again: %v %s %s", err, id1, id2)
	}
	var found *struct {
		roles []string
		unit  string
	}
	for _, w := range d.Workers(ctx) {
		if w.ID == id1 {
			found = &struct {
				roles []string
				unit  string
			}{w.Roles, fmt.Sprint(w.OrgUnits)}
		}
	}
	if found == nil || !slices.Equal(found.roles, []string{"coder", "nurse"}) || found.unit != "[river]" {
		t.Fatalf("the directory lists the member with its latest roles and unit: %+v", found)
	}
	if _, err := d.EnsureMember(ctx, "t", "not-an-email", "x", nil, ""); err == nil {
		t.Fatal("a valid email is required")
	}
}

// A digest of several notifications becomes one inbox row each, every row linking to its own case.
func TestDigestBecomesOneRowPerItem(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.bcl")
	if err := os.WriteFile(path, []byte(replaceAll(membersApp, "%DB%", filepath.Join(dir, "m.db"))), 0o600); err != nil {
		t.Fatal(err)
	}
	h := newAppHarness(t, path, map[string]string{"WS_JWT_SECRET": "ws-test-secret-0123456789abcdef-xyz"})
	items := []any{
		map[string]any{"subject": "A one", "case_id": "case_1", "case_number": "N-1"},
		map[string]any{"subject": "B two", "case_id": "case_2", "case_number": "N-2"},
	}
	if _, err := h.platform.CallIntent(context.Background(), "deliver", map[string]any{"user": "u1", "items": items}, nil); err != nil {
		t.Fatal(err)
	}
	r, _ := h.platform.Resource("db")
	rows, err := r.(*Database).QueryContext(context.Background(), "SELECT user_id, subject, case_id FROM inbox ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var u, s, c string
		_ = rows.Scan(&u, &s, &c)
		got = append(got, u+"|"+s+"|"+c)
	}
	if !slices.Equal(got, []string{"u1|A one|case_1", "u1|B two|case_2"}) {
		t.Fatalf("one row per item, each with its own case: %v", got)
	}
}

func replaceAll(s, old, new string) string {
	out := ""
	for {
		i := indexOf(s, old)
		if i < 0 {
			return out + s
		}
		out += s[:i] + new
		s = s[i+len(old):]
	}
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
