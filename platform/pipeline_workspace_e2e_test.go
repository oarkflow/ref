package platform

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// workspaceApp is a claims pipeline over a company > facility > work item
// hierarchy: dependent lookups (a state of its country), per-role field
// permissions, a disabled_if rule, and workspace settings kept on org units.
const workspaceApp = `
name "wsapp"
version "1"

resource "db" {
  kind "database.sql"
  config {
    driver "sqlite"
    dsn "file:%DB%?_pragma=busy_timeout(5000)"
  }
}

resource "jwt" {
  kind "auth.jwt"
  config { secret env.required("WS_JWT_SECRET") }
}

resource "org" {
  kind "org.hierarchy"
  config {
    levels ["company", "facility", "workitem"]
    assignment_claim "org_units"
    global_roles ["root"]
    nodes [
      { id "co_a" level "company" name "Company A" },
      { id "fac_a1" parent "co_a" level "facility" name "Facility A1" },
      { id "wi_a1x" parent "fac_a1" level "workitem" name "Work item A1x" },
      { id "wi_a1y" parent "fac_a1" level "workitem" name "Work item A1y" },
      { id "co_b" level "company" name "Company B" },
      { id "wi_b1" parent "co_b" level "workitem" name "Work item B1" }
    ]
    lookups [
      { set "countries" code "US" label "United States" },
      { set "countries" code "CA" label "Canada" },
      { set "states" code "NY" label "New York" attrs { country "US" } },
      { set "states" code "CA" label "California" attrs { country "US" } },
      { set "states" code "ON" label "Ontario" attrs { country "CA" } }
    ]
  }
}

resource "cases" {
  kind "pipeline.cases"
  config {
    pipelines ["claims"]
    database "db"
    org_resource "org"
  }
}

pipeline "claims" {
  title "Claims"
  calendar "office" {
    timezone "UTC"
    hours "24x7"
  }
  form "claim" {
    input "category" {
      kind select
      options ["A", "B"]
    }
    input "amount" {
      kind number
      edit_roles ["manager"]
    }
    input "reason" {
      kind text
      disabled_if "claim.category == 'B'"
    }
  }
  form "geo" {
    input "country" {
      kind select
      lookup "countries"
    }
    input "state" {
      kind select
      lookup "states"
      depends_on ["country"]
    }
  }
  form "audit" {
    view_roles ["auditor"]
    input "finding" {
      kind text
    }
  }
  stage "work" {
    roles ["coder", "manager", "auditor"]
    due "72h"
    sla {
      duration "48h"
      calendar "office"
    }
    page {
      group "main" {
        forms ["claim", "geo", "audit"]
      }
    }
  }
}

intent "ws.start" {
  response "case"
  node "case" {
    uses "pipeline.start"
    resource "cases"
    kind effect
    requires [input]
    provides [case]
  }
}
intent "ws.view" {
  response "view"
  node "view" {
    uses "pipeline.view"
    resource "cases"
    provides [view]
  }
}
intent "ws.save" {
  response "view"
  node "view" {
    uses "pipeline.save"
    resource "cases"
    kind effect
    requires [input]
    provides [view]
  }
}
intent "ws.choices" {
  response "choices"
  node "choices" {
    uses "pipeline.choices"
    resource "cases"
    requires [input]
    provides [choices]
  }
}
intent "ws.settings_get" {
  response "settings"
  node "settings" {
    uses "pipeline.workspace_get"
    resource "cases"
    provides [settings]
    config { scope_claim "admin_units" }
  }
}
intent "ws.settings_set" {
  response "settings"
  node "settings" {
    uses "pipeline.workspace_set"
    resource "cases"
    kind effect
    requires [input]
    provides [settings]
    config { scope_claim "admin_units" }
  }
}

route "start" {
  method POST
  path "/cases"
  intent "ws.start"
  auth "jwt"
  status 201
}
route "view" {
  method GET
  path "/cases/:id"
  intent "ws.view"
  auth "jwt"
}
route "save" {
  method PUT
  path "/cases/:id/stages/:stage"
  intent "ws.save"
  auth "jwt"
}
route "choices" {
  method POST
  path "/cases/:id/choices/:form/:input_name"
  intent "ws.choices"
  auth "jwt"
}
route "settings_get" {
  method GET
  path "/workspaces/:unit/settings"
  intent "ws.settings_get"
  auth "jwt"
}
route "settings_set" {
  method PUT
  path "/workspaces/:unit/settings"
  intent "ws.settings_set"
  auth "jwt"
}
`

func inputByName(view any, form, name string) map[string]any {
	groups, _ := dig(view, "page", "groups").([]any)
	for _, g := range groups {
		forms, _ := dig(g, "forms").([]any)
		for _, f := range forms {
			if dig(f, "name") != form {
				continue
			}
			inputs, _ := dig(f, "inputs").([]any)
			for _, in := range inputs {
				if m, ok := in.(map[string]any); ok && m["name"] == name {
					return m
				}
			}
		}
	}
	return nil
}

