package control

import (
	"context"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
)

// KeyManager coordinates storage, retrieval, and ephemeral derivation of master package symmetric keys.
//
// In the Aegis security architecture, master keys are kept exclusively within the control authority
// or hardware security modules and are NEVER transmitted to clients. Instead, client runtimes
// receive ephemeral, short-lived session keys derived via HKDF-SHA256 bound to a specific session ID
// and package version.
type KeyManager struct {
	store Store
}

// NewKeyManager initializes a KeyManager backed by the provided Store.
func NewKeyManager(store Store) *KeyManager {
	return &KeyManager{store: store}
}

// StoreMasterKey validates and securely stores a 32-byte AES-256 master symmetric key for a package.
func (km *KeyManager) StoreMasterKey(ctx context.Context, packageID string, key []byte) error {
	if packageID == "" {
		return errors.New("control: package id cannot be empty")
	}
	if len(key) != 32 {
		return fmt.Errorf("control: master key must be 32 bytes, got %d", len(key))
	}
	if km.store == nil {
		return errors.New("control: store is not configured in key manager")
	}
	return km.store.StoreMasterKey(ctx, packageID, key)
}

// GetMasterKey retrieves the 32-byte master symmetric key associated with the package ID.
func (km *KeyManager) GetMasterKey(ctx context.Context, packageID string) ([]byte, error) {
	if packageID == "" {
		return nil, errors.New("control: package id cannot be empty")
	}
	if km.store == nil {
		return nil, errors.New("control: store is not configured in key manager")
	}
	return km.store.GetMasterKey(ctx, packageID)
}

// GenerateMasterKey creates a fresh, cryptographically secure 32-byte AES-256 master key.
func (km *KeyManager) GenerateMasterKey() ([]byte, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("control: failed to generate master key: %w", err)
	}
	return key, nil
}

// DeriveSessionKeyForPackage retrieves the master key for the specified package and derives
// an ephemeral 32-byte session decryption key bound to the session ID and package version.
func (km *KeyManager) DeriveSessionKeyForPackage(ctx context.Context, packageID, sessionID, packageVersion string) ([]byte, error) {
	masterKey, err := km.GetMasterKey(ctx, packageID)
	if err != nil {
		return nil, fmt.Errorf("control: failed to load master key: %w", err)
	}
	return km.DeriveSessionKey(masterKey, sessionID, packageVersion)
}

// DeriveSessionKey delegates to the package-level DeriveSessionKey function.
func (km *KeyManager) DeriveSessionKey(masterKey []byte, sessionID, packageVersion string) ([]byte, error) {
	return DeriveSessionKey(masterKey, sessionID, packageVersion)
}

// DeriveSessionKey derives a unique 32-byte ephemeral session decryption key using
// standard library crypto/hkdf with HMAC-SHA256.
//
// It binds the derivation to the package version (as salt) and the unique session ID (as context info).
// Ephemeral keys ensure that compromising one session key does not compromise past or future sessions
// or the underlying master package key.
func DeriveSessionKey(masterKey []byte, sessionID, packageVersion string) ([]byte, error) {
	if len(masterKey) != 32 {
		return nil, fmt.Errorf("control: invalid master key size (expected 32 bytes, got %d)", len(masterKey))
	}
	if sessionID == "" {
		return nil, errors.New("control: session id cannot be empty for key derivation")
	}

	// Salt is derived from package version to isolate different package releases
	salt := []byte(packageVersion)
	if len(salt) == 0 {
		salt = []byte("aegis-default-salt")
	}

	// Info context string binds the key to the specific session ID and version
	info := fmt.Sprintf("aegis-session-key:%s:%s", sessionID, packageVersion)

	sessionKey, err := hkdf.Key(sha256.New, masterKey, salt, info, 32)
	if err != nil {
		return nil, fmt.Errorf("control: key derivation failed: %w", err)
	}

	return sessionKey, nil
}
