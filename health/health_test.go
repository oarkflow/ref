package health

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/oarkflow/ref/capability"
)

func TestLivenessTrivialWhenEmpty(t *testing.T) {
	reg := NewRegistry()
	report := reg.Liveness(context.Background())
	if report.Status != StatusUp {
		t.Fatalf("expected StatusUp, got %v", report.Status)
	}
}

func TestReadinessAllHealthy(t *testing.T) {
	reg := NewRegistry()
	reg.Register("a", Simple(func(ctx context.Context) error { return nil }))
	reg.Register("b", Simple(func(ctx context.Context) error { return nil }))
	reg.RegisterLiveness("proc", Simple(func(ctx context.Context) error { return nil }))

	report := reg.Readiness(context.Background())
	if report.Status != StatusUp {
		t.Fatalf("expected StatusUp, got %v (%+v)", report.Status, report.Checks)
	}
	if len(report.Checks) != 3 {
		t.Fatalf("expected 3 checks, got %d", len(report.Checks))
	}
}

func TestReadinessOneErroringCheckIsDown(t *testing.T) {
	reg := NewRegistry()
	reg.Register("ok", Simple(func(ctx context.Context) error { return nil }))
	reg.Register("broken", Simple(func(ctx context.Context) error {
		return errors.New("connection refused")
	}))

	report := reg.Readiness(context.Background())
	if report.Status != StatusDown {
		t.Fatalf("expected StatusDown, got %v", report.Status)
	}

	var found bool
	for _, e := range report.Checks {
		if e.Name == "broken" {
			found = true
			if e.Status != StatusDown {
				t.Errorf("expected broken check to be StatusDown, got %v", e.Status)
			}
			if e.Error == "" {
				t.Errorf("expected non-empty error message")
			}
		}
	}
	if !found {
		t.Fatalf("expected to find 'broken' entry in report")
	}
}

func TestReadinessDegradedWhenNoDownButDegraded(t *testing.T) {
	reg := NewRegistry()
	reg.Register("ok", Simple(func(ctx context.Context) error { return nil }))
	reg.Register("degraded", CheckFunc(func(ctx context.Context) CheckResult {
		return CheckResult{Status: StatusDegraded, Error: "running on standby"}
	}))

	report := reg.Readiness(context.Background())
	if report.Status != StatusDegraded {
		t.Fatalf("expected StatusDegraded, got %v", report.Status)
	}
}

func TestTimeoutDoesNotHangAndReportsDown(t *testing.T) {
	reg := NewRegistry(WithTimeout(30 * time.Millisecond))
	reg.Register("slow", CheckFunc(func(ctx context.Context) CheckResult {
		<-ctx.Done() // simulate a check that respects ctx
		return CheckResult{Status: StatusUp}
	}))
	reg.Register("fast", Simple(func(ctx context.Context) error { return nil }))

	start := time.Now()
	report := reg.Readiness(context.Background())
	elapsed := time.Since(start)

	if elapsed > 2*time.Second {
		t.Fatalf("Readiness took too long (%s); timeout did not bound it", elapsed)
	}
	if report.Status != StatusDown {
		t.Fatalf("expected StatusDown due to timeout, got %v", report.Status)
	}
	for _, e := range report.Checks {
		if e.Name == "slow" && e.Status != StatusDown {
			t.Errorf("expected slow check to be StatusDown (timed out), got %v", e.Status)
		}
		if e.Name == "fast" && e.Status != StatusUp {
			t.Errorf("expected fast check to still succeed, got %v", e.Status)
		}
	}
}

func TestPanicIsRecoveredAndReportedDown(t *testing.T) {
	reg := NewRegistry()
	reg.Register("panicky", CheckFunc(func(ctx context.Context) CheckResult {
		panic("boom")
	}))
	reg.Register("ok", Simple(func(ctx context.Context) error { return nil }))

	var report Report
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("panic escaped Readiness(): %v", r)
			}
		}()
		report = reg.Readiness(context.Background())
	}()

	if report.Status != StatusDown {
		t.Fatalf("expected StatusDown, got %v", report.Status)
	}
	for _, e := range report.Checks {
		if e.Name == "panicky" {
			if e.Status != StatusDown {
				t.Errorf("expected panicky check to be StatusDown, got %v", e.Status)
			}
			if e.Error == "" {
				t.Errorf("expected panic message to be captured")
			}
		}
	}
}

