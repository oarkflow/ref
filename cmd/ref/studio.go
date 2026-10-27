package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/oarkflow/ref/config"
	"github.com/oarkflow/ref/deploy"
	"github.com/oarkflow/ref/platform"
	"github.com/oarkflow/ref/studio"
	studioserver "github.com/oarkflow/ref/studio/server"
)

// studioHost runs REF Studio beside the application. Studio edits a working
// copy of the app's BCL files; activating a version writes it back to the
// application directory and restarts the process, so the running app and its
// files never drift apart. There is no review gate (Approvals 0): this is the
// local authoring tool, and its token is the only credential.
type studioHost struct {
	srv   *studioserver.Server
	http  *http.Server
	token string
	// plat is the running application's platform, set once it has loaded.
	plat atomic.Pointer[platform.Platform]
}

// studioDirs finds the BCL directory and the directory that holds templates/
// and static/, for the layouts serve accepts (config/, bcl/, resources/config).
func studioDirs(appDir string) (cfg, res string, err error) {
	for _, c := range []struct{ cfg, res string }{
		{"config", "."}, {"bcl", "."}, {"resources/config", "resources"},
	} {
		if info, e := os.Stat(filepath.Join(appDir, c.cfg)); e == nil && info.IsDir() {
			return filepath.Join(appDir, c.cfg), filepath.Join(appDir, c.res), nil
		}
	}
	return "", "", fmt.Errorf("studio needs a config/, bcl/ or resources/config directory in %q (a single app.bcl is not supported)", appDir)
}

// studioToken returns $STUDIO_TOKEN, or a token kept in <app>/.data/studio/token
// (created on first use, mode 0600) so a restart does not sign you out.
func studioToken(appDir string) (string, error) {
	if t := os.Getenv("STUDIO_TOKEN"); t != "" {
		if len(t) < 16 {
			return "", errors.New("STUDIO_TOKEN must be at least 16 characters")
		}
		return t, nil
	}
	path := filepath.Join(appDir, ".data", "studio", "token")
	if data, err := os.ReadFile(path); err == nil && len(strings.TrimSpace(string(data))) >= 16 {
		return strings.TrimSpace(string(data)), nil
	}
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	token := hex.EncodeToString(buf)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	return token, os.WriteFile(path, []byte(token+"\n"), 0o600)
}

func startStudio(ctx context.Context, appDir, addr string) (*studioHost, error) {
	appDir, err := filepath.Abs(appDir)
	if err != nil {
		return nil, err
	}
	cfgDir, resDir, err := studioDirs(appDir)
	if err != nil {
		return nil, err
	}
	for _, name := range []string{".env", ".env.example"} {
		if _, err := os.Stat(filepath.Join(appDir, name)); err == nil {
			if err := config.LoadDotenv(filepath.Join(appDir, name)); err != nil {
				return nil, err
			}
			break
		}
	}
	token, err := studioToken(appDir)
	if err != nil {
		return nil, err
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, err
	}
	opts := platform.LoadOptions{AllowEnv: true, Profile: config.Env("APP_ENV", "development")}
	mgr := &deploy.Manager{
		Store: deploy.NewMemoryStore(), App: filepath.Base(appDir), Secret: secret, Approvals: 0,
		Validate: func(ctx context.Context, src []byte) platform.ValidationReport {
			return platform.Validate(ctx, src, cfgDir, opts)
		},
		ValidateBundle: func(ctx context.Context, b platform.Bundle) platform.ValidationReport {
			return platform.ValidateBundle(ctx, b, cfgDir, opts)
		},
	}
	bundle, err := readConfigTree(cfgDir)
	if err != nil {
		return nil, err
	}
	rev, err := mgr.ProposeBundle(ctx, bundle, "ref", "files on disk")
	if err != nil {
		return nil, fmt.Errorf("the application's configuration does not load: %w", err)
	}
	if rev.Status != deploy.StatusApproved {
		if rev, err = mgr.Approve(ctx, rev.ID, "ref", "files on disk"); err != nil {
			return nil, err
		}
	}
	if _, err = mgr.Activate(ctx, rev.ID, "ref"); err != nil {
		return nil, err
	}
	srv, err := studioserver.New(studioserver.Config{
		App: filepath.Base(appDir), Manager: mgr, ConfigDir: cfgDir, ResourcesDir: resDir, LoadOptions: opts,
		Tokens: map[string]studio.Identity{token: {Name: "admin", Roles: []string{"admin"}}},
		OnActivate: func(r *deploy.Revision) {
			if err := writeRevision(cfgDir, resDir, r); err != nil {
				fmt.Fprintln(os.Stderr, "ref: studio: could not write the activated version:", err)
				return
			}
			fmt.Fprintln(os.Stderr, "ref: studio: version", r.Seq, "written to", appDir)
			restartSelf()
		},
	})
	if err != nil {
		return nil, err
	}
	h := &studioHost{srv: srv, token: token}
	h.http = &http.Server{Addr: addr, Handler: h.withLookupTest(srv.Handler()), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := h.http.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Fprintln(os.Stderr, "ref: studio:", err)
		}
	}()
	fmt.Fprintf(os.Stderr, "ref: Studio on http://%s/  (access token in %s)\n", displayAddr(addr), filepath.Join(appDir, ".data", "studio", "token"))
	return h, nil
}

