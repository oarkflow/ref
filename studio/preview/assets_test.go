package preview

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/oarkflow/fh"

	"github.com/oarkflow/ref/platform"
	"github.com/oarkflow/ref/studio"
)

// assetDraft is a testDraft that also carries assets (studio.AssetDraft).
type assetDraft struct {
	*testDraft
	amu    sync.Mutex
	assets platform.Assets
}

func (d *assetDraft) Assets() platform.Assets {
	d.amu.Lock()
	defer d.amu.Unlock()
	return append(platform.Assets(nil), d.assets...)
}

// setAssets replaces the assets and bumps the version, as the server does.
func (d *assetDraft) setAssets(t *testing.T, files ...platform.BundleFile) {
	t.Helper()
	a, err := platform.NewAssets(files)
	if err != nil {
		t.Fatal(err)
	}
	d.amu.Lock()
	d.assets = a
	d.amu.Unlock()
	d.testDraft.mu.Lock()
	d.testDraft.version++
	d.testDraft.mu.Unlock()
}

var _ studio.AssetDraft = (*assetDraft)(nil)

const pageBundleHost = "127.0.0.1:1"

func TestDraftAssetsAreOptional(t *testing.T) {
	plain := newDraft(t, "p", appBundle(pageBundleHost, site(t))...)
	if got := studio.DraftAssets(plain); got != nil {
		t.Errorf("a draft without assets: %v", got)
	}
	d := &assetDraft{testDraft: plain}
	d.setAssets(t, file("templates/pages/a.html", "A"))
	if got := studio.DraftAssets(d); len(got) != 1 {
		t.Errorf("assets: %v", got)
	}
}

// TestPreviewRendersDraftAssets: NewAppFor and MountFor see the draft's
// assets, a scratch directory that exists while the generation lives and is
// gone after it stops, and a new generation is built when an asset changes.
func TestPreviewRendersDraftAssets(t *testing.T) {
	var mu sync.Mutex
	var dirs []string
	var mountedVersions []int64
	opts := Options{
		BaseDir: t.TempDir(),
		NewAppFor: func(env BuildEnv) *fh.App {
			if env.Dir == "" || env.ID != "pages" {
				t.Errorf("env: %+v", env)
			}
			return fh.New(fh.WithStartupBannerDisabled(true))
		},
		MountFor: func(app *fh.App, p *platform.Platform, env BuildEnv) error {
			mu.Lock()
			dirs = append(dirs, env.Dir)
			mountedVersions = append(mountedVersions, env.Version)
			mu.Unlock()
			// A host with a disk-reading engine materialises the overlay
			// into the scratch directory.
			overlay := env.Assets.Overlay(nil)
			err := fs.WalkDir(overlay, ".", func(name string, d fs.DirEntry, err error) error {
				if err != nil || d.IsDir() {
					return err
				}
				b, err := fs.ReadFile(overlay, name)
				if err != nil {
					return err
				}
				dst := filepath.Join(env.Dir, filepath.FromSlash(name))
				if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
					return err
				}
				return os.WriteFile(dst, b, 0o644)
			})
			if err != nil {
				return err
			}
			app.Get("/page", func(c fh.Ctx) error {
				b, err := os.ReadFile(filepath.Join(env.Dir, "templates", "pages", "hello.html"))
				if err != nil {
					return c.SendString("no template: " + err.Error())
				}
				c.Set("Content-Type", "text/html")
				return c.SendString(string(b))
			})
			return p.Mount(app)
		},
	}
	s := newService(t, opts)
	d := &assetDraft{testDraft: newDraft(t, "pages", appBundle(pageBundleHost, site(t))...)}
	d.setAssets(t, file("templates/pages/hello.html", "<h1>first</h1>"))

	ctx := context.Background()
	if st, err := s.EnsureStatus(ctx, d); err != nil || st.State != StateReady {
		t.Fatalf("ensure: %+v %v", st, err)
	}
	resp := get(t, s.Handler(), "GET", "/preview/pages/page", "")
	if body := readBody(t, resp); resp.StatusCode != 200 || !strings.Contains(body, "<h1>first</h1>") {
		t.Fatalf("first: %d %q", resp.StatusCode, body)
	}
	mu.Lock()
	first := dirs[0]
	mu.Unlock()
	if _, err := os.Stat(filepath.Join(first, "templates", "pages", "hello.html")); err != nil {
		t.Fatalf("scratch dir was not populated: %v", err)
	}

	// Changing an asset (and bumping the version) rebuilds with the new one.
	d.setAssets(t, file("templates/pages/hello.html", "<h1>second</h1>"))
	if st, err := s.EnsureStatus(ctx, d); err != nil || st.State != StateReady || st.Serving != d.Version() {
		t.Fatalf("re-ensure: %+v %v", st, err)
	}
	resp = get(t, s.Handler(), "GET", "/preview/pages/page", "")
	if body := readBody(t, resp); !strings.Contains(body, "<h1>second</h1>") {
		t.Fatalf("second: %q", body)
	}
	mu.Lock()
	if len(dirs) != 2 || dirs[0] == dirs[1] || len(mountedVersions) != 2 || mountedVersions[0] == mountedVersions[1] {
		t.Errorf("dirs %v versions %v", dirs, mountedVersions)
	}
	mu.Unlock()

	// The retired generation's scratch directory goes away, and so does the
	// live one's when the preview stops.
	waitGone(t, first)
	mu.Lock()
	second := dirs[1]
	mu.Unlock()
	s.Stop("pages")
	waitGone(t, second)
}

// TestPreviewWithoutHooksIgnoresAssets: a host that sets neither hook keeps the
// old behaviour, and a draft with no assets still builds through MountFor.
func TestPreviewWithoutAssetsStillBuilds(t *testing.T) {
	var saw platform.Assets = platform.Assets{{Path: "x"}}
	s := newService(t, Options{
		BaseDir: t.TempDir(),
		MountFor: func(app *fh.App, p *platform.Platform, env BuildEnv) error {
			saw = env.Assets
			return p.Mount(app)
		},
	})
	d := newDraft(t, "plain", appBundle(pageBundleHost, site(t))...)
	if st, err := s.EnsureStatus(context.Background(), d); err != nil || st.State != StateReady {
		t.Fatalf("ensure: %+v %v", st, err)
	}
	if saw != nil {
		t.Errorf("assets for a draft without any: %v", saw)
	}
}

func waitGone(t *testing.T, dir string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("%s still exists", dir)
}
