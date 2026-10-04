package e2e

import (
	"fmt"
	"math/rand"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// A reference model of the routing rules, checked against the running router.
//
// The model is the written specification, not the implementation: given the
// chain the router builds with no rules at all (the baseline: who is eligible,
// each provider's tier and score), it says what the operator's rules must change.
//
//   - A provider's winning rule is the matching rule with the highest priority;
//     a tie goes to the more specific rule, then to the lower id.
//   - "avoid" removes the provider. "use" moves it above everything without a
//     rule, ordered by rule priority; "only" does that and removes everything
//     with a lower priority.
//   - A rule that names a provider also admits it when only assignment kept it
//     out, if the provider serves the destination; the provider's own limits
//     (paused, circuit open, capabilities) still apply.
//
// Random rule sets and messages are generated from a fixed seed and the model's
// chain must equal the router's, provider for provider, in order.

type modelRule struct {
	ID, Provider, Mode, Account string
	Priority                    int
	Countries, Recipients       []string // upper-case ISO codes; international digits
	Prefix, RecipientPattern    string
	SenderPattern               string
	Words                       []string
	ContentPattern              string
	Types                       []string
	MinSeg, MaxSeg              int
}

func (r modelRule) specificity() int {
	n := 0
	for _, set := range []bool{r.Account != "", len(r.Countries) > 0, len(r.Recipients) > 0, r.Prefix != "", r.RecipientPattern != "", r.SenderPattern != "", len(r.Words) > 0, r.ContentPattern != "", len(r.Types) > 0, r.MinSeg > 0, r.MaxSeg > 0} {
		if set {
			n++
		}
	}
	if len(r.Recipients) > 0 {
		n += 2
	}
	return n
}

type modelMessage struct {
	Account, To, Digits, Country, Text, From, Type string
	Segments                                       int
}

func (r modelRule) matches(m modelMessage) bool {
	has := func(list []string, v string) bool {
		for _, x := range list {
			if x == v {
				return true
			}
		}
		return false
	}
	switch {
	case r.Account != "" && r.Account != m.Account,
		len(r.Countries) > 0 && !has(r.Countries, m.Country),
		len(r.Recipients) > 0 && !has(r.Recipients, m.Digits),
		r.Prefix != "" && !strings.HasPrefix(m.Digits, r.Prefix),
		r.RecipientPattern != "" && !regexp.MustCompile(r.RecipientPattern).MatchString(m.Digits),
		r.SenderPattern != "" && !regexp.MustCompile(r.SenderPattern).MatchString(m.From),
		r.ContentPattern != "" && !regexp.MustCompile(r.ContentPattern).MatchString(m.Text),
		len(r.Types) > 0 && !has(r.Types, m.Type),
		r.MinSeg > 0 && m.Segments < r.MinSeg,
		r.MaxSeg > 0 && m.Segments > r.MaxSeg:
		return false
	}
	if len(r.Words) > 0 {
		lower := strings.ToLower(m.Text)
		found := false
		for _, w := range r.Words {
			if strings.Contains(lower, strings.ToLower(w)) {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// entry is one provider in a chain.
type entry struct {
	ID               string
	Priority, Metric float64
	Exclusive        bool
	Granted          bool // admitted by a rule; its metric is not known
}

type baseline struct {
	chain    map[string]entry // eligible with no rules
	rejected map[string]string
	order    []string
}

// expected is the model's chain.
func expected(base baseline, serves map[string][]string, rules []modelRule, m modelMessage) []string {
	winner := map[string]*modelRule{}
	for i := range rules {
		r := &rules[i]
		if !r.matches(m) {
			continue
		}
		w := winner[r.Provider]
		if w == nil || r.Priority > w.Priority || (r.Priority == w.Priority && (r.specificity() > w.specificity() || (r.specificity() == w.specificity() && r.ID < w.ID))) {
			winner[r.Provider] = r
		}
	}
	var out []entry
	ids := map[string]bool{}
	for id := range base.chain {
		ids[id] = true
	}
	for id := range base.rejected {
		ids[id] = true
	}
	for id := range ids {
		e, eligible := base.chain[id]
		w := winner[id]
		switch {
		case w != nil && w.Mode == "avoid":
			continue
		case w != nil && eligible:
			e.Priority, e.Exclusive = float64(200000+w.Priority*1000), w.Mode == "only"
		case w != nil && !eligible && (base.rejected[id] == "not-available-here" || base.rejected[id] == "a-private-provider-of-someone-else"):
			countries := serves[id]
			ok := len(countries) == 0
			for _, c := range countries {
				ok = ok || c == m.Country
			}
			if !ok {
				continue
			}
			e = entry{ID: id, Priority: float64(200000 + w.Priority*1000), Exclusive: w.Mode == "only", Granted: true}
		case !eligible:
			continue
		}
		out = append(out, e)
	}
	floor, hasFloor := 0.0, false
	for _, e := range out {
		if e.Exclusive && (!hasFloor || e.Priority > floor) {
			floor, hasFloor = e.Priority, true
		}
	}
	var kept []entry
	for _, e := range out {
		if !hasFloor || e.Priority >= floor {
			kept = append(kept, e)
		}
	}
	sort.SliceStable(kept, func(i, j int) bool {
		a, b := kept[i], kept[j]
		if a.Priority != b.Priority {
			return a.Priority > b.Priority
		}
		if a.Metric != b.Metric {
			return a.Metric > b.Metric
		}
		return a.ID < b.ID
	})
	ids2 := make([]string, len(kept))
	for i, e := range kept {
		ids2[i] = e.ID
	}
	return ids2
}

func explainBaseline(b *browser, m modelMessage) (baseline, bool) {
	body := map[string]any{"to": m.To, "text": m.Text}
	if m.From != "" {
		body["from"] = m.From
	}
	if m.Type != "" {
		body["type"] = m.Type
	}
	st, out := b.do("POST", "/ui/route/explain", body)
	if st != 200 {
		return baseline{}, false
	}
	res := asMap(out)
	bl := baseline{chain: map[string]entry{}, rejected: map[string]string{}}
	for _, r := range asList(res["route"]) {
		e := asMap(r)
		id := e["id"].(string)
		prio, _ := e["priority"].(float64)
		score, _ := e["score"].(float64)
		bl.chain[id] = entry{ID: id, Priority: prio, Metric: score - prio}
		bl.order = append(bl.order, id)
	}
	for _, r := range asList(res["rejected"]) {
		e := asMap(r)
		bl.rejected[e["id"].(string)] = Stringify(e["rule"])
	}
	return bl, true
}

func segmentsOf(text string) int {
	if len(text) <= 160 {
		return 1
	}
	return (len(text) + 152) / 153
}

func ruleBody(r modelRule) map[string]any {
	body := map[string]any{"name": "model " + r.ID, "mode": r.Mode, "provider": r.Provider, "priority": r.Priority}
	set := func(k string, v any) { body[k] = v }
	if r.Account != "" {
		set("account", r.Account)
	}
	if len(r.Countries) > 0 {
		set("countries", r.Countries)
	}
	if len(r.Recipients) > 0 {
		set("recipients", r.Recipients)
	}
	if r.Prefix != "" {
		set("recipient_prefix", r.Prefix)
	}
	if r.RecipientPattern != "" {
		set("recipient_pattern", r.RecipientPattern)
	}
	if r.SenderPattern != "" {
		set("sender_pattern", r.SenderPattern)
	}
	if len(r.Words) > 0 {
		set("content_words", r.Words)
	}
	if r.ContentPattern != "" {
		set("content_pattern", r.ContentPattern)
	}
	if len(r.Types) > 0 {
		set("types", r.Types)
	}
	if r.MinSeg > 0 {
		set("min_segments", r.MinSeg)
	}
	if r.MaxSeg > 0 {
		set("max_segments", r.MaxSeg)
	}
	return body
}

func TestRoutingRulesMatchTheModel(t *testing.T) {
	s := start(t)
	op := operator(t, s)
	rulesFixture(t, s, op)
	demo, newco := s.browser(), s.browser()
	demo.login("demo@example.com", "demo-pass-123")
	newco.login("ops@newco.example", "a-long-password-1")
	browsers := map[string]*browser{"demo": demo, "newco": newco}

	// No rules at all: the baseline.
	_, list := op.do("GET", "/ui/admin/routing-rules", nil)
	for _, r := range asList(list) {
		removeRule(t, op, asMap(r)["id"].(string))
	}
	serves := map[string][]string{}
	providers := []string{}
	_, plist := op.do("GET", "/ui/admin/providers", nil)
	for _, p := range asList(plist) {
		m := asMap(p)
		id := m["id"].(string)
		providers = append(providers, id)
		for _, c := range strings.Split(Stringify(m["countries"]), "|") {
			if c != "" {
				serves[id] = append(serves[id], c)
			}
		}
	}
	sort.Strings(providers)

	rng := rand.New(rand.NewSource(20260404))
	numbers := []struct{ typed, digits, country string }{
		{"9841234567", "9779841234567", "NP"}, {"+977 984-123-4568", "9779841234568", "NP"}, {"009779801234567", "9779801234567", "NP"},
		{"977 9856034617", "9779856034617", "NP"}, {"+91 98765 43210", "919876543210", "IN"}, {"+14155552671", "14155552671", "US"},
	}
	texts := []string{"hello there", "Your OTP is 481516", "invoice 42 is due", "SALE 50% off today", "c++ rocks and a.b too", "axb is not a.b", "verification code 99", strings.Repeat("a long message ", 14)}
	froms := []string{"", "ACMEBANK", "SHOP", "+9779800000000"}
	types := []string{"", "otp", "promotional", "transactional"}
	var messages []modelMessage
	for i := 0; i < 40; i++ {
		n := numbers[rng.Intn(len(numbers))]
		text := texts[rng.Intn(len(texts))]
		m := modelMessage{Account: []string{"demo", "newco"}[rng.Intn(2)], To: n.typed, Digits: n.digits, Country: n.country, Text: text, From: froms[rng.Intn(len(froms))], Type: types[rng.Intn(len(types))], Segments: segmentsOf(text)}
		if m.Type == "" {
			m.Type = "transactional"
		}
		messages = append(messages, m)
	}
	bases := make([]baseline, len(messages))
	usable := make([]bool, len(messages))
	for i, m := range messages {
		bases[i], usable[i] = explainBaseline(browsers[m.Account], m)
		if m.From == "" {
			messages[i].From = "SMS"
		}
	}

	words := []string{"otp", "invoice", "sale", "c++", "a.b", "50% off", "verification code", "hello"}
	patterns := []string{`\d{6}`, `(?i)sale`, `a\.b`, `^Your`, `[0-9]{2}$`}
	rounds, checked := 30, 0
	for round := 0; round < rounds; round++ {
		n := 4 + rng.Intn(9)
		prios := rng.Perm(300)
		var rules []modelRule
		for i := 0; i < n; i++ {
			r := modelRule{ID: fmt.Sprintf("rr_model_%02d_%02d", round, i), Mode: []string{"use", "use", "use", "only", "avoid"}[rng.Intn(5)], Priority: 1 + prios[i]}
			r.Provider = providers[rng.Intn(len(providers))]
			if rng.Intn(3) == 0 {
				r.Account = []string{"demo", "newco"}[rng.Intn(2)]
			}
			if rng.Intn(5) == 0 {
				r.Countries = []string{[]string{"NP", "IN", "US"}[rng.Intn(3)]}
			}
			if rng.Intn(6) == 0 {
				nn := numbers[rng.Intn(len(numbers))]
				r.Recipients = []string{nn.typed} // written the way a person would, matched as digits
			}
			if rng.Intn(8) == 0 {
				r.Prefix = []string{"9779841", "9779801", "91", "977985"}[rng.Intn(4)]
			}
			if rng.Intn(6) == 0 {
				r.Words = []string{words[rng.Intn(len(words))], words[rng.Intn(len(words))]}
			}
			if rng.Intn(8) == 0 {
				r.ContentPattern = patterns[rng.Intn(len(patterns))]
			}
			if rng.Intn(10) == 0 {
				r.SenderPattern = []string{`^ACME`, `^SHOP$`, `^\+`}[rng.Intn(3)]
			}
			if rng.Intn(10) == 0 {
				r.Types = []string{[]string{"otp", "promotional", "transactional"}[rng.Intn(3)]}
			}
			if rng.Intn(10) == 0 {
				r.MinSeg = 2
			}
			if rng.Intn(14) == 0 {
				r.RecipientPattern = `98[0-9]{8}$`
			}
			rules = append(rules, r)
		}
		for _, r := range rules {
			st, out := op.do("PUT", "/ui/admin/routing-rules/"+r.ID, ruleBody(r))
			if st != 200 && st != 201 {
				t.Fatalf("round %d: rule %+v = %d %v", round, r, st, out)
			}
		}
		// The model reads recipients as digits: normalise what was typed.
		for i := range rules {
			for j, typed := range rules[i].Recipients {
				for _, n := range numbers {
					if n.typed == typed {
						rules[i].Recipients[j] = n.digits
					}
				}
			}
		}
		for i, m := range messages {
			if !usable[i] {
				continue
			}
			body := map[string]any{"to": m.To, "text": m.Text}
			if m.From != "SMS" {
				body["from"] = m.From
			}
			if m.Type != "transactional" {
				body["type"] = m.Type
			}
			st, out := browsers[m.Account].do("POST", "/ui/route/explain", body)
			var got []string
			if st == 200 {
				for _, r := range asList(asMap(out)["route"]) {
					got = append(got, asMap(r)["id"].(string))
				}
			}
			want := expected(bases[i], serves, rules, m)
			if len(want) == 0 {
				if st == 200 && len(got) != 0 {
					t.Fatalf("round %d message %+v: router chain %v, model says none", round, m, got)
				}
				checked++
				continue
			}
			if st != 200 || strings.Join(got, ",") != strings.Join(want, ",") {
				t.Fatalf("round %d, message %+v\nrouter: %v (status %d)\nmodel:  %v\nrules:\n%s", round, m, got, st, want, dumpRules(rules))
			}
			checked++
		}
		for _, r := range rules {
			removeRule(t, op, r.ID)
		}
	}
	t.Logf("%d routes checked against the model across %d random rule sets", checked, rounds)
	if checked < rounds*20 {
		t.Fatalf("only %d routes checked", checked)
	}
}

func dumpRules(rules []modelRule) string {
	var b strings.Builder
	for _, r := range rules {
		fmt.Fprintf(&b, "  %+v\n", r)
	}
	return b.String()
}
