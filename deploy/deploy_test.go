package deploy

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oarkflow/ref/platform"
	_ "modernc.org/sqlite"
)

func appSource(version string) string {
	return fmt.Sprintf(`
name "hello"
version %q

intent "hello" {
  response "msg"
  node "msg" {
    uses "constant"
    provides [msg]
    config {
      value %q
    }
  }
}

route "hello" {
  method GET
  path "/hello"
  intent "hello"
}
`, version, "hello from "+version)
}

// buildsButFailsToOpen validates (the kind is registered) but cannot be
// compiled: its database driver does not exist.
const buildsButFailsToOpen = `
name "hello"
resource "db" {
  kind "database.sql"
  config {
    driver "no-such-driver"
    dsn "x"
  }
}
intent "hello" {
  response "msg"
  node "msg" {
    uses "constant"
    provides [msg]
    config {
      value "broken"
    }
  }
}
route "hello" {
  method GET
  path "/hello"
  intent "hello"
}
`

func newManager(store Store) *Manager {
	return &Manager{
		Store: store, App: "hello", Secret: []byte("deploy-test-secret-0123456789abcd"), Approvals: 1,
		Validate: func(ctx context.Context, src []byte) platform.ValidationReport {
			return platform.Validate(ctx, src, ".", platform.DefaultLoadOptions())
		},
	}
}

type adminClient struct {
	t    *testing.T
	base string
}

func (c adminClient) do(method, path, token string, body any) (int, map[string]any) {
	c.t.Helper()
	var r io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		r = bytes.NewReader(raw)
	}
	req, _ := http.NewRequest(method, c.base+path, r)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func get(t *testing.T, url string) (string, error) {
	resp, err := http.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("status %d: %s", resp.StatusCode, raw)
	}
	var s string
	_ = json.Unmarshal(raw, &s)
	return s, nil
}

