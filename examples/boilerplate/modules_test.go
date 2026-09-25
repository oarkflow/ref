package boilerplate_test

// End-to-end tests for the business modules declared in bcl/12_projects.bcl,
// bcl/13_gov_projects.bcl, bcl/14_medical_coding.bcl and bcl/15_activity.bcl.
//
// Each test compiles the whole bcl/ directory against a fresh SQLite file,
// mounts it on a real loopback listener (with the SPL renderer, so the web
// pages render too) and drives it over HTTP with one cookie jar per demo user,
// exactly as a browser or API client would.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/oarkflow/fh"
	"github.com/oarkflow/ref/examples/boilerplate/internal/web"
	"github.com/oarkflow/ref/platform"
)

const demoPassword = "Password123!"

type moduleHarness struct {
	t    *testing.T
	p    *platform.Platform
	base string
}

func newModuleHarness(t *testing.T) *moduleHarness {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("DATABASE_URL", "file:"+filepath.Join(dir, "app.db")+"?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)")
	t.Setenv("SESSION_DIR", filepath.Join(dir, "sessions"))
	t.Setenv("UPLOAD_DIR", filepath.Join(dir, "uploads"))

	p, err := platform.LoadDir(context.Background(), "bcl", platform.DefaultLoadOptions())
	if err != nil {
		t.Fatalf("platform.LoadDir: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })

	renderer, err := web.NewSPLRenderer(web.RendererConfig{TemplatesDir: "templates", IsDev: true, AppName: "Modules Test"})
	if err != nil {
		t.Fatalf("NewSPLRenderer: %v", err)
	}
	app := fh.NewFast(fh.WithTemplateEngine(renderer))
	if err := p.Mount(app); err != nil {
		t.Fatalf("Mount: %v", err)
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
		// Close the listener too: if Serve had not registered it before the
		// shutdown ran, Accept would otherwise block forever.
		_ = listener.Close()
		<-served
	})
	return &moduleHarness{t: t, p: p, base: "http://" + listener.Addr().String()}
}

// apiClient is one user agent with its own cookie jar.
type apiClient struct {
	h    *moduleHarness
	name string
	http *http.Client
}

func (h *moduleHarness) anonymous() *apiClient {
	jar, _ := cookiejar.New(nil)
	return &apiClient{h: h, name: "anonymous", http: &http.Client{Jar: jar, Timeout: 20 * time.Second}}
}

func (h *moduleHarness) login(email string) *apiClient {
	h.t.Helper()
	c := h.anonymous()
	c.name = email
	status, body, raw := c.do(http.MethodPost, "/api/v1/auth/login", map[string]any{"email": email, "password": demoPassword})
	if status != http.StatusOK {
		h.t.Fatalf("login %s = %d %s", email, status, raw)
	}
	_ = body
	return c
}

func (c *apiClient) do(method, path string, body any) (int, any, string) {
	c.h.t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			c.h.t.Fatalf("encode: %v", err)
		}
		reader = strings.NewReader(string(encoded))
	}
	req, err := http.NewRequest(method, c.h.base+path, reader)
	if err != nil {
		c.h.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		c.h.t.Fatalf("%s %s %s: %v", c.name, method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var decoded any
	_ = json.Unmarshal(raw, &decoded)
	return resp.StatusCode, decoded, string(raw)
}

// expect asserts a status and returns the decoded body.
func (c *apiClient) expect(want int, method, path string, body any) any {
	c.h.t.Helper()
	status, decoded, raw := c.do(method, path, body)
	if status != want {
		c.h.t.Fatalf("%s: %s %s = %d, want %d: %s", c.name, method, path, status, want, raw)
	}
	return decoded
}

// at walks a decoded JSON value by object keys and list indexes.
func at(v any, path ...any) any {
	for _, p := range path {
		switch key := p.(type) {
		case string:
			m, _ := v.(map[string]any)
			v = m[key]
		case int:
			list, _ := v.([]any)
			if key < 0 || key >= len(list) {
				return nil
			}
			v = list[key]
		}
	}
	return v
}

func list(v any, path ...any) []any {
	out, _ := at(v, path...).([]any)
	return out
}

func str(v any, path ...any) string {
	value := at(v, path...)
	if value == nil {
		return ""
	}
	if f, ok := value.(float64); ok && f == float64(int64(f)) {
		return fmt.Sprint(int64(f))
	}
	return fmt.Sprint(value)
}

func num(v any, path ...any) float64 {
	f, _ := at(v, path...).(float64)
	return f
}

func errorCode(v any) string { return str(v, "error", "code") }

func day(offset int) string {
	return time.Now().UTC().AddDate(0, 0, offset).Format("2006-01-02")
}

// ===========================================================================
// Projects & tasks
// ===========================================================================

func TestModuleProjectsAndTasks(t *testing.T) {
	h := newModuleHarness(t)

	if status, _, _ := h.anonymous().do(http.MethodGet, "/api/v1/projects", nil); status != http.StatusUnauthorized {
		t.Fatalf("anonymous project list = %d, want 401", status)
	}

	owner := h.login("user@example.com")   // usr_user_01
	member := h.login("coder@example.com") // usr_coder_01
	outsider := h.login("officer.koshi@example.com")
	manager := h.login("manager@example.com")
	admin := h.login("admin@example.com")

	// --- create + list --------------------------------------------------------
	created := owner.expect(201, http.MethodPost, "/api/v1/projects", map[string]any{
		"name": "Website relaunch", "description": "New marketing site", "due_date": day(30),
	})
	projectID := str(created, "project", "id")
	if projectID == "" || str(created, "project", "owner_id") != "usr_user_01" || str(created, "project", "status") != "active" {
		t.Fatalf("created project = %v", created)
	}
	// A client-supplied owner is not writable: owner_column stamps the caller.
	spoofed := owner.expect(201, http.MethodPost, "/api/v1/projects", map[string]any{"name": "Side project", "owner_id": "usr_admin_01"})
	if str(spoofed, "project", "owner_id") != "usr_user_01" {
		t.Fatalf("owner_id was spoofable: %v", spoofed)
	}
	owner.expect(422, http.MethodPost, "/api/v1/projects", map[string]any{"name": "x"})

	listed := owner.expect(200, http.MethodGet, "/api/v1/projects", nil)
	if len(list(listed, "projects")) != 2 || str(listed, "projects", 1, "my_role") != "owner" {
		t.Fatalf("owner's projects = %v", listed)
	}

	// --- membership & owner scoping ----------------------------------------------
	owner.expect(201, http.MethodPost, "/api/v1/projects/"+projectID+"/members", map[string]any{"user_id": "usr_coder_01"})
	member.expect(404, http.MethodPost, "/api/v1/projects/"+projectID+"/members", map[string]any{"user_id": "usr_officer_koshi"})
	owner.expect(404, http.MethodPost, "/api/v1/projects/"+projectID+"/members", map[string]any{"user_id": "nobody"})

	if got := list(member.expect(200, http.MethodGet, "/api/v1/projects", nil), "projects"); len(got) != 1 || str(got, 0, "my_role") != "member" {
		t.Fatalf("member's projects = %v", got)
	}
	if got := list(outsider.expect(200, http.MethodGet, "/api/v1/projects", nil), "projects"); len(got) != 0 {
		t.Fatalf("outsider sees projects: %v", got)
	}
	outsider.expect(404, http.MethodGet, "/api/v1/projects/"+projectID, nil)

	// Only the owner may update or delete: database.crud owner_column scoping
	// answers a member with 404.
	member.expect(404, http.MethodPatch, "/api/v1/projects/"+projectID, map[string]any{"status": "on_hold"})
	member.expect(404, http.MethodDelete, "/api/v1/projects/"+str(spoofed, "project", "id"), nil)
	updated := owner.expect(200, http.MethodPatch, "/api/v1/projects/"+projectID, map[string]any{"status": "on_hold"})
	if str(updated, "project", "status") != "on_hold" {
		t.Fatalf("update = %v", updated)
	}
	owner.expect(200, http.MethodDelete, "/api/v1/projects/"+str(spoofed, "project", "id"), nil)
	owner.expect(404, http.MethodGet, "/api/v1/projects/"+str(spoofed, "project", "id"), nil)

	// --- tasks --------------------------------------------------------------------
	task := owner.expect(201, http.MethodPost, "/api/v1/projects/"+projectID+"/tasks", map[string]any{
		"title": "Write launch copy", "assignee_id": "usr_coder_01", "priority": "high", "due_date": day(-2),
	})
	taskID := str(task, "task", "id")
	if taskID == "" || str(task, "task", "status") != "todo" || str(task, "task", "reporter_id") != "usr_user_01" {
		t.Fatalf("created task = %v", task)
	}
	// The assignee defaults to the caller.
	own := owner.expect(201, http.MethodPost, "/api/v1/projects/"+projectID+"/tasks", map[string]any{"title": "Review analytics"})
	if str(own, "task", "assignee_id") != "usr_user_01" || str(own, "task", "priority") != "medium" {
		t.Fatalf("defaulted task = %v", own)
	}
	body := owner.expect(422, http.MethodPost, "/api/v1/projects/"+projectID+"/tasks", map[string]any{"title": "Nope", "assignee_id": "usr_officer_koshi"})
	if !strings.Contains(str(body, "error", "message"), "member") {
		t.Fatalf("non-member assignee error = %v", body)
	}
	outsider.expect(404, http.MethodPost, "/api/v1/projects/"+projectID+"/tasks", map[string]any{"title": "Sneaky"})
	owner.expect(422, http.MethodPost, "/api/v1/projects/"+projectID+"/tasks", map[string]any{"title": "Bad priority", "priority": "whenever"})

	mine := member.expect(200, http.MethodGet, "/api/v1/tasks/mine", nil)
	if got := list(mine, "tasks"); len(got) != 1 || str(got, 0, "id") != taskID {
		t.Fatalf("member's tasks = %v", mine)
	}
	overdue := owner.expect(200, http.MethodGet, "/api/v1/tasks/overdue", nil)
	if got := list(overdue, "tasks"); len(got) != 1 || str(got, 0, "id") != taskID || num(got[0], "days_overdue") != 2 {
		t.Fatalf("overdue = %v", overdue)
	}
	if got := list(outsider.expect(200, http.MethodGet, "/api/v1/tasks/overdue", nil), "tasks"); len(got) != 0 {
		t.Fatalf("outsider sees overdue tasks: %v", got)
	}

	// The overdue flagger (run hourly by the schedule) is admin-only on demand.
	owner.expect(403, http.MethodPost, "/api/v1/admin/tasks/flag-overdue", nil)
	flagged := admin.expect(200, http.MethodPost, "/api/v1/admin/tasks/flag-overdue", nil)
	if num(flagged, "flagged") != 1 {
		t.Fatalf("flag-overdue = %v", flagged)
	}

	// --- comments -----------------------------------------------------------------
	member.expect(201, http.MethodPost, "/api/v1/tasks/"+taskID+"/comments", map[string]any{"body": "Draft is in the doc"})
	outsider.expect(404, http.MethodPost, "/api/v1/tasks/"+taskID+"/comments", map[string]any{"body": "hi"})
	if got := list(owner.expect(200, http.MethodGet, "/api/v1/tasks/"+taskID+"/comments", nil), "comments"); len(got) != 1 || str(got, 0, "author_id") != "usr_coder_01" {
		t.Fatalf("comments = %v", got)
	}

	// --- status workflow ------------------------------------------------------------
	path := "/api/v1/tasks/" + taskID + "/transition"
	for _, bad := range []string{"done", "review", "todo"} {
		body := member.expect(422, http.MethodPost, path, map[string]any{"status": bad})
		if !strings.Contains(str(body, "error", "message"), "invalid status transition") {
			t.Fatalf("todo->%s error = %v", bad, body)
		}
	}
	member.expect(422, http.MethodPost, path, map[string]any{"status": "archived"})
	outsider.expect(404, http.MethodPost, path, map[string]any{"status": "in_progress"})

	started := member.expect(200, http.MethodPost, path, map[string]any{"status": "in_progress"})
	if str(started, "transition", "outcome") != "apply" || str(started, "applied", "value") != "apply" {
		t.Fatalf("todo->in_progress = %v", started)
	}
	member.expect(422, http.MethodPost, path, map[string]any{"status": "done"})

	submitted := member.expect(200, http.MethodPost, path, map[string]any{"status": "review"})
	runID := str(submitted, "applied", "result", "run", "run_id")
	if str(submitted, "transition", "outcome") != "submit" || runID == "" || str(submitted, "applied", "result", "run", "status") != "waiting" {
		t.Fatalf("in_progress->review = %v", submitted)
	}

	// The approval queue is managers only; the submitter never approves.
	member.expect(403, http.MethodGet, "/api/v1/approvals", nil)
	approvals := list(manager.expect(200, http.MethodGet, "/api/v1/approvals", nil), "approvals")
	if len(approvals) != 1 || !strings.Contains(str(approvals, 0, "title"), "Write launch copy") {
		t.Fatalf("manager approvals = %v", approvals)
	}
	approvalID := str(approvals, 0, "task_id")
	manager.expect(422, http.MethodPost, "/api/v1/approvals/"+approvalID+"/decide", map[string]any{"action": "maybe"})
	manager.expect(200, http.MethodPost, "/api/v1/approvals/"+approvalID+"/decide", map[string]any{"action": "approve", "note": "ship it"})

	// The process continues on the durable queue and marks the task done.
	status := waitFor(t, 15*time.Second, func() (string, bool) {
		detail := owner.expect(200, http.MethodGet, "/api/v1/projects/"+projectID, nil)
		for _, item := range list(detail, "tasks") {
			if str(item, "id") == taskID {
				return str(item, "status"), str(item, "status") == "done"
			}
		}
		return "missing", false
	})
	if status != "done" {
		t.Fatalf("approved task status = %s", status)
	}
	if got := list(member.expect(200, http.MethodGet, "/api/v1/tasks/mine", nil), "tasks"); len(got) != 0 {
		t.Fatalf("done task still in my tasks: %v", got)
	}
	if got := list(owner.expect(200, http.MethodGet, "/api/v1/tasks/overdue", nil), "tasks"); len(got) != 0 {
		t.Fatalf("done task still overdue: %v", got)
	}

	// Reject path: the second task goes back to in_progress.
	ownPath := "/api/v1/tasks/" + str(own, "task", "id") + "/transition"
	owner.expect(200, http.MethodPost, ownPath, map[string]any{"status": "in_progress"})
	owner.expect(200, http.MethodPost, ownPath, map[string]any{"status": "review"})
	approvals = list(manager.expect(200, http.MethodGet, "/api/v1/approvals", nil), "approvals")
	if len(approvals) != 1 {
		t.Fatalf("second approval queue = %v", approvals)
	}
	manager.expect(200, http.MethodPost, "/api/v1/approvals/"+str(approvals, 0, "task_id")+"/decide", map[string]any{"action": "reject", "note": "needs numbers"})
	waitFor(t, 15*time.Second, func() (string, bool) {
		detail := owner.expect(200, http.MethodGet, "/api/v1/projects/"+projectID, nil)
		for _, item := range list(detail, "tasks") {
			if str(item, "id") == str(own, "task", "id") {
				return str(item, "status"), str(item, "status") == "in_progress"
			}
		}
		return "missing", false
	})

	// The web page lists the project.
	page := pageFor(t, owner, "/projects")
	if !strings.Contains(page, "Website relaunch") || !strings.Contains(page, "My Open Tasks") {
		t.Fatalf("projects page does not list the project:\n%s", page)
	}
}

// waitFor polls until done reports true, returning the last observed value.
func waitFor(t *testing.T, timeout time.Duration, poll func() (string, bool)) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	last := ""
	for time.Now().Before(deadline) {
		value, done := poll()
		last = value
		if done {
			return value
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting; last value %q", last)
	return last
}

// pageFor fetches an HTML page as a browser would.
func pageFor(t *testing.T, c *apiClient, path string) string {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, c.h.base+path, nil)
	req.Header.Set("Accept", "text/html")
	resp, err := c.http.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s as %s = %d: %s", path, c.name, resp.StatusCode, raw)
	}
	return string(raw)
}

