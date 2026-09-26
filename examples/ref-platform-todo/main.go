package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/oarkflow/fh"
	"github.com/oarkflow/ref/platform"
)

func main() {
	// Setup context for graceful shutdown
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	opts := platform.DefaultLoadOptions()

	// Load the BCL file that contains the entire application definition
	p, err := platform.LoadFile(ctx, "examples/ref-platform-todo/app.bcl", opts)
	if err != nil {
		log.Fatalf("Failed to load BCL configuration: %v", err)
	}
	defer p.Close()

	app := fh.NewFast()

	// Mount the REF intents as HTTP routes
	if err := p.Mount(app); err != nil {
		log.Fatalf("Failed to mount application: %v", err)
	}

	// In a real application, you might want to expose OpenAPI spec:
	// platform.MountArtifacts(app, "/openapi.json", "/contracts.ts")

	// Start server in a goroutine
	go func() {
		log.Println("Starting production-ready Todo Server on :8089")
		if err := app.Listen(":8089"); err != nil {
			log.Printf("Server stopped: %v", err)
		}
	}()

	// Block until signal is received
	<-ctx.Done()
	log.Println("\nReceived shutdown signal. Commencing graceful shutdown...")

	// Shutdown fast-http engine
	if err := app.Shutdown(); err != nil {
		log.Printf("Error during server shutdown: %v", err)
	}

	log.Println("Shutdown complete.")
}
