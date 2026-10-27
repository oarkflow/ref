package platform

import (
	"encoding/base32"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestProcessGateExampleRegistrationAndCaseAPI(t *testing.T) {
	files, err := filepath.Glob("../examples/processgate-pipelines/config/*.bcl")
	if err != nil || len(files) < 100 {
		t.Fatalf("generated pipeline configuration missing: %v (%d files)", err, len(files))
	}
	sort.Strings(files)
	var parts []string
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		parts = append(parts, string(data))
	}
	root := t.TempDir()
	app := filepath.Join(root, "app.bcl")
	if err := os.WriteFile(app, []byte(strings.Join(parts, "\n")), 0600); err != nil {
		t.Fatal(err)
	}
	h := newAppHarness(t, app, map[string]string{
		"PROCESSGATE_PIPELINES_DSN":            "file:" + filepath.Join(root, "cases.db") + "?_pragma=busy_timeout(5000)",
		"PROCESSGATE_PIPELINES_FILES":          filepath.Join(root, "files"),
		"PROCESSGATE_PIPELINES_STATIC":         mustAbs(t, "../examples/processgate-pipelines/static"),
		"PROCESSGATE_PIPELINES_SIGNING_SECRET": "test-signing-secret-2026-long-enough",
		"PROCESSGATE_PIPELINES_JWT_SECRET":     "test-jwt-secret-2026-long-enough",
		"PROCESSGATE_PIPELINES_ADMIN_EMAIL":    "admin@example.test",
		"PROCESSGATE_PIPELINES_ADMIN_PASSWORD": "test-admin-password-long-enough",
	})
	email, password := "member@example.test", "test-member-password-long-enough"
	status, registration := h.call("POST", "/api/auth/register", "", map[string]any{"email": email, "password": password, "name": "Member"})
	if status != 200 {
		t.Fatalf("register: %d %v", status, registration)
	}
	uri, err := url.Parse(Stringify(dig(registration, "uri")))
	if err != nil {
		t.Fatal(err)
	}
	secret := uri.Query().Get("secret")
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secret)
	if err != nil {
		t.Fatal(err)
	}
	code := totpCode(key, time.Now().Unix()/30, 6)
	if status, _ := h.call("POST", "/api/auth/login", "", map[string]any{"email": email, "password": password, "code": code}); status == 200 {
		t.Fatal("pending member signed in")
	}
	if status, body := h.call("POST", "/api/auth/register/confirm", "", map[string]any{"email": email, "password": password, "code": code}); status != 200 {
		t.Fatalf("confirm: %d %v", status, body)
	}
	status, login := h.call("POST", "/api/auth/login", "", map[string]any{"email": email, "password": password, "code": code})
	token := Stringify(dig(login, "token"))
	if status != 200 || token == "" {
		t.Fatalf("login: %d %v", status, login)
	}
	status, description := h.call("GET", "/api/pipelines/forest_encroachment_restoration_action", token, nil)
	if status != 200 || dig(description, "name") != "forest_encroachment_restoration_action" {
		t.Fatalf("describe: %d %v", status, description)
	}
	status, started := h.call("POST", "/api/pipelines/forest_encroachment_restoration_action/cases", token, map[string]any{})
	if status != 201 || dig(started, "case", "id") == nil {
		t.Fatalf("start: %d %v", status, started)
	}
	caseID := Stringify(dig(started, "case", "id"))
	if status, _ := h.call("GET", "/api/pipelines/restaurant_table_reservation/cases/"+caseID, token, nil); status != 404 {
		t.Fatalf("case leaked across pipeline paths: %d", status)
	}
	status, messages := h.call("GET", "/api/notifications", token, nil)
	if status != 200 {
		t.Fatalf("in-app notifications: %d %v", status, messages)
	}
	if status, body := h.call("POST", "/api/pipelines/restaurant_table_reservation/cases", token, map[string]any{}); status != 201 {
		t.Fatalf("notification source start: %d %v", status, body)
	}
	var delivered bool
	for range 40 {
		status, messages = h.call("GET", "/api/notifications", token, nil)
		if status == 200 && len(messages.([]any)) > 0 {
			delivered = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !delivered {
		t.Fatal("case.started notification was not delivered to the in-app inbox")
	}
	status, payable := h.call("POST", "/api/pipelines/public_dog_registration/cases", token, map[string]any{})
	if status != 201 {
		t.Fatalf("paid flow start: %d %v", status, payable)
	}
	paidCaseID := Stringify(dig(payable, "case", "id"))
	if status, _ := h.call("POST", "/api/pipelines/public_dog_registration/cases/"+paidCaseID+"/payments/verify", token, map[string]any{"reference": "receipt-1", "amount": 100, "currency": "NPR"}); status == 200 {
		t.Fatal("applicant verified their own payment")
	}
	status, adminLogin := h.call("POST", "/api/auth/login", "", map[string]any{"email": "admin@example.test", "password": "test-admin-password-long-enough"})
	if status != 200 {
		t.Fatalf("admin login: %d %v", status, adminLogin)
	}
	adminToken := Stringify(dig(adminLogin, "token"))
	status, verified := h.call("POST", "/api/pipelines/public_dog_registration/cases/"+paidCaseID+"/payments/verify", adminToken, map[string]any{"reference": "receipt-1", "amount": 100, "currency": "NPR"})
	if status != 200 || dig(verified, "payment", "reference") != "receipt-1" {
		t.Fatalf("payment verification: %d %v", status, verified)
	}
	if status, _ := h.call("POST", "/api/pipelines/forest_encroachment_restoration_action/cases", "", map[string]any{}); status != 403 {
		t.Fatalf("registered-only pipeline allowed anonymous start: %d", status)
	}
	status, anonymous := h.call("POST", "/api/pipelines/restaurant_table_reservation/cases", "", map[string]any{})
	if status != 201 || dig(anonymous, "access_key") == nil {
		t.Fatalf("anonymous pipeline start: %d %v", status, anonymous)
	}
}

func mustAbs(t *testing.T, path string) string {
	t.Helper()
	abs, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	return abs
}