// ===========================================================================
// Government projects across the hierarchy
// ===========================================================================

func TestModuleGovHierarchy(t *testing.T) {
	h := newModuleHarness(t)

	plainUser := h.login("user@example.com")
	bagmati := h.login("officer.bagmati@example.com")
	koshi := h.login("officer.koshi@example.com")
	ktm := h.login("officer.ktm@example.com")
	admin := h.login("admin@example.com")

	// RBAC first: a plain user has no gov permission at all.
	plainUser.expect(403, http.MethodGet, "/api/v1/gov/projects", nil)

	scope := bagmati.expect(200, http.MethodGet, "/api/v1/gov/scope", nil)
	if str(scope, "id") != "bagmati" || str(scope, "path") != "/np/bagmati/" {
		t.Fatalf("bagmati scope = %v", scope)
	}
	if got := str(admin.expect(200, http.MethodGet, "/api/v1/gov/scope", nil), "id"); got != "np" {
		t.Fatalf("admin scope = %s, want the root", got)
	}
	tree := ktm.expect(200, http.MethodGet, "/api/v1/gov/tree", nil)
	if str(tree, "tree", "id") != "ktm" || len(list(tree, "tree", "children")) != 2 {
		t.Fatalf("ktm tree = %v", tree)
	}

	// --- create, scoped by jurisdiction -----------------------------------------------
	ktmProject := ktm.expect(201, http.MethodPost, "/api/v1/gov/projects", map[string]any{
		"org_unit_id": "ktm-metro", "title": "Durbar Square restoration", "budget_total": 1000,
		"start_date": day(0), "end_date": day(365),
	})
	ktmID := str(ktmProject, "project", "id")
	if ktmID == "" || str(ktmProject, "project", "org_path") != "/np/bagmati/ktm/ktm-metro/" || str(ktmProject, "project", "status") != "proposed" {
		t.Fatalf("ktm project = %v", ktmProject)
	}
	// Lalitpur is in Bagmati but outside the Kathmandu officer's district.
	ktm.expect(403, http.MethodPost, "/api/v1/gov/projects", map[string]any{"org_unit_id": "lalitpur-metro", "title": "Ring road", "budget_total": 10})
	ktm.expect(404, http.MethodPost, "/api/v1/gov/projects", map[string]any{"org_unit_id": "atlantis", "title": "Nowhere", "budget_total": 10})
	ktm.expect(422, http.MethodPost, "/api/v1/gov/projects", map[string]any{"org_unit_id": "tokha", "title": "Backwards", "budget_total": 10, "start_date": day(10), "end_date": day(1)})
	koshiProject := koshi.expect(201, http.MethodPost, "/api/v1/gov/projects", map[string]any{
		"org_unit_id": "biratnagar", "title": "Biratnagar drainage", "budget_total": 5000,
	})
	koshiID := str(koshiProject, "project", "id")
	bagmati.expect(201, http.MethodPost, "/api/v1/gov/projects", map[string]any{"org_unit_id": "lalitpur", "title": "Lalitpur schools", "budget_total": 200})

	// --- hierarchy-scoped listing -----------------------------------------------------
	titles := func(c *apiClient) string {
		var out []string
		for _, p := range list(c.expect(200, http.MethodGet, "/api/v1/gov/projects", nil), "projects") {
			out = append(out, str(p, "title"))
		}
		return strings.Join(out, "|")
	}
	if got := titles(ktm); got != "Durbar Square restoration" {
		t.Fatalf("ktm officer sees %q", got)
	}
	if got := titles(bagmati); got != "Lalitpur schools|Durbar Square restoration" {
		t.Fatalf("bagmati officer sees %q", got)
	}
	if got := titles(koshi); got != "Biratnagar drainage" {
		t.Fatalf("koshi officer sees %q", got)
	}
	if got := titles(admin); got != "Lalitpur schools|Biratnagar drainage|Durbar Square restoration" {
		t.Fatalf("admin sees %q", got)
	}
	// The officer of another state cannot read, change or budget the project.
	koshi.expect(404, http.MethodGet, "/api/v1/gov/projects/"+ktmID, nil)
	koshi.expect(404, http.MethodPost, "/api/v1/gov/projects/"+ktmID+"/status", map[string]any{"status": "approved"})
	koshi.expect(404, http.MethodPost, "/api/v1/gov/projects/"+ktmID+"/budget-lines", map[string]any{"code": "civil", "amount": 1})
	if got := list(koshi.expect(200, http.MethodGet, "/api/v1/gov/projects?status=proposed", nil), "projects"); len(got) != 1 {
		t.Fatalf("status filter = %v", got)
	}

	// --- budget heads inherited down the hierarchy -------------------------------------
	heads := ktm.expect(200, http.MethodGet, "/api/v1/gov/budget-heads?org_unit=ktm-metro", nil)
	var codes []string
	for _, entry := range list(heads, "budget_heads") {
		codes = append(codes, str(entry, "code")+"="+str(entry, "label"))
	}
	joined := strings.Join(codes, "|")
	if !strings.Contains(joined, "civil=Civil works (provincial schedule)") || !strings.Contains(joined, "heritage=Heritage conservation") || strings.Contains(joined, "consultancy") {
		t.Fatalf("ktm-metro budget heads = %s", joined)
	}
	koshi.expect(403, http.MethodGet, "/api/v1/gov/budget-heads?org_unit=ktm-metro", nil)

	lines := "/api/v1/gov/projects/" + ktmID + "/budget-lines"
	line := ktm.expect(201, http.MethodPost, lines, map[string]any{"code": "heritage", "amount": 300, "note": "stone masons"})
	if str(line, "line", "code") != "heritage" {
		t.Fatalf("budget line = %v", line)
	}
	if body := ktm.expect(422, http.MethodPost, lines, map[string]any{"code": "consultancy", "amount": 10}); !strings.Contains(str(body, "error", "message"), "consultancy") {
		t.Fatalf("disabled head error = %v", body)
	}
	if body := ktm.expect(422, http.MethodPost, lines, map[string]any{"code": "civil", "amount": 800}); !strings.Contains(str(body, "error", "message"), "budget") {
		t.Fatalf("over-budget error = %v", body)
	}
	// "heritage" only exists in Kathmandu Metropolitan City.
	koshi.expect(422, http.MethodPost, "/api/v1/gov/projects/"+koshiID+"/budget-lines", map[string]any{"code": "heritage", "amount": 1})
	koshi.expect(201, http.MethodPost, "/api/v1/gov/projects/"+koshiID+"/budget-lines", map[string]any{"code": "consultancy", "amount": 100})

	detail := bagmati.expect(200, http.MethodGet, "/api/v1/gov/projects/"+ktmID, nil)
	if num(detail, "project", "budget_allocated") != 300 || len(list(detail, "budget_lines")) != 1 || len(list(detail, "ancestors")) != 3 {
		t.Fatalf("project detail = %v", detail)
	}

	// --- status workflow ---------------------------------------------------------------
	ktm.expect(422, http.MethodPost, "/api/v1/gov/projects/"+ktmID+"/status", map[string]any{"status": "completed"})
	ktm.expect(200, http.MethodPost, "/api/v1/gov/projects/"+ktmID+"/status", map[string]any{"status": "approved"})

	// --- admin reassignment through database.crud org scoping ---------------------------
	koshi.expect(403, http.MethodPut, "/api/v1/gov/projects/"+koshiID+"/unit", map[string]any{"org_unit_id": "tokha"})
	moved := admin.expect(200, http.MethodPut, "/api/v1/gov/projects/"+koshiID+"/unit", map[string]any{"org_unit_id": "tokha"})
	if str(moved, "project", "org_path") != "/np/bagmati/ktm/tokha/" {
		t.Fatalf("reassigned project = %v", moved)
	}
	if got := titles(koshi); got != "" {
		t.Fatalf("koshi still sees %q after the move", got)
	}
	if got := titles(ktm); got != "Biratnagar drainage|Durbar Square restoration" {
		t.Fatalf("ktm officer after the move sees %q", got)
	}

	page := pageFor(t, bagmati, "/gov")
	if !strings.Contains(page, "Durbar Square restoration") || !strings.Contains(page, "Bagmati Province") {
		t.Fatalf("gov page:\n%s", page)
	}
}

