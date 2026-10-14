package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const testGoSource = `package main

func Add(a, b int64) int64 {
	return a + b
}

func Multiply(a, b int64) int64 {
	return a * b
}

func Factorial(n int64) int64 {
	if n <= 1 {
		return 1
	}
	return n * Factorial(n - 1)
}

func Sum(n int64) int64 {
	total := 0
	for i := 1; i <= n; i++ {
		total += i
	}
	return total
}
`

// TestCLI_EndToEndPipeline verifies the complete keygen -> compile -> package -> inspect -> verify -> run workflow.
func TestCLI_EndToEndPipeline(t *testing.T) {
	tempDir := t.TempDir()

	// 1. Keygen
	keyPrefix := filepath.Join(tempDir, "keys", "testapp")
	var keygenOut, keygenErr bytes.Buffer
	if err := Run([]string{"keygen", "-out", keyPrefix}, &keygenOut, &keygenErr); err != nil {
		t.Fatalf("keygen failed: %v, stderr: %s", err, keygenErr.String())
	}

	privKeyPath := keyPrefix + ".key"
	pubKeyPath := keyPrefix + ".pub"
	encKeyPath := keyPrefix + ".enc"

	if _, err := os.Stat(privKeyPath); err != nil {
		t.Fatalf("private key file not created: %v", err)
	}
	if _, err := os.Stat(pubKeyPath); err != nil {
		t.Fatalf("public key file not created: %v", err)
	}
	if _, err := os.Stat(encKeyPath); err != nil {
		t.Fatalf("encryption key file not created: %v", err)
	}

	encKeyBytes, err := os.ReadFile(encKeyPath)
	if err != nil {
		t.Fatalf("failed to read encryption key file: %v", err)
	}
	encKeyHex := strings.TrimSpace(string(encKeyBytes))
	if len(encKeyHex) != 64 {
		t.Fatalf("expected 64 hex chars encryption key, got %d", len(encKeyHex))
	}

	// 2. Compile Go Source to .air
	srcPath := filepath.Join(tempDir, "math.go")
	if err := os.WriteFile(srcPath, []byte(testGoSource), 0644); err != nil {
		t.Fatalf("failed to write test source: %v", err)
	}

	airPath := filepath.Join(tempDir, "math.air")
	var compileOut, compileErr bytes.Buffer
	if err := Run([]string{
		"compile",
		"-src", srcPath,
		"-entry", "Add,Multiply,Factorial,Sum",
		"-out", airPath,
	}, &compileOut, &compileErr); err != nil {
		t.Fatalf("compile failed: %v, stderr: %s", err, compileErr.String())
	}

	if _, err := os.Stat(airPath); err != nil {
		t.Fatalf(".air file not created: %v", err)
	}

	// 3. Package .air to .apkg
	apkgPath := filepath.Join(tempDir, "math.apkg")
	var pkgOut, pkgErr bytes.Buffer
	if err := Run([]string{
		"package",
		"-module", airPath,
		"-key", privKeyPath,
		"-enckey", encKeyPath,
		"-out", apkgPath,
		"-appid", "com.example.math",
		"-ver", "1.2.3",
	}, &pkgOut, &pkgErr); err != nil {
		t.Fatalf("package failed: %v, stderr: %s", err, pkgErr.String())
	}

	if _, err := os.Stat(apkgPath); err != nil {
		t.Fatalf(".apkg file not created: %v", err)
	}

	// 4. Inspect .apkg
	var inspectOut, inspectErr bytes.Buffer
	if err := Run([]string{"inspect", "-pkg", apkgPath}, &inspectOut, &inspectErr); err != nil {
		t.Fatalf("inspect failed: %v, stderr: %s", err, inspectErr.String())
	}

	inspectStr := inspectOut.String()
	for _, expected := range []string{
		"com.example.math",
		"1.2.3",
		"Payload Hash",
		"Plaintext Hash",
		"IR Version",
		"Signature",
	} {
		if !strings.Contains(inspectStr, expected) {
			t.Errorf("inspect output missing %q, got:\n%s", expected, inspectStr)
		}
	}

	// 5. Verify .apkg with public key
	var verifyOut, verifyErr bytes.Buffer
	if err := Run([]string{
		"verify",
		"-pkg", apkgPath,
		"-pubkey", pubKeyPath,
	}, &verifyOut, &verifyErr); err != nil {
		t.Fatalf("verify failed: %v, stderr: %s", err, verifyErr.String())
	}

	if !strings.Contains(verifyOut.String(), "VALID") {
		t.Errorf("verify output does not contain VALID:\n%s", verifyOut.String())
	}

	// 6. Run functions and check stdout/result
	testCases := []struct {
		fn       string
		args     string
		expected string
	}{
		{"Add", "15,27", "Result: 42"},
		{"Multiply", "6,7", "Result: 42"},
		{"Factorial", "5", "Result: 120"},
		{"Sum", "10", "Result: 55"},
	}

	for _, tc := range testCases {
		t.Run(tc.fn, func(t *testing.T) {
			var runOut, runErr bytes.Buffer
			if err := Run([]string{
				"run",
				"-pkg", apkgPath,
				"-key", encKeyHex,
				"-func", tc.fn,
				"-args", tc.args,
			}, &runOut, &runErr); err != nil {
				t.Fatalf("run %s failed: %v, stderr: %s", tc.fn, err, runErr.String())
			}

			if !strings.Contains(runOut.String(), tc.expected) {
				t.Errorf("run %s: expected %q in output, got:\n%s", tc.fn, tc.expected, runOut.String())
			}
		})
	}

	// 7. Run with -pubkey (verifying package signature in runtime)
	t.Run("RunWithPubkey", func(t *testing.T) {
		var runOut, runErr bytes.Buffer
		if err := Run([]string{
			"run",
			"-pkg", apkgPath,
			"-key", encKeyPath,
			"-func", "Add",
			"-args", "100,200",
			"-pubkey", pubKeyPath,
		}, &runOut, &runErr); err != nil {
			t.Fatalf("run with -pubkey failed: %v, stderr: %s", err, runErr.String())
		}

		if !strings.Contains(runOut.String(), "Result: 300") {
			t.Errorf("expected 'Result: 300', got:\n%s", runOut.String())
		}
	})
}

