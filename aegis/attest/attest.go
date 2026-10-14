// Package attest provides platform-specific hardware attestation verification
// and cryptographic evidence inspection for the Aegis Runtime.
//
// It defines the core [Verifier] interface, attestation [Result] structure, and a
// thread-safe [Registry] for routing platform attestation evidence to appropriate
// verification engines (e.g. Google Play Integrity, Apple App Attest, TPM 2.0,
// Intel SGX, and AMD SEV-SNP).
package attest

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
)

// Integrity levels representing hardware isolation strength.
const (
	// IntegrityStrong indicates hardware-rooted isolation (e.g., TEE, Secure Enclave, TPM 2.0, StrongBox).
	IntegrityStrong = "strong"

	// IntegrityBasic indicates software-enforced or basic integrity without strong hardware root.
	IntegrityBasic = "basic"

	// IntegrityNone indicates unattested local or dev environments with no hardware attestation.
	IntegrityNone = "none"
)

// Well-known platform identifiers compatible with aegis/policy.
const (
	// PlatformNone represents local or development environments without hardware attestation.
	PlatformNone = "none"

	// PlatformAndroid represents Android mobile devices with Play Integrity or KeyStore attestation.
	PlatformAndroid = "android"

	// PlatformIOS represents Apple iOS devices with App Attest or DeviceCheck.
	PlatformIOS = "ios"

	// PlatformDesktopMacOS represents Apple macOS desktop and server environments.
	PlatformDesktopMacOS = "macos-desktop"

	// PlatformDesktopWindows represents Microsoft Windows environments with TPM 2.0.
	PlatformDesktopWindows = "windows-desktop"

	// PlatformDesktopLinux represents standard desktop Linux environments without confidential hardware.
	PlatformDesktopLinux = "linux-desktop"

	// PlatformWeb represents WebAssembly runtimes inside web browsers.
	PlatformWeb = "web"

	// PlatformConfidentialLinux represents Linux systems running confidential computing hardware (SGX, SEV-SNP, TDX).
	PlatformConfidentialLinux = "linux-confidential"

	// PlatformTPM represents generic TPM 2.0 hardware quote validation.
	PlatformTPM = "tpm"

	// PlatformSGX represents Intel SGX confidential enclaves.
	PlatformSGX = "sgx"

	// PlatformSEVSNP represents AMD SEV-SNP confidential virtual machines.
	PlatformSEVSNP = "sev-snp"
)

// Package errors.
var (
	// ErrNilVerifier is returned when attempting to register a nil verifier.
	ErrNilVerifier = errors.New("attest: verifier cannot be nil")

	// ErrEmptyPlatform is returned when a platform identifier is empty.
	ErrEmptyPlatform = errors.New("attest: platform cannot be empty")

	// ErrUnknownPlatform is returned when no verifier is registered for a given platform.
	ErrUnknownPlatform = errors.New("attest: unknown or unsupported platform")

	// ErrEmptyEvidence is returned when attestation evidence payload is nil or empty.
	ErrEmptyEvidence = errors.New("attest: evidence cannot be empty")

	// ErrInvalidEvidence is returned when evidence structure or encoding is malformed.
	ErrInvalidEvidence = errors.New("attest: invalid evidence structure")

	// ErrEvidenceExpired is returned when attestation evidence timestamp exceeds freshness limits.
	ErrEvidenceExpired = errors.New("attest: attestation evidence has expired")

	// ErrIntegrityCheckFailed is returned when hardware integrity checks fail to meet required thresholds.
	ErrIntegrityCheckFailed = errors.New("attest: device integrity verification failed")

	// ErrAppIdentityMismatch is returned when evidence does not match expected application identity.
	ErrAppIdentityMismatch = errors.New("attest: app identity mismatch")
)

// bufferPool recycles bytes.Buffer instances for evidence parsing hot paths.
var bufferPool = sync.Pool{
	New: func() any {
		return new(bytes.Buffer)
	},
}

func getPooledBuffer() *bytes.Buffer {
	buffer := bufferPool.Get().(*bytes.Buffer)
	buffer.Reset()
	return buffer
}

func putPooledBuffer(buffer *bytes.Buffer) {
	if buffer == nil || buffer.Cap() > 128*1024 {
		return
	}
	buffer.Reset()
	bufferPool.Put(buffer)
}

// Verifier evaluates platform-specific hardware attestation evidence.
type Verifier interface {
	// Platform returns the canonical platform identifier handled by this verifier.
	Platform() string

	// Verify evaluates the cryptographic and architectural integrity of the evidence.
	Verify(ctx context.Context, evidence []byte) (*Result, error)
}

