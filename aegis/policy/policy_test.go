package policy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sampleValidJSON = `{
  "application": {
    "id": "app.aegis.vault",
    "version": "1.0.0"
  },
  "protection": {
    "default": "standard",
    "strip_metadata": true,
    "diversify": true
  },
  "functions": {
    "ProcessPayment": {
      "confidentiality": "critical",
      "execution": "remote_confidential",
      "authorization": "per_call",
      "fallback": "hardened_local",
      "fallback_acknowledged": true
    },
    "ComputeMetrics": {
      "confidentiality": "standard",
      "execution": "ordinary_local",
      "authorization": "none"
    },
    "SignToken": {
      "confidentiality": "high",
      "execution": "isolated_local",
      "authorization": "required",
      "fallback": "ordinary_local",
      "fallback_acknowledged": true
    }
  }
}`

func TestParseJSON_Valid(t *testing.T) {
	p, err := ParseJSON([]byte(sampleValidJSON))
	if err != nil {
		t.Fatalf("ParseJSON failed unexpectedly: %v", err)
	}

	if p.Application.ID != "app.aegis.vault" {
		t.Errorf("expected Application.ID 'app.aegis.vault', got %q", p.Application.ID)
	}
	if p.Application.Version != "1.0.0" {
		t.Errorf("expected Application.Version '1.0.0', got %q", p.Application.Version)
	}
	if p.Protection.Default != LevelStandard {
		t.Errorf("expected Protection.Default 'standard', got %q", p.Protection.Default)
	}
	if !p.Protection.StripMetadata || !p.Protection.Diversify {
		t.Errorf("expected StripMetadata and Diversify to be true")
	}
	if len(p.Functions) != 3 {
		t.Fatalf("expected 3 functions, got %d", len(p.Functions))
	}

	payment, ok := p.Functions["ProcessPayment"]
	if !ok {
		t.Fatalf("missing function ProcessPayment")
	}
	if payment.Confidentiality != ConfidentialityCritical {
		t.Errorf("expected ConfidentialityCritical, got %q", payment.Confidentiality)
	}
	if payment.Execution != ExecRemoteConfidential {
		t.Errorf("expected ExecRemoteConfidential, got %q", payment.Execution)
	}
	if payment.Authorization != AuthPerCall {
		t.Errorf("expected AuthPerCall, got %q", payment.Authorization)
	}
	if payment.Fallback != ExecHardenedLocal {
		t.Errorf("expected ExecHardenedLocal fallback, got %q", payment.Fallback)
	}
	if !payment.FallbackAcknowledged {
		t.Errorf("expected fallback_acknowledged=true")
	}
}

func TestParseJSON_Invalid(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr string
	}{
		{
			name:    "empty input",
			input:   "",
			wantErr: "policy: empty policy data",
		},
		{
			name:    "whitespace only",
			input:   "   \t\n  ",
			wantErr: "policy: empty policy data",
		},
		{
			name:    "malformed JSON",
			input:   `{ "application": { "id": "test" `,
			wantErr: "policy: failed to parse JSON policy",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p, err := ParseJSON([]byte(tc.input))
			if err == nil {
				t.Fatalf("expected error containing %q, got nil policy: %+v", tc.wantErr, p)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("expected error to contain %q, got %q", tc.wantErr, err.Error())
			}
		})
	}
}