// ===========================================================================
// Medical coding
// ===========================================================================

func TestModuleMedicalCoding(t *testing.T) {
	h := newModuleHarness(t)

	plainUser := h.login("user@example.com")
	coder := h.login("coder@example.com")
	coder2 := h.login("coder2@example.com")

	multi := map[string]any{
		"patient_id": "PAT-1", "provider_id": "DR-7", "place_of_service": "21",
		"dos_from": day(-10), "dos_to": day(-8),
		"lines": []any{
			map[string]any{"cpt": "99223", "icd": "J18.9", "units": 1},
			map[string]any{"cpt": "71046", "icd": "J18.9", "units": 1, "dos": day(-9)},
		},
	}
	plainUser.expect(403, http.MethodPost, "/api/v1/coding/encounters", multi)

	preview := coder.expect(200, http.MethodPost, "/api/v1/coding/encounters/preview", multi)
	if len(list(preview, "lines")) != 4 || str(preview, "period", "kind") != "multi" {
		t.Fatalf("preview = %v", preview)
	}

	created := coder.expect(201, http.MethodPost, "/api/v1/coding/encounters", multi)
	encounterID := str(created, "saved", "parent", "id")
	if encounterID == "" || num(created, "saved", "inserted") != 4 || str(created, "saved", "parent", "dos_kind") != "multi" || num(created, "saved", "parent", "days") != 3 {
		t.Fatalf("created encounter = %v", created)
	}
	lines := coder.expect(200, http.MethodGet, "/api/v1/coding/encounters/"+encounterID+"/lines", nil)
	var dates []string
	for _, l := range list(lines, "lines") {
		dates = append(dates, str(l, "dos")+"/"+str(l, "cpt"))
	}
	want := strings.Join([]string{day(-10) + "/99223", day(-9) + "/99223", day(-9) + "/71046", day(-8) + "/99223"}, ",")
	if got := strings.Join(dates, ","); got != want {
		t.Fatalf("per-day lines = %s, want %s", got, want)
	}

	// Duplicate billing: same patient and provider on a covered date.
	dup := coder.expect(409, http.MethodPost, "/api/v1/coding/encounters", map[string]any{
		"patient_id": "PAT-1", "provider_id": "DR-7", "dos": day(-9),
		"lines": []any{map[string]any{"cpt": "99232", "icd": "J18.9", "units": 1}},
	})
	if errorCode(dup) != "DOS_OVERLAP" {
		t.Fatalf("overlap error = %v", dup)
	}
	// A different provider on the same day is a different claim.
	single := coder.expect(201, http.MethodPost, "/api/v1/coding/encounters", map[string]any{
		"patient_id": "PAT-1", "provider_id": "DR-9", "dos": day(-9),
		"lines": []any{map[string]any{"cpt": "99232", "icd": "J18.9", "units": 2}},
	})
	if str(single, "saved", "parent", "dos_kind") != "single" || num(single, "saved", "inserted") != 1 {
		t.Fatalf("single-DOS encounter = %v", single)
	}

	// DOS rules: no future dates, lines inside the period — every violation reported.
	bad := coder.expect(422, http.MethodPost, "/api/v1/coding/encounters", map[string]any{
		"patient_id": "PAT-2", "provider_id": "DR-7", "dos": day(3),
		"lines": []any{map[string]any{"cpt": "99213", "icd": "Z00.0", "units": 1, "dos": day(-40)}},
	})
	if errorCode(bad) != "INVALID_DATE_OF_SERVICE" || len(list(bad, "error", "details")) < 2 {
		t.Fatalf("DOS validation = %v", bad)
	}
	coder.expect(422, http.MethodPost, "/api/v1/coding/encounters", map[string]any{
		"patient_id": "PAT-3", "provider_id": "DR-7", "dos": day(-1),
		"lines": []any{map[string]any{"cpt": "bad", "icd": "Z00.0"}},
	})

	// --- work queue & coding status ------------------------------------------------------
	queue := coder.expect(200, http.MethodGet, "/api/v1/coding/queue", nil)
	if len(list(queue, "queue")) != 2 {
		t.Fatalf("queue = %v", queue)
	}
	coder.expect(200, http.MethodPost, "/api/v1/coding/encounters/"+encounterID+"/claim", nil)
	coder2.expect(404, http.MethodPost, "/api/v1/coding/encounters/"+encounterID+"/claim", nil)
	if got := list(coder2.expect(200, http.MethodGet, "/api/v1/coding/queue", nil), "queue"); len(got) != 1 {
		t.Fatalf("coder2 queue should hold only the unclaimed encounter: %v", got)
	}
	coder2.expect(403, http.MethodPost, "/api/v1/coding/encounters/"+encounterID+"/status", map[string]any{"status": "coded"})
	coder.expect(200, http.MethodPost, "/api/v1/coding/encounters/"+encounterID+"/status", map[string]any{"status": "coded"})
	coder.expect(422, http.MethodPost, "/api/v1/coding/encounters/"+encounterID+"/status", map[string]any{"status": "on_hold"})

	coded := list(coder.expect(200, http.MethodGet, "/api/v1/coding/encounters?status=coded", nil), "encounters")
	if len(coded) != 1 || str(coded, 0, "coded_by") != "usr_coder_01" || num(coded[0], "line_count") != 4 {
		t.Fatalf("coded encounters = %v", coded)
	}
	// Nobody holds the pending encounter, so only a manager could void it;
	// claimed, its coder can. Once voided it no longer blocks its dates.
	singleID := str(single, "saved", "parent", "id")
	coder.expect(403, http.MethodPost, "/api/v1/coding/encounters/"+singleID+"/status", map[string]any{"status": "void"})
	coder.expect(200, http.MethodPost, "/api/v1/coding/encounters/"+singleID+"/claim", nil)
	coder.expect(200, http.MethodPost, "/api/v1/coding/encounters/"+singleID+"/status", map[string]any{"status": "void"})
	coder.expect(201, http.MethodPost, "/api/v1/coding/encounters", map[string]any{
		"patient_id": "PAT-1", "provider_id": "DR-9", "dos": day(-9),
		"lines": []any{map[string]any{"cpt": "99232", "icd": "J18.9", "units": 1}},
	})

	page := pageFor(t, coder, "/coding")
	if !strings.Contains(page, "Coding Work Queue") || !strings.Contains(page, "PAT-1") {
		t.Fatalf("coding page:\n%s", page)
	}
}

