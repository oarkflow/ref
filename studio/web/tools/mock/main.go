// Command mock is a development stand-in for the studio server: it implements
// the endpoints of docs/studio-api.md on top of the real studio/model,
// platform and deploy packages, with in-memory drafts and revisions. It exists
// so the web app can be built and exercised before studio/server lands; it is
// not a production server (fixed tokens, no persistence, and a fake preview).
//
//	go run ./studio/web/tools/mock -dir examples/starter/resources/config
//
// Tokens: editor-token-000001 (alice, editor), reviewer-token-0001 (bob,
// reviewer), admin-token-0000001 (root, admin).
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/oarkflow/ref/deploy"
	"github.com/oarkflow/ref/platform"
	"github.com/oarkflow/ref/studio/model"
)

type identity struct {
	Name  string   `json:"name"`
	Roles []string `json:"roles"`
}

var tokens = map[string]identity{
	"editor-token-000001":   {"alice", []string{"editor"}},
	"reviewer-token-0001":   {"bob", []string{"reviewer"}},
	"admin-token-0000001":   {"root", []string{"admin"}},
	"viewer-token-00000001": {"vera", []string{"viewer"}},
}

type diag struct {
	Severity string `json:"severity"`
	Code     string `json:"code,omitempty"`
	Message  string `json:"message"`
	File     string `json:"file,omitempty"`
	Line     int    `json:"line,omitempty"`
	Column   int    `json:"column,omitempty"`
	Offset   int    `json:"offset,omitempty"`
	Path     string `json:"path,omitempty"`
}

type auditEntry struct {
	At     time.Time `json:"at"`
	Who    string    `json:"who"`
	Action string    `json:"action"`
	Target string    `json:"target"`
	Detail string    `json:"detail,omitempty"`
}

type draft struct {
	id, owner, name, base string
	version               int64
	files                 map[string]*model.File
	baseFiles             map[string]string
	undo, redo            []map[string]string
	created, updated      time.Time
	diags                 []diag
	subs                  map[chan []byte]struct{}
	pv                    *mockPreview
}

type server struct {
	mu     sync.Mutex
	dir    string
	mgr    *deploy.Manager
	drafts map[string]*draft
	audit  []auditEntry
	opts   platform.LoadOptions
}

func main() {
	addr := flag.String("addr", "127.0.0.1:8787", "listen address")
	dir := flag.String("dir", "examples/starter/resources/config", "config directory (the 'dir' draft source)")
	flag.Parse()
	s := &server{dir: *dir, drafts: map[string]*draft{}, opts: platform.LoadOptions{AllowEnv: true}}
	s.mgr = &deploy.Manager{
		Store: deploy.NewMemoryStore(), App: "starter", Secret: []byte("mock-secret-mock-secret-mock-000"),
		Approvals: 1,
		Validate: func(ctx context.Context, src []byte) platform.ValidationReport {
			return platform.Validate(ctx, src, s.dir, s.opts)
		},
		ValidateBundle: func(ctx context.Context, b platform.Bundle) platform.ValidationReport {
			return platform.ValidateBundle(ctx, b, s.dir, s.opts)
		},
	}
	platform.AttachBlockDocs("platform", "pipeline")
	if b, err := platform.ReadBundleDir(*dir); err == nil {
		ctx := context.Background()
		if r, err := s.mgr.ProposeBundle(ctx, b, "system", "boot revision"); err == nil {
			if _, err = s.mgr.Approve(ctx, r.ID, "bootstrap", "seed"); err == nil {
				_, _ = s.mgr.Activate(ctx, r.ID, "bootstrap")
			}
		} else {
			log.Printf("seed revision: %v", err)
		}
	}
	mux := http.NewServeMux()
	s.routes(mux)
	h := cors(http.StripPrefix("", stripStudio(mux)))
	log.Printf("studio mock on http://%s (dir %s)", *addr, *dir)
	log.Fatal(http.ListenAndServe(*addr, h))
}

func stripStudio(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.URL.Path = strings.TrimPrefix(r.URL.Path, "/studio")
		next.ServeHTTP(w, r)
	})
}

func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ---- helpers ---------------------------------------------------------------

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, code int, ecode, msg string, details any) {
	writeJSON(w, code, map[string]any{"error": map[string]any{"code": ecode, "message": msg, "details": details}})
}