func TestParseFile(t *testing.T) {
	tempDir := t.TempDir()

	// 1. Valid JSON file
	jsonPath := filepath.Join(tempDir, "policy.json")
	if err := os.WriteFile(jsonPath, []byte(sampleValidJSON), 0o600); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}
	p, err := ParseFile(jsonPath)
	if err != nil {
		t.Fatalf("ParseFile failed on valid JSON: %v", err)
	}
	if p.Application.ID != "app.aegis.vault" {
		t.Errorf("unexpected Application.ID: %q", p.Application.ID)
	}

	// 2. Missing file
	nonExistent := filepath.Join(tempDir, "does_not_exist.json")
	_, err = ParseFile(nonExistent)
	if err == nil {
		t.Fatalf("expected error for non-existent file, got nil")
	}
	if !strings.Contains(err.Error(), "policy: failed to read policy file") {
		t.Errorf("expected read file error prefix, got %q", err.Error())
	}

	// 3. YAML file returns informative unsupported error
	yamlPath := filepath.Join(tempDir, "policy.yaml")
	if err := os.WriteFile(yamlPath, []byte("application:\n  id: test\n"), 0o600); err != nil {
		t.Fatalf("failed to write yaml test file: %v", err)
	}
	_, err = ParseFile(yamlPath)
	if err == nil {
		t.Fatalf("expected YAML unsupported error, got nil")
	}
	if !strings.Contains(err.Error(), "YAML format (.yaml) is not yet supported") {
		t.Errorf("expected YAML notice, got %q", err.Error())
	}

	// 4. File without extension assumes JSON
	noExtPath := filepath.Join(tempDir, "policyfile")
	if err := os.WriteFile(noExtPath, []byte(sampleValidJSON), 0o600); err != nil {
		t.Fatalf("failed to write no-ext test file: %v", err)
	}
	p, err = ParseFile(noExtPath)
	if err != nil {
		t.Fatalf("ParseFile failed on extensionless JSON: %v", err)
	}
	if p.Application.ID != "app.aegis.vault" {
		t.Errorf("unexpected Application.ID: %q", p.Application.ID)
	}
}

func TestValidate_Valid(t *testing.T) {
	p := &Policy{
		Application: ApplicationPolicy{
			ID:      "com.example.service",
			Version: "2.4.1",
		},
		Protection: ProtectionDefaults{
			Default:       LevelHardened,
			StripMetadata: true,
			Diversify:     false,
		},
		Functions: map[string]FuncPolicy{
			"AuthLogin": {
				Confidentiality:      ConfidentialityHigh,
				Execution:            ExecRemoteConfidential,
				Authorization:        AuthRequired,
				Fallback:             ExecIsolatedLocal,
				FallbackAcknowledged: true,
			},
			"HealthCheck": {
				Confidentiality: ConfidentialityStandard,
				Execution:       ExecOrdinaryLocal,
				Authorization:   AuthNone,
			},
		},
	}

	res := Validate(p)
	if !res.OK() {
		t.Fatalf("expected valid policy, got errors: %v", res.Errors)
	}
	if len(res.Errors) != 0 {
		t.Errorf("expected 0 errors, got %d: %v", len(res.Errors), res.Errors)
	}
	if res.Err() != nil {
		t.Errorf("expected nil error, got %v", res.Err())
	}
	if err := p.Validate(); err != nil {
		t.Errorf("p.Validate() returned error: %v", err)
	}
}

func TestValidate_MissingFields(t *testing.T) {
	p := &Policy{
		Application: ApplicationPolicy{
			ID:      "",
			Version: "",
		},
		Protection: ProtectionDefaults{
			Default: "",
		},
		Functions: map[string]FuncPolicy{},
	}

	res := Validate(p)
	if res.OK() {
		t.Fatalf("expected validation failure for missing fields")
	}

	hasAppIDErr := false
	hasAppVersionErr := false
	hasDefaultLevelErr := false

	for _, e := range res.Errors {
		if strings.Contains(e, "application.id is required") {
			hasAppIDErr = true
		}
		if strings.Contains(e, "application.version is required") {
			hasAppVersionErr = true
		}
		if strings.Contains(e, "invalid protection.default") {
			hasDefaultLevelErr = true
		}
	}

	if !hasAppIDErr {
		t.Errorf("expected error for empty application.id, got: %v", res.Errors)
	}
	if !hasAppVersionErr {
		t.Errorf("expected error for empty application.version, got: %v", res.Errors)
	}
	if !hasDefaultLevelErr {
		t.Errorf("expected error for empty protection.default, got: %v", res.Errors)
	}
}

