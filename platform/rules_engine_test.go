package platform

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const rulesEngineApp = `
name "rules"

resource "policy" {
  kind "rules.engine"
  config {
    strict_validation true
    definition "order-policy" {
      version "1"
      source <<BCL
        bcl { version "1.0" }

        decision_table "order.policy" {
          default allow
          hit_policy first
          row "too-many" {
            priority 90
            when {
              all {
                input.items != nil
                len(input.items) > 2
              }
            }
            then { outcome { decision deny reason "at most two items" } }
          }
        }
BCL
    }
  }
}

intent "check" {
  response "result"
  node "policy" {
    uses "rules.evaluate"
    resource "policy"
    kind decision
    requires [input]
    provides [verdict]
    config {
      decision "order.policy"
      definition "order-policy"
    }
  }
  node "result" { uses "collect" requires [input, verdict] provides [result] }
}

route "check" { method POST path "/check" intent "check" }
`

// A rules.engine publishes its definitions when it opens, and rules.evaluate
// publishes its report under the fact the node declares.
func TestRulesEngineDefinitionsPublishAtOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.bcl")
	if err := os.WriteFile(path, []byte(rulesEngineApp), 0o600); err != nil {
		t.Fatal(err)
	}
	h := newAppHarness(t, path, nil)
	if status, body := h.call("POST", "/check", "", map[string]any{"items": []any{1, 2}}); status != 200 || dig(body, "verdict") == nil {
		t.Fatalf("allowed: %d %v", status, body)
	}
	if status, body := h.call("POST", "/check", "", map[string]any{"items": []any{1, 2, 3}}); status != 403 || !strings.Contains(dig(body, "error", "message").(string), "at most two items") {
		t.Fatalf("denied: %d %v", status, body)
	}
}

// An invalid definition fails startup and says why.
func TestRulesEngineInvalidDefinitionFailsStartup(t *testing.T) {
	broken := strings.Replace(rulesEngineApp, `        bcl { version "1.0" }
`, "", 1)
	broken = strings.Replace(broken, "                input.items != nil\n                len(input.items) > 2\n", "                input.items != nil len(input.items) > 2\n", 1)
	_, err := Compile(context.Background(), []byte(broken), ".", DefaultLoadOptions())
	if err == nil {
		t.Fatal("an invalid definition compiled")
	}
	for _, want := range []string{`definition "order-policy"`, "missing bcl version declaration", `unexpected token "len"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q: %v", want, err)
		}
	}
}
