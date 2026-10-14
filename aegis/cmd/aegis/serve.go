package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/oarkflow/ref/aegis/attest"
	"github.com/oarkflow/ref/aegis/control"
	"github.com/oarkflow/ref/signing"
)

func runServe(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)

	addr := fs.String("addr", ":8080", "HTTP listen address")
	keyPath := fs.String("key", "", "Path to Ed25519 signing key PEM file (optional)")
	ttlSec := fs.Int("ttl", 3600, "Session grant TTL in seconds")

	if err := fs.Parse(args); err != nil {
		return err
	}

	store := control.NewMemoryStore()
	keyMgr := control.NewKeyManager(store)
	attestReg := attest.DefaultRegistry()

	var opts []control.ServerOption
	if *keyPath != "" {
		pemData, err := os.ReadFile(*keyPath)
		if err != nil {
			return fmt.Errorf("aegis: serve: failed to read signing key %q: %w", *keyPath, err)
		}
		key, err := signing.ParsePrivateKeyPEM("server-signing-key", signing.EdDSA, pemData)
		if err != nil {
			return fmt.Errorf("aegis: serve: failed to parse signing key: %w", err)
		}
		ks, err := signing.NewKeySet(key)
		if err != nil {
			return fmt.Errorf("aegis: serve: failed to create key set: %w", err)
		}
		opts = append(opts, control.WithServerSigningKeys(ks))
	}

	ttl := time.Duration(*ttlSec) * time.Second
	gs := control.NewGrantService(store, keyMgr,
		control.WithTTL(ttl),
		control.WithAttestRegistry(attestReg),
	)
	opts = append(opts,
		control.WithServerGrantService(gs),
		control.WithServerTTL(ttl),
	)

	srv := control.NewServer(store, opts...)

	httpServer := &http.Server{
		Addr:         *addr,
		Handler:      srv,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	fmt.Fprintf(stdout, "Aegis Control Server listening on http://%s\n", *addr)
	fmt.Fprintf(stdout, "Endpoints: /v1/grant/request, /v1/grant/refresh, /v1/package/register, /v1/keys/jwks, /healthz\n")

	// Setup graceful shutdown
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	serverErr := make(chan error, 1)
	go func() {
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			serverErr <- err
		}
	}()

	select {
	case err := <-serverErr:
		return fmt.Errorf("aegis: serve: HTTP server error: %w", err)
	case <-stop:
		fmt.Fprintln(stdout, "\nShutting down Aegis Control Server...")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return httpServer.Shutdown(ctx)
	}
}
