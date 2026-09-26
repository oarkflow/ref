package platform

import (
	"errors"
	"testing"

	"github.com/oarkflow/ref/intent"
	"github.com/oarkflow/ref/invocation"
)

// TestOpsGuardRequiresPrincipal pins the operations guard: without roles an
// outside caller must still be authenticated; platform-started work is trusted.
func TestOpsGuardRequiresPrincipal(t *testing.T) {
	category := func(err error) intent.Category {
		var f intent.Failure
		if errors.As(err, &f) {
			return f.Category
		}
		return 0
	}
	http := &invocation.Invocation{Transport: invocation.Transport{Protocol: "http"}}
	anon := &ActionContext{Invocation: http}
	if err := requireOperator(anon, nil, "x"); category(err) != intent.CategoryAuth {
		t.Fatalf("anonymous http caller: %v", err)
	}
	if err := requireOperator(&ActionContext{}, nil, "x"); category(err) != intent.CategoryAuth {
		t.Fatalf("anonymous caller without an invocation: %v", err)
	}
	user := &ActionContext{Invocation: http, Principal: Principal{ID: "u1"}}
	if err := requireOperator(user, nil, "x"); err != nil {
		t.Fatalf("authenticated caller without roles: %v", err)
	}
	if err := requireOperator(user, []string{"ops"}, "x"); category(err) != intent.CategoryPermission {
		t.Fatalf("caller without the role: %v", err)
	}
	user.Principal.Roles = []string{"ops"}
	if err := requireOperator(user, []string{"ops"}, "x"); err != nil {
		t.Fatalf("caller with the role: %v", err)
	}
	for _, proto := range []string{"internal", "queue", "process"} {
		ctx := &ActionContext{Invocation: &invocation.Invocation{Transport: invocation.Transport{Protocol: proto}}}
		if err := requireOperator(ctx, nil, "x"); err != nil {
			t.Fatalf("%s work: %v", proto, err)
		}
	}
}
