package bootstrap

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDotenvSetsUnsetVars(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	content := "" +
		"# a comment\n" +
		"\n" +
		"export SESSION_SECRET=abc123\n" +
		"WEBHOOK_SECRET=\"quoted value\"\n" +
		"SINGLE_QUOTED='also quoted'\n" +
		"SPACED = trimmed \n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, key := range []string{"SESSION_SECRET", "WEBHOOK_SECRET", "SINGLE_QUOTED", "SPACED"} {
		os.Unsetenv(key)
	}
	t.Cleanup(func() {
		for _, key := range []string{"SESSION_SECRET", "WEBHOOK_SECRET", "SINGLE_QUOTED", "SPACED"} {
			os.Unsetenv(key)
		}
	})

	if err := LoadDotenv(path); err != nil {
		t.Fatalf("LoadDotenv: %v", err)
	}

	cases := map[string]string{
		"SESSION_SECRET": "abc123",
		"WEBHOOK_SECRET": "quoted value",
		"SINGLE_QUOTED":  "also quoted",
		"SPACED":         "trimmed",
	}
	for key, want := range cases {
		if got := os.Getenv(key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
}

func TestLoadDotenvNeverOverridesRealEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	if err := os.WriteFile(path, []byte("REAL_VAR=from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("REAL_VAR", "from-process")

	if err := LoadDotenv(path); err != nil {
		t.Fatalf("LoadDotenv: %v", err)
	}
	if got := os.Getenv("REAL_VAR"); got != "from-process" {
		t.Fatalf("REAL_VAR = %q, want the process value to survive untouched", got)
	}
}

func TestLoadDotenvMissingFileIsNotAnError(t *testing.T) {
	if err := LoadDotenv(filepath.Join(t.TempDir(), "does-not-exist.env")); err != nil {
		t.Fatalf("LoadDotenv on a missing file = %v, want nil", err)
	}
}

func TestLoadSecretFileSetsUnsetVar(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session_secret")
	if err := os.WriteFile(path, []byte("a-real-secret-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	os.Unsetenv("TEST_SECRET_VAR")
	t.Cleanup(func() { os.Unsetenv("TEST_SECRET_VAR") })

	if err := LoadSecretFile("TEST_SECRET_VAR", path); err != nil {
		t.Fatalf("LoadSecretFile: %v", err)
	}
	// The trailing newline `echo` (unlike `printf`) would append is trimmed —
	// a secrets-manager sidecar rendering with `echo` must not silently
	// produce a value with an extra byte on the end.
	if got := os.Getenv("TEST_SECRET_VAR"); got != "a-real-secret-value" {
		t.Fatalf("TEST_SECRET_VAR = %q, want %q", got, "a-real-secret-value")
	}
}

func TestLoadSecretFileNeverOverridesRealEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session_secret")
	if err := os.WriteFile(path, []byte("from-file"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEST_SECRET_VAR", "from-process")

	if err := LoadSecretFile("TEST_SECRET_VAR", path); err != nil {
		t.Fatalf("LoadSecretFile: %v", err)
	}
	if got := os.Getenv("TEST_SECRET_VAR"); got != "from-process" {
		t.Fatalf("TEST_SECRET_VAR = %q, want the process value to survive untouched", got)
	}
}

func TestLoadSecretFileMissingFileIsNotAnError(t *testing.T) {
	os.Unsetenv("TEST_SECRET_VAR")
	if err := LoadSecretFile("TEST_SECRET_VAR", filepath.Join(t.TempDir(), "does-not-exist")); err != nil {
		t.Fatalf("LoadSecretFile on a missing file = %v, want nil", err)
	}
	if got, ok := os.LookupEnv("TEST_SECRET_VAR"); ok {
		t.Fatalf("TEST_SECRET_VAR = %q, want it to stay unset", got)
	}
}
