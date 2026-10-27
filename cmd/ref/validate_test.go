package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestValidateCommandChecksConfigurationWithoutStartingApp(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "config", "app.bcl")
	if err := os.WriteFile(file, []byte("name \"example\"\nversion \"1.0\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runValidation(context.Background(), "validate", []string{root}); err != nil {
		t.Fatalf("valid app: %v", err)
	}
	if err := os.WriteFile(file, []byte("route \"broken\" {"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runValidation(context.Background(), "validate", []string{root}); err == nil {
		t.Fatal("invalid app passed validation")
	}
}
