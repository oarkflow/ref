package migrations

import (
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func testConfig(t *testing.T) Config {
	dir := t.TempDir()
	return Config{
		Driver:       "sqlite",
		DSN:          "file:" + filepath.Join(dir, "app.db") + "?_pragma=busy_timeout(5000)",
		MigrationDir: "../../resources/migrations",
		SeedDir:      "../../resources/migrations/seeds",
	}
}

func TestPendingListsEveryMigrationOnAFreshDatabase(t *testing.T) {
	mgr, historyDriver, err := NewManager(testConfig(t))
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	pending, err := Pending(mgr, historyDriver)
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	want := []string{"1_create_users_table", "2_create_audit_log_table", "3_create_password_resets_table", "4_create_orders_table", "5_create_todos_table"}
	if len(pending) != len(want) {
		t.Fatalf("Pending = %v, want %d entries (%v)", pending, len(want), want)
	}
	seen := make(map[string]bool, len(pending))
	for _, name := range pending {
		seen[name] = true
	}
	for _, name := range want {
		if !seen[name] {
			t.Errorf("Pending is missing %q: %v", name, pending)
		}
	}
}

func TestApplyClearsPendingAndIsIdempotent(t *testing.T) {
	config := testConfig(t)
	mgr, historyDriver, err := NewManager(config)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if err := Apply(mgr); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	pending, err := Pending(mgr, historyDriver)
	if err != nil {
		t.Fatalf("Pending after Apply: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("Pending after Apply = %v, want none", pending)
	}

	// A second Apply against an already-migrated database must be a
	// no-op, not an error — Manager.ApplyMigration skips a migration whose
	// checksum already matches its recorded history row. This is what lets
	// cmd/server call Apply on every boot without re-running anything.
	if err := Apply(mgr); err != nil {
		t.Fatalf("second Apply (should be a no-op) = %v", err)
	}
}

func TestPendingIsEmptyAfterMigratingWithASeparateManager(t *testing.T) {
	// The two Managers share the same DSN but are otherwise independent
	// instances — this is the shape cmd/migrator (a separate process) and
	// cmd/server (checking on its own boot) actually have: neither knows
	// about the other's in-memory state, only the database they both open.
	config := testConfig(t)
	applier, _, err := NewManager(config)
	if err != nil {
		t.Fatalf("NewManager (applier): %v", err)
	}
	if err := Apply(applier); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	checker, historyDriver, err := NewManager(config)
	if err != nil {
		t.Fatalf("NewManager (checker): %v", err)
	}
	pending, err := Pending(checker, historyDriver)
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("Pending as seen by a fresh Manager = %v, want none", pending)
	}
}
