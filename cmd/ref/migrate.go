package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/oarkflow/ref/config"
	"github.com/oarkflow/ref/contrib/migrate"
)

// runMigration keeps schema management in the generic REF CLI, so application
// directories only need migration and seed files.
func runMigration(ctx context.Context, command string, args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: ref %s <application directory>", command)
	}
	appDir, err := filepath.Abs(args[0])
	if err != nil {
		return err
	}
	if info, err := os.Stat(appDir); err != nil || !info.IsDir() {
		return fmt.Errorf("application directory %q does not exist", appDir)
	}
	for _, name := range []string{".env", ".env.example"} {
		path := filepath.Join(appDir, name)
		if _, err := os.Stat(path); err == nil {
			if err := config.LoadDotenv(path); err != nil {
				return err
			}
			break
		}
	}
	if err := os.Chdir(appDir); err != nil {
		return err
	}
	resources := filepath.Join(appDir, "resources")
	if _, err := os.Stat(resources); err != nil {
		resources = appDir
	}
	migrations := filepath.Join(resources, "migrations")
	if _, err := os.Stat(migrations); err != nil {
		return fmt.Errorf("no migrations directory in %q", resources)
	}
	mgr, _, err := migrate.NewManager(ctx, migrate.Config{
		Driver:       config.Env("DB_DRIVER", "sqlite"),
		DSN:          config.Env("DB_DSN", "file:.data/app.db"),
		MigrationDir: migrations,
		SeedDir:      filepath.Join(migrations, "seeds"),
	})
	if err != nil {
		return err
	}
	if command == "seed" {
		return migrate.Seed(ctx, mgr)
	}
	return migrate.Apply(ctx, mgr)
}
