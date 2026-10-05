package e2e

import (
	"strings"
	"testing"
)

func operator(t *testing.T, s *stack) *browser {
	b := s.browser()
	if st := b.login("operator@example.com", "operator-pass-123"); st != 200 {
		t.Fatalf("operator login = %d", st)
	}
	return b
}

// An operator changes a validation rule while the application runs. The next
// message obeys it, a broken edit is refused and changes nothing, and going back
// to the file restores the original behaviour.
func TestOperatorEditsRulesAtRuntime(t *testing.T) {
	s := start(t)
	op, acct := operator(t, s), s.browser()
	acct.login("demo@example.com", "demo-pass-123")
	send := func(text string) (int, map[string]any) {
		st, out := acct.do("POST", "/ui/messages", map[string]any{"to": "+9779841234567", "text": text})
		return st, asMap(out)
	}
	if st, out := send("a forbidden word"); st != 202 {
		t.Fatalf("before the rule = %d %v", st, out)
	}
	st, rule := op.do("GET", "/ui/admin/rules/validate", nil)
	src, _ := asMap(rule)["source"].(string)
	if st != 200 || !strings.Contains(src, `row "empty-text"`) {
		t.Fatalf("source = %d %.200v", st, rule)
	}
	edited := strings.Replace(src, `row "empty-text"`, `row "forbidden-word" {
      priority 995
      when { message.text matches "(?i)forbidden" }
      then { outcome { decision deny reason "that word is not allowed" attributes { code "FORBIDDEN_WORD" status 422 } } }
    }
    row "empty-text"`, 1)
	if st, out := op.do("PUT", "/ui/admin/rules/validate", map[string]any{"source": edited}); st != 200 {
		t.Fatalf("save = %d %v", st, out)
	}
	if st, out := send("a forbidden word"); st != 422 || str(out, "error", "code") != "FORBIDDEN_WORD" {
		t.Fatalf("after the rule = %d %v", st, out)
	}
	if st, out := send("a fine message"); st != 202 {
		t.Fatalf("other text = %d %v", st, out)
	}
	// A broken edit is refused, and the active rule is unchanged.
	if st, out := op.do("PUT", "/ui/admin/rules/validate", map[string]any{"source": "this is not a rule"}); st != 422 {
		t.Fatalf("broken source = %d %v", st, out)
	}
	if st, _ := send("a forbidden word"); st != 422 {
		t.Fatalf("the earlier edit was lost by a refused one: %d", st)
	}
	// Try a decision against sample facts.
	st, res := op.do("POST", "/ui/admin/rules/validate/try", map[string]any{"decision": "validate", "facts": map[string]any{"message": map[string]any{"text": "forbidden", "to": "9779841234567", "country": "NP", "text_len": 9, "segments": 1, "type": "transactional"}, "user": map[string]any{"status": "active"}}})
	if st != 200 || str(asMap(res), "attributes", "code") != "FORBIDDEN_WORD" {
		t.Fatalf("try = %d %v", st, res)
	}
	// The edit is listed as an override and survives a restart.
	_, cat := op.do("GET", "/ui/admin/rules", nil)
	found := false
	for _, r := range cat.([]any) {
		if m := asMap(r); m["name"] == "validate" && m["overridden"] == true {
			found = true
		}
	}
	if !found {
		t.Fatalf("catalog does not show the override: %v", cat)
	}
	dir, smsc, vendor := s.dir, s.smsc, s.vendor
	s.stop()
	s2 := start(t, opts{dir: dir, smsc: smsc, vendor: vendor, keepUpstreams: true})
	acct2 := s2.browser()
	acct2.login("demo@example.com", "demo-pass-123")
	if st, out := acct2.do("POST", "/ui/messages", map[string]any{"to": "+9779841234567", "text": "forbidden again"}); st != 422 {
		t.Fatalf("after a restart = %d %v", st, out)
	}
	// Back to the file.
	op2 := operator(t, s2)
	if st, out := op2.do("POST", "/ui/admin/rules/validate/reset", nil); st != 200 {
		t.Fatalf("reset = %d %v", st, out)
	}
	if st, out := acct2.do("POST", "/ui/messages", map[string]any{"to": "+9779841234567", "text": "forbidden again"}); st != 202 {
		t.Fatalf("after reset = %d %v", st, out)
	}
	if st, _ := acct2.do("GET", "/ui/admin/rules", nil); st != 403 {
		t.Fatalf("an account reading the rules = %d, want 403", st)
	}
}

