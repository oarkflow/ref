package deploy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/oarkflow/ref/platform"
)

// Admin is the HTTP API for the revision workflow:
//
//	GET  /revisions                  list (newest first, without sources;
//	                                 a bundle revision lists its file paths
//	                                 and which files changed)
//	GET  /revisions/{id}             one revision (with its source, and its
//	                                 files as [{path, content}] for a bundle)
//	GET  /active                     the active revision
//	POST /validate    {source} | {files[, assets]}          static validation report
//	POST /revisions   {source | files[, assets], message}   propose
//
// A document is sent as "source" (one string) or as "files" (an object of
// relative path to content, each path ending in .bcl) — exactly one. A files
// proposal becomes a bundle revision; diagnostics name the file and line.
// A files proposal may also send "assets", an object of path to content for
// page templates and static files ("templates/pages/x.html",
// "static/css/x.css"): they override the host's own files of the same path,
// and a listing shows "assets" and "changed_assets" next to the files.
//
//	POST /revisions/{id}/approve  {comment}
//	POST /revisions/{id}/reject   {comment}
//	POST /revisions/{id}/activate
//	POST /rollback    {to?, reason}
//
// Every request needs "Authorization: Bearer <token>"; the token identifies
// the reviewer, so "approved by someone other than the author" holds per
// person. Mount it on an internal port.
type Admin struct {
	Manager    *Manager
	Supervisor *Supervisor // optional: told to swap right after activation
	// Tokens maps each admin token to the person it identifies.
	Tokens map[string]string
	// MaxSource bounds a proposed document (default 4 MiB).
	MaxSource int64

	users map[string]string // sha256(token) -> user
}

func tokenKey(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// Handler returns the API.
func (a *Admin) Handler() http.Handler {
	a.users = make(map[string]string, len(a.Tokens))
	for token, user := range a.Tokens {
		if len(token) >= 16 && user != "" {
			a.users[tokenKey(token)] = user
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /revisions", a.auth(a.list))
	mux.HandleFunc("GET /revisions/{id}", a.auth(a.get))
	mux.HandleFunc("GET /active", a.auth(a.active))
	mux.HandleFunc("POST /validate", a.auth(a.validate))
	mux.HandleFunc("POST /revisions", a.auth(a.propose))
	mux.HandleFunc("POST /revisions/{id}/{op}", a.auth(a.decide))
	mux.HandleFunc("POST /rollback", a.auth(a.rollback))
	return mux
}

type userKey struct{}

func (a *Admin) auth(next func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		// Looking the token up by its hash keeps the comparison free of
		// timing that depends on how much of a token matched.
		user := ""
		if ok {
			user = a.users[tokenKey(strings.TrimSpace(token))]
		}
		if user == "" {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "a valid admin token is required"})
			return
		}
		next(w, r, user)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (a *Admin) fail(w http.ResponseWriter, err error) {
	var inv *InvalidError
	switch {
	case errors.As(err, &inv):
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "the document is invalid", "report": inv.Report})
	case errors.Is(err, ErrInvalid):
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": err.Error()})
	case errors.Is(err, ErrNotFound):
		writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
	case errors.Is(err, ErrForbidden):
		writeJSON(w, http.StatusForbidden, map[string]any{"error": err.Error()})
	case errors.Is(err, ErrState), errors.Is(err, ErrTampered):
		writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error()})
	default:
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
	}
}

func (a *Admin) body(w http.ResponseWriter, r *http.Request, into any) bool {
	limit := a.MaxSource
	if limit <= 0 {
		limit = 4 << 20
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if err == nil && len(raw) > 0 {
		err = json.Unmarshal(raw, into)
	}
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{"error": fmt.Sprintf("the request body exceeds %d bytes", limit)})
			return false
		}
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON body: " + err.Error()})
		return false
	}
	return true
}

func summary(r *Revision) map[string]any {
	return map[string]any{"id": r.ID, "seq": r.Seq, "status": r.Status, "author": r.Author, "message": r.Message,
		"created_at": r.CreatedAt, "activated_at": r.ActivatedAt, "approvals": r.Approvals, "changes": r.Changes,
		"checksum": r.Checksum, "failure": r.Failure}
}

// summaryWithFiles adds what a bundle revision knows about its files: their
// paths, and which of them differ from the revision it was proposed against.
func summaryWithFiles(r *Revision) map[string]any {
	out := summary(r)
	if r.IsBundle() {
		paths := make([]string, len(r.Files))
		for i, f := range r.Files {
			paths[i] = f.Path
		}
		out["files"] = paths
		out["changed_files"] = r.ChangedFiles
		if r.HasAssets() {
			apaths := make([]string, len(r.Assets))
			for i, f := range r.Assets {
				apaths[i] = f.Path
			}
			out["assets"] = apaths
			out["changed_assets"] = r.ChangedAssets
		}
	}
	return out
}

