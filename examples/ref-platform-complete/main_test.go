package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/oarkflow/fh"
	"github.com/oarkflow/ref/platform"
)

// The webhook intake application end to end: app.bcl compiled and served
// in-process on a fresh PostgreSQL database, delivering to an httptest
// receiver. Runs when TEST_POSTGRES_DSN names a server the test may create
// databases on, and skips otherwise.

const apiKey = "0123456789abcdef0123456789abcdef"

func TestWebhookEndToEnd(t *testing.T) {
	dsn := freshPostgres(t)
	var (
		mu       sync.Mutex
		received []map[string]any
	)
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		body["_event_type_header"] = r.Header.Get("X-Event-Type")
		mu.Lock()
		received = append(received, body)
		mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(receiver.Close)
	target, _ := url.Parse(receiver.URL)

	base := startApp(t, "app.bcl", map[string]string{
		"DATABASE_URL":          dsn,
		"WEBHOOK_API_KEY":       apiKey,
		"WEBHOOK_TARGET_URL":    receiver.URL + "/events",
		"WEBHOOK_ALLOWED_HOST":  target.Hostname(),
		"WEBHOOK_ALLOW_PRIVATE": "true",
		"WEBHOOK_QUEUE_DIR":     t.TempDir(),
	})

	event := map[string]any{
		"event_id":   "b77a8b99-d26f-477c-8bf3-349b5b14ec03",
		"event_type": "invoice.paid",
		"data":       map[string]any{"invoice_id": "inv_123", "amount": 4200},
	}

	// Without the API key, or with a wrong one, nothing is accepted.
	for _, key := range []string{"", "wrong-key-000000000000"} {
		if status, body := call(t, base, "POST", "/events", key, "k-1", event); status != http.StatusUnauthorized {
			t.Fatalf("POST /events with key %q: %d %v, want 401", key, status, body)
		}
	}
	if status, body := call(t, base, "GET", "/events/"+event["event_id"].(string), "wrong-key-000000000000", "", nil); status != http.StatusUnauthorized {
		t.Fatalf("GET /events with a wrong key: %d %v, want 401", status, body)
	}

	status, body := call(t, base, "POST", "/events", apiKey, "invoice-paid-b77a8b99", event)
	if status != http.StatusAccepted {
		t.Fatalf("accept: %d %v", status, body)
	}

	// The job delivers the event to the receiver and records the outcome.
	deadline := time.Now().Add(20 * time.Second)
	var state map[string]any
	for {
		_, body := call(t, base, "GET", "/events/"+event["event_id"].(string), apiKey, "", nil)
		state = firstEvent(body)
		if state["state"] == "delivered" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the event was not delivered; its status is %v", body)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if fmt.Sprint(state["response_status"]) != "202" {
		t.Fatalf("recorded response status %v, want 202", state["response_status"])
	}
	mu.Lock()
	if len(received) != 1 || received[0]["event_id"] != event["event_id"] || received[0]["_event_type_header"] != "invoice.paid" {
		t.Fatalf("the receiver got %v, want the one event with its type header", received)
	}
	mu.Unlock()

	// The same Idempotency-Key replays the first answer without a second
	// delivery.
	if status, body := call(t, base, "POST", "/events", apiKey, "invoice-paid-b77a8b99", event); status != http.StatusAccepted {
		t.Fatalf("idempotent replay: %d %v, want the original 202", status, body)
	}
	time.Sleep(500 * time.Millisecond) // give a wrongly queued second delivery the chance to show
	mu.Lock()
	if len(received) != 1 {
		t.Fatalf("a replayed request was delivered again: %d deliveries", len(received))
	}
	mu.Unlock()
}

func firstEvent(body any) map[string]any {
	m, _ := body.(map[string]any)
	if d, ok := m["data"]; ok {
		m, _ = d.(map[string]any)
	}
	list, _ := m["events"].([]any)
	if len(list) == 0 {
		return nil
	}
	first, _ := list[0].(map[string]any)
	return first
}

func call(t *testing.T, base, method, path, key, idempotency string, body any) (int, any) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		reader = strings.NewReader(string(raw))
	}
	req, err := http.NewRequest(method, base+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("X-API-Key", key)
	}
	if idempotency != "" {
		req.Header.Set("Idempotency-Key", idempotency)
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var decoded any
	if json.Unmarshal(raw, &decoded) != nil {
		decoded = string(raw)
	}
	return resp.StatusCode, decoded
}

func startApp(t *testing.T, bclPath string, env map[string]string) string {
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
	return "http://" + listener.Addr().String()
}

// freshPostgres creates an empty database on TEST_POSTGRES_DSN's server and
// drops it when the test ends; it skips the test when the variable is unset.
func freshPostgres(t *testing.T) string {
	t.Helper()
	admin := os.Getenv("TEST_POSTGRES_DSN")
	if admin == "" {
		t.Skip("TEST_POSTGRES_DSN is not set; skipping the PostgreSQL end-to-end test")
	}
	db, err := sql.Open("pgx", admin)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("reftest_%d", time.Now().UnixNano())
	if _, err := db.Exec("CREATE DATABASE " + name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec("DROP DATABASE IF EXISTS " + name + " WITH (FORCE)")
		_ = db.Close()
	})
	u, err := url.Parse(admin)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	return u.String()
}
