package starter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/oarkflow/fh"
	"github.com/oarkflow/ref/platform"

	_ "modernc.org/sqlite"
)

// The starter end to end, on an in-process SQLite database:
//   - session-cookie login, and RBAC that actually denies the wrong role
//     (an authz {roles ["admin"]} route refuses a signed-in "user" account)
//   - notify.welcome (bcl/03_intents.bcl) reused, unduplicated, across a
//     synchronous route and an asynchronous queue worker
//   - the route_group in bcl/04_routes.bcl really does apply its shared
//     session/auth/authz to every route nested inside it

type harness struct {
	base string
}

func startApp(t *testing.T, bclDir string, env map[string]string) *harness {
	t.Helper()
	for k, v := range env {
		t.Setenv(k, v)
	}
	p, err := platform.LoadDir(context.Background(), bclDir, platform.DefaultLoadOptions())
	if err != nil {
		t.Fatalf("compile %s: %v", bclDir, err)
	}
	t.Cleanup(func() { _ = p.Close() })

	app := fh.NewFast()
	if err := p.Mount(app); err != nil {
		t.Fatalf("mount: %v", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	served := make(chan struct{})
	go func() {
		defer close(served)
		_ = app.Serve(listener)
	}()
	t.Cleanup(func() {
		_ = app.ShutdownWithTimeout(2 * time.Second)
		_ = listener.Close()
		<-served
	})
	return &harness{base: "http://" + listener.Addr().String()}
}

// client is one browser: its own cookie jar, so its own session.
func (h *harness) client(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{Timeout: 15 * time.Second, Jar: jar}
}

func (h *harness) do(t *testing.T, c *http.Client, method, path string, body any) (int, map[string]any) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, h.base+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var decoded map[string]any
	_ = json.Unmarshal(raw, &decoded)
	return resp.StatusCode, decoded
}

// welcomeRecorder stands in for the real notification service in
// bcl/01_resources.bcl's "notifications" resource: it records every delivery
// so the test can prove both transports reached the same place.
type welcomeRecorder struct {
	mu   sync.Mutex
	seen []map[string]any
}

func (r *welcomeRecorder) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(req.Body).Decode(&body)
		r.mu.Lock()
		r.seen = append(r.seen, body)
		r.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}
}

func (r *welcomeRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.seen)
}

func (r *welcomeRecorder) hasEmail(email string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range r.seen {
		if s["email"] == email {
			return true
		}
	}
	return false
}

