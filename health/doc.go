// Package health provides a first-class health-check primitive for REF-based
// services: liveness (is the process alive?) and readiness (can it serve
// traffic, including external dependencies?) reporting, with a thread-safe
// registry, per-check timeouts, panic recovery, and a stdlib net/http
// handler.
//
// # Concepts
//
// A Checker is anything that can report its own health for a given context.
// Most checks are simple boolean-outcome operations (ping a database, check
// a queue depth, verify a circuit breaker isn't open), so the common case is
// covered by the CheckFunc adapter and the Simple helper, which wraps a
// plain `func(ctx context.Context) error` the way the rest of this codebase
// adapts plain functions to single-method interfaces (see
// capability.ProducerFunc).
//
// Checks are registered against a Registry under one of two categories:
//
//   - Register registers a "readiness" check — typically one that reaches
//     out to an external dependency (a database, cache, downstream API, a
//     capability.CircuitBreaker's state, ...). Readiness reports failures
//     here as not-ready.
//   - RegisterLiveness registers a "liveness" check — by convention these
//     must be cheap and have no external dependencies (e.g. "can this
//     process allocate memory / respond at all"). Liveness checks also
//     count towards readiness, since a live process that fails its own
//     liveness checks obviously isn't ready either.
//
// Registry.Liveness runs only the liveness-registered checks (if none were
// registered, liveness trivially reports Up — the mere fact the process
// executed the check is proof of life). Registry.Readiness runs every
// registered check (liveness + readiness) in parallel, each bounded by a
// per-check timeout, with panics recovered and reported as a Down result
// rather than crashing the caller.
//
// # HTTP wiring
//
//	reg := health.NewRegistry()
//	reg.RegisterLiveness("process", health.Simple(func(ctx context.Context) error {
//		return nil // the process is running this code, so it's alive
//	}))
//	reg.Register("primary-db", health.Simple(func(ctx context.Context) error {
//		return db.PingContext(ctx)
//	}))
//	reg.Register("payments-circuit", health.FromCircuitBreaker("payments-circuit", cb.State, "payments-api"))
//
//	mux := http.NewServeMux()
//	mux.Handle("/livez", health.LivenessHandler(reg))
//	mux.Handle("/readyz", health.ReadinessHandler(reg))
//	// or, equivalently, a single handler that serves both paths:
//	mux.Handle("/", health.NewHTTPHandler(reg))
//
//	http.ListenAndServe(":8080", mux)
//
// # Wiring into runtime.Engine
//
// runtime.WithHealthRegistry(reg) attaches a *health.Registry to an Engine
// (retrievable via Engine.Health()) so application code can register
// engine-aware checks (e.g. against the effect store, capability circuit
// breakers, etc.) without the engine itself depending on any particular
// health-check policy.
package health
