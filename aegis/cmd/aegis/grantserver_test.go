package main

import (
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/oarkflow/ref/aegis/control"
)

// TestGrantServerEndToEnd proves phase 4's key-release path: package an
// application, register it with a (real, in-process) control server,
// run it by fetching the key from that server instead of a local -key,
// then revoke the package and confirm a second run is refused.
func TestGrantServerEndToEnd(t *testing.T) {
	tempDir := t.TempDir()
	binPath := filepath.Join(tempDir, "aegis_cli")
	if out, err := exec.Command("go", "build", "-o", binPath, ".").CombinedOutput(); err != nil {
		t.Fatalf("build aegis binary: %v, output: %s", err, out)
	}

	store := control.NewMemoryStore()
	// Matches `aegis serve`'s own default (no -session-keys): the grant
	// hands out the package's exact master key, matching how `aegis
	// package` and `aegis register` work — one key per package, not one
	// built fresh per session. See control.WithRawMasterKeyGrants.
	gs := control.NewGrantService(store, control.NewKeyManager(store), control.WithRawMasterKeyGrants())
	srv := control.NewServer(store, control.WithServerGrantService(gs))
	ts := httptest.NewServer(srv)
	defer ts.Close()

	keyPrefix := filepath.Join(tempDir, "grant_key")
	if out, err := exec.Command(binPath, "keygen", "-out", keyPrefix).CombinedOutput(); err != nil {
		t.Fatalf("keygen: %v, output: %s", err, out)
	}

	srcPath := filepath.Join(tempDir, "test.go")
	if err := os.WriteFile(srcPath, []byte(testGoSource), 0644); err != nil {
		t.Fatal(err)
	}
	airPath := filepath.Join(tempDir, "test.air")
	if out, err := exec.Command(binPath, "compile", "-src", srcPath, "-out", airPath).CombinedOutput(); err != nil {
		t.Fatalf("compile: %v, output: %s", err, out)
	}

	apkgPath := filepath.Join(tempDir, "test.apkg")
	if out, err := exec.Command(binPath, "package",
		"-module", airPath, "-key", keyPrefix+".key", "-enckey", keyPrefix+".enc", "-out", apkgPath,
	).CombinedOutput(); err != nil {
		t.Fatalf("package: %v, output: %s", err, out)
	}

	t.Run("register with the control server", func(t *testing.T) {
		out, err := exec.Command(binPath, "register", "-pkg", apkgPath, "-enckey", keyPrefix+".enc", "-server", ts.URL).CombinedOutput()
		if err != nil {
			t.Fatalf("register: %v, output: %s", err, out)
		}
		if !strings.Contains(string(out), "Registered package") {
			t.Errorf("unexpected register output: %s", out)
		}
	})

	t.Run("run fetches the key from the control server", func(t *testing.T) {
		out, err := exec.Command(binPath, "run",
			"-pkg", apkgPath, "-grant-server", ts.URL, "-pubkey", keyPrefix+".pub",
			"-func", "Add", "-args", "50,25",
		).CombinedOutput()
		if err != nil {
			t.Fatalf("run -grant-server: %v, output: %s", err, out)
		}
		if !strings.Contains(string(out), "Result: 75") {
			t.Errorf("expected 'Result: 75', got: %s", out)
		}
		if !strings.Contains(string(out), "[Grant] obtained from") {
			t.Errorf("expected a [Grant] line, got: %s", out)
		}
	})

	t.Run("-key and -grant-server together are rejected", func(t *testing.T) {
		out, err := exec.Command(binPath, "run",
			"-pkg", apkgPath, "-key", keyPrefix+".enc", "-grant-server", ts.URL,
			"-pubkey", keyPrefix+".pub", "-func", "Add", "-args", "1,1",
		).CombinedOutput()
		if err == nil {
			t.Fatalf("expected a refusal when both -key and -grant-server are given, got: %s", out)
		}
	})

	t.Run("revoking the package denies a subsequent run", func(t *testing.T) {
		// Find the package ID the way register derived it: by inspecting
		// the apkg (register printed it too, but re-deriving it here keeps
		// this test independent of that output's exact wording).
		inspectOut, err := exec.Command(binPath, "inspect", "-pkg", apkgPath).CombinedOutput()
		if err != nil {
			t.Fatalf("inspect: %v", err)
		}
		pkgID := extractPackageID(t, string(inspectOut))

		if err := store.RevokePackage(t.Context(), pkgID, "key compromised"); err != nil {
			t.Fatalf("revoke via store: %v", err)
		}

		out, err := exec.Command(binPath, "run",
			"-pkg", apkgPath, "-grant-server", ts.URL, "-pubkey", keyPrefix+".pub",
			"-func", "Add", "-args", "1,1",
		).CombinedOutput()
		if err == nil {
			t.Fatalf("expected the run to be refused after revocation, got: %s", out)
		}
	})
}

