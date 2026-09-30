// Package preview runs a Studio draft as a live, sandboxed application.
//
// A Manager compiles a draft's bundle with the sandbox applied (see Sandbox and
// README.md), serves it in-process, and exposes it under /preview/{draft id}/.
// One generation exists per draft, rebuilt only when the draft's version
// changes; a rebuild that fails leaves the previous generation serving.
package preview

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/oarkflow/bcl"
	"github.com/oarkflow/fh"
	"github.com/oarkflow/ref/platform"
	"github.com/oarkflow/ref/studio"
)

// State is where a draft's preview is.
type State string

const (
	StateStarting State = "starting"
	StateReady    State = "ready"
	StateFailed   State = "failed"
	StateStopped  State = "stopped"
)

// Status is the state of one draft's preview.
type Status struct {
	ID    string `json:"id"`
	State State  `json:"status"`
	// Version is the draft version this status is about; Serving is the version
	// actually being served (0 when nothing is). They differ after a failed
	// rebuild: the previous build keeps serving.
	Version     int64               `json:"version"`
	Serving     int64               `json:"serving,omitempty"`
	URL         string              `json:"url,omitempty"`
	Error       string              `json:"error,omitempty"`
	Diagnostics []studio.Diagnostic `json:"diagnostics,omitempty"`
	// Sandbox lists what the preview changed or turned off relative to the
	// draft, so the editor can see what it is not showing.
	Sandbox   []Change  `json:"sandbox,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Studio converts a Status to the shared wire type.
func (st Status) Studio() studio.PreviewStatus {
	out := studio.PreviewStatus{Status: string(st.State), URL: st.URL, Version: st.Version, Error: st.Diagnostics}
	switch {
	case st.State == StateFailed && st.Serving != 0:
		out.Message = fmt.Sprintf("build of version %d failed; version %d is still being served", st.Version, st.Serving)
		out.Version = st.Serving
	case st.State == StateFailed:
		out.Message = st.Error
	case st.State == StateReady:
		var kept, changed, off int
		for _, c := range st.Sandbox {
			switch c.Action {
			case ActionDisable:
				off++
			case ActionKeep:
				kept++
			default:
				changed++
			}
		}
		out.Message = fmt.Sprintf("sandboxed: %d resources changed, %d background blocks disabled", changed, off)
	}
	return out
}

// Options configures a Service.
type Options struct {
	// NewApp and Mount are the same hooks deploy.Supervisor has, so the host's
	// template engine, middleware and static handling apply. Mount must call
	// p.Mount(app). Defaults: fh.New with the banner off, and p.Mount.
	NewApp func() *fh.App
	Mount  func(*fh.App, *platform.Platform) error
	// NewAppFor and MountFor are NewApp and Mount for a host that renders the
	// draft's own templates and static files: they replace NewApp and Mount
	// when set, and receive the generation's BuildEnv (its Assets, and a
	// scratch directory removed when the generation retires). A host whose
	// engine reads templates from disk materialises env.Assets over its own
	// files into env.Dir; one with an fs.FS engine uses env.Assets.Overlay.
	// MountFor must call p.Mount(app).
	NewAppFor func(env BuildEnv) *fh.App
	MountFor  func(app *fh.App, p *platform.Platform, env BuildEnv) error
	// Profile is the BCL profile compiled with (default "preview"), so an
	// application can carry `profile "preview" { override ... }` of its own.
	Profile string
	// Registry supplies the capability registry for each build (default
	// platform.NewRegistry).
	Registry func() *platform.Registry
	// ExtraKinds are host-registered resource kinds vouched for as local-only.
	ExtraKinds []string
	// AfterBuild runs after a generation compiles and mounts, before it goes
	// live: the place to run migrations and seed data into the sandboxed
	// database (p.Resource(name) returns it). An error fails the build.
	AfterBuild func(ctx context.Context, id string, p *platform.Platform) error
	// Env supplies environment values by name. The host environment is never
	// read; variables the document requires but Env lacks get a placeholder.
	Env map[string]string
	// BaseDir is the baseDir handed to the compiler (static roots, relative
	// rule directories). Default: the current directory.
	BaseDir string
	// TempDir is the parent of per-generation directories (default os.TempDir()).
	TempDir string
	// Prefix is the URL prefix Handler serves (default "/preview/").
	Prefix string
	// Idle is how long an unused preview lives (default 10m).
	Idle time.Duration
	// Grace and Drain shape the retirement of a replaced generation: it keeps
	// serving for Grace (default 1s), then finishes in-flight requests for at
	// most Drain (default 10s).
	Grace, Drain time.Duration
	// MaxRecords bounds each recorder (default 200).
	MaxRecords int
	Logf       func(format string, args ...any)
}

func (o *Options) defaults() {
	if o.Profile == "" {
		o.Profile = "preview"
	}
	if o.Registry == nil {
		o.Registry = platform.NewRegistry
	}
	if o.NewApp == nil {
		o.NewApp = func() *fh.App { return fh.New(fh.WithStartupBannerDisabled(true)) }
	}
	if o.Mount == nil {
		o.Mount = func(app *fh.App, p *platform.Platform) error { return p.Mount(app) }
	}
	if o.TempDir == "" {
		o.TempDir = os.TempDir()
	}
	if o.Prefix == "" {
		o.Prefix = "/preview/"
	}
	if !strings.HasSuffix(o.Prefix, "/") {
		o.Prefix += "/"
	}
	if o.Idle <= 0 {
		o.Idle = 10 * time.Minute
	}
	if o.Grace <= 0 {
		o.Grace = time.Second
	}
	if o.Drain <= 0 {
		o.Drain = 10 * time.Second
	}
	if o.MaxRecords <= 0 {
		o.MaxRecords = 200
	}
}

// Service is the Manager implementation.
type Service struct {
	opts Options

	mu       sync.Mutex
	entries  map[string]*entry
	subs     map[int]chan Status
	subSeq   int
	closed   bool
	draining bool // Close has started waiting on wg; no further wg.Add

	stopJanitor chan struct{}
	wg          sync.WaitGroup
}

type entry struct {
	id  string
	sem chan struct{} // serialises builds for this draft

	mu     sync.RWMutex
	gen    *generation
	status Status
}

// New returns a Service and starts its idle janitor. Call Close to stop it.
func New(opts Options) *Service {
	opts.defaults()
	s := &Service{opts: opts, entries: map[string]*entry{}, subs: map[int]chan Status{}, stopJanitor: make(chan struct{})}
	s.wg.Add(1)
	go s.janitor()
	return s
}

var _ studio.PreviewManager = (*Service)(nil)

func (s *Service) logf(format string, args ...any) {
	if s.opts.Logf != nil {
		s.opts.Logf(format, args...)
	}
}

func (s *Service) url(id string) string { return s.opts.Prefix + id + "/" }

func (s *Service) entry(id string, create bool) *entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.entries[id]
	if e == nil && create && !s.closed {
		e = &entry{id: id, sem: make(chan struct{}, 1)}
		s.entries[id] = e
	}
	return e
}

func (e *entry) current() *generation {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.gen
}

func (e *entry) setStatus(st Status) {
	e.mu.Lock()
	e.status = st
	e.mu.Unlock()
}

// Ensure implements studio.PreviewManager. A failed build is reported in the
// returned status, not as an error; the error is for cancellation, a closed
// manager or a nil draft.
func (s *Service) Ensure(ctx context.Context, d studio.Draft) (studio.PreviewStatus, error) {
	st, err := s.EnsureStatus(ctx, d)
	if err != nil {
		return studio.PreviewStatus{}, err
	}
	return st.Studio(), nil
}

// EnsureStatus is Ensure with the full Status (serving version, sandbox
// report).
func (s *Service) EnsureStatus(ctx context.Context, d studio.Draft) (Status, error) {
	if d == nil {
		return Status{}, errors.New("preview: nil draft")
	}
	id := d.ID()
	e := s.entry(id, true)
	if e == nil {
		return Status{}, errors.New("preview: manager is closed")
	}
	select {
	case e.sem <- struct{}{}:
	case <-ctx.Done():
		return Status{}, ctx.Err()
	}
	defer func() { <-e.sem }()

	version := d.Version()
	e.mu.RLock()
	st, gen := e.status, e.gen
	e.mu.RUnlock()
	if st.Version == version && (st.State == StateReady || st.State == StateFailed) {
		if gen != nil {
			gen.touch()
		}
		return st, nil
	}

	prev := Status{}
	if gen != nil {
		prev.Serving = gen.version
		prev.URL = s.url(id)
	}
	starting := Status{ID: id, State: StateStarting, Version: version, Serving: prev.Serving, URL: prev.URL, UpdatedAt: time.Now()}
	e.setStatus(starting)
	s.publish(starting)

	next, changes, diags, err := s.build(ctx, d)
	if err != nil && ctx.Err() != nil {
		// Cancelled: leave the previous generation and status alone.
		e.setStatus(st)
		return Status{}, ctx.Err()
	}
	if err != nil {
		failed := Status{ID: id, State: StateFailed, Version: version, Serving: prev.Serving, URL: prev.URL,
			Error: err.Error(), Diagnostics: diags, Sandbox: changes, UpdatedAt: time.Now()}
		e.setStatus(failed)
		s.publish(failed)
		s.logf("preview %s v%d failed: %v", id, version, err)
		return failed, nil
	}

	s.mu.Lock()
	alive := s.entries[id] == e && !s.closed
	s.mu.Unlock()
	if !alive { // Stop or Close ran while this build was in flight
		s.retire(next, 0)
		return Status{}, errors.New("preview: stopped while building")
	}
	e.mu.Lock()
	old := e.gen
	e.gen = next
	ready := Status{ID: id, State: StateReady, Version: version, Serving: version, URL: s.url(id), Sandbox: next.changes, UpdatedAt: time.Now()}
	e.status = ready
	e.mu.Unlock()
	if old != nil {
		s.retire(old, s.opts.Grace)
	}
	s.publish(ready)
	return ready, nil
}

func (s *Service) retire(g *generation, grace time.Duration) {
	// wg.Add must not race with Close's wg.Wait, so it is done under s.mu and
	// only until Close starts waiting. A late retire (a build that finished
	// during shutdown) tears its generation down inline instead.
	s.mu.Lock()
	if s.draining {
		s.mu.Unlock()
		g.close(grace, s.opts.Drain)
		return
	}
	s.wg.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.wg.Done()
		g.close(grace, s.opts.Drain)
	}()
}

// Stop implements studio.PreviewManager.
func (s *Service) Stop(id string) {
	s.mu.Lock()
	e := s.entries[id]
	delete(s.entries, id)
	s.mu.Unlock()
	if e == nil {
		return
	}
	e.mu.Lock()
	g := e.gen
	e.gen = nil
	e.status = Status{ID: id, State: StateStopped, Version: e.status.Version, UpdatedAt: time.Now()}
	st := e.status
	e.mu.Unlock()
	if g != nil {
		s.retire(g, 0)
	}
	s.publish(st)
}

// Status returns a draft's current preview status.
func (s *Service) Status(id string) (Status, bool) {
	e := s.entry(id, false)
	if e == nil {
		return Status{}, false
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.status, true
}

// RequestLog returns the last requests served, oldest first.
func (s *Service) RequestLog(id string) []RequestEntry {
	if e := s.entry(id, false); e != nil {
		if g := e.current(); g != nil {
			return g.rec.requests.snapshot()
		}
	}
	return nil
}

// Outbound returns the last stubbed outbound calls, oldest first.
func (s *Service) Outbound(id string) []OutboundCall {
	if e := s.entry(id, false); e != nil {
		if g := e.current(); g != nil {
			return g.rec.outbound.snapshot()
		}
	}
	return nil
}

// Requests implements studio.PreviewManager: requests served and outbound
// calls stubbed, merged oldest first.
func (s *Service) Requests(id string) []studio.RecordedRequest {
	reqs, outs := s.RequestLog(id), s.Outbound(id)
	out := make([]studio.RecordedRequest, 0, len(reqs)+len(outs))
	for _, r := range reqs {
		u := r.Path
		if r.Query != "" {
			u += "?" + r.Query
		}
		detail := map[string]any{"version": r.Version}
		if r.Route != "" {
			detail["route"] = r.Route
		}
		if r.Intent != "" {
			detail["intent"] = r.Intent
		}
		out = append(out, studio.RecordedRequest{At: r.Time, Kind: "request", Method: r.Method, URL: u,
			Status: r.Status, DurationMS: r.DurationMs, Detail: detail})
	}
	for _, c := range outs {
		out = append(out, studio.RecordedRequest{At: c.Time, Kind: "outbound", Method: c.Method, URL: c.Target,
			Detail: map[string]any{"channel": c.Kind, "preview": c.Preview, "bytes": c.Bytes}})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out
}

// Events delivers status changes (starting, ready, failed, stopped); the func
// cancels the subscription. A host bridges these to Studio's SSE stream.
func (s *Service) Events() (<-chan Status, func()) {
	ch := make(chan Status, 32)
	s.mu.Lock()
	s.subSeq++
	n := s.subSeq
	s.subs[n] = ch
	s.mu.Unlock()
	return ch, func() {
		s.mu.Lock()
		if c, ok := s.subs[n]; ok {
			delete(s.subs, n)
			close(c)
		}
		s.mu.Unlock()
	}
}

func (s *Service) publish(st Status) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ch := range s.subs {
		select {
		case ch <- st:
		default: // a slow subscriber misses events rather than blocking a build
		}
	}
}

// Close stops every preview and the janitor.
func (s *Service) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	ids := make([]string, 0, len(s.entries))
	for id := range s.entries {
		ids = append(ids, id)
	}
	s.mu.Unlock()
	close(s.stopJanitor)
	for _, id := range ids {
		s.Stop(id)
	}
	s.mu.Lock()
	s.draining = true
	s.mu.Unlock()
	s.wg.Wait()
	s.mu.Lock()
	for n, ch := range s.subs {
		delete(s.subs, n)
		close(ch)
	}
	s.mu.Unlock()
}

func (s *Service) janitor() {
	defer s.wg.Done()
	every := min(max(s.opts.Idle/4, 10*time.Millisecond), 30*time.Second)
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-s.stopJanitor:
			return
		case <-t.C:
		}
		cutoff := time.Now().Add(-s.opts.Idle).UnixNano()
		s.mu.Lock()
		var idle []string
		for id, e := range s.entries {
			if g := e.current(); g != nil && g.lastUse.Load() < cutoff {
				idle = append(idle, id)
			}
		}
		s.mu.Unlock()
		for _, id := range idle {
			s.logf("preview %s idle, stopping", id)
			s.Stop(id)
		}
	}
}

// BuildEnv is what a host's NewAppFor and MountFor learn about the generation
// being built.
type BuildEnv struct {
	// ID and Version identify the draft and the version being built.
	ID      string
	Version int64
	// Dir is a scratch directory for this generation, created empty and
	// removed after the generation retires.
	Dir string
	// Assets are the draft's templates and static files (nil when the draft
	// has none; see studio.AssetDraft).
	Assets platform.Assets
}

// build compiles and starts one generation.
func (s *Service) build(ctx context.Context, d studio.Draft) (*generation, []Change, []studio.Diagnostic, error) {
	id, version, bundle := d.ID(), d.Version(), d.Bundle()
	root, err := os.MkdirTemp(s.opts.TempDir, "studio-preview-")
	if err != nil {
		return nil, nil, nil, fmt.Errorf("preview: create temp dir: %w", err)
	}
	rec := newRecorder(s.opts.MaxRecords)
	var changes []Change
	loadOpts := platform.LoadOptions{
		Registry:       s.opts.Registry(),
		AllowEnv:       true,
		Env:            previewEnv(bundle.Source(), s.opts.Env),
		ResolveImports: false,
		Strict:         true,
		Profile:        s.opts.Profile,
		Mutate: func(doc *platform.Document) error {
			c, err := Sandbox(doc, SandboxOptions{Root: root, ExtraKinds: s.opts.ExtraKinds})
			changes = c
			return err
		},
	}
	// The compile context outlives this call (resources may keep it), so it must
	// not be cancelled with the request that asked for the build.
	cctx := platform.WithOffline(context.WithoutCancel(ctx), stubTransport{rec}, stubMailer{rec})

	type result struct {
		p   *platform.Platform
		err error
	}
	done := make(chan result, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				done <- result{nil, fmt.Errorf("preview: compile panicked: %v", r)}
			}
		}()
		p, err := platform.CompileBundle(cctx, bundle, s.opts.BaseDir, loadOpts)
		done <- result{p, err}
	}()
	var p *platform.Platform
	select {
	case r := <-done:
		p, err = r.p, r.err
	case <-ctx.Done():
		go func() { // let the build finish, then release what it opened
			if r := <-done; r.p != nil {
				_ = r.p.Close()
			}
			_ = os.RemoveAll(root)
		}()
		return nil, nil, nil, ctx.Err()
	}
	if err != nil {
		_ = os.RemoveAll(root)
		return nil, changes, s.diagnose(cctx, bundle, err), err
	}

	env := BuildEnv{ID: id, Version: version, Dir: filepath.Join(root, "host"), Assets: studio.DraftAssets(d)}
	if err := os.MkdirAll(env.Dir, 0o755); err != nil {
		_ = p.Close()
		_ = os.RemoveAll(root)
		return nil, changes, nil, fmt.Errorf("preview: create host dir: %w", err)
	}
	var app *fh.App
	if s.opts.NewAppFor != nil {
		app = s.opts.NewAppFor(env)
	} else {
		app = s.opts.NewApp()
	}
	fail := func(err error) (*generation, []Change, []studio.Diagnostic, error) {
		_ = p.Close()
		_ = os.RemoveAll(root)
		return nil, changes, []studio.Diagnostic{{Severity: "error", Message: err.Error()}}, err
	}
	// A document can make mounting panic (fh panics on a static root that does
	// not exist); an editor's mistake must fail the preview, not the process.
	mount := func() error { return s.opts.Mount(app, p) }
	if s.opts.MountFor != nil {
		mount = func() error { return s.opts.MountFor(app, p, env) }
	}
	if err := safely("mount", mount); err != nil {
		return fail(err)
	}
	if s.opts.AfterBuild != nil {
		if err := safely("after-build hook", func() error { return s.opts.AfterBuild(cctx, id, p) }); err != nil {
			return fail(fmt.Errorf("preview: after-build hook: %w", err))
		}
	}
	g := &generation{id: id, version: version, root: root, p: p, app: app, ln: newMemListener(),
		done: make(chan struct{}), rec: rec, routes: buildMatchers(p.Document), changes: changes}
	g.touch()
	go func() {
		defer close(g.done)
		_ = app.Serve(g.ln)
	}()
	g.initProxy(strings.TrimSuffix(s.opts.Prefix, "/") + "/" + id)
	return g, changes, nil, nil
}

// diagnose turns a failed build into diagnostics that name files and lines.
func (s *Service) diagnose(ctx context.Context, b platform.Bundle, err error) []studio.Diagnostic {
	var list bcl.ErrorList
	if errors.As(err, &list) && len(list) > 0 {
		out := make([]studio.Diagnostic, 0, len(list))
		seen := map[studio.Diagnostic]bool{} // bcl reports the same finding more than once
		for _, d := range list {
			x := studio.Diagnostic{Severity: orDefault(d.Severity, "error"), Code: d.Code, Message: d.Message,
				File: d.Span.File, Line: d.Span.Start.Line, Column: d.Span.Start.Column, Offset: d.Span.Start.Offset}
			if !seen[x] {
				seen[x] = true
				out = append(out, x)
			}
		}
		return out
	}
	// Otherwise ask the static validator, which knows blocks and fields.
	report := platform.ValidateBundle(ctx, b, s.opts.BaseDir, platform.LoadOptions{
		Registry: s.opts.Registry(), AllowEnv: true, Strict: true, Profile: s.opts.Profile,
		Env: previewEnv(b.Source(), s.opts.Env),
	})
	var out []studio.Diagnostic
	for _, d := range studio.FromPlatform(report.Diagnostics) {
		if d.Severity == platform.SeverityError {
			out = append(out, d)
		}
	}
	if len(out) == 0 {
		out = append(out, studio.Diagnostic{Severity: "error", Message: err.Error()})
	}
	return out
}

// safely runs f, turning a panic into an error.
func safely(what string, f func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%s panicked: %v", what, r)
		}
	}()
	return f()
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func writeJSONError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": code, "message": msg}})
}
