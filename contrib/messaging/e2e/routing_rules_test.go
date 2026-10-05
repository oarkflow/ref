package e2e

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// rulesFixture creates three interchangeable Nepal providers on the SMPP
// channel, so a rule's effect is visible as which one comes first.
func rulesFixture(t *testing.T, s *stack, op *browser) {
	t.Helper()
	for _, id := range []string{"np_a", "np_b", "np_c"} {
		if st, out := op.do("PUT", "/ui/admin/providers/"+id, map[string]any{"channel": "np_telecom", "kind": "smpp", "quality": 50, "countries": []string{"NP"}}); st != 200 {
			t.Fatalf("provider %s = %d %v", id, st, out)
		}
	}
	op.do("PUT", "/ui/admin/users/newco", map[string]any{"name": "NewCo", "countries": []string{"NP", "IN"}})
	op.do("POST", "/ui/admin/users/newco/topup", map[string]any{"amount": 5, "reference": "t"})
	op.do("PUT", "/ui/admin/users/newco/password", map[string]any{"email": "ops@newco.example", "password": "a-long-password-1"})
}

func explainRoute(b *browser, to, text string, extra map[string]any) (first string, ids []string, rejected map[string]string) {
	body := map[string]any{"to": to, "text": text}
	for k, v := range extra {
		body[k] = v
	}
	_, out := b.do("POST", "/ui/route/explain", body)
	m := asMap(out)
	for _, r := range asList(m["route"]) {
		ids = append(ids, asMap(r)["id"].(string))
	}
	if len(ids) > 0 {
		first = ids[0]
	}
	rejected = map[string]string{}
	for _, r := range asList(m["rejected"]) {
		rr := asMap(r)
		rejected[rr["id"].(string)] = Stringify(rr["rule"])
	}
	return
}

func asList(v any) []any { l, _ := v.([]any); return l }

// explainFull is explainRoute's whole answer, for assertions on the facts of the
// chain (a provider's price, its score) rather than on the order of it.
func explainFull(t *testing.T, b *browser, to, text string, extra ...map[string]any) map[string]any {
	t.Helper()
	body := map[string]any{"to": to, "text": text}
	for _, e := range extra {
		for k, v := range e {
			body[k] = v
		}
	}
	st, out := b.do("POST", "/ui/route/explain", body)
	if st != 200 {
		t.Fatalf("explain %s = %d %v", to, st, out)
	}
	return asMap(out)
}

