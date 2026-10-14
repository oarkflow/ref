package attest

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// SGX and SEV-SNP Constants.
const (
	// SGXQuoteV3 is the version number for Intel SGX Quote Version 3.
	SGXQuoteV3 uint16 = 3

	// SGXQuoteV4 is the version number for Intel SGX Quote Version 4.
	SGXQuoteV4 uint16 = 4

	// SGXMinQuoteSize is the minimum byte size for an Intel SGX Quote (Header 48 bytes + Report Body 384 bytes).
	SGXMinQuoteSize = 432

	// SEVSNPReportSize is the exact byte size of an AMD SEV-SNP Attestation Report.
	SEVSNPReportSize = 1184

	// SEVSNPPolicyDebugBit is the bit in the SEV-SNP policy indicating debug mode is permitted.
	SEVSNPPolicyDebugBit uint64 = 1 << 19
)

// ConfidentialConfig specifies validation parameters for confidential computing attestation evidence
// (Intel SGX and AMD SEV-SNP).
type ConfidentialConfig struct {
	// ExpectedMREnclave optionally enforces that the Intel SGX enclave measurement matches this hex digest.
	ExpectedMREnclave string

	// ExpectedMRSigner optionally enforces that the Intel SGX enclave author key matches this hex digest.
	ExpectedMRSigner string

	// ExpectedMeasurement optionally enforces that the AMD SEV-SNP launch measurement matches this hex digest.
	ExpectedMeasurement string

	// ExpectedReportData optionally enforces that the user report data / nonce binding matches this value.
	ExpectedReportData string

	// AllowDebug specifies whether enclaves or VMs running in debug mode are permitted (defaults to true with warning).
	AllowDebug bool

	// MaxAge specifies the maximum allowed age of the attestation report. A zero value skips freshness validation.
	MaxAge time.Duration

	// Platform overrides the default platform identifier ("linux-confidential", "sgx", or "sev-snp").
	Platform string

	// Clock provides the time reference for freshness checks. Defaults to time.Now.
	Clock func() time.Time
}

// ConfidentialVerifier evaluates Intel SGX quotes and AMD SEV-SNP attestation reports.
type ConfidentialVerifier struct {
	platform            string
	expectedMREnclave   string
	expectedMRSigner    string
	expectedMeasurement string
	expectedReportData  string
	allowDebug          bool
	maxAge              time.Duration
	clock               func() time.Time
}

// NewConfidentialVerifier creates a ConfidentialVerifier for the "linux-confidential" platform.
func NewConfidentialVerifier() *ConfidentialVerifier {
	return NewConfidentialVerifierWithConfig(ConfidentialConfig{
		Platform:   PlatformConfidentialLinux,
		AllowDebug: true,
	})
}

// NewConfidentialVerifierWithPlatform creates a ConfidentialVerifier for a specific platform name
// (e.g. "sgx", "sev-snp", or "linux-confidential").
func NewConfidentialVerifierWithPlatform(platform string) *ConfidentialVerifier {
	if platform == "" {
		platform = PlatformConfidentialLinux
	}
	return NewConfidentialVerifierWithConfig(ConfidentialConfig{
		Platform:   platform,
		AllowDebug: true,
	})
}

// NewConfidentialVerifierWithConfig creates a ConfidentialVerifier with customized validation expectations.
func NewConfidentialVerifierWithConfig(config ConfidentialConfig) *ConfidentialVerifier {
	clock := config.Clock
	if clock == nil {
		clock = time.Now
	}
	platform := config.Platform
	if platform == "" {
		platform = PlatformConfidentialLinux
	}

	return &ConfidentialVerifier{
		platform:            platform,
		expectedMREnclave:   normalizeHex(config.ExpectedMREnclave),
		expectedMRSigner:    normalizeHex(config.ExpectedMRSigner),
		expectedMeasurement: normalizeHex(config.ExpectedMeasurement),
		expectedReportData:  strings.TrimSpace(config.ExpectedReportData),
		allowDebug:          config.AllowDebug,
		maxAge:              config.MaxAge,
		clock:               clock,
	}
}

// Platform returns the platform identifier handled by this verifier.
func (verifier *ConfidentialVerifier) Platform() string {
	return verifier.platform
}

