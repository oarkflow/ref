package main

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	_ "modernc.org/sqlite"

	"github.com/oarkflow/fh"
	"github.com/oarkflow/ref/examples/boilerplate/internal/security"
	"github.com/oarkflow/ref/examples/boilerplate/internal/telemetry"
	"github.com/oarkflow/ref/examples/boilerplate/internal/web"
	"github.com/oarkflow/ref/health"
	"github.com/oarkflow/ref/observer"
	otelobserver "github.com/oarkflow/ref/observer/otel"
	promobserver "github.com/oarkflow/ref/observer/prometheus"
	slogobserver "github.com/oarkflow/ref/observer/slog"
	"github.com/oarkflow/ref/platform"
	"github.com/oarkflow/zlog"
)

func main() {
	// Setup context for graceful shutdown
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	env := os.Getenv("APP_ENV")
	if env == "" {
		env = "development"
	}

	// 1. Initialize high-performance structured logging & audit subsystem (github.com/oarkflow/zlog)
	logger := telemetry.InitLogger(env, "ref-enterprise-auth")
	logger.Info("Initializing REF Enterprise Platform",
		zlog.String("env", env),
		zlog.String("version", "v1.0.0"),
	)

	// 2. Initialize business anomaly detection & threat mitigation engine (github.com/oarkflow/tcpguard)
	anomalyGuard, err := security.NewAnomalyGuard(logger)
	if err != nil {
		logger.Error("Failed to initialize tcpguard anomaly engine", zlog.Err(err))
		log.Fatalf("Failed to initialize tcpguard: %v", err)
	}

	bclDir := resolveDirectory("examples/boilerplate/bcl", "boilerplate/bcl", "./bcl", "bcl")
	templatesDir := resolveDirectory("examples/boilerplate/templates", "boilerplate/templates", "./templates", "templates")

	opts := platform.DefaultLoadOptions()

	// 2b. Production observability: wire Prometheus metrics, OpenTelemetry
	// tracing, and structured slog logging into the REF engine via
	// platform.LoadOptions.Observers. Every node execution, decision,
	// effect, and intent completion compiled from BCL is now observed
	// through all three, with no Go code inside any BCL-compiled node.
	//
	// The OTel tracer below is a no-op (observer/otel falls back to one
	// when given nil) — wiring a real exporter (Jaeger, Tempo, an OTLP
	// collector, …) is a deployment concern, not something to fabricate
	// in a reference example. Swap otelobserver.New(nil) for a real
	// sdktrace.TracerProvider's Tracer() in production.
	promRegistry := prometheus.NewRegistry()
	promObs, err := promobserver.New(promRegistry)
	if err != nil {
		log.Fatalf("Failed to initialize Prometheus observer: %v", err)
	}
	otelObs := otelobserver.New(nil)
	slogLogger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slogObs := slogobserver.New(slogLogger)
	opts.Observers = []observer.Observer{promObs, otelObs, slogObs}

	// 2c. Health checks: a *health.Registry attached here becomes reachable
	// through p.Engine.Health() once the generation compiles, and is also
	// kept directly below to register a readiness check against the
	// primary database once it is open.
	healthRegistry := health.NewRegistry()
	opts.HealthRegistry = healthRegistry

	// 3. Load the entire application definition from the BCL directory.
	// All roles, resources, intents, routes, and static assets are declared in BCL.
	// No Go route handlers or static mounts exist in this file.
	p, err := platform.LoadDir(ctx, bclDir, opts)
	if err != nil {
		logger.Error("Failed to compile BCL configuration", zlog.String("path", bclDir), zlog.Err(err))
		log.Fatalf("Failed to compile BCL configuration from %s: %v", bclDir, err)
	}
	defer p.Close()

	// The "database" resource (bcl/03_resources.bcl) is a database.sql
	// resource, which platform exposes as *platform.Database — a thin
	// wrapper embedding *sql.DB. A readiness check that cannot ping it
	// means requests that touch the database would fail too, so /readyz
	// should say so before a load balancer routes traffic here.
	if res, ok := p.Resource("database"); ok {
		if db, ok := res.(*platform.Database); ok {
			healthRegistry.Register("database", health.Simple(func(ctx context.Context) error {
				return db.PingContext(ctx)
			}))
		}
	}

	// 4. Initialize the SPL Template Rendering Engine (github.com/oarkflow/spl & github.com/oarkflow/template)
	splRenderer, err := web.NewSPLRenderer(web.RendererConfig{
		TemplatesDir: templatesDir,
		IsDev:        env != "production",
		AppName:      "REF Enterprise Auth Boilerplate",
		AppVersion:   "v1.0.0",
	})
	if err != nil {
		logger.Error("Failed to initialize SPL template engine", zlog.Err(err))
		log.Fatalf("Failed to initialize SPL template engine: %v", err)
	}

	// 5. Create FastHTTP application with SPL template adapter and security middleware
	app := fh.NewFast(
		fh.WithTemplateEngine(splRenderer),
	)

	// Attach zlog request logging and tcpguard anomaly detection middlewares
	app.Use(telemetry.HTTPLoggingMiddleware(logger))
	app.Use(anomalyGuard.Middleware())

	// 6. Mount all BCL routes and static file handlers onto the HTTP engine
	if err := p.Mount(app); err != nil {
		logger.Error("Failed to mount BCL application", zlog.Err(err))
		log.Fatalf("Failed to mount BCL application: %v", err)
	}

	// 6b. Operational endpoints. These are deliberately Go-mounted rather
	// than BCL-declared: they expose process internals (metrics, health),
	// not application intents, and every one of them is stdlib
	// net/http.Handler wired through wrapHTTPHandler below since fh's Ctx
	// has no native net/http.Handler adapter.
	app.Get("/metrics", web.WrapHTTPHandler(promhttp.HandlerFor(promRegistry, promhttp.HandlerOpts{})))
	app.Get("/livez", web.WrapHTTPHandler(health.LivenessHandler(healthRegistry)))
	app.Get("/readyz", web.WrapHTTPHandler(health.ReadinessHandler(healthRegistry)))

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	addr := ":" + port

	go func() {
		fmt.Printf("\n🚀 REF Auth Boilerplate (Pure BCL Architecture + TCPGuard + zlog)\n")
		fmt.Printf("   ├─ Server listening at http://localhost%s\n", addr)
		fmt.Printf("   ├─ Architecture: Zero Go routes in main.go — 100%% BCL defined\n")
		fmt.Printf("   ├─ BCL Definitions: %s/*.bcl\n", bclDir)
		fmt.Printf("   ├─ Security: Native Argon2id (RFC 9106) + RBAC (github.com/oarkflow/authz)\n")
		fmt.Printf("   ├─ Anomaly Guard: Business & Threat Detection (github.com/oarkflow/tcpguard)\n")
		fmt.Printf("   ├─ Telemetry: High-Performance Structured Logging (github.com/oarkflow/zlog)\n")
		fmt.Printf("   ├─ Templates: SPL Engine (github.com/oarkflow/spl + template)\n")
		fmt.Printf("   ├─ Observability: /metrics (Prometheus), /livez, /readyz (health)\n")
		fmt.Printf("   ├─ Modules: /projects, /gov, /coding, /activity (JSON under /api/v1/...)\n")
		fmt.Printf("   └─ Demo Accounts (Password123!): admin@, manager@, user@, officer.{bagmati,koshi,ktm}@, coder@, coder2@example.com\n\n")

		if err := app.Listen(addr); err != nil {
			logger.Error("Server stopped", zlog.Err(err))
		}
	}()

	<-ctx.Done()
	logger.Info("Received shutdown signal. Commencing graceful shutdown...")
	if err := app.Shutdown(); err != nil {
		logger.Error("Error during server shutdown", zlog.Err(err))
	}
	logger.Info("Server gracefully stopped.")
}

func resolveDirectory(candidates ...string) string {
	for _, c := range candidates {
		if fi, err := os.Stat(c); err == nil && fi.IsDir() {
			abs, _ := filepath.Abs(c)
			return abs
		}
	}
	if len(candidates) > 0 {
		return candidates[0]
	}
	return "."
}