func TestValidate_NilPolicy(t *testing.T) {
	res := Validate(nil)
	if res.OK() {
		t.Fatalf("expected nil policy to fail validation")
	}
	if len(res.Errors) != 1 || !strings.Contains(res.Errors[0], "policy cannot be nil") {
		t.Errorf("unexpected error for nil policy: %v", res.Errors)
	}
}

func TestValidate_InvalidEnumValues(t *testing.T) {
	p := &Policy{
		Application: ApplicationPolicy{
			ID:      "test-app",
			Version: "1.0",
		},
		Protection: ProtectionDefaults{
			Default: ProtectionLevel("ultra_secure"),
		},
		Functions: map[string]FuncPolicy{
			"BrokenFn": {
				Confidentiality: Confidentiality("super_confidential"),
				Execution:       ExecutionMode("quantum_isolated"),
				Authorization:   Authorization("biometric_face"),
			},
		},
	}

	res := Validate(p)
	if res.OK() {
		t.Fatalf("expected validation failure for invalid enum values")
	}

	errStr := res.Error()
	if !strings.Contains(errStr, "invalid protection.default \"ultra_secure\"") {
		t.Errorf("missing protection.default error in %q", errStr)
	}
	if !strings.Contains(errStr, "invalid confidentiality \"super_confidential\"") {
		t.Errorf("missing confidentiality error in %q", errStr)
	}
	if !strings.Contains(errStr, "invalid execution mode \"quantum_isolated\"") {
		t.Errorf("missing execution mode error in %q", errStr)
	}
	if !strings.Contains(errStr, "invalid authorization \"biometric_face\"") {
		t.Errorf("missing authorization error in %q", errStr)
	}
}

func TestValidate_FallbackWithoutAcknowledgment(t *testing.T) {
	p := &Policy{
		Application: ApplicationPolicy{
			ID:      "test-app",
			Version: "1.0",
		},
		Protection: ProtectionDefaults{
			Default: LevelStandard,
		},
		Functions: map[string]FuncPolicy{
			"SensitiveOp": {
				Confidentiality:      ConfidentialityHigh,
				Execution:            ExecIsolatedLocal,
				Authorization:        AuthRequired,
				Fallback:             ExecHardenedLocal,
				FallbackAcknowledged: false, // Not acknowledged!
			},
		},
	}

	res := Validate(p)
	if res.OK() {
		t.Fatalf("expected validation failure for unacknowledged fallback")
	}

	found := false
	for _, e := range res.Errors {
		if strings.Contains(e, "fallback \"hardened_local\" set but fallback_acknowledged is false") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected unacknowledged fallback error, got %v", res.Errors)
	}
}