// Verify evaluates SGX quote binary structures, SEV-SNP reports, or structured JSON attestation reports.
func (verifier *ConfidentialVerifier) Verify(ctx context.Context, evidence []byte) (*Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	payload, err := decodeEvidencePayload(evidence)
	if err != nil {
		return nil, err
	}

	// 1. Check for AMD SEV-SNP Report binary (1184 bytes)
	if len(payload) == SEVSNPReportSize && payload[0] != '{' {
		return verifier.parseBinarySEVSNPReport(payload)
	}

	// 2. Check for Intel SGX Quote binary (>= 432 bytes)
	if len(payload) >= SGXMinQuoteSize && payload[0] != '{' {
		version := binary.LittleEndian.Uint16(payload[0:2])
		if version == SGXQuoteV3 || version == SGXQuoteV4 {
			return verifier.parseBinarySGXQuote(payload)
		}
	}

	// 3. Parse as structured JSON attestation report
	return verifier.parseJSONConfidentialEvidence(payload)
}

func (verifier *ConfidentialVerifier) parseBinarySGXQuote(data []byte) (*Result, error) {
	if len(data) < SGXMinQuoteSize {
		return nil, fmt.Errorf("%w: SGX quote size %d is below minimum %d bytes", ErrInvalidEvidence, len(data), SGXMinQuoteSize)
	}

	version := binary.LittleEndian.Uint16(data[0:2])
	teeType := binary.LittleEndian.Uint32(data[4:8])
	vendorID := hex.EncodeToString(data[12:28])

	// Report Body begins at offset 48 (384 bytes)
	reportBody := data[48:432]
	attributes := reportBody[48:64] // offset 48 + 48 = 96
	isDebug := attributes[0]&0x02 != 0

	mrEnclave := hex.EncodeToString(reportBody[64:96]) // offset 48 + 64 = 112 (32 bytes)
	mrSigner := hex.EncodeToString(reportBody[128:160]) // offset 48 + 128 = 176 (32 bytes)
	isvProdID := binary.LittleEndian.Uint16(reportBody[168:170]) // offset 48 + 168 = 216
	isvSVN := binary.LittleEndian.Uint16(reportBody[170:172]) // offset 48 + 170 = 218
	reportData := hex.EncodeToString(reportBody[232:296]) // offset 48 + 232 = 280 (64 bytes)

	// Validate expectations
	if verifier.expectedMREnclave != "" && !strings.EqualFold(mrEnclave, verifier.expectedMREnclave) {
		return nil, fmt.Errorf("%w: SGX MRENCLAVE mismatch (got %s, expected %s)", ErrIntegrityCheckFailed, mrEnclave, verifier.expectedMREnclave)
	}

	if verifier.expectedMRSigner != "" && !strings.EqualFold(mrSigner, verifier.expectedMRSigner) {
		return nil, fmt.Errorf("%w: SGX MRSIGNER mismatch (got %s, expected %s)", ErrIntegrityCheckFailed, mrSigner, verifier.expectedMRSigner)
	}

	if verifier.expectedReportData != "" {
		expectedNorm := normalizeHex(verifier.expectedReportData)
		if !strings.EqualFold(reportData, expectedNorm) && !strings.Contains(reportData, expectedNorm) {
			return nil, fmt.Errorf("%w: SGX REPORTDATA mismatch (got %s, expected %s)", ErrIntegrityCheckFailed, reportData, verifier.expectedReportData)
		}
	}

	deviceIntegrity := IntegrityStrong
	var warnings []string

	if isDebug {
		if !verifier.allowDebug {
			return nil, fmt.Errorf("%w: SGX enclave is running in debug mode (debug not allowed)", ErrIntegrityCheckFailed)
		}
		deviceIntegrity = IntegrityBasic
		warnings = append(warnings, "SGX enclave is running in debug mode; enclave memory isolation is not hardware-enforced")
	}

	now := verifier.clock()
	teeName := "intel_sgx"
	if teeType == 0x81 {
		teeName = "intel_tdx"
	}

	details := map[string]string{
		"attestation_type": "intel_sgx_binary_quote",
		"quote_version":    strconv.Itoa(int(version)),
		"tee_type":         teeName,
		"vendor_id":        vendorID,
		"mrenclave":        mrEnclave,
		"mrsigner":         mrSigner,
		"isv_prod_id":      strconv.Itoa(int(isvProdID)),
		"isv_svn":          strconv.Itoa(int(isvSVN)),
		"report_data":      reportData,
		"debug":            strconv.FormatBool(isDebug),
	}

	appIdentity := fmt.Sprintf("mrenclave:%s", mrEnclave)

	return &Result{
		Platform:        verifier.platform,
		Verified:        true,
		AppIdentity:     appIdentity,
		DeviceIntegrity: deviceIntegrity,
		Timestamp:       now.Unix(),
		Details:         details,
		Warnings:        warnings,
	}, nil
}

