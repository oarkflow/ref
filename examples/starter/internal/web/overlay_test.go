package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/oarkflow/fh"

	"github.com/oarkflow/ref/platform"
)

func mustAssets(t *testing.T, files ...platform.BundleFile) platform.Assets {
	t.Helper()
	a, err := platform.NewAssets(files)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestMaterializeTemplatesOverlaysDisk(t *testing.T) {
	base := t.TempDir()
	for name, content := range map[string]string{
		"layouts/base.html":     "BASE",
		"pages/todos/list.html": "DISK LIST",
		"pages/todos/show.html": "DISK SHOW",
	} {
		p := filepath.Join(base, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	assets := mustAssets(t,
		platform.BundleFile{Path: "templates/pages/todos/list.html", Content: "OVERRIDE LIST"},
		platform.BundleFile{Path: "templates/pages/new/page.html", Content: "NEW PAGE"},
		platform.BundleFile{Path: "static/css/x.css", Content: "ignored here"},
	)
	if !HasTemplateAssets(assets) || !HasStaticAssets(assets) {
		t.Fatal("asset kinds not detected")
	}
	if HasTemplateAssets(mustAssets(t, platform.BundleFile{Path: "static/a.css", Content: "x"})) {
		t.Fatal("static-only assets have no templates")
	}
	dst := t.TempDir()
	if err := MaterializeTemplates(base, assets, dst); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{
		"layouts/base.html":     "BASE",
		"pages/todos/list.html": "OVERRIDE LIST",
		"pages/todos/show.html": "DISK SHOW",
		"pages/new/page.html":   "NEW PAGE",
	} {
		b, err := os.ReadFile(filepath.Join(dst, filepath.FromSlash(name)))
		if err != nil || string(b) != want {
			t.Errorf("%s = %q, %v; want %q", name, b, err, want)
		}
	}
	if _, err := os.Stat(filepath.Join(dst, "css", "x.css")); err == nil {
		t.Error("a static asset was written into the templates directory")
	}
	// The disk originals are untouched.
	if b, _ := os.ReadFile(filepath.Join(base, "pages/todos/list.html")); string(b) != "DISK LIST" {
		t.Errorf("the base directory was modified: %q", b)
	}
	if err := MaterializeTemplates(filepath.Join(base, "missing"), nil, t.TempDir()); err == nil {
		t.Error("a missing base directory should fail")
	}
	// A path that got past validation is still refused.
	bad := platform.Assets{{Path: "templates/../evil.html", Content: "x"}}
	if err := MaterializeTemplates(base, bad, t.TempDir()); err == nil {
		t.Error("an escaping asset was written")
	}
}

func TestStaticOverlay(t *testing.T) {
	assets := mustAssets(t,
		platform.BundleFile{Path: "static/css/app.css", Content: "body{color:red}"},
		platform.BundleFile{Path: "static/js/x.js", Content: "console.log(1)"},
		platform.BundleFile{Path: "static/data.json", Content: `{"a":1}`},
		platform.BundleFile{Path: "templates/pages/a.html", Content: "not static"},
	)
	// fh's in-memory test client serves one request per App, so each request
	// gets its own.
	newApp := func() *fh.App {
		app := fh.New(fh.WithStartupBannerDisabled(true))
		app.Use(StaticOverlay("/static", assets))
		app.Get("/static/css/disk.css", func(c fh.Ctx) error { return c.SendString("from disk") })
		app.Get("/static/css/app.css", func(c fh.Ctx) error { return c.SendString("DISK app.css") })
		app.Get("/other", func(c fh.Ctx) error { return c.SendString("other") })
		app.Post("/static/css/app.css", func(c fh.Ctx) error { return c.SendString("posted") })
		return app
	}

	do := func(method, target string) (int, string, http.Header) {
		t.Helper()
		resp, err := newApp().Test(httptest.NewRequest(method, target, nil), 3000)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b), resp.Header
	}
	code, body, h := do("GET", "/static/css/app.css")
	if code != 200 || body != "body{color:red}" || !strings.HasPrefix(h.Get("Content-Type"), "text/css") || h.Get("Cache-Control") != "no-cache" {
		t.Errorf("override: %d %q %v", code, body, h)
	}
	if code, body, h = do("GET", "/static/js/x.js"); code != 200 || body != "console.log(1)" || !strings.Contains(h.Get("Content-Type"), "javascript") {
		t.Errorf("js: %d %q %v", code, body, h)
	}
	if code, body, h = do("GET", "/static/data.json"); code != 200 || body != `{"a":1}` || !strings.Contains(h.Get("Content-Type"), "json") {
		t.Errorf("json: %d %q %v", code, body, h)
	}
	if code, body, _ = do("HEAD", "/static/css/app.css"); code != 200 || body != "" {
		t.Errorf("head: %d %q", code, body)
	}
	// Everything the assets do not define falls through to the app.
	if code, body, _ = do("GET", "/static/css/disk.css"); code != 200 || body != "from disk" {
		t.Errorf("fallthrough: %d %q", code, body)
	}
	if code, body, _ = do("GET", "/other"); code != 200 || body != "other" {
		t.Errorf("other: %d %q", code, body)
	}
	if code, body, _ = do("POST", "/static/css/app.css"); body != "posted" {
		t.Errorf("only GET and HEAD are overridden: %d %q", code, body)
	}
	// A template asset is never served as a static file.
	if code, _, _ = do("GET", "/static/pages/a.html"); code != 404 {
		t.Errorf("template served as static: %d", code)
	}
	if code, _, _ = do("GET", "/static/"); code != 404 {
		t.Errorf("directory: %d", code)
	}
	// Traversal cannot reach a template asset.
	if code, _, _ = do("GET", "/static/../templates/pages/a.html"); code == 200 {
		t.Errorf("traversal served a template")
	}
}