func Stringify(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func addRule(t *testing.T, op *browser, body map[string]any) string {
	t.Helper()
	st, out := op.do("POST", "/ui/admin/routing-rules", body)
	if st != 201 {
		t.Fatalf("add rule %v = %d %v", body["name"], st, out)
	}
	return asMap(out)["id"].(string)
}

func removeRule(t *testing.T, op *browser, id string) {
	t.Helper()
	if st, out := op.do("DELETE", "/ui/admin/routing-rules/"+id, nil); st != 200 {
		t.Fatalf("delete %s = %d %v", id, st, out)
	}
}

// The seven kinds of rule an operator asked for, each shown to change the
// provider chosen, for the account and the destination it names and not for others.
func TestRoutingRulesByScope(t *testing.T) {
	s := start(t)
	op := operator(t, s)
	rulesFixture(t, s, op)
	demo, newco := s.browser(), s.browser()
	demo.login("demo@example.com", "demo-pass-123")
	newco.login("ops@newco.example", "a-long-password-1")
	const nepal, nepal2 = "+9779841234567", "+9779801234567"

	if first, _, _ := explainRoute(demo, nepal, "hello", nil); first != "np_telecom" {
		t.Fatalf("without rules: %s", first)
	}
	check := func(what string, b *browser, to, text string, want string, extra map[string]any) {
		t.Helper()
		if first, ids, _ := explainRoute(b, to, text, extra); first != want {
			t.Fatalf("%s: first = %s (%v), want %s", what, first, ids, want)
		}
	}

	// 1. platform default provider: everyone, every destination.
	r1 := addRule(t, op, map[string]any{"name": "platform default", "mode": "use", "provider": "np_a", "priority": 0})
	check("platform default, demo", demo, nepal, "hello", "np_a", nil)
	check("platform default, newco", newco, nepal, "hello", "np_a", nil)

	// 2. country default provider.
	r2 := addRule(t, op, map[string]any{"name": "Nepal default", "mode": "use", "provider": "np_b", "countries": []string{"np"}, "priority": 10})
	check("country default", demo, nepal, "hello", "np_b", nil)
	check("a rule for another country does not apply", newco, "+919876543210", "hello", "global_fallback", nil)

	// 3. user default provider.
	r3 := addRule(t, op, map[string]any{"name": "demo default", "mode": "use", "provider": "np_c", "account": "demo", "priority": 40})
	check("user default, demo", demo, nepal, "hello", "np_c", nil)
	check("user default does not apply to another account", newco, nepal, "hello", "np_b", nil)

	// 4. user country provider: more specific than 3, so it wins for its country.
	r4 := addRule(t, op, map[string]any{"name": "demo in Nepal", "mode": "use", "provider": "np_telecom", "account": "demo", "countries": []string{"NP"}, "priority": 50})
	check("user + country", demo, nepal, "hello", "np_telecom", nil)

	// 5. user recipient provider: a number range for one account.
	r5 := addRule(t, op, map[string]any{"name": "demo to 9801", "mode": "use", "provider": "np_a", "account": "demo", "recipient_prefix": "9779801", "priority": 70})
	check("user + recipient", demo, nepal2, "hello", "np_a", nil)
	check("user + recipient: another number", demo, nepal, "hello", "np_telecom", nil)
	check("user + recipient: another account", newco, nepal2, "hello", "np_b", nil)
	for _, id := range []string{r1, r2, r3, r4, r5} {
		removeRule(t, op, id)
	}
	check("all rules removed", demo, nepal, "hello", "np_telecom", nil)

	// 6. content based provider, for everyone.
	r6 := addRule(t, op, map[string]any{"name": "OTP traffic", "mode": "use", "provider": "np_b", "content_words": []string{"otp", "verification code"}, "priority": 20})
	check("content, matching", demo, nepal, "Your OTP is 1234", "np_b", nil)
	check("content, case-insensitive phrase", newco, nepal, "Your Verification Code: 99", "np_b", nil)
	check("content, no match", demo, nepal, "hello there", "np_telecom", nil)

	// 7. user content based provider: wins over 6 for its account.
	r7 := addRule(t, op, map[string]any{"name": "demo invoices", "mode": "use", "provider": "np_c", "account": "demo", "content_words": []string{"invoice"}, "priority": 60})
	check("user + content", demo, nepal, "Invoice 42 is due", "np_c", nil)
	check("user + content: another account", newco, nepal, "Invoice 42 is due", "np_telecom", nil)
	check("content rule still applies to the account", demo, nepal, "otp 5", "np_b", nil)
	removeRule(t, op, r6)
	removeRule(t, op, r7)

	// More parameters: message size, type, sender, tenant, a content pattern.
	long := strings.Repeat("a long message ", 20) // more than one segment
	rq := addRule(t, op, map[string]any{"name": "long messages", "mode": "use", "provider": "np_b", "min_segments": 2, "priority": 15})
	check("quantity, long", demo, nepal, long, "np_b", nil)
	check("quantity, short", demo, nepal, "short", "np_telecom", nil)
	rt := addRule(t, op, map[string]any{"name": "promotional type", "mode": "use", "provider": "np_c", "types": []string{"promotional"}, "priority": 16})
	check("message type", demo, nepal, "x", "np_c", map[string]any{"type": "promotional"})
	check("message type, other", demo, nepal, "x", "np_telecom", nil)
	rs := addRule(t, op, map[string]any{"name": "sender", "mode": "use", "provider": "np_a", "sender_pattern": "^ACME", "priority": 17})
	check("sender", demo, nepal, "x", "np_a", map[string]any{"from": "ACMEBANK"})
	check("sender, other", demo, nepal, "x", "np_telecom", map[string]any{"from": "OTHER"})
	rp := addRule(t, op, map[string]any{"name": "pattern", "mode": "use", "provider": "np_b", "content_pattern": "\\d{6}", "priority": 18})
	check("content pattern", demo, nepal, "code 123456 ok", "np_b", nil)
	check("content pattern, no match", demo, nepal, "code 12345 ok", "np_telecom", nil)
	for _, id := range []string{rq, rt, rs, rp} {
		removeRule(t, op, id)
	}

	// Avoid and only.
	ra := addRule(t, op, map[string]any{"name": "never np_telecom for demo", "mode": "avoid", "provider": "np_telecom", "account": "demo", "priority": 30})
	if first, ids, rej := explainRoute(demo, nepal, "hello", nil); first == "np_telecom" || contains(ids, "np_telecom") || !strings.HasPrefix(rej["np_telecom"], "rr_") {
		t.Fatalf("avoid: first %s chain %v rejected %v", first, ids, rej)
	}
	if first, _, _ := explainRoute(newco, nepal, "hello", nil); first != "np_telecom" {
		t.Fatalf("avoid applies to another account: %s", first)
	}
	removeRule(t, op, ra)
	ro := addRule(t, op, map[string]any{"name": "only np_c for demo", "mode": "only", "provider": "np_c", "account": "demo", "priority": 30})
	if first, ids, _ := explainRoute(demo, nepal, "hello", nil); first != "np_c" || len(ids) != 1 {
		t.Fatalf("only: %s %v", first, ids)
	}
	if _, ids, _ := explainRoute(newco, nepal, "hello", nil); len(ids) < 2 {
		t.Fatalf("only applies to another account: %v", ids)
	}
	// A message really goes the way the explanation says.
	_, out := demo.do("POST", "/ui/messages", map[string]any{"to": nepal, "text": "routed by a rule"})
	if str(asMap(out), "provider") != "np_c" {
		t.Fatalf("send = %v", out)
	}
	// Disabling a rule switches it off; enabling brings it back.
	if st, _ := op.do("PUT", "/ui/admin/routing-rules/"+ro+"/enabled", map[string]any{"enabled": false}); st != 200 {
		t.Fatalf("disable = %d", st)
	}
	if first, _, _ := explainRoute(demo, nepal, "hello", nil); first != "np_telecom" {
		t.Fatalf("disabled rule still applies: %s", first)
	}
	op.do("PUT", "/ui/admin/routing-rules/"+ro+"/enabled", map[string]any{"enabled": true})
	if first, _, _ := explainRoute(demo, nepal, "hello", nil); first != "np_c" {
		t.Fatalf("enabled rule does not apply: %s", first)
	}
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

// Bad rules are refused before they are stored, rules survive a restart, and
// only the operator manages them.
func TestRoutingRulesAreCheckedAndKept(t *testing.T) {
	s := start(t)
	op := operator(t, s)
	rulesFixture(t, s, op)
	bad := func(what string, body map[string]any) {
		t.Helper()
		if st, out := op.do("POST", "/ui/admin/routing-rules", body); st < 400 || st >= 500 {
			t.Fatalf("%s: %d %v, want a 4xx", what, st, out)
		}
	}
	bad("unknown provider", map[string]any{"name": "x", "mode": "use", "provider": "nope"})
	bad("unknown account", map[string]any{"name": "x", "mode": "use", "provider": "np_a", "account": "nobody"})
	bad("invalid regular expression", map[string]any{"name": "x", "mode": "use", "provider": "np_a", "content_pattern": "([unclosed"})
	bad("invalid mode", map[string]any{"name": "x", "mode": "sometimes", "provider": "np_a"})
	bad("segments range", map[string]any{"name": "x", "mode": "use", "provider": "np_a", "min_segments": 5, "max_segments": 2})
	bad("prefix is digits", map[string]any{"name": "x", "mode": "use", "provider": "np_a", "recipient_prefix": "97x"})
	if _, list := op.do("GET", "/ui/admin/routing-rules", nil); len(asList(list)) != seededRules {
		t.Fatalf("a refused rule was stored: %d rules, want the %d examples", len(asList(list)), seededRules)
	}
	// A pattern with quotes and backslashes survives into the generated source.
	id := addRule(t, op, map[string]any{"name": `say "hi"`, "mode": "use", "provider": "np_b", "content_pattern": `a\.b"c`, "priority": 5})
	if _, src := op.do("GET", "/ui/admin/rules/custom_routing", nil); !strings.Contains(str(asMap(src), "source"), id) {
		t.Fatalf("the generated rules do not contain the rule")
	}
	// Update in place.
	if st, out := op.do("PUT", "/ui/admin/routing-rules/"+id, map[string]any{"name": "renamed", "mode": "avoid", "provider": "np_b", "priority": 5}); st != 200 {
		t.Fatalf("update = %d %v", st, out)
	}
	_, list := op.do("GET", "/ui/admin/routing-rules", nil)
	renamed := 0
	for _, r := range asList(list) {
		if asMap(r)["name"] == "renamed" && asMap(r)["mode"] == "avoid" {
			renamed++
		}
	}
	if renamed != 1 || len(asList(list)) != seededRules+1 {
		t.Fatalf("after the update: %d rules, %d renamed", len(asList(list)), renamed)
	}
	// Kept across a restart.
	keep := addRule(t, op, map[string]any{"name": "keep me", "mode": "use", "provider": "np_c", "account": "demo", "priority": 40})
	_ = keep
	removeRule(t, op, id)
	dir, smsc, vendor := s.dir, s.smsc, s.vendor
	s.stop()
	s2 := start(t, opts{dir: dir, smsc: smsc, vendor: vendor, keepUpstreams: true})
	demo := s2.browser()
	demo.login("demo@example.com", "demo-pass-123")
	if first, _, _ := explainRoute(demo, "+9779841234567", "hi", nil); first != "np_c" {
		t.Fatalf("after a restart the rule does not apply: %s", first)
	}
	// Access.
	if st, _ := demo.do("GET", "/ui/admin/routing-rules", nil); st != 403 {
		t.Fatalf("an account reading the rules = %d", st)
	}
	if st, _ := s2.browser().do("GET", "/ui/admin/routing-rules", nil); st != 401 {
		t.Fatalf("anonymous = %d", st)
	}
}

// The source editor checks a definition without saving it and says where the problem is.
func TestRuleSourceCheckMarksTheLine(t *testing.T) {
	s := start(t)
	op := operator(t, s)
	_, rule := op.do("GET", "/ui/admin/rules/validate", nil)
	src := str(asMap(rule), "source")
	st, ok := op.do("POST", "/ui/admin/rules/validate/check", map[string]any{"source": src})
	if st != 200 || asMap(ok)["valid"] != true || len(asList(asMap(ok)["diagnostics"])) != 0 {
		t.Fatalf("a valid definition: %d %v", st, ok)
	}
	broken := src + "\n  stray {{\n"
	st, bad := op.do("POST", "/ui/admin/rules/validate/check", map[string]any{"source": broken})
	diags := asList(asMap(bad)["diagnostics"])
	if st != 200 || asMap(bad)["valid"] != false || len(diags) == 0 {
		t.Fatalf("a broken definition: %d %v", st, bad)
	}
	d := asMap(diags[0])
	if line, _ := d["line"].(float64); line < 2 || d["message"] == "" {
		t.Fatalf("diagnostic = %v", d)
	}
	// Checking changes nothing.
	if _, after := op.do("GET", "/ui/admin/rules/validate", nil); str(asMap(after), "source") != src {
		t.Fatalf("a check changed the definition")
	}
	if st, _ := s.browser().do("POST", "/ui/admin/rules/validate/check", map[string]any{"source": src}); st != 401 {
		t.Fatalf("anonymous = %d", st)
	}
}

// seededRules is the number of example routing rules a fresh database starts with.
const seededRules = 8

// liveCustomRouting is the source the engine is actually running: what routing
// reads, not what rules/ holds and not what the table says.
func liveCustomRouting(op *browser) string {
	_, out := op.do("GET", "/ui/admin/rules/custom_routing", nil)
	return str(asMap(out), "source")
}

// waitForRoutingRules waits until the live definition agrees with the table.
//
// custom_routing is generated from routing_rules and republished on a tick
// (config/46_converge.bcl), because rules/custom_routing.bcl is only a
// placeholder. A test that depends on a seeded rule therefore waits for the tick
// rather than assuming boot published it.
func waitForRoutingRules(t *testing.T, op *browser) map[string]bool {
	t.Helper()
	_, list := op.do("GET", "/ui/admin/routing-rules", nil)
	enabled := map[string]bool{}
	for _, r := range asList(list) {
		m := asMap(r)
		enabled[m["id"].(string)] = m["enabled"] == 1.0
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		source := liveCustomRouting(op)
		agrees := true
		for id, on := range enabled {
			if strings.Contains(source, `"`+id+`"`) != on {
				agrees = false
				break
			}
		}
		if agrees {
			return enabled
		}
		if time.Now().After(deadline) {
			t.Fatalf("the live custom_routing never agreed with routing_rules; after 15s it is:\n%s", source)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// The example rules a fresh installation starts with: they are in the table, the
// enabled ones reach the engine by themselves (no change to a rule is needed
// first), and they work the way their notes say.
func TestExampleRoutingRules(t *testing.T) {
	s := start(t)
	op := operator(t, s)
	demo, newco := s.browser(), s.browser()
	demo.login("demo@example.com", "demo-pass-123")
	op.do("PUT", "/ui/admin/users/newco", map[string]any{"name": "NewCo", "countries": []string{"NP"}})
	op.do("POST", "/ui/admin/users/newco/topup", map[string]any{"amount": 5, "reference": "t"})
	op.do("PUT", "/ui/admin/users/newco/password", map[string]any{"email": "ops@newco.example", "password": "a-long-password-1"})
	newco.login("ops@newco.example", "a-long-password-1")

	enabled := waitForRoutingRules(t, op)
	if len(enabled) != seededRules || !enabled["rr_example_demo_live_number"] || !enabled["rr_example_demo_login_code_sandbox"] || enabled["rr_example_long_messages"] {
		t.Fatalf("the examples: %v", enabled)
	}
	// The convergence is the point: a fresh database's rules reached the engine
	// with nobody editing one, and the ones that are off stayed out of it. This is
	// what an operator sees when a rule switched off in the console is still
	// routing traffic.
	if !strings.Contains(liveCustomRouting(op), "rr_example_promo_never_live") {
		t.Fatalf("an enabled example never reached the engine:\n%s", liveCustomRouting(op))
	}

	// 1. Demo to 9856034617 in any written form goes to the live provider, if this installation has one.
	_, providers := op.do("GET", "/ui/admin/providers", nil)
	hasLive := false
	for _, p := range asList(providers) {
		if asMap(p)["id"] == "smspasal_real" {
			hasLive = true
		}
	}
	// The example names the owner's test number; this checks the rule with another provider's help below.
	//
	// No `dlr` in the request on purpose. SMSPasal cannot report delivery
	// receipts, and a request that does not ask for one must still be routable
	// through it: `dlr` defaults to true, and the rule naming the provider has to
	// beat the capability for it to be reachable at all.
	if hasLive {
		for _, to := range []string{"+977 9856034616", "009779856034616", "977 9856034616", "9779856034616", "9856034616", "09856034616"} {
			if first, _, _ := explainRoute(demo, to, "hi", nil); first != "smspasal_real" {
				t.Fatalf("demo to %q (default dlr): first = %s, want smspasal_real", to, first)
			}
			if first, _, _ := explainRoute(demo, to, "hi", map[string]any{"dlr": false}); first != "smspasal_real" {
				t.Fatalf("demo to %q (dlr false): first = %s, want smspasal_real", to, first)
			}
		}
		if first, _, _ := explainRoute(demo, "+9779856034617", "hi", nil); first != "np_telecom" {
			t.Fatalf("another number: %s", first)
		}
		if first, _, _ := explainRoute(newco, "+9779856034616", "hi", nil); first == "smspasal_real" {
			t.Fatalf("another account was routed to the live provider")
		}
		// 3. Promotional messages never use it.
		if _, ids, _ := explainRoute(demo, "+9779856034616", "sale", map[string]any{"type": "promotional"}); contains(ids, "smspasal_real") {
			t.Fatalf("a promotional message was routed to the live provider: %v", ids)
		}
	}

	// 2. The login code template uses the sandbox carrier, for demo only.
	tpl := map[string]any{"template": "otp_login", "vars": map[string]any{"brand": "Acme", "code": "1", "minutes": "5"}}
	_, out := demo.do("POST", "/ui/route/explain", merge(map[string]any{"to": "+9779841234567"}, tpl))
	route := asList(asMap(out)["route"])
	if first := asMap(route[0]); first["id"] != "np_telecom" || first["custom_rule"] != "rr_example_demo_login_code_sandbox" || asMap(out)["sandbox"] != true {
		t.Fatalf("login code template: %v", first)
	}
	_, out = newco.do("POST", "/ui/route/explain", merge(map[string]any{"to": "+9779841234567"}, tpl))
	if first := asMap(asList(asMap(out)["route"])[0]); first["custom_rule"] != nil {
		t.Fatalf("another account matched demo's rule: %v", first)
	}

	// A switched-off example does nothing; switching it on makes it work.
	if first, _, _ := explainRoute(demo, "+919876543210", "hi", nil); first != "global_fallback" {
		t.Fatalf("India before: %s", first)
	}
	op.do("PUT", "/ui/admin/routing-rules/rr_example_india_vendor/enabled", map[string]any{"enabled": true})
	if first, _, _ := explainRoute(demo, "+919876543210", "hi", nil); first != "in_vendor" {
		t.Fatalf("India after: %s (a rule admits a provider assigned to another account)", first)
	}
}

func merge(a, b map[string]any) map[string]any {
	for k, v := range b {
		a[k] = v
	}
	return a
}

// Numbers are matched however they are written, and a rule can name a template.
func TestRoutingRuleNumberFormatsAndTemplates(t *testing.T) {
	s := start(t)
	op := operator(t, s)
	rulesFixture(t, s, op)
	demo := s.browser()
	demo.login("demo@example.com", "demo-pass-123")
	id := addRule(t, op, map[string]any{"name": "formats", "mode": "use", "provider": "np_a", "account": "demo",
		"recipients": []string{"+977 9801111111", "009779802222222", "9803333333", "09804444444"}, "priority": 70})
	for _, to := range []string{"+9779801111111", "00977 9802222222", "9779803333333", "9804444444", "+977-9804444444"} {
		if first, _, _ := explainRoute(demo, to, "hi", nil); first != "np_a" {
			t.Fatalf("%q: first = %s, want np_a", to, first)
		}
	}
	if first, _, _ := explainRoute(demo, "+9779805555555", "hi", nil); first != "np_telecom" {
		t.Fatalf("a number not in the rule: %s", first)
	}
	// An international number written with + is not given the default country.
	removeRule(t, op, id)
	id = addRule(t, op, map[string]any{"name": "india", "mode": "use", "provider": "global_fallback", "recipients": []string{"+91 98765 43210"}, "priority": 70})
	if first, _, _ := explainRoute(demo, "+919876543210", "hi", nil); first != "global_fallback" {
		t.Fatalf("international number: %s", first)
	}
	if _, list := op.do("GET", "/ui/admin/routing-rules", nil); !strings.Contains(strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(toJSON(list), `"`, ""), "\\", ""), " ", ""), "recipients:919876543210") && !strings.Contains(toJSON(list), "919876543210") {
		t.Fatalf("the number was not stored in international form")
	}
	removeRule(t, op, id)
	// Templates.
	id = addRule(t, op, map[string]any{"name": "receipts", "mode": "use", "provider": "np_b", "templates": []string{"payment_received"}, "priority": 20})
	_, out := demo.do("POST", "/ui/route/explain", map[string]any{"to": "+9779841234567", "template": "payment_received", "vars": map[string]any{"currency": "NPR", "amount": "10", "date": "today", "receipt": "r1"}})
	if first := asMap(asList(asMap(out)["route"])[0]); first["id"] != "np_b" {
		t.Fatalf("template rule: %v", first["id"])
	}
	if first, _, _ := explainRoute(demo, "+9779841234567", "plain text", nil); first == "np_b" {
		t.Fatalf("a template rule applied to a plain text message")
	}
	removeRule(t, op, id)
}

func toJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// An operator asks where an account's message would go.
func TestOperatorExplainsARouteForAnAccount(t *testing.T) {
	s := start(t)
	op := operator(t, s)
	waitForRoutingRules(t, op)
	st, out := op.do("POST", "/ui/admin/route/explain", map[string]any{"account": "demo", "to": "9779841234567", "template": "otp_login", "vars": map[string]any{"brand": "A", "code": "1", "minutes": "5"}})
	if st != 200 {
		t.Fatalf("explain = %d %v", st, out)
	}
	if first := asMap(asList(asMap(out)["route"])[0]); first["id"] != "np_telecom" || first["custom_rule"] != "rr_example_demo_login_code_sandbox" {
		t.Fatalf("route = %v", first)
	}
	if st, _ := op.do("POST", "/ui/admin/route/explain", map[string]any{"account": "nobody", "to": "9779841234567", "text": "x"}); st < 400 {
		t.Fatalf("an unknown account = %d", st)
	}
	if st, _ := s.browser().do("POST", "/ui/admin/route/explain", map[string]any{"account": "demo", "to": "9779841234567", "text": "x"}); st != 401 {
		t.Fatalf("anonymous = %d", st)
	}
	demo := s.browser()
	demo.login("demo@example.com", "demo-pass-123")
	if st, _ := demo.do("POST", "/ui/admin/route/explain", map[string]any{"account": "newco", "to": "9779841234567", "text": "x"}); st != 403 {
		t.Fatalf("an account asking about another account = %d", st)
	}
}
