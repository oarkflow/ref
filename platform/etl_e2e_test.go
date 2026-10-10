package platform

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const etlApp = `
name "etl"

resource "db" {
  kind "database.sql"
  config {
    driver "sqlite"
    dsn env("ETL_DSN")
    migrations [
      "CREATE TABLE IF NOT EXISTS switch (on_ INTEGER NOT NULL)",
      "CREATE TABLE IF NOT EXISTS dest (batch_key TEXT PRIMARY KEY, batch_id TEXT NOT NULL)",
      "CREATE TABLE IF NOT EXISTS users (id TEXT PRIMARY KEY, roles TEXT NOT NULL, status TEXT NOT NULL)",
      "INSERT INTO users (id, roles, status) VALUES ('mallory', 'read', 'active') ON CONFLICT (id) DO NOTHING",
      "INSERT INTO users (id, roles, status) VALUES ('feeder', 'ingest', 'active') ON CONFLICT (id) DO NOTHING",
      "INSERT INTO users (id, roles, status) VALUES ('olive', 'read,replay', 'active') ON CONFLICT (id) DO NOTHING",
      "INSERT INTO users (id, roles, status) VALUES ('nobody', '', 'active') ON CONFLICT (id) DO NOTHING",
      "INSERT INTO users (id, roles, status) VALUES ('root', 'admin', 'active') ON CONFLICT (id) DO NOTHING",
      "INSERT INTO users (id, roles, status) VALUES ('olu', 'orders-only', 'active') ON CONFLICT (id) DO NOTHING"
    ]
  }
}

resource "jwt" {
  kind "auth.jwt"
  config { secret env.required("ETL_JWT_SECRET") }
}

resource "flow" {
  kind "etl.engine"
  config {
    database "db"
    principal_query "SELECT roles, status FROM users WHERE id = $1"
    deliver "dest.put"
    poll "10ms"
    sources [
      {
        id "orders" name "Orders" owner "sales-data" format "csv" destination "dest" max_reject_rate 0.5 reject_duplicates true
        retry { max_attempts 2 base "10ms" cap "20ms" }
        rules [
          { name "id required" column "id" check "required" }
          { name "amount" column "amount" check "range" min 0 }
          { name "big" check "expr" expr "amount < 1000" }
        ]
      }
    ]
  }
}

# Refuses until the switch is on; idempotent on the batch key.
intent "dest.put" {
  response "n"
  node "n" {
    uses "database.exec"
    resource "db"
    kind effect
    requires [input]
    provides [n]
    config {
      statement "INSERT INTO dest (batch_key, batch_id) SELECT $1, $2 WHERE EXISTS (SELECT 1 FROM switch WHERE on_ = 1) ON CONFLICT (batch_key) DO UPDATE SET batch_id = excluded.batch_id"
      args ["input.batch.key", "input.batch.id"]
      require_affected true
    }
  }
}

intent "switch_on" {
  response "n"
  node "n" { uses "database.exec" resource "db" kind effect provides [n] config { statement "INSERT INTO switch (on_) VALUES (1)" } }
}

intent "ingest"  { response "r" node "r" { uses "etl.ingest" resource "flow" kind effect requires [input] provides [r] } }
intent "batch"   { response "r" node "r" { uses "etl.batch" resource "flow" provides [r] } }
intent "replay"  { response "r" node "r" { uses "etl.replay" resource "flow" kind effect provides [r] } }
intent "summary" { response "r" node "r" { uses "etl.summary" resource "flow" provides [r] } }
intent "verify"  { response "r" node "r" { uses "etl.verify_audit" resource "flow" provides [r] } }

intent "set_status" { response "r" node "r" { uses "database.exec" resource "db" kind effect provides [r] config { statement "UPDATE users SET status = 'disabled' WHERE id = 'mallory'" } } }
intent "me"      { response "r" node "r" { uses "etl.me" resource "flow" provides [r] } }
intent "health"  { response "r" node "r" { uses "etl.health" resource "flow" provides [r] } }
intent "metrics" { response "r" node "r" { uses "etl.metrics" resource "flow" provides [r] } }
intent "monitor" { response "r" node "r" { uses "etl.monitor" resource "flow" provides [r] } }
intent "roles"   { response "r" node "r" { uses "etl.roles" resource "flow" provides [r] } }
intent "role_put" { response "r" node "r" { uses "etl.role_put" resource "flow" kind effect requires [input] provides [r] } }
intent "gate"    { response "r" node "r" { uses "etl.require" resource "flow" provides [r] config { permission "users.manage" } } }

route "disable" { method POST path "/disable" intent "set_status" auth "jwt" }
route "me"      { method GET  path "/me" intent "me" auth "jwt" }
route "health"  { method GET  path "/healthz" intent "health" allow_anonymous true }
route "hdetail" { method GET  path "/health" intent "health" auth "jwt" }
route "metrics" { method GET  path "/metrics" intent "metrics" auth "jwt" }
route "monitor" { method GET  path "/monitor" intent "monitor" auth "jwt" }
route "roles"   { method GET  path "/roles" intent "roles" auth "jwt" }
route "roleput" { method PUT  path "/roles" intent "role_put" auth "jwt" }
route "gate"    { method GET  path "/gate" intent "gate" auth "jwt" }
route "ingest"  { method POST path "/sources/:source/batches" intent "ingest" auth "jwt" status 202 }
route "batch"   { method GET  path "/batches/:id" intent "batch" auth "jwt" }
route "replay"  { method POST path "/batches/:id/replay" intent "replay" auth "jwt" }
route "summary" { method GET  path "/summary" intent "summary" auth "jwt" }
route "verify"  { method GET  path "/audit/verify" intent "verify" auth "jwt" }
route "switch"  { method POST path "/switch" intent "switch_on" auth "jwt" }
`

