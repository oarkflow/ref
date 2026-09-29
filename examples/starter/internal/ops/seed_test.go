package ops

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/oarkflow/ref/platform"

	_ "modernc.org/sqlite"
)

func openTestDB(t *testing.T) *platform.Database {
	t.Helper()
	path := filepath.Join(t.TempDir(), "seed.db")
	sqlDB, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if _, err := sqlDB.Exec(`CREATE TABLE users (id TEXT PRIMARY KEY, email TEXT NOT NULL UNIQUE, name TEXT NOT NULL, password_hash TEXT NOT NULL, roles TEXT NOT NULL DEFAULT 'user', status TEXT NOT NULL DEFAULT 'active')`); err != nil {
		t.Fatal(err)
	}
	return &platform.Database{DB: sqlDB, Driver: "sqlite", Dialect: "sqlite"}
}

func TestSeedDevAdminSkipsInProduction(t *testing.T) {
	db := openTestDB(t)
	if err := SeedDevAdmin(context.Background(), db, "production", "admin@example.com", "Password123!"); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM users").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("users count = %d, want 0 in production", count)
	}
}

func TestSeedDevAdminSeedsOnceOutsideProduction(t *testing.T) {
	db := openTestDB(t)
	if err := SeedDevAdmin(context.Background(), db, "development", "admin@example.com", "Password123!"); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM users").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("users count = %d, want 1 after seeding", count)
	}

	// A second call must be a no-op: the table is no longer empty.
	if err := SeedDevAdmin(context.Background(), db, "development", "someone-else@example.com", "another password"); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM users").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("users count after second call = %d, want still 1 (not reseeded)", count)
	}
}

func TestSeedDevTodoRoleAccountsSkipsInProduction(t *testing.T) {
	db := openTestDB(t)
	if err := SeedDevTodoRoleAccounts(context.Background(), db, "production", "Password123!"); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM users").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("users count = %d, want 0 in production", count)
	}
}

func TestSeedDevTodoRoleAccountsSeedsEachRoleOnceAndIsIdempotent(t *testing.T) {
	db := openTestDB(t)
	if err := SeedDevTodoRoleAccounts(context.Background(), db, "development", "Password123!"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []struct{ email, role string }{
		{"owner@example.com", "user"},
		{"reviewer@example.com", "reviewer"},
		{"approver@example.com", "approver"},
	} {
		var role string
		if err := db.QueryRow("SELECT roles FROM users WHERE email = ?", want.email).Scan(&role); err != nil {
			t.Fatalf("%s: %v", want.email, err)
		}
		if role != want.role {
			t.Fatalf("%s roles = %q, want %q", want.email, role, want.role)
		}
	}

	// A second call must not duplicate or overwrite any of the three.
	if err := SeedDevTodoRoleAccounts(context.Background(), db, "development", "a different password"); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM users").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("users count after second call = %d, want still 3 (not reseeded)", count)
	}
}
