// Command medical-coding runs the medical-coding example application.
//
//	go run ./examples/medical-coding                     # serve on :8096
//	go run ./examples/medical-coding -token '{"sub":"u1","roles":["admin"]}'
//
// -token prints a development JWT signed by the app's own auth.jwt resource,
// carrying whatever claims the JSON object holds, and exits.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/oarkflow/fh"
	"github.com/oarkflow/ref/platform"
	_ "modernc.org/sqlite"
)

type tokenIssuer interface {
	Issue(platform.Principal, time.Duration, map[string]any) (string, time.Time, error)
}

func main() {
	config := flag.String("config", "examples/medical-coding/app.bcl", "path to the application document")
	addr := flag.String("addr", ":8096", "address to listen on")
	token := flag.String("token", "", "print a development JWT for these JSON claims and exit")
	flag.Parse()

	app, err := platform.LoadFile(context.Background(), *config, platform.DefaultLoadOptions())
	if err != nil {
		log.Fatalf("medical-coding: compile: %v", err)
	}
	defer app.Close()

	if *token != "" {
		fmt.Println(issueToken(app, *token))
		return
	}

	server := fh.NewFast()
	if err := app.Mount(server); err != nil {
		log.Fatalf("medical-coding: mount: %v", err)
	}
	for _, route := range app.Document.Routes {
		log.Printf("medical-coding: %-6s %s -> %s", route.Method, route.Path, route.Intent)
	}
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-stop
		_ = server.ShutdownWithTimeout(10 * time.Second)
	}()
	log.Printf("medical-coding: listening on %s", *addr)
	if err := server.Listen(*addr); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("medical-coding: %v", err)
	}
}

func issueToken(app *platform.Platform, claimsJSON string) string {
	var claims map[string]any
	if err := json.Unmarshal([]byte(claimsJSON), &claims); err != nil {
		log.Fatalf("-token: %v", err)
	}
	resource, ok := app.Resource("jwt")
	if !ok {
		log.Fatal("-token: the app has no \"jwt\" resource")
	}
	issuer, ok := resource.(tokenIssuer)
	if !ok {
		log.Fatal("-token: the \"jwt\" resource cannot issue tokens")
	}
	principal := platform.Principal{ID: fmt.Sprint(claims["sub"])}
	if roles, ok := claims["roles"].([]any); ok {
		for _, r := range roles {
			principal.Roles = append(principal.Roles, fmt.Sprint(r))
		}
	}
	signed, _, err := issuer.Issue(principal, 12*time.Hour, claims)
	if err != nil {
		log.Fatalf("-token: %v", err)
	}
	return signed
}
