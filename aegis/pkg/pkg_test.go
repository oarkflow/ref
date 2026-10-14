package pkg

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"reflect"
	"strings"
	"testing"

	"github.com/oarkflow/ref/aegis/ir"
	"github.com/oarkflow/ref/signing"
)

func testModule() *ir.Module {
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
				TypeIndex: 0,
				Body: []ir.Instruction{
					{Op: ir.OpLocalGet, Operands: [3]uint64{0}},
					{Op: ir.OpLocalGet, Operands: [3]uint64{1}},
					{Op: ir.OpI32Add},
					{Op: ir.OpReturn},
				},
			},
		},
		Exports: []ir.Export{{Name: "add", FuncIndex: 0}},
		Memory:  ir.MemorySpec{InitialPages: 1, MaxPages: 1},
	}
}

func testKeySet() *signing.KeySet {
	key, err := signing.GenerateEd25519("")
	if err != nil {
		panic(err)
	}
	ks, err := signing.NewKeySet(key)
	if err != nil {
		panic(err)
	}
	return ks
}

func testEncryptionKey() []byte {
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		panic(err)
	}
	return k
}

func assertModulesEqual(t *testing.T, expected, actual *ir.Module) {
	t.Helper()

	if actual.Version != expected.Version {
		t.Errorf("version mismatch: expected %d, got %d", expected.Version, actual.Version)
	}
	if actual.BuildID != expected.BuildID {
		t.Errorf("buildID mismatch: expected %x, got %x", expected.BuildID, actual.BuildID)
	}
	if len(actual.Types) != len(expected.Types) {
		t.Fatalf("types count mismatch: expected %d, got %d", len(expected.Types), len(actual.Types))
	}
	for i := range expected.Types {
		if !reflect.DeepEqual(expected.Types[i], actual.Types[i]) {
			t.Errorf("type %d mismatch: expected %+v, got %+v", i, expected.Types[i], actual.Types[i])
		}
	}
	if len(actual.Functions) != len(expected.Functions) {
		t.Fatalf("functions count mismatch: expected %d, got %d", len(expected.Functions), len(actual.Functions))
	}
	for i := range expected.Functions {
		expFn := expected.Functions[i]
		actFn := actual.Functions[i]
		if expFn.TypeIndex != actFn.TypeIndex {
			t.Errorf("func %d type index mismatch: expected %d, got %d", i, expFn.TypeIndex, actFn.TypeIndex)
		}
		if len(actFn.Body) != len(expFn.Body) {
			t.Fatalf("func %d body length mismatch: expected %d, got %d", i, len(expFn.Body), len(actFn.Body))
		}
		for j := range expFn.Body {
			if actFn.Body[j] != expFn.Body[j] {
				t.Errorf("func %d inst %d mismatch: expected %+v, got %+v", i, j, expFn.Body[j], actFn.Body[j])
			}
		}
	}
	if len(actual.Exports) != len(expected.Exports) {
		t.Fatalf("exports count mismatch: expected %d, got %d", len(expected.Exports), len(actual.Exports))
	}
	for i := range expected.Exports {
		if actual.Exports[i] != expected.Exports[i] {
			t.Errorf("export %d mismatch: expected %+v, got %+v", i, expected.Exports[i], actual.Exports[i])
		}
	}
	if actual.Memory != expected.Memory {
		t.Errorf("memory mismatch: expected %+v, got %+v", expected.Memory, actual.Memory)
	}
}

func TestRoundTrip(t *testing.T) {
	ks := testKeySet()
	encKey := testEncryptionKey()

	builder, err := NewBuilder("test-app", "1.0.0", ks, encKey)
	if err != nil {
		t.Fatalf("NewBuilder failed: %v", err)
	}

	builder.SetMinRuntimeVersion("0.1.0")
	builder.SetRequiredCapabilities([]string{"crypto", "memory"})
	builder.SetPolicy(ExecutionPolicy{
		Default: LevelStandard,
		Functions: map[string]ProtectionLevel{
			"add": LevelHardened,
		},
	})

	module := testModule()
	pkg, err := builder.Build(module)
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	// Verify manifest values
	if pkg.Manifest.ApplicationID != "test-app" {
		t.Errorf("manifest appID mismatch: got %q", pkg.Manifest.ApplicationID)
	}
	if pkg.Manifest.Version != "1.0.0" {
		t.Errorf("manifest version mismatch: got %q", pkg.Manifest.Version)
	}
	if pkg.Manifest.MinRuntimeVersion != "0.1.0" {
		t.Errorf("manifest min runtime mismatch: got %q", pkg.Manifest.MinRuntimeVersion)
	}
	if pkg.Manifest.EncryptionAlgorithm != AlgorithmAES256GCM {
		t.Errorf("encryption algorithm mismatch: got %q", pkg.Manifest.EncryptionAlgorithm)
	}
	if pkg.Manifest.HashAlgorithm != AlgorithmSHA256 {
		t.Errorf("hash algorithm mismatch: got %q", pkg.Manifest.HashAlgorithm)
	}

	// Binary serialization
	data, err := Marshal(pkg)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	// Deserialization
	unmarshaledPkg, err := Unmarshal(data)
	if err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	// Verify with trusted keys
	reader := NewReader(ks)
	if err := reader.Verify(unmarshaledPkg); err != nil {
		t.Fatalf("Verify failed: %v", err)
	}

	// Decrypt and compare module
	decryptedModule, err := reader.Decrypt(unmarshaledPkg, encKey)
	if err != nil {
		t.Fatalf("Decrypt failed: %v", err)
	}

	assertModulesEqual(t, module, decryptedModule)
}

