package platform_test

import (
	"context"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/oarkflow/ref/aegis/attest"
	"github.com/oarkflow/ref/aegis/ir"
	"github.com/oarkflow/ref/aegis/platform"
	_ "github.com/oarkflow/ref/aegis/platform/all"
	"github.com/oarkflow/ref/aegis/platform/android"
	"github.com/oarkflow/ref/aegis/platform/apple"
	"github.com/oarkflow/ref/aegis/platform/confidential"
	"github.com/oarkflow/ref/aegis/platform/desktop"
	"github.com/oarkflow/ref/aegis/platform/wasm"
	"github.com/oarkflow/ref/aegis/policy"
)

type testFunc struct {
	name    string
	params  []ir.ValType
	results []ir.ValType
	locals  []ir.ValType
	body    []ir.Instruction
}

func buildTestModule(funcs ...testFunc) ([]byte, error) {
	mod := &ir.Module{
		Version:   ir.Version,
		Memory:    ir.MemorySpec{InitialPages: 1, MaxPages: 4},
		Types:     make([]ir.FuncType, len(funcs)),
		Functions: make([]ir.Function, len(funcs)),
		Exports:   make([]ir.Export, len(funcs)),
	}

	for i, fn := range funcs {
		mod.Types[i] = ir.FuncType{
			Params:  fn.params,
			Results: fn.results,
		}
		mod.Functions[i] = ir.Function{
			Name:      fn.name,
			TypeIndex: uint32(i),
			Locals:    fn.locals,
			Body:      fn.body,
		}
		mod.Exports[i] = ir.Export{
			Name:      fn.name,
			FuncIndex: uint32(i),
		}
	}

	return ir.Encode(mod)
}

func standardArithmeticModule(t *testing.T) []byte {
	t.Helper()
	data, err := buildTestModule(
		testFunc{
			name:    "add",
			params:  []ir.ValType{ir.I64, ir.I64},
			results: []ir.ValType{ir.I64},
			body: []ir.Instruction{
				{Op: ir.LocalGet, Operands: [3]uint64{0, 0, 0}},
				{Op: ir.LocalGet, Operands: [3]uint64{1, 0, 0}},
				{Op: ir.I64Add, Operands: [3]uint64{0, 0, 0}},
				{Op: ir.Return, Operands: [3]uint64{0, 0, 0}},
			},
		},
		testFunc{
			name:    "square",
			params:  []ir.ValType{ir.I64},
			results: []ir.ValType{ir.I64},
			body: []ir.Instruction{
				{Op: ir.LocalGet, Operands: [3]uint64{0, 0, 0}},
				{Op: ir.LocalGet, Operands: [3]uint64{0, 0, 0}},
				{Op: ir.I64Mul, Operands: [3]uint64{0, 0, 0}},
				{Op: ir.Return, Operands: [3]uint64{0, 0, 0}},
			},
		},
		testFunc{
			name:    "loop_infinite",
			params:  []ir.ValType{},
			results: []ir.ValType{},
			body: []ir.Instruction{
				{Op: ir.OpLoop, Operands: [3]uint64{0, 0, 0}},
				{Op: ir.OpBr, Operands: [3]uint64{0, 0, 0}},
				{Op: ir.OpEnd, Operands: [3]uint64{0, 0, 0}},
			},
		},
	)
	if err != nil {
		t.Fatalf("failed to build test module: %v", err)
	}
	return data
}

