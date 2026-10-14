package control

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/oarkflow/ref/aegis/attest"
	"github.com/oarkflow/ref/aegis/runtime"
)

// GrantServiceOption configures optional behavior of the GrantService.
type GrantServiceOption func(*GrantService)

// WithTTL sets the session duration for issued authorization grants.
func WithTTL(ttl time.Duration) GrantServiceOption {
	return func(gs *GrantService) {
		if ttl > 0 {
			gs.ttl = ttl
		}
	}
}

// WithAuditLogger configures the audit logger for recording security and authorization events.
func WithAuditLogger(al *AuditLogger) GrantServiceOption {
	return func(gs *GrantService) {
		gs.auditLogger = al
	}
}

// WithAttestRegistry configures the hardware attestation verifier registry.
func WithAttestRegistry(reg *attest.Registry) GrantServiceOption {
	return func(gs *GrantService) {
		gs.attestRegistry = reg
	}
}

// WithConditions sets default policy constraints attached to issued execution grants.
func WithConditions(conditions ...string) GrantServiceOption {
	return func(gs *GrantService) {
		gs.defaultConditions = append([]string(nil), conditions...)
	}
}

// GrantService evaluates client execution authorization requests, enforces device and
// package revocation policies, verifies platform attestation evidence, and derives
// short-lived session decryption keys via KeyManager.
type GrantService struct {
	store             Store
	keyManager        *KeyManager
	attestRegistry    *attest.Registry
	auditLogger       *AuditLogger
	ttl               time.Duration
	defaultConditions []string
}

// NewGrantService creates an initialized GrantService.
func NewGrantService(store Store, keyManager *KeyManager, opts ...GrantServiceOption) *GrantService {
	if keyManager == nil {
		keyManager = NewKeyManager(store)
	}

	gs := &GrantService{
		store:             store,
		keyManager:        keyManager,
		attestRegistry:    attest.DefaultRegistry(),
		ttl:               time.Hour,
		defaultConditions: []string{"single_host", "enforce_integrity"},
	}

	for _, opt := range opts {
		if opt != nil {
			opt(gs)
		}
	}

	return gs
}

// RequestGrant evaluates an execution grant request from a client runtime, validates
// application identity and revocation, verifies attestation measurements, and derives an
// ephemeral session decryption key.
func (gs *GrantService) RequestGrant(ctx context.Context, req runtime.GrantRequest) (*runtime.Grant, error) {
	return gs.RequestGrantWithSession(ctx, req, "")
}

