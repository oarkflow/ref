package source

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

var ErrCircuitOpen = errors.New("ref: source circuit is open")

type CircuitState uint8

const (
	CircuitClosed CircuitState = iota
	CircuitOpen
	CircuitHalfOpen
)

type CircuitBreaker struct {
	mu            sync.Mutex
	state         CircuitState
	failures      int
	threshold     int
	cooldown      time.Duration
	openedAt      time.Time
	probeInFlight bool
	now           func() time.Time
}

type CircuitConfig struct {
	FailureThreshold int
	Cooldown         time.Duration
}

func NewCircuitBreaker(cfg CircuitConfig) *CircuitBreaker {
	if cfg.FailureThreshold <= 0 {
		cfg.FailureThreshold = 5
	}
	if cfg.Cooldown <= 0 {
		cfg.Cooldown = 5 * time.Second
	}
	return &CircuitBreaker{state: CircuitClosed, threshold: cfg.FailureThreshold, cooldown: cfg.Cooldown, now: time.Now}
}

func (b *CircuitBreaker) State() CircuitState {
	if b == nil {
		return CircuitClosed
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.refreshLocked()
	return b.state
}

func (b *CircuitBreaker) Allow() error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.refreshLocked()
	switch b.state {
	case CircuitOpen:
		return ErrCircuitOpen
	case CircuitHalfOpen:
		if b.probeInFlight {
			return ErrCircuitOpen
		}
		b.probeInFlight = true
	}
	return nil
}

func (b *CircuitBreaker) Success() {
	if b == nil {
		return
	}
	b.mu.Lock()
	b.failures = 0
	b.probeInFlight = false
	b.state = CircuitClosed
	b.openedAt = time.Time{}
	b.mu.Unlock()
}

func (b *CircuitBreaker) Failure() {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.probeInFlight = false
	if b.state == CircuitHalfOpen || b.failures+1 >= b.threshold {
		b.state = CircuitOpen
		b.openedAt = b.now()
		return
	}
	b.failures++
}

func (b *CircuitBreaker) refreshLocked() {
	if b.state == CircuitOpen && !b.openedAt.IsZero() && b.now().Sub(b.openedAt) >= b.cooldown {
		b.state = CircuitHalfOpen
		b.probeInFlight = false
	}
}

func (b *CircuitBreaker) Execute(ctx context.Context, fn func(context.Context) (any, error)) (value any, err error) {
	if err := b.Allow(); err != nil {
		return nil, err
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			b.Failure()
			err = fmt.Errorf("source call panic: %v", recovered)
			value = nil
		}
	}()
	value, err = fn(ctx)
	if err != nil {
		b.Failure()
		return nil, err
	}
	b.Success()
	return value, nil
}
