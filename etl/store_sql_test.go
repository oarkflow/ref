package etl

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

// The whole engine suite runs against every store: memory always, SQLite
// always, Postgres when TEST_POSTGRES_DSN is set (each test gets its own
// table prefix, so one database serves them all).
func init() {
	storeFactories["sqlite"] = func(t *testing.T) Store {
		db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "etl.db")+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
		if err != nil {
			t.Fatal(err)
		}
		db.SetMaxOpenConns(1)
		t.Cleanup(func() { db.Close() })
		return migrated(t, db, "sqlite", "t_")
	}
	if dsn := os.Getenv("TEST_MYSQL_DSN"); dsn != "" {
		n := 0
		storeFactories["mysql"] = func(t *testing.T) Store {
			db, err := sql.Open("mysql", dsn)
			if err != nil {
				t.Fatal(err)
			}
			n++
			prefix := "etl_m" + itoa(os.Getpid()) + "_" + itoa(n) + "_"
			t.Cleanup(func() {
				for _, tb := range []string{"sources", "roles", "batches", "checkpoints", "quarantine", "lineage", "audit", "audit_head", "counters", "breakers", "alerts"} {
					_, _ = db.Exec("DROP TABLE IF EXISTS " + prefix + tb)
				}
				db.Close()
			})
			return migrated(t, db, "mysql", prefix)
		}
	}
	if dsn := os.Getenv("TEST_POSTGRES_DSN"); dsn != "" {
		n := 0
		storeFactories["postgres"] = func(t *testing.T) Store {
			db, err := sql.Open("pgx", dsn)
			if err != nil {
				t.Fatal(err)
			}
			n++
			prefix := "etl_t" + itoa(os.Getpid()) + "_" + itoa(n) + "_"
			t.Cleanup(func() {
				for _, tb := range []string{"sources", "roles", "batches", "checkpoints", "quarantine", "lineage", "audit", "audit_head", "counters", "breakers", "alerts"} {
					_, _ = db.Exec("DROP TABLE IF EXISTS " + prefix + tb)
				}
				db.Close()
			})
			return migrated(t, db, "postgres", prefix)
		}
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

func migrated(t *testing.T, db *sql.DB, dialect, prefix string) Store {
	t.Helper()
	s, err := NewSQLStore(db, dialect, prefix)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(context.Background()); err != nil { // idempotent
		t.Fatal(err)
	}
	return s
}

func TestSQLAuditChainDetectsTampering(t *testing.T) {
	db, _ := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "a.db"))
	db.SetMaxOpenConns(1)
	defer db.Close()
	st := migrated(t, db, "sqlite", "x_")
	e, _ := testEngine(t, st)
	ctx := context.Background()
	e.PutSource(ctx, admin, ordersSource())
	res, _ := e.Ingest(ctx, admin, "orders", "k", rows(t, goodCSV))
	e.RunAll(ctx, admin, res.Batch.ID)
	if bad, total, _ := e.VerifyAudit(ctx, admin); bad != 0 || total < 3 {
		t.Fatalf("bad=%d total=%d", bad, total)
	}
	if _, err := db.Exec(`UPDATE x_audit SET doc = replace(doc, 'role.create', 'role.nothing') WHERE seq = 2`); err == nil {
		t.Fatal("the database let an audit entry be changed")
	}
	if _, err := db.Exec(`DELETE FROM x_audit WHERE seq = 2`); err == nil {
		t.Fatal("the database let an audit entry be deleted")
	}
	// Someone with the power to drop the guard can still be caught by the chain.
	if _, err := db.Exec(`DROP TRIGGER x_audit_no_update`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE x_audit SET doc = replace(doc, 'role.create', 'role.nothing') WHERE seq = 2`); err != nil {
		t.Fatal(err)
	}
	if bad, _, _ := e.VerifyAudit(ctx, admin); bad != 2 {
		t.Fatalf("expected tampering at 2, got %d", bad)
	}
}
