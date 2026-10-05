package e2e

import (
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// The bugs these cover, all of them "the router did not choose the provider I
// told it to", and all of them in the same place: what the routing engine is
// allowed to decide, and what it must not silently decide for you.

// putProvider creates or changes a provider, failing loudly: a silent 422 here
// reads as a routing assertion failing for the wrong reason.
func putProvider(t *testing.T, op *browser, id string, fields map[string]any) {
	t.Helper()
	fields["channel"] = "np_telecom"
	fields["kind"] = "smpp"
	if _, ok := fields["countries"]; !ok {
		fields["countries"] = []string{"NP"}
	}
	st, out := op.do("PUT", "/ui/admin/providers/"+id, fields)
	if st != 200 {
		t.Fatalf("provider %s = %d %v", id, st, out)
	}
}

func price(t *testing.T, op *browser, provider string, perSegment float64) {
	t.Helper()
	st, out := op.do("PUT", "/ui/admin/providers/"+provider+"/costs/NP", map[string]any{"per_segment": perSegment})
	if st != 200 {
		t.Fatalf("price %s = %d %v", provider, st, out)
	}
}

// account makes an account with a balance, a password and a signed-in browser.
func account(t *testing.T, s *stack, op *browser, id, email, password string, fields map[string]any) *browser {
	t.Helper()
	if fields == nil {
		fields = map[string]any{}
	}
	fields["name"] = id
	st, out := op.do("PUT", "/ui/admin/users/"+id, fields)
	if st != 200 {
		t.Fatalf("account %s = %d %v", id, st, out)
	}
	if st, out := op.do("POST", "/ui/admin/users/"+id+"/topup", map[string]any{"amount": 5, "reference": "t"}); st != 200 {
		t.Fatalf("topup %s = %d %v", id, st, out)
	}
	if st, out := op.do("PUT", "/ui/admin/users/"+id+"/password", map[string]any{"email": email, "password": password}); st != 200 {
		t.Fatalf("password %s = %d %v", id, st, out)
	}
	b := s.browser()
	if st := b.login(email, password); st != 200 {
		t.Fatalf("login %s = %d", email, st)
	}
	return b
}

// Routing does not read the request's dlr.
//
// `dlr` defaults to true, so reading it as a preference meant two identical
// messages differing only in that field took different routes — and, worse, it
// made every SMSPasal provider unreachable for ordinary traffic, because their
// HTTP API has no delivery callback (supports_dlr 0) and the row that refused
// them sat ABOVE the row that admits a provider an operator's rule names. That is
// the "I selected my number and it still would not use the live SMSPasal" report.
//
// The chain is now a function of the message and the catalog, and nothing else.
// Whether the receipt can actually be delivered is settled after routing, by
// rules/validate.bcl.
func TestRoutingDoesNotDependOnDlr(t *testing.T) {
	s := start(t)
	op := operator(t, s)
	waitForRoutingRules(t, op)
	demo := s.browser()
	demo.login("demo@example.com", "demo-pass-123")
	const nepal = "+9779841234567"

	// The best carrier available, and one nobody can get a receipt from.
	putProvider(t, op, "no_receipts", map[string]any{"quality": 99, "delivery_rate": 0.99, "supports_dlr": false})
	price(t, op, "no_receipts", 0.011)
	price(t, op, "np_telecom", 0.011)

	// Routing is a function of the message and the catalog, so the chain is the
	// same however the request asks about receipts. The lead is the receiptless
	// carrier because it scores best, not because of anything the request said.
	routeOf := func(extra map[string]any) []string {
		out := explainFull(t, demo, nepal, "hi", extra)
		var ids []string
		for _, c := range asList(asMap(out)["route"]) {
			ids = append(ids, asMap(c)["id"].(string))
		}
		return ids
	}
	unset, off := routeOf(nil), routeOf(map[string]any{"dlr": false})
	if len(unset) == 0 || unset[0] != "no_receipts" {
		t.Fatalf("chain %v, want the best-scoring provider first", unset)
	}
	if !slices.Equal(unset, off) {
		t.Fatalf("dlr: false changed the route: %v vs %v", unset, off)
	}
	// It is in the chain, not filtered out of it, and nothing about it is in the
	// reasons either: a receiptless provider is a provider.
	if !slices.Contains(off, "no_receipts") {
		t.Fatalf("a provider that cannot report receipts was filtered out: %v", off)
	}
	for _, r := range asList(asMap(explainFull(t, demo, nepal, "hi", map[string]any{"dlr": false}))["rejected"]) {
		if asMap(r)["id"] == "no_receipts" {
			t.Fatalf("a provider that cannot report receipts was rejected by routing: %v", r)
		}
	}

	// What dlr does change is the promise, and it is settled after routing: the
	// carrier is the same one, and the request is refused because it cannot keep
	// the receipt it asked for. It is not quietly swapped for a different one.
	st, out := demo.do("POST", "/ui/messages", map[string]any{"to": nepal, "text": "hi", "dlr": true})
	if st != 422 || !strings.Contains(toJSON(out), "NO_DLR_PROVIDER") {
		t.Fatalf("dlr: true over a receiptless lead = %d %v, want 422 NO_DLR_PROVIDER", st, out)
	}

	// Make the receiptless carrier score worse, so np_telecom leads: the same
	// request is then accepted, which is what shows the refusal above was about
	// the carrier the route chose rather than about the flag.
	putProvider(t, op, "no_receipts", map[string]any{"quality": 10, "delivery_rate": 0.8})
	if first, ids, _ := explainRoute(demo, nepal, "hi", nil); first != "np_telecom" {
		t.Fatalf("after demoting it: first = %s (%v)", first, ids)
	}
	if st, out := demo.do("POST", "/ui/messages", map[string]any{"to": nepal, "text": "hi", "dlr": true}); st != 202 {
		t.Fatalf("dlr: true over a carrier that reports = %d %v", st, out)
	}
}

// The receipt promise is kept, but by validation rather than by routing: a
// message that explicitly asks for a receipt is refused when nothing on its route
// can report one, instead of being quietly sent without receipts.
func TestAReceiptRequestIsRefusedWhenNothingCanReportOne(t *testing.T) {
	s := start(t)
	op := operator(t, s)
	waitForRoutingRules(t, op)
	demo := s.browser()
	demo.login("demo@example.com", "demo-pass-123")
	const nepal = "+9779841234567"

	// The whole route is receiptless: one rule naming the only carrier available.
	putProvider(t, op, "receiptless", map[string]any{"quality": 99, "delivery_rate": 0.99, "supports_dlr": false})
	only := addRule(t, op, map[string]any{"name": "everything over the receiptless carrier", "mode": "only", "provider": "receiptless", "account": "demo", "priority": 10})

	st, out := demo.do("POST", "/ui/messages", map[string]any{"to": nepal, "text": "hi", "dlr": true})
	if st != 422 || !strings.Contains(toJSON(out), "NO_DLR_PROVIDER") {
		t.Fatalf("an explicit receipt request = %d %v, want 422 NO_DLR_PROVIDER", st, out)
	}

	// A request that did not ask is sent, over the same carrier, and promises
	// nothing: dlr defaults to true, and that is not an ask.
	status, sent := s.send("demo", map[string]any{"to": nepal, "text": "hi"})
	if status != 202 || str(sent, "provider") != "receiptless" {
		t.Fatalf("a request that did not ask = %d %v", status, sent)
	}
	settled := sendMessage(t, s, "demo", map[string]any{"to": nepal, "text": "hi"})
	if d, _ := get(settled, "message", "want_dlr").(float64); d != 0 {
		t.Fatalf("want_dlr = %v over a carrier that reports none", d)
	}

	// And with a carrier that can report, the same explicit request is accepted.
	removeRule(t, op, only)
	if st, out := demo.do("POST", "/ui/messages", map[string]any{"to": nepal, "text": "hi", "dlr": true}); st != 202 {
		t.Fatalf("over a carrier that reports: %d %v", st, out)
	}
	if d, _ := get(sendMessage(t, s, "demo", map[string]any{"to": nepal, "text": "hi", "dlr": true}), "message", "want_dlr").(float64); d != 1 {
		t.Fatalf("want_dlr = %v over a carrier that reports receipts", d)
	}
}

// A rule that names a provider outranks the limits it trades away, and not the
// ones it cannot. rules/routing.bcl reads as three bands, and the band a row is
// in is a statement about who may overrule it.
func TestARuleOverrulesSoftLimitsButNotHardOnes(t *testing.T) {
	s := start(t)
	op := operator(t, s)
	waitForRoutingRules(t, op)
	demo := s.browser()
	demo.login("demo@example.com", "demo-pass-123")
	const nepal = "+9779841234567"
	long := strings.Repeat("hello ", 60)

	// Long messages (3+ segments) are a soft limit: a rule may name a provider
	// that cannot send them.
	putProvider(t, op, "limited", map[string]any{"quality": 99, "delivery_rate": 0.99, "supports_long": false})
	price(t, op, "limited", 0.011)

	// Without the rule: too many segments for it, whatever its score says.
	if first, ids, _ := explainRoute(demo, nepal, long, nil); first != "np_telecom" {
		t.Fatalf("multi-segment fallback = %s (%v), want np_telecom", first, ids)
	}
	// Short messages do not care, and it is the best carrier for them.
	if first, _, _ := explainRoute(demo, nepal, "hi", nil); first != "limited" {
		t.Fatalf("single segment: %s, want limited", first)
	}

	// With it: the operator is knowingly trading the limit away.
	id := addRule(t, op, map[string]any{"name": "long over limited", "mode": "use", "provider": "limited", "account": "demo", "priority": 10})
	if first, ids, _ := explainRoute(demo, nepal, long, nil); first != "limited" {
		t.Fatalf("with the rule: first = %s (%v), want limited", first, ids)
	}
	removeRule(t, op, id)

	// A hard limit is not a trade-off. A paused provider stays out even when a
	// rule names it, because the message would be lost.
	putProvider(t, op, "limited", map[string]any{"state": "paused"})
	paused := addRule(t, op, map[string]any{"name": "over a paused provider", "mode": "use", "provider": "limited", "account": "demo", "priority": 10})
	_, _, rejected := explainRoute(demo, nepal, "hi", nil)
	if !strings.Contains(rejected["limited"], "provider-not-active") {
		t.Fatalf("the reason should be that the provider is not active, got %v", rejected)
	}
	removeRule(t, op, paused)
}

// "Only this provider" is kept, not merely preferred. If the provider it names
// cannot carry the message, no other provider may: quietly sending it elsewhere
// is the failure this mode exists to prevent.
func TestOnlyIsKeptAndNotJustPreferred(t *testing.T) {
	s := start(t)
	op := operator(t, s)
	waitForRoutingRules(t, op)
	demo := s.browser()
	demo.login("demo@example.com", "demo-pass-123")
	const nepal = "+9779841234567"

	only := addRule(t, op, map[string]any{"name": "only np_telecom", "mode": "only", "provider": "np_telecom", "priority": 10})
	if first, _, _ := explainRoute(demo, nepal, "hi", nil); first != "np_telecom" {
		t.Fatalf("the named provider should carry the message, got %s", first)
	}

	// Pause it. The reservation is unmet, so the message goes nowhere rather
	// than to global_fallback.
	putProvider(t, op, "np_telecom", map[string]any{"state": "paused"})
	st, out := demo.do("POST", "/ui/route/explain", map[string]any{"to": nepal, "text": "hi"})
	if st == 200 {
		t.Fatalf("an unmet 'only' still routed the message: %v", asMap(out)["route"])
	}
	if !strings.Contains(strings.ToLower(toJSON(out)), "reserves") {
		t.Fatalf("the refusal should say the reservation is unmet, got %v", out)
	}
	if !strings.Contains(toJSON(out), "RESERVED_PROVIDER_UNAVAILABLE") {
		t.Fatalf("the refusal should carry its own code, got %v", out)
	}

	// And it is refused rather than queued: nothing is paid for and no job is
	// published for a message that must not be sent.
	if st, out := demo.do("POST", "/ui/messages", map[string]any{"to": nepal, "text": "hi"}); st != 503 {
		t.Fatalf("sending under an unmet 'only' = %d %v, want 503", st, out)
	}

	// Back in use: it carries the message again.
	putProvider(t, op, "np_telecom", map[string]any{"state": "active"})
	if first, _, _ := explainRoute(demo, nepal, "hi", nil); first != "np_telecom" {
		t.Fatalf("after resuming: %s", first)
	}
	removeRule(t, op, only)
}

// A provider nobody has priced is not free. An absent price row used to read as
// zero, so a newly added provider won every cost ranking by being missing from
// the price table.
func TestAnUnpricedProviderIsNotFree(t *testing.T) {
	s := start(t)
	op := operator(t, s)
	waitForRoutingRules(t, op)
	thrifty := account(t, s, op, "thrifty", "ops@thrifty.example", "a-long-password-1", map[string]any{"objective": "lowest_cost"})
	const nepal = "+9779841234567"

	putProvider(t, op, "unpriced", map[string]any{"quality": 99, "delivery_rate": 0.99})
	price(t, op, "np_telecom", 0.011)

	if first, ids, _ := explainRoute(thrifty, nepal, "hi", nil); first == "unpriced" {
		t.Fatalf("the unpriced provider won a lowest_cost routing: %v", ids)
	}

	// Make the priced one dearer. The unpriced one still must not come out
	// cheaper than it by not being priced.
	price(t, op, "np_telecom", 0.5)
	out := explainFull(t, thrifty, nepal, "hi")
	if ranking := str(asMap(out), "objective"); ranking != "route_cost" {
		t.Fatalf("objective = %q, want route_cost", ranking)
	}
	saw := false
	for _, r := range asList(asMap(out)["route"]) {
		e := asMap(r)
		if e["id"] != "unpriced" {
			continue
		}
		saw = true
		if known, _ := e["price_known"].(float64); known == 1 {
			t.Fatalf("unpriced reported a known price: %v", e)
		}
		if cost, _ := e["cost_per_segment"].(float64); cost <= 0.011 {
			t.Fatalf("an unpriced provider cost %v, cheaper than the cheapest price on file", cost)
		}
	}
	if !saw {
		t.Fatalf("the unpriced provider is not even in the chain: %v", asMap(out)["route"])
	}
}

// The per-account objective is a word rules/routing.bcl reads. The console used
// to offer "cost"/"delivery" while the rules tested "lowest_cost"/
// "highest_delivery", and nothing validated what was stored, so choosing an
// objective from the console was silently the default ranking.
func TestAnAccountsObjectiveChangesTheRankingAndIsChecked(t *testing.T) {
	s := start(t)
	op := operator(t, s)
	waitForRoutingRules(t, op)
	const nepal = "+9779841234567"

	// Two interchangeable carriers: cheap and unreliable, dear and reliable.
	putProvider(t, op, "cheap_np", map[string]any{"quality": 80, "delivery_rate": 0.81})
	putProvider(t, op, "np_telecom", map[string]any{"quality": 99, "delivery_rate": 0.99})
	price(t, op, "cheap_np", 0.001)
	price(t, op, "np_telecom", 0.05)

	acct := account(t, s, op, "objtest", "ops@objtest.example", "a-long-password-1", map[string]any{"objective": "highest_delivery"})
	out := explainFull(t, acct, nepal, "hi")
	if ranking := str(asMap(out), "objective"); ranking != "route_delivery" {
		t.Fatalf("objective = %q, want route_delivery", ranking)
	}
	if first := asMap(asList(asMap(out)["route"])[0]); first["id"] != "np_telecom" {
		t.Fatalf("highest_delivery chose %v, want the reliable carrier", first["id"])
	}

	// Same two carriers, the other preference.
	if st, out := op.do("PUT", "/ui/admin/users/objtest", map[string]any{"objective": "lowest_cost"}); st != 200 {
		t.Fatalf("set lowest_cost = %d %v", st, out)
	}
	out = explainFull(t, acct, nepal, "hi")
	if ranking := str(asMap(out), "objective"); ranking != "route_cost" {
		t.Fatalf("objective = %q, want route_cost", ranking)
	}
	if first := asMap(asList(asMap(out)["route"])[0]); first["id"] != "cheap_np" {
		t.Fatalf("lowest_cost chose %v, want the cheap carrier", first["id"])
	}

	// "balanced" is a real choice, not the absence of one.
	if st, out := op.do("PUT", "/ui/admin/users/objtest", map[string]any{"objective": "balanced"}); st != 200 {
		t.Fatalf("set balanced = %d %v", st, out)
	}
	if ranking := str(explainFull(t, acct, nepal, "hi"), "objective"); ranking != "route_balanced" {
		t.Fatalf("objective = %q, want route_balanced", ranking)
	}

	// A word the rules do not know is refused, not stored and then ignored.
	for _, bad := range []string{"cheapest", "cost", "delivery"} {
		if st, _ := op.do("PUT", "/ui/admin/users/objtest", map[string]any{"objective": bad}); st != 422 {
			t.Fatalf("objective %q = %d, want 422", bad, st)
		}
	}
	// And the account kept the objective it had.
	if st, out := op.do("GET", "/ui/admin/users/objtest", nil); st != 200 || str(asMap(out), "objective") != "balanced" {
		t.Fatalf("a refused objective changed the account: %d %v", st, out)
	}
}

// Every rejection carries the code the rule gave it. The reasons were readable
// but reason_code was empty for every row, so a caller could not tell a paused
// provider from a rule that did not apply.
func TestRejectionsCarryTheirReasonCode(t *testing.T) {
	s := start(t)
	op := operator(t, s)
	waitForRoutingRules(t, op)
	demo := s.browser()
	demo.login("demo@example.com", "demo-pass-123")

	putProvider(t, op, "np_telecom", map[string]any{"state": "paused"})
	_, out := demo.do("POST", "/ui/route/explain", map[string]any{"to": "+9779841234567", "text": "hi"})
	seen := 0
	for _, r := range asList(asMap(out)["rejected"]) {
		e := asMap(r)
		if code, _ := e["reason_code"].(string); code == "" {
			t.Fatalf("a rejection with no reason_code: %v", e)
		}
		seen++
	}
	if seen == 0 {
		t.Fatalf("nothing was rejected, so the codes were not checked: %v", out)
	}
}

// The message goes out through the FIRST, highest-scored provider, and what the
// message expects afterwards is what that provider can actually deliver.
//
// `dlr` defaults to true, and the receipts pipeline reads messages.want_dlr. A
// carrier with no delivery callback used to leave that column at "yes it will",
// so the pipeline waited for receipts that could never arrive.
func TestTheMessageGoesThroughTheTopScoredProvider(t *testing.T) {
	s := start(t)
	op := operator(t, s)
	waitForRoutingRules(t, op)
	demo := s.browser()
	demo.login("demo@example.com", "demo-pass-123")
	const nepal = "+9779841234567"

	// The sandbox SMSPasal provider, assigned to demo as well, so it leads on its
	// tier rather than on a rule — and it cannot report receipts, which is the
	// point: a receiptless carrier carries the message either way.
	assigned := []string{"smspasal_live", "demo"}
	if st, out := op.do("PUT", "/ui/admin/providers/smspasal", map[string]any{
		"channel": "smspasal", "kind": "http", "quality": 95, "delivery_rate": 0.98,
		"countries": []string{"NP"}, "supports_dlr": false, "users": assigned, "assigned_only": true,
	}); st != 200 {
		t.Fatalf("assign smspasal = %d %v", st, out)
	}
	price(t, op, "smspasal", 0.012)

	// The chain is scored, and the rule put smspasal first.
	out := explainFull(t, demo, nepal, "hi")
	chain := asList(asMap(out)["route"])
	if len(chain) == 0 || asMap(chain[0])["id"] != "smspasal" {
		t.Fatalf("chain %v, want smspasal first", chain)
	}
	// Scores fall down the chain: the provider used is the highest-scored one.
	first, _ := asMap(chain[0])["score"].(float64)
	for i, c := range chain[1:] {
		if s, _ := asMap(c)["score"].(float64); s > first {
			t.Fatalf("entry %d scored %v, above the first entry's %v", i+1, s, first)
		}
	}

	// And the send really goes through it, not through a fallback.
	status, sent := s.send("demo", map[string]any{"to": nepal, "text": "hi"})
	if status != 202 || str(sent, "provider") != "smspasal" {
		t.Fatalf("send = %d %v", status, sent)
	}
	if routing := asList(sent["route"]); len(routing) == 0 || routing[0] != "smspasal" {
		t.Fatalf("the accepted route %v does not start with the provider used", routing)
	}
	eventually(t, "delivered by smspasal", func() bool { return s.state("demo", str(sent, "id")) == "delivered" })
	_, m := s.do("GET", "/v1/messages/"+str(sent, "id"), s.key("demo"), nil)
	if str(m, "message", "provider") != "smspasal" {
		t.Fatalf("delivered through %q, want smspasal", str(m, "message", "provider"))
	}
	// want_dlr is the request's dlr AND the provider's capability: this carrier
	// reports none, so none is promised.
	if d, _ := get(m, "message", "want_dlr").(float64); d != 0 {
		t.Fatalf("want_dlr = %v for a provider that cannot report receipts", d)
	}

	// An explicit receipt request over a carrier that reports none is refused,
	// and the refusal says which promise cannot be kept — the carrier is not
	// swapped for one that could, because that is a different route.
	if st, out := s.do("POST", "/v1/messages", s.key("demo"), map[string]any{"to": nepal, "text": "hi", "dlr": true}); st != 422 {
		t.Fatalf("a receipt request over a receiptless carrier = %d %v, want 422", st, out)
	}

	// Over a carrier that can report, one is promised. The carrier is chosen by
	// score and its tier, never by dlr: routing does not read it, so the only
	// thing dlr changes is what the message can expect afterwards.
	if st, out := op.do("PUT", "/ui/admin/providers/smspasal", map[string]any{
		"channel": "smspasal", "kind": "http", "users": []string{"smspasal_live"},
	}); st != 200 {
		t.Fatalf("unassign smspasal = %d %v", st, out)
	}
	over := sendMessage(t, s, "demo", map[string]any{"to": nepal, "text": "hi", "dlr": true})
	if str(over, "message", "provider") != "np_telecom" {
		t.Fatalf("over a carrier that reports, delivered through %q", str(over, "message", "provider"))
	}
	if d, _ := get(over, "message", "want_dlr").(float64); d != 1 {
		t.Fatalf("want_dlr = %v over %q, which reports receipts", d, str(over, "message", "provider"))
	}
}

func sendMessage(t *testing.T, s *stack, user string, body map[string]any) map[string]any {
	t.Helper()
	status, out := s.send(user, body)
	if status != 202 {
		t.Fatalf("send = %d %v", status, out)
	}
	eventually(t, "settled", func() bool {
		_, m := s.do("GET", "/v1/messages/"+str(out, "id"), s.key(user), nil)
		return str(m, "message", "state") != "queued" && str(m, "message", "state") != "dispatching"
	})
	_, m := s.do("GET", "/v1/messages/"+str(out, "id"), s.key(user), nil)
	return m
}
func TestSwitchingARuleOffIsEffective(t *testing.T) {
	s := start(t)
	op := operator(t, s)
	waitForRoutingRules(t, op)
	demo := s.browser()
	demo.login("demo@example.com", "demo-pass-123")
	const nepal = "+9779841234567"

	putProvider(t, op, "np_a", map[string]any{"quality": 50, "delivery_rate": 0.9})
	id := addRule(t, op, map[string]any{"name": "demo over np_a", "mode": "use", "provider": "np_a", "account": "demo", "priority": 10})
	if first, _, _ := explainRoute(demo, nepal, "hi", nil); first != "np_a" {
		t.Fatalf("with the rule on: %s, want np_a", first)
	}
	if st, out := op.do("PUT", "/ui/admin/routing-rules/"+id+"/enabled", map[string]any{"enabled": false}); st != 200 {
		t.Fatalf("switch off = %d %v", st, out)
	}
	// Immediately, in the same round trip: not on the next tick.
	if first, ids, _ := explainRoute(demo, nepal, "hi", nil); first == "np_a" {
		t.Fatalf("a switched-off rule is still routing: %v", ids)
	}
	if strings.Contains(liveCustomRouting(op), `"`+id+`"`) {
		t.Fatalf("a switched-off rule is still in the live definition")
	}

	// And it stays off across the convergence tick.
	time.Sleep(3 * time.Second)
	if strings.Contains(liveCustomRouting(op), `"`+id+`"`) {
		t.Fatalf("a switched-off rule came back on a later tick")
	}
	removeRule(t, op, id)
}

// Rule edits and the convergence tick both publish, at the same time, of the same
// definition. Publishing writes a version and then activates it, so two of them
// interleaving left the engine on a version that was not fully written — a 500 on
// an ordinary delete, at random.
func TestConcurrentRuleEditsAndTheTickDoNotCollide(t *testing.T) {
	s := start(t)
	op := operator(t, s)
	waitForRoutingRules(t, op)
	putProvider(t, op, "np_a", map[string]any{"quality": 50, "delivery_rate": 0.9})

	stop := make(chan struct{})
	var tickers sync.WaitGroup
	for i := 0; i < 4; i++ {
		tickers.Add(1)
		go func() {
			defer tickers.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				if st, out := op.do("PUT", "/ui/admin/routing-rules/rr_example_demo_live_number/enabled", map[string]any{"enabled": true}); st != 200 {
					t.Errorf("tick publish = %d %v", st, out)
					return
				}
			}
		}()
	}
	for i := 0; i < 40; i++ {
		id := addRule(t, op, map[string]any{"name": fmt.Sprintf("churn %d", i), "mode": "use", "provider": "np_a", "account": "demo", "priority": i % 50})
		if st, out := op.do("DELETE", "/ui/admin/routing-rules/"+id, nil); st != 200 {
			t.Fatalf("delete under a concurrent publish = %d %v", st, out)
		}
	}
	close(stop)
	tickers.Wait()

	// The definition is intact and still what the table says.
	if !strings.Contains(liveCustomRouting(op), "rr_example_demo_live_number") {
		t.Fatalf("a concurrent publish lost a rule:\n%s", liveCustomRouting(op))
	}
}