func TestTamperDetection(t *testing.T) {
	ks := testKeySet()
	encKey := testEncryptionKey()

	builder, err := NewBuilder("tamper-app", "1.0.0", ks, encKey)
	if err != nil {
		t.Fatalf("NewBuilder failed: %v", err)
	}

	pkg, err := builder.Build(testModule())
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	// Corrupt a byte in the encrypted payload
	pkg.Payload[len(pkg.Payload)-1] ^= 0xFF

	reader := NewReader(ks)
	err = reader.Verify(pkg)
	if err == nil {
		t.Fatal("expected Verify to fail on tampered payload, got nil")
	}
	if !strings.Contains(err.Error(), "payload hash mismatch") {
		t.Errorf("expected payload hash mismatch error, got: %v", err)
	}
}

func TestSignatureVerification_WrongKey(t *testing.T) {
	ks1 := testKeySet()
	ks2 := testKeySet() // Untrusted key set
	encKey := testEncryptionKey()

	builder, err := NewBuilder("sig-app", "1.0.0", ks1, encKey)
	if err != nil {
		t.Fatalf("NewBuilder failed: %v", err)
	}

	pkg, err := builder.Build(testModule())
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	// Verifying with a different key set must fail
	reader := NewReader(ks2)
	err = reader.Verify(pkg)
	if err == nil {
		t.Fatal("expected signature verification to fail with untrusted key, got nil")
	}
	if !strings.Contains(err.Error(), "signature verification failed") {
		t.Errorf("expected signature verification failed error, got: %v", err)
	}
}

func TestWrongDecryptionKey(t *testing.T) {
	ks := testKeySet()
	encKey1 := testEncryptionKey()
	encKey2 := testEncryptionKey()

	builder, err := NewBuilder("enc-app", "1.0.0", ks, encKey1)
	if err != nil {
		t.Fatalf("NewBuilder failed: %v", err)
	}

	pkg, err := builder.Build(testModule())
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	reader := NewReader(ks)
	if err := reader.Verify(pkg); err != nil {
		t.Fatalf("Verify failed: %v", err)
	}

	_, err = reader.Decrypt(pkg, encKey2)
	if err == nil {
		t.Fatal("expected Decrypt to fail with wrong key, got nil")
	}
	if !strings.Contains(err.Error(), "decryption failed") {
		t.Errorf("expected decryption failed error, got: %v", err)
	}
}

func TestPlaintextHashVerification(t *testing.T) {
	ks := testKeySet()
	encKey := testEncryptionKey()

	builder, err := NewBuilder("hash-app", "1.0.0", ks, encKey)
	if err != nil {
		t.Fatalf("NewBuilder failed: %v", err)
	}

	pkg, err := builder.Build(testModule())
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	// Corrupt plaintext hash in manifest and re-sign so Verify passes
	fakeHash := strings.Repeat("0", 64)
	pkg.Manifest.PlaintextHash = fakeHash

	canonicalManifest, err := signing.Canonical(pkg.Manifest)
	if err != nil {
		t.Fatalf("Canonical failed: %v", err)
	}
	newSig, err := ks.Sign(canonicalManifest)
	if err != nil {
		t.Fatalf("Sign failed: %v", err)
	}
	pkg.Signature = newSig

	reader := NewReader(ks)
	// Verify passes because the signature and payload hash are valid
	if err := reader.Verify(pkg); err != nil {
		t.Fatalf("Verify should pass with re-signed manifest, got: %v", err)
	}

	// Decrypt must detect plaintext hash mismatch
	_, err = reader.Decrypt(pkg, encKey)
	if err == nil {
		t.Fatal("expected Decrypt to fail on plaintext hash mismatch, got nil")
	}
	if !strings.Contains(err.Error(), "plaintext hash mismatch") {
		t.Errorf("expected plaintext hash mismatch error, got: %v", err)
	}
}

