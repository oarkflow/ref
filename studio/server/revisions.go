package server

import (
	"net/http"
	"strconv"
	"time"

	"github.com/oarkflow/ref/deploy"
)

// revisionSummary is a revision without its document.
type revisionSummary struct {
	ID           string              `json:"id"`
	App          string              `json:"app"`
	Seq          int64               `json:"seq"`
	Checksum     string              `json:"checksum"`
	Author       string              `json:"author"`
	Message      string              `json:"message,omitempty"`
	Status       string              `json:"status"`
	CreatedAt    time.Time           `json:"created_at"`
	BaseID       string              `json:"base_id,omitempty"`
	Approvals    []deploy.Decision   `json:"approvals,omitempty"`
	Rejection    *deploy.Decision    `json:"rejection,omitempty"`
	ActivatedAt  *time.Time          `json:"activated_at,omitempty"`
	ActivatedBy  string              `json:"activated_by,omitempty"`
	Failure      string              `json:"failure,omitempty"`
	Warnings     []string            `json:"warnings,omitempty"`
	Files        []string            `json:"files,omitempty"`
	ChangedFiles []deploy.FileChange `json:"changed_files,omitempty"`
	// Assets are the paths of the templates and static files the revision
	// overrides; ChangedAssets says which differ from the base revision's.
	Assets        []string            `json:"assets,omitempty"`
	ChangedAssets []deploy.FileChange `json:"changed_assets,omitempty"`
	Changes       int                 `json:"changes"`
	// RequiredApprovals is how many distinct reviewers the deployment needs.
	RequiredApprovals int `json:"required_approvals"`
}

func (s *Server) summarize(r *deploy.Revision) revisionSummary {
	out := summarize(r)
	out.RequiredApprovals = s.requiredApprovals()
	return out
}

func summarize(r *deploy.Revision) revisionSummary {
	s := revisionSummary{ID: r.ID, App: r.App, Seq: r.Seq, Checksum: r.Checksum, Author: r.Author, Message: r.Message,
		Status: r.Status, CreatedAt: r.CreatedAt, BaseID: r.BaseID, Approvals: r.Approvals, Rejection: r.Rejection,
		ActivatedAt: r.ActivatedAt, ActivatedBy: r.ActivatedBy, Failure: r.Failure, Warnings: r.Warnings,
		ChangedFiles: r.ChangedFiles, ChangedAssets: r.ChangedAssets, Changes: len(r.Changes)}
	for _, f := range r.Files {
		s.Files = append(s.Files, f.Path)
	}
	for _, f := range r.Assets {
		s.Assets = append(s.Assets, f.Path)
	}
	return s
}

func (s *Server) manager() (*deploy.Manager, error) {
	if s.cfg.Manager == nil {
		return nil, errf(http.StatusNotImplemented, "not_implemented", "no revision manager is configured")
	}
	return s.cfg.Manager, nil
}

func (s *Server) listRevisions(c *call) error {
	m, err := s.manager()
	if err != nil {
		return err
	}
	limit := 50
	if q := c.r.URL.Query().Get("limit"); q != "" {
		n, err := strconv.Atoi(q)
		if err != nil || n < 1 {
			return errf(http.StatusBadRequest, "bad_request", "limit must be a positive number")
		}
		limit = min(n, 200)
	}
	revs, err := m.Store.List(c.r.Context(), m.App, limit)
	if err != nil {
		return err
	}
	out := make([]revisionSummary, len(revs))
	for i, r := range revs {
		out[i] = s.summarize(r)
	}
	return c.json(http.StatusOK, out)
}

func (s *Server) getRevision(c *call) error {
	m, err := s.manager()
	if err != nil {
		return err
	}
	rev, err := m.Store.Get(c.r.Context(), c.r.PathValue("id"))
	if err != nil {
		return err
	}
	if rev.App != m.App {
		return errf(http.StatusNotFound, "not_found", "no such revision")
	}
	return c.json(http.StatusOK, revisionDetail{Revision: rev, RequiredApprovals: s.requiredApprovals()})
}

func (s *Server) decide(c *call, action string, do func(m *deploy.Manager, id, by, comment string) (*deploy.Revision, error)) error {
	m, err := s.manager()
	if err != nil {
		return err
	}
	var in struct {
		Comment string `json:"comment"`
	}
	if err := c.decode(s, &in); err != nil {
		return err
	}
	rev, err := do(m, c.r.PathValue("id"), c.id.Name, in.Comment)
	if err != nil {
		return err
	}
	s.record(c, action, rev.ID, in.Comment)
	if action == "revision.activate" && s.cfg.OnActivate != nil {
		s.cfg.OnActivate(rev)
	}
	return c.json(http.StatusOK, rev)
}

func (s *Server) approve(c *call) error {
	return s.decide(c, "revision.approve", func(m *deploy.Manager, id, by, comment string) (*deploy.Revision, error) {
		return m.Approve(c.r.Context(), id, by, comment)
	})
}

func (s *Server) reject(c *call) error {
	return s.decide(c, "revision.reject", func(m *deploy.Manager, id, by, comment string) (*deploy.Revision, error) {
		return m.Reject(c.r.Context(), id, by, comment)
	})
}

func (s *Server) activate(c *call) error {
	return s.decide(c, "revision.activate", func(m *deploy.Manager, id, by, _ string) (*deploy.Revision, error) {
		return m.Activate(c.r.Context(), id, by)
	})
}

func (s *Server) rollback(c *call) error {
	m, err := s.manager()
	if err != nil {
		return err
	}
	var in struct {
		To     string `json:"to"`
		Reason string `json:"reason"`
	}
	if err := c.decode(s, &in); err != nil {
		return err
	}
	rev, err := m.Rollback(c.r.Context(), in.To, c.id.Name, in.Reason)
	if err != nil {
		return err
	}
	s.record(c, "revision.rollback", rev.ID, in.Reason)
	if s.cfg.OnActivate != nil {
		s.cfg.OnActivate(rev)
	}
	return c.json(http.StatusOK, rev)
}
