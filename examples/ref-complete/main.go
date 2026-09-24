package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	_ "modernc.org/sqlite"

	"github.com/oarkflow/fh"
	"github.com/oarkflow/ref/platform"
)

func main() {
	configPath := flag.String("config", "examples/ref-complete/app.bcl", "BCL application document")
	addr := flag.String("addr", ":8092", "listen address")
	runDemos := flag.Bool("kernel-demo", true, "run the in-process scheduler, source, effect, replay, AI and streaming demonstrations before serving")
	flag.Parse()

	if err := prepareLocalPaths(); err != nil {
		log.Fatalf("ref-complete: prepare paths: %v", err)
	}
	if os.Getenv("COMPLETE_SESSION_SECRET") == "" {
		_ = os.Setenv("COMPLETE_SESSION_SECRET", "local-only-ref-complete-session-secret-change-me")
	}

	if *runDemos {
		if err := runKernelDemo(context.Background()); err != nil {
			log.Fatalf("ref-complete: kernel demo: %v", err)
		}
	}

	app, err := platform.LoadFile(context.Background(), *configPath, platform.DefaultLoadOptions())
	if err != nil {
		log.Fatalf("ref-complete: compile %s: %v", *configPath, err)
	}
	defer app.Close()

	server := fh.NewFast()
	if err := app.Mount(server); err != nil {
		log.Fatalf("ref-complete: mount: %v", err)
	}
	for _, route := range app.Document.Routes {
		log.Printf("ref-complete: %-6s %-24s -> %s", route.Method, route.Path, route.Intent)
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-stop
		log.Print("ref-complete: shutting down")
		if err := server.ShutdownWithTimeout(10 * time.Second); err != nil {
			log.Printf("ref-complete: shutdown: %v", err)
		}
	}()

	log.Printf("ref-complete: listening on %s", *addr)
	log.Printf("ref-complete: BCL routes are ready; try POST /session then POST /orders")
	if err := server.Listen(*addr); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("ref-complete: %v", err)
	}
}

func prepareLocalPaths() error {
	queueDir := os.Getenv("COMPLETE_QUEUE_DIR")
	if queueDir == "" {
		queueDir = ".data/ref-complete/queue"
	}
	if err := os.MkdirAll(queueDir, 0o755); err != nil {
		return err
	}
	dsn := os.Getenv("COMPLETE_DATABASE_URL")
	if dsn != "" {
		if !strings.HasPrefix(dsn, "file:") {
			return nil
		}
		path := strings.TrimPrefix(dsn, "file:")
		if path != "" && !strings.HasPrefix(path, ":") {
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(".data/ref-complete/ref-complete.db"), 0o755); err != nil {
		return err
	}
	return nil
}
