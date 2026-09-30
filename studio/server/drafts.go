package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/oarkflow/bcl"

	"github.com/oarkflow/ref/deploy"
	"github.com/oarkflow/ref/platform"
	"github.com/oarkflow/ref/studio"
	"github.com/oarkflow/ref/studio/model"
)

// ---------------------------------------------------------------------------
// Meta and schema
// ---------------------------------------------------------------------------

func (s *Server) meta(c *call) error {
	out := map[string]any{
		"app":      s.cfg.App,
		"version":  "v1",
		"identity": c.id,
		"features": map[string]bool{"preview": s.cfg.Preview != nil, "pages": s.cfg.Resources != nil},
	}
	if s.cfg.Manager != nil {
		if rev, err := s.cfg.Manager.Active(c.r.Context()); err == nil && rev != nil {
			out["activeRevision"] = s.summarize(rev)
		}
	}
	return c.json(http.StatusOK, out)
}

var (
	docsMu   sync.Mutex
	docsDone = map[string]bool{}
)

// attachDocs fills the block schemas' doc strings once per directory set.
func attachDocs(dirs []string) {
	key := strings.Join(dirs, "\x00")
	docsMu.Lock()
	defer docsMu.Unlock()
	if docsDone[key] {
		return
	}
	docsDone[key] = true
	platform.AttachBlockDocs(dirs...)
}

func (s *Server) schemaBlocks(c *call) error {
	return c.json(http.StatusOK, platform.BlockSchemas())
}

func (s *Server) schemaCatalog(c *call) error {
	return c.json(http.StatusOK, s.registry.Catalog())
}

// ---------------------------------------------------------------------------
// Validation and diagnostics
// ---------------------------------------------------------------------------

// validate checks a bundle the way a proposal would be checked.
func (s *Server) validate(ctx context.Context, b platform.Bundle) platform.ValidationReport {
	if m := s.cfg.Manager; m != nil && (m.ValidateBundle != nil || m.Validate != nil) {
		return m.ValidateFiles(ctx, b)
	}
	return platform.ValidateBundle(ctx, b, s.cfg.BaseDir, s.cfg.LoadOptions)
}

// reportDiagnostics converts a report, adding a diagnostic for a finding the
// report only has as text and one info per file the structural editor
// cannot open.
func reportDiagnostics(r platform.ValidationReport, state snapshot) []studio.Diagnostic {
	out := studio.FromPlatform(r.Diagnostics)
	if len(out) == 0 {
		for _, m := range r.Errors {
			out = append(out, studio.Diagnostic{Severity: "error", Message: m})
		}
		for _, m := range r.Warnings {
			out = append(out, studio.Diagnostic{Severity: "warning", Message: m})
		}
	}
	for _, name := range state.names() {
		if e := state[name]; e.file == nil {
			out = append(out, studio.Diagnostic{Severity: "info", Code: "studio.text_only", File: name,
				Message: "this file can only be edited as text: " + e.why})
		}
	}
	return out
}

// stateDiagnostics validates a snapshot: the BCL, plus how its routes line up
// with the page templates (warnings, code prefix studio.pages.).
func (s *Server) stateDiagnostics(ctx context.Context, state snapshot) []studio.Diagnostic {
	return append(reportDiagnostics(s.validate(ctx, state.bundle()), state), s.pageDiagnostics(state)...)
}

// diagnostics validates the draft at the given version, caching the result
// for that version.
func (s *Server) diagnostics(ctx context.Context, d *Draft, version int64, state snapshot) []studio.Diagnostic {
	d.mu.Lock()
	if d.diagOK && d.diagVer == version {
		out := cloneDiags(d.diag)
		d.mu.Unlock()
		return out
	}
	d.mu.Unlock()
	diags := s.stateDiagnostics(ctx, state)
	d.mu.Lock()
	if d.version == version {
		d.diag, d.diagVer, d.diagOK = diags, version, true
	}
	d.mu.Unlock()
	return cloneDiags(diags)
}

// cloneDiags copies ds; the result is never nil, so it encodes as [] not null.
func cloneDiags(ds []studio.Diagnostic) []studio.Diagnostic {
	return append(make([]studio.Diagnostic, 0, len(ds)), ds...)
}

type counts struct {
	Errors   int `json:"errors"`
	Warnings int `json:"warnings"`
}

