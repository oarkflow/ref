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
  bulk true
  bulk_max 6
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
  on "created" {
    hook "log.hook"
    durable true
  }
}

entity "sale" {
  database "db"
  auth "jwt"
  tenant_scoped true
  analytics true
  bulk true
  column "region" {
    kind text
  }
  column "channel" {
    kind text
  }
  column "qty" {
    kind integer
  }
  column "amount" {
    kind decimal
  }
  column "price" {
    kind number
  }
  column "sold_on" {
    kind date
  }
  column "margin" {
    kind number
    hidden true
  }
  allow "*" {
    roles ["staff"]
  }
  allow "analytics" {
    roles ["staff", "analyst"]
  }
}

intent "log.hook" {
  response "n"
  node "n" {
    uses "database.exec"
    resource "db"
    kind effect
    requires [input]
    provides [n]
    config {
      statement "INSERT INTO hook_log (event, name) VALUES ($1, $2)"
      args ["input.event", "input.record.name"]
    }
  }
}

intent "log.list" {
  response "rows"
  node "rows" {
    uses "database.query"
    resource "db"
    provides [rows]
    config {
      statement "SELECT event, name FROM hook_log"
    }
  }
}

route "log" { method GET path "/api/log" intent "log.list" auth "jwt" }

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
	// Closing mid-delivery would leave a hook leased to the closed platform.
	waitFor(t, "hooks before the index", func() bool {
		_, body := before.call("GET", "/api/log", staff, nil)
		rows, _ := body.([]any)
		return len(rows) == 2
	})
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

	entityBulk(t, h, db, search)
	entityAnalytics(t, h)
}

func entityAnalytics(t *testing.T, h *appHarness) {
	staff := h.token("jwt", "u1", []string{"staff"}, map[string]any{"tenant_id": "acme"})
	analyst := h.token("jwt", "a1", []string{"analyst"}, map[string]any{"tenant_id": "acme"})
	other := h.token("jwt", "u9", []string{"staff"}, map[string]any{"tenant_id": "globex"})
	status, body := h.call("POST", "/api/sales/-/bulk", staff, map[string]any{"create": []any{
		map[string]any{"region": "north", "channel": "web", "qty": 1, "amount": "0.10", "price": 1.5, "sold_on": "2026-09-21"},
		map[string]any{"region": "north", "channel": "shop", "qty": 2, "amount": "0.20", "price": 2.5, "sold_on": "2026-09-23"},
		map[string]any{"region": "south", "channel": "web", "qty": 3, "amount": "0.05", "price": 3.5, "sold_on": "2026-09-27"},
		map[string]any{"region": "south", "channel": "web", "qty": 4, "amount": "1.00", "price": 4.5, "sold_on": "2026-09-28"},
		map[string]any{"region": "north", "channel": "web", "qty": 5, "price": 5.5, "sold_on": "2026-10-02"},
	}})
	if status != 200 || dig(body, "created") != float64(5) {
		t.Fatalf("seed sales: %d %v", status, body)
	}
	get := func(tok, query string) map[string]any {
		t.Helper()
		status, body := h.call("GET", "/api/sales/-/analytics?"+query, tok, nil)
		if status != 200 {
			t.Fatalf("analytics %s: %d %v", query, status, body)
		}
		return body.(map[string]any)
	}
	summary := func(body map[string]any) string {
		var parts []string
		for _, r := range body["rows"].([]any) {
			row := r.(map[string]any)
			parts = append(parts, fmt.Sprintf("%v %v %v", row["bucket"], row["group"], row["values"]))
		}
		return strings.Join(parts, " | ")
	}

	// Several metrics at once; decimals stay exact.
	body = get(analyst, "metrics=count,sum:amount,avg:amount,count:amount,min:qty,max:qty,sum:qty,avg:qty,avg:price,max:price")
	want := map[string]any{"count": float64(5), "sum_amount": "1.35", "avg_amount": "0.34", "count_amount": float64(4),
		"min_qty": float64(1), "max_qty": float64(5), "sum_qty": float64(15), "avg_qty": float64(3), "avg_price": 3.5, "max_price": 5.5}
	values, _ := dig(body, "rows", 0, "values").(map[string]any)
	for k, v := range want {
		if values[k] != v {
			t.Errorf("%s = %v (%T), want %v", k, values[k], values[k], v)
		}
	}

	for query, want := range map[string]string{
		"group_by=region&metrics=count,sum:amount":                    "<nil> map[region:north] map[count:3 sum_amount:0.30] | <nil> map[region:south] map[count:2 sum_amount:1.05]",
		"group_by=region,channel":                                     "<nil> map[channel:shop region:north] map[count:1] | <nil> map[channel:web region:north] map[count:2] | <nil> map[channel:web region:south] map[count:2]",
		"bucket=week&bucket_field=sold_on&metrics=count,sum:qty":      "2026-09-21 <nil> map[count:3 sum_qty:6] | 2026-09-28 <nil> map[count:2 sum_qty:9]",
		"bucket=month&bucket_field=sold_on&group_by=region":           "2026-09 map[region:north] map[count:2] | 2026-09 map[region:south] map[count:2] | 2026-10 map[region:north] map[count:1]",
		"bucket=day&bucket_field=sold_on&region=north":                "2026-09-21 <nil> map[count:1] | 2026-09-23 <nil> map[count:1] | 2026-10-02 <nil> map[count:1]",
		"bucket=week&bucket_field=sold_on&group_by=region&qty__gte=3": "2026-09-21 map[region:south] map[count:1] | 2026-09-28 map[region:north] map[count:1] | 2026-09-28 map[region:south] map[count:1]",
	} {
		if got := summary(get(staff, query)); got != want {
			t.Errorf("%s:\n got %s\nwant %s", query, got, want)
		}
	}
	if body := get(staff, "bucket=day"); len(body["rows"].([]any)) != 1 || dig(body, "rows", 0, "values", "count") != float64(5) {
		t.Errorf("bucket by created_at: %v", body)
	}
	if body := get(other, ""); dig(body, "rows", 0, "values", "count") != float64(0) {
		t.Errorf("cross-tenant analytics: %v", body)
	}
	for _, query := range []string{
		"metrics=sum:region", "metrics=median:qty", "metrics=sum", "group_by=region,channel,qty", "group_by=margin",
		"bucket=year", "bucket=day&bucket_field=region", "bucket_field=sold_on", "metrics=sum:margin", "colour=red",
	} {
		if status, body := h.call("GET", "/api/sales/-/analytics?"+query, staff, nil); status != 422 {
			t.Errorf("bad analytics %s: %d %v", query, status, body)
		}
	}
	viewer := h.token("jwt", "v1", []string{"viewer"}, map[string]any{"tenant_id": "acme"})
	if status, _ := h.call("GET", "/api/sales/-/analytics", viewer, nil); status != 403 {
		t.Errorf("analytics without the role: %d", status)
	}
}

