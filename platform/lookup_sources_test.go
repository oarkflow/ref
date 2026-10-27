package platform

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func optionValues(opts []string) string { return strings.Join(opts, ",") }

func TestLookupSetValidation(t *testing.T) {
	for name, set := range map[string]map[string]any{
		"no name":       {"type": "static"},
		"bad type":      {"name": "a", "type": "ldap"},
		"write query":   {"name": "a", "type": "sql", "query": "DELETE FROM t"},
		"two stmts":     {"name": "a", "type": "sql", "query": "SELECT 1; SELECT 2"},
		"update inside": {"name": "a", "type": "sql", "query": "WITH x AS (SELECT 1) UPDATE t SET a = 1"},
		"http no path":  {"name": "a", "type": "http"},
		"entry no val":  {"name": "a", "type": "static", "entries": []any{map[string]any{"label": "x"}}},
	} {
		if _, err := lookupSetFromMap(set); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := lookupSetFromMap(map[string]any{"name": "ok", "type": "sql", "query": "SELECT code AS value, label FROM t WHERE a = ? ;"}); err != nil {
		t.Fatalf("valid query refused: %v", err)
	}
}

func TestLookupStaticSetFiltersByDependency(t *testing.T) {
	set, err := lookupSetFromMap(map[string]any{"name": "em", "type": "static", "entries": []any{
		map[string]any{"value": "99281", "label": "Level 1", "attrs": map[string]any{"mdm_level": []any{"Straightforward"}}},
		map[string]any{"value": "99285", "label": "Level 5", "attrs": map[string]any{"mdm_level": []any{"High"}}},
		map[string]any{"value": "ANY", "attrs": map[string]any{"mdm_level": []any{"High", "Low"}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	l := &LookupSources{sets: map[string]*lookupSet{"em": set}, cache: map[string]lookupCached{}}
	all, _ := l.Resolve(context.Background(), "em", nil)
	high, _ := l.Resolve(context.Background(), "em", map[string]any{"mdm_level": "High"})
	if len(all) != 3 || len(high) != 2 || high[0].Value != "99285" || high[1].Label != "ANY" {
		t.Fatalf("all=%v high=%v", all, high)
	}
	if _, err := l.Resolve(context.Background(), "missing", nil); err == nil {
		t.Fatal("unknown set resolved")
	}
}

func TestLookupHTTPSourceCachesAndServesStale(t *testing.T) {
	var calls atomic.Int32
	var fail atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if fail.Load() {
			http.Error(w, "down", http.StatusBadGateway)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"items": []any{
			map[string]any{"id": "A1", "name": "Alpha " + r.URL.Query().Get("region")}, map[string]any{"id": "B2"}}}})
	}))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")
	svc := &HTTPService{client: srv.Client(), baseURL: srv.URL, allowedHosts: map[string]struct{}{strings.Split(host, ":")[0]: {}}, attempts: 1, maxBytes: 1 << 20, allowPrivate: true}
	set, err := lookupSetFromMap(map[string]any{"name": "payers", "type": "http", "path": "/payers", "items_path": "data.items",
		"value_field": "id", "label_field": "name", "ttl": "40ms", "params": map[string]any{"region": "{region}"}})
	if err != nil {
		t.Fatal(err)
	}
	l := &LookupSources{sets: map[string]*lookupSet{"payers": set}, svc: svc, cache: map[string]lookupCached{}}
	ctx := context.Background()

	if opts, err := l.Resolve(ctx, "payers", nil); err != nil || len(opts) != 0 {
		t.Fatalf("missing dependency should give no choices, got %v %v", opts, err)
	}
	if calls.Load() != 0 {
		t.Fatal("called the API without the parameter it needs")
	}
	opts, err := l.Resolve(ctx, "payers", map[string]any{"region": "EU"})
	if err != nil || len(opts) != 2 || opts[0].Label != "Alpha EU" || opts[1].Label != "B2" {
		t.Fatalf("opts=%v err=%v", opts, err)
	}
	_, _ = l.Resolve(ctx, "payers", map[string]any{"region": "EU"})
	if calls.Load() != 1 {
		t.Fatalf("second call within ttl hit the API: %d calls", calls.Load())
	}
	time.Sleep(60 * time.Millisecond)
	fail.Store(true)
	stale, err := l.Resolve(ctx, "payers", map[string]any{"region": "EU"})
	if err != nil || len(stale) != 2 {
		t.Fatalf("API down should serve the last good list: %v %v", stale, err)
	}
	if _, err := l.Resolve(ctx, "payers", map[string]any{"region": "US"}); err == nil {
		t.Fatal("API down with nothing cached should be an error")
	}
}

// A pipeline whose choices come from a static list, a SQL table and an HTTP API, end to end through BCL.
func TestLookupSourcesEndToEnd(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]any{map[string]any{"value": "MEDICARE", "label": "Medicare"}, map[string]any{"value": "ACME", "label": "Acme Health"}})
	}))
	defer api.Close()
	root := t.TempDir()
	app := filepath.Join(root, "app.bcl")
	bcl := `
resource "db" {
  kind "database.sql"
  config {
    driver "sqlite"
    dsn "file:` + filepath.Join(root, "t.db") + `?_pragma=busy_timeout(5000)"
    migrations [
      "CREATE TABLE em_codes (code TEXT, label TEXT, mdm TEXT)",
      "INSERT INTO em_codes VALUES ('99283','ED level 3','Moderate'),('99285','ED level 5','High')"
    ]
  }
}
resource "payer_api" {
  kind "service.http"
  config {
    base_url "` + api.URL + `"
    allowed_hosts ["127.0.0.1"]
    allow_private_networks true
  }
}
resource "catalog" {
  kind "lookup.sources"
  config {
    database "db"
    service "payer_api"
    sets [
      { name "sex" type "static" entries [ { value "F" label "Female" }, { value "M" label "Male" } ] },
      { name "em_codes" type "sql" query "SELECT code AS value, label FROM em_codes WHERE mdm = ?" args ["mdm"] },
      { name "payers" type "http" path "/payers" }
    ]
  }
}
resource "cases" {
  kind "pipeline.cases"
  config {
    lookup_resource "catalog"
    allow_anonymous true
    signing_secret "lookup-test-signing-secret-long-enough"
    pipelines ["claim"]
  }
}
pipeline "claim" {
  title "Claim"
  form "f" {
    input "sex"   { kind select  lookup "sex" }
    input "mdm"   { kind select  options ["Moderate", "High"] }
    input "code"  { kind select  lookup "em_codes"  depends_on ["mdm"] }
    input "payer" { kind select  lookup "payers" }
  }
  stage "s" {
    public true
    page { group "g" { forms ["f"] } }
    action "submit" { outcome advance }
  }
  stage "done" { roles ["x"] }
}
intent "start" { response "c" node "c" { uses "pipeline.start" resource "cases" kind effect requires [input] provides [c] } }
intent "view"  { response "v" node "v" { uses "pipeline.view"  resource "cases" provides [v] } }
intent "choices" { response "v" node "v" { uses "pipeline.choices" resource "cases" requires [input] provides [v] } }
route "start"  { method POST path "/cases" intent "start" allow_anonymous true status 201 }
route "view"   { method GET  path "/cases/:id/stages/:stage" intent "view" allow_anonymous true }
route "choices" { method POST path "/cases/:id/stages/:stage/forms/:form/inputs/:input_name/choices" intent "choices" allow_anonymous true }
`
	if err := os.WriteFile(app, []byte(bcl), 0o600); err != nil {
		t.Fatal(err)
	}
	h := newAppHarness(t, app, nil)
	status, started := h.call("POST", "/cases", "", map[string]any{"pipeline": "claim"})
	if status != 201 {
		t.Fatalf("start: %d %v", status, started)
	}
	id := Stringify(started.(map[string]any)["case"].(map[string]any)["id"])
	key := Stringify(started.(map[string]any)["access_key"])
	inputs := func(view any) map[string][]string {
		out := map[string][]string{}
		page := view.(map[string]any)["page"].(map[string]any)
		for _, in := range page["groups"].([]any)[0].(map[string]any)["forms"].([]any)[0].(map[string]any)["inputs"].([]any) {
			m := in.(map[string]any)
			for _, c := range asSlice(m["choices"]) {
				out[Stringify(m["name"])] = append(out[Stringify(m["name"])], Stringify(c.(map[string]any)["value"]))
			}
		}
		return out
	}
	choices := inputs(started)
	if optionValues(choices["sex"]) != "F,M" {
		t.Errorf("static set: %v", choices["sex"])
	}
	if optionValues(choices["payer"]) != "MEDICARE,ACME" {
		t.Errorf("http set: %v", choices["payer"])
	}
	if len(choices["code"]) != 0 {
		t.Errorf("dependent sql set with no mdm chosen should offer nothing, got %v", choices["code"])
	}
	status, dep := h.callKey("POST", "/cases/"+id+"/stages/s/forms/f/inputs/code/choices", key, map[string]any{"deps": map[string]any{"mdm": "High"}})
	if status != 200 {
		t.Fatalf("choices: %d %v", status, dep)
	}
	var got []string
	for _, c := range asSlice(dep.(map[string]any)["choices"]) {
		got = append(got, Stringify(c.(map[string]any)["value"]))
	}
	if optionValues(got) != "99285" {
		t.Errorf("sql set narrowed by mdm=High: %q (%v)", optionValues(got), dep)
	}
}

