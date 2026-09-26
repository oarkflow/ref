package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/oarkflow/fh"
	"github.com/oarkflow/ref/platform"
)

// The todo application end to end: app.bcl compiled and served in-process on a
// fresh PostgreSQL database. Runs when TEST_POSTGRES_DSN names a server the
// test may create databases on, and skips otherwise.

func TestTodoEndToEnd(t *testing.T) {
	dsn := freshPostgres(t)
	h := startApp(t, "app.bcl", map[string]string{
		"APP_ENV":          "development",
		"DATABASE_URL":     dsn,
		"SESSION_SECRET":   "session-secret-0123456789abcdef-012345",
		"TODO_CACHE_DIR":   t.TempDir(),
		"TODO_SESSION_DIR": t.TempDir(),
		"TODO_QUEUE_DIR":   t.TempDir(),
	})

	anon := h.client(t)
	if status, body := h.do(anon, "GET", "/todos", nil); status != http.StatusUnauthorized {
		t.Fatalf("anonymous list: %d %v, want 401", status, body)
	}
	if status, body := h.do(anon, "POST", "/todos", map[string]any{"title": "sneaky"}); status != http.StatusUnauthorized {
		t.Fatalf("anonymous create: %d %v, want 401", status, body)
	}

	alice := h.client(t)
	if status, body := h.do(alice, "POST", "/auth/register", map[string]any{"email": "alice@example.com", "name": "Alice", "password": "correct-horse-battery"}); status != http.StatusCreated {
		t.Fatalf("register: %d %v", status, body)
	}

	status, body := h.do(alice, "POST", "/todos", map[string]any{"title": "Buy milk", "description": "semi-skimmed"})
	if status != http.StatusCreated {
		t.Fatalf("create: %d %v", status, body)
	}
	todo := field(body, "todo")
	id := fmt.Sprint(todo["id"])
	if todo["title"] != "Buy milk" || id == "" || id == "<nil>" {
		t.Fatalf("created todo %v", body)
	}

	// The business rule the app publishes rejects a title under 3 characters.
	if status, body := h.do(alice, "POST", "/todos", map[string]any{"title": "ab"}); status != http.StatusForbidden || !strings.Contains(fmt.Sprint(body), "at least 3 characters") {
		t.Fatalf("too-short title: %d %v, want 403 with the todo policy's reason", status, body)
	}

	status, body = h.do(alice, "GET", "/todos", nil)
	if status != http.StatusOK || len(listOf(body, "todos")) != 1 {
		t.Fatalf("list: %d %v, want the one todo", status, body)
	}

	status, body = h.do(alice, "PUT", "/todos/"+id, map[string]any{"title": "Buy oat milk", "completed": true})
	if status != http.StatusOK {
		t.Fatalf("update: %d %v", status, body)
	}
	status, body = h.do(alice, "GET", "/todos/"+id, nil)
	if got := field(body, "todo"); status != http.StatusOK || got["title"] != "Buy oat milk" || got["completed"] != true {
		t.Fatalf("get after update: %d %v", status, body)
	}

	// Another user can neither see nor change Alice's todo.
	bob := h.client(t)
	if status, body := h.do(bob, "POST", "/auth/register", map[string]any{"email": "bob@example.com", "name": "Bob", "password": "correct-horse-battery"}); status != http.StatusCreated {
		t.Fatalf("register bob: %d %v", status, body)
	}
	if status, body := h.do(bob, "GET", "/todos/"+id, nil); status != http.StatusNotFound {
		t.Fatalf("bob reading alice's todo: %d %v, want 404", status, body)
	}
	if status, body := h.do(bob, "DELETE", "/todos/"+id, nil); status != http.StatusNotFound {
		t.Fatalf("bob deleting alice's todo: %d %v, want 404", status, body)
	}

	if status, body := h.do(alice, "DELETE", "/todos/"+id, nil); status >= 300 {
		t.Fatalf("delete: %d %v", status, body)
	}
	if status, body := h.do(alice, "GET", "/todos", nil); status != http.StatusOK || len(listOf(body, "todos")) != 0 {
		t.Fatalf("list after delete: %d %v, want none", status, body)
	}
}

// field returns body[key] (or body.data[key]) as an object.
func field(body any, key string) map[string]any {
	m, _ := unwrap(body).(map[string]any)
	if inner, ok := m[key].(map[string]any); ok {
		return inner
	}
	return m
}

func listOf(body any, key string) []any {
	v := unwrap(body)
	if m, ok := v.(map[string]any); ok {
		v = m[key]
	}
	list, _ := v.([]any)
	return list
}

// Harness
// ---------------------------------------------------------------------------

type harness struct {
	base string
}

func startApp(t *testing.T, bclPath string, env map[string]string) *harness {
	t.Helper()
	for k, v := range env {
		t.Setenv(k, v)
	}
	src, err := os.ReadFile(bclPath)
	if err != nil {
		t.Fatal(err)
	}
	p, err := platform.Compile(context.Background(), src, filepath.Dir(bclPath), platform.DefaultLoadOptions())
	if err != nil {
		t.Fatalf("compile %s: %v", bclPath, err)
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
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{Timeout: 15 * time.Second, Jar: jar}
}

func (h *harness) do(c *http.Client, method, path string, body any) (int, any) {
	var reader io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		reader = strings.NewReader(string(raw))
	}
	req, err := http.NewRequest(method, h.base+path, reader)
	if err != nil {
		panic(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var decoded any
	if json.Unmarshal(raw, &decoded) != nil {
		decoded = string(raw)
	}
	return resp.StatusCode, decoded
}

// unwrap strips the {"data": ...} envelope when there is one.
func unwrap(v any) any {
	if m, ok := v.(map[string]any); ok {
		if d, ok := m["data"]; ok {
			return d
		}
	}
	return v
}

// freshPostgres creates an empty database on TEST_POSTGRES_DSN's server and
// drops it when the test ends; it skips the test when the variable is unset.
func freshPostgres(t *testing.T) string {
	t.Helper()
	admin := os.Getenv("TEST_POSTGRES_DSN")
	if admin == "" {
		t.Skip("TEST_POSTGRES_DSN is not set; skipping the PostgreSQL end-to-end test")
	}
	db := openDB(t, admin)
	name := fmt.Sprintf("reftest_%d", time.Now().UnixNano())
	if _, err := db.Exec("CREATE DATABASE " + name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Exec("DROP DATABASE IF EXISTS " + name + " WITH (FORCE)") })
	u, err := url.Parse(admin)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	return u.String()
}

func openDB(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}
