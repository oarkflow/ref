package runtime

import (
	"github.com/oarkflow/ref/capability"
	"github.com/oarkflow/ref/effect"
	"github.com/oarkflow/ref/health"
	"github.com/oarkflow/ref/observer"
)

// Option configures the REF Engine.
type Option func(*Engine)

// WithEffectStore configures a custom durable EffectStore.
func WithEffectStore(store effect.EffectStore) Option {
	return func(e *Engine) {
		e.effectStore = store
	}
}

func WithEffectResolver(name string, resolver effect.EffectResolver) Option {
	return func(e *Engine) {
		if name != "" && resolver != nil {
			e.effectResolvers[name] = resolver
		}
	}
}

func WithEffectErrorHandler(fn effect.EffectErrorFunc) Option {
	return func(e *Engine) {
		e.effectErrorHandler = fn
	}
}

// WithObserver attaches an observer to all plan executions.
func WithObserver(obs observer.Observer) Option {
	return func(e *Engine) {
		e.observers = append(e.observers, obs)
	}
}

// WithCapability registers a capability at engine initialization.
// Panics if the registration fails (duplicate name or conflicting fact provider),
// since this is a programmer error that must be caught at startup.
func WithCapability(reg capability.Registration) Option {
	return func(e *Engine) {
		if err := e.capabilities.Register(reg); err != nil {
			panic("ref: WithCapability: " + err.Error())
		}
	}
}

// TryCapability registers a capability at engine initialization.
// Returns an error instead of panicking — use when registration failure
// is expected (e.g. optional capabilities).
func TryCapability(reg capability.Registration) Option {
	return func(e *Engine) {
		_ = e.capabilities.Register(reg)
	}
}

// WithHealthRegistry attaches a *health.Registry to the Engine, retrievable
// via Engine.Health(). The engine itself does not populate or depend on
// the registry — this option only gives application code a well-known
// place to register engine-related checks (effect store connectivity,
// capability circuit breakers, etc.) and to serve them over HTTP via
// health.NewHTTPHandler / health.LivenessHandler / health.ReadinessHandler.
func WithHealthRegistry(reg *health.Registry) Option {
	return func(e *Engine) {
		e.healthRegistry = reg
	}
}
