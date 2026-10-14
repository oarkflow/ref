package runtime_test

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"strings"
	"testing"
	"time"

	"github.com/oarkflow/ref/aegis/ir"
	"github.com/oarkflow/ref/aegis/pkg"
	"github.com/oarkflow/ref/aegis/runtime"
	"github.com/oarkflow/ref/signing"
)

func newTestKeySet() *signing.KeySet {
	key, err := signing.GenerateEd25519("test-key")
	if err != nil {
		panic(err)
	}
	ks, err := signing.NewKeySet(key)
	if err != nil {
		panic(err)
	}
	return ks
}

func newTestEncryptionKey() []byte {
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		panic(err)
	}
	return k
}

func newArithmeticModule() *ir.Module {
	return &ir.Module{
		Version: ir.Version,
		Types: []ir.FuncType{
			{
				Params:  []ir.ValType{ir.I32, ir.I32},
				Results: []ir.ValType{ir.I32},
			},
		},
		Functions: []ir.Function{
			{
				Name:      "add",
				TypeIndex: 0,
				Body: []ir.Instruction{
					{Op: ir.OpLocalGet, Operands: [3]uint64{0, 0, 0}},
					{Op: ir.OpLocalGet, Operands: [3]uint64{1, 0, 0}},
					{Op: ir.OpI32Add},
					{Op: ir.OpReturn},
				},
			},
		},
		Exports: []ir.Export{
			{Name: "add", FuncIndex: 0},
		},
		Memory: ir.MemorySpec{InitialPages: 1, MaxPages: 2},
	}
}

func newInfiniteLoopModule() *ir.Module {
	return &ir.Module{
		Version: ir.Version,
		Types: []ir.FuncType{
			{
				Params:  []ir.ValType{},
				Results: []ir.ValType{},
			},
		},
		Functions: []ir.Function{
			{
				Name:      "spin",
				TypeIndex: 0,
				Body: []ir.Instruction{
					{Op: ir.OpLoop},
					{Op: ir.OpNop},
					{Op: ir.OpBr, Operands: [3]uint64{0, 0, 0}},
					{Op: ir.OpEnd},
					{Op: ir.OpReturn},
				},
			},
		},
		Exports: []ir.Export{
			{Name: "spin", FuncIndex: 0},
		},
		Memory: ir.MemorySpec{InitialPages: 1, MaxPages: 1},
	}
}

func newHostIOModule() *ir.Module {
	// Offset layout:
	// 0..15:   "input_name" (key to fetch)
	// 32..47:  "output_greeting" (key to output)
	// 64..127: buffer for input data
	return &ir.Module{
		Version: ir.Version,
		Types: []ir.FuncType{
			// Type 0: (key_ptr: i32, key_len: i32, dst_ptr: i32, dst_cap: i32) -> (copied: i32)
			{
				Params:  []ir.ValType{ir.I32, ir.I32, ir.I32, ir.I32},
				Results: []ir.ValType{ir.I32},
			},
			// Type 1: (key_ptr: i32, key_len: i32, val_ptr: i32, val_len: i32) -> ()
			{
				Params:  []ir.ValType{ir.I32, ir.I32, ir.I32, ir.I32},
				Results: []ir.ValType{},
			},
			// Type 2: () -> () - entry point
			{
				Params:  []ir.ValType{},
				Results: []ir.ValType{},
			},
		},
		Imports: []ir.Import{
			{Module: "env", Name: "get_input", Type: 0},
			{Module: "env", Name: "set_output", Type: 1},
		},
		Functions: []ir.Function{
			{
				Name:      "process_io",
				TypeIndex: 2,
				Locals:    []ir.ValType{ir.I32}, // local 0: copied bytes count
				Body: []ir.Instruction{
					// get_input(0, 10, 64, 64) -> copied
					{Op: ir.OpI32Const, Operands: [3]uint64{0, 0, 0}},  // key_ptr = 0
					{Op: ir.OpI32Const, Operands: [3]uint64{10, 0, 0}}, // key_len = 10 ("input_name")
					{Op: ir.OpI32Const, Operands: [3]uint64{64, 0, 0}}, // dst_ptr = 64
					{Op: ir.OpI32Const, Operands: [3]uint64{64, 0, 0}}, // dst_cap = 64
					{Op: ir.OpCallImport, Operands: [3]uint64{0, 0, 0}},
					{Op: ir.OpLocalSet, Operands: [3]uint64{0, 0, 0}}, // store copied count

					// set_output(32, 15, 64, copied)
					{Op: ir.OpI32Const, Operands: [3]uint64{32, 0, 0}}, // key_ptr = 32 ("output_greeting")
					{Op: ir.OpI32Const, Operands: [3]uint64{15, 0, 0}}, // key_len = 15
					{Op: ir.OpI32Const, Operands: [3]uint64{64, 0, 0}}, // val_ptr = 64
					{Op: ir.OpLocalGet, Operands: [3]uint64{0, 0, 0}},  // val_len = copied
					{Op: ir.OpCallImport, Operands: [3]uint64{1, 0, 0}},

					{Op: ir.OpReturn},
				},
			},
		},
		Exports: []ir.Export{
			{Name: "process_io", FuncIndex: 0},
		},
		Memory: ir.MemorySpec{InitialPages: 1, MaxPages: 2},
		Data: []ir.DataSegment{
			{Offset: 0, Data: []byte("input_name")},
			{Offset: 32, Data: []byte("output_greeting")},
		},
	}
}

