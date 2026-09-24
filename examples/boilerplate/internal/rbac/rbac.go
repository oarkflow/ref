package rbac

import (
	"context"
	"strings"
	"sync"

	"github.com/oarkflow/authz"
	authzStores "github.com/oarkflow/authz/pkg/stores"
	"github.com/oarkflow/ref/examples/boilerplate/internal/domain"
)

// Standard system permissions.
const (
	PermWildcard       = "*"
	PermUsersWildcard  = "users:*"
	PermUsersRead      = "users:read"
	PermUsersWrite     = "users:write"
	PermUsersDelete    = "users:delete"
	PermRolesManage    = "roles:manage"
	PermAuditRead      = "audit:read"
	PermReportsRead    = "reports:read"
	PermReportsWrite   = "reports:write"
	PermDashboardView  = "dashboard:view"
	PermAdminDashboard = "admin:dashboard"
	PermProfileView    = "profile:view"
	PermProfileUpdate  = "profile:update"
)

// RouteRule associates an HTTP method and path pattern with a required permission.
type RouteRule struct {
	Method     string
	PathPrefix string
	Permission string
	Roles      []string // optional minimum roles
}

// Manager orchestrates RBAC and route-based authorization using oarkflow/authz.
type Manager struct {
	mu             sync.RWMutex
	engine         *authz.Engine
	roleStore      authz.RoleStore
	roles          map[string]domain.RoleDefinition
	routeRules     []RouteRule
	superuserRoles map[string]struct{}
}

// NewRBACManager initializes the enterprise RBAC manager backed by oarkflow/authz.
func NewRBACManager() (*Manager, error) {
	policyStore := authzStores.NewMemoryPolicyStore()
	roleStore := authzStores.NewMemoryRoleStore()
	aclStore := authzStores.NewMemoryACLStore()
	auditStore := authzStores.NewMemoryAuditStore()
	roleMembershipStore := authzStores.NewMemoryRoleMembershipStore()
	tenantStore := authzStores.NewMemoryTenantStore()

	engine := authz.NewEngine(
		policyStore,
		roleStore,
		aclStore,
		auditStore,
		authz.WithRoleMembershipStore(roleMembershipStore),
		authz.WithTenantStore(tenantStore),
	)

	mgr := &Manager{
		engine:    engine,
		roleStore: roleStore,
		roles:     make(map[string]domain.RoleDefinition),
		superuserRoles: map[string]struct{}{
			domain.RoleSuperAdmin: {},
		},
		routeRules: defaultRouteRules(),
	}

	if err := mgr.bootstrapRoles(); err != nil {
		return nil, err
	}

	return mgr, nil
}

// bootstrapRoles sets up the initial roles, permissions, and inheritance graph.
func (m *Manager) bootstrapRoles() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	defs := []domain.RoleDefinition{
		{
			Name:        domain.RoleSuperAdmin,
			Description: "Full unrestricted platform access",
			Permissions: []string{PermWildcard},
			Inherits:    []string{domain.RoleAdmin},
		},
		{
			Name:        domain.RoleAdmin,
			Description: "System administration and user management",
			Permissions: []string{
				PermAdminDashboard,
				PermUsersWildcard,
				PermRolesManage,
				PermAuditRead,
			},
			Inherits: []string{domain.RoleManager},
		},
		{
			Name:        domain.RoleManager,
			Description: "Operational oversight, content, and reports",
			Permissions: []string{
				PermReportsRead,
				PermReportsWrite,
				PermUsersRead,
			},
			Inherits: []string{domain.RoleUser},
		},
		{
			Name:        domain.RoleUser,
			Description: "Standard authenticated member",
			Permissions: []string{
				PermDashboardView,
				PermProfileView,
				PermProfileUpdate,
			},
			Inherits: []string{domain.RoleGuest},
		},
		{
			Name:        domain.RoleGuest,
			Description: "Public / unauthenticated visitor",
			Permissions: []string{},
			Inherits:    []string{},
		},
	}

	for _, d := range defs {
		m.roles[d.Name] = d

		// Register in oarkflow/authz role store
		perms := make([]authz.Permission, len(d.Permissions))
		for i, p := range d.Permissions {
			perms[i] = authz.Permission{
				Resource: extractResource(p),
				Action:   authz.Action(extractAction(p)),
			}
		}

		role := authz.Role{
			ID:          d.Name,
			Name:        d.Name,
			TenantID:    "default",
			Permissions: perms,
			Inherits:    d.Inherits,
		}
		_ = m.engine.CreateRole(context.Background(), &role)
	}

	return nil
}

