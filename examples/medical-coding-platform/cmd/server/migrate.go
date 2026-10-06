package main

import (
	"context"

	"github.com/oarkflow/ref/config"
	"github.com/oarkflow/ref/contrib/migrate"
	"github.com/oarkflow/zlog"
)

func ensureMigrated(ctx context.Context, logger *zlog.Logger) error {
	driver := config.Env("DB_DRIVER", "sqlite")
	dsn := config.Env("DB_DSN", "file:.data/clear/app.db?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	migrationDir := config.Env("MIGRATIONS_DIR", config.ResolveDir("examples/medical-coding-platform/resources/migrations", "./resources/migrations", "resources/migrations"))
	seedDir := config.Env("MIGRATIONS_SEED_DIR", config.ResolveDir("examples/medical-coding-platform/resources/migrations/seeds", "./resources/migrations/seeds", "resources/migrations/seeds"))

	return migrate.EnsureMigrated(ctx, migrate.Config{
		Driver:       driver,
		DSN:          dsn,
		MigrationDir: migrationDir,
		SeedDir:      seedDir,
	}, logger)
}
