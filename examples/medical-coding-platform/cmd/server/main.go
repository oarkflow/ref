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

	"github.com/oarkflow/fh"
	"github.com/oarkflow/ref/examples/medical-coding-platform/internal/bootstrap"
	"github.com/oarkflow/ref/observer"
	promobserver "github.com/oarkflow/ref/observer/prometheus"
	slogobserver "github.com/oarkflow/ref/observer/slog"
	"github.com/oarkflow/ref/platform"
	"github.com/oarkflow/zlog"
	"github.com/prometheus/client_golang/prometheus"

	_ "modernc.org/sqlite"
	// For PostgreSQL in production:
	// _ "github.com/jackc/pgx/v5/stdlib"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	_ = bootstrap.LoadDotenv(bootstrap.DotenvPath())

	secretsDir := env("SECRETS_DIR", "./.data/secrets")
	_ = bootstrap.LoadSecretFile("SESSION_SECRET", filepath.Join(secretsDir, "session_secret"))
	_ = bootstrap.LoadSecretFile("JWT_SECRET", filepath.Join(secretsDir, "jwt_secret"))

	port := env("PORT", "3000")
	envName := env("APP_ENV", "development")

	zlogOpts := zlog.Options{
		Level: zlog.DebugLevel,
		Sink:  zlog.NewWriterSink(os.Stderr, zlog.NewConsoleEncoder(), zlog.TraceLevel),
	}
	logger := zlog.New(zlogOpts)
	logger.Info("starting CLEAR medical coding platform", zlog.String("env", envName), zlog.String("port", port))

	bclDir := bootstrap.ResolveDir("examples/medical-coding-platform/resources/config", "./resources/config", "resources/config")

	opts := platform.DefaultLoadOptions()
	opts.Profile = envName

	slogLogger := slog.New(zlog.NewSlogHandler(logger))
	promRegistry := prometheus.NewRegistry()
	promObs, err := promobserver.New(promRegistry)
	if err == nil {
		opts.Observers = []observer.Observer{slogobserver.New(slogLogger), promObs}
	} else {
		opts.Observers = []observer.Observer{slogobserver.New(slogLogger)}
	}

	// Auto-migration check via oarkflow/migrate
	if err := ensureMigrated(ctx, logger); err != nil {
		log.Fatalf("clear-platform: migration check failed: %v", err)
	}

	p, err := LoadRecursiveConfig(ctx, bclDir, opts)
	if err != nil {
		logger.Error("compiling BCL application document", zlog.String("dir", bclDir), zlog.Err(err))
		log.Fatalf("clear-platform: compiling %s: %v", bclDir, err)
	}
	defer p.Close()

	app := fh.NewFast(fh.WithErrorHandler(defaultErrorHandler(logger)))

	// Basic CORS for frontend compatibility
	app.Use(func(c fh.Ctx) error {
		c.Set("Access-Control-Allow-Origin", "*")
		c.Set("Access-Control-Allow-Methods", "GET,POST,PUT,PATCH,DELETE,OPTIONS")
		c.Set("Access-Control-Allow-Headers", "Content-Type,Authorization,X-Requested-With,X-Tenant-Id")
		if c.Method() == "OPTIONS" {
			return c.SendStatus(204)
		}
		return c.Next()
	})

	// Health check endpoint
	app.Get("/ping", func(c fh.Ctx) error {
		return c.SendString("OK")
	})

	if err := p.Mount(app); err != nil {
		logger.Error("mounting BCL routes", zlog.Err(err))
		log.Fatalf("clear-platform: mount error: %v", err)
	}

	addr := ":" + port
	go func() {
		logger.Info("server listening", zlog.String("addr", addr))
		if err := app.Listen(addr); err != nil {
			logger.Error("server stopped", zlog.Err(err))
		}
	}()

	<-ctx.Done()
	logger.Info("shutting down server")
	_ = app.ShutdownWithTimeout(5 * time.Second)
	logger.Info("server stopped gracefully")
}

func env(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}
