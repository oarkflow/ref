package platform

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const outboxApp = `
name "outbox"

resource "db" {
  kind "database.sql"
  config {
    driver "sqlite"
    dsn env("OB_DSN")
    migrations [
      "CREATE TABLE IF NOT EXISTS switch (on_ INTEGER NOT NULL)",
      "CREATE TABLE IF NOT EXISTS delivered (case_number TEXT NOT NULL, event TEXT NOT NULL)"
    ]
  }
}

resource "jwt" {
  kind "auth.jwt"
  config {
    secret env.required("OB_JWT_SECRET")
  }
}

resource "cases" {
  kind "pipeline.cases"
  config {
    database "db"
    event_max_attempts 2
    event_retry_base "10ms"
  }
}

pipeline "ticket" {
  on "case.started" {
    hook "deliver"
  }
  form "t" {
    input "title" {
      kind text
    }
  }
  stage "open" {
    public true
    page {
      group "g" {
        forms ["t"]
      }
    }
  }
}

# Fails (not found) until the switch is on, then records the delivery.
intent "deliver" {
  response "n"
  node "n" {
    uses "database.exec"
    resource "db"
    kind effect
    requires [input]
    provides [n]
    config {
      statement "INSERT INTO delivered (case_number, event) SELECT $1, $2 WHERE EXISTS (SELECT 1 FROM switch WHERE on_ = 1)"
      args ["input.case.number", "input.event.name"]
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

intent "delivered" {
  response "rows"
  node "rows" {
    uses "database.query"
    resource "db"
    provides [rows]
    config {
      statement "SELECT case_number, event FROM delivered"
    }
  }
}

intent "start" {
  response "case"
  node "case" {
    uses "pipeline.start"
    resource "cases"
    kind effect
    requires [input]
    provides [case]
  }
}

intent "events" {
  response "r"
  node "r" {
    uses "pipeline.events"
    resource "cases"
    kind effect
    provides [r]
    config {
      roles ["ops"]
    }
  }
}

route "start" { method POST path "/start" intent "start" auth "jwt" status 201 }
route "events" { method GET path "/events" intent "events" auth "jwt" }
route "requeue" { method POST path "/events/:event_id/:op" intent "events" auth "jwt" }
route "switch" { method POST path "/switch" intent "switch_on" auth "jwt" }
route "delivered" { method GET path "/delivered" intent "delivered" auth "jwt" }
`

func TestPipelineOutboxRetryDeadLetterRequeue(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.bcl")
	if err := os.WriteFile(path, []byte(outboxApp), 0o600); err != nil {
		t.Fatal(err)
	}
	h := newAppHarness(t, path, map[string]string{
		"OB_DSN":        "file:" + filepath.Join(dir, "o.db") + "?_pragma=busy_timeout(5000)",
		"OB_JWT_SECRET": "outbox-test-secret-0123456789abcdef-xyz",
	})
	user := h.token("jwt", "u1", nil, nil)
	ops := h.token("jwt", "op1", []string{"ops"}, nil)

	status, body := h.call("POST", "/start", user, map[string]any{})
	if status != 201 {
		t.Fatalf("start: %d %v", status, body)
	}
	number := fmt.Sprint(dig(body, "case", "number"))

	// The hook fails twice and the event is dead-lettered.
	var dead []any
	waitFor(t, "dead letter", func() bool {
		_, body := h.call("GET", "/events", ops, nil)
		dead, _ = dig(body, "dead").([]any)
		return len(dead) == 1
	})
	if dig(dead, 0, "attempts") != float64(2) || dig(dead, 0, "event", "event") != "case.started" || dig(dead, 0, "last_error") == nil {
		t.Fatalf("dead event: %v", dead)
	}
	if status, _ := h.call("GET", "/events", user, nil); status != 403 {
		t.Fatalf("events without ops role: %d", status)
	}

	// Fix the cause, requeue: delivered exactly once.
	h.call("POST", "/switch", user, map[string]any{})
	if status, body := h.call("POST", "/events/"+fmt.Sprint(dig(dead, 0, "id"))+"/requeue", ops, map[string]any{}); status != 200 {
		t.Fatalf("requeue: %d %v", status, body)
	}
	waitFor(t, "delivery", func() bool {
		_, body := h.call("GET", "/delivered", user, nil)
		rows, _ := body.([]any)
		return len(rows) == 1 && dig(rows, 0, "case_number") == number
	})
	if _, body := h.call("GET", "/events", ops, nil); len(dig(body, "dead").([]any)) != 0 {
		t.Fatalf("still dead after delivery: %v", body)
	}
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
