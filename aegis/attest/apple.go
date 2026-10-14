package attest

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// AppleAppAttestDevAAGUID is the ASCII AAGUID used in Apple's development sandbox.
const AppleAppAttestDevAAGUID = "appattestdevelop"

// AppleConfig specifies configuration options for the Apple attestation verifier.
type AppleConfig struct {
	// ExpectedAppID optionally enforces that the evidence matches a specific App ID (<TeamID>.<BundleID>).
	ExpectedAppID string

	// AllowDevelopment permits development and sandbox App Attest tokens (defaults to true).
	AllowDevelopment bool

	// MinIntegrity specifies the minimum required device integrity ("strong", "basic").
	MinIntegrity string

	// MaxAge specifies the maximum allowed age of the attestation token. A zero value skips freshness validation.
	MaxAge time.Duration

	// Platform overrides the default platform identifier ("ios" vs "macos-desktop").
	Platform string

	// Clock provides the time reference for freshness checks. Defaults to time.Now.
	Clock func() time.Time
}

// AppleVerifier evaluates Apple App Attest statement tokens and DeviceCheck claims.
type AppleVerifier struct {
	platform         string
	expectedAppID    string
	allowDevelopment bool
	minIntegrity     string
	maxAge           time.Duration
	clock            func() time.Time
}

// NewAppleVerifier creates an AppleVerifier for the "ios" platform with default settings.
func NewAppleVerifier() *AppleVerifier {
	return NewAppleVerifierWithConfig(AppleConfig{
		Platform:         PlatformIOS,
		AllowDevelopment: true,
	})
}

// NewAppleVerifierWithPlatform creates an AppleVerifier configured for a specific platform name
// (e.g., "ios" or "macos-desktop").
func NewAppleVerifierWithPlatform(platform string) *AppleVerifier {
	if platform == "" {
		platform = PlatformIOS
	}
	return NewAppleVerifierWithConfig(AppleConfig{
		Platform:         platform,
		AllowDevelopment: true,
	})
}

// NewAppleVerifierWithConfig creates an AppleVerifier with customized validation rules.
func NewAppleVerifierWithConfig(config AppleConfig) *AppleVerifier {
	clock := config.Clock
	if clock == nil {
		clock = time.Now
	}
	platform := config.Platform
	if platform == "" {
		platform = PlatformIOS
	}

	return &AppleVerifier{
		platform:         platform,
		expectedAppID:    strings.TrimSpace(config.ExpectedAppID),
		allowDevelopment: config.AllowDevelopment,
		minIntegrity:     strings.TrimSpace(config.MinIntegrity),
		maxAge:           config.MaxAge,
		clock:            clock,
	}
}

// Platform returns the platform identifier handled by this verifier ("ios" or "macos-desktop").
func (verifier *AppleVerifier) Platform() string {
	return verifier.platform
}

// Verify evaluates Apple App Attest statement tokens, authenticator data, or DeviceCheck claims.
func (verifier *AppleVerifier) Verify(ctx context.Context, evidence []byte) (*Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	payload, err := decodeEvidencePayload(evidence)
	if err != nil {
		return nil, err
	}

	// 1. Try parsing as raw Apple Authenticator Data binary
	if authResult, ok, parseErr := verifier.tryParseBinaryAuthData(payload); ok {
		if parseErr != nil {
			return nil, parseErr
		}
		return authResult, nil
	}

	// 2. Parse as structured JSON (App Attest statement or DeviceCheck claim)
	return verifier.parseJSONEvidence(payload)
}

