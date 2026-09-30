package server

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

func TestSQLDraftsSurviveARestart(t *testing.T) {
	mut, path := withSQL(t)
	e1 := newEnv(t, mut)
	dir := e1.dir
	id, v := e1.newDraft(tokEditor, "active")
	setPath := func(e *env, ver int64, p string) obj {
		return e.ops(id, tokEditor, ver, obj{"op": "setField", "file": "01_routes.bcl", "path": "route/health.ping/path", "value": `"` + p + `"`})
	}
	a := setPath(e1, v, "/a")
	b := setPath(e1, ver(a), "/b")
	undone := e1.ok(200, "POST", "/api/v1/drafts/"+id+"/undo", tokEditor, nil) // state /a, redo has /b
	before := e1.ok(200, "GET", "/api/v1/drafts/"+id, tokEditor, nil)
	if ver(undone) <= ver(b) || !strings.Contains(e1.file(id, "01_routes.bcl", tokEditor), `"/a"`) {
		t.Fatalf("undo = %v", undone)
	}
	e1.srv.Close()

	// A new server over the same database file.
	e2 := newEnvDir(t, dir, sqlMutator(t, path))
	after := e2.ok(200, "GET", "/api/v1/drafts/"+id, tokEditor, nil)
	for _, k := range []string{"version", "owner", "name", "baseRevision", "dirty", "files", "createdAt"} {
		if b1, b2 := mustJSON(before[k]), mustJSON(after[k]); b1 != b2 {
			t.Errorf("%s: %s before the restart, %s after", k, b1, b2)
		}
	}
	if !strings.Contains(e2.file(id, "01_routes.bcl", tokEditor), `"/a"`) {
		t.Fatal("the draft's state was not restored")
	}
	// The diff base survived too.
	d := e2.ok(200, "GET", "/api/v1/drafts/"+id+"/diff", tokEditor, nil)
	if fs := d["files"].([]any); len(fs) != 1 || fs[0].(obj)["path"] != "01_routes.bcl" || fs[0].(obj)["status"] != "modified" {
		t.Fatalf("diff = %v", d)
	}
	// Redo restores /b; undo twice returns to the original; a third undo has nothing.
	e2.ok(200, "POST", "/api/v1/drafts/"+id+"/redo", tokEditor, nil)
	if !strings.Contains(e2.file(id, "01_routes.bcl", tokEditor), `"/b"`) {
		t.Fatal("redo after a restart did not restore /b")
	}
	e2.ok(200, "POST", "/api/v1/drafts/"+id+"/undo", tokEditor, nil)
	e2.ok(200, "POST", "/api/v1/drafts/"+id+"/undo", tokEditor, nil)
	if got := e2.file(id, "01_routes.bcl", tokEditor); got != routesBCLFormatted(t, e2) {
		t.Fatalf("after undoing everything the file is\n%s", got)
	}
	if c := e2.fail(409, "POST", "/api/v1/drafts/"+id+"/undo", tokEditor, nil); c != "nothing_to_undo" {
		t.Fatal(c)
	}
	// Versions keep increasing across the restart.
	cur := ver(e2.ok(200, "GET", "/api/v1/drafts/"+id, tokEditor, nil))
	if now := ver(e2.ops(id, tokEditor, cur, obj{"op": "setField", "file": "01_routes.bcl", "path": "route/health.ping/path", "value": `"/c"`})); now <= ver(undone) {
		t.Fatalf("version %d after the restart, %d before", now, ver(undone))
	}
}

