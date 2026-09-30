package server

import (
	"crypto/rand"
	"encoding/hex"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// RateLimit limits mutating requests (POST, PUT, PATCH, DELETE) per identity
// with a token bucket. Reads are not limited. The zero value uses the
// defaults; a negative Rate turns limiting off.
type RateLimit struct {
	// Rate is the sustained requests per second (default 30).
	Rate float64
	// Burst is the bucket size (default 60).
	Burst int
}

func (l RateLimit) withDefaults() RateLimit {
	if l.Rate == 0 {
		l.Rate = 30
	}
	if l.Burst <= 0 {
		l.Burst = 60
	}
	return l
}

type bucket struct {
	tokens float64
	last   time.Time
}

// limiter is a set of token buckets keyed by identity.
type limiter struct {
	cfg RateLimit
	now func() time.Time

	mu      sync.Mutex
	buckets map[string]*bucket
}

func newLimiter(cfg RateLimit, now func() time.Time) *limiter {
	cfg = cfg.withDefaults()
	if cfg.Rate < 0 {
		return nil
	}
	return &limiter{cfg: cfg, now: now, buckets: map[string]*bucket{}}
}

// allow takes a token for key. When it cannot, retry is how long until one
// is available.
func (l *limiter) allow(key string) (ok bool, retry time.Duration) {
	if l == nil {
		return true, 0
	}
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.buckets[key]
	if b == nil {
		if len(l.buckets) >= 4096 { // forget buckets that have refilled
			for k, x := range l.buckets {
				if now.Sub(x.last).Seconds()*l.cfg.Rate >= float64(l.cfg.Burst) {
					delete(l.buckets, k)
				}
			}
		}
		b = &bucket{tokens: float64(l.cfg.Burst), last: now}
		l.buckets[key] = b
	}
	b.tokens = math.Min(float64(l.cfg.Burst), b.tokens+now.Sub(b.last).Seconds()*l.cfg.Rate)
	b.last = now
	if b.tokens < 1 {
		return false, time.Duration((1 - b.tokens) / l.cfg.Rate * float64(time.Second))
	}
	b.tokens--
	return true, 0
}

func isMutating(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

// ---------------------------------------------------------------------------
// Request ids
// ---------------------------------------------------------------------------

const requestIDHeader = "X-Request-Id"

func newRequestID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "req_" + hex.EncodeToString(b[:])
}

// validRequestID accepts a client-supplied id only if it is short and made of
// safe characters, since it is echoed in headers and logs.
func validRequestID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, c := range id {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_', c == '.':
		default:
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// CORS
// ---------------------------------------------------------------------------

// corsAllowed reports whether origin may call the API from a browser. With
// no AllowedOrigins the API is same-origin only.
func (s *Server) corsAllowed(origin string) bool {
	for _, o := range s.cfg.AllowedOrigins {
		if o == "*" || strings.EqualFold(o, origin) {
			return true
		}
	}
	return false
}

// cors applies the cross-origin policy to an API request and reports whether
// the request has been answered (a preflight).
func (s *Server) cors(w http.ResponseWriter, r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" || !strings.HasPrefix(r.URL.Path, "/api/") {
		return false
	}
	h := w.Header()
	h.Add("Vary", "Origin")
	preflight := r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != ""
	if !s.corsAllowed(origin) {
		if preflight {
			s.writeErr(w, errf(http.StatusForbidden, "forbidden_origin", "this origin is not allowed"))
			return true
		}
		return false
	}
	h.Set("Access-Control-Allow-Origin", origin)
	h.Set("Access-Control-Expose-Headers", "X-Request-Id, X-Next-Before, Retry-After")
	if !preflight {
		return false
	}
	h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
	h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Request-Id")
	h.Set("Access-Control-Max-Age", "600")
	w.WriteHeader(http.StatusNoContent)
	return true
}

func retryAfterSeconds(d time.Duration) string {
	return strconv.Itoa(max(1, int(math.Ceil(d.Seconds()))))
}