// Result encapsulates the outcome of verifying hardware or platform attestation evidence.
type Result struct {
	// Platform is the platform identifier that produced and verified this evidence.
	Platform string `json:"platform"`

	// Verified indicates whether all cryptographic assertions and policy checks passed.
	Verified bool `json:"verified"`

	// AppIdentity is the verified application identifier (e.g., package name or bundle ID).
	AppIdentity string `json:"app_identity"`

	// DeviceIntegrity indicates the hardware backing tier ("strong", "basic", "none").
	DeviceIntegrity string `json:"device_integrity"`

	// Timestamp is the Unix timestamp (seconds) when the attestation was generated or recorded.
	Timestamp int64 `json:"timestamp"`

	// Details contains platform-specific metadata, claims, and measurements.
	Details map[string]string `json:"details,omitempty"`

	// Warnings records non-fatal integrity advisories or downgrade notices.
	Warnings []string `json:"warnings,omitempty"`
}

// IsStrong reports whether the result passed verification with strong hardware integrity.
func (result *Result) IsStrong() bool {
	return result != nil && result.Verified && result.DeviceIntegrity == IntegrityStrong
}

// IsBasicOrStrong reports whether the result passed verification with basic or strong integrity.
func (result *Result) IsBasicOrStrong() bool {
	if result == nil || !result.Verified {
		return false
	}
	return result.DeviceIntegrity == IntegrityStrong || result.DeviceIntegrity == IntegrityBasic
}

// Clone returns a deep copy of the Result.
func (result *Result) Clone() *Result {
	if result == nil {
		return nil
	}
	cloned := *result
	if result.Details != nil {
		cloned.Details = make(map[string]string, len(result.Details))
		for key, value := range result.Details {
			cloned.Details[key] = value
		}
	}
	if result.Warnings != nil {
		cloned.Warnings = make([]string, len(result.Warnings))
		copy(cloned.Warnings, result.Warnings)
	}
	return &cloned
}

// SetDetail sets a key-value pair in the details map, initializing it if nil.
func (result *Result) SetDetail(key, value string) {
	if result.Details == nil {
		result.Details = make(map[string]string)
	}
	result.Details[key] = value
}

// AddWarning adds a warning message to the warnings slice.
func (result *Result) AddWarning(warning string) {
	result.Warnings = append(result.Warnings, warning)
}

// Registry manages platform-specific verifiers and routes verification requests.
type Registry struct {
	mutex     sync.RWMutex
	verifiers map[string]Verifier
	aliases   map[string]string
}

// NewRegistry creates a new Registry pre-populated with optional verifiers.
func NewRegistry(verifiers ...Verifier) *Registry {
	reg := &Registry{
		verifiers: make(map[string]Verifier),
		aliases:   make(map[string]string),
	}
	for _, verifier := range verifiers {
		if verifier != nil {
			reg.Register(verifier)
		}
	}
	return reg
}

// Register registers a verifier for its canonical platform name.
func (registry *Registry) Register(verifier Verifier) {
	if verifier == nil {
		return
	}
	registry.mutex.Lock()
	defer registry.mutex.Unlock()

	platformKey := strings.ToLower(strings.TrimSpace(verifier.Platform()))
	registry.verifiers[platformKey] = verifier
}

// RegisterAlias maps an alias name to an existing platform identifier.
func (registry *Registry) RegisterAlias(alias, targetPlatform string) {
	registry.mutex.Lock()
	defer registry.mutex.Unlock()

	aliasKey := strings.ToLower(strings.TrimSpace(alias))
	targetKey := strings.ToLower(strings.TrimSpace(targetPlatform))
	registry.aliases[aliasKey] = targetKey
}

// VerifierFor returns the verifier registered for the given platform name or alias.
func (registry *Registry) VerifierFor(platform string) (Verifier, bool) {
	registry.mutex.RLock()
	defer registry.mutex.RUnlock()

	platformKey := strings.ToLower(strings.TrimSpace(platform))
	if target, exists := registry.aliases[platformKey]; exists {
		platformKey = target
	}

	verifier, exists := registry.verifiers[platformKey]
	return verifier, exists
}

// Platforms returns a slice of all registered canonical platform names.
func (registry *Registry) Platforms() []string {
	registry.mutex.RLock()
	defer registry.mutex.RUnlock()

	platforms := make([]string, 0, len(registry.verifiers))
	for platform := range registry.verifiers {
		platforms = append(platforms, platform)
	}
	return platforms
}

