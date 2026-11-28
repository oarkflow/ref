package platform

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/oarkflow/fh"
	"github.com/oarkflow/ref/intent"
)

// Identity actions over an identity.users resource: sign-in, invitations,
// password changes and tenant administration.
//
// identity.admin is one action with an operation, like org.write, because
// every administrative operation shares the same guard: the caller must
// administer the target tenant, checked against the directory itself rather
// than the roles in their token.

func registerIdentityActions(r *Registry) {
	registerIdentityRegistrationActions(r)
	mustAction(r, "identity.login", ActionFactoryFunc(buildIdentityLogin), ActionInfo{
		Family:       "identity",
		Summary:      "Verify an email and password against identity.users; issue a JWT or establish a server-side session",
		ResourceKind: "identity",
		Provides:     "JWT mode: { token, token_type, expires_at, user, tenant_id, roles, org_unit }; session mode returns user and principal details without a bearer token",
		Kind:         "effect",
		Config: []ConfigField{
			{Name: "email_fact", Type: "fact", Required: true},
			{Name: "password_fact", Type: "fact", Required: true},
			{Name: "tenant_fact", Type: "fact", Required: true, Summary: "Optional when the user belongs to one tenant; use an optional input path"},
			{Name: "code_fact", Type: "fact", Required: true, Summary: "TOTP code; use an optional input path when MFA is not required"},
			{Name: "token_issuer", Type: "resource", Summary: "Required for JWT mode; the auth.jwt resource to sign the login token"},
			{Name: "session", Type: "resource", Summary: "Optional session resource; when set, establish an HttpOnly browser session instead of returning a bearer token"},
			{Name: "ttl", Type: "duration", Summary: "Token lifetime; defaults to the auth.jwt resource's ttl"},
			{Name: "session_fields", Type: "[]object", Summary: "Required in session mode; each object maps key to a principal JSON path using source; set optional true only for claims that may be absent"},
		},
	})
	mustAction(r, "identity.accept_invite", ActionFactoryFunc(buildIdentityAcceptInvite), ActionInfo{
		Family:       "identity",
		Summary:      "Consume a single-use invitation: set the new account's password (or confirm an existing one's) and join the tenant",
		ResourceKind: "identity",
		Kind:         "effect",
		Config: []ConfigField{
			{Name: "token_fact", Type: "fact", Required: true},
			{Name: "password_fact", Type: "fact", Required: true},
			{Name: "name_fact", Type: "fact", Required: true},
		},
	})
	mustAction(r, "identity.change_password", ActionFactoryFunc(buildIdentityChangePassword), ActionInfo{
		Family:       "identity",
		Summary:      "Change the signed-in user's password after verifying the current one",
		ResourceKind: "identity",
		Kind:         "effect",
		Config: []ConfigField{
			{Name: "current_fact", Type: "fact", Required: true},
			{Name: "new_fact", Type: "fact", Required: true},
		},
	})
	mustAction(r, "identity.admin", ActionFactoryFunc(buildIdentityAdmin), ActionInfo{
		Family:       "identity",
		Summary:      "Administer a tenant's users: invite, list_users, list_invitations, get_user, suspend, reactivate, add_membership, change_membership, remove_membership, audit",
		ResourceKind: "identity",
		Kind:         "effect",
		Config: []ConfigField{
			{Name: "operation", Type: "string", Required: true},
			{Name: "tenant_sources", Type: "[]string", Summary: "Fact or request paths checked in order to select the target tenant"},
			{Name: "user_sources", Type: "[]string", Summary: "Fact or request paths checked in order to select the target user"},
			{Name: "tenant_fact", Type: "fact", Summary: "Deprecated single-source alias for tenant_sources"},
			{Name: "user_fact", Type: "fact", Summary: "Deprecated single-source alias for user_sources"},
			{Name: "input_fact", Type: "fact", Required: true, Summary: "Object carrying email, name, roles, org_unit"},
			{Name: "org_resource", Type: "resource", Summary: "org.hierarchy resource: with it, an administrator may place people (invite, add or change a membership) only in units inside their own org scope"},
		},
	})
}

var identityOperations = []string{"invite", "list_users", "list_invitations", "get_user", "suspend", "reactivate",
	"add_membership", "change_membership", "remove_membership", "audit"}

func identityDirectory(build BuildContext, spec NodeSpec) (*IdentityDirectory, error) {
	return requireResource[*IdentityDirectory](build, spec, "an identity.users resource")
}

