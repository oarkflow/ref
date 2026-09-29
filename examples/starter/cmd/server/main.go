// Command server boots the application declared in ../../resources/config.
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
// to resources/config/, not to this file. See ../../README.md's "How to add a product".
package main

import (
	"context"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
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
	"github.com/oarkflow/ref/examples/starter/internal/telemetry"
	"github.com/oarkflow/ref/examples/starter/internal/web"
	"github.com/oarkflow/ref/health"
	"github.com/oarkflow/ref/observer"
	otelobserver "github.com/oarkflow/ref/observer/otel"
	promobserver "github.com/oarkflow/ref/observer/prometheus"
	slogobserver "github.com/oarkflow/ref/observer/slog"
	"github.com/oarkflow/ref/platform"

	_ "modernc.org/sqlite"
	// To run against PostgreSQL instead: set DB_DRIVER=pgx and DB_DSN=..., and
	// uncomment the driver import below. Nothing else in this repository
	// changes — the database resource's driver/dsn live entirely in
	// resources/config/01_resources.bcl. Verified end to end against a real PostgreSQL
	// 16 container while building this starter: login, session cookies,
	// the orders workflow (flow.branch + a SERIAL primary key), and the
	// password-reset flow all work identically to SQLite.
	// _ "github.com/jackc/pgx/v5/stdlib"
)

