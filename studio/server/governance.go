package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/oarkflow/ref/deploy"
	"github.com/oarkflow/ref/platform"
)

// ---------------------------------------------------------------------------
// Audit storage
// ---------------------------------------------------------------------------

// AuditStore keeps the audit log beyond the in-memory ring. Entries carry a
// strictly increasing ID, which is the pagination cursor.
type AuditStore interface {
	AppendAudit(ctx context.Context, e AuditEntry) error
	// ListAudit returns up to limit entries newest first; before > 0 keeps
	// only those with a smaller ID.
	ListAudit(ctx context.Context, before int64, limit int) ([]AuditEntry, error)
}

// ---------------------------------------------------------------------------
// Comments
// ---------------------------------------------------------------------------

// Comment is one message in a revision's discussion thread.
type Comment struct {
	ID         string    `json:"id"`
	RevisionID string    `json:"revisionId"`
	Author     string    `json:"author"`
	At         time.Time `json:"at"`
	Body       string    `json:"body"`
	// ReplyTo is the ID of the comment this answers, if any.
	ReplyTo string `json:"replyTo,omitempty"`
}

// CommentStore keeps revision comments.
type CommentStore interface {
	AddComment(ctx context.Context, c Comment) error
	// ListComments returns a revision's comments, oldest first.
	ListComments(ctx context.Context, revisionID string) ([]Comment, error)
}

// MemoryComments is the default CommentStore.
type MemoryComments struct {
	mu   sync.Mutex
	byID map[string][]Comment
}

// NewMemoryComments returns an empty store.
func NewMemoryComments() *MemoryComments { return &MemoryComments{byID: map[string][]Comment{}} }

// AddComment implements CommentStore.
func (m *MemoryComments) AddComment(_ context.Context, c Comment) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.byID[c.RevisionID] = append(m.byID[c.RevisionID], c)
	return nil
}

// ListComments implements CommentStore.
func (m *MemoryComments) ListComments(_ context.Context, revisionID string) ([]Comment, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := append([]Comment{}, m.byID[revisionID]...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out, nil
}

const maxCommentRunes = 8000

func newCommentID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "cmt_" + hex.EncodeToString(b[:])
}

// revisionFor loads the revision named in the path, checking it belongs to
// the configured app.
func (s *Server) revisionFor(c *call, id string) (*deploy.Revision, error) {
	m, err := s.manager()
	if err != nil {
		return nil, err
	}
	rev, err := m.Store.Get(c.r.Context(), id)
	if err != nil {
		return nil, err
	}
	if rev.App != m.App {
		return nil, errf(http.StatusNotFound, "not_found", "no such revision")
	}
	return rev, nil
}

func (s *Server) listComments(c *call) error {
	rev, err := s.revisionFor(c, c.r.PathValue("id"))
	if err != nil {
		return err
	}
	out, err := s.comments.ListComments(c.r.Context(), rev.ID)
	if err != nil {
		return err
	}
	return c.json(http.StatusOK, out)
}

func (s *Server) addComment(c *call) error {
	rev, err := s.revisionFor(c, c.r.PathValue("id"))
	if err != nil {
		return err
	}
	var in struct {
		Body    string `json:"body"`
		ReplyTo string `json:"replyTo"`
	}
	if err := c.decode(s, &in); err != nil {
		return err
	}
	body := strings.TrimSpace(in.Body)
	switch {
	case body == "":
		return errf(http.StatusUnprocessableEntity, "invalid_comment", "a comment needs a body")
	case utf8.RuneCountInString(body) > maxCommentRunes:
		return errf(http.StatusUnprocessableEntity, "invalid_comment", "a comment is limited to %d characters", maxCommentRunes)
	}
	existing, err := s.comments.ListComments(c.r.Context(), rev.ID)
	if err != nil {
		return err
	}
	if in.ReplyTo != "" {
		found := false
		for _, x := range existing {
			if x.ID == in.ReplyTo {
				found = true
				break
			}
		}
		if !found {
			return errf(http.StatusUnprocessableEntity, "invalid_comment", "replyTo names no comment on this revision")
		}
	}
	if len(existing) >= s.cfg.MaxCommentsPerRevision {
		return errf(http.StatusConflict, "too_many_comments", "this revision already has %d comments", len(existing))
	}
	cm := Comment{ID: newCommentID(), RevisionID: rev.ID, Author: c.id.Name, At: s.cfg.Now().UTC(), Body: body, ReplyTo: in.ReplyTo}
	if err := s.comments.AddComment(c.r.Context(), cm); err != nil {
		return err
	}
	s.record(c, "revision.comment", rev.ID, cm.ID)
	return c.json(http.StatusCreated, cm)
}

