package platform

import (
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
)

// The routing action's contract with its callers: why a candidate was left out is
// machine-readable, a reservation an operator's rule made is honoured when it
// cannot be, and a generated rule set that is not published does not stop
// messages.

// reserveApp ranks candidates against a main eligibility decision and an
// operator-generated one, the way sms.prepare does: the generated definition
// ("ops") runs first and its allow attributes override the main decision's.
const reserveApp = `
name "reserve"

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
          row "paused" { priority 40 when { provider.state != "active" } then { outcome { decision deny reason "the provider is not active" attributes { code "PROVIDER_STATE" transient true } } } }
          row "grant" { priority 30 when { provider.granted == true } then { outcome { decision allow attributes { tier "rule" priority 200000 } } } }
          row "soft" { priority 20 when { provider.supports_dlr == 0 } then { outcome { decision deny reason "the provider cannot report delivery receipts" attributes { code "NO_DLR" } } } }
          row "tier" { priority 1 when { provider.id != "" } then { outcome { decision allow attributes { tier "country" priority 50000 } } } }
        }
        decision_schema "best" { effects [allow] default allow strategy first_match }
        ranking "best" {
          selection best
          priority_path "provider.priority"
          score "quality" { metric "provider.quality" weight 100 normalize [0, 100] }
        }
BCL
    }
    definition "ops" {
      version "1"
      source <<BCL
        bcl { version "1.0" }
        decision_schema "for_reserved" { effects [allow, deny] default allow strategy first_match }
        decision_table "for_reserved" {
          default allow
          hit_policy first
          row "reserved" { priority 10 when { provider.id == "reserved" } then { outcome { decision allow attributes { tier "rule" priority 200000 reserves true rule_id "rr_reserved" } } } }
        }
        decision_schema "for_ruled" { effects [allow, deny] default allow strategy first_match }
        decision_table "for_ruled" {
          default allow
          hit_policy first
          row "ruled" { priority 10 when { provider.id == "ruled" } then { outcome { decision allow attributes { tier "rule" priority 200000 rule_id "rr_ruled" } } } }
        }
BCL
    }
  }
}

intent "pick" {
  response "route"
  node "candidates" {
    uses "expression" kind pure requires [input] provides [candidates]
    config { expression "input.candidates" }
  }
  node "route" {
    uses "rules.rank" resource "policy" kind read requires [candidates] provides [route]
    config {
      definition "route"
      candidates_fact "candidates"
      eligibility "ok"
      eligibility_extra ["ops/for_{id}"]
      ranking "best"
    }
  }
}
route "pick" { method POST path "/pick" intent "pick" }
`

func newReserveHarness(t *testing.T) *appHarness {
	t.Helper()
	path := filepath.Join(t.TempDir(), "app.bcl")
	if err := os.WriteFile(path, []byte(reserveApp), 0o600); err != nil {
		t.Fatal(err)
	}
	return newAppHarness(t, path, nil)
}

func TestRulesRankRejectionCarriesItsReasonCode(t *testing.T) {
	h := newReserveHarness(t)
	codes := func(body any) map[string]string {
		out := map[string]string{}
		rejected, _ := dig(body, "rejected").([]any)
		for _, r := range rejected {
			e := r.(map[string]any)
			code, _ := e["reason_code"].(string)
			out[e["id"].(string)] = code
		}
		return out
	}
	_, body := h.call("POST", "/pick", "", map[string]any{"candidates": []any{
		map[string]any{"id": "paused_one", "state": "paused", "quality": 99},
		map[string]any{"id": "no_receipts", "state": "active", "supports_dlr": 0, "quality": 50},
		map[string]any{"id": "fine", "state": "active", "supports_dlr": 1, "quality": 50},
	}})
	// The code a rule gives lives in its attributes. A caller reading only
	// reason_code got an empty string for every row before, which is what made
	// "which provider was refused, and why" unanswerable from the API.
	got := codes(body)
	if got["paused_one"] != "PROVIDER_STATE" || got["no_receipts"] != "NO_DLR" {
		t.Fatalf("reason codes %v", got)
	}
	if len(got) != 2 {
		t.Fatalf("rejected %v", got)
	}
	_, body = h.call("POST", "/pick", "", map[string]any{"candidates": []any{
		map[string]any{"id": "paused_one", "state": "paused"},
	}})
	if got := codes(body); got["paused_one"] != "PROVIDER_STATE" {
		t.Fatalf("reason codes %v", got)
	}
}

