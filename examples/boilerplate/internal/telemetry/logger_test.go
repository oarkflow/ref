package telemetry_test

import (
	"errors"
	"testing"

	"github.com/oarkflow/ref/examples/boilerplate/internal/telemetry"
	"github.com/oarkflow/zlog"
)

func TestTelemetryLogger(t *testing.T) {
	logger := telemetry.InitLogger("development", "test-telemetry")
	if logger == nil {
		t.Fatal("expected logger to not be nil")
	}

	logger.Info("Telemetry test message",
		zlog.String("component", "telemetry"),
		zlog.Int("metric", 42),
	)

	logger.Warn("Warning test message", zlog.String("alert", "low_memory"))
	logger.Error("Error test message", zlog.Err(errors.New("simulated error")))

	audit := telemetry.NewAuditLogger(logger)
	if audit == nil {
		t.Fatal("expected AuditLogger to not be nil")
	}

	audit.LogAuthSuccess("user@example.com", "user", "127.0.0.1")
	audit.LogAuthFailure("user@example.com", "bad_password", "127.0.0.1")
	audit.LogRoleChange("admin@example.com", "user@example.com", "user", "admin", "127.0.0.1")
	audit.LogAnomaly("anomaly_rate", "high", "threshold exceeded", "127.0.0.1")
}
