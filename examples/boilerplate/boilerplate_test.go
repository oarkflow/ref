package boilerplate_test

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/oarkflow/fh"
	"github.com/oarkflow/ref/examples/boilerplate/internal/auth"
	"github.com/oarkflow/ref/examples/boilerplate/internal/domain"
	"github.com/oarkflow/ref/examples/boilerplate/internal/rbac"
	"github.com/oarkflow/ref/examples/boilerplate/internal/security"
	"github.com/oarkflow/ref/examples/boilerplate/internal/telemetry"
	"github.com/oarkflow/ref/examples/boilerplate/internal/web"
	"github.com/oarkflow/ref/platform"
	"github.com/oarkflow/zlog"
)

// ===========================================================================
// 1. Argon2id Cryptographic Password Hasher Tests
// ===========================================================================

func TestArgon2idSecurity(t *testing.T) {
	hasher := auth.NewPasswordHasher(auth.FastArgon2idConfig())

	rawPassword := "SecurePass123!@#"
	hash, err := hasher.Hash(rawPassword)
	if err != nil {
		t.Fatalf("Argon2id Hash failed: %v", err)
	}

	if !strings.HasPrefix(hash, "$argon2id$v=19$") {
		t.Fatalf("expected Argon2id PHC prefix, got %q", hash)
	}

	// Positive verification
	match, err := hasher.Verify(rawPassword, hash)
	if err != nil {
		t.Fatalf("Argon2id Verify failed: %v", err)
	}
	if !match {
		t.Fatal("expected password to match")
	}

	// Negative verification
	matchWrong, err := hasher.Verify("WrongPassword123!", hash)
	if err != nil {
		t.Fatalf("Argon2id Verify with wrong password failed: %v", err)
	}
	if matchWrong {
		t.Fatal("expected wrong password to be rejected")
	}

	// Timing attack defense against empty or invalid input
	matchEmpty, _ := hasher.Verify("", hash)
	if matchEmpty {
		t.Fatal("expected empty password to fail")
	}
}

// ===========================================================================
// 2. Auth Module Application Service Tests (Register, Login, Reset, RBAC)
// ===========================================================================

func setupAuthTest(t *testing.T) (*auth.Service, auth.UserRepository, *rbac.Manager) {
	t.Helper()
	hasher := auth.NewPasswordHasher(auth.FastArgon2idConfig())
	repo := auth.NewMemoryStore()
	if err := repo.SeedInitialUsers(hasher); err != nil {
		t.Fatalf("SeedInitialUsers failed: %v", err)
	}

	rbacMgr, err := rbac.NewRBACManager()
	if err != nil {
		t.Fatalf("NewRBACManager failed: %v", err)
	}

	svc := auth.NewService(repo, repo, repo, hasher, auth.ServiceConfig{
		SessionTTL:    1 * time.Hour,
		ResetTokenTTL: 15 * time.Minute,
	})

	return svc, repo, rbacMgr
}

func TestAuthRegister(t *testing.T) {
	svc, _, _ := setupAuthTest(t)
	ctx := context.Background()

	// 1. Success registration
	u, err := svc.Register(ctx, auth.RegisterInput{
		Name:     "David Test",
		Email:    "david@example.com",
		Password: "StrongPassword123!",
		Roles:    []string{domain.RoleUser},
	})
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}
	if u.Email != "david@example.com" {
		t.Errorf("expected email david@example.com, got %s", u.Email)
	}
	if len(u.Roles) != 1 || u.Roles[0] != domain.RoleUser {
		t.Errorf("expected role user, got %v", u.Roles)
	}

	// 2. Duplicate registration rejection
	_, errDup := svc.Register(ctx, auth.RegisterInput{
		Name:     "David Dup",
		Email:    "david@example.com",
		Password: "StrongPassword123!",
	})
	if errDup != auth.ErrUserAlreadyExists {
		t.Fatalf("expected ErrUserAlreadyExists, got %v", errDup)
	}

	// 3. Weak password rejection
	_, errWeak := svc.Register(ctx, auth.RegisterInput{
		Name:     "Weak User",
		Email:    "weak@example.com",
		Password: "weak",
	})
	if errWeak != auth.ErrWeakPassword {
		t.Fatalf("expected ErrWeakPassword, got %v", errWeak)
	}
}

