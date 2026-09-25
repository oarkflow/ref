package capability

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/hmac"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/subtle"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"hash"
	"math/big"
	"strings"
	"time"

	"github.com/oarkflow/ref/invocation"
)

// JWT verification for the capability layer.
//
// This mirrors the verification discipline already established in
// platform/jwt.go (the algorithm is fixed by the configured key, never by the
// token's own header; "none" is rejected outright; HMAC comparison is
// constant-time; exp/nbf/iat are checked with bounded clock skew). It is
// duplicated rather than imported because platform/jwt.go's types are
// package-private to platform and platform is a separate, higher-level
// package that this module must not depend on from capability/. No new
// third-party dependency is introduced — only the standard library crypto
// packages, exactly as platform/jwt.go uses them.

var jwtBase64 = base64.RawURLEncoding

// JWTAlgorithm is a supported JWS signing algorithm.
type JWTAlgorithm string

// Supported algorithms. "none" is deliberately not represented here.
const (
	JWTAlgHS256 JWTAlgorithm = "HS256"
	JWTAlgHS384 JWTAlgorithm = "HS384"
	JWTAlgHS512 JWTAlgorithm = "HS512"
	JWTAlgRS256 JWTAlgorithm = "RS256"
	JWTAlgRS384 JWTAlgorithm = "RS384"
	JWTAlgRS512 JWTAlgorithm = "RS512"
	JWTAlgES256 JWTAlgorithm = "ES256"
	JWTAlgES384 JWTAlgorithm = "ES384"
	JWTAlgES512 JWTAlgorithm = "ES512"
)

// ErrInvalidToken is returned (wrapped) for every JWT verification failure.
// It is deliberately opaque about which check failed, for the same reason
// platform/jwt.go's ErrTokenInvalid is opaque: telling an untrusted caller
// exactly why their token failed is an oracle.
var ErrInvalidToken = errors.New("ref: invalid token")

// JWTKey is one configured verification (and optionally signing) key, with
// its algorithm fixed at construction time.
type JWTKey struct {
	ID        string
	Algorithm JWTAlgorithm
	secret    []byte
	rsaPublic *rsa.PublicKey
	ecPublic  *ecdsa.PublicKey
}

// NewJWTHMACKey builds a symmetric verification key. A minimum secret length
// is enforced so a short, brute-forceable secret cannot be configured by
// accident.
func NewJWTHMACKey(algorithm JWTAlgorithm, secret []byte) (*JWTKey, error) {
	switch algorithm {
	case JWTAlgHS256, JWTAlgHS384, JWTAlgHS512:
	default:
		return nil, fmt.Errorf("%s is not an HMAC algorithm", algorithm)
	}
	if len(secret) < 32 {
		return nil, fmt.Errorf("an HMAC signing secret must contain at least 32 bytes, got %d", len(secret))
	}
	return &JWTKey{Algorithm: algorithm, secret: secret}, nil
}

// NewJWTPublicKeyPEM parses a PEM-encoded RSA or ECDSA public key (or
// certificate) for verification.
func NewJWTPublicKeyPEM(algorithm JWTAlgorithm, data []byte) (*JWTKey, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("no PEM block found in the configured public key")
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		cert, certErr := x509.ParseCertificate(block.Bytes)
		if certErr != nil {
			return nil, fmt.Errorf("parse public key: %w", err)
		}
		parsed = cert.PublicKey
	}
	key := &JWTKey{Algorithm: algorithm}
	switch typed := parsed.(type) {
	case *rsa.PublicKey:
		if !strings.HasPrefix(string(algorithm), "RS") {
			return nil, fmt.Errorf("%s cannot verify with an RSA key", algorithm)
		}
		key.rsaPublic = typed
	case *ecdsa.PublicKey:
		if !strings.HasPrefix(string(algorithm), "ES") {
			return nil, fmt.Errorf("%s cannot verify with an ECDSA key", algorithm)
		}
		key.ecPublic = typed
	default:
		return nil, fmt.Errorf("unsupported public key type %T", parsed)
	}
	return key, nil
}