// optionalFact reads a string by dotted path from the facts and the
// principal, empty when absent.
func optionalFact(ctx *ActionContext, path string) string {
	if path == "" {
		return ""
	}
	value, ok := resolvePath(orgRoot(ctx), path)
	if !ok || value == nil {
		return ""
	}
	return strings.TrimSpace(Stringify(value))
}

// rawFact reads a string without trimming (passwords keep their spaces).
func rawFact(ctx *ActionContext, path string) string {
	value, ok := resolvePath(ctx.Inputs, path)
	if !ok || value == nil {
		return ""
	}
	return Stringify(value)
}

func buildIdentityLogin(build BuildContext, spec NodeSpec) (Action, error) {
	d, err := identityDirectory(build, spec)
	if err != nil {
		return nil, err
	}
	if err := rejectUnknownConfig("identity.login", spec.Config,
		"email_fact", "password_fact", "tenant_fact", "code_fact", "token_issuer", "session", "ttl", "session_fields"); err != nil {
		return nil, fmt.Errorf("node %q: %w", spec.Name, err)
	}
	sessionName := configString(spec.Config, "session", "")
	var issuer *jwtAuth
	if name := configString(spec.Config, "token_issuer", ""); name != "" {
		resolved, ok := build.Resource(name)
		if !ok {
			return nil, fmt.Errorf("node %q: token_issuer %q is not declared", spec.Name, name)
		}
		if issuer, ok = resolved.(*jwtAuth); !ok {
			return nil, fmt.Errorf("node %q: token_issuer %q is not an auth.jwt resource", spec.Name, name)
		}
	}
	if sessionName == "" && issuer == nil {
		return nil, fmt.Errorf("node %q: identity.login requires token_issuer in JWT mode", spec.Name)
	}
	if sessionName == "" && (issuer.signing == nil || !issuer.signing.CanSign()) {
		return nil, fmt.Errorf("node %q: token_issuer must be an auth.jwt resource with a signing key", spec.Name)
	}
	ttl, err := configDuration(spec.Config, "ttl", 0)
	if err != nil {
		return nil, fmt.Errorf("node %q: %w", spec.Name, err)
	}
	if err := exactlyOneOutput(spec); err != nil {
		return nil, err
	}
	emailFact, err := requiredString(spec.Config, "email_fact")
	if err != nil {
		return nil, fmt.Errorf("node %q: %w", spec.Name, err)
	}
	passwordFact, err := requiredString(spec.Config, "password_fact")
	if err != nil {
		return nil, fmt.Errorf("node %q: %w", spec.Name, err)
	}
	tenantFact, err := requiredString(spec.Config, "tenant_fact")
	if err != nil {
		return nil, fmt.Errorf("node %q: %w", spec.Name, err)
	}
	codeFact, err := requiredString(spec.Config, "code_fact")
	if err != nil {
		return nil, fmt.Errorf("node %q: %w", spec.Name, err)
	}
	var sessionFields []identitySessionField
	if sessionName != "" {
		for i, field := range configBlocks(spec.Config, "session_fields", "session_field") {
			key, keyErr := requiredString(field, "key")
			source, sourceErr := requiredString(field, "source")
			if keyErr != nil || sourceErr != nil {
				return nil, fmt.Errorf("node %q: session_fields[%d] requires key and source", spec.Name, i)
			}
			key, source = strings.TrimSpace(key), strings.TrimSpace(source)
			for _, previous := range sessionFields {
				if previous.Key == key {
					return nil, fmt.Errorf("node %q: duplicate session field key %q", spec.Name, key)
				}
			}
			optional, _ := field["optional"].(bool)
			sessionFields = append(sessionFields, identitySessionField{Key: key, Source: source, Optional: optional})
		}
		if len(sessionFields) == 0 {
			return nil, fmt.Errorf("node %q: session mode requires session_fields", spec.Name)
		}
	}
	var sessions *SessionManager
	if sessionName != "" {
		resource, ok := build.Resource(sessionName)
		if !ok {
			return nil, fmt.Errorf("node %q: session resource %q is not declared", spec.Name, sessionName)
		}
		sessions, ok = resource.(*SessionManager)
		if !ok {
			return nil, fmt.Errorf("node %q: resource %q is not a session manager", spec.Name, sessionName)
		}
	}

	return ActionFunc(func(ctx *ActionContext) (ActionResult, error) {
		result, err := d.Login(ctx.Context, optionalFact(ctx, emailFact), rawFact(ctx, passwordFact),
			optionalFact(ctx, tenantFact), optionalFact(ctx, codeFact))
		if err != nil {
			return ActionResult{}, err
		}
		if sessions != nil {
			sess, found := sessionFromContext(ctx.Context)
			httpCtx, httpFound := ctx.Context.Value(httpContextKey{}).(fh.Ctx)
			if !found || !httpFound {
				return ActionResult{}, intent.Failure{Code: "SESSION_UNAVAILABLE", Category: intent.CategoryUnavailable, Message: "this route has no session; add the configured session resource to the route"}
			}
			encoded, err := json.Marshal(result.Principal)
			if err != nil {
				return ActionResult{}, fmt.Errorf("encode authenticated principal: %w", err)
			}
			var principal map[string]any
			if err := json.Unmarshal(encoded, &principal); err != nil {
				return ActionResult{}, fmt.Errorf("decode authenticated principal: %w", err)
			}
			values := make(map[string]any, len(sessionFields))
			for _, field := range sessionFields {
				value, ok := resolvePath(principal, field.Source)
				if !ok || value == nil {
					if field.Optional {
						continue
					}
					return ActionResult{}, fmt.Errorf("node %q: session_fields source %q did not resolve on the authenticated principal", spec.Name, field.Source)
				}
				values[field.Key] = value
			}
			if err := sessions.Regenerate(httpCtx, sess); err != nil {
				return ActionResult{}, err
			}
			// Regenerate preserves prior session values; clear them before
			// attaching the authenticated principal to avoid role/scope carryover.
			sess.Clear()
			for key, value := range values {
				sess.Set(key, value)
			}
			return singleOutput(spec, map[string]any{
				"user": result.User.View(), "tenant_id": result.Membership.TenantID,
				"roles": result.Membership.Roles, "org_unit": result.Membership.OrgUnit,
				"principal": result.Principal,
			}), nil
		}
		extra := map[string]any{}
		for k, v := range result.Principal.Claims {
			extra[k] = v
		}
		token, expires, err := issuer.Issue(result.Principal, ttl, extra)
		if err != nil {
			return ActionResult{}, err
		}
		return singleOutput(spec, map[string]any{
			"token": token, "token_type": "Bearer", "expires_at": expires.Format(time.RFC3339),
			"user":      result.User.View(),
			"tenant_id": result.Membership.TenantID, "roles": result.Membership.Roles, "org_unit": result.Membership.OrgUnit,
		}), nil
	}), nil
}

