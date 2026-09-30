// Package server is the Studio HTTP API (docs/studio-api.md): drafts of an
// application's BCL bundle edited through studio/model, validation, diffs,
// and the deploy revision workflow, plus the embedded web app.
//
// Mount Server.Handler under a prefix with http.StripPrefix and set
// Config.BasePath to that prefix so URLs in responses (the preview URL, the
// preview cookie path) are right.
package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/oarkflow/ref/deploy"
	"github.com/oarkflow/ref/platform"
	"github.com/oarkflow/ref/studio"
	"github.com/oarkflow/ref/studio/preview"
)

// Config configures a Server.
type Config struct {
	// App names the application (shown by /meta).
	App string
	// Manager runs the revision workflow: propose, approve, activate,
	// rollback, and validation with the deployment's options. Without it the
	// revision endpoints answer 501 and drafts can only start from ConfigDir.
	Manager *deploy.Manager
	// Tokens maps a bearer token (at least 16 characters) to its identity.
	Tokens map[string]studio.Identity
	// ConfigDir is the directory a draft with from "dir" starts from.
	ConfigDir string
	// BaseDir is the base directory for validating imports (default ConfigDir).
	BaseDir string
	// LoadOptions are the options used to validate when Manager is nil.
	LoadOptions platform.LoadOptions
	// Registry supplies the catalog (default platform.NewRegistry()).
	Registry *platform.Registry
	// Resources is the host's resources directory as a file system: the
	// tree that holds templates/ and static/. Assets in a draft override its
	// files, the page tools list and analyse its templates, and
	// import-from-disk copies from it. Nil means drafts see only their own
	// assets.
	Resources fs.FS
	// ResourcesDir is Resources as a directory path (used when Resources is nil).
	ResourcesDir string
	// TemplateGlobals names variables the host supplies to every template
	// (beyond the built-in list), so the linkage check does not ask the
	// route's intent for them.
	TemplateGlobals []string
	// DocSourceDirs are Go source directories ("platform", "pipeline" in a
	// checkout) whose doc comments fill the block schemas' Doc fields.
	DocSourceDirs []string
	// Store holds drafts (default: in memory).
	Store Store
	// Preview builds preview generations; nil disables the preview endpoints
	// (unless PreviewOptions is set). A ready Manager is used as it is: build
	// it with Options.Prefix = BasePath + "/preview/" if the handler is
	// mounted under a prefix, and the server passes it the full request path.
	Preview studio.PreviewManager
	// PreviewOptions makes the server build (and own, and close) a
	// studio/preview service. Its Prefix defaults to BasePath + "/preview/".
	// Ignored when Preview is set.
	PreviewOptions *preview.Options
	// AuditStore keeps the audit log durably (the in-memory ring still
	// serves reads when it is nil or fails). See OpenSQL.
	AuditStore AuditStore
	// Comments keeps revision comments (default: in memory). See OpenSQL.
	Comments CommentStore
	// RateLimit limits mutating requests per identity (see RateLimit).
	RateLimit RateLimit
	// AllowedOrigins lists the browser origins allowed to call the API
	// cross-origin ("*" allows any). Default: none, same-origin only.
	AllowedOrigins []string
	// OnActivate is called after a revision becomes active (activate and
	// rollback), e.g. to tell a deploy.Supervisor to swap.
	OnActivate func(*deploy.Revision)
	// Audit is called for every audit entry, in addition to the in-memory ring.
	Audit func(AuditEntry)
	// AuditSize is how many entries the ring keeps (default 1000).
	AuditSize int
	// BasePath is the prefix the handler is mounted under (e.g. "/studio").
	BasePath string
	// Web is the web app (default studio.WebAssets()). Nil serves the embedded one.
	Web fs.FS
	// MaxBody bounds a request body that can carry file contents: ops and
	// file replacement (default 24 MiB).
	MaxBody int64
	// MaxSmallBody bounds every other request body (default 1 MiB).
	MaxSmallBody int64
	// MaxOps bounds the ops in one batch (default 1000).
	MaxOps int
	// MaxCommentsPerRevision bounds a revision's thread (default 500).
	MaxCommentsPerRevision int
	// MaxDraftsPerOwner bounds an editor's drafts (default 50).
	MaxDraftsPerOwner int
	// Now is the clock (default time.Now).
	Now func() time.Time
	// Logf logs internal errors (default: discard).
	Logf func(format string, args ...any)
}

