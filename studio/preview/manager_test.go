package preview

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oarkflow/fh"
	"github.com/oarkflow/ref/platform"
	"github.com/oarkflow/ref/studio"
	"github.com/oarkflow/template"
)

func newService(t *testing.T, opts Options) *Service {
	t.Helper()
	if opts.TempDir == "" {
		opts.TempDir = t.TempDir()
	}
	if opts.Grace == 0 {
		opts.Grace = time.Millisecond
	}
	s := New(opts)
	t.Cleanup(s.Close)
	return s
}

func get(t *testing.T, h http.Handler, method, target string, body string) *http.Response {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Result()
}

func readBody(t *testing.T, r *http.Response) string {
	t.Helper()
	b, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatal(err)
	}
	_ = r.Body.Close()
	return string(b)
}

// site is a static dir with an HTML page holding root-relative links.
func site(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	html := `<!doctype html><html><body><a href="/hello">hi</a><img src='/site/logo.png'><a href="//cdn.example/x">cdn</a><a href="rel.html">r</a><form action="/notify"></form></body></html>`
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte(html), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestEnsureBuildsAndServes(t *testing.T) {
	tr := newTrap(t)
	s := newService(t, Options{BaseDir: t.TempDir()})
	d := newDraft(t, "d1", appBundle(tr.addr(), site(t))...)

	st, err := s.EnsureStatus(context.Background(), d)
	if err != nil || st.State != StateReady || st.URL != "/preview/d1/" || st.Serving != d.Version() {
		t.Fatalf("status=%+v err=%v", st, err)
	}
	if len(st.Sandbox) == 0 {
		t.Error("sandbox report empty")
	}
	resp := get(t, s.Handler(), "GET", "/preview/d1/hello", "")
	if body := readBody(t, resp); resp.StatusCode != 200 || !strings.Contains(body, "pong") {
		t.Fatalf("status=%d body=%q", resp.StatusCode, body)
	}
	reqs := s.RequestLog("d1")
	if len(reqs) != 1 || reqs[0].Route != "hello" || reqs[0].Intent != "hello" || reqs[0].Path != "/hello" || reqs[0].Status != 200 {
		t.Fatalf("recorded %+v", reqs)
	}
	if reqs[0].Version != d.Version() {
		t.Errorf("version %d", reqs[0].Version)
	}
}

func TestUnknownPreviewIs404JSON(t *testing.T) {
	s := newService(t, Options{})
	for _, path := range []string{"/preview/nope/", "/preview/", "/other"} {
		resp := get(t, s.Handler(), "GET", path, "")
		if resp.StatusCode != 404 || !strings.Contains(resp.Header.Get("Content-Type"), "json") || !strings.Contains(readBody(t, resp), `"error"`) {
			t.Errorf("%s: %d %v", path, resp.StatusCode, resp.Header)
		}
	}
}

func TestRebuildOnlyWhenVersionChanges(t *testing.T) {
	var builds atomic.Int32
	s := newService(t, Options{BaseDir: t.TempDir(), AfterBuild: func(context.Context, string, *platform.Platform) error {
		builds.Add(1)
		return nil
	}})
	tr := newTrap(t)
	d := newDraft(t, "d2", appBundle(tr.addr(), site(t))...)
	ctx := context.Background()

	first, _ := s.EnsureStatus(ctx, d)
	again, _ := s.EnsureStatus(ctx, d)
	if builds.Load() != 1 || first.Version != again.Version || again.State != StateReady {
		t.Fatalf("builds=%d first=%+v again=%+v", builds.Load(), first, again)
	}
	e := s.entry("d2", false)
	old := e.current()

	// Change a route path and bump the version.
	files := appBundle(tr.addr(), site(t))
	files[2].Content = strings.Replace(files[2].Content, `"/hello"`, `"/hello2"`, 1)
	d.set(t, files...)
	next, _ := s.EnsureStatus(ctx, d)
	if builds.Load() != 2 || next.Serving != d.Version() || e.current() == old {
		t.Fatalf("builds=%d next=%+v", builds.Load(), next)
	}
	if r := get(t, s.Handler(), "GET", "/preview/d2/hello2", ""); r.StatusCode != 200 {
		t.Errorf("new route: %d", r.StatusCode)
	}
	if r := get(t, s.Handler(), "GET", "/preview/d2/hello", ""); r.StatusCode == 200 {
		t.Errorf("old route still served")
	}
	// The retired generation is drained and its temp dir removed.
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(old.root); os.IsNotExist(err) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("old generation's temp dir not removed")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestFailedBuildReportsFileAndLineAndKeepsServing(t *testing.T) {
	tr := newTrap(t)
	s := newService(t, Options{BaseDir: t.TempDir()})
	d := newDraft(t, "d3", appBundle(tr.addr(), site(t))...)
	ctx := context.Background()
	if st, _ := s.EnsureStatus(ctx, d); st.State != StateReady {
		t.Fatalf("baseline: %+v", st)
	}
	good := d.Version()

	// A syntax error on line 4 of the second file.
	files := appBundle(tr.addr(), site(t))
	files[1].Content = "\n# comment\nintent \"hello\" {\n  response = = \"pong\"\n}\n"
	d.set(t, files...)
	st, err := s.EnsureStatus(ctx, d)
	if err != nil || st.State != StateFailed || st.Version != d.Version() || st.Serving != good {
		t.Fatalf("status=%+v err=%v", st, err)
	}
	if len(st.Diagnostics) == 0 {
		t.Fatal("no diagnostics")
	}
	dg := st.Diagnostics[0]
	if dg.File != "01_logic.bcl" || dg.Line != 4 {
		t.Fatalf("diagnostic %+v; want 01_logic.bcl line 4", dg)
	}
	// The previous build keeps answering, and asking again is cached.
	if r := get(t, s.Handler(), "GET", "/preview/d3/hello", ""); r.StatusCode != 200 {
		t.Errorf("previous generation stopped serving: %d", r.StatusCode)
	}
	again, _ := s.EnsureStatus(ctx, d)
	if again.State != StateFailed {
		t.Errorf("failure not cached: %+v", again)
	}
}

func TestSemanticFailureNamesBlock(t *testing.T) {
	tr := newTrap(t)
	s := newService(t, Options{BaseDir: t.TempDir()})
	files := appBundle(tr.addr(), site(t))
	// Parses fine, but the route points at an intent that does not exist.
	files[2].Content = strings.Replace(files[2].Content, `intent "hello" }`, `intent "missing" }`, 1)
	d := newDraft(t, "d4", files...)
	st, _ := s.EnsureStatus(context.Background(), d)
	if st.State != StateFailed || len(st.Diagnostics) == 0 {
		t.Fatalf("%+v", st)
	}
	if !strings.Contains(st.Error, "missing") {
		t.Errorf("error does not mention the intent: %s", st.Error)
	}
	if got := st.Diagnostics[0]; got.File != "02_routes.bcl" || got.Line == 0 {
		t.Errorf("no file/line on semantic error: %+v", got)
	}
}

func TestSandboxedBuildRefusesUnknownKind(t *testing.T) {
	tr := newTrap(t)
	s := newService(t, Options{BaseDir: t.TempDir()})
	files := appBundle(tr.addr(), site(t))
	files[0].Content += "\nresource \"r\" { kind \"cache.redis\" }\n"
	st, _ := s.EnsureStatus(context.Background(), newDraft(t, "d5", files...))
	if st.State != StateFailed || !strings.Contains(st.Error, "cache.redis") {
		t.Fatalf("%+v", st)
	}
}

func TestSandboxSafetyOutboundAndFiles(t *testing.T) {
	tr := newTrap(t)
	tmp := t.TempDir()
	cwd := t.TempDir()
	t.Chdir(cwd)
	s := newService(t, Options{BaseDir: t.TempDir(), TempDir: tmp})
	d := newDraft(t, "safe", appBundle(tr.addr(), site(t))...)
	st, _ := s.EnsureStatus(context.Background(), d)
	if st.State != StateReady {
		t.Fatalf("%+v", st)
	}
	resp := get(t, s.Handler(), "POST", "/preview/safe/notify", `{"a":1}`)
	if body := readBody(t, resp); resp.StatusCode != 200 {
		t.Fatalf("notify: %d %s", resp.StatusCode, body)
	}
	if n := tr.count(); n != 0 {
		t.Fatalf("%d connection(s) reached the real host", n)
	}
	out := s.Outbound("safe")
	kinds := map[string]OutboundCall{}
	for _, c := range out {
		kinds[c.Kind] = c
	}
	if kinds["http"].Method != "POST" || !strings.HasSuffix(kinds["http"].Target, "/hook") || !strings.Contains(kinds["http"].Preview, `"a":1`) {
		t.Errorf("http call not recorded: %+v", out)
	}
	if !strings.Contains(kinds["smtp"].Target, "someone@example.test") || !strings.Contains(kinds["smtp"].Preview, "Hello") {
		t.Errorf("mail not recorded: %+v", out)
	}
	// The database driver in the document was pgx to nowhere.invalid; the
	// sandbox put a sqlite file in the temp root instead.
	var dbs []string
	_ = filepath.WalkDir(tmp, func(p string, de os.DirEntry, _ error) error {
		if strings.HasSuffix(p, ".db") {
			dbs = append(dbs, p)
		}
		return nil
	})
	root := s.entry("safe", false).current().root
	if !strings.HasPrefix(root, tmp) {
		t.Fatalf("root %q outside temp dir %q", root, tmp)
	}
	// Nothing was created in the working directory.
	if ents, _ := os.ReadDir(cwd); len(ents) != 0 {
		t.Errorf("files created in cwd: %v", ents)
	}
	s.Stop("safe")
	deadline := time.Now().Add(3 * time.Second)
	for {
		if ents, _ := os.ReadDir(tmp); len(ents) == 0 {
			break
		}
		if time.Now().After(deadline) {
			ents, _ := os.ReadDir(tmp)
			t.Fatalf("temp dir not cleaned after Stop: %v", ents)
		}
		time.Sleep(10 * time.Millisecond)
	}
	_ = dbs
}

// snapshot lists every file under dir with size and mtime.
func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	_ = filepath.WalkDir(dir, func(p string, de os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		info, _ := de.Info()
		if info != nil {
			out[p] = info.ModTime().String() + "/" + string(rune(info.Size()))
		}
		return nil
	})
	return out
}

func TestSandboxSafetyStarter(t *testing.T) {
	starterDir, err := filepath.Abs(filepath.Join("..", "..", "examples", "starter"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(starterDir); err != nil {
		t.Skip("starter not present")
	}
	b := starterBundle(t)
	t.Chdir(starterDir)
	before := snapshot(t, starterDir)
	tmp := t.TempDir()
	tr := newTrap(t)
	home := t.TempDir()
	t.Setenv("HOME", home)                       // tcpguard's GeoIP cache would land in $HOME/.ipdata
	t.Setenv("NOTIFY_HOST", hostname(tr.addr())) // host env must not matter, and must not be read
	t.Setenv("NOTIFY_URL", "http://"+tr.addr()+"/welcome")
	t.Setenv("DB_DSN", "file:"+filepath.Join(starterDir, ".data", "should-not-be-used.db"))

	s := newService(t, Options{TempDir: tmp, Registry: starterRegistry, BaseDir: filepath.Join(starterDir, "resources", "config"),
		NewApp: func() *fh.App {
			engine := template.NewSPL("resources/templates", ".html").Config(template.SPLConfig{
				Directory: "resources/templates", Extension: ".html", SSR: true, SecureMode: false,
				Globals: map[string]any{"title": "starter", "appName": "starter", "appVersion": "0", "currentYear": "2026",
					"environment": "preview", "demoPassword": "", "error": "", "success": "", "user": map[string]any{},
					"users": []map[string]any{}, "redirect": "/dashboard", "email": "", "name": "", "token": ""},
			})
			return fh.New(fh.WithStartupBannerDisabled(true), fh.WithTemplateEngine(engine))
		}})
	d := &testDraft{id: "starter", bundle: b, version: 1}
	begin := time.Now()
	st, err := s.EnsureStatus(context.Background(), d)
	t.Logf("starter build took %v", time.Since(begin))
	if err != nil || st.State != StateReady {
		t.Fatalf("starter did not build: %+v err=%v", st, err)
	}
	// Exercise the app: liveness, a static file and an authenticated JSON route
	// (401 without a session). SSR pages are not requested: the root module pins
	// an older oarkflow/template than the starter, whose SPL refuses the
	// login page's script tags; that is a module-version matter, not preview's.
	h := s.Handler()
	for _, p := range []string{"/preview/starter/health", "/preview/starter/static/js/sw-register.js", "/preview/starter/api/v1/users"} {
		begin := time.Now()
		r := get(t, h, "GET", p, "")
		body := readBody(t, r)
		t.Logf("%s -> %d in %v", p, r.StatusCode, time.Since(begin))
		if r.StatusCode >= 500 {
			t.Errorf("%s: %d %s", p, r.StatusCode, body)
		}
	}
	if n := tr.count(); n != 0 {
		t.Fatalf("%d connection(s) reached the real host", n)
	}
	for _, c := range st.Sandbox {
		if c.Block == "worker/welcome-delivery" && c.Action != ActionDisable {
			t.Errorf("worker not disabled: %+v", c)
		}
	}
	g := s.entry("starter", false).current()
	if !strings.HasPrefix(g.root, tmp) {
		t.Fatalf("root outside temp: %s", g.root)
	}
	if _, err := os.Stat(filepath.Join(g.root, "db", "database.db")); err != nil {
		t.Errorf("sandboxed database not in temp root: %v", err)
	}
	if ents, _ := os.ReadDir(home); len(ents) != 0 {
		t.Errorf("preview wrote into HOME (GeoIP cache?): %v", ents)
	}
	s.Stop("starter")
	after := snapshot(t, starterDir)
	for p, v := range after {
		if before[p] != v {
			t.Errorf("starter tree changed: %s", p)
		}
	}
	for p := range before {
		if _, ok := after[p]; !ok {
			t.Errorf("starter file vanished: %s", p)
		}
	}
	if _, err := os.Stat(filepath.Join(starterDir, ".data")); err == nil {
		t.Error(".data was created in the starter directory")
	}
}

func TestPrefixRewriting(t *testing.T) {
	tr := newTrap(t)
	s := newService(t, Options{BaseDir: t.TempDir()})
	dir := site(t)
	d := newDraft(t, "px", appBundle(tr.addr(), dir)...)
	if st, _ := s.EnsureStatus(context.Background(), d); st.State != StateReady {
		t.Fatalf("%+v", st)
	}
	h := s.Handler()

	r := get(t, h, "GET", "/preview/px/go", "")
	if r.StatusCode != 302 || r.Header.Get("Location") != "/preview/px/hello" {
		t.Errorf("redirect: %d Location=%q", r.StatusCode, r.Header.Get("Location"))
	}
	if c := r.Header.Get("Set-Cookie"); !strings.Contains(c, "Path=/preview/px/") || strings.Contains(c, "Path=/;") {
		t.Errorf("cookie path: %q", c)
	}

	r = get(t, h, "GET", "/preview/px/site/index.html", "")
	body := readBody(t, r)
	for _, want := range []string{`href="/preview/px/hello"`, `src='/preview/px/site/logo.png'`, `action="/preview/px/notify"`, `href="//cdn.example/x"`, `href="rel.html"`} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %s in %s", want, body)
		}
	}
	if r.Header.Get("Content-Length") == "" {
		t.Error("content-length not updated")
	}

	if r = get(t, h, "GET", "/preview/px", ""); r.StatusCode != http.StatusTemporaryRedirect || r.Header.Get("Location") != "/preview/px/" {
		t.Errorf("bare id: %d %q", r.StatusCode, r.Header.Get("Location"))
	}
}

func TestRewriteUnits(t *testing.T) {
	p := "/preview/a"
	cases := map[string]string{"/x": "/preview/a/x", "//h/x": "//h/x", "https://h/x": "https://h/x", "rel": "rel", "/preview/a/x": "/preview/a/x", "/preview/a": "/preview/a"}
	for in, want := range cases {
		if got := rewriteLocation(in, p); got != want {
			t.Errorf("location %q -> %q, want %q", in, got, want)
		}
	}
	if got := rewriteCookie("a=b; Path=/x; Secure", p); got != "a=b; Path=/preview/a/x; Secure" {
		t.Errorf("cookie: %q", got)
	}
	if got := rewriteCookie("a=b", p); got != "a=b; Path=/preview/a/" {
		t.Errorf("cookie without path: %q", got)
	}
	if got := rewriteCookie("a=b; Path=/preview/a/z", p); got != "a=b; Path=/preview/a/z" {
		t.Errorf("already scoped: %q", got)
	}
}

func TestIdleExpiry(t *testing.T) {
	tr := newTrap(t)
	tmp := t.TempDir()
	s := newService(t, Options{BaseDir: t.TempDir(), TempDir: tmp, Idle: 80 * time.Millisecond})
	d := newDraft(t, "idle", appBundle(tr.addr(), site(t))...)
	if st, _ := s.EnsureStatus(context.Background(), d); st.State != StateReady {
		t.Fatalf("%+v", st)
	}
	// Use keeps it alive past the idle time.
	for i := 0; i < 4; i++ {
		time.Sleep(40 * time.Millisecond)
		if r := get(t, s.Handler(), "GET", "/preview/idle/hello", ""); r.StatusCode != 200 {
			t.Fatalf("expired while in use: %d", r.StatusCode)
		}
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		if r := get(t, s.Handler(), "GET", "/preview/idle/hello", ""); r.StatusCode == 404 {
			// The request itself must not count as use once gone.
			break
		} else {
			// Each request above refreshes lastUse; stop sending until expiry.
			_ = r
		}
		if time.Now().After(deadline) {
			t.Fatal("never expired")
		}
		time.Sleep(200 * time.Millisecond)
	}
	if st, ok := s.Status("idle"); ok && st.State != StateStopped {
		t.Errorf("status after expiry: %+v", st)
	}
}

func TestConcurrentEnsureBuildsOnce(t *testing.T) {
	var builds atomic.Int32
	s := newService(t, Options{BaseDir: t.TempDir(), AfterBuild: func(context.Context, string, *platform.Platform) error {
		builds.Add(1)
		time.Sleep(50 * time.Millisecond) // widen the race window
		return nil
	}})
	tr := newTrap(t)
	d := newDraft(t, "conc", appBundle(tr.addr(), site(t))...)
	var wg sync.WaitGroup
	results := make([]Status, 12)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			st, err := s.EnsureStatus(context.Background(), d)
			if err != nil {
				t.Error(err)
			}
			results[i] = st
			if i%3 == 0 {
				_ = get(t, s.Handler(), "GET", "/preview/conc/hello", "")
			}
		}()
	}
	wg.Wait()
	if builds.Load() != 1 {
		t.Fatalf("built %d times", builds.Load())
	}
	for i, st := range results {
		if st.State != StateReady || st.Serving != d.Version() {
			t.Errorf("result %d: %+v", i, st)
		}
	}
}

