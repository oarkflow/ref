package web

import (
	"net/url"
	"strings"

	"github.com/oarkflow/fh"
	"github.com/oarkflow/ref/examples/boilerplate/internal/auth"
	"github.com/oarkflow/ref/examples/boilerplate/internal/domain"
	"github.com/oarkflow/ref/examples/boilerplate/internal/rbac"
)

func formValue(c fh.Ctx, key string) string {
	values, err := url.ParseQuery(string(c.Body()))
	if err != nil {
		return ""
	}
	return values.Get(key)
}

// ViewHandler handles web dashboard and admin views.
type ViewHandler struct {
	authService *auth.Service
	userRepo    auth.UserRepository
	rbacManager *rbac.Manager
}

// NewViewHandler constructs a view handler.
func NewViewHandler(authService *auth.Service, userRepo auth.UserRepository, rbacManager *rbac.Manager) *ViewHandler {
	return &ViewHandler{
		authService: authService,
		userRepo:    userRepo,
		rbacManager: rbacManager,
	}
}

// Home redirects to dashboard if authenticated, or login if guest.
func (h *ViewHandler) Home(c fh.Ctx) error {
	if user, ok := c.Context().Value(domain.ContextKeyUser).(*domain.User); ok && user != nil {
		return c.Redirect("/dashboard")
	}
	return c.Redirect("/login")
}

// Dashboard renders the main user dashboard with role and permission breakdown.
func (h *ViewHandler) Dashboard(c fh.Ctx) error {
	user := c.Context().Value(domain.ContextKeyUser).(*domain.User)
	principal := c.Context().Value(domain.ContextKeyPrincipal).(*domain.Principal)

	userCount, _ := h.userRepo.Count(c.Context())

	return c.Render("pages/dashboard/index", map[string]any{
		"title":       "Dashboard — Overview",
		"activeNav":   "dashboard",
		"user":        user,
		"principal":   principal,
		"userCount":   userCount,
		"isAdmin":     h.rbacManager.HasRole(user.Roles, domain.RoleAdmin),
		"isManager":   h.rbacManager.HasRole(user.Roles, domain.RoleManager),
		"permissions": principal.Permissions,
	}, "layouts/base")
}

// Admin renders the admin management panel (protected by RBAC).
func (h *ViewHandler) Admin(c fh.Ctx) error {
	user := c.Context().Value(domain.ContextKeyUser).(*domain.User)
	principal := c.Context().Value(domain.ContextKeyPrincipal).(*domain.Principal)

	users, total, _ := h.userRepo.List(c.Context(), 0, 50)
	roles := h.rbacManager.GetRoleDefinitions()

	return c.Render("pages/dashboard/admin", map[string]any{
		"title":      "Admin Portal — System Governance",
		"activeNav":  "admin",
		"user":       user,
		"principal":  principal,
		"users":      users,
		"totalUsers": total,
		"roles":      roles,
		"success":    c.Query("success", ""),
		"error":      c.Query("error", ""),
	}, "layouts/base")
}

// Manager renders the operations and reports view (protected by RBAC).
func (h *ViewHandler) Manager(c fh.Ctx) error {
	user := c.Context().Value(domain.ContextKeyUser).(*domain.User)
	principal := c.Context().Value(domain.ContextKeyPrincipal).(*domain.Principal)

	reports := []map[string]any{
		{"id": "REP-2026-001", "name": "Security Audit Log", "status": "Clean", "updated": "Just now"},
		{"id": "REP-2026-002", "name": "Argon2id Hashing Benchmark", "status": "OWASP Compliant", "updated": "1 hour ago"},
		{"id": "REP-2026-003", "name": "RBAC Policy Evaluation", "status": "Optimized", "updated": "3 hours ago"},
	}

	return c.Render("pages/dashboard/manager", map[string]any{
		"title":     "Operations & Reports",
		"activeNav": "manager",
		"user":      user,
		"principal": principal,
		"reports":   reports,
	}, "layouts/base")
}

// Profile renders the user profile and password change form.
func (h *ViewHandler) Profile(c fh.Ctx) error {
	user := c.Context().Value(domain.ContextKeyUser).(*domain.User)
	principal := c.Context().Value(domain.ContextKeyPrincipal).(*domain.Principal)

	return c.Render("pages/dashboard/profile", map[string]any{
		"title":     "User Profile & Security",
		"activeNav": "profile",
		"user":      user,
		"principal": principal,
		"success":   c.Query("success", ""),
		"error":     c.Query("error", ""),
	}, "layouts/base")
}

// HandleChangePassword updates the authenticated user's password using Argon2id.
func (h *ViewHandler) HandleChangePassword(c fh.Ctx) error {
	user := c.Context().Value(domain.ContextKeyUser).(*domain.User)
	currentPass := formValue(c, "current_password")
	newPass := formValue(c, "new_password")
	confirmPass := formValue(c, "confirm_password")

	if newPass != confirmPass {
		return c.Redirect("/profile?error=" + "New passwords do not match")
	}

	err := h.authService.ChangePassword(c.Context(), auth.ChangePasswordInput{
		UserID:          user.ID,
		CurrentPassword: currentPass,
		NewPassword:     newPass,
	})

	if err != nil {
		return c.Redirect("/profile?error=" + err.Error())
	}

	return c.Redirect("/profile?success=" + "Password updated successfully with Argon2id!")
}

// HandleUpdateRole allows an admin to switch roles for demo testing.
func (h *ViewHandler) HandleUpdateRole(c fh.Ctx) error {
	targetUserID := formValue(c, "user_id")
	newRole := formValue(c, "role")

	targetUser, err := h.userRepo.FindByID(c.Context(), targetUserID)
	if err != nil {
		return c.Redirect("/dashboard/admin?error=" + "User not found")
	}

	if newRole != "" {
		targetUser.Roles = []string{strings.ToLower(newRole)}
		if err := h.userRepo.Update(c.Context(), targetUser); err != nil {
			return c.Redirect("/dashboard/admin?error=" + err.Error())
		}
	}

	return c.Redirect("/dashboard/admin?success=" + "User role updated successfully!")
}