// A rule that reserves a message for one provider is kept, not merely
// preferred: when that provider cannot take it, nothing may. The reservation is a
// filter on identity, not a score, so it holds whatever the priority was.
func TestRulesRankHonoursAReservation(t *testing.T) {
	h := newReserveHarness(t)
	// The reserved provider is usable: it alone carries the message.
	_, body := h.call("POST", "/pick", "", map[string]any{"candidates": []any{
		map[string]any{"id": "reserved", "state": "active", "quality": 1},
		map[string]any{"id": "other", "state": "active", "quality": 99},
	}})
	if got := chainOf(body); !slices.Equal(got, []string{"reserved"}) {
		t.Fatalf("chain %v, want the reserved provider alone", got)
	}
	// It is paused: nothing may carry the message, and the reason says which
	// provider the message was waiting for.
	_, body = h.call("POST", "/pick", "", map[string]any{"candidates": []any{
		map[string]any{"id": "reserved", "state": "paused", "quality": 99},
		map[string]any{"id": "other", "state": "active", "quality": 99},
	}})
	if got := chainOf(body); len(got) != 0 {
		t.Fatalf("chain %v, want empty: an unmet reservation must not widen", got)
	}
	if dig(body, "empty") != true {
		t.Fatalf("empty flag %v", dig(body, "empty"))
	}
	if dig(body, "reserved_for") != "reserved" {
		t.Fatalf("reserved_for = %v, want reserved", dig(body, "reserved_for"))
	}
	rejected, _ := dig(body, "rejected").([]any)
	sawOther, sawSelf := false, false
	for _, r := range rejected {
		e := r.(map[string]any)
		switch {
		case e["id"] == "other" && e["reason_code"] == "RESERVED_ELSEWHERE":
			sawOther = true
		case e["id"] == "reserved" && e["reason_code"] == "RESERVED_PROVIDER_UNAVAILABLE":
			sawSelf = true
		}
	}
	if !sawOther || !sawSelf {
		t.Fatalf("the refused candidates should say why: other=%v reserved=%v\n%v", sawOther, sawSelf, rejected)
	}
}

// A generated rule set that is not published yet — a fresh database, a render
// that has not run — must not fail every message. The candidates simply go
// unruled, which is what they were before the rules existed.
func TestRulesRankSkipsAnUnpublishedExtraDefinition(t *testing.T) {
	const app = `
name "no_ops"
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
          row "tier" { priority 1 when { provider.id != "" } then { outcome { decision allow attributes { tier "country" priority 50000 } } } }
        }
        decision_schema "best" { effects [allow] default allow strategy first_match }
        ranking "best" {
          selection best
          priority_path "provider.priority"
          score "quality" { metric "provider.quality" weight 100 normalize [0, 100] }
        }
BCL
    }
  }
}
intent "pick" {
  response "route"
  node "candidates" { uses "expression" kind pure requires [input] provides [candidates] config { expression "input.candidates" } }
  node "route" {
    uses "rules.rank" resource "policy" kind read requires [candidates] provides [route]
    config { definition "route" candidates_fact "candidates" eligibility "ok" eligibility_extra ["ops/for_{id}"] ranking "best" }
  }
}
route "pick" { method POST path "/pick" intent "pick" }
`
	path := filepath.Join(t.TempDir(), "app.bcl")
	if err := os.WriteFile(path, []byte(app), 0o600); err != nil {
		t.Fatal(err)
	}
	h := newAppHarness(t, path, nil)
	status, body := h.call("POST", "/pick", "", map[string]any{"candidates": []any{
		map[string]any{"id": "a", "state": "active", "quality": 90},
		map[string]any{"id": "b", "state": "active", "quality": 10},
	}})
	if status != 200 {
		t.Fatalf("a missing generated definition failed the request: %d %v", status, body)
	}
	if got := chainOf(body); !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("chain %v", got)
	}
}

