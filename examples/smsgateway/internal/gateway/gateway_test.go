package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/oarkflow/ref/examples/smsgateway/internal/sandbox"
)

func msg() Message {
	return Message{ID: "msg_1", From: "ACME", To: "9779841234567", Text: "hello", Segments: 1, Country: "NP", WantDLR: true}
}

func openHTTPGw(t *testing.T, url string, extra map[string]any) Gateway {
	t.Helper()
	plugin := map[string]any{"url": url, "allow_private_networks": true, "id_path": "data.id", "error_path": "error.code"}
	for k, v := range extra {
		plugin[k] = v
	}
	g, err := Open(context.Background(), "http", "vendor", plugin)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = g.Close() })
	return g
}

func classOf(err error) Class {
	c, _ := ClassOf(err)
	return c
}

func TestHTTPSendExtractsTheProviderID(t *testing.T) {
	var got map[string]any
	var auth, idem string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth, idem = r.Header.Get("Authorization"), r.Header.Get("Idempotency-Key")
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"data":{"id":"v-77"}}`))
	}))
	defer srv.Close()
	g := openHTTPGw(t, srv.URL, map[string]any{"auth": map[string]any{"type": "bearer", "token": "tok"}})
	rec, err := g.Send(context.Background(), msg())
	if err != nil || rec.ProviderMessageID != "v-77" {
		t.Fatalf("send = %+v, %v", rec, err)
	}
	if auth != "Bearer tok" || idem != "msg_1" {
		t.Fatalf("auth=%q idempotency-key=%q", auth, idem)
	}
	if got["to"] != "9779841234567" || got["reference"] != "msg_1" {
		t.Fatalf("body = %v", got)
	}
}

func TestHTTPMessageTextCannotChangeTheRequestShape(t *testing.T) {
	var raw []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"data":{"id":"x"}}`))
	}))
	defer srv.Close()
	g := openHTTPGw(t, srv.URL, map[string]any{"body": map[string]any{"to": "{{to}}", "text": "{{text}}", "admin": false}})
	m := msg()
	m.Text = `", "admin": true, "x": "{{to}} \u0000`
	if _, err := g.Send(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("the body is not JSON: %s", raw)
	}
	if got["admin"] != false || got["text"] != m.Text || len(got) != 3 {
		t.Fatalf("message text altered the request: %s", raw)
	}
}

func TestHTTPClassifiesFailures(t *testing.T) {
	cases := []struct {
		status int
		body   string
		header map[string]string
		class  Class
		code   string
	}{
		{429, `{}`, map[string]string{"Retry-After": "7"}, ClassThrottled, "http_429"},
		{503, `{}`, nil, ClassRetryable, "http_503"},
		{500, `{}`, nil, ClassRetryable, "http_500"},
		{400, `{}`, nil, ClassPermanent, "http_400"},
		{401, `{}`, nil, ClassProvider, "http_401"},
		{402, `{}`, nil, ClassProvider, "http_402"},
		{400, `{"error":{"code":"invalid_number"}}`, nil, ClassPermanent, "invalid_number"},
		{400, `{"error":{"code":"suspended"}}`, nil, ClassProvider, "suspended"},
	}
	for _, c := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			for k, v := range c.header {
				w.Header().Set(k, v)
			}
			w.WriteHeader(c.status)
			_, _ = w.Write([]byte(c.body))
		}))
		g := openHTTPGw(t, srv.URL, map[string]any{"permanent_codes": []string{"invalid_number"}, "provider_codes": []string{"suspended"}})
		_, err := g.Send(context.Background(), msg())
		class, ge := ClassOf(err)
		if err == nil || class != c.class || ge.Code != c.code {
			t.Errorf("status %d %s: class=%v code=%v err=%v; want %v %s", c.status, c.body, class, ge, err, c.class, c.code)
		}
		if c.status == 429 && ge.RetryAfter != 7*time.Second {
			t.Errorf("Retry-After = %v", ge.RetryAfter)
		}
		srv.Close()
	}
}

