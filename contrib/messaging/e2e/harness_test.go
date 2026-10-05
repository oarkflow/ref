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

// doList is do for an endpoint that answers with a list, which do cannot hold.
func (s *stack) doList(method, path, key string, body any) (int, []any) {
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
	var out []any
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, out
}

func (s *stack) admin(method, path string, body any) (int, map[string]any) {
	s.t.Helper()
	return s.do(method, path, adminKey, body)
}

// adminList is an operator endpoint whose answer is a list, which do cannot hold
// (it wants an object).
func (s *stack) adminList(path string) []any {
	s.t.Helper()
	status, out := s.doList("GET", path, adminKey, nil)
	if status != 200 {
		s.t.Fatalf("GET %s = %d %v", path, status, out)
	}
	return out
}

// asAccountList is the account's own message list.
func (s *stack) asAccountList(user string) []any {
	s.t.Helper()
	status, out := s.doList("GET", "/v1/messages", s.key(user), nil)
	if status != 200 {
		s.t.Fatalf("GET /v1/messages = %d", status)
	}
	return out
}

// carriers is every carrier in this installation, as an operator sees them: the
// ids and channels an account must never be shown.
func (s *stack) carriers() (ids, channels []string) {
	for _, c := range s.adminList("/v1/admin/providers") {
		ids = append(ids, str(asMap(c), "id"))
		channels = append(channels, str(asMap(c), "channel"))
	}
	return
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

// carrier reads which carrier carried a message, from the OPERATOR's view. An
// account is never told, so a test that wants to check the routing asks here —
// which is the same separation the API itself keeps.
func (s *stack) carrier(id string) string {
	s.t.Helper()
	status, out := s.admin("GET", "/v1/admin/messages/"+id, nil)
	if status != 200 {
		s.t.Fatalf("admin message %s: %d %v", id, status, out)
	}
	return str(asMap(out), "message", "provider")
}

// explainAs runs the OPERATOR's dry run for an account, which is the only view
// that names carriers: an account's own dry run (POST /ui/route/explain) has no
// carrier in it, on purpose. Every routing assertion goes through here, and so
// does every assertion that an account is NOT shown one.
func (s *stack) explainAs(account, to, text string, extra map[string]any) map[string]any {
	s.t.Helper()
	body := map[string]any{"account": account, "to": to, "text": text}
	for k, v := range extra {
		body[k] = v
	}
	status, out := s.admin("POST", "/v1/admin/route/explain", body)
	if status != 200 {
		s.t.Fatalf("explain %s -> %s: %d %v", account, to, status, out)
	}
	return asMap(out)
}

// explainRoute is explainAs for an account's own browser, so a test can ask "what
// would happen to THIS account's message" without repeating the account id. The
// browser only supplies who the account is; the answer comes from the operator's
// view, because the account's own does not name carriers.
func (s *stack) explainRoute(b *browser, to, text string, extra map[string]any) (first string, ids []string, rejected map[string]string) {
	s.t.Helper()
	return routeOf(s.explainAs(whoami(b), to, text, extra))
}

func routeOf(out map[string]any) (first string, ids []string, rejected map[string]string) {
	for _, c := range asList(out["route"]) {
		ids = append(ids, asMap(c)["id"].(string))
	}
	if len(ids) > 0 {
		first = ids[0]
	}
	rejected = map[string]string{}
	for _, r := range asList(out["rejected"]) {
		rr := asMap(r)
		rejected[rr["id"].(string)] = Stringify(rr["rule"])
	}
	return
}

// whoami is the account a browser is signed in as.
func whoami(b *browser) string {
	b.t.Helper()
	st, me := b.do("GET", "/ui/me", nil)
	if st != 200 {
		b.t.Fatalf("/ui/me = %d %v", st, me)
	}
	return str(asMap(me), "id")
}

// asAccount is the account's own view of one message.
func (s *stack) asAccount(user, id string) map[string]any {
	s.t.Helper()
	status, out := s.do("GET", "/v1/messages/"+id, s.key(user), nil)
	if status != 200 {
		s.t.Fatalf("message %s: %d %v", id, status, out)
	}
	return asMap(out)
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
