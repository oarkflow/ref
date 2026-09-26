package platform

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

const residencyApp = `
name "residency"

resource "db_eu" {
  kind "database.sql"
  region "eu-west-1"
  config {
    driver "sqlite"
    dsn env.required("RES_EU_DSN")
    migrations ["CREATE TABLE IF NOT EXISTS notes (body TEXT)"]
  }
}

resource "db_us" {
  kind "database.sql"
  region "us-east-1"
  config {
    driver "sqlite"
    dsn env.required("RES_US_DSN")
    migrations ["CREATE TABLE IF NOT EXISTS notes (body TEXT)"]
  }
}

resource "cases" {
  kind "pipeline.cases"
  config { database "db_eu" }
}

resource "crm_eu" {
  kind "service.http"
  region "eu-west-1"
  config {
    base_url env.required("RES_UPSTREAM")
    allowed_hosts ["127.0.0.1"]
    allow_private_networks true
  }
}

resource "crm_us" {
  kind "service.http"
  region "us-east-1"
  config {
    base_url env.required("RES_UPSTREAM")
    allowed_hosts ["127.0.0.1"]
    allow_private_networks true
  }
}

resource "jwt" {
  kind "auth.jwt"
  config { secret env.required("RES_JWT_SECRET") }
}

tenant "acme" {
  region "eu"
}

tenant "tiny" {
  region "ap"
}

residency {
  claim "data_region"
  audit "db_eu"
  zone "eu" {
    regions ["eu-west-1", "eu-central-1"]
  }
  zone "us" {
    regions ["us-*"]
  }
  zone "ap" {
    regions ["ap-south-1"]
    hosts ["api.ap.example.com"]
  }
  policy "customer-pii" {
    regions ["eu"]
    entities ["customer"]
    pipelines ["kyc"]
    intents ["crm.sync"]
    hosts ["127.0.0.1"]
  }
}

pipeline "kyc" {
  stage "apply" {
    kind form
    input "name" { kind text }
  }
}

entity "customer" {
  database "db_eu"
  auth "jwt"
  column "name" { kind text  required true }
}

intent "note.eu" {
  response "ok"
  node "ok" {
    uses "database.exec"
    resource "db_eu"
    requires [input]
    provides [ok]
    config {
      statement "INSERT INTO notes (body) VALUES ($1)"
      args ["input.body"]
    }
  }
}

intent "note.us" {
  response "ok"
  node "ok" {
    uses "database.exec"
    resource "db_us"
    requires [input]
    provides [ok]
    config {
      statement "INSERT INTO notes (body) VALUES ($1)"
      args ["input.body"]
    }
  }
}

intent "note.us.read" {
  response "rows"
  node "rows" {
    uses "database.query"
    resource "db_us"
    provides [rows]
    config { statement "SELECT body FROM notes" }
  }
}

intent "crm.sync" {
  response "reply"
  node "reply" {
    uses "service.http"
    resource "crm_eu"
    requires [input]
    provides [reply]
    config {
      method POST
      url "/sync"
      body_fact "input"
    }
  }
}

intent "crm.us" {
  response "reply"
  node "reply" {
    uses "service.http"
    resource "crm_us"
    requires [input]
    provides [reply]
    config {
      method POST
      url "/sync"
      body_fact "input"
    }
  }
}

intent "audit.list" {
  response "rows"
  node "rows" {
    uses "database.query"
    resource "db_eu"
    provides [rows]
    config { statement "SELECT tenant_id, action, outcome, detail FROM platform_audit ORDER BY sequence" }
  }
}

route "note.eu"      { method POST path "/notes/eu" intent "note.eu" auth "jwt" }
route "note.us"      { method POST path "/notes/us" intent "note.us" auth "jwt" }
route "note.us.read" { method GET  path "/notes/us" intent "note.us.read" auth "jwt" }
route "crm.sync"     { method POST path "/crm/eu"   intent "crm.sync" auth "jwt" }
route "crm.us"       { method POST path "/crm/us"   intent "crm.us" auth "jwt" }
route "audit"        { method GET  path "/audit"    intent "audit.list" auth "jwt" }
`

