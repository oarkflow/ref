// Command migrator applies the schema in ../../resources/migrations against
// whichever database DB_DRIVER/DB_DSN name — SQLite or PostgreSQL, from the
// same two declarative BCL environment variables cmd/server reads.
//
// `go run ./cmd/server` also checks for pending migrations on every boot
// and offers to apply them (see cmd/server/main.go's ensureMigrated) — that
// closes the "fresh checkout, forgot to migrate, server just fails" gap
// this being a *separate* step used to leave open. This binary is still the
// one to reach for deliberately: rollback, status, history, force,
// validate — see github.com/oarkflow/migrate's own README for the full
// command list — none of which cmd/server's boot-time check attempts.
//
// github.com/oarkflow/migrate's migrations are themselves BCL
// (resources/migrations/*.bcl): one declarative schema per file, compiled
// to dialect-correct SQL by the tool itself (SQLite's rowid-based
// auto-increment vs PostgreSQL's SERIAL, TEXT vs VARCHAR sizing, ...) — the
// same problem resources/config/01_resources.bcl used to solve by hand
// with a ternary over two full copies of every CREATE TABLE statement,
// which is what this replaces.
package main

import (
	"log"
	"os"

	"github.com/oarkflow/ref/examples/starter/internal/bootstrap"
	"github.com/oarkflow/ref/examples/starter/internal/migrations"
)

func main() {
	if err := bootstrap.LoadDotenv(bootstrap.DotenvPath()); err != nil {
		log.Fatalf("migrator: loading %s: %v", bootstrap.DotenvPath(), err)
	}

	mgr, _, err := migrations.NewManager(migrations.Config{
		Driver:       env("DB_DRIVER", "sqlite"),
		DSN:          env("DB_DSN", "file:.data/starter/app.db?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"),
		MigrationDir: bootstrap.ResolveDir("examples/starter/resources/migrations", "./resources/migrations", "resources/migrations"),
		SeedDir:      bootstrap.ResolveDir("examples/starter/resources/migrations/seeds", "./resources/migrations/seeds", "resources/migrations/seeds"),
	})
	if err != nil {
		log.Fatalf("migrator: %v", err)
	}

	// Delegates to github.com/oarkflow/migrate's own CLI dispatch over
	// os.Args — `migrate`, `migration:rollback --step=1`, `status`,
	// `migration:validate`, `history`, ... see its README for the full list.
	mgr.Run()
}

func env(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}