// TestCLI_SubprocessBinary builds the actual CLI executable and tests it as an external process.
func TestCLI_SubprocessBinary(t *testing.T) {
	tempDir := t.TempDir()
	binPath := filepath.Join(tempDir, "aegis_cli")

	// Compile CLI binary
	cmd := exec.Command("go", "build", "-o", binPath, ".")
	cmd.Dir = "."
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to build aegis binary: %v, output: %s", err, string(output))
	}

	keyPrefix := filepath.Join(tempDir, "sub_key")
	keygenCmd := exec.Command(binPath, "keygen", "-out", keyPrefix)
	if out, err := keygenCmd.CombinedOutput(); err != nil {
		t.Fatalf("binary keygen failed: %v, output: %s", err, string(out))
	}

	srcPath := filepath.Join(tempDir, "test.go")
	if err := os.WriteFile(srcPath, []byte(testGoSource), 0644); err != nil {
		t.Fatalf("failed to write test source: %v", err)
	}

	airPath := filepath.Join(tempDir, "test.air")
	compileCmd := exec.Command(binPath, "compile", "-src", srcPath, "-out", airPath)
	if out, err := compileCmd.CombinedOutput(); err != nil {
		t.Fatalf("binary compile failed: %v, output: %s", err, string(out))
	}

	apkgPath := filepath.Join(tempDir, "test.apkg")
	pkgCmd := exec.Command(binPath, "package",
		"-module", airPath,
		"-key", keyPrefix+".key",
		"-enckey", keyPrefix+".enc",
		"-out", apkgPath,
	)
	if out, err := pkgCmd.CombinedOutput(); err != nil {
		t.Fatalf("binary package failed: %v, output: %s", err, string(out))
	}

	inspectCmd := exec.Command(binPath, "inspect", "-pkg", apkgPath)
	inspectOut, err := inspectCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("binary inspect failed: %v, output: %s", err, string(inspectOut))
	}
	if !strings.Contains(string(inspectOut), "Package ID") {
		t.Errorf("inspect output missing 'Package ID': %s", string(inspectOut))
	}

	verifyCmd := exec.Command(binPath, "verify", "-pkg", apkgPath, "-pubkey", keyPrefix+".pub")
	verifyOut, err := verifyCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("binary verify failed: %v, output: %s", err, string(verifyOut))
	}
	if !strings.Contains(string(verifyOut), "VALID") {
		t.Errorf("verify output missing 'VALID': %s", string(verifyOut))
	}

	runCmd := exec.Command(binPath, "run",
		"-pkg", apkgPath,
		"-key", keyPrefix+".enc",
		"-func", "Add",
		"-args", "50,25",
	)
	runOut, err := runCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("binary run failed: %v, output: %s", err, string(runOut))
	}
	if !strings.Contains(string(runOut), "Result: 75") {
		t.Errorf("binary run expected 'Result: 75', got: %s", string(runOut))
	}
}