// Server is the Studio API.
type Server struct {
	cfg      Config
	users    map[string]studio.Identity // sha256(token) -> identity
	store    Store
	audit    *auditLog
	registry *platform.Registry
	web      fs.FS
	mux      *http.ServeMux
	comments CommentStore
	limiter  *limiter

	// done is closed by Close; event streams end when it is.
	done      chan struct{}
	closeOnce sync.Once
	wg        sync.WaitGroup
	// ownedPreview is the preview service the server built itself.
	ownedPreview *preview.Service
	// forwarding is true when the preview manager's status events are
	// relayed to draft event streams.
	forwarding bool
	// restoreBase re-adds BasePath to a preview request's path before it
	// reaches an owned preview service (the mount stripped it).
	restoreBase bool

	pmu       sync.Mutex
	psessions map[string]previewSession // cookie value -> session

	// flows caches each draft's journey graph for its current version.
	flows flowCache
}

// New builds a Server from cfg.
func New(cfg Config) (*Server, error) {
	if len(cfg.Tokens) == 0 {
		return nil, errors.New("studio/server: at least one token is required")
	}
	s := &Server{cfg: cfg, users: make(map[string]studio.Identity, len(cfg.Tokens)), psessions: map[string]previewSession{},
		done: make(chan struct{})}
	for token, id := range cfg.Tokens {
		switch {
		case len(token) < 16:
			return nil, errors.New("studio/server: a token must be at least 16 characters")
		case strings.TrimSpace(id.Name) == "":
			return nil, errors.New("studio/server: a token's identity needs a name")
		case len(id.Roles) == 0:
			return nil, fmt.Errorf("studio/server: identity %q has no roles", id.Name)
		}
		for _, r := range id.Roles {
			if !studio.ValidRole(studio.Role(r)) {
				return nil, fmt.Errorf("studio/server: identity %q has unknown role %q", id.Name, r)
			}
		}
		s.users[tokenKey(token)] = id
	}
	if s.cfg.BaseDir == "" {
		s.cfg.BaseDir = cfg.ConfigDir
	}
	if s.cfg.Resources == nil && s.cfg.ResourcesDir != "" {
		s.cfg.Resources = os.DirFS(s.cfg.ResourcesDir)
	}
	s.cfg.BasePath = strings.TrimRight(cfg.BasePath, "/")
	if s.cfg.MaxBody <= 0 {
		s.cfg.MaxBody = 24 << 20
	}
	if s.cfg.MaxSmallBody <= 0 {
		s.cfg.MaxSmallBody = 1 << 20
	}
	if s.cfg.MaxOps <= 0 {
		s.cfg.MaxOps = 1000
	}
	if s.cfg.MaxCommentsPerRevision <= 0 {
		s.cfg.MaxCommentsPerRevision = 500
	}
	if s.cfg.MaxDraftsPerOwner <= 0 {
		s.cfg.MaxDraftsPerOwner = 50
	}
	if s.cfg.Now == nil {
		s.cfg.Now = time.Now
	}
	if s.cfg.Logf == nil {
		s.cfg.Logf = func(string, ...any) {}
	}
	s.registry = cfg.Registry
	if s.registry == nil {
		s.registry = platform.NewRegistry()
	}
	s.store = cfg.Store
	if s.store == nil {
		s.store = NewMemoryStore()
	}
	s.web = cfg.Web
	if s.web == nil {
		s.web = studio.WebAssets()
	}
	s.audit = newAuditLog(cfg.AuditSize, cfg.Audit, cfg.AuditStore, s.cfg.Logf)
	s.comments = cfg.Comments
	if s.comments == nil {
		s.comments = NewMemoryComments()
	}
	s.limiter = newLimiter(cfg.RateLimit, s.cfg.Now)
	if len(cfg.DocSourceDirs) > 0 {
		attachDocs(cfg.DocSourceDirs)
	}
	if s.cfg.Preview == nil && cfg.PreviewOptions != nil {
		opts := *cfg.PreviewOptions
		if opts.Prefix == "" {
			opts.Prefix = s.cfg.BasePath + "/preview/"
			s.restoreBase = s.cfg.BasePath != ""
		}
		if opts.Logf == nil {
			opts.Logf = s.cfg.Logf
		}
		s.ownedPreview = preview.New(opts)
		s.cfg.Preview = s.ownedPreview
	}
	if ev, ok := s.cfg.Preview.(interface {
		Events() (<-chan preview.Status, func())
	}); ok {
		s.forwardPreviewEvents(ev)
	}
	s.routes()
	return s, nil
}

// forwardPreviewEvents relays the preview manager's status changes to the
// event stream of the draft they are about.
func (s *Server) forwardPreviewEvents(ev interface {
	Events() (<-chan preview.Status, func())
}) {
	ch, cancel := ev.Events()
	s.forwarding = true
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer cancel()
		for {
			select {
			case st, ok := <-ch:
				if !ok {
					return
				}
				s.Notify(st.ID, "preview", st.Studio())
			case <-s.done:
				return
			}
		}
	}()
}

