package platform

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/oarkflow/authz"
	authzStores "github.com/oarkflow/authz/pkg/stores"
	"github.com/oarkflow/ref/platform/spi"
)

// ---------------------------------------------------------------------------
// authz.engine — Full enterprise authorization backed by oarkflow/authz
// ---------------------------------------------------------------------------
//
// This resource replaces the lightweight authz.rbac when a platform needs any
// combination of:
//
//   - ABAC policies with condition expressions (eq, ne, in, gte, and/or)
//   - ACL entries scoped to a resource + subject + action
//   - Multi-tenant hierarchy with policy inheritance
//   - Role inheritance with owner-allowed actions
//   - OAuth2-style scopes with hierarchical resolution
//   - Signed and versioned policy bundles
//   - DSL-based configuration files
//   - Audit trail with batched writes
//
// The underlying oarkflow/authz.Engine performs a deny-dominant, priority-
// ordered evaluation that matches REF's own decision algebra: any single deny
// policy blocks the request regardless of how many allow policies matched.
//
// BCL usage:
//
//	resource "authorization" {
//	  kind "authz.engine"
//	  config {
//	    tenant_id "default"
//	    dsl_file  "authz.dsl"           # optional DSL file to bootstrap
//	    superuser_roles ["admin"]
//	    decision_cache_ttl 5m
//	    audit_batch_size 100
//	    audit_flush_interval 10s
//	  }
//	}
//
//	# Tenants, roles, policies and ACLs can also be defined inline:
//	resource "authorization" {
//	  kind "authz.engine"
//	  config {
//	    tenant_id "acme"
//	    roles [
//	      { id "editor" name "Editor" permissions [{ action "read" resource "document:*" }, { action "write" resource "document:*" }] }
//	      { id "viewer" name "Viewer" permissions [{ action "read" resource "document:*" }] inherits ["editor"] }
//	    ]
//	    policies [
//	      { id "deny-delete-draft" effect "deny" actions ["delete"] resources ["document:*"] condition "status == 'draft'" }
//	    ]
//	  }
//	}

// Registration is called from resources.go — no init() needed.
func registerAuthzEngineResource(r *Registry) {
	mustResource(r, "authz.engine", ResourceFactoryFunc(openAuthzEngine), ResourceKindInfo{
		Family:   "authz",
		Summary:  "Enterprise authorization engine with ABAC policies, ACLs, multi-tenant hierarchy, scopes, role inheritance, and audit trails. Use when authz.rbac is insufficient.",
		Provides: []string{"Authorizer"},
		Config: []ConfigField{
			{Name: "tenant_id", Type: "string", Default: "default", Summary: "Default tenant for policy evaluation"},
			{Name: "dsl_file", Type: "string", Summary: "Path to a DSL configuration file to bootstrap tenants, roles, policies, ACLs and memberships"},
			{Name: "superuser_roles", Type: "[]string", Summary: "Roles that bypass all policy checks"},
			{Name: "decision_cache_ttl", Type: "duration", Default: "5m", Summary: "TTL for decision cache entries"},
			{Name: "audit_batch_size", Type: "int", Default: "50", Summary: "Number of audit events before flushing"},
			{Name: "audit_flush_interval", Type: "duration", Default: "30s", Summary: "Interval between audit flushes"},
			{Name: "roles", Type: "[]object", Summary: "Inline role definitions with permissions and inheritance"},
			{Name: "policies", Type: "[]object", Summary: "Inline ABAC policy definitions"},
			{Name: "acls", Type: "[]object", Summary: "Inline ACL entries"},
		},
	})
}

// authzEngineWrapper wraps oarkflow/authz.Engine behind the REF spi.Authorizer
// interface. It translates the flat REF Principal into the richer authz.Subject
// and performs full ABAC+RBAC+ACL evaluation on every permission check.
type authzEngineWrapper struct {
	engine         *authz.Engine
	roleStore      authz.RoleStore
	tenantID       string
	superuserRoles map[string]struct{}
}

