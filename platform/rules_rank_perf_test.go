package platform

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// perfApp is a router with `providers` candidates and `rules` operator rules,
// shaped like the SMS gateway's: every rule names one provider and tests a
// number, an account and the text.
func perfApp(providers, rules int, split bool) string {
	var custom strings.Builder
	rows := make([]strings.Builder, providers)
	for i := 0; i < rules; i++ {
		out := &custom
		if split {
			out = &rows[i%providers]
		}
		fmt.Fprintf(out, `
          row "r%d" {
            priority %d
            when {
              all {
                provider.id == "p%d"
                user.id == "u%d"
                message.to matches "^(9779%08d)$"
                message.text matches "(?i)(word%d|other%d)"
              }
            }
            then { outcome { decision allow attributes { priority %d } } }
          }`, i, 1000+i, i%providers, i%37, i, i, i, 200000+i*1000)
	}
	tables := `        decision_schema "custom" { effects [allow, deny] default allow strategy first_match }
        decision_table "custom" {
          default allow
          hit_policy first
` + custom.String() + `
        }`
	extra := "custom/custom"
	if split {
		var b strings.Builder
		for i := range rows {
			fmt.Fprintf(&b, `        decision_schema "for_p%d" { effects [allow, deny] default allow strategy first_match }
        decision_table "for_p%d" {
          default allow
          hit_policy first
%s
        }
`, i, i, rows[i].String())
		}
		tables = b.String()
		extra = "custom/for_{id}"
	}
	return `
name "perf"
resource "policy" {
  kind "rules.engine"
  config {
    definition "route" {
      version "1"
      source <<BCL
        bcl { version "1.0" }
        decision_schema "ok" { effects [allow, deny] default deny strategy first_match }
        decision_table "ok" {
          default deny
          hit_policy first
          row "paused" { priority 10 when { provider.state != "active" } then { outcome { decision deny reason "paused" } } }
          row "country" { priority 5 when { contains(provider.countries, "|" + message.country + "|") } then { outcome { decision allow attributes { priority 50000 } } } }
          row "any" { priority 1 when { provider.countries == "" } then { outcome { decision allow attributes { priority 10000 } } } }
        }
        decision_schema "best" { effects [allow] default allow strategy first_match }
        ranking "best" {
          selection best
          priority_path "provider.priority"
          score "quality" { metric "provider.quality" weight 100 normalize [0, 100] }
          score "cost" { metric "provider.cost" weight -50 normalize [0, 0.2] }
        }
BCL
    }
    definition "custom" {
      version "1"
      source <<BCL
        bcl { version "1.0" }
` + tables + `
BCL
    }
  }
}
intent "pick" {
  response "route"
  node "facts" {
    uses "expression" kind pure requires [input] provides [message]
    config { expression "input.message" }
  }
  node "user" {
    uses "expression" kind pure requires [input] provides [user]
    config { expression "input.user" }
  }
  node "candidates" {
    uses "expression" kind pure requires [input] provides [candidates]
    config { expression "input.candidates" }
  }
  node "route" {
    uses "rules.rank" resource "policy" kind read requires [message, user, candidates] provides [route]
    config { definition "route" candidates_fact "candidates" eligibility "ok" eligibility_extra ["` + extra + `"] ranking "best" }
  }
}
route "pick" { method POST path "/pick" intent "pick" }
`
}

func perfRequest(providers int) map[string]any {
	cands := make([]any, providers)
	for i := range cands {
		cands[i] = map[string]any{"id": fmt.Sprintf("p%d", i), "state": "active", "quality": 60 + i%40, "cost": 0.01 * float64(1+i%9), "countries": []string{"", "|NP|"}[i%2]}
	}
	return map[string]any{
		"message":    map[string]any{"to": "977900000007", "country": "NP", "text": "your order word7 has shipped"},
		"user":       map[string]any{"id": "u7"},
		"candidates": cands,
	}
}

// The router answers in a few milliseconds with a realistic catalog and a large
// rule set, and its cost grows in step with the rules (providers x rules).
func TestRoutingPerformance(t *testing.T) {
	if testing.Short() {
		t.Skip("timing")
	}
	const n = 100
	for _, size := range []struct {
		providers, rules int
		split            bool
		limit            time.Duration
	}{{6, 20, false, 10 * time.Millisecond}, {20, 500, false, 40 * time.Millisecond}, {20, 5000, false, 250 * time.Millisecond}, {20, 500, true, 10 * time.Millisecond}, {20, 5000, true, 40 * time.Millisecond}} {
		t.Run(fmt.Sprintf("%dx%d split=%v", size.providers, size.rules, size.split), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "app.bcl")
			if err := os.WriteFile(path, []byte(perfApp(size.providers, size.rules, size.split)), 0o600); err != nil {
				t.Fatal(err)
			}
			h := newAppHarness(t, path, nil)
			req := perfRequest(size.providers)
			status, body := h.call("POST", "/pick", "", req)
			if status != 200 || len(chainOf(body)) == 0 {
				t.Fatalf("pick: %d %v", status, body)
			}
			if want := fmt.Sprintf("p%d", 7%size.providers); chainOf(body)[0] != want {
				t.Fatalf("rule 7 should put %s first, chain %v", want, chainOf(body))
			}
			start := time.Now()
			for i := 0; i < n; i++ {
				if status, _ := h.call("POST", "/pick", "", req); status != 200 {
					t.Fatalf("status %d", status)
				}
			}
			per := time.Since(start) / n
			t.Logf("%d providers x %d rules split=%v: %v per route", size.providers, size.rules, size.split, per)
			if per > size.limit {
				t.Fatalf("routing took %v per message, want under %v", per, size.limit)
			}
		})
	}
}
