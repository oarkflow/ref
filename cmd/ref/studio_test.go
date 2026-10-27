package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/oarkflow/ref/deploy"
	"github.com/oarkflow/ref/platform"
)

func TestWriteRevisionMirrorsBundle(t *testing.T) {
	root := t.TempDir()
	cfg, res := filepath.Join(root, "config"), root
	if err := os.MkdirAll(cfg, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"keep.bcl": "old", "gone.bcl": "x", "notes.txt": "leave me"} {
		if err := os.WriteFile(filepath.Join(cfg, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	rev := &deploy.Revision{
		Files:  []platform.BundleFile{{Path: "keep.bcl", Content: "new"}, {Path: "added.bcl", Content: "a"}},
		Assets: []platform.BundleFile{{Path: "templates/p.html", Content: "<p>"}},
	}
	if err := writeRevision(cfg, res, rev); err != nil {
		t.Fatal(err)
	}
	read := func(p string) string { b, _ := os.ReadFile(filepath.Join(root, p)); return string(b) }
	if read("config/keep.bcl") != "new" || read("config/added.bcl") != "a" || read("templates/p.html") != "<p>" {
		t.Fatal("files not written")
	}
	if _, err := os.Stat(filepath.Join(cfg, "gone.bcl")); err == nil {
		t.Fatal("a file the revision dropped was kept")
	}
	if read("config/notes.txt") != "leave me" {
		t.Fatal("a non-BCL file was touched")
	}
	nested := &deploy.Revision{Files: []platform.BundleFile{{Path: "00_core~~app.bcl", Content: "n"}}}
	if err := writeRevision(cfg, res, nested); err != nil {
		t.Fatal(err)
	}
	if read("config/00_core/app.bcl") != "n" {
		t.Fatal("nested path not written into its folder")
	}
	if _, err := os.Stat(filepath.Join(cfg, "keep.bcl")); err == nil {
		t.Fatal("top-level file the revision dropped was kept")
	}
	bundle, err := readConfigTree(cfg)
	if err != nil || len(bundle) != 1 || bundle[0].Path != "00_core~~app.bcl" {
		t.Fatalf("readConfigTree: %v %v", bundle, err)
	}
	rev.Assets = []platform.BundleFile{{Path: "templates/../../evil.html", Content: "x"}}
	if err := writeRevision(cfg, res, rev); err == nil {
		t.Fatal("asset escaping the application was written")
	}
}

func TestStudioDirsAndToken(t *testing.T) {
	root := t.TempDir()
	if _, _, err := studioDirs(root); err == nil {
		t.Fatal("no config dir accepted")
	}
	if err := os.MkdirAll(filepath.Join(root, "resources", "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg, res, err := studioDirs(root)
	if err != nil || cfg != filepath.Join(root, "resources", "config") || res != filepath.Join(root, "resources") {
		t.Fatalf("dirs: %s %s %v", cfg, res, err)
	}
	t.Setenv("STUDIO_TOKEN", "")
	a, err := studioToken(root)
	if err != nil || len(a) < 16 {
		t.Fatalf("token: %q %v", a, err)
	}
	if b, _ := studioToken(root); a != b {
		t.Fatal("token not stable across restarts")
	}
	t.Setenv("STUDIO_TOKEN", "short")
	if _, err := studioToken(root); err == nil {
		t.Fatal("short token accepted")
	}
}

func TestLookupTestEndpointGuards(t *testing.T) {
	h := &studioHost{token: "0123456789abcdef-token"}
	reached := false
	handler := h.withLookupTest(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached = true }))
	call := func(method, path, auth, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	if call("GET", "/api/v1/drafts", "", "").Code != 200 || !reached {
		t.Fatal("other paths must pass through to Studio")
	}
	if c := call("POST", "/api/v1/lookup-test", "", "{}").Code; c != 401 {
		t.Fatalf("no token: %d", c)
	}
	if c := call("POST", "/api/v1/lookup-test", "Bearer wrong", "{}").Code; c != 401 {
		t.Fatalf("wrong token: %d", c)
	}
	if c := call("GET", "/api/v1/lookup-test", "Bearer "+h.token, "").Code; c != 405 {
		t.Fatalf("GET: %d", c)
	}
	if c := call("POST", "/api/v1/lookup-test", "Bearer "+h.token, "{}").Code; c != 503 {
		t.Fatalf("app not loaded yet: %d", c)
	}
}