func TestAuthLogin(t *testing.T) {
	svc, _, _ := setupAuthTest(t)
	ctx := context.Background()

	// 1. Valid login with pre-seeded demo user (Charlie User)
	user, sess, err := svc.Login(ctx, auth.LoginInput{
		Email:    "user@example.com",
		Password: "Password123!",
	})
	if err != nil {
		t.Fatalf("Login failed: %v", err)
	}
	if user.Email != "user@example.com" {
		t.Errorf("expected user@example.com, got %s", user.Email)
	}
	if sess.ID == "" {
		t.Fatal("expected non-empty session ID")
	}

	// Validate session
	valUser, valSess, err := svc.ValidateSession(ctx, sess.ID)
	if err != nil {
		t.Fatalf("ValidateSession failed: %v", err)
	}
	if valUser.ID != user.ID || valSess.ID != sess.ID {
		t.Fatal("session validation returned mismatched identity")
	}

	// 2. Login with wrong password
	_, _, errWrong := svc.Login(ctx, auth.LoginInput{
		Email:    "user@example.com",
		Password: "WrongPassword999!",
	})
	if errWrong != auth.ErrInvalidCredentials {
		t.Fatalf("expected ErrInvalidCredentials, got %v", errWrong)
	}

	// 3. Login with nonexistent user
	_, _, errNon := svc.Login(ctx, auth.LoginInput{
		Email:    "nonexistent@example.com",
		Password: "Password123!",
	})
	if errNon != auth.ErrInvalidCredentials {
		t.Fatalf("expected ErrInvalidCredentials, got %v", errNon)
	}
}

func TestForgotPasswordAndResetLifecycle(t *testing.T) {
	svc, _, _ := setupAuthTest(t)
	ctx := context.Background()

	email := "user@example.com"

	// 1. Request forgot password token
	plainToken, err := svc.ForgotPassword(ctx, auth.ForgotPasswordInput{Email: email})
	if err != nil {
		t.Fatalf("ForgotPassword failed: %v", err)
	}
	if plainToken == "" {
		t.Fatal("expected non-empty reset token")
	}

	// 2. Reset password with valid token
	newPassword := "BrandNewArgon2idPass123!"
	errReset := svc.ResetPassword(ctx, auth.ResetPasswordInput{
		Token:       plainToken,
		NewPassword: newPassword,
	})
	if errReset != nil {
		t.Fatalf("ResetPassword failed: %v", errReset)
	}

	// 3. Try to consume the exact same token again (Single-use enforcement)
	errReplay := svc.ResetPassword(ctx, auth.ResetPasswordInput{
		Token:       plainToken,
		NewPassword: "AnotherPassword123!",
	})
	if errReplay == nil {
		t.Fatal("expected error on replaying consumed reset token, got nil")
	}

	// 4. Verify login with the new password
	user, _, errLogin := svc.Login(ctx, auth.LoginInput{
		Email:    email,
		Password: newPassword,
	})
	if errLogin != nil {
		t.Fatalf("Login with new password failed: %v", errLogin)
	}
	if user.Email != email {
		t.Errorf("expected %s, got %s", email, user.Email)
	}

	// 5. Old password must be rejected
	_, _, errOld := svc.Login(ctx, auth.LoginInput{
		Email:    email,
		Password: "Password123!",
	})
	if errOld != auth.ErrInvalidCredentials {
		t.Fatalf("expected old password to be rejected, got %v", errOld)
	}
}

// ===========================================================================
// 3. Enterprise RBAC with oarkflow/authz Tests
// ===========================================================================