// Providers are created and tuned at runtime; routing follows.
func TestOperatorManagesProviders(t *testing.T) {
	s := start(t)
	op, acct := operator(t, s), s.browser()
	acct.login("demo@example.com", "demo-pass-123")
	// The chain is asked for as an operator: an account's dry run names no carrier.
	route := func() []string {
		_, out := op.do("POST", "/ui/admin/route/explain", map[string]any{"account": "demo", "to": "+9779841234567", "text": "hi"})
		var ids []string
		for _, r := range asMap(out)["route"].([]any) {
			ids = append(ids, asMap(r)["id"].(string))
		}
		return ids
	}
	if got := route(); got[0] != "np_telecom" {
		t.Fatalf("route = %v", got)
	}
	if st, out := op.do("PUT", "/ui/admin/providers/nope", map[string]any{"channel": "no_such_channel"}); st != 422 {
		t.Fatalf("unknown channel = %d %v", st, out)
	}
	// A cheaper, better provider on the same channel, for Nepal only.
	if st, out := op.do("PUT", "/ui/admin/providers/np_gold", map[string]any{"channel": "np_telecom", "kind": "smpp", "quality": 99, "delivery_rate": 0.999, "countries": []string{"np"}, "description": "gold"}); st != 200 {
		t.Fatalf("create = %d %v", st, out)
	}
	if st, out := op.do("PUT", "/ui/admin/providers/np_gold/costs/*", map[string]any{"per_segment": 0.001}); st != 200 {
		t.Fatalf("price = %d %v", st, out)
	}
	if got := route(); got[0] != "np_gold" {
		t.Fatalf("route after adding a better provider = %v", got)
	}
	// It carries a real message: its own queue entry is the channel's.
	st, out := acct.do("POST", "/ui/messages", map[string]any{"to": "+9779841234567", "text": "through the new provider"})
	id, _ := asMap(out)["id"].(string)
	if st != 202 || s.carrier(str(asMap(out), "id")) != "np_gold" {
		t.Fatalf("send = %d %v", st, out)
	}
	eventually(t, "delivered over the shared channel", func() bool {
		_, m := acct.do("GET", "/ui/messages/"+id, nil)
		return str(asMap(m), "status") == "delivered"
	})
	// Assigned to an account only, it disappears for everyone else.
	op.do("PUT", "/ui/admin/providers/np_gold", map[string]any{"channel": "np_telecom", "quality": 99, "countries": []string{"NP"}, "users": []string{"acme_bob"}, "assigned_only": true})
	if got := route(); got[0] != "np_telecom" {
		t.Fatalf("an assigned-only provider is still offered to another account: %v", got)
	}
	// Paused, it is not used; the tier list shows why.
	op.do("PUT", "/ui/admin/providers/np_gold", map[string]any{"channel": "np_telecom", "quality": 99, "countries": []string{"NP"}})
	op.do("PUT", "/ui/admin/providers/np_gold/state", map[string]any{"state": "paused"})
	if got := route(); got[0] != "np_telecom" {
		t.Fatalf("a paused provider is used: %v", got)
	}
	st, list := op.do("GET", "/ui/admin/providers", nil)
	if st != 200 || len(list.([]any)) < 6 {
		t.Fatalf("list = %d %v", st, list)
	}
	if st, _ := op.do("GET", "/ui/admin/channels", nil); st != 200 {
		t.Fatalf("channels = %d", st)
	}
	if st, _ := acct.do("PUT", "/ui/admin/providers/x", map[string]any{"channel": "np_telecom"}); st != 403 {
		t.Fatalf("an account managing providers = %d", st)
	}
}

// Accounts: limits, money and passwords are managed by the operator.
func TestOperatorManagesAccounts(t *testing.T) {
	s := start(t)
	op := operator(t, s)
	if st, out := op.do("PUT", "/ui/admin/users/newco", map[string]any{"name": "NewCo", "daily_limit": 1, "countries": []string{"NP"}}); st != 200 {
		t.Fatalf("create = %d %v", st, out)
	}
	op.do("POST", "/ui/admin/users/newco/topup", map[string]any{"amount": 5, "reference": "t1"})
	if st, out := op.do("PUT", "/ui/admin/users/newco/password", map[string]any{"email": "ops@newco.example", "password": "a-long-password-1"}); st != 200 {
		t.Fatalf("password = %d %v", st, out)
	}
	nc := s.browser()
	if st := nc.login("ops@newco.example", "a-long-password-1"); st != 200 {
		t.Fatalf("login = %d", st)
	}
	send := func(to string) int {
		st, _ := nc.do("POST", "/ui/messages", map[string]any{"to": to, "text": "hi"})
		return st
	}
	if a, b := send("+9779841234567"), send("+9779841234568"); a != 202 || b == 202 {
		t.Fatalf("daily limit 1: %d then %d", a, b)
	}
	if st := send("+919876543210"); st == 202 {
		t.Fatalf("a number outside the account's countries was accepted")
	}
	op.do("PUT", "/ui/admin/users/newco", map[string]any{"name": "NewCo", "daily_limit": 100, "countries": []string{"NP", "IN"}})
	if st := send("+9779841234569"); st != 202 {
		t.Fatalf("after raising the limit = %d", st)
	}
	if st, list := op.do("GET", "/ui/admin/users", nil); st != 200 || len(asMap(list)["rows"].([]any)) < 5 {
		t.Fatalf("users = %d %v", st, list)
	}
}
