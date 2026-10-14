package runtime

import (
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"

	"github.com/oarkflow/ref/aegis/vm"
	"github.com/oarkflow/ref/signing"
)

// OfflinePolicy defines authorization caching and attestation requirements when operating
// disconnected from the central grant authority.
type OfflinePolicy struct {
	// AllowCachedGrants permits reusing an unexpired, cached grant without contacting the grant provider.
	AllowCachedGrants bool `json:"allow_cached_grants"`

	// MaxCacheAge specifies the maximum allowed age of a cached grant in seconds.
	MaxCacheAge int64 `json:"max_cache_age"`

	// RequireAttestation specifies whether attestation evidence must be verified for offline execution.
	RequireAttestation bool `json:"require_attestation"`
}

// Config specifies the runtime operating parameters, trusted verification keys,
// virtual machine configuration, and authorization providers.
type Config struct {
	// VM specifies virtual machine bounds, execution fuel limits, and memory capacity.
	VM vm.Config

	// TrustedKeys contains public keys trusted to verify package manifest signatures.
	TrustedKeys *signing.KeySet

	// Capabilities declares the platform capabilities provided by the host environment (e.g. "crypto", "storage").
	Capabilities []string

	// GrantProvider resolves authorization requests and delivers session decryption keys.
	GrantProvider GrantProvider

	// Offline defines the offline grant reuse and attestation policy.
	Offline OfflinePolicy

	// HostFuncs provides initial custom host functions exposed to guest execution.
	HostFuncs []vm.HostFunc

	// Metrics optionally records audit and telemetry events for package execution.
	Metrics MetricsCollector
}

// DefaultConfig returns a default Config with standard VM bounds and safe offline policy defaults.
func DefaultConfig() Config {
	return Config{
		VM: vm.DefaultConfig(),
		Offline: OfflinePolicy{
			AllowCachedGrants:  false,
			MaxCacheAge:        3600, // 1 hour
			RequireAttestation: false,
		},
	}
}

// LoadTrustedKeysJWKS parses a JWKS JSON document and sets the trusted verification keys.
func (cfg *Config) LoadTrustedKeysJWKS(jwksData []byte) error {
	keySet, err := signing.ParseJWKS(jwksData)
	if err != nil {
		return fmt.Errorf("runtime: failed to parse trusted JWKS: %w", err)
	}
	cfg.TrustedKeys = keySet
	return nil
}

// LoadTrustedKeysPEM parses a PEM-encoded public key and sets the trusted verification keys.
func (cfg *Config) LoadTrustedKeysPEM(keyID string, algorithm signing.Algorithm, pemData []byte) error {
	key, err := signing.ParsePublicKeyPEM(keyID, algorithm, pemData)
	if err != nil {
		return fmt.Errorf("runtime: failed to parse trusted public key PEM: %w", err)
	}
	keySet, err := signing.NewKeySet(nil, key)
	if err != nil {
		return fmt.Errorf("runtime: failed to create key set from PEM: %w", err)
	}
	cfg.TrustedKeys = keySet
	return nil
}

// ParseTrustedKeysFromPEM parses multiple concatenated PEM blocks and returns a KeySet.
func ParseTrustedKeysFromPEM(pemData []byte) (*signing.KeySet, error) {
	var keys []*signing.Key
	rest := pemData
	keyIndex := 0

	for len(rest) > 0 {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}

		keyIndex++
		keyID := fmt.Sprintf("key-%d", keyIndex)

		// Try parsing as PKIX public key
		if pub, err := x509.ParsePKIXPublicKey(block.Bytes); err == nil {
			if k, err := signing.NewKey(keyID, signing.EdDSA, pub); err == nil {
				keys = append(keys, k)
				continue
			} else if k, err := signing.NewKey(keyID, signing.RS256, pub); err == nil {
				keys = append(keys, k)
				continue
			}
		}

		// Try parsing as PKCS#1 public key
		if pub, err := x509.ParsePKCS1PublicKey(block.Bytes); err == nil {
			if k, err := signing.NewKey(keyID, signing.RS256, pub); err == nil {
				keys = append(keys, k)
				continue
			}
		}

		// Try parsing as X.509 certificate
		if cert, err := x509.ParseCertificate(block.Bytes); err == nil {
			if k, err := signing.NewKey(keyID, signing.RS256, cert.PublicKey); err == nil {
				keys = append(keys, k)
				continue
			} else if k, err := signing.NewKey(keyID, signing.EdDSA, cert.PublicKey); err == nil {
				keys = append(keys, k)
				continue
			}
		}
	}

	if len(keys) == 0 {
		return nil, errors.New("runtime: no usable public keys found in PEM data")
	}

	return signing.NewKeySet(nil, keys...)
}

// Validate checks the configuration for required fields and logical consistency.
func (cfg *Config) Validate() error {
	if cfg.TrustedKeys == nil {
		return errors.New("runtime: trusted keys must be provided")
	}
	if cfg.GrantProvider == nil {
		return errors.New("runtime: grant provider must be configured")
	}
	return nil
}
