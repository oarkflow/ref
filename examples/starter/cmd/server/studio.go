package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/oarkflow/fh"
	"github.com/oarkflow/zlog"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/oarkflow/ref/deploy"
	"github.com/oarkflow/ref/examples/starter/internal/ops"
	"github.com/oarkflow/ref/examples/starter/internal/web"
	"github.com/oarkflow/ref/health"
	"github.com/oarkflow/ref/platform"
	"github.com/oarkflow/ref/studio"
	"github.com/oarkflow/ref/studio/preview"
	studioserver "github.com/oarkflow/ref/studio/server"
)

// studioParams is what the supervised boot path hands newStudio.
type studioParams struct {
	Manager    *deploy.Manager
	Supervisor *deploy.Supervisor
	Deps       starterDeps
	AdminToken string
	AdminAddr  string
	BCLDir     string
	Opts       platform.LoadOptions
}

// studioHandle is a built Studio: its handler (to mount under /studio) and
// what to release at shutdown.
type studioHandle struct {
	Handler http.Handler
	closers []func()
}

// Close stops the preview generations and closes the draft database.
func (h *studioHandle) Close() {
	for i := len(h.closers) - 1; i >= 0; i-- {
		h.closers[i]()
	}
}

// studioTokens turns the environment into Studio identities. Its names match
// the admin API's ("admin", "reviewer"), so the "author cannot approve" rule
// holds across both. A token that is set but shorter than 16 characters, or
// that reuses another identity's token, is an error rather than being
// dropped: a silently missing reviewer looks like a permissions bug later.
func studioTokens(adminToken string) (map[string]studio.Identity, error) {
	if len(adminToken) < 16 {
		return nil, errors.New("STARTER_STUDIO=1 needs STARTER_ADMIN_TOKEN, at least 16 characters (it is Studio's admin login)")
	}
	tokens := map[string]studio.Identity{adminToken: {Name: "admin", Roles: []string{"admin"}}}
	add := func(envVar, name, role string) error {
		t := os.Getenv(envVar)
		if t == "" {
			return nil
		}
		if len(t) < 16 {
			return fmt.Errorf("%s must be at least 16 characters (it is Studio's %s login)", envVar, name)
		}
		if _, taken := tokens[t]; taken {
			return fmt.Errorf("%s must differ from the other Studio tokens", envVar)
		}
		tokens[t] = studio.Identity{Name: name, Roles: []string{role}}
		return nil
	}
	if err := add("STARTER_REVIEWER_TOKEN", "reviewer", "reviewer"); err != nil {
		return nil, err
	}
	if err := add("STARTER_EDITOR_TOKEN", "editor", "editor"); err != nil {
		return nil, err
	}
	return tokens, nil
}

// newStudio builds the Studio API and web app for the supervised admin
// listener: page tools over the starter's own templates, live preview through
// the starter's own renderer and mount code, and (STARTER_STUDIO_PERSIST=1)
// drafts and the audit log in the starter's database.
func newStudio(ctx context.Context, sp studioParams) (*studioHandle, error) {
	tokens, err := studioTokens(sp.AdminToken)
	if err != nil {
		return nil, err
	}
	d := sp.Deps
	h := &studioHandle{}

	// The resources directory is the parent of templates/ and static/: that is
	// the root the page tools read (templates/pages/…, static/css/…) and the
	// root a draft's assets override.
	resourcesDir := filepath.Dir(d.templatesDir)
	if filepath.Dir(d.staticDir) != resourcesDir {
		d.logger.Warn("templates and static live in different resource roots; Studio's page tools use the templates one",
			zlog.String("templates", d.templatesDir), zlog.String("static", d.staticDir))
	}

	host := newPreviewHost(d, sp.BCLDir)
	pv := host.options()
	cfg := studioserver.Config{
		App:             "starter",
		Manager:         sp.Manager,
		Tokens:          tokens,
		ConfigDir:       sp.BCLDir,
		LoadOptions:     sp.Opts,
		ResourcesDir:    resourcesDir,
		TemplateGlobals: web.GlobalNames(),
		PreviewOptions:  &pv,
		BasePath:        "/studio",
		OnActivate:      func(*deploy.Revision) { sp.Supervisor.Notify() },
		Logf: func(format string, args ...any) {
			d.logger.Info(fmt.Sprintf("studio: "+format, args...))
		},
	}
	if src := os.Getenv("STARTER_STUDIO_SRC"); src != "" {
		cfg.DocSourceDirs = []string{filepath.Join(src, "platform"), filepath.Join(src, "pipeline")}
	}

	persist := os.Getenv("STARTER_STUDIO_PERSIST") == "1"
	if persist {
		db, dialect, err := openStudioDB()
		if err != nil {
			return nil, err
		}
		store, sink, err := studioserver.OpenSQL(ctx, db, dialect, "studio_")
		if err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("studio: opening the draft store: %w", err)
		}
		cfg.Store, cfg.AuditStore, cfg.Comments = store, sink, sink
		h.closers = append(h.closers, func() { _ = db.Close() })
	}

	srv, err := studioserver.New(cfg)
	if err != nil {
		h.Close()
		return nil, fmt.Errorf("studio: %w", err)
	}
	h.closers = append(h.closers, func() { _ = srv.Close() })
	h.Handler = srv.Handler()

	names := make([]string, 0, len(tokens))
	for _, id := range tokens {
		names = append(names, id.Name)
	}
	sort.Strings(names)
	storage := "in memory (drafts are lost on restart; STARTER_STUDIO_PERSIST=1 keeps them)"
	if persist {
		storage = "in the database (tables studio_*)"
	}
	d.logger.Info("studio available",
		zlog.String("url", "http://"+sp.AdminAddr+"/studio/"),
		zlog.String("logins", strings.Join(names, ",")),
		zlog.String("drafts", storage),
		zlog.String("preview", "sandboxed, at /studio/preview/{draft}/"))
	return h, nil
}