func rank(roles []string) int {
	best := 0
	for _, r := range roles {
		v := map[string]int{"viewer": 1, "editor": 2, "reviewer": 3, "admin": 4}[r]
		best = max(best, v)
	}
	return best
}

type handler func(w http.ResponseWriter, r *http.Request, who identity)

func (s *server) auth(min string, h handler) http.HandlerFunc {
	need := map[string]int{"viewer": 1, "editor": 2, "reviewer": 3, "admin": 4}[min]
	return func(w http.ResponseWriter, r *http.Request) {
		tok, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if tok == "" {
			tok = r.URL.Query().Get("token")
		}
		who, ok := tokens[tok]
		if !ok {
			fail(w, 401, "unauthorized", "missing or unknown token", nil)
			return
		}
		if rank(who.Roles) < need {
			fail(w, 403, "forbidden", "requires role "+min, nil)
			return
		}
		h(w, r, who)
	}
}

func newID(prefix string) string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return prefix + hex.EncodeToString(b[:])
}

func (s *server) log(who, action, target, detail string) {
	s.audit = append(s.audit, auditEntry{time.Now().UTC(), who, action, target, detail})
}

// ---- drafts ----------------------------------------------------------------

func (d *draft) bundle() (platform.Bundle, error) {
	files := make([]platform.BundleFile, 0, len(d.files))
	for n, f := range d.files {
		files = append(files, platform.BundleFile{Path: n, Content: f.Text()})
	}
	return platform.NewBundle(files)
}

func (d *draft) snapshot() map[string]string {
	m := make(map[string]string, len(d.files))
	for n, f := range d.files {
		m[n] = f.Text()
	}
	return m
}

func (d *draft) restore(m map[string]string) error {
	files := map[string]*model.File{}
	for n, c := range m {
		f, err := model.Open(n, []byte(c))
		if err != nil {
			return err
		}
		files[n] = f
	}
	d.files = files
	return nil
}

func (d *draft) dirty() bool {
	cur := d.snapshot()
	if len(cur) != len(d.baseFiles) {
		return true
	}
	for n, c := range cur {
		if d.baseFiles[n] != c {
			return true
		}
	}
	return false
}

func toDiags(rep platform.ValidationReport) []diag {
	out := []diag{}
	for _, d := range rep.Diagnostics {
		x := diag{Severity: d.Severity, Code: d.Code, Message: d.Message, Path: d.Path}
		if d.Span != nil {
			x.File, x.Line, x.Column, x.Offset = d.Span.File, d.Span.Line, d.Span.Column, d.Span.Offset
		}
		out = append(out, x)
	}
	if len(out) == 0 { // old-style reports
		for _, e := range rep.Errors {
			out = append(out, diag{Severity: "error", Message: e})
		}
		for _, e := range rep.Warnings {
			out = append(out, diag{Severity: "warning", Message: e})
		}
	}
	return out
}

func (s *server) revalidate(d *draft) platform.ValidationReport {
	b, err := d.bundle()
	if err != nil {
		d.diags = []diag{{Severity: "error", Message: err.Error()}}
		return platform.ValidationReport{Errors: []string{err.Error()}}
	}
	rep := platform.ValidateBundle(context.Background(), b, s.dir, s.opts)
	d.diags = toDiags(rep)
	return rep
}

func (d *draft) summary() map[string]any {
	names := make([]string, 0, len(d.files))
	for n := range d.files {
		names = append(names, n)
	}
	sort.Strings(names)
	errs, warns := 0, 0
	for _, x := range d.diags {
		if x.Severity == "error" {
			errs++
		} else {
			warns++
		}
	}
	return map[string]any{"id": d.id, "owner": d.owner, "name": d.name, "baseRevision": d.base, "version": d.version,
		"files": names, "dirty": d.dirty(), "createdAt": d.created, "updatedAt": d.updated,
		"diagnostics": map[string]int{"errors": errs, "warnings": warns},
		"canUndo":     len(d.undo) > 0, "canRedo": len(d.redo) > 0}
}

func (d *draft) broadcast(event string, v any) {
	b, _ := json.Marshal(v)
	msg := []byte("event: " + event + "\ndata: " + string(b) + "\n\n")
	for ch := range d.subs {
		select {
		case ch <- msg:
		default:
		}
	}
}

func (s *server) getDraft(w http.ResponseWriter, r *http.Request, who identity) *draft {
	d, ok := s.drafts[r.PathValue("id")]
	if !ok || (d.owner != who.Name && rank(who.Roles) < 4) {
		fail(w, 404, "not_found", "no such draft", nil)
		return nil
	}
	return d
}

