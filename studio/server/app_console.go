package server

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/oarkflow/ref/platform"
)

func (s *Server) getAppManager() platform.AppManager {
	s.pmu.Lock()
	defer s.pmu.Unlock()
	if s.appMgr == nil {
		s.appMgr = platform.NewMemoryAppManager()
	}
	return s.appMgr
}

// GET /api/v1/apps
func (s *Server) listApps(c *call) error {
	q := c.r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	filter := platform.AppFilter{
		Status: q.Get("status"),
		Search: q.Get("search"),
		Limit:  limit,
		Offset: offset,
	}

	apps, err := s.getAppManager().ListApps(c.r.Context(), filter)
	if err != nil {
		return errf(http.StatusInternalServerError, "internal", "failed to list apps: %v", err)
	}
	return c.json(http.StatusOK, map[string]any{"apps": apps})
}

// POST /api/v1/apps
func (s *Server) createApp(c *call) error {
	var body struct {
		ID          string         `json:"id"`
		Name        string         `json:"name"`
		Description string         `json:"description"`
		Version     string         `json:"version"`
		SourceBCL   string         `json:"source_bcl"`
		Status      string         `json:"status"`
		Metadata    map[string]any `json:"metadata"`
	}
	if err := c.decode(s, &body); err != nil {
		return err
	}
	if strings.TrimSpace(body.Name) == "" {
		return errf(http.StatusBadRequest, "invalid_input", "name is required")
	}

	created, err := s.getAppManager().CreateApp(c.r.Context(), platform.AppDef{
		ID:          body.ID,
		Name:        body.Name,
		Description: body.Description,
		Version:     body.Version,
		SourceBCL:   body.SourceBCL,
		Status:      platform.AppStatus(body.Status),
		CreatedBy:   c.id.Name,
		Metadata:    body.Metadata,
	})
	if err != nil {
		return errf(http.StatusBadRequest, "invalid_input", "%s", err.Error())
	}
	s.record(c, "app.create", created.ID, created.Name)
	return c.json(http.StatusCreated, map[string]any{"app": created})
}

// GET /api/v1/apps/{id}
func (s *Server) getApp(c *call) error {
	id := c.r.PathValue("id")
	if id == "" {
		return errf(http.StatusBadRequest, "invalid_input", "id is required")
	}
	app, err := s.getAppManager().GetApp(c.r.Context(), id)
	if err != nil {
		return errf(http.StatusNotFound, "not_found", "app %q not found", id)
	}
	return c.json(http.StatusOK, map[string]any{"app": app})
}

// PUT /api/v1/apps/{id}
func (s *Server) updateApp(c *call) error {
	id := c.r.PathValue("id")
	if id == "" {
		return errf(http.StatusBadRequest, "invalid_input", "id is required")
	}
	var body struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		Version     string         `json:"version"`
		SourceBCL   string         `json:"source_bcl"`
		Status      string         `json:"status"`
		Metadata    map[string]any `json:"metadata"`
	}
	if err := c.decode(s, &body); err != nil {
		return err
	}

	updated, err := s.getAppManager().UpdateApp(c.r.Context(), id, platform.AppDef{
		Name:        body.Name,
		Description: body.Description,
		Version:     body.Version,
		SourceBCL:   body.SourceBCL,
		Status:      platform.AppStatus(body.Status),
		UpdatedBy:   c.id.Name,
		Metadata:    body.Metadata,
	})
	if err != nil {
		return errf(http.StatusBadRequest, "invalid_input", "%s", err.Error())
	}
	s.record(c, "app.update", id, fmt.Sprintf("revision %d", updated.RevisionID))
	return c.json(http.StatusOK, map[string]any{"app": updated})
}

// POST /api/v1/apps/{id}/activate
func (s *Server) activateApp(c *call) error {
	id := c.r.PathValue("id")
	if id == "" {
		return errf(http.StatusBadRequest, "invalid_input", "id is required")
	}
	var body struct {
		RevisionID int64 `json:"revision_id"`
	}
	_ = c.decode(s, &body)

	if err := s.getAppManager().ActivateApp(c.r.Context(), id, body.RevisionID); err != nil {
		return errf(http.StatusBadRequest, "invalid_input", "%s", err.Error())
	}
	s.record(c, "app.activate", id, fmt.Sprintf("revision %d", body.RevisionID))
	return c.json(http.StatusOK, map[string]any{"activated": true, "id": id, "revision_id": body.RevisionID})
}

