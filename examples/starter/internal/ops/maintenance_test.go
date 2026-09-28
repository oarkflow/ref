package ops

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/oarkflow/fh"
)

func TestMaintenanceGateDefaultsOff(t *testing.T) {
	g := NewMaintenanceGate(false, "")
	if g.On() {
		t.Fatal("a gate built with on=false must start off")
	}
	if err := g.HealthCheck(context.Background()); err != nil {
		t.Fatalf("health check while off = %v, want nil", err)
	}
}

func TestMaintenanceGateSetAndMessage(t *testing.T) {
	g := NewMaintenanceGate(false, "")
	g.Set(true, "planned upgrade")
	if !g.On() {
		t.Fatal("Set(true, ...) should turn the gate on")
	}
	if g.Message() != "planned upgrade" {
		t.Fatalf("Message() = %q, want %q", g.Message(), "planned upgrade")
	}
	if err := g.HealthCheck(context.Background()); err == nil {
		t.Fatal("health check while on must return an error")
	}

	// An empty message on the next Set keeps the previous one — turning
	// maintenance off shouldn't have to repeat why it was turned on.
	g.Set(false, "")
	if g.On() {
		t.Fatal("Set(false, \"\") should turn the gate off")
	}
	if g.Message() != "planned upgrade" {
		t.Fatalf("Message() after clearing = %q, want the previous message preserved", g.Message())
	}
}

// TestMaintenanceMiddlewareExemptsHealthChecks uses a real listener rather
// than fh's app.Test() helper, which is known to hang when called more than
// once against the same *App instance (see examples/boilerplate's
// boilerplate_test.go for the same workaround) — this test needs two
// requests against one app.
func TestMaintenanceMiddlewareExemptsHealthChecks(t *testing.T) {
	g := NewMaintenanceGate(true, "down for upgrade")
	app := fh.NewFast()
	app.Use(g.Middleware())
	app.Get("/health", func(c fh.Ctx) error { return c.SendString("ok") })
	app.Get("/api/v1/orders", func(c fh.Ctx) error { return c.SendString("ok") })

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	served := make(chan struct{})
	go func() {
		defer close(served)
		_ = app.Serve(listener)
	}()
	t.Cleanup(func() {
		_ = app.ShutdownWithTimeout(2 * time.Second)
		_ = listener.Close()
		<-served
	})
	base := "http://" + listener.Addr().String()

	resp, err := http.Get(base + "/health")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("GET /health while in maintenance = %d, want 200 (exempt)", resp.StatusCode)
	}

	// Accept: application/json exercises the JSON branch, which needs no
	// template engine — this package's own test app has none. The HTML
	// branch (Accept: text/html, rendered via MaintenancePage/Layout) is
	// covered in examples/starter's own test suite, which has a real SPL
	// renderer wired.
	req, err := http.NewRequest(http.MethodGet, base+"/api/v1/orders", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 503 {
		t.Fatalf("GET /api/v1/orders while in maintenance = %d, want 503", resp.StatusCode)
	}
}
