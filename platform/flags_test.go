package platform

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const flagsApp = `
name "flags"
environment "staging"
flag_store "kv"

resource "kv" {
  kind "cache.memory"
}

resource "jwt" {
  kind "auth.jwt"
  config {
    secret env.required("FLAG_JWT_SECRET")
  }
}

flag "new_checkout" {
  description "Redesigned checkout"
  default false
  rule "staff" {
    roles ["staff"]
    value true
  }
  rule "pilot" {
    tenants ["acme"]
    rollout 50
  }
}

flag "big_orders" {
  default false
  rule "large" {
    condition "input.amount > 1000"
    value true
  }
}

flag "staging_only" {
  rule "env" {
    environments ["staging"]
  }
}

flag "pricing" {
  default "control"
  variant "control" {
    weight 50
  }
  variant "annual" {
    weight 50
  }
}

intent "flags.all" {
  response "flags"
  node "flags" {
    uses "flag.all"
    provides [flags]
  }
}

intent "checkout" {
  response "page"
  node "page" {
    uses "expression"
    requires [input]
    provides [page]
    config {
      expression "flags.new_checkout ? 'new' : 'old'"
    }
  }
}

intent "flags.set" {
  response "flag"
  node "flag" {
    uses "flag.set"
    kind effect
    requires [input]
    provides [flag]
    config {
      roles ["admin"]
    }
  }
}

route "flags" {
  method GET
  path "/api/flags"
  intent "flags.all"
  auth "jwt"
}
route "checkout" {
  method POST
  path "/api/checkout"
  intent "checkout"
  auth "jwt"
}
route "beta" {
  method POST
  path "/api/beta"
  intent "checkout"
  auth "jwt"
  flag "new_checkout"
}
route "set" {
  method POST
  path "/api/flags"
  intent "flags.set"
  auth "jwt"
}
`

func TestFeatureFlags(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.bcl")
	if err := os.WriteFile(path, []byte(flagsApp), 0o600); err != nil {
		t.Fatal(err)
	}
	h := newAppHarness(t, path, map[string]string{"FLAG_JWT_SECRET": "flag-test-secret-0123456789abcdef-xyz"})
	staff := h.token("jwt", "s1", []string{"staff"}, nil)
	admin := h.token("jwt", "root", []string{"admin"}, nil)
	outsider := h.token("jwt", "u1", nil, map[string]any{"tenant_id": "globex"})

	_, body := h.call("GET", "/api/flags", staff, nil)
	if dig(body, "new_checkout") != true || dig(body, "staging_only") != true || dig(body, "big_orders") != false {
		t.Fatalf("staff flags: %v", body)
	}
	if v := dig(body, "pricing"); v != "control" && v != "annual" {
		t.Fatalf("variant: %v", v)
	}
	_, body = h.call("GET", "/api/flags", outsider, nil)
	if dig(body, "new_checkout") != false {
		t.Fatalf("outsider flag: %v", body)
	}

	// Flags in expressions and as route gates.
	if _, body := h.call("POST", "/api/checkout", staff, map[string]any{}); body != "new" {
		t.Fatalf("expression: %v", body)
	}
	if _, body := h.call("POST", "/api/checkout", outsider, map[string]any{}); body != "old" {
		t.Fatalf("expression off: %v", body)
	}
	if status, _ := h.call("POST", "/api/beta", outsider, map[string]any{}); status != 404 {
		t.Fatalf("gated route: %d", status)
	}
	if status, _ := h.call("POST", "/api/beta", staff, map[string]any{}); status != 200 {
		t.Fatalf("gated route on: %d", status)
	}

	// Run-time override: only admins; turns it on for everyone; clears.
	if status, _ := h.call("POST", "/api/flags", staff, map[string]any{"flag": "new_checkout", "value": true}); status != 403 {
		t.Fatalf("staff override: %d", status)
	}
	if status, body := h.call("POST", "/api/flags", admin, map[string]any{"flag": "new_checkout", "value": true, "reason": "launch", "ttl": "1h"}); status != 200 || dig(body, "override", "reason") != "launch" {
		t.Fatalf("override: %d %v", status, body)
	}
	if status, _ := h.call("POST", "/api/beta", outsider, map[string]any{}); status != 200 {
		t.Fatalf("after override: %d", status)
	}
	h.call("POST", "/api/flags", admin, map[string]any{"flag": "new_checkout", "off": true})
	if _, body := h.call("POST", "/api/checkout", staff, map[string]any{}); body != "old" {
		t.Fatalf("kill switch: %v", body)
	}
	h.call("POST", "/api/flags", admin, map[string]any{"flag": "new_checkout", "clear": true})
	if _, body := h.call("POST", "/api/checkout", staff, map[string]any{}); body != "new" {
		t.Fatalf("cleared: %v", body)
	}

	// Rollout is sticky per user and close to its percentage; conditions see input.
	reg := h.platform.flags
	on := 0
	for i := 0; i < 2000; i++ {
		s := flagSubject{principal: Principal{ID: fmt.Sprintf("user-%d", i)}, tenant: "acme", env: Env{}}
		first, _ := reg.evaluate("new_checkout", s, time.Now())
		again, _ := reg.evaluate("new_checkout", s, time.Now())
		if first.Value != again.Value {
			t.Fatal("rollout is not sticky")
		}
		if first.Value == true {
			on++
		}
	}
	if on < 900 || on > 1100 {
		t.Fatalf("50%% rollout gave %d/2000", on)
	}
	res, _ := reg.evaluate("big_orders", flagSubject{env: Env{"input": map[string]any{"amount": 5000}}}, time.Now())
	if res.Value != true || res.Rule != "large" {
		t.Fatalf("condition: %+v", res)
	}
}
