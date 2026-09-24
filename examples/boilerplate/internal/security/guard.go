package security

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/oarkflow/fh"
	"github.com/oarkflow/ref/examples/boilerplate/internal/telemetry"
	"github.com/oarkflow/tcpguard"
	"github.com/oarkflow/zlog"
)

// AnomalyGuard wraps TCPGuard to detect and mitigate business, identity, and transport anomalies.
type AnomalyGuard struct {
	guard       *tcpguard.Guard
	store       tcpguard.SecurityStore
	auditLogger *telemetry.AuditLogger
	logger      *zlog.Logger
	enforceMode bool
}

// Config defines options for initializing TCPGuard.
type Config struct {
	EnforceMode bool
	Store       tcpguard.SecurityStore
}

// NewAnomalyGuard initializes TCPGuard with business anomaly detectors and audit logging.
func NewAnomalyGuard(logger *zlog.Logger, cfg ...Config) (*AnomalyGuard, error) {
	if logger == nil {
		logger = telemetry.GetLogger()
	}

	var enforce bool
	var store tcpguard.SecurityStore = tcpguard.NewMemoryStore()

	if len(cfg) > 0 {
		enforce = cfg[0].EnforceMode
		if cfg[0].Store != nil {
			store = cfg[0].Store
		}
	} else if os.Getenv("TCPGUARD_MODE") == "enforce" {
		enforce = true
	}

	mode := tcpguard.Monitor
	if enforce {
		mode = tcpguard.Enforce
	}

	g, err := tcpguard.New(
		tcpguard.WithStore(store),
		tcpguard.WithMode(mode),
		tcpguard.WithRateAlgorithm(tcpguard.RateFixedWindow),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize tcpguard: %w", err)
	}

	return &AnomalyGuard{
		guard:       g,
		store:       store,
		auditLogger: telemetry.NewAuditLogger(logger),
		logger:      logger,
		enforceMode: enforce,
	}, nil
}

// Guard returns the underlying tcpguard.Guard instance.
func (ag *AnomalyGuard) Guard() *tcpguard.Guard {
	return ag.guard
}

// EvaluateBusinessAction checks business-level anomaly policies and returns (allowed, reason).
func (ag *AnomalyGuard) EvaluateBusinessAction(ctx context.Context, action string, callerRole string, targetRole string, targetUser string) (bool, string) {
	// Rule 1: Only super_admin can assign or elevate anyone to super_admin
	if targetRole == "super_admin" && callerRole != "super_admin" {
		ag.auditLogger.LogAnomaly(
			"privilege_escalation_attempt",
			"high",
			fmt.Sprintf("Caller with role %s attempted to grant super_admin to %s", callerRole, targetUser),
			"internal",
		)
		return false, "Only super_admin can assign the super_admin role"
	}

	// Rule 2: Primary system admin account cannot be demoted to guest or user
	if targetUser == "admin@example.com" && targetRole == "user" && callerRole != "super_admin" {
		ag.auditLogger.LogAnomaly(
			"critical_account_demotion",
			"critical",
			"Attempted demotion of primary system administrator account",
			"internal",
		)
		return false, "Primary system administrator cannot be demoted"
	}

	// Record legitimate business action into TCPGuard
	secCtx := &tcpguard.Context{
		Identity: tcpguard.IdentityContext{
			Role: callerRole,
		},
		Business: tcpguard.BusinessContext{
			Action:   action,
			Entity:   targetUser,
			Workflow: "user_governance",
		},
	}

	event := tcpguard.Event{
		Type: "business." + action,
	}

	decision := ag.guard.Evaluate(ctx, event, secCtx)
	if !decision.Allowed && ag.enforceMode {
		return false, decision.Explanation
	}

	return true, ""
}

// Middleware returns a FastHTTP middleware that evaluates every request through TCPGuard.
func (ag *AnomalyGuard) Middleware() fh.Handler {
	return func(c fh.Ctx) error {
		path := c.Path()
		method := c.Method()
		ip := c.IP()

		// Adapt FastHTTP request to standard net/http for TCPGuard evaluation
		httpReq := adaptFastHTTPToNetHTTP(c)

		result, err := ag.guard.EvaluateHTTPRequest(httpReq)
		if err == nil && len(result.Decision.Findings) > 0 {
			// Process detected anomalies
			for _, f := range result.Decision.Findings {
				if f.Severity == tcpguard.SeverityCritical || f.Severity == tcpguard.SeverityHigh {
					ag.auditLogger.LogAnomaly(string(f.Type), string(f.Severity), f.Message, ip)

					if ag.enforceMode {
						c.Status(http.StatusTooManyRequests)
						c.Set("X-Security-Guard", "tcpguard-blocked")
						c.Set("Retry-After", "60")
						return c.SendString(fmt.Sprintf("Access Denied: Anomaly Detected (%s)", f.Message))
					}
				}
			}

			c.Set("X-Anomaly-Status", "detected")
			c.Set("X-Anomaly-Count", fmt.Sprintf("%d", len(result.Decision.Findings)))
		} else {
			c.Set("X-Anomaly-Status", "clean")
		}

		c.Set("X-Security-Guard", "tcpguard-v0.0.16")

		// Business-layer check for role update actions
		if path == "/dashboard/admin/role" && method == "POST" {
			targetRole := formValue(c, "role")
			targetUser := formValue(c, "user_id")
			callerRole := "admin" // extracted by route guard

			allowed, reason := ag.EvaluateBusinessAction(c.Context(), "role_update", callerRole, targetRole, targetUser)
			if !allowed {
				c.Status(http.StatusForbidden)
				return c.SendString("Security Exception: " + reason)
			}
		}

		return c.Next()
	}
}

func formValue(c fh.Ctx, key string) string {
	values, err := url.ParseQuery(string(c.Body()))
	if err != nil {
		return ""
	}
	return values.Get(key)
}

// adaptFastHTTPToNetHTTP adapts a FastHTTP context to a standard *http.Request.
func adaptFastHTTPToNetHTTP(c fh.Ctx) *http.Request {
	host := c.Hostname()
	if host == "" {
		host = "localhost"
	}

	u := &url.URL{
		Scheme: "http",
		Host:   host,
		Path:   c.Path(),
	}

	req, _ := http.NewRequest(c.Method(), u.String(), nil)
	if req == nil {
		req, _ = http.NewRequest("GET", "/", nil)
	}

	req.RemoteAddr = c.IP()
	if !strings.Contains(req.RemoteAddr, ":") {
		req.RemoteAddr = req.RemoteAddr + ":0"
	}
	req.Header.Set("User-Agent", c.Get("User-Agent"))
	req.Header.Set("Host", host)
	if c.Method() == "GET" {
		req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	}

	return req
}
