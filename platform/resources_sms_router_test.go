package platform

import (
	"context"
	"testing"

	"github.com/oarkflow/ref/platform/spi"
)

// mockSMSProvider is a controllable test provider.
type mockSMSProvider struct {
	id          string
	submitFunc  func(ctx context.Context, msg spi.SMSMessage) (spi.SMSResult, error)
	healthFunc  func(ctx context.Context) spi.SMSHealthReport
	submitsList []spi.SMSMessage
}

func (m *mockSMSProvider) ProviderID() string {
	return m.id
}

func (m *mockSMSProvider) Submit(ctx context.Context, msg spi.SMSMessage) (spi.SMSResult, error) {
	m.submitsList = append(m.submitsList, msg)
	if m.submitFunc != nil {
		return m.submitFunc(ctx, msg)
	}
	return spi.SMSResult{
		OK:            true,
		ProviderMsgID: "msg_" + m.id,
		ProviderID:    m.id,
		Segments:      1,
		LatencyMs:     20,
		Attempts:      1,
	}, nil
}

func (m *mockSMSProvider) Health(ctx context.Context) spi.SMSHealthReport {
	if m.healthFunc != nil {
		return m.healthFunc(ctx)
	}
	return spi.SMSHealthReport{
		Available:   true,
		SuccessRate: 1.0,
		OpenWindow:  10,
	}
}

func TestSMSRouterFailover(t *testing.T) {
	provA := &mockSMSProvider{
		id: "provider_a",
		submitFunc: func(ctx context.Context, msg spi.SMSMessage) (spi.SMSResult, error) {
			return spi.SMSResult{
				OK:         false,
				ProviderID: "provider_a",
				Error: &spi.SMSError{
					Kind:      "transport",
					Message:   "connection reset",
					Retryable: true,
				},
			}, nil
		},
	}

	provB := &mockSMSProvider{
		id: "provider_b",
		submitFunc: func(ctx context.Context, msg spi.SMSMessage) (spi.SMSResult, error) {
			return spi.SMSResult{
				OK:            true,
				ProviderMsgID: "b_12345",
				ProviderID:    "provider_b",
				Segments:      1,
				LatencyMs:     15,
			}, nil
		},
	}

	router := &SMSRouter{
		name: "test_router",
		cfg: SMSRouterConfig{
			Failover:   true,
			MaxRetries: 3,
		},
		providers: map[string]*routerProvider{
			"provider_a": {
				name:     "provider_a",
				provider: provA,
				priority: 10,
				tier:     1,
				enabled:  true,
			},
			"provider_b": {
				name:     "provider_b",
				provider: provB,
				priority: 5,
				tier:     1,
				enabled:  true,
			},
		},
	}

	res, err := router.Submit(context.Background(), spi.SMSMessage{
		To:   "+9779800000001",
		Text: "Test failover message",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.OK {
		t.Fatalf("expected successful failover, got failure: %+v", res.Error)
	}
	if res.ProviderID != "provider_b" {
		t.Fatalf("expected provider_b to handle message, got: %s", res.ProviderID)
	}
	if res.Attempts != 2 {
		t.Fatalf("expected 2 attempts, got: %d", res.Attempts)
	}

	metrics := router.Metrics()
	if metrics["failovers_total"].(int64) != 1 {
		t.Fatalf("expected 1 failover recorded in metrics, got %v", metrics["failovers_total"])
	}
}

func TestSMSRouterHealthFilter(t *testing.T) {
	provHealthy := &mockSMSProvider{id: "healthy_p"}
	provUnhealthy := &mockSMSProvider{
		id: "unhealthy_p",
		healthFunc: func(ctx context.Context) spi.SMSHealthReport {
			return spi.SMSHealthReport{
				Available:   false,
				SuccessRate: 0.10, // below 0.80 threshold
			}
		},
	}

	router := &SMSRouter{
		name: "health_router",
		cfg: SMSRouterConfig{
			Failover:       true,
			MaxRetries:     2,
			MinSuccessRate: 0.80,
		},
		providers: map[string]*routerProvider{
			"unhealthy_p": {
				name:     "unhealthy_p",
				provider: provUnhealthy,
				health:   provUnhealthy,
				priority: 100, // higher priority, but unhealthy
				tier:     1,
				enabled:  true,
			},
			"healthy_p": {
				name:     "healthy_p",
				provider: provHealthy,
				health:   provHealthy,
				priority: 10,
				tier:     1,
				enabled:  true,
			},
		},
	}

	res, err := router.Submit(context.Background(), spi.SMSMessage{
		To:   "+15551234567",
		Text: "Health priority test",
	})
	if err != nil || !res.OK {
		t.Fatalf("unexpected failure: %v, %+v", err, res)
	}
	// Healthy provider should be selected first despite lower initial priority
	if res.ProviderID != "healthy_p" {
		t.Fatalf("expected healthy_p to be selected first, got: %s", res.ProviderID)
	}
}

func TestSMSRouterCircuitBreaker(t *testing.T) {
	provA := &mockSMSProvider{id: "prov_a"}
	provB := &mockSMSProvider{id: "prov_b"}

	cbA := &mockCircuitBreaker{allowed: false} // open circuit

	router := &SMSRouter{
		name: "cb_router",
		cfg: SMSRouterConfig{
			Failover:   true,
			MaxRetries: 2,
		},
		providers: map[string]*routerProvider{
			"prov_a": {
				name:     "prov_a",
				provider: provA,
				cb:       cbA,
				priority: 50,
				tier:     1,
				enabled:  true,
			},
			"prov_b": {
				name:     "prov_b",
				provider: provB,
				priority: 10,
				tier:     1,
				enabled:  true,
			},
		},
	}

	res, err := router.Submit(context.Background(), spi.SMSMessage{
		To:   "+15551234567",
		Text: "Circuit breaker test",
	})
	if err != nil || !res.OK {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.ProviderID != "prov_b" {
		t.Fatalf("expected prov_b because prov_a circuit is open, got: %s", res.ProviderID)
	}
}

type mockCircuitBreaker struct {
	allowed bool
}

func (m *mockCircuitBreaker) Allow(ctx context.Context, key string) (bool, error) {
	return m.allowed, nil
}

func (m *mockCircuitBreaker) Record(ctx context.Context, key string, success bool) error {
	return nil
}

func TestSMSRecordDLR(t *testing.T) {
	router := &SMSRouter{
		name: "dlr_router",
		providers: map[string]*routerProvider{
			"p1": {
				name:    "p1",
				enabled: true,
			},
		},
	}

	router.RecordDLR(context.Background(), "p1", "DELIVRD", 45)
	p := router.providers["p1"]
	if p.submitsSuccess.Load() != 1 {
		t.Fatalf("expected 1 success recorded, got %d", p.submitsSuccess.Load())
	}

	router.RecordDLR(context.Background(), "p1", "UNDELIV", 55)
	if p.submitsFailed.Load() != 1 {
		t.Fatalf("expected 1 failed recorded, got %d", p.submitsFailed.Load())
	}
}
