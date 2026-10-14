package control

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Common storage errors.
var (
	ErrPackageNotFound   = errors.New("control: package not found")
	ErrMasterKeyNotFound = errors.New("control: master key not found for package")
	ErrGrantNotFound     = errors.New("control: grant not found")
	ErrDeviceNotFound    = errors.New("control: device not found")
)

// Store defines persistent or in-memory persistence for packages, master encryption keys,
// authorization grants, and device registration states.
type Store interface {
	// StorePackage persists or updates package metadata.
	StorePackage(ctx context.Context, meta PackageRecord) error

	// GetPackage retrieves package metadata by unique package ID.
	GetPackage(ctx context.Context, packageID string) (*PackageRecord, error)

	// RevokePackage marks a package version as revoked with an explanatory reason.
	RevokePackage(ctx context.Context, packageID, reason string) error

	// IsPackageRevoked checks if a package is revoked, returning revocation status and reason.
	IsPackageRevoked(ctx context.Context, packageID string) (bool, string, error)

	// StoreMasterKey associates a 32-byte master encryption key with a package ID.
	StoreMasterKey(ctx context.Context, packageID string, key []byte) error

	// GetMasterKey retrieves the master encryption key associated with a package ID.
	GetMasterKey(ctx context.Context, packageID string) ([]byte, error)

	// RecordGrant persists an issued authorization grant record.
	RecordGrant(ctx context.Context, grant GrantRecord) error

	// RevokeGrant revokes an active grant before its scheduled expiration.
	RevokeGrant(ctx context.Context, grantID string) error

	// IsGrantRevoked checks if a grant has been revoked.
	IsGrantRevoked(ctx context.Context, grantID string) (bool, error)

	// RecordDevice registers or updates a client device record.
	RecordDevice(ctx context.Context, device DeviceRecord) error

	// IsDeviceRevoked checks if a client device has been quarantined or blacklisted.
	IsDeviceRevoked(ctx context.Context, deviceID string) (bool, error)
}

// MemoryStore provides a high-performance, thread-safe in-memory implementation of Store.
type MemoryStore struct {
	mu         sync.RWMutex
	packages   map[string]PackageRecord
	masterKeys map[string][]byte
	grants     map[string]GrantRecord
	devices    map[string]DeviceRecord
}

// NewMemoryStore constructs a new, initialized in-memory Store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		packages:   make(map[string]PackageRecord),
		masterKeys: make(map[string][]byte),
		grants:     make(map[string]GrantRecord),
		devices:    make(map[string]DeviceRecord),
	}
}