func TestHTTPTimeoutAndConnectionErrorsAreRetryable(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { time.Sleep(300 * time.Millisecond) }))
	defer slow.Close()
	g := openHTTPGw(t, slow.URL, map[string]any{"timeout": "50ms"})
	if _, err := g.Send(context.Background(), msg()); classOf(err) != ClassRetryable {
		t.Fatalf("a timeout must be retryable, got %v", err)
	}
	gone := httptest.NewServer(http.NotFoundHandler())
	url := gone.URL
	gone.Close()
	g2 := openHTTPGw(t, url, nil)
	if _, err := g2.Send(context.Background(), msg()); classOf(err) != ClassRetryable {
		t.Fatalf("a refused connection must be retryable, got %v", err)
	}
}

func TestHTTPRefusesPrivateNetworksUnlessAllowed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{}`)) }))
	defer srv.Close()
	g, err := Open(context.Background(), "http", "ssrf", map[string]any{"url": srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	_, err = g.Send(context.Background(), msg())
	class, ge := ClassOf(err)
	if err == nil || class != ClassProvider || ge.Code != "blocked_address" {
		t.Fatalf("a loopback vendor URL must be refused, got %v", err)
	}
}

func TestHTTPDoesNotFollowRedirects(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("the redirect target was contacted")
	}))
	defer target.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer srv.Close()
	g := openHTTPGw(t, srv.URL, nil)
	if _, err := g.Send(context.Background(), msg()); err == nil {
		t.Fatal("a redirect is not success")
	}
}

func TestHTTPParsesReceipts(t *testing.T) {
	g := openHTTPGw(t, "http://example.invalid/send", map[string]any{
		"dlr": map[string]any{"id_field": "id", "ref_field": "reference", "status_field": "status",
			"delivered": []string{"DELIVRD"}, "failed": []string{"UNDELIV"}, "list_path": "items"},
	}).(DLRParser)
	out, err := g.ParseDLR(nil, []byte(`{"items":[{"id":"a","status":"DELIVRD"},{"id":"b","reference":"msg_9","status":"UNDELIV"},{"id":"c","status":"ENROUTE"},{"status":"DELIVRD"}]}`))
	if err != nil || len(out) != 3 {
		t.Fatalf("receipts = %+v, %v", out, err)
	}
	if out[0].Status != StatusDelivered || out[1].Status != StatusFailed || out[1].MessageID != "msg_9" || out[2].Status != StatusUnknown {
		t.Fatalf("receipts = %+v", out)
	}
	if _, err := g.ParseDLR(nil, []byte(`not json`)); err == nil {
		t.Fatal("a malformed receipt must be an error")
	}
}

func TestHTTPRejectsBadConfiguration(t *testing.T) {
	for name, plugin := range map[string]map[string]any{
		"relative url":   {"url": "/send"},
		"ftp":            {"url": "ftp://x/send"},
		"unknown key":    {"url": "http://x/send", "colour": "red"},
		"bad format":     {"url": "http://x/send", "format": "xml"},
		"bad bool value": {"url": "http://x/send", "allow_private_networks": "maybe"},
	} {
		if _, err := Open(context.Background(), "http", "x", plugin); err == nil {
			t.Errorf("%s must be rejected", name)
		}
	}
}

func TestUnknownPluginKind(t *testing.T) {
	if _, err := Open(context.Background(), "carrier-pigeon", "x", nil); err == nil {
		t.Fatal("an unregistered kind must fail")
	}
	if len(Kinds()) < 3 {
		t.Fatalf("kinds = %v", Kinds())
	}
}

func TestMockFailsInEachWayAnUpstreamDoes(t *testing.T) {
	g, _ := Open(context.Background(), "mock", "m", map[string]any{"reject_prefixes": []string{"1555"}})
	m := g.(*Mock)
	if _, err := m.Send(context.Background(), Message{ID: "a", To: "15551234567"}); classOf(err) != ClassPermanent {
		t.Fatalf("reject prefix = %v", err)
	}
	m.SetDown(true)
	if _, err := m.Send(context.Background(), msg()); classOf(err) != ClassRetryable {
		t.Fatalf("down = %v", err)
	}
	m.SetDown(false)
	m.FailNext(1, Errorf(ClassThrottled, "slow_down", "x"))
	if _, err := m.Send(context.Background(), msg()); classOf(err) != ClassThrottled {
		t.Fatalf("scripted = %v", err)
	}
	m.LoseResponseNext(1)
	if _, err := m.Send(context.Background(), msg()); err == nil || m.Count("msg_1") != 1 {
		t.Fatalf("a lost response delivers upstream and reports failure: %v count=%d", err, m.Count("msg_1"))
	}
}

// ---- SMPP, against smppflow's own server -----------------------------------

func smppGateway(t *testing.T, smsc *sandbox.SMSC, password string) (Gateway, chan DLR) {
	t.Helper()
	g, err := Open(context.Background(), "smpp", "np", map[string]any{
		"addr": smsc.Addr(), "system_id": smsc.SystemID, "password": password,
		"response_timeout": "3s", "connect_timeout": "3s",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = g.Close() })
	dlrs := make(chan DLR, 8)
	g.(DLRSource).SetDLRSink(func(_ context.Context, d DLR) { dlrs <- d })
	return g, dlrs
}

func TestSMPPSubmitsAndReportsReceipts(t *testing.T) {
	smsc, err := sandbox.StartSMSC(sandbox.SMSCConfig{SystemID: "smsgw", Password: "pw", DLRDelay: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer smsc.Close()
	g, dlrs := smppGateway(t, smsc, "pw")
	rec, err := g.Send(context.Background(), msg())
	if err != nil || rec.ProviderMessageID == "" {
		t.Fatalf("send = %+v, %v", rec, err)
	}
	select {
	case d := <-dlrs:
		if d.ProviderMessageID != rec.ProviderMessageID || d.Status != StatusDelivered || d.Provider != "np" {
			t.Fatalf("receipt = %+v for %+v", d, rec)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no delivery receipt")
	}
	if got := smsc.Submitted(); len(got) != 1 || got[0].To != "9779841234567" || got[0].Text != "hello" {
		t.Fatalf("the SMSC saw %+v", got)
	}
}

func TestSMPPClassifiesWhatTheSMSCAnswers(t *testing.T) {
	smsc, err := sandbox.StartSMSC(sandbox.SMSCConfig{SystemID: "smsgw", Password: "pw", RejectPrefixes: []string{"97700"}})
	if err != nil {
		t.Fatal(err)
	}
	defer smsc.Close()

	// Wrong password: the provider refuses us; another provider may accept.
	bad, _ := smppGateway(t, smsc, "wrong")
	if _, err := bad.Send(context.Background(), msg()); classOf(err) != ClassProvider {
		t.Fatalf("a refused bind = %v", err)
	}

	g, _ := smppGateway(t, smsc, "pw")
	m := msg()
	m.To = "977001234567"
	if _, err := g.Send(context.Background(), m); classOf(err) != ClassPermanent {
		t.Fatalf("an invalid destination = %v", err)
	}
	// A system error is retryable, and the session recovers afterwards.
	smsc.SetDown(true)
	if _, err := g.Send(context.Background(), msg()); classOf(err) != ClassRetryable {
		t.Fatalf("a system error = %v", err)
	}
	smsc.SetDown(false)
	if _, err := g.Send(context.Background(), msg()); err != nil {
		t.Fatalf("the gateway must recover once the upstream does: %v", err)
	}
}

func TestSMPPUnreachableIsRetryable(t *testing.T) {
	smsc, err := sandbox.StartSMSC(sandbox.SMSCConfig{SystemID: "smsgw", Password: "pw"})
	if err != nil {
		t.Fatal(err)
	}
	g, _ := smppGateway(t, smsc, "pw")
	smsc.Close()
	time.Sleep(50 * time.Millisecond)
	if _, err := g.Send(context.Background(), msg()); classOf(err) != ClassRetryable {
		t.Fatalf("an unreachable SMSC = %v", err)
	}
}

func TestSMPPConcurrentSends(t *testing.T) {
	smsc, err := sandbox.StartSMSC(sandbox.SMSCConfig{SystemID: "smsgw", Password: "pw"})
	if err != nil {
		t.Fatal(err)
	}
	defer smsc.Close()
	g, _ := smppGateway(t, smsc, "pw")
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m := msg()
			m.ID = "m" + string(rune('a'+i))
			if _, err := g.Send(context.Background(), m); err != nil {
				t.Errorf("send %d: %v", i, err)
			}
		}()
	}
	wg.Wait()
	if n := len(smsc.Submitted()); n != 20 {
		t.Fatalf("the SMSC saw %d of 20", n)
	}
}
