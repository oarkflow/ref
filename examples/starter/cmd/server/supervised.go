package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/oarkflow/fh"
	"github.com/oarkflow/zlog"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/oarkflow/ref/deploy"
	"github.com/oarkflow/ref/examples/starter/internal/ops"
	"github.com/oarkflow/ref/examples/starter/internal/web"
	"github.com/oarkflow/ref/health"
	"github.com/oarkflow/ref/platform"
)

// starterDeps is the process-level wiring both the plain boot path and the
// supervised one hand to every generation they build.
type starterDeps struct {
	logger       *zlog.Logger
	boot         Bootstrap
	health       *health.Registry
	maintenance  *ops.MaintenanceGate
	promRegistry *prometheus.Registry
	staticDir    string
	templatesDir string
	// staticOverlay, when set, answers requests for the current revision's
	// static assets before the platform's routes see them (see
	// web.StaticOverlay). Only the supervised path sets it.
	staticOverlay fh.Handler
}

// prepareGeneration does what every freshly compiled generation needs before
// it serves: a database readiness check and the dev-only account seeds.
func prepareGeneration(ctx context.Context, p *platform.Platform, d starterDeps) error {
	// The "database" resource (resources/config/01_resources.bcl) is a database.sql
	// resource; a readiness check that cannot ping it means requests that
	// touch the database would fail too, so /readyz should say so before a
	// load balancer routes traffic here.
	db, ok := generationDatabase(p)
	if !ok {
		return nil
	}
	d.health.Register("database", health.Simple(func(ctx context.Context) error {
		return db.PingContext(ctx)
	}))
	return seedGeneration(ctx, db, d)
}

// generationDatabase is the compiled document's "database" resource.
func generationDatabase(p *platform.Platform) (*platform.Database, bool) {
	res, ok := p.Resource("database")
	if !ok {
		return nil, false
	}
	db, ok := res.(*platform.Database)
	return db, ok
}

// seedGeneration creates the dev-only accounts. Studio's preview calls it on
// its sandboxed database (and not prepareGeneration, which would replace the
// live app's readiness check).
func seedGeneration(ctx context.Context, db *platform.Database, d starterDeps) error {
	if err := ops.SeedDevAdmin(ctx, db, d.boot.Env, d.boot.AdminEmail, d.boot.AdminPassword); err != nil {
		return fmt.Errorf("seeding dev admin: %w", err)
	}
	// owner@example.com/reviewer@example.com/approver@example.com,
	// same password as the admin account — see ops.SeedDevTodoRoleAccounts's
	// own doc comment. Lets the todo workflow example's review/
	// approval steps be walked by logging in as each role in turn,
	// with no manual `UPDATE users SET roles = ...`.
	if err := ops.SeedDevTodoRoleAccounts(ctx, db, d.boot.Env, d.boot.AdminPassword); err != nil {
		return fmt.Errorf("seeding dev todo role accounts: %w", err)
	}
	return nil
}

// newStarterApp is the fh app every generation (live or Studio preview)
// serves from: the SPL renderer reading templates from templatesDir, and the
// HTML-aware error handler.
func newStarterApp(d starterDeps, templatesDir, label string) *fh.App {
	opts := []fh.Option{fh.WithStartupBannerDisabled(true), fh.WithErrorHandler(htmlAwareErrorHandler(d.logger))}
	renderer, err := web.NewSPLRenderer(web.RendererConfig{
		TemplatesDir: templatesDir,
		IsDev:        d.boot.Env != "production",
		AppName:      "starter",
		AppVersion:   d.boot.AppVersion,
		DemoPassword: d.boot.AdminPassword,
	})
	if err != nil {
		d.logger.Error("template engine", zlog.String("generation", label), zlog.Err(err))
	} else {
		opts = append(opts, fh.WithTemplateEngine(renderer))
	}
	return fh.NewFast(opts...)
}

// mountStarter registers the middleware, the compiled document's routes and
// the process-level endpoints on app.
func mountStarter(app *fh.App, p *platform.Platform, d starterDeps) error {
	app.Use(d.maintenance.Middleware())
	app.Use(httpAccessLog(d.logger))
	if d.staticOverlay != nil {
		app.Use(d.staticOverlay)
	}
	if err := p.Mount(app); err != nil {
		return err
	}
	app.Get("/livez", wrapHTTPHandler(health.LivenessHandler(d.health)))
	app.Get("/readyz", wrapHTTPHandler(health.ReadinessHandler(d.health)))
	// A service worker must be served from the root to get root scope —
	// resources/config/08_static.bcl's "/static" prefix can't give it that,
	// so this is the one static asset served directly in Go instead of
	// through a BCL `static` block, the same reason /livez and /readyz are.
	// Always no-cache, deliberately stronger than 08_static.bcl's own
	// default: a stale service worker doesn't just show an old page once,
	// it can keep controlling every future load until it's explicitly
	// unregistered — see resources/static/sw.js's own doc comment.
	swPath := filepath.Join(d.staticDir, "sw.js")
	app.Get("/sw.js", func(c fh.Ctx) error {
		c.Set("Cache-Control", "no-cache")
		c.Set("Content-Type", "text/javascript; charset=utf-8")
		return c.SendFile(swPath)
	})
	// Every REF node/decision/effect/execution event, counted and timed —
	// promobserver.New is the only wiring; nothing in resources/config/ knows
	// metrics exist. Scrape it like any other Prometheus target.
	app.Get("/metrics", wrapHTTPHandler(promhttp.HandlerFor(d.promRegistry, promhttp.HandlerOpts{})))
	return nil
}

