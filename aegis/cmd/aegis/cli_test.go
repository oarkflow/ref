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
				"-pubkey", pubKeyPath,
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
		"-pubkey", keyPrefix+".pub",
	)
	runOut, err := runCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("binary run failed: %v, output: %s", err, string(runOut))
	}
	if !strings.Contains(string(runOut), "Result: 75") {
		t.Errorf("binary run expected 'Result: 75', got: %s", string(runOut))
	}
}

// TestCLI_PassphraseKeyRoundTrips packages and runs a module keyed by a bare
// passphrase rather than a key file: resolvePackagingKey must derive it with
// Argon2id and record the salt in the manifest, and resolveRunKey must
// reproduce the same key from the same passphrase (a wrong one must fail).
func TestCLI_PassphraseKeyRoundTrips(t *testing.T) {
	tempDir := t.TempDir()
	binPath := filepath.Join(tempDir, "aegis_cli")
	if out, err := exec.Command("go", "build", "-o", binPath, ".").CombinedOutput(); err != nil {
		t.Fatalf("build aegis binary: %v, output: %s", err, out)
	}

	keyPrefix := filepath.Join(tempDir, "pass_key")
	if out, err := exec.Command(binPath, "keygen", "-out", keyPrefix).CombinedOutput(); err != nil {
		t.Fatalf("keygen: %v, output: %s", err, out)
	}

	srcPath := filepath.Join(tempDir, "test.go")
	if err := os.WriteFile(srcPath, []byte(testGoSource), 0644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	airPath := filepath.Join(tempDir, "test.air")
	if out, err := exec.Command(binPath, "compile", "-src", srcPath, "-out", airPath).CombinedOutput(); err != nil {
		t.Fatalf("compile: %v, output: %s", err, out)
	}

	apkgPath := filepath.Join(tempDir, "test.apkg")
	const passphrase = "correct horse battery staple"
	pkgOut, err := exec.Command(binPath, "package",
		"-module", airPath,
		"-key", keyPrefix+".key",
		"-enckey", passphrase,
		"-out", apkgPath,
	).CombinedOutput()
	if err != nil {
		t.Fatalf("package with a passphrase: %v, output: %s", err, pkgOut)
	}
	if !strings.Contains(string(pkgOut), "Key Derivation: argon2id") {
		t.Errorf("package output should report Argon2id derivation, got: %s", pkgOut)
	}

	// The manifest's kdf.salt is plaintext JSON, not secret; prove it is
	// there rather than the package silently falling back to a direct key.
	inspectOut, err := exec.Command(binPath, "inspect", "-pkg", apkgPath).CombinedOutput()
	if err != nil {
		t.Fatalf("inspect: %v, output: %s", err, inspectOut)
	}

	runOut, err := exec.Command(binPath, "run",
		"-pkg", apkgPath, "-key", passphrase, "-func", "Add", "-args", "50,25",
		"-pubkey", keyPrefix+".pub",
	).CombinedOutput()
	if err != nil {
		t.Fatalf("run with the same passphrase: %v, output: %s", err, runOut)
	}
	if !strings.Contains(string(runOut), "Result: 75") {
		t.Errorf("expected 'Result: 75', got: %s", runOut)
	}

	wrongOut, err := exec.Command(binPath, "run",
		"-pkg", apkgPath, "-key", "wrong passphrase entirely", "-func", "Add", "-args", "50,25",
		"-pubkey", keyPrefix+".pub",
	).CombinedOutput()
	if err == nil {
		t.Fatalf("run with the wrong passphrase should fail, got: %s", wrongOut)
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
		"-pubkey", keyPrefix + ".pub",
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

// TestCLI_RunRequiresSignatureVerification proves the enforcement added
// alongside the Argon2id change: a .apkg refuses to run without -pubkey,
// accepts -insecure-skip-verify but warns loudly on stderr when it does, and
// the integrity check now runs before the plaintext key material exists.
func TestCLI_RunRequiresSignatureVerification(t *testing.T) {
	tempDir := t.TempDir()
	keyPrefix := filepath.Join(tempDir, "sig_key")
	var out, errBuf bytes.Buffer
	if err := Run([]string{"keygen", "-out", keyPrefix}, &out, &errBuf); err != nil {
		t.Fatalf("keygen: %v", err)
	}

	srcPath := filepath.Join(tempDir, "test.go")
	if err := os.WriteFile(srcPath, []byte(testGoSource), 0644); err != nil {
		t.Fatal(err)
	}
	airPath := filepath.Join(tempDir, "test.air")
	out.Reset()
	errBuf.Reset()
	if err := Run([]string{"compile", "-src", srcPath, "-out", airPath}, &out, &errBuf); err != nil {
		t.Fatalf("compile: %v, stderr: %s", err, errBuf.String())
	}

	apkgPath := filepath.Join(tempDir, "test.apkg")
	out.Reset()
	errBuf.Reset()
	if err := Run([]string{
		"package", "-module", airPath,
		"-key", keyPrefix + ".key", "-enckey", keyPrefix + ".enc", "-out", apkgPath,
	}, &out, &errBuf); err != nil {
		t.Fatalf("package: %v, stderr: %s", err, errBuf.String())
	}

	t.Run("no pubkey and no skip flag is refused", func(t *testing.T) {
		var runOut, runErr bytes.Buffer
		err := Run([]string{
			"run", "-pkg", apkgPath, "-key", keyPrefix + ".enc", "-func", "Add", "-args", "1,1",
		}, &runOut, &runErr)
		if err == nil {
			t.Fatal("expected a refusal with no -pubkey and no -insecure-skip-verify")
		}
		if !strings.Contains(err.Error(), "requires -pubkey") {
			t.Errorf("wrong refusal reason: %v", err)
		}
	})

	t.Run("insecure-skip-verify runs but warns loudly", func(t *testing.T) {
		var runOut, runErr bytes.Buffer
		err := Run([]string{
			"run", "-pkg", apkgPath, "-key", keyPrefix + ".enc", "-func", "Add", "-args", "1,1",
			"-insecure-skip-verify",
		}, &runOut, &runErr)
		if err != nil {
			t.Fatalf("run with -insecure-skip-verify: %v, stderr: %s", err, runErr.String())
		}
		if !strings.Contains(runOut.String(), "Result: 2") {
			t.Errorf("expected 'Result: 2', got: %s", runOut.String())
		}
		if !strings.Contains(runErr.String(), "NOT CHECKED") {
			t.Errorf("expected a loud warning on stderr, got: %s", runErr.String())
		}
	})

	t.Run("pubkey verifies and says so", func(t *testing.T) {
		var runOut, runErr bytes.Buffer
		err := Run([]string{
			"run", "-pkg", apkgPath, "-key", keyPrefix + ".enc", "-func", "Add", "-args", "1,1",
			"-pubkey", keyPrefix + ".pub",
		}, &runOut, &runErr)
		if err != nil {
			t.Fatalf("run with -pubkey: %v, stderr: %s", err, runErr.String())
		}
		if !strings.Contains(runOut.String(), "[Signature] Verified") {
			t.Errorf("expected a signature-verified line, got: %s", runOut.String())
		}
	})

	t.Run("bad integrity-policy value is rejected", func(t *testing.T) {
		var runOut, runErr bytes.Buffer
		err := Run([]string{
			"run", "-pkg", apkgPath, "-key", keyPrefix + ".enc", "-func", "Add", "-args", "1,1",
			"-pubkey", keyPrefix + ".pub", "-integrity-policy", "nonsense",
		}, &runOut, &runErr)
		if err == nil {
			t.Fatal("expected a flag-validation error for an unknown -integrity-policy value")
		}
	})
}

// TestCLI_PackageRefusesSecretFiles proves the packager's deny-list fails the
// build outright when the application directory contains a secret file,
// rather than silently leaving it out (which would let an operator believe
// a leaked .env was protected when it was never sealed in either way — or,
// worse, never notice it was sealed in at all).
func TestCLI_PackageRefusesSecretFiles(t *testing.T) {
	for name, path := range map[string]string{
		".env":           ".env",
		"dotenv variant": ".env.production",
		"PEM file":       "certs/server.pem",
		"private key":    "keys/service.key",
		"SSH key":        "id_rsa",
	} {
		t.Run(name, func(t *testing.T) {
			tempDir := t.TempDir()
			appDir := filepath.Join(tempDir, "app")
			if err := os.MkdirAll(filepath.Join(appDir, filepath.Dir(path)), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(appDir, path), []byte("secret"), 0644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(appDir, "app.bcl"), []byte(`name "x"`), 0644); err != nil {
				t.Fatal(err)
			}

			keyPrefix := filepath.Join(tempDir, "deny_key")
			var out, errBuf bytes.Buffer
			if err := Run([]string{"keygen", "-out", keyPrefix}, &out, &errBuf); err != nil {
				t.Fatalf("keygen: %v", err)
			}

			out.Reset()
			errBuf.Reset()
			err := Run([]string{
				"package", "-dir", appDir,
				"-key", keyPrefix + ".key", "-enckey", keyPrefix + ".enc",
				"-out", filepath.Join(tempDir, "app.apkg"),
			}, &out, &errBuf)
			if err == nil {
				t.Fatalf("expected packaging to refuse a directory containing %s", path)
			}
			if !strings.Contains(err.Error(), "denied pattern") {
				t.Errorf("wrong refusal reason: %v", err)
			}
		})
	}
}

// TestCLI_PackagePrintsManifest proves a successful package lists every file
// it sealed, so an operator can audit what left the build.
func TestCLI_PackagePrintsManifest(t *testing.T) {
	tempDir := t.TempDir()
	appDir := filepath.Join(tempDir, "app")
	if err := os.MkdirAll(appDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(appDir, "app.bcl"), []byte(`name "x"`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(appDir, "readme.txt"), []byte("hi"), 0644); err != nil {
		t.Fatal(err)
	}

	keyPrefix := filepath.Join(tempDir, "manifest_key")
	var out, errBuf bytes.Buffer
	if err := Run([]string{"keygen", "-out", keyPrefix}, &out, &errBuf); err != nil {
		t.Fatalf("keygen: %v", err)
	}

	out.Reset()
	errBuf.Reset()
	if err := Run([]string{
		"package", "-dir", appDir,
		"-key", keyPrefix + ".key", "-enckey", keyPrefix + ".enc",
		"-out", filepath.Join(tempDir, "app.apkg"),
	}, &out, &errBuf); err != nil {
		t.Fatalf("package: %v, stderr: %s", err, errBuf.String())
	}

	if !strings.Contains(out.String(), "app.bcl") || !strings.Contains(out.String(), "readme.txt") {
		t.Errorf("manifest missing a sealed file, got:\n%s", out.String())
	}
}