// openStudioDB opens the database the draft store lives in: the one the
// document's "database" resource names (DB_DRIVER, DB_DSN), the same defaults
// as resources/config/01_resources.bcl. ensureMigrated has run by now, so a
// SQLite file exists.
func openStudioDB() (*sql.DB, string, error) {
	driver := env("DB_DRIVER", "sqlite")
	dsn := env("DB_DSN", "file:.data/starter/app.db?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	db, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, "", fmt.Errorf("studio: opening %s database: %w (is the driver imported in main.go?)", driver, err)
	}
	dialect := driver
	if driver == "pgx" {
		dialect = "postgres"
	}
	return db, dialect, nil
}

// previewHost holds what Studio's preview builds share: the starter's process
// wiring with the pieces that must not reach the live app swapped for private
// ones, and where each draft's sandbox lives.
type previewHost struct {
	deps    starterDeps
	bclDir  string
	gate    *ops.MaintenanceGate
	mu      sync.Mutex
	sandbox map[string]string // draft id -> the preview generation's root directory
}

func newPreviewHost(d starterDeps, bclDir string) *previewHost {
	pd := d
	// Private instances: a preview must not flip the live maintenance gate,
	// replace the live readiness checks, or expose the live metrics.
	pd.maintenance = ops.NewMaintenanceGate(false, "")
	pd.health = health.NewRegistry()
	pd.promRegistry = prometheus.NewRegistry()
	pd.staticOverlay = nil
	return &previewHost{deps: pd, bclDir: bclDir, gate: pd.maintenance, sandbox: map[string]string{}}
}

// options builds studio/preview's Options from the starter's own renderer and
// mount code, so a draft's template override renders in preview exactly as it
// would live.
func (h *previewHost) options() preview.Options {
	d := h.deps
	return preview.Options{
		BaseDir: h.bclDir,
		Registry: func() *platform.Registry {
			r := platform.NewRegistry()
			// ops.maintenance_set is installed process-wide, bound to the live gate.
			if err := h.gate.ReplaceActionsOn(r); err != nil {
				d.logger.Warn("preview: maintenance actions not rebound", zlog.Err(err))
			}
			return r
		},
		NewAppFor: func(env preview.BuildEnv) *fh.App {
			dir := d.templatesDir
			if web.HasTemplateAssets(env.Assets) {
				dir = filepath.Join(env.Dir, "templates")
				_ = os.MkdirAll(dir, 0o755)
			}
			return newStarterApp(d, dir, "preview "+env.ID)
		},
		MountFor: func(app *fh.App, p *platform.Platform, env preview.BuildEnv) error {
			dd := d
			if web.HasTemplateAssets(env.Assets) {
				if err := web.MaterializeTemplates(d.templatesDir, env.Assets, filepath.Join(env.Dir, "templates")); err != nil {
					return fmt.Errorf("draft templates: %w", err)
				}
			}
			if web.HasStaticAssets(env.Assets) {
				dd.staticOverlay = web.StaticOverlay("/static", env.Assets)
			}
			h.mu.Lock()
			h.sandbox[env.ID] = filepath.Dir(env.Dir) // env.Dir is <root>/host
			h.mu.Unlock()
			return mountStarter(app, p, dd)
		},
		// The sandboxed database starts empty; give it the starter's schema
		// and the dev accounts so login works in the preview.
		AfterBuild: func(ctx context.Context, id string, p *platform.Platform) error {
			db, ok := generationDatabase(p)
			if !ok {
				return nil
			}
			h.mu.Lock()
			root := h.sandbox[id]
			h.mu.Unlock()
			file := filepath.Join(root, "db", "database.db")
			if root == "" {
				return errors.New("preview sandbox directory unknown")
			}
			if _, err := os.Stat(file); err != nil {
				// studio/preview's sandbox puts the database at <root>/db/<resource>.db.
				return fmt.Errorf("sandboxed database not at %s (preview layout changed?): %w", file, err)
			}
			if err := migrateSQLite("file:" + file + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"); err != nil {
				return fmt.Errorf("migrating the sandboxed database: %w", err)
			}
			return seedGeneration(ctx, db, d)
		},
		Logf: func(format string, args ...any) {
			d.logger.Info(fmt.Sprintf("preview: "+format, args...))
		},
	}
}
