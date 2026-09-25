package platform

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sessionClaimsApp = `
name "session-claims"
resource "db" {
  kind "database.sql"
  config {
    driver "sqlite"
    dsn env.required("SC_DSN")
    migrations ["CREATE TABLE users (id TEXT PRIMARY KEY, email TEXT, password_hash TEXT, roles TEXT, org_units TEXT)"]
  }
}
resource "sessions" {
  kind "session.memory"
  config { secret env.required("SC_SECRET") cookie "sc" max_age 1h }
}
resource "cookie_auth" {
  kind "auth.session"
  config { session "sessions" claims ["org_units", "department"] }
}
resource "org" {
  kind "org.hierarchy"
  config {
    nodes [
      { id "hq" name "HQ" },
      { id "east" parent "hq" name "East" },
      { id "west" parent "hq" name "West" }
    ]
  }
}
intent "login" {
  response "principal"
  node "user" {
    uses "database.query"
    resource "db"
    requires [input]
    provides [user]
    config { statement "SELECT id, email, password_hash, roles, org_units FROM users WHERE email = $1" args ["input.email"] }
  }
  node "principal" {
    uses "auth.login"
    resource "sessions"
    kind effect
    requires [input, user]
    provides [principal]
    config { roles_field "roles" session_fields ["org_units"] }
  }
}
intent "scope" {
  response "scope"
  node "scope" { uses "org.scope" resource "org" provides [scope] }
}
route "login" { method POST path "/login" intent "login" session "sessions" }
route "scope" { method GET path "/scope" intent "scope" session "sessions" auth "cookie_auth" }
`

func TestSessionClaimsReachOrgScope(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.bcl")
	if err := os.WriteFile(path, []byte(sessionClaimsApp), 0o600); err != nil {
		t.Fatal(err)
	}
	h := newAppHarness(t, path, map[string]string{
		"SC_DSN":    "file:" + filepath.Join(dir, "sc.db"),
		"SC_SECRET": strings.Repeat("c", 48),
	})
	hash, err := HashPasswordArgon2id("correct horse battery", FastArgon2idParams())
	if err != nil {
		t.Fatal(err)
	}
	db, _ := h.platform.Resource("db")
	if _, err := db.(*Database).Exec(`INSERT INTO users VALUES ('u1', 'officer@example.com', ?, 'officer', 'east')`, hash); err != nil {
		t.Fatal(err)
	}
	if status, _ := h.call("GET", "/scope", "", nil); status != 401 {
		t.Fatalf("anonymous scope = %d", status)
	}
	if status, body := h.call("POST", "/login", "", map[string]any{"email": "officer@example.com", "password": "correct horse battery"}); status != 200 {
		t.Fatalf("login = %d %v", status, body)
	}
	status, body := h.call("GET", "/scope", "", nil)
	if status != 200 || Stringify(dig(body, "assigned_ids", 0)) != "east" {
		t.Fatalf("scope from session claims = %d %v", status, body)
	}
}
