package e2e

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func accounts(t *testing.T, op *browser, provider string) map[string]map[string]any {
	t.Helper()
	st, out := op.do("GET", "/ui/admin/providers/"+provider+"/accounts", nil)
	if st != 200 {
		t.Fatalf("accounts of %s = %d %v", provider, st, out)
	}
	byName := map[string]map[string]any{}
	for _, a := range out.([]any) {
		m := asMap(a)
		byName[m["name"].(string)] = m
	}
	return byName
}

// A provider holds several accounts. One the provider refuses is rested and
// the next is tried at once, with no failover and no attempt used up; secrets
// are sealed and never read back; changing public fields keeps the secrets.
func TestProviderAccountsRotateAndRest(t *testing.T) {
	s := start(t)
	op := operator(t, s)
	put := func(name string, body map[string]any) {
		t.Helper()
		if st, out := op.do("PUT", "/ui/admin/providers/premium_np/accounts/"+name, body); st != 200 {
			t.Fatalf("put %s = %d %v", name, st, out)
		}
	}
	// "primary" now holds a token the vendor refuses; "second" holds a good one.
	put("primary", map[string]any{"secret": map[string]any{"token": "refused-token"}})
	put("second", map[string]any{"secret": map[string]any{"token": "sandbox-token"}, "note": "backup"})

	st, raw := op.do("GET", "/ui/admin/providers/premium_np/accounts", nil)
	if st != 200 || strings.Contains(fmt.Sprint(raw), "refused-token") || strings.Contains(fmt.Sprint(raw), "sandbox-token") {
		t.Fatalf("a secret came back from the API: %v", raw)
	}
	if got := accounts(t, op, "premium_np"); len(got) != 2 || got["second"]["has_secret"] != 1.0 {
		t.Fatalf("accounts = %v", got)
	}

	status, out := s.send("acme_bob", map[string]any{"to": "+9779841234567", "text": "rotate"})
	if status != 202 || str(out, "status") != "accepted" {
		t.Fatalf("send = %d %v", status, out)
	}
	id := str(out, "id")
	eventually(t, "delivered", func() bool { return s.state("acme_bob", id) == "delivered" })
	if p := s.carrier(id); p != "premium_np" {
		t.Fatalf("it left the provider: delivered via %q", p)
	}
	got := accounts(t, op, "premium_np")
	if got["primary"]["failures"] != 1.0 || got["primary"]["cooldown_until_ms"].(float64) == 0 {
		t.Fatalf("the refused account is not resting: %v", got["primary"])
	}
	if got["second"]["used"] != 1.0 {
		t.Fatalf("the good account was not used: %v", got["second"])
	}
	// The next message goes straight to the good account (the other is resting).
	_, out = s.send("acme_bob", map[string]any{"to": "+9779841234568", "text": "again"})
	id2 := str(out, "id")
	eventually(t, "second message delivered", func() bool { return s.state("acme_bob", id2) == "delivered" })
	if got := accounts(t, op, "premium_np"); got["second"]["used"] != 2.0 || got["primary"]["failures"] != 1.0 {
		t.Fatalf("after the second message: %v", got)
	}

	// Public fields change without touching the secret: the account still works.
	put("second", map[string]any{"public": map[string]any{"note": "x"}})
	_, out = s.send("acme_bob", map[string]any{"to": "+9779841234569", "text": "third"})
	id3 := str(out, "id")
	eventually(t, "still delivers after a public-only update", func() bool { return s.state("acme_bob", id3) == "delivered" })

	// With every account switched off the provider is skipped, not failed: the next one carries the message.
	put("second", map[string]any{"state": "disabled"})
	put("primary", map[string]any{"state": "disabled"})
	_, out = s.send("acme_bob", map[string]any{"to": "+9779841234560", "text": "no accounts"})
	id4 := str(out, "id")
	eventually(t, "delivered by the next provider", func() bool { return s.state("acme_bob", id4) == "delivered" })
	if p := s.carrier(id4); p == "premium_np" {
		t.Fatalf("a provider with no usable account carried a message")
	}
	// Clearing the rest period and switching it on again brings it back; an account can be removed.
	put("primary", map[string]any{"state": "active", "reset": true, "secret": map[string]any{"token": "sandbox-token"}})
	if got := accounts(t, op, "premium_np"); got["primary"]["failures"] != 0.0 || got["primary"]["cooldown_until_ms"] != 0.0 {
		t.Fatalf("reset: %v", got["primary"])
	}
	if st, _ := op.do("DELETE", "/ui/admin/providers/premium_np/accounts/second", nil); st != 200 {
		t.Fatalf("delete = %d", st)
	}
	if st, _ := op.do("DELETE", "/ui/admin/providers/premium_np/accounts/second", nil); st != 404 {
		t.Fatalf("delete twice = %d", st)
	}
	if st, _ := op.do("PUT", "/ui/admin/providers/nope/accounts/x", map[string]any{}); st != 404 {
		t.Fatalf("account of an unknown provider = %d", st)
	}
	if st, _ := s.browser().do("GET", "/ui/admin/providers/premium_np/accounts", nil); st != 401 {
		t.Fatalf("anonymous read = %d", st)
	}
}