func TestMarshalUnmarshalRoundTrip(t *testing.T) {
	ks := testKeySet()
	encKey := testEncryptionKey()

	builder, err := NewBuilder("roundtrip-app", "2.1.0", ks, encKey)
	if err != nil {
		t.Fatalf("NewBuilder failed: %v", err)
	}

	builder.SetMinRuntimeVersion("1.0.0")
	builder.SetRequiredCapabilities([]string{"cap1", "cap2"})
	builder.SetPolicy(ExecutionPolicy{
		Default: LevelIsolated,
		Functions: map[string]ProtectionLevel{
			"f1": LevelStandard,
			"f2": LevelHardened,
		},
	})

	pkg, err := builder.Build(testModule())
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	data, err := Marshal(pkg)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	pkg2, err := Unmarshal(data)
	if err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	// Check Manifest fields
	if pkg.Manifest.PackageID != pkg2.Manifest.PackageID {
		t.Errorf("PackageID mismatch: got %q, want %q", pkg2.Manifest.PackageID, pkg.Manifest.PackageID)
	}
	if pkg.Manifest.ApplicationID != pkg2.Manifest.ApplicationID {
		t.Errorf("ApplicationID mismatch: got %q, want %q", pkg2.Manifest.ApplicationID, pkg.Manifest.ApplicationID)
	}
	if pkg.Manifest.BuildID != pkg2.Manifest.BuildID {
		t.Errorf("BuildID mismatch: got %q, want %q", pkg2.Manifest.BuildID, pkg.Manifest.BuildID)
	}
	if pkg.Manifest.Version != pkg2.Manifest.Version {
		t.Errorf("Version mismatch: got %q, want %q", pkg2.Manifest.Version, pkg.Manifest.Version)
	}
	if !pkg.Manifest.CreatedAt.Equal(pkg2.Manifest.CreatedAt) {
		t.Errorf("CreatedAt mismatch: got %v, want %v", pkg2.Manifest.CreatedAt, pkg.Manifest.CreatedAt)
	}
	if pkg.Manifest.PayloadHash != pkg2.Manifest.PayloadHash {
		t.Errorf("PayloadHash mismatch: got %q, want %q", pkg2.Manifest.PayloadHash, pkg.Manifest.PayloadHash)
	}
	if pkg.Manifest.PlaintextHash != pkg2.Manifest.PlaintextHash {
		t.Errorf("PlaintextHash mismatch: got %q, want %q", pkg2.Manifest.PlaintextHash, pkg.Manifest.PlaintextHash)
	}
	if pkg.Manifest.HashAlgorithm != pkg2.Manifest.HashAlgorithm {
		t.Errorf("HashAlgorithm mismatch: got %q, want %q", pkg2.Manifest.HashAlgorithm, pkg.Manifest.HashAlgorithm)
	}
	if pkg.Manifest.MinRuntimeVersion != pkg2.Manifest.MinRuntimeVersion {
		t.Errorf("MinRuntimeVersion mismatch: got %q, want %q", pkg2.Manifest.MinRuntimeVersion, pkg.Manifest.MinRuntimeVersion)
	}
	if pkg.Manifest.IRVersion != pkg2.Manifest.IRVersion {
		t.Errorf("IRVersion mismatch: got %d, want %d", pkg2.Manifest.IRVersion, pkg.Manifest.IRVersion)
	}
	if !reflect.DeepEqual(pkg.Manifest.RequiredCapabilities, pkg2.Manifest.RequiredCapabilities) {
		t.Errorf("RequiredCapabilities mismatch: got %v, want %v", pkg2.Manifest.RequiredCapabilities, pkg.Manifest.RequiredCapabilities)
	}
	if !reflect.DeepEqual(pkg.Manifest.ExecutionPolicy, pkg2.Manifest.ExecutionPolicy) {
		t.Errorf("ExecutionPolicy mismatch: got %+v, want %+v", pkg2.Manifest.ExecutionPolicy, pkg.Manifest.ExecutionPolicy)
	}
	if pkg.Manifest.EncryptionAlgorithm != pkg2.Manifest.EncryptionAlgorithm {
		t.Errorf("EncryptionAlgorithm mismatch: got %q, want %q", pkg2.Manifest.EncryptionAlgorithm, pkg.Manifest.EncryptionAlgorithm)
	}
	if pkg.Manifest.KeyDerivation != pkg2.Manifest.KeyDerivation {
		t.Errorf("KeyDerivation mismatch: got %q, want %q", pkg2.Manifest.KeyDerivation, pkg.Manifest.KeyDerivation)
	}

	// Check Payload and Signature
	if !bytes.Equal(pkg.Payload, pkg2.Payload) {
		t.Error("Payload bytes mismatch")
	}
	if pkg.Signature != pkg2.Signature {
		t.Errorf("Signature mismatch: got %+v, want %+v", pkg2.Signature, pkg.Signature)
	}
}