// JWTClaims is the registered claim set plus whatever else the token carried.
type JWTClaims struct {
	Issuer    string
	Subject   string
	Audience  []string
	ExpiresAt int64
	NotBefore int64
	IssuedAt  int64
	ID        string
	Extra     map[string]any
}

// JWTAuthenticatorConfig configures NewJWTAuthenticator.
type JWTAuthenticatorConfig struct {
	// Keys verifies a token. A token with a "kid" header selects that key by
	// ID; a token without one is tried against every key, which is what lets
	// a deployment rotate a symmetric secret without a flag day.
	Keys map[string]*JWTKey

	// Issuer and Audience must match the token's claims when set.
	Issuer   string
	Audience string

	// Skew tolerates clock drift between issuer and this host. Default 60s.
	Skew time.Duration

	// AllowMissingExpiry permits a token with no exp claim. Defaults to
	// false: a JWT without an expiry is a bearer credential that lives
	// forever, and that should be a deliberate opt-in.
	AllowMissingExpiry bool

	// Now overrides the clock, for tests.
	Now func() time.Time

	// RolesClaim names the claim carrying roles (default "roles").
	RolesClaim string
	// ScopesClaim names the claim carrying scopes (default "scope", which may
	// be a space-delimited string per OAuth2, or "scopes" as a string list).
	ScopesClaim string
	// UsernameClaim names the claim carrying a human-readable username
	// (default "preferred_username", falling back to "sub" when absent).
	UsernameClaim string

	// ExtractToken pulls the raw bearer token out of the PrincipalHint. The
	// default reads hint.BearerToken.
	ExtractToken func(hint invocation.PrincipalHint) (string, error)
}

// NewJWTAuthenticator builds an AuthenticatorFunc that verifies a JWT carried
// in the invocation's PrincipalHint and produces a PrincipalFact from its
// claims. This is the recommended default authenticator for any ref
// application that authenticates callers with bearer JWTs: wire it into
// NewAuthCapability directly.
//
//	jwtAuth := capability.NewJWTAuthenticator(capability.JWTAuthenticatorConfig{
//	    Keys: map[string]*capability.JWTKey{"": hmacKey},
//	})
//	reg := capability.NewAuthCapability("auth.jwt", jwtAuth)
func NewJWTAuthenticator(cfg JWTAuthenticatorConfig) AuthenticatorFunc {
	extract := cfg.ExtractToken
	if extract == nil {
		extract = func(hint invocation.PrincipalHint) (string, error) {
			if hint.BearerToken == "" {
				return "", fmt.Errorf("%w: no bearer token presented", ErrUnauthenticated)
			}
			return hint.BearerToken, nil
		}
	}
	rolesClaim := cfg.RolesClaim
	if rolesClaim == "" {
		rolesClaim = "roles"
	}
	scopesClaim := cfg.ScopesClaim
	if scopesClaim == "" {
		scopesClaim = "scope"
	}
	usernameClaim := cfg.UsernameClaim
	if usernameClaim == "" {
		usernameClaim = "preferred_username"
	}

	return func(hint invocation.PrincipalHint) (PrincipalFact, error) {
		token, err := extract(hint)
		if err != nil {
			return PrincipalFact{}, err
		}
		if len(cfg.Keys) == 0 {
			return PrincipalFact{}, fmt.Errorf("%w: no verification keys configured", ErrUnauthenticated)
		}

		claims, body, err := verifyJWT(token, cfg.Keys, jwtVerifyOptions{
			Issuer:             cfg.Issuer,
			Audience:           cfg.Audience,
			Skew:               cfg.Skew,
			AllowMissingExpiry: cfg.AllowMissingExpiry,
			Now:                cfg.Now,
		})
		if err != nil {
			return PrincipalFact{}, fmt.Errorf("%w: %v", ErrUnauthenticated, err)
		}

		principal := PrincipalFact{
			ID:     claims.Subject,
			Roles:  extractStringList(body, rolesClaim),
			Scopes: extractStringList(body, scopesClaim),
			Claims: body,
		}
		if username, ok := body[usernameClaim].(string); ok && username != "" {
			principal.Username = username
		} else {
			principal.Username = claims.Subject
		}
		if principal.ID == "" {
			return PrincipalFact{}, fmt.Errorf("%w: token has no subject claim", ErrUnauthenticated)
		}
		return principal, nil
	}
}