// SMPP accounts bind separately: an account the carrier refuses at bind time is rested.
func TestSmppAccountsBindSeparately(t *testing.T) {
	s := start(t)
	op := operator(t, s)
	// "aaa_bad" sorts first, so it is tried first; the seeded "primary" is good.
	op.do("PUT", "/ui/admin/providers/np_telecom/accounts/aaa_bad", map[string]any{"public": map[string]any{"system_id": "wrong"}, "secret": map[string]any{"password": "wrong"}})
	_, out := s.send("demo", map[string]any{"to": "+9779841234567", "text": "bind"})
	id := str(out, "id")
	eventually(t, "delivered over the working account", func() bool { return s.state("demo", id) == "delivered" })
	if p := s.carrier(id); p != "np_telecom" {
		t.Fatalf("carrier = %s", p)
	}
	if got := accounts(t, op, "np_telecom"); got["aaa_bad"]["failures"] != 1.0 || got["primary"]["used"] != 1.0 {
		t.Fatalf("accounts = %v", got)
	}
}

// The API key and address of SMSPasal come from the provider's account, and a
// demo account can be given the provider by the operator: nothing is hard wired.
func TestAccountKeyAndAddressAreUsed(t *testing.T) {
	s := start(t)
	op := operator(t, s)
	demo := s.browser()
	demo.login("demo@example.com", "demo-pass-123")
	// Which carrier would carry it, and whether it is a sandbox, is the
	// operator's question. An account's own explain says nothing about carriers,
	// so it is asked as an operator here.
	explain := func() map[string]any {
		_, out := op.do("POST", "/ui/admin/route/explain", map[string]any{"account": "demo", "to": "+9779856034617", "text": "hi", "dlr": false})
		return asMap(out)
	}
	e := explain()
	if first := asMap(e["route"].([]any)[0])["id"]; first != "np_telecom" || e["sandbox"] != true {
		t.Fatalf("by default demo goes to the sandbox carrier: %v sandbox=%v", first, e["sandbox"])
	}
	// The operator gives demo the SMSPasal provider (without "dlr", as it has no receipts).
	if st, out := op.do("PUT", "/ui/admin/providers/smspasal", map[string]any{"channel": "smspasal", "countries": []string{"NP"}, "users": []string{"demo"}, "assigned_only": true, "supports_dlr": false, "quality": 90}); st != 200 {
		t.Fatalf("assign = %d %v", st, out)
	}
	if first := asMap(explain()["route"].([]any)[0])["id"]; first != "smspasal" {
		t.Fatalf("the assigned provider is not first: %v", first)
	}
	// With a wrong key in the account the vendor refuses it; with the right one it accepts.
	op.do("PUT", "/ui/admin/providers/smspasal/accounts/primary", map[string]any{"secret": map[string]any{"key": "wrong-key-wrong-key"}})
	_, out := demo.do("POST", "/ui/messages", map[string]any{"to": "+9779856034617", "text": "refused", "dlr": false})
	badID := str(asMap(out), "id")
	eventually(t, "fails over after the key is refused", func() bool { return s.carrier(badID) == "np_telecom" })
	op.do("PUT", "/ui/admin/providers/smspasal/accounts/primary", map[string]any{"secret": map[string]any{"key": "test-key-0001"}, "reset": true})
	_, out = demo.do("POST", "/ui/messages", map[string]any{"to": "+9779856034617", "text": "accepted", "dlr": false})
	goodID := str(asMap(out), "id")
	eventually(t, "accepted with the right key", func() bool {
		return s.state("demo", goodID) == "delivered" && s.carrier(goodID) == "smspasal"
	})
	if got := s.vendor.Messages(); len(got) == 0 || got[len(got)-1].Text != "accepted" || got[len(got)-1].To != "9856034617" {
		t.Fatalf("the vendor saw %+v", got)
	}
	// The provider's settings decide the address too: a closed port makes sends fail over.
	if st, out := op.do("PUT", "/ui/admin/providers/smspasal/settings", map[string]any{"values": map[string]any{"url": "http://127.0.0.1:1/smsapi/index.php"}}); st != 200 {
		t.Fatalf("settings = %d %v", st, out)
	}
	_, out = demo.do("POST", "/ui/messages", map[string]any{"to": "+9779856034617", "text": "nowhere", "dlr": false})
	nowhere := str(asMap(out), "id")
	eventually(t, "unreachable address does not deliver through smspasal", func() bool {
		return s.state("demo", nowhere) == "delivered" && s.carrier(nowhere) != "smspasal"
	})
}