type identitySessionField struct {
	Key, Source string
	Optional    bool
}

func buildIdentityAcceptInvite(build BuildContext, spec NodeSpec) (Action, error) {
	d, err := identityDirectory(build, spec)
	if err != nil {
		return nil, err
	}
	if err := rejectUnknownConfig("identity.accept_invite", spec.Config, "token_fact", "password_fact", "name_fact"); err != nil {
		return nil, fmt.Errorf("node %q: %w", spec.Name, err)
	}
	tokenFact, err := requiredString(spec.Config, "token_fact")
	if err != nil {
		return nil, fmt.Errorf("node %q: %w", spec.Name, err)
	}
	passwordFact, err := requiredString(spec.Config, "password_fact")
	if err != nil {
		return nil, fmt.Errorf("node %q: %w", spec.Name, err)
	}
	nameFact, err := requiredString(spec.Config, "name_fact")
	if err != nil {
		return nil, fmt.Errorf("node %q: %w", spec.Name, err)
	}
	return ActionFunc(func(ctx *ActionContext) (ActionResult, error) {
		user, m, err := d.AcceptInvite(ctx.Context, optionalFact(ctx, tokenFact), rawFact(ctx, passwordFact), optionalFact(ctx, nameFact))
		if err != nil {
			return ActionResult{}, err
		}
		return acknowledgement(spec, memberView(user, m)), nil
	}), nil
}

func buildIdentityChangePassword(build BuildContext, spec NodeSpec) (Action, error) {
	d, err := identityDirectory(build, spec)
	if err != nil {
		return nil, err
	}
	if err := rejectUnknownConfig("identity.change_password", spec.Config, "current_fact", "new_fact"); err != nil {
		return nil, fmt.Errorf("node %q: %w", spec.Name, err)
	}
	currentFact, err := requiredString(spec.Config, "current_fact")
	if err != nil {
		return nil, fmt.Errorf("node %q: %w", spec.Name, err)
	}
	newFact, err := requiredString(spec.Config, "new_fact")
	if err != nil {
		return nil, fmt.Errorf("node %q: %w", spec.Name, err)
	}
	return ActionFunc(func(ctx *ActionContext) (ActionResult, error) {
		if ctx.Principal.ID == "" {
			return ActionResult{}, errUnauthenticated
		}
		if err := d.ChangePassword(ctx.Context, ctx.Principal.ID, rawFact(ctx, currentFact), rawFact(ctx, newFact)); err != nil {
			return ActionResult{}, err
		}
		return acknowledgement(spec, map[string]any{"changed": true}), nil
	}), nil
}

