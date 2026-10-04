package e2e

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func (s *stack) page(path string) (int, string) {
	s.t.Helper()
	resp, err := http.Get(s.app.URL() + path)
	if err != nil {
		s.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestConsolePagesAndStatic(t *testing.T) {
	s := start(t)
	for path, want := range map[string]string{
		"/": "Send a message", "/messages": "Ledger", "/admin": "Providers", "/login": "Sign in", "/admin/providers": "New provider", "/admin/rules": "Generated rules", "/admin/accounts": "New account", "/campaigns": "Your campaigns", "/audiences": "Saved audiences", "/notifications": "Mark all as read",
		"/static/css/app.css": "--brand", "/static/js/ui.js": "/ui/me", "/static/js/providers.js": "Price per segment", "/static/js/accounts.js": "API key", "/static/js/rules.js": "routing-rules", "/static/js/bulk.js": "campaignform",
	} {
		status, body := s.page(path)
		if status != 200 || !strings.Contains(body, want) {
			t.Fatalf("%s = %d, missing %q in %.200s", path, status, want, body)
		}
	}
	if _, body := s.page("/"); !strings.Contains(body, "ui.js?v=") || !strings.Contains(body, `id="fieldlist"`) || !strings.Contains(body, "SMS Gateway") {
		t.Fatalf("layout or loop did not render: %.400s", body)
	}
}

func TestSplMessageTemplates(t *testing.T) {
	s := start(t)
	send := func(body map[string]any) (int, map[string]any) { return s.send("demo", body) }
	status, out := send(map[string]any{"to": "9841234567", "template": "otp_login", "vars": map[string]any{"brand": "Acme", "code": "482913", "minutes": 5}})
	if status != 202 || out["type"] != "otp" || out["from"] != "ACMEBANK" {
		t.Fatalf("otp = %d %v", status, out)
	}
	eventually(t, "otp delivered", func() bool { return s.state("demo", str(out, "id")) == "delivered" })
	var got string
	for _, m := range s.smsc.Submitted() {
		got = m.Text
	}
	if got != "Your Acme login code is 482913. It expires in 5 minutes. Do not share it with anyone." {
		t.Fatalf("sent text = %q", got)
	}
	status, out = send(map[string]any{"to": "9841234567", "template": "otp_login", "lang": "ne", "vars": map[string]any{"brand": "Acme", "code": "1", "minutes": 5}})
	if status != 202 || out["encoding"] != "ucs2" {
		t.Fatalf("nepali otp = %d %v", status, out)
	}
	if status, out = send(map[string]any{"to": "9841234567", "template": "nope"}); status != 422 {
		t.Fatalf("unknown template = %d %v", status, out)
	}
}
