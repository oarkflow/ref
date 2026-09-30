package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeClock is a settable clock.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func TestRateLimitOnMutatingRequests(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	e := newEnv(t, func(c *Config) {
		c.RateLimit = RateLimit{Rate: 1, Burst: 3}
		c.Now = clock.Now
	})
	post := func(token string) (int, http.Header, []byte) {
		return e.callFull("POST", "/api/v1/drafts", token, obj{"from": "dir"}, nil)
	}
	for i := range 3 {
		if status, _, raw := post(tokEditor); status != 201 {
			t.Fatalf("request %d: %d %s", i, status, raw)
		}
	}
	status, hdr, raw := post(tokEditor)
	if status != 429 || hdr.Get("Retry-After") != "1" || !strings.Contains(string(raw), `"rate_limited"`) {
		t.Fatalf("over the limit: %d %v %s", status, hdr, raw)
	}
	// Another identity has its own bucket; reads are never limited.
	if status, _, raw := post(tokEditor2); status != 201 {
		t.Fatalf("other identity: %d %s", status, raw)
	}
	for range 20 {
		if status, _, _ := e.callFull("GET", "/api/v1/drafts", tokEditor, nil, nil); status != 200 {
			t.Fatalf("a read was limited: %d", status)
		}
	}
	// A failed authentication does not spend the caller's tokens.
	if status, _, _ := post("nope-nope-nope-nope"); status != 401 {
		t.Fatal(status)
	}
	// The bucket refills with time.
	clock.advance(2 * time.Second)
	for range 2 {
		if status, _, raw := post(tokEditor); status != 201 {
			t.Fatalf("after refilling: %d %s", status, raw)
		}
	}
	if status, _, _ := post(tokEditor); status != 429 {
		t.Fatalf("only two tokens had refilled: %d", status)
	}
}

func TestRateLimitCanBeDisabled(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.RateLimit = RateLimit{Rate: -1}; c.MaxDraftsPerOwner = 1000 })
	for i := range 150 {
		if status, _, raw := e.callFull("POST", "/api/v1/drafts", tokEditor, obj{"from": "dir"}, nil); status != 201 {
			t.Fatalf("request %d: %d %s", i, status, raw)
		}
	}
}

func TestRequestIDs(t *testing.T) {
	e := newEnv(t)
	// Generated when absent, on success and on error.
	_, hdr, _ := e.callFull("GET", "/api/v1/meta", tokViewer, nil, nil)
	if id := hdr.Get("X-Request-Id"); !strings.HasPrefix(id, "req_") || len(id) < 8 {
		t.Fatalf("generated id = %q", id)
	}
	// A sane client id is kept and appears in the error body.
	status, hdr, raw := e.callFull("GET", "/api/v1/drafts/drf_nope", tokEditor, nil, map[string]string{"X-Request-Id": "trace-123.abc_DEF"})
	var body struct {
		Error struct{ Code, RequestId string }
	}
	if err := json.Unmarshal(raw, &body); err != nil || status != 404 {
		t.Fatalf("%d %s (%v)", status, raw, err)
	}
	if hdr.Get("X-Request-Id") != "trace-123.abc_DEF" || body.Error.RequestId != "trace-123.abc_DEF" || body.Error.Code != "not_found" {
		t.Fatalf("header %q, body %+v", hdr.Get("X-Request-Id"), body)
	}
	// Unsafe or oversized ids are replaced, never echoed.
	for _, bad := range []string{"has space", "semi;colon", "<script>", strings.Repeat("a", 65), "new\tline"} {
		_, hdr, _ := e.callFull("GET", "/api/v1/meta", tokViewer, nil, map[string]string{"X-Request-Id": bad})
		if got := hdr.Get("X-Request-Id"); got == bad || !strings.HasPrefix(got, "req_") {
			t.Errorf("id %q was answered with %q", bad, got)
		}
	}
	// Two requests, two ids.
	_, h1, _ := e.callFull("GET", "/api/v1/meta", tokViewer, nil, nil)
	_, h2, _ := e.callFull("GET", "/api/v1/meta", tokViewer, nil, nil)
	if h1.Get("X-Request-Id") == h2.Get("X-Request-Id") {
		t.Fatal("request ids repeat")
	}
	// Auth failures and unknown endpoints carry one too.
	_, hdr, raw = e.callFull("GET", "/api/v1/meta", "", nil, nil)
	if hdr.Get("X-Request-Id") == "" || !strings.Contains(string(raw), `"requestId"`) {
		t.Fatalf("401 = %v %s", hdr, raw)
	}
}