// ---------------------------------------------------------------------------
// Revision diff
// ---------------------------------------------------------------------------

// diffBundles lists the files that differ between two bundles, with unified
// diffs, sorted by path.
func diffBundles(base, cur platform.Bundle) []fileDiff {
	old := map[string]string{}
	for _, f := range base {
		old[f.Path] = f.Content
	}
	files := []fileDiff{}
	seen := map[string]bool{}
	for _, f := range cur {
		seen[f.Path] = true
		content := f.Content
		prev, had := old[f.Path]
		switch {
		case !had:
			files = append(files, fileDiff{Path: f.Path, Status: "added", Unified: unifiedDiff(f.Path, nil, &content)})
		case prev != content:
			files = append(files, fileDiff{Path: f.Path, Status: "modified", Unified: unifiedDiff(f.Path, &prev, &content)})
		}
	}
	for _, f := range base {
		if !seen[f.Path] {
			content := f.Content
			files = append(files, fileDiff{Path: f.Path, Status: "removed", Unified: unifiedDiff(f.Path, &content, nil)})
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files
}

// revisionDiff compares a revision with another: ?against=<id> | active. With
// no against it compares with the revision it was proposed against, or with
// an empty bundle if it had none.
func (s *Server) revisionDiff(c *call) error {
	rev, err := s.revisionFor(c, c.r.PathValue("id"))
	if err != nil {
		return err
	}
	m, _ := s.manager()
	against := c.r.URL.Query().Get("against")
	var from *deploy.Revision
	switch against {
	case "":
		if rev.BaseID != "" {
			if from, err = s.revisionFor(c, rev.BaseID); err != nil {
				return err
			}
		}
	case "active":
		if from, err = m.Active(c.r.Context()); err != nil {
			return err
		}
	default:
		if from, err = s.revisionFor(c, against); err != nil {
			return err
		}
	}
	curBundle, err := revisionBundle(rev)
	if err != nil {
		return err
	}
	var baseBundle platform.Bundle
	fromID := ""
	if from != nil {
		if baseBundle, err = revisionBundle(from); err != nil {
			return err
		}
		fromID = from.ID
	}
	files := diffBundles(baseBundle, curBundle)
	hasBCL := len(files) > 0
	var baseAssets platform.Bundle
	if from != nil {
		baseAssets = platform.Bundle(from.Assets)
	}
	files = append(files, diffBundles(baseAssets, platform.Bundle(rev.Assets))...)
	changes := []platform.DocumentChange{}
	if hasBCL {
		var baseDoc, curDoc *platform.Document
		if from != nil {
			baseDoc = s.validate(c.r.Context(), baseBundle).Document
		}
		curDoc = s.validate(c.r.Context(), curBundle).Document
		if curDoc != nil && (from == nil || baseDoc != nil) {
			changes = platform.DiffDocuments(baseDoc, curDoc)
		}
	}
	return c.json(http.StatusOK, map[string]any{"from": fromID, "to": rev.ID, "files": files, "changes": changes})
}

// requiredApprovals is how many distinct reviewers must approve a revision
// (0 when none is required).
func (s *Server) requiredApprovals() int {
	if s.cfg.Manager == nil || s.cfg.Manager.Approvals < 0 {
		return 0
	}
	return s.cfg.Manager.Approvals
}

// revisionDetail is a revision with the approvals the deployment requires.
type revisionDetail struct {
	*deploy.Revision
	RequiredApprovals int `json:"required_approvals"`
}

// ---------------------------------------------------------------------------
// Audit listing
// ---------------------------------------------------------------------------

// parseLimit reads ?limit= (default def, capped at max).
func parseLimit(r *http.Request, def, max int) (int, error) {
	q := r.URL.Query().Get("limit")
	if q == "" {
		return def, nil
	}
	n, err := strconv.Atoi(q)
	if err != nil || n < 1 {
		return 0, errf(http.StatusBadRequest, "bad_request", "limit must be a positive number")
	}
	return min(n, max), nil
}

func (s *Server) auditList(c *call) error {
	limit, err := parseLimit(c.r, 100, 1000)
	if err != nil {
		return err
	}
	var before int64
	if q := c.r.URL.Query().Get("before"); q != "" {
		if before, err = strconv.ParseInt(q, 10, 64); err != nil || before < 1 {
			return errf(http.StatusBadRequest, "bad_request", "before must be an audit entry id")
		}
	}
	out, err := s.audit.list(c.r.Context(), before, limit)
	if err != nil {
		return err
	}
	if len(out) == limit && limit > 0 {
		c.w.Header().Set("X-Next-Before", strconv.FormatInt(out[len(out)-1].ID, 10))
	}
	return c.json(http.StatusOK, out)
}