func revFiles(r *deploy.Revision) map[string]string {
	m := map[string]string{}
	for _, f := range r.Files {
		m[f.Path] = f.Content
	}
	return m
}

// ---- tree ------------------------------------------------------------------

type blockNode struct {
	Kind     string       `json:"kind"`
	Path     string       `json:"path"`
	Type     string       `json:"type,omitempty"`
	ID       string       `json:"id,omitempty"`
	Name     string       `json:"name,omitempty"`
	Raw      string       `json:"raw,omitempty"`
	Comment  string       `json:"comment,omitempty"`
	Line     int          `json:"line"`
	Start    int          `json:"start"`
	End      int          `json:"end"`
	Children []*blockNode `json:"children,omitempty"`
}

func toNode(f *model.File, n model.Node, deep bool) *blockNode {
	b := &blockNode{Kind: n.Kind.String(), Path: n.Path.String(), Comment: n.Doc, Line: n.Line, Start: n.Start, End: n.End}
	if n.Kind == model.KindBlock {
		b.Type, b.ID = n.Head, n.ID
		if deep {
			kids, _ := f.Statements(n.Path)
			b.Children = []*blockNode{}
			for _, k := range kids {
				b.Children = append(b.Children, toNode(f, k, true))
			}
		}
	} else {
		b.Name, b.Raw = n.Head, n.Value
	}
	return b
}

func tree(f *model.File, deep bool) []*blockNode {
	out := []*blockNode{}
	for _, n := range f.Blocks() {
		out = append(out, toNode(f, n, deep))
	}
	return out
}

// ---- ops -------------------------------------------------------------------

type op struct {
	Op      string `json:"op"`
	File    string `json:"file"`
	Path    string `json:"path"`
	Value   string `json:"value"`
	Parent  string `json:"parent"`
	Type    string `json:"type"`
	ID      string `json:"id"`
	Body    string `json:"body"`
	NewID   string `json:"newId"`
	Index   int    `json:"index"`
	Content string `json:"content"`
	NewFile string `json:"newFile"`
}

func applyOps(files map[string]*model.File, ops []op) (map[string]*model.File, []string, error) {
	out := make(map[string]*model.File, len(files))
	for k, v := range files {
		out[k] = v
	}
	changed := map[string]bool{}
	for i, o := range ops {
		fail := func(err error) error { return fmt.Errorf("op %d (%s): %w", i, o.Op, err) }
		switch o.Op {
		case "addFile":
			if _, ok := out[o.File]; ok {
				return nil, nil, fail(errors.New("file exists"))
			}
			f, err := model.Open(o.File, []byte(o.Content))
			if err != nil {
				return nil, nil, fail(err)
			}
			out[o.File] = f
			changed[o.File] = true
			continue
		case "removeFile":
			if _, ok := out[o.File]; !ok {
				return nil, nil, fail(errors.New("no such file"))
			}
			delete(out, o.File)
			changed[o.File] = true
			continue
		case "renameFile":
			f, ok := out[o.File]
			if !ok {
				return nil, nil, fail(errors.New("no such file"))
			}
			if _, dup := out[o.NewFile]; dup {
				return nil, nil, fail(errors.New("target exists"))
			}
			nf, err := model.Open(o.NewFile, f.Source())
			if err != nil {
				return nil, nil, fail(err)
			}
			delete(out, o.File)
			out[o.NewFile] = nf
			changed[o.File], changed[o.NewFile] = true, true
			continue
		}
		f, ok := out[o.File]
		if !ok {
			return nil, nil, fail(errors.New("no such file " + o.File))
		}
		var nf *model.File
		var err error
		switch o.Op {
		case "setField":
			nf, err = f.SetField(model.ParsePath(o.Path), o.Value)
		case "removeField":
			nf, err = f.RemoveField(model.ParsePath(o.Path))
		case "addBlock":
			nf, err = f.AddBlock(model.ParsePath(o.Parent), o.Type, o.ID, o.Body)
		case "removeBlock":
			nf, err = f.RemoveBlock(model.ParsePath(o.Path))
		case "renameBlock":
			nf, err = f.RenameBlock(model.ParsePath(o.Path), o.NewID)
		case "moveBlock":
			nf, err = f.MoveBlock(model.ParsePath(o.Path), o.Index)
		default:
			err = errors.New("unknown op")
		}
		if err != nil {
			return nil, nil, fail(err)
		}
		out[o.File] = nf
		changed[o.File] = true
	}
	names := make([]string, 0, len(changed))
	for n := range changed {
		names = append(names, n)
	}
	sort.Strings(names)
	return out, names, nil
}