func buildIdentityAdmin(build BuildContext, spec NodeSpec) (Action, error) {
	d, err := identityDirectory(build, spec)
	if err != nil {
		return nil, err
	}
	if err := rejectUnknownConfig("identity.admin", spec.Config, "operation", "tenant_sources", "user_sources", "tenant_fact", "user_fact", "input_fact", "org_resource"); err != nil {
		return nil, fmt.Errorf("node %q: %w", spec.Name, err)
	}
	var org *OrgHierarchy
	if name := configString(spec.Config, "org_resource", ""); name != "" {
		r, ok := build.Resource(name)
		org, _ = r.(*OrgHierarchy)
		if !ok || org == nil {
			return nil, fmt.Errorf("node %q: org_resource %q is not an org.hierarchy resource", spec.Name, name)
		}
	}
	operation := strings.ToLower(configString(spec.Config, "operation", ""))
	known := false
	for _, op := range identityOperations {
		known = known || op == operation
	}
	if !known {
		return nil, fmt.Errorf("node %q: identity.admin operation must be one of %s", spec.Name, strings.Join(identityOperations, ", "))
	}
	tenantSources := configStrings(spec.Config, "tenant_sources")
	userSources := configStrings(spec.Config, "user_sources")
	if legacy := configString(spec.Config, "tenant_fact", ""); legacy != "" {
		if len(tenantSources) != 0 {
			return nil, fmt.Errorf("node %q: use tenant_sources or tenant_fact, not both", spec.Name)
		}
		tenantSources = []string{legacy}
	}
	if legacy := configString(spec.Config, "user_fact", ""); legacy != "" {
		if len(userSources) != 0 {
			return nil, fmt.Errorf("node %q: use user_sources or user_fact, not both", spec.Name)
		}
		userSources = []string{legacy}
	}
	tenantSources, err = normalizeIdentitySources("tenant_sources", tenantSources)
	if err != nil {
		return nil, fmt.Errorf("node %q: %w", spec.Name, err)
	}
	userSources, err = normalizeIdentitySources("user_sources", userSources)
	if err != nil {
		return nil, fmt.Errorf("node %q: %w", spec.Name, err)
	}
	if len(tenantSources) == 0 {
		return nil, fmt.Errorf("node %q: identity.admin requires tenant_sources", spec.Name)
	}
	needsUser := operation != "invite" && operation != "list_users" && operation != "list_invitations" &&
		operation != "audit" && operation != "add_membership"
	if needsUser && len(userSources) == 0 {
		return nil, fmt.Errorf("node %q: identity.admin operation %q requires user_sources", spec.Name, operation)
	}
	inputFact, err := requiredString(spec.Config, "input_fact")
	if err != nil {
		return nil, fmt.Errorf("node %q: %w", spec.Name, err)
	}

	return ActionFunc(func(ctx *ActionContext) (ActionResult, error) {
		body, _ := resolvePath(ctx.Inputs, inputFact)
		input, _ := body.(map[string]any)
		tenant := identityFirstSource(ctx, tenantSources)
		if err := d.Authorize(ctx.Context, ctx.Principal, tenant); err != nil {
			return ActionResult{}, err
		}
		// The operations below consult the actor's global roles too; hand
		// them only what the database still grants.
		actor, err := d.VerifiedPrincipal(ctx.Context, ctx.Principal)
		if err != nil {
			return ActionResult{}, err
		}
		userID := identityFirstSource(ctx, userSources)
		if needsUser && userID == "" {
			return ActionResult{}, invalidInput("a user id is required")
		}
		if org != nil && (operation == "invite" || operation == "add_membership" || operation == "change_membership") {
			if unit := strings.TrimSpace(Stringify(input["org_unit"])); unit != "" || operation == "invite" {
				snap := org.Snapshot(ctx.TenantID)
				scope := org.ScopeFor(ctx.Principal, snap.Tree)
				switch {
				case unit == "" && !scope.Global:
					return ActionResult{}, invalidInput("choose the workspace this person works in")
				case unit != "" && !snap.Tree.Has(unit):
					return ActionResult{}, invalidInput("unknown workspace %q", unit)
				case unit != "" && !scope.Covers(snap.Tree, unit):
					return ActionResult{}, outOfScope(unit)
				}
			}
		}
		var out any
		switch operation {
		case "invite":
			inv, err := d.Invite(ctx.Context, actor, tenant, Stringify(input["email"]), Stringify(input["name"]),
				stringList(input["roles"]), strings.TrimSpace(Stringify(input["org_unit"])))
			if err != nil {
				return ActionResult{}, err
			}
			// The plaintext token is published once, for delivery (an email
			// node, typically); only its hash is stored.
			out = map[string]any{
				"token": inv.Token, "email": inv.Email, "user_id": inv.UserID, "tenant_id": inv.TenantID,
				"roles": inv.Roles, "org_unit": inv.OrgUnit, "expires_at": inv.ExpiresAt.Format(time.RFC3339),
			}
		case "list_users":
			if out, err = d.ListMembers(ctx.Context, tenant); err != nil {
				return ActionResult{}, err
			}
		case "list_invitations":
			if out, err = d.ListInvitations(ctx.Context, tenant); err != nil {
				return ActionResult{}, err
			}
		case "get_user":
			if out, err = d.GetMember(ctx.Context, actor, tenant, userID); err != nil {
				return ActionResult{}, err
			}
		case "suspend", "reactivate":
			status := UserSuspended
			if operation == "reactivate" {
				status = UserActive
			}
			if out, err = d.SetStatus(ctx.Context, actor, tenant, userID, status); err != nil {
				return ActionResult{}, err
			}
		case "add_membership":
			if out, err = d.AddMembership(ctx.Context, actor, tenant, userID, Stringify(input["email"]),
				stringList(input["roles"]), strings.TrimSpace(Stringify(input["org_unit"]))); err != nil {
				return ActionResult{}, err
			}
		case "change_membership":
			var roles []string
			if raw, ok := input["roles"]; ok {
				roles = stringList(raw)
				if roles == nil {
					roles = []string{}
				}
			}
			var orgUnit *string
			if raw, ok := input["org_unit"]; ok {
				unit := strings.TrimSpace(Stringify(raw))
				orgUnit = &unit
			}
			if roles == nil && orgUnit == nil {
				return ActionResult{}, invalidInput("send roles, org_unit or both")
			}
			if out, err = d.ChangeMembership(ctx.Context, actor, tenant, userID, roles, orgUnit); err != nil {
				return ActionResult{}, err
			}
		case "remove_membership":
			if err := d.RemoveMembership(ctx.Context, actor, tenant, userID); err != nil {
				return ActionResult{}, err
			}
			out = map[string]any{"removed": true, "user_id": userID, "tenant_id": tenant}
		case "audit":
			limit := 100
			if v, ok := requestValue(ctx, "query", "limit"); ok {
				if n, err := strconv.Atoi(v); err == nil {
					limit = n
				}
			}
			if out, err = d.AuditTrail(ctx.Context, tenant, limit); err != nil {
				return ActionResult{}, err
			}
		}
		return acknowledgement(spec, out), nil
	}), nil
}

