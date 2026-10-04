// Package e2e runs the examples/smsgateway application directory, which is
// nothing but BCL, rules and templates, end to end: real HTTP, a real SMPP
// bind, real vendor HTTP calls, the broker queue and SQLite.
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/oarkflow/ref/contrib/messaging"
	"github.com/oarkflow/ref/contrib/messaging/sim"
	"github.com/oarkflow/ref/platform"
	"github.com/oarkflow/ref/serve"
)

const adminKey = "test-admin-key-0123456789abcdef"

// appDir is the application under test.
var appDir, _ = filepath.Abs("../../../examples/smsgateway")

type stack struct {
	t      *testing.T
	app    *serve.App
	smsc   *sim.SMSC
	vendor *sim.Vendor
	dir    string
	client *http.Client
	keys   map[string]string
}

type opts struct {
	env           map[string]string
	dir           string
	app           string // an application directory other than the example
	smsc          *sim.SMSC
	vendor        *sim.Vendor
	keepUpstreams bool
}

func start(t *testing.T, o ...opts) *stack {
	t.Helper()
	var op opts
	if len(o) > 0 {
		op = o[0]
	}
	messaging.Register()
	app := appDir
	if op.app != "" {
		app = op.app
	}
	smsc, vendor, dir := op.smsc, op.vendor, op.dir
	var err error
	if smsc == nil {
		if smsc, err = sim.StartSMSC(sim.SMSCConfig{SystemID: "smsgw", Password: "sandbox", DLRDelay: 30 * time.Millisecond}); err != nil {
			t.Fatal(err)
		}
	}
	if vendor == nil {
		if vendor, err = sim.StartVendor("sandbox-token"); err != nil {
			t.Fatal(err)
		}
		vendor.Delay = 30 * time.Millisecond
	}
	if dir == "" {
		dir = t.TempDir()
	}
	env := map[string]string{
		"APP_ENV": "test", "SMS_ADMIN_KEY": adminKey, "SMS_SANDBOX": "false", "SMS_WEBHOOK_SECRET": "sandbox-secret-0123456789",
		"SMS_DSN":        "file:" + filepath.Join(dir, "sms.db") + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)",
		"SMS_BROKER_DIR": filepath.Join(dir, "broker"), "SMS_SESSION_DIR": filepath.Join(dir, "sessions"),
		"SMS_RULES_DIR": filepath.Join(app, "rules"),
		"NP_SMPP_ADDR":  smsc.Addr(),
		"PREMIUM_URL":   vendor.URL() + "/premium_np", "IN_VENDOR_URL": vendor.URL() + "/in_vendor", "GLOBAL_URL": vendor.URL() + "/global_fallback", "SMSPASAL_KEY": sim.PasalKey, "SMSPASAL_ALLOW_PRIVATE": "true", "SMSPASAL_HOST": "127.0.0.1",
	}
	for k, v := range op.env {
		env[k] = v
	}
	a, err := serve.Start(context.Background(), serve.Options{
		Dir: app, Addr: "127.0.0.1:0", Env: "test",
		Load: func(lo *platform.LoadOptions) {
			lo.Env = func(k string) (string, bool) { v, ok := env[k]; return v, ok }
		},
	})
	if err != nil {
		smsc.Close()
		vendor.Close()
		t.Fatalf("start: %v", err)
	}
	s := &stack{t: t, app: a, smsc: smsc, vendor: vendor, dir: dir, client: &http.Client{Timeout: 20 * time.Second}, keys: map[string]string{}}
	s.pointSmspasalAtVendor()
	vendor.SetCallback(a.URL()+"/v1/webhooks/dlr/{provider}", "sandbox-secret-0123456789")
	t.Cleanup(func() {
		_ = a.Stop(5 * time.Second)
		if !op.keepUpstreams {
			smsc.Close()
			vendor.Close()
		}
	})
	return s
}

// stop shuts the application down but leaves upstreams and storage.
func (s *stack) stop() { _ = s.app.Stop(5 * time.Second) }

func (s *stack) do(method, path, key string, body any) (int, map[string]any) {
	s.t.Helper()
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			s.t.Fatal(err)
		}
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, s.app.URL()+path, rdr)
	if err != nil {
		s.t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		s.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	if len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, &out); err != nil {
			s.t.Fatalf("%s %s: %d, body is not JSON: %s", method, path, resp.StatusCode, raw)
		}
	}
	return resp.StatusCode, out
}

func (s *stack) admin(method, path string, body any) (int, map[string]any) {
	s.t.Helper()
	return s.do(method, path, adminKey, body)
}

// key issues (once) an API key for a seeded user.
func (s *stack) key(user string) string {
	s.t.Helper()
	if k, ok := s.keys[user]; ok {
		return k
	}
	status, out := s.admin("POST", "/v1/admin/users/"+user+"/key", nil)
	if status != 200 {
		s.t.Fatalf("issue key for %s: %d %v", user, status, out)
	}
	k, _ := out["api_key"].(string)
	s.keys[user] = k
	return k
}

func (s *stack) send(user string, body map[string]any) (int, map[string]any) {
	s.t.Helper()
	return s.do("POST", "/v1/messages", s.key(user), body)
}

func get(m map[string]any, path ...string) any {
	var cur any = m
	for _, p := range path {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = mm[p]
	}
	return cur
}

func str(m map[string]any, path ...string) string {
	s, _ := get(m, path...).(string)
	return s
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// pointSmspasalAtVendor sets the smspasal provider's API URL to this test's
// vendor stand-in, which listens on a random port. The application keeps the
// URL in the provider's settings (the form on /admin/providers), not in an
// environment variable.
func (s *stack) pointSmspasalAtVendor() {
	s.t.Helper()
	b := s.browser()
	if st := b.login("operator@example.com", "operator-pass-123"); st != 200 {
		s.t.Fatalf("operator login = %d", st)
	}
	if st, out := b.do("PUT", "/ui/admin/providers/smspasal/settings", map[string]any{"values": map[string]any{"url": s.vendor.URL() + "/smsapi/index.php"}}); st != 200 {
		s.t.Fatalf("point smspasal at the vendor: %d %v", st, out)
	}
	body := map[string]any{
		"public": map[string]any{"routeid": "10259", "campaign": "9801"},
		"secret": map[string]any{"key": sim.PasalKey},
	}
	if st, out := b.do("PUT", "/ui/admin/providers/smspasal/accounts/primary", body); st != 200 {
		s.t.Fatalf("point smspasal at the vendor: %d %v", st, out)
	}
}