func TestRevisionLifecycleWithHotSwap(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := newManager(NewMemoryStore())
	const alice, bob = "alice-token-0123456789", "bob-token-0123456789ab"

	// Seed v1 (proposed by alice, approved by bob, activated).
	v1, err := m.Propose(ctx, []byte(appSource("v1")), "alice", "initial")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Approve(ctx, v1.ID, "bob", "ok"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Activate(ctx, v1.ID, "bob"); err != nil {
		t.Fatal(err)
	}

	sup := &Supervisor{Manager: m, Poll: 50 * time.Millisecond, Grace: 300 * time.Millisecond, Drain: 2 * time.Second,
		Build: func(ctx context.Context, src []byte) (*platform.Platform, error) {
			return platform.Compile(ctx, src, ".", platform.DefaultLoadOptions())
		}}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	served := make(chan error, 1)
	go func() { served <- sup.Serve(ctx, ln) }()
	app := "http://" + ln.Addr().String()
	waitFor(t, func() bool { s, _ := get(t, app+"/hello"); return s == "hello from v1" })

	admin := httptest.NewServer((&Admin{Manager: m, Supervisor: sup, Tokens: map[string]string{alice: "alice", bob: "bob"}}).Handler())
	defer admin.Close()
	c := adminClient{t, admin.URL}

	if status, _ := c.do("GET", "/revisions", "wrong-token-0000000", nil); status != 401 {
		t.Fatalf("bad token: %d", status)
	}
	// An invalid document is refused with its report.
	status, body := c.do("POST", "/revisions", alice, map[string]any{"source": `name "x"` + "\n" + `route "r" { method GET path "/r" intent "missing" }`})
	if status != 422 || !strings.Contains(fmt.Sprint(body["report"]), "missing") {
		t.Fatalf("invalid proposal: %d %v", status, body)
	}

	// v2: proposed by alice, diffed, approved by bob (not alice), activated.
	status, body = c.do("POST", "/revisions", alice, map[string]any{"source": appSource("v2"), "message": "greet v2"})
	if status != 201 || body["status"] != StatusPending {
		t.Fatalf("propose: %d %v", status, body)
	}
	v2 := fmt.Sprint(body["id"])
	if !strings.Contains(fmt.Sprint(body["changes"]), "intent hello changed") && !strings.Contains(fmt.Sprint(body["changes"]), "changed") {
		t.Fatalf("diff: %v", body["changes"])
	}
	if status, _ := c.do("POST", "/revisions/"+v2+"/activate", bob, map[string]any{}); status != 409 {
		t.Fatalf("activate before approval: %d", status)
	}
	if status, _ := c.do("POST", "/revisions/"+v2+"/approve", alice, map[string]any{}); status != 403 {
		t.Fatalf("self-approval: %d", status)
	}
	if status, _ := c.do("POST", "/revisions/"+v2+"/approve", bob, map[string]any{"comment": "lgtm"}); status != 200 {
		t.Fatalf("approve: %d", status)
	}

	// Hammer the app during the swap. New connections must never fail: the
	// listener is never closed and in-flight requests finish. Keep-alive
	// clients may race the old generation closing an idle connection (as
	// with any HTTP server shutting down), so they retry an idempotent GET
	// once — what browsers and most HTTP clients do.
	fresh := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
	var failures, retried, total atomic.Int64
	stop := make(chan struct{})
	var wg sync.WaitGroup
	hammer := func(keepAlive bool) {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			total.Add(1)
			if keepAlive {
				if _, err := get(t, app+"/hello"); err != nil {
					retried.Add(1)
					if _, err := get(t, app+"/hello"); err != nil {
						failures.Add(1)
						t.Logf("keep-alive request failed after a retry: %v", err)
					}
				}
				continue
			}
			resp, err := fresh.Get(app + "/hello")
			if err != nil || resp.StatusCode != 200 {
				failures.Add(1)
				t.Logf("fresh-connection request failed: %v", err)
			}
			if resp != nil {
				resp.Body.Close()
			}
		}
	}
	for i := 0; i < 4; i++ {
		wg.Add(2)
		go hammer(true)
		go hammer(false)
	}
	if status, _ := c.do("POST", "/revisions/"+v2+"/activate", bob, map[string]any{}); status != 200 {
		t.Fatalf("activate: %d", status)
	}
	waitFor(t, func() bool { s, _ := get(t, app+"/hello"); return s == "hello from v2" })
	time.Sleep(600 * time.Millisecond) // past the grace period: the old generation drains under load
	close(stop)
	wg.Wait()
	if failures.Load() != 0 || total.Load() < 20 {
		t.Fatalf("%d of %d requests failed during the swap", failures.Load(), total.Load())
	}
	t.Logf("%d requests across the swap, none failed (%d keep-alive retries)", total.Load(), retried.Load())

	// v3 validates but does not build: marked failed, v2 keeps serving.
	status, body = c.do("POST", "/revisions", alice, map[string]any{"source": buildsButFailsToOpen})
	if status != 201 {
		t.Fatalf("propose v3: %d %v", status, body)
	}
	v3 := fmt.Sprint(body["id"])
	c.do("POST", "/revisions/"+v3+"/approve", bob, map[string]any{})
	c.do("POST", "/revisions/"+v3+"/activate", bob, map[string]any{})
	waitFor(t, func() bool { _, b := c.do("GET", "/revisions/"+v3, alice, nil); return b["status"] == StatusFailed })
	if s, err := get(t, app+"/hello"); s != "hello from v2" {
		t.Fatalf("after failed build: %q %v", s, err)
	}
	if _, b := c.do("GET", "/active", alice, nil); b["id"] != v2 || b["serving"] != v2 {
		t.Fatalf("active after failure: %v", b)
	}

	// Roll back to v1.
	if status, b := c.do("POST", "/rollback", bob, map[string]any{"reason": "v2 greeting was wrong"}); status != 200 || b["id"] != v1.ID {
		t.Fatalf("rollback: %d %v", status, b)
	}
	waitFor(t, func() bool { s, _ := get(t, app+"/hello"); return s == "hello from v1" })

	// A source edited in the store cannot be activated.
	v4, _ := m.Propose(ctx, []byte(appSource("v4")), "alice", "")
	m.Approve(ctx, v4.ID, "bob", "")
	tampered, _ := m.Store.Get(ctx, v4.ID)
	tampered.Source = strings.Replace(tampered.Source, "v4", "evil", -1)
	_ = m.Store.Update(ctx, tampered)
	if status, _ := c.do("POST", "/revisions/"+v4.ID+"/activate", bob, map[string]any{}); status != 409 {
		t.Fatalf("tampered activation: %d", status)
	}

	cancel()
	if err := <-served; err != nil {
		t.Fatal(err)
	}
}

func TestSQLStoreRevisions(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "r.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s, err := NewSQLStore(db, "sqlite", "")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	m := newManager(s)
	r1, err := m.Propose(ctx, []byte(appSource("v1")), "alice", "one")
	if err != nil {
		t.Fatal(err)
	}
	r2, _ := m.Propose(ctx, []byte(appSource("v2")), "alice", "two")
	if r1.Seq != 1 || r2.Seq != 2 {
		t.Fatalf("seq: %d %d", r1.Seq, r2.Seq)
	}
	m.Approve(ctx, r1.ID, "bob", "")
	if _, err := m.Activate(ctx, r1.ID, "bob"); err != nil {
		t.Fatal(err)
	}
	active, _ := m.Active(ctx)
	list, _ := s.List(ctx, "hello", 0)
	if active == nil || active.ID != r1.ID || len(list) != 2 || list[0].Seq != 2 {
		t.Fatalf("active/list: %+v %d", active, len(list))
	}
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