func TestValidate_FallbackStrength(t *testing.T) {
	tests := []struct {
		name        string
		execution   ExecutionMode
		fallback    ExecutionMode
		acknowledge bool
		wantErr     string
	}{
		{
			name:        "ordinary cannot fallback to ordinary",
			execution:   ExecOrdinaryLocal,
			fallback:    ExecOrdinaryLocal,
			acknowledge: true,
			wantErr:     "fallback \"ordinary_local\" must be a weaker level than execution \"ordinary_local\"",
		},
		{
			name:        "ordinary cannot fallback to hardened",
			execution:   ExecOrdinaryLocal,
			fallback:    ExecHardenedLocal,
			acknowledge: true,
			wantErr:     "fallback \"hardened_local\" must be a weaker level than execution \"ordinary_local\"",
		},
		{
			name:        "hardened cannot fallback to isolated",
			execution:   ExecHardenedLocal,
			fallback:    ExecIsolatedLocal,
			acknowledge: true,
			wantErr:     "fallback \"isolated_local\" must be a weaker level than execution \"hardened_local\"",
		},
		{
			name:        "isolated cannot fallback to isolated",
			execution:   ExecIsolatedLocal,
			fallback:    ExecIsolatedLocal,
			acknowledge: true,
			wantErr:     "fallback \"isolated_local\" must be a weaker level than execution \"isolated_local\"",
		},
		{
			name:        "remote cannot fallback to remote",
			execution:   ExecRemoteConfidential,
			fallback:    ExecRemoteConfidential,
			acknowledge: true,
			wantErr:     "fallback \"remote_confidential\" must be a weaker level than execution \"remote_confidential\"",
		},
		{
			name:        "fallback cannot be confidential_when_available",
			execution:   ExecRemoteConfidential,
			fallback:    ExecConfidentialWhenAvailable,
			acknowledge: true,
			wantErr:     "fallback cannot be \"confidential_when_available\"",
		},
		{
			name:        "fallback with invalid mode",
			execution:   ExecRemoteConfidential,
			fallback:    ExecutionMode("invalid_mode"),
			acknowledge: true,
			wantErr:     "invalid fallback execution mode \"invalid_mode\"",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := &Policy{
				Application: ApplicationPolicy{ID: "app", Version: "1.0"},
				Protection:  ProtectionDefaults{Default: LevelStandard},
				Functions: map[string]FuncPolicy{
					"TestFunc": {
						Confidentiality:      ConfidentialityStandard,
						Execution:            tc.execution,
						Authorization:        AuthNone,
						Fallback:             tc.fallback,
						FallbackAcknowledged: tc.acknowledge,
					},
				},
			}

			res := Validate(p)
			if res.OK() {
				t.Fatalf("expected validation error containing %q, got OK", tc.wantErr)
			}
			found := false
			for _, e := range res.Errors {
				if strings.Contains(e, tc.wantErr) {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("expected error %q in %v", tc.wantErr, res.Errors)
			}
		})
	}
}

func TestValidate_LowConfidentialityWarning(t *testing.T) {
	p := &Policy{
		Application: ApplicationPolicy{ID: "app", Version: "1.0"},
		Protection:  ProtectionDefaults{Default: LevelStandard},
		Functions: map[string]FuncPolicy{
			"IsolatedLow": {
				Confidentiality: ConfidentialityLow,
				Execution:       ExecIsolatedLocal,
				Authorization:   AuthNone,
			},
			"RemoteLow": {
				Confidentiality: ConfidentialityLow,
				Execution:       ExecRemoteConfidential,
				Authorization:   AuthNone,
			},
			"IsolatedHigh": {
				Confidentiality: ConfidentialityHigh,
				Execution:       ExecIsolatedLocal,
				Authorization:   AuthNone,
			},
		},
	}

	res := Validate(p)
	if !res.OK() {
		t.Fatalf("expected policy to have zero errors, got %v", res.Errors)
	}

	if len(res.Warnings) != 2 {
		t.Fatalf("expected 2 warnings, got %d: %v", len(res.Warnings), res.Warnings)
	}

	foundIsolated := false
	foundRemote := false
	for _, w := range res.Warnings {
		if strings.Contains(w, "function \"IsolatedLow\" requests isolated_local execution but confidentiality is low") {
			foundIsolated = true
		}
		if strings.Contains(w, "function \"RemoteLow\" requests remote_confidential execution but confidentiality is low") {
			foundRemote = true
		}
	}
	if !foundIsolated || !foundRemote {
		t.Errorf("missing expected warnings: isolated=%v, remote=%v in %v", foundIsolated, foundRemote, res.Warnings)
	}
}

