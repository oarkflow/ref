package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"sync"
	"testing"
	"time"
)

func TestLoadsTheBCL(t *testing.T) {
	s := startStack(t, nil)
	names := map[string]bool{}
	for _, h := range s.app.Hub.Health(context.Background()) {
		names[h.Name] = true
	}
	for _, want := range []string{"np_telecom", "premium_np", "in_vendor", "global_fallback"} {
		if !names[want] {
			t.Errorf("provider %s is declared in the BCL but not running", want)
		}
	}
	if status, out := s.do("GET", "/healthz", "", nil); status != 200 || out["status"] != "ok" {
		t.Fatalf("healthz = %d %v", status, out)
	}
}

func TestSendOverSMPPThroughTheBCLPipeline(t *testing.T) {
	s := startStack(t, nil)
	status, out := s.send("demo", map[string]any{"to": "+977 9841234567", "text": "Your code is 482913", "type": "otp"})
	if status != 202 {
		t.Fatalf("send: %d %v", status, out)
	}
	id, _ := out["id"].(string)
	if out["provider"] != "np_telecom" || out["country"] != "NP" || out["price"] != 0.03 || out["duplicate"] != false {
		t.Fatalf("accepted = %v", out)
	}
	msg := s.waitMessage("demo", id, "delivered")
	if msg["provider"] != "np_telecom" {
		t.Fatalf("delivered by %v", msg["provider"])
	}
	if msg["error"] != nil {
		t.Fatalf("a delivered message carries no error, got %v", msg["error"])
	}
	got := s.smsc.Submitted()
	if len(got) != 1 || got[0].To != "9779841234567" || got[0].Text != "Your code is 482913" || got[0].From != "DEMO" {
		t.Fatalf("the SMSC received %+v", got)
	}
	bal, held := s.balance("demo")
	if bal != 24.97 || held != 0 {
		t.Fatalf("balance = %v held = %v; an OTP costs 0.03 of the 25.00 opening balance", bal, held)
	}
	_, ledger := s.do("GET", "/v1/ledger", s.key("demo"), nil)
	kinds := map[string]int{}
	for _, e := range ledger["entries"].([]any) {
		kinds[e.(map[string]any)["kind"].(string)]++
	}
	if kinds["hold"] != 1 || kinds["capture"] != 1 || kinds["release"] != 0 {
		t.Fatalf("ledger kinds = %v, want one hold and one capture", kinds)
	}
}

func TestRejectedRequests(t *testing.T) {
	s := startStack(t, nil)
	key := s.key("demo")
	cases := []struct {
		name   string
		key    string
		body   map[string]any
		status int
		code   string
	}{
		{"no credentials", "", map[string]any{"to": "9841234567", "text": "hi"}, 401, ""},
		{"wrong key", "smsk_nope", map[string]any{"to": "9841234567", "text": "hi"}, 401, ""},
		{"missing text", key, map[string]any{"to": "9841234567"}, 422, ""},
		{"unknown field", key, map[string]any{"to": "9841234567", "text": "hi", "colour": "red"}, 422, ""},
		{"bad type", key, map[string]any{"to": "9841234567", "text": "hi", "type": "spam"}, 422, ""},
		{"bad number", key, map[string]any{"to": "12345", "text": "hi"}, 422, "INVALID_NUMBER"},
		{"unroutable country code", key, map[string]any{"to": "+999123456789", "text": "hi"}, 422, "INVALID_NUMBER"},
		{"bad sender", key, map[string]any{"to": "9841234567", "text": "hi", "from": "bad;sender"}, 422, ""},
	}
	for _, c := range cases {
		status, out := s.do("POST", "/v1/messages", c.key, c.body)
		if status != c.status || (c.code != "" && errCode(out) != c.code) {
			t.Errorf("%s: %d %v, want %d %s", c.name, status, out, c.status, c.code)
		}
	}
	if got := s.smsc.Submitted(); len(got) != 0 {
		t.Fatalf("a rejected request reached the SMSC: %+v", got)
	}
	if bal, held := s.balance("demo"); bal != 25 || held != 0 {
		t.Fatalf("rejected requests must cost nothing: %v/%v", bal, held)
	}
}