// entityBulk runs the bulk journey against the places runEntityData left:
// "Bridge Repair" and "Straße cleanup" for acme.
func entityBulk(t *testing.T, h *appHarness, db *Database, search func(tok, q string) float64) {
	staff := h.token("jwt", "u1", []string{"staff"}, map[string]any{"tenant_id": "acme"})
	other := h.token("jwt", "u9", []string{"staff"}, map[string]any{"tenant_id": "globex"})
	viewer := h.token("jwt", "v1", []string{"viewer"}, map[string]any{"tenant_id": "acme"})
	hooks := func() int {
		_, body := h.call("GET", "/api/log", staff, nil)
		rows, _ := body.([]any)
		return len(rows)
	}
	pending := func() int {
		var n int
		if err := db.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM ref_entity_events").Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	waitFor(t, "earlier hooks", func() bool { return hooks() == 4 && pending() == 0 })
	one := func(q string) map[string]any {
		t.Helper()
		_, body := h.call("GET", "/api/places?q="+q, staff, nil)
		rec, _ := dig(body, "items", 0).(map[string]any)
		if rec == nil {
			t.Fatalf("no place matches %q: %v", q, body)
		}
		return rec
	}
	bridge, strasse := one("bridge"), one("strasse")
	bulk := func(tok string, req map[string]any) (int, any) {
		t.Helper()
		return h.call("POST", "/api/places/-/bulk", tok, req)
	}

	// Atomic success: creates, updates and deletes in one transaction, with
	// their tokens and durable hooks.
	status, body := bulk(staff, map[string]any{
		"create": []any{map[string]any{"name": "Alpha pier", "code": "B-1"}, map[string]any{"name": "Beta pier", "code": "B-2"}},
		"update": []any{map[string]any{"id": strasse["id"], "version": strasse["version"], "city": "Bonn"}},
		"delete": []any{bridge["id"]},
	})
	if status != 200 || dig(body, "created") != float64(2) || dig(body, "updated") != float64(1) || dig(body, "deleted") != float64(1) ||
		dig(body, "failed") != float64(0) || dig(body, "results", 0, "record", "name") != "Alpha pier" || dig(body, "results", 2, "record", "city") != "Bonn" {
		t.Fatalf("bulk: %d %v", status, body)
	}
	if search(staff, "pier") != 2 || search(staff, "bonn") != 1 || search(staff, "bridge") != 0 {
		t.Fatal("bulk changes are not indexed")
	}
	waitFor(t, "bulk hooks", func() bool { return hooks() == 6 && pending() == 0 })

	// Every invalid item is reported, and nothing is applied.
	status, body = bulk(staff, map[string]any{
		"create": []any{map[string]any{"name": "Gamma pier"}, map[string]any{"code": "B-3"}},
		"update": []any{map[string]any{"id": "missing", "name": "Nope", "version": 1}},
	})
	if status != 422 || dig(body, "error", "code") != "BULK_REJECTED" || len(dig(body, "error", "details").([]any)) != 2 ||
		dig(body, "error", "details", 0, "index") != float64(1) || dig(body, "error", "details", 1, "status") != float64(404) {
		t.Fatalf("invalid bulk: %d %v", status, body)
	}
	// A database failure mid-batch rolls back the items before it, their
	// tokens and their hook events.
	status, body = bulk(staff, map[string]any{
		"create": []any{map[string]any{"name": "Delta pier", "code": "B-9"}, map[string]any{"name": "Echo pier", "code": "B-9"}},
	})
	if status != 409 || dig(body, "error", "details", 0, "index") != float64(1) {
		t.Fatalf("duplicate in bulk: %d %v", status, body)
	}
	if search(staff, "gamma") != 0 || search(staff, "delta") != 0 || pending() != 0 {
		t.Fatal("a rejected bulk request left changes behind")
	}
	// A stale version is a conflict like a single update's.
	status, _ = bulk(staff, map[string]any{"update": []any{map[string]any{"id": strasse["id"], "version": strasse["version"], "city": "Bern"}}})
	if status != 409 {
		t.Fatalf("stale bulk update: %d", status)
	}

	// Row conditions apply per record; with atomic false each item stands alone.
	status, body = h.call("POST", "/api/places", staff, map[string]any{"name": "Vault", "city": "Locked", "code": "B-8"})
	if status != 201 {
		t.Fatalf("create locked: %d %v", status, body)
	}
	locked := fmt.Sprint(dig(body, "id"))
	if status, body := bulk(staff, map[string]any{"delete": []any{locked}}); status != 403 {
		t.Fatalf("row condition in bulk: %d %v", status, body)
	}
	status, body = bulk(staff, map[string]any{"atomic": false,
		"create": []any{map[string]any{"name": "Foxtrot pier", "code": "B-10"}},
		"delete": []any{locked, map[string]any{"id": "missing"}},
	})
	if status != 200 || dig(body, "created") != float64(1) || dig(body, "failed") != float64(2) ||
		dig(body, "results", 1, "status") != float64(403) || dig(body, "results", 2, "status") != float64(404) || dig(body, "results", 0, "ok") != true {
		t.Fatalf("non-atomic bulk: %d %v", status, body)
	}
	if search(staff, "foxtrot") != 1 {
		t.Fatal("the non-atomic create was not applied")
	}

	// Scope, roles and limits.
	if status, body := bulk(other, map[string]any{"update": []any{map[string]any{"id": locked, "name": "Mine", "version": 1}}}); status != 404 {
		t.Fatalf("cross-tenant bulk: %d %v", status, body)
	}
	if status, _ := bulk(viewer, map[string]any{"create": []any{map[string]any{"name": "Viewer pier"}}}); status != 403 {
		t.Fatalf("bulk without the role: %d", status)
	}
	var many []any
	for i := range 7 {
		many = append(many, map[string]any{"name": fmt.Sprintf("Many %d", i)})
	}
	for _, req := range []map[string]any{
		{"create": many}, {}, {"insert": []any{}}, {"update": []any{map[string]any{"name": "No id"}}},
		{"update": []any{map[string]any{"id": locked, "version": 1}}, "delete": []any{locked}},
	} {
		if status, body := bulk(staff, req); status != 422 {
			t.Fatalf("bad bulk request %v: %d %v", req, status, body)
		}
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

func TestAnalyticsHelpers(t *testing.T) {
	for day, want := range map[string]string{"2026-09-21": "2026-09-21", "2026-09-27": "2026-09-21", "2026-10-01": "2026-09-28", "2026-01-01": "2025-12-29"} {
		if got := isoWeekStart(day); got != want {
			t.Errorf("isoWeekStart(%s) = %v, want %s", day, got, want)
		}
	}
	for _, c := range [][3]int64{{135, 4, 34}, {134, 4, 34}, {-135, 4, -34}, {10, 4, 3}, {9, 4, 2}, {0, 3, 0}} {
		if got := roundDiv(c[0], c[1]); got != c[2] {
			t.Errorf("roundDiv(%d, %d) = %d, want %d", c[0], c[1], got, c[2])
		}
	}
	for v, want := range map[any]int64{"30": 30, "30.000": 30, int64(7): 7, float64(4): 4} {
		if got, ok := sqlInt(v); !ok || got != want {
			t.Errorf("sqlInt(%v) = %d, %v", v, got, ok)
		}
	}
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
