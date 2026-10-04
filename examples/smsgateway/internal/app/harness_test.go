package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/oarkflow/fh"

	"github.com/oarkflow/ref/examples/smsgateway/internal/sandbox"
	"github.com/oarkflow/ref/examples/smsgateway/internal/sms"
)

const adminKey = "test-admin-key-0123456789abcdef"

// stack is the whole application on temp storage, with the sandbox upstreams,
// served over real HTTP.
type stack struct {
	t      *testing.T
	app    *App
	smsc   *sandbox.SMSC
	vendor *sandbox.Vendor
	dir    string
	base   string
	client *http.Client
	keys   map[string]string
	stop   func()
	// stopOnce makes shutdown idempotent for tests that stop the app early.
	stopOnce *sync.Once
}

// shutdown stops the application but leaves the upstreams and storage.
func (s *stack) shutdown() { s.stopOnce.Do(s.stop) }

func startStack(t *testing.T, extraEnv map[string]string) *stack {
	t.Helper()
	return startStackWith(t, stackOpts{env: extraEnv})
}

// stackOpts lets a test restart the application on the same storage and
// upstreams.
type stackOpts struct {
	env    map[string]string
	dir    string
	smsc   *sandbox.SMSC
	vendor *sandbox.Vendor
	// keepUpstreams leaves the sandbox running when the stack closes.
	keepUpstreams bool
}

func startStackWith(t *testing.T, o stackOpts) *stack {
	t.Helper()
	smsc, vendor, dir := o.smsc, o.vendor, o.dir
	var err error
	if smsc == nil {
		smsc, err = sandbox.StartSMSC(sandbox.SMSCConfig{SystemID: "smsgw", Password: "sandbox", DLRDelay: 30 * time.Millisecond})
		if err != nil {
			t.Fatal(err)
		}
	}
	if vendor == nil {
		vendor, err = sandbox.StartVendor("sandbox-token")
		if err != nil {
			t.Fatal(err)
		}
		vendor.Delay = 30 * time.Millisecond
	}
	if dir == "" {
		dir = t.TempDir()
	}
	extraEnv := o.env
	env := map[string]string{
		"APP_ENV":                  "test",
		"SMS_ADMIN_KEY":            adminKey,
		"SMS_DSN":                  "file:" + filepath.Join(dir, "sms.db") + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)",
		"SMS_BROKER_DIR":           filepath.Join(dir, "broker"),
		"NP_SMPP_ADDR":             smsc.Addr(),
		"IN_VENDOR_URL":            vendor.URL() + "/send",
		"IN_VENDOR_WEBHOOK_SECRET": "sandbox-secret",
	}
	for k, v := range extraEnv {
		env[k] = v
	}
	a, err := Load(context.Background(), Options{
		Dir: "../../app",
		Env: func(k string) (string, bool) { v, ok := env[k]; return v, ok },
	})
	if err != nil {
		smsc.Close()
		vendor.Close()
		t.Fatalf("load: %v", err)
	}
	s := &stack{t: t, app: a, smsc: smsc, vendor: vendor, dir: dir, client: &http.Client{Timeout: 15 * time.Second}, keys: map[string]string{}}
	srv := fh.NewFast()
	if err := a.Mount(srv); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	served := make(chan struct{})
	go func() {
		defer close(served)
		_ = srv.Serve(ln)
	}()
	s.base = "http://" + ln.Addr().String()
	vendor.SetCallback(s.base+"/v1/webhooks/dlr/in_vendor", "sandbox-secret")
	s.stop = func() {
		_ = srv.ShutdownWithTimeout(2 * time.Second)
		_ = ln.Close()
		<-served
		_ = a.Close()
	}
	var once sync.Once
	t.Cleanup(func() {
		once.Do(s.stop)
		if !o.keepUpstreams {
			smsc.Close()
			vendor.Close()
		}
	})
	s.stopOnce = &once
	return s
}

// do sends a JSON request. key is sent as a bearer token.
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
	req, err := http.NewRequest(method, s.base+path, rdr)
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
			s.t.Fatalf("%s %s: status %d, body is not JSON: %s", method, path, resp.StatusCode, raw)
		}
	}
	return resp.StatusCode, out
}

func (s *stack) admin(method, path string, body any) (int, map[string]any) {
	s.t.Helper()
	return s.do(method, path, adminKey, body)
}

// key issues (once) and returns an API key for a seeded user.
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

// waitMessage polls until the message reaches one of the states.
func (s *stack) waitMessage(user, id string, states ...string) map[string]any {
	s.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	var last map[string]any
	for time.Now().Before(deadline) {
		status, out := s.do("GET", "/v1/messages/"+id, s.key(user), nil)
		if status == 200 {
			last = out
			for _, st := range states {
				if out["state"] == st {
					return out
				}
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	s.t.Fatalf("message %s did not reach %v; last view: %v", id, states, last)
	return nil
}

func (s *stack) balance(user string) (balance, held float64) {
	s.t.Helper()
	status, out := s.do("GET", "/v1/balance", s.key(user), nil)
	if status != 200 {
		s.t.Fatalf("balance: %d %v", status, out)
	}
	return out["balance"].(float64), out["held"].(float64)
}

func str(m map[string]any, path ...string) string {
	var cur any = m
	for _, p := range path {
		mm, ok := cur.(map[string]any)
		if !ok {
			return ""
		}
		cur = mm[p]
	}
	s, _ := cur.(string)
	return s
}

func errCode(out map[string]any) string { return str(out, "error", "code") }

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

var _ = sms.StateQueued