// defaultRouteRules defines route-based authorization rules.
func defaultRouteRules() []RouteRule {
	return []RouteRule{
		{Method: "GET", PathPrefix: "/dashboard/admin", Permission: PermAdminDashboard, Roles: []string{domain.RoleAdmin, domain.RoleSuperAdmin}},
		{Method: "GET", PathPrefix: "/dashboard/manager", Permission: PermReportsRead, Roles: []string{domain.RoleManager, domain.RoleAdmin, domain.RoleSuperAdmin}},
		{Method: "GET", PathPrefix: "/dashboard", Permission: PermDashboardView},
		{Method: "GET", PathPrefix: "/profile", Permission: PermProfileView},
		{Method: "POST", PathPrefix: "/profile", Permission: PermProfileUpdate},

		// API routes
		{Method: "GET", PathPrefix: "/api/v1/admin/users", Permission: PermUsersRead, Roles: []string{domain.RoleAdmin, domain.RoleSuperAdmin}},
		{Method: "POST", PathPrefix: "/api/v1/admin/users", Permission: PermUsersWrite, Roles: []string{domain.RoleAdmin, domain.RoleSuperAdmin}},
		{Method: "DELETE", PathPrefix: "/api/v1/admin/users", Permission: PermUsersDelete, Roles: []string{domain.RoleAdmin, domain.RoleSuperAdmin}},
		{Method: "GET", PathPrefix: "/api/v1/reports", Permission: PermReportsRead, Roles: []string{domain.RoleManager, domain.RoleAdmin, domain.RoleSuperAdmin}},
		{Method: "GET", PathPrefix: "/api/v1/me", Permission: PermProfileView},
	}
}

// HasRole checks whether a user has a specific role (directly or through inheritance).
func (m *Manager) HasRole(userRoles []string, requiredRole string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, r := range userRoles {
		if r == requiredRole {
			return true
		}
		// Superadmin inherits everything
		if r == domain.RoleSuperAdmin {
			return true
		}
		// Check hierarchy chain
		if m.inheritsRole(r, requiredRole, make(map[string]bool)) {
			return true
		}
	}
	return false
}

// HasAnyRole checks whether a user has at least one of the given roles.
func (m *Manager) HasAnyRole(userRoles []string, requiredRoles ...string) bool {
	for _, req := range requiredRoles {
		if m.HasRole(userRoles, req) {
			return true
		}
	}
	return false
}

func (m *Manager) inheritsRole(currentRole, targetRole string, visited map[string]bool) bool {
	if visited[currentRole] {
		return false
	}
	visited[currentRole] = true

	def, ok := m.roles[currentRole]
	if !ok {
		return false
	}
	for _, inherited := range def.Inherits {
		if inherited == targetRole {
			return true
		}
		if m.inheritsRole(inherited, targetRole, visited) {
			return true
		}
	}
	return false
}

// PermissionsForRoles collects all effective permissions granted to the given roles (including inherited).
func (m *Manager) PermissionsForRoles(roles ...string) []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	permSet := make(map[string]struct{})
	visited := make(map[string]bool)

	var collect func(role string)
	collect = func(role string) {
		if visited[role] {
			return
		}
		visited[role] = true

		def, ok := m.roles[role]
		if !ok {
			return
		}
		for _, p := range def.Permissions {
			permSet[p] = struct{}{}
		}
		for _, inh := range def.Inherits {
			collect(inh)
		}
	}

	for _, r := range roles {
		collect(r)
	}

	result := make([]string, 0, len(permSet))
	for p := range permSet {
		result = append(result, p)
	}
	return result
}

// HasPermission checks if the principal is authorized for a specific permission.
func (m *Manager) HasPermission(_ context.Context, principal *domain.Principal, permission string) bool {
	if principal == nil {
		return false
	}

	// Super admin bypass
	for _, r := range principal.Roles {
		if _, isSuper := m.superuserRoles[r]; isSuper {
			return true
		}
	}

	effectivePerms := m.PermissionsForRoles(principal.Roles...)
	for _, ep := range effectivePerms {
		if matchPermission(ep, permission) {
			return true
		}
	}
	return false
}

// AuthorizeRoute performs route-based authorization against the registered rules.
// Returns (allowed bool, reason string).
func (m *Manager) AuthorizeRoute(method, path string, principal *domain.Principal) (bool, string) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, rule := range m.routeRules {
		if rule.Method != "" && !strings.EqualFold(rule.Method, method) {
			continue
		}
		if strings.HasPrefix(path, rule.PathPrefix) {
			if principal == nil {
				return false, "authentication required for this route"
			}

			// Check role constraints if specified
			if len(rule.Roles) > 0 {
				if !m.HasAnyRole(principal.Roles, rule.Roles...) {
					return false, "insufficient role for route " + path
				}
			}

			// Check permission constraint if specified
			if rule.Permission != "" {
				if !m.HasPermission(context.Background(), principal, rule.Permission) {
					return false, "missing required permission: " + rule.Permission
				}
			}
			return true, ""
		}
	}

	// Route has no specific restrictions
	return true, ""
}

// GetRoleDefinitions returns all defined roles with their permissions and inheritance.
func (m *Manager) GetRoleDefinitions() []domain.RoleDefinition {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]domain.RoleDefinition, 0, len(m.roles))
	for _, d := range m.roles {
		result = append(result, d)
	}
	return result
}

// matchPermission checks if granted permission satisfies requested permission.
// Supports wildcards (e.g. '*' or 'users:*').
func matchPermission(granted, requested string) bool {
	if granted == PermWildcard || granted == requested {
		return true
	}
	if strings.HasSuffix(granted, ":*") {
		prefix := strings.TrimSuffix(granted, ":*")
		if strings.HasPrefix(requested, prefix+":") {
			return true
		}
	}
	return false
}

func extractResource(perm string) string {
	parts := strings.Split(perm, ":")
	if len(parts) > 1 {
		return parts[0]
	}
	return perm
}

func extractAction(perm string) string {
	parts := strings.Split(perm, ":")
	if len(parts) > 1 {
		return parts[1]
	}
	return "*"
}