func (h *studioHost) Close() {
	h.srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = h.http.Shutdown(ctx)
}

func displayAddr(addr string) string {
	if strings.HasPrefix(addr, ":") {
		return "127.0.0.1" + addr
	}
	return addr
}

// Studio bundles are flat, but applications keep BCL in folders
// (config/00_core/01_app.bcl). A nested path is carried through the bundle as
// one file name with "~~" standing for each "/".
const nestSep = "~~"

func encodeBundlePath(rel string) string {
	return strings.ReplaceAll(filepath.ToSlash(rel), "/", nestSep)
}
func decodeBundlePath(name string) string {
	return filepath.FromSlash(strings.ReplaceAll(name, nestSep, "/"))
}

// readConfigTree reads every .bcl file below dir into a flat bundle.
func readConfigTree(dir string) (platform.Bundle, error) {
	var files []platform.BundleFile
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(d.Name(), ".bcl") {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		if strings.Contains(filepath.Base(rel), nestSep) {
			return fmt.Errorf("file name %q contains %q, which Studio uses for folders", rel, nestSep)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files = append(files, platform.BundleFile{Path: encodeBundlePath(rel), Content: string(data)})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return platform.NewBundle(files)
}

// writeRevision makes the application directory match a revision: BCL files in
// the config directory tree (files the revision no longer has are removed) and
// assets under the resources directory. Each file is replaced atomically.
func writeRevision(cfgDir, resDir string, r *deploy.Revision) error {
	files := r.Files
	if len(files) == 0 {
		files = []platform.BundleFile{{Path: "main.bcl", Content: r.Source}}
	}
	keep := map[string]bool{}
	for _, f := range files {
		rel := decodeBundlePath(f.Path)
		target := filepath.Join(cfgDir, rel)
		if back, err := filepath.Rel(cfgDir, target); err != nil || strings.HasPrefix(back, "..") {
			return fmt.Errorf("file %q escapes the configuration directory", f.Path)
		}
		keep[filepath.ToSlash(rel)] = true
		if err := writeAtomic(target, f.Content); err != nil {
			return err
		}
	}
	err := filepath.WalkDir(cfgDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(d.Name(), ".bcl") {
			return err
		}
		rel, err := filepath.Rel(cfgDir, path)
		if err != nil {
			return err
		}
		if !keep[filepath.ToSlash(rel)] {
			return os.Remove(path)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, a := range r.Assets {
		target := filepath.Join(resDir, filepath.FromSlash(a.Path))
		if rel, err := filepath.Rel(resDir, target); err != nil || strings.HasPrefix(rel, "..") {
			return fmt.Errorf("asset path %q escapes the application", a.Path)
		}
		if err := writeAtomic(target, a.Content); err != nil {
			return err
		}
	}
	return nil
}

func writeAtomic(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".studio-tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// withLookupTest adds POST /api/v1/lookup-test to Studio's API. It evaluates a
// lookup set from the working copy against the running application's own
// connections, so the author sees real choices (or the real error) before the
// change goes live. It needs the same admin token as the rest of Studio.
func (h *studioHost) withLookupTest(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/lookup-test" {
			next.ServeHTTP(w, r)
			return
		}
		reply := func(status int, v any) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(v)
		}
		fail := func(status int, msg string) { reply(status, map[string]any{"error": map[string]any{"message": msg}}) }
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if subtle.ConstantTimeCompare([]byte(got), []byte(h.token)) != 1 {
			fail(http.StatusUnauthorized, "a valid token is required")
			return
		}
		if r.Method != http.MethodPost {
			fail(http.StatusMethodNotAllowed, "use POST")
			return
		}
		plat := h.plat.Load()
		if plat == nil {
			fail(http.StatusServiceUnavailable, "the application is still starting")
			return
		}
		var req struct {
			Database string         `json:"database"`
			Service  string         `json:"service"`
			Set      map[string]any `json:"set"`
			Deps     map[string]any `json:"deps"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
			fail(http.StatusBadRequest, "the request is not valid JSON")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		res, err := plat.TestLookup(ctx, platform.LookupTestRequest{Database: req.Database, Service: req.Service, Set: req.Set, Deps: req.Deps})
		if err != nil {
			fail(http.StatusUnprocessableEntity, err.Error())
			return
		}
		reply(http.StatusOK, res)
	})
}
