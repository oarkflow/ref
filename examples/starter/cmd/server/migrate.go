package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/oarkflow/zlog"

	"github.com/oarkflow/ref/examples/starter/internal/bootstrap"
	"github.com/oarkflow/ref/examples/starter/internal/migrations"
)

// ensureMigrated checks resources/migrations/*.bcl against the database
// this boot is about to open and, if anything is pending, gets it applied
// before platform.LoadDir ever touches an application table — a fresh
// checkout (or a checkout with a new migration file since the last deploy)
// used to fail here with a raw "no such table" from the seeding step deep
// inside LoadDir; this turns that into a clear, actionable step instead.
//
// Three outcomes, depending on how this process was started:
//   - AUTO_MIGRATE=true (or "1"): applied without asking — the shape a
//     container entrypoint or CI job wants, where nothing can answer a
//     prompt.
//   - An interactive terminal, AUTO_MIGRATE unset: asks once, applies on
//     "y", refuses to boot on anything else — so this is never destructive
//     without a human saying so.
//   - Neither (a service manager with no attached terminal, and
//     AUTO_MIGRATE unset): refuses to boot with a message naming exactly
//     what to run, rather than guessing.
//
// This is deliberately narrower than `go run ./cmd/migrator` itself: it
// only ever adds missing migrations forward, through the same idempotent
// Manager.ApplyMigration the CLI uses (internal/migrations.Apply) — never
// rollback, never force, never seed. Reach for the migrator binary
// directly for any of those.
func ensureMigrated(logger *zlog.Logger) error {
	driver := env("DB_DRIVER", "sqlite")
	dsn := env("DB_DSN", "file:.data/starter/app.db?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	// MIGRATIONS_DIR/MIGRATIONS_SEED_DIR override the resolved default —
	// useful for a nonstandard layout, and for a test that must not rely on
	// bootstrap.ResolveDir's CWD-based guessing matching go test's own CWD
	// (cmd/server's own package directory, which none of its candidates do).
	migrationDir, seedDir := migrationDirs()

	if fi, err := os.Stat(migrationDir); err != nil || !fi.IsDir() {
		// ListMigrationMap silently reports zero declared migrations for a
		// directory that does not exist — indistinguishable, to Pending,
		// from a genuinely empty (but real) one, and "0 pending" then reads
		// as "fully migrated" instead of "misconfigured." Catching it here,
		// with the resolved path in the message, turns a silent false
		// negative into a startup failure that names the actual problem.
		return fmt.Errorf("migration directory %q does not exist (MIGRATIONS_DIR to override)", migrationDir)
	}

	mgr, historyDriver, err := migrations.NewManager(migrations.Config{
		Driver:       driver,
		DSN:          dsn,
		MigrationDir: migrationDir,
		SeedDir:      seedDir,
	})
	if err != nil {
		return fmt.Errorf("connecting to check migrations: %w", err)
	}

	pending, err := migrations.Pending(mgr, historyDriver)
	if err != nil {
		return fmt.Errorf("checking migrations: %w", err)
	}
	if len(pending) == 0 {
		return nil
	}

	names := strings.Join(pending, ", ")
	logger.Warn("pending migrations", zlog.Int("count", len(pending)), zlog.String("names", names))

	switch auto := os.Getenv("AUTO_MIGRATE"); {
	case auto == "true" || auto == "1":
		logger.Info("AUTO_MIGRATE is set — applying without asking")
	case isInteractiveTerminal():
		prompt := fmt.Sprintf("%d pending migration(s) (%s). Run them now? [y/N]: ", len(pending), names)
		if !promptYesNo(prompt) {
			return fmt.Errorf("%d pending migration(s) not applied — run `go run ./cmd/migrator cli migrate` yourself, or set AUTO_MIGRATE=true, then start the server again", len(pending))
		}
	default:
		return fmt.Errorf("%d pending migration(s) (%s) and no terminal to ask — run `go run ./cmd/migrator cli migrate` first, or set AUTO_MIGRATE=true", len(pending), names)
	}

	if err := migrations.Apply(mgr); err != nil {
		return fmt.Errorf("applying migrations: %w", err)
	}
	logger.Info("migrations applied", zlog.Int("count", len(pending)))
	return nil
}

// isInteractiveTerminal reports whether this process can plausibly read an
// answer from a human right now — true for an ordinary terminal session,
// false when stdin is a pipe, a redirected file, or absent (a container
// with no -it, a service manager, CI). A package-level func var, not a
// plain function, so a test can substitute it and drive ensureMigrated's
// decision logic deterministically regardless of the test runner's own
// stdin.
var isInteractiveTerminal = func() bool {
	stat, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return stat.Mode()&os.ModeCharDevice != 0
}

// promptYesNo asks on stderr (stdout may be piped/redirected for logs) and
// reads one line from stdin. Anything other than "y"/"yes" (any case) is a
// no — an empty line (just pressing enter) included, so the default is
// always "don't touch the database" unless the answer is unambiguous. Also
// a func var — see isInteractiveTerminal's comment.
var promptYesNo = func(prompt string) bool {
	fmt.Fprint(os.Stderr, prompt)
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	default:
		return false
	}
}

// migrationDirs resolves the migration and seed directories; MIGRATIONS_DIR
// and MIGRATIONS_SEED_DIR override the CWD-based guess.
func migrationDirs() (migrationDir, seedDir string) {
	migrationDir = env("MIGRATIONS_DIR", bootstrap.ResolveDir("examples/starter/resources/migrations", "./resources/migrations", "resources/migrations"))
	seedDir = env("MIGRATIONS_SEED_DIR", bootstrap.ResolveDir("examples/starter/resources/migrations/seeds", "./resources/migrations/seeds", "resources/migrations/seeds"))
	return migrationDir, seedDir
}

// migrateSQLite applies every declared migration to the SQLite database at
// dsn. Studio's preview uses it to give a sandboxed (empty) database the
// starter's schema; unlike ensureMigrated it never asks and never touches the
// database the process itself serves.
func migrateSQLite(dsn string) error {
	migrationDir, seedDir := migrationDirs()
	if fi, err := os.Stat(migrationDir); err != nil || !fi.IsDir() {
		return fmt.Errorf("migration directory %q does not exist (MIGRATIONS_DIR to override)", migrationDir)
	}
	mgr, _, err := migrations.NewManager(migrations.Config{
		Driver: "sqlite", DSN: dsn, MigrationDir: migrationDir, SeedDir: seedDir,
	})
	if err != nil {
		return fmt.Errorf("connecting to the preview database: %w", err)
	}
	return migrations.Apply(mgr)
}

func env(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}
