package platform

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const csrfApp = sessionClaimsApp + `
resource "jwt" {
  kind "auth.jwt"
  config { secret env.required("SC_JWT_SECRET") }
}
route "act" { method POST path "/act" intent "scope" session "sessions" auth "cookie_auth" }
route "act_cors" {
  method POST
  path "/act-cors"
  intent "scope"
  session "sessions"
  auth "cookie_auth"
  cors {
    allow_origins ["https://app.example"]
    allow_credentials true
  }
}
route "act_bearer" { method POST path "/act-bearer" intent "scope" session "sessions" auth "jwt" }
`

// TestCSRFOnCookieSessions pins the CSRF defence: an unsafe request that a
// session cookie authenticated must come from the request's own origin or a
// CORS-allowed one; bearer tokens and non-browser clients are unaffected.
func TestCSRFOnCookieSessions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.bcl")
	if err := os.WriteFile(path, []byte(csrfApp), 0o600); err != nil {
		t.Fatal(err)
	}
	h := newAppHarness(t, path, map[string]string{
		"SC_DSN":        "file:" + filepath.Join(dir, "sc.db"),
		"SC_SECRET":     strings.Repeat("c", 48),
		"SC_JWT_SECRET": strings.Repeat("j", 48),
	})
	hash, err := HashPasswordArgon2id("correct horse battery", FastArgon2idParams())
	if err != nil {
		t.Fatal(err)
	}
	db, _ := h.platform.Resource("db")
	if _, err := db.(*Database).Exec(`INSERT INTO users VALUES ('u1', 'officer@example.com', ?, 'officer', 'east')`, hash); err != nil {
		t.Fatal(err)
	}
	if status, body := h.call("POST", "/login", "", map[string]any{"email": "officer@example.com", "password": "correct horse battery"}); status != 200 {
		t.Fatalf("login = %d %v", status, body)
	}
	self := h.base
	send := func(path, token string, headers map[string]string) int {
		t.Helper()
		req, err := http.NewRequest("POST", h.base+path, strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := h.client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if testing.Verbose() {
			raw, _ := io.ReadAll(resp.Body)
			t.Logf("%s: %d %s", path, resp.StatusCode, raw)
		}
		return resp.StatusCode
	}
	cases := []struct {
		name, path string
		headers    map[string]string
		want       int
	}{
		{"no browser headers", "/act", nil, 200},
		{"same origin", "/act", map[string]string{"Origin": self}, 200},
		{"same origin referer", "/act", map[string]string{"Referer": self + "/page"}, 200},
		{"sec-fetch same-origin", "/act", map[string]string{"Sec-Fetch-Site": "same-origin"}, 200},
		{"cross origin", "/act", map[string]string{"Origin": "https://evil.example"}, 403},
		{"cross origin referer", "/act", map[string]string{"Referer": "https://evil.example/x"}, 403},
		{"null origin", "/act", map[string]string{"Origin": "null"}, 403},
		{"sec-fetch cross-site", "/act", map[string]string{"Sec-Fetch-Site": "cross-site"}, 403},
		{"sec-fetch same-site", "/act", map[string]string{"Sec-Fetch-Site": "same-site", "Origin": "https://sub.example"}, 403},
		{"cors-allowed origin", "/act-cors", map[string]string{"Origin": "https://app.example", "Sec-Fetch-Site": "cross-site"}, 200},
		{"cors route, other origin", "/act-cors", map[string]string{"Origin": "https://evil.example"}, 403},
	}
	for _, tc := range cases {
		if got := send(tc.path, "", tc.headers); got != tc.want {
			t.Errorf("%s: status %d, want %d", tc.name, got, tc.want)
		}
	}
	// A bearer token is not ambient authority: a foreign Origin passes.
	token := h.token("jwt", "u1", nil, map[string]any{"org_units": "east"})
	if got := send("/act-bearer", token, map[string]string{"Origin": "https://evil.example", "Sec-Fetch-Site": "cross-site"}); got != 200 {
		t.Errorf("bearer with foreign origin: %d", got)
	}
}