func buildPackage(t *testing.T, mod *ir.Module, ks *signing.KeySet, encKey []byte) []byte {
	t.Helper()
	builder, err := pkg.NewBuilder("test-app", "1.0.0", ks, encKey)
	if err != nil {
		t.Fatalf("pkg.NewBuilder failed: %v", err)
	}
	p, err := builder.Build(mod)
	if err != nil {
		t.Fatalf("builder.Build failed: %v", err)
	}
	data, err := pkg.Marshal(p)
	if err != nil {
		t.Fatalf("pkg.Marshal failed: %v", err)
	}
	return data
}

func TestRuntime_EndToEnd(t *testing.T) {
	ctx := context.Background()
	ks := newTestKeySet()
	encKey := newTestEncryptionKey()
	mod := newArithmeticModule()
	pkgBytes := buildPackage(t, mod, ks, encKey)

	grantProvider := runtime.NewStaticGrantProvider(encKey)
	metrics := runtime.NewMetrics()

	cfg := runtime.DefaultConfig()
	cfg.TrustedKeys = ks
	cfg.GrantProvider = grantProvider
	cfg.Metrics = metrics

	rt, err := runtime.New(cfg)
	if err != nil {
		t.Fatalf("runtime.New failed: %v", err)
	}
	defer func() { _ = rt.Close() }()

	// 1. Load Package
	lp, err := rt.LoadPackage(ctx, pkgBytes)
	if err != nil {
		t.Fatalf("LoadPackage failed: %v", err)
	}
	if lp == nil {
		t.Fatal("expected non-nil LoadedPackage")
	}
	if lp.IsAuthorized() {
		t.Fatal("package should not be authorized before Authorize is called")
	}
	if lp.Manifest().ApplicationID != "test-app" {
		t.Errorf("expected application ID 'test-app', got %s", lp.Manifest().ApplicationID)
	}

	// 2. Authorize Package
	grantReq := runtime.GrantRequest{
		DeviceID: "device-001",
		Platform: "darwin/arm64",
	}
	if err := rt.Authorize(ctx, lp, grantReq); err != nil {
		t.Fatalf("Authorize failed: %v", err)
	}
	if !lp.IsAuthorized() {
		t.Fatal("package should be authorized after Authorize")
	}
	if lp.Module() == nil {
		t.Fatal("decrypted module should be populated")
	}

	// 3. Execute with Args
	execReq := runtime.ExecutionRequest{
		Function: "add",
		Args:     []uint64{15, 27},
		Fuel:     1000,
	}
	result, err := rt.Execute(ctx, lp, execReq)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if len(result.Returns) != 1 || result.Returns[0] != 42 {
		t.Errorf("expected return value 42, got %v", result.Returns)
	}

	resBytes, ok := result.Outputs["result"]
	if !ok {
		t.Fatal("expected 'result' in Outputs")
	}
	if binary.LittleEndian.Uint64(resBytes) != 42 {
		t.Errorf("expected result output 42, got %d", binary.LittleEndian.Uint64(resBytes))
	}

	if result.FuelUsed <= 0 {
		t.Errorf("expected FuelUsed > 0, got %d", result.FuelUsed)
	}
	if result.Duration <= 0 {
		t.Errorf("expected Duration > 0, got %d", result.Duration)
	}
	if result.SessionID == "" {
		t.Error("expected non-empty SessionID")
	}

	// 4. Verify Metrics
	if metrics.ExecutionCount() != 1 {
		t.Errorf("expected 1 execution, got %d", metrics.ExecutionCount())
	}
	if metrics.ErrorCount() != 0 {
		t.Errorf("expected 0 errors, got %d", metrics.ErrorCount())
	}
	if metrics.FuelConsumed() <= 0 {
		t.Errorf("expected FuelConsumed > 0, got %d", metrics.FuelConsumed())
	}
	if len(metrics.Events()) != 1 {
		t.Errorf("expected 1 event, got %d", len(metrics.Events()))
	}
}

