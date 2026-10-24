package preview

import (
	"strings"
	"sync"
	"time"

	"github.com/oarkflow/ref/graph"
	"github.com/oarkflow/ref/observer"
)

// TraceEvent is one step of a running intent in a preview generation: a node
// started, or finished (with how long it took and whether it failed).
type TraceEvent struct {
	Draft  string    `json:"draft"`
	Time   time.Time `json:"time"`
	Phase  string    `json:"phase"`             // "start" | "end" | "done"
	Intent string    `json:"intent"`            // the intent the node belongs to
	Node   string    `json:"node,omitempty"`    // the node's id within the intent (empty for "done")
	Kind   string    `json:"kind,omitempty"`    // graph node kind: pure, read, decision, effect...
	Ms     float64   `json:"ms,omitempty"`      // end/done: wall time
	Error  string    `json:"error,omitempty"`   // end/done: why it failed
	Gen    int64     `json:"version,omitempty"` // draft version the generation was built from
}

// Trace delivers execution events from every preview generation; the func
// cancels the subscription. A host bridges these to Studio's SSE stream.
func (s *Service) Trace() (<-chan TraceEvent, func()) {
	ch := make(chan TraceEvent, 256)
	s.mu.Lock()
	s.traceSeq++
	n := s.traceSeq
	s.traceSubs[n] = ch
	s.mu.Unlock()
	return ch, func() {
		s.mu.Lock()
		if c, ok := s.traceSubs[n]; ok {
			delete(s.traceSubs, n)
			close(c)
		}
		s.mu.Unlock()
	}
}

func (s *Service) publishTrace(ev TraceEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ch := range s.traceSubs {
		select {
		case ch <- ev:
		default: // a slow viewer misses events; it never slows the run
		}
	}
}

// traceObserver adapts the execution kernel's observer to TraceEvents. It runs
// on the scheduler's goroutines, so it only formats and hands off.
type traceObserver struct {
	draft   string
	version int64
	emit    func(TraceEvent)

	mu     sync.Mutex
	starts map[graph.NodeID][]time.Time // FIFO per node: concurrent runs of one node pair up in order
}

func newTraceObserver(draft string, version int64, emit func(TraceEvent)) *traceObserver {
	return &traceObserver{draft: draft, version: version, emit: emit, starts: map[graph.NodeID][]time.Time{}}
}

// splitNode turns a capability name ("platform.<intent>.<node>") into its
// intent and node. Names that are not intent nodes (the authz gate, decode and
// operation steps) keep the whole tail as the node.
func splitNode(name string) (intent, node string) {
	rest, ok := strings.CutPrefix(name, "platform.")
	if !ok {
		return "", name
	}
	i := strings.LastIndex(rest, ".")
	if i < 0 {
		return rest, ""
	}
	return rest[:i], rest[i+1:]
}

func (o *traceObserver) NodeStarted(info graph.NodeInfo) {
	now := time.Now()
	o.mu.Lock()
	o.starts[info.ID] = append(o.starts[info.ID], now)
	o.mu.Unlock()
	intent, node := splitNode(info.Name)
	o.emit(TraceEvent{Draft: o.draft, Time: now, Phase: "start", Intent: intent, Node: node, Kind: info.Kind.String(), Gen: o.version})
}

func (o *traceObserver) NodeFinished(info graph.NodeInfo, err error) {
	now := time.Now()
	var ms float64
	o.mu.Lock()
	if q := o.starts[info.ID]; len(q) > 0 {
		ms = float64(now.Sub(q[0]).Microseconds()) / 1000
		o.starts[info.ID] = q[1:]
	}
	o.mu.Unlock()
	intent, node := splitNode(info.Name)
	ev := TraceEvent{Draft: o.draft, Time: now, Phase: "end", Intent: intent, Node: node, Kind: info.Kind.String(), Ms: ms, Gen: o.version}
	if err != nil {
		ev.Error = err.Error()
	}
	o.emit(ev)
}

func (o *traceObserver) ExecutionFinished(intent string, durationMs float64, err error) {
	ev := TraceEvent{Draft: o.draft, Time: time.Now(), Phase: "done", Intent: intent, Ms: durationMs, Gen: o.version}
	if err != nil {
		ev.Error = err.Error()
	}
	o.emit(ev)
}

func (o *traceObserver) DecisionMade(string, uint8, string)   {}
func (o *traceObserver) EffectCommitted(string, error)        {}
func (o *traceObserver) SourceFetched(observer.SourceMetrics) {}

var _ observer.Observer = (*traceObserver)(nil)
