package platform

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const integrationApp = `
name "integrations"

resource "upstream" {
  kind "service.http"
  config {
    base_url env.required("UPSTREAM_URL")
    allowed_hosts ["127.0.0.1"]
    allow_private_networks true
    timeout 5s
  }
}

resource "orchestrator" {
  kind "workflow.http"
  config { service "upstream" }
}

resource "db" {
  kind "database.sql"
  config { driver "sqlite" dsn env.required("INTEG_DSN") }
}

resource "kb" {
  kind "search.sql"
  config { database "db" }
}

intent "claims.submit" {
  response "reply"
  node "reply" {
    family grpc
    resource "upstream"
    requires [input]
    provides [reply]
    config { service "billing.v1.ClaimService" method "SubmitClaim" request_fact "input" headers { x-request-source "ref" } }
  }
}

intent "events.deliver" {
  response "delivery"
  node "delivery" {
    family webhook
    resource "upstream"
    requires [input]
    provides [delivery]
    config {
      url "/hooks/claims"
      event_type "claim.submitted"
      payload_fact "input"
      id_fact "input.claim_id"
      secret env.required("HOOK_SECRET")
    }
  }
}

intent "kb.index" {
  response "ok"
  node "ok" {
    uses "search.index"
    resource "kb"
    requires [input]
    provides [ok]
    config { collection "policies" documents_fact "input.docs" }
  }
}

intent "kb.ask" {
  response "grounding"
  node "grounding" {
    family rag
    resource "kb"
    requires [input]
    provides [grounding]
    config { collection "policies" text_fact "input.question" limit 3 }
  }
}

intent "wf.start" {
  response "run"
  node "run" {
    family workflow
    resource "orchestrator"
    requires [input]
    provides [run]
    config { workflow "prior-auth" idempotency_key "{{ input.request_id }}" }
  }
}

intent "wf.signal" {
  response "ack"
  node "ack" {
    uses "workflow.signal"
    resource "orchestrator"
    requires [input]
    provides [ack]
    config { run_fact "input.run_id" signal "approve" payload_fact "input" }
  }
}

intent "wf.status" {
  response "run"
  node "run" {
    uses "workflow.status"
    resource "orchestrator"
    requires [input]
    provides [run]
    config { run_fact "input.run_id" }
  }
}

intent "report.progress" {
  response "report"
  node "started" {
    family stream
    requires [input]
    provides [started]
    config { event "progress" data "starting {{ input.name }}" }
  }
  node "rows" {
    uses "constant"
    requires [started]
    provides [rows]
    config { value [1, 2, 3] }
  }
  node "counted" {
    family stream
    requires [rows]
    provides [counted]
    config { event "progress" data_fact "rows" }
  }
  node "report" {
    uses "collect"
    requires [counted, rows]
    provides [report]
  }
}

intent "report.fail" {
  response "never"
  node "started" {
    family stream
    requires [input]
    provides [started]
    config { event "progress" data "working" }
  }
  # Declared pure: as the decision "deny" defaults to, it would refuse the
  # request before anything streamed. This case is a failure mid-stream.
  node "never" {
    uses "deny"
    kind pure
    requires [started]
    provides [never]
    config { message "not allowed" }
  }
}

route "claims.submit" { method POST path "/claims" intent "claims.submit" }
route "events.deliver" { method POST path "/deliver" intent "events.deliver" }
route "kb.index" { method POST path "/kb" intent "kb.index" }
route "kb.ask" { method POST path "/kb/ask" intent "kb.ask" }
route "wf.start" { method POST path "/wf" intent "wf.start" }
route "wf.signal" { method POST path "/wf/signal" intent "wf.signal" }
route "wf.status" { method POST path "/wf/status" intent "wf.status" }
route "report.progress" { method POST path "/report" intent "report.progress" mode "stream" }
route "report.progress.json" { method POST path "/report.json" intent "report.progress" }
route "report.fail" { method POST path "/report/fail" intent "report.fail" mode "stream" }
`

// fakeUpstream plays a Connect gRPC server, a webhook receiver and a REST
// workflow orchestrator.
type fakeUpstream struct {
	mu       sync.Mutex
	secret   []byte
	webhooks []map[string]any
	signals  []string
	starts   []string
}