// 1. Instantiating and registering each backend.
func TestBackendInstantiationAndRegistry(t *testing.T) {
	d := desktop.New()
	an := android.New()
	ap := apple.New()
	w := wasm.New()
	c := confidential.New()

	backends := []platform.ExecutionBackend{d, an, ap, w, c}
	expectedNames := []string{"desktop", "android", "apple", "wasm", "confidential"}

	for i, b := range backends {
		if b.Name() != expectedNames[i] {
			t.Errorf("backend[%d] expected name %q, got %q", i, expectedNames[i], b.Name())
		}
	}

	// Custom Registry
	customReg := platform.NewRegistry(d, an, ap, w, c)
	if len(customReg.List()) != 5 {
		t.Errorf("expected 5 backends in custom registry, got %d", len(customReg.List()))
	}

	for _, name := range expectedNames {
		b, ok := customReg.Get(name)
		if !ok || b == nil {
			t.Errorf("expected to find backend %q in custom registry", name)
		}
	}

	// Alias routing tests
	aliasTests := []struct {
		alias    string
		expected string
	}{
		{"linux", "desktop"},
		{"linux-desktop", "desktop"},
		{"macos-desktop", "desktop"},
		{"windows-desktop", "desktop"},
		{"play", "android"},
		{"play-integrity", "android"},
		{"ios", "apple"},
		{"app-attest", "apple"},
		{"web", "wasm"},
		{"browser", "wasm"},
		{"sgx", "confidential"},
		{"sev-snp", "confidential"},
		{"linux-confidential", "confidential"},
	}

	for _, tt := range aliasTests {
		b, ok := customReg.Get(tt.alias)
		if !ok || b.Name() != tt.expected {
			t.Errorf("alias %q resolved to %v (expected backend %q)", tt.alias, b, tt.expected)
		}
	}

	// Global DefaultRegistry
	defaultReg := platform.DefaultRegistry()
	if len(defaultReg.List()) < 5 {
		t.Errorf("expected at least 5 backends in default registry, got %d", len(defaultReg.List()))
	}
	for _, name := range expectedNames {
		if _, ok := defaultReg.Get(name); !ok {
			t.Errorf("expected %q in default registry", name)
		}
	}
}

// 2. Verifying capabilities honesty (no ordinary desktop or mobile backend sets ResistsPrivilegedAttacker = true).
func TestCapabilitiesHonesty(t *testing.T) {
	d := desktop.New().Capabilities()
	an := android.New().Capabilities()
	ap := apple.New().Capabilities()
	w := wasm.New().Capabilities()
	c := confidential.New().Capabilities()

	// CRITICAL RULE: ResistsPrivilegedAttacker MUST only be true for hardware-backed isolation!
	if d.ResistsPrivilegedAttacker {
		t.Errorf("desktop backend violates honesty: ResistsPrivilegedAttacker must be false")
	}
	if an.ResistsPrivilegedAttacker {
		t.Errorf("android backend violates honesty: ResistsPrivilegedAttacker must be false")
	}
	if ap.ResistsPrivilegedAttacker {
		t.Errorf("apple backend violates honesty: ResistsPrivilegedAttacker must be false")
	}
	if w.ResistsPrivilegedAttacker {
		t.Errorf("wasm backend violates honesty: ResistsPrivilegedAttacker must be false")
	}
	if !c.ResistsPrivilegedAttacker {
		t.Errorf("confidential backend must have ResistsPrivilegedAttacker: true")
	}

	// Confidential backend isolation invariant
	if !c.HasHardwareIsolation {
		t.Errorf("confidential backend must have HasHardwareIsolation: true")
	}
	if !c.HasMemoryEncryption {
		t.Errorf("confidential backend must have HasMemoryEncryption: true")
	}
	if !c.HasRemoteAttestation {
		t.Errorf("confidential backend must have HasRemoteAttestation: true")
	}

	// Ordinary backends must not claim hardware isolation
	if d.HasHardwareIsolation {
		t.Errorf("desktop backend must not claim HasHardwareIsolation")
	}
	if an.HasHardwareIsolation {
		t.Errorf("android backend must not claim HasHardwareIsolation")
	}
	if ap.HasHardwareIsolation {
		t.Errorf("apple backend must not claim HasHardwareIsolation")
	}
	if w.HasHardwareIsolation {
		t.Errorf("wasm backend must not claim HasHardwareIsolation")
	}

	// Verify all backends provide honest limitations and residual risks
	allCaps := []struct {
		name string
		caps platform.Capabilities
	}{
		{"desktop", d},
		{"android", an},
		{"apple", ap},
		{"wasm", w},
		{"confidential", c},
	}

	for _, item := range allCaps {
		if len(item.caps.Limitations) == 0 {
			t.Errorf("backend %q must document non-empty Limitations", item.name)
		}
		if len(item.caps.ResidualRisks) == 0 {
			t.Errorf("backend %q must document non-empty ResidualRisks", item.name)
		}
		if !item.caps.IsHonest() {
			t.Errorf("backend %q failed IsHonest() invariant check", item.name)
		}
	}

	// Verify IsHonest detects dishonesty
	dishonest := platform.Capabilities{
		ResistsPrivilegedAttacker: true,
		HasHardwareIsolation:     false, // Dishonest!
		HasMemoryEncryption:       false,
	}
	if dishonest.IsHonest() {
		t.Errorf("dishonest capabilities claiming ResistsPrivilegedAttacker without hardware isolation must fail IsHonest()")
	}
}