func TestRuntime_HostIO(t *testing.T) {
	ctx := context.Background()
	ks := newTestKeySet()
	encKey := newTestEncryptionKey()
	mod := newHostIOModule()
	pkgBytes := buildPackage(t, mod, ks, encKey)

	grantProvider := runtime.NewStaticGrantProvider(encKey)
	cfg := runtime.DefaultConfig()
	cfg.TrustedKeys = ks
	cfg.GrantProvider = grantProvider

	rt, err := runtime.New(cfg)
	if err != nil {
		t.Fatalf("runtime.New failed: %v", err)
	}
	defer func() { _ = rt.Close() }()

	lp, err := rt.LoadPackage(ctx, pkgBytes)
	if err != nil {
		t.Fatalf("LoadPackage failed: %v", err)
	}

	if err := rt.Authorize(ctx, lp, runtime.GrantRequest{}); err != nil {
		t.Fatalf("Authorize failed: %v", err)
	}

	// Provide named input
	testPayload := []byte("Aegis-Secured-Token-12345")
	execReq := runtime.ExecutionRequest{
		Function: "process_io",
		Inputs: map[string][]byte{
			"input_name": testPayload,
		},
		Fuel: 2000,
	}

	result, err := rt.Execute(ctx, lp, execReq)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	outputVal, ok := result.Outputs["output_greeting"]
	if !ok {
		t.Fatalf("expected output key 'output_greeting' in Outputs: %v", result.Outputs)
	}
	if string(outputVal) != string(testPayload) {
		t.Errorf("expected output %q, got %q", string(testPayload), string(outputVal))
	}
}

func TestRuntime_ExpiredGrant(t *testing.T) {
	ctx := context.Background()
	ks := newTestKeySet()
	encKey := newTestEncryptionKey()
	mod := newArithmeticModule()
	pkgBytes := buildPackage(t, mod, ks, encKey)

	metrics := runtime.NewMetrics()
	grantProvider := runtime.NewStaticGrantProviderWithTTL(encKey, 1*time.Second)

	cfg := runtime.DefaultConfig()
	cfg.TrustedKeys = ks
	cfg.GrantProvider = grantProvider
	cfg.Metrics = metrics

	rt, err := runtime.New(cfg)
	if err != nil {
		t.Fatalf("runtime.New failed: %v", err)
	}
	defer func() { _ = rt.Close() }()

	lp, err := rt.LoadPackage(ctx, pkgBytes)
	if err != nil {
		t.Fatalf("LoadPackage failed: %v", err)
	}

	if err := rt.Authorize(ctx, lp, runtime.GrantRequest{}); err != nil {
		t.Fatalf("Authorize failed: %v", err)
	}

	// Wait for the grant to expire
	time.Sleep(1200 * time.Millisecond)

	_, err = rt.Execute(ctx, lp, runtime.ExecutionRequest{
		Function: "add",
		Args:     []uint64{1, 2},
	})
	if err == nil {
		t.Fatal("expected execution error on expired grant, got nil")
	}
	if !strings.Contains(err.Error(), "expired") {
		t.Errorf("expected error mentioning 'expired', got %v", err)
	}

	if metrics.ErrorCount() != 1 {
		t.Errorf("expected ErrorCount 1, got %d", metrics.ErrorCount())
	}
}