func TestCheckPlatform_FullSupport(t *testing.T) {
	p := &Policy{
		Application: ApplicationPolicy{ID: "secure-app", Version: "1.0"},
		Protection:  ProtectionDefaults{Default: LevelStandard},
		Functions: map[string]FuncPolicy{
			"LocalSecure": {
				Confidentiality: ConfidentialityHigh,
				Execution:       ExecIsolatedLocal,
				Authorization:   AuthRequired,
			},
			"RemoteVault": {
				Confidentiality: ConfidentialityCritical,
				Execution:       ExecRemoteConfidential,
				Authorization:   AuthPerCall,
			},
			"LocalHard": {
				Confidentiality: ConfidentialityStandard,
				Execution:       ExecHardenedLocal,
				Authorization:   AuthNone,
			},
		},
	}

	// PlatformConfidentialLinux supports HardenedLocal, IsolatedLocal, and RemoteConfidential
	report := CheckPlatform(p, PlatformConfidentialLinux)
	if !report.Supported() {
		t.Fatalf("expected platform to fully support policy, got errors: %v", report.Errors)
	}
	if len(report.Errors) != 0 {
		t.Errorf("expected 0 errors, got %v", report.Errors)
	}
	if len(report.Warnings) != 0 {
		t.Errorf("expected 0 warnings, got %v", report.Warnings)
	}
	if report.HasDowngrades() {
		t.Errorf("expected no downgrades on fully supported platform")
	}

	for _, fn := range report.Functions {
		if !fn.Supported {
			t.Errorf("expected function %q to be supported", fn.Function)
		}
		if fn.Downgraded {
			t.Errorf("expected function %q to not be downgraded", fn.Function)
		}
		if fn.ActualExecution != fn.RequestedExecution {
			t.Errorf("expected function %q actual %q == requested %q", fn.Function, fn.ActualExecution, fn.RequestedExecution)
		}
		if fn.ResidualRisk == "" {
			t.Errorf("expected function %q residual risk to be non-empty", fn.Function)
		}
	}
}

func TestCheckPlatform_Downgrade(t *testing.T) {
	p := &Policy{
		Application: ApplicationPolicy{ID: "desktop-app", Version: "1.0"},
		Protection:  ProtectionDefaults{Default: LevelStandard},
		Functions: map[string]FuncPolicy{
			"SecureCompute": {
				Confidentiality:      ConfidentialityHigh,
				Execution:            ExecIsolatedLocal,
				Authorization:        AuthRequired,
				Fallback:             ExecHardenedLocal,
				FallbackAcknowledged: true,
			},
		},
	}

	// PlatformDesktopLinux supports HardenedLocal and RemoteConfidential, but NOT IsolatedLocal
	report := CheckPlatform(p, PlatformDesktopLinux)
	if !report.Supported() {
		t.Fatalf("expected platform to be supported via fallback, got errors: %v", report.Errors)
	}
	if len(report.Errors) != 0 {
		t.Errorf("expected 0 errors, got %v", report.Errors)
	}
	if !report.HasDowngrades() {
		t.Errorf("expected HasDowngrades() to be true")
	}
	if len(report.Warnings) != 1 {
		t.Fatalf("expected 1 downgrade warning, got %d: %v", len(report.Warnings), report.Warnings)
	}

	fnReport, ok := report.FunctionReport("SecureCompute")
	if !ok {
		t.Fatalf("missing function report for SecureCompute")
	}
	if !fnReport.Supported {
		t.Errorf("expected Supported=true")
	}
	if !fnReport.Downgraded {
		t.Errorf("expected Downgraded=true")
	}
	if fnReport.ActualExecution != ExecHardenedLocal {
		t.Errorf("expected actual execution %s, got %s", ExecHardenedLocal, fnReport.ActualExecution)
	}
	if !strings.Contains(fnReport.ResidualRisk, "Security downgraded from requested isolated_local to hardened_local") {
		t.Errorf("unexpected residual risk text: %q", fnReport.ResidualRisk)
	}
}