// ===========================================================================
// Activity feed
// ===========================================================================

func TestModuleActivityFeed(t *testing.T) {
	h := newModuleHarness(t)

	user := h.login("user@example.com")
	manager := h.login("manager@example.com")
	user.expect(201, http.MethodPost, "/api/v1/projects", map[string]any{"name": "Activity demo"})
	project := user.expect(201, http.MethodPost, "/api/v1/projects", map[string]any{"name": "Second demo"})
	user.expect(201, http.MethodPost, "/api/v1/projects/"+str(project, "project", "id")+"/tasks", map[string]any{"title": "Tracked task"})

	// Everyone else's activity is managers-only; a user sees their own.
	user.expect(403, http.MethodGet, "/api/v1/activity", nil)
	user.expect(403, http.MethodGet, "/api/v1/activity/summary", nil)
	mine := list(user.expect(200, http.MethodGet, "/api/v1/activity/me", nil), "events")
	actions := map[string]int{}
	for _, e := range mine {
		actions[str(e, "action")]++
	}
	if actions["auth.login"] != 1 || actions["project.created"] != 2 || actions["task.created"] != 1 {
		t.Fatalf("my activity = %v", actions)
	}
	if got := list(user.expect(200, http.MethodGet, "/api/v1/activity/me?action=task.", nil), "events"); len(got) != 1 {
		t.Fatalf("my task activity = %v", got)
	}

	// Filters: actor, action prefix, date range, limit.
	feed := manager.expect(200, http.MethodGet, "/api/v1/activity?actor=usr_user_01", nil)
	if num(feed, "count") != 4 {
		t.Fatalf("actor filter = %v", feed)
	}
	for _, e := range list(feed, "events") {
		if str(e, "actor_id") != "usr_user_01" || str(e, "actor_name") != "Charlie User" {
			t.Fatalf("actor filter leaked %v", e)
		}
	}
	projectsOnly := manager.expect(200, http.MethodGet, "/api/v1/activity?action=project.", nil)
	if num(projectsOnly, "count") != 2 {
		t.Fatalf("action filter = %v", projectsOnly)
	}
	for _, e := range list(projectsOnly, "events") {
		if !strings.HasPrefix(str(e, "action"), "project.") {
			t.Fatalf("action filter leaked %v", e)
		}
	}
	today := day(0)
	if got := num(manager.expect(200, http.MethodGet, "/api/v1/activity?since="+today+"&until="+today, nil), "count"); got != 5 {
		t.Fatalf("today's activity = %v, want all 5 events (2 logins, 2 projects, 1 task)", got)
	}
	if got := num(manager.expect(200, http.MethodGet, "/api/v1/activity?since="+day(1), nil), "count"); got != 0 {
		t.Fatalf("activity since tomorrow = %v", got)
	}
	if got := num(manager.expect(200, http.MethodGet, "/api/v1/activity?until="+day(-1), nil), "count"); got != 0 {
		t.Fatalf("activity until yesterday = %v", got)
	}
	if got := num(manager.expect(200, http.MethodGet, "/api/v1/activity?limit=2", nil), "count"); got != 2 {
		t.Fatalf("limit = %v", got)
	}
	q := url.Values{"actor": {"usr_user_01"}, "action": {"auth."}}
	if got := num(manager.expect(200, http.MethodGet, "/api/v1/activity?"+q.Encode(), nil), "count"); got != 1 {
		t.Fatalf("combined filters = %v", got)
	}

	// Per-user summary (SQL GROUP BY) and totals (data.aggregate).
	summary := manager.expect(200, http.MethodGet, "/api/v1/activity/summary?since="+today, nil)
	var charlie any
	for _, row := range list(summary, "users") {
		if str(row, "actor_id") == "usr_user_01" {
			charlie = row
		}
	}
	if charlie == nil || num(charlie, "total") != 4 || num(charlie, "logins") != 1 || num(charlie, "project_events") != 3 {
		t.Fatalf("summary = %v", summary)
	}
	if num(summary, "totals", "sum") < 5 {
		t.Fatalf("summary totals = %v", at(summary, "totals"))
	}
	perUser := manager.expect(200, http.MethodGet, "/api/v1/activity/users/usr_user_01/summary", nil)
	if len(list(perUser, "counts")) != 3 || str(perUser, "counts", 0, "action") != "project.created" || len(list(perUser, "by_action", "project.created")) != 2 {
		t.Fatalf("per-user summary = %v", perUser)
	}

	page := pageFor(t, manager, "/activity")
	if !strings.Contains(page, "project.created") || !strings.Contains(page, "Charlie User") {
		t.Fatalf("activity page:\n%s", page)
	}
	req, _ := http.NewRequest(http.MethodGet, h.base+"/activity", nil)
	req.Header.Set("Accept", "text/html")
	resp, err := user.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("user on /activity = %d, want 403", resp.StatusCode)
	}
}

