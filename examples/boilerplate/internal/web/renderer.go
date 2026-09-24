package web

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/oarkflow/template"
)

// RendererConfig configures the SPL template rendering engine.
type RendererConfig struct {
	TemplatesDir string
	IsDev        bool
	AppName      string
	AppVersion   string
}

// NewSPLRenderer sets up github.com/oarkflow/template with github.com/oarkflow/spl.
func NewSPLRenderer(cfg RendererConfig) (*template.SPLEngine, error) {
	if cfg.TemplatesDir == "" {
		cfg.TemplatesDir = "./templates"
	}
	if cfg.AppName == "" {
		cfg.AppName = "REF Enterprise Boilerplate"
	}
	if cfg.AppVersion == "" {
		cfg.AppVersion = "v1.0.0"
	}

	cleanDir := filepath.Clean(cfg.TemplatesDir)

	engine := template.NewSPL(cleanDir, ".html").Config(template.SPLConfig{
		Directory:  cleanDir,
		Extension:  ".html",
		SSR:        true,
		SecureMode: false,
		Reload:     cfg.IsDev,
		Globals: map[string]any{
			"title":              "Security Portal",
			"appName":            cfg.AppName,
			"appVersion":         cfg.AppVersion,
			"currentYear":        fmt.Sprintf("%d", time.Now().Year()),
			"environment":        ternary(cfg.IsDev, "development", "production"),
			"error":              "",
			"success":            "",
			"user": map[string]any{
				"Name":  "Alice Admin",
				"Email": "admin@example.com",
				"ID":    "usr_admin_01",
				"Roles": []string{"admin"},
			},
			"isAdmin":            true,
			"isManager":          true,
			"redirect":           "/dashboard",
			"demoToken":          "",
			"resetURL":           "",
			"email":              "",
			"name":               "",
			"token":              "",
			"message":            "",
			"requiredRoles":      []string{},
			"requiredPermission": "",
			"userRoles":          []string{},
			"reports": []map[string]any{
				{"id": "REP-2026-001", "name": "Argon2id Entropy Audit", "status": "Passed (OWASP)", "updated": "Just now"},
				{"id": "REP-2026-002", "name": "RBAC Role Hierarchy Integrity", "status": "Verified (authz)", "updated": "5 mins ago"},
				{"id": "REP-2026-003", "name": "Session Cookie Hardening Check", "status": "Compliant (Strict/Lax)", "updated": "12 mins ago"},
			},
			"permissions": []string{
				"admin:dashboard", "users:*", "roles:manage", "audit:read",
				"reports:read", "reports:write", "users:read",
				"dashboard:view", "profile:view", "profile:update",
			},
			"totalUsers": 3,
			"users": []map[string]any{
				{"ID": "usr_admin_01", "Name": "Alice Admin", "Email": "admin@example.com", "Roles": []string{"admin"}, "Status": "active"},
				{"ID": "usr_manager_01", "Name": "Bob Manager", "Email": "manager@example.com", "Roles": []string{"manager"}, "Status": "active"},
				{"ID": "usr_user_01", "Name": "Charlie User", "Email": "user@example.com", "Roles": []string{"user"}, "Status": "active"},
			},
			"roles": []map[string]any{
				{"Name": "super_admin", "Description": "Unrestricted root administrator", "Inherits": []string{}, "Permissions": []string{"*"}},
				{"Name": "admin", "Description": "System governance, user administration, and security audit", "Inherits": []string{"manager"}, "Permissions": []string{"admin:dashboard", "users:*", "roles:manage", "audit:read"}},
				{"Name": "manager", "Description": "Operations, metrics and reporting", "Inherits": []string{"user"}, "Permissions": []string{"reports:read", "reports:write", "users:read"}},
				{"Name": "user", "Description": "Standard authenticated platform member", "Inherits": []string{"guest"}, "Permissions": []string{"dashboard:view", "profile:view", "profile:update"}},
				{"Name": "guest", "Description": "Public unauthenticated visitor", "Inherits": []string{}, "Permissions": []string{}},
			},
		},
	})

	return engine, nil
}

func ternary(cond bool, a, b string) string {
	if cond {
		return a
	}
	return b
}
