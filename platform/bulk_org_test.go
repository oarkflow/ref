package platform

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const bulkApp = `
name "bulk"

resource "db" {
  kind "database.sql"
  config {
    driver "sqlite"
    dsn env.required("BULK_DSN")
    migrations [
      "CREATE TABLE orders (id INTEGER PRIMARY KEY AUTOINCREMENT, tenant_id TEXT NOT NULL, customer TEXT NOT NULL)",
      "CREATE TABLE items (id INTEGER PRIMARY KEY AUTOINCREMENT, tenant_id TEXT NOT NULL, order_id INTEGER NOT NULL, sku TEXT NOT NULL, qty INTEGER NOT NULL CHECK (qty > 0))",
      "CREATE TABLE tickets (id INTEGER PRIMARY KEY AUTOINCREMENT, unit TEXT NOT NULL, title TEXT NOT NULL)"
    ]
  }
}

resource "jwt" {
  kind "auth.jwt"
  config { secret env.required("BULK_SECRET") }
}

resource "org" {
  kind "org.hierarchy"
  config {
    global_roles ["admin"]
    nodes [
      { id "hq" name "HQ" },
      { id "east" parent "hq" name "East" },
      { id "west" parent "hq" name "West" },
      { id "east-1" parent "east" name "East 1" }
    ]
  }
}

intent "orders.create" {
  response "saved"
  node "saved" {
    uses "database.insert_many"
    resource "db"
    kind effect
    requires [input]
    provides [saved]
    config {
      parent {
        table "orders"
        values { customer "input.customer" }
        link "order_id"
      }
      table "items"
      columns ["sku", "qty"]
      rows "input.items"
      tenant_column "tenant_id"
    }
  }
}

intent "orders.count" {
  response "counts"
  node "counts" {
    uses "database.query"
    resource "db"
    provides [counts]
    config { statement "SELECT (SELECT COUNT(*) FROM orders) AS orders, (SELECT COUNT(*) FROM items) AS items" }
  }
}

intent "tickets.create" {
  response "ticket"
  node "ticket" {
    uses "database.crud"
    resource "db"
    kind effect
    requires [input]
    provides [ticket]
    config {
      operation "create"
      table "tickets"
      columns ["id", "unit", "title"]
      org_resource "org"
      org_column "unit"
    }
  }
}

intent "tickets.list" {
  response "tickets"
  node "tickets" {
    uses "database.crud"
    resource "db"
    provides [tickets]
    config {
      operation "list"
      table "tickets"
      columns ["id", "unit", "title"]
      order_by "id"
      org_resource "org"
      org_column "unit"
    }
  }
}

route "orders.create" { method POST path "/orders" intent "orders.create" auth "jwt" tenant { required true } }
route "orders.count" { method GET path "/orders/count" intent "orders.count" auth "jwt" tenant { required true } }
route "tickets.create" { method POST path "/tickets" intent "tickets.create" auth "jwt" }
route "tickets.list" { method GET path "/tickets" intent "tickets.list" auth "jwt" }
`

func TestInsertManyParentIsAtomicAndChunked(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.bcl")
	if err := os.WriteFile(path, []byte(bulkApp), 0o600); err != nil {
		t.Fatal(err)
	}
	h := newAppHarness(t, path, map[string]string{
		"BULK_DSN":    "file:" + filepath.Join(dir, "bulk.db"),
		"BULK_SECRET": strings.Repeat("b", 40),
	})
	tok := h.token("jwt", "u1", []string{"admin"}, map[string]any{"tenant_id": "t1"})

	// 700 items x 4 columns = 2800 bind parameters, well past SQLite's 999, so
	// the insert must be split into several statements inside one transaction.
	items := make([]any, 700)
	for i := range items {
		items[i] = map[string]any{"sku": "sku-" + Stringify(i), "qty": 1}
	}
	status, body := h.call("POST", "/orders", tok, map[string]any{"customer": "Ram", "items": items})
	if status != 200 || Stringify(dig(body, "inserted")) != "700" || dig(body, "parent", "id") == nil {
		t.Fatalf("bulk create = %d %v", status, body)
	}

	// The last item violates CHECK (qty > 0): the whole order, header included,
	// must roll back.
	bad := []any{map[string]any{"sku": "a", "qty": 2}, map[string]any{"sku": "b", "qty": 0}}
	if status, _ := h.call("POST", "/orders", tok, map[string]any{"customer": "Sita", "items": bad}); status < 400 {
		t.Fatalf("constraint violation should fail, got %d", status)
	}
	_, counts := h.call("GET", "/orders/count", tok, nil)
	if Stringify(dig(counts, 0, "orders")) != "1" || Stringify(dig(counts, 0, "items")) != "700" {
		t.Fatalf("rollback left partial rows: %v", counts)
	}
}

func TestCrudOrgScopingWithoutPathColumn(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.bcl")
	if err := os.WriteFile(path, []byte(bulkApp), 0o600); err != nil {
		t.Fatal(err)
	}
	h := newAppHarness(t, path, map[string]string{
		"BULK_DSN":    "file:" + filepath.Join(dir, "bulk.db"),
		"BULK_SECRET": strings.Repeat("b", 40),
	})
	admin := h.token("jwt", "a", []string{"admin"}, nil)
	east := h.token("jwt", "e", []string{"staff"}, map[string]any{"org_units": "east"})
	west := h.token("jwt", "w", []string{"staff"}, map[string]any{"org_units": []any{"west"}})

	for _, tc := range []struct {
		token, unit string
		want        int
	}{
		{admin, "hq", 200}, {east, "east-1", 200}, {east, "east", 200}, {west, "west", 200},
		{east, "west", 403}, {west, "east-1", 403}, {east, "hq", 403}, {east, "", 422},
	} {
		body := map[string]any{"title": "t", "unit": tc.unit}
		if status, resp := h.call("POST", "/tickets", tc.token, body); status != tc.want {
			t.Fatalf("create in %q = %d %v, want %d", tc.unit, status, resp, tc.want)
		}
	}
	count := func(token string) int {
		_, body := h.call("GET", "/tickets", token, nil)
		list, _ := body.([]any)
		return len(list)
	}
	if got := count(east); got != 2 {
		t.Fatalf("east sees %d tickets, want 2", got)
	}
	if got := count(west); got != 1 {
		t.Fatalf("west sees %d, want 1", got)
	}
	if got := count(admin); got != 4 {
		t.Fatalf("admin sees %d, want 4", got)
	}
}