func TestInvalidInputs(t *testing.T) {
	ks := testKeySet()
	encKey := testEncryptionKey()

	builder, err := NewBuilder("app", "1.0", ks, encKey)
	if err != nil {
		t.Fatalf("NewBuilder failed: %v", err)
	}

	// Nil module to Build
	if _, err := builder.Build(nil); err == nil {
		t.Error("expected error for nil module, got nil")
	}

	reader := NewReader(ks)

	// Nil package to Verify
	if err := reader.Verify(nil); err == nil {
		t.Error("expected error for nil package in Verify, got nil")
	}

	// Nil trusted keys in Reader
	nilKeyReader := NewReader(nil)
	pkg, err := builder.Build(testModule())
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}
	if err := nilKeyReader.Verify(pkg); err == nil {
		t.Error("expected error for nil trusted keys in Verify, got nil")
	}

	// Decrypt with bad key length
	if _, err := reader.Decrypt(pkg, []byte("short")); err == nil {
		t.Error("expected error for short key in Decrypt, got nil")
	}
	if _, err := reader.Decrypt(pkg, nil); err == nil {
		t.Error("expected error for nil key in Decrypt, got nil")
	}

	// Decrypt with short payload
	corruptedPkg := &Package{
		Manifest:  pkg.Manifest,
		Payload:   []byte{1, 2, 3}, // too short for nonce
		Signature: pkg.Signature,
	}
	// Re-sign so Verify passes
	corruptedPkg.Manifest.PayloadHash = SHA256Hex(corruptedPkg.Payload)
	cm, _ := signing.Canonical(corruptedPkg.Manifest)
	corruptedPkg.Signature, _ = ks.Sign(cm)

	if _, err := reader.Decrypt(corruptedPkg, encKey); err == nil {
		t.Error("expected error for payload shorter than nonce in Decrypt, got nil")
	}

	// Marshal nil package
	if _, err := Marshal(nil); err == nil {
		t.Error("expected error for Marshal(nil), got nil")
	}

	// Unmarshal invalid binary
	if _, err := Unmarshal(nil); err == nil {
		t.Error("expected error for Unmarshal(nil), got nil")
	}
	if _, err := Unmarshal([]byte("too short")); err == nil {
		t.Error("expected error for Unmarshal short bytes, got nil")
	}

	// Bad magic
	badMagic := make([]byte, 24)
	copy(badMagic[:4], "XXXX")
	if _, err := Unmarshal(badMagic); err == nil {
		t.Error("expected error for invalid magic header, got nil")
	}

	// Unsupported version
	badVersion := make([]byte, 24)
	copy(badVersion[:4], Magic)
	badVersion[4] = 99
	if _, err := Unmarshal(badVersion); err == nil {
		t.Error("expected error for unsupported format version, got nil")
	}
}

func TestBuilderValidation(t *testing.T) {
	ks := testKeySet()
	encKey := testEncryptionKey()

	tests := []struct {
		name    string
		appID   string
		version string
		ks      *signing.KeySet
		key     []byte
		errMsg  string
	}{
		{
			name:    "empty appID",
			appID:   "",
			version: "1.0",
			ks:      ks,
			key:     encKey,
			errMsg:  "application ID cannot be empty",
		},
		{
			name:    "empty version",
			appID:   "app",
			version: "",
			ks:      ks,
			key:     encKey,
			errMsg:  "version cannot be empty",
		},
		{
			name:    "nil keySet",
			appID:   "app",
			version: "1.0",
			ks:      nil,
			key:     encKey,
			errMsg:  "key set cannot be nil",
		},
		{
			name:    "short key",
			appID:   "app",
			version: "1.0",
			ks:      ks,
			key:     make([]byte, 16),
			errMsg:  "encryption key must be 32 bytes",
		},
		{
			name:    "nil key",
			appID:   "app",
			version: "1.0",
			ks:      ks,
			key:     nil,
			errMsg:  "encryption key must be 32 bytes",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewBuilder(tc.appID, tc.version, tc.ks, tc.key)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.errMsg)
			}
			if !strings.Contains(err.Error(), tc.errMsg) {
				t.Errorf("expected error containing %q, got: %v", tc.errMsg, err)
			}
		})
	}
}