// 3. Running module execution through the desktop backend and confidential backend.
func TestModuleExecution(t *testing.T) {
	ctx := context.Background()
	modBytes := standardArithmeticModule(t)

	cfg := platform.BackendConfig{
		MaxMemoryPages:   2,
		InitialFuel:      10000,
		AttestationNonce: []byte("test-execution-nonce"),
	}

	// 3a. Desktop backend execution
	t.Run("DesktopBackend", func(t *testing.T) {
		b := desktop.New()
		if err := b.Initialize(ctx, cfg); err != nil {
			t.Fatalf("failed to initialize desktop backend: %v", err)
		}
		if err := b.LoadModule(ctx, modBytes); err != nil {
			t.Fatalf("failed to load module into desktop backend: %v", err)
		}

		// Execute "add" (15 + 27 = 42)
		res, err := b.Execute(ctx, platform.ExecutionRequest{
			Function: "add",
			Args:     []uint64{15, 27},
		})
		if err != nil {
			t.Fatalf("desktop execution failed: %v", err)
		}
		if len(res.Returns) != 1 || res.Returns[0] != 42 {
			t.Errorf("expected return [42], got %v", res.Returns)
		}
		if res.FuelUsed <= 0 {
			t.Errorf("expected FuelUsed > 0, got %d", res.FuelUsed)
		}
		if res.Duration <= 0 {
			t.Errorf("expected Duration > 0, got %d", res.Duration)
		}

		// Execute "square" (9 * 9 = 81)
		resSquare, err := b.Execute(ctx, platform.ExecutionRequest{
			Function: "square",
			Args:     []uint64{9},
		})
		if err != nil {
			t.Fatalf("desktop square execution failed: %v", err)
		}
		if len(resSquare.Returns) != 1 || resSquare.Returns[0] != 81 {
			t.Errorf("expected return [81], got %v", resSquare.Returns)
		}

		_ = b.Close()
	})

	// 3b. Confidential backend execution
	t.Run("ConfidentialBackend", func(t *testing.T) {
		b := confidential.New()
		if err := b.Initialize(ctx, cfg); err != nil {
			t.Fatalf("failed to initialize confidential backend: %v", err)
		}
		if err := b.LoadModule(ctx, modBytes); err != nil {
			t.Fatalf("failed to load module into confidential backend: %v", err)
		}

		res, err := b.Execute(ctx, platform.ExecutionRequest{
			Function: "add",
			Args:     []uint64{100, 250},
		})
		if err != nil {
			t.Fatalf("confidential execution failed: %v", err)
		}
		if len(res.Returns) != 1 || res.Returns[0] != 350 {
			t.Errorf("expected return [350], got %v", res.Returns)
		}
		if res.FuelUsed <= 0 {
			t.Errorf("expected FuelUsed > 0, got %d", res.FuelUsed)
		}

		_ = b.Close()
	})

	// 3c. Android, Apple, and Wasm backend execution
	otherBackends := []struct {
		name string
		b    platform.ExecutionBackend
	}{
		{"android", android.New()},
		{"apple", apple.New()},
		{"wasm", wasm.New()},
	}

	for _, tt := range otherBackends {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.b.Initialize(ctx, cfg); err != nil {
				t.Fatalf("failed to initialize %s backend: %v", tt.name, err)
			}
			if err := tt.b.LoadModule(ctx, modBytes); err != nil {
				t.Fatalf("failed to load module into %s backend: %v", tt.name, err)
			}

			res, err := tt.b.Execute(ctx, platform.ExecutionRequest{
				Function: "add",
				Args:     []uint64{7, 3},
			})
			if err != nil {
				t.Fatalf("%s execution failed: %v", tt.name, err)
			}
			if len(res.Returns) != 1 || res.Returns[0] != 10 {
				t.Errorf("%s expected return [10], got %v", tt.name, res.Returns)
			}
			_ = tt.b.Close()
		})
	}

	// 3d. Error cases: uninitialized, unloaded, not found, fuel exhausted
	t.Run("ExecutionErrorCases", func(t *testing.T) {
		uninit := desktop.New()
		_, err := uninit.Execute(ctx, platform.ExecutionRequest{Function: "add"})
		if !errors.Is(err, platform.ErrBackendNotInitialized) {
			t.Errorf("expected ErrBackendNotInitialized, got %v", err)
		}

		initOnly := desktop.New()
		_ = initOnly.Initialize(ctx, cfg)
		_, err = initOnly.Execute(ctx, platform.ExecutionRequest{Function: "add"})
		if !errors.Is(err, platform.ErrModuleNotLoaded) {
			t.Errorf("expected ErrModuleNotLoaded, got %v", err)
		}

		ready := desktop.New()
		_ = ready.Initialize(ctx, cfg)
		_ = ready.LoadModule(ctx, modBytes)

		// Non-existent function
		_, err = ready.Execute(ctx, platform.ExecutionRequest{Function: "nonexistent"})
		if !errors.Is(err, platform.ErrFunctionNotFound) {
			t.Errorf("expected ErrFunctionNotFound, got %v", err)
		}

		// Fuel exhaustion
		_, err = ready.Execute(ctx, platform.ExecutionRequest{
			Function: "loop_infinite",
			Fuel:     5, // very little fuel
		})
		if !errors.Is(err, platform.ErrFuelExhausted) {
			t.Errorf("expected ErrFuelExhausted, got %v", err)
		}
	})
}