// TestCLI_ErrorHandling tests edge cases and expected error behaviors.
func TestCLI_ErrorHandling(t *testing.T) {
	tempDir := t.TempDir()

	// Missing args
	var out, errBuf bytes.Buffer
	if err := Run([]string{}, &out, &errBuf); err != nil {
		t.Errorf("expected nil for no args (prints usage), got: %v", err)
	}

	// Unknown command
	out.Reset()
	errBuf.Reset()
	if err := Run([]string{"unknown_cmd"}, &out, &errBuf); err == nil {
		t.Errorf("expected error for unknown command, got nil")
	}

	// Keygen with empty out
	out.Reset()
	errBuf.Reset()
	if err := Run([]string{"keygen", "-out", ""}, &out, &errBuf); err == nil {
		t.Errorf("expected error for keygen with empty -out, got nil")
	}

	// Compile with missing -src
	out.Reset()
	errBuf.Reset()
	if err := Run([]string{"compile"}, &out, &errBuf); err == nil {
		t.Errorf("expected error for compile without -src, got nil")
	}

	// Compile with non-existent source
	out.Reset()
	errBuf.Reset()
	if err := Run([]string{"compile", "-src", "does_not_exist.go"}, &out, &errBuf); err == nil {
		t.Errorf("expected error for non-existent source, got nil")
	}

	// Compile with non-existent entry function
	srcPath := filepath.Join(tempDir, "simple.go")
	_ = os.WriteFile(srcPath, []byte("package main\nfunc Exists() int64 { return 1 }\n"), 0644)
	out.Reset()
	errBuf.Reset()
	if err := Run([]string{"compile", "-src", srcPath, "-entry", "NonExistent"}, &out, &errBuf); err == nil {
		t.Errorf("expected error for non-existent entry function, got nil")
	}

	// Run with invalid key (wrong key causes decryption failure)
	keyPrefix := filepath.Join(tempDir, "err_key")
	_ = Run([]string{"keygen", "-out", keyPrefix}, &out, &errBuf)

	airPath := filepath.Join(tempDir, "simple.air")
	_ = Run([]string{"compile", "-src", srcPath, "-out", airPath}, &out, &errBuf)

	apkgPath := filepath.Join(tempDir, "simple.apkg")
	_ = Run([]string{
		"package",
		"-module", airPath,
		"-key", keyPrefix + ".key",
		"-enckey", keyPrefix + ".enc",
		"-out", apkgPath,
	}, &out, &errBuf)

	// Wrong 32-byte hex key
	wrongKeyHex := "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"
	out.Reset()
	errBuf.Reset()
	if err := Run([]string{
		"run",
		"-pkg", apkgPath,
		"-key", wrongKeyHex,
		"-func", "Exists",
	}, &out, &errBuf); err == nil {
		t.Errorf("expected decryption error with wrong key, got nil")
	}

	// Verify with tampered package
	tamperedPath := filepath.Join(tempDir, "tampered.apkg")
	pkgData, _ := os.ReadFile(apkgPath)
	// Corrupt payload
	pkgData[len(pkgData)-1] ^= 0xFF
	_ = os.WriteFile(tamperedPath, pkgData, 0644)

	out.Reset()
	errBuf.Reset()
	if err := Run([]string{
		"verify",
		"-pkg", tamperedPath,
		"-pubkey", keyPrefix + ".pub",
	}, &out, &errBuf); err == nil {
		t.Errorf("expected verification error for tampered package, got nil")
	}
}
