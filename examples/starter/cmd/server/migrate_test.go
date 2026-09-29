package main

import (
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/oarkflow/zlog"

	_ "modernc.org/sqlite"
)

// withMigrationEnv points DB_DRIVER/DB_DSN and the migration/seed dirs
// ensureMigrated reads at a fresh temporary SQLite database, the way a
// real fresh checkout's first `go run ./cmd/server` would see one.
func withMigrationEnv(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DB_DRIVER", "sqlite")
	t.Setenv("DB_DSN", "file:"+filepath.Join(dir, "app.db")+"?_pragma=busy_timeout(5000)")
	t.Setenv("AUTO_MIGRATE", "")
	// bootstrap.ResolveDir's own candidates assume the process runs from the
	// repository root or examples/starter — never true for `go test`'s own
	// CWD (this package's directory), so it silently falls back to an
	// unresolved, nonexistent path. Overriding directly is what
	// ensureMigrated's own MIGRATIONS_DIR/MIGRATIONS_SEED_DIR env vars are
	// for — exercising the same override a real nonstandard deployment
	// would use, not a test-only shortcut.
	t.Setenv("MIGRATIONS_DIR", "../../resources/migrations")
	t.Setenv("MIGRATIONS_SEED_DIR", "../../resources/migrations/seeds")
}

func silentLogger() *zlog.Logger {
	return zlog.New(zlog.Options{Level: zlog.ErrorLevel, Sink: zlog.NewWriterSink(io.Discard, zlog.NewJSONEncoder(), zlog.TraceLevel)})
}

func TestEnsureMigratedAutoMigrateAppliesWithoutPrompting(t *testing.T) {
	withMigrationEnv(t)
	t.Setenv("AUTO_MIGRATE", "true")

	promptCalls := 0
	restorePrompt := promptYesNo
	promptYesNo = func(string) bool { promptCalls++; return false }
	t.Cleanup(func() { promptYesNo = restorePrompt })

	if err := ensureMigrated(silentLogger()); err != nil {
		t.Fatalf("ensureMigrated with AUTO_MIGRATE=true: %v", err)
	}
	if promptCalls != 0 {
		t.Fatalf("promptYesNo was called %d times, want 0 — AUTO_MIGRATE must never ask", promptCalls)
	}

	// Calling it again against the now-migrated database must stay a
	// silent no-op: nothing pending, so it must not even reach the
	// AUTO_MIGRATE/prompt decision at all.
	if err := ensureMigrated(silentLogger()); err != nil {
		t.Fatalf("ensureMigrated on an already-migrated database: %v", err)
	}
}

func TestEnsureMigratedInteractiveAppliesOnYes(t *testing.T) {
	withMigrationEnv(t)

	restoreInteractive := isInteractiveTerminal
	isInteractiveTerminal = func() bool { return true }
	t.Cleanup(func() { isInteractiveTerminal = restoreInteractive })

	var promptSeen string
	restorePrompt := promptYesNo
	promptYesNo = func(prompt string) bool { promptSeen = prompt; return true }
	t.Cleanup(func() { promptYesNo = restorePrompt })

	if err := ensureMigrated(silentLogger()); err != nil {
		t.Fatalf("ensureMigrated with a \"yes\" answer: %v", err)
	}
	if !strings.Contains(promptSeen, "pending migration") {
		t.Fatalf("prompt = %q, want it to mention pending migrations", promptSeen)
	}
}

func TestEnsureMigratedInteractiveRefusesOnNo(t *testing.T) {
	withMigrationEnv(t)

	restoreInteractive := isInteractiveTerminal
	isInteractiveTerminal = func() bool { return true }
	t.Cleanup(func() { isInteractiveTerminal = restoreInteractive })

	restorePrompt := promptYesNo
	promptYesNo = func(string) bool { return false }
	t.Cleanup(func() { promptYesNo = restorePrompt })

	err := ensureMigrated(silentLogger())
	if err == nil {
		t.Fatal("ensureMigrated with a \"no\" answer = nil error, want a refusal")
	}
	if !strings.Contains(err.Error(), "cmd/migrator") {
		t.Fatalf("error = %q, want it to name the migrator command to run instead", err.Error())
	}
}

func TestEnsureMigratedNonInteractiveWithoutAutoMigrateRefusesClearly(t *testing.T) {
	withMigrationEnv(t)

	restoreInteractive := isInteractiveTerminal
	isInteractiveTerminal = func() bool { return false }
	t.Cleanup(func() { isInteractiveTerminal = restoreInteractive })

	promptCalls := 0
	restorePrompt := promptYesNo
	promptYesNo = func(string) bool { promptCalls++; return true }
	t.Cleanup(func() { promptYesNo = restorePrompt })

	err := ensureMigrated(silentLogger())
	if err == nil {
		t.Fatal("ensureMigrated with no terminal and no AUTO_MIGRATE = nil error, want a refusal")
	}
	if promptCalls != 0 {
		t.Fatalf("promptYesNo was called %d times, want 0 — nothing can answer it here", promptCalls)
	}
	for _, want := range []string{"cmd/migrator", "AUTO_MIGRATE"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to mention %q", err.Error(), want)
		}
	}
}
