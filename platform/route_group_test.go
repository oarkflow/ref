package platform

import (
	"context"
	"testing"
)

const routeGroupApp = `
name "route-group-app"
role "admin" { permissions ["*"] }
role "user" { permissions ["self:read"] }
resource "authorization" {
  kind "authz.rbac"
  config { superuser_roles ["admin"] default_roles ["user"] }
}
resource "db" {
  kind "database.sql"
  config { driver "sqlite" dsn env.required("RG_DSN") }
}
resource "sessions" { kind "session.memory" config { secret "01234567890123456789012345678901" } }
resource "session_auth" {
  kind "auth.session"
  config { session "sessions" roles_claim "roles" }
}
intent "ping" {
  response "out"
  node "out" { uses "collect" requires [input] provides [out] config { unwrap true } }
}
route_group "api" {
  prefix "/api/v1"
  session "sessions"
  auth "session_auth"
  authz { roles ["user"] authorizer "authorization" }
  cache_control "no-store"
  route "ping-a" { method GET path "/a" intent "ping" }
  route "ping-b" {
    method GET
    path "/b"
    intent "ping"
    # overrides the group's authz with an open route.
    authz { roles ["admin"] authorizer "authorization" }
  }
}
route "ping-top" { method GET path "/ping" intent "ping" }
`

func TestRouteGroupExpandsPrefixAndInheritsDefaults(t *testing.T) {
	t.Setenv("RG_DSN", "file:"+t.TempDir()+"/rg.db")
	p, err := Compile(context.Background(), []byte(routeGroupApp), "", DefaultLoadOptions())
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	defer p.Close()

	byName := map[string]RouteSpec{}
	for _, r := range p.Document.Routes {
		byName[r.Name] = r
	}
	if len(byName) != 3 {
		t.Fatalf("expected 3 routes (2 grouped + 1 top-level), got %d: %+v", len(byName), byName)
	}

	a, ok := byName["ping-a"]
	if !ok {
		t.Fatal("ping-a missing")
	}
	if a.Path != "/api/v1/a" {
		t.Fatalf("ping-a path = %q, want /api/v1/a", a.Path)
	}
	if a.CacheControl != "no-store" {
		t.Fatalf("ping-a should inherit the group's cache_control, got %q", a.CacheControl)
	}
	if a.Authz == nil || len(a.Authz.Roles) != 1 || a.Authz.Roles[0] != "user" {
		t.Fatalf("ping-a should inherit the group's authz, got %+v", a.Authz)
	}

	b, ok := byName["ping-b"]
	if !ok {
		t.Fatal("ping-b missing")
	}
	if b.Path != "/api/v1/b" {
		t.Fatalf("ping-b path = %q, want /api/v1/b", b.Path)
	}
	if b.Authz == nil || len(b.Authz.Roles) != 1 || b.Authz.Roles[0] != "admin" {
		t.Fatalf("ping-b's own authz should win over the group's, got %+v", b.Authz)
	}

	top, ok := byName["ping-top"]
	if !ok {
		t.Fatal("ping-top missing")
	}
	if top.Path != "/ping" || top.Authz != nil {
		t.Fatalf("a top-level route outside any group must be untouched, got %+v", top)
	}
}

func TestRouteGroupRejectsDuplicateRouteName(t *testing.T) {
	src := `
name "dup"
resource "db" { kind "database.sql" config { driver "sqlite" dsn env.required("RG_DSN2") } }
intent "ping" { response "out" node "out" { uses "collect" requires [input] provides [out] } }
route "ping" { method GET path "/ping" intent "ping" }
route_group "api" {
  prefix "/api"
  route "ping" { method GET path "/ping" intent "ping" }
}
`
	t.Setenv("RG_DSN2", "file:"+t.TempDir()+"/dup.db")
	if _, err := Compile(context.Background(), []byte(src), "", DefaultLoadOptions()); err == nil {
		t.Fatal("expected a duplicate route name (one top-level, one inside a group) to be rejected")
	}
}