func (verifier *ConfidentialVerifier) parseBinarySEVSNPReport(data []byte) (*Result, error) {
	if len(data) != SEVSNPReportSize {
		return nil, fmt.Errorf("%w: SEV-SNP report size is %d bytes, expected %d", ErrInvalidEvidence, len(data), SEVSNPReportSize)
	}

	version := binary.LittleEndian.Uint32(data[0:4])
	guestSVN := binary.LittleEndian.Uint32(data[4:8])
	policy := binary.LittleEndian.Uint64(data[8:16])
	familyID := hex.EncodeToString(data[16:32])
	imageID := hex.EncodeToString(data[32:48])
	vmpl := binary.LittleEndian.Uint32(data[48:52])

	reportData := hex.EncodeToString(data[80:144]) // 64 bytes
	measurement := hex.EncodeToString(data[144:192]) // 48 bytes (SHA-384 launch digest)
	hostData := hex.EncodeToString(data[192:224]) // 32 bytes

	isDebug := policy&SEVSNPPolicyDebugBit != 0

	// Validate expectations
	if verifier.expectedMeasurement != "" && !strings.EqualFold(measurement, verifier.expectedMeasurement) {
		return nil, fmt.Errorf("%w: SEV-SNP measurement mismatch (got %s, expected %s)", ErrIntegrityCheckFailed, measurement, verifier.expectedMeasurement)
	}

	if verifier.expectedReportData != "" {
		expectedNorm := normalizeHex(verifier.expectedReportData)
		if !strings.EqualFold(reportData, expectedNorm) && !strings.Contains(reportData, expectedNorm) {
			return nil, fmt.Errorf("%w: SEV-SNP REPORT_DATA mismatch (got %s, expected %s)", ErrIntegrityCheckFailed, reportData, verifier.expectedReportData)
		}
	}

	deviceIntegrity := IntegrityStrong
	var warnings []string

	if isDebug {
		if !verifier.allowDebug {
			return nil, fmt.Errorf("%w: SEV-SNP guest is running in debug mode (debug not allowed)", ErrIntegrityCheckFailed)
		}
		deviceIntegrity = IntegrityBasic
		warnings = append(warnings, "SEV-SNP guest is running in debug mode; VM memory encryption is inspectable by hypervisor")
	}

	now := verifier.clock()
	details := map[string]string{
		"attestation_type": "amd_sev_snp_binary_report",
		"report_version":   strconv.Itoa(int(version)),
		"tee_type":         "amd_sev_snp",
		"guest_svn":        strconv.Itoa(int(guestSVN)),
		"policy":           fmt.Sprintf("0x%016x", policy),
		"family_id":        familyID,
		"image_id":         imageID,
		"vmpl":             strconv.Itoa(int(vmpl)),
		"measurement":      measurement,
		"report_data":      reportData,
		"host_data":        hostData,
		"debug":            strconv.FormatBool(isDebug),
	}

	appIdentity := fmt.Sprintf("measurement:%s", measurement)

	return &Result{
		Platform:        verifier.platform,
		Verified:        true,
		AppIdentity:     appIdentity,
		DeviceIntegrity: deviceIntegrity,
		Timestamp:       now.Unix(),
		Details:         details,
		Warnings:        warnings,
	}, nil
}

type confidentialJSONEvidence struct {
	TEEType         string `json:"tee_type"`
	MREnclave       string `json:"mrenclave"`
	MRSigner        string `json:"mrsigner"`
	Measurement     string `json:"measurement"`
	ReportData      string `json:"report_data"`
	ISVProdID       any    `json:"isv_prod_id"`
	ISVSVN          any    `json:"isv_svn"`
	GuestSVN        any    `json:"guest_svn"`
	Debug           *bool  `json:"debug"`
	AppIdentity     string `json:"app_identity"`
	DeviceIntegrity string `json:"device_integrity"`
	Timestamp       int64  `json:"timestamp"`
	ExpiresAt       int64  `json:"expires_at"`
}

