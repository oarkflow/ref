package auth

import (
	"context"
	"strings"

	"github.com/oarkflow/fh"
	"github.com/oarkflow/ref/examples/boilerplate/internal/domain"
	"github.com/oarkflow/ref/examples/boilerplate/internal/rbac"
)

const SessionCookieName = "ref_boilerplate_sid"

// Middleware provides HTTP middleware handlers for auth and rbac.
type Middleware struct {
	authService *Service
	rbacManager *rbac.Manager
}

// NewMiddleware constructs auth/rbac middlewares.
func NewMiddleware(authService *Service, rbacManager *rbac.Manager) *Middleware {
	return &Middleware{
		authService: authService,
		rbacManager: rbacManager,
	}
}

// SessionLoader extracts session from cookie or Authorization header and attaches identity to context.
// It is non-blocking: requests without sessions continue as guest.
func (m *Middleware) SessionLoader() fh.HandlerFunc {
	return func(c fh.Ctx) error {
		var token string

		// 1. Check Bearer header
		authHeader := c.Get("Authorization")
		if strings.HasPrefix(authHeader, "Bearer ") {
			token = strings.TrimPrefix(authHeader, "Bearer ")
		}

		// 2. Check Cookie
		if token == "" {
			token = c.GetCookie(SessionCookieName)
		}

		if token != "" {
			user, sess, err := m.authService.ValidateSession(c.Context(), token)
			if err == nil && user != nil {
				effectivePerms := m.rbacManager.PermissionsForRoles(user.Roles...)
				principal := &domain.Principal{
					ID:          user.ID,
					Email:       user.Email,
					Name:        user.Name,
					Roles:       user.Roles,
					Permissions: effectivePerms,
					SessionID:   sess.ID,
				}

				// Attach to context
				ctx := context.WithValue(c.Context(), domain.ContextKeyUser, user)
				ctx = context.WithValue(ctx, domain.ContextKeyPrincipal, principal)
				ctx = context.WithValue(ctx, domain.ContextKeySession, sess)
				c.SetContext(ctx)

				// Also store in locals for easy template access
				c.Locals("currentUser", user)
				c.Locals("currentPrincipal", principal)
			}
		}

		return c.Next()
	}
}

// RequireAuth ensures that the request has an active, valid principal.
func (m *Middleware) RequireAuth() fh.HandlerFunc {
	return func(c fh.Ctx) error {
		principal, ok := c.Context().Value(domain.ContextKeyPrincipal).(*domain.Principal)
		if !ok || principal == nil {
			if isHTMLRequest(c) {
				return c.Redirect("/login?redirect=" + c.Path())
			}
			return c.Status(401).JSON(map[string]any{
				"error":   "Unauthorized",
				"message": "Authentication is required to access this resource",
			})
		}
		return c.Next()
	}
}

// RequireRole enforces that the authenticated user possesses at least one of the specified roles.
func (m *Middleware) RequireRole(roles ...string) fh.HandlerFunc {
	return func(c fh.Ctx) error {
		principal, ok := c.Context().Value(domain.ContextKeyPrincipal).(*domain.Principal)
		if !ok || principal == nil {
			if isHTMLRequest(c) {
				return c.Redirect("/login?redirect=" + c.Path())
			}
			return c.Status(401).JSON(map[string]any{
				"error":   "Unauthorized",
				"message": "Authentication is required to access this resource",
			})
		}

		if !m.rbacManager.HasAnyRole(principal.Roles, roles...) {
			if isHTMLRequest(c) {
				c.Status(403)
				return c.Render("pages/errors/403", map[string]any{
					"title":         "403 Forbidden - Access Denied",
					"requiredRoles": roles,
					"userRoles":     principal.Roles,
					"message":       "You do not possess the required role permissions to view this resource.",
				}, "layouts/base")
			}
			return c.Status(403).JSON(map[string]any{
				"error":          "Forbidden",
				"message":        "You do not have the required role to perform this action",
				"required_roles": roles,
				"user_roles":     principal.Roles,
			})
		}

		return c.Next()
	}
}

// RequirePermission enforces that the caller has a specific permission.
func (m *Middleware) RequirePermission(permission string) fh.HandlerFunc {
	return func(c fh.Ctx) error {
		principal, ok := c.Context().Value(domain.ContextKeyPrincipal).(*domain.Principal)
		if !ok || principal == nil {
			if isHTMLRequest(c) {
				return c.Redirect("/login?redirect=" + c.Path())
			}
			return c.Status(401).JSON(map[string]any{
				"error":   "Unauthorized",
				"message": "Authentication is required to access this resource",
			})
		}

		if !m.rbacManager.HasPermission(c.Context(), principal, permission) {
			if isHTMLRequest(c) {
				c.Status(403)
				return c.Render("pages/errors/403", map[string]any{
					"title":              "403 Forbidden - Access Denied",
					"requiredPermission": permission,
					"userRoles":          principal.Roles,
					"message":            "Your roles do not grant the required permission: " + permission,
				}, "layouts/base")
			}
			return c.Status(403).JSON(map[string]any{
				"error":               "Forbidden",
				"message":             "Missing required permission",
				"required_permission": permission,
				"user_roles":          principal.Roles,
			})
		}

		return c.Next()
	}
}

// RouteAuthorizer automatically checks the request against configured route authorization rules.
func (m *Middleware) RouteAuthorizer() fh.HandlerFunc {
	return func(c fh.Ctx) error {
		principal, _ := c.Context().Value(domain.ContextKeyPrincipal).(*domain.Principal)
		allowed, reason := m.rbacManager.AuthorizeRoute(c.Method(), c.Path(), principal)
		if !allowed {
			if principal == nil {
				if isHTMLRequest(c) {
					return c.Redirect("/login?redirect=" + c.Path())
				}
				return c.Status(401).JSON(map[string]any{
					"error":   "Unauthorized",
					"message": reason,
				})
			}

			if isHTMLRequest(c) {
				c.Status(403)
				return c.Render("pages/errors/403", map[string]any{
					"title":     "403 Forbidden",
					"message":   reason,
					"userRoles": principal.Roles,
				}, "layouts/base")
			}
			return c.Status(403).JSON(map[string]any{
				"error":   "Forbidden",
				"message": reason,
			})
		}
		return c.Next()
	}
}

// Helper to determine if the client expects an HTML response
func isHTMLRequest(c fh.Ctx) bool {
	accept := c.Get("Accept")
	return strings.Contains(accept, "text/html") || accept == "" || strings.Contains(accept, "*/*")
}