func TestRBACRoleHierarchyAndPermissions(t *testing.T) {
	_, _, rbacMgr := setupAuthTest(t)
	ctx := context.Background()

	// Super Admin checks
	superAdminPrincipal := &domain.Principal{
		ID:    "usr_superadmin",
		Roles: []string{domain.RoleSuperAdmin},
	}
	if !rbacMgr.HasRole(superAdminPrincipal.Roles, domain.RoleAdmin) {
		t.Error("expected super_admin to inherit admin")
	}
	if !rbacMgr.HasRole(superAdminPrincipal.Roles, domain.RoleManager) {
		t.Error("expected super_admin to inherit manager")
	}
	if !rbacMgr.HasPermission(ctx, superAdminPrincipal, "anything:at:all") {
		t.Error("expected super_admin to have unrestricted permission bypass")
	}

	// Admin checks
	adminPrincipal := &domain.Principal{
		ID:    "usr_admin",
		Roles: []string{domain.RoleAdmin},
	}
	if !rbacMgr.HasRole(adminPrincipal.Roles, domain.RoleManager) {
		t.Error("expected admin to inherit manager")
	}
	if !rbacMgr.HasRole(adminPrincipal.Roles, domain.RoleUser) {
		t.Error("expected admin to inherit user")
	}
	if !rbacMgr.HasPermission(ctx, adminPrincipal, rbac.PermAdminDashboard) {
		t.Error("expected admin to have admin:dashboard permission")
	}
	if !rbacMgr.HasPermission(ctx, adminPrincipal, rbac.PermUsersDelete) {
		t.Error("expected admin to have users:delete via users:* wildcard")
	}
	if !rbacMgr.HasPermission(ctx, adminPrincipal, rbac.PermReportsRead) {
		t.Error("expected admin to have reports:read inherited from manager")
	}

	// Manager checks
	managerPrincipal := &domain.Principal{
		ID:    "usr_manager",
		Roles: []string{domain.RoleManager},
	}
	if rbacMgr.HasRole(managerPrincipal.Roles, domain.RoleAdmin) {
		t.Error("manager should NOT inherit admin")
	}
	if !rbacMgr.HasRole(managerPrincipal.Roles, domain.RoleUser) {
		t.Error("manager should inherit user")
	}
	if !rbacMgr.HasPermission(ctx, managerPrincipal, rbac.PermReportsRead) {
		t.Error("manager should have reports:read")
	}
	if rbacMgr.HasPermission(ctx, managerPrincipal, rbac.PermAdminDashboard) {
		t.Error("manager should NOT have admin:dashboard")
	}

	// User checks
	userPrincipal := &domain.Principal{
		ID:    "usr_user",
		Roles: []string{domain.RoleUser},
	}
	if !rbacMgr.HasPermission(ctx, userPrincipal, rbac.PermDashboardView) {
		t.Error("user should have dashboard:view")
	}
	if rbacMgr.HasPermission(ctx, userPrincipal, rbac.PermReportsRead) {
		t.Error("user should NOT have reports:read")
	}
	if rbacMgr.HasPermission(ctx, userPrincipal, rbac.PermAdminDashboard) {
		t.Error("user should NOT have admin:dashboard")
	}
}

func TestRouteBasedAuthorization(t *testing.T) {
	_, _, rbacMgr := setupAuthTest(t)

	adminPrincipal := &domain.Principal{ID: "usr_admin", Roles: []string{domain.RoleAdmin}}
	userPrincipal := &domain.Principal{ID: "usr_user", Roles: []string{domain.RoleUser}}

	// 1. Admin route
	allowedAdmin, _ := rbacMgr.AuthorizeRoute("GET", "/dashboard/admin", adminPrincipal)
	if !allowedAdmin {
		t.Error("admin should be allowed on /dashboard/admin")
	}

	allowedUserOnAdmin, reason := rbacMgr.AuthorizeRoute("GET", "/dashboard/admin", userPrincipal)
	if allowedUserOnAdmin {
		t.Error("standard user should be denied on /dashboard/admin")
	}
	if reason == "" {
		t.Error("expected denial reason for unauthorized route")
	}

	// 2. Unauthenticated check on protected route
	allowedUnauth, _ := rbacMgr.AuthorizeRoute("GET", "/dashboard/admin", nil)
	if allowedUnauth {
		t.Error("unauthenticated caller must be denied on /dashboard/admin")
	}

	// 3. User route
	allowedUser, _ := rbacMgr.AuthorizeRoute("GET", "/dashboard", userPrincipal)
	if !allowedUser {
		t.Error("user should be allowed on /dashboard")
	}
}