func asSlice(v any) []any { s, _ := v.([]any); return s }

func TestTestLookup(t *testing.T) {
	root := t.TempDir()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(root, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE fac (facility_id TEXT, facility_name TEXT, status TEXT); INSERT INTO fac VALUES ('a','Alpha','active'),('b','Beta','closed')`); err != nil {
		t.Fatal(err)
	}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"value":"x","label":"Ex"}]`))
	}))
	defer api.Close()
	svc := &HTTPService{client: api.Client(), baseURL: api.URL, allowedHosts: map[string]struct{}{"127.0.0.1": {}}, attempts: 1, maxBytes: 1 << 20, allowPrivate: true}
	p := &Platform{resources: map[string]Resource{"database": &Database{DB: db}, "payer_api": svc}}
	ctx := context.Background()

	good, err := p.TestLookup(ctx, LookupTestRequest{Database: "database", Set: map[string]any{"type": "sql", "query": "SELECT facility_id AS value, facility_name AS label FROM fac WHERE status = ?", "args": []any{"status"}}, Deps: map[string]any{"status": "active"}})
	if err != nil || good.Total != 1 || good.Options[0].Label != "Alpha" {
		t.Fatalf("good query: %+v %v", good, err)
	}
	// The mistake this exists to catch: a column that is not there.
	if _, err := p.TestLookup(ctx, LookupTestRequest{Database: "database", Set: map[string]any{"type": "sql", "query": "SELECT id AS value, name AS label FROM fac"}}); err == nil || !strings.Contains(err.Error(), "no such column") {
		t.Fatalf("wrong column should be reported, got %v", err)
	}
	if _, err := p.TestLookup(ctx, LookupTestRequest{Database: "database", Set: map[string]any{"type": "sql", "query": "DELETE FROM fac"}}); err == nil {
		t.Fatal("a write was accepted")
	}
	var n int
	_ = db.QueryRow("SELECT COUNT(*) FROM fac").Scan(&n)
	if n != 2 {
		t.Fatalf("rows changed: %d", n)
	}
	if _, err := p.TestLookup(ctx, LookupTestRequest{Database: "nope", Set: map[string]any{"type": "sql", "query": "SELECT 1"}}); err == nil || !strings.Contains(err.Error(), "make it live") {
		t.Fatalf("unknown connection: %v", err)
	}
	web, err := p.TestLookup(ctx, LookupTestRequest{Service: "payer_api", Set: map[string]any{"type": "http", "path": "/p"}})
	if err != nil || web.Total != 1 || web.Options[0].Value != "x" {
		t.Fatalf("http: %+v %v", web, err)
	}
	if _, err := p.TestLookup(ctx, LookupTestRequest{Service: "payer_api", Set: map[string]any{"type": "http", "path": "/p", "items_path": "data.items"}}); err == nil {
		t.Fatal("a wrong items_path should be reported")
	}
	st, err := p.TestLookup(ctx, LookupTestRequest{Set: map[string]any{"type": "static", "entries": []any{map[string]any{"value": "1", "label": "One"}}}})
	if err != nil || st.Total != 1 {
		t.Fatalf("static: %+v %v", st, err)
	}
}