// extractStringList reads a claim that may be a JSON string array, or a
// single space-delimited string (as OAuth2 "scope" typically is).
func extractStringList(body map[string]any, claim string) []string {
	value, ok := body[claim]
	if !ok {
		return nil
	}
	switch typed := value.(type) {
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if text, ok := item.(string); ok && text != "" {
				out = append(out, text)
			}
		}
		return out
	case []string:
		return typed
	case string:
		if typed == "" {
			return nil
		}
		return strings.Fields(typed)
	default:
		return nil
	}
}

// jwtVerifyOptions is what verifyJWT checks beyond the signature.
type jwtVerifyOptions struct {
	Issuer             string
	Audience           string
	Skew               time.Duration
	AllowMissingExpiry bool
	Now                func() time.Time
}

// verifyJWT verifies a compact JWS token against one of the supplied keys and
// returns its registered claims plus the raw claim body. It follows the same
// verification discipline as platform.VerifyJWT: the algorithm used to verify
// always comes from the configured key, never trusted from the token header,
// and "none" is rejected unconditionally.
func verifyJWT(token string, keys map[string]*JWTKey, opts jwtVerifyOptions) (JWTClaims, map[string]any, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return JWTClaims{}, nil, ErrInvalidToken
	}
	headerJSON, err := jwtBase64.DecodeString(parts[0])
	if err != nil {
		return JWTClaims{}, nil, ErrInvalidToken
	}
	var header struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := json.Unmarshal(headerJSON, &header); err != nil {
		return JWTClaims{}, nil, ErrInvalidToken
	}
	if strings.EqualFold(header.Alg, "none") || header.Alg == "" {
		return JWTClaims{}, nil, ErrInvalidToken
	}

	signature, err := jwtBase64.DecodeString(parts[2])
	if err != nil {
		return JWTClaims{}, nil, ErrInvalidToken
	}
	signingInput := []byte(parts[0] + "." + parts[1])

	candidates := make([]*JWTKey, 0, len(keys))
	if header.Kid != "" {
		if key, ok := keys[header.Kid]; ok {
			candidates = append(candidates, key)
		}
	}
	if len(candidates) == 0 {
		for _, key := range keys {
			candidates = append(candidates, key)
		}
	}
	verified := false
	for _, key := range candidates {
		if key == nil || !strings.EqualFold(string(key.Algorithm), header.Alg) {
			continue
		}
		if verifyJWTSignature(key, signingInput, signature) {
			verified = true
			break
		}
	}
	if !verified {
		return JWTClaims{}, nil, ErrInvalidToken
	}

	bodyJSON, err := jwtBase64.DecodeString(parts[1])
	if err != nil {
		return JWTClaims{}, nil, ErrInvalidToken
	}
	var body map[string]any
	if err := json.Unmarshal(bodyJSON, &body); err != nil {
		return JWTClaims{}, nil, ErrInvalidToken
	}
	claims := jwtClaimsFrom(body)
	if err := validateJWTClaims(claims, opts); err != nil {
		return JWTClaims{}, nil, err
	}
	return claims, body, nil
}

