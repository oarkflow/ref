// Command server boots the application declared in ../../bcl.
//
// There is deliberately no business logic here. This file owns exactly
// three things that a BCL document cannot express on its own: bootstrap
// settings needed before the document can even be parsed (listen address,
// replica id, log level — Bootstrap below), driver imports (the blank
// modernc.org/sqlite import, or a pgx one for PostgreSQL), and the
// process-level operational endpoints (/livez, /readyz) that expose process
// internals rather than application intents.
//
// Anything else — a new route, a new resource, a new backend — is a change
// to bcl/, not to this file. See ../../README.md's "How to add a product".
package main

import (
	"context"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	ocfg "github.com/oarkflow/config"
	bclparser "github.com/oarkflow/config/parsers/bcl"
	"github.com/oarkflow/config/providers/file"
	"github.com/oarkflow/fh"
	"github.com/oarkflow/zlog"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	refconfig "github.com/oarkflow/ref/config"
	"github.com/oarkflow/ref/examples/starter/internal/bootstrap"
	"github.com/oarkflow/ref/examples/starter/internal/ops"
	"github.com/oarkflow/ref/examples/starter/internal/web"
	"github.com/oarkflow/ref/health"
	"github.com/oarkflow/ref/observer"
	promobserver "github.com/oarkflow/ref/observer/prometheus"
	slogobserver "github.com/oarkflow/ref/observer/slog"
	"github.com/oarkflow/ref/platform"

	_ "modernc.org/sqlite"
	// To run against PostgreSQL instead: set DB_DRIVER=pgx and DB_DSN=..., and
	// uncomment the driver import below. Nothing else in this repository
	// changes — the database resource's driver/dsn live entirely in
	// bcl/01_resources.bcl. Verified end to end against a real PostgreSQL
	// 16 container while building this starter: login, session cookies,
	// the orders workflow (flow.branch + a SERIAL primary key), and the
	// password-reset flow all work identically to SQLite.
	// _ "github.com/jackc/pgx/v5/stdlib"
)