// RequestGrantWithSession allows callers (such as testing or predetermined session coordinators)
// to specify an explicit session ID. If customSessionID is empty, a random 128-bit hex ID is generated.
func (gs *GrantService) RequestGrantWithSession(ctx context.Context, req runtime.GrantRequest, customSessionID string) (*runtime.Grant, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	startTime := time.Now().UTC()

	// 1. Basic validation
	req.PackageID = strings.TrimSpace(req.PackageID)
	if req.PackageID == "" {
		gs.logEvent(ctx, SecurityEvent{
			EventType: EventGrantDenied,
			Success:   false,
			Reason:    "package ID cannot be empty",
			Timestamp: startTime,
		})
		return nil, errors.New("control: package id is required")
	}

	// 2. Check device revocation
	if req.DeviceID != "" {
		revoked, err := gs.store.IsDeviceRevoked(ctx, req.DeviceID)
		if err == nil && revoked {
			gs.logEvent(ctx, SecurityEvent{
				EventType: EventGrantDenied,
				PackageID: req.PackageID,
				DeviceID:  req.DeviceID,
				Success:   false,
				Reason:    "device is revoked or quarantined",
				Timestamp: startTime,
			})
			return nil, fmt.Errorf("control: device %q is revoked", req.DeviceID)
		}
	}

	// 3. Check package revocation
	revoked, reason, err := gs.store.IsPackageRevoked(ctx, req.PackageID)
	if err == nil && revoked {
		gs.logEvent(ctx, SecurityEvent{
			EventType: EventGrantDenied,
			PackageID: req.PackageID,
			DeviceID:  req.DeviceID,
			Success:   false,
			Reason:    fmt.Sprintf("package is revoked: %s", reason),
			Timestamp: startTime,
		})
		return nil, fmt.Errorf("control: package %q is revoked: %s", req.PackageID, reason)
	}

	// 4. Validate against registered package metadata if available
	pkgRecord, _ := gs.store.GetPackage(ctx, req.PackageID)
	if pkgRecord != nil {
		if pkgRecord.Revoked {
			gs.logEvent(ctx, SecurityEvent{
				EventType: EventGrantDenied,
				PackageID: req.PackageID,
				DeviceID:  req.DeviceID,
				Success:   false,
				Reason:    fmt.Sprintf("package is revoked: %s", pkgRecord.RevokeReason),
				Timestamp: startTime,
			})
			return nil, fmt.Errorf("control: package %q is revoked: %s", req.PackageID, pkgRecord.RevokeReason)
		}

		if req.ApplicationID != "" && pkgRecord.ApplicationID != "" && req.ApplicationID != pkgRecord.ApplicationID {
			gs.logEvent(ctx, SecurityEvent{
				EventType:     EventGrantDenied,
				PackageID:     req.PackageID,
				ApplicationID: req.ApplicationID,
				DeviceID:      req.DeviceID,
				Success:       false,
				Reason:        fmt.Sprintf("application ID mismatch: expected %s, got %s", pkgRecord.ApplicationID, req.ApplicationID),
				Timestamp:     startTime,
			})
			return nil, fmt.Errorf("control: application identity mismatch (expected %q, got %q)", pkgRecord.ApplicationID, req.ApplicationID)
		}

		if req.Version == "" && pkgRecord.Version != "" {
			req.Version = pkgRecord.Version
		}
	}

	if req.Version == "" {
		req.Version = "1.0.0"
	}

	// 5. Verify attestation evidence if provided
	if len(req.Attestation) > 0 && gs.attestRegistry != nil {
		platform := req.Platform
		if platform == "" {
			platform = attest.PlatformNone
		}

		attestResult, attestErr := gs.attestRegistry.Verify(ctx, platform, req.Attestation)
		if attestErr != nil {
			gs.logEvent(ctx, SecurityEvent{
				EventType: EventAttestationFailed,
				PackageID: req.PackageID,
				DeviceID:  req.DeviceID,
				Success:   false,
				Reason:    fmt.Sprintf("attestation failed: %v", attestErr),
				Timestamp: startTime,
			})
			gs.logEvent(ctx, SecurityEvent{
				EventType: EventGrantDenied,
				PackageID: req.PackageID,
				DeviceID:  req.DeviceID,
				Success:   false,
				Reason:    fmt.Sprintf("attestation failed: %v", attestErr),
				Timestamp: startTime,
			})
			return nil, fmt.Errorf("control: attestation verification failed: %w", attestErr)
		}

		if attestResult != nil && !attestResult.Verified {
			gs.logEvent(ctx, SecurityEvent{
				EventType: EventAttestationFailed,
				PackageID: req.PackageID,
				DeviceID:  req.DeviceID,
				Success:   false,
				Reason:    "attestation evidence not verified",
				Timestamp: startTime,
			})
			return nil, errors.New("control: attestation evidence failed verification")
		}
	}

	// 6. Obtain master package key
	masterKey, err := gs.keyManager.GetMasterKey(ctx, req.PackageID)
	if err != nil {
		gs.logEvent(ctx, SecurityEvent{
			EventType: EventGrantDenied,
			PackageID: req.PackageID,
			DeviceID:  req.DeviceID,
			Success:   false,
			Reason:    fmt.Sprintf("master key not found: %v", err),
			Timestamp: startTime,
		})
		return nil, fmt.Errorf("control: package master key not found: %w", err)
	}

	// 7. Determine session ID
	sessionID := strings.TrimSpace(customSessionID)
	if sessionID == "" {
		id, err := generateRandomSessionID()
		if err != nil {
			return nil, fmt.Errorf("control: failed to generate session id: %w", err)
		}
		sessionID = id
	}

	// 8. Derive ephemeral session key via KeyManager
	sessionKey, err := gs.keyManager.DeriveSessionKey(masterKey, sessionID, req.Version)
	if err != nil {
		gs.logEvent(ctx, SecurityEvent{
			EventType: EventGrantDenied,
			PackageID: req.PackageID,
			SessionID: sessionID,
			DeviceID:  req.DeviceID,
			Success:   false,
			Reason:    fmt.Sprintf("key derivation failed: %v", err),
			Timestamp: startTime,
		})
		return nil, fmt.Errorf("control: session key derivation failed: %w", err)
	}

	// 9. Calculate grant expiration
	now := time.Now().UTC()
	expiresAt := now.Add(gs.ttl)

	// 10. Persist grant record
	grantRecord := GrantRecord{
		GrantID:       sessionID,
		SessionID:     sessionID,
		PackageID:     req.PackageID,
		ApplicationID: req.ApplicationID,
		DeviceID:      req.DeviceID,
		Key:           sessionKey,
		IssuedAt:      now,
		ExpiresAt:     expiresAt,
		Conditions:    gs.defaultConditions,
	}
	if err := gs.store.RecordGrant(ctx, grantRecord); err != nil {
		return nil, fmt.Errorf("control: failed to persist grant record: %w", err)
	}

	// 11. Record or update device presence
	if req.DeviceID != "" {
		_ = gs.store.RecordDevice(ctx, DeviceRecord{
			DeviceID:            req.DeviceID,
			Platform:            req.Platform,
			RegisteredAt:        now,
			LastSeenAt:          now,
			AttestationEvidence: req.Attestation,
		})
	}

	// 12. Record audit telemetry event
	gs.logEvent(ctx, SecurityEvent{
		EventType:     EventGrantIssued,
		PackageID:     req.PackageID,
		ApplicationID: req.ApplicationID,
		SessionID:     sessionID,
		DeviceID:      req.DeviceID,
		Success:       true,
		Timestamp:     now,
		Details: map[string]any{
			"version":    req.Version,
			"platform":   req.Platform,
			"expires_at": expiresAt.Unix(),
		},
	})

	return &runtime.Grant{
		Key:        sessionKey,
		ExpiresAt:  expiresAt.Unix(),
		SessionID:  sessionID,
		IssuedAt:   now.Unix(),
		Conditions: gs.defaultConditions,
	}, nil
}

