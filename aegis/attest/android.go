package attest

import (
	"context"
	"crypto/x509"
	"encoding/asn1"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// AndroidKeyAttestationOID is the ASN.1 extension OID for Android KeyStore Key Attestation (1.3.6.1.4.1.11129.2.1.17).
var AndroidKeyAttestationOID = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 11129, 2, 1, 17}

// androidKeyDescription models the ASN.1 structure in the KeyStore attestation certificate extension.
type androidKeyDescription struct {
	AttestationVersion       int
	AttestationSecurityLevel asn1.Enumerated
	KeymasterVersion         int
	KeymasterSecurityLevel   asn1.Enumerated
	AttestationChallenge     []byte
	UniqueID                 []byte
	SoftwareEnforced         asn1.RawValue
	TeeEnforced              asn1.RawValue
}

// AndroidConfig specifies configuration options for the Android attestation verifier.
type AndroidConfig struct {
	// ExpectedPackage optionally enforces that the evidence matches a specific application package name.
	ExpectedPackage string

	// MinIntegrity specifies the minimum required device integrity ("strong", "basic").
	MinIntegrity string

	// MaxAge specifies the maximum allowed age of the attestation token. A zero value skips freshness validation.
	MaxAge time.Duration

	// Clock provides the time reference for freshness checks. Defaults to time.Now.
	Clock func() time.Time
}

// AndroidVerifier evaluates Google Play Integrity tokens and Android KeyStore hardware key attestations.
type AndroidVerifier struct {
	platform        string
	expectedPackage string
	minIntegrity    string
	maxAge          time.Duration
	clock           func() time.Time
}

// NewAndroidVerifier creates an AndroidVerifier with default configuration.
func NewAndroidVerifier() *AndroidVerifier {
	return NewAndroidVerifierWithConfig(AndroidConfig{})
}

// NewAndroidVerifierWithConfig creates an AndroidVerifier with customized validation rules.
func NewAndroidVerifierWithConfig(config AndroidConfig) *AndroidVerifier {
	clock := config.Clock
	if clock == nil {
		clock = time.Now
	}
	return &AndroidVerifier{
		platform:        PlatformAndroid,
		expectedPackage: strings.TrimSpace(config.ExpectedPackage),
		minIntegrity:    strings.TrimSpace(config.MinIntegrity),
		maxAge:          config.MaxAge,
		clock:           clock,
	}
}

// Platform returns the platform identifier handled by this verifier ("android").
func (verifier *AndroidVerifier) Platform() string {
	return verifier.platform
}

// Verify evaluates Android Play Integrity tokens or KeyStore attestation evidence.
func (verifier *AndroidVerifier) Verify(ctx context.Context, evidence []byte) (*Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	payload, err := decodeEvidencePayload(evidence)
	if err != nil {
		return nil, err
	}

	// 1. Try parsing as X.509 Certificate (DER or PEM) for Android KeyStore hardware key attestation
	if certResult, ok, parseErr := verifier.tryParseCertificate(payload); ok {
		if parseErr != nil {
			return nil, parseErr
		}
		return certResult, nil
	}

	// 2. Parse as structured JSON (Play Integrity or JSON mock/test vector)
	return verifier.parseJSONEvidence(payload)
}