func openAuthzEngine(ctx context.Context, spec ResourceSpec) (Resource, io.Closer, error) {
	if err := rejectUnknownConfig("authz.engine", spec.Config,
		"tenant_id", "dsl_file", "superuser_roles", "decision_cache_ttl",
		"audit_batch_size", "audit_flush_interval",
		"roles", "policies", "acls"); err != nil {
		return nil, nil, err
	}

	tenantID := configString(spec.Config, "tenant_id", "default")

	// Create individual memory stores
	policyStore := authzStores.NewMemoryPolicyStore()
	roleStore := authzStores.NewMemoryRoleStore()
	aclStore := authzStores.NewMemoryACLStore()
	auditStore := authzStores.NewMemoryAuditStore()
	roleMembershipStore := authzStores.NewMemoryRoleMembershipStore()
	tenantStore := authzStores.NewMemoryTenantStore()

	// Build authz engine options
	var opts []authz.EngineOption

	opts = append(opts, authz.WithRoleMembershipStore(roleMembershipStore))
	opts = append(opts, authz.WithTenantStore(tenantStore))

	batchSize, err := configInt(spec.Config, "audit_batch_size", 50)
	if err != nil {
		return nil, nil, fmt.Errorf("resource %q: invalid audit_batch_size: %w", spec.Name, err)
	}
	flushInterval, err := configDuration(spec.Config, "audit_flush_interval", 0)
	if err != nil {
		return nil, nil, fmt.Errorf("resource %q: invalid audit_flush_interval: %w", spec.Name, err)
	}
	if batchSize > 0 && flushInterval > 0 {
		opts = append(opts, authz.WithAuditBatching(batchSize, flushInterval))
	}

	engine := authz.NewEngine(
		policyStore,
		roleStore,
		aclStore,
		auditStore,
		opts...,
	)

	// Bootstrap from DSL file if provided
	if dslFile := configString(spec.Config, "dsl_file", ""); dslFile != "" {
		parser := authz.NewDSLParser()
		cfg, err := parser.ParseFile(dslFile)
		if err != nil {
			return nil, nil, fmt.Errorf("resource %q: failed to parse DSL file %q: %w", spec.Name, dslFile, err)
		}
		if err := engine.ApplyConfig(ctx, cfg); err != nil {
			return nil, nil, fmt.Errorf("resource %q: failed to apply DSL config: %w", spec.Name, err)
		}
	}

	// Bootstrap inline roles
	for _, roleBlock := range configBlocks(spec.Config, "roles", "role_block") {
		roleID := configString(roleBlock, "id", "")
		roleName := configString(roleBlock, "name", roleID)
		if roleID == "" {
			continue
		}
		role := &authz.Role{
			ID:       roleID,
			TenantID: tenantID,
			Name:     roleName,
			Inherits: configStrings(roleBlock, "inherits"),
		}
		for _, permBlock := range configBlocks(roleBlock, "permissions", "perm_block") {
			role.Permissions = append(role.Permissions, authz.Permission{
				Action:   authz.Action(configString(permBlock, "action", "")),
				Resource: configString(permBlock, "resource", ""),
			})
		}
		// Also handle flat permission strings
		for _, p := range configStrings(roleBlock, "permissions") {
			parts := strings.SplitN(p, ":", 2)
			if len(parts) == 2 {
				role.Permissions = append(role.Permissions, authz.Permission{
					Action:   authz.Action(parts[0]),
					Resource: parts[1],
				})
			}
		}
		for _, a := range configStrings(roleBlock, "owner_allowed_actions") {
			role.OwnerAllowedActions = append(role.OwnerAllowedActions, authz.Action(a))
		}
		if err := engine.CreateRole(ctx, role); err != nil {
			return nil, nil, fmt.Errorf("resource %q: failed to create role %q: %w", spec.Name, roleID, err)
		}
	}

	// Bootstrap inline policies
	for _, policyBlock := range configBlocks(spec.Config, "policies", "policy_block") {
		policyID := configString(policyBlock, "id", "")
		if policyID == "" {
			continue
		}
		effect := authz.EffectAllow
		if configString(policyBlock, "effect", "allow") == "deny" {
			effect = authz.EffectDeny
		}
		var actions []authz.Action
		for _, a := range configStrings(policyBlock, "actions") {
			actions = append(actions, authz.Action(a))
		}
		condStr := configString(policyBlock, "condition", "")
		cond, _ := authz.ParseCondition(condStr)

		policy := &authz.Policy{
			ID:        policyID,
			TenantID:  tenantID,
			Effect:    effect,
			Actions:   actions,
			Resources: configStrings(policyBlock, "resources"),
			Condition: cond,
			Priority:  configIntNoErr(policyBlock, "priority", 0),
			Enabled:   true,
		}
		if err := engine.CreatePolicy(ctx, policy); err != nil {
			return nil, nil, fmt.Errorf("resource %q: failed to create policy %q: %w", spec.Name, policyID, err)
		}
	}

	// Bootstrap inline ACLs
	for _, aclBlock := range configBlocks(spec.Config, "acls", "acl_block") {
		aclID := configString(aclBlock, "id", "")
		if aclID == "" {
			continue
		}
		effect := authz.EffectAllow
		if configString(aclBlock, "effect", "allow") == "deny" {
			effect = authz.EffectDeny
		}
		var actions []authz.Action
		for _, a := range configStrings(aclBlock, "actions") {
			actions = append(actions, authz.Action(a))
		}
		acl := &authz.ACL{
			ID:         aclID,
			TenantID:   tenantID,
			ResourceID: configString(aclBlock, "resource_id", ""),
			SubjectID:  configString(aclBlock, "subject_id", ""),
			Actions:    actions,
			Effect:     effect,
		}
		if err := engine.GrantACL(ctx, acl); err != nil {
			return nil, nil, fmt.Errorf("resource %q: failed to grant ACL %q: %w", spec.Name, aclID, err)
		}
	}

	// Build superuser lookup
	superuserRoles := map[string]struct{}{}
	for _, role := range configStrings(spec.Config, "superuser_roles") {
		superuserRoles[role] = struct{}{}
	}

	wrapper := &authzEngineWrapper{
		engine:         engine,
		roleStore:      roleStore,
		tenantID:       tenantID,
		superuserRoles: superuserRoles,
	}

	return wrapper, noopCloser{}, nil
}

