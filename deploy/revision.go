// Package deploy manages an application's configuration lifecycle: every
// change to the BCL document is a revision that is validated, diffed against
// what is live, approved by someone other than its author, activated, and —
// if needed — rolled back. A Supervisor serves the active revision and
// swaps generations without dropping connections; a revision that fails to
// build never replaces the one being served.
package deploy

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/oarkflow/ref/platform"
)

// Revision statuses.
const (
	StatusPending    = "pending"    // awaiting approval
	StatusApproved   = "approved"   // may be activated
	StatusRejected   = "rejected"   // closed by a reviewer
	StatusActive     = "active"     // what the supervisor serves
	StatusSuperseded = "superseded" // was active; a rollback target
	StatusFailed     = "failed"     // activated but did not build
)

// Revision is one version of an application's document.
type Revision struct {
	ID        string    `json:"id"`
	App       string    `json:"app"`
	Seq       int64     `json:"seq"`
	Source    string    `json:"source"`
	Checksum  string    `json:"checksum"`
	Signature string    `json:"signature,omitempty"`
	Author    string    `json:"author"`
	Message   string    `json:"message,omitempty"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
	// BaseID is the revision that was active when this one was proposed;
	// Changes is the block-level diff against it.
	BaseID    string                    `json:"base_id,omitempty"`
	Changes   []platform.DocumentChange `json:"changes,omitempty"`
	Warnings  []string                  `json:"warnings,omitempty"`
	Approvals []Decision                `json:"approvals,omitempty"`
	Rejection *Decision                 `json:"rejection,omitempty"`
	// ActivatedAt/By record the latest activation (a rollback re-activates).
	ActivatedAt *time.Time `json:"activated_at,omitempty"`
	ActivatedBy string     `json:"activated_by,omitempty"`
	Failure     string     `json:"failure,omitempty"`
	History     []Event    `json:"history,omitempty"`
}

// Decision is a reviewer's approval or rejection.
type Decision struct {
	By      string    `json:"by"`
	At      time.Time `json:"at"`
	Comment string    `json:"comment,omitempty"`
}

// Event is one step of a revision's life.
type Event struct {
	At     time.Time `json:"at"`
	By     string    `json:"by"`
	Action string    `json:"action"`
	Note   string    `json:"note,omitempty"`
}

// Errors.
var (
	ErrNotFound  = errors.New("deploy: not found")
	ErrState     = errors.New("deploy: not allowed in the revision's current state")
	ErrForbidden = errors.New("deploy: not permitted")
	ErrInvalid   = errors.New("deploy: the document is invalid")
	ErrTampered  = errors.New("deploy: the revision's source does not match its checksum or signature")
)

// InvalidError carries the validation report of a rejected proposal.
type InvalidError struct{ Report platform.ValidationReport }

func (e *InvalidError) Error() string {
	return fmt.Sprintf("%v: %s", ErrInvalid, strings.Join(e.Report.Errors, "; "))
}

func (e *InvalidError) Unwrap() error { return ErrInvalid }

// Store persists revisions.
type Store interface {
	Create(ctx context.Context, r *Revision) error
	Get(ctx context.Context, id string) (*Revision, error)
	Update(ctx context.Context, r *Revision) error
	// List returns an app's revisions, newest first.
	List(ctx context.Context, app string, limit int) ([]*Revision, error)
	NextSeq(ctx context.Context, app string) (int64, error)
}

// Manager applies the revision workflow.
type Manager struct {
	Store Store
	App   string
	// Validate checks a document statically (platform.Validate with the
	// deployment's load options).
	Validate func(ctx context.Context, src []byte) platform.ValidationReport
	// Secret signs revisions (HMAC-SHA256), so a source edited in the store
	// cannot be activated.
	Secret []byte
	// Approvals is how many distinct reviewers must approve (default 1; 0
	// activates without review, for development).
	Approvals int
	// AllowSelfApproval lets an author approve their own revision.
	AllowSelfApproval bool
	Now               func() time.Time

	mu sync.Mutex // serialises state changes within a process
}

func (m *Manager) now() time.Time {
	if m.Now != nil {
		return m.Now().UTC()
	}
	return time.Now().UTC()
}

func newID() string {
	var b [10]byte
	_, _ = rand.Read(b[:])
	return "rev_" + hex.EncodeToString(b[:])
}

func (m *Manager) sign(r *Revision) string {
	if len(m.Secret) == 0 {
		return ""
	}
	mac := hmac.New(sha256.New, m.Secret)
	fmt.Fprintf(mac, "%s|%d|%s", r.App, r.Seq, r.Checksum)
	return hex.EncodeToString(mac.Sum(nil))
}

// Verify checks that a revision's source matches its checksum and signature.
func (m *Manager) Verify(r *Revision) error {
	sum := sha256.Sum256([]byte(r.Source))
	if hex.EncodeToString(sum[:]) != r.Checksum {
		return ErrTampered
	}
	if len(m.Secret) > 0 && subtle.ConstantTimeCompare([]byte(m.sign(r)), []byte(r.Signature)) != 1 {
		return ErrTampered
	}
	return nil
}

// Active returns the app's active revision (nil when there is none).
func (m *Manager) Active(ctx context.Context) (*Revision, error) {
	list, err := m.Store.List(ctx, m.App, 0)
	if err != nil {
		return nil, err
	}
	for _, r := range list {
		if r.Status == StatusActive {
			return r, nil
		}
	}
	return nil, nil
}

// Propose validates a document and records it as a pending revision (or an
// approved one when no approvals are required).
func (m *Manager) Propose(ctx context.Context, src []byte, author, message string) (*Revision, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if strings.TrimSpace(author) == "" {
		return nil, fmt.Errorf("%w: an author is required", ErrForbidden)
	}
	report := m.Validate(ctx, src)
	if !report.Valid {
		return nil, &InvalidError{Report: report}
	}
	seq, err := m.Store.NextSeq(ctx, m.App)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(src)
	now := m.now()
	r := &Revision{ID: newID(), App: m.App, Seq: seq, Source: string(src), Checksum: hex.EncodeToString(sum[:]),
		Author: author, Message: message, Status: StatusPending, CreatedAt: now, Warnings: report.Warnings}
	r.Signature = m.sign(r)
	active, err := m.Active(ctx)
	if err != nil {
		return nil, err
	}
	var base *platform.Document
	if active != nil {
		r.BaseID = active.ID
		if prev := m.Validate(ctx, []byte(active.Source)); prev.Document != nil {
			base = prev.Document
		}
	}
	r.Changes = platform.DiffDocuments(base, report.Document)
	r.History = append(r.History, Event{At: now, By: author, Action: "proposed", Note: message})
	if m.Approvals <= 0 {
		r.Status = StatusApproved
		r.History = append(r.History, Event{At: now, By: "system", Action: "approved", Note: "no review required"})
	}
	if err := m.Store.Create(ctx, r); err != nil {
		return nil, err
	}
	return r, nil
}

func (m *Manager) load(ctx context.Context, id string) (*Revision, error) {
	r, err := m.Store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if r.App != m.App {
		return nil, fmt.Errorf("%w: revision %q", ErrNotFound, id)
	}
	return r, nil
}

// Approve records a reviewer's approval; enough distinct approvals make the
// revision approved.
func (m *Manager) Approve(ctx context.Context, id, by, comment string) (*Revision, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, err := m.load(ctx, id)
	if err != nil {
		return nil, err
	}
	if r.Status != StatusPending {
		return nil, fmt.Errorf("%w: revision %d is %s", ErrState, r.Seq, r.Status)
	}
	if by == r.Author && !m.AllowSelfApproval {
		return nil, fmt.Errorf("%w: the author cannot approve their own revision", ErrForbidden)
	}
	if slices.ContainsFunc(r.Approvals, func(d Decision) bool { return d.By == by }) {
		return nil, fmt.Errorf("%w: %s already approved revision %d", ErrState, by, r.Seq)
	}
	now := m.now()
	r.Approvals = append(r.Approvals, Decision{By: by, At: now, Comment: comment})
	r.History = append(r.History, Event{At: now, By: by, Action: "approved", Note: comment})
	if len(r.Approvals) >= max(1, m.Approvals) {
		r.Status = StatusApproved
	}
	return r, m.Store.Update(ctx, r)
}

// Reject closes a pending or approved revision.
func (m *Manager) Reject(ctx context.Context, id, by, comment string) (*Revision, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, err := m.load(ctx, id)
	if err != nil {
		return nil, err
	}
	if r.Status != StatusPending && r.Status != StatusApproved {
		return nil, fmt.Errorf("%w: revision %d is %s", ErrState, r.Seq, r.Status)
	}
	if strings.TrimSpace(comment) == "" {
		return nil, fmt.Errorf("%w: say why the revision is rejected", ErrForbidden)
	}
	now := m.now()
	r.Status = StatusRejected
	r.Rejection = &Decision{By: by, At: now, Comment: comment}
	r.History = append(r.History, Event{At: now, By: by, Action: "rejected", Note: comment})
	return r, m.Store.Update(ctx, r)
}

// Activate makes an approved revision the active one (a supervisor then
// builds and serves it). The previously active revision is superseded.
func (m *Manager) Activate(ctx context.Context, id, by string) (*Revision, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, err := m.load(ctx, id)
	if err != nil {
		return nil, err
	}
	if r.Status != StatusApproved {
		return nil, fmt.Errorf("%w: only an approved revision can be activated (revision %d is %s)", ErrState, r.Seq, r.Status)
	}
	return m.activate(ctx, r, by, "activated")
}

// Rollback re-activates a previously active revision: the given one, or the
// most recently superseded when id is empty.
func (m *Manager) Rollback(ctx context.Context, id, by, reason string) (*Revision, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var target *Revision
	if id != "" {
		r, err := m.load(ctx, id)
		if err != nil {
			return nil, err
		}
		target = r
	} else {
		list, err := m.Store.List(ctx, m.App, 0)
		if err != nil {
			return nil, err
		}
		for _, r := range list {
			if r.Status == StatusSuperseded && (target == nil || r.ActivatedAt.After(*target.ActivatedAt)) {
				target = r
			}
		}
		if target == nil {
			return nil, fmt.Errorf("%w: there is no earlier revision to roll back to", ErrState)
		}
	}
	if target.Status != StatusSuperseded {
		return nil, fmt.Errorf("%w: only a previously active revision can be rolled back to (revision %d is %s)", ErrState, target.Seq, target.Status)
	}
	return m.activate(ctx, target, by, "rolled back to: "+reason)
}

func (m *Manager) activate(ctx context.Context, r *Revision, by, note string) (*Revision, error) {
	if err := m.Verify(r); err != nil {
		return nil, err
	}
	now := m.now()
	current, err := m.Active(ctx)
	if err != nil {
		return nil, err
	}
	if current != nil && current.ID != r.ID {
		current.Status = StatusSuperseded
		current.History = append(current.History, Event{At: now, By: by, Action: "superseded", Note: fmt.Sprintf("by revision %d", r.Seq)})
		if err := m.Store.Update(ctx, current); err != nil {
			return nil, err
		}
	}
	r.Status, r.ActivatedAt, r.ActivatedBy, r.Failure = StatusActive, &now, by, ""
	r.History = append(r.History, Event{At: now, By: by, Action: "activated", Note: note})
	return r, m.Store.Update(ctx, r)
}

// MarkFailed records that the active revision could not be built and
// re-activates the revision still being served (restoreID), so the store
// keeps describing what is actually live.
func (m *Manager) MarkFailed(ctx context.Context, id, restoreID, reason string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, err := m.load(ctx, id)
	if err != nil {
		return err
	}
	now := m.now()
	r.Status, r.Failure = StatusFailed, reason
	r.History = append(r.History, Event{At: now, By: "supervisor", Action: "failed", Note: reason})
	if err := m.Store.Update(ctx, r); err != nil {
		return err
	}
	if restoreID == "" {
		return nil
	}
	prev, err := m.load(ctx, restoreID)
	if err != nil {
		return err
	}
	prev.Status = StatusActive
	prev.History = append(prev.History, Event{At: now, By: "supervisor", Action: "restored", Note: fmt.Sprintf("revision %d failed to build", r.Seq)})
	return m.Store.Update(ctx, prev)
}
