// Package otelobserver implements observer.Observer using OpenTelemetry
// tracing. It is a separate, opt-in module so the core execution/observer
// packages stay free of the OpenTelemetry dependency; import this package
// only if you want spans emitted for kernel lifecycle events.
//
// Usage:
//
//	tp := sdktrace.NewTracerProvider(...) // wire up your exporter of choice
//	obs := otelobserver.New(tp.Tracer("myapp"))
//	sched := execution.NewScheduler(obs)
//
// # Limitation: no context propagation
//
// observer.Observer's methods are called with plain values (node name/kind,
// intent, durations) — never a context.Context — so this observer cannot
// participate in real span parent/child linkage the way an instrumented
// call site normally would. It approximates tracing as follows:
//
//   - Node spans: NodeStarted opens a span keyed by graph.NodeID in a
//     concurrent map; the matching NodeFinished looks it up and ends it.
//     This is correct as long as node IDs are unique for the lifetime of
//     the span (true within one plan execution) but the span has no parent
//     link to the enclosing intent span, since NodeStarted doesn't receive
//     one.
//   - Execution (intent) spans: there is no ExecutionStarted hook — only
//     ExecutionFinished(intent, durationMs, err) fires, after the fact.
//     This observer reconstructs an approximate span for the whole
//     execution by starting it at (end time - duration) and ending it
//     immediately, using trace.WithTimestamp on both Start and End. The
//     span's duration is accurate; its position in any broader trace
//     (e.g. an HTTP request span) is not established because no incoming
//     context is available.
//   - DecisionMade / EffectCommitted: these have no start/end pair at all
//     in the interface, so they are recorded as zero-duration spans
//     (started and ended immediately) rather than as events on a parent,
//     since no current span is reliably available at the call site.
//
// If your application already has a context.Context available when driving
// the scheduler, prefer wrapping the scheduler call in your own span and
// treat this observer's spans as supplementary detail, not a replacement
// for proper request tracing.
package otelobserver

import (
	"context"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/oarkflow/ref/graph"
	"github.com/oarkflow/ref/observer"
)

// Observer implements observer.Observer by emitting OpenTelemetry spans.
type Observer struct {
	tracer trace.Tracer

	mu    sync.Mutex
	nodes map[graph.NodeID]trace.Span
}

var _ observer.Observer = (*Observer)(nil)

// New creates an Observer that emits spans via tracer. If tracer is nil,
// the global no-op/registered tracer for this package's instrumentation
// name is used (otel.Tracer("github.com/oarkflow/ref/observer/otel")).
func New(tracer trace.Tracer) *Observer {
	if tracer == nil {
		tracer = trace.NewNoopTracerProvider().Tracer("github.com/oarkflow/ref/observer/otel")
	}
	return &Observer{
		tracer: tracer,
		nodes:  make(map[graph.NodeID]trace.Span),
	}
}

// NodeStarted opens a span for the node, keyed by its NodeID.
func (o *Observer) NodeStarted(info graph.NodeInfo) {
	_, span := o.tracer.Start(context.Background(), "ref.node."+info.Name,
		trace.WithAttributes(
			attribute.String("ref.node.name", info.Name),
			attribute.String("ref.node.kind", info.Kind.String()),
			attribute.Int64("ref.node.id", int64(info.ID)),
		),
	)
	o.mu.Lock()
	o.nodes[info.ID] = span
	o.mu.Unlock()
}

// NodeFinished ends the span opened by the matching NodeStarted call.
func (o *Observer) NodeFinished(info graph.NodeInfo, err error) {
	o.mu.Lock()
	span, ok := o.nodes[info.ID]
	if ok {
		delete(o.nodes, info.ID)
	}
	o.mu.Unlock()

	if !ok {
		// No matching start (e.g. observer attached mid-execution); create
		// a zero-duration span so the event is not lost.
		_, span = o.tracer.Start(context.Background(), "ref.node."+info.Name)
	}
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	} else {
		span.SetStatus(codes.Ok, "")
	}
	span.End()
}

// DecisionMade records a policy decision as a short-lived span.
func (o *Observer) DecisionMade(policy string, verdict uint8, message string) {
	_, span := o.tracer.Start(context.Background(), "ref.decision."+policy,
		trace.WithAttributes(
			attribute.String("ref.decision.policy", policy),
			attribute.Int64("ref.decision.verdict", int64(verdict)),
			attribute.String("ref.decision.message", message),
		),
	)
	if verdict == 2 { // execution.VerdictDeny
		span.SetStatus(codes.Error, "denied")
	}
	span.End()
}

// EffectCommitted records an effect commit as a short-lived span.
func (o *Observer) EffectCommitted(name string, err error) {
	_, span := o.tracer.Start(context.Background(), "ref.effect."+name,
		trace.WithAttributes(attribute.String("ref.effect.name", name)),
	)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	} else {
		span.SetStatus(codes.Ok, "")
	}
	span.End()
}

// ExecutionFinished reconstructs an approximate span for the whole
// execution, spanning [now-duration, now]. See the package doc comment for
// why this can only be approximate.
func (o *Observer) ExecutionFinished(intent string, durationMs float64, err error) {
	end := time.Now()
	start := end.Add(-time.Duration(durationMs * float64(time.Millisecond)))

	_, span := o.tracer.Start(context.Background(), "ref.execution."+intent,
		trace.WithTimestamp(start),
		trace.WithAttributes(
			attribute.String("ref.execution.intent", intent),
			attribute.Float64("ref.execution.duration_ms", durationMs),
		),
	)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	} else {
		span.SetStatus(codes.Ok, "")
	}
	span.End(trace.WithTimestamp(end))
}

// SourceFetched records an external data source fetch as a short-lived
// span.
func (o *Observer) SourceFetched(metrics observer.SourceMetrics) {
	end := time.Now()
	start := end.Add(-metrics.TotalTime)

	_, span := o.tracer.Start(context.Background(), "ref.source."+metrics.SourceName,
		trace.WithTimestamp(start),
		trace.WithAttributes(
			attribute.String("ref.source.name", metrics.SourceName),
			attribute.String("ref.source.kind", metrics.SourceKind),
			attribute.String("ref.source.operation", metrics.Operation),
			attribute.Bool("ref.source.cache_hit", metrics.CacheHit),
			attribute.Int64("ref.source.result_count", metrics.ResultCount),
		),
	)
	if metrics.Error {
		span.SetStatus(codes.Error, "source fetch failed")
	} else {
		span.SetStatus(codes.Ok, "")
	}
	span.End(trace.WithTimestamp(end))
}
