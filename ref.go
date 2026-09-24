package ref

import (
	"context"
	"fmt"

	"github.com/oarkflow/ref/capability"
	"github.com/oarkflow/ref/effect"
	"github.com/oarkflow/ref/execution"
	"github.com/oarkflow/ref/fact"
	"github.com/oarkflow/ref/graph"
	"github.com/oarkflow/ref/intent"
	"github.com/oarkflow/ref/invocation"
	"github.com/oarkflow/ref/observer"
	"github.com/oarkflow/ref/runtime"
)

// Re-exported primary types
type (
	Engine         = runtime.Engine
	Option         = runtime.Option
	DispatchResult = runtime.DispatchResult

	Invocation = invocation.Invocation
	Input      = invocation.Input

	Intent[I, O any] = intent.Intent[I, O]
	Outcome[T any]   = intent.Outcome[T]
	OutcomeMeta      = intent.OutcomeMeta
	Failure          = intent.Failure
	Category         = intent.Category

	Effect     = effect.Effect
	EffectPlan = effect.EffectPlan
	EffectKind = effect.EffectKind

	EffectError     = effect.EffectError
	EffectErrorFunc = effect.EffectErrorFunc

	Constraint = execution.Constraint
	Obligation = execution.Obligation

	NodeContext   = execution.NodeContext
	NodeExecutor  = execution.NodeExecutor
	CompiledNode  = execution.CompiledNode
	Program       = execution.Program
	Budget        = execution.Budget
	DecisionSet   = execution.DecisionSet
	Verdict       = execution.Verdict
	Trace         = execution.Trace
	NodeTrace     = execution.NodeTrace
	FactTrace     = execution.FactTrace
	DecisionTrace = execution.DecisionTrace
	EffectTrace   = execution.EffectTrace

	Observer = observer.Observer

	CircuitState = capability.CircuitState
)

const (
	LocalTransactional = effect.LocalTransactional
	DurableDelivery    = effect.DurableDelivery
	FireAndForget      = effect.FireAndForget

	NoSpeculation    = graph.NoSpeculation
	PreAuthSafe      = graph.PreAuthSafe
	PostIdentitySafe = graph.PostIdentitySafe
	PostPolicySafe   = graph.PostPolicySafe

	CircuitClosed   = capability.CircuitClosed
	CircuitOpen     = capability.CircuitOpen
	CircuitHalfOpen = capability.CircuitHalfOpen
)

// Re-exported constructors and helpers
var (
	NewEngine = runtime.NewEngine
	NewInput  = invocation.NewInput

	AcquireDispatchResult = runtime.AcquireDispatchResult
	ReleaseDispatchResult = runtime.ReleaseDispatchResult

	WithEffectStore        = runtime.WithEffectStore
	WithEffectResolver     = runtime.WithEffectResolver
	WithEffectErrorHandler = runtime.WithEffectErrorHandler
	WithObserver           = runtime.WithObserver
	WithCapability         = runtime.WithCapability

	Pure     = capability.Pure
	Read     = capability.Read
	Decision = capability.Decision

	NewCircuitBreakerCapability         = capability.NewCircuitBreakerCapability
	NewInMemoryCircuitBreaker           = capability.NewInMemoryCircuitBreaker
	NewInMemoryCircuitBreakerCapability = capability.NewInMemoryCircuitBreakerCapability
	DefaultInMemoryCircuitBreakerConfig = capability.DefaultInMemoryCircuitBreakerConfig
	NewTrace                            = execution.NewTrace
	CompareTraces                       = execution.CompareTraces
)

// NewKey creates a typed fact key with a stable definition ID.
func NewKey[T any](name string) fact.Key[T] {
	return fact.NewKey[T](name)
}

// Publish stores a typed fact by its Key in the node context.
func Publish[T any](nc *NodeContext, key fact.Key[T], value T) bool {
	return execution.Publish(nc, key, value)
}

// Require retrieves a typed fact by its Key from the node context.
func Require[T any](nc *NodeContext, key fact.Key[T]) (T, error) {
	return execution.Require(nc, key)
}

// Register registers a strongly typed intent into the engine.
func Register[I, O any](e *Engine, it intent.Intent[I, O], customDecoder ...intent.DecoderFunc[I]) error {
	return intent.Register(e.Intents(), it, customDecoder...)
}

// RegisterCapability registers a capability into the engine.
func RegisterCapability(e *Engine, reg capability.Registration) error {
	return e.RegisterCapability(reg)
}

type FHAdapter struct {
	engine *Engine
}

func NewFHAdapter(engine *Engine) *FHAdapter {
	return &FHAdapter{engine: engine}
}

func (a *FHAdapter) Capabilities() any {
	return a.engine.Capabilities()
}

func (a *FHAdapter) Intents() any {
	return a.engine.Intents()
}

func (a *FHAdapter) RegisterDefinition(value any) error {
	definition, ok := value.(*intent.Definition)
	if !ok {
		return fmt.Errorf("ref: FH definition must be *intent.Definition, got %T", value)
	}
	return a.engine.RegisterDefinition(definition)
}

func (a *FHAdapter) Compile() error {
	return a.engine.Compile()
}

func (a *FHAdapter) Dispatch(ctx context.Context, value any) (any, error) {
	inv, ok := value.(*invocation.Invocation)
	if !ok {
		return nil, fmt.Errorf("ref: FH invocation must be *invocation.Invocation, got %T", value)
	}
	return a.engine.Dispatch(ctx, inv)
}

func (a *FHAdapter) DispatchPreview(ctx context.Context, value any) (any, error) {
	inv, ok := value.(*invocation.Invocation)
	if !ok {
		return nil, fmt.Errorf("ref: FH invocation must be *invocation.Invocation, got %T", value)
	}
	return a.engine.DispatchPreview(ctx, inv)
}

func (a *FHAdapter) Plan(name string) (any, bool) {
	return a.engine.Plan(intent.Name(name))
}
