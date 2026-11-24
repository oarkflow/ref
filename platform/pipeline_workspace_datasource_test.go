package platform

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// A hospital keeps its providers in its own table (here sqlite, in practice
// MySQL or any database.sql dialect) keyed by facility_id. The workflow maps the
// columns and filters by the case's facility; nothing about the rows is in BCL.
func datasourceApp(db string) string {
	return `
name "ds"
resource "jwt" {
  kind "auth.jwt"
  config { secret env.required("WS_JWT_SECRET") }
}
resource "db" {
  kind "database.sql"
  config {
    driver "sqlite"
    dsn "file:` + db + `?_pragma=busy_timeout(5000)"
    migrations [
      "CREATE TABLE tbl_providers (npi TEXT, full_name TEXT, facility_id TEXT, kind TEXT, specialties TEXT, active INTEGER, rank INTEGER)",
      "INSERT INTO tbl_providers VALUES ('n1','Dr Sharma','a','physician','CARD|GM',1,2),('n2','Dr Rai','a','physician','EM',1,1),('n3','Dr Gone','a','physician','CARD',0,3),('n4','Dr Lama','a','resident','CARD',1,4),('n5','Bill Co','a','billing','',1,5),('n6','Dr Koirala','b','physician','GM',1,1)"
    ]
  }
}
resource "org" {
  kind "org.hierarchy"
  config {
    levels ["group", "facility"]
    assignment_claim "org_units"
    nodes [
      { id "g" level "group" name "Group" },
      { id "a" parent "g" level "facility" name "Hospital A" },
      { id "b" parent "g" level "facility" name "Clinic B" }
    ]
    lookups [
      { set "nurses" node "a" code "rn-own" label "Own nurse A" }
    ]
  }
}
resource "catalog" {
  kind "lookup.sources"
  config {
    database "db"
    sets [
      { name "providers" type "sql"
        query "SELECT npi, full_name, facility_id, kind, specialties, active, rank FROM tbl_providers"
        value_field "npi" label_field "full_name" sort "rank"
        attrs { provider_type "kind" specialty "specialties" }
        filters [
          { on "facility_id" op "eq" value "{facility}" },
          { on "active" op "eq" value "1" }
        ] },
      { name "nurses" type "sql" merge true
        query "SELECT npi AS value, full_name AS label FROM tbl_providers WHERE facility_id = ? AND kind = 'resident'"
        args ["facility"] }
    ]
  }
}
resource "cases" {
  kind "pipeline.cases"
  config {
    org_resource "org"
    lookup_resource "catalog"
  }
}
pipeline "enc" {
  workspace { level "facility"  required true }
  form "f" {
    input "specialty" { kind select  options ["CARD", "GM", "EM"] }
    input "provider"  { kind select  lookup "providers"  lookup_where ["provider_type=physician"]  depends_on ["specialty"] }
    input "billing"   { kind select  lookup "providers"  lookup_where ["provider_type=billing"] }
    input "nurse"     { kind select  lookup "nurses" }
  }
  stage "intake" {
    roles ["nurse"]
    page { group "g" { forms ["f"] } }
    action "go" { outcome advance }
  }
  stage "done" { roles ["nurse"] }
}
intent "start" { response "r"  node "r" { uses "pipeline.start"  resource "cases"  kind effect  requires [input]  provides [r] } }
intent "choices" { response "r"  node "r" { uses "pipeline.choices"  resource "cases"  requires [input]  provides [r] } }
route "start" { method POST  path "/cases"  intent "start"  auth "jwt"  status 201 }
route "choices" { method POST  path "/cases/:id/choices/:form/:input_name"  intent "choices"  auth "jwt" }
`
}