func TestCheckPlatform_UnsupportedNoFallback(t *testing.T) {
	p := &Policy{
		Application: ApplicationPolicy{ID: "desktop-app", Version: "1.0"},
		Protection:  ProtectionDefaults{Default: LevelStandard},
		Functions: map[string]FuncPolicy{
			"IsolatedNoFallback": {
				Confidentiality: ConfidentialityHigh,
				Execution:       ExecIsolatedLocal,
				Authorization:   AuthRequired,
				// No fallback specified!
			},
			"IsolatedUnacknowledgedFallback": {
				Confidentiality:      ConfidentialityHigh,
				Execution:            ExecIsolatedLocal,
				Authorization:        AuthRequired,
				Fallback:             ExecHardenedLocal,
				FallbackAcknowledged: false, // Not acknowledged!
			},
		},
	}

	report := CheckPlatform(p, PlatformDesktopLinux)
	if report.Supported() {
		t.Fatalf("expected platform check to fail, but got Supported=true")
	}
	if len(report.Errors) != 2 {
		t.Fatalf("expected 2 errors, got %d: %v", len(report.Errors), report.Errors)
	}

	fn1, _ := report.FunctionReport("IsolatedNoFallback")
	if fn1.Supported {
		t.Errorf("expected IsolatedNoFallback to not be supported")
	}

	fn2, _ := report.FunctionReport("IsolatedUnacknowledgedFallback")
	if fn2.Supported {
		t.Errorf("expected IsolatedUnacknowledgedFallback to not be supported")
	}
}

func TestCheckPlatform_ConfidentialWhenAvailable(t *testing.T) {
	p := &Policy{
		Application: ApplicationPolicy{ID: "adaptive-app", Version: "1.0"},
		Protection:  ProtectionDefaults{Default: LevelStandard},
		Functions: map[string]FuncPolicy{
			"AdaptiveWork": {
				Confidentiality: ConfidentialityHigh,
				Execution:       ExecConfidentialWhenAvailable,
				Authorization:   AuthNone,
			},
		},
	}

	// 1. On PlatformConfidentialLinux (has local isolation): should resolve to isolated_local without downgrade
	reportConf := CheckPlatform(p, PlatformConfidentialLinux)
	if !reportConf.Supported() {
		t.Fatalf("unexpected error on confidential linux: %v", reportConf.Errors)
	}
	fnConf, _ := reportConf.FunctionReport("AdaptiveWork")
	if fnConf.ActualExecution != ExecIsolatedLocal {
		t.Errorf("expected ExecIsolatedLocal on confidential linux, got %s", fnConf.ActualExecution)
	}
	if fnConf.Downgraded {
		t.Errorf("expected no downgrade on confidential platform")
	}

	// 2. On PlatformDesktopLinux (no local isolation, but supports remote confidential):
	// should resolve to remote_confidential without downgrade
	reportDesktop := CheckPlatform(p, PlatformDesktopLinux)
	if !reportDesktop.Supported() {
		t.Fatalf("unexpected error on desktop linux: %v", reportDesktop.Errors)
	}
	fnDesktop, _ := reportDesktop.FunctionReport("AdaptiveWork")
	if fnDesktop.ActualExecution != ExecRemoteConfidential {
		t.Errorf("expected ExecRemoteConfidential on desktop linux, got %s", fnDesktop.ActualExecution)
	}
	if fnDesktop.Downgraded {
		t.Errorf("expected no downgrade when remote confidential is available")
	}

	// 3. On a limited platform with neither isolated nor remote confidential:
	// should downgrade gracefully to hardened_local (or ordinary_local if no hardened)
	limitedPlatform := PlatformCapabilities{
		Name:                       "airgap-standard-host",
		SupportsHardenedLocal:      true,
		SupportsIsolatedLocal:      false,
		SupportsRemoteConfidential: false,
		SupportsAttestation:        false,
	}
	reportLimited := CheckPlatform(p, limitedPlatform)
	if !reportLimited.Supported() {
		t.Fatalf("expected graceful downgrade on limited platform, got errors: %v", reportLimited.Errors)
	}
	fnLimited, _ := reportLimited.FunctionReport("AdaptiveWork")
	if fnLimited.ActualExecution != ExecHardenedLocal {
		t.Errorf("expected ExecHardenedLocal on limited platform, got %s", fnLimited.ActualExecution)
	}
	if !fnLimited.Downgraded {
		t.Errorf("expected Downgraded=true on limited platform")
	}
	if len(reportLimited.Warnings) == 0 {
		t.Errorf("expected downgrade warning for confidential_when_available")
	}
}