// ---- routes ----------------------------------------------------------------

func (s *server) routes(mux *http.ServeMux) {
	const p = "/api/v1"
	s.previewRoutes(mux)
	mux.HandleFunc("GET "+p+"/meta", s.auth("viewer", func(w http.ResponseWriter, r *http.Request, who identity) {
		s.mu.Lock()
		defer s.mu.Unlock()
		var active any
		if a, _ := s.mgr.Active(r.Context()); a != nil {
			active = a.ID
		}
		writeJSON(w, 200, map[string]any{"app": "starter", "version": "mock", "identity": who, "activeRevision": active,
			"features": map[string]bool{"preview": true, "pages": false}})
	}))
	mux.HandleFunc("GET "+p+"/schema/blocks", s.auth("viewer", func(w http.ResponseWriter, r *http.Request, _ identity) {
		writeJSON(w, 200, platform.BlockSchemas())
	}))
	mux.HandleFunc("GET "+p+"/schema/catalog", s.auth("viewer", func(w http.ResponseWriter, r *http.Request, _ identity) {
		writeJSON(w, 200, platform.NewRegistry().Catalog())
	}))

	mux.HandleFunc("GET "+p+"/drafts", s.auth("editor", func(w http.ResponseWriter, r *http.Request, who identity) {
		s.mu.Lock()
		defer s.mu.Unlock()
		out := []map[string]any{}
		for _, d := range s.drafts {
			if d.owner == who.Name || rank(who.Roles) >= 4 {
				out = append(out, d.summary())
			}
		}
		sort.Slice(out, func(i, j int) bool { return out[i]["createdAt"].(time.Time).After(out[j]["createdAt"].(time.Time)) })
		writeJSON(w, 200, out)
	}))
	mux.HandleFunc("POST "+p+"/drafts", s.auth("editor", s.createDraft))
	mux.HandleFunc("GET "+p+"/drafts/{id}", s.auth("editor", func(w http.ResponseWriter, r *http.Request, who identity) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if d := s.getDraft(w, r, who); d != nil {
			sum := d.summary()
			sum["diagnostics"] = d.diags
			writeJSON(w, 200, sum)
		}
	}))
	mux.HandleFunc("DELETE "+p+"/drafts/{id}", s.auth("editor", func(w http.ResponseWriter, r *http.Request, who identity) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if d := s.getDraft(w, r, who); d != nil {
			delete(s.drafts, d.id)
			s.log(who.Name, "draft.delete", d.id, "")
			w.WriteHeader(204)
		}
	}))
	mux.HandleFunc("GET "+p+"/drafts/{id}/files", s.auth("editor", func(w http.ResponseWriter, r *http.Request, who identity) {
		s.mu.Lock()
		defer s.mu.Unlock()
		d := s.getDraft(w, r, who)
		if d == nil {
			return
		}
		type fi struct {
			Path        string         `json:"path"`
			Size        int            `json:"size"`
			Diagnostics map[string]int `json:"diagnostics"`
		}
		out := []fi{}
		for n, f := range d.files {
			c := map[string]int{"errors": 0, "warnings": 0}
			for _, x := range d.diags {
				if x.File == n {
					if x.Severity == "error" {
						c["errors"]++
					} else {
						c["warnings"]++
					}
				}
			}
			out = append(out, fi{n, len(f.Text()), c})
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
		writeJSON(w, 200, out)
	}))
	mux.HandleFunc("GET "+p+"/drafts/{id}/files/{file}", s.auth("editor", func(w http.ResponseWriter, r *http.Request, who identity) {
		s.mu.Lock()
		defer s.mu.Unlock()
		d := s.getDraft(w, r, who)
		if d == nil {
			return
		}
		f, ok := d.files[r.PathValue("file")]
		if !ok {
			fail(w, 404, "not_found", "no such file", nil)
			return
		}
		writeJSON(w, 200, map[string]any{"path": f.Name(), "content": f.Text(), "version": d.version})
	}))
	mux.HandleFunc("PUT "+p+"/drafts/{id}/files/{file}", s.auth("editor", s.putFile))
	mux.HandleFunc("GET "+p+"/drafts/{id}/files/{file}/tree", s.auth("editor", func(w http.ResponseWriter, r *http.Request, who identity) {
		s.mu.Lock()
		defer s.mu.Unlock()
		d := s.getDraft(w, r, who)
		if d == nil {
			return
		}
		f, ok := d.files[r.PathValue("file")]
		if !ok {
			fail(w, 404, "not_found", "no such file", nil)
			return
		}
		writeJSON(w, 200, tree(f, true))
	}))
	mux.HandleFunc("GET "+p+"/drafts/{id}/tree", s.auth("editor", func(w http.ResponseWriter, r *http.Request, who identity) {
		s.mu.Lock()
		defer s.mu.Unlock()
		d := s.getDraft(w, r, who)
		if d == nil {
			return
		}
		out := map[string]any{}
		for n, f := range d.files {
			out[n] = tree(f, false)
		}
		writeJSON(w, 200, out)
	}))
	mux.HandleFunc("POST "+p+"/drafts/{id}/ops", s.auth("editor", s.postOps))
	mux.HandleFunc("POST "+p+"/drafts/{id}/undo", s.auth("editor", s.undoRedo(true)))
	mux.HandleFunc("POST "+p+"/drafts/{id}/redo", s.auth("editor", s.undoRedo(false)))
	mux.HandleFunc("POST "+p+"/drafts/{id}/format", s.auth("editor", s.format))
	mux.HandleFunc("POST "+p+"/drafts/{id}/validate", s.auth("editor", func(w http.ResponseWriter, r *http.Request, who identity) {
		s.mu.Lock()
		defer s.mu.Unlock()
		d := s.getDraft(w, r, who)
		if d == nil {
			return
		}
		rep := s.revalidate(d)
		d.broadcast("diagnostics", map[string]any{"diagnostics": d.diags})
		writeJSON(w, 200, map[string]any{"valid": rep.Valid, "diagnostics": d.diags, "summary": rep.Summary})
	}))
	mux.HandleFunc("GET "+p+"/drafts/{id}/diff", s.auth("editor", s.diff))
	mux.HandleFunc("GET "+p+"/drafts/{id}/events", s.auth("editor", s.events))
	mux.HandleFunc("POST "+p+"/drafts/{id}/propose", s.auth("editor", s.propose))

	mux.HandleFunc("GET "+p+"/revisions", s.auth("viewer", func(w http.ResponseWriter, r *http.Request, _ identity) {
		revs, err := s.mgr.Store.List(r.Context(), s.mgr.App, 100)
		if err != nil {
			fail(w, 500, "internal", err.Error(), nil)
			return
		}
		out := []any{}
		for _, rev := range revs {
			out = append(out, revSummary(rev))
		}
		writeJSON(w, 200, out)
	}))
	mux.HandleFunc("GET "+p+"/revisions/{id}", s.auth("viewer", func(w http.ResponseWriter, r *http.Request, _ identity) {
		rev, err := s.mgr.Store.Get(r.Context(), r.PathValue("id"))
		if err != nil {
			fail(w, 404, "not_found", err.Error(), nil)
			return
		}
		writeJSON(w, 200, rev)
	}))
	decide := func(min string, do func(ctx context.Context, id string, who identity, comment string) (*deploy.Revision, error), name string) http.HandlerFunc {
		return s.auth(min, func(w http.ResponseWriter, r *http.Request, who identity) {
			var in struct{ Comment string }
			_ = json.NewDecoder(r.Body).Decode(&in)
			s.mu.Lock()
			defer s.mu.Unlock()
			rev, err := do(r.Context(), r.PathValue("id"), who, in.Comment)
			if err != nil {
				code := 409
				switch {
				case errors.Is(err, deploy.ErrForbidden):
					code = 403
				case errors.Is(err, deploy.ErrNotFound):
					code = 404
				}
				fail(w, code, "revision_"+name, err.Error(), nil)
				return
			}
			s.log(who.Name, "revision."+name, rev.ID, in.Comment)
			writeJSON(w, 200, rev)
		})
	}
	mux.HandleFunc("POST "+p+"/revisions/{id}/approve", decide("reviewer", func(ctx context.Context, id string, who identity, c string) (*deploy.Revision, error) {
		return s.mgr.Approve(ctx, id, who.Name, c)
	}, "approve"))
	mux.HandleFunc("POST "+p+"/revisions/{id}/reject", decide("reviewer", func(ctx context.Context, id string, who identity, c string) (*deploy.Revision, error) {
		return s.mgr.Reject(ctx, id, who.Name, c)
	}, "reject"))
	mux.HandleFunc("POST "+p+"/revisions/{id}/activate", decide("reviewer", func(ctx context.Context, id string, who identity, _ string) (*deploy.Revision, error) {
		return s.mgr.Activate(ctx, id, who.Name)
	}, "activate"))
	mux.HandleFunc("POST "+p+"/rollback", s.auth("admin", func(w http.ResponseWriter, r *http.Request, who identity) {
		var in struct{ To, Reason string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		s.mu.Lock()
		defer s.mu.Unlock()
		rev, err := s.mgr.Rollback(r.Context(), in.To, who.Name, in.Reason)
		if err != nil {
			fail(w, 409, "rollback_failed", err.Error(), nil)
			return
		}
		s.log(who.Name, "revision.rollback", rev.ID, in.Reason)
		writeJSON(w, 200, rev)
	}))
	mux.HandleFunc("GET "+p+"/audit", s.auth("admin", func(w http.ResponseWriter, r *http.Request, _ identity) {
		s.mu.Lock()
		defer s.mu.Unlock()
		out := make([]auditEntry, len(s.audit))
		for i, e := range s.audit {
			out[len(s.audit)-1-i] = e
		}
		writeJSON(w, 200, out)
	}))
}

func revSummary(r *deploy.Revision) map[string]any {
	out := map[string]any{"id": r.ID, "seq": r.Seq, "status": r.Status, "author": r.Author, "message": r.Message,
		"created_at": r.CreatedAt, "activated_at": r.ActivatedAt, "approvals": r.Approvals, "changes": r.Changes,
		"checksum": r.Checksum, "failure": r.Failure, "base_id": r.BaseID}
	if r.IsBundle() {
		paths := make([]string, len(r.Files))
		for i, f := range r.Files {
			paths[i] = f.Path
		}
		out["files"] = paths
		out["changed_files"] = r.ChangedFiles
	}
	return out
}

func (s *server) createDraft(w http.ResponseWriter, r *http.Request, who identity) {
	var in struct{ Name, From string }
	_ = json.NewDecoder(r.Body).Decode(&in)
	s.mu.Lock()
	defer s.mu.Unlock()
	base := map[string]string{}
	baseRev := ""
	switch {
	case in.From == "dir":
		b, err := platform.ReadBundleDir(s.dir)
		if err != nil {
			fail(w, 422, "bad_source", err.Error(), nil)
			return
		}
		for _, f := range b {
			base[f.Path] = f.Content
		}
	default:
		var rev *deploy.Revision
		if id, ok := strings.CutPrefix(in.From, "revision:"); ok {
			rev, _ = s.mgr.Store.Get(r.Context(), id)
		} else {
			rev, _ = s.mgr.Active(r.Context())
		}
		if rev == nil {
			fail(w, 404, "not_found", "no such revision", nil)
			return
		}
		baseRev = rev.ID
		base = revFiles(rev)
		if len(base) == 0 {
			base["main.bcl"] = rev.Source
		}
	}
	d := &draft{id: newID("dr_"), owner: who.Name, name: in.Name, base: baseRev, baseFiles: base, created: time.Now().UTC(),
		updated: time.Now().UTC(), subs: map[chan []byte]struct{}{}}
	if d.name == "" {
		d.name = "Draft " + d.id[3:7]
	}
	if err := d.restore(base); err != nil {
		fail(w, 422, "bad_source", err.Error(), nil)
		return
	}
	s.revalidate(d)
	s.drafts[d.id] = d
	s.log(who.Name, "draft.create", d.id, in.From)
	writeJSON(w, 201, d.summary())
}

func (s *server) putFile(w http.ResponseWriter, r *http.Request, who identity) {
	var in struct {
		Content   string `json:"content"`
		IfVersion int64  `json:"ifVersion"`
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil {
		fail(w, 400, "bad_request", "invalid JSON", nil)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.getDraft(w, r, who)
	if d == nil {
		return
	}
	if in.IfVersion != d.version {
		fail(w, 409, "stale", "the draft changed", map[string]any{"version": d.version})
		return
	}
	name := r.PathValue("file")
	nf, err := model.Open(name, []byte(in.Content))
	if err != nil {
		fail(w, 422, "parse", err.Error(), map[string]any{"diagnostics": []diag{{Severity: "error", Message: err.Error(), File: name}}})
		return
	}
	prev := d.snapshot()
	d.files[name] = nf
	d.undo, d.redo = append(d.undo, prev), nil
	d.version++
	d.updated = time.Now().UTC()
	s.revalidate(d)
	d.broadcast("changed", map[string]any{"version": d.version, "changed": []string{name}})
	s.log(who.Name, "draft.putFile", d.id, name)
	writeJSON(w, 200, map[string]any{"version": d.version, "applied": 1, "diagnostics": d.diags, "changed": []string{name}})
}

func (s *server) postOps(w http.ResponseWriter, r *http.Request, who identity) {
	var in struct {
		Ops       []op  `json:"ops"`
		IfVersion int64 `json:"ifVersion"`
		DryRun    bool  `json:"dryRun"`
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil {
		fail(w, 400, "bad_request", "invalid JSON", nil)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.getDraft(w, r, who)
	if d == nil {
		return
	}
	if in.IfVersion != d.version {
		fail(w, 409, "stale", "the draft changed since version "+fmt.Sprint(in.IfVersion), map[string]any{"version": d.version})
		return
	}
	next, changed, err := applyOps(d.files, in.Ops)
	if err != nil {
		fail(w, 422, "op_failed", err.Error(), nil)
		return
	}
	if in.DryRun {
		writeJSON(w, 200, map[string]any{"version": d.version, "applied": len(in.Ops), "diagnostics": d.diags, "changed": changed})
		return
	}
	prev := d.snapshot()
	d.files = next
	d.undo, d.redo = append(d.undo, prev), nil
	s.commit(d, who, "draft.ops", fmt.Sprintf("%d ops", len(in.Ops)))
	writeJSON(w, 200, map[string]any{"version": d.version, "applied": len(in.Ops), "diagnostics": d.diags, "changed": changed})
}

func (s *server) commit(d *draft, who identity, action, detail string, changedOverride ...string) {
	d.version++
	d.updated = time.Now().UTC()
	s.revalidate(d)
	d.broadcast("changed", map[string]any{"version": d.version, "changed": changedOverride})
	d.broadcast("diagnostics", map[string]any{"diagnostics": d.diags})
	s.log(who.Name, action, d.id, detail)
}

func (s *server) undoRedo(undo bool) handler {
	return func(w http.ResponseWriter, r *http.Request, who identity) {
		s.mu.Lock()
		defer s.mu.Unlock()
		d := s.getDraft(w, r, who)
		if d == nil {
			return
		}
		from, to := &d.undo, &d.redo
		if !undo {
			from, to = &d.redo, &d.undo
		}
		if len(*from) == 0 {
			fail(w, 409, "nothing", "nothing to do", nil)
			return
		}
		cur := d.snapshot()
		target := (*from)[len(*from)-1]
		*from = (*from)[:len(*from)-1]
		if err := d.restore(target); err != nil {
			fail(w, 500, "internal", err.Error(), nil)
			return
		}
		*to = append(*to, cur)
		names := []string{}
		for n := range target {
			names = append(names, n)
		}
		sort.Strings(names)
		s.commit(d, who, map[bool]string{true: "draft.undo", false: "draft.redo"}[undo], "", names...)
		writeJSON(w, 200, map[string]any{"version": d.version, "applied": 1, "diagnostics": d.diags, "changed": names})
	}
}

func (s *server) format(w http.ResponseWriter, r *http.Request, who identity) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.getDraft(w, r, who)
	if d == nil {
		return
	}
	prev := d.snapshot()
	next := map[string]*model.File{}
	changed := []string{}
	for n, f := range d.files {
		nf, err := f.Reformat()
		if err != nil {
			fail(w, 422, "format", err.Error(), nil)
			return
		}
		if !nf.Equal(f) {
			changed = append(changed, n)
		}
		next[n] = nf
	}
	sort.Strings(changed)
	if len(changed) > 0 {
		d.files = next
		d.undo, d.redo = append(d.undo, prev), nil
		s.commit(d, who, "draft.format", "", changed...)
	}
	writeJSON(w, 200, map[string]any{"version": d.version, "applied": len(changed), "diagnostics": d.diags, "changed": changed})
}

// ---- diff, events, propose -------------------------------------------------

func (s *server) diff(w http.ResponseWriter, r *http.Request, who identity) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.getDraft(w, r, who)
	if d == nil {
		return
	}
	cur := d.snapshot()
	names := map[string]bool{}
	for n := range cur {
		names[n] = true
	}
	for n := range d.baseFiles {
		names[n] = true
	}
	list := []string{}
	for n := range names {
		list = append(list, n)
	}
	sort.Strings(list)
	type fd struct {
		Path    string `json:"path"`
		Status  string `json:"status"`
		Unified string `json:"unified,omitempty"`
	}
	files := []fd{}
	for _, n := range list {
		a, aok := d.baseFiles[n]
		b, bok := cur[n]
		switch {
		case aok && !bok:
			files = append(files, fd{n, "removed", unified(n, a, "")})
		case !aok && bok:
			files = append(files, fd{n, "added", unified(n, "", b)})
		case a != b:
			files = append(files, fd{n, "modified", unified(n, a, b)})
		}
	}
	changes := []platform.DocumentChange{}
	if bb, err := platform.NewBundle(toFiles(d.baseFiles)); err == nil {
		if cb, err := d.bundle(); err == nil {
			before := platform.ValidateBundle(r.Context(), bb, s.dir, s.opts)
			after := platform.ValidateBundle(r.Context(), cb, s.dir, s.opts)
			if before.Document != nil && after.Document != nil {
				changes = platform.DiffDocuments(before.Document, after.Document)
			}
		}
	}
	writeJSON(w, 200, map[string]any{"files": files, "changes": changes})
}

func toFiles(m map[string]string) []platform.BundleFile {
	out := []platform.BundleFile{}
	for n, c := range m {
		out = append(out, platform.BundleFile{Path: n, Content: c})
	}
	return out
}

// unified renders a whole-file diff with a simple LCS (fine for config files).
func unified(name, a, b string) string {
	al, bl := strings.Split(a, "\n"), strings.Split(b, "\n")
	if a == "" {
		al = nil
	}
	if b == "" {
		bl = nil
	}
	n, m := len(al), len(bl)
	dp := make([][]int, n+1)
	for i := range dp {
		dp[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if al[i] == bl[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else {
				dp[i][j] = max(dp[i+1][j], dp[i][j+1])
			}
		}
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "--- a/%s\n+++ b/%s\n", name, name)
	i, j := 0, 0
	for i < n || j < m {
		switch {
		case i < n && j < m && al[i] == bl[j]:
			sb.WriteString(" " + al[i] + "\n")
			i++
			j++
		case j < m && (i == n || dp[i][j+1] >= dp[i+1][j]):
			sb.WriteString("+" + bl[j] + "\n")
			j++
		default:
			sb.WriteString("-" + al[i] + "\n")
			i++
		}
	}
	return sb.String()
}

func (s *server) events(w http.ResponseWriter, r *http.Request, who identity) {
	s.mu.Lock()
	d := s.getDraft(w, r, who)
	if d == nil {
		s.mu.Unlock()
		return
	}
	ch := make(chan []byte, 16)
	d.subs[ch] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(d.subs, ch)
		s.mu.Unlock()
	}()
	fl, ok := w.(http.Flusher)
	if !ok {
		fail(w, 500, "internal", "no streaming", nil)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write([]byte(": connected\n\n"))
	fl.Flush()
	tick := time.NewTicker(20 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case m := <-ch:
			_, _ = w.Write(m)
			fl.Flush()
		case <-tick.C:
			_, _ = w.Write([]byte(": ping\n\n"))
			fl.Flush()
		}
	}
}

func (s *server) propose(w http.ResponseWriter, r *http.Request, who identity) {
	var in struct{ Message string }
	_ = json.NewDecoder(r.Body).Decode(&in)
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.getDraft(w, r, who)
	if d == nil {
		return
	}
	b, err := d.bundle()
	if err != nil {
		fail(w, 422, "invalid", err.Error(), nil)
		return
	}
	rev, err := s.mgr.ProposeBundle(r.Context(), b, who.Name, in.Message)
	if err != nil {
		var inv *deploy.InvalidError
		if errors.As(err, &inv) {
			fail(w, 422, "invalid", err.Error(), map[string]any{"diagnostics": toDiags(inv.Report)})
			return
		}
		fail(w, 409, "propose_failed", err.Error(), nil)
		return
	}
	s.log(who.Name, "revision.propose", rev.ID, in.Message)
	writeJSON(w, 201, rev)
}

var _ = filepath.Join
var _ = os.Stdout
