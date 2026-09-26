package pipeline

import (
	"encoding/json"
	"testing"

	"github.com/oarkflow/ref/signing"
)

func TestCertificateKeySignatureVerifiesOfflineWithJWKS(t *testing.T) {
	key, err := signing.GenerateEd25519("certs-1")
	if err != nil {
		t.Fatal(err)
	}
	keys, _ := signing.NewKeySet(key)
	e := newEngine(t, true)
	e.Signer = keys

	c := &Case{ID: "case_1", Number: "PP-2026-000001", Pipeline: "passport",
		Data: goodData()}
	cert, err := e.issue(c, "approval", Actor{ID: "u-officer"})
	if err != nil {
		t.Fatal(err)
	}
	if cert.KeySignature == nil || cert.KeySignature.KeyID != "certs-1" || cert.Signature == "" {
		t.Fatalf("certificate signatures: %+v", cert)
	}
	if status := e.Verify(*cert); !status.Valid {
		t.Fatalf("engine verify: %+v", status)
	}

	// Offline: only the published JWKS and the certificate as JSON.
	jwks, _ := json.Marshal(keys.JWKS())
	public, err := signing.ParseJWKS(jwks)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(cert)
	var held Certificate
	if err := json.Unmarshal(raw, &held); err != nil {
		t.Fatal(err)
	}
	if err := VerifyCertificateSignature(held, public); err != nil {
		t.Fatalf("offline verification: %v", err)
	}
	tampered := held
	tampered.Subject = map[string]any{"applicant.full_name": "Mallory"}
	if VerifyCertificateSignature(tampered, public) == nil {
		t.Fatal("tampered certificate verified offline")
	}
	// Re-hashing the altered content does not help without the private key.
	sealed := tampered
	_ = NewEngine(e.C).seal(&sealed) // hash only: no secret, no signer
	sealed.KeySignature = held.KeySignature
	if VerifyCertificateSignature(sealed, public) == nil {
		t.Fatal("re-hashed tampered certificate verified offline")
	}
	if e.Verify(sealed).Valid {
		t.Fatal("re-hashed tampered certificate verified by the engine")
	}

	// Backward compatibility: a certificate issued with the HMAC only still
	// verifies after a signer is added.
	legacy := newEngine(t, true)
	old, err := legacy.issue(&Case{ID: "case_2", Number: "PP-2026-000002", Pipeline: "passport", Data: goodData()}, "approval", Actor{ID: "u-officer"})
	if err != nil {
		t.Fatal(err)
	}
	if old.KeySignature != nil || !e.Verify(*old).Valid {
		t.Fatalf("legacy certificate under a signer: %+v", e.Verify(*old))
	}
	// A signer-only engine refuses a certificate that carries no signature.
	keyOnly := NewEngine(e.C)
	keyOnly.Signer = keys
	if keyOnly.Verify(*old).Valid {
		t.Fatal("unsigned certificate accepted by a signer-only engine")
	}
	if !keyOnly.Verify(*cert).Valid {
		t.Fatalf("signed certificate under a signer-only engine: %+v", keyOnly.Verify(*cert))
	}
}
