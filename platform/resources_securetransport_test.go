package platform

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRegistrationGrantsAreSingleUseBoundAndOwnTheirStrings(t *testing.T) {
	g := newGrantStore()
	// The binding comes from a request buffer that is reused afterwards.
	buf := []byte("cookie-value-123")
	token, err := g.issue("anon:"+string(buf), string(buf), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	for i := range buf {
		buf[i] = 'x'
	}
	if !g.consume(token, "anon:cookie-value-123", "cookie-value-123") {
		t.Fatal("the grant must keep its own copy of what it was bound to")
	}
	if g.consume(token, "anon:cookie-value-123", "cookie-value-123") {
		t.Fatal("a grant works once")
	}
	for name, try := range map[string]func(string) bool{
		"another principal": func(tok string) bool { return g.consume(tok, "anon:other", "b") },
		"another binding":   func(tok string) bool { return g.consume(tok, "anon:a", "other") },
		"garbage":           func(string) bool { return g.consume("not-a-token", "anon:a", "b") },
	} {
		tok, _ := g.issue("anon:a", "b", time.Minute)
		if try(tok) {
			t.Errorf("%s must be refused", name)
		}
		if name != "garbage" && g.consume(tok, "anon:a", "b") {
			t.Errorf("%s: a refused presentation must burn the grant", name)
		}
	}
	short, _ := g.issue("anon:a", "b", -time.Second)
	if g.consume(short, "anon:a", "b") {
		t.Fatal("an expired grant must be refused")
	}
}

func TestOnlyWritesToSignInPathsAreProtected(t *testing.T) {
	s := &SecureTransport{protect: []string{"/ui/", "/login", "/register"}}
	for _, c := range []struct {
		method, path string
		want         bool
	}{
		{"GET", "/ui/me", true}, {"POST", "/ui/etl/sources/x/batches", true}, {"DELETE", "/ui/access/keys/k", true},
		{"POST", "/login", true}, {"POST", "/register", true},
		{"GET", "/login", false}, {"GET", "/register", false}, {"HEAD", "/login", false},
		{"GET", "/static/js/core.js", false}, {"GET", "/healthz", false}, {"POST", "/v1/sources/x/batches", false}, {"GET", "/metrics", false},
	} {
		if got := s.isProtected(c.method, c.path); got != c.want {
			t.Errorf("%s %s protected = %v, want %v", c.method, c.path, got, c.want)
		}
	}
}

func TestPrincipalTagsDifferPerPrincipalAndRevealNothing(t *testing.T) {
	a, b := principalTag("user:alice"), principalTag("user:bob")
	if a == b || a != principalTag("user:alice") || strings.Contains(a, "alice") {
		t.Fatalf("tags %q %q", a, b)
	}
}

const secureApp = `
name "secure"
resource "sessions" {
  kind "session.memory"
  config { secret env.required("SEC_SECRET") cookie "sc" max_age 1h }
}
resource "secure" {
  kind "transport.secure"
  config {
    session "sessions"
    origins ["http://127.0.0.1:8080"]
    server_key_file env.required("SEC_KEY")
    signing_key_file env.required("SEC_SIGN")
    create_key_file true
    wasm_dir env.required("SEC_WASM")
  }
}
intent "hello" { response "r" node "r" { uses "constant" kind pure provides [r] config { value { hello "world" } } } }
route "api" { method GET path "/ui/hello" intent "hello" allow_anonymous true }
route "write" { method POST path "/login" intent "hello" allow_anonymous true }
route "page" { method GET path "/login" intent "hello" allow_anonymous true }
route "open" { method GET path "/plain" intent "hello" allow_anonymous true }
`

func TestSecureTransportRefusesPlainRequestsToProtectedRoutes(t *testing.T) {
	dir := t.TempDir()
	wasm := filepath.Join(dir, "wasm")
	if err := os.MkdirAll(wasm, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := `{"protocol":"fh-secure-transport-v1","assets":{"securefetch.wasm":{"integrity":"sha256-AAAA"},"wasm_exec.js":{"integrity":"sha256-BBBB"}}}`
	if err := os.WriteFile(filepath.Join(wasm, "asset-manifest.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "app.bcl")
	if err := os.WriteFile(path, []byte(secureApp), 0o600); err != nil {
		t.Fatal(err)
	}
	h := newAppHarness(t, path, map[string]string{
		"SEC_SECRET": strings.Repeat("s", 48), "SEC_KEY": filepath.Join(dir, "k", "transport.key"),
		"SEC_SIGN": filepath.Join(dir, "k", "signing.key"), "SEC_WASM": wasm,
	})
	for _, f := range []string{"transport.key", "signing.key"} {
		if info, err := os.Stat(filepath.Join(dir, "k", f)); err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("%s should be created with mode 0600: %v %v", f, info, err)
		}
	}
	get := func(method, p string, headers map[string]string) (int, string, http.Header) {
		req, _ := http.NewRequest(method, h.base+p, strings.NewReader("{}"))
		req.Host = "127.0.0.1:8080"
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b), resp.Header
	}
	if status, _, _ := get("GET", "/ui/hello", nil); status != 426 {
		t.Fatalf("a plain request to a protected route: %d", status)
	}
	if status, _, _ := get("POST", "/login", nil); status != 426 {
		t.Fatalf("a plain write to a sign-in path: %d", status)
	}
	if status, _, _ := get("GET", "/login", nil); status != 200 {
		t.Fatalf("the sign-in page itself is plain: %d", status)
	}
	if status, _, _ := get("GET", "/plain", nil); status != 200 {
		t.Fatalf("an unprotected route: %d", status)
	}
	status, body, header := get("GET", "/secure-config.json", nil)
	if status != 200 || !strings.Contains(header.Get("Set-Cookie"), "etl_boot=") || !strings.Contains(header.Get("Set-Cookie"), "HttpOnly") ||
		!strings.Contains(header.Get("Set-Cookie"), "SameSite=Strict") || header.Get("Cache-Control") != "no-store" {
		t.Fatalf("bootstrap: %d %v", status, header)
	}
	var cfg map[string]any
	if err := json.Unmarshal([]byte(body), &cfg); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"pinnedServerKey", "pinnedServerKeyID", "registrationToken", "responseSigningPublicKey", "wasmIntegrity", "wasmExecIntegrity", "principalTag"} {
		if cfg[key] == nil || cfg[key] == "" {
			t.Errorf("bootstrap lacks %s: %v", key, cfg)
		}
	}
	if cfg["wasmIntegrity"] != "sha256-AAAA" || cfg["requireResponseSignature"] != true || cfg["authenticated"] != false {
		t.Errorf("bootstrap content: %v", cfg)
	}
	if strings.Contains(body, "PrivateKey") || strings.Contains(body, "signing.key") {
		t.Fatal("the bootstrap must never carry private material")
	}
	req, _ := http.NewRequest("GET", h.base+"/secure-config.json", nil)
	req.Host = "evil.example"
	resp, _ := http.DefaultClient.Do(req)
	if resp.StatusCode != 421 {
		t.Fatalf("an unknown host must not get a grant: %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestSecureTransportNeedsAKeyAndTheBuiltClient(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.bcl")
	app := strings.Replace(secureApp, "create_key_file true", "create_key_file false", 1)
	if err := os.WriteFile(path, []byte(app), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SEC_SECRET", strings.Repeat("s", 48))
	t.Setenv("SEC_KEY", filepath.Join(dir, "missing.key"))
	t.Setenv("SEC_SIGN", filepath.Join(dir, "missing-sign.key"))
	t.Setenv("SEC_WASM", filepath.Join(dir, "nowhere"))
	src, _ := os.ReadFile(path)
	if _, err := Compile(t.Context(), src, dir, DefaultLoadOptions()); err == nil || !strings.Contains(err.Error(), "server_key_file") {
		t.Fatalf("a missing key must stop the deployment: %v", err)
	}
}
