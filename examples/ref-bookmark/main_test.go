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

// The bookmark service end to end: app.bcl compiled and served in-process on a
// fresh PostgreSQL database. Runs when TEST_POSTGRES_DSN names a server the
// test may create databases on, and skips otherwise.

func TestBookmarksEndToEnd(t *testing.T) {
	dsn := freshPostgres(t)
	config, err := filepath.Abs("app.bcl")
	if err != nil {
		t.Fatal(err)
	}
	// The job queue writes under .data/ relative to the working directory.
	t.Chdir(t.TempDir())
	h := startApp(t, config, map[string]string{
		"APP_ENV":         "development",
		"DATABASE_DRIVER": "pgx",
		"DATABASE_URL":    dsn,
		"JWT_SECRET":      "jwt-secret-0123456789abcdef-0123456789",
	})

	anon := h.client(t)
	if status, body := h.do(anon, "GET", "/bookmarks", nil); status != http.StatusUnauthorized {
		t.Fatalf("anonymous list: %d %v, want 401", status, body)
	}
	if status, body := h.do(anon, "POST", "/bookmarks", map[string]any{"url": "https://example.com", "title": "x"}); status != http.StatusUnauthorized {
		t.Fatalf("anonymous create: %d %v, want 401", status, body)
	}

	ada := h.client(t)
	token := register(t, h, ada, "ada@example.com")

	// The API client journey, with the bearer token and no cookie.
	api := h.client(t)
	api.Jar = nil
	api.Transport = bearer(token)

	status, body := h.do(api, "POST", "/bookmarks", map[string]any{"url": "https://go.dev/doc", "title": "Go docs", "tags": []string{"go"}})
	if status != http.StatusCreated {
		t.Fatalf("create: %d %v", status, body)
	}
	created := object(body, "bookmark_row")
	id := fmt.Sprint(created["id"])
	if created["title"] != "Go docs" || id == "" || id == "<nil>" {
		t.Fatalf("created bookmark %v", body)
	}

	status, body = h.do(ada, "GET", "/bookmarks", nil)
	if status != http.StatusOK || len(list(body)) != 1 {
		t.Fatalf("list with the session cookie: %d %v, want the one bookmark", status, body)
	}

	status, body = h.do(api, "PUT", "/bookmarks/"+id, map[string]any{"title": "The Go documentation"})
	if status != http.StatusOK {
		t.Fatalf("update: %d %v", status, body)
	}
	status, body = h.do(api, "GET", "/bookmarks/"+id, nil)
	if status != http.StatusOK || !strings.Contains(fmt.Sprint(body), "The Go documentation") {
		t.Fatalf("get after update: %d %v", status, body)
	}

	// Another account can neither read nor delete Ada's bookmark.
	eve := h.client(t)
	register(t, h, eve, "eve@example.com")
	// bookmark.get answers an id it does not find for the caller with an empty
	// list rather than a 404; either way nothing of Ada's may show.
	if status, body := h.do(eve, "GET", "/bookmarks/"+id, nil); status >= 500 || len(list(body)) != 0 || strings.Contains(fmt.Sprint(body), "go.dev") {
		t.Fatalf("eve reading ada's bookmark: %d %v, want nothing", status, body)
	}
	if status, body := h.do(eve, "DELETE", "/bookmarks/"+id, nil); status >= 500 || len(list(body)) != 0 {
		t.Fatalf("eve deleting ada's bookmark: %d %v, want nothing deleted", status, body)
	}
	if status, body := h.do(api, "GET", "/bookmarks/"+id, nil); status != http.StatusOK || len(list(body)) != 1 {
		t.Fatalf("ada's bookmark after eve's delete: %d %v, want it still there", status, body)
	}

	status, body = h.do(api, "DELETE", "/bookmarks/"+id, nil)
	if status >= 300 {
		t.Fatalf("delete: %d %v", status, body)
	}
	if status, body := h.do(api, "GET", "/bookmarks", nil); status != http.StatusOK || len(list(body)) != 0 {
		t.Fatalf("list after delete: %d %v, want none", status, body)
	}
}

// register creates an account, logs c in and returns the JWT login issued.
func register(t *testing.T, h *harness, c *http.Client, email string) string {
	t.Helper()
	if status, body := h.do(c, "POST", "/auth/register", map[string]any{"email": email, "name": "User", "password": "correct horse battery staple"}); status >= 300 {
		t.Fatalf("register %s: %d %v", email, status, body)
	}
	status, body := h.do(c, "POST", "/auth/login", map[string]any{"email": email, "password": "correct horse battery staple"})
	if status != http.StatusOK {
		t.Fatalf("login %s: %d %v", email, status, body)
	}
	token := findString(body, "token")
	if token == "" {
		t.Fatalf("login returned no token: %v", body)
	}
	return token
}

type bearer string

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+string(b))
	return http.DefaultTransport.RoundTrip(r)
}

// findString finds the first string under key anywhere in v.
func findString(v any, key string) string {
	switch x := v.(type) {
	case map[string]any:
		if s, ok := x[key].(string); ok {
			return s
		}
		for _, item := range x {
			if s := findString(item, key); s != "" {
				return s
			}
		}
	case []any:
		for _, item := range x {
			if s := findString(item, key); s != "" {
				return s
			}
		}
	}
	return ""
}

// object returns body[key], or the body itself, as one object; a one-row list
// is its row.
func object(body any, key string) map[string]any {
	v := unwrap(body)
	if l, ok := v.([]any); ok && len(l) > 0 {
		v = l[0]
	}
	m, _ := v.(map[string]any)
	if inner, ok := m[key].(map[string]any); ok {
		return inner
	}
	return m
}

// list returns the first array in the body.
func list(body any) []any {
	v := unwrap(body)
	if l, ok := v.([]any); ok {
		return l
	}
	if m, ok := v.(map[string]any); ok {
		for _, item := range m {
			if l, ok := item.([]any); ok {
				return l
			}
		}
	}
	return nil
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