func TestRuntime_UnauthorizedExecutionRejection(t *testing.T) {
	ctx := context.Background()
	ks := newTestKeySet()
	encKey := newTestEncryptionKey()
	mod := newArithmeticModule()
	pkgBytes := buildPackage(t, mod, ks, encKey)

	metrics := runtime.NewMetrics()
	grantProvider := runtime.NewStaticGrantProvider(encKey)

	cfg := runtime.DefaultConfig()
	cfg.TrustedKeys = ks
	cfg.GrantProvider = grantProvider
	cfg.Metrics = metrics

	rt, err := runtime.New(cfg)
	if err != nil {
		t.Fatalf("runtime.New failed: %v", err)
	}
	defer func() { _ = rt.Close() }()

	lp, err := rt.LoadPackage(ctx, pkgBytes)
	if err != nil {
		t.Fatalf("LoadPackage failed: %v", err)
	}

	// Attempt Execute without calling Authorize
	_, err = rt.Execute(ctx, lp, runtime.ExecutionRequest{
		Function: "add",
		Args:     []uint64{5, 10},
	})
	if err == nil {
		t.Fatal("expected error executing unauthorized package, got nil")
	}
	if !strings.Contains(err.Error(), "not authorized") {
		t.Errorf("expected error mentioning 'not authorized', got %v", err)
	}
	if metrics.ErrorCount() != 1 {
		t.Errorf("expected ErrorCount 1, got %d", metrics.ErrorCount())
	}
}

func TestRuntime_FuelLimitEnforcement(t *testing.T) {
	ctx := context.Background()
	ks := newTestKeySet()
	encKey := newTestEncryptionKey()
	mod := newInfiniteLoopModule()
	pkgBytes := buildPackage(t, mod, ks, encKey)

	metrics := runtime.NewMetrics()
	grantProvider := runtime.NewStaticGrantProvider(encKey)

	cfg := runtime.DefaultConfig()
	cfg.TrustedKeys = ks
	cfg.GrantProvider = grantProvider
	cfg.Metrics = metrics

	rt, err := runtime.New(cfg)
	if err != nil {
		t.Fatalf("runtime.New failed: %v", err)
	}
	defer func() { _ = rt.Close() }()

	lp, err := rt.LoadPackage(ctx, pkgBytes)
	if err != nil {
		t.Fatalf("LoadPackage failed: %v", err)
	}

	if err := rt.Authorize(ctx, lp, runtime.GrantRequest{}); err != nil {
		t.Fatalf("Authorize failed: %v", err)
	}

	// Execute with limited fuel
	const fuelCap = 100
	_, err = rt.Execute(ctx, lp, runtime.ExecutionRequest{
		Function: "spin",
		Fuel:     fuelCap,
	})
	if err == nil {
		t.Fatal("expected execution failure due to fuel exhaustion, got nil")
	}

	if !strings.Contains(err.Error(), "fuel") {
		t.Errorf("expected error mentioning fuel exhaustion, got %v", err)
	}

	if metrics.ErrorCount() != 1 {
		t.Errorf("expected ErrorCount 1, got %d", metrics.ErrorCount())
	}
	if metrics.FuelConsumed() != fuelCap {
		t.Errorf("expected FuelConsumed %d, got %d", fuelCap, metrics.FuelConsumed())
	}
}

func TestRuntime_MissingRequiredCapabilities(t *testing.T) {
	ctx := context.Background()
	ks := newTestKeySet()
	encKey := newTestEncryptionKey()

	builder, err := pkg.NewBuilder("test-app", "1.0.0", ks, encKey)
	if err != nil {
		t.Fatalf("NewBuilder failed: %v", err)
	}
	builder.SetRequiredCapabilities([]string{"quantum-tee", "crypto-accelerator"})

	p, err := builder.Build(newArithmeticModule())
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}
	data, err := pkg.Marshal(p)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	cfg := runtime.DefaultConfig()
	cfg.TrustedKeys = ks
	cfg.GrantProvider = runtime.NewStaticGrantProvider(encKey)
	cfg.Capabilities = []string{"basic-crypto"} // missing required capabilities

	rt, err := runtime.New(cfg)
	if err != nil {
		t.Fatalf("runtime.New failed: %v", err)
	}
	defer func() { _ = rt.Close() }()

	_, err = rt.LoadPackage(ctx, data)
	if err == nil {
		t.Fatal("expected failure loading package with missing capabilities, got nil")
	}
	if !strings.Contains(err.Error(), "missing required capability") {
		t.Errorf("expected missing required capability error, got %v", err)
	}
}

func TestRuntime_TamperedPackageSignature(t *testing.T) {
	ctx := context.Background()
	ks := newTestKeySet()
	encKey := newTestEncryptionKey()
	mod := newArithmeticModule()
	pkgBytes := buildPackage(t, mod, ks, encKey)

	// Tamper with bytes in the container
	pkgBytes[len(pkgBytes)-10] ^= 0xFF

	cfg := runtime.DefaultConfig()
	cfg.TrustedKeys = ks
	cfg.GrantProvider = runtime.NewStaticGrantProvider(encKey)

	rt, err := runtime.New(cfg)
	if err != nil {
		t.Fatalf("runtime.New failed: %v", err)
	}
	defer func() { _ = rt.Close() }()

	_, err = rt.LoadPackage(ctx, pkgBytes)
	if err == nil {
		t.Fatal("expected failure loading tampered package, got nil")
	}
}