func TestETLEngineEndToEnd(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.bcl")
	if err := os.WriteFile(path, []byte(etlApp), 0o600); err != nil {
		t.Fatal(err)
	}
	h := newAppHarness(t, path, map[string]string{
		"ETL_DSN":        "file:" + filepath.Join(dir, "e.db") + "?_pragma=busy_timeout(5000)",
		"ETL_JWT_SECRET": "etl-test-secret-0123456789abcdef-xyz",
	})
	ingest := h.token("jwt", "feeder", []string{"ingest"}, nil)
	operator := h.token("jwt", "olive", []string{"read", "replay"}, nil)
	nobody := h.token("jwt", "nobody", nil, nil)

	csv := "id,amount\n1,10\n2,20\n3,-5\n4,5000\n5,7\n"
	status, body := h.call("POST", "/sources/orders/batches", ingest, map[string]any{"key": "feed-1", "data": csv})
	if status != 202 {
		t.Fatalf("ingest: %d %v", status, body)
	}
	id := fmt.Sprint(dig(body, "batch", "id"))
	if dig(body, "batch", "quarantined") != float64(2) {
		t.Fatalf("expected 2 refused rows (negative amount, expression): %v", body)
	}
	if status, _ := h.call("POST", "/sources/orders/batches", nobody, map[string]any{"key": "x", "data": csv}); status != 403 {
		t.Fatalf("ingest without a role: %d", status)
	}
	if status, body := h.call("POST", "/sources/orders/batches", ingest, map[string]any{"key": "feed-1b", "data": csv}); status != 409 || dig(body, "error", "code") != "DUPLICATE_CONTENT" {
		t.Fatalf("same content under a new key must be refused: %d %v", status, body)
	}
	if _, body := h.call("POST", "/sources/orders/batches", ingest, map[string]any{"key": "feed-1", "data": csv}); dig(body, "duplicate") != true {
		t.Fatalf("repeat should be a duplicate: %v", body)
	}

	// The destination refuses until the switch is on: retry, then hold.
	waitFor(t, "held", func() bool {
		_, body := h.call("GET", "/batches/"+id, operator, nil)
		return dig(body, "batch", "status") == "held"
	})
	if status, _ := h.call("POST", "/batches/"+id+"/replay", ingest, nil); status != 403 {
		t.Fatalf("replay by an ingester: %d", status)
	}
	h.call("POST", "/switch", operator, map[string]any{})
	if status, body := h.call("POST", "/batches/"+id+"/replay", operator, nil); status != 200 {
		t.Fatalf("replay: %d %v", status, body)
	}
	waitFor(t, "delivered", func() bool {
		_, body := h.call("GET", "/batches/"+id, operator, nil)
		return dig(body, "batch", "status") == "delivered"
	})
	_, body = h.call("GET", "/batches/"+id, operator, nil)
	if dig(body, "batch", "delivered") != float64(3) {
		t.Fatalf("delivered: %v", body)
	}
	if lineage, _ := dig(body, "lineage").([]any); len(lineage) < 3 {
		t.Fatalf("lineage: %v", body)
	}
	_, body = h.call("GET", "/summary", operator, nil)
	if dig(body, "rows_lost") != float64(0) || dig(body, "delivered") != float64(3) {
		t.Fatalf("summary: %v", body)
	}
	_, body = h.call("GET", "/audit/verify", operator, nil)
	if dig(body, "ok") != true {
		t.Fatalf("audit: %v", body)
	}

	// Access: a custom role limited to one source, health and metrics by permission.
	admin := h.token("jwt", "root", []string{"admin"}, nil)
	if status, body := h.call("PUT", "/roles", admin, map[string]any{"id": "orders-only", "name": "Orders only", "permissions": []string{"read", "monitor.read"}, "sources": []string{"orders"}}); status != 200 {
		t.Fatalf("role put: %d %v", status, body)
	}
	if status, _ := h.call("PUT", "/roles", operator, map[string]any{"id": "x-role", "name": "x"}); status != 403 {
		t.Fatalf("role put without permission: %d", status)
	}
	scoped := h.token("jwt", "olu", []string{"orders-only"}, nil)
	_, body = h.call("GET", "/me", scoped, nil)
	if dig(body, "permissions", "read", 0) != "orders" {
		t.Fatalf("me: %v", body)
	}
	if status, _ := h.call("GET", "/gate", scoped, nil); status != 403 {
		t.Fatalf("gate: %d", status)
	}
	if status, _ := h.call("GET", "/gate", admin, nil); status != 200 {
		t.Fatalf("gate admin: %d", status)
	}
	if status, body := h.call("GET", "/healthz", "", nil); status != 200 || dig(body, "checks") != nil {
		t.Fatalf("anonymous health shows detail or fails: %d %v", status, body)
	}
	if _, body := h.call("GET", "/health", scoped, nil); dig(body, "checks") == nil {
		t.Fatalf("health detail for a monitor: %v", body)
	}
	if status, _ := h.call("GET", "/metrics", nobody, nil); status != 403 {
		t.Fatalf("metrics without monitor.read: %d", status)
	}
	status, body = h.call("GET", "/monitor", scoped, nil)
	if status != 200 || len(dig(body, "sources").([]any)) != 1 {
		t.Fatalf("scoped monitor: %d %v", status, body)
	}

	// The session or token says "admin", the user table says "read": the table wins,
	// and disabling the account ends access at once.
	mallory := h.token("jwt", "mallory", []string{"admin"}, nil)
	if status, _ := h.call("PUT", "/roles", mallory, map[string]any{"id": "evil", "name": "Evil", "permissions": []string{"ingest"}}); status != 403 {
		t.Fatalf("a stale admin claim must not grant admin: %d", status)
	}
	_, body = h.call("GET", "/me", mallory, nil)
	perms, _ := dig(body, "permissions").(map[string]any)
	if _, hasRoles := perms["roles.manage"]; hasRoles {
		t.Fatalf("me should reflect the user table, not the claim: %v", body)
	}
	if _, hasRead := perms["read"]; !hasRead {
		t.Fatalf("me should show what the table grants: %v", body)
	}
	time.Sleep(2100 * time.Millisecond) // the identity cache lives two seconds
	h.call("POST", "/disable", admin, map[string]any{})
	time.Sleep(2100 * time.Millisecond)
	if status, body := h.call("GET", "/me", mallory, nil); status != 401 {
		t.Fatalf("a disabled account must be signed out: %d %v", status, body)
	}
}
