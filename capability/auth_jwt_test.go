package capability_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/oarkflow/ref/capability"
	"github.com/oarkflow/ref/invocation"
)

func mustHMACKey(t *testing.T, secret string) *capability.JWTKey {
	t.Helper()
	key, err := capability.NewJWTHMACKey(capability.JWTAlgHS256, []byte(secret))
	if err != nil {
		t.Fatalf("NewJWTHMACKey: %v", err)
	}
	return key
}

// signHS256 builds a compact JWS token signed with HS256, for test purposes
// only. It intentionally lives in the test file rather than the production
// code: production signing is out of scope for the capability layer, which
// only ever verifies tokens minted elsewhere (e.g. by platform.SignJWT or an
// external IdP).
func signHS256(t *testing.T, secret string, alg string, claims map[string]any) string {
	t.Helper()
	header := map[string]any{"alg": alg, "typ": "JWT"}
	headerJSON, err := json.Marshal(header)
	if err != nil {
		t.Fatalf("marshal header: %v", err)
	}
	bodyJSON, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	enc := base64.RawURLEncoding
	signingInput := enc.EncodeToString(headerJSON) + "." + enc.EncodeToString(bodyJSON)

	if alg == "none" {
		// "none" carries an empty signature segment.
		return signingInput + "."
	}

	// HMAC-SHA256 signature.
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(signingInput))
	return signingInput + "." + enc.EncodeToString(mac.Sum(nil))
}

func TestNewJWTAuthenticator_ValidToken(t *testing.T) {
	secret := "supersecretsupersecretsupersecret!!"
	key := mustHMACKey(t, secret)

	now := time.Now()
	token := signHS256(t, secret, "HS256", map[string]any{
		"sub":   "user-42",
		"exp":   now.Add(time.Hour).Unix(),
		"iat":   now.Unix(),
		"roles": []string{"admin", "editor"},
		"scope": "read write",
	})

	auth := capability.NewJWTAuthenticator(capability.JWTAuthenticatorConfig{
		Keys: map[string]*capability.JWTKey{"": key},
	})

	principal, err := auth(invocation.NewPrincipalHint(token, "", nil, ""))
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if principal.ID != "user-42" {
		t.Errorf("expected ID user-42, got %q", principal.ID)
	}
	if len(principal.Roles) != 2 || principal.Roles[0] != "admin" || principal.Roles[1] != "editor" {
		t.Errorf("expected roles [admin editor], got %v", principal.Roles)
	}
	if len(principal.Scopes) != 2 || principal.Scopes[0] != "read" || principal.Scopes[1] != "write" {
		t.Errorf("expected scopes [read write], got %v", principal.Scopes)
	}
}

func TestNewJWTAuthenticator_ExpiredToken(t *testing.T) {
	secret := "supersecretsupersecretsupersecret!!"
	key := mustHMACKey(t, secret)

	token := signHS256(t, secret, "HS256", map[string]any{
		"sub": "user-42",
		"exp": time.Now().Add(-time.Hour).Unix(),
	})

	auth := capability.NewJWTAuthenticator(capability.JWTAuthenticatorConfig{
		Keys: map[string]*capability.JWTKey{"": key},
	})

	_, err := auth(invocation.NewPrincipalHint(token, "", nil, ""))
	if err == nil {
		t.Fatal("expected error for expired token")
	}
	if !errors.Is(err, capability.ErrUnauthenticated) {
		t.Errorf("expected error to wrap ErrUnauthenticated, got %v", err)
	}
}

func TestNewJWTAuthenticator_InvalidSignature(t *testing.T) {
	secret := "supersecretsupersecretsupersecret!!"
	wrongSecret := "wrongwrongwrongwrongwrongwrongwrong"
	key := mustHMACKey(t, secret)

	token := signHS256(t, wrongSecret, "HS256", map[string]any{
		"sub": "user-42",
		"exp": time.Now().Add(time.Hour).Unix(),
	})

	auth := capability.NewJWTAuthenticator(capability.JWTAuthenticatorConfig{
		Keys: map[string]*capability.JWTKey{"": key},
	})

	_, err := auth(invocation.NewPrincipalHint(token, "", nil, ""))
	if err == nil {
		t.Fatal("expected error for invalid signature")
	}
	if !errors.Is(err, capability.ErrUnauthenticated) {
		t.Errorf("expected error to wrap ErrUnauthenticated, got %v", err)
	}
}

func TestNewJWTAuthenticator_RejectsNoneAlgorithm(t *testing.T) {
	secret := "supersecretsupersecretsupersecret!!"
	key := mustHMACKey(t, secret)

	token := signHS256(t, secret, "none", map[string]any{
		"sub": "user-42",
		"exp": time.Now().Add(time.Hour).Unix(),
	})

	auth := capability.NewJWTAuthenticator(capability.JWTAuthenticatorConfig{
		Keys: map[string]*capability.JWTKey{"": key},
	})

	_, err := auth(invocation.NewPrincipalHint(token, "", nil, ""))
	if err == nil {
		t.Fatal("expected \"none\" algorithm to be rejected")
	}
	if !errors.Is(err, capability.ErrUnauthenticated) {
		t.Errorf("expected error to wrap ErrUnauthenticated, got %v", err)
	}
}

func TestNewJWTAuthenticator_NoBearerToken(t *testing.T) {
	secret := "supersecretsupersecretsupersecret!!"
	key := mustHMACKey(t, secret)

	auth := capability.NewJWTAuthenticator(capability.JWTAuthenticatorConfig{
		Keys: map[string]*capability.JWTKey{"": key},
	})

	_, err := auth(invocation.NewPrincipalHint("", "", nil, ""))
	if err == nil {
		t.Fatal("expected error when no bearer token is present")
	}
	if !errors.Is(err, capability.ErrUnauthenticated) {
		t.Errorf("expected error to wrap ErrUnauthenticated, got %v", err)
	}
}

func TestNewJWTAuthenticator_MissingExpiryRejectedByDefault(t *testing.T) {
	secret := "supersecretsupersecretsupersecret!!"
	key := mustHMACKey(t, secret)

	token := signHS256(t, secret, "HS256", map[string]any{
		"sub": "user-42",
	})

	auth := capability.NewJWTAuthenticator(capability.JWTAuthenticatorConfig{
		Keys: map[string]*capability.JWTKey{"": key},
	})

	_, err := auth(invocation.NewPrincipalHint(token, "", nil, ""))
	if err == nil {
		t.Fatal("expected token without exp to be rejected by default")
	}
}

func TestNewJWTAuthenticator_IssuerAudienceMismatch(t *testing.T) {
	secret := "supersecretsupersecretsupersecret!!"
	key := mustHMACKey(t, secret)

	token := signHS256(t, secret, "HS256", map[string]any{
		"sub": "user-42",
		"exp": time.Now().Add(time.Hour).Unix(),
		"iss": "https://issuer.example",
		"aud": "wrong-audience",
	})

	auth := capability.NewJWTAuthenticator(capability.JWTAuthenticatorConfig{
		Keys:     map[string]*capability.JWTKey{"": key},
		Issuer:   "https://issuer.example",
		Audience: "expected-audience",
	})

	_, err := auth(invocation.NewPrincipalHint(token, "", nil, ""))
	if err == nil {
		t.Fatal("expected audience mismatch to be rejected")
	}
}
