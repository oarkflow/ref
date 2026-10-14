// Package control provides central policy authority, package authorization,
// master key management, and execution grant issuance for the Aegis Runtime.
package control

import (
	"time"

	"github.com/oarkflow/ref/aegis/runtime"
)

// Security event type constants.
const (
	EventGrantRequested    = "grant_requested"
	EventGrantIssued       = "grant_issued"
	EventGrantDenied       = "grant_denied"
	EventGrantRefreshed    = "grant_refreshed"
	EventGrantRevoked      = "grant_revoked"
	EventPackageRegistered = "package_registered"
	EventPackageRevoked    = "package_revoked"
	EventIntegrityReport   = "integrity_report"
	EventAttestationFailed = "attestation_failed"
)

// PackageRecord represents a registered package metadata record managed by the control server.
type PackageRecord struct {
	// PackageID is the globally unique package identifier.
	PackageID string `json:"package_id"`

	// ApplicationID is the logical application identifier.
	ApplicationID string `json:"application_id"`

	// Version is the package semantic version string.
	Version string `json:"version"`

	// BuildID is the unique digest or build identifier.
	BuildID string `json:"build_id,omitempty"`

	// ManifestJSON contains optional canonical manifest JSON representation.
	ManifestJSON []byte `json:"manifest_json,omitempty"`

	// Revoked indicates whether package execution has been revoked.
	Revoked bool `json:"revoked"`

	// RevokedAt is the timestamp when revocation took effect.
	RevokedAt *time.Time `json:"revoked_at,omitempty"`

	// RevokeReason explains why the package was revoked.
	RevokeReason string `json:"revoke_reason,omitempty"`

	// CreatedAt is the record creation timestamp.
	CreatedAt time.Time `json:"created_at"`

	// UpdatedAt is the timestamp of the last status change.
	UpdatedAt time.Time `json:"updated_at"`
}

// GrantRecord stores issued authorization grant sessions and their cryptographic bindings.
type GrantRecord struct {
	// GrantID is the unique identifier for this grant record (typically matches SessionID).
	GrantID string `json:"grant_id"`

	// SessionID is the unique session identifier issued to the client.
	SessionID string `json:"session_id"`

	// PackageID is the package identifier authorized by this grant.
	PackageID string `json:"package_id"`

	// ApplicationID is the authorized application identifier.
	ApplicationID string `json:"application_id"`

	// DeviceID is the bound device identifier, if device-bound.
	DeviceID string `json:"device_id,omitempty"`

	// Key is the 32-byte derived session decryption key.
	// Omitted from JSON serialization for security hygiene.
	Key []byte `json:"-"`

	// IssuedAt is the timestamp when the grant was created.
	IssuedAt time.Time `json:"issued_at"`

	// ExpiresAt is the timestamp when the grant expires.
	ExpiresAt time.Time `json:"expires_at"`

	// Revoked indicates if the grant has been explicitly revoked before expiration.
	Revoked bool `json:"revoked"`

	// RevokedAt is the timestamp when the grant was revoked.
	RevokedAt *time.Time `json:"revoked_at,omitempty"`

	// RevokeReason records why the grant was invalidated early.
	RevokeReason string `json:"revoke_reason,omitempty"`

	// Conditions lists policy constraints imposed on this session.
	Conditions []string `json:"conditions,omitempty"`
}

// DeviceRecord tracks client device hardware identity, platform, and attestation status.
type DeviceRecord struct {
	// DeviceID is the unique hardware or client node identifier.
	DeviceID string `json:"device_id"`

	// Platform indicates the operating system and architecture (e.g., "darwin/arm64").
	Platform string `json:"platform"`

	// RegisteredAt is the timestamp when the device was first seen.
	RegisteredAt time.Time `json:"registered_at"`

	// LastSeenAt is the timestamp of the most recent request from this device.
	LastSeenAt time.Time `json:"last_seen_at"`

	// Revoked reports whether the device has been blacklisted or quarantined.
	Revoked bool `json:"revoked"`

	// RevokedAt is the timestamp when the device was revoked.
	RevokedAt *time.Time `json:"revoked_at,omitempty"`

	// RevokeReason describes why the device was revoked.
	RevokeReason string `json:"revoke_reason,omitempty"`

	// AttestationEvidence contains the latest raw attestation evidence presented.
	AttestationEvidence []byte `json:"attestation_evidence,omitempty"`

	// Metadata contains arbitrary client or enrollment metadata.
	Metadata map[string]string `json:"metadata,omitempty"`
}