// runSupervised serves the document through deploy.Supervisor (opt-in with
// STARTER_SUPERVISOR=1). The files in bclDir become the first revision,
// approved by "system" and activated at boot; later revisions go through the
// admin API (STARTER_ADMIN_ADDR, default 127.0.0.1:8081, bearer token
// STARTER_ADMIN_TOKEN). Revisions live in memory, so they do not survive a
// restart: the files are the durable source until a SQL store is wired in.
//
// Optional: STARTER_REVISION_SECRET signs revisions (HMAC);
// STARTER_REVIEWER_TOKEN (at least 16 characters; a shorter one is an error)
// adds a second identity, "reviewer", so one person can propose and another
// approve;
// STARTER_ALLOW_SELF_APPROVAL=1 lets one token approve its own proposals.
//
// STARTER_STUDIO=1 also mounts the visual editor (studio/) on the admin
// listener under /studio, using the same tokens: STARTER_ADMIN_TOKEN is
// "admin", STARTER_REVIEWER_TOKEN "reviewer", and the optional
// STARTER_EDITOR_TOKEN (at least 16 characters) "editor", who can edit
// drafts and propose but not approve. STARTER_STUDIO_SRC may name a checkout
// of the ref repository, so block docs come from its Go doc comments, and
// STARTER_STUDIO_PERSIST=1 keeps drafts and the audit log in the application
// database. Studio's page tools and sandboxed preview are wired in studio.go;
// CONFIGURATION.md's "Studio" section is the operator's guide.
//
// The boot revision is a bundle with one file per .bcl. A later revision may be
// proposed the same way (POST /revisions with {"files": {"04_routes.bcl": "…"}})
// and reviewers see which files changed.
//
// A revision may also carry assets: {"files": {...}, "assets":
// {"templates/pages/todos/list.html": "…", "static/css/x.css": "…"}}. They
// override the templates and static files on disk path for path, so a
// revision names only what it changes; the boot revision has none, which
// leaves the disk as the base. The new generation renders with them as soon
// as the revision is activated, and rolling back returns to the old ones.
func runSupervised(ctx context.Context, d starterDeps, bclDir string, opts platform.LoadOptions) error {
	token := os.Getenv("STARTER_ADMIN_TOKEN")
	if len(token) < 16 {
		return errors.New("STARTER_SUPERVISOR=1 needs STARTER_ADMIN_TOKEN, at least 16 characters (the admin API bearer token)")
	}
	adminAddr := os.Getenv("STARTER_ADMIN_ADDR")
	if adminAddr == "" {
		adminAddr = "127.0.0.1:8081"
	}

	registerHTMLErrorPages()
	overlays := newTemplateOverlays()
	defer overlays.removeAll()

	mgr := &deploy.Manager{
		Store:             deploy.NewMemoryStore(),
		App:               "starter",
		Approvals:         1,
		AllowSelfApproval: os.Getenv("STARTER_ALLOW_SELF_APPROVAL") == "1",
		Validate: func(ctx context.Context, src []byte) platform.ValidationReport {
			return platform.Validate(ctx, src, bclDir, opts)
		},
		// Bundle proposals (the files of resources/config, edited together)
		// get diagnostics that name the file and line.
		ValidateBundle: func(ctx context.Context, b platform.Bundle) platform.ValidationReport {
			return platform.ValidateBundle(ctx, b, bclDir, opts)
		},
	}
	if secret := os.Getenv("STARTER_REVISION_SECRET"); secret != "" {
		mgr.Secret = []byte(secret)
	}

	// One file per .bcl, so later revisions can be proposed (and reviewed)
	// file by file; the joined document is what platform.LoadDir compiles.
	files, err := platform.ReadBundleDir(bclDir)
	if err != nil {
		return err
	}
	rev, err := mgr.ProposeBundle(ctx, files, "bootstrap", "initial revision from "+bclDir)
	if err != nil {
		return fmt.Errorf("bootstrap revision: %w", err)
	}
	if rev, err = mgr.Approve(ctx, rev.ID, "system", "bootstrap from files"); err != nil {
		return err
	}
	if _, err = mgr.Activate(ctx, rev.ID, "system"); err != nil {
		return err
	}

	sup := &deploy.Supervisor{
		Manager: mgr,
		Build: func(ctx context.Context, src []byte) (*platform.Platform, error) {
			p, err := platform.Compile(ctx, src, bclDir, opts)
			if err != nil {
				return nil, err
			}
			if err := prepareGeneration(ctx, p, d); err != nil {
				_ = p.Close()
				return nil, err
			}
			return p, nil
		},
		// Each generation renders with its own revision's templates: the
		// directory on disk, or, when the revision overrides some, a scratch
		// copy of it with the overrides applied (made in MountFor, removed in
		// Closed).
		NewAppFor: func(rev *deploy.Revision) *fh.App {
			dir := d.templatesDir
			if web.HasTemplateAssets(rev.Assets) {
				if tmp, err := overlays.create(rev.ID); err != nil {
					d.logger.Error("template overrides unavailable, using the templates on disk",
						zlog.String("revision", rev.ID), zlog.Err(err))
				} else {
					dir = tmp
				}
			}
			return newStarterApp(d, dir, rev.ID)
		},
		MountFor: func(app *fh.App, p *platform.Platform, rev *deploy.Revision) error {
			if tmp, ok := overlays.dir(rev.ID); ok {
				if err := web.MaterializeTemplates(d.templatesDir, rev.Assets, tmp); err != nil {
					return fmt.Errorf("revision %d templates: %w", rev.Seq, err)
				}
			}
			dd := d
			if web.HasStaticAssets(rev.Assets) {
				// The document's `static "assets"` block serves /static from disk.
				dd.staticOverlay = web.StaticOverlay("/static", rev.Assets)
			}
			return mountStarter(app, p, dd)
		},
		Closed: overlays.remove,
		Logf:   func(format string, args ...any) { d.logger.Info(fmt.Sprintf(format, args...)) },
	}

	tokens := map[string]string{token: "admin"}
	if reviewer := os.Getenv("STARTER_REVIEWER_TOKEN"); reviewer != "" && reviewer != token {
		if len(reviewer) < 16 {
			return errors.New("STARTER_REVIEWER_TOKEN must be at least 16 characters")
		}
		tokens[reviewer] = "reviewer"
	}
	adminAPI := (&deploy.Admin{Manager: mgr, Supervisor: sup, Tokens: tokens}).Handler()
	handler := adminAPI
	if os.Getenv("STARTER_STUDIO") == "1" {
		st, err := newStudio(ctx, studioParams{
			Manager: mgr, Supervisor: sup, Deps: d, AdminToken: token, AdminAddr: adminAddr,
			BCLDir: bclDir, Opts: opts,
		})
		if err != nil {
			return err
		}
		defer st.Close()
		mux := http.NewServeMux()
		mux.Handle("/studio/", http.StripPrefix("/studio", st.Handler))
		mux.Handle("/", adminAPI)
		handler = mux
	}
	admin := &http.Server{
		Addr:              adminAddr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		d.logger.Info("admin API listening", zlog.String("addr", adminAddr))
		if err := admin.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			d.logger.Error("admin API stopped", zlog.Err(err))
		}
	}()
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = admin.Shutdown(shutdownCtx)
	}()

	ln, err := net.Listen("tcp", ":"+d.boot.Port)
	if err != nil {
		return err
	}
	d.logger.Info("listening (supervised)", zlog.String("addr", ln.Addr().String()))
	return sup.Serve(ctx, ln)
}