// Close ends every event stream, stops relaying preview events and closes the
// preview service the server built (a Manager passed in Config.Preview is the
// caller's to close). It is safe to call more than once. Call it before
// http.Server.Shutdown, which would otherwise wait for the open event streams.
func (s *Server) Close() error {
	s.closeOnce.Do(func() {
		close(s.done)
		s.wg.Wait()
		if s.ownedPreview != nil {
			s.ownedPreview.Close()
		}
	})
	return nil
}

// Handler is the API and the web app.
func (s *Server) Handler() http.Handler { return s }

// ServeHTTP implements http.Handler. Every response carries an X-Request-Id
// (the client's, if it sent a sane one), and API requests get the CORS policy.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	id := r.Header.Get(requestIDHeader)
	if !validRequestID(id) {
		id = newRequestID()
	}
	w.Header().Set(requestIDHeader, id)
	if s.cors(w, r) {
		return
	}
	s.mux.ServeHTTP(w, r)
}

// Get implements studio.DraftSource.
func (s *Server) Get(id string) (studio.Draft, bool) {
	d, ok := s.store.Get(id)
	if !ok {
		return nil, false
	}
	return d, true
}

// Notify publishes a custom event (e.g. "preview") to a draft's event stream.
func (s *Server) Notify(draftID, event string, data any) {
	if d, ok := s.store.Get(draftID); ok {
		d.publish(event, data)
	}
}

func tokenKey(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// ---------------------------------------------------------------------------
// Errors
// ---------------------------------------------------------------------------

// apiError is an error with an HTTP status and a stable code.
type apiError struct {
	Status  int
	Code    string
	Message string
	Details any
}

func (e *apiError) Error() string { return e.Message }

func errf(status int, code, format string, args ...any) *apiError {
	return &apiError{Status: status, Code: code, Message: fmt.Sprintf(format, args...)}
}

func (e *apiError) with(details any) *apiError { e.Details = details; return e }

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) writeErr(w http.ResponseWriter, err error) {
	var ae *apiError
	if !errors.As(err, &ae) {
		ae = s.mapDeployErr(err)
	}
	rid := w.Header().Get(requestIDHeader)
	if ae.Status >= 500 {
		s.cfg.Logf("studio: %s: internal error: %v", rid, err)
	}
	body := map[string]any{"code": ae.Code, "message": ae.Message}
	if ae.Details != nil {
		body["details"] = ae.Details
	}
	if rid != "" {
		body["requestId"] = rid
	}
	writeJSON(w, ae.Status, map[string]any{"error": body})
}

// mapDeployErr turns deploy and platform errors into API errors; anything
// unrecognised is logged and reported as a plain 500.
func (s *Server) mapDeployErr(err error) *apiError {
	var inv *deploy.InvalidError
	switch {
	case errors.As(err, &inv):
		return errf(http.StatusUnprocessableEntity, "invalid", "the document is invalid").with(map[string]any{
			"diagnostics": studio.FromPlatform(inv.Report.Diagnostics),
			"errors":      inv.Report.Errors,
			"warnings":    inv.Report.Warnings,
		})
	case errors.Is(err, deploy.ErrInvalid):
		return errf(http.StatusUnprocessableEntity, "invalid", "%s", err.Error())
	case errors.Is(err, deploy.ErrNotFound):
		return errf(http.StatusNotFound, "not_found", "%s", err.Error())
	case errors.Is(err, deploy.ErrForbidden):
		return errf(http.StatusForbidden, "forbidden", "%s", err.Error())
	case errors.Is(err, deploy.ErrState), errors.Is(err, deploy.ErrTampered):
		return errf(http.StatusConflict, "conflict", "%s", err.Error())
	}
	return errf(http.StatusInternalServerError, "internal", "internal error")
}

// ---------------------------------------------------------------------------
// Requests
// ---------------------------------------------------------------------------

// call is one authenticated request.
type call struct {
	w  http.ResponseWriter
	r  *http.Request
	id studio.Identity
	// limit bounds the request body; it starts at Config.MaxSmallBody and a
	// handler that takes file contents raises it to Config.MaxBody.
	limit int64
}

type handlerFunc func(c *call) error

// route registers pattern behind authentication and a minimum role.
func (s *Server) route(pattern string, min studio.Role, h handlerFunc) {
	s.mux.HandleFunc(pattern, s.wrap(min, false, h))
}