// POST /api/v1/apps/{id}/deactivate
func (s *Server) deactivateApp(c *call) error {
	id := c.r.PathValue("id")
	if id == "" {
		return errf(http.StatusBadRequest, "invalid_input", "id is required")
	}
	if err := s.getAppManager().DeactivateApp(c.r.Context(), id); err != nil {
		return errf(http.StatusBadRequest, "invalid_input", "%s", err.Error())
	}
	s.record(c, "app.deactivate", id, "")
	return c.json(http.StatusOK, map[string]any{"deactivated": true, "id": id})
}

// DELETE /api/v1/apps/{id}
func (s *Server) deleteApp(c *call) error {
	id := c.r.PathValue("id")
	if id == "" {
		return errf(http.StatusBadRequest, "invalid_input", "id is required")
	}
	if err := s.getAppManager().DeleteApp(c.r.Context(), id); err != nil {
		return errf(http.StatusBadRequest, "invalid_input", "%s", err.Error())
	}
	s.record(c, "app.delete", id, "")
	return c.json(http.StatusOK, map[string]any{"deleted": true, "id": id})
}

// POST /api/v1/apps/{id}/rollback
func (s *Server) rollbackApp(c *call) error {
	id := c.r.PathValue("id")
	if id == "" {
		return errf(http.StatusBadRequest, "invalid_input", "id is required")
	}
	var body struct {
		RevisionID int64 `json:"revision_id"`
	}
	if err := c.decode(s, &body); err != nil {
		return err
	}
	if body.RevisionID <= 0 {
		return errf(http.StatusBadRequest, "invalid_input", "revision_id is required")
	}

	if err := s.getAppManager().RollbackApp(c.r.Context(), id, body.RevisionID); err != nil {
		return errf(http.StatusBadRequest, "invalid_input", "%s", err.Error())
	}
	s.record(c, "app.rollback", id, fmt.Sprintf("revision %d", body.RevisionID))
	return c.json(http.StatusOK, map[string]any{"rolled_back": true, "id": id, "revision_id": body.RevisionID})
}

// GET /api/v1/apps/{id}/health
func (s *Server) appHealth(c *call) error {
	id := c.r.PathValue("id")
	if id == "" {
		return errf(http.StatusBadRequest, "invalid_input", "id is required")
	}
	h, err := s.getAppManager().AppHealth(c.r.Context(), id)
	if err != nil {
		return errf(http.StatusNotFound, "not_found", "app %q not found", id)
	}
	return c.json(http.StatusOK, map[string]any{"health": h})
}

// GET /api/v1/apps/{id}/metrics
func (s *Server) appMetrics(c *call) error {
	id := c.r.PathValue("id")
	if id == "" {
		return errf(http.StatusBadRequest, "invalid_input", "id is required")
	}
	m, err := s.getAppManager().AppMetrics(c.r.Context(), id)
	if err != nil {
		return errf(http.StatusNotFound, "not_found", "app %q not found", id)
	}
	return c.json(http.StatusOK, map[string]any{"metrics": m})
}

// GET /api/v1/apps/{id}/logs
func (s *Server) appLogs(c *call) error {
	id := c.r.PathValue("id")
	if id == "" {
		return errf(http.StatusBadRequest, "invalid_input", "id is required")
	}
	limit, _ := strconv.Atoi(c.r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 100
	}
	logs, err := s.getAppManager().AppLogs(c.r.Context(), id, limit)
	if err != nil {
		return errf(http.StatusNotFound, "not_found", "app %q not found", id)
	}
	return c.json(http.StatusOK, map[string]any{"logs": logs})
}

// POST /api/v1/apps/{id}/run
func (s *Server) runAppIntent(c *call) error {
	id := c.r.PathValue("id")
	if id == "" {
		return errf(http.StatusBadRequest, "invalid_input", "id is required")
	}
	var body struct {
		Intent string         `json:"intent"`
		Inputs map[string]any `json:"inputs"`
	}
	if err := c.decode(s, &body); err != nil {
		return err
	}
	if body.Intent == "" {
		return errf(http.StatusBadRequest, "invalid_input", "intent is required")
	}

	result, err := s.getAppManager().RunIntent(c.r.Context(), id, body.Intent, body.Inputs)
	if err != nil {
		return errf(http.StatusInternalServerError, "execution_error", "%s", err.Error())
	}
	return c.json(http.StatusOK, map[string]any{"result": result})
}
