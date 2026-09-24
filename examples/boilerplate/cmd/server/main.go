package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	_ "modernc.org/sqlite"

	"github.com/oarkflow/fh"
	"github.com/oarkflow/ref/boilerplate/internal/web"
	"github.com/oarkflow/ref/platform"
)

func main() {
	// Setup context for graceful shutdown
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	bclDir := resolveDirectory("boilerplate/bcl", "./bcl", "bcl")
	templatesDir := resolveDirectory("boilerplate/templates", "./templates", "templates")

	opts := platform.DefaultLoadOptions()

	// Load the entire application definition from the BCL directory.
	// All roles, resources, intents, routes, and static assets are declared in BCL.
	// No Go route handlers or static mounts exist in this file.
	p, err := platform.LoadDir(ctx, bclDir, opts)
	if err != nil {
		log.Fatalf("Failed to compile BCL configuration from %s: %v", bclDir, err)
	}
	defer p.Close()

	// Initialize the SPL Template Rendering Engine (github.com/oarkflow/spl & github.com/oarkflow/template)
	splRenderer, err := web.NewSPLRenderer(web.RendererConfig{
		TemplatesDir: templatesDir,
		IsDev:        os.Getenv("APP_ENV") != "production",
		AppName:      "REF Enterprise Auth Boilerplate",
		AppVersion:   "v1.0.0",
	})
	if err != nil {
		log.Fatalf("Failed to initialize SPL template engine: %v", err)
	}

	// Create FastHTTP application with SPL template adapter attached
	app := fh.NewFast(
		fh.WithTemplateEngine(splRenderer),
	)

	// Mount all BCL routes and static file handlers onto the HTTP engine
	if err := p.Mount(app); err != nil {
		log.Fatalf("Failed to mount BCL application: %v", err)
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	addr := ":" + port

	go func() {
		fmt.Printf("\n🚀 REF Auth Boilerplate (Pure BCL Defined Architecture)\n")
		fmt.Printf("   ├─ Server listening at http://localhost%s\n", addr)
		fmt.Printf("   ├─ Architecture: Zero Go routes in main.go — 100%% BCL defined\n")
		fmt.Printf("   ├─ BCL Definitions: %s/*.bcl\n", bclDir)
		fmt.Printf("   ├─ Security: Native Argon2id Password Hashing (RFC 9106) + RBAC\n")
		fmt.Printf("   ├─ Templates: SPL Engine (github.com/oarkflow/spl + template)\n")
		fmt.Printf("   └─ Demo Accounts: admin@example.com, manager@example.com, user@example.com\n\n")

		if err := app.Listen(addr); err != nil {
			log.Printf("Server stopped: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("\nReceived shutdown signal. Commencing graceful shutdown...")
	if err := app.Shutdown(); err != nil {
		log.Printf("Error during server shutdown: %v", err)
	}
	log.Println("Server gracefully stopped.")
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