func (s *Server) wrap(min studio.Role, queryToken bool, h handlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := s.authenticate(r, queryToken)
		if !ok {
			w.Header().Set("WWW-Authenticate", `Bearer realm="studio"`)
			s.writeErr(w, errf(http.StatusUnauthorized, "unauthorized", "a valid token is required"))
			return
		}
		if !id.Has(min) {
			s.writeErr(w, errf(http.StatusForbidden, "forbidden", "this needs the %s role", min))
			return
		}
		if isMutating(r.Method) {
			if r.ContentLength > s.cfg.MaxBody {
				s.writeErr(w, errf(http.StatusRequestEntityTooLarge, "too_large", "the request body exceeds %d bytes", s.cfg.MaxBody))
				return
			}
			if ok, retry := s.limiter.allow(id.Name); !ok {
				w.Header().Set("Retry-After", retryAfterSeconds(retry))
				s.writeErr(w, errf(http.StatusTooManyRequests, "rate_limited", "too many requests; retry in %s", retry.Round(time.Millisecond)))
				return
			}
		}
		if err := h(&call{w: w, r: r, id: id, limit: s.cfg.MaxSmallBody}); err != nil {
			s.writeErr(w, err)
		}
	}
}

// authenticate resolves the bearer token. Browsers' EventSource cannot set
// headers, so the event stream (queryToken) also accepts ?access_token=.
func (s *Server) authenticate(r *http.Request, queryToken bool) (studio.Identity, bool) {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok && queryToken {
		token, ok = r.URL.Query().Get("access_token"), true
	}
	if !ok {
		return studio.Identity{}, false
	}
	// Looking the token up by its hash keeps comparison timing independent
	// of how much of a token matched.
	id, ok := s.users[tokenKey(strings.TrimSpace(token))]
	return id, ok
}

// decode reads the JSON body into v. An empty body leaves v untouched.
func (c *call) decode(s *Server, v any) error {
	raw, err := io.ReadAll(http.MaxBytesReader(c.w, c.r.Body, c.limit))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			return errf(http.StatusRequestEntityTooLarge, "too_large", "the request body exceeds %d bytes", c.limit)
		}
		return errf(http.StatusBadRequest, "bad_request", "read body: %v", err)
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return errf(http.StatusBadRequest, "bad_request", "invalid JSON: %v", err)
	}
	return nil
}

func (c *call) json(status int, v any) error {
	writeJSON(c.w, status, v)
	return nil
}

// ---------------------------------------------------------------------------
// Audit
// ---------------------------------------------------------------------------

// AuditEntry is one recorded mutation. ID increases with time and is the
// cursor for GET /audit?before=.
type AuditEntry struct {
	ID     int64     `json:"id"`
	At     time.Time `json:"at"`
	Who    string    `json:"who"`
	Action string    `json:"action"`
	Target string    `json:"target,omitempty"`
	Detail string    `json:"detail,omitempty"`
}

type auditLog struct {
	mu     sync.Mutex
	size   int
	ring   []AuditEntry
	lastID int64
	hook   func(AuditEntry)
	store  AuditStore
	logf   func(string, ...any)
}

func newAuditLog(size int, hook func(AuditEntry), store AuditStore, logf func(string, ...any)) *auditLog {
	if size <= 0 {
		size = 1000
	}
	return &auditLog{size: size, hook: hook, store: store, logf: logf}
}

func (a *auditLog) add(e AuditEntry) {
	a.mu.Lock()
	// IDs are nanosecond timestamps, forced to increase, so they order
	// entries across restarts without a sequence in the database.
	id := e.At.UnixNano()
	if id <= a.lastID {
		id = a.lastID + 1
	}
	a.lastID = id
	e.ID = id
	a.ring = append(a.ring, e)
	if len(a.ring) > a.size {
		a.ring = append(a.ring[:0], a.ring[len(a.ring)-a.size:]...)
	}
	hook, store := a.hook, a.store
	a.mu.Unlock()
	if store != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := store.AppendAudit(ctx, e); err != nil {
			a.logf("studio: audit store: %v", err)
		}
		cancel()
	}
	if hook != nil {
		hook(e)
	}
}

// list returns up to limit entries with an ID below before (all if 0), newest
// first: from the store when there is one, else (or if it fails) the ring.
func (a *auditLog) list(ctx context.Context, before int64, limit int) ([]AuditEntry, error) {
	if a.store != nil {
		out, err := a.store.ListAudit(ctx, before, limit)
		if err == nil {
			return out, nil
		}
		a.logf("studio: audit store: %v", err)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]AuditEntry, 0, min(limit, len(a.ring)))
	for i := len(a.ring) - 1; i >= 0 && len(out) < limit; i-- {
		if before > 0 && a.ring[i].ID >= before {
			continue
		}
		out = append(out, a.ring[i])
	}
	return out, nil
}

