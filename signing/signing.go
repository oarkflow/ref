// Package signing holds asymmetric signing keys (Ed25519 and RSA with
// SHA-256), key sets for rotation, JWKS export and import, and canonical JSON.
//
// It is a leaf package so every layer that signs something — tokens in the
// platform, certificates in the pipeline, revisions in deploy — shares one
// implementation and one wire format, and a verifier holding only a JWKS
// document can check any of them offline.
//
// The rules that matter:
//
//   - The algorithm belongs to the key, never to the signature. A signature
//     that names another algorithm than its key's is refused, which is what
//     rules out algorithm confusion.
//   - A key set signs with exactly one active key and verifies with every key
//     it holds. Rotating means adding the new key as active and keeping the
//     old one for verification until what it signed has expired.
//   - Key ids default to the RFC 7638 JWK thumbprint, so two deployments that
//     load the same key agree on its id without coordination.
package signing

import (
	"bytes"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strings"
)

// Algorithm is a supported signature algorithm, named as in JOSE.
type Algorithm string

// The supported algorithms.
const (
	// EdDSA is Ed25519 (RFC 8032). Preferred: small keys, deterministic
	// signatures, no parameters to get wrong.
	EdDSA Algorithm = "EdDSA"
	// RS256 is RSASSA-PKCS1-v1_5 with SHA-256.
	RS256 Algorithm = "RS256"
	// PS256 is RSASSA-PSS with SHA-256 and a salt the size of the hash.
	PS256 Algorithm = "PS256"
)

// ErrInvalidSignature is the single error every verification failure maps to.
var ErrInvalidSignature = errors.New("signing: the signature is not valid")

var b64 = base64.RawURLEncoding

// ParseAlgorithm validates an algorithm name. The empty name is EdDSA.
func ParseAlgorithm(name string) (Algorithm, error) {
	switch strings.ToUpper(strings.TrimSpace(name)) {
	case "", "EDDSA", "ED25519":
		return EdDSA, nil
	case "RS256":
		return RS256, nil
	case "PS256":
		return PS256, nil
	default:
		return "", fmt.Errorf("signing: unsupported algorithm %q (use EdDSA, RS256 or PS256)", name)
	}
}

// Key is one signing or verification key with its algorithm fixed.
type Key struct {
	ID        string
	Algorithm Algorithm
	private   crypto.Signer
	public    crypto.PublicKey
}

// NewKey pairs key material (ed25519.PrivateKey/PublicKey or
// *rsa.PrivateKey/PublicKey) with an algorithm. An Ed25519 key is always
// EdDSA; an RSA key takes RS256 unless PS256 is asked for. An empty id
// becomes the key's JWK thumbprint.
func NewKey(id string, algorithm Algorithm, material any) (*Key, error) {
	k := &Key{ID: id}
	switch typed := material.(type) {
	case ed25519.PrivateKey:
		if len(typed) != ed25519.PrivateKeySize {
			return nil, errors.New("signing: malformed Ed25519 private key")
		}
		k.private, k.public = typed, typed.Public()
	case *ed25519.PrivateKey:
		return NewKey(id, algorithm, *typed)
	case ed25519.PublicKey:
		if len(typed) != ed25519.PublicKeySize {
			return nil, errors.New("signing: malformed Ed25519 public key")
		}
		k.public = typed
	case *rsa.PrivateKey:
		k.private, k.public = typed, &typed.PublicKey
	case *rsa.PublicKey:
		k.public = typed
	default:
		return nil, fmt.Errorf("signing: unsupported key type %T (use Ed25519 or RSA)", material)
	}
	switch pub := k.public.(type) {
	case ed25519.PublicKey:
		if algorithm != "" && algorithm != EdDSA {
			return nil, fmt.Errorf("signing: an Ed25519 key signs with EdDSA, not %s", algorithm)
		}
		k.Algorithm = EdDSA
	case *rsa.PublicKey:
		if pub.N.BitLen() < 2048 {
			return nil, fmt.Errorf("signing: an RSA key needs at least 2048 bits, got %d", pub.N.BitLen())
		}
		switch algorithm {
		case "", RS256:
			k.Algorithm = RS256
		case PS256:
			k.Algorithm = PS256
		default:
			return nil, fmt.Errorf("signing: an RSA key signs with RS256 or PS256, not %s", algorithm)
		}
	}
	if k.ID == "" {
		k.ID = k.Thumbprint()[:16]
	}
	return k, nil
}

// GenerateEd25519 creates a fresh Ed25519 signing key.
func GenerateEd25519(id string) (*Key, error) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	return NewKey(id, EdDSA, private)
}

