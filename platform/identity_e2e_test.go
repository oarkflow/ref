package platform

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/oarkflow/ref/pipeline"
	"github.com/oarkflow/ref/signing"
)

// The identity example end to end over HTTP: the bootstrap admin signs in,
// invites a colleague who accepts once, memberships change under last-admin
// protection, a suspended account cannot sign in, and one Ed25519 key set
// signs the tokens, arbitrary payloads and issued licences — all verifiable
// with the published JWKS.

const (
	identityAdminEmail    = "root@acme.test"
	identityAdminPassword = "correct horse battery staple"
)

func newIdentityApp(t *testing.T) *appHarness {
	key, err := signing.GenerateEd25519("identity-1")
	if err != nil {
		t.Fatal(err)
	}
	pem, _ := key.MarshalPrivateKeyPEM()
	env := map[string]string{
		"IDENTITY_DB_DRIVER":          "sqlite",
		"IDENTITY_DSN":                "file:" + t.TempDir() + "/identity.db?_pragma=busy_timeout(5000)",
		"IDENTITY_SIGNING_KEY":        string(pem),
		"IDENTITY_ADMIN_EMAIL":        identityAdminEmail,
		"IDENTITY_ADMIN_PASSWORD":     identityAdminPassword,
		"IDENTITY_ARGON2_MEMORY":      "8192",
		"IDENTITY_ARGON2_ITERATIONS":  "1",
		"IDENTITY_ARGON2_PARALLELISM": "1",
	}
	if admin := os.Getenv("TEST_POSTGRES_DSN"); admin != "" {
		env["IDENTITY_DB_DRIVER"], env["IDENTITY_DSN"] = "pgx", freshPostgres(t, admin)
	}
	return newAppHarness(t, "../examples/identity/app.bcl", env)
}

func errorCode(body any) string { return Stringify(dig(body, "error", "code")) }

func (h *appHarness) login(email, password string) (int, any, string) {
	h.t.Helper()
	status, body := h.call("POST", "/api/auth/login", "", map[string]any{"email": email, "password": password})
	return status, body, Stringify(dig(body, "token"))
}

