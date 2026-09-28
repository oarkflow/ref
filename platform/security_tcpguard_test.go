package platform

import (
	"context"
	"net"
	"net/http"
	"strings"
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

const testAbusePolicy = `pack "abuse-test" {
  version "1.0.0"
  mode enforce
}

guard "abuse-test" {
  mode enforce
  version "1.0.0"
}

detector "abuse" {
  type abuse
  window 10m
  auth_ip_failure_threshold 2
}

rule "brute-force-ban" {
  status active
  priority 100

  trigger {
    on auth.login_failed
  }

  when {
    abuse.auth.ip_failures greater_or_equal 2
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
      run ban_ip
    }
  }
}`

const testAbuseDoc = `
name "guard-abuse-test"
environment "test"

resource "guard" {
  kind "security.tcpguard"
  config {
    source ` + "`" + testAbusePolicy + "`" + `
    mode "enforce"
  }
}

shape "login_input" { kind object prop "password" { kind string } }

intent "login" {
  response "response"
  node "validate" { uses "validate.schema" requires [input] provides [validated] config { source_fact "input" shape "login_input" } }
  node "check" {
    uses "validate.expression"
    requires [input, validated]
    provides [ok]
    config { expression "input.password == 'correct'" message "bad credentials" }
  }
  node "response" { uses "collect" requires [ok] provides [response] }
}

intent "ping" {
  response "response"
  node "ok" { uses "collect" requires [input] provides [response] config { unwrap true } }
}

route "login" {
  method POST
  path "/login"
  intent "login"
  allow_anonymous true
  security_event { on_success "auth.login_success" on_failure "auth.login_failed" }
}
route "open" { method GET path "/open" intent "ping" allow_anonymous true }
`

// TestSecurityEventFeedsAbuseDetectorAcrossRequests proves SecurityEventSpec
// (routes.go's serve) and security.report_event both actually reach
// Guard.Evaluate, not just that they compile: two failed /login attempts —
// each reported as "auth.login_failed" only after that request's own
// response is already decided (this is instrumentation, not a gate) — feed
// oarkflow/tcpguard's built-in abuse detector's per-IP counter, and once
// the rule's "ban_ip" action fires on the second one, a completely
// unrelated *later* request (GET /open, evaluated the normal way as
// "request.received") is blocked — proving the guard's own stateful ban
// applies deployment-wide, not just to the triggering request.
func TestSecurityEventFeedsAbuseDetectorAcrossRequests(t *testing.T) {
	ctx := context.Background()
	p, err := Compile(ctx, []byte(testAbuseDoc), t.TempDir(), DefaultLoadOptions())
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	defer p.Close()

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

	failLogin := func() int {
		resp, err := client.Post(base+"/login", "application/json", strings.NewReader(`{"password":"wrong"}`))
		if err != nil {
			t.Fatalf("POST /login: %v", err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}
	getOpen := func() int {
		resp, err := client.Get(base + "/open")
		if err != nil {
			t.Fatalf("GET /open: %v", err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}

	if status := getOpen(); status != http.StatusOK {
		t.Fatalf("GET /open before any failures = %d, want 200", status)
	}
	if status := failLogin(); status != http.StatusUnprocessableEntity {
		t.Fatalf("first failed login = %d, want 422", status)
	}
	if status := getOpen(); status != http.StatusOK {
		t.Fatalf("GET /open after 1 failure = %d, want 200 (below the threshold of 2)", status)
	}
	if status := failLogin(); status != http.StatusUnprocessableEntity {
		t.Fatalf("second failed login = %d, want 422 (its own response is unaffected by the ban it triggers)", status)
	}
	if status := getOpen(); status != http.StatusForbidden {
		t.Fatalf("GET /open after 2 failures = %d, want 403 (the ban_ip action should have fired)", status)
	}
}

const testIdentityPolicy = `pack "identity-test" {
  version "1.0.0"
  mode enforce
}

guard "identity-test" {
  mode enforce
  version "1.0.0"
}

detector "abuse" {
  type abuse
  window 10m
  auth_ip_failure_threshold 1000
  auth_user_failure_threshold 2
}

rule "account-lock" {
  status active
  priority 100

  trigger {
    on auth.login_failed
  }

  when {
    abuse.auth.user_failures greater_or_equal 2
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
      run lock_user
    }
  }
}`

const testIdentityDoc = `
name "guard-identity-test"
environment "test"

resource "guard" {
  kind "security.tcpguard"
  config {
    source ` + "`" + testIdentityPolicy + "`" + `
    mode "enforce"
    identity_fields ["email"]
  }
}

shape "login_input" { kind object prop "email" { kind string } prop "password" { kind string } }

intent "login" {
  response "response"
  node "validate" { uses "validate.schema" requires [input] provides [validated] config { source_fact "input" shape "login_input" } }
  node "check" {
    uses "validate.expression"
    requires [input, validated]
    provides [ok]
    config { expression "input.password == 'correct'" message "bad credentials" }
  }
  node "response" { uses "collect" requires [ok] provides [response] }
}

route "login" {
  method POST
  path "/login"
  intent "login"
  allow_anonymous true
  security_event { on_success "auth.login_success" on_failure "auth.login_failed" }
}
`

// TestSecurityTCPGuardIdentityFieldsTracksPerAccount proves the
// "identity_fields" config (platform/resources_security.go's
// jsonIdentityExtractor) genuinely extracts an identity from the request
// body and that a rule keyed on the per-*account* signal
// (abuse.auth.user_failures, not IP) fires correctly — attacking a single
// account from what the detector otherwise treats as the same source
// would look identical either way, so this specifically drives the
// detector's user threshold far below its IP threshold to isolate that
// the per-account half of the condition (and not the per-IP half) is what
// triggered the lock.
func TestSecurityTCPGuardIdentityFieldsTracksPerAccount(t *testing.T) {
	ctx := context.Background()
	p, err := Compile(ctx, []byte(testIdentityDoc), t.TempDir(), DefaultLoadOptions())
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	defer p.Close()

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

	login := func(email, password string) int {
		body := `{"email":"` + email + `","password":"` + password + `"}`
		resp, err := client.Post(base+"/login", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatalf("POST /login: %v", err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}

	if status := login("alice@example.com", "wrong"); status != http.StatusUnprocessableEntity {
		t.Fatalf("alice's first failed login = %d, want 422", status)
	}
	// A different account, from the "same" source (one test client): the
	// IP-based counter would treat this identically to a second attempt
	// against alice's account, but the account-scoped lock must not fire
	// for bob — proving user_failures is genuinely keyed per-identity, not
	// silently falling back to per-IP.
	if status := login("bob@example.com", "wrong"); status != http.StatusUnprocessableEntity {
		t.Fatalf("bob's first failed login = %d, want 422 (unrelated account, must not be affected by alice's count)", status)
	}
	if status := login("alice@example.com", "wrong"); status != http.StatusUnprocessableEntity {
		t.Fatalf("alice's second failed login = %d, want 422 (its own response is unaffected by the lock it triggers)", status)
	}
	if status := login("alice@example.com", "correct"); status != http.StatusForbidden {
		t.Fatalf("alice's login after 2 failures = %d, want 403 (lock_user should have fired for her account)", status)
	}
	if status := login("bob@example.com", "correct"); status != http.StatusOK {
		t.Fatalf("bob's login = %d, want 200 (his account was never locked)", status)
	}
}
