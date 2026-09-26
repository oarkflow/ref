package platform

import (
	"os"
	"path/filepath"
	"testing"
)

const keepAliveApp = `
name "keepalive"
resource "db" {
  kind "database.sql"
  config { driver "sqlite" dsn env.required("KA_DSN") migrations ["CREATE TABLE audit (msg TEXT)"] }
}
intent "do" {
  response "out"
  # Nothing consumes the audit write or the guard; both must still run.
  node "audit" {
    uses "database.exec"
    resource "db"
    kind effect
    requires [input]
    config { statement "INSERT INTO audit (msg) VALUES ('hit')" }
  }
  node "guard" {
    uses "decision.expression"
    requires [input]
    provides [allowed]
    config { expression "input.amount <= 100 && input.currency == 'USD'" message "limit exceeded" }
  }
  node "out" { uses "collect" requires [input] provides [out] }
}
intent "count" {
  response "n"
  node "n" { uses "database.query" resource "db" provides [n] config { statement "SELECT COUNT(*) AS c FROM audit" } }
}
route "do" { method POST path "/do" intent "do" }
route "count" { method GET path "/count" intent "count" }
`

func TestUnconsumedEffectsAndGuardsRun(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.bcl")
	if err := os.WriteFile(path, []byte(keepAliveApp), 0o600); err != nil {
		t.Fatal(err)
	}
	h := newAppHarness(t, path, map[string]string{"KA_DSN": "file:" + filepath.Join(dir, "ka.db")})

	status, body := h.call("POST", "/do", "", map[string]any{"amount": 50, "currency": "USD"})
	if status != 200 {
		t.Fatalf("allowed request = %d %v", status, body)
	}
	for key := range body.(map[string]any) {
		if len(key) > 1 && key[:2] == "__" {
			t.Fatalf("synthetic fact %q leaked into the response: %v", key, body)
		}
	}
	// The guard's second clause must be evaluated, not just the first.
	if status, body := h.call("POST", "/do", "", map[string]any{"amount": 50, "currency": "EUR"}); status != 403 || dig(body, "error", "message") != "limit exceeded" {
		t.Fatalf("guard not enforced, or its message lost: %d %v", status, body)
	}
	_, counts := h.call("GET", "/count", "", nil)
	if Stringify(dig(counts, 0, "c")) != "1" {
		t.Fatalf("the unconsumed effect ran %v times, want exactly 1 (denied request must not write)", dig(counts, 0, "c"))
	}
}
