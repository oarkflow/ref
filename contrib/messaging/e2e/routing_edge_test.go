package e2e

import (
	"strings"
	"testing"
)

func putRule(t *testing.T, op *browser, id string, body map[string]any) {
	t.Helper()
	if st, out := op.do("PUT", "/ui/admin/routing-rules/"+id, body); st != 200 && st != 201 {
		t.Fatalf("rule %s = %d %v", id, st, out)
	}
}

func clearRules(t *testing.T, op *browser) {
	t.Helper()
	_, list := op.do("GET", "/ui/admin/routing-rules", nil)
	for _, r := range asList(list) {
		removeRule(t, op, asMap(r)["id"].(string))
	}
}

// Two rules that name the same provider for the same message: the higher
// priority wins; at the same priority the more specific rule wins; at the same
// specificity the lower id wins, so the outcome never depends on insertion order
// or on how the database happens to scan.
func TestRoutingRuleTiesAreDeterministic(t *testing.T) {
	s := start(t)
	op := operator(t, s)
	rulesFixture(t, s, op)
	demo := s.browser()
	demo.login("demo@example.com", "demo-pass-123")
	clearRules(t, op)
	const nepal = "+9779841234567"

	// More specific at the same priority: an account and a country beat an account alone.
	putRule(t, op, "rr_t_broad", map[string]any{"name": "broad", "mode": "avoid", "provider": "np_a", "account": "demo", "priority": 10})
	putRule(t, op, "rr_t_narrow", map[string]any{"name": "narrow", "mode": "use", "provider": "np_a", "account": "demo", "countries": []string{"NP"}, "priority": 10})
	if first, _, _ := explainRoute(demo, nepal, "hi", nil); first != "np_a" {
		t.Fatalf("the more specific rule should win: first = %s", first)
	}
	clearRules(t, op)

	// The same specificity: the lower id, whichever was saved first.
	for _, order := range [][2]string{{"rr_t_a_use", "rr_t_b_avoid"}, {"rr_t_b_avoid", "rr_t_a_use"}} {
		for _, id := range order {
			mode := "use"
			if strings.HasSuffix(id, "avoid") {
				mode = "avoid"
			}
			putRule(t, op, id, map[string]any{"name": id, "mode": mode, "provider": "np_a", "account": "demo", "priority": 10})
		}
		if first, _, _ := explainRoute(demo, nepal, "hi", nil); first != "np_a" {
			t.Fatalf("order %v: the lower id (use) should win, first = %s", order, first)
		}
		clearRules(t, op)
	}

	// A higher priority wins whatever its mode, and a lower one never overrides it.
	putRule(t, op, "rr_t_low_use", map[string]any{"name": "low", "mode": "use", "provider": "np_a", "priority": 5})
	putRule(t, op, "rr_t_high_avoid", map[string]any{"name": "high", "mode": "avoid", "provider": "np_a", "priority": 6})
	if _, ids, _ := explainRoute(demo, nepal, "hi", nil); contains(ids, "np_a") {
		t.Fatalf("the higher-priority avoid should win: %v", ids)
	}
	// Equal providers with equal scores keep a stable order: the id.
	clearRules(t, op)
	_, ids, _ := explainRoute(demo, nepal, "hi", nil)
	for i := 0; i < 5; i++ {
		_, again, _ := explainRoute(demo, nepal, "hi", nil)
		if strings.Join(again, ",") != strings.Join(ids, ",") {
			t.Fatalf("the chain changed between identical requests: %v then %v", ids, again)
		}
	}
	if ia, ib := indexOf(ids, "np_a"), indexOf(ids, "np_b"); ia < 0 || ib < ia {
		t.Fatalf("providers with equal scores should be ordered by id: %v", ids)
	}
}

func indexOf(l []string, s string) int {
	for i, x := range l {
		if x == s {
			return i
		}
	}
	return -1
}

