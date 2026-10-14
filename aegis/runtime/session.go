package runtime

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"
)

// GrantProvider negotiates authorization requests with a policy authority or local enclave
// to issue session decryption keys.
type GrantProvider interface {
	// RequestGrant requests an execution authorization grant for the specified package.
	RequestGrant(ctx context.Context, req GrantRequest) (*Grant, error)
}

// GrantRequest encapsulates package metadata and device environment sent to obtain an execution grant.
type GrantRequest struct {
	// PackageID is the unique identifier of the package.
	PackageID string `json:"package_id"`

	// ApplicationID is the logical application identifier.
	ApplicationID string `json:"application_id"`

	// BuildID is the unique build digest or identifier.
	BuildID string `json:"build_id"`

	// Version is the package semantic version string.
	Version string `json:"version"`

	// Platform indicates the host operating system and architecture (e.g., "darwin/arm64").
	Platform string `json:"platform"`

	// DeviceID is a unique hardware or node identifier for device-bound grants.
	DeviceID string `json:"device_id"`

	// Attestation contains hardware or enclave attestation measurements, if required.
	Attestation []byte `json:"attestation,omitempty"`
}

// Grant represents an authorization token and payload decryption key issued for a package execution session.
type Grant struct {
	// Key is the 32-byte AES-256-GCM symmetric decryption key.
	Key []byte `json:"key"`

	// ExpiresAt is the Unix timestamp (in seconds) when this grant expires (0 for no expiration).
	ExpiresAt int64 `json:"expires_at"`

	// SessionID is a unique session identifier generated for this grant.
	SessionID string `json:"session_id"`

	// Conditions lists policy constraints or requirements imposed on this session.
	Conditions []string `json:"conditions,omitempty"`

	// IssuedAt is the Unix timestamp (in seconds) when the grant was issued.
	IssuedAt int64 `json:"issued_at,omitempty"`
}

// IsExpired reports whether the grant has expired relative to the given reference time.
func (grant *Grant) IsExpired(referenceTime time.Time) bool {
	if grant == nil {
		return true
	}
	if grant.ExpiresAt <= 0 {
		return false
	}
	return referenceTime.Unix() >= grant.ExpiresAt
}

// StaticGrantProvider provides static keys and pre-configured grants for testing,
// local development, or offline provisioning.
type StaticGrantProvider struct {
	mutex      sync.RWMutex
	defaultKey []byte
	ttl        time.Duration
	grants     map[string]*Grant
	keys       map[string][]byte
}

// NewStaticGrantProvider creates a StaticGrantProvider using defaultKey for all requests
// with a default 1-hour session lifetime.
func NewStaticGrantProvider(defaultKey []byte) *StaticGrantProvider {
	return NewStaticGrantProviderWithTTL(defaultKey, time.Hour)
}

// NewStaticGrantProviderWithTTL creates a StaticGrantProvider with an explicit session TTL.
func NewStaticGrantProviderWithTTL(defaultKey []byte, ttl time.Duration) *StaticGrantProvider {
	var keyCopy []byte
	if len(defaultKey) > 0 {
		keyCopy = make([]byte, len(defaultKey))
		copy(keyCopy, defaultKey)
	}
	return &StaticGrantProvider{
		defaultKey: keyCopy,
		ttl:        ttl,
		grants:     make(map[string]*Grant),
		keys:       make(map[string][]byte),
	}
}

// RegisterKey associates an encryption key with a specific package ID or application ID.
func (provider *StaticGrantProvider) RegisterKey(packageOrAppID string, key []byte) {
	provider.mutex.Lock()
	defer provider.mutex.Unlock()

	keyCopy := make([]byte, len(key))
	copy(keyCopy, key)
	provider.keys[packageOrAppID] = keyCopy
}

// RegisterGrant associates a pre-configured Grant with a specific package ID.
func (provider *StaticGrantProvider) RegisterGrant(packageID string, grant *Grant) {
	provider.mutex.Lock()
	defer provider.mutex.Unlock()

	if grant == nil {
		delete(provider.grants, packageID)
		return
	}

	keyCopy := make([]byte, len(grant.Key))
	copy(keyCopy, grant.Key)

	grantCopy := *grant
	grantCopy.Key = keyCopy
	provider.grants[packageID] = &grantCopy
}

// RequestGrant issues or returns an authorization grant matching the request.
func (provider *StaticGrantProvider) RequestGrant(ctx context.Context, req GrantRequest) (*Grant, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	provider.mutex.RLock()
	defer provider.mutex.RUnlock()

	// 1. Check for pre-registered grant by package ID
	if grant, exists := provider.grants[req.PackageID]; exists {
		keyCopy := make([]byte, len(grant.Key))
		copy(keyCopy, grant.Key)
		grantCopy := *grant
		grantCopy.Key = keyCopy
		return &grantCopy, nil
	}

	// 2. Check for key registered by package ID or application ID
	var selectedKey []byte
	if key, exists := provider.keys[req.PackageID]; exists {
		selectedKey = key
	} else if key, exists := provider.keys[req.ApplicationID]; exists {
		selectedKey = key
	} else if len(provider.defaultKey) > 0 {
		selectedKey = provider.defaultKey
	}

	if len(selectedKey) == 0 {
		return nil, fmt.Errorf("runtime: no grant or encryption key registered for package %q", req.PackageID)
	}

	sessionID, err := generateRandomSessionID()
	if err != nil {
		return nil, fmt.Errorf("runtime: failed to generate session id: %w", err)
	}

	now := time.Now()
	var expiresAt int64
	if provider.ttl != 0 {
		expiresAt = now.Add(provider.ttl).Unix()
	}

	keyCopy := make([]byte, len(selectedKey))
	copy(keyCopy, selectedKey)

	return &Grant{
		Key:        keyCopy,
		ExpiresAt:  expiresAt,
		SessionID:  sessionID,
		IssuedAt:   now.Unix(),
		Conditions: []string{"offline", "static"},
	}, nil
}

// generateRandomSessionID generates a cryptographically random hexadecimal session identifier.
func generateRandomSessionID() (string, error) {
	var sessionBytes [16]byte
	if _, err := rand.Read(sessionBytes[:]); err != nil {
		return "", errors.New("runtime: failed to read crypto rand")
	}
	return hex.EncodeToString(sessionBytes[:]), nil
}
