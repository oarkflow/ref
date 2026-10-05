package e2e

import (
	"testing"
)

// state reads a message through its owner's API.
// state is the account's own status word for a message, from rules/status.bcl:
// accepted, sending, sent, delivered or failed. It is not the gateway's internal
// state, and it says nothing about which carrier carried it.
func (s *stack) state(user, id string) string {
	s.t.Helper()
	status, out := s.do("GET", "/v1/messages/"+id, s.key(user), nil)
	if status != 200 {
		return ""
	}
	return str(out, "status")
}

func (s *stack) balance(user string) (balance, held float64) {
	s.t.Helper()
	status, out := s.admin("GET", "/v1/admin/users/"+user+"/balance", nil)
	if status != 200 {
		s.t.Fatalf("balance %s = %d %v", user, status, out)
	}
	balance, _ = out["balance"].(float64)
	held, _ = out["held"].(float64)
	return
}

// provider reads which carrier carried a message. It asks as an operator,
// because an account is not told: /v1/messages/{id} has no provider field.
func (s *stack) provider(user, id string) string {
	s.t.Helper()
	return s.carrier(id)
}

func TestSmppDeliveryAndPayment(t *testing.T) {
	s := start(t)
	before, _ := s.balance("demo")
	status, out := s.send("demo", map[string]any{"to": "+977 984-123-4567", "text": "hello", "reference": "a1"})
	if status != 202 || str(out, "status") != "accepted" {
		t.Fatalf("send = %d %v", status, out)
	}
	id := str(out, "id")
	if got := s.provider("demo", id); got != "np_telecom" {
		t.Fatalf("routed through %q, want np_telecom", got)
	}
	eventually(t, "delivered", func() bool { return s.state("demo", id) == "delivered" })
	if n := len(s.smsc.Submitted()); n != 1 {
		t.Fatalf("SMSC submissions = %d", n)
	}
	after, held := s.balance("demo")
	if held != 0 || before-after < 0.0149 || before-after > 0.0151 {
		t.Fatalf("balance %v -> %v, held %v", before, after, held)
	}
}

func TestIdempotentReference(t *testing.T) {
	s := start(t)
	before, _ := s.balance("demo")
	var first string
	for i := 0; i < 3; i++ {
		status, out := s.send("demo", map[string]any{"to": "9841234567", "text": "once", "reference": "same"})
		if status != 202 {
			t.Fatalf("send %d = %d %v", i, status, out)
		}
		if i == 0 {
			first = str(out, "id")
		} else if str(out, "id") != first || out["duplicate"] != true {
			t.Fatalf("repeat %d = %v", i, out)
		}
	}
	if status, out := s.send("demo", map[string]any{"to": "9841234567", "text": "other", "reference": "same"}); status != 409 {
		t.Fatalf("reused reference = %d %v", status, out)
	}
	eventually(t, "delivered", func() bool { return s.state("demo", first) == "delivered" })
	after, _ := s.balance("demo")
	if before-after > 0.0151 || len(s.smsc.Submitted()) != 1 {
		t.Fatalf("charged %v, submissions %d", before-after, len(s.smsc.Submitted()))
	}
}

func TestInsufficientFunds(t *testing.T) {
	s := start(t)
	s.admin("PUT", "/v1/admin/users/poor", map[string]any{"name": "Poor"})
	status, out := s.send("poor", map[string]any{"to": "9841234567", "text": "hello"})
	if status != 409 || str(out, "error", "code") != "INSUFFICIENT_FUNDS" {
		t.Fatalf("send = %d %v", status, out)
	}
}

func TestVendorProviderAndWebhook(t *testing.T) {
	s := start(t)
	status, out := s.send("acme_bob", map[string]any{"to": "9841234567", "text": "via vendor"})
	if status != 202 {
		t.Fatalf("send = %d %v", status, out)
	}
	id := str(out, "id")
	if got := s.provider("acme_bob", id); got != "premium_np" {
		t.Fatalf("routed through %q, want premium_np", got)
	}
	eventually(t, "delivered by webhook", func() bool { return s.state("acme_bob", id) == "delivered" })
	if len(s.vendor.Messages()) != 1 {
		t.Fatalf("vendor messages = %d", len(s.vendor.Messages()))
	}
	if status, _ := s.do("POST", "/v1/webhooks/dlr/premium", "wrong", map[string]any{"id": "x", "status": "DELIVRD"}); status != 401 {
		t.Fatalf("bad webhook secret = %d", status)
	}
}