func TestHTTPHandlersStatusCodesAndJSON(t *testing.T) {
	t.Run("healthy readiness returns 200", func(t *testing.T) {
		reg := NewRegistry()
		reg.Register("ok", Simple(func(ctx context.Context) error { return nil }))

		req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
		w := httptest.NewRecorder()
		ReadinessHandler(reg).ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", w.Code)
		}
		var body map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("invalid JSON body: %v", err)
		}
		if body["status"] != "up" {
			t.Fatalf("expected status=up, got %v", body["status"])
		}
	})

	t.Run("unhealthy readiness returns 503", func(t *testing.T) {
		reg := NewRegistry()
		reg.Register("broken", Simple(func(ctx context.Context) error {
			return errors.New("down")
		}))

		req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
		w := httptest.NewRecorder()
		ReadinessHandler(reg).ServeHTTP(w, req)

		if w.Code != http.StatusServiceUnavailable {
			t.Fatalf("expected 503, got %d", w.Code)
		}
		var body map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("invalid JSON body: %v", err)
		}
		if body["status"] != "down" {
			t.Fatalf("expected status=down, got %v", body["status"])
		}
	})

	t.Run("liveness handler is independent of readiness failures", func(t *testing.T) {
		reg := NewRegistry()
		reg.Register("broken", Simple(func(ctx context.Context) error {
			return errors.New("down")
		}))

		req := httptest.NewRequest(http.MethodGet, "/livez", nil)
		w := httptest.NewRecorder()
		LivenessHandler(reg).ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200 for liveness even with a failing readiness check, got %d", w.Code)
		}
	})

	t.Run("NewHTTPHandler mounts both paths", func(t *testing.T) {
		reg := NewRegistry()
		reg.Register("ok", Simple(func(ctx context.Context) error { return nil }))
		handler := NewHTTPHandler(reg)

		for _, path := range []string{"/livez", "/readyz", "/healthz"} {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)
			if w.Code != http.StatusOK {
				t.Errorf("path %s: expected 200, got %d", path, w.Code)
			}
		}
	})
}

func TestFromCircuitBreakerAdapter(t *testing.T) {
	cb := capability.NewInMemoryCircuitBreaker(capability.InMemoryCircuitBreakerConfig{
		FailureThreshold: 1,
		SuccessThreshold: 1,
		OpenTimeout:      time.Hour,
	})

	check := FromCircuitBreaker("payments", cb.State, "payments-api")

	// Closed initially -> Up.
	res := check.Check(context.Background())
	if res.Status != StatusUp {
		t.Fatalf("expected StatusUp for closed circuit, got %v", res.Status)
	}

	// Trip the breaker open.
	_ = cb.Record("payments-api", false)

	res = check.Check(context.Background())
	if res.Status != StatusDegraded {
		t.Fatalf("expected StatusDegraded for open circuit, got %v", res.Status)
	}
	if res.Error == "" {
		t.Fatalf("expected a non-empty error message for open circuit")
	}

	reg := NewRegistry()
	reg.Register("payments-circuit", check)
	report := reg.Readiness(context.Background())
	if report.Status != StatusDegraded {
		t.Fatalf("expected registry-level StatusDegraded, got %v", report.Status)
	}
}

func TestStatusString(t *testing.T) {
	cases := map[Status]string{
		StatusUp:       "up",
		StatusDegraded: "degraded",
		StatusDown:     "down",
		Status(99):     "unknown",
	}
	for s, want := range cases {
		if got := s.String(); got != want {
			t.Errorf("Status(%d).String() = %q, want %q", s, got, want)
		}
	}
}
