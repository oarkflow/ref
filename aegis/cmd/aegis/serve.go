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
	storePath := fs.String("store", "", "Path to a SQLite file for packages, grants and revocations to survive a restart (default: in-memory, forgotten on exit)")
	rateLimit := fs.Int("rate-limit", 120, "Requests per minute per client IP across every endpoint (0 disables)")
	rateBurst := fs.Int("rate-burst", 20, "Requests a client may make immediately before the per-minute rate applies")
	sessionKeys := fs.Bool("session-keys", false, "Hand out an HKDF-derived per-session key instead of the package's exact master key; only correct if every package was itself built fresh per session (see control.WithRawMasterKeyGrants). Leave this off for anything sealed with `aegis package` and registered with `aegis register`")

	if err := fs.Parse(args); err != nil {
		return err
	}

	var store control.Store
	if *storePath != "" {
		sqlStore, err := control.OpenSQLite(*storePath)
		if err != nil {
			return fmt.Errorf("aegis: serve: open store %q: %w", *storePath, err)
		}
		defer sqlStore.Close()
		store = sqlStore
		fmt.Fprintf(stdout, "Persistent store: %s\n", *storePath)
	} else {
		store = control.NewMemoryStore()
		fmt.Fprintln(stderr, "aegis: serve: no -store given; every package, grant and revocation will be forgotten when this process exits")
	}
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
	gsOpts := []control.GrantServiceOption{
		control.WithTTL(ttl),
		control.WithAttestRegistry(attestReg),
	}
	if !*sessionKeys {
		// Every package this CLI can seal (`aegis package`) and register
		// (`aegis register`) uses one fixed key for the whole package, not
		// one built fresh per session; a grant must hand that same key
		// back. See WithRawMasterKeyGrants's doc comment for the
		// alternative -session-keys opts into.
		gsOpts = append(gsOpts, control.WithRawMasterKeyGrants())
	}
	gs := control.NewGrantService(store, keyMgr, gsOpts...)
	opts = append(opts,
		control.WithServerGrantService(gs),
		control.WithServerTTL(ttl),
	)
	if *rateLimit > 0 {
		opts = append(opts, control.WithServerRateLimit(*rateLimit, *rateBurst))
	}

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