func TestFailoverWhenProviderIsDown(t *testing.T) {
	s := start(t)
	s.vendor.FailWith(500)
	status, out := s.send("acme_bob", map[string]any{"to": "9841234567", "text": "needs failover"})
	if status != 202 {
		t.Fatalf("send = %d %v", status, out)
	}
	id := str(out, "id")
	eventually(t, "delivered", func() bool { return s.state("acme_bob", id) == "delivered" })
	if p := s.provider("acme_bob", id); p != "np_telecom" {
		t.Fatalf("delivered via %q, want np_telecom", p)
	}
	_, held := s.balance("acme_bob")
	if held != 0 {
		t.Fatalf("held = %v", held)
	}
}

func TestPermanentFailureReleasesFunds(t *testing.T) {
	s := start(t)
	s.smsc.SetRejectPrefixes("97798")
	before, _ := s.balance("demo")
	status, out := s.send("demo", map[string]any{"to": "9841234567", "text": "rejected"})
	if status != 202 {
		t.Fatalf("send = %d %v", status, out)
	}
	id := str(out, "id")
	eventually(t, "failed", func() bool { return s.state("demo", id) == "failed" })
	after, held := s.balance("demo")
	if after != before || held != 0 {
		t.Fatalf("balance %v -> %v held %v: a failed message must not cost anything", before, after, held)
	}
}

func TestAccountIsolation(t *testing.T) {
	s := start(t)
	_, out := s.send("demo", map[string]any{"to": "9841234567", "text": "private"})
	id := str(out, "id")
	if status, _ := s.do("GET", "/v1/messages/"+id, s.key("acme_alice"), nil); status != 404 {
		t.Fatalf("another account read the message: %d", status)
	}
	if status, _ := s.do("GET", "/v1/messages/"+id, "", nil); status != 401 {
		t.Fatalf("anonymous read = %d", status)
	}
}

// SMSPasal-style provider: a GET text API answering in plain text.
func TestSmspasalProvider(t *testing.T) {
	s := start(t)
	status, out := s.send("smspasal_live", map[string]any{"to": "+9779856034617", "text": "Hello from the gateway", "from": "TESTER", "dlr": false})
	if status != 202 || out["receipt"] != false {
		t.Fatalf("send = %d %v", status, out)
	}
	id := str(out, "id")
	if got := s.provider("smspasal_live", id); got != "smspasal" {
		t.Fatalf("routed through %q, want smspasal", got)
	}
	eventually(t, "accepted by smspasal", func() bool { return s.state("smspasal_live", id) == "delivered" })
	got := s.vendor.Messages()
	if len(got) != 1 || got[0].To != "9856034617" || got[0].From != "TESTER" || got[0].Text != "Hello from the gateway" {
		t.Fatalf("vendor saw %+v (the number must be national, without 977)", got)
	}
	if b, held := s.balance("smspasal_live"); held != 0 || b > 0.9881 {
		t.Fatalf("balance %v held %v", b, held)
	}
}

// An ERR answer is classified by rules: a refusal by the provider (here a wrong
// key in its only account) fails over without retrying, and the message is
// charged once, at the price quoted.
func TestSmspasalErrorsAreClassified(t *testing.T) {
	s := start(t)
	op := operator(t, s)
	if st, out := op.do("PUT", "/ui/admin/providers/smspasal/accounts/primary", map[string]any{"secret": map[string]any{"key": "wrong-key-wrong-key"}, "public": map[string]any{"routeid": "10259", "campaign": "9801"}}); st != 200 {
		t.Fatalf("set key = %d %v", st, out)
	}
	before, _ := s.balance("smspasal_live")
	status, out := s.send("smspasal_live", map[string]any{"to": "+9779856034617", "text": "x", "from": "TESTER", "dlr": false})
	if status != 202 {
		t.Fatalf("send = %d %v", status, out)
	}
	id := str(out, "id")
	eventually(t, "delivered", func() bool { return s.state("smspasal_live", id) == "delivered" })
	if p := s.carrier(id); p != "np_telecom" {
		t.Fatalf("delivered via %q, want np_telecom", p)
	}
	// The attempt count is the operator's to see, not the account's.
	_, full := s.admin("GET", "/v1/admin/messages/"+id, nil)
	if a, _ := get(asMap(full), "message", "total_attempts").(float64); a != 2 {
		t.Fatalf("attempts = %v, want 2 (one refused, no retry)", a)
	}
	after, held := s.balance("smspasal_live")
	if held != 0 || before-after > 0.0151 || before-after < 0.0149 {
		t.Fatalf("balance %v -> %v held %v", before, after, held)
	}
}
