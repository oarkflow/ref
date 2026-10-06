package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnvHelpers(t *testing.T) {
	tmpDir := t.TempDir()

	// Test LoadDotenv
	dotenvPath := filepath.Join(tmpDir, ".env")
	content := `# Comment line
FOO_TEST=bar
export BAZ_TEST="qux"
SINGLE_QUOTE='hello world'
`
	if err := os.WriteFile(dotenvPath, []byte(content), 0644); err != nil {
		t.Fatalf("writing dotenv: %v", err)
	}

	if err := LoadDotenv(dotenvPath); err != nil {
		t.Fatalf("LoadDotenv failed: %v", err)
	}

	if v := os.Getenv("FOO_TEST"); v != "bar" {
		t.Errorf("FOO_TEST: got %q, want %q", v, "bar")
	}
	if v := os.Getenv("BAZ_TEST"); v != "qux" {
		t.Errorf("BAZ_TEST: got %q, want %q", v, "qux")
	}
	if v := os.Getenv("SINGLE_QUOTE"); v != "hello world" {
		t.Errorf("SINGLE_QUOTE: got %q, want %q", v, "hello world")
	}

	// Test Env fallback
	if v := Env("NON_EXISTENT_KEY_12345", "fallback_val"); v != "fallback_val" {
		t.Errorf("Env fallback: got %q, want fallback_val", v)
	}
	if v := Env("FOO_TEST", "fallback_val"); v != "bar" {
		t.Errorf("Env existing: got %q, want bar", v)
	}

	// Test LoadSecretFile
	secretPath := filepath.Join(tmpDir, "secret_file")
	if err := os.WriteFile(secretPath, []byte("super-secret-key\n"), 0600); err != nil {
		t.Fatalf("writing secret file: %v", err)
	}
	if err := LoadSecretFile("SECRET_TEST_VAR", secretPath); err != nil {
		t.Fatalf("LoadSecretFile failed: %v", err)
	}
	if v := os.Getenv("SECRET_TEST_VAR"); v != "super-secret-key" {
		t.Errorf("SECRET_TEST_VAR: got %q, want super-secret-key", v)
	}

	// Test ResolveDir
	subDir := filepath.Join(tmpDir, "my_dir")
	if err := os.Mkdir(subDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	resolved := ResolveDir(filepath.Join(tmpDir, "not_exist"), subDir)
	if resolved != subDir {
		t.Errorf("ResolveDir: got %q, want %q", resolved, subDir)
	}
}