func (f *fakeUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	w.Header().Set("Content-Type", "application/json")
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.URL.Path == "/billing.v1.ClaimService/SubmitClaim":
		var msg map[string]any
		_ = json.Unmarshal(body, &msg)
		if r.Header.Get("Connect-Protocol-Version") != "1" || r.Header.Get("X-Request-Source") != "ref" {
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"code":"invalid_argument","message":"missing connect headers"}`))
			return
		}
		if msg["patient_id"] == "missing" {
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"code":"not_found","message":"patient not found"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"claimId": "CLM-" + Stringify(msg["patient_id"]), "accepted": true})
	case r.URL.Path == "/hooks/claims":
		id, ts, sig := r.Header.Get("webhook-id"), r.Header.Get("webhook-timestamp"), r.Header.Get("webhook-signature")
		mac := hmac.New(sha256.New, f.secret)
		mac.Write([]byte(id + "." + ts + "."))
		mac.Write(body)
		if sig != "v1,"+base64.StdEncoding.EncodeToString(mac.Sum(nil)) {
			w.WriteHeader(401)
			return
		}
		var event map[string]any
		_ = json.Unmarshal(body, &event)
		event["_id"] = id
		f.webhooks = append(f.webhooks, event)
		w.WriteHeader(204)
	case r.URL.Path == "/workflows/prior-auth/runs" && r.Method == http.MethodPost:
		f.starts = append(f.starts, r.Header.Get("Idempotency-Key"))
		_, _ = w.Write([]byte(`{"id":"run-1","status":"running","version":1}`))
	case r.URL.Path == "/runs/run-1/signals/approve":
		f.signals = append(f.signals, "approve")
		w.WriteHeader(202)
	case r.URL.Path == "/runs/run-1" && r.Method == http.MethodGet:
		_, _ = w.Write([]byte(`{"id":"run-1","workflow":"prior-auth","status":"completed","result":{"approved":true}}`))
	case strings.HasPrefix(r.URL.Path, "/runs/"):
		w.WriteHeader(404)
		_, _ = w.Write([]byte(`{"error":"no such run"}`))
	default:
		w.WriteHeader(404)
	}
}

func newIntegrationHarness(t *testing.T) (*appHarness, *fakeUpstream) {
	t.Helper()
	secret := strings.Repeat("s", 32)
	fake := &fakeUpstream{secret: []byte(secret)}
	upstream := httptest.NewServer(fake)
	t.Cleanup(upstream.Close)
	dir := t.TempDir()
	path := filepath.Join(dir, "app.bcl")
	if err := os.WriteFile(path, []byte(integrationApp), 0o600); err != nil {
		t.Fatal(err)
	}
	h := newAppHarness(t, path, map[string]string{
		"UPSTREAM_URL": upstream.URL,
		"INTEG_DSN":    "file:" + filepath.Join(dir, "integ.db"),
		"HOOK_SECRET":  secret,
	})
	return h, fake
}

func TestNodeTypeDefaultActionsAreRegistered(t *testing.T) {
	r := NewRegistry()
	for name, nt := range r.nodeTypes {
		if nt.DefaultAction == "" {
			continue
		}
		if _, ok := r.action(nt.DefaultAction); !ok {
			t.Errorf("node type %q defaults to unregistered action %q", name, nt.DefaultAction)
		}
	}
}

func TestGRPCWebhookRAGWorkflowActions(t *testing.T) {
	h, fake := newIntegrationHarness(t)

	t.Run("grpc", func(t *testing.T) {
		status, body := h.call("POST", "/claims", "", map[string]any{"patient_id": "P1"})
		if status != 200 || dig(body, "claimId") != "CLM-P1" {
			t.Fatalf("grpc = %d %v", status, body)
		}
		status, body = h.call("POST", "/claims", "", map[string]any{"patient_id": "missing"})
		if status != 404 || dig(body, "error", "code") != "GRPC_NOT_FOUND" {
			t.Fatalf("grpc not_found mapping = %d %v", status, body)
		}
	})

	t.Run("webhook", func(t *testing.T) {
		status, body := h.call("POST", "/deliver", "", map[string]any{"claim_id": "c-9", "amount": 120})
		if status != 200 || dig(body, "webhook_id") != "c-9" || dig(body, "status") != float64(204) {
			t.Fatalf("webhook = %d %v", status, body)
		}
		fake.mu.Lock()
		defer fake.mu.Unlock()
		if len(fake.webhooks) != 1 || fake.webhooks[0]["type"] != "claim.submitted" || dig(fake.webhooks[0], "data", "amount") != float64(120) {
			t.Fatalf("receiver saw %v", fake.webhooks)
		}
	})

	t.Run("rag", func(t *testing.T) {
		status, _ := h.call("POST", "/kb", "", map[string]any{"docs": []any{
			map[string]any{"id": "pol-1", "title": "Timely filing", "text": "Claims must be filed within 365 days of the date of service."},
			map[string]any{"id": "pol-2", "title": "Modifiers", "text": "Modifier 25 marks a significant, separately identifiable E/M service."},
		}})
		if status != 200 {
			t.Fatalf("index = %d", status)
		}
		status, body := h.call("POST", "/kb/ask", "", map[string]any{"question": "filed"})
		if status != 200 || dig(body, "count") != float64(1) || !strings.Contains(Stringify(dig(body, "context")), "[pol-1] Timely filing") {
			t.Fatalf("rag = %d %v", status, body)
		}
	})

	t.Run("workflow", func(t *testing.T) {
		status, body := h.call("POST", "/wf", "", map[string]any{"request_id": "req-7", "patient": "P1"})
		if status != 200 || dig(body, "id") != "run-1" || dig(body, "workflow") != "prior-auth" {
			t.Fatalf("start = %d %v", status, body)
		}
		if status, _ := h.call("POST", "/wf/signal", "", map[string]any{"run_id": "run-1"}); status != 200 {
			t.Fatalf("signal = %d", status)
		}
		status, body = h.call("POST", "/wf/status", "", map[string]any{"run_id": "run-1"})
		if status != 200 || dig(body, "status") != "completed" || dig(body, "result", "approved") != true {
			t.Fatalf("status = %d %v", status, body)
		}
		if status, _ := h.call("POST", "/wf/status", "", map[string]any{"run_id": "nope"}); status != 404 {
			t.Fatalf("unknown run should map the upstream 404, got %d", status)
		}
		fake.mu.Lock()
		defer fake.mu.Unlock()
		if len(fake.starts) != 1 || fake.starts[0] != "req-7" || len(fake.signals) != 1 {
			t.Fatalf("orchestrator saw starts=%v signals=%v", fake.starts, fake.signals)
		}
	})
}

type sseEvent struct{ name, data string }

func readSSE(t *testing.T, h *appHarness, path string, body any) (int, []sseEvent) {
	t.Helper()
	raw, _ := json.Marshal(body)
	resp, err := h.client.Post(h.base+path, "application/json", strings.NewReader(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var events []sseEvent
	var cur sseEvent
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			cur.name = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			cur.data += strings.TrimPrefix(line, "data: ")
		case line == "" && (cur.name != "" || cur.data != ""):
			events = append(events, cur)
			cur = sseEvent{}
		}
	}
	return resp.StatusCode, events
}

func TestStreamEmitIncrementalEvents(t *testing.T) {
	h, _ := newIntegrationHarness(t)
	status, events := readSSE(t, h, "/report", map[string]any{"name": "claims"})
	if status != 200 || len(events) != 3 {
		t.Fatalf("stream = %d %+v", status, events)
	}
	if events[0] != (sseEvent{"progress", "starting claims"}) || events[1] != (sseEvent{"progress", "[1,2,3]"}) || events[2].name != "result" {
		t.Fatalf("events = %+v", events)
	}
	// The same intent on a JSON route: stream.emit is a no-op.
	code, body := h.call("POST", "/report.json", "", map[string]any{"name": "claims"})
	if code != 200 || dig(body, "counted", "emitted") != false {
		t.Fatalf("json route = %d %v", code, body)
	}
	// A failure after streaming began arrives as an error event.
	status, events = readSSE(t, h, "/report/fail", map[string]any{})
	if status != 200 || len(events) != 2 || events[0].name != "progress" || events[1].name != "error" || !strings.Contains(events[1].data, "PERMISSION_DENIED") {
		t.Fatalf("failing stream = %d %+v", status, events)
	}
}

// The emitter is bounded; a slow reader applies backpressure and a client
// that disappears cancels the producer instead of stranding it.
func TestStreamEmitterBackpressureAndCancellation(t *testing.T) {
	emitter := &streamEmitter{events: make(chan []byte, 2)}
	done := make(chan dispatchOutcome, 1)
	ctx, cancel := context.WithCancel(context.Background())
	produced := make(chan int, 1)
	go func() {
		n := 0
		for i := 0; i < 1000; i++ {
			if emitter.emit(ctx, sseFrame("tick", []byte("x"))) != nil {
				break
			}
			n++
		}
		produced <- n
		done <- dispatchOutcome{err: context.Canceled}
	}()
	reader := &sseReader{events: emitter.events, done: done, onFinal: func(dispatchOutcome) []byte { return nil }}
	buf := make([]byte, 16)
	for i := 0; i < 5; i++ { // read a few frames, then "disconnect"
		if _, err := reader.Read(buf); err != nil {
			t.Fatal(err)
		}
	}
	cancel()
	select {
	case n := <-produced:
		if n >= 1000 {
			t.Fatalf("producer was not throttled: %d frames", n)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("producer blocked after the client went away")
	}
}
