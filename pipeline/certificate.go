package pipeline

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/oarkflow/ref/signing"
)

// Certificate is an issued document: a passport approval, a licence, a
// registration certificate. Its content is hashed and, when the engine has a
// signing key, signed, so anyone holding the certificate can have it verified
// and any edit to it is detected.
type Certificate struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	Title      string         `json:"title,omitempty"`
	Number     string         `json:"number"`
	CaseID     string         `json:"case_id"`
	CaseNumber string         `json:"case_number"`
	Pipeline   string         `json:"pipeline"`
	Subject    map[string]any `json:"subject"`
	IssuedAt   time.Time      `json:"issued_at"`
	ExpiresAt  *time.Time     `json:"expires_at,omitempty"`
	IssuedBy   string         `json:"issued_by,omitempty"`
	// Hash is SHA-256 over the canonical content; Signature is HMAC-SHA256
	// over the hash with the engine's signing key.
	Hash      string `json:"hash"`
	Signature string `json:"signature,omitempty"`
	// KeySignature is an asymmetric signature (Ed25519 or RSA) over the
	// canonical content, made with the engine's Signer. Anyone holding the
	// issuer's public keys (its JWKS) can verify it offline — no call back to
	// the issuer, no shared secret.
	KeySignature *signing.Signature `json:"key_signature,omitempty"`
	// Code is a short verification code printed on the document.
	Code    string     `json:"code"`
	Revoked *time.Time `json:"revoked_at,omitempty"`
}

// Canonical returns the signed content: the certificate's identifying fields
// and subject as JSON with sorted keys. It is what Hash digests and what
// KeySignature signs, so an offline verifier recomputes exactly this.
func (c Certificate) Canonical() []byte { return c.canonical() }

// canonical renders the signed content deterministically (sorted keys).
func (c Certificate) canonical() []byte {
	content := map[string]any{
		"id": c.ID, "name": c.Name, "number": c.Number, "case_id": c.CaseID,
		"case_number": c.CaseNumber, "pipeline": c.Pipeline, "subject": c.Subject,
		"issued_at": c.IssuedAt.UTC().Format(time.RFC3339),
	}
	if c.ExpiresAt != nil {
		content["expires_at"] = c.ExpiresAt.UTC().Format(time.RFC3339)
	}
	raw, _ := json.Marshal(content) // encoding/json sorts map keys
	return raw
}

func (e *Engine) issue(c *Case, name string, actor Actor) (*Certificate, error) {
	spec, ok := e.C.Certificate(name)
	if !ok {
		return nil, fmt.Errorf("%w: certificate %q", ErrNotFound, name)
	}
	for _, existing := range c.Certificates {
		if existing.Name == name && existing.Revoked == nil {
			return &existing, nil // issuing is idempotent per case
		}
	}
	now := e.now()
	subject := map[string]any{}
	for _, path := range spec.Fields {
		if v, ok := c.Get(path); ok {
			subject[path] = v
		}
	}
	cert := Certificate{
		ID:         e.newID("cert"),
		Name:       name,
		Title:      spec.Title,
		CaseID:     c.ID,
		CaseNumber: c.Number,
		Pipeline:   c.Pipeline,
		Subject:    subject,
		IssuedAt:   now.Truncate(time.Second),
		IssuedBy:   actor.ID,
	}
	cert.Number = FormatNumber(spec.NumberFormat, int64(len(c.Certificates)+1), now)
	if spec.NumberFormat == "" {
		cert.Number = c.Number + "-" + strings.ToUpper(name)
	} else {
		cert.Number = strings.ReplaceAll(cert.Number, "{case}", c.Number)
	}
	if d, _ := parseDuration(spec.Validity); d > 0 {
		exp := cert.IssuedAt.Add(d)
		cert.ExpiresAt = &exp
	}
	if err := e.seal(&cert); err != nil {
		return nil, err
	}
	c.Certificates = append(c.Certificates, cert)
	c.History = append(c.History, Entry{At: now, Actor: actor.ID, Stage: c.Stage, Action: "certificate_issued", To: cert.Number})
	return &cert, nil
}

func (e *Engine) seal(cert *Certificate) error {
	sum := sha256.Sum256(cert.canonical())
	cert.Hash = hex.EncodeToString(sum[:])
	if len(e.SigningKey) > 0 {
		mac := hmac.New(sha256.New, e.SigningKey)
		mac.Write(sum[:])
		cert.Signature = hex.EncodeToString(mac.Sum(nil))
	}
	if e.Signer != nil && e.Signer.Active() != nil {
		sig, err := e.Signer.Sign(cert.canonical())
		if err != nil {
			return fmt.Errorf("sign certificate: %w", err)
		}
		cert.KeySignature = &sig
	}
	cert.Code = strings.ToUpper(cert.Hash[:4] + "-" + cert.Hash[4:8] + "-" + cert.Hash[8:12])
	return nil
}

// Verifier checks an asymmetric signature; *signing.KeySet (for example one
// parsed from the issuer's published JWKS) is one.
type Verifier interface {
	Verify(payload []byte, sig signing.Signature) error
}

// VerifyCertificateSignature checks a certificate offline: its content must
// match its hash and its key signature must verify under keys. It does not
// consult revocation or expiry, which only the issuer knows.
func VerifyCertificateSignature(cert Certificate, keys Verifier) error {
	sum := sha256.Sum256(cert.canonical())
	if hex.EncodeToString(sum[:]) != cert.Hash {
		return fmt.Errorf("the certificate content has been altered")
	}
	if cert.KeySignature == nil {
		return fmt.Errorf("the certificate carries no key signature")
	}
	return keys.Verify(cert.canonical(), *cert.KeySignature)
}

// CertificateStatus is the outcome of a verification.
type CertificateStatus struct {
	Valid   bool   `json:"valid"`
	Reason  string `json:"reason,omitempty"`
	Expired bool   `json:"expired,omitempty"`
	Revoked bool   `json:"revoked,omitempty"`
}

// Verify checks a certificate's integrity, signature, expiry and revocation.
func (e *Engine) Verify(cert Certificate) CertificateStatus {
	sum := sha256.Sum256(cert.canonical())
	if hex.EncodeToString(sum[:]) != cert.Hash {
		return CertificateStatus{Reason: "the certificate content has been altered"}
	}
	// A key signature, when present, must verify. The HMAC is still checked
	// whenever a secret is configured, so certificates issued before a signer
	// was added keep verifying exactly as they did.
	if cert.KeySignature != nil {
		if e.Signer == nil || e.Signer.Verify(cert.canonical(), *cert.KeySignature) != nil {
			return CertificateStatus{Reason: "the signature is not valid"}
		}
	}
	if len(e.SigningKey) > 0 {
		mac := hmac.New(sha256.New, e.SigningKey)
		mac.Write(sum[:])
		expected := hex.EncodeToString(mac.Sum(nil))
		if !hmac.Equal([]byte(expected), []byte(cert.Signature)) {
			return CertificateStatus{Reason: "the signature is not valid"}
		}
	} else if e.Signer != nil && cert.KeySignature == nil {
		// Without a secret, an unsigned certificate is only a hash anyone
		// could have computed.
		return CertificateStatus{Reason: "the certificate is not signed"}
	}
	if cert.Revoked != nil {
		return CertificateStatus{Reason: "the certificate was revoked", Revoked: true}
	}
	if cert.ExpiresAt != nil && e.now().After(*cert.ExpiresAt) {
		return CertificateStatus{Reason: "the certificate has expired", Expired: true}
	}
	return CertificateStatus{Valid: true}
}

func randomID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}