// A rule's attributes win over the main decision's, which is how an operator's
// rule both admits a provider the tiers would refuse and puts it on top.
func TestRulesRankExtraAttributesOverrideEligibility(t *testing.T) {
	h := newReserveHarness(t)
	// "ruled" has no receipts, which the main decision refuses. A rule naming it
	// both admits it and puts it on top, and it wins on priority rather than on
	// its quality.
	_, body := h.call("POST", "/pick", "", map[string]any{"candidates": []any{
		map[string]any{"id": "ruled", "state": "active", "supports_dlr": 0, "quality": 10},
		map[string]any{"id": "fine", "state": "active", "supports_dlr": 1, "quality": 99},
	}})
	if got := chainOf(body); !slices.Equal(got, []string{"ruled", "fine"}) {
		t.Fatalf("chain %v, want the ruled provider first", got)
	}
	first := dig(body, "chain").([]any)[0].(map[string]any)
	if first["rule_id"] != "rr_ruled" {
		t.Fatalf("the winner does not name the rule that chose it: %v", first)
	}
}

// Every runtime publish gets its own version name. The name used to be a
// millisecond timestamp alone, so two publishes of the same definition in the
// same millisecond — an operator saving a rule while the application's tick
// republishes the generated set — shared a name, and the second one activated
// the first one's version. That surfaced as a random 500 on an unrelated request.
func TestRuntimePublishVersionsAreUnique(t *testing.T) {
	w := &rulesEngineWrapper{}
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		v := w.nextVersion("rt")
		if seen[v] {
			t.Fatalf("version %q was handed out twice", v)
		}
		seen[v] = true
	}
	// Concurrent callers, which is how a tick and a request collide.
	var wg sync.WaitGroup
	var mu sync.Mutex
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				v := w.nextVersion("rt")
				mu.Lock()
				dup := seen[v]
				seen[v] = true
				mu.Unlock()
				if dup {
					t.Errorf("version %q was handed out twice", v)
					return
				}
			}
		}()
	}
	wg.Wait()
}

// Priority orders; it does not filter. A candidate with the worst possible score
// stays in the chain as long as the filters kept it, and no ranking value can
// remove one — which is what an `exclusive` priority floor used to do, making the
// outcome depend on how the tiers happened to be numbered rather than on whether
// a provider may carry the message.
func TestPriorityOnlyOrdersAndNeverFilters(t *testing.T) {
	const app = `
name "order_only"
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
          row "tier" { priority 1 when { provider.id != "" } then { outcome { decision allow attributes { tier "country" priority 1 } } } }
        }
        decision_schema "best" { effects [allow] default allow strategy first_match }
        ranking "best" {
          selection best
          priority_path "provider.priority"
          score "quality" { metric "provider.quality" weight 100 normalize [0, 100] }
        }
BCL
    }
  }
}
intent "pick" {
  response "route"
  node "candidates" { uses "expression" kind pure requires [input] provides [candidates] config { expression "input.candidates" } }
  node "route" {
    uses "rules.rank" resource "policy" kind read requires [candidates] provides [route]
    config { definition "route" candidates_fact "candidates" eligibility "ok" ranking "best" }
  }
}
route "pick" { method POST path "/pick" intent "pick" }
`
	path := filepath.Join(t.TempDir(), "app.bcl")
	if err := os.WriteFile(path, []byte(app), 0o600); err != nil {
		t.Fatal(err)
	}
	h := newAppHarness(t, path, nil)

	// Ten times the priority spread, so the ordering is unambiguous, and every
	// candidate survives the filter. The worst-scoring one is still there.
	_, body := h.call("POST", "/pick", "", map[string]any{"candidates": []any{
		map[string]any{"id": "a", "quality": 100, "priority": 1000},
		map[string]any{"id": "b", "quality": 90, "priority": 900},
		map[string]any{"id": "worst", "quality": 1, "priority": 1},
		map[string]any{"id": "c", "quality": 50, "priority": 500},
	}})
	got := chainOf(body)
	if !slices.Equal(got, []string{"a", "b", "c", "worst"}) {
		t.Fatalf("chain %v: scored worst first, nothing dropped", got)
	}
	if dig(body, "empty") != false {
		t.Fatalf("empty %v", dig(body, "empty"))
	}
}