// Verify evaluates attestation evidence against the verifier registered for the platform.
func (registry *Registry) Verify(ctx context.Context, platform string, evidence []byte) (*Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	platform = strings.TrimSpace(platform)
	if platform == "" {
		return nil, ErrEmptyPlatform
	}

	verifier, found := registry.VerifierFor(platform)
	if !found {
		return nil, fmt.Errorf("%w: %q", ErrUnknownPlatform, platform)
	}

	return verifier.Verify(ctx, evidence)
}

// DefaultRegistry creates and returns a Registry configured with standard verifiers
// for all well-known platforms.
func DefaultRegistry() *Registry {
	reg := NewRegistry(
		NewNoneVerifier(),
		NewNoneVerifierWithPlatform(PlatformDesktopLinux),
		NewNoneVerifierWithPlatform(PlatformWeb),
		NewAndroidVerifier(),
		NewAppleVerifier(),
		NewAppleVerifierWithPlatform(PlatformDesktopMacOS),
		NewTPMVerifier(),
		NewTPMVerifierWithPlatform(PlatformDesktopWindows),
		NewConfidentialVerifier(),
		NewConfidentialVerifierWithPlatform(PlatformSGX),
		NewConfidentialVerifierWithPlatform(PlatformSEVSNP),
	)

	// Register convenient platform aliases
	reg.RegisterAlias("linux", PlatformDesktopLinux)
	reg.RegisterAlias("macos", PlatformDesktopMacOS)
	reg.RegisterAlias("darwin", PlatformDesktopMacOS)
	reg.RegisterAlias("darwin/arm64", PlatformDesktopMacOS)
	reg.RegisterAlias("darwin/amd64", PlatformDesktopMacOS)
	reg.RegisterAlias("windows", PlatformDesktopWindows)
	reg.RegisterAlias("confidential", PlatformConfidentialLinux)

	return reg
}

// decodeEvidencePayload normalizes evidence that may be passed as base64, JWT, or raw JSON/binary.
func decodeEvidencePayload(evidence []byte) ([]byte, error) {
	trimmed := bytes.TrimSpace(evidence)
	if len(trimmed) == 0 {
		return nil, ErrEmptyEvidence
	}

	// 1. If it already starts with JSON object/array or binary magic indicators, return directly
	if trimmed[0] == '{' || trimmed[0] == '[' {
		return trimmed, nil
	}

	// TPM 2.0 quote binary magic "\xffTCG"
	if len(trimmed) >= 4 && trimmed[0] == 0xff && trimmed[1] == 0x54 && trimmed[2] == 0x43 && trimmed[3] == 0x47 {
		return trimmed, nil
	}

	// ASN.1 DER sequence indicator 0x30
	if trimmed[0] == 0x30 {
		return trimmed, nil
	}

	// 2. Check for standard JWT format (header.payload.signature)
	parts := bytes.Split(trimmed, []byte("."))
	if len(parts) == 3 {
		payloadBytes, err := base64.RawURLEncoding.DecodeString(string(parts[1]))
		if err == nil && len(payloadBytes) > 0 {
			return payloadBytes, nil
		}
		// Try standard URL encoding with padding
		payloadBytes, err = base64.URLEncoding.DecodeString(string(parts[1]))
		if err == nil && len(payloadBytes) > 0 {
			return payloadBytes, nil
		}
	}

	// 3. Try standard Base64 decoding
	if decoded, err := base64.StdEncoding.DecodeString(string(trimmed)); err == nil && len(decoded) > 0 {
		return decoded, nil
	}

	// 4. Try standard URL Base64 decoding
	if decoded, err := base64.URLEncoding.DecodeString(string(trimmed)); err == nil && len(decoded) > 0 {
		return decoded, nil
	}

	// 5. Try Raw URL Base64 decoding
	if decoded, err := base64.RawURLEncoding.DecodeString(string(trimmed)); err == nil && len(decoded) > 0 {
		return decoded, nil
	}

	// Default fallback to raw bytes
	return trimmed, nil
}

// parseTimestampSeconds normalizes timestamps provided as seconds or milliseconds Unix timestamps.
func parseTimestampSeconds(raw int64) int64 {
	if raw <= 0 {
		return 0
	}
	// If greater than 1e11 (Sun Sep 9 2001 in milliseconds is 1e12), treat as milliseconds
	if raw > 100_000_000_000 {
		return raw / 1000
	}
	return raw
}
