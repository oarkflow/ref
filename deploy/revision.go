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
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/oarkflow/ref/platform"
	"github.com/oarkflow/ref/signing"
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
	ID        string `json:"id"`
	App       string `json:"app"`
	Seq       int64  `json:"seq"`
	Source    string `json:"source"`
	Checksum  string `json:"checksum"`
	Signature string `json:"signature,omitempty"`
	// KeySignature is an asymmetric (Ed25519 or RSA) signature over
	// SigningPayload, made by the Manager's Signer. Unlike the HMAC, anyone
	// with the public key can check it, and the key that verifies cannot sign.
	KeySignature *signing.Signature `json:"key_signature,omitempty"`
	// ApprovalSignature (HMAC) and ApprovalKeySignature (the Signer's key)
	// cover ApprovalPayload: the revision's identity, the status "approved"
	// and its sorted approvers. They are made when the revision becomes
	// approved, so a status or approvals list edited in the store cannot be
	// activated.
	ApprovalSignature    string             `json:"approval_signature,omitempty"`
	ApprovalKeySignature *signing.Signature `json:"approval_key_signature,omitempty"`
	Author               string             `json:"author"`
	Message              string             `json:"message,omitempty"`
	Status               string             `json:"status"`
	CreatedAt            time.Time          `json:"created_at"`
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
	// Signer additionally signs revisions with an asymmetric key (typically
	// Ed25519; a *signing.KeySet). Verifier checks those signatures; when it
	// is nil and Signer can verify too (a *signing.KeySet can), Signer is
	// used. A verifier holding only public keys lets a host check revisions
	// it could never have signed.
	Signer   Signer
	Verifier Verifier
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

// Signer signs a revision's payload; *signing.KeySet implements it.
type Signer interface {
	Sign(payload []byte) (signing.Signature, error)
}

// Verifier checks a revision's asymmetric signature; *signing.KeySet
// implements it.
type Verifier interface {
	Verify(payload []byte, sig signing.Signature) error
}

// SigningPayload is what a revision's key signature covers: the app, the
// sequence and the source checksum, under a domain prefix so the signature
// cannot be replayed as a signature over anything else.
func SigningPayload(r *Revision) []byte {
	return fmt.Appendf(nil, "ref-revision/v1|%s|%d|%s", r.App, r.Seq, r.Checksum)
}

// ApprovalPayload is what a revision's approval signatures cover: the app,
// the sequence, the source checksum, the status "approved" and the sorted
// list of approvers, under its own domain prefix.
func ApprovalPayload(r *Revision) []byte {
	approvers := make([]string, len(r.Approvals))
	for i, d := range r.Approvals {
		approvers[i] = d.By
	}
	slices.Sort(approvers)
	list, _ := json.Marshal(approvers)
	return fmt.Appendf(nil, "ref-revision-approval/v1|%s|%d|%s|%s|%s", r.App, r.Seq, r.Checksum, StatusApproved, list)
}

func (m *Manager) verifier() Verifier {
	if m.Verifier != nil {
		return m.Verifier
	}
	if v, ok := m.Signer.(Verifier); ok {
		return v
	}
	return nil
}

func (m *Manager) sign(r *Revision) string {
	if len(m.Secret) == 0 {
		return ""
	}
	mac := hmac.New(sha256.New, m.Secret)
	fmt.Fprintf(mac, "%s|%d|%s", r.App, r.Seq, r.Checksum)
	return hex.EncodeToString(mac.Sum(nil))
}

func (m *Manager) signApprovalHMAC(r *Revision) string {
	if len(m.Secret) == 0 {
		return ""
	}
	mac := hmac.New(sha256.New, m.Secret)
	mac.Write(ApprovalPayload(r))
	return hex.EncodeToString(mac.Sum(nil))
}

// signApproval signs a revision's approval state, as it becomes approved.
func (m *Manager) signApproval(r *Revision) error {
	r.ApprovalSignature = m.signApprovalHMAC(r)
	r.ApprovalKeySignature = nil
	if m.Signer != nil {
		sig, err := m.Signer.Sign(ApprovalPayload(r))
		if err != nil {
			return fmt.Errorf("deploy: sign approval: %w", err)
		}
		r.ApprovalKeySignature = &sig
	}
	return nil
}

// VerifyActivation checks what activating or serving a revision needs:
// Verify, a valid approval signature (the key signature or the HMAC, as for
// Verify), and an approvals list that meets the Manager's threshold with
// distinct approvers other than the author (unless AllowSelfApproval).
// A revision approved before approval signatures existed has none and is
// refused: re-propose and re-approve it.
func (m *Manager) VerifyActivation(r *Revision) error {
	if err := m.Verify(r); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, d := range r.Approvals {
		if d.By == "" || seen[d.By] || (d.By == r.Author && !m.AllowSelfApproval) {
			return fmt.Errorf("%w: revision %d has an invalid approvals list", ErrTampered, r.Seq)
		}
		seen[d.By] = true
	}
	if m.Approvals > 0 && len(seen) < m.Approvals {
		return fmt.Errorf("%w: revision %d has %d of %d approvals", ErrTampered, r.Seq, len(seen), m.Approvals)
	}
	verifier := m.verifier()
	if len(m.Secret) == 0 && verifier == nil {
		return nil
	}
	if verifier != nil && r.ApprovalKeySignature != nil && verifier.Verify(ApprovalPayload(r), *r.ApprovalKeySignature) == nil {
		return nil
	}
	if len(m.Secret) > 0 && r.ApprovalSignature != "" &&
		subtle.ConstantTimeCompare([]byte(m.signApprovalHMAC(r)), []byte(r.ApprovalSignature)) == 1 {
		return nil
	}
	return fmt.Errorf("%w: revision %d has no valid approval signature", ErrTampered, r.Seq)
}

// Verify checks that a revision's source matches its checksum and that it
// carries a valid signature: the asymmetric key signature (with a Verifier)
// or the HMAC (with a Secret) — either one is enough, so revisions signed
// before a key was introduced keep verifying. With neither configured only
// the checksum is checked.
func (m *Manager) Verify(r *Revision) error {
	sum := sha256.Sum256([]byte(r.Source))
	if hex.EncodeToString(sum[:]) != r.Checksum {
		return ErrTampered
	}
	verifier := m.verifier()
	if len(m.Secret) == 0 && verifier == nil {
		return nil
	}
	if verifier != nil && r.KeySignature != nil && verifier.Verify(SigningPayload(r), *r.KeySignature) == nil {
		return nil
	}
	if len(m.Secret) > 0 && r.Signature != "" && subtle.ConstantTimeCompare([]byte(m.sign(r)), []byte(r.Signature)) == 1 {
		return nil
	}
	return ErrTampered
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
	if m.Signer != nil {
		sig, err := m.Signer.Sign(SigningPayload(r))
		if err != nil {
			return nil, fmt.Errorf("deploy: sign revision: %w", err)
		}
		r.KeySignature = &sig
	}
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
		if err := m.signApproval(r); err != nil {
			return nil, err
		}
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
		if err := m.signApproval(r); err != nil {
			return nil, err
		}
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
	if err := m.VerifyActivation(r); err != nil {
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
