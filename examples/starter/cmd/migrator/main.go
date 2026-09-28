// Command migrator applies the schema in ../../resources/migrations against
// whichever database DB_DRIVER/DB_DSN name — SQLite or PostgreSQL, from the
// same two declarative BCL environment variables cmd/server reads.
//
// This is a separate binary, run as a separate step
// (`go run ./cmd/migrator migrate`, then `go run ./cmd/server`), on purpose:
// schema changes are deliberate, reviewable, and rollback-able
// (`migration:rollback`, `status`, `history` — see
// github.com/oarkflow/migrate's own README for the full command list), not
// something that silently reruns on every server boot.
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
	"path/filepath"
	"strings"

	"github.com/oarkflow/migrate"

	"github.com/oarkflow/ref/examples/starter/internal/bootstrap"
)

func main() {
	if err := bootstrap.LoadDotenv(bootstrap.DotenvPath()); err != nil {
		log.Fatalf("migrator: loading %s: %v", bootstrap.DotenvPath(), err)
	}

	driver := env("DB_DRIVER", "sqlite")
	dsn := env("DB_DSN", "file:.data/starter/app.db?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	// cmd/server names the driver the way database/sql knows it ("pgx", the
	// name jackc/pgx/v5/stdlib registers); github.com/oarkflow/migrate names
	// dialects the way it groups drivers ("postgres" for any of pgx/pg/
	// postgresql). Translate once, here, rather than making every migration
	// file or the server's own BCL aware of the difference.
	dialect := driver
	if dialect == "pgx" {
		dialect = "postgres"
	}

	// platform.LoadDir's database.sql resource (cmd/server's path into the
	// same DSN) creates the SQLite file's parent directory before opening
	// it; this binary connects directly through migrate.NewDriver instead,
	// which does not, so a fresh checkout with no .data/ yet fails here
	// first, on the very first documented command, without this.
	if dialect == "sqlite" {
		mkdirForSQLiteDSN(dsn)
	}

	dbDriver, err := migrate.NewDriver(dialect, dsn)
	if err != nil {
		log.Fatalf("migrator: connecting (%s driver, dialect %s): %v", driver, dialect, err)
	}
	historyDriver, err := migrate.NewHistoryDriver("db", dialect, dsn, "schema_migrations")
	if err != nil {
		log.Fatalf("migrator: history driver: %v", err)
	}

	mgr := migrate.NewManager(
		migrate.WithDriver(dbDriver),
		migrate.WithHistoryDriver(historyDriver),
		migrate.WithDialect(dialect),
		migrate.WithMigrationDir(bootstrap.ResolveDir("examples/starter/resources/migrations", "./resources/migrations", "resources/migrations")),
		migrate.WithSeedDir(bootstrap.ResolveDir("examples/starter/resources/migrations/seeds", "./resources/migrations/seeds", "resources/migrations/seeds")),
	)

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

// mkdirForSQLiteDSN mirrors platform/resources_sql.go's openDatabase: a
// SQLite file DSN's directory must exist before the driver can create the
// file itself.
func mkdirForSQLiteDSN(dsn string) {
	path := strings.TrimPrefix(dsn, "file:")
	if idx := strings.Index(path, "?"); idx != -1 {
		path = path[:idx]
	}
	if path == "" || path == ":memory:" || strings.HasPrefix(path, ":memory:") {
		return
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		_ = os.MkdirAll(dir, 0755)
	}
}