// extractPackageID pulls "Package ID:    <id>" out of `aegis inspect`'s output.
func extractPackageID(t *testing.T, inspectOutput string) string {
	t.Helper()
	for _, line := range strings.Split(inspectOutput, "\n") {
		if strings.Contains(line, "Package ID") {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				return strings.TrimSpace(parts[1])
			}
		}
	}
	t.Fatalf("could not find Package ID in inspect output: %s", inspectOutput)
	return ""
}

// TestRevokeCommand proves `aegis revoke` (the CLI wrapper around
// POST /v1/package/revoke) actually takes effect against a real server.
func TestRevokeCommand(t *testing.T) {
	tempDir := t.TempDir()
	binPath := filepath.Join(tempDir, "aegis_cli")
	if out, err := exec.Command("go", "build", "-o", binPath, ".").CombinedOutput(); err != nil {
		t.Fatalf("build aegis binary: %v, output: %s", err, out)
	}

	store := control.NewMemoryStore()
	gs := control.NewGrantService(store, control.NewKeyManager(store), control.WithRawMasterKeyGrants())
	srv := control.NewServer(store, control.WithServerGrantService(gs))
	ts := httptest.NewServer(srv)
	defer ts.Close()

	keyPrefix := filepath.Join(tempDir, "revoke_key")
	if out, err := exec.Command(binPath, "keygen", "-out", keyPrefix).CombinedOutput(); err != nil {
		t.Fatalf("keygen: %v, output: %s", err, out)
	}
	srcPath := filepath.Join(tempDir, "test.go")
	if err := os.WriteFile(srcPath, []byte(testGoSource), 0644); err != nil {
		t.Fatal(err)
	}
	airPath := filepath.Join(tempDir, "test.air")
	if out, err := exec.Command(binPath, "compile", "-src", srcPath, "-out", airPath).CombinedOutput(); err != nil {
		t.Fatalf("compile: %v, output: %s", err, out)
	}
	apkgPath := filepath.Join(tempDir, "test.apkg")
	if out, err := exec.Command(binPath, "package",
		"-module", airPath, "-key", keyPrefix+".key", "-enckey", keyPrefix+".enc", "-out", apkgPath,
	).CombinedOutput(); err != nil {
		t.Fatalf("package: %v, output: %s", err, out)
	}
	if out, err := exec.Command(binPath, "register", "-pkg", apkgPath, "-enckey", keyPrefix+".enc", "-server", ts.URL).CombinedOutput(); err != nil {
		t.Fatalf("register: %v, output: %s", err, out)
	}

	// Works before revocation.
	if out, err := exec.Command(binPath, "run",
		"-pkg", apkgPath, "-grant-server", ts.URL, "-pubkey", keyPrefix+".pub", "-func", "Add", "-args", "1,1",
	).CombinedOutput(); err != nil {
		t.Fatalf("run before revoke: %v, output: %s", err, out)
	}

	inspectOut, err := exec.Command(binPath, "inspect", "-pkg", apkgPath).CombinedOutput()
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	pkgID := extractPackageID(t, string(inspectOut))

	revokeOut, err := exec.Command(binPath, "revoke", "-package-id", pkgID, "-reason", "test revocation", "-server", ts.URL).CombinedOutput()
	if err != nil {
		t.Fatalf("revoke: %v, output: %s", err, revokeOut)
	}
	if !strings.Contains(string(revokeOut), "Revoked package") {
		t.Errorf("unexpected revoke output: %s", revokeOut)
	}

	if out, err := exec.Command(binPath, "run",
		"-pkg", apkgPath, "-grant-server", ts.URL, "-pubkey", keyPrefix+".pub", "-func", "Add", "-args", "1,1",
	).CombinedOutput(); err == nil {
		t.Fatalf("expected run to fail after revoke, got: %s", out)
	}
}