// Sandbox is a property of a carrier, so it is shown to the operator and not to
// the account: an account that could tell a stand-in from a real carrier could
// tell which carriers the gateway has, which is the thing it is not told.
func TestSandboxLabelIsTheOperators(t *testing.T) {
	s := start(t)
	op := operator(t, s)
	demo := s.browser()
	demo.login("demo@example.com", "demo-pass-123")
	_, out := demo.do("POST", "/ui/messages", map[string]any{"to": "+9779841234567", "text": "labelled"})
	if _, leaked := asMap(out)["sandbox"]; leaked {
		t.Fatalf("the account was told which carriers are sandbox: %v", out)
	}
	eventually(t, "the message is delivered", func() bool {
		_, l := demo.do("GET", "/ui/messages", nil)
		rows, _ := l.([]any)
		return len(rows) == 1 && asMap(rows[0])["status"] == "delivered"
	})
	_, listed := demo.do("GET", "/ui/messages", nil)
	for _, r := range mustList(t, listed) {
		if _, leaked := asMap(r)["sandbox"]; leaked {
			t.Fatalf("the message list names a carrier's environment: %v", r)
		}
	}

	// The operator sees it on both the dry run and the message list.
	_, e := op.do("POST", "/ui/admin/route/explain", map[string]any{"account": "demo", "to": "+9779841234567", "text": "hi"})
	if asMap(e)["sandbox"] != true {
		t.Fatalf("the operator's dry run lost the sandbox label: %v", asMap(e))
	}

	// "np_telecom" goes live: the operator unticks Sandbox on the provider.
	if st, out := op.do("PUT", "/ui/admin/providers/np_telecom", map[string]any{"channel": "np_telecom", "kind": "smpp", "quality": 92, "delivery_rate": 0.97, "countries": []string{"NP"}, "sandbox": false, "max_attempts": 3, "backoff_initial_s": 1, "backoff_max_s": 15}); st != 200 {
		t.Fatalf("mark live = %d %v", st, out)
	}
	_, e = op.do("POST", "/ui/admin/route/explain", map[string]any{"account": "demo", "to": "+9779841234567", "text": "hi"})
	if asMap(e)["sandbox"] != false {
		t.Fatalf("after marking the channel live: %v", asMap(e))
	}
}

// mustList is a list endpoint's body, or a failure: a page of messages that did
// not come back as a list is a bug worth stopping on.
func mustList(t *testing.T, out any) []any {
	t.Helper()
	rows, ok := out.([]any)
	if !ok {
		t.Fatalf("expected a list, got %T: %v", out, out)
	}
	return rows
}

// Providers of different channels have different settings forms, and the
// settings are used: a sender id forced by the provider, an endpoint.
func TestProviderSettingsDifferByChannelAndAreUsed(t *testing.T) {
	s := start(t)
	op := operator(t, s)
	_, ch := op.do("GET", "/ui/admin/channels", nil)
	names := func(channel string) []string {
		for _, c := range ch.([]any) {
			m := asMap(c)
			if m["name"] == channel {
				var fs []map[string]any
				_ = json.Unmarshal([]byte(m["setting_fields"].(string)), &fs)
				var out []string
				for _, f := range fs {
					out = append(out, f["name"].(string))
				}
				return out
			}
		}
		return nil
	}
	if got := strings.Join(names("smspasal"), ","); got != "url,from_override" {
		t.Fatalf("smspasal settings = %q", got)
	}
	if got := strings.Join(names("premium_np"), ","); got != "endpoint,from_override" {
		t.Fatalf("vendor settings = %q", got)
	}
	// A sender id forced by a vendor provider reaches the vendor.
	op.do("PUT", "/ui/admin/providers/premium_np/settings", map[string]any{"values": map[string]any{"endpoint": "/send", "from_override": "ACMEBANK"}})
	bob := s.browser()
	_ = bob
	_, out := s.send("acme_bob", map[string]any{"to": "+9779841234567", "text": "forced sender", "from": "SOMETHING"})
	id := str(out, "id")
	eventually(t, "delivered", func() bool { return s.state("acme_bob", id) == "delivered" })
	got := s.vendor.Messages()
	if len(got) == 0 || got[len(got)-1].From != "ACMEBANK" {
		t.Fatalf("the vendor saw %+v, want sender ACMEBANK", got)
	}
	// The endpoint is used: a path the vendor does not know is answered 404, which the delivery
	// rules treat as a rejected request, so the message fails and its money is released.
	before, _ := s.balance("acme_bob")
	op.do("PUT", "/ui/admin/providers/premium_np/settings", map[string]any{"values": map[string]any{"endpoint": "/no-such-path"}})
	_, out = s.send("acme_bob", map[string]any{"to": "+9779841234568", "text": "wrong endpoint"})
	id2 := str(out, "id")
	eventually(t, "rejected", func() bool { return s.state("acme_bob", id2) == "failed" })
	if after, held := s.balance("acme_bob"); held != 0 || after != before {
		t.Fatalf("balance %v -> %v held %v: a rejected message must cost nothing", before, after, held)
	}
	if st, _ := op.do("PUT", "/ui/admin/providers/nope/settings", map[string]any{"values": map[string]any{}}); st != 404 {
		t.Fatalf("settings of an unknown provider = %d", st)
	}
}