func TestResidencyEndToEnd(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	dir := t.TempDir()
	h := newAppHarness(t, writeApp(t, residencyApp), map[string]string{
		"RES_EU_DSN":     "file:" + filepath.Join(dir, "eu.db"),
		"RES_US_DSN":     "file:" + filepath.Join(dir, "us.db"),
		"RES_UPSTREAM":   upstream.URL,
		"RES_JWT_SECRET": "residency-test-secret-0123456789abcdef",
	})
	acme := h.token("jwt", "u1", []string{"staff"}, map[string]any{"tenant_id": "acme"})
	// globex has no tenant block: its home comes from the data_region claim.
	globex := h.token("jwt", "u2", []string{"staff"}, map[string]any{"tenant_id": "globex", "data_region": "us"})
	// A tenant with no home region is not constrained.
	free := h.token("jwt", "u3", []string{"staff"}, map[string]any{"tenant_id": "free"})
	// tiny is homed in ap-south-1, whose zone also limits outbound hosts.
	tiny := h.token("jwt", "u4", []string{"staff"}, map[string]any{"tenant_id": "tiny"})
	// A tenant block's region wins over a claim.
	spoof := h.token("jwt", "u5", []string{"staff"}, map[string]any{"tenant_id": "acme", "data_region": "us"})

	note := map[string]any{"body": "hello"}
	expect := func(name, method, path, token string, body any, want int) any {
		t.Helper()
		status, resp := h.call(method, path, token, body)
		if status != want {
			t.Fatalf("%s: %d %v, want %d", name, status, resp, want)
		}
		return resp
	}

	expect("acme writes in eu", "POST", "/notes/eu", acme, note, 200)
	resp := expect("acme writes in us", "POST", "/notes/us", acme, note, 403)
	if dig(resp, "error", "code") != "PERMISSION_DENIED" || !strings.Contains(strings.ToLower(Stringify(resp)), "residency") {
		t.Fatalf("refusal body: %v", resp)
	}
	expect("acme may still read us", "GET", "/notes/us", acme, nil, 200)
	expect("claim cannot override the tenant block", "POST", "/notes/us", spoof, note, 403)

	expect("globex writes in us", "POST", "/notes/us", globex, note, 200)
	expect("globex writes in eu", "POST", "/notes/eu", globex, note, 403)
	expect("globex creates an eu customer", "POST", "/api/customers", globex, map[string]any{"name": "Bob"}, 403)
	expect("acme creates an eu customer", "POST", "/api/customers", acme, map[string]any{"name": "Ann"}, 201)

	expect("unconstrained tenant writes anywhere", "POST", "/notes/us", free, note, 200)
	expect("unconstrained tenant writes anywhere", "POST", "/notes/eu", free, note, 200)

	before := calls.Load()
	expect("acme calls the eu crm", "POST", "/crm/eu", acme, note, 200)
	expect("acme calls the us crm", "POST", "/crm/us", acme, note, 403)
	expect("globex calls the eu crm", "POST", "/crm/eu", globex, note, 403)
	expect("globex calls the us crm", "POST", "/crm/us", globex, note, 200)
	if got := calls.Load() - before; got != 2 {
		t.Fatalf("upstream saw %d calls, want 2 — a refused call must never leave", got)
	}
	// tiny's zone allows only api.ap.example.com as an outbound host, so the
	// call is refused before it is sent even though the region check does not
	// apply (crm_us is not in ap-south-1 either — both refuse).
	expect("tiny's egress host", "POST", "/crm/us", tiny, note, 403)
	if got := calls.Load() - before; got != 2 {
		t.Fatalf("upstream saw %d calls after tiny", got)
	}

	resp = expect("audit", "GET", "/audit", acme, nil, 200)
	rows, _ := resp.([]any)
	denied := 0
	for _, row := range rows {
		if dig(row, "action") == "residency.denied" && dig(row, "outcome") == "denied" {
			denied++
		}
	}
	if denied < 6 {
		t.Fatalf("audit rows: %v", rows)
	}
}