// ParsePrivateKeyPEM reads a PKCS#8 (Ed25519 or RSA) or PKCS#1 (RSA) key.
func ParsePrivateKeyPEM(id string, algorithm Algorithm, data []byte) (*Key, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("signing: no PEM block found in the private key")
	}
	if parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		return NewKey(id, algorithm, parsed)
	}
	if parsed, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return NewKey(id, algorithm, parsed)
	}
	return nil, errors.New("signing: the private key is not a PKCS#8 Ed25519/RSA or PKCS#1 RSA key")
}

// ParsePublicKeyPEM reads a PKIX public key, a PKCS#1 RSA public key or the
// public key of an X.509 certificate.
func ParsePublicKeyPEM(id string, algorithm Algorithm, data []byte) (*Key, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("signing: no PEM block found in the public key")
	}
	if parsed, err := x509.ParsePKIXPublicKey(block.Bytes); err == nil {
		return NewKey(id, algorithm, parsed)
	}
	if parsed, err := x509.ParsePKCS1PublicKey(block.Bytes); err == nil {
		return NewKey(id, algorithm, parsed)
	}
	if certificate, err := x509.ParseCertificate(block.Bytes); err == nil {
		return NewKey(id, algorithm, certificate.PublicKey)
	}
	return nil, errors.New("signing: the public key is not a PKIX, PKCS#1 or certificate PEM")
}

// ParseKeyPEM reads either a private or a public key.
func ParseKeyPEM(id string, algorithm Algorithm, data []byte) (*Key, error) {
	if k, err := ParsePrivateKeyPEM(id, algorithm, data); err == nil {
		return k, nil
	}
	return ParsePublicKeyPEM(id, algorithm, data)
}

// MarshalPrivateKeyPEM encodes a key's private half as PKCS#8 PEM.
func (k *Key) MarshalPrivateKeyPEM() ([]byte, error) {
	if k.private == nil {
		return nil, errors.New("signing: the key holds no private material")
	}
	der, err := x509.MarshalPKCS8PrivateKey(k.private)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), nil
}

// MarshalPublicKeyPEM encodes a key's public half as PKIX PEM.
func (k *Key) MarshalPublicKeyPEM() ([]byte, error) {
	der, err := x509.MarshalPKIXPublicKey(k.public)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), nil
}

// CanSign reports whether the key holds private material.
func (k *Key) CanSign() bool { return k != nil && k.private != nil }

// Public returns the public key (ed25519.PublicKey or *rsa.PublicKey).
func (k *Key) Public() crypto.PublicKey { return k.public }

// Private returns the private key, or nil for a verification-only key.
func (k *Key) Private() crypto.Signer { return k.private }

// PublicOnly returns a copy without private material.
func (k *Key) PublicOnly() *Key {
	return &Key{ID: k.ID, Algorithm: k.Algorithm, public: k.public}
}

// Sign signs payload with the key's algorithm.
func (k *Key) Sign(payload []byte) ([]byte, error) {
	if !k.CanSign() {
		return nil, errors.New("signing: the key holds no private material")
	}
	switch k.Algorithm {
	case EdDSA:
		return ed25519.Sign(k.private.(ed25519.PrivateKey), payload), nil
	case RS256:
		digest := sha256.Sum256(payload)
		return rsa.SignPKCS1v15(rand.Reader, k.private.(*rsa.PrivateKey), crypto.SHA256, digest[:])
	case PS256:
		digest := sha256.Sum256(payload)
		return rsa.SignPSS(rand.Reader, k.private.(*rsa.PrivateKey), crypto.SHA256, digest[:],
			&rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash})
	default:
		return nil, fmt.Errorf("signing: unsupported algorithm %q", k.Algorithm)
	}
}

// Verify reports whether signature is the key's signature over payload.
func (k *Key) Verify(payload, signature []byte) bool {
	if k == nil {
		return false
	}
	switch k.Algorithm {
	case EdDSA:
		pub, ok := k.public.(ed25519.PublicKey)
		return ok && len(signature) == ed25519.SignatureSize && ed25519.Verify(pub, payload, signature)
	case RS256:
		pub, ok := k.public.(*rsa.PublicKey)
		digest := sha256.Sum256(payload)
		return ok && rsa.VerifyPKCS1v15(pub, crypto.SHA256, digest[:], signature) == nil
	case PS256:
		pub, ok := k.public.(*rsa.PublicKey)
		digest := sha256.Sum256(payload)
		return ok && rsa.VerifyPSS(pub, crypto.SHA256, digest[:], signature,
			&rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash}) == nil
	default:
		return false
	}
}

