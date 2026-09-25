package slogobserver

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/oarkflow/ref/graph"
	"github.com/oarkflow/ref/observer"
)

func newTestObserver(buf *bytes.Buffer, level slog.Level) *Observer {
	handler := slog.NewTextHandler(buf, &slog.HandlerOptions{Level: level})
	return New(slog.New(handler))
}

func TestNodeFinishedLogsInfoOnSuccess(t *testing.T) {
	var buf bytes.Buffer
	obs := newTestObserver(&buf, slog.LevelDebug)

	obs.NodeStarted(graph.NodeInfo{ID: 1, Name: "validate", Kind: graph.ReadNode})
	obs.NodeFinished(graph.NodeInfo{ID: 1, Name: "validate", Kind: graph.ReadNode}, nil)

	out := buf.String()
	if !strings.Contains(out, "node started") {
		t.Errorf("expected 'node started' log line, got: %s", out)
	}
	if !strings.Contains(out, "node finished") || !strings.Contains(out, "level=INFO") {
		t.Errorf("expected INFO 'node finished' log line, got: %s", out)
	}
}

func TestNodeFinishedLogsErrorOnFailure(t *testing.T) {
	var buf bytes.Buffer
	obs := newTestObserver(&buf, slog.LevelInfo)

	obs.NodeFinished(graph.NodeInfo{ID: 2, Name: "commit", Kind: graph.EffectNode}, errors.New("boom"))

	out := buf.String()
	if !strings.Contains(out, "level=ERROR") || !strings.Contains(out, "boom") {
		t.Errorf("expected ERROR log line containing 'boom', got: %s", out)
	}
}

func TestDecisionMadeLogsWarnOnDeny(t *testing.T) {
	var buf bytes.Buffer
	obs := newTestObserver(&buf, slog.LevelInfo)

	obs.DecisionMade("region-lock", 2, "denied")

	out := buf.String()
	if !strings.Contains(out, "level=WARN") {
		t.Errorf("expected WARN log line for deny verdict, got: %s", out)
	}
}

func TestExecutionFinishedAndSourceFetched(t *testing.T) {
	var buf bytes.Buffer
	obs := newTestObserver(&buf, slog.LevelInfo)

	obs.ExecutionFinished("checkout", 12.3, nil)
	obs.EffectCommitted("send-email", errors.New("smtp down"))
	obs.SourceFetched(observer.SourceMetrics{
		SourceName: "users-db",
		SourceKind: "database",
		CacheHit:   true,
	})

	out := buf.String()
	if !strings.Contains(out, "execution finished") {
		t.Errorf("expected 'execution finished' line, got: %s", out)
	}
	if !strings.Contains(out, "effect committed") || !strings.Contains(out, "level=ERROR") {
		t.Errorf("expected ERROR 'effect committed' line, got: %s", out)
	}
	if !strings.Contains(out, "source fetched") || !strings.Contains(out, "cache_hit=true") {
		t.Errorf("expected 'source fetched' line with cache_hit=true, got: %s", out)
	}
}