// 4. Attestation generation and verification against aegis/attest.
func TestAttestationVerification(t *testing.T) {
	ctx := context.Background()
	nonce := []byte("challenge-nonce-1234")

	cfg := platform.BackendConfig{
		MaxMemoryPages:   2,
		InitialFuel:      1000,
		AttestationNonce: nonce,
	}

	// 4a. Desktop attestation verified against attest.NoneVerifier
	t.Run("DesktopAttest", func(t *testing.T) {
		b := desktop.New()
		_ = b.Initialize(ctx, cfg)
		evidence, err := b.Attest(ctx)
		if err != nil {
			t.Fatalf("desktop attest failed: %v", err)
		}

		verifier := attest.NewNoneVerifierWithPlatform(attest.PlatformDesktopLinux)
		res, err := verifier.Verify(ctx, evidence)
		if err != nil {
			t.Fatalf("desktop verification failed: %v", err)
		}
		if !res.Verified {
			t.Errorf("expected desktop attestation verified=true")
		}
		if res.DeviceIntegrity != attest.IntegrityNone {
			t.Errorf("expected desktop DeviceIntegrity=%q, got %q", attest.IntegrityNone, res.DeviceIntegrity)
		}
	})

	// 4b. Android attestation verified against attest.AndroidVerifier
	t.Run("AndroidAttest", func(t *testing.T) {
		b := android.NewWithPackage("com.oarkflow.aegis")
		_ = b.Initialize(ctx, cfg)
		evidence, err := b.Attest(ctx)
		if err != nil {
			t.Fatalf("android attest failed: %v", err)
		}

		verifier := attest.NewAndroidVerifierWithConfig(attest.AndroidConfig{
			ExpectedPackage: "com.oarkflow.aegis",
		})
		res, err := verifier.Verify(ctx, evidence)
		if err != nil {
			t.Fatalf("android verification failed: %v", err)
		}
		if !res.Verified {
			t.Errorf("expected android attestation verified=true")
		}
		if res.DeviceIntegrity != attest.IntegrityStrong {
			t.Errorf("expected android DeviceIntegrity=%q, got %q", attest.IntegrityStrong, res.DeviceIntegrity)
		}
	})

	// 4c. Apple attestation verified against attest.AppleVerifier
	t.Run("AppleAttest", func(t *testing.T) {
		b := apple.NewWithAppID("TEAM123456.com.oarkflow.aegis")
		_ = b.Initialize(ctx, cfg)
		evidence, err := b.Attest(ctx)
		if err != nil {
			t.Fatalf("apple attest failed: %v", err)
		}

		verifier := attest.NewAppleVerifierWithConfig(attest.AppleConfig{
			ExpectedAppID: "TEAM123456.com.oarkflow.aegis",
		})
		res, err := verifier.Verify(ctx, evidence)
		if err != nil {
			t.Fatalf("apple verification failed: %v", err)
		}
		if !res.Verified {
			t.Errorf("expected apple attestation verified=true")
		}
		if res.DeviceIntegrity != attest.IntegrityStrong {
			t.Errorf("expected apple DeviceIntegrity=%q, got %q", attest.IntegrityStrong, res.DeviceIntegrity)
		}
	})

	// 4d. Wasm attestation verified against attest.NoneVerifier
	t.Run("WasmAttest", func(t *testing.T) {
		b := wasm.New()
		_ = b.Initialize(ctx, cfg)
		evidence, err := b.Attest(ctx)
		if err != nil {
			t.Fatalf("wasm attest failed: %v", err)
		}

		verifier := attest.NewNoneVerifierWithPlatform(attest.PlatformWeb)
		res, err := verifier.Verify(ctx, evidence)
		if err != nil {
			t.Fatalf("wasm verification failed: %v", err)
		}
		if !res.Verified {
			t.Errorf("expected wasm attestation verified=true")
		}
		if res.DeviceIntegrity != attest.IntegrityNone {
			t.Errorf("expected wasm DeviceIntegrity=%q, got %q", attest.IntegrityNone, res.DeviceIntegrity)
		}
	})

	// 4e. Confidential attestation verified against attest.ConfidentialVerifier
	t.Run("ConfidentialAttest", func(t *testing.T) {
		b := confidential.New()
		_ = b.Initialize(ctx, cfg)
		evidence, err := b.Attest(ctx)
		if err != nil {
			t.Fatalf("confidential attest failed: %v", err)
		}

		expectedReportData := hex.EncodeToString(nonce)
		verifier := attest.NewConfidentialVerifierWithConfig(attest.ConfidentialConfig{
			ExpectedReportData: expectedReportData,
		})
		res, err := verifier.Verify(ctx, evidence)
		if err != nil {
			t.Fatalf("confidential verification failed: %v", err)
		}
		if !res.Verified {
			t.Errorf("expected confidential attestation verified=true")
		}
		if res.DeviceIntegrity != attest.IntegrityStrong {
			t.Errorf("expected confidential DeviceIntegrity=%q, got %q", attest.IntegrityStrong, res.DeviceIntegrity)
		}
	})
}

