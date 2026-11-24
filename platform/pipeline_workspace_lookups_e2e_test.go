package platform

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

const hospitalWorkspaceApp = `
name "ws"

resource "jwt" {
  kind "auth.jwt"
  config { secret env.required("WS_JWT_SECRET") }
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
      { set "providers" node "a" code "dr-a" label "Dr A" attrs { specialty ["CARD"] } },
      { set "providers" node "b" code "dr-b" label "Dr B" attrs { specialty ["GM"] } },
      { set "cpt" code "1" label "Code 1" attrs { specialty ["CARD", "GM"] } },
      { set "cpt" code "2" label "Code 2" attrs { specialty ["CARD"] } },
      { set "cpt" node "b" code "2" disabled true },
      { set "cpt" node "a" code "9" label "Code 9 (A only)" attrs { specialty ["CARD"] } }
    ]
  }
}

resource "cases" {
  kind "pipeline.cases"
  config { org_resource "org" }
}

pipeline "enc" {
  workspace { level "facility"  required true  label "Facility" }
  form "f" {
    input "specialty" { kind select  options ["CARD", "GM"]  required true }
    input "provider"  { kind select  lookup "providers"  depends_on ["specialty"] }
    input "codes"     { kind multiselect  lookup "cpt"  depends_on ["f.specialty"] }
  }
  stage "intake" {
    roles ["nurse"]
    page { group "g" { forms ["f"] } }
    action "go" { outcome advance }
  }
  stage "done" {
    roles ["nurse"]
    page { group "g" { mode readonly  forms ["f"] } }
    action "ok" { outcome advance }
  }
}

intent "start" { response "r"  node "r" { uses "pipeline.start"  resource "cases"  kind effect  requires [input]  provides [r] } }
intent "view"  { response "r"  node "r" { uses "pipeline.view"   resource "cases"  requires [input]  provides [r] } }
intent "act"   { response "r"  node "r" { uses "pipeline.act"    resource "cases"  kind effect  requires [input]  provides [r] } }
intent "choices" { response "r"  node "r" { uses "pipeline.choices"  resource "cases"  requires [input]  provides [r] } }
intent "write" { response "r"  node "r" { uses "lookup.write"  resource "org"  kind effect  requires [input]  provides [r]  config { operation "upsert"  roles ["admin"] } } }

route "start" { method POST  path "/cases"  intent "start"  auth "jwt"  status 201 }
route "view"  { method GET   path "/cases/:id"  intent "view"  auth "jwt" }
route "act"   { method POST  path "/cases/:id/stages/:stage/actions/:action"  intent "act"  auth "jwt" }
route "choices" { method POST  path "/cases/:id/choices/:form/:input_name"  intent "choices"  auth "jwt" }
route "write" { method POST  path "/lookup"  intent "write"  auth "jwt" }
`