func TestSameReferenceDeliversAndChargesOnce(t *testing.T) {
	s := startStack(t, nil)
	body := map[string]any{"to": "9841234567", "text": "invoice 1001 paid", "reference": "inv-1001"}
	status, first := s.send("demo", body)
	if status != 202 {
		t.Fatalf("%d %v", status, first)
	}
	for range 3 {
		status, again := s.send("demo", body)
		if status != 202 || again["id"] != first["id"] || again["duplicate"] != true {
			t.Fatalf("replay = %d %v", status, again)
		}
	}
	s.waitMessage("demo", first["id"].(string), "delivered")
	if n := len(s.smsc.Submitted()); n != 1 {
		t.Fatalf("the SMSC saw %d submissions", n)
	}
	if bal, _ := s.balance("demo"); bal != 24.985 {
		t.Fatalf("balance = %v, want one Nepal message at 0.015", bal)
	}
	status, conflict := s.send("demo", map[string]any{"to": "9841234567", "text": "another text", "reference": "inv-1001"})
	if status != 409 || errCode(conflict) != "REFERENCE_REUSED" {
		t.Fatalf("a reused reference with new content: %d %v", status, conflict)
	}
}

func TestInsufficientFundsIsRefusedBeforeAnythingIsSent(t *testing.T) {
	s := startStack(t, nil)
	s.admin("PUT", "/v1/admin/users/broke", map[string]any{"name": "Broke", "api_key": "smsk_broke_0123456789abcdef"})
	s.keys["broke"] = "smsk_broke_0123456789abcdef"
	if status, out := s.admin("POST", "/v1/admin/users/broke/topup", map[string]any{"amount": 0.01, "reference": "t1"}); status != 200 {
		t.Fatalf("topup: %d %v", status, out)
	}
	status, out := s.send("broke", map[string]any{"to": "9841234567", "text": "hi"})
	if status != 409 || errCode(out) != "INSUFFICIENT_FUNDS" {
		t.Fatalf("send = %d %v", status, out)
	}
	if len(s.smsc.Submitted()) != 0 {
		t.Fatal("an unpaid message was sent")
	}
	if bal, held := s.balance("broke"); bal != 0.01 || held != 0 {
		t.Fatalf("balance = %v/%v", bal, held)
	}
}

func TestRoutingByUserTenantAndCountry(t *testing.T) {
	s := startStack(t, nil)

	// A platform user in Nepal: the country provider.
	_, demo := s.send("demo", map[string]any{"to": "9841234567", "text": "a"})
	if demo["provider"] != "np_telecom" {
		t.Errorf("demo -> Nepal went to %v", demo["provider"])
	}
	// ACME's tenant has the premium Nepal route, with the country provider as
	// the fallback.
	_, bob := s.send("acme_bob", map[string]any{"to": "9841234567", "text": "b"})
	if bob["provider"] != "premium_np" {
		t.Errorf("acme_bob -> Nepal went to %v", bob["provider"])
	}
	if route, _ := bob["route"].([]any); len(route) != 3 || route[1] != "np_telecom" {
		t.Errorf("acme_bob's chain = %v", bob["route"])
	}
	// India: Alice is assigned the vendor; everyone else gets the catch-all.
	_, alice := s.send("acme_alice", map[string]any{"to": "+919876543210", "text": "c"})
	if alice["provider"] != "in_vendor" {
		t.Errorf("acme_alice -> India went to %v", alice["provider"])
	}
	_, bobIn := s.send("acme_bob", map[string]any{"to": "+919876543210", "text": "d"})
	if bobIn["provider"] != "global_fallback" {
		t.Errorf("acme_bob -> India went to %v (the vendor is Alice's)", bobIn["provider"])
	}
	// A user's own rate (0.012 for Nepal) applies only to her.
	if _, alice2 := s.send("acme_alice", map[string]any{"to": "9841234567", "text": "e"}); alice2["price"] != 0.012 {
		t.Errorf("acme_alice's Nepal price = %v", alice2["price"])
	}

	for _, m := range []struct {
		user string
		resp map[string]any
	}{{"demo", demo}, {"acme_bob", bob}, {"acme_alice", alice}, {"acme_bob", bobIn}} {
		s.waitMessage(m.user, m.resp["id"].(string), "delivered")
	}
	// The India vendor really was called over HTTP, with its own token, and its
	// receipt came back through the webhook.
	got := s.vendor.Messages()
	if len(got) != 1 || got[0].To != "919876543210" || got[0].Reference != alice["id"] {
		t.Fatalf("the vendor received %+v", got)
	}
}