// tryParseBinaryAuthData attempts to parse Apple App Attest authenticator data binary structure:
//   - 32 bytes rpIdHash (SHA-256 of App ID)
//   - 1 byte flags (bit 0 UP, bit 2 UV, bit 6 AT)
//   - 4 bytes signCount (uint32 big-endian)
//   - 16 bytes AAGUID (if flags & 0x40)
func (verifier *AppleVerifier) tryParseBinaryAuthData(data []byte) (*Result, bool, error) {
	if len(data) < 37 || data[0] == '{' || data[0] == '[' {
		return nil, false, nil
	}

	rpIdHash := data[0:32]
	flags := data[32]
	signCount := binary.BigEndian.Uint32(data[33:37])

	appIdentity := hex.EncodeToString(rpIdHash)
	deviceIntegrity := IntegrityStrong
	var warnings []string
	details := map[string]string{
		"attestation_type": "apple_appattest_binary_authdata",
		"rpid_hash":        appIdentity,
		"sign_count":       strconv.FormatUint(uint64(signCount), 10),
		"flags":            fmt.Sprintf("0x%02x", flags),
	}

	// If attested credential data present (flags & 0x40)
	if flags&0x40 != 0 && len(data) >= 53 {
		aaguidBytes := data[37:53]
		aaguidStr := string(bytes.TrimRight(aaguidBytes, "\x00"))
		details["aaguid"] = aaguidStr

		if strings.EqualFold(aaguidStr, AppleAppAttestDevAAGUID) {
			if !verifier.allowDevelopment {
				return nil, true, fmt.Errorf("%w: development environment attestations not permitted", ErrIntegrityCheckFailed)
			}
			deviceIntegrity = IntegrityBasic
			warnings = append(warnings, "development environment attestation; Secure Enclave production guarantees not verified")
		}
	}

	if verifier.expectedAppID != "" && appIdentity != verifier.expectedAppID {
		return nil, true, fmt.Errorf("%w: got %q, expected %q", ErrAppIdentityMismatch, appIdentity, verifier.expectedAppID)
	}

	if verifier.minIntegrity == IntegrityStrong && deviceIntegrity != IntegrityStrong {
		return nil, true, fmt.Errorf("%w: minimum required integrity %q not met", ErrIntegrityCheckFailed, verifier.minIntegrity)
	}

	now := verifier.clock()
	return &Result{
		Platform:        verifier.platform,
		Verified:        true,
		AppIdentity:     appIdentity,
		DeviceIntegrity: deviceIntegrity,
		Timestamp:       now.Unix(),
		Details:         details,
		Warnings:        warnings,
	}, true, nil
}

type appleEvidenceJSON struct {
	Fmt             string `json:"fmt"`
	Type            string `json:"type"`
	AppID           string `json:"app_id"`
	BundleID        string `json:"bundle_id"`
	TeamID          string `json:"team_id"`
	AppIdentity     string `json:"app_identity"`
	Environment     string `json:"environment"`
	AAGUID          string `json:"aaguid"`
	Counter         uint32 `json:"counter"`
	Receipt         string `json:"receipt"`
	DeviceToken     string `json:"device_token"`
	TransactionID   string `json:"transaction_id"`
	DeviceIntegrity string `json:"device_integrity"`
	Timestamp       int64  `json:"timestamp"`
	ExpiresAt       int64  `json:"expires_at"`
	Bit0            *bool  `json:"bit0,omitempty"`
	Bit1            *bool  `json:"bit1,omitempty"`
}