func TestEnsureHonoursContext(t *testing.T) {
	block := make(chan struct{})
	s := newService(t, Options{BaseDir: t.TempDir(), AfterBuild: func(context.Context, string, *platform.Platform) error {
		<-block
		return nil
	}})
	tr := newTrap(t)
	d := newDraft(t, "ctx", appBundle(tr.addr(), site(t))...)
	go func() { _, _ = s.EnsureStatus(context.Background(), d) }()
	time.Sleep(50 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := s.EnsureStatus(ctx, d); err == nil {
		t.Fatal("waiting Ensure ignored its context")
	}
	close(block)
}

func TestEventsAndStop(t *testing.T) {
	s := newService(t, Options{BaseDir: t.TempDir()})
	events, cancel := s.Events()
	defer cancel()
	tr := newTrap(t)
	d := newDraft(t, "ev", appBundle(tr.addr(), site(t))...)
	if _, err := s.EnsureStatus(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	s.Stop("ev")
	var got []State
	timeout := time.After(3 * time.Second)
	for len(got) < 3 {
		select {
		case st := <-events:
			got = append(got, st.State)
		case <-timeout:
			t.Fatalf("events so far: %v", got)
		}
	}
	if got[0] != StateStarting || got[1] != StateReady || got[2] != StateStopped {
		t.Fatalf("events: %v", got)
	}
	if r := get(t, s.Handler(), "GET", "/preview/ev/hello", ""); r.StatusCode != 404 {
		t.Errorf("stopped preview served: %d", r.StatusCode)
	}
}

func TestAfterBuildFailureFailsBuild(t *testing.T) {
	s := newService(t, Options{BaseDir: t.TempDir(), AfterBuild: func(context.Context, string, *platform.Platform) error {
		return os.ErrPermission
	}})
	tr := newTrap(t)
	st, _ := s.EnsureStatus(context.Background(), newDraft(t, "ab", appBundle(tr.addr(), site(t))...))
	if st.State != StateFailed || !strings.Contains(st.Error, "after-build") {
		t.Fatalf("%+v", st)
	}
	if r := get(t, s.Handler(), "GET", "/preview/ab/hello", ""); r.StatusCode != 404 {
		t.Errorf("failed first build is being served: %d", r.StatusCode)
	}
}

func TestRingKeepsLast(t *testing.T) {
	r := newRing[int](3)
	for i := 1; i <= 5; i++ {
		r.add(i)
	}
	if got := r.snapshot(); len(got) != 3 || got[0] != 3 || got[2] != 5 {
		t.Fatalf("%v", got)
	}
}

func TestRouteMatching(t *testing.T) {
	ms := buildMatchers(platform.Document{Routes: []platform.RouteSpec{
		{Name: "list", Method: "GET", Path: "/todos", Intent: "todo.list"},
		{Name: "show", Method: "GET", Path: "/todos/:id", Intent: "todo.show"},
		{Name: "new", Method: "GET", Path: "/todos/new", Intent: "todo.new"},
		{Name: "make", Method: "POST", Path: "/todos", Process: "todo.flow"},
		{Name: "files", Method: "GET", Path: "/files/*path", Intent: "f"},
	}})
	for _, c := range []struct{ m, p, name, intent string }{
		{"GET", "/todos", "list", "todo.list"},
		{"GET", "/todos/7", "show", "todo.show"},
		{"GET", "/todos/new", "new", "todo.new"}, // static beats :id
		{"POST", "/todos", "make", "todo.flow"},
		{"GET", "/files/a/b/c", "files", "f"},
		{"GET", "/nope", "", ""},
	} {
		if n, i := matchRoute(ms, c.m, c.p); n != c.name || i != c.intent {
			t.Errorf("%s %s -> %q/%q, want %q/%q", c.m, c.p, n, i, c.name, c.intent)
		}
	}
}

func TestImplementsStudioContract(t *testing.T) {
	tr := newTrap(t)
	s := newService(t, Options{BaseDir: t.TempDir()})
	var pm studio.PreviewManager = s
	d := newDraft(t, "ct", appBundle(tr.addr(), site(t))...)
	st, err := pm.Ensure(context.Background(), d)
	if err != nil || st.Status != "ready" || st.URL != "/preview/ct/" || st.Version != d.Version() || st.Message == "" {
		t.Fatalf("%+v %v", st, err)
	}
	_ = get(t, pm.Handler(), "POST", "/preview/ct/notify", `{"x":1}`)
	_ = get(t, pm.Handler(), "GET", "/preview/ct/hello?a=1", "")
	reqs := pm.Requests("ct")
	var kinds []string
	for i, r := range reqs {
		kinds = append(kinds, r.Kind)
		if i > 0 && r.At.Before(reqs[i-1].At) {
			t.Errorf("not sorted oldest first")
		}
	}
	joined := strings.Join(kinds, ",")
	if !strings.Contains(joined, "outbound") || strings.Count(joined, "request") != 2 {
		t.Fatalf("kinds %v", kinds)
	}
	last := reqs[len(reqs)-1]
	if last.URL != "/hello?a=1" || last.Detail["route"] != "hello" || last.Status != 200 {
		t.Errorf("%+v", last)
	}

	// A failed rebuild keeps serving and says so in the shared status.
	files := appBundle(tr.addr(), site(t))
	files[1].Content = "\n# c\nintent \"hello\" {\n  response = = \"pong\"\n}\n"
	d.set(t, files...)
	bad, _ := pm.Ensure(context.Background(), d)
	if len(bad.Error) != 1 {
		t.Errorf("duplicate diagnostics: %+v", bad.Error)
	}
	if bad.Status != "failed" || len(bad.Error) == 0 || bad.Error[0].File != "01_logic.bcl" || !strings.Contains(bad.Message, "still being served") {
		t.Fatalf("%+v", bad)
	}
	pm.Stop("ct")
	if got := pm.Requests("ct"); len(got) != 0 {
		t.Errorf("requests after stop: %d", len(got))
	}
}