func TestWorkspaceLookupsFromDatasource(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.bcl")
	if err := os.WriteFile(path, []byte(datasourceApp(filepath.Join(dir, "ds.db"))), 0o600); err != nil {
		t.Fatal(err)
	}
	h := newAppHarness(t, path, map[string]string{"WS_JWT_SECRET": "ws-test-secret-0123456789abcdef-xyz"})
	nurseA := h.token("jwt", "na", []string{"nurse"}, map[string]any{"org_units": []string{"a"}})
	nurseB := h.token("jwt", "nb", []string{"nurse"}, map[string]any{"org_units": []string{"b"}})
	start := func(tok, unit string) string {
		status, body := h.call("POST", "/cases", tok, map[string]any{"data": map[string]any{}, "org_unit": unit})
		if status != 201 {
			t.Fatalf("start %s: %d %v", unit, status, body)
		}
		return fmt.Sprint(dig(body, "case", "id"))
	}
	choices := func(tok, id, input string, deps map[string]any) []string {
		_, r := h.call("POST", "/cases/"+id+"/choices/f/"+input, tok, map[string]any{"deps": deps})
		var out []string
		list, _ := dig(r, "choices").([]any)
		for _, c := range list {
			out = append(out, fmt.Sprint(dig(c, "value")))
		}
		return out
	}
	a, b := start(nurseA, "a"), start(nurseB, "b")

	// Hospital A's physicians for CARD: its own rows, active only, physicians only, by rank.
	if got := choices(nurseA, a, "provider", map[string]any{"specialty": "CARD"}); fmt.Sprint(got) != "[n1]" {
		t.Fatalf("A CARD physicians: %v", got)
	}
	if got := choices(nurseA, a, "provider", map[string]any{"specialty": "GM"}); fmt.Sprint(got) != "[n1]" {
		t.Fatalf("a multi-valued column maps to a list attribute: %v", got)
	}
	if got := choices(nurseA, a, "provider", map[string]any{"specialty": "EM"}); fmt.Sprint(got) != "[n2]" {
		t.Fatalf("A EM physicians: %v", got)
	}
	if got := choices(nurseA, a, "billing", nil); fmt.Sprint(got) != "[n5]" {
		t.Fatalf("A billing provider: %v", got)
	}
	// Typing narrows server-side, before any row cap: "rai" finds Dr Rai among A's physicians.
	_, r := h.call("POST", "/cases/"+a+"/choices/f/provider", nurseA, map[string]any{"deps": map[string]any{"specialty": "EM"}, "q": "RAI"})
	if list, _ := dig(r, "choices").([]any); len(list) != 1 || fmt.Sprint(dig(list[0], "value")) != "n2" {
		t.Fatalf("type-ahead: %v", r)
	}
	_, r = h.call("POST", "/cases/"+a+"/choices/f/provider", nurseA, map[string]any{"deps": map[string]any{"specialty": "EM"}, "q": "zzz"})
	if list, _ := dig(r, "choices").([]any); len(list) != 0 {
		t.Fatalf("type-ahead with no match: %v", r)
	}
	// Clinic B sees only its own row, whatever A holds.
	if got := choices(nurseB, b, "provider", map[string]any{"specialty": "GM"}); fmt.Sprint(got) != "[n6]" {
		t.Fatalf("B GM physicians: %v", got)
	}
	if got := choices(nurseB, b, "billing", nil); len(got) != 0 {
		t.Fatalf("B has no billing provider: %v", got)
	}
	// A set that merges adds the workspace's own entries to the datasource rows.
	if got := choices(nurseA, a, "nurse", nil); fmt.Sprint(got) != "[n4 rn-own]" {
		t.Fatalf("merged nurses at A: %v", got)
	}
	if got := choices(nurseB, b, "nurse", nil); len(got) != 0 {
		t.Fatalf("no nurses at B: %v", got)
	}
}

func TestMigrationFilesSplitStatementsAndSkipComments(t *testing.T) {
	dir := t.TempDir()
	body := "-- a comment; with a semicolon\nCREATE TABLE t (a TEXT);\nINSERT INTO t VALUES\n  ('x'),\n  ('y;z');\n-- trailing\nINSERT INTO t VALUES ('last');"
	if err := os.WriteFile(filepath.Join(dir, "seed.sql"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := readMigrationFile(ResourceSpec{BaseDir: dir}, "seed.sql")
	if err != nil || len(got) != 3 {
		t.Fatalf("statements: %d %v %v", len(got), got, err)
	}
	if _, err := readMigrationFile(ResourceSpec{BaseDir: dir}, "missing.sql"); err == nil {
		t.Fatal("a missing file is an error")
	}
}

func TestLookupSetWritesNeedSingleStatements(t *testing.T) {
	bad := map[string]any{"name": "s", "type": "sql", "query": "SELECT 1", "upsert": "INSERT INTO a VALUES (1); DROP TABLE a"}
	if _, err := lookupSetFromMap(bad); err == nil {
		t.Fatal("two statements are refused")
	}
	bad["upsert"] = "SELECT 1"
	if _, err := lookupSetFromMap(bad); err == nil {
		t.Fatal("a write statement must write")
	}
	bad["upsert"] = "INSERT INTO a VALUES (?)"
	if _, err := lookupSetFromMap(bad); err != nil {
		t.Fatal(err)
	}
}