// StorePackage stores or updates package metadata in memory.
func (s *MemoryStore) StorePackage(ctx context.Context, meta PackageRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if meta.PackageID == "" {
		return errors.New("control: package id cannot be empty")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	if meta.CreatedAt.IsZero() {
		meta.CreatedAt = now
	}
	meta.UpdatedAt = now

	// Clone manifest bytes if present
	if len(meta.ManifestJSON) > 0 {
		mCopy := make([]byte, len(meta.ManifestJSON))
		copy(mCopy, meta.ManifestJSON)
		meta.ManifestJSON = mCopy
	}

	s.packages[meta.PackageID] = meta
	return nil
}

// GetPackage retrieves a deep copy of the package record for the given ID.
func (s *MemoryStore) GetPackage(ctx context.Context, packageID string) (*PackageRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	rec, exists := s.packages[packageID]
	if !exists {
		return nil, fmt.Errorf("%w: %q", ErrPackageNotFound, packageID)
	}

	copyRec := rec
	if len(rec.ManifestJSON) > 0 {
		mCopy := make([]byte, len(rec.ManifestJSON))
		copy(mCopy, rec.ManifestJSON)
		copyRec.ManifestJSON = mCopy
	}
	return &copyRec, nil
}

// RevokePackage marks a package version as revoked with an explanatory reason.
func (s *MemoryStore) RevokePackage(ctx context.Context, packageID, reason string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	rec, exists := s.packages[packageID]
	if !exists {
		// Create a stub revoked record if not already registered
		now := time.Now().UTC()
		s.packages[packageID] = PackageRecord{
			PackageID:    packageID,
			Revoked:      true,
			RevokedAt:    &now,
			RevokeReason: reason,
			CreatedAt:    now,
			UpdatedAt:    now,
		}
		return nil
	}

	now := time.Now().UTC()
	rec.Revoked = true
	rec.RevokedAt = &now
	rec.RevokeReason = reason
	rec.UpdatedAt = now
	s.packages[packageID] = rec
	return nil
}

// IsPackageRevoked returns whether a package is revoked, along with the revocation reason.
func (s *MemoryStore) IsPackageRevoked(ctx context.Context, packageID string) (bool, string, error) {
	if err := ctx.Err(); err != nil {
		return false, "", err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	rec, exists := s.packages[packageID]
	if !exists {
		return false, "", nil
	}

	return rec.Revoked, rec.RevokeReason, nil
}

// StoreMasterKey associates a 32-byte master encryption key with a package ID.
func (s *MemoryStore) StoreMasterKey(ctx context.Context, packageID string, key []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if packageID == "" {
		return errors.New("control: package id cannot be empty")
	}
	if len(key) != 32 {
		return fmt.Errorf("control: master key must be 32 bytes, got %d", len(key))
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	keyCopy := make([]byte, len(key))
	copy(keyCopy, key)
	s.masterKeys[packageID] = keyCopy
	return nil
}

// GetMasterKey retrieves a copy of the master encryption key associated with a package ID.
func (s *MemoryStore) GetMasterKey(ctx context.Context, packageID string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	key, exists := s.masterKeys[packageID]
	if !exists {
		return nil, fmt.Errorf("%w: %q", ErrMasterKeyNotFound, packageID)
	}

	keyCopy := make([]byte, len(key))
	copy(keyCopy, key)
	return keyCopy, nil
}

// RecordGrant stores an issued authorization grant.
func (s *MemoryStore) RecordGrant(ctx context.Context, grant GrantRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if grant.GrantID == "" && grant.SessionID != "" {
		grant.GrantID = grant.SessionID
	}
	if grant.GrantID == "" {
		return errors.New("control: grant id cannot be empty")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	grantCopy := grant
	if len(grant.Key) > 0 {
		kCopy := make([]byte, len(grant.Key))
		copy(kCopy, grant.Key)
		grantCopy.Key = kCopy
	}
	if len(grant.Conditions) > 0 {
		cCopy := make([]string, len(grant.Conditions))
		copy(cCopy, grant.Conditions)
		grantCopy.Conditions = cCopy
	}

	s.grants[grant.GrantID] = grantCopy
	return nil
}

// RevokeGrant invalidates an issued authorization grant before expiration.
func (s *MemoryStore) RevokeGrant(ctx context.Context, grantID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	grant, exists := s.grants[grantID]
	if !exists {
		// Also check by session ID
		for id, g := range s.grants {
			if g.SessionID == grantID {
				grant = g
				grantID = id
				exists = true
				break
			}
		}
	}
	if !exists {
		return fmt.Errorf("%w: %q", ErrGrantNotFound, grantID)
	}

	now := time.Now().UTC()
	grant.Revoked = true
	grant.RevokedAt = &now
	grant.RevokeReason = "revoked by authority"
	s.grants[grantID] = grant
	return nil
}

// IsGrantRevoked checks if a grant is revoked or does not exist.
func (s *MemoryStore) IsGrantRevoked(ctx context.Context, grantID string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	grant, exists := s.grants[grantID]
	if !exists {
		for _, g := range s.grants {
			if g.SessionID == grantID {
				grant = g
				exists = true
				break
			}
		}
	}
	if !exists {
		return false, fmt.Errorf("%w: %q", ErrGrantNotFound, grantID)
	}

	return grant.Revoked, nil
}

// RecordDevice stores or updates a client device record.
func (s *MemoryStore) RecordDevice(ctx context.Context, device DeviceRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if device.DeviceID == "" {
		return errors.New("control: device id cannot be empty")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	if device.RegisteredAt.IsZero() {
		device.RegisteredAt = now
	}
	device.LastSeenAt = now

	devCopy := device
	if len(device.AttestationEvidence) > 0 {
		eCopy := make([]byte, len(device.AttestationEvidence))
		copy(eCopy, device.AttestationEvidence)
		devCopy.AttestationEvidence = eCopy
	}
	if device.Metadata != nil {
		mCopy := make(map[string]string, len(device.Metadata))
		for k, v := range device.Metadata {
			mCopy[k] = v
		}
		devCopy.Metadata = mCopy
	}

	s.devices[device.DeviceID] = devCopy
	return nil
}

// IsDeviceRevoked checks if a device has been revoked or quarantined.
func (s *MemoryStore) IsDeviceRevoked(ctx context.Context, deviceID string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	dev, exists := s.devices[deviceID]
	if !exists {
		return false, nil
	}

	return dev.Revoked, nil
}