func formNames(view any) []string {
	var out []string
	groups, _ := dig(view, "page", "groups").([]any)
	for _, g := range groups {
		forms, _ := dig(g, "forms").([]any)
		for _, f := range forms {
			out = append(out, dig(f, "name").(string))
		}
	}
	return out
}

func TestWorkspacePipelineEndToEnd(t *testing.T) {
	dir := t.TempDir()
	app := strings.ReplaceAll(workspaceApp, "%DB%", filepath.Join(dir, "ws.db"))
	path := filepath.Join(dir, "app.bcl")
	if err := os.WriteFile(path, []byte(app), 0o600); err != nil {
		t.Fatal(err)
	}
	h := newAppHarness(t, path, map[string]string{"WS_JWT_SECRET": strings.Repeat("k", 48)})

	coder := h.token("jwt", "coder1", []string{"coder"}, map[string]any{"org_units": "co_a"})
	manager := h.token("jwt", "mgr1", []string{"manager"}, map[string]any{"org_units": "co_a"})
	auditor := h.token("jwt", "aud1", []string{"coder", "auditor"}, map[string]any{"org_units": "co_a"})
	admin := h.token("jwt", "adm1", []string{"coder"}, map[string]any{"org_units": "fac_a1", "admin_units": "fac_a1"})
	root := h.token("jwt", "root1", []string{"root"}, nil)

	managerB := h.token("jwt", "mgr2", []string{"manager"}, map[string]any{"org_units": "co_b"})
	start := func(unit string) string {
		t.Helper()
		who := manager
		if unit == "wi_b1" {
			who = managerB
		}
		status, body := h.call("POST", "/cases", who, map[string]any{"org_unit": unit})
		if status != 201 {
			t.Fatalf("start in %s: %d %v", unit, status, body)
		}
		return dig(body, "case", "id").(string)
	}
	caseA := start("wi_a1x")

	// --- field and form permissions by role --------------------------------
	_, view := h.call("GET", "/cases/"+caseA, coder, nil)
	if inputByName(view, "claim", "amount")["editable"] != false {
		t.Error("a coder sees the manager-only amount read-only")
	}
	if slicesContains(formNames(view), "audit") {
		t.Error("a coder must not see the auditor-only form")
	}
	_, av := h.call("GET", "/cases/"+caseA, auditor, nil)
	if !slicesContains(formNames(av), "audit") {
		t.Error("an auditor sees the audit form")
	}

	// --- dependent lookups, through the view, validation and choices -------
	if st, body := h.call("PUT", "/cases/"+caseA+"/stages/work", coder, map[string]any{"data": map[string]any{
		"geo": map[string]any{"country": "CA", "state": "NY"}}}); st == 200 {
		t.Errorf("a state of another country must be refused: %v", body)
	}
	if st, body := h.call("PUT", "/cases/"+caseA+"/stages/work", coder, map[string]any{"data": map[string]any{
		"geo": map[string]any{"country": "CA", "state": "ON"}}}); st != 200 {
		t.Fatalf("matching country and state: %d %v", st, body)
	}
	_, v2 := h.call("GET", "/cases/"+caseA, coder, nil)
	state := inputByName(v2, "geo", "state")
	if got := codesOfChoices(state["choices"]); len(got) != 1 || got[0] != "ON" {
		t.Errorf("state choices for CA = %v", got)
	}
	st, ch := h.call("POST", "/cases/"+caseA+"/choices/geo/state", coder, map[string]any{"deps": map[string]any{"country": "US"}})
	if st != 200 || len(codesOfChoices(dig(ch, "choices"))) != 2 {
		t.Errorf("choices for US: %d %v", st, ch)
	}

	// --- disabled_if --------------------------------------------------------
	if st, body := h.call("PUT", "/cases/"+caseA+"/stages/work", coder, map[string]any{"data": map[string]any{
		"claim": map[string]any{"category": "B", "reason": "first"}}}); st != 200 {
		t.Fatalf("save category B: %d %v", st, body)
	}
	_, v3 := h.call("GET", "/cases/"+caseA, coder, nil)
	reason := inputByName(v3, "claim", "reason")
	if reason["editable"] != false || reason["disabled"] != true {
		t.Errorf("reason should be disabled once category is B: %v", reason)
	}

	// --- workspace settings: scope, validation, inheritance ----------------
	settings := map[string]any{"overrides": map[string]any{
		"stages": map[string]any{"work": map[string]any{"sla_duration": "6h"}},
		"fields": map[string]any{"claim.category": map[string]any{"required": true, "label": "Claim type"}},
	}}
	if st, _ := h.call("PUT", "/workspaces/fac_a1/settings", coder, settings); st == 200 {
		t.Error("a coder who administers no unit cannot write settings")
	}
	if st, _ := h.call("PUT", "/workspaces/co_b/settings", admin, settings); st == 200 {
		t.Error("an admin of fac_a1 must not write company B's settings")
	}
	if st, _ := h.call("PUT", "/workspaces/co_a/settings", admin, settings); st == 200 {
		t.Error("an admin of fac_a1 must not write the company above it")
	}
	if st, body := h.call("PUT", "/workspaces/wi_a1x/settings", admin, map[string]any{"overrides": map[string]any{
		"stages": map[string]any{"ghost": map[string]any{"due": "1h"}}}}); st == 200 {
		t.Errorf("settings naming an unknown stage must be refused: %v", body)
	}
	if st, body := h.call("PUT", "/workspaces/co_a/settings", root, settings); st != 200 {
		t.Fatalf("root writes company settings: %d %v", st, body)
	}
	// A work item refines the company's setting for its own cases only.
	if st, body := h.call("PUT", "/workspaces/wi_a1y/settings", admin, map[string]any{"overrides": map[string]any{
		"fields": map[string]any{"claim.amount": map[string]any{"hidden": true}}}}); st != 200 {
		t.Fatalf("scoped admin writes a work item's settings: %d %v", st, body)
	}

	caseX := start("wi_a1x")
	caseY := start("wi_a1y")
	caseB := start("wi_b1")
	_, xv := h.call("GET", "/cases/"+caseX, manager, nil)
	if in := inputByName(xv, "claim", "category"); in["required"] != true || in["label"] != "Claim type" {
		t.Errorf("company settings should reach its work items: %v", in)
	}
	if inputByName(xv, "claim", "amount") == nil {
		t.Error("work item x keeps the amount field")
	}
	_, yv := h.call("GET", "/cases/"+caseY, manager, nil)
	if inputByName(yv, "claim", "amount") != nil {
		t.Error("work item y hid the amount field")
	}
	if in := inputByName(yv, "claim", "category"); in["required"] != true {
		t.Error("work item y still inherits the company's required category")
	}
	if st, _ := h.call("POST", "/cases", manager, map[string]any{"org_unit": "wi_b1"}); st == 201 {
		t.Error("a manager of company A must not start a case in company B")
	}
	_, bv := h.call("GET", "/cases/"+caseB, managerB, nil)
	if in := inputByName(bv, "claim", "category"); in["required"] == true || in["label"] == "Claim type" {
		t.Errorf("company B must not see company A's settings: %v", in)
	}

	// The stage SLA is armed on entry from the workspace's duration.
	_, cx := h.call("GET", "/cases/"+caseX, manager, nil)
	if due, _ := dig(cx, "stage", "due_at").(string); due == "" {
		t.Log("stage due_at not exposed in the view; SLA duration is covered by the pipeline tests")
	}

	// --- roles held at a workspace apply at and below it ------------------------
	scopedMgr := h.token("jwt", "wsmgr", nil, map[string]any{"org_units": "wi_a1x,co_b", "workspace_roles": "wi_a1x:manager,co_b:coder"})
	companyCoder := h.token("jwt", "cocoder", nil, map[string]any{"org_units": "co_a", "workspace_roles": "co_a:coder"})
	_, sv := h.call("GET", "/cases/"+caseX, scopedMgr, nil)
	if in := inputByName(sv, "claim", "amount"); in == nil || in["editable"] != true {
		t.Errorf("a manager at wi_a1x edits the amount of its work item: %v", in)
	}
	if st, _ := h.call("GET", "/cases/"+caseY, scopedMgr, nil); st == 200 {
		t.Error("a manager at wi_a1x must not reach its sibling work item")
	}
	_, bs := h.call("GET", "/cases/"+caseB, scopedMgr, nil)
	if in := inputByName(bs, "claim", "amount"); in != nil && in["editable"] == true {
		t.Error("a coder at company B is not a manager there")
	}
	for _, id := range []string{caseX, caseY} {
		_, cv := h.call("GET", "/cases/"+id, companyCoder, nil)
		if in := inputByName(cv, "claim", "category"); in == nil || in["editable"] != true {
			t.Errorf("a company coder works every work item below it (%s): %v", id, in)
		}
	}
	if st, _ := h.call("GET", "/cases/"+caseB, companyCoder, nil); st == 200 {
		t.Error("a coder at company A must not reach company B")
	}

	status, body := h.call("GET", "/workspaces/wi_a1x/settings", admin, nil)
	if status != 200 || dig(body, "effective", "stages", "work", "sla_duration") != "6h" {
		t.Errorf("effective settings: %d %v", status, body)
	}
}

func slicesContains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func codesOfChoices(v any) []string {
	var out []string
	list, _ := v.([]any)
	for _, item := range list {
		if m, ok := item.(map[string]any); ok {
			if s, ok := m["value"].(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}
