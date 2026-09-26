package signing

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"testing"
)

func rsaKey(t *testing.T, id string, alg Algorithm) *Key {
	t.Helper()
	private, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	k, err := NewKey(id, alg, private)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestSignVerifyEveryAlgorithm(t *testing.T) {
	ed, err := GenerateEd25519("")
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []*Key{ed, rsaKey(t, "rs", RS256), rsaKey(t, "ps", PS256)} {
		t.Run(string(k.Algorithm), func(t *testing.T) {
			set, err := NewKeySet(k)
			if err != nil {
				t.Fatal(err)
			}
			sig, err := set.SignJSON(map[string]any{"b": 1, "a": []any{"x", true}})
			if err != nil {
				t.Fatal(err)
			}
			if sig.Algorithm != string(k.Algorithm) || sig.KeyID != k.ID {
				t.Fatalf("signature header: %+v", sig)
			}
			// Key order does not matter: the payload is canonical.
			if err := set.VerifyJSON(map[string]any{"a": []any{"x", true}, "b": 1}, sig); err != nil {
				t.Fatal(err)
			}
			if err := set.VerifyJSON(map[string]any{"a": []any{"x", false}, "b": 1}, sig); !errors.Is(err, ErrInvalidSignature) {
				t.Fatalf("tampered payload verified: %v", err)
			}
			// The algorithm comes from the key: a signature claiming another
			// one is refused.
			forged := sig
			forged.Algorithm = "HS256"
			if err := set.VerifyJSON(map[string]any{"a": []any{"x", true}, "b": 1}, forged); err == nil {
				t.Fatal("algorithm mismatch verified")
			}
		})
	}
}

func TestPEMRoundTripAndJWKS(t *testing.T) {
	ed, _ := GenerateEd25519("ed-1")
	rs := rsaKey(t, "rs-1", RS256)
	for _, k := range []*Key{ed, rs} {
		privatePEM, err := k.MarshalPrivateKeyPEM()
		if err != nil {
			t.Fatal(err)
		}
		back, err := ParsePrivateKeyPEM(k.ID, k.Algorithm, privatePEM)
		if err != nil || back.Thumbprint() != k.Thumbprint() || !back.CanSign() {
			t.Fatalf("private round trip: %v", err)
		}
		publicPEM, _ := k.MarshalPublicKeyPEM()
		pub, err := ParseKeyPEM("", "", publicPEM)
		if err != nil || pub.CanSign() || pub.Thumbprint() != k.Thumbprint() {
			t.Fatalf("public round trip: %v", err)
		}
		if pub.ID != k.Thumbprint()[:16] {
			t.Fatalf("default kid = %q", pub.ID)
		}
	}

	set, err := NewKeySet(ed, rs.PublicOnly())
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(set.JWKS())
	offline, err := ParseJWKS(raw)
	if err != nil {
		t.Fatal(err)
	}
	if offline.Active() != nil || len(offline.Keys()) != 2 {
		t.Fatalf("offline set: %+v", offline.Keys())
	}
	sig, _ := set.Sign([]byte("hello"))
	if err := offline.Verify([]byte("hello"), sig); err != nil {
		t.Fatalf("JWKS verification: %v", err)
	}
	if _, err := offline.Sign([]byte("x")); err == nil {
		t.Fatal("a verification-only set signed")
	}
}

func TestRotationKeepsOldKeysVerifying(t *testing.T) {
	oldKey, _ := GenerateEd25519("2025")
	newKey, _ := GenerateEd25519("2026")
	before, _ := NewKeySet(oldKey)
	sig, _ := before.Sign([]byte("issued last year"))

	after, err := NewKeySet(newKey, oldKey.PublicOnly())
	if err != nil {
		t.Fatal(err)
	}
	if err := after.Verify([]byte("issued last year"), sig); err != nil {
		t.Fatalf("old signature after rotation: %v", err)
	}
	fresh, _ := after.Sign([]byte("today"))
	if fresh.KeyID != "2026" {
		t.Fatalf("signed with %q", fresh.KeyID)
	}
	if _, err := NewKeySet(newKey, newKey); err == nil {
		t.Fatal("duplicate kid accepted")
	}
}

func TestCanonical(t *testing.T) {
	got, err := Canonical(map[string]any{"z": "<&>", "a": map[string]any{"y": 1.5, "b": nil}, "m": []int{3, 1}})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"a":{"b":null,"y":1.5},"m":[3,1],"z":"<&>"}`
	if string(got) != want {
		t.Fatalf("canonical = %s, want %s", got, want)
	}
}

func TestWeakRSARefused(t *testing.T) {
	private, _ := rsa.GenerateKey(rand.Reader, 1024)
	if _, err := NewKey("", RS256, private); err == nil {
		t.Fatal("1024-bit RSA accepted")
	}
}