func (verifier *ConfidentialVerifier) parseJSONConfidentialEvidence(data []byte) (*Result, error) {
	var payload confidentialJSONEvidence
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidEvidence, err)
	}

	mrEnclave := normalizeHex(payload.MREnclave)
	mrSigner := normalizeHex(payload.MRSigner)
	measurement := normalizeHex(payload.Measurement)
	reportData := normalizeHex(payload.ReportData)

	// Validate expectations
	if verifier.expectedMREnclave != "" && mrEnclave != "" && !strings.EqualFold(mrEnclave, verifier.expectedMREnclave) {
		return nil, fmt.Errorf("%w: SGX MRENCLAVE mismatch (got %s, expected %s)", ErrIntegrityCheckFailed, mrEnclave, verifier.expectedMREnclave)
	}

	if verifier.expectedMRSigner != "" && mrSigner != "" && !strings.EqualFold(mrSigner, verifier.expectedMRSigner) {
		return nil, fmt.Errorf("%w: SGX MRSIGNER mismatch (got %s, expected %s)", ErrIntegrityCheckFailed, mrSigner, verifier.expectedMRSigner)
	}

	if verifier.expectedMeasurement != "" && measurement != "" && !strings.EqualFold(measurement, verifier.expectedMeasurement) {
		return nil, fmt.Errorf("%w: SEV-SNP measurement mismatch (got %s, expected %s)", ErrIntegrityCheckFailed, measurement, verifier.expectedMeasurement)
	}

	if verifier.expectedReportData != "" && reportData != "" {
		expectedNorm := normalizeHex(verifier.expectedReportData)
		if !strings.EqualFold(reportData, expectedNorm) && !strings.Contains(reportData, expectedNorm) {
			return nil, fmt.Errorf("%w: REPORTDATA mismatch (got %s, expected %s)", ErrIntegrityCheckFailed, reportData, verifier.expectedReportData)
		}
	}

	// Freshness
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

	// Device integrity & Debug detection
	deviceIntegrity := IntegrityStrong
	var warnings []string

	isDebug := payload.Debug != nil && *payload.Debug
	if isDebug {
		if !verifier.allowDebug {
			return nil, fmt.Errorf("%w: confidential environment is running in debug mode (debug not allowed)", ErrIntegrityCheckFailed)
		}
		deviceIntegrity = IntegrityBasic
		warnings = append(warnings, "confidential environment is running in debug mode; memory isolation is not enforced")
	}

	if payload.DeviceIntegrity != "" {
		switch strings.ToLower(payload.DeviceIntegrity) {
		case IntegrityStrong:
			if !isDebug {
				deviceIntegrity = IntegrityStrong
			}
		case IntegrityBasic:
			deviceIntegrity = IntegrityBasic
		case IntegrityNone:
			deviceIntegrity = IntegrityNone
		}
	}

	// App identity
	appIdentity := payload.AppIdentity
	if appIdentity == "" {
		if mrEnclave != "" {
			appIdentity = fmt.Sprintf("mrenclave:%s", mrEnclave)
		} else if measurement != "" {
			appIdentity = fmt.Sprintf("measurement:%s", measurement)
		} else {
			appIdentity = "confidential-enclave"
		}
	}

	details := make(map[string]string)
	teeType := payload.TEEType
	if teeType == "" {
		if mrEnclave != "" {
			teeType = "intel_sgx"
		} else if measurement != "" {
			teeType = "amd_sev_snp"
		} else {
			teeType = "confidential_enclave"
		}
	}
	details["tee_type"] = teeType
	if mrEnclave != "" {
		details["mrenclave"] = mrEnclave
	}
	if mrSigner != "" {
		details["mrsigner"] = mrSigner
	}
	if measurement != "" {
		details["measurement"] = measurement
	}
	if reportData != "" {
		details["report_data"] = reportData
	}
	if payload.ISVProdID != nil {
		details["isv_prod_id"] = fmt.Sprintf("%v", payload.ISVProdID)
	}
	if payload.ISVSVN != nil {
		details["isv_svn"] = fmt.Sprintf("%v", payload.ISVSVN)
	}
	if payload.GuestSVN != nil {
		details["guest_svn"] = fmt.Sprintf("%v", payload.GuestSVN)
	}
	details["debug"] = strconv.FormatBool(isDebug)

	return &Result{
		Platform:        verifier.platform,
		Verified:        deviceIntegrity != IntegrityNone,
		AppIdentity:     appIdentity,
		DeviceIntegrity: deviceIntegrity,
		Timestamp:       tokenTimeSec,
		Details:         details,
		Warnings:        warnings,
	}, nil
}