// TestResidencyEgressHosts checks the host allowlist on its own: a service in
// the tenant's own region, reached through a host the zone does not list.
func TestResidencyEgressHosts(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer upstream.Close()
	app := `
name "egress"
resource "svc" {
  kind "service.http"
  region "ap-south-1"
  config {
    base_url env.required("EG_UPSTREAM")
    allowed_hosts ["127.0.0.1"]
    allow_private_networks true
  }
}
resource "jwt" {
  kind "auth.jwt"
  config { secret env.required("EG_JWT_SECRET") }
}
residency {
  claim "home"
  zone "ap" {
    regions ["ap-south-1"]
    hosts ["api.ap.example.com"]
  }
  zone "ap-open" {
    regions ["ap-south-1"]
    hosts ["127.0.0.1"]
  }
}
intent "call" {
  response "reply"
  node "reply" {
    uses "service.http"
    resource "svc"
    requires [input]
    provides [reply]
    config {
      method POST
      url "/x"
      body_fact "input"
    }
  }
}
route "call" { method POST path "/call" intent "call" auth "jwt" }
`
	h := newAppHarness(t, writeApp(t, app), map[string]string{
		"EG_UPSTREAM": upstream.URL, "EG_JWT_SECRET": "egress-test-secret-0123456789abcdef-x",
	})
	closed := h.token("jwt", "u1", nil, map[string]any{"tenant_id": "t1", "home": "ap"})
	open := h.token("jwt", "u2", nil, map[string]any{"tenant_id": "t2", "home": "ap-open"})
	if status, body := h.call("POST", "/call", closed, map[string]any{}); status != 403 || !strings.Contains(Stringify(body), "outbound host") {
		t.Fatalf("closed zone: %d %v", status, body)
	}
	if status, body := h.call("POST", "/call", open, map[string]any{}); status != 200 {
		t.Fatalf("open zone: %d %v", status, body)
	}
}