// 5. Policy compatibility checks (refusing isolated_local execution on desktop/wasm unless fallback acknowledged).
func TestPolicyCompatibilityAndDowngrades(t *testing.T) {
	desktopCaps := desktop.New().Capabilities()
	wasmCaps := wasm.New().Capabilities()
	confidentialCaps := confidential.New().Capabilities()

	// 5a. Direct check for isolated_local
	// Desktop and Wasm MUST reject isolated_local
	if ok, reason := desktopCaps.ValidateAgainstPolicy(policy.ExecIsolatedLocal); ok {
		t.Errorf("desktop must reject isolated_local directly")
	} else if reason == "" {
		t.Errorf("desktop rejection must provide descriptive reason")
	}

	if ok, reason := wasmCaps.ValidateAgainstPolicy(policy.ExecIsolatedLocal); ok {
		t.Errorf("wasm must reject isolated_local directly")
	} else if reason == "" {
		t.Errorf("wasm rejection must provide descriptive reason")
	}

	// Confidential backend MUST accept isolated_local
	if ok, reason := confidentialCaps.ValidateAgainstPolicy(policy.ExecIsolatedLocal); !ok {
		t.Errorf("confidential backend must accept isolated_local, got rejection: %s", reason)
	}

	// 5b. Rule 3 enforcement: Reject unacknowledged downgrades
	t.Run("UnacknowledgedFallbackRejected", func(t *testing.T) {
		fp := policy.FuncPolicy{
			Execution:            policy.ExecIsolatedLocal,
			Fallback:             policy.ExecHardenedLocal,
			FallbackAcknowledged: false, // NOT acknowledged!
		}
		ok, reason := desktopCaps.ValidateFuncPolicy(fp)
		if ok {
			t.Errorf("desktop must reject isolated_local when fallback is not acknowledged")
		}
		if reason == "" {
			t.Errorf("expected rejection reason")
		}
	})

	// 5c. Acknowledged fallback on Desktop: downgraded to hardened_local
	t.Run("AcknowledgedFallbackOnDesktop", func(t *testing.T) {
		fp := policy.FuncPolicy{
			Execution:            policy.ExecIsolatedLocal,
			Fallback:             policy.ExecHardenedLocal,
			FallbackAcknowledged: true, // Acknowledged!
		}
		ok, explanation := desktopCaps.ValidateFuncPolicy(fp)
		if !ok {
			t.Errorf("desktop must accept acknowledged fallback to hardened_local: %s", explanation)
		}
		if explanation == "" {
			t.Errorf("expected explanation detailing downgrade")
		}
	})

	// 5d. Wasm platform compatibility
	t.Run("WasmPolicyCompatibility", func(t *testing.T) {
		// Wasm lacks hardened_local support too, so fallback to hardened_local must fail
		fpHardened := policy.FuncPolicy{
			Execution:            policy.ExecIsolatedLocal,
			Fallback:             policy.ExecHardenedLocal,
			FallbackAcknowledged: true,
		}
		if ok, _ := wasmCaps.ValidateFuncPolicy(fpHardened); ok {
			t.Errorf("wasm must reject fallback to hardened_local (wasm does not support hardened_local)")
		}

		// Fallback to ordinary_local on wasm with acknowledgment must succeed
		fpOrdinary := policy.FuncPolicy{
			Execution:            policy.ExecIsolatedLocal,
			Fallback:             policy.ExecOrdinaryLocal,
			FallbackAcknowledged: true,
		}
		if ok, explanation := wasmCaps.ValidateFuncPolicy(fpOrdinary); !ok {
			t.Errorf("wasm must accept acknowledged fallback to ordinary_local, got: %s", explanation)
		}
	})

	// 5e. Interoperability with policy.CheckPlatform
	t.Run("CheckPlatformInteroperability", func(t *testing.T) {
		pol := &policy.Policy{
			Application: policy.ApplicationPolicy{
				ID:      "com.example.secureapp",
				Version: "1.0.0",
			},
			Protection: policy.ProtectionDefaults{
				Default: policy.LevelStandard,
			},
			Functions: map[string]policy.FuncPolicy{
				"critical_key_derive": {
					Confidentiality:      policy.ConfidentialityCritical,
					Execution:            policy.ExecIsolatedLocal,
					Fallback:             policy.ExecHardenedLocal,
					FallbackAcknowledged: true,
				},
				"unyielding_secret": {
					Confidentiality:      policy.ConfidentialityCritical,
					Execution:            policy.ExecIsolatedLocal,
					Fallback:             "",
					FallbackAcknowledged: false,
				},
			},
		}

		// Check against Desktop platform capabilities
		desktopReport := policy.CheckPlatform(pol, desktopCaps.ToPolicyCapabilities("linux-desktop"))
		if desktopReport.Supported() {
			t.Errorf("desktop platform report should NOT be fully supported due to unyielding_secret lacking fallback")
		}

		critReport, ok := desktopReport.FunctionReport("critical_key_derive")
		if !ok || !critReport.Supported || !critReport.Downgraded {
			t.Errorf("critical_key_derive should be supported via downgrade on desktop, got %+v", critReport)
		}

		unyieldingReport, ok := desktopReport.FunctionReport("unyielding_secret")
		if !ok || unyieldingReport.Supported {
			t.Errorf("unyielding_secret must not be supported on desktop without fallback, got %+v", unyieldingReport)
		}

		// Check against Confidential platform capabilities
		confReport := policy.CheckPlatform(pol, confidentialCaps.ToPolicyCapabilities("linux-confidential"))
		if !confReport.Supported() {
			t.Errorf("confidential platform report should be fully supported, got errors: %v", confReport.Errors)
		}
		if confReport.HasDowngrades() {
			t.Errorf("confidential platform should run both functions directly without downgrades")
		}
	})
}
