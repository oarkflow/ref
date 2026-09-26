package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Human-in-the-loop review modes. A stage declares any of them with review
// blocks:
//
//   - diff: the reviewer sees, field by field, what changed since the
//     previous submission (or the last approved version); approving records
//     the revision reviewed.
//   - gate: a hard gate — the case cannot advance until N distinct reviewers
//     (optionally holding given roles) approve. Rejections are recorded.
//   - triage: cases are classified into priority/queue buckets on arrival;
//     work lists are ordered by priority.
//   - sampling: only a deterministic percentage of cases (by a hash of the
//     case id), or cases matching a condition, need human review; the rest
//     pass with an audit record saying they were not sampled.

// Triage is a case's classification by a triage review.
type Triage struct {
	Stage    string    `json:"stage"`
	Bucket   string    `json:"bucket,omitempty"`
	Priority int       `json:"priority"`
	Queue    string    `json:"queue,omitempty"`
	At       time.Time `json:"at"`
}

// Snapshot kinds.
const (
	SnapshotSubmission = "submission"
	SnapshotApproved   = "approved"
)

// Snapshot is a version of a case's data seen by a diff review.
type Snapshot struct {
	Stage string `json:"stage"`
	// Kind: submission (the data a stage was entered with) or approved (the
	// data a reviewer approved).
	Kind     string         `json:"kind"`
	Revision int64          `json:"revision"`
	By       string         `json:"by,omitempty"`
	At       time.Time      `json:"at"`
	Data     map[string]any `json:"data"`
}

// maxSnapshots bounds the snapshots a case keeps; the newest of each stage
// and kind always survives.
const maxSnapshots = 20

// ReviewState is a stage visit's review record.
type ReviewState struct {
	Sampling *SamplingDecision `json:"sampling,omitempty"`
	// ReviewedRevision is the revision a diff review approved.
	ReviewedRevision int64      `json:"reviewed_revision,omitempty"`
	ReviewedBy       string     `json:"reviewed_by,omitempty"`
	ReviewedAt       *time.Time `json:"reviewed_at,omitempty"`
}

// SamplingDecision records why a case was or was not sampled for review.
// Bucket is reproducible: SampleBucket(salt, pipeline, stage, case id).
type SamplingDecision struct {
	Sampled bool      `json:"sampled"`
	Reason  string    `json:"reason"`
	Bucket  int       `json:"bucket"`
	Percent float64   `json:"percent"`
	At      time.Time `json:"at"`
}

// FieldChange is one field that differs between two versions.
type FieldChange struct {
	Path   string `json:"path"`
	Change string `json:"change"` // added, changed or removed
	Before any    `json:"before,omitempty"`
	After  any    `json:"after,omitempty"`
}

// ReviewView is the review state a reviewer sees.
type ReviewView struct {
	Modes            []string          `json:"modes"`
	Diff             *DiffView         `json:"diff,omitempty"`
	Gate             *GateView         `json:"gate,omitempty"`
	Triage           *Triage           `json:"triage,omitempty"`
	Sampling         *SamplingDecision `json:"sampling,omitempty"`
	ReviewedRevision int64             `json:"reviewed_revision,omitempty"`
}

// DiffView is what changed since the baseline.
type DiffView struct {
	// Against: submission or approved.
	Against string `json:"against"`
	// First is set when there is no baseline yet (a first submission):
	// every field is reported as added.
	First        bool          `json:"first,omitempty"`
	BaseRevision int64         `json:"base_revision,omitempty"`
	BaseAt       *time.Time    `json:"base_at,omitempty"`
	Revision     int64         `json:"revision,omitempty"`
	Changes      []FieldChange `json:"changes"`
}

// GateView summarises a review gate.
type GateView struct {
	Node       string     `json:"node"`
	Required   int        `json:"required"`
	Approvals  int        `json:"approvals"`
	Rejections int        `json:"rejections"`
	Open       bool       `json:"open"`
	Decisions  []Approval `json:"decisions,omitempty"`
}

// reviewOf returns the stage's review block of a mode.
func reviewOf(st *Stage, mode string) *Review {
	for i := range st.Reviews {
		if st.Reviews[i].Mode == mode {
			return &st.Reviews[i]
		}
	}
	return nil
}

