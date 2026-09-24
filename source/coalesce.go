package source

import (
	"context"
	"fmt"
	"sync"
)

// Coalescer deduplicates identical in-flight source requests.
//
// When multiple goroutines simultaneously request the same key, only the first
// one executes the fetch function. All other callers wait for and receive the
// same result. Once the call completes, the key is released — subsequent
// requests trigger a new fetch.
//
// Unlike caching, coalescing only deduplicates concurrent in-flight requests.
// It doesn't store results beyond the lifetime of the call. Use Cache for TTL-based
// result reuse; use Coalescer when 1000 simultaneous requests asking for the same
// thing should only hit the source once.
type Coalescer struct {
	mu    sync.Mutex
	calls map[string]*coalesceCall
}

type coalesceCall struct {
	done chan struct{}
	val  any
	err  error
}

// NewCoalescer creates a new Coalescer.
func NewCoalescer() *Coalescer {
	return &Coalescer{
		calls: make(map[string]*coalesceCall),
	}
}

// Do executes fn only if no identical request (same key) is already in flight.
// If one is, it blocks until that call completes and returns the same result.
//
// The key should encode the operation and parameters (e.g. "users:id=123" or
// a hash). Do not include authorization scope in the key unless different scopes
// should share results — usually they should not.
func (c *Coalescer) Do(key string, fn func() (any, error)) (any, error) {
	return c.DoContext(context.Background(), key, fn)
}

func (c *Coalescer) DoContext(ctx context.Context, key string, fn func() (any, error)) (value any, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	c.mu.Lock()
	if call, ok := c.calls[key]; ok {
		c.mu.Unlock()
		select {
		case <-call.done:
			return call.val, call.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	call := &coalesceCall{done: make(chan struct{})}
	c.calls[key] = call
	c.mu.Unlock()

	defer func() {
		if recovered := recover(); recovered != nil {
			call.err = fmt.Errorf("ref: source coalesced call panic: %v", recovered)
			value = nil
			err = call.err
		}
		close(call.done)
		c.mu.Lock()
		if c.calls[key] == call {
			delete(c.calls, key)
		}
		c.mu.Unlock()
	}()
	call.val, call.err = fn()
	return call.val, call.err
}

// InFlight returns the number of currently coalesced keys.
func (c *Coalescer) InFlight() int {
	c.mu.Lock()
	n := len(c.calls)
	c.mu.Unlock()
	return n
}
