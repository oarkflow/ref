package security_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/oarkflow/ref/examples/boilerplate/internal/security"
	"github.com/oarkflow/ref/examples/boilerplate/internal/telemetry"
)

func TestAnomalyGuardInitialization(t *testing.T) {
	logger := telemetry.InitLogger("development", "test-service")
	guard, err := security.NewAnomalyGuard(logger, security.Config{
		EnforceMode: false,
	})
	if err != nil {
		t.Fatalf("NewAnomalyGuard failed: %v", err)
	}
	if guard.Guard() == nil {
		t.Fatal("expected inner tcpguard.Guard to not be nil")
	}
}

func TestBusinessAnomalyRules(t *testing.T) {
	logger := telemetry.InitLogger("development", "test-service")
	guard, err := security.NewAnomalyGuard(logger, security.Config{
		EnforceMode: true,
	})
	if err != nil {
		t.Fatalf("NewAnomalyGuard failed: %v", err)
	}

	ctx := context.Background()

	// Scenario 1: Admin attempts to promote user to super_admin (Should be blocked by business anomaly rule)
	allowed, reason := guard.EvaluateBusinessAction(ctx, "role_update", "admin", "super_admin", "usr_user_01")
	if allowed {
		t.Fatal("expected promotion to super_admin by regular admin to be blocked")
	}
	if reason == "" {
		t.Fatal("expected reason for blocking super_admin promotion")
	}

	// Scenario 2: Super admin promotes user to super_admin (Should be allowed)
	allowedSuper, _ := guard.EvaluateBusinessAction(ctx, "role_update", "super_admin", "super_admin", "usr_user_01")
	if !allowedSuper {
		t.Fatal("expected super_admin to be permitted to promote to super_admin")
	}

	// Scenario 3: Attempting to demote the primary admin account (Should be blocked)
	allowedDemote, reasonDemote := guard.EvaluateBusinessAction(ctx, "role_update", "admin", "user", "admin@example.com")
	if allowedDemote {
		t.Fatal("expected demotion of primary admin account to be blocked")
	}
	if reasonDemote == "" {
		t.Fatal("expected reason for blocking primary admin demotion")
	}

	// Scenario 4: Legitimate role update (admin promoting user to manager)
	allowedLegit, _ := guard.EvaluateBusinessAction(ctx, "role_update", "admin", "manager", "usr_user_01")
	if !allowedLegit {
		t.Fatal("expected legitimate role promotion to manager to be allowed")
	}
}

func TestTCPGuardHTTPRequestEvaluation(t *testing.T) {
	logger := telemetry.InitLogger("development", "test-service")
	guard, err := security.NewAnomalyGuard(logger, security.Config{
		EnforceMode: false,
	})
	if err != nil {
		t.Fatalf("NewAnomalyGuard failed: %v", err)
	}

	req, _ := http.NewRequestWithContext(context.Background(), "GET", "http://localhost:8080/dashboard/admin", nil)
	req.RemoteAddr = "10.0.0.1:54321"
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7)")
	req.Header.Set("Host", "localhost:8080")

	result, err := guard.Guard().EvaluateHTTPRequest(req)
	if err != nil {
		t.Fatalf("EvaluateHTTPRequest failed: %v", err)
	}

	t.Logf("Decision effect: %v, findings: %d", result.Decision.Effect, len(result.Decision.Findings))
}