func TestSHA256Hex(t *testing.T) {
	testVectors := []struct {
		input    []byte
		expected string
	}{
		{
			input:    []byte(""),
			expected: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		},
		{
			input:    []byte("hello world"),
			expected: "b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9",
		},
	}

	for _, tv := range testVectors {
		got := SHA256Hex(tv.input)
		if got != tv.expected {
			t.Errorf("SHA256Hex(%q) = %s, want %s", string(tv.input), got, tv.expected)
		}
		if !VerifyHash(tv.input, tv.expected) {
			t.Errorf("VerifyHash failed for input %q", string(tv.input))
		}
		// Test case insensitivity
		if !VerifyHash(tv.input, strings.ToUpper(tv.expected)) {
			t.Errorf("VerifyHash should be case-insensitive for input %q", string(tv.input))
		}
	}

	// VerifyHash edge cases
	if VerifyHash([]byte("hello"), "badhex") {
		t.Error("VerifyHash should return false for malformed hex")
	}
	if VerifyHash([]byte("hello"), hex.EncodeToString([]byte("short"))) {
		t.Error("VerifyHash should return false for hex of wrong length")
	}
	if VerifyHash([]byte("hello"), hex.EncodeToString(make([]byte, 32))) {
		t.Error("VerifyHash should return false for mismatched digest")
	}
}

func TestAADBinding(t *testing.T) {
	ks := testKeySet()
	encKey := testEncryptionKey()

	builder, err := NewBuilder("aad-app", "1.0.0", ks, encKey)
	if err != nil {
		t.Fatalf("NewBuilder failed: %v", err)
	}

	pkg, err := builder.Build(testModule())
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	// Tamper with PackageID in manifest and re-sign
	pkg.Manifest.PackageID = "different-pkg-id"
	cm, err := signing.Canonical(pkg.Manifest)
	if err != nil {
		t.Fatalf("Canonical failed: %v", err)
	}
	pkg.Signature, err = ks.Sign(cm)
	if err != nil {
		t.Fatalf("Sign failed: %v", err)
	}

	reader := NewReader(ks)
	// Verify should pass because signature and payload hash are valid
	if err := reader.Verify(pkg); err != nil {
		t.Fatalf("Verify should pass with re-signed manifest, got: %v", err)
	}

	// Decrypt must fail because AAD (PackageID|BuildID) will not authenticate
	_, err = reader.Decrypt(pkg, encKey)
	if err == nil {
		t.Fatal("expected Decrypt to fail due to AAD mismatch, got nil")
	}
	if !strings.Contains(err.Error(), "decryption failed") {
		t.Errorf("expected decryption failed error, got: %v", err)
	}
}

func TestUnsupportedIRVersion(t *testing.T) {
	ks := testKeySet()
	encKey := testEncryptionKey()

	builder, err := NewBuilder("ir-app", "1.0.0", ks, encKey)
	if err != nil {
		t.Fatalf("NewBuilder failed: %v", err)
	}

	pkg, err := builder.Build(testModule())
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	// Modify IRVersion to an unsupported version and re-sign
	pkg.Manifest.IRVersion = 9999
	cm, _ := signing.Canonical(pkg.Manifest)
	pkg.Signature, _ = ks.Sign(cm)

	reader := NewReader(ks)
	err = reader.Verify(pkg)
	if err == nil {
		t.Fatal("expected Verify to fail on unsupported IR version, got nil")
	}
	if !strings.Contains(err.Error(), "unsupported IR version") {
		t.Errorf("expected unsupported IR version error, got: %v", err)
	}
}

func TestUnmarshalTrailingData(t *testing.T) {
	ks := testKeySet()
	encKey := testEncryptionKey()

	builder, err := NewBuilder("trail-app", "1.0.0", ks, encKey)
	if err != nil {
		t.Fatalf("NewBuilder failed: %v", err)
	}

	pkg, err := builder.Build(testModule())
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	data, err := Marshal(pkg)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	// Append unexpected trailing bytes
	tamperedData := append(data, []byte("extra_bytes")...)
	_, err = Unmarshal(tamperedData)
	if err == nil {
		t.Fatal("expected Unmarshal to fail on trailing data, got nil")
	}
	if !strings.Contains(err.Error(), "unexpected trailing data") {
		t.Errorf("expected trailing data error, got: %v", err)
	}
}
