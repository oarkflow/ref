package platform

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/oarkflow/fh"
)

const testGuardPolicy = `pack "test" {
  version "1.0.0"
  mode enforce
}

guard "test" {
  mode enforce
  version "1.0.0"
}

rule "block-marked-path" {
  status active
  priority 100

  scope {
    paths ["/blocked"]
  }

  trigger {
    on request.received
  }

  when {
    request.path equals "/blocked"
  }

  risk {
    base 90
    max 100
  }

  severity {
    critical when risk.score greater_or_equal 90
  }

  actions {
    critical {
      run block
    }
  }
}`

const testGuardDoc = `
name "guard-test"
environment "test"

resource "guard" {
  kind "security.tcpguard"
  config {
    source ` + "`" + testGuardPolicy + "`" + `
    mode "enforce"
  }
}

intent "ping" {
  response "response"
  node "ok" { uses "collect" requires [input] provides [response] config { unwrap true } }
}

route "blocked" { method GET path "/blocked" intent "ping" allow_anonymous true }
route "open" { method GET path "/open" intent "ping" allow_anonymous true }
`

// TestSecurityTCPGuardBlocksMatchedPath proves the resource is actually
// wired into the request pipeline (routes.go's serve), not just that it
// compiles: a rule scoped to "/blocked" refuses that path and lets an
// otherwise-identical route through untouched.
func TestSecurityTCPGuardBlocksMatchedPath(t *testing.T) {
	ctx := context.Background()
	p, err := Compile(ctx, []byte(testGuardDoc), t.TempDir(), DefaultLoadOptions())
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	defer p.Close()
	if p.guard == nil {
		t.Fatal("expected a security.tcpguard resource to be discovered")
	}

	app := fh.NewFast()
	if err := p.Mount(app); err != nil {
		t.Fatalf("mount: %v", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	served := make(chan struct{})
	go func() {
		defer close(served)
		_ = app.Serve(listener)
	}()
	defer func() {
		_ = app.ShutdownWithTimeout(2 * time.Second)
		_ = listener.Close()
		<-served
	}()

	base := "http://" + listener.Addr().String()
	client := &http.Client{Timeout: 10 * time.Second}

	getStatus := func(path string) int {
		resp, err := client.Get(base + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}

	if status := getStatus("/open"); status != http.StatusOK {
		t.Fatalf("GET /open = %d, want 200 (unblocked path)", status)
	}
	if status := getStatus("/blocked"); status != http.StatusForbidden {
		t.Fatalf("GET /blocked = %d, want 403 (guard should have refused it)", status)
	}
}