// ===========================================================================
// 4. SPL Template Rendering Engine Tests (oarkflow/template + oarkflow/spl)
// ===========================================================================

func TestSPLTemplateRendering(t *testing.T) {
	engine, err := web.NewSPLRenderer(web.RendererConfig{
		TemplatesDir: "templates",
		IsDev:        true,
		AppName:      "Test App",
	})
	if err != nil {
		t.Fatalf("NewSPLRenderer failed: %v", err)
	}

	// 1. Render Login View with Auth Layout
	var buf bytes.Buffer
	err = engine.Render(&buf, "pages/auth/login", map[string]any{
		"title":    "Sign In",
		"redirect": "/dashboard",
		"email":    "test@example.com",
	})
	if err != nil {
		t.Fatalf("Render login failed: %v", err)
	}

	loginHTML := buf.String()
	if !strings.Contains(loginHTML, "Welcome Back") {
		t.Error("expected login HTML to contain 'Welcome Back'")
	}
	if !strings.Contains(loginHTML, "Sign In") {
		t.Error("expected login HTML to contain 'Sign In'")
	}
	if !strings.Contains(loginHTML, "test@example.com") {
		t.Error("expected pre-filled email in login HTML")
	}

	// 2. Render Dashboard View with Base Layout and User Data
	buf.Reset()
	err = engine.Render(&buf, "pages/dashboard/index", map[string]any{
		"title": "User Dashboard",
		"user": map[string]any{
			"Name":  "Alice Wonderland",
			"Email": "alice@example.com",
			"ID":    "usr_alice",
			"Roles": []string{"admin", "manager"},
		},
		"isAdmin":     true,
		"isManager":   true,
		"permissions": []string{"users:*", "reports:read"},
	})
	if err != nil {
		t.Fatalf("Render dashboard failed: %v", err)
	}

	dashHTML := buf.String()
	if !strings.Contains(dashHTML, "Hello, Alice Wonderland") {
		t.Error("expected dashboard HTML to contain user name")
	}
	if !strings.Contains(dashHTML, "Admin Portal") {
		t.Error("expected dashboard HTML to render Admin Portal link for admin")
	}
	if !strings.Contains(dashHTML, "users:*") {
		t.Error("expected dashboard HTML to list effective permission users:*")
	}

	// 3. Render 403 Error Page with RBAC Explanation
	buf.Reset()
	err = engine.Render(&buf, "pages/errors/403", map[string]any{
		"title":         "Access Denied",
		"message":       "Insufficient privileges",
		"requiredRoles": []string{"admin"},
		"userRoles":     []string{"user"},
	})
	if err != nil {
		t.Fatalf("Render 403 failed: %v", err)
	}

	errHTML := buf.String()
	if !strings.Contains(errHTML, "403 — Access Denied") {
		t.Error("expected 403 error page to contain '403 — Access Denied'")
	}
	if !strings.Contains(errHTML, "admin") {
		t.Error("expected 403 error page to mention required role admin")
	}
}

// ===========================================================================
// 5. Multi-File BCL Architecture & Platform Mount Tests
// ===========================================================================

