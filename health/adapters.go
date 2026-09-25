package health

import (
	"context"
	"fmt"

	"github.com/oarkflow/ref/capability"
)

// FromCircuitBreaker adapts a circuit breaker's state lookup (matching the
// shape of capability.InMemoryCircuitBreaker.State / capability.CircuitStateFunc,
// and any Redis-backed or other implementation exposing the same
// State(key string) capability.CircuitState method) into a CheckFunc.
//
// An open circuit is reported as StatusDegraded (the dependency is being
// deliberately bypassed, not necessarily unreachable at this instant); a
// half-open circuit is also reported as StatusDegraded, since it is still
// recovering; a closed circuit is StatusUp.
//
// name is used only to make the resulting error message identify which
// breaker tripped when multiple breakers are registered under different
// check names.
func FromCircuitBreaker(name string, state func(key string) capability.CircuitState, key string) CheckFunc {
	return func(ctx context.Context) CheckResult {
		switch state(key) {
		case capability.CircuitOpen:
			return CheckResult{
				Status: StatusDegraded,
				Error:  fmt.Sprintf("circuit breaker %q is open for key %q", name, key),
			}
		case capability.CircuitHalfOpen:
			return CheckResult{
				Status: StatusDegraded,
				Error:  fmt.Sprintf("circuit breaker %q is half-open for key %q", name, key),
			}
		default: // capability.CircuitClosed
			return CheckResult{Status: StatusUp}
		}
	}
}

// Note on data-store adapters: effect.EffectStore (see effect/store.go) does
// not expose a ping-style method — it deals in transactions, recovery and
// scheduled delivery, none of which are safe/cheap to invoke purely to test
// connectivity. Rather than force a synthetic adapter onto that interface,
// arbitrary dependency checks (a *sql.DB, an effect.EffectStore-backed
// store, a cache client, ...) are covered directly by Simple, e.g.:
//
//	reg.Register("primary-db", health.Simple(func(ctx context.Context) error {
//		return db.PingContext(ctx)
//	}))
