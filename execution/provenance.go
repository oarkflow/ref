package execution

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"sync"
	"time"

	"github.com/oarkflow/ref/fact"
	"github.com/oarkflow/ref/graph"
)

type NodeTrace struct {
	ID        graph.NodeID
	Name      string
	StartedAt time.Time
	EndedAt   time.Time
	Error     string
}

type FactTrace struct {
	Slot     fact.PlanSlot
	Producer uint32
	Sequence uint64
	Value    any
}

type DecisionTrace struct {
	NodeID      graph.NodeID
	Verdict     Verdict
	Reasons     []Reason
	Constraints ConstraintSet
	Obligations []Obligation
}

type EffectTrace struct {
	NodeID graph.NodeID
	Name   string
	Kind   string
}

type Trace struct {
	mu sync.Mutex

	InvocationID string
	Intent       string
	PlanVersion  int
	StartedAt    time.Time
	FinishedAt   time.Time

	Nodes     []NodeTrace
	Facts     []FactTrace
	Decisions []DecisionTrace
	Effects   []EffectTrace

	OutcomeState ExecutionState
	OutcomeValue any
	Error        string
}

func NewTrace(invocationID, intent string, version int) *Trace {
	return &Trace{InvocationID: invocationID, Intent: intent, PlanVersion: version, StartedAt: time.Now()}
}

func (t *Trace) NodeStarted(id graph.NodeID, name string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.Nodes = append(t.Nodes, NodeTrace{ID: id, Name: name, StartedAt: time.Now()})
	t.mu.Unlock()
}

func (t *Trace) NodeFinished(id graph.NodeID, err error) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for i := len(t.Nodes) - 1; i >= 0; i-- {
		if t.Nodes[i].ID != id || !t.Nodes[i].EndedAt.IsZero() {
			continue
		}
		t.Nodes[i].EndedAt = time.Now()
		if err != nil {
			t.Nodes[i].Error = err.Error()
		}
		return
	}
}

func (t *Trace) RecordDecision(id graph.NodeID, ds *DecisionSet) {
	if t == nil || ds == nil {
		return
	}
	t.mu.Lock()
	t.Decisions = append(t.Decisions, DecisionTrace{
		NodeID: id, Verdict: ds.Verdict(), Reasons: ds.Reasons(), Constraints: ds.Constraints(), Obligations: ds.Obligations(),
	})
	t.mu.Unlock()
}

func (t *Trace) RecordEffect(id graph.NodeID, value any) {
	if t == nil || value == nil {
		return
	}
	var name, kind string
	if e, ok := value.(interface{ Name() string }); ok {
		name = e.Name()
	}
	if k, ok := value.(interface{ Kind() fmt.Stringer }); ok {
		kind = k.Kind().String()
	}
	if name == "" {
		name = "effect"
	}
	t.mu.Lock()
	t.Effects = append(t.Effects, EffectTrace{NodeID: id, Name: name, Kind: kind})
	t.mu.Unlock()
}

func (t *Trace) SetFacts(store *fact.Store) {
	if t == nil || store == nil || !store.ProvenanceEnabled() {
		return
	}
	items := store.ProvenanceSnapshot()
	t.mu.Lock()
	t.Facts = make([]FactTrace, 0, len(items))
	for _, item := range items {
		t.Facts = append(t.Facts, FactTrace{Slot: item.Slot, Producer: item.Producer, Sequence: item.Sequence, Value: item.Value})
	}
	t.mu.Unlock()
}

func (t *Trace) Finalize(state ExecutionState, value any, err error) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.FinishedAt = time.Now()
	t.OutcomeState = state
	t.OutcomeValue = value
	if err != nil {
		t.Error = err.Error()
	}
	t.mu.Unlock()
}

func (t *Trace) Snapshot() *Trace {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	copyTrace := &Trace{
		InvocationID: t.InvocationID, Intent: t.Intent, PlanVersion: t.PlanVersion,
		StartedAt: t.StartedAt, FinishedAt: t.FinishedAt, OutcomeState: t.OutcomeState,
		OutcomeValue: t.OutcomeValue, Error: t.Error,
		Nodes:     append([]NodeTrace(nil), t.Nodes...),
		Facts:     append([]FactTrace(nil), t.Facts...),
		Decisions: make([]DecisionTrace, len(t.Decisions)),
		Effects:   append([]EffectTrace(nil), t.Effects...),
	}
	for i, decision := range t.Decisions {
		copyTrace.Decisions[i] = decision
		copyTrace.Decisions[i].Reasons = append([]Reason(nil), decision.Reasons...)
		copyTrace.Decisions[i].Constraints.Regions = append([]string(nil), decision.Constraints.Regions...)
		copyTrace.Decisions[i].Constraints.Fields = append([]string(nil), decision.Constraints.Fields...)
		copyTrace.Decisions[i].Constraints.Filters = append([]Constraint(nil), decision.Constraints.Filters...)
		copyTrace.Decisions[i].Obligations = append([]Obligation(nil), decision.Obligations...)
	}
	return copyTrace
}