// Bootstrap is host-level configuration needed before the BCL document can
// even be parsed. It is deliberately separate from the document's own
// resources and secrets (bcl/00_app.bcl, bcl/01_resources.bcl) and from its
// config-revision lifecycle (docs/deploy.md at the repository root, if you
// grow into it) — this is process wiring, not application configuration.
type Bootstrap struct {
	Port      string `env:"PORT" default:"8080"`
	Env       string `env:"APP_ENV" default:"development"`
	ReplicaID string `env:"REPLICA_ID" default:""`
	LogLevel  string `env:"LOG_LEVEL" default:"info"`
	// Maintenance takes the whole process into maintenance mode from boot —
	// useful for a maintenance window that starts before the process does.
	// Once running, an admin can flip the same gate at runtime through
	// POST /api/v1/admin/maintenance (bcl/10_maintenance.bcl) without a
	// restart.
	Maintenance bool `env:"MAINTENANCE" default:"false"`
	// LogWebhookURL, when set, ships every structured log line (HTTP access
	// logs and every DAG node/decision/effect event) to this URL as JSON —
	// see cmd/server/logging.go's webhookWriter. This is the "one env var,
	// no code change" plugin point for a third-party log collector.
	LogWebhookURL  string `env:"LOG_WEBHOOK_URL" default:""`
	LogWebhookAuth string `env:"LOG_WEBHOOK_AUTH" default:""`
	// AdminEmail/AdminPassword seed exactly one administrator account, and
	// only when Env is not "production" and the users table is still
	// empty — see seed.go. Change the default password immediately after
	// first login on any clone that isn't purely local.
	AdminEmail    string `env:"ADMIN_EMAIL" default:"admin@example.com"`
	AdminPassword string `env:"ADMIN_PASSWORD" default:"Password123!"`
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// .env, if present, is loaded into the real process environment before
	// anything else runs — before Bootstrap, before the BCL document's own
	// env()/env.required() calls, before everything. See dotenv.go: a real
	// environment variable always wins over the file.
	if err := bootstrap.LoadDotenv(bootstrap.DotenvPath()); err != nil {
		log.Fatalf("starter: loading %s: %v", bootstrap.DotenvPath(), err)
	}

	boot, err := loadBootstrap(ctx)
	if err != nil {
		log.Fatalf("starter: %v", err)
	}

	logger := newLogger(boot.Env, boot.LogLevel, boot.LogWebhookURL, boot.LogWebhookAuth)
	logger.Info("starting", zlog.String("env", boot.Env), zlog.String("replica", boot.ReplicaID))

	bclDir := bootstrap.ResolveDir("examples/starter/bcl", "./bcl", "bcl")
	templatesDir := bootstrap.ResolveDir("examples/starter/templates", "./templates", "templates")

	// Maintenance mode: one gate shared by the HTTP middleware below, the
	// readiness check, and the "ops.maintenance_set" action a BCL admin
	// route can call (bcl/10_maintenance.bcl). RegisterAction installs into
	// a process-wide driver map that platform.DefaultLoadOptions()'s
	// NewRegistry() snapshots at the moment it's called, so this must run
	// strictly before that — not just before LoadDir.
	maintenance := ops.NewMaintenanceGate(boot.Maintenance, "")
	maintenance.RegisterAction()

	opts := platform.DefaultLoadOptions()
	opts.ReplicaID = boot.ReplicaID
	// BCL's `profile "name" { override "resource.x" { ... } }` blocks
	// (bcl/11_environments.bcl) apply only when Profile matches — this is
	// how "various environments" beyond a single informational string
	// works: no filename convention, no code change, just opts.Profile set
	// from the same APP_ENV every other environment-aware check reads.
	opts.Profile = boot.Env
	// zlog.NewSlogHandler bridges the same zlog logger into stdlib log/slog,
	// which observer/slog already knows how to consume. Every REF node
	// execution, decision, effect and intent completion compiled from bcl/
	// is observed through it, in the same structured format as the HTTP
	// access log below — one logging mechanism, not two, and (with
	// LOG_WEBHOOK_URL set) one third-party sink for both.
	slogLogger := slog.New(zlog.NewSlogHandler(logger))
	// A dedicated registry, not prometheus.DefaultRegisterer: a library that
	// happens to register its own default-registry metrics never collides
	// with this one, and /metrics never exposes anything but what this
	// process itself declared.
	promRegistry := prometheus.NewRegistry()
	promObs, err := promobserver.New(promRegistry)
	if err != nil {
		log.Fatalf("starter: prometheus observer: %v", err)
	}
	opts.Observers = []observer.Observer{slogobserver.New(slogLogger), promObs}

	healthRegistry := health.NewRegistry()
	opts.HealthRegistry = healthRegistry
	healthRegistry.Register("maintenance", health.Simple(maintenance.HealthCheck))

	p, err := platform.LoadDir(ctx, bclDir, opts)
	if err != nil {
		logger.Error("compiling bcl", zlog.String("path", bclDir), zlog.Err(err))
		log.Fatalf("starter: compiling %s: %v", bclDir, err)
	}
	defer p.Close()

	// The "database" resource (bcl/01_resources.bcl) is a database.sql
	// resource; a readiness check that cannot ping it means requests that
	// touch the database would fail too, so /readyz should say so before a
	// load balancer routes traffic here.
	if res, ok := p.Resource("database"); ok {
		if db, ok := res.(*platform.Database); ok {
			healthRegistry.Register("database", health.Simple(func(ctx context.Context) error {
				return db.PingContext(ctx)
			}))
			if err := ops.SeedDevAdmin(ctx, db, boot.Env, boot.AdminEmail, boot.AdminPassword); err != nil {
				logger.Error("seeding dev admin", zlog.Err(err))
				log.Fatalf("starter: seeding dev admin: %v", err)
			}
		}
	}

	// The SPL renderer is Go glue a BCL document has no way to name — see
	// internal/web/renderer.go's package doc. Every page it renders (which
	// template, which layout, which intent feeds it) is still declared
	// entirely in bcl/04_routes.bcl.
	renderer, err := web.NewSPLRenderer(web.RendererConfig{
		TemplatesDir: templatesDir,
		IsDev:        boot.Env != "production",
		AppName:      "starter",
	})
	if err != nil {
		log.Fatalf("starter: template engine: %v", err)
	}

	app := fh.NewFast(fh.WithTemplateEngine(renderer))
	app.Use(maintenance.Middleware())
	app.Use(httpAccessLog(logger))
	if err := p.Mount(app); err != nil {
		logger.Error("mounting bcl routes", zlog.Err(err))
		log.Fatalf("starter: mount: %v", err)
	}
	app.Get("/livez", wrapHTTPHandler(health.LivenessHandler(healthRegistry)))
	app.Get("/readyz", wrapHTTPHandler(health.ReadinessHandler(healthRegistry)))
	// Every REF node/decision/effect/execution event, counted and timed —
	// promobserver.New above is the only wiring; nothing in bcl/ knows
	// metrics exist. Scrape it like any other Prometheus target.
	app.Get("/metrics", wrapHTTPHandler(promhttp.HandlerFor(promRegistry, promhttp.HandlerOpts{})))

	addr := ":" + boot.Port
	go func() {
		logger.Info("listening", zlog.String("addr", addr))
		if err := app.Listen(addr); err != nil {
			logger.Error("server stopped", zlog.Err(err))
		}
	}()

	<-ctx.Done()
	logger.Info("shutting down")
	if err := app.ShutdownWithTimeout(10 * time.Second); err != nil {
		logger.Error("shutdown", zlog.Err(err))
	}
	logger.Info("stopped")
}

// loadBootstrap layers an optional config.bcl (github.com/oarkflow/config's
// own BCL/env/flag/dotenv providers, hot-reload and secret redaction — a
// good fit once a deployment wants more than plain environment variables)
// under plain process environment variables, which always win. This is the
// intended way to load these settings; see ref/config.FromOarkflow.
func loadBootstrap(ctx context.Context) (Bootstrap, error) {
	oc := ocfg.New()
	oc.Providers(file.Optional("config.bcl", bclparser.New()))
	if err := oc.Load(ctx); err != nil {
		return Bootstrap{}, err
	}
	return refconfig.Load[Bootstrap](
		refconfig.FromOarkflow(oc, "app"),
		refconfig.FromEnv(""),
	)
}
