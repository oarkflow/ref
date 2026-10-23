package serve_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/oarkflow/ref/aegis/respond"
	"github.com/oarkflow/ref/aegis/vfs"
	"github.com/oarkflow/ref/serve"
)

const gateApp = `
name "gate-test"
version "1.0.0"
environment "testing"

intent "hello" {
  response "result"
  node "resp" {
    uses "data.transform"
    kind pure
    provides [result]
    config {
      data {
        extract {
          message "'hi'"
        }
      }
    }
  }
}

route "get_hello" {
  method "GET"
  path "/hello"
  intent "hello"
}
`

// TestServe_GateEnforcesRespondActions drives a real server through the
// respond.LocalEnforcer: restrict, isolate, then release.
func TestServe_GateEnforcesRespondActions(t *testing.T) {
	mem := vfs.New()
	mem.WriteFile("config/app.bcl", []byte(gateApp), 0o644)

	enf := &respond.LocalEnforcer{}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	app, err := serve.Start(ctx, serve.Options{FS: mem, Addr: "127.0.0.1:0", Env: "testing", Gate: enf.Gate})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Stop(2 * time.Second)

	status := func(method, path string) int {
		t.Helper()
		req, _ := http.NewRequestWithContext(ctx, method, app.URL()+path, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	apply := func(a respond.Action) {
		t.Helper()
		if err := enf.Apply(ctx, a); err != nil {
			t.Fatal(err)
		}
		if err := enf.Verify(ctx, a); err != nil {
			t.Fatal(err)
		}
	}

	if got := status("GET", "/hello"); got != 200 {
		t.Fatalf("baseline GET = %d", got)
	}
	apply(respond.Action{Kind: respond.ActRevokeCaps, Caps: respond.CapWrite})
	if got := status("GET", "/hello"); got != 200 {
		t.Fatalf("restricted GET = %d, want 200", got)
	}
	if got := status("POST", "/hello"); got != http.StatusForbidden {
		t.Fatalf("restricted POST = %d, want 403", got)
	}
	apply(respond.Action{Kind: respond.ActIsolate})
	if got := status("GET", "/hello"); got != http.StatusServiceUnavailable {
		t.Fatalf("isolated GET = %d, want 503", got)
	}
	if got := status("GET", "/livez"); got != 200 {
		t.Fatalf("isolated /livez = %d, want 200", got)
	}
	apply(respond.Action{Kind: respond.ActRelease})
	apply(respond.Action{Kind: respond.ActRestore, Caps: respond.CapWrite})
	if got := status("GET", "/hello"); got != 200 {
		t.Fatalf("released GET = %d, want 200", got)
	}
}
