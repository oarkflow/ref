package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/oarkflow/ref/studio"
	"github.com/oarkflow/ref/studio/preview"
)

// withPreview makes the server build its own preview service.
func withPreview(t *testing.T, basePath string) func(*Config) {
	return func(c *Config) {
		c.BasePath = basePath
		c.PreviewOptions = &preview.Options{
			TempDir: t.TempDir(), BaseDir: c.ConfigDir,
			Grace: 10 * time.Millisecond, Drain: time.Second,
		}
	}
}

// browser is an HTTP client holding cookies, like an iframe would.
type browser struct {
	t      *testing.T
	client *http.Client
	base   string
}

func newBrowser(t *testing.T, base string) *browser {
	jar, _ := cookiejar.New(nil)
	return &browser{t: t, base: base, client: &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func (b *browser) do(method, path, token string, body io.Reader) (int, http.Header, string) {
	b.t.Helper()
	req, err := http.NewRequest(method, b.base+path, body)
	if err != nil {
		b.t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := b.client.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, string(raw)
}

func (b *browser) ensure(id string) (int, studio.PreviewStatus) {
	b.t.Helper()
	status, _, raw := b.do("POST", "/api/v1/drafts/"+id+"/preview", tokEditor, nil)
	var st studio.PreviewStatus
	if err := json.Unmarshal([]byte(raw), &st); err != nil {
		b.t.Fatalf("preview status: %v: %s", err, raw)
	}
	return status, st
}

// previewEvent waits for a preview event with the given status, skipping the
// ones before it (a build reports "starting" first).
func previewEvent(t *testing.T, s *sseStream, status string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		ev := s.waitFor("preview", time.Until(deadline))
		var st studio.PreviewStatus
		if err := json.Unmarshal([]byte(ev.data), &st); err != nil {
			t.Fatalf("preview event %q: %v", ev.data, err)
		}
		if st.Status == status {
			return
		}
	}
	t.Fatalf("no %q preview event", status)
}

func TestPreviewEndToEnd(t *testing.T) {
	e := newEnv(t, withPreview(t, ""))
	t.Cleanup(func() { e.srv.Close() })
	if e.ok(200, "GET", "/api/v1/meta", tokViewer, nil)["features"].(obj)["preview"] != true {
		t.Fatal("preview is not advertised")
	}
	id, v := e.newDraft(tokEditor, "active")
	stream := e.openStream(id, tokEditor)
	stream.waitFor("changed", 5*time.Second)
	br := newBrowser(t, e.ts.URL)

	// Start: ready, with the cookie an iframe needs.
	status, st := br.ensure(id)
	if status != 200 || st.Status != "ready" || st.URL != "/preview/"+id+"/" || st.Version != v {
		t.Fatalf("ensure = %d %+v", status, st)
	}
	previewEvent(t, stream, "ready")

	// The iframe carries only the cookie (br has no bearer for these).
	code, _, body := br.do("GET", "/preview/"+id+"/health", "", nil)
	if code != 200 || body == "" {
		t.Fatalf("GET /health: %d %q", code, body)
	}
	if code, _, _ := newBrowser(t, e.ts.URL).do("GET", "/preview/"+id+"/health", "", nil); code != 401 {
		t.Fatalf("a browser without the cookie: %d", code)
	}

	// Edit the route; the running preview is still the old build until it is rebuilt.
	out := e.ops(id, tokEditor, v, obj{"op": "setField", "file": "01_routes.bcl", "path": "route/health.ping/path", "value": `"/health2"`})
	if code, _, _ := br.do("GET", "/preview/"+id+"/health2", "", nil); code != 404 {
		t.Fatalf("the edit shows before a rebuild: %d", code)
	}
	status, st = br.ensure(id)
	if status != 200 || st.Status != "ready" || st.Version != ver(out) {
		t.Fatalf("rebuild = %d %+v", status, st)
	}
	if code, _, body := br.do("GET", "/preview/"+id+"/health2", "", nil); code != 200 || body == "" {
		t.Fatalf("GET /health2 after the rebuild: %d %q", code, body)
	}
	if code, _, _ := br.do("GET", "/preview/"+id+"/health", "", nil); code != 404 {
		t.Fatalf("the old path still answers: %d", code)
	}

	// The request console saw the traffic.
	status, _, raw := br.do("GET", "/api/v1/drafts/"+id+"/preview/requests", tokEditor, nil)
	var reqs []studio.RecordedRequest
	if err := json.Unmarshal([]byte(raw), &reqs); err != nil || status != 200 {
		t.Fatalf("requests = %d %s (%v)", status, raw, err)
	}
	var saw bool
	for _, r := range reqs {
		if r.Kind == "request" && r.Method == "GET" && strings.Contains(r.URL, "/health2") && r.Status == 200 {
			saw = true
		}
	}
	if !saw {
		t.Fatalf("no recorded request for /health2: %+v", reqs)
	}

	// A change that breaks the build: diagnostics name the file and line, and
	// the previous build keeps serving.
	bad := e.ops(id, tokEditor, ver(out), obj{"op": "setField", "file": "01_routes.bcl", "path": "route/health.ping/intent", "value": `"no.such.intent"`})
	status, st = br.ensure(id)
	if status != 200 || st.Status != "failed" || len(st.Error) == 0 {
		t.Fatalf("failed build = %d %+v", status, st)
	}
	var located bool
	for _, d := range st.Error {
		if d.File == "01_routes.bcl" && d.Line > 0 {
			located = true
		}
	}
	if !located {
		t.Fatalf("no diagnostic with a file and line: %+v", st.Error)
	}
	if !strings.Contains(st.Message, "still") {
		t.Errorf("the status should say the previous build is serving: %q", st.Message)
	}
	previewEvent(t, stream, "failed")
	if code, _, _ := br.do("GET", "/preview/"+id+"/health2", "", nil); code != 200 {
		t.Fatalf("the previous build stopped serving: %d", code)
	}

	// Undo the breaking edit and the next build is ready again.
	e.ok(200, "POST", "/api/v1/drafts/"+id+"/undo", tokEditor, nil)
	if status, st = br.ensure(id); status != 200 || st.Status != "ready" || st.Version <= ver(bad) {
		t.Fatalf("recovered build = %d %+v", status, st)
	}

	// Stop: the preview is gone (404 from the preview service, not 401).
	if status, _, _ := br.do("DELETE", "/api/v1/drafts/"+id+"/preview", tokEditor, nil); status != 204 {
		t.Fatal(status)
	}
	if code, _, body := br.do("GET", "/preview/"+id+"/health2", "", nil); code != 404 || !strings.Contains(body, "preview_not_found") {
		t.Fatalf("after stop: %d %q", code, body)
	}
}

func TestPreviewMountedUnderAPrefix(t *testing.T) {
	e := newEnv(t, withPreview(t, "/studio"))
	t.Cleanup(func() { e.srv.Close() })
	mux := http.NewServeMux()
	mux.Handle("/studio/", http.StripPrefix("/studio", e.srv))
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	br := newBrowser(t, ts.URL)

	id, v := e.newDraft(tokEditor, "active")
	// A route that redirects to another route inside the preview.
	e.ops(id, tokEditor, v, obj{"op": "addBlock", "file": "01_routes.bcl", "type": "route", "id": "go",
		"body": "method GET\npath \"/go\"\nintent \"health.ping\"\nstatus 302\nheaders { Location \"/health\" }"})

	status, _, raw := br.do("POST", "/studio/api/v1/drafts/"+id+"/preview", tokEditor, nil)
	var st studio.PreviewStatus
	if err := json.Unmarshal([]byte(raw), &st); err != nil || status != 200 || st.Status != "ready" || st.URL != "/studio/preview/"+id+"/" {
		t.Fatalf("ensure = %d %s (%v)", status, raw, err)
	}
	u, _ := url.Parse(ts.URL)
	var cookie *http.Cookie
	for _, c := range br.client.Jar.Cookies(&url.URL{Scheme: "http", Host: u.Host, Path: "/studio/preview/" + id + "/"}) {
		if c.Name == previewCookie {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("the preview cookie is not sent to the preview path")
	}
	if code, _, body := br.do("GET", "/studio/preview/"+id+"/health", "", nil); code != 200 || body == "" {
		t.Fatalf("GET through the mount: %d %q", code, body)
	}
	// The app's own redirect lands back inside the preview, under the mount.
	code, hdr, _ := br.do("GET", "/studio/preview/"+id+"/go", "", nil)
	if code != 302 || hdr.Get("Location") != "/studio/preview/"+id+"/health" {
		t.Fatalf("redirect: %d Location=%q", code, hdr.Get("Location"))
	}
	// Outside the mount there is nothing.
	if code, _, _ := br.do("GET", "/preview/"+id+"/health", "", nil); code == 200 {
		t.Fatalf("served outside the mount: %d", code)
	}
}

func TestPreviewOffWithoutOptions(t *testing.T) {
	e := newEnv(t)
	if e.ok(200, "GET", "/api/v1/meta", tokViewer, nil)["features"].(obj)["preview"] != false {
		t.Fatal("preview advertised without a manager")
	}
	if err := e.srv.Close(); err != nil {
		t.Fatal(err)
	}
}
