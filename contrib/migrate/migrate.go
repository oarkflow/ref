// Package migrate provides reusable BCL migration and database seeding orchestration
// for Ref applications using github.com/oarkflow/migrate.
package migrate

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/oarkflow/migrate"
	"github.com/oarkflow/zlog"
)

// Config configures the database migration and seeding environment.
type Config struct {
	Driver       string
	DSN          string
	MigrationDir string
	SeedDir      string
	HistoryTable string
}

// Dialect returns the normalized database dialect for migrate.
func (c Config) Dialect() string {
	if c.Driver == "pgx" {
		return "postgres"
	}
	if c.Driver == "" {
		return "sqlite"
	}
	return c.Driver
}

// NewManager initializes a new migrate.Manager and HistoryDriver.
func NewManager(ctx context.Context, c Config) (*migrate.Manager, migrate.HistoryDriver, error) {
	dialect := c.Dialect()
	if dialect == "sqlite" {
		mkdirForSQLiteDSN(c.DSN)
	}
	dbDriver, err := migrate.NewDriver(ctx, dialect, c.DSN)
	if err != nil {
		return nil, nil, fmt.Errorf("connecting (%s driver, dialect %s): %w", c.Driver, dialect, err)
	}
	historyTable := c.HistoryTable
	if historyTable == "" {
		historyTable = "schema_migrations"
	}
	historyDriver, err := migrate.NewHistoryDriver(ctx, "db", dialect, c.DSN, historyTable)
	if err != nil {
		return nil, nil, fmt.Errorf("history driver: %w", err)
	}
	mgr := migrate.NewManager(
		migrate.WithDriver(dbDriver),
		migrate.WithHistoryDriver(historyDriver),
		migrate.WithDialect(dialect),
		migrate.WithMigrationDir(c.MigrationDir),
		migrate.WithSeedDir(c.SeedDir),
		migrate.WithContext(ctx),
	)
	return mgr, historyDriver, nil
}

// Pending returns all declared migration names that have not yet been applied.
func Pending(ctx context.Context, mgr *migrate.Manager, historyDriver migrate.HistoryDriver) ([]string, error) {
	declared, err := mgr.ListMigrationMap()
	if err != nil {
		return nil, fmt.Errorf("listing migrations: %w", err)
	}
	applied, err := historyDriver.Load(ctx)
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

// Apply executes all declared BCL migrations in deterministic filename order.
func Apply(ctx context.Context, mgr *migrate.Manager) error {
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
			if err := mgr.ApplyMigration(ctx, m); err != nil {
				return fmt.Errorf("applying %q: %w", m.Name, err)
			}
		}
	}
	return nil
}

// Seed discovers and executes all .bcl seed files.
func Seed(ctx context.Context, mgr *migrate.Manager) error {
	seeds, err := mgr.ListSeedFiles(false)
	if err != nil {
		return fmt.Errorf("listing seeds: %w", err)
	}
	if len(seeds) == 0 {
		entries, err := os.ReadDir(mgr.SeedDir())
		if err == nil {
			for _, e := range entries {
				if !e.IsDir() && strings.HasSuffix(e.Name(), ".bcl") {
					seeds = append(seeds, filepath.Join(mgr.SeedDir(), e.Name()))
				}
			}
		}
	}
	return mgr.RunSeeds(ctx, false, false, seeds...)
}

// EnsureMigrated verifies that all schema migrations are applied. If pending migrations
// exist, it applies them automatically when AUTO_MIGRATE=true or prompts on an interactive terminal.
func EnsureMigrated(ctx context.Context, c Config, logger *zlog.Logger) error {
	if fi, err := os.Stat(c.MigrationDir); err != nil || !fi.IsDir() {
		return fmt.Errorf("migration directory %q does not exist", c.MigrationDir)
	}

	mgr, historyDriver, err := NewManager(ctx, c)
	if err != nil {
		return fmt.Errorf("connecting to check migrations: %w", err)
	}

	pending, err := Pending(ctx, mgr, historyDriver)
	if err != nil {
		return fmt.Errorf("checking migrations: %w", err)
	}
	if len(pending) == 0 {
		return nil
	}

	names := strings.Join(pending, ", ")
	if logger != nil {
		logger.Warn("pending migrations", zlog.Int("count", len(pending)), zlog.String("names", names))
	}

	switch auto := os.Getenv("AUTO_MIGRATE"); {
	case auto == "true" || auto == "1":
		if logger != nil {
			logger.Info("AUTO_MIGRATE is set — applying migrations automatically")
		}
	case IsInteractiveTerminal():
		prompt := fmt.Sprintf("%d pending migration(s) (%s). Run them now? [y/N]: ", len(pending), names)
		if !PromptYesNo(prompt) {
			return fmt.Errorf("%d pending migration(s) not applied — set AUTO_MIGRATE=true or run migrator CLI", len(pending))
		}
	default:
		return fmt.Errorf("%d pending migration(s) (%s) — run migrator CLI or set AUTO_MIGRATE=true", len(pending), names)
	}

	if err := Apply(ctx, mgr); err != nil {
		return fmt.Errorf("applying migrations: %w", err)
	}
	if logger != nil {
		logger.Info("migrations applied successfully", zlog.Int("count", len(pending)))
	}
	return nil
}

// IsInteractiveTerminal returns true if stdin is an interactive character terminal.
func IsInteractiveTerminal() bool {
	stat, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return stat.Mode()&os.ModeCharDevice != 0
}

// PromptYesNo prompts the user via stderr and reads a yes/no response.
func PromptYesNo(prompt string) bool {
	fmt.Fprint(os.Stderr, prompt)
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	default:
		return false
	}
}

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