func jwtClaimsFrom(body map[string]any) JWTClaims {
	claims := JWTClaims{Extra: body}
	claims.Issuer, _ = body["iss"].(string)
	claims.Subject, _ = body["sub"].(string)
	claims.ID, _ = body["jti"].(string)
	if value, ok := toFloat(body["exp"]); ok {
		claims.ExpiresAt = int64(value)
	}
	if value, ok := toFloat(body["nbf"]); ok {
		claims.NotBefore = int64(value)
	}
	if value, ok := toFloat(body["iat"]); ok {
		claims.IssuedAt = int64(value)
	}
	switch audience := body["aud"].(type) {
	case string:
		if audience != "" {
			claims.Audience = []string{audience}
		}
	case []any:
		for _, item := range audience {
			if text, ok := item.(string); ok {
				claims.Audience = append(claims.Audience, text)
			}
		}
	}
	return claims
}

func toFloat(v any) (float64, bool) {
	switch typed := v.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case json.Number:
		f, err := typed.Float64()
		return f, err == nil
	default:
		return 0, false
	}
}

func validateJWTClaims(claims JWTClaims, opts jwtVerifyOptions) error {
	now := time.Now()
	if opts.Now != nil {
		now = opts.Now()
	}
	skew := opts.Skew
	if skew <= 0 {
		skew = time.Minute
	}
	if claims.ExpiresAt == 0 && !opts.AllowMissingExpiry {
		return ErrInvalidToken
	}
	if claims.ExpiresAt != 0 && now.Add(-skew).Unix() >= claims.ExpiresAt {
		return ErrInvalidToken
	}
	if claims.NotBefore != 0 && now.Add(skew).Unix() < claims.NotBefore {
		return ErrInvalidToken
	}
	if claims.IssuedAt != 0 && now.Add(skew).Unix() < claims.IssuedAt {
		return ErrInvalidToken
	}
	if opts.Issuer != "" && claims.Issuer != opts.Issuer {
		return ErrInvalidToken
	}
	if opts.Audience != "" {
		matched := false
		for _, audience := range claims.Audience {
			if audience == opts.Audience {
				matched = true
				break
			}
		}
		if !matched {
			return ErrInvalidToken
		}
	}
	return nil
}

func verifyJWTSignature(key *JWTKey, input, signature []byte) bool {
	switch key.Algorithm {
	case JWTAlgHS256, JWTAlgHS384, JWTAlgHS512:
		if len(key.secret) == 0 {
			return false
		}
		mac := hmac.New(jwtHash(key.Algorithm), key.secret)
		mac.Write(input)
		return subtle.ConstantTimeCompare(mac.Sum(nil), signature) == 1
	case JWTAlgRS256, JWTAlgRS384, JWTAlgRS512:
		if key.rsaPublic == nil {
			return false
		}
		digest, cryptoHash := jwtDigest(key.Algorithm, input)
		return rsa.VerifyPKCS1v15(key.rsaPublic, cryptoHash, digest, signature) == nil
	case JWTAlgES256, JWTAlgES384, JWTAlgES512:
		if key.ecPublic == nil {
			return false
		}
		size := (key.ecPublic.Curve.Params().BitSize + 7) / 8
		if len(signature) != 2*size {
			return false
		}
		digest, _ := jwtDigest(key.Algorithm, input)
		r := new(big.Int).SetBytes(signature[:size])
		s := new(big.Int).SetBytes(signature[size:])
		return ecdsa.Verify(key.ecPublic, digest, r, s)
	default:
		return false
	}
}

func jwtHash(algorithm JWTAlgorithm) func() hash.Hash {
	switch algorithm {
	case JWTAlgHS384, JWTAlgRS384, JWTAlgES384:
		return sha512.New384
	case JWTAlgHS512, JWTAlgRS512, JWTAlgES512:
		return sha512.New
	default:
		return sha256.New
	}
}

func jwtDigest(algorithm JWTAlgorithm, input []byte) ([]byte, crypto.Hash) {
	switch algorithm {
	case JWTAlgRS384, JWTAlgES384:
		sum := sha512.Sum384(input)
		return sum[:], crypto.SHA384
	case JWTAlgRS512, JWTAlgES512:
		sum := sha512.Sum512(input)
		return sum[:], crypto.SHA512
	default:
		sum := sha256.Sum256(input)
		return sum[:], crypto.SHA256
	}
}
