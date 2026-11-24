package platform

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

const informationApp = `
name "info"
resource "jwt" {
  kind "auth.jwt"
  config { secret env.required("WS_JWT_SECRET") }
}
resource "cases" {
  kind "pipeline.cases"
  config { }
}
pipeline "enc" {
  info_roles ["coder"]
  information {
    to ["creator", "previous", "user"]
    due "24h"
    fields true
  }
  form "f" {
    input "a" { kind text }
    input "mrn" { kind text  pattern "^M[0-9]+$" }
  }
  stage "intake" {
    roles ["nurse"]
    page { group "g" { forms ["f"] } }
    action "send" { outcome advance }
  }
  stage "coding" {
    roles ["coder"]
    sla { duration "2h" }
    page { group "g" { forms ["f"] } }
    action "done" { outcome advance }
  }
  stage "end" { roles ["coder"] }
}
intent "start" { response "r"  node "r" { uses "pipeline.start"  resource "cases"  kind effect  requires [input]  provides [r] } }
intent "act" { response "r"  node "r" { uses "pipeline.act"  resource "cases"  kind effect  requires [input]  provides [r] } }
intent "get" { response "r"  node "r" { uses "pipeline.overview"  resource "cases"  requires [input]  provides [r] } }
intent "list" { response "r"  node "r" { uses "pipeline.list"  resource "cases"  requires [input]  provides [r] } }
intent "ask" { response "r"  node "r" { uses "pipeline.request_information"  resource "cases"  kind effect  requires [input]  provides [r] } }
intent "answer" { response "r"  node "r" { uses "pipeline.submit_information"  resource "cases"  kind effect  requires [input]  provides [r] } }
route "start" { method POST  path "/cases"  intent "start"  auth "jwt"  status 201 }
route "act" { method POST  path "/cases/:id/stages/:stage/actions/:action"  intent "act"  auth "jwt" }
route "get" { method GET  path "/cases/:id/overview"  intent "get"  auth "jwt" }
route "list" { method GET  path "/cases"  intent "list"  auth "jwt" }
route "ask" { method POST  path "/cases/:id/information-request"  intent "ask"  auth "jwt" }
route "answer" { method POST  path "/cases/:id/information-response"  intent "answer"  auth "jwt" }
`

func TestQuestionsPutToARoleCanBeFoundAndAnsweredByThatRole(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.bcl")
	if err := os.WriteFile(path, []byte(informationApp), 0o600); err != nil {
		t.Fatal(err)
	}
	h := newAppHarness(t, path, map[string]string{"WS_JWT_SECRET": "ws-test-secret-0123456789abcdef-xyz"})
	n1 := h.token("jwt", "n1", []string{"nurse"}, nil)
	n2 := h.token("jwt", "n2", []string{"nurse"}, nil)
	coder := h.token("jwt", "c1", []string{"coder"}, nil)

	_, body := h.call("POST", "/cases", n1, map[string]any{"data": map[string]any{}})
	id := fmt.Sprint(dig(body, "case", "id"))
	if status, r := h.call("POST", "/cases/"+id+"/stages/intake/actions/send", n1, map[string]any{}); status != 200 {
		t.Fatalf("send: %d %v", status, r)
	}
	// The coder sees which targets the stage allows.
	_, ov := h.call("GET", "/cases/"+id+"/overview", coder, nil)
	if targets, _ := dig(ov, "work", "info_targets").([]any); len(targets) != 3 {
		t.Fatalf("targets offered: %v", dig(ov, "work", "info_targets"))
	}
	if status, r := h.call("POST", "/cases/"+id+"/information-request", coder, map[string]any{"reason": "recheck the dates", "to": "previous"}); status != 200 {
		t.Fatalf("ask: %d %v", status, r)
	}

	// Another nurse (not the creator) can find the question, open the case, and answer it; the coder cannot.
	_, rows := h.call("GET", "/cases?scope=answer&pipeline=enc", n2, nil)
	if list, _ := rows.([]any); len(list) != 1 {
		t.Fatalf("questions for the nurse: %v", rows)
	}
	if _, rows := h.call("GET", "/cases?scope=answer&pipeline=enc", coder, nil); len(asSlice(rows)) != 0 {
		t.Fatalf("the asker has no question to answer: %v", rows)
	}
	_, ov = h.call("GET", "/cases/"+id+"/overview", n2, nil)
	if dig(ov, "information_can_answer") != true || dig(ov, "work", "sla", "status") != "paused" {
		t.Fatalf("the nurse can answer and the clock is stopped: %v", ov)
	}
	if status, _ := h.call("POST", "/cases/"+id+"/information-response", coder, map[string]any{"response": map[string]any{"answer": "x"}}); status != 403 {
		t.Fatalf("the coder may not answer a question put to nurses: %d", status)
	}
	if status, r := h.call("POST", "/cases/"+id+"/information-response", n2, map[string]any{"response": map[string]any{"answer": "dates fixed"}}); status != 200 {
		t.Fatalf("answer: %d %v", status, r)
	}
	_, ov = h.call("GET", "/cases/"+id+"/overview", coder, nil)
	if dig(ov, "work", "sla", "status") == "paused" || dig(ov, "information") != nil {
		t.Fatalf("answered: clock restarted and nothing pending: %v", ov)
	}
}

func TestAnswerFieldsAreValidatedAndMergedIntoTheCase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.bcl")
	if err := os.WriteFile(path, []byte(informationApp), 0o600); err != nil {
		t.Fatal(err)
	}
	h := newAppHarness(t, path, map[string]string{"WS_JWT_SECRET": "ws-test-secret-0123456789abcdef-xyz"})
	n1 := h.token("jwt", "n1", []string{"nurse"}, nil)
	coder := h.token("jwt", "c1", []string{"coder"}, nil)
	_, body := h.call("POST", "/cases", n1, map[string]any{"data": map[string]any{}})
	id := fmt.Sprint(dig(body, "case", "id"))
	h.call("POST", "/cases/"+id+"/stages/intake/actions/send", n1, map[string]any{})
	if status, r := h.call("POST", "/cases/"+id+"/information-request", coder, map[string]any{"reason": "confirm the MRN", "fields": []string{"f.mrn"}}); status != 200 {
		t.Fatalf("ask: %d %v", status, r)
	}
	if status, _ := h.call("POST", "/cases/"+id+"/information-response", n1, map[string]any{"response": map[string]any{"answer": "see chart"}}); status != 422 {
		t.Fatalf("an answer without the input asked for is incomplete: %d", status)
	}
	if status, _ := h.call("POST", "/cases/"+id+"/information-response", n1, map[string]any{"response": map[string]any{"data": map[string]any{"f.mrn": "bad"}}}); status != 422 {
		t.Fatalf("the value is validated like any entry: %d", status)
	}
	if status, r := h.call("POST", "/cases/"+id+"/information-response", n1, map[string]any{"response": map[string]any{"data": map[string]any{"f.mrn": "M77"}}}); status != 200 {
		t.Fatalf("answer: %d %v", status, r)
	}
	_, ov := h.call("GET", "/cases/"+id+"/overview", coder, nil)
	found := false
	for _, f := range asSlice(dig(ov, "fields")) {
		if dig(f, "path") == "f.mrn" && dig(f, "value") == "M77" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the answer is in the case data: %v", dig(ov, "fields"))
	}
}