func (a *Admin) list(w http.ResponseWriter, r *http.Request, _ string) {
	revs, err := a.Manager.Store.List(r.Context(), a.Manager.App, 100)
	if err != nil {
		a.fail(w, err)
		return
	}
	out := make([]any, len(revs))
	for i, rev := range revs {
		out[i] = summaryWithFiles(rev)
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *Admin) get(w http.ResponseWriter, r *http.Request, _ string) {
	rev, err := a.Manager.load(r.Context(), r.PathValue("id"))
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rev)
}

func (a *Admin) active(w http.ResponseWriter, r *http.Request, _ string) {
	rev, err := a.Manager.Active(r.Context())
	if err != nil {
		a.fail(w, err)
		return
	}
	if rev == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "no active revision"})
		return
	}
	out := summaryWithFiles(rev)
	if a.Supervisor != nil {
		if cur := a.Supervisor.Current(); cur != nil {
			out["serving"] = cur.ID
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// document is a proposed document: one "source" string, or a "files" object
// of path to content — exactly one of the two.
type document struct {
	Source  *string           `json:"source"`
	Files   map[string]string `json:"files"`
	Assets  map[string]string `json:"assets"`
	Message string            `json:"message"`
}

// bundle turns the request's files (and assets) into a validated Bundle.
// isBundle is false when the request is a plain source document; done is true
// when the request was malformed and the response has been written.
func (a *Admin) bundle(w http.ResponseWriter, in document) (b platform.Bundle, assets platform.Assets, isBundle, done bool) {
	switch {
	case in.Source != nil && in.Files != nil:
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": `send either "source" or "files", not both`})
		return nil, nil, false, true
	case in.Source == nil && in.Files == nil:
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": `send "source" (one document) or "files" (path to content)`})
		return nil, nil, false, true
	case in.Source != nil && len(in.Assets) > 0:
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": `"assets" go with "files", not "source"`})
		return nil, nil, false, true
	case in.Source != nil:
		return nil, nil, false, false
	}
	limit := a.MaxSource
	if limit <= 0 {
		limit = 4 << 20
	}
	files := make([]platform.BundleFile, 0, len(in.Files))
	var total int64
	for p, c := range in.Files {
		total += int64(len(c))
		files = append(files, platform.BundleFile{Path: p, Content: c})
	}
	extra := make([]platform.BundleFile, 0, len(in.Assets))
	for p, c := range in.Assets {
		total += int64(len(c))
		extra = append(extra, platform.BundleFile{Path: p, Content: c})
	}
	if total > limit {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{"error": fmt.Sprintf("the files and assets total %d bytes; the limit is %d", total, limit)})
		return nil, nil, false, true
	}
	b, err := platform.NewBundle(files)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": err.Error()})
		return nil, nil, false, true
	}
	assets, err = platform.NewAssets(extra)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": err.Error()})
		return nil, nil, false, true
	}
	return b, assets, true, false
}

func (a *Admin) validate(w http.ResponseWriter, r *http.Request, _ string) {
	var in document
	if !a.body(w, r, &in) {
		return
	}
	b, _, isBundle, done := a.bundle(w, in)
	if done {
		return
	}
	if isBundle {
		writeJSON(w, http.StatusOK, a.Manager.ValidateFiles(r.Context(), b))
		return
	}
	writeJSON(w, http.StatusOK, a.Manager.Validate(r.Context(), []byte(*in.Source)))
}

func (a *Admin) propose(w http.ResponseWriter, r *http.Request, user string) {
	var in document
	if !a.body(w, r, &in) {
		return
	}
	b, assets, isBundle, done := a.bundle(w, in)
	if done {
		return
	}
	var (
		rev *Revision
		err error
	)
	if isBundle {
		rev, err = a.Manager.ProposeBundleAssets(r.Context(), b, assets, user, in.Message)
	} else {
		rev, err = a.Manager.Propose(r.Context(), []byte(*in.Source), user, in.Message)
	}
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, summaryWithFiles(rev))
}

func (a *Admin) decide(w http.ResponseWriter, r *http.Request, user string) {
	var in struct {
		Comment string `json:"comment"`
	}
	if !a.body(w, r, &in) {
		return
	}
	var (
		rev *Revision
		err error
	)
	switch r.PathValue("op") {
	case "approve":
		rev, err = a.Manager.Approve(r.Context(), r.PathValue("id"), user, in.Comment)
	case "reject":
		rev, err = a.Manager.Reject(r.Context(), r.PathValue("id"), user, in.Comment)
	case "activate":
		rev, err = a.Manager.Activate(r.Context(), r.PathValue("id"), user)
		if err == nil && a.Supervisor != nil {
			a.Supervisor.Notify()
		}
	default:
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "unknown operation"})
		return
	}
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, summaryWithFiles(rev))
}

func (a *Admin) rollback(w http.ResponseWriter, r *http.Request, user string) {
	var in struct {
		To     string `json:"to"`
		Reason string `json:"reason"`
	}
	if !a.body(w, r, &in) {
		return
	}
	rev, err := a.Manager.Rollback(r.Context(), in.To, user, in.Reason)
	if err != nil {
		a.fail(w, err)
		return
	}
	if a.Supervisor != nil {
		a.Supervisor.Notify()
	}
	writeJSON(w, http.StatusOK, summaryWithFiles(rev))
}