// RefreshGrant extends the validity of an unexpired, active grant session.
func (gs *GrantService) RefreshGrant(ctx context.Context, sessionID, packageID string) (*runtime.Grant, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, errors.New("control: session id cannot be empty")
	}

	// 1. Check package revocation
	if packageID != "" {
		revoked, reason, err := gs.store.IsPackageRevoked(ctx, packageID)
		if err == nil && revoked {
			return nil, fmt.Errorf("control: package %q is revoked: %s", packageID, reason)
		}
	}

	// 2. Check grant revocation status
	revoked, err := gs.store.IsGrantRevoked(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("control: failed to find grant session: %w", err)
	}
	if revoked {
		return nil, errors.New("control: grant has been revoked")
	}

	// 3. Load master key and re-derive or maintain session key
	masterKey, err := gs.keyManager.GetMasterKey(ctx, packageID)
	if err != nil {
		return nil, fmt.Errorf("control: master key not found: %w", err)
	}

	version := "1.0.0"
	if pkgRec, _ := gs.store.GetPackage(ctx, packageID); pkgRec != nil && pkgRec.Version != "" {
		version = pkgRec.Version
	}

	sessionKey, err := gs.keyManager.DeriveSessionKey(masterKey, sessionID, version)
	if err != nil {
		return nil, fmt.Errorf("control: failed to re-derive session key: %w", err)
	}

	now := time.Now().UTC()
	expiresAt := now.Add(gs.ttl)

	// Update grant in store
	grantRecord := GrantRecord{
		GrantID:    sessionID,
		SessionID:  sessionID,
		PackageID:  packageID,
		Key:        sessionKey,
		IssuedAt:   now,
		ExpiresAt:  expiresAt,
		Conditions: gs.defaultConditions,
	}
	_ = gs.store.RecordGrant(ctx, grantRecord)

	gs.logEvent(ctx, SecurityEvent{
		EventType: EventGrantRefreshed,
		PackageID: packageID,
		SessionID: sessionID,
		Success:   true,
		Timestamp: now,
	})

	return &runtime.Grant{
		Key:        sessionKey,
		ExpiresAt:  expiresAt.Unix(),
		SessionID:  sessionID,
		IssuedAt:   now.Unix(),
		Conditions: gs.defaultConditions,
	}, nil
}

// RevokeGrant immediately terminates an active grant session.
func (gs *GrantService) RevokeGrant(ctx context.Context, grantID string, reason string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	grantID = strings.TrimSpace(grantID)
	if grantID == "" {
		return errors.New("control: grant id cannot be empty")
	}

	if err := gs.store.RevokeGrant(ctx, grantID); err != nil {
		return err
	}

	gs.logEvent(ctx, SecurityEvent{
		EventType: EventGrantRevoked,
		SessionID: grantID,
		Success:   true,
		Reason:    reason,
		Timestamp: time.Now().UTC(),
	})
	return nil
}

func (gs *GrantService) logEvent(ctx context.Context, event SecurityEvent) {
	if gs.auditLogger != nil {
		gs.auditLogger.Log(ctx, event)
	}
}

// generateRandomSessionID generates a cryptographically random hexadecimal session identifier.
func generateRandomSessionID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", errors.New("control: failed to read crypto rand")
	}
	return hex.EncodeToString(b[:]), nil
}