func TestResidencyCompileChecks(t *testing.T) {
	base := `
name "r"
resource "db_eu" {
  kind "database.sql"
  region "eu-west-1"
  config { driver "sqlite" dsn "file::memory:" }
}
resource "db_us" {
  kind "database.sql"
  region "us-east-1"
  config { driver "sqlite" dsn "file::memory:" }
}
resource "db_none" {
  kind "database.sql"
  config { driver "sqlite" dsn "file::memory:" }
}
resource "api" {
  kind "service.http"
  region "eu-west-1"
  config {
    base_url "https://api.example.com"
    allowed_hosts ["api.example.com", "evil.example.org"]
  }
}
entity "customer" {
  database "db_us"
  column "name" { kind text }
}
entity "draft" {
  database "db_none"
  column "name" { kind text }
}
pipeline "kyc" {
  stage "apply" {
    kind form
    input "name" { kind text }
  }
}
intent "sync" {
  response "r"
  node "r" {
    uses "service.http"
    resource "api"
    requires [input]
    provides [r]
    config { url "/x" }
  }
}
`
	cases := map[string]string{
		`residency {
  policy "pii" {
    regions ["eu-*"]
    entities ["customer"]
  }
}`: `entity "customer", which is bound to resource "db_us" in region "us-east-1"`,
		`residency {
  zone "eu" { regions ["eu-west-1"] }
  policy "pii" {
    regions ["eu"]
    entities ["draft"]
  }
}`: `declares no region`,
		`resource "cases" {
  kind "pipeline.cases"
  config { database "db_us" }
}
residency {
  policy "pii" {
    regions ["eu-west-1"]
    pipelines ["kyc"]
  }
}`: `pipeline "kyc", which is bound to resource "cases" in region "us-east-1"`,
		`residency {
  policy "pii" {
    regions ["eu-west-1"]
    pipelines ["kyc"]
  }
}`: `no pipeline.cases resource runs it`,
		`residency {
  policy "pii" {
    regions ["eu-west-1"]
    intents ["sync"]
    hosts ["*.example.com"]
  }
}`: `may call host "evil.example.org"`,
		`residency {
  policy "pii" {
    regions ["us-east-1"]
    resources ["db_eu"]
  }
}`: `resource "db_eu", which is bound to resource "db_eu" in region "eu-west-1"`,
		`residency {
  policy "pii" {
    regions ["eu-west-1"]
    entities ["nope"]
  }
}`: `undeclared entity "nope"`,
		`residency {
  policy "pii" { entities ["customer"] }
}`: `needs regions`,
		`residency {
  zone "eu" { }
}`: `needs regions`,
		`residency {
  audit "nope"
}`: `undeclared resource "nope"`,
		`tenant "acme" {
  region "eu"
}`: `no residency block`,
	}
	for extra, want := range cases {
		src := []byte(base + extra)
		_, err := Compile(context.Background(), src, t.TempDir(), DefaultLoadOptions())
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s\n→ %v\nwant %q", extra, err, want)
		}
		report := Validate(context.Background(), src, t.TempDir(), DefaultLoadOptions())
		if report.Valid || !strings.Contains(strings.Join(report.Errors, "\n"), want) {
			t.Fatalf("Validate(%s) = %v", extra, report.Errors)
		}
	}

	// A compliant document compiles: region inheritance through config.database
	// puts the pipeline in eu-west-1.
	ok := strings.Replace(base, `database "db_us"`, `database "db_eu"`, 1) + `
resource "cases" {
  kind "pipeline.cases"
  config { database "db_eu" }
}
residency {
  zone "eu" { regions ["eu-west-1", "eu-central-1"] }
  policy "pii" {
    regions ["eu"]
    entities ["customer"]
    pipelines ["kyc"]
    resources ["cases"]
  }
}`
	p, err := Compile(context.Background(), []byte(ok), t.TempDir(), DefaultLoadOptions())
	if err != nil {
		t.Fatalf("compliant document: %v", err)
	}
	_ = p.Close()
	if report := Validate(context.Background(), []byte(ok), t.TempDir(), DefaultLoadOptions()); !report.Valid {
		t.Fatalf("Validate compliant: %v", report.Errors)
	}
}

func TestResidencyMatching(t *testing.T) {
	for _, c := range []struct {
		region  string
		allowed []string
		want    bool
	}{
		{"eu-west-1", []string{"eu-west-1"}, true},
		{"EU-West-1", []string{"eu-*"}, true},
		{"us-east-1", []string{"eu-*"}, false},
		{"eu-west-1", nil, false},
	} {
		if got := regionMatch(c.region, c.allowed); got != c.want {
			t.Fatalf("regionMatch(%q, %v) = %v", c.region, c.allowed, got)
		}
	}
	for _, c := range []struct {
		host    string
		allowed []string
		want    bool
	}{
		{"api.eu.example.com", []string{"*.eu.example.com"}, true},
		{"eu.example.com", []string{"*.eu.example.com"}, false},
		{"evil.com", []string{"*.eu.example.com"}, false},
		{"API.example.com.", []string{"api.example.com"}, true},
		{"x", []string{"*"}, true},
	} {
		if got := hostMatch(c.host, c.allowed); got != c.want {
			t.Fatalf("hostMatch(%q, %v) = %v", c.host, c.allowed, got)
		}
	}
	regions := effectiveRegions([]ResourceSpec{
		{Name: "a", Region: "EU-West-1"},
		{Name: "b", Config: map[string]any{"database": "a"}},
		{Name: "c", Config: map[string]any{"store": "b"}},
		{Name: "d", Config: map[string]any{"database": "e"}},
		{Name: "e", Config: map[string]any{"database": "d"}},
	})
	if regions["b"] != "eu-west-1" || regions["c"] != "eu-west-1" || regions["d"] != "" {
		t.Fatalf("inheritance: %v", regions)
	}
}