// ---------------------------------------------------------------------------
// JWK
// ---------------------------------------------------------------------------

// JWK is a public JSON Web Key (RFC 7517; OKP keys per RFC 8037).
type JWK struct {
	Kty string `json:"kty"`
	Kid string `json:"kid,omitempty"`
	Alg string `json:"alg,omitempty"`
	Use string `json:"use,omitempty"`
	Crv string `json:"crv,omitempty"`
	X   string `json:"x,omitempty"`
	N   string `json:"n,omitempty"`
	E   string `json:"e,omitempty"`
}

// JWKS is a JSON Web Key Set.
type JWKS struct {
	Keys []JWK `json:"keys"`
}

// JWK renders the key's public half.
func (k *Key) JWK() JWK {
	out := JWK{Kid: k.ID, Alg: string(k.Algorithm), Use: "sig"}
	switch pub := k.public.(type) {
	case ed25519.PublicKey:
		out.Kty, out.Crv, out.X = "OKP", "Ed25519", b64.EncodeToString(pub)
	case *rsa.PublicKey:
		out.Kty = "RSA"
		out.N = b64.EncodeToString(pub.N.Bytes())
		out.E = b64.EncodeToString(big.NewInt(int64(pub.E)).Bytes())
	}
	return out
}

// Thumbprint is the RFC 7638 SHA-256 JWK thumbprint, base64url encoded.
func (k *Key) Thumbprint() string {
	j := k.JWK()
	var members string
	switch j.Kty {
	case "OKP":
		members = fmt.Sprintf(`{"crv":%q,"kty":"OKP","x":%q}`, j.Crv, j.X)
	default:
		members = fmt.Sprintf(`{"e":%q,"kty":"RSA","n":%q}`, j.E, j.N)
	}
	sum := sha256.Sum256([]byte(members))
	return b64.EncodeToString(sum[:])
}

// KeyFromJWK converts a public JWK. Keys this package does not support
// return an error.
func KeyFromJWK(j JWK) (*Key, error) {
	if j.Use != "" && j.Use != "sig" {
		return nil, fmt.Errorf("signing: key %q is not a signing key (use %q)", j.Kid, j.Use)
	}
	switch j.Kty {
	case "OKP":
		if j.Crv != "Ed25519" {
			return nil, fmt.Errorf("signing: unsupported OKP curve %q", j.Crv)
		}
		x, err := b64.DecodeString(j.X)
		if err != nil {
			return nil, fmt.Errorf("signing: key %q: %w", j.Kid, err)
		}
		return NewKey(j.Kid, EdDSA, ed25519.PublicKey(x))
	case "RSA":
		n, err := b64.DecodeString(j.N)
		if err != nil {
			return nil, fmt.Errorf("signing: key %q: %w", j.Kid, err)
		}
		e, err := b64.DecodeString(j.E)
		if err != nil {
			return nil, fmt.Errorf("signing: key %q: %w", j.Kid, err)
		}
		alg, err := ParseAlgorithm(j.Alg)
		if err != nil || alg == EdDSA {
			alg = RS256
		}
		return NewKey(j.Kid, alg, &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())})
	default:
		return nil, fmt.Errorf("signing: unsupported key type %q", j.Kty)
	}
}

// ---------------------------------------------------------------------------
// Key sets
// ---------------------------------------------------------------------------

// Signature is a detached signature and the key that made it.
type Signature struct {
	Algorithm string `json:"alg"`
	KeyID     string `json:"kid"`
	// Value is the base64url (unpadded) signature.
	Value string `json:"sig"`
}

// KeySet signs with its active key and verifies with any key it holds.
type KeySet struct {
	active *Key
	keys   map[string]*Key
	order  []string
}

// NewKeySet builds a set. active may be nil for a verification-only set; it
// is also a verification key. Two keys with the same id are refused.
func NewKeySet(active *Key, verification ...*Key) (*KeySet, error) {
	s := &KeySet{active: active, keys: map[string]*Key{}}
	all := verification
	if active != nil {
		if !active.CanSign() {
			return nil, fmt.Errorf("signing: the active key %q holds no private material", active.ID)
		}
		all = append([]*Key{active}, verification...)
	}
	for _, k := range all {
		if k == nil {
			continue
		}
		if _, dup := s.keys[k.ID]; dup {
			return nil, fmt.Errorf("signing: duplicate key id %q", k.ID)
		}
		s.keys[k.ID] = k
		s.order = append(s.order, k.ID)
	}
	if len(s.keys) == 0 {
		return nil, errors.New("signing: a key set needs at least one key")
	}
	return s, nil
}