// What an operator types is matched as written: quotes, ampersands, angle
// brackets, regular-expression characters in words, any written form of a number.
func TestRoutingRulesMatchTextExactly(t *testing.T) {
	s := start(t)
	op := operator(t, s)
	rulesFixture(t, s, op)
	demo := s.browser()
	demo.login("demo@example.com", "demo-pass-123")
	clearRules(t, op)
	const nepal = "+9779841234567"
	routed := func(text string) bool {
		first, _, _ := explainRoute(demo, nepal, text, nil)
		return first == "np_c"
	}
	cases := []struct {
		name  string
		rule  map[string]any
		match []string
		skip  []string
	}{
		{"words are literal text, not patterns",
			map[string]any{"content_words": []string{"c++", "a.b", "(x)", "[y]", "$5", "^z", "50% off"}},
			[]string{"I like C++ a lot", "see a.b now", "call (x) now", "list [y] here", "only $5 today", "^z marks", "SALE 50% OFF"},
			[]string{"c is fine", "axb", "x alone", "y alone", "5 only", "z alone", "50 off"}},
		{"a pattern with angle brackets and ampersands",
			map[string]any{"content_pattern": `<b>\d+</b> & co`},
			[]string{"<b>42</b> & co"}, []string{"<b>x</b> & co", "42 & co"}},
		{"a pattern with quotes and backslashes",
			map[string]any{"content_pattern": `say "hi\\there"`},
			[]string{`say "hi\there"`}, []string{`say "hithere"`}},
		{"a rule named with quotes, ampersands and an apostrophe",
			map[string]any{"name": `Bob's "VIP" & <co>`, "content_words": []string{"vip"}},
			[]string{"vip pass"}, []string{"plain"}},
	}
	for i, c := range cases {
		body := map[string]any{"name": "case", "mode": "use", "provider": "np_c", "priority": 10 + i}
		for k, v := range c.rule {
			body[k] = v
		}
		putRule(t, op, "rr_text", body)
		for _, text := range c.match {
			if !routed(text) {
				t.Errorf("%s: %q should match", c.name, text)
			}
		}
		for _, text := range c.skip {
			if routed(text) {
				t.Errorf("%s: %q should not match", c.name, text)
			}
		}
		removeRule(t, op, "rr_text")
	}
	// The reason a rule gives is its name, as typed.
	putRule(t, op, "rr_name", map[string]any{"name": `Bob's "VIP" & <co>`, "mode": "avoid", "provider": "np_c", "priority": 3})
	_, src := op.do("GET", "/ui/admin/rules/custom_routing", nil)
	if !strings.Contains(str(asMap(src), "source"), `Bob's \"VIP\" & <co>`) {
		t.Fatalf("the generated rule altered the name: %.600s", str(asMap(src), "source"))
	}
}

// Recipients are read the way the router reads a message's number, a prefix may be
// written with + or 00, countries and types are normalised, and what cannot be
// right is refused before it is stored.
func TestRoutingRuleInputIsNormalisedAndChecked(t *testing.T) {
	s := start(t)
	op := operator(t, s)
	rulesFixture(t, s, op)
	demo := s.browser()
	demo.login("demo@example.com", "demo-pass-123")
	clearRules(t, op)
	const nepal = "+9779841234567"

	putRule(t, op, "rr_in", map[string]any{"name": "forms", "mode": "use", "provider": "np_c", "priority": 10, "recipients": []string{" +977 984-123-4567 "}})
	for _, to := range []string{"9841234567", "+977 9841234567", "009779841234567", "977 984 123 4567", "09841234567", "(+977) 984-123-4567"} {
		if first, _, _ := explainRoute(demo, to, "hi", nil); first != "np_c" {
			t.Errorf("a message to %q should match the recipient: first = %s", to, first)
		}
	}
	if first, _, _ := explainRoute(demo, "+9779841234568", "hi", nil); first == "np_c" {
		t.Errorf("another number matched")
	}
	removeRule(t, op, "rr_in")

	putRule(t, op, "rr_prefix", map[string]any{"name": "prefix", "mode": "use", "provider": "np_c", "priority": 10, "recipient_prefix": "+977984", "countries": []string{" np "}, "types": []string{"Transactional"}})
	if first, _, _ := explainRoute(demo, nepal, "hi", nil); first != "np_c" {
		t.Errorf("prefix written with +, country in lower case, type capitalised: first = %s", first)
	}
	removeRule(t, op, "rr_prefix")

	bad := func(what string, body map[string]any, wantMessage string) {
		t.Helper()
		base := map[string]any{"name": "x", "mode": "use", "provider": "np_c"}
		for k, v := range body {
			base[k] = v
		}
		st, out := op.do("POST", "/ui/admin/routing-rules", base)
		msg := Stringify(get(asMap(out), "error", "message"))
		if st != 422 || !strings.Contains(msg, wantMessage) {
			t.Errorf("%s: %d %q, want 422 mentioning %q", what, st, msg, wantMessage)
		}
	}
	bad("a recipient that is not a number", map[string]any{"recipients": []string{"hello"}}, "hello")
	bad("a recipient that is too short", map[string]any{"recipients": []string{"98412"}}, "98412")
	bad("one bad recipient among good ones", map[string]any{"recipients": []string{"9841234567", "12"}}, "12")
	bad("a word that holds the separator", map[string]any{"content_words": []string{"a|b"}}, "cannot contain")
	bad("a country that is not two letters", map[string]any{"countries": []string{"NPL"}}, "two-letter")
	bad("a message type with odd characters", map[string]any{"types": []string{"o-tp!"}}, "message type")
	if _, list := op.do("GET", "/ui/admin/routing-rules", nil); len(asList(list)) != 0 {
		t.Fatalf("a refused rule was stored: %v", list)
	}

	// A provider id is a plain word, since the rules refer to it by name.
	for _, id := range []string{"Bad-Id", "has space", "quo%22te"} {
		st, out := op.do("PUT", "/ui/admin/providers/"+strings.ReplaceAll(id, " ", "%20"), map[string]any{"channel": "np_telecom", "kind": "smpp"})
		if st != 422 && st != 404 && st != 400 {
			t.Errorf("provider id %q accepted: %d %v", id, st, out)
		}
	}
}