// The rule tester is a form: the facts a definition reads are listed, with kinds and samples.
func TestRuleFactsAreListedForTheTester(t *testing.T) {
	s := start(t)
	op := operator(t, s)
	_, rule := op.do("GET", "/ui/admin/rules/validate", nil)
	kinds := map[string]string{}
	for _, f := range asMap(rule)["facts"].([]any) {
		m := asMap(f)
		kinds[m["path"].(string)] = m["kind"].(string)
	}
	if kinds["user.status"] != "text" || kinds["message.text_len"] != "number" || kinds["message.optout"] != "bool" {
		t.Fatalf("fact kinds = %v", kinds)
	}
}

// An operator tests an account's credentials: the real call is made with that
// account, nothing is routed or paid for, and the answer is the provider's own.
func TestOperatorTestsAnAccountsCredentials(t *testing.T) {
	s := start(t)
	op := operator(t, s)
	test := func(provider, account string) map[string]any {
		t.Helper()
		st, out := op.do("POST", "/ui/admin/providers/"+provider+"/accounts/"+account+"/test", map[string]any{"to": "+9779856034617", "text": "credential test"})
		if st != 200 {
			t.Fatalf("test %s/%s = %d %v", provider, account, st, out)
		}
		return asMap(out)
	}
	before, _ := s.balance("demo")
	got := test("smspasal", "primary")
	if got["ok"] != true || got["provider_message_id"] == "" || got["to"] != "9779856034617" || got["sandbox"] != true {
		t.Fatalf("good credentials: %v", got)
	}
	if v := s.vendor.Messages(); len(v) != 1 || v[0].Text != "credential test" || v[0].To != "9856034617" {
		t.Fatalf("the vendor saw %+v", v)
	}
	// A refused key is reported with the provider's words, and the account is not marked as failing.
	op.do("PUT", "/ui/admin/providers/smspasal/accounts/primary", map[string]any{"secret": map[string]any{"key": "wrong-key-wrong-key"}})
	bad := test("smspasal", "primary")
	if bad["ok"] != false || !strings.Contains(fmt.Sprint(bad["error"]), "INVALID API KEY") {
		t.Fatalf("bad credentials: %v", bad)
	}
	if acc := accounts(t, op, "smspasal")["primary"]; acc["failures"] != 0.0 || acc["cooldown_until_ms"] != 0.0 || acc["used"] != 0.0 {
		t.Fatalf("a test changed the account's counters: %v", acc)
	}
	// Every kind of channel can be tested: SMPP binds with the account, a vendor checks its token.
	if ok := test("np_telecom", "primary"); ok["ok"] != true {
		t.Fatalf("smpp: %v", ok)
	}
	op.do("PUT", "/ui/admin/providers/premium_np/accounts/primary", map[string]any{"secret": map[string]any{"token": "refused"}})
	if bad := test("premium_np", "primary"); bad["ok"] != false {
		t.Fatalf("vendor with a refused token: %v", bad)
	}
	after, held := s.balance("demo")
	if before != after || held != 0 {
		t.Fatalf("a test cost money: %v -> %v", before, after)
	}
	if st, _ := op.do("POST", "/ui/admin/providers/smspasal/accounts/ghost/test", map[string]any{"to": "+9779856034617"}); st != 404 {
		t.Fatalf("unknown account = %d", st)
	}
	if st, _ := s.browser().do("POST", "/ui/admin/providers/smspasal/accounts/primary/test", map[string]any{"to": "+9779856034617"}); st != 401 {
		t.Fatalf("anonymous = %d", st)
	}
}
