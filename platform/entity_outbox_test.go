package platform

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

const entityOutboxApp = `
name "entity-outbox"

resource "db" {
  kind "database.sql"
  config {
    driver env("EO_DRIVER", "sqlite")
    dsn env("EO_DSN")
    migrations [
      "CREATE TABLE IF NOT EXISTS switch (on_ INTEGER NOT NULL)",
      "CREATE TABLE IF NOT EXISTS synced (event_id TEXT PRIMARY KEY, code TEXT NOT NULL, event TEXT NOT NULL, actor TEXT NOT NULL)"
    ]
  }
}

resource "jwt" {
  kind "auth.jwt"
  config {
    secret env.required("EO_JWT_SECRET")
  }
}

entity "invoice" {
  database "db"
  auth "jwt"
  column "code" {
    required true
    unique true
  }
  column "amount" {
    kind decimal
  }
  on "created" {
    hook "sync"
    durable true
    max_attempts 2
    retry_base "10ms"
  }
  on "deleted" {
    hook "sync"
    durable true
  }
}

# Fails (not found) until the switch is on; idempotent on event_id.
intent "sync" {
  response "n"
  node "n" {
    uses "database.exec"
    resource "db"
    kind effect
    requires [input]
    provides [n]
    config {
      statement "INSERT INTO synced (event_id, code, event, actor) SELECT $1, $2, $3, $4 WHERE EXISTS (SELECT 1 FROM switch WHERE on_ = 1) ON CONFLICT (event_id) DO NOTHING"
      args ["input.event_id", "input.record.code", "input.event", "input.actor"]
      require_affected true
    }
  }
}

intent "switch_on" {
  response "n"
  node "n" {
    uses "database.exec"
    resource "db"
    kind effect
    provides [n]
    config {
      statement "INSERT INTO switch (on_) VALUES (1)"
    }
  }
}

intent "synced" {
  response "rows"
  node "rows" {
    uses "database.query"
    resource "db"
    provides [rows]
    config {
      statement "SELECT code, event, actor FROM synced ORDER BY event"
    }
  }
}

intent "events" {
  response "r"
  node "r" {
    uses "entity.events"
    resource "db"
    kind effect
    provides [r]
    config {
      entity "invoice"
      roles ["ops"]
    }
  }
}

route "events" { method GET path "/ops/invoice-events" intent "events" auth "jwt" }
route "requeue" { method POST path "/ops/invoice-events/:event_id/:op" intent "events" auth "jwt" }
route "switch" { method POST path "/switch" intent "switch_on" auth "jwt" }
route "synced" { method GET path "/synced" intent "synced" auth "jwt" }
`

func TestEntityDurableHooks(t *testing.T) {
	dir := t.TempDir()
	runEntityDurableHooks(t, "sqlite", "file:"+filepath.Join(dir, "e.db")+"?_pragma=busy_timeout(5000)")
}

func TestEntityDurableHooksPostgres(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN not set")
	}
	runEntityDurableHooks(t, "pgx", freshPostgres(t, dsn))
}

func runEntityDurableHooks(t *testing.T, driver, dsn string) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.bcl")
	if err := os.WriteFile(path, []byte(appSQL(driver, entityOutboxApp)), 0o600); err != nil {
		t.Fatal(err)
	}
	h := newAppHarness(t, path, map[string]string{
		"EO_DRIVER":     driver,
		"EO_DSN":        dsn,
		"EO_JWT_SECRET": "entity-outbox-secret-0123456789abcdef-xyz",
	})
	user := h.token("jwt", "u1", nil, nil)
	ops := h.token("jwt", "op1", []string{"ops"}, nil)

	status, body := h.call("POST", "/api/invoices", user, map[string]any{"code": "INV-1", "amount": "10.50"})
	if status != 201 {
		t.Fatalf("create: %d %v", status, body)
	}
	id := fmt.Sprint(dig(body, "id"))
	// A change that does not commit records no hook event.
	if status, _ := h.call("POST", "/api/invoices", user, map[string]any{"code": "INV-1"}); status != 409 {
		t.Fatalf("duplicate: %d", status)
	}

	// The downstream is failing: two attempts, then the event is dead-lettered.
	var dead []any
	waitFor(t, "dead letter", func() bool {
		_, body := h.call("GET", "/ops/invoice-events", ops, nil)
		dead, _ = dig(body, "dead").([]any)
		return len(dead) == 1
	})
	if dig(dead, 0, "attempts") != float64(2) || dig(dead, 0, "event") != "created" || dig(dead, 0, "last_error") == nil ||
		dig(dead, 0, "input", "record", "amount") != "10.50" {
		t.Fatalf("dead event: %v", dead)
	}
	if status, _ := h.call("GET", "/ops/invoice-events", user, nil); status != 403 {
		t.Fatalf("events without the ops role: %d", status)
	}

	// Fix the cause and requeue: delivered once, with the actor.
	h.call("POST", "/switch", user, map[string]any{})
	if status, body := h.call("POST", "/ops/invoice-events/"+fmt.Sprint(dig(dead, 0, "id"))+"/requeue", ops, map[string]any{}); status != 200 {
		t.Fatalf("requeue: %d %v", status, body)
	}
	if status, _ := h.call("POST", "/ops/invoice-events/"+fmt.Sprint(dig(dead, 0, "id"))+"/requeue", ops, map[string]any{}); status != 404 {
		t.Fatalf("requeue of a live event: %d", status)
	}
	// Deleting delivers straight away now that the downstream works.
	if status, body := h.call("DELETE", "/api/invoices/"+id, user, nil); status != 200 {
		t.Fatalf("delete: %d %v", status, body)
	}
	waitFor(t, "delivery", func() bool {
		_, body := h.call("GET", "/synced", user, nil)
		rows, _ := body.([]any)
		return len(rows) == 2
	})
	_, body = h.call("GET", "/synced", user, nil)
	if dig(body, 0, "event") != "created" || dig(body, 1, "event") != "deleted" || dig(body, 0, "code") != "INV-1" || dig(body, 0, "actor") != "u1" {
		t.Fatalf("synced: %v", body)
	}
	if _, body := h.call("GET", "/ops/invoice-events", ops, nil); len(dig(body, "dead").([]any)) != 0 {
		t.Fatalf("still dead after delivery: %v", body)
	}
}

func TestEntityHookOptionsNeedDurable(t *testing.T) {
	_, err := compileEntity(EntitySpec{Name: "x", Database: "db", Columns: []EntityColumn{{Name: "a"}},
		On: []EntityHook{{Event: "created", Hook: "h", MaxAttempts: 3}}}, "sqlite")
	if err == nil {
		t.Fatal("max_attempts on a non-durable hook was accepted")
	}
}