// templateOverlays owns the scratch directories that hold a revision's
// templates when it overrides some: one per revision, made when its
// generation is built and removed when that generation has drained.
type templateOverlays struct {
	mu   sync.Mutex
	dirs map[string]string // revision id -> directory
}

func newTemplateOverlays() *templateOverlays {
	return &templateOverlays{dirs: map[string]string{}}
}

func (o *templateOverlays) create(revID string) (string, error) {
	dir, err := os.MkdirTemp("", "starter-templates-")
	if err != nil {
		return "", err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.dirs[revID] = dir
	return dir, nil
}

func (o *templateOverlays) dir(revID string) (string, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	d, ok := o.dirs[revID]
	return d, ok
}

// remove is deploy.Supervisor's Closed hook.
func (o *templateOverlays) remove(rev *deploy.Revision) {
	o.mu.Lock()
	dir, ok := o.dirs[rev.ID]
	delete(o.dirs, rev.ID)
	o.mu.Unlock()
	if ok {
		_ = os.RemoveAll(dir)
	}
}

func (o *templateOverlays) removeAll() {
	o.mu.Lock()
	dirs := o.dirs
	o.dirs = map[string]string{}
	o.mu.Unlock()
	for _, dir := range dirs {
		_ = os.RemoveAll(dir)
	}
}