func TestIdentityExample(t *testing.T) {
	h := newIdentityApp(t)
	dir, _ := h.platform.Resource("users")
	directory := dir.(*IdentityDirectory)

	// --- Sign-in ---
	status, body, admin := h.login(identityAdminEmail, identityAdminPassword)
	if status != 200 || admin == "" || dig(body, "tenant_id") != "acme" || fmt.Sprint(dig(body, "roles")) != "[admin]" {
		t.Fatalf("admin login: %d %v", status, body)
	}
	adminID := Stringify(dig(body, "user", "id"))
	if dig(body, "user", "password_hash") != nil {
		t.Fatal("the password hash leaked into the response")
	}
	header := decodeSegment(t, admin, 0)
	if header["alg"] != "EdDSA" || header["kid"] != "identity-1" {
		t.Fatalf("token header: %v", header)
	}
	claims := decodeSegment(t, admin, 1)
	if claims["tenant_id"] != "acme" || claims["email"] != identityAdminEmail || claims["iss"] != "identity-example" {
		t.Fatalf("token claims: %v", claims)
	}

	// Wrong password and unknown account are the same failure.
	s1, wrong, _ := h.login(identityAdminEmail, "not the password at all")
	s2, unknown, _ := h.login("nobody@acme.test", "not the password at all")
	if s1 != 401 || s2 != 401 || errorCode(wrong) != "INVALID_CREDENTIALS" || fmt.Sprint(wrong) != fmt.Sprint(unknown) {
		t.Fatalf("failures differ: %d %v / %d %v", s1, wrong, s2, unknown)
	}

	// --- The JWKS verifies the token offline ---
	status, jwks := h.call("GET", "/.well-known/jwks.json", "", nil)
	if status != 200 || dig(jwks, "keys", 0, "kty") != "OKP" || dig(jwks, "keys", 0, "kid") != "identity-1" {
		t.Fatalf("jwks: %d %v", status, jwks)
	}
	rawJWKS, _ := json.Marshal(jwks)
	jwtKeys, err := parseJWKS(rawJWKS)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := VerifyJWT(admin, jwtKeys, JWTVerifyOptions{Issuer: "identity-example", Audience: "identity-example"}); err != nil {
		t.Fatalf("token does not verify against the JWKS: %v", err)
	}

	// --- Invitations ---
	status, inv := h.call("POST", "/api/admin/invitations", admin, map[string]any{
		"email": "Alice@Acme.test", "name": "Alice", "roles": []string{"editor"}, "org_unit": "ktm"})
	if status != 201 || dig(inv, "email") != "alice@acme.test" || Stringify(dig(inv, "token")) == "" {
		t.Fatalf("invite: %d %v", status, inv)
	}
	token := Stringify(dig(inv, "token"))
	aliceID := Stringify(dig(inv, "user_id"))
	if status, body := h.call("POST", "/api/admin/invitations", admin, map[string]any{"email": "not-an-email"}); status != 422 {
		t.Fatalf("bad email: %d %v", status, body)
	}
	status, pending := h.call("GET", "/api/admin/invitations", admin, nil)
	if status != 200 || len(pending.([]any)) != 1 || dig(pending, 0, "token") != nil {
		t.Fatalf("pending invitations: %d %v", status, pending)
	}
	// An invited account cannot sign in before accepting.
	if status, _, _ := h.login("alice@acme.test", "anything-long-enough"); status != 401 {
		t.Fatalf("invited login: %d", status)
	}
	// A weak password is refused and does not consume the invitation.
	if status, body := h.call("POST", "/api/auth/invitations/accept", "", map[string]any{"token": token, "password": "short"}); status != 422 || errorCode(body) != "WEAK_PASSWORD" {
		t.Fatalf("weak password: %d %v", status, body)
	}
	alicePassword := "alice's long passphrase"
	status, member := h.call("POST", "/api/auth/invitations/accept", "", map[string]any{"token": token, "password": alicePassword})
	if status != 200 || dig(member, "status") != "active" || dig(member, "tenant_id") != "acme" || dig(member, "org_unit") != "ktm" {
		t.Fatalf("accept: %d %v", status, member)
	}
	// Single use.
	if status, body := h.call("POST", "/api/auth/invitations/accept", "", map[string]any{"token": token, "password": alicePassword}); status != 422 || errorCode(body) != "INVALID_INVITATION" {
		t.Fatalf("second accept: %d %v", status, body)
	}
	if status, body := h.call("POST", "/api/auth/invitations/accept", "", map[string]any{"token": "made-up", "password": alicePassword}); status != 422 || errorCode(body) != "INVALID_INVITATION" {
		t.Fatalf("unknown token: %d %v", status, body)
	}

	// An expired invitation is refused.
	status, inv = h.call("POST", "/api/admin/invitations", admin, map[string]any{"email": "bob@acme.test", "roles": []string{"viewer"}})
	if status != 201 {
		t.Fatalf("invite bob: %d %v", status, inv)
	}
	directory.now = func() time.Time { return time.Now().Add(73 * time.Hour) }
	status, body = h.call("POST", "/api/auth/invitations/accept", "", map[string]any{"token": dig(inv, "token"), "password": "bob's long passphrase"})
	directory.now = time.Now
	if status != 422 || errorCode(body) != "INVALID_INVITATION" {
		t.Fatalf("expired invitation: %d %v", status, body)
	}

	// --- Alice signs in with her membership's roles and org unit ---
	status, body, alice := h.login("ALICE@acme.test", alicePassword)
	if status != 200 || fmt.Sprint(dig(body, "roles")) != "[editor]" {
		t.Fatalf("alice login: %d %v", status, body)
	}
	if units := decodeSegment(t, alice, 1)["org_units"]; fmt.Sprint(units) != "[ktm]" {
		t.Fatalf("org_units claim: %v", units)
	}
	if status, _ := h.call("GET", "/api/admin/users", alice, nil); status != 403 {
		t.Fatalf("editor listing users: %d", status)
	}
	status, users := h.call("GET", "/api/admin/users", admin, nil)
	if status != 200 || len(users.([]any)) != 2 {
		t.Fatalf("list users: %d %v", status, users)
	}
	if status, body := h.call("GET", "/api/admin/users/"+aliceID, admin, nil); status != 200 || dig(body, "email") != "alice@acme.test" {
		t.Fatalf("get user: %d %v", status, body)
	}

	// --- The last admin is protected ---
	if status, body := h.call("DELETE", "/api/admin/users/"+adminID+"/membership", admin, nil); status != 409 || errorCode(body) != "LAST_ADMIN" {
		t.Fatalf("remove last admin: %d %v", status, body)
	}
	if status, body := h.call("PUT", "/api/admin/users/"+adminID+"/membership", admin, map[string]any{"roles": []string{"viewer"}}); status != 409 || errorCode(body) != "LAST_ADMIN" {
		t.Fatalf("demote last admin: %d %v", status, body)
	}
	if status, _ := h.call("POST", "/api/admin/users/"+adminID+"/suspend", admin, nil); status != 403 {
		t.Fatalf("self-suspension: %d", status)
	}

	// --- Suspension ---
	if status, body := h.call("POST", "/api/admin/users/"+aliceID+"/suspend", admin, nil); status != 200 || dig(body, "status") != "suspended" {
		t.Fatalf("suspend: %d %v", status, body)
	}
	if status, body, _ := h.login("alice@acme.test", alicePassword); status != 403 || errorCode(body) != "ACCOUNT_SUSPENDED" {
		t.Fatalf("suspended login: %d %v", status, body)
	}
	if status, _, _ := h.login("alice@acme.test", "wrong password here"); status != 401 {
		t.Fatalf("suspended login with a wrong password must not reveal the suspension: %d", status)
	}
	if status, body := h.call("POST", "/api/admin/users/"+aliceID+"/reactivate", admin, nil); status != 200 || dig(body, "status") != "active" {
		t.Fatalf("reactivate: %d %v", status, body)
	}

	// --- Promotion; authority comes from the directory, not the token ---
	status, body = h.call("PUT", "/api/admin/users/"+aliceID+"/membership", admin, map[string]any{"roles": []string{"admin", "editor"}})
	if status != 200 || fmt.Sprint(dig(body, "roles")) != "[admin editor]" {
		t.Fatalf("promote: %d %v", status, body)
	}
	_, _, alice = h.login("alice@acme.test", alicePassword)
	if status, body := h.call("PUT", "/api/admin/users/"+adminID+"/membership", alice, map[string]any{"roles": []string{"viewer"}}); status != 200 {
		t.Fatalf("demote the first admin now that alice is one: %d %v", status, body)
	}
	// The first admin's token still says "admin", but the directory does not.
	if status, _ := h.call("GET", "/api/admin/users", admin, nil); status != 403 {
		t.Fatalf("demoted admin with a stale token: %d", status)
	}
	if status, body := h.call("DELETE", "/api/admin/users/"+aliceID+"/membership", alice, nil); status != 409 || errorCode(body) != "LAST_ADMIN" {
		t.Fatalf("alice removing herself as the last admin: %d %v", status, body)
	}

	// --- Add and remove a membership ---
	status, body = h.call("POST", "/api/admin/memberships", alice, map[string]any{"email": identityAdminEmail, "roles": []string{"auditor"}})
	if status != 409 {
		t.Fatalf("adding an existing member: %d %v", status, body)
	}
	if status, body := h.call("DELETE", "/api/admin/users/"+adminID+"/membership", alice, nil); status != 200 || dig(body, "removed") != true {
		t.Fatalf("remove membership: %d %v", status, body)
	}
	if status, body := h.call("POST", "/api/admin/memberships", alice, map[string]any{"email": identityAdminEmail, "roles": []string{"auditor"}}); status != 201 || fmt.Sprint(dig(body, "roles")) != "[auditor]" {
		t.Fatalf("add membership: %d %v", status, body)
	}

	// --- Password change ---
	if status, _ := h.call("POST", "/api/auth/password", alice, map[string]any{"current_password": "wrong", "new_password": "a brand new passphrase"}); status != 401 {
		t.Fatalf("change with a wrong current password: %d", status)
	}
	if status, body := h.call("POST", "/api/auth/password", alice, map[string]any{"current_password": alicePassword, "new_password": "a brand new passphrase"}); status != 200 {
		t.Fatalf("change password: %d %v", status, body)
	}
	if status, _, _ := h.login("alice@acme.test", alicePassword); status != 401 {
		t.Fatalf("old password still works: %d", status)
	}
	if status, _, _ := h.login("alice@acme.test", "a brand new passphrase"); status != 200 {
		t.Fatalf("new password: %d", status)
	}

	// --- Audit trail ---
	status, entries := h.call("GET", "/api/admin/audit", alice, nil)
	if status != 200 {
		t.Fatalf("audit: %d %v", status, entries)
	}
	var actions []string
	for _, e := range entries.([]any) {
		actions = append(actions, Stringify(dig(e, "action")))
	}
	joined := strings.Join(actions, ",")
	for _, want := range []string{"user.bootstrapped", "user.invited", "invitation.accepted", "user.suspended", "user.reactivated", "membership.changed", "membership.removed", "membership.added", "user.login"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("audit trail misses %s: %s", want, joined)
		}
	}

	// --- Signing arbitrary payloads ---
	status, sig := h.call("POST", "/api/sign", alice, map[string]any{"payload": map[string]any{"order": "A-1", "total": 12.5}})
	if status != 200 || dig(sig, "alg") != "EdDSA" || dig(sig, "kid") != "identity-1" {
		t.Fatalf("sign: %d %v", status, sig)
	}
	status, body = h.call("POST", "/api/verify", "", map[string]any{"payload": map[string]any{"total": 12.5, "order": "A-1"}, "signature": sig})
	if status != 200 || dig(body, "valid") != true {
		t.Fatalf("verify: %d %v", status, body)
	}
	status, body = h.call("POST", "/api/verify", "", map[string]any{"payload": map[string]any{"total": 99, "order": "A-1"}, "signature": sig})
	if status != 200 || dig(body, "valid") != false {
		t.Fatalf("verify tampered: %d %v", status, body)
	}

	// --- A licence certificate signed with the key set ---
	status, started := h.call("POST", "/api/licences", alice, map[string]any{
		"data": map[string]any{"application": map[string]any{"holder": "Asha Rai", "trade": "Bakery"}}})
	if status != 201 {
		t.Fatalf("start licence: %d %v", status, started)
	}
	caseID := Stringify(dig(started, "case", "id"))
	if status, body := h.call("POST", "/api/licences/"+caseID+"/stages/application/actions/submit", alice, map[string]any{}); status != 200 {
		t.Fatalf("submit: %d %v", status, body)
	}
	status, got := h.call("GET", "/api/licences/"+caseID, alice, nil)
	if status != 200 || len(dig(got, "certificates").([]any)) != 1 {
		t.Fatalf("case: %d %v", status, got)
	}
	code := Stringify(dig(got, "certificates", 0, "code"))
	status, verified := h.call("GET", "/api/licence-certificates/"+code, "", nil)
	if status != 200 || dig(verified, "valid") != true || dig(verified, "certificate", "key_signature", "kid") != "identity-1" {
		t.Fatalf("verify certificate: %d %v", status, verified)
	}
	// Offline: the certificate as published plus the JWKS, nothing else.
	var cert pipeline.Certificate
	raw, _ := json.Marshal(dig(verified, "certificate"))
	if err := json.Unmarshal(raw, &cert); err != nil {
		t.Fatal(err)
	}
	offline, err := signing.ParseJWKS(rawJWKS)
	if err != nil {
		t.Fatal(err)
	}
	if err := pipeline.VerifyCertificateSignature(cert, offline); err != nil {
		t.Fatalf("offline certificate verification: %v", err)
	}
	cert.Subject["application.holder"] = "Mallory"
	if pipeline.VerifyCertificateSignature(cert, offline) == nil {
		t.Fatal("tampered certificate verified offline")
	}
}

func TestIdentityExampleValidates(t *testing.T) {
	src, err := os.ReadFile("../examples/identity/app.bcl")
	if err != nil {
		t.Fatal(err)
	}
	opts := DefaultLoadOptions()
	opts.Env = func(string) (string, bool) { return "", false }
	r := Validate(context.Background(), src, "../examples/identity", opts)
	if !r.Valid {
		t.Fatalf("identity example should validate: %v", r.Errors)
	}
	if !strings.Contains(strings.Join(r.Warnings, " "), "IDENTITY_SIGNING_KEY") {
		t.Fatalf("expected a warning about the unset signing key: %v", r.Warnings)
	}
	// An unknown kind or a typo in a new kind's name is caught statically.
	bad := strings.Replace(string(src), `kind "crypto.signer"`, `kind "crypto.signr"`, 1)
	if r := Validate(context.Background(), []byte(bad), "../examples/identity", opts); r.Valid {
		t.Fatal("unregistered kind validated")
	}
}