func TestWorkspacesScopeLookupsAndRolesEndToEnd(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.bcl")
	if err := os.WriteFile(path, []byte(hospitalWorkspaceApp), 0o600); err != nil {
		t.Fatal(err)
	}
	h := newAppHarness(t, path, map[string]string{"WS_JWT_SECRET": "ws-test-secret-0123456789abcdef-xyz"})
	nurseA := h.token("jwt", "na", []string{"nurse"}, map[string]any{"org_units": []string{"a"}})
	nurseB := h.token("jwt", "nb", []string{"nurse"}, map[string]any{"org_units": []string{"b"}})
	adminA := h.token("jwt", "aa", []string{"admin"}, map[string]any{"org_units": []string{"a"}})

	// A case must start in a facility inside the caller's scope.
	if status, _ := h.call("POST", "/cases", nurseA, map[string]any{"data": map[string]any{}}); status != 422 {
		t.Fatalf("a workspace is required: %d", status)
	}
	if status, _ := h.call("POST", "/cases", nurseA, map[string]any{"data": map[string]any{}, "org_unit": "b"}); status != 403 {
		t.Fatalf("a facility outside the scope is refused: %d", status)
	}
	if status, _ := h.call("POST", "/cases", nurseA, map[string]any{"data": map[string]any{}, "org_unit": "g"}); status != 422 && status != 400 && status != 403 {
		t.Fatalf("the wrong level is refused: %d", status)
	}
	status, body := h.call("POST", "/cases", nurseA, map[string]any{"data": map[string]any{}, "org_unit": "a"})
	if status != 201 {
		t.Fatalf("start: %d %v", status, body)
	}
	id := fmt.Sprint(dig(body, "case", "id"))

	// Each workspace offers its own providers, narrowed by the dependency.
	choices := func(token, caseID, input, specialty string) []string {
		_, r := h.call("POST", "/cases/"+caseID+"/choices/f/"+input, token, map[string]any{"deps": map[string]any{"specialty": specialty, "f.specialty": specialty}})
		var out []string
		list, _ := dig(r, "choices").([]any)
		for _, c := range list {
			out = append(out, fmt.Sprint(dig(c, "value")))
		}
		return out
	}
	if got := choices(nurseA, id, "provider", "CARD"); fmt.Sprint(got) != "[dr-a]" {
		t.Fatalf("hospital A providers for CARD: %v", got)
	}
	if got := choices(nurseA, id, "provider", "GM"); len(got) != 0 {
		t.Fatalf("no GM provider at A: %v", got)
	}
	// CPT codes: the group's list, A's addition; a dependency written form.input matches the bare attribute.
	if got := choices(nurseA, id, "codes", "CARD"); fmt.Sprint(got) != "[1 2 9]" {
		t.Fatalf("A cpt codes: %v", got)
	}

	// Clinic B switches code 2 off and has no code 9; its nurse cannot open A's case.
	_, bb := h.call("POST", "/cases", nurseB, map[string]any{"data": map[string]any{}, "org_unit": "b"})
	bid := fmt.Sprint(dig(bb, "case", "id"))
	if got := choices(nurseB, bid, "codes", "CARD"); fmt.Sprint(got) != "[1]" {
		t.Fatalf("B cpt codes: %v", got)
	}
	if status, _ := h.call("GET", "/cases/"+id, nurseB, nil); status != 403 && status != 404 {
		t.Fatalf("B's nurse must not open A's case: %d", status)
	}

	// A value outside the workspace's list is refused on save.
	status, r := h.call("POST", "/cases/"+id+"/stages/intake/actions/go", nurseA, map[string]any{"data": map[string]any{"f": map[string]any{"specialty": "CARD", "provider": "dr-b"}}})
	if status != 422 {
		t.Fatalf("another workspace's provider is refused: %d %v", status, r)
	}
	if status, r = h.call("POST", "/cases/"+id+"/stages/intake/actions/go", nurseA, map[string]any{"data": map[string]any{"f": map[string]any{"specialty": "CARD", "provider": "dr-a", "codes": []string{"1", "9"}}}}); status != 200 {
		t.Fatalf("the workspace's own values are accepted: %d %v", status, r)
	}

	// Reference data is written by an admin of that workspace, and only there.
	entry := map[string]any{"set": "providers", "node_id": "a", "code": "dr-new", "label": "Dr New", "attrs": map[string]any{"specialty": []string{"CARD"}}}
	if status, _ := h.call("POST", "/lookup", nurseA, entry); status != 403 {
		t.Fatalf("a nurse may not write reference data: %d", status)
	}
	if status, _ := h.call("POST", "/lookup", adminA, map[string]any{"set": "providers", "node_id": "b", "code": "x", "label": "X"}); status != 403 {
		t.Fatalf("an admin may not write outside their workspace: %d", status)
	}
	if status, r := h.call("POST", "/lookup", adminA, entry); status != 200 {
		t.Fatalf("an admin writes their own workspace's data: %d %v", status, r)
	}
}
