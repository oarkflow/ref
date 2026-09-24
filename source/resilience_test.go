package source

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestCircuitBreakerOpensAndRecovers(t *testing.T) {
	breaker := NewCircuitBreaker(CircuitConfig{FailureThreshold: 2, Cooldown: 10 * time.Millisecond})
	breaker.now = func() time.Time { return time.Now() }
	failure := errors.New("down")
	if _, err := breaker.Execute(context.Background(), func(context.Context) (any, error) { return nil, failure }); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if _, err := breaker.Execute(context.Background(), func(context.Context) (any, error) { return nil, failure }); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if breaker.State() != CircuitOpen {
		t.Fatalf("expected open circuit, got %v", breaker.State())
	}
	if _, err := breaker.Execute(context.Background(), func(context.Context) (any, error) { return "ok", nil }); !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("expected open-circuit rejection, got %v", err)
	}
	time.Sleep(15 * time.Millisecond)
	if breaker.State() != CircuitHalfOpen {
		t.Fatalf("expected half-open circuit, got %v", breaker.State())
	}
	value, err := breaker.Execute(context.Background(), func(context.Context) (any, error) { return "ok", nil })
	if err != nil || value != "ok" || breaker.State() != CircuitClosed {
		t.Fatalf("expected successful recovery, value=%v err=%v state=%v", value, err, breaker.State())
	}
}

func TestCircuitBreakerAllowsOnlyOneHalfOpenProbe(t *testing.T) {
	breaker := NewCircuitBreaker(CircuitConfig{FailureThreshold: 1, Cooldown: time.Millisecond})
	breaker.now = func() time.Time { return time.Now() }
	breaker.Failure()
	time.Sleep(2 * time.Millisecond)
	if err := breaker.Allow(); err != nil {
		t.Fatal(err)
	}
	if err := breaker.Allow(); !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("expected second probe rejection, got %v", err)
	}
}
