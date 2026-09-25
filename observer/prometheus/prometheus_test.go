package promobserver

import (
	"errors"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/oarkflow/ref/graph"
	"github.com/oarkflow/ref/observer"
)

func TestObserverRecordsMetrics(t *testing.T) {
	reg := prometheus.NewRegistry()
	obs, err := New(reg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	node := graph.NodeInfo{ID: 1, Name: "validate", Kind: graph.ReadNode}
	obs.NodeStarted(node)
	obs.NodeFinished(node, nil)
	obs.NodeFinished(graph.NodeInfo{ID: 2, Name: "commit", Kind: graph.EffectNode}, errors.New("boom"))

	obs.DecisionMade("tenant-scope", 1, "allowed")
	obs.DecisionMade("region-lock", 2, "denied")

	obs.EffectCommitted("charge-card", nil)
	obs.EffectCommitted("send-email", errors.New("smtp down"))

	obs.ExecutionFinished("checkout", 42.5, nil)

	obs.SourceFetched(observer.SourceMetrics{
		SourceName: "users-db",
		SourceKind: "database",
		TotalTime:  1500000, // 1.5ms in nanoseconds
		CacheHit:   true,
	})

	if got := testutil.CollectAndCount(obs.nodeExecutions); got != 2 {
		t.Errorf("nodeExecutions count = %d, want 2", got)
	}
	if got := testutil.CollectAndCount(obs.nodeDuration); got == 0 {
		t.Errorf("nodeDuration count = %d, want > 0", got)
	}
	if got := testutil.CollectAndCount(obs.decisions); got != 2 {
		t.Errorf("decisions count = %d, want 2", got)
	}
	if got := testutil.CollectAndCount(obs.effects); got != 2 {
		t.Errorf("effects count = %d, want 2", got)
	}
	if got := testutil.CollectAndCount(obs.executionDur); got != 1 {
		t.Errorf("executionDur count = %d, want 1", got)
	}
	if got := testutil.CollectAndCount(obs.sourceFetch); got != 1 {
		t.Errorf("sourceFetch count = %d, want 1", got)
	}
	if got := testutil.CollectAndCount(obs.sourceCacheHit); got != 1 {
		t.Errorf("sourceCacheHit count = %d, want 1", got)
	}

	nodeErrs := testutil.ToFloat64(obs.nodeExecutions.WithLabelValues("commit", graph.EffectNode.String(), "error"))
	if nodeErrs != 1 {
		t.Errorf("commit error count = %v, want 1", nodeErrs)
	}
}

func TestNewDefaultsToDefaultRegisterer(t *testing.T) {
	// Use a dedicated namespace to avoid colliding with other tests that
	// might register against the real DefaultRegisterer in the same run.
	obs, err := NewWithNamespace(nil, "ref_test_default")
	if err != nil {
		t.Fatalf("NewWithNamespace: %v", err)
	}
	if obs == nil {
		t.Fatal("expected non-nil observer")
	}
	prometheus.DefaultRegisterer.Unregister(obs.nodeExecutions)
	prometheus.DefaultRegisterer.Unregister(obs.nodeDuration)
	prometheus.DefaultRegisterer.Unregister(obs.decisions)
	prometheus.DefaultRegisterer.Unregister(obs.effects)
	prometheus.DefaultRegisterer.Unregister(obs.executionDur)
	prometheus.DefaultRegisterer.Unregister(obs.sourceFetch)
	prometheus.DefaultRegisterer.Unregister(obs.sourceCacheHit)
}
