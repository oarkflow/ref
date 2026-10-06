package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/oarkflow/zlog"

	"github.com/oarkflow/ref/examples/medical-coding-platform/internal/bootstrap"
	"github.com/oarkflow/ref/examples/medical-coding-platform/internal/migrations"
)

func ensureMigrated(ctx context.Context, logger *zlog.Logger) error {
	driver := env("DB_DRIVER", "sqlite")
	dsn := env("DB_DSN", "file:.data/clear/app.db?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	migrationDir, seedDir := migrationDirs()

	if fi, err := os.Stat(migrationDir); err != nil || !fi.IsDir() {
		return fmt.Errorf("migration directory %q does not exist (MIGRATIONS_DIR to override)", migrationDir)
	}

	mgr, historyDriver, err := migrations.NewManager(ctx, migrations.Config{
		Driver:       driver,
		DSN:          dsn,
		MigrationDir: migrationDir,
		SeedDir:      seedDir,
	})
	if err != nil {
		return fmt.Errorf("connecting to check migrations: %w", err)
	}

	pending, err := migrations.Pending(ctx, mgr, historyDriver)
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
		logger.Info("AUTO_MIGRATE is set — applying migrations automatically")
	case isInteractiveTerminal():
		prompt := fmt.Sprintf("%d pending migration(s) (%s). Run them now? [y/N]: ", len(pending), names)
		if !promptYesNo(prompt) {
			return fmt.Errorf("%d pending migration(s) not applied — set AUTO_MIGRATE=true or run migrator CLI", len(pending))
		}
	default:
		return fmt.Errorf("%d pending migration(s) (%s) — run migrator CLI or set AUTO_MIGRATE=true", len(pending), names)
	}

	if err := migrations.Apply(ctx, mgr); err != nil {
		return fmt.Errorf("applying migrations: %w", err)
	}
	logger.Info("migrations applied successfully", zlog.Int("count", len(pending)))
	return nil
}

var isInteractiveTerminal = func() bool {
	stat, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return stat.Mode()&os.ModeCharDevice != 0
}

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

func migrationDirs() (migrationDir, seedDir string) {
	migrationDir = env("MIGRATIONS_DIR", bootstrap.ResolveDir("examples/medical-coding-platform/resources/migrations", "./resources/migrations", "resources/migrations"))
	seedDir = env("MIGRATIONS_SEED_DIR", bootstrap.ResolveDir("examples/medical-coding-platform/resources/migrations/seeds", "./resources/migrations/seeds", "resources/migrations/seeds"))
	return migrationDir, seedDir
}
