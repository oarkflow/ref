package attest

import (
	"context"
	"encoding/json"
	"time"
)

// NoneVerifier implements the [Verifier] interface for desktop, local development,
// or environments where hardware attestation is unavailable or unconfigured.
//
// It returns Verified=true with DeviceIntegrity="none" and a warning that no hardware
// attestation is available, allowing unhindered developer workflows while making
// unmitigated risk explicit in policy evaluations.
type NoneVerifier struct {
	platform string
	clock    func() time.Time
}

// NewNoneVerifier creates a NoneVerifier for the default "none" platform.
func NewNoneVerifier() *NoneVerifier {
	return &NoneVerifier{
		platform: PlatformNone,
		clock:    time.Now,
	}
}

// NewNoneVerifierWithPlatform creates a NoneVerifier configured for a specific platform name
// (e.g., "linux-desktop" or "web").
func NewNoneVerifierWithPlatform(platform string) *NoneVerifier {
	if platform == "" {
		platform = PlatformNone
	}
	return &NoneVerifier{
		platform: platform,
		clock:    time.Now,
	}
}

// Platform returns the platform identifier handled by this verifier.
func (verifier *NoneVerifier) Platform() string {
	return verifier.platform
}

// Verify evaluates the evidence for desktop or local development environments.
//
// For NoneVerifier, empty evidence is accepted. If structured JSON evidence is supplied,
// application identity and optional details are extracted. The result is always marked
// Verified=true with DeviceIntegrity="none" and a warning indicating the absence of hardware backing.
func (verifier *NoneVerifier) Verify(ctx context.Context, evidence []byte) (*Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	appIdentity := "local"
	details := map[string]string{
		"environment": "unattested-development",
	}

	if len(evidence) > 0 {
		var payload struct {
			AppIdentity string            `json:"app_identity"`
			PackageName string            `json:"package_name"`
			Details     map[string]string `json:"details"`
		}
		if err := json.Unmarshal(evidence, &payload); err == nil {
			if payload.AppIdentity != "" {
				appIdentity = payload.AppIdentity
			} else if payload.PackageName != "" {
				appIdentity = payload.PackageName
			}
			for key, val := range payload.Details {
				details[key] = val
			}
		}
	}

	now := time.Now()
	if verifier.clock != nil {
		now = verifier.clock()
	}

	return &Result{
		Platform:        verifier.platform,
		Verified:        true,
		AppIdentity:     appIdentity,
		DeviceIntegrity: IntegrityNone,
		Timestamp:       now.Unix(),
		Details:         details,
		Warnings:        []string{"no hardware attestation available"},
	}, nil
}