// ParseJWKS builds a verification-only set from a JWKS document, skipping
// keys it does not support.
func ParseJWKS(data []byte) (*KeySet, error) {
	var doc JWKS
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("signing: parse JWKS: %w", err)
	}
	var keys []*Key
	for _, j := range doc.Keys {
		if k, err := KeyFromJWK(j); err == nil {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return nil, errors.New("signing: the JWKS document holds no usable signing key")
	}
	return NewKeySet(nil, keys...)
}

// Active is the signing key (nil for a verification-only set).
func (s *KeySet) Active() *Key { return s.active }

// Key returns the key with id.
func (s *KeySet) Key(id string) (*Key, bool) {
	k, ok := s.keys[id]
	return k, ok
}

// Keys returns every key, the active one first.
func (s *KeySet) Keys() []*Key {
	out := make([]*Key, 0, len(s.order))
	for _, id := range s.order {
		out = append(out, s.keys[id])
	}
	return out
}

// JWKS renders every key's public half.
func (s *KeySet) JWKS() JWKS {
	out := JWKS{Keys: make([]JWK, 0, len(s.order))}
	for _, k := range s.Keys() {
		out.Keys = append(out.Keys, k.JWK())
	}
	return out
}

// Sign signs payload with the active key.
func (s *KeySet) Sign(payload []byte) (Signature, error) {
	if s.active == nil {
		return Signature{}, errors.New("signing: the key set has no active signing key")
	}
	raw, err := s.active.Sign(payload)
	if err != nil {
		return Signature{}, err
	}
	return Signature{Algorithm: string(s.active.Algorithm), KeyID: s.active.ID, Value: b64.EncodeToString(raw)}, nil
}

// Verify checks sig over payload. The key is chosen by id, and its own
// algorithm must match the one the signature names.
func (s *KeySet) Verify(payload []byte, sig Signature) error {
	k, ok := s.keys[sig.KeyID]
	if !ok || string(k.Algorithm) != sig.Algorithm {
		return ErrInvalidSignature
	}
	raw, err := b64.DecodeString(strings.TrimRight(sig.Value, "="))
	if err != nil || !k.Verify(payload, raw) {
		return ErrInvalidSignature
	}
	return nil
}

// SignJSON signs the canonical JSON of v.
func (s *KeySet) SignJSON(v any) (Signature, error) {
	payload, err := Canonical(v)
	if err != nil {
		return Signature{}, err
	}
	return s.Sign(payload)
}

// VerifyJSON verifies sig over the canonical JSON of v.
func (s *KeySet) VerifyJSON(v any, sig Signature) error {
	payload, err := Canonical(v)
	if err != nil {
		return ErrInvalidSignature
	}
	return s.Verify(payload, sig)
}

// ---------------------------------------------------------------------------
// Canonical JSON
// ---------------------------------------------------------------------------

// Canonical renders v as canonical JSON: object keys sorted by their UTF-8
// bytes, no insignificant whitespace, no HTML escaping, numbers as written.
// Two values that are equal as JSON produce the same bytes, which is what a
// signature over "this object" needs. (It follows RFC 8785 except that
// numbers keep their decimal text rather than being re-serialised as IEEE
// doubles, which only differs for numbers that do not round-trip.)
func Canonical(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var generic any
	if err := decoder.Decode(&generic); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := writeCanonical(&out, generic); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func writeCanonical(out *bytes.Buffer, v any) error {
	switch typed := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for k := range typed {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				out.WriteByte(',')
			}
			if err := writeString(out, k); err != nil {
				return err
			}
			out.WriteByte(':')
			if err := writeCanonical(out, typed[k]); err != nil {
				return err
			}
		}
		out.WriteByte('}')
	case []any:
		out.WriteByte('[')
		for i, item := range typed {
			if i > 0 {
				out.WriteByte(',')
			}
			if err := writeCanonical(out, item); err != nil {
				return err
			}
		}
		out.WriteByte(']')
	case string:
		return writeString(out, typed)
	case json.Number:
		out.WriteString(typed.String())
	case bool:
		if typed {
			out.WriteString("true")
		} else {
			out.WriteString("false")
		}
	case nil:
		out.WriteString("null")
	default:
		return fmt.Errorf("signing: cannot canonicalise %T", v)
	}
	return nil
}

func writeString(out *bytes.Buffer, s string) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		return err
	}
	out.Write(bytes.TrimRight(buf.Bytes(), "\n"))
	return nil
}
