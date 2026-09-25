package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Case statuses.
const (
	CaseDraft      = "draft"
	CaseInProgress = "in_progress"
	CaseReturned   = "returned"
	CaseApproved   = "approved"
	CaseRejected   = "rejected"
	CaseWithdrawn  = "withdrawn"
	CaseCompleted  = "completed"
)

// Stage statuses.
const (
	StagePending   = "pending"
	StageActive    = "active"
	StageReturned  = "returned" // reopened for correction
	StageCompleted = "completed"
	StageRejected  = "rejected"
	StageSkipped   = "skipped"
)

// Node statuses.
const (
	NodePending    = "pending"
	NodeInProgress = "in_progress"
	NodePassed     = "passed"
	NodeFailed     = "failed"
	NodeWaived     = "waived"
	NodeSkipped    = "skipped"
)

// Verdicts a reviewer records on an input.
const (
	VerdictVerified = "verified"
	VerdictFlagged  = "flagged"
)

// Case is one run of a pipeline.
type Case struct {
	ID       string `json:"id"`
	Number   string `json:"number"`
	Pipeline string `json:"pipeline"`
	// Revision increases on every change; stores use it for optimistic
	// concurrency so two officers cannot overwrite each other.
	Revision  int64  `json:"revision"`
	TenantID  string `json:"tenant_id,omitempty"`
	OrgUnit   string `json:"org_unit,omitempty"`
	Status    string `json:"status"`
	Stage     string `json:"stage"`
	CreatedBy string `json:"created_by,omitempty"`
	// AccessKeyHash lets an anonymous applicant come back to a public case:
	// the host hands out a random key at start and stores only its SHA-256.
	AccessKeyHash string `json:"access_key_hash,omitempty"`

	Data         map[string]any         `json:"data"`
	Stages       map[string]*StageState `json:"stages"`
	History      []Entry                `json:"history,omitempty"`
	Certificates []Certificate          `json:"certificates,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// StageState is one stage's progress.
type StageState struct {
	Status      string                `json:"status"`
	EnteredAt   *time.Time            `json:"entered_at,omitempty"`
	CompletedAt *time.Time            `json:"completed_at,omitempty"`
	DueAt       *time.Time            `json:"due_at,omitempty"`
	CompletedBy string                `json:"completed_by,omitempty"`
	Nodes       map[string]*NodeState `json:"nodes,omitempty"`
	// Flags are inputs a reviewer sent back for correction, keyed by
	// "form.input". While a stage is returned only these are editable.
	Flags map[string]Flag `json:"flags,omitempty"`
	// ReturnedFrom is the stage that sent the case back here; resubmitting
	// goes straight back to it.
	ReturnedFrom string `json:"returned_from,omitempty"`
	Visits       int    `json:"visits,omitempty"`
}

// Flag is one input returned for correction.
type Flag struct {
	Path    string    `json:"path"`
	Comment string    `json:"comment,omitempty"`
	By      string    `json:"by,omitempty"`
	At      time.Time `json:"at"`
}

// NodeState is one node's progress.
type NodeState struct {
	Status    string             `json:"status"`
	Actors    []string           `json:"actors,omitempty"`
	Approvals []Approval         `json:"approvals,omitempty"`
	Verdicts  map[string]Verdict `json:"verdicts,omitempty"`
	Result    map[string]any     `json:"result,omitempty"`
	Comment   string             `json:"comment,omitempty"`
	UpdatedAt *time.Time         `json:"updated_at,omitempty"`
}

// Approval is one approver's decision.
type Approval struct {
	By      string    `json:"by"`
	At      time.Time `json:"at"`
	Comment string    `json:"comment,omitempty"`
}

// Verdict is a reviewer's decision on one input.
type Verdict struct {
	Status  string    `json:"status"`
	Comment string    `json:"comment,omitempty"`
	By      string    `json:"by,omitempty"`
	At      time.Time `json:"at"`
}

// Entry is one audit-trail record.
type Entry struct {
	At      time.Time `json:"at"`
	Actor   string    `json:"actor,omitempty"`
	Stage   string    `json:"stage,omitempty"`
	Node    string    `json:"node,omitempty"`
	Action  string    `json:"action"`
	Comment string    `json:"comment,omitempty"`
	Changes []string  `json:"changes,omitempty"`
	From    string    `json:"from,omitempty"`
	To      string    `json:"to,omitempty"`
}

// Actor is whoever is acting on a case.
type Actor struct {
	ID     string         `json:"id"`
	Roles  []string       `json:"roles,omitempty"`
	Claims map[string]any `json:"claims,omitempty"`
}

// HasAnyRole reports whether the actor holds one of roles. An empty list means
// "no role restriction" and returns false: callers decide what that means.
func (a Actor) HasAnyRole(roles []string) bool {
	for _, r := range roles {
		if slices.Contains(a.Roles, r) {
			return true
		}
	}
	return false
}

// Clone deep-copies a case, so an engine operation never mutates the caller's
// value and a failed operation leaves nothing half-applied.
func (c *Case) Clone() *Case {
	raw, err := json.Marshal(c)
	if err != nil {
		panic(fmt.Sprintf("pipeline: clone case: %v", err))
	}
	var out Case
	if err := json.Unmarshal(raw, &out); err != nil {
		panic(fmt.Sprintf("pipeline: clone case: %v", err))
	}
	if out.Data == nil {
		out.Data = map[string]any{}
	}
	if out.Stages == nil {
		out.Stages = map[string]*StageState{}
	}
	return &out
}

// Terminal reports whether the case can no longer change.
func (c *Case) Terminal() bool {
	switch c.Status {
	case CaseApproved, CaseRejected, CaseWithdrawn, CaseCompleted:
		return true
	}
	return false
}

// Get reads a dotted data path ("applicant.full_name").
func (c *Case) Get(path string) (any, bool) {
	var cur any = c.Data
	for _, seg := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = m[seg]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

// Set writes a dotted data path, creating intermediate objects.
func (c *Case) Set(path string, value any) {
	if c.Data == nil {
		c.Data = map[string]any{}
	}
	segs := strings.Split(path, ".")
	cur := c.Data
	for _, seg := range segs[:len(segs)-1] {
		next, ok := cur[seg].(map[string]any)
		if !ok {
			next = map[string]any{}
			cur[seg] = next
		}
		cur = next
	}
	cur[segs[len(segs)-1]] = value
}

// Evaluator evaluates the definition's expressions. The host supplies it (REF
// uses its own expression language), which keeps this package language-free.
type Evaluator interface {
	Eval(expr string, env map[string]any) (any, error)
}

// Automation runs an automated node's hook and reports whether it passed.
type Automation interface {
	RunNode(ctx context.Context, hook string, c *Case, stage, node string) (result map[string]any, passed bool, err error)
}

// Errors reported by the engine. Use errors.Is / errors.As.
var (
	ErrForbidden = errors.New("pipeline: not permitted")
	ErrState     = errors.New("pipeline: not allowed in the case's current state")
	ErrNotFound  = errors.New("pipeline: not found")
	ErrConflict  = errors.New("pipeline: the case was changed by someone else; reload and retry")
)

// FieldError is one invalid input.
type FieldError struct {
	Path    string `json:"path"`
	Rule    string `json:"rule"`
	Message string `json:"message"`
}

// ValidationError lists every invalid input at once.
type ValidationError struct {
	Message string       `json:"message"`
	Fields  []FieldError `json:"fields"`
}

func (e *ValidationError) Error() string {
	if len(e.Fields) == 0 {
		return e.Message
	}
	return fmt.Sprintf("%s: %s (and %d more)", e.Message, e.Fields[0].Message, len(e.Fields)-1)
}

func forbidden(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrForbidden, fmt.Sprintf(format, args...))
}

func badState(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrState, fmt.Sprintf(format, args...))
}
