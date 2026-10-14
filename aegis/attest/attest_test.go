package attest

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"sync"
	"testing"
	"time"
)

// --- NoneVerifier Tests ---

func TestNoneVerifier(t *testing.T) {
	ctx := context.Background()
	verifier := NewNoneVerifier()

	if verifier.Platform() != PlatformNone {
		t.Fatalf("expected platform %q, got %q", PlatformNone, verifier.Platform())
	}

	// 1. Valid empty evidence
	res, err := verifier.Verify(ctx, nil)
	if err != nil {
		t.Fatalf("unexpected error for nil evidence: %v", err)
	}
	if !res.Verified {
		t.Error("expected Verified=true")
	}
	if res.DeviceIntegrity != IntegrityNone {
		t.Errorf("expected DeviceIntegrity=%q, got %q", IntegrityNone, res.DeviceIntegrity)
	}
	if res.AppIdentity != "local" {
		t.Errorf("expected AppIdentity='local', got %q", res.AppIdentity)
	}
	if len(res.Warnings) == 0 || res.Warnings[0] != "no hardware attestation available" {
		t.Errorf("expected 'no hardware attestation available' warning, got %v", res.Warnings)
	}

	// 2. Custom JSON evidence
	customJSON := []byte(`{"app_identity": "com.example.devapp", "details": {"git_commit": "abcdef"}}`)
	res, err = verifier.Verify(ctx, customJSON)
	if err != nil {
		t.Fatalf("unexpected error for JSON evidence: %v", err)
	}
	if res.AppIdentity != "com.example.devapp" {
		t.Errorf("expected AppIdentity='com.example.devapp', got %q", res.AppIdentity)
	}
	if res.Details["git_commit"] != "abcdef" {
		t.Errorf("expected detail git_commit='abcdef', got %q", res.Details["git_commit"])
	}

	// 3. Custom platform instance
	linuxDesktopVerifier := NewNoneVerifierWithPlatform(PlatformDesktopLinux)
	if linuxDesktopVerifier.Platform() != PlatformDesktopLinux {
		t.Fatalf("expected platform %q, got %q", PlatformDesktopLinux, linuxDesktopVerifier.Platform())
	}
	res, err = linuxDesktopVerifier.Verify(ctx, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Platform != PlatformDesktopLinux {
		t.Errorf("expected result platform %q, got %q", PlatformDesktopLinux, res.Platform)
	}

	// 4. Context cancellation
	cancCtx, cancel := context.WithCancel(ctx)
	cancel()
	_, err = verifier.Verify(cancCtx, nil)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

// --- AndroidVerifier Tests ---

func TestAndroidVerifier(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	verifier := NewAndroidVerifierWithConfig(AndroidConfig{
		ExpectedPackage: "com.example.app",
		MaxAge:          10 * time.Minute,
		Clock:           func() time.Time { return now },
	})

	// 1. Valid Play Integrity token (Strong integrity)
	playPayload := map[string]any{
		"requestDetails": map[string]any{
			"requestPackageName": "com.example.app",
			"timestampMillis":    now.UnixMilli(),
			"nonce":              "test-nonce-1234",
		},
		"appIntegrity": map[string]any{
			"appRecognitionVerdict":   "PLAY_RECOGNIZED",
			"packageName":             "com.example.app",
			"certificateSha256Digest": []string{"abcdef1234567890"},
			"versionCode":             101,
		},
		"deviceIntegrity": map[string]any{
			"deviceRecognitionVerdict": []string{"MEETS_STRONG_INTEGRITY", "MEETS_DEVICE_INTEGRITY", "MEETS_BASIC_INTEGRITY"},
		},
		"accountDetails": map[string]any{
			"appLicensingVerdict": "LICENSED",
		},
	}
	tokenBytes, _ := json.Marshal(playPayload)

	res, err := verifier.Verify(ctx, tokenBytes)
	if err != nil {
		t.Fatalf("unexpected error for valid Play token: %v", err)
	}
	if !res.Verified {
		t.Error("expected Verified=true")
	}
	if res.DeviceIntegrity != IntegrityStrong {
		t.Errorf("expected DeviceIntegrity=%q, got %q", IntegrityStrong, res.DeviceIntegrity)
	}
	if res.AppIdentity != "com.example.app" {
		t.Errorf("expected AppIdentity='com.example.app', got %q", res.AppIdentity)
	}
	if !res.IsStrong() {
		t.Error("expected IsStrong()=true")
	}

	// 2. Base64-encoded token
	b64Token := []byte(base64.StdEncoding.EncodeToString(tokenBytes))
	res, err = verifier.Verify(ctx, b64Token)
	if err != nil {
		t.Fatalf("unexpected error for base64 Play token: %v", err)
	}
	if !res.Verified || res.DeviceIntegrity != IntegrityStrong {
		t.Errorf("expected verified strong result, got %+v", res)
	}

	// 3. JWT-formatted token (header.payload.signature)
	headerB64 := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"ES256"}`))
	payloadB64 := base64.RawURLEncoding.EncodeToString(tokenBytes)
	sigB64 := base64.RawURLEncoding.EncodeToString([]byte(`fake-signature-bytes`))
	jwtToken := []byte(headerB64 + "." + payloadB64 + "." + sigB64)

	res, err = verifier.Verify(ctx, jwtToken)
	if err != nil {
		t.Fatalf("unexpected error for JWT Play token: %v", err)
	}
	if !res.Verified {
		t.Error("expected Verified=true for JWT token")
	}

	// 4. Basic integrity token (with warning)
	basicPayload := map[string]any{
		"package_name": "com.example.app",
		"deviceIntegrity": map[string]any{
			"deviceRecognitionVerdict": []string{"MEETS_BASIC_INTEGRITY"},
		},
		"timestamp": now.Unix(),
	}
	basicBytes, _ := json.Marshal(basicPayload)
	res, err = verifier.Verify(ctx, basicBytes)
	if err != nil {
		t.Fatalf("unexpected error for basic token: %v", err)
	}
	if res.DeviceIntegrity != IntegrityBasic {
		t.Errorf("expected IntegrityBasic, got %q", res.DeviceIntegrity)
	}
	if len(res.Warnings) == 0 {
		t.Error("expected warning for basic integrity")
	}

	// 5. Package name mismatch
	mismatchPayload := map[string]any{
		"package_name": "com.wrong.app",
		"deviceIntegrity": map[string]any{
			"deviceRecognitionVerdict": []string{"MEETS_STRONG_INTEGRITY"},
		},
		"timestamp": now.Unix(),
	}
	mismatchBytes, _ := json.Marshal(mismatchPayload)
	_, err = verifier.Verify(ctx, mismatchBytes)
	if !errors.Is(err, ErrAppIdentityMismatch) {
		t.Errorf("expected ErrAppIdentityMismatch, got %v", err)
	}

	// 6. Expired token
	expiredPayload := map[string]any{
		"package_name": "com.example.app",
		"deviceIntegrity": map[string]any{
			"deviceRecognitionVerdict": []string{"MEETS_STRONG_INTEGRITY"},
		},
		"timestamp": now.Add(-1 * time.Hour).Unix(),
	}
	expiredBytes, _ := json.Marshal(expiredPayload)
	_, err = verifier.Verify(ctx, expiredBytes)
	if !errors.Is(err, ErrEvidenceExpired) {
		t.Errorf("expected ErrEvidenceExpired, got %v", err)
	}

	// 7. Empty evidence
	_, err = verifier.Verify(ctx, nil)
	if !errors.Is(err, ErrEmptyEvidence) {
		t.Errorf("expected ErrEmptyEvidence, got %v", err)
	}

	// 8. Corrupt / Invalid token
	_, err = verifier.Verify(ctx, []byte("not valid json or cert"))
	if !errors.Is(err, ErrInvalidEvidence) {
		t.Errorf("expected ErrInvalidEvidence, got %v", err)
	}

	// 9. Minimum integrity requirement failure
	strictVerifier := NewAndroidVerifierWithConfig(AndroidConfig{
		ExpectedPackage: "com.example.app",
		MinIntegrity:    IntegrityStrong,
		Clock:           func() time.Time { return now },
	})
	_, err = strictVerifier.Verify(ctx, basicBytes)
	if !errors.Is(err, ErrIntegrityCheckFailed) {
		t.Errorf("expected ErrIntegrityCheckFailed for basic token on strict verifier, got %v", err)
	}

	// 10. Hardware KeyStore certificate attestation
	certDER := generateMockAndroidAttestationCert(t, "com.example.app", 2 /* StrongBox */)
	res, err = verifier.Verify(ctx, certDER)
	if err != nil {
		t.Fatalf("unexpected error for KeyStore cert: %v", err)
	}
	if !res.Verified || res.DeviceIntegrity != IntegrityStrong {
		t.Errorf("expected verified strong KeyStore cert, got %+v", res)
	}
}

// --- AppleVerifier Tests ---

func TestAppleVerifier(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	verifier := NewAppleVerifierWithConfig(AppleConfig{
		ExpectedAppID:    "TEAM123456.com.example.app",
		AllowDevelopment: true,
		MaxAge:           10 * time.Minute,
		Clock:            func() time.Time { return now },
	})

	// 1. Valid App Attest token (Production)
	prodPayload := map[string]any{
		"fmt":         "apple-appattest",
		"app_id":      "TEAM123456.com.example.app",
		"counter":     1,
		"receipt":     "mock-apple-receipt-data",
		"environment": "production",
		"aaguid":      "21fb3d21-f050-482d-a2d0-60b6c253e7f4",
		"timestamp":   now.Unix(),
	}
	prodBytes, _ := json.Marshal(prodPayload)

	res, err := verifier.Verify(ctx, prodBytes)
	if err != nil {
		t.Fatalf("unexpected error for production App Attest token: %v", err)
	}
	if !res.Verified {
		t.Error("expected Verified=true")
	}
	if res.DeviceIntegrity != IntegrityStrong {
		t.Errorf("expected DeviceIntegrity=%q, got %q", IntegrityStrong, res.DeviceIntegrity)
	}
	if res.AppIdentity != "TEAM123456.com.example.app" {
		t.Errorf("expected AppIdentity='TEAM123456.com.example.app', got %q", res.AppIdentity)
	}

	// 2. Base64 encoded App Attest token
	b64Prod := []byte(base64.StdEncoding.EncodeToString(prodBytes))
	res, err = verifier.Verify(ctx, b64Prod)
	if err != nil {
		t.Fatalf("unexpected error for base64 App Attest token: %v", err)
	}
	if !res.Verified {
		t.Error("expected Verified=true for base64 token")
	}

	// 3. Development App Attest token (allowed with warning)
	devPayload := map[string]any{
		"fmt":         "apple-appattest",
		"app_id":      "TEAM123456.com.example.app",
		"environment": "development",
		"aaguid":      AppleAppAttestDevAAGUID,
		"timestamp":   now.Unix(),
	}
	devBytes, _ := json.Marshal(devPayload)
	res, err = verifier.Verify(ctx, devBytes)
	if err != nil {
		t.Fatalf("unexpected error for dev token: %v", err)
	}
	if res.DeviceIntegrity != IntegrityBasic {
		t.Errorf("expected IntegrityBasic for dev token, got %q", res.DeviceIntegrity)
	}
	if len(res.Warnings) == 0 {
		t.Error("expected warning for dev token")
	}

	// 4. Reject development token when disallowed
	noDevVerifier := NewAppleVerifierWithConfig(AppleConfig{
		ExpectedAppID:    "TEAM123456.com.example.app",
		AllowDevelopment: false,
		Clock:            func() time.Time { return now },
	})
	_, err = noDevVerifier.Verify(ctx, devBytes)
	if !errors.Is(err, ErrIntegrityCheckFailed) {
		t.Errorf("expected ErrIntegrityCheckFailed for dev token when disallowed, got %v", err)
	}

	// 5. DeviceCheck claim token
	bit0 := true
	bit1 := false
	dcPayload := map[string]any{
		"fmt":            "apple-devicecheck",
		"device_token":   "device-token-xyz-123",
		"transaction_id": "tx-9999",
		"app_identity":   "TEAM123456.com.example.app",
		"bit0":           &bit0,
		"bit1":           &bit1,
		"timestamp":      now.Unix(),
	}
	dcBytes, _ := json.Marshal(dcPayload)
	res, err = verifier.Verify(ctx, dcBytes)
	if err != nil {
		t.Fatalf("unexpected error for DeviceCheck token: %v", err)
	}
	if res.DeviceIntegrity != IntegrityBasic {
		t.Errorf("expected IntegrityBasic for DeviceCheck, got %q", res.DeviceIntegrity)
	}
	if res.Details["device_token"] != "device-token-xyz-123" {
		t.Errorf("expected device_token detail, got %v", res.Details)
	}

	// 6. Binary Apple authenticator data
	rpIdHash := sha256.Sum256([]byte("TEAM123456.com.example.app"))
	var authData [53]byte
	copy(authData[0:32], rpIdHash[:])
	authData[32] = 0x41 // UP (0x01) + AT (0x40)
	binary.BigEndian.PutUint32(authData[33:37], 15) // signCount
	copy(authData[37:53], []byte(AppleAppAttestDevAAGUID))

	binaryVerifier := NewAppleVerifierWithConfig(AppleConfig{
		ExpectedAppID:    hex.EncodeToString(rpIdHash[:]),
		AllowDevelopment: true,
		Clock:            func() time.Time { return now },
	})
	res, err = binaryVerifier.Verify(ctx, authData[:])
	if err != nil {
		t.Fatalf("unexpected error for binary auth data: %v", err)
	}
	if res.DeviceIntegrity != IntegrityBasic {
		t.Errorf("expected basic integrity for dev AAGUID, got %s", res.DeviceIntegrity)
	}

	// 7. App ID mismatch
	mismatchPayload := map[string]any{
		"fmt":       "apple-appattest",
		"app_id":    "OTHERTEAM.com.different.app",
		"timestamp": now.Unix(),
	}
	mismatchBytes, _ := json.Marshal(mismatchPayload)
	_, err = verifier.Verify(ctx, mismatchBytes)
	if !errors.Is(err, ErrAppIdentityMismatch) {
		t.Errorf("expected ErrAppIdentityMismatch, got %v", err)
	}

	// 8. Expired token
	expiredPayload := map[string]any{
		"fmt":       "apple-appattest",
		"app_id":    "TEAM123456.com.example.app",
		"timestamp": now.Add(-2 * time.Hour).Unix(),
	}
	expiredBytes, _ := json.Marshal(expiredPayload)
	_, err = verifier.Verify(ctx, expiredBytes)
	if !errors.Is(err, ErrEvidenceExpired) {
		t.Errorf("expected ErrEvidenceExpired, got %v", err)
	}

	// 9. Empty evidence
	_, err = verifier.Verify(ctx, nil)
	if !errors.Is(err, ErrEmptyEvidence) {
		t.Errorf("expected ErrEmptyEvidence, got %v", err)
	}

	// 10. Corrupt evidence
	_, err = verifier.Verify(ctx, []byte("{bad-json"))
	if !errors.Is(err, ErrInvalidEvidence) {
		t.Errorf("expected ErrInvalidEvidence, got %v", err)
	}
}

// --- TPMVerifier Tests ---

func TestTPMVerifier(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	expectedDigest := "4a5e1e4baab89f3a32518a88c31bc87f618f76673e2cc77ab2127b7afdeda33b"

	verifier := NewTPMVerifierWithConfig(TPMConfig{
		ExpectedPCRDigest: expectedDigest,
		ExpectedNonce:     "test-tpm-nonce",
		ExpectedPCRs: map[int]string{
			0: "0000000000000000000000000000000000000000000000000000000000000000",
			7: "1111111111111111111111111111111111111111111111111111111111111111",
		},
		MaxAge: 10 * time.Minute,
		Clock:  func() time.Time { return now },
	})

	if verifier.Platform() != PlatformTPM {
		t.Fatalf("expected platform %q, got %q", PlatformTPM, verifier.Platform())
	}

	// 1. Valid JSON TPM Quote
	safeTrue := true
	quoteJSON := map[string]any{
		"magic":      "TPM_GENERATED",
		"type":       "quote",
		"pcr_digest": expectedDigest,
		"pcrs": map[string]string{
			"0": "0000000000000000000000000000000000000000000000000000000000000000",
			"7": "1111111111111111111111111111111111111111111111111111111111111111",
		},
		"nonce":            "test-tpm-nonce",
		"clock":            1234567,
		"safe":             &safeTrue,
		"firmware_version": "1.0.4",
		"timestamp":        now.Unix(),
	}
	quoteBytes, _ := json.Marshal(quoteJSON)

	res, err := verifier.Verify(ctx, quoteBytes)
	if err != nil {
		t.Fatalf("unexpected error for valid TPM quote JSON: %v", err)
	}
	if !res.Verified {
		t.Error("expected Verified=true")
	}
	if res.DeviceIntegrity != IntegrityStrong {
		t.Errorf("expected DeviceIntegrity=%q, got %q", IntegrityStrong, res.DeviceIntegrity)
	}
	if res.Details["pcr_digest"] != expectedDigest {
		t.Errorf("expected PCR digest detail, got %v", res.Details)
	}

	// 2. Valid Binary TPM 2.0 Quote structure
	binaryQuote := generateMockTPMBinaryQuote(expectedDigest, "test-tpm-nonce", 123456, true, 100200)
	binaryVerifier := NewTPMVerifierWithConfig(TPMConfig{
		ExpectedPCRDigest: expectedDigest,
		ExpectedNonce:     "test-tpm-nonce",
		Clock:             func() time.Time { return now },
	})
	res, err = binaryVerifier.Verify(ctx, binaryQuote)
	if err != nil {
		t.Fatalf("unexpected error for binary TPM quote: %v", err)
	}
	if !res.Verified || res.DeviceIntegrity != IntegrityStrong {
		t.Errorf("expected verified strong binary TPM result, got %+v", res)
	}

	// 3. PCR Digest mismatch
	badDigestQuote := map[string]any{
		"magic":      "TPM_GENERATED",
		"type":       "quote",
		"pcr_digest": "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff",
		"nonce":      "test-tpm-nonce",
		"timestamp":  now.Unix(),
	}
	badDigestBytes, _ := json.Marshal(badDigestQuote)
	_, err = verifier.Verify(ctx, badDigestBytes)
	if !errors.Is(err, ErrIntegrityCheckFailed) {
		t.Errorf("expected ErrIntegrityCheckFailed for digest mismatch, got %v", err)
	}

	// 4. Individual PCR measurement mismatch
	badPCRQuote := map[string]any{
		"magic":      "TPM_GENERATED",
		"type":       "quote",
		"pcr_digest": expectedDigest,
		"pcrs": map[string]string{
			"0": "0000000000000000000000000000000000000000000000000000000000000000",
			"7": "bad-tampered-pcr-measurement",
		},
		"nonce":     "test-tpm-nonce",
		"timestamp": now.Unix(),
	}
	badPCRBytes, _ := json.Marshal(badPCRQuote)
	_, err = verifier.Verify(ctx, badPCRBytes)
	if !errors.Is(err, ErrIntegrityCheckFailed) {
		t.Errorf("expected ErrIntegrityCheckFailed for PCR mismatch, got %v", err)
	}

	// 5. Nonce mismatch
	badNonceQuote := map[string]any{
		"magic":      "TPM_GENERATED",
		"type":       "quote",
		"pcr_digest": expectedDigest,
		"nonce":      "wrong-nonce",
		"timestamp":  now.Unix(),
	}
	badNonceBytes, _ := json.Marshal(badNonceQuote)
	_, err = verifier.Verify(ctx, badNonceBytes)
	if !errors.Is(err, ErrIntegrityCheckFailed) {
		t.Errorf("expected ErrIntegrityCheckFailed for nonce mismatch, got %v", err)
	}

	// 6. Unsafe clock warning
	safeFalse := false
	unsafeClockQuote := map[string]any{
		"magic":      "TPM_GENERATED",
		"type":       "quote",
		"pcr_digest": expectedDigest,
		"nonce":      "test-tpm-nonce",
		"safe":       &safeFalse,
		"timestamp":  now.Unix(),
	}
	unsafeBytes, _ := json.Marshal(unsafeClockQuote)
	lenientVerifier := NewTPMVerifierWithConfig(TPMConfig{
		ExpectedPCRDigest: expectedDigest,
		ExpectedNonce:     "test-tpm-nonce",
		Clock:             func() time.Time { return now },
	})
	res, err = lenientVerifier.Verify(ctx, unsafeBytes)
	if err != nil {
		t.Fatalf("unexpected error for unsafe clock: %v", err)
	}
	if len(res.Warnings) == 0 {
		t.Error("expected warning for unsafe TPM clock")
	}

	// 7. Expired quote
	expiredQuote := map[string]any{
		"magic":      "TPM_GENERATED",
		"type":       "quote",
		"pcr_digest": expectedDigest,
		"nonce":      "test-tpm-nonce",
		"timestamp":  now.Add(-2 * time.Hour).Unix(),
	}
	expiredBytes, _ := json.Marshal(expiredQuote)
	_, err = verifier.Verify(ctx, expiredBytes)
	if !errors.Is(err, ErrEvidenceExpired) {
		t.Errorf("expected ErrEvidenceExpired, got %v", err)
	}

	// 8. Empty evidence
	_, err = verifier.Verify(ctx, nil)
	if !errors.Is(err, ErrEmptyEvidence) {
		t.Errorf("expected ErrEmptyEvidence, got %v", err)
	}

	// 9. Invalid corrupt evidence
	_, err = verifier.Verify(ctx, []byte{0x01, 0x02, 0x03})
	if !errors.Is(err, ErrInvalidEvidence) {
		t.Errorf("expected ErrInvalidEvidence, got %v", err)
	}
}

// --- ConfidentialVerifier Tests ---

func TestConfidentialVerifier(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	expectedMREnclave := "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	expectedMRSigner := "d7a8fbb307d7809469ca9abcb0082e4f8d5651e46d3cdb762d02d0bf37c9e592"
	expectedMeasurement := "a1b2c3d4e5f60718293a4b5c6d7e8f00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff00"
	expectedReportData := "aabbccdd11223344"

	verifier := NewConfidentialVerifierWithConfig(ConfidentialConfig{
		ExpectedMREnclave:   expectedMREnclave,
		ExpectedMRSigner:    expectedMRSigner,
		ExpectedReportData:  expectedReportData,
		AllowDebug:          true,
		MaxAge:              10 * time.Minute,
		Clock:               func() time.Time { return now },
	})

	if verifier.Platform() != PlatformConfidentialLinux {
		t.Fatalf("expected platform %q, got %q", PlatformConfidentialLinux, verifier.Platform())
	}

	// 1. Valid SGX Quote JSON
	debugFalse := false
	sgxJSON := map[string]any{
		"tee_type":    "sgx",
		"mrenclave":   expectedMREnclave,
		"mrsigner":    expectedMRSigner,
		"report_data": expectedReportData,
		"debug":       &debugFalse,
		"timestamp":   now.Unix(),
	}
	sgxBytes, _ := json.Marshal(sgxJSON)

	res, err := verifier.Verify(ctx, sgxBytes)
	if err != nil {
		t.Fatalf("unexpected error for valid SGX JSON: %v", err)
	}
	if !res.Verified {
		t.Error("expected Verified=true")
	}
	if res.DeviceIntegrity != IntegrityStrong {
		t.Errorf("expected DeviceIntegrity=%q, got %q", IntegrityStrong, res.DeviceIntegrity)
	}
	if res.Details["mrenclave"] != expectedMREnclave {
		t.Errorf("expected MREnclave detail, got %v", res.Details)
	}

	// 2. Valid Binary SGX Quote v3
	binaryQuote := generateMockSGXBinaryQuote(expectedMREnclave, expectedMRSigner, expectedReportData, false)
	res, err = verifier.Verify(ctx, binaryQuote)
	if err != nil {
		t.Fatalf("unexpected error for binary SGX quote: %v", err)
	}
	if !res.Verified || res.DeviceIntegrity != IntegrityStrong {
		t.Errorf("expected verified strong binary SGX result, got %+v", res)
	}

	// 3. Valid Binary AMD SEV-SNP Report
	sevSnpVerifier := NewConfidentialVerifierWithConfig(ConfidentialConfig{
		ExpectedMeasurement: expectedMeasurement,
		ExpectedReportData:  expectedReportData,
		AllowDebug:          true,
		Clock:               func() time.Time { return now },
	})
	sevSnpReport := generateMockSEVSNPBinaryReport(expectedMeasurement, expectedReportData, false)
	res, err = sevSnpVerifier.Verify(ctx, sevSnpReport)
	if err != nil {
		t.Fatalf("unexpected error for binary SEV-SNP report: %v", err)
	}
	if !res.Verified || res.DeviceIntegrity != IntegrityStrong {
		t.Errorf("expected verified strong SEV-SNP result, got %+v", res)
	}

	// 4. Debug mode SGX quote (allowed with warning, downgraded to basic)
	debugTrue := true
	debugSGX := map[string]any{
		"tee_type":    "sgx",
		"mrenclave":   expectedMREnclave,
		"mrsigner":    expectedMRSigner,
		"report_data": expectedReportData,
		"debug":       &debugTrue,
		"timestamp":   now.Unix(),
	}
	debugBytes, _ := json.Marshal(debugSGX)
	res, err = verifier.Verify(ctx, debugBytes)
	if err != nil {
		t.Fatalf("unexpected error for debug enclave: %v", err)
	}
	if res.DeviceIntegrity != IntegrityBasic {
		t.Errorf("expected IntegrityBasic for debug enclave, got %q", res.DeviceIntegrity)
	}
	if len(res.Warnings) == 0 {
		t.Error("expected warning for debug enclave")
	}

	// 5. Reject debug mode when disallowed
	strictConfidential := NewConfidentialVerifierWithConfig(ConfidentialConfig{
		ExpectedMREnclave: expectedMREnclave,
		AllowDebug:        false,
		Clock:             func() time.Time { return now },
	})
	_, err = strictConfidential.Verify(ctx, debugBytes)
	if !errors.Is(err, ErrIntegrityCheckFailed) {
		t.Errorf("expected ErrIntegrityCheckFailed for debug enclave when disallowed, got %v", err)
	}

	// 6. MRENCLAVE mismatch
	badMREnclave := map[string]any{
		"tee_type":  "sgx",
		"mrenclave": "0000000000000000000000000000000000000000000000000000000000000000",
		"timestamp": now.Unix(),
	}
	badBytes, _ := json.Marshal(badMREnclave)
	_, err = verifier.Verify(ctx, badBytes)
	if !errors.Is(err, ErrIntegrityCheckFailed) {
		t.Errorf("expected ErrIntegrityCheckFailed for MRENCLAVE mismatch, got %v", err)
	}

	// 7. Expired confidential evidence
	expiredSGX := map[string]any{
		"tee_type":  "sgx",
		"mrenclave": expectedMREnclave,
		"timestamp": now.Add(-2 * time.Hour).Unix(),
	}
	expiredBytes, _ := json.Marshal(expiredSGX)
	_, err = verifier.Verify(ctx, expiredBytes)
	if !errors.Is(err, ErrEvidenceExpired) {
		t.Errorf("expected ErrEvidenceExpired, got %v", err)
	}

	// 8. Empty evidence
	_, err = verifier.Verify(ctx, nil)
	if !errors.Is(err, ErrEmptyEvidence) {
		t.Errorf("expected ErrEmptyEvidence, got %v", err)
	}

	// 9. Invalid / corrupt evidence
	_, err = verifier.Verify(ctx, []byte("{broken-json"))
	if !errors.Is(err, ErrInvalidEvidence) {
		t.Errorf("expected ErrInvalidEvidence, got %v", err)
	}
}

// --- Registry Tests ---

func TestRegistry(t *testing.T) {
	ctx := context.Background()

	// 1. Custom registry registration and dispatch
	reg := NewRegistry()
	noneVerifier := NewNoneVerifier()
	reg.Register(noneVerifier)

	retrieved, found := reg.VerifierFor(PlatformNone)
	if !found || retrieved != noneVerifier {
		t.Fatal("expected to retrieve registered NoneVerifier")
	}

	res, err := reg.Verify(ctx, PlatformNone, nil)
	if err != nil {
		t.Fatalf("unexpected error during registry verify: %v", err)
	}
	if !res.Verified {
		t.Error("expected Verified=true")
	}

	// 2. Unknown platform error
	_, err = reg.Verify(ctx, "nonexistent-platform", nil)
	if !errors.Is(err, ErrUnknownPlatform) {
		t.Errorf("expected ErrUnknownPlatform, got %v", err)
	}

	// 3. Empty platform error
	_, err = reg.Verify(ctx, "   ", nil)
	if !errors.Is(err, ErrEmptyPlatform) {
		t.Errorf("expected ErrEmptyPlatform, got %v", err)
	}

	// 4. DefaultRegistry platforms and alias routing
	defaultReg := DefaultRegistry()
	platforms := defaultReg.Platforms()
	if len(platforms) < 5 {
		t.Errorf("expected at least 5 platforms, got %d: %v", len(platforms), platforms)
	}

	// Verify all canonical platforms route properly
	canonicalPlatforms := []string{
		PlatformNone,
		PlatformAndroid,
		PlatformIOS,
		PlatformTPM,
		PlatformConfidentialLinux,
		PlatformDesktopMacOS,
		PlatformDesktopWindows,
		PlatformDesktopLinux,
		PlatformWeb,
		PlatformSGX,
		PlatformSEVSNP,
	}

	for _, plat := range canonicalPlatforms {
		if _, ok := defaultReg.VerifierFor(plat); !ok {
			t.Errorf("expected DefaultRegistry to support %q", plat)
		}
	}

	// Verify aliases route properly
	aliasChecks := map[string]string{
		"linux":         PlatformDesktopLinux,
		"macos":         PlatformDesktopMacOS,
		"darwin":        PlatformDesktopMacOS,
		"darwin/arm64":  PlatformDesktopMacOS,
		"windows":       PlatformDesktopWindows,
		"confidential":  PlatformConfidentialLinux,
	}

	for alias := range aliasChecks {
		if _, ok := defaultReg.VerifierFor(alias); !ok {
			t.Errorf("expected alias %q to resolve in DefaultRegistry", alias)
		}
	}

	// 5. Concurrency safety test
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, _ = defaultReg.Verify(ctx, PlatformNone, nil)
			if idx%5 == 0 {
				defaultReg.Register(NewNoneVerifierWithPlatform(PlatformNone))
			}
		}(i)
	}
	wg.Wait()
}

// --- Result Helpers Test ---

func TestResultHelpers(t *testing.T) {
	res := &Result{
		Platform:        PlatformAndroid,
		Verified:        true,
		DeviceIntegrity: IntegrityStrong,
		Timestamp:       1234567890,
	}

	if !res.IsStrong() {
		t.Error("expected IsStrong()=true")
	}
	if !res.IsBasicOrStrong() {
		t.Error("expected IsBasicOrStrong()=true")
	}

	res.SetDetail("key", "val")
	if res.Details["key"] != "val" {
		t.Errorf("expected detail 'val', got %q", res.Details["key"])
	}

	res.AddWarning("test-warning")
	if len(res.Warnings) != 1 || res.Warnings[0] != "test-warning" {
		t.Errorf("expected warning 'test-warning', got %v", res.Warnings)
	}

	cloned := res.Clone()
	if cloned == nil || cloned == res {
		t.Fatal("expected non-nil independent cloned pointer")
	}
	if cloned.Details["key"] != "val" {
		t.Errorf("expected cloned detail 'val', got %q", cloned.Details["key"])
	}

	cloned.SetDetail("key", "mutated")
	if res.Details["key"] == "mutated" {
		t.Error("modifying cloned details affected original result")
	}

	var nilResult *Result
	if nilResult.IsStrong() {
		t.Error("nilResult.IsStrong() should be false")
	}
	if nilResult.Clone() != nil {
		t.Error("nilResult.Clone() should be nil")
	}
}

// --- Helper Functions to build mock binary attestations ---

func generateMockAndroidAttestationCert(t *testing.T, packageName string, securityLevel int) []byte {
	privKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate RSA key: %v", err)
	}

	keyDesc := androidKeyDescription{
		AttestationVersion:       3,
		AttestationSecurityLevel: asn1.Enumerated(securityLevel),
		KeymasterVersion:         4,
		KeymasterSecurityLevel:   asn1.Enumerated(securityLevel),
		AttestationChallenge:     []byte("test-challenge"),
		UniqueID:                 []byte("device-unique-id"),
	}

	extValue, err := asn1.Marshal(keyDesc)
	if err != nil {
		t.Fatalf("failed to marshal ASN.1 key description: %v", err)
	}

	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			CommonName: packageName,
		},
		NotBefore: time.Now().Add(-1 * time.Minute),
		NotAfter:  time.Now().Add(24 * time.Hour),
		ExtraExtensions: []pkix.Extension{
			{
				Id:    AndroidKeyAttestationOID,
				Value: extValue,
			},
		},
	}

	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &privKey.PublicKey, privKey)
	if err != nil {
		t.Fatalf("failed to create x509 certificate: %v", err)
	}

	return certDER
}

func generateMockTPMBinaryQuote(pcrDigestHex, nonce string, clock uint64, isSafe bool, firmwareVersion uint64) []byte {
	pcrDigest, _ := hex.DecodeString(pcrDigestHex)
	nonceBytes := []byte(nonce)
	signerBytes := []byte("mock-aik-signer-name")

	buf := new(bytes.Buffer)
	// 1. Magic "\xffTCG"
	_ = binary.Write(buf, binary.BigEndian, TPMGeneratedMagic)
	// 2. Type 0x8018
	_ = binary.Write(buf, binary.BigEndian, TPMStAttestQuote)
	// 3. qualifiedSigner
	_ = binary.Write(buf, binary.BigEndian, uint16(len(signerBytes)))
	buf.Write(signerBytes)
	// 4. extraData (nonce)
	_ = binary.Write(buf, binary.BigEndian, uint16(len(nonceBytes)))
	buf.Write(nonceBytes)
	// 5. clockInfo
	_ = binary.Write(buf, binary.BigEndian, clock)
	_ = binary.Write(buf, binary.BigEndian, uint32(0)) // resetCount
	_ = binary.Write(buf, binary.BigEndian, uint32(0)) // restartCount
	safeByte := byte(0)
	if isSafe {
		safeByte = 1
	}
	buf.WriteByte(safeByte)
	// 6. firmwareVersion
	_ = binary.Write(buf, binary.BigEndian, firmwareVersion)
	// 7. TPMS_QUOTE_INFO:
	// pcrSelect count (1)
	_ = binary.Write(buf, binary.BigEndian, uint32(1))
	// hashAlg (0x000b = SHA256), size (3 bytes), select bytes (0x01, 0x00, 0x80)
	_ = binary.Write(buf, binary.BigEndian, uint16(0x000b))
	buf.WriteByte(3)
	buf.Write([]byte{0x01, 0x00, 0x80})
	// pcrDigest
	_ = binary.Write(buf, binary.BigEndian, uint16(len(pcrDigest)))
	buf.Write(pcrDigest)

	return buf.Bytes()
}

func generateMockSGXBinaryQuote(mrEnclaveHex, mrSignerHex, reportDataHex string, isDebug bool) []byte {
	quote := make([]byte, SGXMinQuoteSize)

	// Version 3
	binary.LittleEndian.PutUint16(quote[0:2], SGXQuoteV3)
	// att_key_type 2
	binary.LittleEndian.PutUint16(quote[2:4], 2)
	// tee_type 0 (SGX)
	binary.LittleEndian.PutUint32(quote[4:8], 0)

	// Report body starts at offset 48 (384 bytes)
	// Attributes start at offset 48 + 48 = 96
	if isDebug {
		quote[96] = 0x02 // DEBUG bit
	}

	mrEnclave, _ := hex.DecodeString(mrEnclaveHex)
	copy(quote[112:144], mrEnclave) // MRENCLAVE at offset 48 + 64 = 112 (32 bytes)

	mrSigner, _ := hex.DecodeString(mrSignerHex)
	copy(quote[176:208], mrSigner) // MRSIGNER at offset 48 + 128 = 176 (32 bytes)

	binary.LittleEndian.PutUint16(quote[216:218], 1) // isv_prod_id
	binary.LittleEndian.PutUint16(quote[218:220], 2) // isv_svn

	reportData, _ := hex.DecodeString(reportDataHex)
	copy(quote[280:344], reportData) // REPORTDATA at offset 48 + 232 = 280 (64 bytes)

	return quote
}

func generateMockSEVSNPBinaryReport(measurementHex, reportDataHex string, isDebug bool) []byte {
	report := make([]byte, SEVSNPReportSize)

	binary.LittleEndian.PutUint32(report[0:4], 1) // version
	binary.LittleEndian.PutUint32(report[4:8], 2) // guest_svn

	var policy uint64 = 0x30000
	if isDebug {
		policy |= SEVSNPPolicyDebugBit
	}
	binary.LittleEndian.PutUint64(report[8:16], policy)

	reportData, _ := hex.DecodeString(reportDataHex)
	copy(report[80:144], reportData) // 64 bytes REPORT_DATA

	measurement, _ := hex.DecodeString(measurementHex)
	copy(report[144:192], measurement) // 48 bytes MEASUREMENT (SHA-384)

	return report
}
