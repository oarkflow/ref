package main

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/oarkflow/fh"
	"github.com/oarkflow/ref/platform"
)

type journeyClient struct {
	t      *testing.T
	base   string
	client *http.Client
}

func startCompleteApp(t *testing.T) string {
	t.Helper()
	t.Setenv("COMPLETE_SESSION_SECRET", strings.Repeat("s", 48))
	t.Setenv("COMPLETE_QUEUE_DIR", t.TempDir())
	t.Setenv("COMPLETE_DATABASE_URL", "file:"+filepath.Join(t.TempDir(), "complete.db")+"?cache=shared&mode=rwc")
	if err := prepareLocalPaths(); err != nil {
		t.Fatal(err)
	}
	app, err := platform.LoadFile(context.Background(), "app.bcl", platform.DefaultLoadOptions())
	if err != nil {
		t.Fatal(err)
	}
	server := fh.NewFast()
	if err := app.Mount(server); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	served := make(chan struct{})
	go func() {
		defer close(served)
		_ = server.Serve(listener)
	}()
	t.Cleanup(func() {
		_ = server.ShutdownWithTimeout(3 * time.Second)
		<-served
		_ = app.Close()
	})
	return "http://" + listener.Addr().String()
}

func newJourneyClient(t *testing.T, base string) *journeyClient {
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &journeyClient{t: t, base: base, client: &http.Client{Jar: jar, Timeout: 10 * time.Second}}
}

func (c *journeyClient) do(method, path, body string, headers ...string) (int, string) {
	c.t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, c.base+path, reader)
	if err != nil {
		c.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := c.client.Do(req)
	if err != nil {
		c.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(data)
}

func (c *journeyClient) json(method, path, body string, wantStatus int, headers ...string) map[string]any {
	c.t.Helper()
	status, raw := c.do(method, path, body, headers...)
	if status != wantStatus {
		c.t.Fatalf("%s %s: status %d, want %d: %s", method, path, status, wantStatus, raw)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		c.t.Fatalf("%s %s: decode %q: %v", method, path, raw, err)
	}
	return out
}

// TestCompleteJourney drives the README walkthrough over HTTP: sessions,
// the compiled branch and its stream projection, route idempotency, RBAC,
// the SQLite order write, and the durable order.fulfil process parking at
// the human approval task until an approver decides it.
func TestCompleteJourney(t *testing.T) {
	base := startCompleteApp(t)
	alice := newJourneyClient(t, base)
	bob := newJourneyClient(t, base)

	alice.json("POST", "/session", `{"user":"alice","roles":["customer"]}`, 200)
	if who := alice.json("GET", "/session", "", 200); who["user_id"] != "alice" {
		t.Fatalf("session identity = %v", who)
	}

	if got := alice.json("POST", "/flow/route", `{"subject":"large-ticket","amount":250}`, 200); got["result"] != "large" {
		t.Fatalf("flow.route = %v, want large", got)
	}
	if status, raw := alice.do("POST", "/flow/stream", `{"subject":"stream-ticket","amount":25}`); status != 200 ||
		!strings.Contains(raw, "event: result") || !strings.Contains(raw, `"result":"small"`) {
		t.Fatalf("flow.stream = %d %q", status, raw)
	}

	// Anonymous callers are denied by route RBAC.
	anon := newJourneyClient(t, base)
	if status, raw := anon.do("POST", "/guarded", `{}`); status < 400 || status >= 500 {
		t.Fatalf("anonymous /guarded = %d %s, want 4xx", status, raw)
	}
	if status, raw := alice.do("POST", "/cache", `{"key":"configuration"}`); status != 422 {
		t.Fatalf("/cache without Idempotency-Key = %d %s, want 422", status, raw)
	}
	first := alice.json("POST", "/cache", `{"key":"configuration"}`, 200, "Idempotency-Key", "cache-1")
	second := alice.json("POST", "/cache", `{"key":"configuration"}`, 200, "Idempotency-Key", "cache-1")
	if first["cached"].(map[string]any)["found"] != false || second["cached"].(map[string]any)["found"] != false {
		t.Fatalf("idempotent replay re-executed the graph: first=%v second=%v", first, second)
	}
	if third := alice.json("POST", "/cache", `{"key":"configuration"}`, 200, "Idempotency-Key", "cache-2"); third["cached"].(map[string]any)["found"] != true {
		t.Fatalf("new key did not observe the cached value: %v", third)
	}

	order := `{"id":"order-1","amount":2500,"note":"needs review"}`
	created := alice.json("POST", "/orders", order, 201, "Idempotency-Key", "order-1")
	replayed := alice.json("POST", "/orders", order, 201, "Idempotency-Key", "order-1")
	run := created["run"].(map[string]any)
	runID, _ := run["run_id"].(string)
	if runID == "" || run["process"] != "order.fulfil" || created["stored"] != float64(1) {
		t.Fatalf("order.create = %v", created)
	}
	if replayed["run"].(map[string]any)["run_id"] != runID {
		t.Fatalf("idempotent replay started a new run: %v", replayed)
	}
	orders := alice.json("GET", "/orders", "", 200)["orders"].([]any)
	if len(orders) != 1 || orders[0].(map[string]any)["id"] != "order-1" || orders[0].(map[string]any)["customer_id"] != "alice" {
		t.Fatalf("orders = %v", orders)
	}

	// An amount over 1000 parks at the approval task; a customer cannot decide it.
	bob.json("POST", "/session", `{"user":"bob","roles":["approver"]}`, 200)
	var taskID string
	deadline := time.Now().Add(10 * time.Second)
	for taskID == "" && time.Now().Before(deadline) {
		for _, task := range bob.json("GET", "/tasks", "", 200)["tasks"].([]any) {
			if task := task.(map[string]any); task["run_id"] == runID && task["step"] == "approve" {
				taskID, _ = task["task_id"].(string)
			}
		}
		if taskID == "" {
			time.Sleep(50 * time.Millisecond)
		}
	}
	if taskID == "" {
		t.Fatalf("no approval task for run %s", runID)
	}
	if status, raw := alice.do("POST", "/tasks/"+taskID+"/decide", `{"action":"approve"}`); status < 400 || status >= 500 {
		t.Fatalf("customer decided task: %d %s", status, raw)
	}
	bob.json("POST", "/tasks/"+taskID+"/decide", `{"action":"approve","note":"reviewed"}`, 200)

	var status map[string]any
	for time.Now().Before(deadline) {
		status = alice.json("GET", "/runs/"+runID, "", 200)
		if status["status"] == "completed" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if status["status"] != "completed" || status["output"] != "finished" {
		t.Fatalf("run %s did not finish through approval: %v", runID, status)
	}
}