// record writes an audit entry for c's identity.
func (s *Server) record(c *call, action, target, detail string) {
	s.audit.add(AuditEntry{At: s.cfg.Now().UTC(), Who: c.id.Name, Action: action, Target: target, Detail: detail})
}

// ---------------------------------------------------------------------------
// Routes
// ---------------------------------------------------------------------------

func (s *Server) routes() {
	s.mux = http.NewServeMux()
	const api = "/api/v1"
	v, e, rv, ad := studio.RoleViewer, studio.RoleEditor, studio.RoleReviewer, studio.RoleAdmin

	s.route("GET "+api+"/meta", v, s.meta)
	s.route("GET "+api+"/schema/blocks", v, s.schemaBlocks)
	s.route("GET "+api+"/schema/catalog", v, s.schemaCatalog)

	s.route("GET "+api+"/drafts", e, s.listDrafts)
	s.route("POST "+api+"/drafts", e, s.createDraft)
	s.route("GET "+api+"/drafts/{id}", e, s.getDraft)
	s.route("DELETE "+api+"/drafts/{id}", e, s.deleteDraft)
	s.route("GET "+api+"/drafts/{id}/files", e, s.listFiles)
	s.route("GET "+api+"/drafts/{id}/files/{file}", e, s.getFile)
	s.route("PUT "+api+"/drafts/{id}/files/{file}", e, s.putFile)
	s.route("GET "+api+"/drafts/{id}/files/{file}/tree", e, s.fileTree)
	s.route("GET "+api+"/drafts/{id}/tree", e, s.draftTree)
	s.route("POST "+api+"/drafts/{id}/ops", e, s.applyOps)
	s.route("POST "+api+"/drafts/{id}/undo", e, s.undo)
	s.route("POST "+api+"/drafts/{id}/redo", e, s.redo)
	s.route("POST "+api+"/drafts/{id}/format", e, s.format)
	s.route("POST "+api+"/drafts/{id}/validate", e, s.validateDraft)
	s.route("GET "+api+"/drafts/{id}/diff", e, s.diff)
	s.mux.HandleFunc("GET "+api+"/drafts/{id}/events", s.wrap(e, true, s.events))
	s.route("POST "+api+"/drafts/{id}/propose", e, s.propose)

	s.route("GET "+api+"/drafts/{id}/assets", e, s.listAssets)
	s.route("POST "+api+"/drafts/{id}/assets/rename", e, s.renameAsset)
	s.route("POST "+api+"/drafts/{id}/assets/import-from-disk", e, s.importFromDisk)
	s.route("GET "+api+"/drafts/{id}/assets/{path...}", e, s.getAsset)
	s.route("PUT "+api+"/drafts/{id}/assets/{path...}", e, s.putAsset)
	s.route("DELETE "+api+"/drafts/{id}/assets/{path...}", e, s.deleteAsset)
	s.route("GET "+api+"/drafts/{id}/templates", e, s.listTemplates)
	s.route("GET "+api+"/drafts/{id}/templates/{name...}", e, s.getTemplate)
	s.route("GET "+api+"/drafts/{id}/flows", v, s.getFlows)

	s.route("POST "+api+"/drafts/{id}/preview", e, s.previewEnsure)
	s.route("DELETE "+api+"/drafts/{id}/preview", e, s.previewStop)
	s.route("GET "+api+"/drafts/{id}/preview/requests", e, s.previewRequests)
	s.mux.HandleFunc("/preview/{id}/{rest...}", s.previewProxy)

	s.route("GET "+api+"/revisions", v, s.listRevisions)
	s.route("GET "+api+"/revisions/{id}", v, s.getRevision)
	s.route("GET "+api+"/revisions/{id}/diff", v, s.revisionDiff)
	s.route("GET "+api+"/revisions/{id}/comments", v, s.listComments)
	s.route("POST "+api+"/revisions/{id}/comments", e, s.addComment)
	s.route("POST "+api+"/revisions/{id}/approve", rv, s.approve)
	s.route("POST "+api+"/revisions/{id}/reject", rv, s.reject)
	s.route("POST "+api+"/revisions/{id}/activate", rv, s.activate)
	s.route("POST "+api+"/rollback", ad, s.rollback)
	s.route("GET "+api+"/audit", ad, s.auditList)

	s.mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		s.writeErr(w, errf(http.StatusNotFound, "not_found", "no such endpoint"))
	})
	s.mux.HandleFunc("/", s.serveWeb)
}
