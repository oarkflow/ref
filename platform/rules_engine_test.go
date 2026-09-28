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

// A "dir" auto-discovers one definition per ".bcl" file, named by its path
// relative to the directory, nested however deep the author wants — and
// skips an underscore-prefixed file, which exists to be "import"ed by a
// real definition rather than published on its own.
func TestRulesEngineDirScansNestedDirectories(t *testing.T) {
	root := t.TempDir()
	rulesDir := filepath.Join(root, "rules")
	if err := os.MkdirAll(filepath.Join(rulesDir, "orders"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A fragment the real definition imports — must NOT become its own
	// definition (it has no bcl-version declaration and would fail to
	// compile as one).
	if err := os.WriteFile(filepath.Join(rulesDir, "orders", "_shared.bcl"), []byte(`
		decision_schema "reused" { effects [allow, deny] default allow strategy first_match }
	`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rulesDir, "orders", "cap.bcl"), []byte(`
		bcl { version "1.0" }
		import "./_shared.bcl"
		decision_table "reused" {
			default allow
			hit_policy first
			row "over" {
				when { input.amount > 5000 }
				then { outcome { decision deny reason "over cap" } }
			}
		}
	`), 0o600); err != nil {
		t.Fatal(err)
	}

	app := `
name "rules-dir"

resource "policy" {
  kind "rules.engine"
  config {
    dir "` + filepath.ToSlash(rulesDir) + `"
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
      decision "reused"
      definition "orders.cap"
    }
  }
  node "result" { uses "collect" requires [input, verdict] provides [result] }
}

route "check" { method POST path "/check" intent "check" }
`
	path := filepath.Join(t.TempDir(), "app.bcl")
	if err := os.WriteFile(path, []byte(app), 0o600); err != nil {
		t.Fatal(err)
	}
	h := newAppHarness(t, path, nil)
	if status, _ := h.call("POST", "/check", "", map[string]any{"amount": 100}); status != 200 {
		t.Fatalf("under cap: %d", status)
	}
	if status, body := h.call("POST", "/check", "", map[string]any{"amount": 9000}); status != 403 || !strings.Contains(dig(body, "error", "message").(string), "over cap") {
		t.Fatalf("over cap: %d %v", status, body)
	}
}

// A file starting with "_" would fail to compile as a standalone definition
// (it deliberately has no bcl-version declaration, being a fragment) —
// proving it was actually skipped rather than merely not referenced.
func TestRulesEngineDirSkipsUnderscorePrefixedFragments(t *testing.T) {
	rulesDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(rulesDir, "_fragment.bcl"), []byte(`not valid bcl {{{`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rulesDir, "solo.bcl"), []byte(`
		bcl { version "1.0" }
		decision_table "solo" {
			default allow
			hit_policy first
		}
	`), 0o600); err != nil {
		t.Fatal(err)
	}
	app := `
name "rules-dir-skip"

resource "policy" {
  kind "rules.engine"
  config {
    dir "` + filepath.ToSlash(rulesDir) + `"
  }
}
`
	path := filepath.Join(t.TempDir(), "app.bcl")
	if err := os.WriteFile(path, []byte(app), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := Compile(context.Background(), []byte(app), filepath.Dir(path), DefaultLoadOptions())
	if err != nil {
		t.Fatalf("compile: %v (the malformed _fragment.bcl should have been skipped, not published)", err)
	}
	_ = p.Close()
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