func countDiagnostics(ds []studio.Diagnostic, file string) counts {
	var n counts
	for _, d := range ds {
		if file != "" && d.File != file {
			continue
		}
		switch d.Severity {
		case "error":
			n.Errors++
		case "warning":
			n.Warnings++
		}
	}
	return n
}

// parseDiagnostics turns a BCL syntax error in file into diagnostics.
func parseDiagnostics(file string, err error) []studio.Diagnostic {
	var list bcl.ErrorList
	if errors.As(err, &list) && len(list) > 0 {
		out := make([]studio.Diagnostic, 0, len(list))
		for _, d := range list {
			sev := d.Severity
			if sev == "" {
				sev = "error"
			}
			out = append(out, studio.Diagnostic{Severity: sev, Code: d.Code, Message: d.Message, File: file,
				Line: d.Span.Start.Line, Column: d.Span.Start.Column, Offset: d.Span.Start.Offset})
		}
		return out
	}
	return []studio.Diagnostic{{Severity: "error", Code: "parse", Message: err.Error(), File: file}}
}

// ---------------------------------------------------------------------------
// Drafts
// ---------------------------------------------------------------------------

type draftSummary struct {
	ID           string   `json:"id"`
	Owner        string   `json:"owner"`
	Name         string   `json:"name"`
	BaseRevision string   `json:"baseRevision,omitempty"`
	Version      int64    `json:"version"`
	Files        []string `json:"files"`
	// Assets are the page templates and static files the draft overrides.
	Assets      []string  `json:"assets"`
	Dirty       bool      `json:"dirty"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
	Diagnostics counts    `json:"diagnostics"`
}

func (s *Server) summary(ctx context.Context, d *Draft) draftSummary {
	v := d.view()
	diags := s.diagnostics(ctx, d, v.version, v.state)
	return draftSummary{ID: d.id, Owner: d.owner, Name: v.name, BaseRevision: d.base, Version: v.version,
		Files: v.state.names(), Assets: v.state.assets().Paths(), Dirty: v.dirty(d.baseFiles, d.baseAssets),
		CreatedAt: d.created, UpdatedAt: v.updated, Diagnostics: countDiagnostics(diags, "")}
}

// draft finds the draft named in the path and checks the caller may use it:
// its owner, or an admin.
func (s *Server) draft(c *call) (*Draft, error) {
	d, ok := s.store.Get(c.r.PathValue("id"))
	if !ok {
		return nil, errf(http.StatusNotFound, "not_found", "no such draft")
	}
	if d.owner != c.id.Name && !c.id.Has(studio.RoleAdmin) {
		return nil, errf(http.StatusForbidden, "forbidden", "this draft belongs to %s", d.owner)
	}
	return d, nil
}

func (s *Server) listDrafts(c *call) error {
	out := []draftSummary{}
	for _, d := range s.store.List() {
		if d.owner == c.id.Name || c.id.Has(studio.RoleAdmin) {
			out = append(out, s.summary(c.r.Context(), d))
		}
	}
	return c.json(http.StatusOK, out)
}

// revisionBundle is a revision's files; a legacy single-document revision
// becomes one file, main.bcl.
func revisionBundle(r *deploy.Revision) (platform.Bundle, error) {
	if r.IsBundle() {
		return platform.NewBundle(r.Files)
	}
	return platform.NewBundle([]platform.BundleFile{{Path: "main.bcl", Content: r.Source}})
}

// revisionAssets is a revision's template and static overrides.
func revisionAssets(r *deploy.Revision) (platform.Assets, error) {
	return platform.NewAssets(r.Assets)
}

func (s *Server) initialBundle(ctx context.Context, from string) (platform.Bundle, platform.Assets, string, error) {
	needManager := func() error {
		if s.cfg.Manager == nil {
			return errf(http.StatusNotImplemented, "not_implemented", "no revision manager is configured")
		}
		return nil
	}
	switch {
	case from == "dir":
		if s.cfg.ConfigDir == "" {
			return nil, nil, "", errf(http.StatusUnprocessableEntity, "no_config_dir", "no config directory is configured")
		}
		b, err := platform.ReadBundleDir(s.cfg.ConfigDir)
		if err != nil {
			return nil, nil, "", errf(http.StatusUnprocessableEntity, "invalid", "%s", trimPrefixErr(err).Error())
		}
		return b, nil, "", nil
	case from == "" || from == "active":
		if err := needManager(); err != nil {
			return nil, nil, "", err
		}
		rev, err := s.cfg.Manager.Active(ctx)
		if err != nil {
			return nil, nil, "", err
		}
		if rev == nil {
			return nil, nil, "", errf(http.StatusConflict, "no_active_revision", "there is no active revision")
		}
		b, err := revisionBundle(rev)
		if err != nil {
			return nil, nil, "", err
		}
		a, err := revisionAssets(rev)
		if err != nil {
			return nil, nil, "", err
		}
		return b, a, rev.ID, nil
	case strings.HasPrefix(from, "revision:"):
		if err := needManager(); err != nil {
			return nil, nil, "", err
		}
		id := strings.TrimPrefix(from, "revision:")
		rev, err := s.cfg.Manager.Store.Get(ctx, id)
		if err != nil {
			return nil, nil, "", err
		}
		if rev.App != s.cfg.Manager.App {
			return nil, nil, "", errf(http.StatusNotFound, "not_found", "no such revision")
		}
		b, err := revisionBundle(rev)
		if err != nil {
			return nil, nil, "", err
		}
		a, err := revisionAssets(rev)
		if err != nil {
			return nil, nil, "", err
		}
		return b, a, rev.ID, nil
	}
	return nil, nil, "", errf(http.StatusBadRequest, "bad_request", `from must be "active", "revision:<id>" or "dir"`)
}

func (s *Server) createDraft(c *call) error {
	var in struct {
		Name string `json:"name"`
		From string `json:"from"`
	}
	if err := c.decode(s, &in); err != nil {
		return err
	}
	owned := 0
	for _, d := range s.store.List() {
		if d.owner == c.id.Name {
			owned++
		}
	}
	if owned >= s.cfg.MaxDraftsPerOwner {
		return errf(http.StatusConflict, "too_many_drafts", "you already have %d drafts; delete one first", owned)
	}
	bundle, assets, base, err := s.initialBundle(c.r.Context(), in.From)
	if err != nil {
		return err
	}
	d := newDraft(c.id.Name, in.Name, base, bundle, assets, s.cfg.Now().UTC())
	if err := s.store.Create(d); err != nil {
		return err
	}
	s.record(c, "draft.create", d.id, fmt.Sprintf("from %q, %d files, %d assets", orDefault(in.From, "active"), len(bundle), len(assets)))
	return c.json(http.StatusCreated, s.summary(c.r.Context(), d))
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func (s *Server) getDraft(c *call) error {
	d, err := s.draft(c)
	if err != nil {
		return err
	}
	v := d.view()
	diags := s.diagnostics(c.r.Context(), d, v.version, v.state)
	return c.json(http.StatusOK, struct {
		draftSummary
		Diagnostics []studio.Diagnostic `json:"diagnostics"`
	}{draftSummary{ID: d.id, Owner: d.owner, Name: v.name, BaseRevision: d.base, Version: v.version,
		Files: v.state.names(), Assets: v.state.assets().Paths(), Dirty: v.dirty(d.baseFiles, d.baseAssets),
		CreatedAt: d.created, UpdatedAt: v.updated, Diagnostics: countDiagnostics(diags, "")}, diags})
}

func (s *Server) deleteDraft(c *call) error {
	d, err := s.draft(c)
	if err != nil {
		return err
	}
	if s.cfg.Preview != nil {
		s.cfg.Preview.Stop(d.id)
	}
	if !s.store.Delete(d.id) {
		return errf(http.StatusInternalServerError, "internal", "could not delete the draft")
	}
	d.close()
	s.flows.drop(d.id)
	s.record(c, "draft.delete", d.id, "")
	c.w.WriteHeader(http.StatusNoContent)
	return nil
}

// ---------------------------------------------------------------------------
// Files and trees
// ---------------------------------------------------------------------------

func (s *Server) listFiles(c *call) error {
	d, err := s.draft(c)
	if err != nil {
		return err
	}
	v := d.view()
	diags := s.diagnostics(c.r.Context(), d, v.version, v.state)
	type item struct {
		Path        string `json:"path"`
		Size        int    `json:"size"`
		Diagnostics counts `json:"diagnostics"`
	}
	out := []item{}
	for _, name := range v.state.names() {
		out = append(out, item{Path: name, Size: len(v.state[name].text()), Diagnostics: countDiagnostics(diags, name)})
	}
	return c.json(http.StatusOK, out)
}

func (s *Server) getFile(c *call) error {
	d, err := s.draft(c)
	if err != nil {
		return err
	}
	v := d.view()
	name := c.r.PathValue("file")
	e, ok := v.state[name]
	if !ok {
		return errf(http.StatusNotFound, "not_found", "no file %q in the draft", name)
	}
	return c.json(http.StatusOK, map[string]any{"path": name, "content": e.text(), "version": v.version})
}

func (s *Server) putFile(c *call) error {
	d, err := s.draft(c)
	if err != nil {
		return err
	}
	name := c.r.PathValue("file")
	var in struct {
		Content   string `json:"content"`
		IfVersion *int64 `json:"ifVersion"`
	}
	c.limit = s.cfg.MaxBody
	if err := c.decode(s, &in); err != nil {
		return err
	}
	ch, err := d.replaceFile(s.cfg.Now().UTC(), in.IfVersion, name, in.Content)
	if err != nil {
		var pe *parseError
		if errors.As(err, &pe) {
			return errf(http.StatusUnprocessableEntity, "invalid_bcl", "the content is not valid BCL").
				with(map[string]any{"diagnostics": parseDiagnostics(name, pe.err)})
		}
		return err
	}
	s.record(c, "draft.put_file", d.id, name)
	return s.finish(c, d, ch, false)
}

// blockNode is a statement in the tree endpoints.
type blockNode struct {
	Kind     string      `json:"kind"`
	Path     string      `json:"path"`
	Type     string      `json:"type,omitempty"`
	ID       string      `json:"id,omitempty"`
	Name     string      `json:"name,omitempty"`
	Raw      string      `json:"raw,omitempty"`
	Comment  string      `json:"comment,omitempty"`
	Line     int         `json:"line"`
	Start    int         `json:"start"`
	End      int         `json:"end"`
	Opaque   bool        `json:"opaque,omitempty"`
	Children []blockNode `json:"children,omitempty"`
}

const maxTreeDepth = 64

func toNode(f *model.File, n model.Node, depth int) blockNode {
	b := blockNode{Path: n.Path.String(), Comment: n.Doc, Line: n.Line, Start: n.Start, End: n.End, Opaque: n.Opaque}
	if n.Kind == model.KindBlock {
		b.Kind, b.Type, b.ID = "block", n.Head, n.ID
		if depth < maxTreeDepth {
			if kids, err := f.Statements(n.Path); err == nil {
				for _, k := range kids {
					b.Children = append(b.Children, toNode(f, k, depth+1))
				}
			}
		}
		return b
	}
	b.Kind, b.Name, b.Raw = "field", n.Head, n.Value
	return b
}

func fileTree(e entry) []blockNode {
	out := []blockNode{}
	if e.file == nil {
		return out
	}
	top, err := e.file.Statements(nil)
	if err != nil {
		return out
	}
	for _, n := range top {
		out = append(out, toNode(e.file, n, 0))
	}
	return out
}

func (s *Server) fileTree(c *call) error {
	d, err := s.draft(c)
	if err != nil {
		return err
	}
	name := c.r.PathValue("file")
	e, ok := d.view().state[name]
	if !ok {
		return errf(http.StatusNotFound, "not_found", "no file %q in the draft", name)
	}
	return c.json(http.StatusOK, fileTree(e))
}

// draftTree lists each file's top-level statements without their children.
func (s *Server) draftTree(c *call) error {
	d, err := s.draft(c)
	if err != nil {
		return err
	}
	v := d.view()
	out := map[string][]blockNode{}
	for name, e := range v.state {
		nodes := fileTree(e)
		for i := range nodes {
			nodes[i].Children = nil
		}
		out[name] = nodes
	}
	return c.json(http.StatusOK, out)
}

// ---------------------------------------------------------------------------
// Edits
// ---------------------------------------------------------------------------

type editResult struct {
	Version     int64               `json:"version"`
	Applied     int                 `json:"applied"`
	Diagnostics []studio.Diagnostic `json:"diagnostics"`
	Changed     []string            `json:"changed"`
}

// finish validates the outcome of an edit, tells subscribers and replies.
func (s *Server) finish(c *call, d *Draft, ch change, dry bool) error {
	diags := s.diagnostics(c.r.Context(), d, ch.version, ch.state)
	if dry {
		diags = s.stateDiagnostics(c.r.Context(), ch.state)
	}
	changed := ch.changed
	if changed == nil {
		changed = []string{}
	}
	if !dry {
		s.persist(d)
	}
	if !dry && len(changed) > 0 {
		d.publish("changed", changedEvent{Version: ch.version, Changed: changed})
		d.publish("diagnostics", map[string]any{"version": ch.version, "diagnostics": diags})
	}
	return c.json(http.StatusOK, editResult{Version: ch.version, Applied: ch.applied, Diagnostics: diags, Changed: changed})
}

// opFailure turns a failed op into a 422 that names the op.
func opFailure(err error) error {
	var oe *opError
	if errors.As(err, &oe) {
		return errf(http.StatusUnprocessableEntity, "op_failed", "%s", oe.Error()).
			with(map[string]any{"index": oe.Index, "op": oe.Op})
	}
	return err
}

func (s *Server) applyOps(c *call) error {
	d, err := s.draft(c)
	if err != nil {
		return err
	}
	var in struct {
		Ops       []Op   `json:"ops"`
		IfVersion *int64 `json:"ifVersion"`
		DryRun    bool   `json:"dryRun"`
	}
	c.limit = s.cfg.MaxBody
	if err := c.decode(s, &in); err != nil {
		return err
	}
	if len(in.Ops) == 0 {
		return errf(http.StatusBadRequest, "bad_request", "ops is empty")
	}
	if len(in.Ops) > s.cfg.MaxOps {
		return errf(http.StatusUnprocessableEntity, "too_many_ops", "a batch is limited to %d ops (got %d)", s.cfg.MaxOps, len(in.Ops))
	}
	ch, err := d.applyOps(s.cfg.Now().UTC(), in.IfVersion, in.Ops, in.DryRun)
	if err != nil {
		return opFailure(err)
	}
	if !in.DryRun {
		s.record(c, "draft.ops", d.id, opsDetail(in.Ops, ch.changed))
	}
	return s.finish(c, d, ch, in.DryRun)
}

func opsDetail(ops []Op, changed []string) string {
	names := make([]string, 0, len(ops))
	for _, o := range ops {
		names = append(names, o.Op)
	}
	if len(names) > 8 {
		names = append(names[:8], "…")
	}
	return fmt.Sprintf("%d ops (%s); files: %s", len(ops), strings.Join(names, ","), strings.Join(changed, ","))
}

func (s *Server) undo(c *call) error { return s.stepHistory(c, false) }
func (s *Server) redo(c *call) error { return s.stepHistory(c, true) }

func (s *Server) stepHistory(c *call, redo bool) error {
	d, err := s.draft(c)
	if err != nil {
		return err
	}
	ch, err := d.step(s.cfg.Now().UTC(), redo)
	if err != nil {
		return err
	}
	action := "draft.undo"
	if redo {
		action = "draft.redo"
	}
	s.record(c, action, d.id, strings.Join(ch.changed, ","))
	return s.finish(c, d, ch, false)
}

func (s *Server) format(c *call) error {
	d, err := s.draft(c)
	if err != nil {
		return err
	}
	var in struct {
		IfVersion *int64 `json:"ifVersion"`
	}
	if err := c.decode(s, &in); err != nil {
		return err
	}
	ch, err := d.reformat(s.cfg.Now().UTC(), in.IfVersion)
	if err != nil {
		return err
	}
	s.record(c, "draft.format", d.id, strings.Join(ch.changed, ","))
	return s.finish(c, d, ch, false)
}

func (s *Server) validateDraft(c *call) error {
	d, err := s.draft(c)
	if err != nil {
		return err
	}
	var in struct {
		IfVersion *int64 `json:"ifVersion"`
	}
	if err := c.decode(s, &in); err != nil {
		return err
	}
	v := d.view()
	if in.IfVersion != nil && *in.IfVersion != v.version {
		return errStale(v.version)
	}
	report := s.validate(c.r.Context(), v.state.bundle())
	diags := append(reportDiagnostics(report, v.state), s.pageDiagnostics(v.state)...)
	d.mu.Lock()
	if d.version == v.version {
		d.diag, d.diagVer, d.diagOK = diags, v.version, true
	}
	d.mu.Unlock()
	return c.json(http.StatusOK, map[string]any{"valid": report.Valid, "version": v.version, "diagnostics": diags, "summary": report.Summary})
}

// ---------------------------------------------------------------------------
// Diff
// ---------------------------------------------------------------------------

type fileDiff struct {
	Path    string `json:"path"`
	Status  string `json:"status"` // added, modified, removed
	Unified string `json:"unified,omitempty"`
}

func (s *Server) diff(c *call) error {
	d, err := s.draft(c)
	if err != nil {
		return err
	}
	v := d.view()
	files := diffBundles(d.baseFiles, v.state.bundle())
	hasBCL := len(files) > 0
	files = append(files, diffBundles(platform.Bundle(d.baseAssets), platform.Bundle(v.state.assets()))...)
	changes := []platform.DocumentChange{}
	if hasBCL {
		if cur := s.validate(c.r.Context(), v.state.bundle()); cur.Document != nil {
			if base := s.baseDocument(c.r.Context(), d); base != nil {
				changes = platform.DiffDocuments(base, cur.Document)
			} else {
				changes = platform.DiffDocuments(nil, cur.Document)
			}
		}
	}
	return c.json(http.StatusOK, map[string]any{"version": v.version, "files": files, "changes": changes})
}

// baseDocument is the parsed document the draft started from.
func (s *Server) baseDocument(ctx context.Context, d *Draft) *platform.Document {
	d.mu.Lock()
	if d.baseDone {
		doc := d.baseDoc
		d.mu.Unlock()
		return doc
	}
	d.mu.Unlock()
	doc := s.validate(ctx, d.baseFiles).Document
	d.mu.Lock()
	d.baseDoc, d.baseDone = doc, true
	d.mu.Unlock()
	return doc
}

// ---------------------------------------------------------------------------
// Events
// ---------------------------------------------------------------------------

func (s *Server) events(c *call) error {
	d, err := s.draft(c)
	if err != nil {
		return err
	}
	rc := http.NewResponseController(c.w)
	h := c.w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	c.w.WriteHeader(http.StatusOK)

	ch, cancel := d.subscribe()
	defer cancel()
	send := func(name string, data any) bool {
		raw, err := marshalEvent(data)
		if err != nil {
			return true
		}
		if _, err := fmt.Fprintf(c.w, "event: %s\ndata: %s\n\n", name, raw); err != nil {
			return false
		}
		return rc.Flush() == nil
	}
	if !send("changed", changedEvent{Version: d.Version(), Changed: []string{}}) {
		return nil
	}
	tick := time.NewTicker(15 * time.Second)
	defer tick.Stop()
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return nil
			}
			if !send(ev.Name, ev.Data) {
				return nil
			}
		case <-tick.C:
			if _, err := fmt.Fprint(c.w, ": ping\n\n"); err != nil || rc.Flush() != nil {
				return nil
			}
		case <-c.r.Context().Done():
			return nil
		case <-s.done:
			send("shutdown", map[string]string{"reason": "server is shutting down"})
			return nil
		}
	}
}

// persist saves the draft after an edit. A failure is logged, not returned:
// the edit is already in memory and visible to every client, and the next
// save retries the whole state.
func (s *Server) persist(d *Draft) {
	if err := s.store.Save(d); err != nil {
		s.cfg.Logf("studio: saving draft %s: %v", d.id, err)
	}
}

// ---------------------------------------------------------------------------
// Propose
// ---------------------------------------------------------------------------

func (s *Server) propose(c *call) error {
	d, err := s.draft(c)
	if err != nil {
		return err
	}
	if s.cfg.Manager == nil {
		return errf(http.StatusNotImplemented, "not_implemented", "no revision manager is configured")
	}
	var in struct {
		Message   string `json:"message"`
		IfVersion *int64 `json:"ifVersion"`
	}
	if err := c.decode(s, &in); err != nil {
		return err
	}
	v := d.view()
	if in.IfVersion != nil && *in.IfVersion != v.version {
		return errStale(v.version)
	}
	var rev *deploy.Revision
	if assets := v.state.assets(); len(assets) > 0 {
		rev, err = s.cfg.Manager.ProposeBundleAssets(c.r.Context(), v.state.bundle(), assets, c.id.Name, in.Message)
	} else {
		rev, err = s.cfg.Manager.ProposeBundle(c.r.Context(), v.state.bundle(), c.id.Name, in.Message)
	}
	if err != nil {
		return err
	}
	s.record(c, "revision.propose", rev.ID, fmt.Sprintf("from draft %s v%d: %s (%d assets)", d.id, v.version, in.Message, len(rev.Assets)))
	return c.json(http.StatusCreated, rev)
}