func TestExplainShowsWhyAProviderWasChosen(t *testing.T) {
	s := startStack(t, nil)
	status, out := s.do("POST", "/v1/route/explain", s.key("acme_bob"), map[string]any{"to": "9841234567", "text": "x", "type": "otp"})
	if status != 200 {
		t.Fatalf("%d %v", status, out)
	}
	if out["objective"] != "highest_delivery" {
		t.Fatalf("an OTP is routed for delivery, got %v", out["objective"])
	}
	cands, _ := out["candidates"].([]any)
	tiers := map[string]string{}
	for _, c := range cands {
		m := c.(map[string]any)
		tiers[m["provider"].(string)] = m["tier"].(string)
	}
	if tiers["premium_np"] != "tenant" || tiers["np_telecom"] != "country" || tiers["global_fallback"] != "platform" {
		t.Fatalf("tiers = %v", tiers)
	}
	if len(s.smsc.Submitted()) != 0 {
		t.Fatal("explain must not send")
	}
}

func TestAccountsSeeOnlyTheirOwnMessages(t *testing.T) {
	s := startStack(t, nil)
	_, out := s.send("demo", map[string]any{"to": "9841234567", "text": "private"})
	id := out["id"].(string)
	if status, _ := s.do("GET", "/v1/messages/"+id, s.key("acme_bob"), nil); status != 404 {
		t.Fatalf("another account's message must be 404, got %d", status)
	}
	status, list := s.do("GET", "/v1/messages", s.key("acme_bob"), nil)
	if status != 200 || list["count"] != float64(0) {
		t.Fatalf("bob's list = %d %v", status, list)
	}
	if status, _ := s.do("GET", "/v1/admin/stats", s.key("demo"), nil); status != 401 && status != 403 {
		t.Fatalf("an account key must not open the admin API, got %d", status)
	}
	if status, _ := s.do("GET", "/v1/admin/stats", "", nil); status != 401 {
		t.Fatalf("no key = %d", status)
	}
}

func TestFailsOverWhenTheUpstreamRefusesTheProvider(t *testing.T) {
	s := startStack(t, nil)
	s.smsc.Password = "rotated" // the bind will be refused: a provider fault
	_, out := s.send("demo", map[string]any{"to": "9841234567", "text": "hello", "reference": "fo-1"})
	id := out["id"].(string)
	msg := s.waitMessage("demo", id, "delivered", "submitted")
	if msg["provider"] != "global_fallback" {
		t.Fatalf("delivered by %v, want the fallback", msg["provider"])
	}
	log, _ := msg["attempt_log"].([]any)
	if len(log) != 2 || log[0].(map[string]any)["outcome"] != "failover" || log[1].(map[string]any)["outcome"] != "accepted" {
		t.Fatalf("attempt log = %v", log)
	}
	if bal, held := s.balance("demo"); bal != 24.985 || held != 0 {
		t.Fatalf("balance = %v/%v: one charge at the user's price, whichever provider carried it", bal, held)
	}
	if len(s.smsc.Submitted()) != 0 {
		t.Fatal("the refused provider received the message")
	}
	// The failure feeds routing: the provider's health records it.
	_, stats := s.admin("GET", "/v1/admin/stats", nil)
	var failed float64
	for _, p := range stats["providers"].([]any) {
		if m := p.(map[string]any); m["name"] == "np_telecom" {
			failed, _ = m["failed"].(float64)
		}
	}
	if failed < 1 {
		t.Fatalf("np_telecom's failure was not recorded: %v", stats["providers"])
	}
}

func TestRetriesTheProviderThenFailsOver(t *testing.T) {
	s := startStack(t, nil)
	s.smsc.SetDown(true) // submit_sm answers ESME_RSYSERR: retryable
	_, out := s.send("demo", map[string]any{"to": "9841234567", "text": "hello"})
	msg := s.waitMessage("demo", out["id"].(string), "delivered", "submitted")
	if msg["provider"] != "global_fallback" {
		t.Fatalf("provider = %v", msg["provider"])
	}
	// np_telecom's retry policy is three attempts, then the next provider.
	log, _ := msg["attempt_log"].([]any)
	var retries int
	for _, l := range log {
		if l.(map[string]any)["outcome"] == "retry" {
			retries++
		}
	}
	if retries != 2 || msg["attempts"] != float64(4) {
		t.Fatalf("retries=%d attempts=%v log=%v; want 2 retries and 4 attempts in all", retries, msg["attempts"], log)
	}
	if bal, held := s.balance("demo"); bal != 24.985 || held != 0 {
		t.Fatalf("balance = %v/%v", bal, held)
	}
}

