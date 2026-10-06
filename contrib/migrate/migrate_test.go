package migrate

import (
	"path/filepath"
	"testing"
)

func TestConfigDialect(t *testing.T) {
	tests := []struct {
		driver string
		want   string
	}{
		{"", "sqlite"},
		{"sqlite", "sqlite"},
		{"postgres", "postgres"},
		{"pgx", "postgres"},
		{"mysql", "mysql"},
	}

	for _, tt := range tests {
		cfg := Config{Driver: tt.driver}
		if got := cfg.Dialect(); got != tt.want {
			t.Errorf("Dialect(%q) = %q, want %q", tt.driver, got, tt.want)
		}
	}
}

func TestMkdirForSQLiteDSN(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "nested", "dir", "test.db")
	dsn := "file:" + dbPath + "?cache=shared"

	mkdirForSQLiteDSN(dsn)

	parentDir := filepath.Dir(dbPath)
	if fi, err := filepath.Glob(parentDir); err != nil || len(fi) == 0 {
		t.Errorf("expected directory %q to be created", parentDir)
	}
}
