package capability

import (
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newTestRedisCircuitBreaker(t *testing.T, cfg RedisCircuitBreakerConfig) (*RedisCircuitBreaker, *miniredis.Miniredis) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("failed to start miniredis: %v", err)
	}
	t.Cleanup(mr.Close)

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	cb := NewRedisCircuitBreaker(client, cfg)
	return cb, mr
}

func TestRedisCircuitBreaker_ClosedToOpenOnFailureThreshold(t *testing.T) {
	cb, _ := newTestRedisCircuitBreaker(t, RedisCircuitBreakerConfig{
		FailureThreshold: 3,
		SuccessThreshold: 2,
		OpenTimeout:      time.Minute,
	})

	key := "svc-a"

	for i := 0; i < 2; i++ {
		allowed, err := cb.Allow(key)
		if err != nil {
			t.Fatalf("Allow: %v", err)
		}
		if !allowed {
			t.Fatalf("expected allowed while closed (iteration %d)", i)
		}
		if err := cb.Record(key, false); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}

	if state := cb.State(key); state != CircuitClosed {
		t.Fatalf("expected still closed after 2 failures, got %v", state)
	}

	// third failure should trip the breaker open
	if err := cb.Record(key, false); err != nil {
		t.Fatalf("Record: %v", err)
	}

	if state := cb.State(key); state != CircuitOpen {
		t.Fatalf("expected open after reaching failure threshold, got %v", state)
	}

	allowed, err := cb.Allow(key)
	if err != nil {
		t.Fatalf("Allow: %v", err)
	}
	if allowed {
		t.Fatalf("expected not allowed while open")
	}
}

func TestRedisCircuitBreaker_OpenBlocksUntilTimeout(t *testing.T) {
	cb, _ := newTestRedisCircuitBreaker(t, RedisCircuitBreakerConfig{
		FailureThreshold: 1,
		SuccessThreshold: 1,
		OpenTimeout:      150 * time.Millisecond,
	})

	key := "svc-b"

	if _, err := cb.Allow(key); err != nil {
		t.Fatalf("Allow: %v", err)
	}
	if err := cb.Record(key, false); err != nil {
		t.Fatalf("Record: %v", err)
	}

	if state := cb.State(key); state != CircuitOpen {
		t.Fatalf("expected open, got %v", state)
	}

	allowed, err := cb.Allow(key)
	if err != nil {
		t.Fatalf("Allow: %v", err)
	}
	if allowed {
		t.Fatalf("expected blocked immediately after opening")
	}

	time.Sleep(200 * time.Millisecond)

	if state := cb.State(key); state != CircuitHalfOpen {
		t.Fatalf("expected half_open after timeout elapsed, got %v", state)
	}

	allowed, err = cb.Allow(key)
	if err != nil {
		t.Fatalf("Allow: %v", err)
	}
	if !allowed {
		t.Fatalf("expected probe allowed once timeout elapsed")
	}
}

func TestRedisCircuitBreaker_HalfOpenToClosedOnSuccessThreshold(t *testing.T) {
	cb, _ := newTestRedisCircuitBreaker(t, RedisCircuitBreakerConfig{
		FailureThreshold: 1,
		SuccessThreshold: 2,
		OpenTimeout:      50 * time.Millisecond,
	})

	key := "svc-c"

	if _, err := cb.Allow(key); err != nil {
		t.Fatalf("Allow: %v", err)
	}
	if err := cb.Record(key, false); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if state := cb.State(key); state != CircuitOpen {
		t.Fatalf("expected open, got %v", state)
	}

	time.Sleep(80 * time.Millisecond)

	allowed, err := cb.Allow(key)
	if err != nil {
		t.Fatalf("Allow: %v", err)
	}
	if !allowed {
		t.Fatalf("expected probe allowed in half-open")
	}
	if state := cb.State(key); state != CircuitHalfOpen {
		t.Fatalf("expected half_open, got %v", state)
	}

	if err := cb.Record(key, true); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if state := cb.State(key); state != CircuitHalfOpen {
		t.Fatalf("expected still half_open after 1 of 2 successes, got %v", state)
	}

	if err := cb.Record(key, true); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if state := cb.State(key); state != CircuitClosed {
		t.Fatalf("expected closed after success threshold reached, got %v", state)
	}
}

func TestRedisCircuitBreaker_HalfOpenToOpenOnFailure(t *testing.T) {
	cb, _ := newTestRedisCircuitBreaker(t, RedisCircuitBreakerConfig{
		FailureThreshold: 1,
		SuccessThreshold: 2,
		OpenTimeout:      50 * time.Millisecond,
	})

	key := "svc-d"

	if _, err := cb.Allow(key); err != nil {
		t.Fatalf("Allow: %v", err)
	}
	if err := cb.Record(key, false); err != nil {
		t.Fatalf("Record: %v", err)
	}

	time.Sleep(80 * time.Millisecond)

	if _, err := cb.Allow(key); err != nil {
		t.Fatalf("Allow: %v", err)
	}
	if state := cb.State(key); state != CircuitHalfOpen {
		t.Fatalf("expected half_open, got %v", state)
	}

	// a failure while probing should re-open the circuit immediately
	if err := cb.Record(key, false); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if state := cb.State(key); state != CircuitOpen {
		t.Fatalf("expected open after half-open probe failure, got %v", state)
	}

	allowed, err := cb.Allow(key)
	if err != nil {
		t.Fatalf("Allow: %v", err)
	}
	if allowed {
		t.Fatalf("expected blocked immediately after re-opening")
	}
}

func TestRedisCircuitBreaker_ConcurrentAllowRecordDoesNotCorruptState(t *testing.T) {
	cb, _ := newTestRedisCircuitBreaker(t, RedisCircuitBreakerConfig{
		FailureThreshold: 10,
		SuccessThreshold: 3,
		OpenTimeout:      100 * time.Millisecond,
	})

	key := "svc-concurrent"

	const goroutines = 20
	const iterations = 25

	var wg sync.WaitGroup
	wg.Add(goroutines)

	for g := 0; g < goroutines; g++ {
		go func(id int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				allowed, err := cb.Allow(key)
				if err != nil {
					t.Errorf("Allow: %v", err)
					return
				}
				if allowed {
					// alternate success/failure to exercise both paths
					success := (id+i)%2 == 0
					if err := cb.Record(key, success); err != nil {
						t.Errorf("Record: %v", err)
						return
					}
				}
				_ = cb.State(key)
			}
		}(g)
	}

	wg.Wait()

	// After the storm, state must be one of the three valid states - the
	// real assertion here is that -race finds no data race and the script
	// evaluation never leaves the hash in a torn/invalid state.
	switch state := cb.State(key); state {
	case CircuitClosed, CircuitOpen, CircuitHalfOpen:
		// ok
	default:
		t.Fatalf("unexpected/corrupted state: %v", state)
	}
}