func (verifier *AndroidVerifier) tryParseCertificate(data []byte) (*Result, bool, error) {
	var certDER []byte

	// Check PEM encoding
	if block, _ := pem.Decode(data); block != nil {
		certDER = block.Bytes
	} else if len(data) > 0 && data[0] == 0x30 {
		certDER = data
	} else {
		return nil, false, nil
	}

	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return nil, false, nil // Not a certificate, continue to JSON parsing
	}

	// Look for Android Key Attestation extension OID 1.3.6.1.4.1.11129.2.1.17
	var attestationExt []byte
	for _, ext := range cert.Extensions {
		if ext.Id.Equal(AndroidKeyAttestationOID) {
			attestationExt = ext.Value
			break
		}
	}

	if attestationExt == nil {
		return nil, false, nil
	}

	var keyDesc androidKeyDescription
	if _, err := asn1.Unmarshal(attestationExt, &keyDesc); err != nil {
		return nil, true, fmt.Errorf("%w: failed to parse android key attestation ASN.1: %w", ErrInvalidEvidence, err)
	}

	now := verifier.clock()
	timestamp := cert.NotBefore.Unix()
	if verifier.maxAge > 0 && cert.NotBefore.Unix() > 0 {
		if now.Sub(cert.NotBefore) > verifier.maxAge {
			return nil, true, ErrEvidenceExpired
		}
	}

	appIdentity := cert.Subject.CommonName
	if appIdentity == "" && len(cert.DNSNames) > 0 {
		appIdentity = cert.DNSNames[0]
	}
	if appIdentity == "" {
		appIdentity = "android-keystore"
	}

	if verifier.expectedPackage != "" && appIdentity != verifier.expectedPackage {
		return nil, true, fmt.Errorf("%w: got %q, expected %q", ErrAppIdentityMismatch, appIdentity, verifier.expectedPackage)
	}

	deviceIntegrity := IntegrityBasic
	var warnings []string

	// AttestationSecurityLevel: 0=Software, 1=TrustedEnvironment, 2=StrongBox
	switch keyDesc.AttestationSecurityLevel {
	case 2:
		deviceIntegrity = IntegrityStrong
	case 1:
		deviceIntegrity = IntegrityStrong
		warnings = append(warnings, "key backed by TEE rather than dedicated StrongBox hardware")
	default:
		deviceIntegrity = IntegrityBasic
		warnings = append(warnings, "key attestation is software-enforced without hardware backing")
	}

	if verifier.minIntegrity == IntegrityStrong && deviceIntegrity != IntegrityStrong {
		return nil, true, fmt.Errorf("%w: minimum required integrity %q not met", ErrIntegrityCheckFailed, verifier.minIntegrity)
	}

	details := map[string]string{
		"attestation_type":           "android_keystore_certificate",
		"attestation_version":        strconv.Itoa(keyDesc.AttestationVersion),
		"attestation_security_level": strconv.Itoa(int(keyDesc.AttestationSecurityLevel)),
		"keymaster_version":          strconv.Itoa(keyDesc.KeymasterVersion),
		"keymaster_security_level":   strconv.Itoa(int(keyDesc.KeymasterSecurityLevel)),
	}

	return &Result{
		Platform:        verifier.platform,
		Verified:        true,
		AppIdentity:     appIdentity,
		DeviceIntegrity: deviceIntegrity,
		Timestamp:       timestamp,
		Details:         details,
		Warnings:        warnings,
	}, true, nil
}

type playIntegrityPayload struct {
	RequestDetails struct {
		RequestPackageName string `json:"requestPackageName"`
		TimestampMillis    any    `json:"timestampMillis"`
		Nonce              string `json:"nonce"`
	} `json:"requestDetails"`
	AppIntegrity struct {
		AppRecognitionVerdict   string   `json:"appRecognitionVerdict"`
		PackageName             string   `json:"packageName"`
		CertificateSha256Digest []string `json:"certificateSha256Digest"`
		VersionCode             any      `json:"versionCode"`
	} `json:"appIntegrity"`
	DeviceIntegrity struct {
		DeviceRecognitionVerdict []string `json:"deviceRecognitionVerdict"`
	} `json:"deviceIntegrity"`
	AccountDetails struct {
		AppLicensingVerdict string `json:"appLicensingVerdict"`
	} `json:"accountDetails"`

	// Mock / test vector fields
	PackageName     string   `json:"package_name"`
	AppIdentity     string   `json:"app_identity"`
	DeviceIntegrityField string `json:"device_integrity"`
	Verdict         []string `json:"verdict"`
	SecurityLevel   string   `json:"security_level"`
	Timestamp       int64    `json:"timestamp"`
	TimestampMillis any      `json:"timestampMillis"`
	ExpiresAt       int64    `json:"expires_at"`
	Nonce           string   `json:"nonce"`
}