func TestStarterEndToEnd(t *testing.T) {
	recorder := &welcomeRecorder{}
	notify := httptest.NewServer(recorder.handler())
	defer notify.Close()

	dir := t.TempDir()
	bclDir, err := filepath.Abs("bcl")
	if err != nil {
		t.Fatal(err)
	}

	h := startApp(t, bclDir, map[string]string{
		"APP_ENV":        "development",
		"DB_DSN":         "file:" + filepath.Join(dir, "app.db") + "?_pragma=busy_timeout(5000)",
		"SESSION_SECRET": "test-session-secret-0123456789ab-0123456789ab",
		"WEBHOOK_SECRET": "test-webhook-secret-0123456789ab",
		"NOTIFY_URL":     notify.URL,
	})

	if status, body := h.do(t, h.client(t), "GET", "/health", nil); status != 200 {
		t.Fatalf("GET /health = %d %v", status, body)
	}

	// --- The seeded admin account (bcl/01_resources.bcl's migration) -------
	admin := h.client(t)
	status, resp := h.do(t, admin, "POST", "/login", map[string]any{
		"email": "admin@example.com", "password": "Password123!",
	})
	if status != 200 {
		t.Fatalf("admin login = %d %v", status, resp)
	}

	// Transport 1: the synchronous route, inside a route_group, requiring
	// the admin role.
	if status, resp := h.do(t, admin, "POST", "/api/v1/notify/welcome", map[string]any{
		"email": "sync@example.com", "name": "Sync",
	}); status != 200 {
		t.Fatalf("POST /api/v1/notify/welcome = %d %v", status, resp)
	}
	if !recorder.hasEmail("sync@example.com") {
		t.Fatalf("the synchronous route did not deliver: %+v", recorder.seen)
	}

	// Transport 2: the same business logic, queued and run by
	// bcl/05_workers.bcl's worker instead of inline in the request.
	if status, resp := h.do(t, admin, "POST", "/api/v1/notify/welcome-async", map[string]any{
		"email": "async@example.com", "name": "Async",
	}); status != 202 {
		t.Fatalf("POST /api/v1/notify/welcome-async = %d %v", status, resp)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && !recorder.hasEmail("async@example.com") {
		time.Sleep(50 * time.Millisecond)
	}
	if !recorder.hasEmail("async@example.com") {
		t.Fatalf("the worker never delivered the queued job: %+v", recorder.seen)
	}
	if recorder.count() != 2 {
		t.Fatalf("expected exactly 2 deliveries (one per transport), got %d: %+v", recorder.count(), recorder.seen)
	}

	// The route_group's own admin-only route.
	if status, resp := h.do(t, admin, "GET", "/api/v1/users", nil); status != 200 {
		t.Fatalf("GET /api/v1/users = %d %v", status, resp)
	} else if _, ok := resp["users"]; !ok {
		t.Fatalf("GET /api/v1/users response had no users key: %v", resp)
	}

	// --- A freshly registered, ordinary "user" account ----------------------
	member := h.client(t)
	if status, resp := h.do(t, member, "POST", "/register", map[string]any{
		"email": "member@example.com", "name": "Member", "password": "correct horse battery",
	}); status != 201 {
		t.Fatalf("register = %d %v", status, resp)
	}

	// Admin-only: refused.
	if status, resp := h.do(t, member, "GET", "/api/v1/users", nil); status != 403 {
		t.Fatalf("GET /api/v1/users as a member = %d %v, want 403", status, resp)
	}

	// /me has no authz block of its own, so it inherits the group's
	// roles ["user"] default — this is what proves route_group inheritance
	// actually applies, not just that an override can replace it.
	if status, resp := h.do(t, member, "GET", "/api/v1/me", nil); status != 200 {
		t.Fatalf("GET /api/v1/me as a member = %d %v, want 200 (inherited from the group)", status, resp)
	} else if dig(resp, "me", "email") != "member@example.com" {
		t.Fatalf("GET /api/v1/me returned the wrong caller: %v", resp)
	}

	// The route_group's shared authz requires "user"; notify.welcome's own
	// override requires "admin" — proving a nested route's own authz wins
	// over the group's default (platform/route_group.go).
	if status, resp := h.do(t, member, "POST", "/api/v1/notify/welcome", map[string]any{
		"email": "member-triggered@example.com",
	}); status != 403 {
		t.Fatalf("POST /api/v1/notify/welcome as a member = %d %v, want 403 (route overrides group authz)", status, resp)
	}

	// Anonymous is refused before any intent runs.
	anon := h.client(t)
	if status, resp := h.do(t, anon, "GET", "/api/v1/users", nil); status != 401 {
		t.Fatalf("GET /api/v1/users anonymous = %d %v, want 401", status, resp)
	}
}

// TestOrdersWorkflowExample exercises bcl/09_workflow_example.bcl end to
// end: flow.branch's tier classification, decision.table + flow.switch's
// status machine (including its "invalid transition" rejection and its
// cancel path), and the priority_shipping feature flag actually changing
// which switch case runs for the same nominal transition, depending on the
// caller's role.
func TestOrdersWorkflowExample(t *testing.T) {
	dir := t.TempDir()
	bclDir, err := filepath.Abs("bcl")
	if err != nil {
		t.Fatal(err)
	}
	h := startApp(t, bclDir, map[string]string{
		"APP_ENV":        "development",
		"DB_DSN":         "file:" + filepath.Join(dir, "app.db") + "?_pragma=busy_timeout(5000)",
		"SESSION_SECRET": "test-session-secret-0123456789ab-0123456789ab",
		"WEBHOOK_SECRET": "test-webhook-secret-0123456789ab",
		"NOTIFY_URL":     "http://127.0.0.1:0", // unused by this test
	})

	admin := h.client(t)
	if status, resp := h.do(t, admin, "POST", "/login", map[string]any{
		"email": "admin@example.com", "password": "Password123!",
	}); status != 200 {
		t.Fatalf("admin login = %d %v", status, resp)
	}

	member := h.client(t)
	if status, resp := h.do(t, member, "POST", "/register", map[string]any{
		"email": "buyer@example.com", "name": "Buyer", "password": "correct horse battery",
	}); status != 201 {
		t.Fatalf("register = %d %v", status, resp)
	}

	// flow.branch: amount > 500 takes the "large" case, <= 500 takes "small".
	_, small := h.do(t, member, "POST", "/api/v1/orders", map[string]any{"amount": 10})
	if tier := dig(small, "orders", 0, "tier"); tier != "small" {
		t.Fatalf("a 10-unit order classified as %v, want \"small\": %v", tier, small)
	}
	_, large := h.do(t, member, "POST", "/api/v1/orders", map[string]any{"amount": 1000})
	if tier := dig(large, "orders", 0, "tier"); tier != "large" {
		t.Fatalf("a 1000-unit order classified as %v, want \"large\": %v", tier, large)
	}
	largeID := fmt.Sprintf("%v", dig(large, "orders", 0, "id"))

	// decision.table + flow.switch: pending -> shipped directly is not a
	// legal transition, and reject-invalid's validate.expression must catch
	// it as 422, before any UPDATE runs.
	if status, resp := h.do(t, member, "POST", "/api/v1/orders/"+largeID+"/transition", map[string]any{"status": "shipped"}); status != 422 {
		t.Fatalf("pending->shipped = %d %v, want 422 (not in the workflow)", status, resp)
	}

	// The priority_shipping flag's "admin_preview" rule only matches an
	// admin caller, so the same >500 pending->paid transition takes the
	// plain "apply" switch case for a member (no priority) and the
	// "apply_priority" case for an admin (priority stamped) — a feature
	// flag genuinely changing which case runs, not just a value.
	if status, resp := h.do(t, member, "POST", "/api/v1/orders/"+largeID+"/transition", map[string]any{"status": "paid"}); status != 200 {
		t.Fatalf("member pending->paid = %d %v", status, resp)
	}
	if status, order := h.do(t, member, "GET", "/api/v1/orders/"+largeID, nil); status != 200 {
		t.Fatalf("GET order = %d %v", status, order)
	} else if p := dig(order, "order", "priority"); p != float64(0) {
		t.Fatalf("a member's order was stamped priority=%v, want 0 (the flag's admin_preview rule shouldn't match a member)", p)
	}

	// orders.get/orders.transition scope every query by owner_id, so this
	// has to be the admin's own order — the flag's "admin_preview" rule
	// matches the caller's role, not who owns the order being transitioned.
	_, large2 := h.do(t, admin, "POST", "/api/v1/orders", map[string]any{"amount": 900})
	large2ID := fmt.Sprintf("%v", dig(large2, "orders", 0, "id"))
	if status, resp := h.do(t, admin, "POST", "/api/v1/orders/"+large2ID+"/transition", map[string]any{"status": "paid"}); status != 200 {
		t.Fatalf("admin pending->paid = %d %v", status, resp)
	}
	if status, order := h.do(t, admin, "GET", "/api/v1/orders/"+large2ID, nil); status != 200 {
		t.Fatalf("GET order = %d %v", status, order)
	} else if p := dig(order, "order", "priority"); p != float64(1) {
		t.Fatalf("an admin-applied >500 transition was stamped priority=%v, want 1 (the flag's admin_preview rule)", p)
	}

	// The cancel case: legal from any non-delivered status, regardless of
	// the `status` field sent — orders.cancel always sets 'cancelled'.
	_, small2 := h.do(t, member, "POST", "/api/v1/orders", map[string]any{"amount": 5})
	small2ID := fmt.Sprintf("%v", dig(small2, "orders", 0, "id"))
	if status, resp := h.do(t, member, "POST", "/api/v1/orders/"+small2ID+"/transition", map[string]any{"status": "cancelled"}); status != 200 {
		t.Fatalf("cancel = %d %v", status, resp)
	}
	if status, order := h.do(t, member, "GET", "/api/v1/orders/"+small2ID, nil); status != 200 || dig(order, "order", "status") != "cancelled" {
		t.Fatalf("GET order after cancel = %d %v, want status cancelled", status, order)
	}
}

// dig reads a nested value by a chain of string keys (into a map) or ints
// (into a slice) — database.query's RETURNING results come back as a
// one-row slice, so a chain often needs to step through one.
func dig(v any, path ...any) any {
	for _, key := range path {
		switch k := key.(type) {
		case string:
			m, ok := v.(map[string]any)
			if !ok {
				return nil
			}
			v = m[k]
		case int:
			s, ok := v.([]any)
			if !ok || k < 0 || k >= len(s) {
				return nil
			}
			v = s[k]
		}
	}
	return v
}

func TestMain(m *testing.M) {
	// bcl/*.bcl resolve relative paths (the sqlite dsn's directory, if any)
	// against the current working directory, which go test already sets to
	// this package's directory — nothing to change here, this only documents
	// the assumption for anyone moving the test.
	os.Exit(m.Run())
}
