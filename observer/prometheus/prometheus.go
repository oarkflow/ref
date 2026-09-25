// Package promobserver implements observer.Observer using Prometheus client
// metrics. It is a separate, opt-in module so the core execution/observer
// packages stay free of the prometheus dependency; import this package only
// if you want Prometheus metrics wired into the kernel.
//
// Usage:
//
//	reg := prometheus.NewRegistry() // or nil to use prometheus.DefaultRegisterer
//	obs, err := promobserver.New(reg)
//	if err != nil {
//		log.Fatal(err)
//	}
//	sched := execution.NewScheduler(obs)
//
// The observer is safe to register at any tier (Critical, Async, Lossy) via
// observer.TieredObserver, since all metric updates are cheap, lock-free
// counter/histogram operations.
package promobserver

import (
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/oarkflow/ref/graph"
	"github.com/oarkflow/ref/observer"
)

// Observer implements observer.Observer, recording execution kernel
// lifecycle events as Prometheus metrics.
type Observer struct {
	nodeExecutions *prometheus.CounterVec
	nodeDuration   *prometheus.HistogramVec
	decisions      *prometheus.CounterVec
	effects        *prometheus.CounterVec
	executionDur   *prometheus.HistogramVec
	sourceFetch    *prometheus.HistogramVec
	sourceCacheHit *prometheus.CounterVec

	mu      sync.Mutex
	started map[graph.NodeID]nodeStart
}

type nodeStart struct {
	t time.Time
}

var _ observer.Observer = (*Observer)(nil)

// New creates an Observer and registers its metrics against reg. If reg is
// nil, prometheus.DefaultRegisterer is used. Metrics are namespaced under
// "ref_" by default; use NewWithNamespace to customize.
func New(reg prometheus.Registerer) (*Observer, error) {
	return NewWithNamespace(reg, "ref")
}

// NewWithNamespace behaves like New but allows overriding the metric name
// prefix (e.g. "myapp" produces "myapp_node_executions_total").
func NewWithNamespace(reg prometheus.Registerer, namespace string) (*Observer, error) {
	if reg == nil {
		reg = prometheus.DefaultRegisterer
	}

	o := &Observer{
		nodeExecutions: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "node_executions_total",
			Help:      "Total number of node executions by node name, kind, and outcome.",
		}, []string{"name", "kind", "outcome"}),

		nodeDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "node_duration_seconds",
			Help:      "Node execution duration in seconds, by node name and kind.",
			Buckets:   prometheus.DefBuckets,
		}, []string{"name", "kind"}),

		decisions: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "decisions_total",
			Help:      "Total number of policy decisions by policy name and verdict.",
		}, []string{"policy", "verdict"}),

		effects: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "effects_committed_total",
			Help:      "Total number of effects committed by effect name and outcome.",
		}, []string{"name", "outcome"}),

		executionDur: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "execution_duration_seconds",
			Help:      "Total execution duration in seconds, by intent and outcome.",
			Buckets:   prometheus.DefBuckets,
		}, []string{"intent", "outcome"}),

		sourceFetch: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "source_fetch_duration_seconds",
			Help:      "External data source fetch duration in seconds, by source name and kind.",
			Buckets:   prometheus.DefBuckets,
		}, []string{"source", "kind"}),

		sourceCacheHit: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "source_cache_hits_total",
			Help:      "Total number of source fetches served from cache, by source name and kind.",
		}, []string{"source", "kind"}),

		started: make(map[graph.NodeID]nodeStart),
	}

	collectors := []prometheus.Collector{
		o.nodeExecutions, o.nodeDuration, o.decisions,
		o.effects, o.executionDur, o.sourceFetch, o.sourceCacheHit,
	}
	for _, c := range collectors {
		if err := reg.Register(c); err != nil {
			return nil, err
		}
	}

	return o, nil
}

func outcome(err error) string {
	if err != nil {
		return "error"
	}
	return "ok"
}

// NodeStarted records the start time for a node so NodeFinished can compute
// its duration.
func (o *Observer) NodeStarted(info graph.NodeInfo) {
	o.mu.Lock()
	o.started[info.ID] = nodeStart{t: time.Now()}
	o.mu.Unlock()
}

// NodeFinished records the node execution outcome and duration.
func (o *Observer) NodeFinished(info graph.NodeInfo, err error) {
	o.mu.Lock()
	start, ok := o.started[info.ID]
	if ok {
		delete(o.started, info.ID)
	}
	o.mu.Unlock()

	name, kind := info.Name, info.Kind.String()
	o.nodeExecutions.WithLabelValues(name, kind, outcome(err)).Inc()
	if ok {
		o.nodeDuration.WithLabelValues(name, kind).Observe(time.Since(start.t).Seconds())
	}
}

// DecisionMade records a policy decision by policy name and verdict.
func (o *Observer) DecisionMade(policy string, verdict uint8, message string) {
	o.decisions.WithLabelValues(policy, verdictLabel(verdict)).Inc()
}

// EffectCommitted records an effect commit outcome.
func (o *Observer) EffectCommitted(name string, err error) {
	o.effects.WithLabelValues(name, outcome(err)).Inc()
}

// ExecutionFinished records the total execution duration for an intent.
func (o *Observer) ExecutionFinished(intent string, durationMs float64, err error) {
	o.executionDur.WithLabelValues(intent, outcome(err)).Observe(durationMs / 1000.0)
}

// SourceFetched records external source fetch timing and cache-hit signal.
func (o *Observer) SourceFetched(metrics observer.SourceMetrics) {
	o.sourceFetch.WithLabelValues(metrics.SourceName, metrics.SourceKind).Observe(metrics.TotalTime.Seconds())
	if metrics.CacheHit {
		o.sourceCacheHit.WithLabelValues(metrics.SourceName, metrics.SourceKind).Inc()
	}
}

func verdictLabel(v uint8) string {
	switch v {
	case 0:
		return "pending"
	case 1:
		return "allow"
	case 2:
		return "deny"
	default:
		return "unknown"
	}
}