func TestBodySizeLimits(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.MaxSmallBody = 256; c.MaxBody = 4096 })
	id, v := e.newDraft(tokEditor, "dir")

	// Small endpoints refuse a large body, whatever it says it contains.
	big := strings.Repeat("x", 1000)
	if c := e.fail(413, "POST", "/api/v1/drafts", tokEditor, obj{"name": big, "from": "dir"}); c != "too_large" {
		t.Fatal(c)
	}
	e.fail(413, "POST", "/api/v1/drafts/"+id+"/propose", tokEditor, obj{"message": big})
	e.fail(413, "POST", "/api/v1/revisions/"+proposeChange(t, e, tokEditor2, "/p")+"/comments", tokEditor, obj{"body": big})
	// A body that does not declare its length (chunked) is cut off too.
	req, _ := http.NewRequest("POST", e.ts.URL+"/api/v1/drafts", io_reader(`{"name":"`+big+`","from":"dir"}`))
	req.ContentLength = -1
	req.Header.Set("Authorization", "Bearer "+tokEditor)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 413 {
		t.Fatalf("chunked body: %d", resp.StatusCode)
	}

	// Ops and file replacement may carry more, up to MaxBody.
	content := "# " + strings.Repeat("c", 1000) + "\n"
	e.ok(200, "POST", "/api/v1/drafts/"+id+"/ops", tokEditor, obj{"ifVersion": v, "ops": []obj{{"op": "addFile", "file": "05_big.bcl", "content": content}}})
	over := "# " + strings.Repeat("c", 5000) + "\n"
	if c := e.fail(413, "POST", "/api/v1/drafts/"+id+"/ops", tokEditor, obj{"ops": []obj{{"op": "addFile", "file": "06_big.bcl", "content": over}}}); c != "too_large" {
		t.Fatal(c)
	}
	e.fail(413, "PUT", "/api/v1/drafts/"+id+"/files/05_big.bcl", tokEditor, obj{"content": over})
	e.ok(200, "PUT", "/api/v1/drafts/"+id+"/files/05_big.bcl", tokEditor, obj{"content": content + "# more\n"})

	// The declared length is checked before the body is read, against the ceiling.
	req, _ = http.NewRequest("POST", e.ts.URL+"/api/v1/drafts/"+id+"/ops", io_reader(over))
	req.Header.Set("Authorization", "Bearer "+tokEditor)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 413 {
		t.Fatalf("declared length: %d", resp.StatusCode)
	}
}

func TestOpsPerBatchLimit(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.MaxOps = 3 })
	id, v := e.newDraft(tokEditor, "dir")
	op := obj{"op": "setField", "file": "01_routes.bcl", "path": "route/health.ping/path", "value": `"/x"`}
	e.ok(200, "POST", "/api/v1/drafts/"+id+"/ops", tokEditor, obj{"ifVersion": v, "ops": []obj{op, op, op}})
	if c := e.fail(422, "POST", "/api/v1/drafts/"+id+"/ops", tokEditor, obj{"ops": []obj{op, op, op, op}}); c != "too_many_ops" {
		t.Fatal(c)
	}
}

