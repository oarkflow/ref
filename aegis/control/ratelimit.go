package control

import (
	"net"
	"net/http"
	"sync"
	"time"
)

// RateLimiter is a per-key token bucket, shared across however many keys
// (normally remote IPs) call Allow. It has no external dependency: the
// control server is meant to be both self-hosted and run by a customer on
// whatever they already have, and a rate limiter is cheap enough to write
// in a dozen lines that it is not worth a library for.
type RateLimiter struct {
	mu       sync.Mutex
	buckets  map[string]*bucket
	rate     float64 // tokens added per second
	burst    float64 // bucket capacity
	lastSwep time.Time
}

type bucket struct {
	tokens   float64
	lastSeen time.Time
}

// NewRateLimiter allows up to burst requests immediately per key, then
// refills at ratePerMinute/60 tokens per second. A key that has not been
// seen in over ten minutes is forgotten the next time Allow sweeps, so a
// long-running server does not accumulate one bucket per IP forever.
func NewRateLimiter(ratePerMinute int, burst int) *RateLimiter {
	if ratePerMinute <= 0 {
		ratePerMinute = 1
	}
	if burst <= 0 {
		burst = ratePerMinute
	}
	return &RateLimiter{
		buckets: make(map[string]*bucket),
		rate:    float64(ratePerMinute) / 60.0,
		burst:   float64(burst),
	}
}

// Allow reports whether a request under key may proceed, consuming one
// token if so.
func (r *RateLimiter) Allow(key string) bool {
	now := time.Now()
	r.mu.Lock()
	defer r.mu.Unlock()

	if now.Sub(r.lastSwep) > 10*time.Minute {
		for k, b := range r.buckets {
			if now.Sub(b.lastSeen) > 10*time.Minute {
				delete(r.buckets, k)
			}
		}
		r.lastSwep = now
	}

	b, ok := r.buckets[key]
	if !ok {
		b = &bucket{tokens: r.burst, lastSeen: now}
		r.buckets[key] = b
	} else {
		elapsed := now.Sub(b.lastSeen).Seconds()
		b.tokens += elapsed * r.rate
		if b.tokens > r.burst {
			b.tokens = r.burst
		}
		b.lastSeen = now
	}

	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// RateLimited wraps next so every request must pass limiter.Allow(key)
// first, where key is the remote IP with its port stripped (so a client
// behind no proxy is rate limited as itself, not as itself-plus-ephemeral-
// port). A request over the limit gets 429 with a Retry-After hint.
func RateLimited(limiter *RateLimiter, next http.Handler) http.Handler {
	if limiter == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		key := req.RemoteAddr
		if host, _, err := net.SplitHostPort(req.RemoteAddr); err == nil {
			key = host
		}
		if !limiter.Allow(key) {
			w.Header().Set("Retry-After", "1")
			http.Error(w, `{"error":"rate limit exceeded"}`, http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, req)
	})
}
