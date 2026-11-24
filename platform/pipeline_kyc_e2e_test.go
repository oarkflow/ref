package platform

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const kycApp = `
name "kyc"

resource "jwt" {
  kind "auth.jwt"
  config {
    secret env.required("KYC_JWT_SECRET")
  }
}

resource "cases" {
  kind "pipeline.cases"
  config {
    allow_anonymous false
    __LIVENESS__
  }
}

pipeline "onboard" {
  form "who" {
    input "name" {
      kind text
      required true
    }
    input "id_photo" {
      kind text
    }
  }
  stage "details" {
    public true
    page {
      group "g" {
        forms ["who"]
      }
    }
    route "small" {
      when "who.name == 'Skip Liveness'"
      to "end"
    }
  }
  stage "kyc" {
    public true
    auto_advance true
    node "live" {
      kind liveness
      challenges 2
      match_with "who.id_photo"
      min_score 0.8
      min_match 0.5
    }
  }
}

intent "start" {
  response "case"
  node "case" {
    uses "pipeline.start"
    resource "cases"
    kind effect
    requires [input]
    provides [case]
  }
}
intent "act" {
  response "v"
  node "v" {
    uses "pipeline.act"
    resource "cases"
    kind effect
    requires [input]
    provides [v]
  }
}
intent "node" {
  response "v"
  node "v" {
    uses "pipeline.node"
    resource "cases"
    kind effect
    requires [input]
    provides [v]
  }
}

route "start" { method POST path "/cases" intent "start" auth "jwt" status 201 }
route "act" { method POST path "/cases/:id/stages/:stage/actions/:action" intent "act" auth "jwt" }
route "node" { method POST path "/cases/:id/stages/:stage/nodes/:node/:verb" intent "node" auth "jwt" }
`

func writeKYCApp(t *testing.T, liveness string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "app.bcl")
	if err := os.WriteFile(path, []byte(strings.ReplaceAll(kycApp, "__LIVENESS__", liveness)), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPipelineKYCLivenessAndRoutesEndToEnd(t *testing.T) {
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fw-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		switch r.URL.Path {
		case "/v1/pad":
			_ = json.NewEncoder(w).Encode(map[string]any{"score": 0.95})
		case "/v1/challenge":
			res := map[string]bool{}
			for _, a := range body["actions"].([]any) {
				res[a.(string)] = true
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"results": res})
		case "/v1/embed":
			_ = json.NewEncoder(w).Encode(map[string]any{"embedding": []float64{1, 2, 3}})
		}
	}))
	defer worker.Close()

	path := writeKYCApp(t, fmt.Sprintf("liveness_url %q\n    liveness_token env.required(\"KYC_FW_TOKEN\")", worker.URL))
	h := newAppHarness(t, path, map[string]string{
		"KYC_JWT_SECRET": "kyc-test-secret-0123456789abcdef-xyz",
		"KYC_FW_TOKEN":   "fw-token",
	})
	user := h.token("jwt", "u1", nil, nil)
	start := func(name string) string {
		status, body := h.call("POST", "/cases", user, map[string]any{"data": map[string]any{"who": map[string]any{"name": name, "id_photo": "ref-image"}}})
		if status != 201 {
			t.Fatalf("start: %d %v", status, body)
		}
		return fmt.Sprint(dig(body, "case", "id"))
	}

	// A stage route sends this applicant straight to the end.
	skip := start("Skip Liveness")
	status, body := h.call("POST", "/cases/"+skip+"/stages/details/actions/submit", user, map[string]any{})
	if status != 200 || dig(body, "case", "status") != "completed" {
		t.Fatalf("route to end: %d %v", status, body)
	}

	// Everyone else goes through liveness: challenge, then verify.
	id := start("Asha Rai")
	if status, body := h.call("POST", "/cases/"+id+"/stages/details/actions/submit", user, map[string]any{}); status != 200 {
		t.Fatalf("submit: %d %v", status, body)
	}
	status, body = h.call("POST", "/cases/"+id+"/stages/kyc/nodes/live/challenge", user, map[string]any{})
	if status != 200 {
		t.Fatalf("challenge: %d %v", status, body)
	}
	nonce, _ := dig(body, "nodes", 0, "result", "nonce").(string)
	if nonce == "" {
		t.Fatalf("no nonce in %v", body)
	}
	if status, _ := h.call("POST", "/cases/"+id+"/stages/kyc/nodes/live/verify", user, map[string]any{"result": map[string]any{"nonce": "bad", "frames": []string{"x"}}}); status == 200 {
		t.Fatal("a wrong nonce must be refused")
	}
	status, body = h.call("POST", "/cases/"+id+"/stages/kyc/nodes/live/verify", user, map[string]any{"result": map[string]any{"nonce": nonce, "frames": []string{"f1", "f2", "f3"}}})
	if status != 200 {
		t.Fatalf("verify: %d %v", status, body)
	}
	if dig(body, "nodes", 0, "status") != "passed" || dig(body, "case", "status") != "completed" {
		t.Fatalf("liveness should pass and auto-complete the case: %v", body)
	}
}

func TestPipelineLivenessNodeNeedsAVerifier(t *testing.T) {
	path := writeKYCApp(t, "")
	t.Setenv("KYC_JWT_SECRET", "kyc-test-secret-0123456789abcdef-xyz")
	src, _ := os.ReadFile(path)
	_, err := Compile(t.Context(), src, filepath.Dir(path), DefaultLoadOptions())
	if err == nil || !strings.Contains(err.Error(), "liveness_url") {
		t.Fatalf("want a startup error naming liveness_url, got %v", err)
	}
}