// SecurityEvent records high-priority audit events for telemetry, forensics, and compliance.
type SecurityEvent struct {
	// ID is the unique identifier for this security event.
	ID string `json:"id"`

	// Timestamp is the exact time the event occurred.
	Timestamp time.Time `json:"timestamp"`

	// EventType specifies the event category (e.g. EventGrantIssued).
	EventType string `json:"event_type"`

	// PackageID is the package associated with the event.
	PackageID string `json:"package_id,omitempty"`

	// ApplicationID is the application identifier.
	ApplicationID string `json:"application_id,omitempty"`

	// SessionID is the session identifier.
	SessionID string `json:"session_id,omitempty"`

	// DeviceID is the client hardware or node identifier.
	DeviceID string `json:"device_id,omitempty"`

	// Success indicates if the operation succeeded or was denied.
	Success bool `json:"success"`

	// Reason explains the authorization outcome or failure details.
	Reason string `json:"reason,omitempty"`

	// ClientIP records the remote network address.
	ClientIP string `json:"client_ip,omitempty"`

	// Details holds arbitrary structured context metadata.
	Details map[string]any `json:"details,omitempty"`
}

// GrantRequestPayload encapsulates the HTTP JSON body for requesting an execution grant.
type GrantRequestPayload struct {
	runtime.GrantRequest

	// SessionID optionally allows the client or test to specify a desired session ID.
	// If omitted, a cryptographically secure random session ID is generated.
	SessionID string `json:"session_id,omitempty"`
}

// GrantResponse encapsulates the HTTP JSON response for grant requests.
type GrantResponse struct {
	Success bool           `json:"success"`
	Grant   *runtime.Grant `json:"grant,omitempty"`
	Error   string         `json:"error,omitempty"`
}

// GrantRefreshPayload encapsulates the HTTP JSON body for refreshing an active grant.
type GrantRefreshPayload struct {
	SessionID string `json:"session_id"`
	PackageID string `json:"package_id"`
}

// GrantRevokePayload encapsulates the HTTP JSON body for revoking a grant session.
type GrantRevokePayload struct {
	GrantID string `json:"grant_id"`
	Reason  string `json:"reason,omitempty"`
}

// PackageRegisterPayload encapsulates the HTTP JSON body for registering a package and master key.
type PackageRegisterPayload struct {
	PackageID     string `json:"package_id"`
	ApplicationID string `json:"application_id"`
	Version       string `json:"version"`
	BuildID       string `json:"build_id,omitempty"`
	// MasterKey is a 64-character hex string representing the 32-byte master symmetric key.
	MasterKey string `json:"master_key"`
}

// PackageRevokePayload encapsulates the HTTP JSON body for revoking a package version.
type PackageRevokePayload struct {
	PackageID string `json:"package_id"`
	Reason    string `json:"reason"`
}

// PackageStatusResponse encapsulates the HTTP JSON response for checking package revocation status.
type PackageStatusResponse struct {
	PackageID    string     `json:"package_id"`
	ApplicationID string    `json:"application_id,omitempty"`
	Version      string     `json:"version,omitempty"`
	Revoked      bool       `json:"revoked"`
	RevokeReason string     `json:"revoke_reason,omitempty"`
	RevokedAt    *time.Time `json:"revoked_at,omitempty"`
}

// IntegrityReportPayload encapsulates the HTTP JSON body for client telemetry and integrity signals.
type IntegrityReportPayload struct {
	DeviceID  string         `json:"device_id"`
	PackageID string         `json:"package_id"`
	SessionID string         `json:"session_id,omitempty"`
	Signals   map[string]any `json:"signals"`
	Timestamp int64          `json:"timestamp,omitempty"`
}