func (verifier *AndroidVerifier) parseJSONEvidence(data []byte) (*Result, error) {
	var payload playIntegrityPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidEvidence, err)
	}

	// 1. Resolve Package Name / App Identity
	packageName := payload.AppIntegrity.PackageName
	if packageName == "" {
		packageName = payload.RequestDetails.RequestPackageName
	}
	if packageName == "" {
		packageName = payload.PackageName
	}
	if packageName == "" {
		packageName = payload.AppIdentity
	}

	if verifier.expectedPackage != "" && packageName != verifier.expectedPackage {
		return nil, fmt.Errorf("%w: got %q, expected %q", ErrAppIdentityMismatch, packageName, verifier.expectedPackage)
	}
	if packageName == "" {
		packageName = "unknown.android.package"
	}

	// 2. Resolve Timestamp and Freshness
	var tokenTimeSec int64
	if payload.Timestamp > 0 {
		tokenTimeSec = parseTimestampSeconds(payload.Timestamp)
	} else if payload.RequestDetails.TimestampMillis != nil {
		tokenTimeSec = parseAnyTimestamp(payload.RequestDetails.TimestampMillis)
	} else if payload.TimestampMillis != nil {
		tokenTimeSec = parseAnyTimestamp(payload.TimestampMillis)
	}

	now := verifier.clock()
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

	// 3. Resolve Device Integrity
	verdicts := payload.DeviceIntegrity.DeviceRecognitionVerdict
	if len(verdicts) == 0 {
		verdicts = payload.Verdict
	}

	deviceIntegrity := IntegrityNone
	hasStrong := hasVerdict(verdicts, "MEETS_STRONG_INTEGRITY") ||
		strings.EqualFold(payload.DeviceIntegrityField, "strong") ||
		strings.EqualFold(payload.SecurityLevel, "StrongBox") ||
		strings.EqualFold(payload.SecurityLevel, "TrustedEnvironment")

	hasBasic := hasVerdict(verdicts, "MEETS_DEVICE_INTEGRITY") ||
		hasVerdict(verdicts, "MEETS_BASIC_INTEGRITY") ||
		strings.EqualFold(payload.DeviceIntegrityField, "basic") ||
		strings.EqualFold(payload.SecurityLevel, "Software")

	if hasStrong {
		deviceIntegrity = IntegrityStrong
	} else if hasBasic {
		deviceIntegrity = IntegrityBasic
	}

	var warnings []string
	if deviceIntegrity == IntegrityBasic {
		warnings = append(warnings, "device only meets basic integrity; hardware StrongBox not verified")
	} else if deviceIntegrity == IntegrityNone {
		warnings = append(warnings, "device does not meet Play Integrity or hardware attestation standards")
	}

	if payload.AccountDetails.AppLicensingVerdict != "" && payload.AccountDetails.AppLicensingVerdict != "LICENSED" {
		warnings = append(warnings, fmt.Sprintf("application licensing verdict: %s", payload.AccountDetails.AppLicensingVerdict))
	}
	if payload.AppIntegrity.AppRecognitionVerdict != "" && payload.AppIntegrity.AppRecognitionVerdict != "PLAY_RECOGNIZED" {
		warnings = append(warnings, fmt.Sprintf("play app recognition verdict: %s", payload.AppIntegrity.AppRecognitionVerdict))
	}

	// 4. Check minimum integrity threshold
	if verifier.minIntegrity == IntegrityStrong && deviceIntegrity != IntegrityStrong {
		return nil, fmt.Errorf("%w: required strong integrity, got %s", ErrIntegrityCheckFailed, deviceIntegrity)
	}
	if verifier.minIntegrity == IntegrityBasic && deviceIntegrity == IntegrityNone {
		return nil, fmt.Errorf("%w: required at least basic integrity, got none", ErrIntegrityCheckFailed)
	}

	verified := deviceIntegrity == IntegrityStrong || deviceIntegrity == IntegrityBasic

	details := make(map[string]string)
	if len(verdicts) > 0 {
		details["verdict"] = strings.Join(verdicts, ",")
	}
	if payload.AppIntegrity.AppRecognitionVerdict != "" {
		details["app_verdict"] = payload.AppIntegrity.AppRecognitionVerdict
	}
	if payload.AccountDetails.AppLicensingVerdict != "" {
		details["licensing_verdict"] = payload.AccountDetails.AppLicensingVerdict
	}
	if len(payload.AppIntegrity.CertificateSha256Digest) > 0 {
		details["cert_sha256"] = strings.Join(payload.AppIntegrity.CertificateSha256Digest, ",")
	}
	if payload.RequestDetails.Nonce != "" {
		details["nonce"] = payload.RequestDetails.Nonce
	} else if payload.Nonce != "" {
		details["nonce"] = payload.Nonce
	}

	return &Result{
		Platform:        verifier.platform,
		Verified:        verified,
		AppIdentity:     packageName,
		DeviceIntegrity: deviceIntegrity,
		Timestamp:       tokenTimeSec,
		Details:         details,
		Warnings:        warnings,
	}, nil
}

func hasVerdict(verdicts []string, target string) bool {
	for _, v := range verdicts {
		if strings.EqualFold(strings.TrimSpace(v), target) {
			return true
		}
	}
	return false
}

func parseAnyTimestamp(val any) int64 {
	switch v := val.(type) {
	case float64:
		return parseTimestampSeconds(int64(v))
	case int64:
		return parseTimestampSeconds(v)
	case int:
		return parseTimestampSeconds(int64(v))
	case string:
		if parsed, err := strconv.ParseInt(v, 10, 64); err == nil {
			return parseTimestampSeconds(parsed)
		}
	}
	return 0
}
