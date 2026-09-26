package platform

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Identity actions over an identity.users resource: sign-in, invitations,
// password changes and tenant administration.
//
// identity.admin is one action with an operation, like org.write, because
// every administrative operation shares the same guard: the caller must
// administer the target tenant, checked against the directory itself rather
// than the roles in their token.

func registerIdentityActions(r *Registry) {
	mustAction(r, "identity.login", ActionFactoryFunc(buildIdentityLogin), ActionInfo{
		Family:       "identity",
		Summary:      "Verify an email and password against identity.users and issue a JWT carrying the chosen tenant's roles",
		ResourceKind: "identity",
		Provides:     "{ token, token_type, expires_at, user, tenant_id, roles, org_unit }",
		Kind:         "effect",
		Config: []ConfigField{
			{Name: "email_fact", Type: "fact", Default: "input.email"},
			{Name: "password_fact", Type: "fact", Default: "input.password"},
			{Name: "tenant_fact", Type: "fact", Default: "input.tenant_id", Summary: "Optional when the user belongs to one tenant"},
			{Name: "code_fact", Type: "fact", Default: "input.code", Summary: "TOTP code, for accounts with MFA enabled"},
			{Name: "token_issuer", Type: "resource", Summary: "auth.jwt resource; defaults to the directory's token_issuer"},
			{Name: "ttl", Type: "duration", Summary: "Token lifetime; defaults to the auth.jwt resource's ttl"},
		},
	})
	mustAction(r, "identity.accept_invite", ActionFactoryFunc(buildIdentityAcceptInvite), ActionInfo{
		Family:       "identity",
		Summary:      "Consume a single-use invitation: set the new account's password (or confirm an existing one's) and join the tenant",
		ResourceKind: "identity",
		Kind:         "effect",
		Config: []ConfigField{
			{Name: "token_fact", Type: "fact", Default: "input.token"},
			{Name: "password_fact", Type: "fact", Default: "input.password"},
			{Name: "name_fact", Type: "fact", Default: "input.name"},
		},
	})
	mustAction(r, "identity.change_password", ActionFactoryFunc(buildIdentityChangePassword), ActionInfo{
		Family:       "identity",
		Summary:      "Change the signed-in user's password after verifying the current one",
		ResourceKind: "identity",
		Kind:         "effect",
		Config: []ConfigField{
			{Name: "current_fact", Type: "fact", Default: "input.current_password"},
			{Name: "new_fact", Type: "fact", Default: "input.new_password"},
		},
	})
	mustAction(r, "identity.admin", ActionFactoryFunc(buildIdentityAdmin), ActionInfo{
		Family:       "identity",
		Summary:      "Administer a tenant's users: invite, list_users, list_invitations, get_user, suspend, reactivate, add_membership, change_membership, remove_membership, audit",
		ResourceKind: "identity",
		Kind:         "effect",
		Config: []ConfigField{
			{Name: "operation", Type: "string", Required: true},
			{Name: "tenant_fact", Type: "fact", Summary: "Target tenant (default: input.tenant_id, then the request's tenant)"},
			{Name: "user_fact", Type: "fact", Summary: "Target user id (default: path :id or :user_id, then input.user_id)"},
			{Name: "input_fact", Type: "fact", Default: "input", Summary: "Object carrying email, name, roles, org_unit"},
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
		"email_fact", "password_fact", "tenant_fact", "code_fact", "token_issuer", "ttl"); err != nil {
		return nil, fmt.Errorf("node %q: %w", spec.Name, err)
	}
	issuer := d.issuer
	if name := configString(spec.Config, "token_issuer", ""); name != "" {
		resolved, ok := build.Resource(name)
		if !ok {
			return nil, fmt.Errorf("node %q: token_issuer %q is not declared", spec.Name, name)
		}
		if issuer, ok = resolved.(*jwtAuth); !ok {
			return nil, fmt.Errorf("node %q: token_issuer %q is not an auth.jwt resource", spec.Name, name)
		}
	}
	if issuer == nil || issuer.signing == nil || !issuer.signing.CanSign() {
		return nil, fmt.Errorf("node %q: identity.login needs a token_issuer (an auth.jwt resource with a signing key), on the node or on %q", spec.Name, spec.Resource)
	}
	ttl, err := configDuration(spec.Config, "ttl", 0)
	if err != nil {
		return nil, fmt.Errorf("node %q: %w", spec.Name, err)
	}
	if err := exactlyOneOutput(spec); err != nil {
		return nil, err
	}
	emailFact := configString(spec.Config, "email_fact", "input.email")
	passwordFact := configString(spec.Config, "password_fact", "input.password")
	tenantFact := configString(spec.Config, "tenant_fact", "input.tenant_id")
	codeFact := configString(spec.Config, "code_fact", "input.code")

	return ActionFunc(func(ctx *ActionContext) (ActionResult, error) {
		result, err := d.Login(ctx.Context, optionalFact(ctx, emailFact), rawFact(ctx, passwordFact),
			optionalFact(ctx, tenantFact), optionalFact(ctx, codeFact))
		if err != nil {
			return ActionResult{}, err
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

func buildIdentityAcceptInvite(build BuildContext, spec NodeSpec) (Action, error) {
	d, err := identityDirectory(build, spec)
	if err != nil {
		return nil, err
	}
	if err := rejectUnknownConfig("identity.accept_invite", spec.Config, "token_fact", "password_fact", "name_fact"); err != nil {
		return nil, fmt.Errorf("node %q: %w", spec.Name, err)
	}
	tokenFact := configString(spec.Config, "token_fact", "input.token")
	passwordFact := configString(spec.Config, "password_fact", "input.password")
	nameFact := configString(spec.Config, "name_fact", "input.name")
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
	currentFact := configString(spec.Config, "current_fact", "input.current_password")
	newFact := configString(spec.Config, "new_fact", "input.new_password")
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
	if err := rejectUnknownConfig("identity.admin", spec.Config, "operation", "tenant_fact", "user_fact", "input_fact"); err != nil {
		return nil, fmt.Errorf("node %q: %w", spec.Name, err)
	}
	operation := strings.ToLower(configString(spec.Config, "operation", ""))
	known := false
	for _, op := range identityOperations {
		known = known || op == operation
	}
	if !known {
		return nil, fmt.Errorf("node %q: identity.admin operation must be one of %s", spec.Name, strings.Join(identityOperations, ", "))
	}
	tenantFact := configString(spec.Config, "tenant_fact", "")
	userFact := configString(spec.Config, "user_fact", "")
	inputFact := configString(spec.Config, "input_fact", "input")

	return ActionFunc(func(ctx *ActionContext) (ActionResult, error) {
		body, _ := resolvePath(ctx.Inputs, inputFact)
		input, _ := body.(map[string]any)
		tenant := optionalFact(ctx, tenantFact)
		if tenant == "" && tenantFact == "" {
			tenant = strings.TrimSpace(Stringify(input["tenant_id"]))
		}
		if tenant == "" {
			tenant = ctx.TenantID
		}
		if tenant == "" {
			tenant = ctx.Principal.TenantID
		}
		if err := d.Authorize(ctx.Context, ctx.Principal, tenant); err != nil {
			return ActionResult{}, err
		}
		// The operations below consult the actor's global roles too; hand
		// them only what the database still grants.
		actor, err := d.VerifiedPrincipal(ctx.Context, ctx.Principal)
		if err != nil {
			return ActionResult{}, err
		}
		userID := optionalFact(ctx, userFact)
		if userID == "" && userFact == "" {
			for _, name := range []string{"user_id", "id"} {
				if v, ok := requestValue(ctx, "path", name); ok && v != "" {
					userID = v
					break
				}
			}
			if userID == "" {
				userID = strings.TrimSpace(Stringify(input["user_id"]))
			}
		}
		needsUser := operation != "invite" && operation != "list_users" && operation != "list_invitations" &&
			operation != "audit" && operation != "add_membership"
		if needsUser && userID == "" {
			return ActionResult{}, invalidInput("a user id is required")
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
