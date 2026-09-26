package platform

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/oarkflow/ref/signing"
)

func pemOf(t *testing.T, k *signing.Key) string {
	t.Helper()
	raw, err := k.MarshalPrivateKeyPEM()
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func publicPEMOf(t *testing.T, k *signing.Key) string {
	t.Helper()
	raw, err := k.MarshalPublicKeyPEM()
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func openTestSigner(t *testing.T, config map[string]any) *Signer {
	t.Helper()
	res, _, err := openSigner(context.Background(), ResourceSpec{Name: "keys", Kind: "crypto.signer", Config: config})
	if err != nil {
		t.Fatal(err)
	}
	return res.(*Signer)
}

func openTestJWT(t *testing.T, signer *Signer, extra map[string]any) *jwtAuth {
	t.Helper()
	config := map[string]any{"signer": "keys", "issuer": "test", "audience": "test"}
	for k, v := range extra {
		config[k] = v
	}
	res, _, err := openJWTAuth(context.Background(), ResourceSpec{Name: "jwt", Kind: "auth.jwt", Config: config,
		resolved: map[string]Resource{"keys": signer}})
	if err != nil {
		t.Fatal(err)
	}
	return res.(*jwtAuth)
}

func rsaSigningKey(t *testing.T, id string, alg signing.Algorithm) *signing.Key {
	t.Helper()
	private, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	k, err := signing.NewKey(id, alg, private)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestJWTRoundTripWithSigner(t *testing.T) {
	ed, _ := signing.GenerateEd25519("ed")
	for _, tc := range []struct {
		name  string
		key   *signing.Key
		alg   string
		extra map[string]any
	}{
		{"EdDSA", ed, "EdDSA", nil},
		{"RS256", rsaSigningKey(t, "rs", signing.RS256), "RS256", map[string]any{"algorithm": "RS256"}},
		{"PS256", rsaSigningKey(t, "ps", signing.PS256), "PS256", map[string]any{"algorithm": "PS256"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := map[string]any{"private_key": pemOf(t, tc.key), "key_id": tc.key.ID}
			for k, v := range tc.extra {
				config[k] = v
			}
			auth := openTestJWT(t, openTestSigner(t, config), nil)
			token, _, err := auth.Issue(Principal{ID: "u1", TenantID: "acme", Roles: []string{"admin"}}, time.Minute, nil)
			if err != nil {
				t.Fatal(err)
			}
			header := decodeSegment(t, token, 0)
			if header["alg"] != tc.alg || header["kid"] != tc.key.ID {
				t.Fatalf("header: %v", header)
			}
			principal, err := auth.Authenticate(context.Background(), Credentials{BearerToken: token})
			if err != nil || principal.ID != "u1" || principal.TenantID != "acme" || principal.Roles[0] != "admin" {
				t.Fatalf("authenticate: %v %+v", err, principal)
			}
			// One flipped signature byte fails.
			parts := strings.Split(token, ".")
			sig, _ := base64Raw.DecodeString(parts[2])
			sig[0] ^= 1
			if _, err := auth.Authenticate(context.Background(), Credentials{BearerToken: parts[0] + "." + parts[1] + "." + base64Raw.EncodeToString(sig)}); err == nil {
				t.Fatal("tampered token accepted")
			}
			// An HS256 token forged with the public key as the secret is
			// refused: the algorithm comes from the key.
			public, _ := tc.key.MarshalPublicKeyPEM()
			if len(public) >= 32 {
				hmacKey, _ := NewHMACKey(HS256, public)
				hmacKey.ID = tc.key.ID
				forged, _ := SignJWT(hmacKey, JWTClaims{Subject: "u1", ExpiresAt: time.Now().Add(time.Minute).Unix(), Issuer: "test", Audience: []string{"test"}}, nil)
				if _, err := auth.Authenticate(context.Background(), Credentials{BearerToken: forged}); err == nil {
					t.Fatal("HS256 forgery with the public key accepted")
				}
			}
		})
	}
}

func TestJWTKeyRotationKeepsOldKidVerifying(t *testing.T) {
	oldKey, _ := signing.GenerateEd25519("2025")
	newKey := rsaSigningKey(t, "2026", signing.RS256)

	before := openTestJWT(t, openTestSigner(t, map[string]any{"private_key": pemOf(t, oldKey), "key_id": "2025"}), nil)
	oldToken, _, err := before.Issue(Principal{ID: "u1"}, time.Hour, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Rotate: the RSA key signs, the Ed25519 key stays for verification only.
	after := openTestJWT(t, openTestSigner(t, map[string]any{
		"algorithm": "RS256", "private_key": pemOf(t, newKey), "key_id": "2026",
		"keys": []any{map[string]any{"key_id": "2025", "algorithm": "EdDSA", "public_key": publicPEMOf(t, oldKey)}},
	}), nil)
	if p, err := after.Authenticate(context.Background(), Credentials{BearerToken: oldToken}); err != nil || p.ID != "u1" {
		t.Fatalf("old kid after rotation: %v", err)
	}
	newToken, _, _ := after.Issue(Principal{ID: "u2"}, time.Hour, nil)
	if h := decodeSegment(t, newToken, 0); h["kid"] != "2026" || h["alg"] != "RS256" {
		t.Fatalf("new token header: %v", h)
	}
	// The pre-rotation deployment does not know the new key.
	if _, err := before.Authenticate(context.Background(), Credentials{BearerToken: newToken}); err == nil {
		t.Fatal("unknown kid accepted")
	}
	// The JWKS lists both keys, active first.
	jwks := after.JWKS()
	if len(jwks.Keys) != 2 || jwks.Keys[0].Kid != "2026" || jwks.Keys[1].Kty != "OKP" {
		t.Fatalf("jwks: %+v", jwks)
	}
	// Retired keys lose their private half even when configured with it.
	retired := openTestSigner(t, map[string]any{"private_key": pemOf(t, newKey), "algorithm": "RS256",
		"keys": []any{map[string]any{"private_key": pemOf(t, oldKey), "algorithm": "EdDSA"}}})
	if k := retired.keys.Keys()[1]; k.CanSign() {
		t.Fatal("a retired key kept its private material")
	}
}

func TestJWTSignerConfigErrors(t *testing.T) {
	key, _ := signing.GenerateEd25519("k")
	signer := openTestSigner(t, map[string]any{"private_key": pemOf(t, key)})
	_, _, err := openJWTAuth(context.Background(), ResourceSpec{Name: "jwt", Kind: "auth.jwt",
		Config:   map[string]any{"signer": "keys", "secret": strings.Repeat("s", 40)},
		resolved: map[string]Resource{"keys": signer}})
	if err == nil || !strings.Contains(err.Error(), "replaces") {
		t.Fatalf("signer plus secret: %v", err)
	}
	if _, _, err := openSigner(context.Background(), ResourceSpec{Name: "keys", Config: map[string]any{}}); err == nil {
		t.Fatal("a signer without keys opened")
	}
	if _, _, err := openSigner(context.Background(), ResourceSpec{Name: "keys", Config: map[string]any{"private_key": pemOf(t, key), "algorithm": "RS256"}}); err == nil {
		t.Fatal("an Ed25519 key opened as RS256")
	}
	// A plain PEM-configured auth.jwt handles EdDSA as well.
	auth, _, err := openJWTAuth(context.Background(), ResourceSpec{Name: "jwt", Kind: "auth.jwt", Config: map[string]any{
		"algorithm": "EdDSA", "public_key": publicPEMOf(t, key), "private_key": pemOf(t, key)}})
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := auth.(*jwtAuth).Issue(Principal{ID: "u"}, time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := auth.(*jwtAuth).Authenticate(context.Background(), Credentials{BearerToken: token}); err != nil {
		t.Fatalf("PEM EdDSA: %v", err)
	}
}

func TestParseJWKSAcceptsOKP(t *testing.T) {
	key, _ := signing.GenerateEd25519("okp")
	set, _ := signing.NewKeySet(key)
	raw, _ := json.Marshal(set.JWKS())
	keys, err := parseJWKS(raw)
	if err != nil || keys["okp"] == nil || keys["okp"].Algorithm != EdDSA {
		t.Fatalf("parseJWKS: %v %+v", err, keys)
	}
}

func decodeSegment(t *testing.T, token string, index int) map[string]any {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("not a JWT: %q", token)
	}
	raw, err := base64Raw.DecodeString(parts[index])
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}
