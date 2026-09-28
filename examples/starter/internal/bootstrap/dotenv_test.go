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