func TestRuntime_ClosedRuntime(t *testing.T) {
	ctx := context.Background()
	ks := newTestKeySet()
	encKey := newTestEncryptionKey()
	mod := newArithmeticModule()
	pkgBytes := buildPackage(t, mod, ks, encKey)

	cfg := runtime.DefaultConfig()
	cfg.TrustedKeys = ks
	cfg.GrantProvider = runtime.NewStaticGrantProvider(encKey)

	rt, err := runtime.New(cfg)
	if err != nil {
		t.Fatalf("runtime.New failed: %v", err)
	}

	lp, err := rt.LoadPackage(ctx, pkgBytes)
	if err != nil {
		t.Fatalf("LoadPackage failed: %v", err)
	}

	// Close runtime
	if err := rt.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	// Operations should now fail
	if _, err := rt.LoadPackage(ctx, pkgBytes); err == nil {
		t.Error("expected LoadPackage to fail on closed runtime")
	}
	if err := rt.Authorize(ctx, lp, runtime.GrantRequest{}); err == nil {
		t.Error("expected Authorize to fail on closed runtime")
	}
	if _, err := rt.Execute(ctx, lp, runtime.ExecutionRequest{Function: "add"}); err == nil {
		t.Error("expected Execute to fail on closed runtime")
	}
}

func TestRuntime_OfflinePolicyAttestation(t *testing.T) {
	ctx := context.Background()
	ks := newTestKeySet()
	encKey := newTestEncryptionKey()
	mod := newArithmeticModule()
	pkgBytes := buildPackage(t, mod, ks, encKey)

	cfg := runtime.DefaultConfig()
	cfg.TrustedKeys = ks
	cfg.GrantProvider = runtime.NewStaticGrantProvider(encKey)
	cfg.Offline = runtime.OfflinePolicy{
		RequireAttestation: true,
	}

	rt, err := runtime.New(cfg)
	if err != nil {
		t.Fatalf("runtime.New failed: %v", err)
	}
	defer func() { _ = rt.Close() }()

	lp, err := rt.LoadPackage(ctx, pkgBytes)
	if err != nil {
		t.Fatalf("LoadPackage failed: %v", err)
	}

	// Authorize without attestation evidence
	err = rt.Authorize(ctx, lp, runtime.GrantRequest{})
	if err == nil {
		t.Fatal("expected error requiring attestation, got nil")
	}
	if !strings.Contains(err.Error(), "attestation") {
		t.Errorf("expected attestation error, got %v", err)
	}

	// Authorize with attestation evidence
	err = rt.Authorize(ctx, lp, runtime.GrantRequest{Attestation: []byte("enclave-quote-evidence")})
	if err != nil {
		t.Fatalf("Authorize with attestation should succeed, got: %v", err)
	}
}

func TestRuntime_ConfigKeyParsers(t *testing.T) {
	key, err := signing.GenerateEd25519("pem-key")
	if err != nil {
		t.Fatalf("GenerateEd25519 failed: %v", err)
	}
	pubPEM, err := key.MarshalPublicKeyPEM()
	if err != nil {
		t.Fatalf("MarshalPublicKeyPEM failed: %v", err)
	}

	cfg := runtime.DefaultConfig()
	if err := cfg.LoadTrustedKeysPEM("pem-key", signing.EdDSA, pubPEM); err != nil {
		t.Fatalf("LoadTrustedKeysPEM failed: %v", err)
	}
	if cfg.TrustedKeys == nil {
		t.Fatal("expected TrustedKeys to be non-nil")
	}

	ks, err := signing.NewKeySet(key)
	if err != nil {
		t.Fatalf("NewKeySet failed: %v", err)
	}
	jwksDoc := ks.JWKS()
	importJSON, err := signing.Canonical(jwksDoc)
	if err != nil {
		t.Fatalf("Canonical JWKS failed: %v", err)
	}

	cfg2 := runtime.DefaultConfig()
	if err := cfg2.LoadTrustedKeysJWKS(importJSON); err != nil {
		t.Fatalf("LoadTrustedKeysJWKS failed: %v", err)
	}
	if cfg2.TrustedKeys == nil {
		t.Fatal("expected TrustedKeys to be non-nil from JWKS")
	}
}
