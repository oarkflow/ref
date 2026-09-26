package deploy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
)

// Admin is the HTTP API for the revision workflow:
//
//	GET  /revisions                  list (newest first, without sources)
//	GET  /revisions/{id}             one revision (with its source)
//	GET  /active                     the active revision
//	POST /validate    {source}       static validation report
//	POST /revisions   {source, message}  propose
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

func (a *Admin) list(w http.ResponseWriter, r *http.Request, _ string) {
	revs, err := a.Manager.Store.List(r.Context(), a.Manager.App, 100)
	if err != nil {
		a.fail(w, err)
		return
	}
	out := make([]any, len(revs))
	for i, rev := range revs {
		out[i] = summary(rev)
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
	out := summary(rev)
	if a.Supervisor != nil {
		if cur := a.Supervisor.Current(); cur != nil {
			out["serving"] = cur.ID
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *Admin) validate(w http.ResponseWriter, r *http.Request, _ string) {
	var in struct {
		Source string `json:"source"`
	}
	if !a.body(w, r, &in) {
		return
	}
	writeJSON(w, http.StatusOK, a.Manager.Validate(r.Context(), []byte(in.Source)))
}

func (a *Admin) propose(w http.ResponseWriter, r *http.Request, user string) {
	var in struct {
		Source  string `json:"source"`
		Message string `json:"message"`
	}
	if !a.body(w, r, &in) {
		return
	}
	rev, err := a.Manager.Propose(r.Context(), []byte(in.Source), user, in.Message)
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, summary(rev))
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
	writeJSON(w, http.StatusOK, summary(rev))
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
	writeJSON(w, http.StatusOK, summary(rev))
}
