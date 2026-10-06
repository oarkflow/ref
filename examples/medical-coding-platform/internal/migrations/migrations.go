package migrations

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/oarkflow/migrate"
)

type Config struct {
	Driver       string
	DSN          string
	MigrationDir string
	SeedDir      string
	HistoryTable string
}

func (c Config) Dialect() string {
	if c.Driver == "pgx" {
		return "postgres"
	}
	return c.Driver
}

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
