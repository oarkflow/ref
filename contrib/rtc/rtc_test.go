package rtc_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/oarkflow/fh"
	"github.com/oarkflow/ref/contrib/rtc"
	"github.com/oarkflow/ref/platform"
	"github.com/oarkflow/rtcd/auth"
	"github.com/oarkflow/rtcd/client"
	"github.com/oarkflow/rtcd/server"
	_ "modernc.org/sqlite"
)

const serviceKey = "ref-app-service-key-0123456789"

// startService runs a real call service.
func startService(t *testing.T) string {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "r.db")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	s, err := server.New(context.Background(), server.Config{
		DB: db, Dialect: "sqlite", TokenSecret: "token-secret-token-secret-0123456",
		Keys: []auth.ServiceKey{{Name: "ref-app", Key: serviceKey, Tenant: "demo"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	app := fh.NewFast()
	s.Mount(app)
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	go func() { _ = app.Serve(ln) }()
	t.Cleanup(func() { _ = app.ShutdownWithTimeout(time.Second); _ = ln.Close() })
	return "http://" + ln.Addr().String()
}

func appBCL(url, key string) string {
	return fmt.Sprintf(`
name "rtc-client"
environment "test"
resource "keys" {
  kind "auth.api_key"
  config { keys [
    { value "ada-secret-key-0123456789" principal_id "ada" }
    { value "bob-secret-key-0123456789" principal_id "bob" } ] }
}
resource "calls" { kind "rtc.service" config { url "%s" api_key "%s" } }

intent "call.start"  { response "r" node "n" { uses "call.start"  resource "calls" kind effect requires [input] provides [r] config { to_fact "input.to" mode_fact "input.mode" } } }
intent "call.answer" { response "r" node "n" { uses "call.answer" resource "calls" kind effect requires [input] provides [r] config { call_fact "input.call" } } }
intent "call.get"    { response "r" node "n" { uses "call.get"    resource "calls" kind read   requires [input] provides [r] config { call_fact "input.call" } } }
intent "call.token"  { response "r" node "n" { uses "call.token"  resource "calls" kind effect requires [input] provides [r] } }
route "call.start"  { method POST path "/call/start"  intent "call.start"  auth "keys" }
route "call.answer" { method POST path "/call/answer" intent "call.answer" auth "keys" }
route "call.get"    { method POST path "/call/get"    intent "call.get"    auth "keys" }
route "call.token"  { method POST path "/call/token"  intent "call.token"  auth "keys" }
`, url, key)
}

type app struct {
	t    *testing.T
	addr string
}

func startApp(t *testing.T, url, key string) *app {
	t.Helper()
	rtc.Register()
	p, err := platform.Compile(context.Background(), []byte(appBCL(url, key)), t.TempDir(), platform.DefaultLoadOptions())
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })
	srv := fh.NewFast()
	if err := p.Mount(srv); err != nil {
		t.Fatal(err)
	}
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.ShutdownWithTimeout(time.Second); _ = ln.Close() })
	return &app{t: t, addr: ln.Addr().String()}
}

func (a *app) post(user, path string, body any) (int, map[string]any) {
	a.t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", "http://"+a.addr+path, strings.NewReader(string(b)))
	req.Header.Set("Content-Type", "application/json")
	if user != "" {
		req.Header.Set("X-API-Key", user+"-secret-key-0123456789")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		a.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, out
}

func TestTheAppActsForItsUsersThroughTheService(t *testing.T) {
	url := startService(t)
	a := startApp(t, url, serviceKey)

	status, res := a.post("ada", "/call/start", map[string]any{"to": []string{"bob"}, "mode": "sfu"})
	if status != 200 {
		t.Fatalf("start: %d %v", status, res)
	}
	call := res["call"].(map[string]any)
	if call["initiator"] != "ada" || call["e2ee"] != true || call["mode"] != "sfu" {
		t.Fatalf("%v", call)
	}
	id := call["id"].(string)
	// bob is invited and sees it; ada's app cannot make bob's answer for him.
	if status, _ := a.post("bob", "/call/answer", map[string]any{"call": id}); status != 200 {
		t.Fatalf("answer: %d", status)
	}
	if status, res := a.post("bob", "/call/get", map[string]any{"call": id}); status != 200 || res["status"] != "active" {
		t.Fatalf("get: %d %v", status, res)
	}
	// Without a signed-in user the app never reaches the service.
	if status, _ := a.post("", "/call/start", map[string]any{"to": []string{"bob"}}); status != 401 {
		t.Fatalf("an anonymous start: %d", status)
	}
}

func TestATokenLetsTheBrowserTalkToTheServiceDirectly(t *testing.T) {
	url := startService(t)
	a := startApp(t, url, serviceKey)
	status, res := a.post("ada", "/call/token", map[string]any{})
	if status != 200 || res["token"] == "" || res["url"] != url {
		t.Fatalf("%d %v", status, res)
	}
	// The token works at the service as ada and only as ada.
	u := client.NewUser(url, res["token"].(string))
	started, err := u.Start(context.Background(), client.StartRequest{To: []string{"bob"}})
	if err != nil || started.Call.Initiator != "ada" {
		t.Fatalf("%+v %v", started, err)
	}
	// The app's own view agrees: the call is there for ada.
	if status, got := a.post("ada", "/call/get", map[string]any{"call": started.Call.ID}); status != 200 || got["id"] != started.Call.ID {
		t.Fatalf("%d %v", status, got)
	}
	// Somebody else's app (with another tenant's key) could not see it, and a
	// wrong key makes the app's calls unavailable rather than leaking details.
	bad := startApp(t, url, "a-key-the-service-does-not-know-1")
	status, res = bad.post("ada", "/call/start", map[string]any{"to": []string{"bob"}})
	if status < 500 || strings.Contains(fmt.Sprint(res), "key") {
		t.Fatalf("a refused service key: %d %v", status, res)
	}
}

func TestMisconfigurationIsRefusedAtStartup(t *testing.T) {
	rtc.Register()
	for name, bcl := range map[string]string{
		"no key": strings.Replace(appBCL("http://x", "k"), `api_key "k"`, "", 1),
		"no url": strings.Replace(appBCL("http://x", "k"), `url "http://x"`, "", 1),
		"typo":   strings.Replace(appBCL("http://x", "k"), `api_key "k"`, `api_kee "k"`, 1),
	} {
		if p, err := platform.Compile(context.Background(), []byte(bcl), t.TempDir(), platform.DefaultLoadOptions()); err == nil {
			_ = p.Close()
			t.Errorf("%s: accepted", name)
		}
	}
}
