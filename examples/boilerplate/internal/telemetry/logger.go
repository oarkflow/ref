package telemetry

import (
	"sync"
	"time"

	"github.com/oarkflow/fh"
	"github.com/oarkflow/zlog"
)

var (
	defaultLogger *zlog.Logger
	once          sync.Once
)

// InitLogger initializes and returns the enterprise zlog logger.
func InitLogger(env, service string) *zlog.Logger {
	once.Do(func() {
		if env == "production" {
			defaultLogger = zlog.NewProductionLogger(service, env)
		} else {
			defaultLogger = zlog.NewDevelopment()
		}
	})
	return defaultLogger
}

// GetLogger returns the global logger instance or initializes a development logger.
func GetLogger() *zlog.Logger {
	if defaultLogger == nil {
		return InitLogger("development", "ref-boilerplate")
	}
	return defaultLogger
}

// HTTPLoggingMiddleware creates a FastHTTP middleware that logs all incoming requests using zlog.
func HTTPLoggingMiddleware(logger *zlog.Logger) fh.Handler {
	return func(c fh.Ctx) error {
		start := time.Now()
		method := c.Method()
		path := c.Path()
		ip := c.IP()
		userAgent := c.Get("User-Agent")

		// Execute next handlers in the pipeline
		err := c.Next()

		duration := time.Since(start)
		statusCode := c.StatusCode()
		bytesWritten := len(c.ResponseBody())

		attrs := []zlog.Attr{
			zlog.String("method", method),
			zlog.String("path", path),
			zlog.Int("status", statusCode),
			zlog.Duration("duration", duration),
			zlog.String("ip", ip),
			zlog.String("user_agent", userAgent),
			zlog.Int("bytes", bytesWritten),
		}

		if err != nil {
			attrs = append(attrs, zlog.Err(err))
			logger.Error("HTTP request failed", attrs...)
		} else if statusCode >= 500 {
			logger.Error("HTTP server error", attrs...)
		} else if statusCode >= 400 {
			logger.Warn("HTTP client error", attrs...)
		} else {
			logger.Info("HTTP request processed", attrs...)
		}

		return err
	}
}

// AuditLogger provides structured security and compliance event logging.
type AuditLogger struct {
	logger *zlog.Logger
}

// NewAuditLogger constructs an audit logger backed by zlog.
func NewAuditLogger(logger *zlog.Logger) *AuditLogger {
	return &AuditLogger{logger: logger}
}

// LogAuthSuccess records a successful user login.
func (a *AuditLogger) LogAuthSuccess(email, role, ip string) {
	a.logger.Info("User authentication successful",
		zlog.String("event", "auth.login.success"),
		zlog.String("user", email),
		zlog.String("role", role),
		zlog.String("ip", ip),
	)
}

// LogAuthFailure records a failed login attempt (brute force indicator).
func (a *AuditLogger) LogAuthFailure(email, reason, ip string) {
	a.logger.Warn("User authentication failed",
		zlog.String("event", "auth.login.failure"),
		zlog.String("user", email),
		zlog.String("reason", reason),
		zlog.String("ip", ip),
	)
}

// LogRoleChange records an administrative privilege modification.
func (a *AuditLogger) LogRoleChange(adminEmail, targetUser, oldRole, newRole, ip string) {
	a.logger.Warn("User role modified by administrator",
		zlog.String("event", "rbac.role.mutation"),
		zlog.String("admin", adminEmail),
		zlog.String("target_user", targetUser),
		zlog.String("old_role", oldRole),
		zlog.String("new_role", newRole),
		zlog.String("ip", ip),
	)
}

// LogAnomaly records a detected security anomaly from tcpguard.
func (a *AuditLogger) LogAnomaly(threatType, severity, message, ip string) {
	a.logger.Error("Security anomaly intercepted by TCPGuard",
		zlog.String("event", "security.anomaly"),
		zlog.String("threat_type", threatType),
		zlog.String("severity", severity),
		zlog.String("message", message),
		zlog.String("ip", ip),
	)
}
