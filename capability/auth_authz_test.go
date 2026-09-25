package capability_test

import (
	"context"
	"errors"
	"testing"

	"github.com/oarkflow/authz"
	authzStores "github.com/oarkflow/authz/pkg/stores"

	"github.com/oarkflow/ref/capability"
	"github.com/oarkflow/ref/execution"
	"github.com/oarkflow/ref/fact"
	"github.com/oarkflow/ref/invocation"
)

func newTestAuthzEngine(t *testing.T) *authz.Engine {
	t.Helper()
	engine := authz.NewEngine(
		authzStores.NewMemoryPolicyStore(),
		authzStores.NewMemoryRoleStore(),
		authzStores.NewMemoryACLStore(),
		authzStores.NewMemoryAuditStore(),
	)

	role := &authz.Role{
		ID:       "editor",
		TenantID: "default",
		Name:     "Editor",
		Permissions: []authz.Permission{
			{Action: "read", Resource: "document:*"},
			{Action: "write", Resource: "document:*"},
		},
	}
	if err := engine.CreateRole(context.Background(), role); err != nil {
		t.Fatalf("create role: %v", err)
	}
	return engine
}

func TestNewAuthzAuthenticator_ResolvesPrincipal(t *testing.T) {
	engine := newTestAuthzEngine(t)

	auth := capability.NewAuthzAuthenticator(engine, capability.AuthzAuthenticatorConfig{
		TenantID: "default",
		ResolvePrincipal: func(hint invocation.PrincipalHint) (capability.PrincipalFact, error) {
			if hint.APIKey != "valid-key" {
				return capability.PrincipalFact{}, errors.New("bad api key")
			}
			return capability.PrincipalFact{ID: "user-1", Roles: []string{"editor"}}, nil
		},
	})

	principal, err := auth(invocation.NewPrincipalHint("", "valid-key", nil, ""))
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if principal.ID != "user-1" {
		t.Errorf("expected ID user-1, got %q", principal.ID)
	}

	_, err = auth(invocation.NewPrincipalHint("", "bad-key", nil, ""))
	if err == nil {
		t.Fatal("expected failure for bad api key")
	}
}

func TestNewAuthzPolicyCapability_AllowsPermittedAction(t *testing.T) {
	engine := newTestAuthzEngine(t)

	reg := capability.NewAuthzPolicyCapability("authz.check", engine,
		func(nc *execution.NodeContext) (string, string, error) {
			return "document:*", "read", nil
		},
		capability.AuthzPolicyConfig{TenantID: "default"},
	)

	defToSlot := map[fact.DefinitionID]fact.PlanSlot{
		capability.PrincipalKey.DefinitionID(): 0,
	}
	facts := fact.NewStore(1)
	decisions := execution.NewDecisionSet()
	inv := &invocation.Invocation{ID: "inv-authz-allow"}

	nc := execution.NewNodeContext(context.Background(), inv, facts, nil, decisions, 0, defToSlot, nil, 0)
	execution.Publish(nc, capability.PrincipalKey, capability.PrincipalFact{
		ID:    "user-1",
		Roles: []string{"editor"},
	})

	if err := reg.Run(nc); err != nil {
		t.Fatalf("expected allow, got error: %v", err)
	}
}

func TestNewAuthzPolicyCapability_DeniesUnpermittedAction(t *testing.T) {
	engine := newTestAuthzEngine(t)

	reg := capability.NewAuthzPolicyCapability("authz.check", engine,
		func(nc *execution.NodeContext) (string, string, error) {
			return "document:*", "delete", nil
		},
		capability.AuthzPolicyConfig{TenantID: "default"},
	)

	defToSlot := map[fact.DefinitionID]fact.PlanSlot{
		capability.PrincipalKey.DefinitionID(): 0,
	}
	facts := fact.NewStore(1)
	decisions := execution.NewDecisionSet()
	inv := &invocation.Invocation{ID: "inv-authz-deny"}

	nc := execution.NewNodeContext(context.Background(), inv, facts, nil, decisions, 0, defToSlot, nil, 0)
	execution.Publish(nc, capability.PrincipalKey, capability.PrincipalFact{
		ID:    "user-1",
		Roles: []string{"editor"},
	})

	err := reg.Run(nc)
	if err == nil {
		t.Fatal("expected deny for unpermitted action")
	}
	if !errors.Is(err, capability.ErrUnauthorized) {
		t.Errorf("expected error to wrap ErrUnauthorized, got %v", err)
	}
}

func TestNewAuthzPolicyCapability_SuperuserBypasses(t *testing.T) {
	engine := newTestAuthzEngine(t)

	reg := capability.NewAuthzPolicyCapability("authz.check", engine,
		func(nc *execution.NodeContext) (string, string, error) {
			return "document:*", "delete", nil
		},
		capability.AuthzPolicyConfig{TenantID: "default", SuperuserRoles: []string{"admin"}},
	)

	defToSlot := map[fact.DefinitionID]fact.PlanSlot{
		capability.PrincipalKey.DefinitionID(): 0,
	}
	facts := fact.NewStore(1)
	decisions := execution.NewDecisionSet()
	inv := &invocation.Invocation{ID: "inv-authz-superuser"}

	nc := execution.NewNodeContext(context.Background(), inv, facts, nil, decisions, 0, defToSlot, nil, 0)
	execution.Publish(nc, capability.PrincipalKey, capability.PrincipalFact{
		ID:    "root",
		Roles: []string{"admin"},
	})

	if err := reg.Run(nc); err != nil {
		t.Fatalf("expected superuser bypass to allow, got error: %v", err)
	}
}

func TestNewAuthzPolicyCapability_MissingPrincipalDenied(t *testing.T) {
	engine := newTestAuthzEngine(t)

	reg := capability.NewAuthzPolicyCapability("authz.check", engine,
		func(nc *execution.NodeContext) (string, string, error) {
			return "document:*", "read", nil
		},
		capability.AuthzPolicyConfig{TenantID: "default"},
	)

	defToSlot := map[fact.DefinitionID]fact.PlanSlot{
		capability.PrincipalKey.DefinitionID(): 0,
	}
	facts := fact.NewStore(1)
	decisions := execution.NewDecisionSet()
	inv := &invocation.Invocation{ID: "inv-authz-missing"}

	nc := execution.NewNodeContext(context.Background(), inv, facts, nil, decisions, 0, defToSlot, nil, 0)

	if err := reg.Run(nc); err == nil {
		t.Fatal("expected error when principal fact is missing")
	}
}
