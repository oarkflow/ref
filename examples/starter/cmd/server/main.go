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
	"path/filepath"
	"syscall"
	"time"

	ocfg "github.com/oarkflow/config"
	bclparser "github.com/oarkflow/config/parsers/bcl"
	"github.com/oarkflow/config/providers/file"
	"github.com/oarkflow/fh"
	"github.com/oarkflow/zlog"

	refconfig "github.com/oarkflow/ref/config"
	"github.com/oarkflow/ref/examples/starter/internal/web"
	"github.com/oarkflow/ref/health"
	"github.com/oarkflow/ref/observer"
	slogobserver "github.com/oarkflow/ref/observer/slog"
	"github.com/oarkflow/ref/platform"

	_ "modernc.org/sqlite"
	// To run against PostgreSQL instead: set DB_DRIVER=pgx and DB_DSN=..., and
	// uncomment the driver import below. Nothing else in this repository
	// changes — the database resource's driver/dsn live entirely in
	// bcl/01_resources.bcl.
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
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	boot, err := loadBootstrap(ctx)
	if err != nil {
		log.Fatalf("starter: %v", err)
	}

	logger := newLogger(boot.Env, boot.LogLevel)
	logger.Info("starting", zlog.String("env", boot.Env), zlog.String("replica", boot.ReplicaID))

	bclDir := resolveDir("examples/starter/bcl", "./bcl", "bcl")
	templatesDir := resolveDir("examples/starter/templates", "./templates", "templates")

	opts := platform.DefaultLoadOptions()
	opts.ReplicaID = boot.ReplicaID
	// zlog.NewSlogHandler bridges the same zlog logger into stdlib log/slog,
	// which observer/slog already knows how to consume. Every REF node
	// execution, decision, effect and intent completion compiled from bcl/
	// is observed through it, in the same structured format as the HTTP
	// access log below — one logging mechanism, not two.
	slogLogger := slog.New(zlog.NewSlogHandler(logger))
	opts.Observers = []observer.Observer{slogobserver.New(slogLogger)}

	healthRegistry := health.NewRegistry()
	opts.HealthRegistry = healthRegistry

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
	app.Use(httpAccessLog(logger))
	if err := p.Mount(app); err != nil {
		logger.Error("mounting bcl routes", zlog.Err(err))
		log.Fatalf("starter: mount: %v", err)
	}
	app.Get("/livez", wrapHTTPHandler(health.LivenessHandler(healthRegistry)))
	app.Get("/readyz", wrapHTTPHandler(health.ReadinessHandler(healthRegistry)))

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

func resolveDir(candidates ...string) string {
	for _, c := range candidates {
		if fi, err := os.Stat(c); err == nil && fi.IsDir() {
			abs, _ := filepath.Abs(c)
			return abs
		}
	}
	return candidates[0]
}