func TestResidencyGuard(t *testing.T) {
	doc := Document{
		Resources: []ResourceSpec{
			{Name: "eu", Kind: "database.sql", Region: "eu-west-1"},
			{Name: "plain", Kind: "database.sql"},
			{Name: "cache", Kind: "cache.memory"},
		},
		Tenants:   []TenantSpec{{Name: "acme", Region: "eu-west-1"}},
		Residency: &ResidencySpec{RequireRegion: true},
	}
	plan, err := compileResidency(doc, nil)
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry()
	write := func(resource string) *residencyGuard {
		return plan.guardFor("i", NodeSpec{Name: "n", Uses: "database.exec", Resource: resource}, registry)
	}
	ctx := func(tenant string) *ActionContext {
		return &ActionContext{Context: context.Background(), TenantID: tenant}
	}
	if _, err := write("eu").check(ctx("acme")); err != nil {
		t.Fatalf("home region: %v", err)
	}
	if _, err := write("plain").check(ctx("acme")); err == nil || !strings.Contains(err.Error(), "declared region") {
		t.Fatalf("require_region: %v", err)
	}
	if _, err := write("plain").check(ctx("other")); err != nil {
		t.Fatalf("unbound tenant: %v", err)
	}
	if g := plan.guardFor("i", NodeSpec{Name: "n", Uses: "database.query", Resource: "eu"}, registry); g != nil {
		t.Fatal("a read needs no guard")
	}
	if g := plan.guardFor("i", NodeSpec{Name: "n", Uses: "expression"}, registry); g != nil {
		t.Fatal("a node without a resource needs no guard")
	}
	var none *residencyPlan
	if none.guardFor("i", NodeSpec{Resource: "eu"}, registry) != nil {
		t.Fatal("no residency block, no guard")
	}
}

// TestResidencyDurableHook: a durable entity hook runs later, from the
// outbox, with no request identity. It must still be held to the residency of
// the tenant whose change produced it — before, the missing tenant made the
// hook unconstrained, so an eu tenant's record could be sent to a us service.
func TestResidencyDurableHook(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	src := strings.Replace(residencyApp, `column "name" { kind text  required true }`,
		`column "name" { kind text  required true }
  on "created" {
    hook "crm.us"
    durable true
    max_attempts 1
  }`, 1)
	dir := t.TempDir()
	h := newAppHarness(t, writeApp(t, src), map[string]string{
		"RES_EU_DSN":     "file:" + filepath.Join(dir, "eu.db") + "?_pragma=busy_timeout(5000)",
		"RES_US_DSN":     "file:" + filepath.Join(dir, "us.db"),
		"RES_UPSTREAM":   upstream.URL,
		"RES_JWT_SECRET": "residency-test-secret-0123456789abcdef",
	})
	acme := h.token("jwt", "u1", []string{"staff"}, map[string]any{"tenant_id": "acme"})
	free := h.token("jwt", "u3", []string{"staff"}, map[string]any{"tenant_id": "free"})

	// An unconstrained tenant's hook reaches the us service.
	if status, body := h.call("POST", "/api/customers", free, map[string]any{"name": "Fay"}); status != 201 {
		t.Fatalf("free create: %d %v", status, body)
	}
	waitFor(t, "the free tenant's hook", func() bool { return calls.Load() == 1 })

	// acme is homed in eu: its hook must be refused, and never leave.
	if status, body := h.call("POST", "/api/customers", acme, map[string]any{"name": "Ann"}); status != 201 {
		t.Fatalf("acme create: %d %v", status, body)
	}
	waitFor(t, "the acme hook refusal", func() bool {
		_, resp := h.call("GET", "/audit", acme, nil)
		rows, _ := resp.([]any)
		for _, row := range rows {
			if dig(row, "tenant_id") == "acme" && dig(row, "action") == "residency.denied" &&
				strings.Contains(Stringify(dig(row, "detail")), "crm_us") {
				return true
			}
		}
		return false
	})
	if got := calls.Load(); got != 1 {
		t.Fatalf("upstream saw %d calls, want 1 — acme's hook left the region", got)
	}
}
