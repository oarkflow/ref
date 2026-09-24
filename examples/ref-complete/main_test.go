package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/oarkflow/ref/platform"
)

func TestKernelDemo(t *testing.T) {
	if err := runKernelDemo(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestBCLApplicationLoads(t *testing.T) {
	t.Setenv("COMPLETE_SESSION_SECRET", strings.Repeat("s", 48))
	t.Setenv("COMPLETE_QUEUE_DIR", t.TempDir())
	t.Setenv("COMPLETE_DATABASE_URL", "file:"+filepath.Join(t.TempDir(), "complete.db")+"?cache=shared&mode=rwc")

	app, err := platform.LoadFile(context.Background(), "app.bcl", platform.DefaultLoadOptions())
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	if len(app.Document.Routes) != 11 {
		t.Fatalf("expected 11 configured routes, got %d", len(app.Document.Routes))
	}
	routes := make(map[string]bool, len(app.Document.Routes))
	for _, route := range app.Document.Routes {
		routes[route.Name] = true
	}
	for _, name := range []string{"orders.create", "tasks.decide", "flow.stream"} {
		if !routes[name] {
			t.Fatalf("expected route %q to be compiled", name)
		}
	}
}
