package otelobserver

import (
	"context"
	"errors"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/oarkflow/ref/graph"
	"github.com/oarkflow/ref/observer"
)

func newTestObserver(t *testing.T) (*Observer, *tracetest.InMemoryExporter) {
	t.Helper()
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	return New(tp.Tracer("test")), exporter
}

func spanNames(spans tracetest.SpanStubs) []string {
	names := make([]string, len(spans))
	for i, s := range spans {
		names[i] = s.Name
	}
	return names
}

func TestNodeSpanLifecycle(t *testing.T) {
	obs, exporter := newTestObserver(t)

	node := graph.NodeInfo{ID: 1, Name: "validate", Kind: graph.ReadNode}
	obs.NodeStarted(node)
	obs.NodeFinished(node, nil)

	spans := exporter.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("expected 1 span, got %d: %v", len(spans), spanNames(spans))
	}
	if spans[0].Name != "ref.node.validate" {
		t.Errorf("span name = %q, want ref.node.validate", spans[0].Name)
	}
}

func TestNodeSpanRecordsError(t *testing.T) {
	obs, exporter := newTestObserver(t)

	node := graph.NodeInfo{ID: 1, Name: "commit", Kind: graph.EffectNode}
	obs.NodeStarted(node)
	obs.NodeFinished(node, errors.New("boom"))

	spans := exporter.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("expected 1 span, got %d", len(spans))
	}
	if spans[0].Status.Code.String() != "Error" {
		t.Errorf("status = %v, want Error", spans[0].Status.Code)
	}
	if len(spans[0].Events) == 0 {
		t.Errorf("expected recorded error event on span")
	}
}

func TestExecutionAndSourceSpans(t *testing.T) {
	obs, exporter := newTestObserver(t)

	obs.ExecutionFinished("checkout", 10, nil)
	obs.DecisionMade("region-lock", 2, "denied")
	obs.EffectCommitted("send-email", errors.New("smtp down"))
	obs.SourceFetched(observer.SourceMetrics{
		SourceName: "users-db",
		SourceKind: "database",
		CacheHit:   true,
	})

	spans := exporter.GetSpans()
	names := spanNames(spans)
	if len(spans) != 4 {
		t.Fatalf("expected 4 spans, got %d: %v", len(spans), names)
	}

	want := map[string]bool{
		"ref.execution.checkout":   false,
		"ref.decision.region-lock": false,
		"ref.effect.send-email":    false,
		"ref.source.users-db":      false,
	}
	for _, n := range names {
		if _, ok := want[n]; ok {
			want[n] = true
		}
	}
	for n, seen := range want {
		if !seen {
			t.Errorf("expected span %q, not found in %v", n, names)
		}
	}
}