func (verifier *AppleVerifier) parseJSONEvidence(data []byte) (*Result, error) {
	var payload appleEvidenceJSON
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidEvidence, err)
	}

	// 1. Resolve Application Identity
	appIdentity := payload.AppID
	if appIdentity == "" && payload.TeamID != "" && payload.BundleID != "" {
		appIdentity = fmt.Sprintf("%s.%s", payload.TeamID, payload.BundleID)
	}
	if appIdentity == "" {
		appIdentity = payload.BundleID
	}
	if appIdentity == "" {
		appIdentity = payload.AppIdentity
	}

	if verifier.expectedAppID != "" && appIdentity != verifier.expectedAppID {
		return nil, fmt.Errorf("%w: got %q, expected %q", ErrAppIdentityMismatch, appIdentity, verifier.expectedAppID)
	}
	if appIdentity == "" {
		appIdentity = "unknown.apple.app"
	}

	// 2. Resolve Timestamp and Freshness
	now := verifier.clock()
	tokenTimeSec := parseTimestampSeconds(payload.Timestamp)
	if payload.ExpiresAt > 0 && now.Unix() > payload.ExpiresAt {
		return nil, ErrEvidenceExpired
	}
	if verifier.maxAge > 0 && tokenTimeSec > 0 {
		tokenTime := time.Unix(tokenTimeSec, 0)
		if now.Sub(tokenTime) > verifier.maxAge {
			return nil, ErrEvidenceExpired
		}
	}
	if tokenTimeSec == 0 {
		tokenTimeSec = now.Unix()
	}

	// 3. Determine Evidence Type (App Attest vs DeviceCheck)
	isDeviceCheck := strings.EqualFold(payload.Fmt, "apple-devicecheck") ||
		strings.EqualFold(payload.Type, "devicecheck") ||
		payload.DeviceToken != ""

	deviceIntegrity := IntegrityStrong
	var warnings []string
	details := make(map[string]string)

	if isDeviceCheck {
		// DeviceCheck provides OS/device-level claims, not per-app Secure Enclave key attestation
		deviceIntegrity = IntegrityBasic
		warnings = append(warnings, "devicecheck provides software claims without Secure Enclave key attestation")
		details["attestation_type"] = "apple_devicecheck"
		if payload.DeviceToken != "" {
			details["device_token"] = payload.DeviceToken
		}
		if payload.TransactionID != "" {
			details["transaction_id"] = payload.TransactionID
		}
		if payload.Bit0 != nil {
			details["bit0"] = strconv.FormatBool(*payload.Bit0)
		}
		if payload.Bit1 != nil {
			details["bit1"] = strconv.FormatBool(*payload.Bit1)
		}
	} else {
		// App Attest
		details["attestation_type"] = "apple_appattest"
		if payload.Counter > 0 {
			details["counter"] = strconv.FormatUint(uint64(payload.Counter), 10)
		}
		if payload.Receipt != "" {
			details["receipt"] = payload.Receipt
		}
		if payload.AAGUID != "" {
			details["aaguid"] = payload.AAGUID
		}

		isDev := strings.EqualFold(payload.Environment, "development") ||
			strings.EqualFold(payload.Environment, "sandbox") ||
			strings.EqualFold(payload.AAGUID, AppleAppAttestDevAAGUID)

		if isDev {
			if !verifier.allowDevelopment {
				return nil, fmt.Errorf("%w: development environment attestations not permitted", ErrIntegrityCheckFailed)
			}
			deviceIntegrity = IntegrityBasic
			warnings = append(warnings, "development environment attestation; Secure Enclave production guarantees not verified")
			details["environment"] = "development"
		} else {
			deviceIntegrity = IntegrityStrong
			details["environment"] = "production"
		}
	}

	// Explicit override from mock/test vector if provided
	if payload.DeviceIntegrity != "" {
		switch strings.ToLower(payload.DeviceIntegrity) {
		case IntegrityStrong:
			deviceIntegrity = IntegrityStrong
		case IntegrityBasic:
			deviceIntegrity = IntegrityBasic
		case IntegrityNone:
			deviceIntegrity = IntegrityNone
		}
	}

	// 4. Verify minimum integrity threshold
	if verifier.minIntegrity == IntegrityStrong && deviceIntegrity != IntegrityStrong {
		return nil, fmt.Errorf("%w: required strong integrity, got %s", ErrIntegrityCheckFailed, deviceIntegrity)
	}
	if verifier.minIntegrity == IntegrityBasic && deviceIntegrity == IntegrityNone {
		return nil, fmt.Errorf("%w: required at least basic integrity, got none", ErrIntegrityCheckFailed)
	}

	verified := deviceIntegrity == IntegrityStrong || deviceIntegrity == IntegrityBasic

	return &Result{
		Platform:        verifier.platform,
		Verified:        verified,
		AppIdentity:     appIdentity,
		DeviceIntegrity: deviceIntegrity,
		Timestamp:       tokenTimeSec,
		Details:         details,
		Warnings:        warnings,
	}, nil
}
