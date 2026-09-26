package platform

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
)

// The MySQL variants of the multi-dialect journeys. They run when
// TEST_MYSQL_DSN names a MySQL-compatible server, e.g.
//
//	TEST_MYSQL_DSN='ref:ref@tcp(127.0.0.1:53306)/?parseTime=true'
//
// and each test gets a fresh database, created and dropped around it.

// freshMySQL creates an empty database on the server adminDSN points at and
// returns its DSN; the database is dropped when the test ends.
func freshMySQL(t *testing.T, adminDSN string) string {
	t.Helper()
	cfg, err := mysql.ParseDSN(adminDSN)
	if err != nil {
		t.Fatal(err)
	}
	cfg.DBName = ""
	admin, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("reftest_%d", time.Now().UnixNano())
	if _, err := admin.Exec("CREATE DATABASE " + name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec("DROP DATABASE IF EXISTS " + name)
		admin.Close()
	})
	cfg.DBName = name
	return cfg.FormatDSN()
}

var dollarParam = regexp.MustCompile(`\$[0-9]+`)

// appSQL rewrites the hand-written SQL in a test app for driver. Statements in
// database.* actions and migrations are passed to the driver verbatim, so an
// app targeting MySQL writes MySQL: ? placeholders, bounded keys, and no
// ON CONFLICT.
func appSQL(driver, src string) string {
	if driver != "mysql" {
		return src
	}
	src = dollarParam.ReplaceAllString(src, "?")
	return strings.NewReplacer(
		"TEXT PRIMARY KEY", "VARCHAR(191) PRIMARY KEY",
		"INSERT INTO synced", "INSERT IGNORE INTO synced",
		"WHERE EXISTS", "FROM DUAL WHERE EXISTS",
		" ON CONFLICT (event_id) DO NOTHING", "",
	).Replace(src)
}

func mysqlDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("TEST_MYSQL_DSN not set")
	}
	return freshMySQL(t, dsn)
}

func TestEntitiesMySQL(t *testing.T) {
	runEntities(t, "mysql", mysqlDSN(t))
}

// TestEntitiesMySQLRestart starts the entity app twice against one database:
// the second startup re-runs every migration over existing tables and indexes.
func TestEntitiesMySQLRestart(t *testing.T) {
	dsn := mysqlDSN(t)
	runEntities(t, "mysql", dsn)
	path := filepath.Join(t.TempDir(), "app.bcl")
	if err := os.WriteFile(path, []byte(appSQL("mysql", entityApp)), 0o600); err != nil {
		t.Fatal(err)
	}
	h := newAppHarness(t, path, map[string]string{
		"ENT_DRIVER":     "mysql",
		"ENT_DSN":        dsn,
		"ENT_JWT_SECRET": "entity-test-secret-0123456789abcdef-xyz",
	})
	staff := h.token("jwt", "u1", []string{"staff"}, map[string]any{"tenant_id": "acme"})
	if status, body := h.call("GET", "/api/projects", staff, nil); status != 200 {
		t.Fatalf("list after restart: %d %v", status, body)
	}
}

func TestEntityDurableHooksMySQL(t *testing.T) {
	runEntityDurableHooks(t, "mysql", mysqlDSN(t))
}