func TestBCLLoadDirAndMount(t *testing.T) {
	ctx := context.Background()
	opts := platform.DefaultLoadOptions()

	// 1. Compile the multi-file BCL directory
	p, err := platform.LoadDir(ctx, "bcl", opts)
	if err != nil {
		t.Fatalf("platform.LoadDir failed on bcl directory: %v", err)
	}
	defer p.Close()

	// 2. Initialize SPL template engine
	splRenderer, err := web.NewSPLRenderer(web.RendererConfig{
		TemplatesDir: "templates",
		IsDev:        true,
		AppName:      "Test BCL App",
	})
	if err != nil {
		t.Fatalf("NewSPLRenderer failed: %v", err)
	}

	app := fh.NewFast(fh.WithTemplateEngine(splRenderer))

	// 3. Mount all BCL routes onto fh
	if err := p.Mount(app); err != nil {
		t.Fatalf("platform.Mount onto fh.App failed: %v", err)
	}

	routes := app.Routes()
	if len(routes) == 0 {
		t.Fatal("expected mounted routes on app, got 0")
	}

	// Verify key routes were registered from BCL
	expectedRoutes := map[string]string{
		"/login":                  "POST",
		"/register":               "POST",
		"/dashboard":              "GET",
		"/dashboard/admin":        "GET",
		"/api/v1/auth/login":      "POST",
		"/api/v1/auth/register":   "POST",
	}

	for expectedPath, expectedMethod := range expectedRoutes {
		found := false
		for _, r := range routes {
			if r.Path == expectedPath && r.Method == expectedMethod {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected route %s %s to be mounted from BCL", expectedMethod, expectedPath)
		}
	}
}

// ===========================================================================
// 6. Structured Logging & Security Auditing (github.com/oarkflow/zlog)
// ===========================================================================

func TestZLogStructuredAuditing(t *testing.T) {
	logger := telemetry.InitLogger("development", "boilerplate-audit-test")
	if logger == nil {
		t.Fatal("expected zlog.Logger to be initialized")
	}

	audit := telemetry.NewAuditLogger(logger)
	if audit == nil {
		t.Fatal("expected AuditLogger to be created")
	}

	// Test recording various compliance & forensic audit events
	audit.LogAuthSuccess("admin@example.com", "admin", "192.168.1.100")
	audit.LogAuthFailure("intruder@example.com", "invalid_password", "10.0.0.99")
	audit.LogRoleChange("admin@example.com", "user@example.com", "user", "manager", "192.168.1.100")
	audit.LogAnomaly("credential_stuffing_attempt", "high", "Rapid repeated logins detected", "10.0.0.99")

	// Verify direct zlog attribute logging
	logger.Info("Audit subsystem verified",
		zlog.String("module", "telemetry"),
		zlog.String("status", "operational"),
	)
}

// ===========================================================================
// 7. Business & Threat Anomaly Detection (github.com/oarkflow/tcpguard)
// ===========================================================================

func TestTCPGuardBusinessAnomalyDetection(t *testing.T) {
	logger := telemetry.InitLogger("development", "boilerplate-guard-test")
	guard, err := security.NewAnomalyGuard(logger, security.Config{
		EnforceMode: true,
	})
	if err != nil {
		t.Fatalf("NewAnomalyGuard failed: %v", err)
	}

	ctx := context.Background()

	// 1. Business Anomaly: Privilege escalation prevention
	allowed, reason := guard.EvaluateBusinessAction(ctx, "role_update", "admin", "super_admin", "user_02")
	if allowed {
		t.Fatal("expected privilege escalation to super_admin by regular admin to be denied")
	}
	if !strings.Contains(reason, "Only super_admin") {
		t.Fatalf("expected reason to mention super_admin, got %q", reason)
	}

	// 2. Business Anomaly: Protection of primary system administrator
	allowedDemote, reasonDemote := guard.EvaluateBusinessAction(ctx, "role_update", "admin", "user", "admin@example.com")
	if allowedDemote {
		t.Fatal("expected demotion of primary admin account to be denied")
	}
	if !strings.Contains(reasonDemote, "Primary system administrator cannot be demoted") {
		t.Fatalf("expected reason to protect primary admin, got %q", reasonDemote)
	}

	// 3. Legitimate operation allowed
	allowedManager, _ := guard.EvaluateBusinessAction(ctx, "role_update", "admin", "manager", "user_02")
	if !allowedManager {
		t.Fatal("expected legitimate admin role promotion to manager to be allowed")
	}
}


