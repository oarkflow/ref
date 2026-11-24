package platform

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const limitsApp = `
name "lim"
resource "jwt" {
  kind "auth.jwt"
  config { secret env.required("WS_JWT_SECRET") }
}
resource "cases" {
  kind "pipeline.cases"
  config { }
}
pipeline "enc" {
  limits { per_user 2  count "open" }
  form "f" { input "a" { kind text } }
  stage "intake" {
    roles ["nurse"]
    require_start true
    page { group "g" { forms ["f"] } }
    action "send" { outcome advance }
  }
  stage "coding" {
    roles ["coder"]
    require_start true
    page { group "g" { forms ["f"] } }
    action "done" { outcome advance }
  }
  stage "end" { roles ["coder"] }
}
intent "start" { response "r"  node "r" { uses "pipeline.start"  resource "cases"  kind effect  requires [input]  provides [r] } }
intent "act" { response "r"  node "r" { uses "pipeline.act"  resource "cases"  kind effect  requires [input]  provides [r] } }
intent "list" { response "r"  node "r" { uses "pipeline.list"  resource "cases"  requires [input]  provides [r] } }
intent "work" { response "r"  node "r" { uses "pipeline.work"  resource "cases"  kind effect  requires [input]  provides [r] } }
route "start" { method POST  path "/cases"  intent "start"  auth "jwt"  status 201 }
route "act" { method POST  path "/cases/:id/stages/:stage/actions/:action"  intent "act"  auth "jwt" }
route "list" { method GET  path "/cases"  intent "list"  auth "jwt" }
route "work" { method POST  path "/cases/:id/stages/:stage/work/:op"  intent "work"  auth "jwt" }
`

func TestCreationLimitAndStartingFromTheWorkList(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.bcl")
	if err := os.WriteFile(path, []byte(limitsApp), 0o600); err != nil {
		t.Fatal(err)
	}
	h := newAppHarness(t, path, map[string]string{"WS_JWT_SECRET": "ws-test-secret-0123456789abcdef-xyz"})
	nurse := h.token("jwt", "n1", []string{"nurse"}, nil)
	coder := h.token("jwt", "c1", []string{"coder"}, nil)
	start := func() (int, any) { return h.call("POST", "/cases", nurse, map[string]any{"data": map[string]any{}}) }

	ids := []string{}
	for i := 0; i < 2; i++ {
		status, body := start()
		if status != 201 {
			t.Fatalf("case %d: %d %v", i+1, status, body)
		}
		ids = append(ids, fmt.Sprint(dig(body, "case", "id")))
	}
	// The creator has already started their data: no second Start on the stage they created it in.
	if status, r := h.call("POST", "/cases/"+ids[0]+"/stages/intake/work/start", nurse, map[string]any{}); status == 200 {
		t.Fatalf("starting data that is already started must be refused, got %v", r)
	}
	if status, r := h.call("POST", "/cases/"+ids[0]+"/stages/intake/actions/send", nurse, map[string]any{"data": map[string]any{"f": map[string]any{"a": "x"}}}); status != 200 {
		t.Fatalf("send without a second Start: %d %v", status, r)
	}

	// The limit counts unfinished cases: a third is refused, with a reason a person can act on.
	status, body := start()
	if status < 400 || !strings.Contains(fmt.Sprint(body), "limit of 2") {
		t.Fatalf("the third case must be refused with the limit: %d %v", status, body)
	}

	// The coding step lists what was entered and offers Start for each; starting is how work begins.
	_, rows := h.call("GET", "/cases?scope=queue&pipeline=enc", coder, nil)
	list, _ := rows.([]any)
	if len(list) != 1 || dig(list[0], "can_start") != true || dig(list[0], "stage") != "coding" {
		t.Fatalf("coding queue: %v", rows)
	}
	if status, r := h.call("POST", "/cases/"+ids[0]+"/stages/coding/actions/done", coder, map[string]any{}); status == 200 {
		t.Fatalf("acting before Start must be refused: %v", r)
	}
	if status, r := h.call("POST", "/cases/"+ids[0]+"/stages/coding/work/start", coder, map[string]any{}); status != 200 {
		t.Fatalf("start from the list: %d %v", status, r)
	}
	_, rows = h.call("GET", "/cases?scope=queue&pipeline=enc", coder, nil)
	list, _ = rows.([]any)
	if len(list) != 1 || dig(list[0], "can_start") == true {
		t.Fatalf("a started case is not offered for Start again: %v", rows)
	}
}
