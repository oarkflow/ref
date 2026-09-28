// Package telemetry is bootstrapping glue for OpenTelemetry tracing — Go
// code because there is no way to declare an OTLP exporter or a
// TracerProvider in BCL, the same reason internal/web's SPL renderer and
// internal/ops's maintenance gate exist as their own Go packages rather
// than as application logic.
//
// Every DAG node/decision/effect/execution ref/platform runs is already
// observed through observer.Observer (see cmd/server/main.go's Observers
// slice); this package turns that same stream into OpenTelemetry spans via
// github.com/oarkflow/ref/observer/otel, exported over OTLP/HTTP — read
// that package's own doc comment for the one real limitation (no incoming
// context, so node/execution spans cannot link to a caller's own trace).
package telemetry

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

// NewTracerProvider builds an OTLP/HTTP tracer provider pointed at
// endpoint — a bare host:port, matching the OTEL_EXPORTER_OTLP_ENDPOINT
// convention (e.g. "localhost:4318" for a local otel-collector, no
// scheme) — tagged with appName/appVersion and, when set, replicaID.
//
// The returned shutdown func flushes buffered spans and closes the
// exporter; call it once, on process shutdown, with a bounded context —
// it blocks until the flush completes or that context expires.
func NewTracerProvider(ctx context.Context, endpoint, appName, appVersion, replicaID string) (trace.TracerProvider, func(context.Context) error, error) {
	exporter, err := otlptracehttp.New(ctx,
		otlptracehttp.WithEndpoint(endpoint),
		// This starter has no example of a collector reachable only over
		// TLS; a deployment that needs it drops WithInsecure() and, if the
		// collector's certificate isn't publicly trusted, adds
		// otlptracehttp.WithTLSClientConfig.
		otlptracehttp.WithInsecure(),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("telemetry: otlp/http exporter (endpoint %q): %w", endpoint, err)
	}

	attrs := []attribute.KeyValue{
		semconv.ServiceName(appName),
		semconv.ServiceVersion(appVersion),
	}
	if replicaID != "" {
		attrs = append(attrs, semconv.ServiceInstanceID(replicaID))
	}
	res := resource.NewWithAttributes(semconv.SchemaURL, attrs...)

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
	)
	return tp, tp.Shutdown, nil
}