func TestCORS(t *testing.T) {
	const good, bad = "https://studio.example.com", "https://evil.example.com"
	e := newEnv(t, func(c *Config) { c.AllowedOrigins = []string{good} })
	hdr := func(origin string) map[string]string { return map[string]string{"Origin": origin} }

	// An allowed origin gets the headers on ordinary requests.
	status, h, _ := e.callFull("GET", "/api/v1/meta", tokViewer, nil, hdr(good))
	if status != 200 || h.Get("Access-Control-Allow-Origin") != good || !strings.Contains(h.Get("Vary"), "Origin") ||
		!strings.Contains(h.Get("Access-Control-Expose-Headers"), "X-Request-Id") {
		t.Fatalf("allowed: %d %v", status, h)
	}
	// ... and a preflight is answered without needing a token.
	pf := map[string]string{"Origin": good, "Access-Control-Request-Method": "POST", "Access-Control-Request-Headers": "authorization"}
	status, h, _ = e.callFull("OPTIONS", "/api/v1/drafts", "", nil, pf)
	if status != 204 || h.Get("Access-Control-Allow-Origin") != good ||
		!strings.Contains(h.Get("Access-Control-Allow-Headers"), "Authorization") || !strings.Contains(h.Get("Access-Control-Allow-Methods"), "POST") {
		t.Fatalf("preflight: %d %v", status, h)
	}
	// A disallowed origin gets no CORS headers, and its preflight is refused.
	status, h, _ = e.callFull("GET", "/api/v1/meta", tokViewer, nil, hdr(bad))
	if status != 200 || h.Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("disallowed: %d %v", status, h)
	}
	pf["Origin"] = bad
	status, h, raw := e.callFull("OPTIONS", "/api/v1/drafts", "", nil, pf)
	if status != 403 || h.Get("Access-Control-Allow-Origin") != "" || !strings.Contains(string(raw), "forbidden_origin") {
		t.Fatalf("disallowed preflight: %d %v %s", status, h, raw)
	}
	// Same-origin requests (no Origin header) are untouched.
	if _, h, _ = e.callFull("GET", "/api/v1/meta", tokViewer, nil, nil); h.Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("no origin: %v", h)
	}
	// The default is same-origin only.
	plain := newEnv(t)
	if _, h, _ = plain.callFull("GET", "/api/v1/meta", tokViewer, nil, hdr(good)); h.Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("default policy allowed an origin: %v", h)
	}
	status, _, _ = plain.callFull("OPTIONS", "/api/v1/drafts", "", nil, map[string]string{"Origin": good, "Access-Control-Request-Method": "POST"})
	if status != 403 {
		t.Fatalf("default preflight: %d", status)
	}
	// "*" allows any origin.
	star := newEnv(t, func(c *Config) { c.AllowedOrigins = []string{"*"} })
	if _, h, _ = star.callFull("GET", "/api/v1/meta", tokViewer, nil, hdr(bad)); h.Get("Access-Control-Allow-Origin") != bad {
		t.Fatalf("wildcard: %v", h)
	}
	// The web app and preview paths are not part of the API policy.
	if _, h, _ = e.callFull("GET", "/", "", nil, hdr(bad)); h.Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("web app: %v", h)
	}
}

func TestCloseEndsEventStreams(t *testing.T) {
	e := newEnv(t)
	id, _ := e.newDraft(tokEditor, "dir")
	s1, s2 := e.openStream(id, tokEditor), e.openStream(id, tokEditor)
	s1.waitFor("changed", 5*time.Second)
	s2.waitFor("changed", 5*time.Second)

	closed := make(chan struct{})
	go func() { _ = e.srv.Close(); _ = e.srv.Close(); close(closed) }() // idempotent
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not return")
	}
	for i, s := range []*sseStream{s1, s2} {
		got := false
		for {
			ev, ok := s.next(5 * time.Second)
			if !ok {
				break
			}
			if ev.name == "shutdown" {
				got = true
			}
		}
		if !got {
			t.Errorf("stream %d ended without a shutdown event", i)
		}
	}
	// Streams opened after Close end at once.
	s3 := e.openStream(id, tokEditor)
	for {
		if _, ok := s3.next(5 * time.Second); !ok {
			break
		}
	}
}
