package rtc_test

import (
	"context"
	"io"
	"net/http"
	"net/http/cookiejar"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/oarkflow/ref/contrib/rtc"
	"github.com/oarkflow/ref/serve"
)

// The shipped example must keep compiling and working: it is the documentation
// people copy from.
func TestExampleApplication(t *testing.T) {
	rtc.Register()
	dir := t.TempDir()
	t.Setenv("SESSION_SECRET", strings.Repeat("s", 48))
	t.Setenv("DEMO_PASSWORD", "demo-password")
	t.Setenv("OPS_API_KEY", "ops-key-0123456789abcdef")
	t.Setenv("RT_DB", "file:"+filepath.ToSlash(filepath.Join(dir, "rt.db"))+"?_pragma=busy_timeout(5000)")
	t.Setenv("RT_LOG", filepath.Join(dir, "app.log"))

	root, _ := filepath.Abs("../../examples/realtime-platform")
	app, err := serve.Start(context.Background(), serve.Options{Dir: root, Addr: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("the example does not start: %v", err)
	}
	t.Cleanup(func() { _ = app.Stop(time.Second) })
	base := app.URL()

	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, Timeout: 10 * time.Second}
	post := func(path, body string) (int, string) {
		resp, err := client.Post(base+path, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	if status, _ := post("/api/login", `{"user":"ada","password":"wrong"}`); status != 401 {
		t.Fatalf("wrong password: %d", status)
	}
	if status, body := post("/api/login", `{"user":"ada","password":"demo-password"}`); status != 200 {
		t.Fatalf("login: %d %s", status, body)
	}
	status, body := post("/api/dm", `{"user":"bob"}`)
	if status != 200 || !strings.Contains(body, `"kind":"dm"`) {
		t.Fatalf("dm: %d %s", status, body)
	}
	if status, _ := post("/api/presence", `{"topic":"user:bob"}`); status != 403 {
		t.Fatalf("presence of another user's private topic: %d", status)
	}
	resp, err := client.Get(base + "/")
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(page), "Realtime Platform") {
		t.Fatalf("index: %d", resp.StatusCode)
	}
}