// Bootstrap is host-level configuration needed before the BCL document can
// even be parsed. It is deliberately separate from the document's own
// resources and secrets (resources/config/00_app.bcl, resources/config/01_resources.bcl) and from its
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
	// POST /api/v1/admin/maintenance (resources/config/10_maintenance.bcl) without a
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
	// AppVersion tags both the SPL renderer's global and, when tracing is
	// enabled, the OTel resource's service.version.
	AppVersion string `env:"APP_VERSION" default:"0.1.0"`
	// TracingEndpoint enables OpenTelemetry tracing when set — see
	// internal/telemetry/tracing.go's doc comment.
	TracingEndpoint string `env:"OTEL_EXPORTER_OTLP_ENDPOINT" default:""`
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

	// SESSION_SECRET / WEBHOOK_SECRET, the same way, from files instead of a
	// human-edited .env — the shape a secrets-manager sidecar (Vault Agent,
	// the AWS/GCP Secrets Manager CSI driver, ...) renders into a pod, and
	// the shape scripts/render-secrets-from-vault.sh renders locally against
	// a real Vault. SESSION_SECRET_FILE/WEBHOOK_SECRET_FILE override the
	// path per secret; SECRETS_DIR overrides the directory both default
	// names (session_secret, webhook_secret) resolve against. Either
	// SESSION_SECRET/WEBHOOK_SECRET already being set (the quick-start path)
	// or neither file existing (no secrets manager configured) is the normal
	// case — see LoadSecretFile's doc comment. resources/config/01_resources.bcl's
	// session resource reads SESSION_SECRET directly (env.required, not the
	// declarative `secret` block resources/config/00_app.bcl also declares it as),
	// which is why this has to run here, in the process environment, rather
	// than through that block's own env-then-file fallback — that fallback
	// is what actually resolves WEBHOOK_SECRET (via the "webhook_secret"
	// trigger reference in resources/config/07_triggers.bcl), so setting it here too
	// is redundant for that one but harmless, and keeps both secrets on one
	// visible mechanism instead of two different ones.
	secretsDir := os.Getenv("SECRETS_DIR")
	if secretsDir == "" {
		secretsDir = "./.data/secrets"
	}
	sessionSecretFile := os.Getenv("SESSION_SECRET_FILE")
	if sessionSecretFile == "" {
		sessionSecretFile = filepath.Join(secretsDir, "session_secret")
	}
	if err := bootstrap.LoadSecretFile("SESSION_SECRET", sessionSecretFile); err != nil {
		log.Fatalf("starter: loading %s: %v", sessionSecretFile, err)
	}
	webhookSecretFile := os.Getenv("WEBHOOK_SECRET_FILE")
	if webhookSecretFile == "" {
		webhookSecretFile = filepath.Join(secretsDir, "webhook_secret")
	}
	if err := bootstrap.LoadSecretFile("WEBHOOK_SECRET", webhookSecretFile); err != nil {
		log.Fatalf("starter: loading %s: %v", webhookSecretFile, err)
	}

	boot, err := loadBootstrap(ctx)
	if err != nil {
		log.Fatalf("starter: %v", err)
	}

	logger := newLogger(boot.Env, boot.LogLevel, boot.LogWebhookURL, boot.LogWebhookAuth)
	logger.Info("starting", zlog.String("env", boot.Env), zlog.String("replica", boot.ReplicaID))

	bclDir := bootstrap.ResolveDir("examples/starter/resources/config", "./resources/config", "resources/config")
	templatesDir := bootstrap.ResolveDir("examples/starter/resources/templates", "./resources/templates", "resources/templates")
	staticDir := bootstrap.ResolveDir("examples/starter/resources/static", "./resources/static", "resources/static")

	// Maintenance mode: one gate shared by the HTTP middleware below, the
	// readiness check, and the "ops.maintenance_set" action a BCL admin
	// route can call (resources/config/10_maintenance.bcl). RegisterAction installs into
	// a process-wide driver map that platform.DefaultLoadOptions()'s
	// NewRegistry() snapshots at the moment it's called, so this must run
	// strictly before that — not just before LoadDir.
	maintenance := ops.NewMaintenanceGate(boot.Maintenance, "")
	maintenance.RegisterAction()

	opts := platform.DefaultLoadOptions()
	opts.ReplicaID = boot.ReplicaID
	// BCL's `profile "name" { override "resource.x" { ... } }` blocks
	// (resources/config/11_environments.bcl) apply only when Profile matches — this is
	// how "various environments" beyond a single informational string
	// works: no filename convention, no code change, just opts.Profile set
	// from the same APP_ENV every other environment-aware check reads.
	opts.Profile = boot.Env
	// zlog.NewSlogHandler bridges the same zlog logger into stdlib log/slog,
	// which observer/slog already knows how to consume. Every REF node
	// execution, decision, effect and intent completion compiled from resources/config/
	// is observed through it, in the same structured format as the HTTP
	// access log below — one logging mechanism, not two, and (with
	// LOG_WEBHOOK_URL set) one third-party sink for both.
	// slogDurationFixHandler (logging.go) works around a real gap in
	// zlog.NewSlogHandler's own encoders: a Duration attr renders as a raw
	// nanosecond count, not a us/ms/s-suffixed string.
	slogLogger := slog.New(slogDurationFixHandler{zlog.NewSlogHandler(logger)})
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

	// Distributed tracing, opt-in: unset OTEL_EXPORTER_OTLP_ENDPOINT (the
	// standard OTel env var, e.g. "localhost:4318" for a local
	// otel-collector) and this is a no-op, same cost as not importing the
	// package at all. See internal/telemetry/tracing.go and
	// github.com/oarkflow/ref/observer/otel's own doc comment for what
	// span this stream can and cannot express (no incoming request
	// context, so no parent link to an HTTP span from this alone).
	var tracerShutdown func(context.Context) error
	if boot.TracingEndpoint != "" {
		tp, shutdown, err := telemetry.NewTracerProvider(ctx, boot.TracingEndpoint, "starter", boot.AppVersion, boot.ReplicaID)
		if err != nil {
			log.Fatalf("starter: tracing: %v", err)
		}
		tracerShutdown = shutdown
		opts.Observers = append(opts.Observers, otelobserver.New(tp.Tracer("starter")))
		logger.Info("tracing enabled", zlog.String("endpoint", boot.TracingEndpoint))
	}

	healthRegistry := health.NewRegistry()
	opts.HealthRegistry = healthRegistry
	healthRegistry.Register("maintenance", health.Simple(maintenance.HealthCheck))

	// Checked, and applied if needed, on every boot — see migrate.go's
	// ensureMigrated doc comment for the three ways this can go depending on
	// whether anything can answer a prompt. Must run before LoadDir: the
	// very first thing LoadDir does past compiling the document is seed the
	// dev admin account (seed.go), which queries the users table directly
	// and previously turned a forgotten migration into a raw driver error.
	if err := ensureMigrated(logger); err != nil {
		log.Fatalf("starter: %v", err)
	}

	p, err := platform.LoadDir(ctx, bclDir, opts)
	if err != nil {
		logger.Error("compiling bcl", zlog.String("path", bclDir), zlog.Err(err))
		log.Fatalf("starter: compiling %s: %v", bclDir, err)
	}
	defer p.Close()

	// The "database" resource (resources/config/01_resources.bcl) is a database.sql
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
			// owner@example.com/reviewer@example.com/approver@example.com,
			// same password as the admin account — see ops.SeedDevTodoRoleAccounts's
			// own doc comment. Lets the todo workflow example's review/
			// approval steps be walked by logging in as each role in turn,
			// with no manual `UPDATE users SET roles = ...`.
			if err := ops.SeedDevTodoRoleAccounts(ctx, db, boot.Env, boot.AdminPassword); err != nil {
				logger.Error("seeding dev todo role accounts", zlog.Err(err))
				log.Fatalf("starter: seeding dev todo role accounts: %v", err)
			}
		}
	}

	// The SPL renderer is Go glue a BCL document has no way to name — see
	// internal/web/renderer.go's package doc. Every page it renders (which
	// template, which layout, which intent feeds it) is still declared
	// entirely in resources/config/04_routes.bcl.
	renderer, err := web.NewSPLRenderer(web.RendererConfig{
		TemplatesDir: templatesDir,
		IsDev:        boot.Env != "production",
		AppName:      "starter",
		AppVersion:   boot.AppVersion,
		DemoPassword: boot.AdminPassword,
	})
	if err != nil {
		log.Fatalf("starter: template engine: %v", err)
	}

	app := fh.NewFast(fh.WithTemplateEngine(renderer), fh.WithErrorHandler(htmlAwareErrorHandler(logger)))

	// Every route failure — an RBAC denial, a 404, a bad request, ... — is
	// plain JSON by default (platform/routes.go's projectFailure), correct
	// for the JSON API but not for someone who followed a link in a
	// browser. registerHTMLErrorPages (errors.go) renders
	// resources/templates/pages/errors/error.html instead, for exactly the
	// requests that asked for HTML and aren't hitting /api/*. Must run
	// before the first request a real listener could receive; there is no
	// registry-construction ordering rule to respect here (unlike
	// RegisterActionDriver), but registering it here, alongside every other
	// piece of request-handling wiring, keeps that obvious.
	registerHTMLErrorPages()

	app.Use(maintenance.Middleware())
	app.Use(httpAccessLog(logger))
	if err := p.Mount(app); err != nil {
		logger.Error("mounting bcl routes", zlog.Err(err))
		log.Fatalf("starter: mount: %v", err)
	}
	app.Get("/livez", wrapHTTPHandler(health.LivenessHandler(healthRegistry)))
	app.Get("/readyz", wrapHTTPHandler(health.ReadinessHandler(healthRegistry)))
	// A service worker must be served from the root to get root scope —
	// resources/config/08_static.bcl's "/static" prefix can't give it that,
	// so this is the one static asset served directly in Go instead of
	// through a BCL `static` block, the same reason /livez and /readyz are.
	// Always no-cache, deliberately stronger than 08_static.bcl's own
	// default: a stale service worker doesn't just show an old page once,
	// it can keep controlling every future load until it's explicitly
	// unregistered — see resources/static/sw.js's own doc comment.
	swPath := filepath.Join(staticDir, "sw.js")
	app.Get("/sw.js", func(c fh.Ctx) error {
		c.Set("Cache-Control", "no-cache")
		c.Set("Content-Type", "text/javascript; charset=utf-8")
		return c.SendFile(swPath)
	})
	// Every REF node/decision/effect/execution event, counted and timed —
	// promobserver.New above is the only wiring; nothing in resources/config/ knows
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
	if tracerShutdown != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := tracerShutdown(shutdownCtx); err != nil {
			logger.Error("tracing shutdown", zlog.Err(err))
		}
		cancel()
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
