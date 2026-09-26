package deploy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"

	"github.com/oarkflow/ref/signing"
)

func TestRevisionSignedWithEd25519(t *testing.T) {
	ctx := context.Background()
	key, err := signing.GenerateEd25519("deploy-2026")
	if err != nil {
		t.Fatal(err)
	}
	keys, _ := signing.NewKeySet(key)

	store := NewMemoryStore()
	m := newManager(store)
	m.Secret = nil // asymmetric only
	m.Signer = keys
	m.Approvals = 0

	r, err := m.Propose(ctx, []byte(appSource("1")), "alice", "first")
	if err != nil {
		t.Fatal(err)
	}
	if r.KeySignature == nil || r.KeySignature.Algorithm != "EdDSA" || r.KeySignature.KeyID != "deploy-2026" || r.Signature != "" {
		t.Fatalf("revision signature: %+v / %q", r.KeySignature, r.Signature)
	}
	if err := m.Verify(r); err != nil {
		t.Fatalf("verify: %v", err)
	}

	// A host holding only the published public key verifies it too.
	raw, _ := json.Marshal(keys.JWKS())
	public, err := signing.ParseJWKS(raw)
	if err != nil {
		t.Fatal(err)
	}
	reader := &Manager{Verifier: public}
	if err := reader.Verify(r); err != nil {
		t.Fatalf("verify with the public key: %v", err)
	}

	// Tampering with the source (and fixing up the checksum, as someone with
	// write access to the store could) breaks the signature.
	tampered := *r
	tampered.Source = appSource("evil")
	sum := sha256Hex(tampered.Source)
	tampered.Checksum = sum
	if err := m.Verify(&tampered); !errors.Is(err, ErrTampered) {
		t.Fatalf("tampered revision: %v", err)
	}
	// So does dropping the signature, or signing with another key.
	unsigned := *r
	unsigned.KeySignature = nil
	if err := m.Verify(&unsigned); !errors.Is(err, ErrTampered) {
		t.Fatalf("unsigned revision: %v", err)
	}
	other, _ := signing.GenerateEd25519("deploy-2026")
	otherKeys, _ := signing.NewKeySet(other)
	forged := tampered
	sig, _ := otherKeys.Sign(SigningPayload(&forged))
	forged.KeySignature = &sig
	if err := m.Verify(&forged); !errors.Is(err, ErrTampered) {
		t.Fatalf("revision signed by another key: %v", err)
	}

	// Activation refuses a tampered revision in the store.
	if err := store.Update(ctx, &tampered); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Activate(ctx, r.ID, "bob"); !errors.Is(err, ErrTampered) {
		t.Fatalf("activate tampered: %v", err)
	}
}

func TestRevisionVerifyAcceptsHMACOrKey(t *testing.T) {
	ctx := context.Background()
	// A revision signed before a key was introduced (HMAC only)...
	legacy := newManager(NewMemoryStore())
	legacy.Approvals = 0
	old, err := legacy.Propose(ctx, []byte(appSource("1")), "alice", "")
	if err != nil {
		t.Fatal(err)
	}
	// ...still verifies once the manager also holds a key.
	key, _ := signing.GenerateEd25519("")
	keys, _ := signing.NewKeySet(key)
	both := newManager(legacy.Store)
	both.Signer = keys
	if err := both.Verify(old); err != nil {
		t.Fatalf("HMAC-only revision under a keyed manager: %v", err)
	}
	fresh, err := both.Propose(ctx, []byte(appSource("2")), "alice", "")
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Signature == "" || fresh.KeySignature == nil {
		t.Fatalf("both signatures expected: %+v", fresh)
	}
	// Either signature alone is enough.
	keyOnly := *fresh
	keyOnly.Signature = ""
	hmacOnly := *fresh
	hmacOnly.KeySignature = nil
	for _, r := range []*Revision{&keyOnly, &hmacOnly} {
		if err := both.Verify(r); err != nil {
			t.Fatalf("verify: %v", err)
		}
	}
	bad := *fresh
	bad.Signature, bad.KeySignature = "00", nil
	if err := both.Verify(&bad); !errors.Is(err, ErrTampered) {
		t.Fatalf("bad HMAC: %v", err)
	}
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