// identityFirstSource resolves configured fact paths and request values in order.
// Request sources use request.<kind>.<name>, such as request.path.id or
// request.query.tenant_id. request.tenant exposes the middleware tenant context.
func identityFirstSource(ctx *ActionContext, sources []string) string {
	for _, source := range sources {
		source = strings.TrimSpace(source)
		if source == "request.tenant" && ctx.TenantID != "" {
			return strings.TrimSpace(ctx.TenantID)
		}
		if strings.HasPrefix(source, "request.") {
			parts := strings.SplitN(strings.TrimPrefix(source, "request."), ".", 2)
			if len(parts) == 2 {
				if value, ok := requestValue(ctx, parts[0], parts[1]); ok && strings.TrimSpace(value) != "" {
					return strings.TrimSpace(value)
				}
			}
			continue
		}
		if value := optionalFact(ctx, source); value != "" {
			return value
		}
	}
	return ""
}

func normalizeIdentitySources(name string, sources []string) ([]string, error) {
	clean := make([]string, 0, len(sources))
	for _, source := range sources {
		source = strings.TrimSpace(source)
		if source == "" {
			return nil, fmt.Errorf("config.%s contains an empty source", name)
		}
		if strings.HasPrefix(source, "request.") && source != "request.tenant" {
			parts := strings.SplitN(strings.TrimPrefix(source, "request."), ".", 2)
			if len(parts) != 2 || parts[1] == "" || (parts[0] != "path" && parts[0] != "query" && parts[0] != "header") {
				return nil, fmt.Errorf("config.%s source %q must use request.path.<name>, request.query.<name>, request.header.<name>, or request.tenant", name, source)
			}
		}
		clean = append(clean, source)
	}
	return clean, nil
}
