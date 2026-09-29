// Package migrations wraps github.com/oarkflow/migrate the same way for
// both binaries that need it: cmd/migrator (the full CLI — migrate,
// rollback, status, history, ...) and cmd/server (which only ever needs to
// ask "is anything pending?" and, if so, apply it — see main.go's
// ensureMigrated). One Config and one Manager constructor means the two
// binaries can never quietly disagree about how a dialect or a migration
// directory resolves.
package migrations

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/oarkflow/migrate"
)

// Config names everything needed to build a Manager against one database
// and one set of declared migrations.
type Config struct {
	// Driver is database/sql's name for the driver ("sqlite", "pgx", ...) —
	// the same value DB_DRIVER carries. Dialect() translates it to the name
	// github.com/oarkflow/migrate groups dialects by ("postgres" for pgx).
	Driver string
	DSN    string
	// MigrationDir and SeedDir are resolved paths (see bootstrap.ResolveDir),
	// not raw config — this package does not know about the repository
	// layout, on purpose, so it stays reusable if that layout ever changes.
	MigrationDir string
	SeedDir      string
	// HistoryTable defaults to "schema_migrations" when empty.
	HistoryTable string
}

// Dialect is Driver translated to github.com/oarkflow/migrate's naming.
func (c Config) Dialect() string {
	if c.Driver == "pgx" {
		return "postgres"
	}
	return c.Driver
}

// NewManager connects to the database and returns a Manager plus its own
// history driver (the Manager does not expose the one it was built with,
// and Pending needs to read it directly).
func NewManager(c Config) (*migrate.Manager, migrate.HistoryDriver, error) {
	dialect := c.Dialect()
	if dialect == "sqlite" {
		mkdirForSQLiteDSN(c.DSN)
	}
	dbDriver, err := migrate.NewDriver(dialect, c.DSN)
	if err != nil {
		return nil, nil, fmt.Errorf("connecting (%s driver, dialect %s): %w", c.Driver, dialect, err)
	}
	historyTable := c.HistoryTable
	if historyTable == "" {
		historyTable = "schema_migrations"
	}
	historyDriver, err := migrate.NewHistoryDriver("db", dialect, c.DSN, historyTable)
	if err != nil {
		return nil, nil, fmt.Errorf("history driver: %w", err)
	}
	mgr := migrate.NewManager(
		migrate.WithDriver(dbDriver),
		migrate.WithHistoryDriver(historyDriver),
		migrate.WithDialect(dialect),
		migrate.WithMigrationDir(c.MigrationDir),
		migrate.WithSeedDir(c.SeedDir),
	)
	return mgr, historyDriver, nil
}

// Pending lists every declared migration whose name has never been
// recorded in the history table — a fresh database, or a migration file
// added since the last deploy. It does not detect a *modified* migration
// that was already applied (a checksum mismatch); ApplyMigration itself
// catches that, with a clearer error, the moment Apply reaches it.
//
// Read-only: opens the same connection ValidateHistoryStorage/Load would,
// never a write transaction.
func Pending(mgr *migrate.Manager, historyDriver migrate.HistoryDriver) ([]string, error) {
	declared, err := mgr.ListMigrationMap()
	if err != nil {
		return nil, fmt.Errorf("listing migrations: %w", err)
	}
	applied, err := historyDriver.Load()
	if err != nil {
		return nil, fmt.Errorf("loading migration history: %w", err)
	}
	appliedNames := make(map[string]bool, len(applied))
	for _, h := range applied {
		appliedNames[h.Name] = true
	}
	pending := make([]string, 0, len(declared))
	for name := range declared {
		if !appliedNames[name] {
			pending = append(pending, name)
		}
	}
	sort.Strings(pending)
	return pending, nil
}

// Apply runs every declared migration, in the same deterministic order (by
// filename) the "migrate" CLI command uses, through Manager.ApplyMigration —
// which is itself idempotent (skips a migration whose checksum already
// matches the recorded one), so calling Apply when nothing is pending is a
// safe no-op, not a redundant re-run.
func Apply(mgr *migrate.Manager) error {
	declared, err := mgr.ListMigrationMap()
	if err != nil {
		return fmt.Errorf("listing migrations: %w", err)
	}
	seen := make(map[string]bool, len(declared))
	paths := make([]string, 0, len(declared))
	for _, path := range declared {
		if seen[path] {
			continue
		}
		seen[path] = true
		paths = append(paths, path)
	}
	sort.Slice(paths, func(i, j int) bool { return filepath.Base(paths[i]) < filepath.Base(paths[j]) })

	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("reading %s: %w", path, err)
		}
		parsed, err := migrate.ParseMigrationsBCL(data)
		if err != nil {
			return fmt.Errorf("parsing %s: %w", path, err)
		}
		for _, m := range parsed {
			if err := mgr.ApplyMigration(m); err != nil {
				return fmt.Errorf("applying %q: %w", m.Name, err)
			}
		}
	}
	return nil
}

// mkdirForSQLiteDSN mirrors platform/resources_sql.go's openDatabase: a
// SQLite file DSN's directory must exist before the driver can create the
// file itself. github.com/oarkflow/migrate connects directly, unlike
// platform's database.sql resource, so nothing else creates it first.
func mkdirForSQLiteDSN(dsn string) {
	path := strings.TrimPrefix(dsn, "file:")
	if idx := strings.Index(path, "?"); idx != -1 {
		path = path[:idx]
	}
	if path == "" || strings.HasPrefix(path, ":memory:") {
		return
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		_ = os.MkdirAll(dir, 0755)
	}
}