// ===========================================================================
// Regressions in the original boilerplate intents, found while wiring the
// modules: a decision.expression node publishes no fact, so the node that
// required it never ran (registration and role updates always answered 403),
// and "&&"/"||" are not boolean operators in the expression language.
// ===========================================================================

func TestBoilerplateAuthFlowsRegression(t *testing.T) {
	h := newModuleHarness(t)

	anon := h.anonymous()
	registered := anon.expect(201, http.MethodPost, "/api/v1/auth/register", map[string]any{
		"email": "Newcomer@Example.com", "name": "Newcomer", "password": "Password123!",
	})
	if str(registered, "principal", "id") != "newcomer@example.com" || at(registered, "principal", "claims", "password_hash") != nil {
		t.Fatalf("register = %v", registered)
	}
	// The registration opened a session.
	anon.expect(200, http.MethodGet, "/api/v1/me", nil)
	dup := h.anonymous().expect(422, http.MethodPost, "/api/v1/auth/register", map[string]any{
		"email": "newcomer@example.com", "name": "Again", "password": "Password123!",
	})
	if !strings.Contains(str(dup, "error", "message"), "already exists") {
		t.Fatalf("duplicate registration = %v", dup)
	}
	h.login("newcomer@example.com")

	admin := h.login("admin@example.com")
	admin.expect(200, http.MethodPost, "/dashboard/admin/role", map[string]any{"user_id": "usr_user_01", "role": "officer"})
	body := admin.expect(403, http.MethodPost, "/dashboard/admin/role", map[string]any{"user_id": "usr_user_01", "role": "super_admin"})
	if !strings.Contains(str(body, "error", "message"), "privilege escalation") {
		t.Fatalf("escalation = %v", body)
	}
	h.login("user@example.com").expect(403, http.MethodPost, "/dashboard/admin/role", map[string]any{"user_id": "usr_user_01", "role": "admin"})
}
