package platform

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// entityDataApp exercises the entity search index, bulk operations and
// analytics.
const entityDataApp = `
name "entity-data"

resource "db" {
  kind "database.sql"
  config {
    driver env("ED_DRIVER", "sqlite")
    dsn env("ED_DSN")
    migrations ["CREATE TABLE IF NOT EXISTS hook_log (event TEXT, name TEXT)"]
  }
}

resource "jwt" {
  kind "auth.jwt"
  config {
    secret env.required("ED_JWT_SECRET")
  }
}

entity "place" {
  database "db"
  path "/api/places"
  auth "jwt"
  tenant_scoped true
  soft_delete true
  versioned true
  search ["name", "city"]
  search_index true
  column "name" {
    kind text
    required true
  }
  column "city" {
    kind text
  }
  column "code" {
    kind text
    unique true
  }
  allow "*" {
    roles ["staff"]
  }
  allow "delete" {
    roles ["staff"]
    condition "record.city != 'Locked'"
  }
}

intent "reindex" {
  response "r"
  node "r" {
    uses "entity.reindex"
    resource "db"
    kind effect
    provides [r]
    config {
      entity "place"
      roles ["ops"]
    }
  }
}

route "reindex" { method POST path "/ops/places/reindex" intent "reindex" auth "jwt" }
`

func TestEntityData(t *testing.T) {
	dir := t.TempDir()
	runEntityData(t, "sqlite", "file:"+filepath.Join(dir, "d.db")+"?_pragma=busy_timeout(5000)")
}

func TestEntityDataPostgres(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN not set")
	}
	runEntityData(t, "pgx", freshPostgres(t, dsn))
}

func entityDataHarness(t *testing.T, driver, dsn, app string) *appHarness {
	path := filepath.Join(t.TempDir(), "app.bcl")
	if err := os.WriteFile(path, []byte(app), 0o600); err != nil {
		t.Fatal(err)
	}
	return newAppHarness(t, path, map[string]string{
		"ED_DRIVER": driver, "ED_DSN": dsn, "ED_JWT_SECRET": "entity-data-secret-0123456789abcdef-xyz",
	})
}

func runEntityData(t *testing.T, driver, dsn string) {
	// Records written before the index existed are backfilled at startup.
	plain := strings.Replace(entityDataApp[:strings.Index(entityDataApp, `intent "reindex"`)], "search_index true", "search_index false", 1)
	before := entityDataHarness(t, driver, dsn, plain)
	staff := before.token("jwt", "u1", []string{"staff"}, map[string]any{"tenant_id": "acme"})
	for _, rec := range []map[string]any{
		{"name": "Bridge Repair", "city": "Zürich", "code": "P-1"},
		{"name": "Straße cleanup", "city": "Köln", "code": "P-2"},
	} {
		if status, body := before.call("POST", "/api/places", staff, rec); status != 201 {
			t.Fatalf("create before the index: %d %v", status, body)
		}
	}
	_ = before.platform.Close()

	h := entityDataHarness(t, driver, dsn, entityDataApp)
	staff = h.token("jwt", "u1", []string{"staff"}, map[string]any{"tenant_id": "acme"})
	other := h.token("jwt", "u9", []string{"staff"}, map[string]any{"tenant_id": "globex"})
	ops := h.token("jwt", "op", []string{"ops"}, map[string]any{"tenant_id": "acme"})
	search := func(tok, q string) float64 {
		t.Helper()
		status, body := h.call("GET", "/api/places?q="+strings.ReplaceAll(q, " ", "+"), tok, nil)
		if status != 200 {
			t.Fatalf("search %q: %d %v", q, status, body)
		}
		n, _ := dig(body, "total").(float64)
		return n
	}
	waitFor(t, "search backfill", func() bool { return search(staff, "zurich") == 1 })

	status, body := h.call("POST", "/api/places", staff, map[string]any{"name": "Road survey", "city": "Bern", "code": "P-3"})
	if status != 201 {
		t.Fatalf("create: %d %v", status, body)
	}
	road := fmt.Sprint(dig(body, "id"))
	h.call("POST", "/api/places", other, map[string]any{"name": "Bridge elsewhere", "code": "P-9"})

	// Every query word must prefix a word of the row; case and accents fold.
	for q, want := range map[string]float64{
		"bri": 1, "bri rep": 1, "rep zur": 1, "ZÜR": 1, "strasse": 1, "koln": 1, "ridge": 0, "bri bern": 0,
		"road bern": 1, "": 3, "!!": 3,
	} {
		if got := search(staff, q); got != want {
			t.Errorf("search %q: %v, want %v", q, got, want)
		}
	}
	if search(other, "bridge") != 1 {
		t.Fatal("the index crossed tenants")
	}

	// A change that does not commit writes no tokens.
	db := entityDataDB(t, h)
	tokens := entityTokenCount(t, db)
	if status, _ := h.call("POST", "/api/places", staff, map[string]any{"name": "Duplicate zebra", "code": "P-1"}); status != 409 {
		t.Fatalf("duplicate: %d", status)
	}
	if entityTokenCount(t, db) != tokens || search(staff, "zebra") != 0 {
		t.Fatal("a rolled-back create left tokens behind")
	}

	// Updates replace the tokens; deletes drop them.
	status, body = h.call("PATCH", "/api/places/"+road, staff, map[string]any{"name": "Tunnel survey", "version": 1})
	if status != 200 {
		t.Fatalf("update: %d %v", status, body)
	}
	if search(staff, "road") != 0 || search(staff, "tunnel") != 1 {
		t.Fatal("the update did not reindex")
	}
	if status, _ := h.call("DELETE", "/api/places/"+road, staff, nil); status != 200 {
		t.Fatalf("delete: %d", status)
	}
	if search(staff, "tunnel") != 0 {
		t.Fatal("the delete left the record searchable")
	}

	// A rebuild restores a damaged index.
	if _, err := db.ExecContext(context.Background(), "DELETE FROM place_search"); err != nil {
		t.Fatal(err)
	}
	if search(staff, "bridge") != 0 {
		t.Fatal("the index was not emptied")
	}
	if status, _ := h.call("POST", "/ops/places/reindex", staff, map[string]any{}); status != 403 {
		t.Fatalf("reindex without the role: %d", status)
	}
	status, body = h.call("POST", "/ops/places/reindex", ops, map[string]any{})
	if status != 200 || dig(body, "indexed") != float64(3) { // two acme rows and globex's; the deleted one is skipped
		t.Fatalf("reindex: %d %v", status, body)
	}
	if search(staff, "bridge") != 1 || search(staff, "tunnel") != 0 {
		t.Fatal("the rebuild is wrong")
	}
}

func entityDataDB(t *testing.T, h *appHarness) *Database {
	t.Helper()
	res, ok := h.platform.Resource("db")
	if !ok {
		t.Fatal("no db resource")
	}
	return res.(*Database)
}

func entityTokenCount(t *testing.T, db *Database) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM place_search").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestSearchTokens(t *testing.T) {
	got := searchTokens("Bridge-Repair: Zürich STRASSE Straße ﬁle café 42b bridge", 100)
	want := []string{"bridge", "repair", "zurich", "strasse", "file", "cafe", "42b"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("tokens: %v", got)
	}
	if n := len(searchTokens("a b c d e f g h i j", 3)); n != 3 {
		t.Fatalf("limit: %d", n)
	}
	if tok := searchTokens(strings.Repeat("x", 100), 1)[0]; len(tok) != searchTokenRunes {
		t.Fatalf("long word: %d", len(tok))
	}
}
