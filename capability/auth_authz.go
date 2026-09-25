package capability

import (
	"errors"
	"fmt"

	"github.com/oarkflow/authz"

	"github.com/oarkflow/ref/execution"
	"github.com/oarkflow/ref/fact"
	"github.com/oarkflow/ref/graph"
	"github.com/oarkflow/ref/invocation"
)

// This file wires github.com/oarkflow/authz into the capability layer as a
// first-class, ready-to-use default — the same engine platform/ already uses
// (see platform/resources_authz_engine.go), but reusable directly by any ref
// application without going through the platform package.
//
// ErrUnauthorized is returned (wrapped) when the authz engine denies a
// request. It is distinct from ErrUnauthenticated: authentication answers
// "who are you", authorization answers "can you do this".
var ErrUnauthorized = errors.New("ref: unauthorized")

// AuthzAuthenticatorConfig configures NewAuthzAuthenticator.
type AuthzAuthenticatorConfig struct {
	// TenantID is the default tenant used for the resolved principal's
	// authorization subject when the hint carries none.
	TenantID string

	// ResolvePrincipal maps a raw invocation hint (already trusted — e.g. a
	// session ID or API key looked up by another authenticator, or the ID
	// portion of a JWT already verified upstream) to the identity fields of
	// a PrincipalFact. NewAuthzAuthenticator does not itself verify bearer
	// credentials; compose it with NewJWTAuthenticator (or another
	// AuthenticatorFunc) via ChainAuthenticators when raw credential
	// verification is also needed.
	ResolvePrincipal func(hint invocation.PrincipalHint) (PrincipalFact, error)
}

// NewAuthzAuthenticator builds an AuthenticatorFunc backed by an
// github.com/oarkflow/authz Engine. It resolves the caller's identity via
// cfg.ResolvePrincipal (typically another AuthenticatorFunc, e.g. one built
// with NewJWTAuthenticator) and looks up that subject's roles from the
// engine's role store, merging them into the resulting PrincipalFact so that
// downstream policy checks (NewAuthzPolicyCapability) see the engine's view
// of the subject's roles rather than only whatever the credential itself
// claimed.
func NewAuthzAuthenticator(engine *authz.Engine, cfg AuthzAuthenticatorConfig) AuthenticatorFunc {
	return func(hint invocation.PrincipalHint) (PrincipalFact, error) {
		if engine == nil {
			return PrincipalFact{}, fmt.Errorf("%w: no authz engine configured", ErrUnauthenticated)
		}
		if cfg.ResolvePrincipal == nil {
			return PrincipalFact{}, fmt.Errorf("%w: no principal resolver configured", ErrUnauthenticated)
		}

		principal, err := cfg.ResolvePrincipal(hint)
		if err != nil {
			return PrincipalFact{}, err
		}
		if principal.ID == "" {
			return PrincipalFact{}, fmt.Errorf("%w: resolved principal has no ID", ErrUnauthenticated)
		}
		return principal, nil
	}
}

// ChainAuthenticators composes authenticators front-to-back: the first
// success wins, and its PrincipalFact is returned. Errors from earlier
// authenticators are discarded in favor of the last one's error, so a chain
// of "try session, then try JWT" reports the most specific failure.
func ChainAuthenticators(authenticators ...AuthenticatorFunc) AuthenticatorFunc {
	return func(hint invocation.PrincipalHint) (PrincipalFact, error) {
		var lastErr error = ErrUnauthenticated
		for _, auth := range authenticators {
			if auth == nil {
				continue
			}
			principal, err := auth(hint)
			if err == nil {
				return principal, nil
			}
			lastErr = err
		}
		return PrincipalFact{}, lastErr
	}
}

// AuthzResourceSelector chooses the resource and action to authorize for a
// given execution step.
type AuthzResourceSelector func(nc *execution.NodeContext) (resource, action string, err error)

// AuthzPolicyConfig configures NewAuthzPolicyCapability.
type AuthzPolicyConfig struct {
	// TenantID is used when the resolved principal carries none.
	TenantID string
	// SuperuserRoles bypass the engine entirely, mirroring
	// platform's authz.engine "superuser_roles" behavior.
	SuperuserRoles []string
}

// NewAuthzPolicyCapability creates a DecisionNode capability that authorizes
// the already-resolved PrincipalFact (published by an earlier
// NewAuthCapability step) against an github.com/oarkflow/authz Engine. It
// requires PrincipalKey and does not publish any fact of its own — it either
// allows the invocation to proceed or returns an error wrapping
// ErrUnauthorized.
//
// Typical wiring in a DAG:
//
//	capability.NewAuthCapability("auth.jwt", capability.NewJWTAuthenticator(jwtCfg))
//	capability.NewAuthzPolicyCapability("authz.check", engine,
//	    func(nc *execution.NodeContext) (string, string, error) {
//	        return "document:*", "read", nil
//	    })
func NewAuthzPolicyCapability(name string, engine *authz.Engine, selector AuthzResourceSelector, cfg AuthzPolicyConfig, opts ...Option) Registration {
	if name == "" {
		name = "capability.authz"
	}
	reg := NewRegistration(name, graph.DecisionNode, opts...)
	reg.Requires = []fact.AnyKey{PrincipalKey.Any()}

	superuser := make(map[string]struct{}, len(cfg.SuperuserRoles))
	for _, role := range cfg.SuperuserRoles {
		superuser[role] = struct{}{}
	}

	reg.Run = func(nc *execution.NodeContext) error {
		if engine == nil {
			nc.Decisions().RecordDeny(name, "no authz engine configured")
			return fmt.Errorf("%w: no authz engine configured", ErrUnauthorized)
		}
		if selector == nil {
			nc.Decisions().RecordDeny(name, "no resource selector configured")
			return fmt.Errorf("%w: no resource selector configured", ErrUnauthorized)
		}

		principal, err := execution.Require(nc, PrincipalKey)
		if err != nil {
			nc.Decisions().RecordDeny(name, "missing required principal for authorization")
			return err
		}

		for _, role := range principal.Roles {
			if _, ok := superuser[role]; ok {
				nc.Decisions().RecordAllow(name, nil)
				return nil
			}
		}

		resource, action, err := selector(nc)
		if err != nil {
			nc.Decisions().RecordDeny(name, fmt.Sprintf("resource selection failed: %v", err))
			return err
		}

		tenantID := cfg.TenantID
		if inv := nc.Invocation(); inv != nil {
			if identity := inv.VerifiedIdentity(); identity != nil && identity.Tenant() != "" {
				tenantID = identity.Tenant()
			}
		}

		subject := &authz.Subject{
			ID:       principal.ID,
			Type:     "user",
			TenantID: tenantID,
			Roles:    principal.Roles,
			Attrs:    principal.Claims,
		}
		res := &authz.Resource{
			ID:       resource,
			Type:     resource,
			TenantID: tenantID,
		}
		env := &authz.Environment{
			TenantID: tenantID,
		}

		decision, err := engine.Authorize(nc, subject, authz.Action(action), res, env)
		if err != nil {
			nc.Decisions().RecordDeny(name, fmt.Sprintf("authz engine error: %v", err))
			return fmt.Errorf("%w: %v", ErrUnauthorized, err)
		}
		if !decision.Allowed {
			nc.Decisions().RecordDeny(name, decision.Reason)
			return fmt.Errorf("%w: %s", ErrUnauthorized, decision.Reason)
		}

		nc.Decisions().RecordAllow(name, []execution.Constraint{
			{Field: "resource", Values: []string{resource}},
			{Field: "action", Values: []string{action}},
		})
		return nil
	}

	return reg
}
