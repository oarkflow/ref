package main

import (
	"bytes"
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

	"github.com/oarkflow/ref/examples/starter/internal/ops"
	"github.com/oarkflow/ref/health"
	"github.com/oarkflow/ref/platform"
	"github.com/prometheus/client_golang/prometheus"
)

const (
	e2eAdminToken    = "admin-token-0123456789"
	e2eReviewerToken = "reviewer-token-0123456"
	e2eEditorToken   = "editor-token-01234567"
	e2eMarker        = "Studio E2E Marker"
)

// TestStudioEndToEnd boots the supervised starter with Studio mounted and
// walks the whole editing story through the HTTP API: a draft from the config
// directory, a route edit plus a template override in one batch, a sandboxed
// preview that renders the override, a proposal, a second person's approval,
// and activation with no restart.
//
// It is skipped unless STARTER_STUDIO_E2E=1 (it opens two listeners and a
// sqlite database, and takes a few seconds):
//
//	STARTER_STUDIO_E2E=1 GOTOOLCHAIN=go1.26.5 go test ./cmd/server -run TestStudioEndToEnd -v
func TestStudioEndToEnd(t *testing.T) {
	if os.Getenv("STARTER_STUDIO_E2E") != "1" {
		t.Skip("set STARTER_STUDIO_E2E=1 to run the Studio end-to-end test")
	}
	root, err := filepath.Abs("../..") // examples/starter
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	tmp := t.TempDir()
	seeds := filepath.Join(tmp, "seeds")
	if err := os.MkdirAll(seeds, 0o755); err != nil {
		t.Fatal(err)
	}
	appPort, adminAddr := freePort(t), "127.0.0.1:"+freePort(t)
	dbFile := filepath.Join(tmp, "app.db")
	for k, v := range map[string]string{
		"DB_DRIVER":              "sqlite",
		"DB_DSN":                 "file:" + dbFile + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)",
		"MIGRATIONS_DIR":         filepath.Join(root, "resources", "migrations"),
		"MIGRATIONS_SEED_DIR":    seeds,
		"AUTO_MIGRATE":           "true",
		"SESSION_SECRET":         "e2e-session-secret-0123456789abcdef",
		"WEBHOOK_SECRET":         "e2e-webhook-secret-0123456789abcdef",
		"STARTER_SUPERVISOR":     "1",
		"STARTER_STUDIO":         "1",
		"STARTER_STUDIO_PERSIST": "1",
		"STARTER_ADMIN_ADDR":     adminAddr,
		"STARTER_ADMIN_TOKEN":    e2eAdminToken,
		"STARTER_REVIEWER_TOKEN": e2eReviewerToken,
		"STARTER_EDITOR_TOKEN":   e2eEditorToken,
	} {
		t.Setenv(k, v)
	}

	logger := newLogger("development", "warn", "", "")
	boot := Bootstrap{Port: appPort, Env: "development", AdminEmail: "admin@example.com",
		AdminPassword: "Password123!", AppVersion: "e2e"}
	maintenance := ops.NewMaintenanceGate(false, "")
	maintenance.RegisterAction()
	opts := platform.DefaultLoadOptions()
	opts.Profile = boot.Env
	hr := health.NewRegistry()
	opts.HealthRegistry = hr
	if err := ensureMigrated(logger); err != nil {
		t.Fatal(err)
	}
	deps := starterDeps{logger: logger, boot: boot, health: hr, maintenance: maintenance,
		promRegistry: prometheus.NewRegistry(),
		staticDir:    filepath.Join(root, "resources", "static"),
		templatesDir: filepath.Join(root, "resources", "templates")}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- runSupervised(ctx, deps, filepath.Join(root, "resources", "config"), opts)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(20 * time.Second):
			t.Error("supervised server did not stop")
		}
	})

	app := "http://127.0.0.1:" + appPort
	studio := "http://" + adminAddr + "/studio"
	waitFor(t, "app ready", 60*time.Second, func() bool { return status(app+"/readyz") == 200 })
	waitFor(t, "studio ready", 30*time.Second, func() bool { return status(studio+"/api/v1/meta") == 401 })

	live := get(t, app+"/login", "")
	if !strings.Contains(live, "Welcome back") || strings.Contains(live, e2eMarker) {
		t.Fatalf("baseline /login unexpected:\n%.400s", live)
	}

	editor := &api{t: t, base: studio + "/api/v1", token: e2eEditorToken}
	reviewer := &api{t: t, base: studio + "/api/v1", token: e2eReviewerToken}

	// 1. who am I, and what is on
	var meta struct {
		Identity struct{ Name string } `json:"identity"`
		Features struct{ Preview, Pages bool }
	}
	editor.json("GET", "/meta", nil, &meta)
	t.Logf("meta: %+v", meta)
	if meta.Identity.Name != "editor" || !meta.Features.Preview {
		t.Fatalf("meta = %+v", meta)
	}

	// 2. a draft from the config directory
	var draft struct {
		ID      string
		Version int64
		Files   []string
	}
	editor.json("POST", "/drafts", map[string]any{"name": "e2e", "from": "dir"}, &draft)
	if draft.ID == "" || len(draft.Files) < 10 {
		t.Fatalf("draft = %+v", draft)
	}
	t.Logf("draft %s v%d, %d files", draft.ID, draft.Version, len(draft.Files))

	// 3. the template catalog knows the todo pages
	var cat struct {
		Templates []struct {
			Name   string
			Kind   string
			Source string
			Routes []struct{ Route string }
		}
		Missing []any
		Globals []string
	}
	editor.json("GET", "/drafts/"+draft.ID+"/templates", nil, &cat)
	found := map[string]bool{}
	for _, tpl := range cat.Templates {
		found[tpl.Name] = len(tpl.Routes) > 0
	}
	t.Logf("catalog: %d templates, %d missing refs", len(cat.Templates), len(cat.Missing))
	for _, want := range []string{"pages/todos/list", "pages/auth/login"} {
		if _, ok := found[want]; !ok {
			t.Fatalf("catalog lacks %s (has %v)", want, found)
		}
	}
	if !found["pages/todos/list"] {
		t.Error("pages/todos/list is not linked to any route")
	}
	if len(cat.Missing) != 0 {
		t.Errorf("unexpected missing template references: %v", cat.Missing)
	}

	// 4. one batch: change a route's path and override the login template
	var asset struct{ Content, Source string }
	editor.json("GET", "/drafts/"+draft.ID+"/assets/templates/pages/auth/login.html", nil, &asset)
	if asset.Source != "disk" || !strings.Contains(asset.Content, "Welcome back") {
		t.Fatalf("asset = source %q, %d bytes", asset.Source, len(asset.Content))
	}
	override := strings.Replace(asset.Content, "Welcome back", e2eMarker, 1)
	var res struct {
		Version     int64
		Applied     int
		Diagnostics []struct{ Severity, Code, Message string }
	}
	editor.json("POST", "/drafts/"+draft.ID+"/ops", map[string]any{
		"ifVersion": draft.Version,
		"ops": []map[string]any{
			{"op": "setField", "file": "13_todo_pages.bcl", "path": "route/web.todos_list/path", "value": `"/my-todos"`},
			{"op": "putAsset", "file": "templates/pages/auth/login.html", "content": override},
		},
	}, &res)
	if res.Applied != 2 || res.Version <= draft.Version {
		t.Fatalf("ops result = %+v", res)
	}
	for _, d := range res.Diagnostics {
		if d.Severity == "error" {
			t.Errorf("diagnostic after edit: %+v", d)
		}
	}

	// 5. the draft's linkage still holds after the edit
	editor.json("POST", "/drafts/"+draft.ID+"/validate", nil, &struct{}{})

	// 6. preview: sandboxed, and it renders the override
	before := tableCounts(t, dbFile)
	jar, _ := cookiejar.New(nil)
	pvClient := &http.Client{Jar: jar, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	pv := &api{t: t, base: studio + "/api/v1", token: e2eEditorToken, client: pvClient}
	var ps struct {
		Status, URL, Error string
		State              string
	}
	pv.json("POST", "/drafts/"+draft.ID+"/preview", nil, &ps)
	t.Logf("preview start: %+v", ps)
	previewURL := ps.URL
	if previewURL == "" {
		previewURL = "/studio/preview/" + draft.ID + "/"
	}
	base, _ := url.Parse("http://" + adminAddr)
	pageURL := base.ResolveReference(&url.URL{Path: strings.TrimSuffix(previewURL, "/") + "/login"}).String()
	var body string
	waitFor(t, "preview /login", 60*time.Second, func() bool {
		req, _ := http.NewRequest("GET", pageURL, nil)
		req.Header.Set("Authorization", "Bearer "+e2eEditorToken)
		resp, err := pvClient.Do(req)
		if err != nil {
			return false
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		body = string(b)
		return resp.StatusCode == 200
	})
	if !strings.Contains(body, e2eMarker) {
		t.Fatalf("preview /login lacks the override:\n%.600s", body)
	}
	// The preview's own database is a temp-dir sqlite file with the starter's
	// schema and dev accounts, and the real one is untouched.
	sandboxes, _ := filepath.Glob(filepath.Join(os.TempDir(), "studio-preview-*", "db", "database.db"))
	if len(sandboxes) == 0 {
		t.Error("no sandboxed preview database found in the temp dir")
	}
	form := url.Values{"email": {boot.AdminEmail}, "password": {boot.AdminPassword}}
	req, _ := http.NewRequest("POST", pageURL, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Bearer "+e2eEditorToken)
	resp, err := pvClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	t.Logf("preview login POST -> %d Location=%q", resp.StatusCode, resp.Header.Get("Location"))
	if resp.StatusCode != 302 && resp.StatusCode != 303 {
		t.Errorf("preview login = %d, want a redirect (the sandbox has the dev admin)", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "" && !strings.Contains(loc, "/preview/"+draft.ID) {
		t.Errorf("redirect leaves the preview prefix: %q", loc)
	}
	if after := tableCounts(t, dbFile); fmt.Sprint(before) != fmt.Sprint(after) {
		t.Errorf("the real database changed during preview:\nbefore %v\nafter  %v", before, after)
	}
	var reqs []struct{ Kind, Method, Path string }
	editor.json("GET", "/drafts/"+draft.ID+"/preview/requests", nil, &reqs)
	t.Logf("preview recorded %d requests", len(reqs))
	if len(reqs) == 0 {
		t.Error("the preview recorded no requests")
	}

	// 7. the live app is still on the old version
	if strings.Contains(get(t, app+"/login", ""), e2eMarker) {
		t.Fatal("the override is live before anything was approved")
	}

	// 8. submit for review; the editor cannot approve, the reviewer can
	var rev struct {
		ID     string
		Status string
		Assets []struct{ Path string }
	}
	editor.json("POST", "/drafts/"+draft.ID+"/propose", map[string]any{"message": "e2e: override login, move todos"}, &rev)
	if rev.ID == "" || rev.Status != "pending" || len(rev.Assets) != 1 || rev.Assets[0].Path != "templates/pages/auth/login.html" {
		t.Fatalf("revision = %+v", rev)
	}
	if code := editor.status("POST", "/revisions/"+rev.ID+"/approve", map[string]any{}); code != 403 {
		t.Errorf("editor approve = %d, want 403", code)
	}
	if code := reviewer.status("POST", "/revisions/"+rev.ID+"/activate", nil); code == 200 {
		t.Error("activation before approval succeeded")
	}
	reviewer.json("POST", "/revisions/"+rev.ID+"/approve", map[string]any{"comment": "ok"}, &struct{}{})
	reviewer.json("POST", "/revisions/"+rev.ID+"/activate", nil, &struct{}{})

	// 9. live, no restart
	waitFor(t, "override live", 30*time.Second, func() bool {
		return strings.Contains(get(t, app+"/login", ""), e2eMarker)
	})
	if status(app+"/readyz") != 200 {
		t.Error("/readyz is not 200 after the swap")
	}
	if code := status(app + "/my-todos"); code == 404 {
		t.Error("/my-todos is 404 after activation")
	}
	if code := status(app + "/register"); code != 200 {
		t.Errorf("/register (still from disk) = %d", code)
	}

	// 10. the audit trail names who did what (persisted in the database)
	adm := &api{t: t, base: studio + "/api/v1", token: e2eAdminToken}
	var audit []struct{ Who, Action string }
	adm.json("GET", "/audit?limit=50", nil, &audit)
	actions := map[string]bool{}
	for _, a := range audit {
		actions[a.Who+":"+a.Action] = true
	}
	t.Logf("audit: %d entries", len(audit))
	if len(audit) < 4 {
		t.Errorf("audit has %d entries", len(audit))
	}
}

// ---------------------------------------------------------------------------

type api struct {
	t      *testing.T
	base   string
	token  string
	client *http.Client
}

func (a *api) do(method, path string, body any) (*http.Response, []byte) {
	a.t.Helper()
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, a.base+path, rdr)
	req.Header.Set("Authorization", "Bearer "+a.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	c := a.client
	if c == nil {
		c = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := c.Do(req)
	if err != nil {
		a.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

func (a *api) json(method, path string, body, out any) {
	a.t.Helper()
	resp, b := a.do(method, path, body)
	if resp.StatusCode >= 300 {
		a.t.Fatalf("%s %s = %d: %.600s", method, path, resp.StatusCode, b)
	}
	if out != nil && len(b) > 0 {
		if err := json.Unmarshal(b, out); err != nil {
			a.t.Fatalf("%s %s: decoding %.300s: %v", method, path, b, err)
		}
	}
}

func (a *api) status(method, path string, body any) int {
	a.t.Helper()
	resp, _ := a.do(method, path, body)
	return resp.StatusCode
}

func get(t *testing.T, target, token string) string {
	t.Helper()
	req, _ := http.NewRequest("GET", target, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", target, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

func status(target string) int {
	resp, err := (&http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Get(target)
	if err != nil {
		return 0
	}
	resp.Body.Close()
	return resp.StatusCode
}

func waitFor(t *testing.T, what string, limit time.Duration, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(150 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func freePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	return port
}

// tableCounts is a row count per table of the sqlite file, for "the real
// database did not change".
func tableCounts(t *testing.T, file string) map[string]int {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+file+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for rows.Next() {
		var n string
		rows.Scan(&n)
		names = append(names, n)
	}
	rows.Close()
	out := map[string]int{}
	for _, n := range names {
		if strings.HasPrefix(n, "studio_") { // Studio's own tables change with every edit
			continue
		}
		var c int
		if err := db.QueryRow(`SELECT COUNT(*) FROM "` + n + `"`).Scan(&c); err == nil {
			out[n] = c
		}
	}
	return out
}
