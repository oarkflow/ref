package control

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestSQLStoreSurvivesRestart is the actual point of phase 4's persistent
// store: MemoryStore forgets everything when the process exits. This opens
// a real file, closes it, and reopens it as a fresh *SQLStore to prove the
// data — including a revocation — is still there.
func TestSQLStoreSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.db")
	ctx := context.Background()

	s1, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s1.StorePackage(ctx, PackageRecord{PackageID: "pkg-1", ApplicationID: "app", Version: "1.0.0"}); err != nil {
		t.Fatal(err)
	}
	key := make([]byte, 32)
	if err := s1.StoreMasterKey(ctx, "pkg-1", key); err != nil {
		t.Fatal(err)
	}
	if err := s1.RevokePackage(ctx, "pkg-1", "restart test"); err != nil {
		t.Fatal(err)
	}
	if err := s1.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected a real file on disk at %s: %v", path, err)
	}

	s2, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()

	revoked, reason, err := s2.IsPackageRevoked(ctx, "pkg-1")
	if err != nil || !revoked || reason != "restart test" {
		t.Fatalf("revocation did not survive restart: revoked=%v reason=%q err=%v", revoked, reason, err)
	}
	gotKey, err := s2.GetMasterKey(ctx, "pkg-1")
	if err != nil || string(gotKey) != string(key) {
		t.Fatalf("master key did not survive restart: %v, %v", gotKey, err)
	}
}

func TestOpenSQLiteRejectsNilDB(t *testing.T) {
	if _, err := NewSQLStore(nil); err == nil {
		t.Fatal("expected an error for a nil *sql.DB")
	}
}
