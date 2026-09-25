// Package backoff provides a tiny, dependency-free jittered exponential
// backoff helper shared by packages that must not import one another
// (e.g. execution and effect). It intentionally has zero imports of any
// other package in this module so it can be used from anywhere without
// creating an import cycle.
package backoff

import (
	"math/rand"
	"time"
)

// FullJitter computes a jittered exponential backoff delay using the
// "Full Jitter" algorithm described in the AWS Architecture Blog post
// "Exponential Backoff And Jitter":
//
//	sleep = random_between(0, min(cap, base * 2^attempt))
//
// Full Jitter is chosen (over "no jitter" or "equal jitter") because it
// spreads retries most evenly across the backoff window, which minimizes
// the odds of a retry storm / thundering herd when many callers back off
// on the same schedule.
//
// base is the starting backoff unit (attempt 0 uses base itself, before
// jitter). attempt is the zero-based retry attempt number. cap bounds the
// maximum pre-jitter delay; pass 0 for no cap. If base <= 0, FullJitter
// returns 0 (no delay).
//
// FullJitter is safe for concurrent use.
func FullJitter(base time.Duration, attempt int, cap time.Duration) time.Duration {
	if base <= 0 {
		return 0
	}
	if attempt < 0 {
		attempt = 0
	}

	exp := base
	for i := 0; i < attempt; i++ {
		next := exp * 2
		if next < exp { // overflow guard
			exp = time.Duration(1<<62 - 1)
			break
		}
		exp = next
		if cap > 0 && exp >= cap {
			exp = cap
			break
		}
	}
	if cap > 0 && exp > cap {
		exp = cap
	}
	if exp <= 0 {
		return 0
	}
	// math/rand's package-level functions use a global, mutex-guarded
	// source and are safe for concurrent use.
	return time.Duration(rand.Int63n(int64(exp)))
}