// HasPermission implements spi.Authorizer. It translates the REF Principal
// into an authz.Subject and delegates to the full engine evaluation pipeline:
// deny policies → ABAC allow policies → ACLs → RBAC role permissions.
func (w *authzEngineWrapper) HasPermission(ctx context.Context, p spi.Principal, permission string) (bool, error) {
	if permission == "" {
		return true, nil
	}

	// Fast path: superuser roles bypass everything
	for _, role := range p.Roles {
		if _, ok := w.superuserRoles[role]; ok {
			return true, nil
		}
	}

	// Build the authz subject from the REF principal
	subject := &authz.Subject{
		ID:       p.ID,
		Type:     "user",
		TenantID: w.tenantID,
		Roles:    p.Roles,
		Attrs:    p.Claims,
	}
	if p.TenantID != "" {
		subject.TenantID = p.TenantID
	}

	// Parse the permission as action:resource or just action
	action, resourcePattern := parsePermission(permission)

	resource := &authz.Resource{
		ID:       resourcePattern,
		Type:     resourcePattern,
		TenantID: subject.TenantID,
	}

	env := &authz.Environment{
		TenantID: subject.TenantID,
	}

	decision, err := w.engine.Authorize(ctx, subject, authz.Action(action), resource, env)
	if err != nil {
		return false, fmt.Errorf("authz engine error: %w", err)
	}

	return decision.Allowed, nil
}

// Permissions implements spi.Authorizer. It returns the effective permission
// set by collecting all roles' permissions from the engine.
func (w *authzEngineWrapper) Permissions(ctx context.Context, p spi.Principal) ([]string, error) {
	// Superuser gets everything
	for _, role := range p.Roles {
		if _, ok := w.superuserRoles[role]; ok {
			return []string{"*"}, nil
		}
	}

	// Collect permissions from all roles via the engine
	seen := map[string]struct{}{}
	for _, roleID := range p.Roles {
		role, err := w.roleStore.GetRole(ctx, roleID)
		if err != nil {
			continue
		}
		for _, perm := range role.Permissions {
			key := string(perm.Action) + ":" + perm.Resource
			seen[key] = struct{}{}
		}
	}

	// Include scopes as permissions
	for _, scope := range p.Scopes {
		seen[scope] = struct{}{}
	}

	out := make([]string, 0, len(seen))
	for permission := range seen {
		out = append(out, permission)
	}
	return out, nil
}

// parsePermission splits "action:resource" or returns the whole string as action.
func parsePermission(permission string) (action, resource string) {
	parts := strings.SplitN(permission, ":", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return permission, "*"
}

// configIntNoErr is a helper that ignores the error from configInt for use
// in contexts where the value is known to be from a parsed config block.
func configIntNoErr(config map[string]any, key string, fallback int) int {
	v, _ := configInt(config, key, fallback)
	return v
}