// routesBCLFormatted is the draft's original 01_routes.bcl as the draft
// stores it (formatted by the first edit).
func routesBCLFormatted(t *testing.T, e *env) string {
	t.Helper()
	id, _ := e.newDraft(tokAdmin, "active")
	return e.file(id, "01_routes.bcl", tokAdmin)
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func TestSQLStorePrunesUnreferencedContents(t *testing.T) {
	mut, path := withSQL(t)
	e := newEnv(t, mut)
	id, v := e.newDraft(tokEditor, "active")
	db := openTestDB(t, path+"?mode=ro")
	count := func(where string, args ...any) int {
		t.Helper()
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM studio_blobs WHERE draft_id = ? `+where, append([]any{id}, args...)...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	set := func(ver int64, p string) obj {
		return e.ops(id, tokEditor, ver, obj{"op": "setField", "file": "01_routes.bcl", "path": "route/health.ping/path", "value": `"` + p + `"`})
	}
	// Two files at the start (base files and state share contents).
	if n := count(""); n != 2 {
		t.Fatalf("%d contents after creating the draft, want 2", n)
	}
	a := set(v, "/a")
	e.ok(200, "POST", "/api/v1/drafts/"+id+"/undo", tokEditor, nil)
	if n := count(`AND content LIKE '%"/a"%'`); n != 1 {
		t.Fatalf("the undone edit's content should be kept for redo (found %d)", n)
	}
	// A new edit drops the redo stack; /a is now unreachable and must go.
	set(ver(a)+1, "/b")
	if n := count(`AND content LIKE '%"/a"%'`); n != 0 {
		t.Fatalf("an unreachable content is still stored (%d)", n)
	}
	// Many edits store each distinct content once, not once per undo step.
	cur := ver(e.ok(200, "GET", "/api/v1/drafts/"+id, tokEditor, nil))
	for i := range 10 {
		cur = ver(set(cur, "/p"+strconv.Itoa(i)))
	}
	if n := count(""); n > 14 { // 2 originals + the formatted original + /b + 10 edits
		t.Fatalf("%d contents stored for 12 distinct versions of one file", n)
	}
}

func TestSQLStoreDeleteRemovesRows(t *testing.T) {
	mut, path := withSQL(t)
	e := newEnv(t, mut)
	id, v := e.newDraft(tokEditor, "active")
	e.ops(id, tokEditor, v, obj{"op": "setField", "file": "01_routes.bcl", "path": "route/health.ping/path", "value": `"/x"`})
	e.ok(204, "DELETE", "/api/v1/drafts/"+id, tokEditor, nil)
	db := openTestDB(t, path+"?mode=ro")
	for _, table := range []string{"studio_drafts", "studio_blobs"} {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil || n != 0 {
			t.Fatalf("%s has %d rows after the draft was deleted (%v)", table, n, err)
		}
	}
	e.fail(404, "GET", "/api/v1/drafts/"+id, tokEditor, nil)
	// ... and it stays gone after a restart.
	e2 := newEnvDir(t, e.dir, sqlMutator(t, path))
	e2.fail(404, "GET", "/api/v1/drafts/"+id, tokEditor, nil)
}

func TestSQLMigrateIsRepeatable(t *testing.T) {
	db := openTestDB(t, t.TempDir()+"/m.db")
	for range 3 {
		if _, _, err := OpenSQL(context.Background(), db, "sqlite", "x_"); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := OpenSQL(context.Background(), db, "oracle", ""); err == nil {
		t.Fatal("accepted an unsupported dialect")
	}
	if _, _, err := OpenSQL(context.Background(), db, "sqlite", "bad prefix;"); err == nil {
		t.Fatal("accepted an unsafe table prefix")
	}
}

func TestSQLPlaceholdersForPostgres(t *testing.T) {
	b := sqlBase{dialect: "postgres", prefix: "s_"}
	got := b.q(`SELECT a FROM {p}t WHERE x = ? AND y = ? LIMIT ?`)
	if got != `SELECT a FROM s_t WHERE x = $1 AND y = $2 LIMIT $3` {
		t.Fatal(got)
	}
}

// auditPages walks GET /audit with ?before= and returns every id, newest first.
func auditPages(t *testing.T, e *env, limit int) []int64 {
	t.Helper()
	var ids []int64
	before := ""
	for range 50 {
		path := "/api/v1/audit?limit=" + strconv.Itoa(limit) + before
		status, hdr, raw := e.callFull("GET", path, tokAdmin, nil, nil)
		if status != 200 {
			t.Fatalf("%s: %d %s", path, status, raw)
		}
		var page []AuditEntry
		if err := json.Unmarshal(raw, &page); err != nil {
			t.Fatal(err)
		}
		for _, a := range page {
			ids = append(ids, a.ID)
		}
		next := hdr.Get("X-Next-Before")
		if len(page) < limit {
			if next != "" {
				t.Fatalf("a short page should have no cursor (got %q)", next)
			}
			return ids
		}
		if next == "" {
			t.Fatal("a full page should carry a cursor")
		}
		before = "&before=" + next
	}
	t.Fatal("pagination did not end")
	return nil
}

func checkAuditIDs(t *testing.T, ids []int64, want int) {
	t.Helper()
	if len(ids) < want {
		t.Fatalf("%d audit entries, want at least %d", len(ids), want)
	}
	seen := map[int64]bool{}
	for i, id := range ids {
		if id <= 0 || seen[id] {
			t.Fatalf("entry %d has a bad or repeated id %d", i, id)
		}
		seen[id] = true
		if i > 0 && id >= ids[i-1] {
			t.Fatalf("ids are not strictly decreasing: %d after %d", id, ids[i-1])
		}
	}
}

func TestAuditPaginationRing(t *testing.T) {
	e := newEnv(t)
	for range 5 {
		e.newDraft(tokEditor, "dir")
	}
	checkAuditIDs(t, auditPages(t, e, 2), 5)
	if c := e.fail(400, "GET", "/api/v1/audit?before=abc", tokAdmin, nil); c != "bad_request" {
		t.Fatal(c)
	}
	e.fail(400, "GET", "/api/v1/audit?before=0", tokAdmin, nil)
	e.fail(400, "GET", "/api/v1/audit?limit=0", tokAdmin, nil)
}

func TestAuditPaginationAndPersistenceSQL(t *testing.T) {
	mut, path := withSQL(t)
	e := newEnv(t, mut, func(c *Config) { c.AuditSize = 2 }) // a tiny ring: reads must come from SQL
	for range 5 {
		e.newDraft(tokEditor, "dir")
	}
	first := auditPages(t, e, 2)
	checkAuditIDs(t, first, 5)
	e.srv.Close()

	e2 := newEnvDir(t, e.dir, sqlMutator(t, path), func(c *Config) { c.AuditSize = 2 })
	all := auditPages(t, e2, 3)
	checkAuditIDs(t, all, 5)
	if all[len(all)-5] != first[len(first)-5] {
		t.Fatalf("the earliest entries differ after a restart: %v vs %v", first, all)
	}
	// New entries sort after the old ones.
	e2.newDraft(tokEditor, "dir")
	latest := auditPages(t, e2, 100)
	if latest[0] <= all[0] {
		t.Fatalf("a new entry (%d) does not sort after the old ones (%d)", latest[0], all[0])
	}
}