func CompareTraces(expected, actual *Trace) error {
	if expected == nil || actual == nil {
		if expected == actual {
			return nil
		}
		return fmt.Errorf("ref: replay trace is missing")
	}
	left, right := expected.Snapshot(), actual.Snapshot()
	sort.SliceStable(left.Nodes, func(i, j int) bool { return left.Nodes[i].ID < left.Nodes[j].ID })
	sort.SliceStable(right.Nodes, func(i, j int) bool { return right.Nodes[i].ID < right.Nodes[j].ID })
	sort.SliceStable(left.Facts, func(i, j int) bool { return left.Facts[i].Slot < left.Facts[j].Slot })
	sort.SliceStable(right.Facts, func(i, j int) bool { return right.Facts[i].Slot < right.Facts[j].Slot })
	sort.SliceStable(left.Decisions, func(i, j int) bool { return left.Decisions[i].NodeID < left.Decisions[j].NodeID })
	sort.SliceStable(right.Decisions, func(i, j int) bool { return right.Decisions[i].NodeID < right.Decisions[j].NodeID })
	sort.SliceStable(left.Effects, func(i, j int) bool {
		if left.Effects[i].NodeID == left.Effects[j].NodeID {
			return left.Effects[i].Name < left.Effects[j].Name
		}
		return left.Effects[i].NodeID < left.Effects[j].NodeID
	})
	sort.SliceStable(right.Effects, func(i, j int) bool {
		if right.Effects[i].NodeID == right.Effects[j].NodeID {
			return right.Effects[i].Name < right.Effects[j].Name
		}
		return right.Effects[i].NodeID < right.Effects[j].NodeID
	})
	if left.InvocationID != right.InvocationID || left.Intent != right.Intent || left.PlanVersion != right.PlanVersion {
		return fmt.Errorf("ref: replay identity or plan version mismatch")
	}
	if len(left.Nodes) != len(right.Nodes) {
		return fmt.Errorf("ref: replay node count mismatch: %d != %d", len(left.Nodes), len(right.Nodes))
	}
	for i := range left.Nodes {
		if left.Nodes[i].ID != right.Nodes[i].ID || left.Nodes[i].Name != right.Nodes[i].Name {
			return fmt.Errorf("ref: replay node %d mismatch", i)
		}
	}
	if len(left.Facts) != len(right.Facts) {
		return fmt.Errorf("ref: replay fact count mismatch: %d != %d", len(left.Facts), len(right.Facts))
	}
	for i := range left.Facts {
		if left.Facts[i].Slot != right.Facts[i].Slot || !sameTraceValue(left.Facts[i].Value, right.Facts[i].Value) {
			return fmt.Errorf("ref: replay fact slot %d mismatch", left.Facts[i].Slot)
		}
	}
	if len(left.Decisions) != len(right.Decisions) {
		return fmt.Errorf("ref: replay decision count mismatch")
	}
	for i := range left.Decisions {
		if left.Decisions[i].NodeID != right.Decisions[i].NodeID || left.Decisions[i].Verdict != right.Decisions[i].Verdict || len(left.Decisions[i].Reasons) != len(right.Decisions[i].Reasons) {
			return fmt.Errorf("ref: replay decision %d mismatch", i)
		}
		for j := range left.Decisions[i].Reasons {
			if left.Decisions[i].Reasons[j] != right.Decisions[i].Reasons[j] {
				return fmt.Errorf("ref: replay decision reason mismatch")
			}
		}
	}
	if len(left.Effects) != len(right.Effects) {
		return fmt.Errorf("ref: replay effect count mismatch")
	}
	for i := range left.Effects {
		if left.Effects[i] != right.Effects[i] {
			return fmt.Errorf("ref: replay effect %d mismatch", i)
		}
	}
	return nil
}

func sameTraceValue(left, right any) bool {
	if reflect.DeepEqual(left, right) {
		return true
	}
	return fmt.Sprint(left) == fmt.Sprint(right)
}

func (t *Trace) Digest() (string, error) {
	if t == nil {
		return "", nil
	}
	canonical := t.Snapshot()
	canonical.StartedAt = time.Time{}
	canonical.FinishedAt = time.Time{}
	for i := range canonical.Nodes {
		canonical.Nodes[i].StartedAt = time.Time{}
		canonical.Nodes[i].EndedAt = time.Time{}
	}
	sort.SliceStable(canonical.Nodes, func(i, j int) bool { return canonical.Nodes[i].ID < canonical.Nodes[j].ID })
	for i := range canonical.Facts {
		canonical.Facts[i].Sequence = 0
	}
	sort.SliceStable(canonical.Facts, func(i, j int) bool { return canonical.Facts[i].Slot < canonical.Facts[j].Slot })
	sort.SliceStable(canonical.Decisions, func(i, j int) bool { return canonical.Decisions[i].NodeID < canonical.Decisions[j].NodeID })
	sort.SliceStable(canonical.Effects, func(i, j int) bool {
		if canonical.Effects[i].NodeID == canonical.Effects[j].NodeID {
			return canonical.Effects[i].Name < canonical.Effects[j].Name
		}
		return canonical.Effects[i].NodeID < canonical.Effects[j].NodeID
	})
	encoded, err := json.Marshal(canonical)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}