func TestPermanentFailureStopsAndCostsNothing(t *testing.T) {
	s := startStack(t, nil)
	s.smsc.SetRejectPrefixes("977984")
	_, out := s.send("demo", map[string]any{"to": "9841234567", "text": "hello"})
	msg := s.waitMessage("demo", out["id"].(string), "failed")
	if str(msg, "error", "code") != "invalid_destination_address" {
		t.Fatalf("error = %v", msg["error"])
	}
	if msg["payment"] != "released" {
		t.Fatalf("payment = %v", msg["payment"])
	}
	if bal, held := s.balance("demo"); bal != 25 || held != 0 {
		t.Fatalf("a failed message must cost nothing: %v/%v", bal, held)
	}
	for _, h := range s.app.Hub.Health(context.Background()) {
		if h.Name == "global_fallback" && h.Sent != 0 {
			t.Fatal("a permanently invalid destination was tried on another provider")
		}
	}
}

func TestReceiptWebhookIsAuthenticated(t *testing.T) {
	s := startStack(t, nil)
	post := func(path, secret string, body map[string]any) int {
		raw, _ := json.Marshal(body)
		req, _ := http.NewRequest("POST", s.base+path, bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		if secret != "" {
			req.Header.Set("X-Webhook-Secret", secret)
		}
		resp, err := s.client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}
	rcpt := map[string]any{"id": "vnd-x", "status": "DELIVRD"}
	if st := post("/v1/webhooks/dlr/in_vendor", "", rcpt); st != 401 {
		t.Errorf("no secret = %d", st)
	}
	if st := post("/v1/webhooks/dlr/in_vendor", "wrong", rcpt); st != 401 {
		t.Errorf("wrong secret = %d", st)
	}
	if st := post("/v1/webhooks/dlr/nobody", "x", rcpt); st != 404 {
		t.Errorf("unknown provider = %d", st)
	}
	if st := post("/v1/webhooks/dlr/np_telecom", "x", rcpt); st != 404 {
		t.Errorf("a provider without a webhook = %d", st)
	}
	if st := post("/v1/webhooks/dlr/in_vendor", "sandbox-secret", rcpt); st != 202 {
		t.Errorf("valid receipt = %d", st)
	}
}

func TestRuntimeConfigurationReachesTheRouter(t *testing.T) {
	s := startStack(t, nil)

	// A new account, created and funded through the admin API.
	status, out := s.admin("PUT", "/v1/admin/users/newco", map[string]any{"name": "NewCo", "tenant": "newco", "default_sender": "NEWCO"})
	if status != 200 {
		t.Fatalf("create user: %d %v", status, out)
	}
	if _, out := s.admin("POST", "/v1/admin/users/newco/topup", map[string]any{"amount": 5, "reference": "pay-1"}); out["applied"] != true {
		t.Fatalf("topup = %v", out)
	}
	// Replaying the payment credits nothing.
	if _, out := s.admin("POST", "/v1/admin/users/newco/topup", map[string]any{"amount": 5, "reference": "pay-1"}); out["applied"] != false || out["balance"] != 5.0 {
		t.Fatalf("replayed topup = %v", out)
	}

	// Before it has a provider of its own, NewCo uses the platform's.
	if _, out := s.do("POST", "/v1/route/explain", s.key("newco"), map[string]any{"to": "9841234567", "text": "x"}); out["route"].([]any)[0].(map[string]any)["provider"] != "np_telecom" {
		t.Fatalf("before: %v", out["route"])
	}

	// NewCo brings its own gateway, created at runtime. It is built (and would
	// be refused if it could not be) before anything is stored.
	status, out = s.admin("PUT", "/v1/admin/providers/newco_gw", map[string]any{
		"kind": "mock", "owner": "newco",
		"config": map[string]any{"countries": []string{"NP"}, "cost_per_segment": 0.005, "quality": 90, "plugin": map[string]any{"dlr": "delivered", "dlr_delay": "20ms"}},
	})
	if status != 200 {
		t.Fatalf("create provider: %d %v", status, out)
	}
	// Every node applies the change through its configuration consumer.
	eventually(t, "the router to learn the new provider", func() bool {
		_, out := s.do("POST", "/v1/route/explain", s.key("newco"), map[string]any{"to": "9841234567", "text": "x"})
		route, _ := out["route"].([]any)
		return len(route) == 1 && route[0].(map[string]any)["provider"] == "newco_gw"
	})
	_, sent := s.send("newco", map[string]any{"to": "9841234567", "text": "own route"})
	if sent["provider"] != "newco_gw" {
		t.Fatalf("sent via %v", sent["provider"])
	}
	s.waitMessage("newco", sent["id"].(string), "delivered")
	if len(s.smsc.Submitted()) != 0 {
		t.Fatal("a user-owned provider's traffic leaked to the platform's")
	}
	// Nobody else can use it.
	if _, out := s.do("POST", "/v1/route/explain", s.key("demo"), map[string]any{"to": "9841234567", "text": "x"}); out["route"].([]any)[0].(map[string]any)["provider"] != "np_telecom" {
		t.Fatalf("demo's route = %v", out["route"])
	}

	// Removing the provider returns NewCo to the platform.
	if status, _ := s.admin("DELETE", "/v1/admin/providers/newco_gw", nil); status != 200 {
		t.Fatalf("delete provider = %d", status)
	}
	eventually(t, "the router to forget it", func() bool {
		_, out := s.do("POST", "/v1/route/explain", s.key("newco"), map[string]any{"to": "9841234567", "text": "x"})
		route, _ := out["route"].([]any)
		return len(route) > 0 && route[0].(map[string]any)["provider"] == "np_telecom"
	})

	// A BCL-declared provider cannot be changed from the API.
	if status, out := s.admin("DELETE", "/v1/admin/providers/np_telecom", nil); status != 422 {
		t.Fatalf("deleting a BCL provider = %d %v", status, out)
	}
	// A provider that cannot be built is refused, and nothing is stored.
	if status, _ := s.admin("PUT", "/v1/admin/providers/broken", map[string]any{"kind": "http", "config": map[string]any{"plugin": map[string]any{"url": "ftp://x"}}}); status != 422 {
		t.Fatalf("a broken provider = %d", status)
	}
	// A private-network vendor is refused unless explicitly allowed.
	status, _ = s.admin("PUT", "/v1/admin/providers/ssrf", map[string]any{"kind": "http", "config": map[string]any{"countries": []string{"NP"}, "plugin": map[string]any{"url": "http://127.0.0.1:1/send", "allow_private_networks": "false"}}})
	if status != 200 {
		t.Fatalf("creating the SSRF-guarded provider = %d", status)
	}
	s.admin("POST", "/v1/admin/providers/ssrf/state", map[string]any{"state": "active"})
	_, out = s.admin("GET", "/v1/admin/providers", nil)
	if len(out["providers"].([]any)) != 5 {
		t.Fatalf("providers = %v", out["providers"])
	}
}

func TestOptOutAndSuspension(t *testing.T) {
	s := startStack(t, nil)
	if status, out := s.admin("POST", "/v1/admin/optouts", map[string]any{"number": "+977 9841234567", "reason": "asked to stop"}); status != 200 {
		t.Fatalf("optout: %d %v", status, out)
	}
	status, out := s.send("demo", map[string]any{"to": "9841234567", "text": "hi"})
	if status != 403 || errCode(out) != "OPTED_OUT" {
		t.Fatalf("send to an opted-out number = %d %v", status, out)
	}
	s.admin("DELETE", "/v1/admin/optouts", map[string]any{"number": "9841234567"})
	if status, _ := s.send("demo", map[string]any{"to": "9841234567", "text": "hi"}); status != 202 {
		t.Fatalf("after the opt-out is lifted = %d", status)
	}

	s.admin("PUT", "/v1/admin/users/demo", map[string]any{"status": "suspended"})
	eventually(t, "the suspension", func() bool {
		st, o := s.send("demo", map[string]any{"to": "9841234567", "text": "hi"})
		return st == 403 && errCode(o) == "ACCOUNT_SUSPENDED"
	})
	// The key survived the update: a suspended account is refused, not unknown.
	if st, _ := s.do("GET", "/v1/balance", s.key("demo"), nil); st != 200 {
		t.Fatalf("a suspended account can still read its balance, got %d", st)
	}
}

func TestQueuedWorkSurvivesARestart(t *testing.T) {
	s1 := startStackWith(t, stackOpts{keepUpstreams: true})
	// A message scheduled a little ahead is accepted and paid for, then the
	// whole application stops before it is due.
	at := time.Now().Add(1500 * time.Millisecond).UTC().Format(time.RFC3339Nano)
	status, out := s1.send("demo", map[string]any{"to": "9841234567", "text": "see you after the restart", "schedule_at": at})
	if status != 202 {
		t.Fatalf("send = %d %v", status, out)
	}
	id := out["id"].(string)
	key := s1.key("demo")
	s1.shutdown()

	s2 := startStackWith(t, stackOpts{dir: s1.dir, smsc: s1.smsc, vendor: s1.vendor})
	s2.keys["demo"] = key // keys are stored hashed and survive the restart
	msg := s2.waitMessage("demo", id, "delivered")
	if msg["provider"] != "np_telecom" {
		t.Fatalf("provider = %v", msg["provider"])
	}
	if n := len(s1.smsc.Submitted()); n != 1 {
		t.Fatalf("the SMSC saw %d submissions", n)
	}
	if bal, held := s2.balance("demo"); bal != 24.985 || held != 0 {
		t.Fatalf("balance after the restart = %v/%v", bal, held)
	}
	// Seeds never overwrite what the API changed: the opening balance was
	// credited once, ever.
	if _, out := s2.do("GET", "/v1/ledger", key, nil); len(out["entries"].([]any)) < 3 {
		t.Fatalf("ledger = %v", out)
	}
}

func TestAddedStageGuardsThePipeline(t *testing.T) {
	s := startStack(t, nil)
	// The content policy applies to promotions only, and is a node in the BCL.
	status, out := s.send("demo", map[string]any{"to": "9841234567", "text": "You have won the LOTTERY!", "type": "promotional"})
	if status != 422 || errCode(out) != "CONTENT_BLOCKED" {
		t.Fatalf("a blocked promotion = %d %v", status, out)
	}
	if bal, held := s.balance("demo"); bal != 25 || held != 0 {
		t.Fatalf("a blocked message must cost nothing: %v/%v", bal, held)
	}
	if status, out := s.send("demo", map[string]any{"to": "9841234567", "text": "Our lottery of ideas", "type": "transactional"}); status != 202 {
		t.Fatalf("the policy must not touch other types: %d %v", status, out)
	}
	if status, out := s.send("demo", map[string]any{"to": "9841234567", "text": "Spring sale: 20% off", "type": "promotional"}); status != 202 {
		t.Fatalf("a clean promotion = %d %v", status, out)
	}
}

func TestBurstOfMessagesIsDeliveredOnceAndBilledExactly(t *testing.T) {
	s := startStack(t, nil)
	const perUser = 60
	users := []string{"demo", "acme_bob"}
	type sent struct{ user, id string }
	var (
		mu  sync.Mutex
		all []sent
		wg  sync.WaitGroup
	)
	for _, u := range users {
		s.key(u) // issue keys up front: issuing one replaces the last
	}
	start := time.Now()
	for _, u := range users {
		for i := range perUser {
			wg.Add(1)
			go func() {
				defer wg.Done()
				body := map[string]any{"to": fmt.Sprintf("98412%05d", i), "text": fmt.Sprintf("burst %d", i), "reference": fmt.Sprintf("%s-%d", u, i)}
				if status, out := s.send(u, body); status != 202 {
					t.Errorf("send %s/%d: %d %v", u, i, status, out)
				} else {
					mu.Lock()
					all = append(all, sent{u, out["id"].(string)})
					mu.Unlock()
				}
			}()
		}
	}
	wg.Wait()
	accepted := time.Since(start)
	for _, m := range all {
		s.waitMessage(m.user, m.id, "delivered")
	}
	t.Logf("%d messages accepted in %v and delivered in %v", len(all), accepted.Round(time.Millisecond), time.Since(start).Round(time.Millisecond))

	// demo routes to the SMSC (np_telecom); acme_bob to the premium mock.
	if got := len(s.smsc.Submitted()); got != perUser {
		t.Fatalf("the SMSC saw %d messages for %d sent", got, perUser)
	}
	seen := map[string]int{}
	for _, m := range s.smsc.Submitted() {
		seen[m.To]++
	}
	for to, n := range seen {
		if n != 1 {
			t.Fatalf("%s was delivered %d times", to, n)
		}
	}
	if bal, held := s.balance("demo"); math.Abs(bal-(25-perUser*0.015)) > 1e-9 || held != 0 {
		t.Fatalf("demo balance = %v/%v, want %v", bal, held, 25-perUser*0.015)
	}
	if bal, held := s.balance("acme_bob"); math.Abs(bal-(10-perUser*0.015)) > 1e-9 || held != 0 {
		t.Fatalf("bob balance = %v/%v, want %v", bal, held, 10-perUser*0.015)
	}
}