func TestEnums_Valid(t *testing.T) {
	// ProtectionLevel
	levels := []ProtectionLevel{LevelStandard, LevelHardened, LevelIsolated}
	for _, l := range levels {
		if !l.Valid() {
			t.Errorf("expected level %q to be valid", l)
		}
	}
	if ProtectionLevel("invalid").Valid() || ProtectionLevel("").Valid() {
		t.Errorf("invalid protection level should not be valid")
	}

	// Confidentiality
	confs := []Confidentiality{ConfidentialityLow, ConfidentialityStandard, ConfidentialityHigh, ConfidentialityCritical}
	for _, c := range confs {
		if !c.Valid() {
			t.Errorf("expected confidentiality %q to be valid", c)
		}
	}
	if Confidentiality("invalid").Valid() || Confidentiality("").Valid() {
		t.Errorf("invalid confidentiality should not be valid")
	}

	// ExecutionMode
	modes := []ExecutionMode{ExecOrdinaryLocal, ExecHardenedLocal, ExecIsolatedLocal, ExecRemoteConfidential, ExecConfidentialWhenAvailable}
	for _, m := range modes {
		if !m.Valid() {
			t.Errorf("expected execution mode %q to be valid", m)
		}
		if m.Rank() <= 0 {
			t.Errorf("expected positive rank for valid mode %q", m)
		}
	}
	if ExecutionMode("invalid").Valid() || ExecutionMode("").Valid() {
		t.Errorf("invalid execution mode should not be valid")
	}
	if ExecutionMode("invalid").Rank() != 0 {
		t.Errorf("invalid execution mode rank should be 0")
	}

	// Authorization
	auths := []Authorization{AuthNone, AuthRequired, AuthPerCall}
	for _, a := range auths {
		if !a.Valid() {
			t.Errorf("expected authorization %q to be valid", a)
		}
	}
	if Authorization("invalid").Valid() || Authorization("").Valid() {
		t.Errorf("invalid authorization should not be valid")
	}
}

func TestWellKnownPlatforms(t *testing.T) {
	platforms := WellKnownPlatforms()
	expectedNames := []string{
		"linux-desktop",
		"android",
		"ios",
		"web",
		"macos-desktop",
		"windows-desktop",
		"linux-confidential",
	}

	for _, name := range expectedNames {
		p, ok := platforms[name]
		if !ok {
			t.Errorf("expected well-known platform %q to be present", name)
		}
		if p.Name != name {
			t.Errorf("platform name mismatch: expected %q, got %q", name, p.Name)
		}
		lookedUp, ok := LookupPlatform(name)
		if !ok || lookedUp.Name != name {
			t.Errorf("LookupPlatform(%q) failed", name)
		}
	}

	_, ok := LookupPlatform("non-existent-os")
	if ok {
		t.Errorf("expected LookupPlatform for non-existent-os to return false")
	}
}

func TestValidationPool(t *testing.T) {
	res := AcquireValidationResult()
	if !res.OK() {
		t.Errorf("acquired result should be OK")
	}
	res.Errors = append(res.Errors, "error1")
	res.Warnings = append(res.Warnings, "warning1")
	if res.OK() {
		t.Errorf("should not be OK after error")
	}
	ReleaseValidationResult(res)

	res2 := AcquireValidationResult()
	if !res2.OK() {
		t.Errorf("re-acquired result should have clean state")
	}
	ReleaseValidationResult(res2)
}