func gateNodeName(r *Review) string { return orDefault(r.Node, "gate") }

// withGateNodes returns def with a gate node added to every stage whose gate
// review does not declare one itself. The caller's definition is not changed.
func withGateNodes(def *Definition) *Definition {
	var out *Definition
	for i, st := range def.Stages {
		r := reviewOf(&st, ReviewGate)
		if r == nil || slices.ContainsFunc(st.Nodes, func(n Node) bool { return n.Name == gateNodeName(r) }) {
			continue
		}
		if out == nil {
			d := *def
			d.Stages = slices.Clone(def.Stages)
			out = &d
		}
		out.Stages[i].Nodes = append(slices.Clone(st.Nodes), Node{
			Name: gateNodeName(r), Title: "Review gate", Kind: NodeGate, Roles: r.Roles,
			Approvals: max(1, r.Approvals), DistinctFrom: []string{"applicant"},
		})
	}
	if out == nil {
		return def
	}
	return out
}

func (c *Compiled) checkReviews(st Stage) error {
	seen := map[string]bool{}
	for _, r := range st.Reviews {
		if seen[r.Mode] {
			return fmt.Errorf("review %q declared twice", r.Mode)
		}
		seen[r.Mode] = true
		switch r.Mode {
		case ReviewDiff:
			if !oneOf(r.Against, "", SnapshotSubmission, SnapshotApproved) {
				return fmt.Errorf("review diff: against must be submission or approved")
			}
		case ReviewGate:
			if r.Approvals < 0 {
				return fmt.Errorf("review gate: approvals must be at least 1")
			}
			name := gateNodeName(&r)
			if !validName(name) {
				return fmt.Errorf("review gate: invalid node name %q", name)
			}
			for _, n := range st.Nodes {
				if n.Name == name && n.Kind != NodeGate {
					return fmt.Errorf("review gate: node %q is a %s node, not a gate", name, n.Kind)
				}
			}
		case ReviewTriage:
			if len(r.Buckets) == 0 {
				return fmt.Errorf("review triage needs at least one bucket")
			}
			names := map[string]bool{}
			for _, b := range r.Buckets {
				if !validName(b.Name) || names[b.Name] {
					return fmt.Errorf("review triage: bucket %q: missing or duplicate name", b.Name)
				}
				names[b.Name] = true
				if b.Priority < 1 {
					return fmt.Errorf("review triage: bucket %q: priority must be at least 1 (1 is the most urgent)", b.Name)
				}
			}
			if r.DefaultPriority < 0 {
				return fmt.Errorf("review triage: default_priority must be at least 1")
			}
		case ReviewSampling:
			if r.Percent < 0 || r.Percent > 100 {
				return fmt.Errorf("review sampling: percent must be between 0 and 100")
			}
		default:
			return fmt.Errorf("review %q is not diff, gate, triage or sampling", r.Mode)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Entering a reviewed stage
// ---------------------------------------------------------------------------

// enterReviews applies a stage's review modes when it opens: triage
// classifies the case, diff snapshots the submission, sampling decides
// whether a person reviews it at all. It returns the roles the triage bucket
// routes to, and done when the stage passed without review.
func (e *Engine) enterReviews(ctx context.Context, c *Case, st *Stage, ss *StageState, actor Actor, env map[string]any, depth int) ([]string, bool, error) {
	if len(st.Reviews) == 0 {
		return nil, false, nil
	}
	now := e.now()
	var routeRoles []string
	if r := reviewOf(st, ReviewTriage); r != nil {
		t, roles, err := e.triage(c, st, r, env, now)
		if err != nil {
			return nil, false, err
		}
		c.Triage, routeRoles = t, roles
		c.History = append(c.History, Entry{At: now, Actor: actor.ID, Stage: st.Name, Action: "triage",
			To: fmt.Sprintf("%s (priority %d, queue %s)", orDefault(t.Bucket, "default"), t.Priority, orDefault(t.Queue, "-"))})
		c.emit("triaged", st.Name, actor.ID, now, map[string]any{"bucket": t.Bucket, "priority": t.Priority, "queue": t.Queue})
	}
	if reviewOf(st, ReviewDiff) != nil {
		c.addSnapshot(Snapshot{Stage: st.Name, Kind: SnapshotSubmission, Revision: c.Revision + 1, By: actor.ID, At: now, Data: copyData(c.Data)})
	}
	r := reviewOf(st, ReviewSampling)
	if r == nil {
		return routeRoles, false, nil
	}
	d, err := e.sample(c, st, r, env, now)
	if err != nil {
		return nil, false, err
	}
	ss.Review = &ReviewState{Sampling: d}
	detail := map[string]any{"reason": d.Reason, "bucket": d.Bucket, "percent": d.Percent}
	if d.Sampled {
		c.History = append(c.History, Entry{At: now, Actor: "system", Stage: st.Name, Action: "sampled", Comment: d.Reason})
		c.emit("review.sampled", st.Name, "system", now, detail)
		return routeRoles, false, nil
	}
	// Not sampled: the stage passes without review, on the record.
	e.initNodes(c, st, ss, env)
	for _, ns := range ss.Nodes {
		if ns.Status != NodePassed {
			ns.Status = NodeSkipped
		}
	}
	c.History = append(c.History, Entry{At: now, Actor: "system", Stage: st.Name, Action: "not_sampled", Comment: d.Reason})
	c.emit("review.not_sampled", st.Name, "system", now, detail)
	return nil, true, e.completeStage(ctx, c, st.Name, Actor{ID: "system"}, "", depth+1)
}

// triage classifies a case: the first bucket whose condition holds.
func (e *Engine) triage(c *Case, st *Stage, r *Review, env map[string]any, now time.Time) (*Triage, []string, error) {
	lowest := 0
	for _, b := range r.Buckets {
		ok, err := e.cond(b.Condition, env)
		if err != nil {
			return nil, nil, fmt.Errorf("pipeline: stage %q triage bucket %q: %w", st.Name, b.Name, err)
		}
		if ok {
			return &Triage{Stage: st.Name, Bucket: b.Name, Priority: b.Priority, Queue: b.Queue, At: now}, b.Roles, nil
		}
		lowest = max(lowest, b.Priority)
	}
	prio := r.DefaultPriority
	if prio == 0 {
		prio = lowest + 1
	}
	return &Triage{Stage: st.Name, Priority: prio, Queue: r.DefaultQueue, At: now}, nil, nil
}

// SampleBucket is a case's reproducible sampling bucket, 0..9999: a case is
// in a p% sample when its bucket is below p*100.
func SampleBucket(salt, pipeline, stage, caseID string) int {
	sum := sha256.Sum256([]byte(salt + "\x00" + pipeline + "\x00" + stage + "\x00" + caseID))
	return int(binary.BigEndian.Uint64(sum[:8]) % 10000)
}

func (e *Engine) sample(c *Case, st *Stage, r *Review, env map[string]any, now time.Time) (*SamplingDecision, error) {
	d := &SamplingDecision{Bucket: SampleBucket(r.Salt, c.Pipeline, st.Name, c.ID), Percent: r.Percent, At: now}
	for _, expr := range r.AlwaysReviewIf {
		ok, err := e.cond(expr, env)
		if err != nil {
			return nil, fmt.Errorf("pipeline: stage %q always_review_if: %w", st.Name, err)
		}
		if ok {
			d.Sampled, d.Reason = true, "always reviewed: "+expr
			return d, nil
		}
	}
	if r.SampleIf != "" {
		ok, err := e.cond(r.SampleIf, env)
		if err != nil {
			return nil, fmt.Errorf("pipeline: stage %q sample_if: %w", st.Name, err)
		}
		if ok {
			d.Sampled, d.Reason = true, "sampled by condition: "+r.SampleIf
			return d, nil
		}
	}
	threshold := int(r.Percent * 100)
	pct := strconv.FormatFloat(r.Percent, 'f', -1, 64)
	if d.Bucket < threshold {
		d.Sampled, d.Reason = true, fmt.Sprintf("in the %s%% sample (bucket %d < %d)", pct, d.Bucket, threshold)
	} else {
		d.Reason = fmt.Sprintf("not sampled: outside the %s%% sample (bucket %d >= %d)", pct, d.Bucket, threshold)
	}
	return d, nil
}

// recordReviewed marks the revision a diff review approved.
func (e *Engine) recordReviewed(c *Case, st *Stage, ss *StageState, actor Actor, now time.Time) {
	if reviewOf(st, ReviewDiff) == nil || actor.ID == "system" {
		return
	}
	if ss.Review == nil {
		ss.Review = &ReviewState{}
	}
	ss.Review.ReviewedRevision, ss.Review.ReviewedBy, ss.Review.ReviewedAt = c.Revision, actor.ID, &now
	c.addSnapshot(Snapshot{Stage: st.Name, Kind: SnapshotApproved, Revision: c.Revision, By: actor.ID, At: now, Data: copyData(c.Data)})
}

// addSnapshot appends a snapshot, dropping the oldest superseded ones once
// the case holds more than maxSnapshots.
func (c *Case) addSnapshot(s Snapshot) {
	c.Snapshots = append(c.Snapshots, s)
	for len(c.Snapshots) > maxSnapshots {
		drop := -1
		for i, old := range c.Snapshots {
			if slices.ContainsFunc(c.Snapshots[i+1:], func(n Snapshot) bool { return n.Stage == old.Stage && n.Kind == old.Kind }) {
				drop = i
				break
			}
		}
		if drop < 0 {
			return
		}
		c.Snapshots = slices.Delete(c.Snapshots, drop, drop+1)
	}
}

func copyData(m map[string]any) map[string]any {
	raw, err := json.Marshal(m)
	if err != nil {
		return map[string]any{}
	}
	out := map[string]any{}
	_ = json.Unmarshal(raw, &out)
	return out
}

// ---------------------------------------------------------------------------
// Gates
// ---------------------------------------------------------------------------

// tallyGate counts a gate's decisions: it opens once Approvals distinct
// reviewers approved. A rejection is recorded but never opens or closes it;
// the reviewer returns or rejects the case with a stage action.
func tallyGate(n *Node, decisions []Approval) (string, map[string]any) {
	need := max(1, n.Approvals)
	yes, no := 0, 0
	for _, d := range decisions {
		if d.Decision == "approve" {
			yes++
		} else {
			no++
		}
	}
	status := NodeInProgress
	if yes >= need {
		status = NodePassed
	}
	return status, map[string]any{"approve": yes, "reject": no, "required": need}
}

// closedGate reports the first gate of the stage that is not open yet.
func (e *Engine) closedGate(st *Stage, ss *StageState) (string, bool) {
	for _, n := range st.Nodes {
		if n.Kind != NodeGate {
			continue
		}
		ns := ss.Nodes[n.Name]
		if ns == nil || ns.Status == NodeSkipped || ns.Status == NodePassed {
			continue
		}
		yes := 0
		for _, a := range ns.Approvals {
			if a.Decision == "approve" {
				yes++
			}
		}
		return fmt.Sprintf("%q has %d of %d approvals", n.Name, yes, max(1, n.Approvals)), true
	}
	return "", false
}

// ---------------------------------------------------------------------------
// Diff
// ---------------------------------------------------------------------------

// DiffData compares two versions of case data field by field. Repeatable
// forms are compared entry by entry ("addresses[0].line").
func DiffData(before, after map[string]any) []FieldChange {
	a, b := map[string]any{}, map[string]any{}
	flatten("", before, a)
	flatten("", after, b)
	var out []FieldChange
	for path, v := range b {
		old, ok := a[path]
		switch {
		case !ok:
			out = append(out, FieldChange{Path: path, Change: "added", After: v})
		case !sameValue(old, v):
			out = append(out, FieldChange{Path: path, Change: "changed", Before: old, After: v})
		}
	}
	for path, v := range a {
		if _, ok := b[path]; !ok {
			out = append(out, FieldChange{Path: path, Change: "removed", Before: v})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func flatten(prefix string, v any, out map[string]any) {
	switch x := v.(type) {
	case map[string]any:
		for k, item := range x {
			p := k
			if prefix != "" {
				p = prefix + "." + k
			}
			flatten(p, item, out)
		}
	case []any:
		scalars := true
		for _, item := range x {
			switch item.(type) {
			case map[string]any, []any:
				scalars = false
			}
		}
		if scalars { // a multiselect value
			if len(x) > 0 {
				out[prefix] = x
			}
			return
		}
		for i, item := range x {
			flatten(fmt.Sprintf("%s[%d]", prefix, i), item, out)
		}
	default:
		if !isEmpty(v) {
			out[prefix] = v
		}
	}
}

// sameValue compares JSON-decoded values, treating numbers by value.
func sameValue(a, b any) bool {
	if fa, ok := toNumber(a); ok {
		if fb, ok := toNumber(b); ok {
			return fa == fb
		}
	}
	return reflect.DeepEqual(a, b)
}

// diff renders the diff review of a stage visit.
func (e *Engine) diff(c *Case, st *Stage, r *Review, reveal bool) *DiffView {
	against := orDefault(r.Against, SnapshotSubmission)
	dv := &DiffView{Against: against, Changes: []FieldChange{}}
	current := -1
	for i := len(c.Snapshots) - 1; i >= 0; i-- {
		if s := c.Snapshots[i]; s.Stage == st.Name && s.Kind == SnapshotSubmission {
			current = i
			break
		}
	}
	after := c.Data
	if current >= 0 {
		after = c.Snapshots[current].Data
		dv.Revision = c.Snapshots[current].Revision
	}
	var base *Snapshot
	for i := len(c.Snapshots) - 1; i >= 0; i-- {
		s := &c.Snapshots[i]
		if s.Stage != st.Name || s.Kind != against || i == current {
			continue
		}
		if against == SnapshotSubmission && current >= 0 && i > current {
			continue
		}
		base = s
		break
	}
	before := map[string]any{}
	if base == nil {
		dv.First = true
	} else {
		before, dv.BaseRevision = base.Data, base.Revision
		at := base.At
		dv.BaseAt = &at
	}
	sensitive := e.sensitivePaths()
	for _, ch := range DiffData(before, after) {
		if !reveal && sensitive[fieldOf(ch.Path)] {
			if ch.Before != nil {
				ch.Before = mask(fmt.Sprint(ch.Before))
			}
			if ch.After != nil {
				ch.After = mask(fmt.Sprint(ch.After))
			}
		}
		dv.Changes = append(dv.Changes, ch)
	}
	return dv
}

// fieldOf strips entry indexes: "addresses[0].line" -> "addresses.line".
func fieldOf(path string) string {
	for {
		i := strings.IndexByte(path, '[')
		if i < 0 {
			return path
		}
		j := strings.IndexByte(path[i:], ']')
		if j < 0 {
			return path
		}
		path = path[:i] + path[i+j+1:]
	}
}

func (e *Engine) sensitivePaths() map[string]bool {
	out := map[string]bool{}
	for name, cf := range e.C.forms {
		for _, in := range cf.inputs {
			if in.Sensitive {
				out[name+"."+in.Name] = true
			}
		}
	}
	return out
}

// reviewView renders the stage's review state for staff.
func (e *Engine) reviewView(c *Case, st *Stage, ss *StageState, reveal bool) *ReviewView {
	if len(st.Reviews) == 0 {
		return nil
	}
	rv := &ReviewView{}
	for _, r := range st.Reviews {
		rv.Modes = append(rv.Modes, r.Mode)
	}
	if r := reviewOf(st, ReviewDiff); r != nil {
		rv.Diff = e.diff(c, st, r, reveal)
	}
	if r := reviewOf(st, ReviewGate); r != nil {
		name := gateNodeName(r)
		for _, n := range st.Nodes {
			if n.Name != name {
				continue
			}
			gv := &GateView{Node: name, Required: max(1, n.Approvals)}
			if ns := ss.Nodes[name]; ns != nil {
				gv.Decisions = ns.Approvals
				gv.Open = ns.Status == NodePassed
				for _, a := range ns.Approvals {
					if a.Decision == "approve" {
						gv.Approvals++
					} else {
						gv.Rejections++
					}
				}
			}
			rv.Gate = gv
		}
	}
	if c.Triage != nil && c.Triage.Stage == st.Name {
		rv.Triage = c.Triage
	}
	if ss.Review != nil {
		rv.Sampling = ss.Review.Sampling
		rv.ReviewedRevision = ss.Review.ReviewedRevision
	}
	return rv
}

func asList(v any) []any {
	list, _ := v.([]any)
	return list
}
