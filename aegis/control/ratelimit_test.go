package control

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRateLimiterAllowsUpToBurstThenBlocks(t *testing.T) {
	rl := NewRateLimiter(60, 3) // 1/sec refill, burst 3
	for i := 0; i < 3; i++ {
		if !rl.Allow("client-a") {
			t.Fatalf("request %d should be allowed within the burst", i)
		}
	}
	if rl.Allow("client-a") {
		t.Fatal("the 4th immediate request should be blocked")
	}
}

func TestRateLimiterKeysAreIndependent(t *testing.T) {
	rl := NewRateLimiter(60, 1)
	if !rl.Allow("client-a") {
		t.Fatal("client-a's first request should be allowed")
	}
	if !rl.Allow("client-b") {
		t.Fatal("client-b is a different key and should not be affected by client-a's usage")
	}
}

func TestRateLimitedMiddlewareReturns429(t *testing.T) {
	rl := NewRateLimiter(60, 1)
	handler := RateLimited(rl, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.RemoteAddr = "203.0.113.5:54321"

	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, req)
	if rec1.Code != http.StatusOK {
		t.Fatalf("first request: got %d, want 200", rec1.Code)
	}

	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req)
	if rec2.Code != http.StatusTooManyRequests {
		t.Fatalf("second request (over burst): got %d, want 429", rec2.Code)
	}
	if rec2.Header().Get("Retry-After") == "" {
		t.Error("expected a Retry-After header on 429")
	}
}

func TestRateLimitedWithNilLimiterPassesThrough(t *testing.T) {
	handler := RateLimited(nil, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	for i := 0; i < 5; i++ {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d: a nil limiter should never block, got %d", i, rec.Code)
		}
	}
}

func TestServerRateLimitOptionAppliesToRealEndpoint(t *testing.T) {
	srv := NewServer(NewMemoryStore(), WithServerRateLimit(60, 1))
	req := httptest.NewRequest(http.MethodGet, "/v1/package/does-not-exist/status", nil)
	req.RemoteAddr = "198.51.100.9:1111"

	rec1 := httptest.NewRecorder()
	srv.ServeHTTP(rec1, req)
	if rec1.Code == http.StatusTooManyRequests {
		t.Fatalf("first request should not be rate limited (burst 1): got %d", rec1.Code)
	}
	rec2 := httptest.NewRecorder()
	srv.ServeHTTP(rec2, req)
	if rec2.Code != http.StatusTooManyRequests {
		t.Fatalf("second request within the burst window: got %d, want 429", rec2.Code)
	}
}

// TestHealthzIsNeverRateLimited proves the exemption in ServeHTTP: a
// liveness probe (polled far more often than any real client calls the
// sensitive endpoints) must never share a quota with them, or routine
// health checks from the same IP as a real client would starve that
// client's own requests.
func TestHealthzIsNeverRateLimited(t *testing.T) {
	srv := NewServer(NewMemoryStore(), WithServerRateLimit(60, 1))
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.RemoteAddr = "198.51.100.9:1111"

	for i := 0; i < 10; i++ {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("/healthz request %d: got %d, want 200 (never rate limited)", i, rec.Code)
		}
	}
}
