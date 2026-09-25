// Package slogobserver implements observer.Observer using the standard
// library's structured logger (log/slog). It has no third-party
// dependencies and is a good default choice for adopters who just want
// visibility into kernel lifecycle events without wiring up a metrics or
// tracing backend.
//
// Usage:
//
//	obs := slogobserver.New(slog.Default())
//	sched := execution.NewScheduler(obs)
//
// Normal completions (node/effect/execution success, decision allow) log at
// Info. Policy denies log at Warn. Node failures, effect failures, and
// execution failures log at Error.
package slogobserver

import (
	"context"
	"log/slog"

	"github.com/oarkflow/ref/graph"
	"github.com/oarkflow/ref/observer"
)

// Observer implements observer.Observer by emitting structured log records.
type Observer struct {
	log *slog.Logger
}

var _ observer.Observer = (*Observer)(nil)

// New creates a slog-backed Observer. If logger is nil, slog.Default() is
// used.
func New(logger *slog.Logger) *Observer {
	if logger == nil {
		logger = slog.Default()
	}
	return &Observer{log: logger}
}

// NodeStarted logs the start of a node execution at Debug (high volume,
// usually not needed in production unless debugging).
func (o *Observer) NodeStarted(info graph.NodeInfo) {
	o.log.LogAttrs(context.Background(), slog.LevelDebug, "node started",
		slog.String("node", info.Name),
		slog.String("kind", info.Kind.String()),
		slog.Uint64("node_id", uint64(info.ID)),
	)
}

// NodeFinished logs node completion: Info on success, Error on failure.
func (o *Observer) NodeFinished(info graph.NodeInfo, err error) {
	level := slog.LevelInfo
	if err != nil {
		level = slog.LevelError
	}
	attrs := []slog.Attr{
		slog.String("node", info.Name),
		slog.String("kind", info.Kind.String()),
		slog.Uint64("node_id", uint64(info.ID)),
	}
	if err != nil {
		attrs = append(attrs, slog.String("error", err.Error()))
	}
	o.log.LogAttrs(context.Background(), level, "node finished", attrs...)
}

// DecisionMade logs a policy decision: Warn on deny (verdict 2), Info
// otherwise.
func (o *Observer) DecisionMade(policy string, verdict uint8, message string) {
	level := slog.LevelInfo
	if verdict == 2 { // execution.VerdictDeny
		level = slog.LevelWarn
	}
	o.log.LogAttrs(context.Background(), level, "decision made",
		slog.String("policy", policy),
		slog.Uint64("verdict", uint64(verdict)),
		slog.String("message", message),
	)
}

// EffectCommitted logs an effect commit: Info on success, Error on failure.
func (o *Observer) EffectCommitted(name string, err error) {
	level := slog.LevelInfo
	attrs := []slog.Attr{slog.String("effect", name)}
	if err != nil {
		level = slog.LevelError
		attrs = append(attrs, slog.String("error", err.Error()))
	}
	o.log.LogAttrs(context.Background(), level, "effect committed", attrs...)
}

// ExecutionFinished logs the end of an intent execution: Info on success,
// Error on failure.
func (o *Observer) ExecutionFinished(intent string, durationMs float64, err error) {
	level := slog.LevelInfo
	attrs := []slog.Attr{
		slog.String("intent", intent),
		slog.Float64("duration_ms", durationMs),
	}
	if err != nil {
		level = slog.LevelError
		attrs = append(attrs, slog.String("error", err.Error()))
	}
	o.log.LogAttrs(context.Background(), level, "execution finished", attrs...)
}

// SourceFetched logs an external data source fetch: Info on success, Error
// on failure (metrics.Error == true).
func (o *Observer) SourceFetched(metrics observer.SourceMetrics) {
	level := slog.LevelInfo
	if metrics.Error {
		level = slog.LevelError
	}
	o.log.LogAttrs(context.Background(), level, "source fetched",
		slog.String("source", metrics.SourceName),
		slog.String("kind", metrics.SourceKind),
		slog.String("operation", metrics.Operation),
		slog.Duration("total_time", metrics.TotalTime),
		slog.Bool("cache_hit", metrics.CacheHit),
		slog.Int64("result_count", metrics.ResultCount),
	)
}
