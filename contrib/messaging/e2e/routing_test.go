package e2e

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestRoutingByCountryAndAssignment(t *testing.T) {
	s := start(t)
	for _, c := range []struct{ user, to, provider string }{
		{"demo", "+9779841234567", "np_telecom"},     // country route
		{"acme_bob", "+9779841234567", "premium_np"}, // tenant assignment beats the country route
		{"acme_alice", "+919876543210", "in_vendor"}, // user assignment
		{"demo", "+919876543210", "global_fallback"}, // nothing assigned: the catch-all
	} {
		to := c.to
		status, out := s.send(c.user, map[string]any{"to": to, "text": "route me"})
		if status != 202 {
			t.Fatalf("%s -> %s: %d %v", c.user, to, status, out)
		}
		// Which carrier carried it is the operator's question, not the account's.
		if got := s.carrier(str(out, "id")); got != c.provider {
			t.Fatalf("%s -> %s: routed through %q, want %s", c.user, to, got, c.provider)
		}
	}
}

func TestOtpNeedsAQualityProvider(t *testing.T) {
	s := start(t)
	status, out := s.do("POST", "/v1/route/explain", s.key("demo"), map[string]any{"to": "+919876543210", "text": "code 1", "type": "otp"})
	if status != 422 && status != 503 {
		t.Fatalf("an OTP to a destination with only a low quality route = %d %v", status, out)
	}
}

func TestTransientFailureIsRetriedOnTheSameProvider(t *testing.T) {
	s := start(t)
	s.smsc.SetDown(true)
	status, out := s.send("demo", map[string]any{"to": "+9779841234567", "text": "retry me"})
	if status != 202 {
		t.Fatalf("send = %d %v", status, out)
	}
	id := str(out, "id")
	time.Sleep(1500 * time.Millisecond)
	s.smsc.SetDown(false)
	eventually(t, "delivered", func() bool { return s.state("demo", id) == "delivered" })
	if p := s.carrier(id); p != "np_telecom" {
		t.Fatalf("carrier = %s", p)
	}
	// How many times it was tried is the operator's to see, not the account's.
	_, full := s.admin("GET", "/v1/admin/messages/"+id, nil)
	if n, _ := get(asMap(full), "message", "total_attempts").(float64); n < 2 {
		t.Fatalf("attempts = %v, want at least 2", n)
	}
}

func TestBurstIsPaidForOnce(t *testing.T) {
	s := start(t)
	s.admin("POST", "/v1/admin/users/demo/topup", map[string]any{"amount": 100, "reference": "burst"})
	before, _ := s.balance("demo")
	s.key("demo") // issue before the goroutines: issuing rotates the key
	const n = 40
	ids := make([]string, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, out := s.send("demo", map[string]any{"to": "+9779841234567", "text": "burst", "reference": fmt.Sprintf("b%d", i)})
			ids[i] = str(out, "id")
		}(i)
	}
	wg.Wait()
	for _, id := range ids {
		id := id
		eventually(t, "delivered", func() bool { return id != "" && s.state("demo", id) == "delivered" })
	}
	after, held := s.balance("demo")
	spent := before - after
	if held != 0 || spent < n*0.0149 || spent > n*0.0151 || len(s.smsc.Submitted()) != n {
		t.Fatalf("spent %v held %v submissions %d", spent, held, len(s.smsc.Submitted()))
	}
}

func TestMessageSurvivesARestart(t *testing.T) {
	s := start(t)
	dir, smsc, vendor := s.dir, s.smsc, s.vendor
	status, out := s.send("demo", map[string]any{"to": "+9779841234567", "text": "later", "delay_seconds": 3})
	if status != 202 {
		t.Fatalf("send = %d %v", status, out)
	}
	id := str(out, "id")
	s.stop()
	s2 := start(t, opts{dir: dir, smsc: smsc, vendor: vendor, keepUpstreams: true})
	eventually(t, "delivered after restart", func() bool { return s2.state("demo", id) == "delivered" })
	if _, held := s2.balance("demo"); held != 0 || len(smsc.Submitted()) != 1 {
		t.Fatalf("held %v submissions %d", held, len(smsc.Submitted()))
	}
}
