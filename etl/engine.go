package etl

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/oarkflow/ref/backoff"
)

// Hooks do the work the engine does not decide. A nil hook passes rows through.
// A hook that returns an error wrapped with Permanent is not retried.
type Hooks struct {
	// Transform returns the transformed rows and the version of the transform
	// that produced them (recorded in lineage).
	Transform func(ctx context.Context, src *Source, b *Batch, rows []Row) ([]Row, string, error)
	// Transfer moves the rows to the receiving system and returns once it has
	// acknowledged them.
	Transfer func(ctx context.Context, src *Source, b *Batch, rows []Row) error
	// Deliver puts the rows on the destination and returns its reference for
	// the batch. It must be idempotent on b.Key.
	Deliver func(ctx context.Context, src *Source, b *Batch, rows []Row) (ref string, err error)
	// Verify asks the destination whether it already has what b.DeliveryKey
	// names. It is called only when an earlier delivery call was interrupted
	// and its outcome is not known, so a batch that was in fact delivered is
	// recorded as delivered and not sent a second time. Without it an
	// interrupted delivery is repeated, and the destination must deduplicate.
	Verify func(ctx context.Context, src *Source, b *Batch, rows []Row) (ref string, found bool, err error)
}

type permanent struct{ err error }

func (p permanent) Error() string { return p.err.Error() }
func (p permanent) Unwrap() error { return p.err }

// Permanent marks an error that retrying cannot fix.
func Permanent(err error) error { return permanent{err} }

type traceKey struct{}

// WithTrace carries a caller's trace id into Ingest, so a batch can be found
// from the request that sent it.
func WithTrace(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, traceKey{}, id)
}

// Engine runs batches. It is safe for concurrent use: every write is a
// revision-checked Commit, so two workers advancing one batch cannot both win.
type Engine struct {
	Store Store
	Hooks Hooks
	Eval  Evaluator
	Now   func() time.Time
	// Backoff computes a retry delay; default is full jitter.
	Backoff func(base time.Duration, attempt int, cap time.Duration) time.Duration
	// CheckExpr reports whether an expression compiles; sources with a bad
	// expression rule are refused when registered rather than when data arrives.
	CheckExpr func(expr string) error
	// Blobs holds the chunks of large batches and the files ingested by name
	// (nil: every checkpoint is kept inline in the store, so a batch must fit in memory).
	Blobs Blobs
	// ChunkRows is how many rows one chunk holds (default 5000). With Blobs set,
	// a batch bigger than this is processed a chunk at a time: memory stays at
	// one chunk however large the batch is.
	ChunkRows int
	// InboxPrefix is where files to ingest by name live in Blobs (default "inbox/").
	InboxPrefix string
	// StageDelay is how long a batch waits between stages when a sweeper
	// drives it (zero: as fast as the sweeper runs). Advance itself ignores it.
	StageDelay time.Duration
	// Logger receives structured logs (default slog.Default()). Every record
	// is also kept in Logs for the console.
	Logger *slog.Logger
	Logs   *Logbook
	// StageTimeout bounds one hook call (default 30s). A hook that outruns it,
	// or panics, is a failed attempt like any other.
	StageTimeout time.Duration
	// LeaseTTL is how long a worker owns a batch's stage before another may
	// take it over (default twice the stage timeout plus 30s): the recovery
	// time after a worker dies mid-stage.
	LeaseTTL time.Duration
	// Owner names this worker in leases (default: a random id per process).
	Owner string
	// BreakerThreshold consecutive failures open a destination's circuit for
	// BreakerCooldown (defaults 5 and 30s). While it is open batches wait
	// without using up their attempts.
	BreakerThreshold int
	BreakerCooldown  time.Duration

	// Grace is how long a hook that has been cancelled (its timeout passed) is
	// given to return before it is counted as abandoned (default 2s). Hooks that
	// honour their context, as database and HTTP calls do, return well within it.
	Grace time.Duration
	// WedgeAfter is how long a hook may stay abandoned before the process is
	// reported as wedged (default 2m): health goes down, so a supervisor or load
	// balancer restarts it, and OnWedged is called once. A goroutine that ignores
	// cancellation can only be reclaimed by restarting the process.
	WedgeAfter time.Duration
	OnWedged   func(abandoned int, for_ time.Duration)
	// MaxAbandoned caps hooks left running after a timeout (default 32): past
	// it new hook calls fail at once instead of piling up behind a hung receiver.
	MaxAbandoned int
	// Notify is told when an alert opens or clears (event "opened" or "cleared").
	// A returned error is logged and the notification is not marked sent, so it
	// is tried again on the next evaluation.
	Notify func(ctx context.Context, event string, a AlertRecord) error

	brMu      sync.Mutex
	brCache   map[string]cachedBreaker
	abandon   atomic.Int64 // hooks still running after their timeout and grace
	abandonAt atomic.Int64 // unix nanoseconds since which at least one has been
	wedged    atomic.Bool
	once      sync.Once
	rolesMu   sync.Mutex
	roles     []*Role
	rolesAt   time.Time
	heartbeat atomic.Int64 // unix nanoseconds of the last sweep
	auditMu   sync.Mutex
	auditAt   time.Time
	auditBad  int64
}

func (e *Engine) init() {
	e.once.Do(func() {
		if e.Logs == nil {
			e.Logs = NewLogbook(1000)
		}
		if e.Owner == "" {
			e.Owner = newID("w_")
		}
		e.brCache = map[string]cachedBreaker{}
		if e.Logger == nil {
			e.Logger = slog.Default()
		}
	})
}

func (e *Engine) now() time.Time {
	if e.Now != nil {
		return e.Now().UTC()
	}
	return time.Now().UTC()
}

func newID(prefix string) string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

func (e *Engine) audit(actor, action, batch, source, trace, detail string) AuditEntry {
	return AuditEntry{At: e.now(), Actor: actor, Action: action, BatchID: batch, SourceID: source, TraceID: trace, Detail: detail}
}

// log writes one structured record to the logger and the logbook.
func (e *Engine) log(level, msg string, b *Batch, stage int, kv ...any) {
	e.init()
	entry := LogEntry{At: e.now(), Level: level, Message: msg, Stage: stage, Attrs: map[string]any{}}
	args := []any{}
	if b != nil {
		entry.BatchID, entry.SourceID, entry.TraceID = b.ID, b.SourceID, b.TraceID
		args = append(args, "batch", b.ID, "source", b.SourceID, "trace", b.TraceID)
	}
	if stage > 0 {
		args = append(args, "stage", stage)
	}
	for i := 0; i+1 < len(kv); i += 2 {
		entry.Attrs[fmt.Sprint(kv[i])] = kv[i+1]
	}
	args = append(args, kv...)
	e.Logs.Add(entry)
	lv := map[string]slog.Level{"debug": slog.LevelDebug, "info": slog.LevelInfo, "warn": slog.LevelWarn, "error": slog.LevelError}[level]
	e.Logger.Log(context.Background(), lv, "etl: "+msg, args...)
}

func defaults(p RetryPolicy) RetryPolicy {
	if p.MaxAttempts <= 0 {
		p.MaxAttempts = 4
	}
	if p.Base <= 0 {
		p.Base = 2 * time.Second
	}
	if p.Cap <= 0 {
		p.Cap = 10 * time.Minute
	}
	return p
}

// ---------------------------------------------------------------------------
// Access
// ---------------------------------------------------------------------------

const rolesTTL = 2 * time.Second

func (e *Engine) loadRoles(ctx context.Context) ([]*Role, error) {
	e.rolesMu.Lock()
	defer e.rolesMu.Unlock()
	if e.roles != nil && time.Since(e.rolesAt) < rolesTTL {
		return e.roles, nil
	}
	list, err := e.Store.ListRoles(ctx)
	if err != nil {
		return nil, err
	}
	e.roles, e.rolesAt = list, time.Now()
	return list, nil
}

// EnsureRoles creates the built-in roles that do not exist yet.
func (e *Engine) EnsureRoles(ctx context.Context) error {
	for _, r := range BuiltinRoles() {
		if _, err := e.Store.GetRole(ctx, r.ID); err == nil {
			continue
		} else if !errors.Is(err, ErrNotFound) {
			return err
		}
		r.UpdatedAt = e.now()
		if err := e.Store.Commit(ctx, Change{Role: &r, Audit: []AuditEntry{e.audit("system", "role.create", "", "", "", "built-in role "+r.ID)}}); err != nil {
			return err
		}
	}
	e.rolesMu.Lock()
	e.roles = nil
	e.rolesMu.Unlock()
	return nil
}

// Access resolves what an actor may do from their roles.
func (e *Engine) Access(ctx context.Context, actor Actor) (*Access, error) {
	if actor.ID == "" {
		return newAccess(nil, nil), nil
	}
	roles, err := e.loadRoles(ctx)
	if err != nil {
		return nil, err
	}
	return newAccess(roles, actor.Roles), nil
}

// need requires a permission, for a source when one is in play. A caller who
// holds the permission only on other sources gets ErrNotFound, so the
// existence of a source they cannot see is not disclosed.
func (e *Engine) need(ctx context.Context, actor Actor, perm, source string) (*Access, error) {
	acc, err := e.Access(ctx, actor)
	if err != nil {
		return nil, err
	}
	if !acc.Has(perm) {
		return nil, ErrForbidden
	}
	if !acc.Allows(perm, source) {
		return nil, ErrNotFound
	}
	return acc, nil
}

// Require is need without a source, for applications that guard their own
// operations (users, keys) with the engine's roles.
func (e *Engine) Require(ctx context.Context, actor Actor, perm string) error {
	_, err := e.need(ctx, actor, perm, "")
	return err
}

// intersect narrows a source filter by a scope (nil means no limit).
func intersect(want string, scope []string) (ids []string, none bool) {
	switch {
	case want != "" && scope == nil:
		return []string{want}, false
	case want != "":
		if contains(scope, want) {
			return []string{want}, false
		}
		return nil, true
	case scope != nil && len(scope) == 0:
		return nil, true
	}
	return scope, false
}

// ListRoles returns the roles. Anyone who manages users or roles may read them.
func (e *Engine) ListRoles(ctx context.Context, actor Actor) ([]*Role, error) {
	acc, err := e.Access(ctx, actor)
	if err != nil {
		return nil, err
	}
	if !acc.Has(PermRoles) && !acc.Has(PermUsers) {
		return nil, ErrForbidden
	}
	return e.Store.ListRoles(ctx)
}

var roleID = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,40}$`)

// PutRole creates or updates a role. The administrator role always keeps every
// permission, so the system cannot be locked out of its own settings.
func (e *Engine) PutRole(ctx context.Context, actor Actor, r Role) (*Role, error) {
	if _, err := e.need(ctx, actor, PermRoles, ""); err != nil {
		return nil, err
	}
	r.ID = strings.TrimSpace(r.ID)
	if !roleID.MatchString(r.ID) || strings.TrimSpace(r.Name) == "" {
		return nil, fmt.Errorf("%w: a role needs a name and an id of lowercase letters, digits, - or _", ErrInvalid)
	}
	known := map[string]bool{}
	for _, p := range Permissions {
		known[p.ID] = true
	}
	for _, p := range r.Permissions {
		if !known[p] {
			return nil, fmt.Errorf("%w: unknown permission %q", ErrInvalid, p)
		}
	}
	for _, s := range r.Sources {
		if s == "*" {
			continue
		}
		if _, err := e.Store.GetSource(ctx, s); err != nil {
			return nil, fmt.Errorf("%w: unknown source %q", ErrInvalid, s)
		}
	}
	action := "role.create"
	if cur, err := e.Store.GetRole(ctx, r.ID); err == nil {
		r.System, action = cur.System, "role.update"
	} else if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	if r.ID == RoleAdmin {
		r.Permissions, r.Sources = allPermissions(), nil
	}
	r.UpdatedAt = e.now()
	detail := fmt.Sprintf("permissions [%s], sources [%s]", strings.Join(r.Permissions, " "), strings.Join(r.Sources, " "))
	if err := e.Store.Commit(ctx, Change{Role: &r, Audit: []AuditEntry{e.audit(actor.ID, action, "", "", "", r.ID+": "+detail)}}); err != nil {
		return nil, err
	}
	e.rolesMu.Lock()
	e.roles = nil
	e.rolesMu.Unlock()
	e.log("info", action+" "+r.ID, nil, 0, "actor", actor.ID)
	return &r, nil
}

// DeleteRole removes a role that is not built in. Callers that assign roles to
// users must check the role is unused first.
func (e *Engine) DeleteRole(ctx context.Context, actor Actor, id string) error {
	if _, err := e.need(ctx, actor, PermRoles, ""); err != nil {
		return err
	}
	cur, err := e.Store.GetRole(ctx, id)
	if err != nil {
		return err
	}
	if cur.System {
		return fmt.Errorf("%w: %q is a built-in role and cannot be deleted", ErrState, id)
	}
	if err := e.Store.Commit(ctx, Change{DeleteRole: id, Audit: []AuditEntry{e.audit(actor.ID, "role.delete", "", "", "", id)}}); err != nil {
		return err
	}
	e.rolesMu.Lock()
	e.roles = nil
	e.rolesMu.Unlock()
	return nil
}

// Note records an application-level event (a user created, a key revoked) in
// the audit trail. The actor must hold the permission the event belongs to; an
// empty permission only requires a signed-in actor (a person changing their own
// password).
func (e *Engine) Note(ctx context.Context, actor Actor, perm, action, detail string) error {
	if perm == "" {
		if actor.ID == "" {
			return ErrForbidden
		}
	} else if _, err := e.need(ctx, actor, perm, ""); err != nil {
		return err
	}
	if strings.TrimSpace(action) == "" {
		return fmt.Errorf("%w: an audit entry needs an action", ErrInvalid)
	}
	return e.Store.Commit(ctx, Change{Audit: []AuditEntry{e.audit(actor.ID, action, "", "", "", detail)}})
}

// ---------------------------------------------------------------------------
// Sources
// ---------------------------------------------------------------------------

// PutSource registers or updates a source. Updating bumps its version.
func (e *Engine) PutSource(ctx context.Context, actor Actor, s Source) (*Source, error) {
	if _, err := e.need(ctx, actor, PermManage, s.ID); err != nil {
		return nil, err
	}
	if strings.TrimSpace(s.ID) == "" || strings.TrimSpace(s.Owner) == "" {
		return nil, fmt.Errorf("%w: a source needs an id and an owner", ErrInvalid)
	}
	switch s.Format {
	case "csv", "jsonl", "json":
	default:
		return nil, fmt.Errorf("%w: format must be csv, jsonl or json", ErrInvalid)
	}
	if s.MaxRejectRate < 0 || s.MaxRejectRate > 1 {
		return nil, fmt.Errorf("%w: max_reject_rate must be between 0 and 1", ErrInvalid)
	}
	if _, _, err := Validate(&s, nil, e.Eval); err != nil {
		return nil, err
	}
	if e.CheckExpr != nil {
		for _, r := range s.Rules {
			if r.Kind == "expr" {
				if err := e.CheckExpr(r.Expr); err != nil {
					return nil, fmt.Errorf("%w: rule %q: %v", ErrInvalid, r.Name, err)
				}
			}
		}
	}
	now := e.now()
	s.Retry = defaults(s.Retry)
	s.UpdatedAt = now
	action := "source.create"
	if cur, err := e.Store.GetSource(ctx, s.ID); err == nil {
		s.Version, s.CreatedAt, s.Paused = cur.Version+1, cur.CreatedAt, cur.Paused
		action = "source.update"
	} else if errors.Is(err, ErrNotFound) {
		s.Version, s.CreatedAt = 1, now
	} else {
		return nil, err
	}
	if err := e.Store.Commit(ctx, Change{Source: &s, Audit: []AuditEntry{e.audit(actor.ID, action, "", s.ID, "", fmt.Sprintf("version %d, %d rules", s.Version, len(s.Rules)))}}); err != nil {
		return nil, err
	}
	e.log("info", action+" "+s.ID, nil, 0, "actor", actor.ID, "version", s.Version)
	return &s, nil
}

// SetPaused stops or resumes new batches from a source.
func (e *Engine) SetPaused(ctx context.Context, actor Actor, id string, paused bool) (*Source, error) {
	if _, err := e.need(ctx, actor, PermManage, id); err != nil {
		return nil, err
	}
	s, err := e.Store.GetSource(ctx, id)
	if err != nil {
		return nil, err
	}
	s.Paused, s.UpdatedAt = paused, e.now()
	action := "source.resume"
	if paused {
		action = "source.pause"
	}
	e.log("info", action+" "+id, nil, 0, "actor", actor.ID)
	return s, e.Store.Commit(ctx, Change{Source: s, Audit: []AuditEntry{e.audit(actor.ID, action, "", id, "", "")}})
}

// ---------------------------------------------------------------------------
// Running batches
// ---------------------------------------------------------------------------

// IngestResult is what Ingest returns.
type IngestResult struct {
	Batch     *Batch
	Duplicate bool // the key was seen before with the same content
}

// refuse counts an upload turned away before it became a batch.
func (e *Engine) refuse(ctx context.Context, source, reason, actor string) {
	t := NewTally()
	t.Add("etl_refused_total", 1, "source", source, "reason", reason)
	if reason == "duplicate_content" {
		t.Add("etl_duplicates_rejected_total", 1, "source", source)
	}
	e.bump(ctx, t)
	e.log("warn", "upload refused: "+reason, nil, 0, "source", source, "actor", actor)
}

// bump commits counters on their own, for events that change no other state.
// It is best effort: a counter lost here never blocks real work.
func (e *Engine) bump(ctx context.Context, t *Tally) {
	if d := t.Deltas(); len(d) > 0 {
		_ = e.Store.Commit(context.WithoutCancel(ctx), Change{Counters: d})
	}
}

// Advance runs the batch's next stage. A failing stage schedules a retry, and
// after the source's attempt limit holds the batch at its last checkpoint.
// A retrying batch that is not due returns ErrNotDue.
func (e *Engine) Advance(ctx context.Context, actor Actor, id string) (*Batch, error) {
	b, err := e.Store.GetBatch(ctx, id)
	if err != nil {
		if _, perr := e.need(ctx, actor, PermAdvance, ""); perr != nil {
			return nil, perr
		}
		return nil, err
	}
	if _, err := e.need(ctx, actor, PermAdvance, b.SourceID); err != nil {
		return nil, err
	}
	return e.advance(ctx, actor.ID, b)
}

func (e *Engine) runnable(b *Batch) error {
	switch b.Status {
	case StatusInFlight:
	case StatusRetrying:
		if e.now().Before(b.NextAttemptAt) {
			return ErrNotDue
		}
	default:
		return fmt.Errorf("%w: batch is %s", ErrState, b.Status)
	}
	return nil
}

func (e *Engine) leaseTTL() time.Duration {
	if e.LeaseTTL > 0 {
		return e.LeaseTTL
	}
	return 2*e.stageTimeout() + 30*time.Second
}

func (e *Engine) stageTimeout() time.Duration {
	if e.StageTimeout > 0 {
		return e.StageTimeout
	}
	return 30 * time.Second
}

// guard runs a hook so that a panic or a hang cannot take the worker down: both
// come back as an ordinary failed attempt. A hook that ignores its context
// keeps running in the background until it returns; the batch moves on.
func (e *Engine) guard(ctx context.Context, name string, t *Tally, fn func(ctx context.Context) error) error {
	// The hook is not cancelled when the process begins to shut down: a stage in
	// progress is allowed to finish (up to its timeout) so its outcome is
	// recorded, instead of being abandoned half done.
	maxAbandoned := int64(e.MaxAbandoned)
	if maxAbandoned <= 0 {
		maxAbandoned = 32
	}
	if e.abandon.Load() >= maxAbandoned {
		t.Add("etl_hook_rejected_total", 1, "hook", name)
		return fmt.Errorf("%s was not called: %d earlier calls are still running after their timeout", name, e.abandon.Load())
	}
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), e.stageTimeout())
	defer cancel()
	done := make(chan error, 1)
	var timedOut atomic.Bool
	go func() {
		defer func() {
			if timedOut.Load() {
				if e.abandon.Add(-1) == 0 { // the late hook has finally returned
					e.abandonAt.Store(0)
					e.wedged.Store(false)
				}
			}
			if r := recover(); r != nil {
				t.Add("etl_hook_panics_total", 1, "hook", name)
				done <- fmt.Errorf("%s panicked: %v", name, r)
			}
		}()
		done <- fn(cctx)
	}()
	select {
	case err := <-done:
		return err
	case <-cctx.Done():
		t.Add("etl_hook_timeouts_total", 1, "hook", name)
		// The context is cancelled: give a hook that honours it time to unwind.
		grace := e.Grace
		if grace <= 0 {
			grace = 2 * time.Second
		}
		select {
		case <-done:
			return fmt.Errorf("%s did not answer within %s (stopped when cancelled)", name, e.stageTimeout())
		case <-time.After(grace):
		}
		// A Go function cannot be stopped from outside. The batch moves on; the
		// call is counted until it returns, too many of them block new calls,
		// and one that lasts is reported by health so the process gets restarted.
		timedOut.Store(true)
		if e.abandon.Add(1) == 1 {
			e.abandonAt.Store(time.Now().UnixNano())
		}
		return fmt.Errorf("%s did not answer within %s and ignored cancellation", name, e.stageTimeout())
	}
}

func (e *Engine) advance(ctx context.Context, actorID string, seen *Batch) (*Batch, error) {
	e.init()
	if err := e.runnable(seen); err != nil {
		return seen, err
	}
	now := e.now()
	b, err := e.Store.Claim(ctx, seen.ID, e.Owner, now, e.leaseTTL())
	if err != nil {
		if errors.Is(err, ErrLeased) {
			t := NewTally()
			t.Add("etl_lease_conflicts_total", 1)
			e.bump(ctx, t)
		}
		return seen, err
	}
	held := true
	defer func() {
		if held {
			_ = e.Store.Release(context.WithoutCancel(ctx), b.ID, e.Owner)
		}
	}()
	if b.Revision != seen.Revision {
		return b, ErrConflict // changed since it was read: the caller looks again
	}
	if err := e.runnable(b); err != nil {
		return b, err
	}
	src, err := e.Store.GetSource(ctx, b.SourceID)
	if err != nil {
		return nil, err
	}
	cp, err := e.Store.LatestCheckpoint(ctx, b.ID)
	if err != nil {
		return nil, err
	}
	if len(cp.Chunks) == 0 && RowsHash(cp.Rows) != cp.Hash {
		e.log("error", "checkpoint is corrupt", b, b.Stage)
		return nil, fmt.Errorf("etl: checkpoint of batch %s is corrupt", b.ID)
	}
	stage := b.Stage
	attempt := b.Attempts + 1
	tally := NewTally()
	ch := Change{}
	// A destination that keeps failing is left alone for a while: the batch
	// waits, and the wait does not use up one of its attempts.
	if (stage == StageTransfer || stage == StageDelivery) && (e.Hooks.Transfer != nil || e.Hooks.Deliver != nil) {
		if until, open := e.circuitOpen(ctx, src.Destination, now); open {
			b.NextAttemptAt, b.UpdatedAt = until, now
			if n := len(b.Events); n == 0 || !strings.HasPrefix(b.Events[n-1].Message, "Waiting: the circuit") {
				b.Events = append(b.Events, Event{At: now, Stage: stage, Kind: "info", Message: fmt.Sprintf("Waiting: the circuit for %s is open until %s after repeated failures; no attempt used", src.Destination, until.Format("15:04:05"))})
			}
			tally.Add("etl_circuit_waits_total", 1, "destination", src.Destination)
			ch.Batch, ch.Counters = b, tally.Deltas()
			if err := e.commit(ctx, ch); err != nil {
				return nil, err
			}
			held = false
			e.log("warn", "waiting for an open circuit", b, stage, "destination", src.Destination, "until", until.Format(time.RFC3339))
			return b, nil
		}
	}
	var runErr error
	started := time.Now()
	hctx := WithTrace(ctx, b.TraceID)
	switch stage {
	case StageTransform:
		var out *Checkpoint
		var version string
		var total int
		out, version, total, runErr = e.transformAll(hctx, src, b, cp, tally)
		if runErr == nil {
			b.RowsOut = total
			out.BatchID, out.Stage, out.At = b.ID, stage, now
			b.Chunks = len(out.Chunks)
			ch.Checkpoints = []Checkpoint{*out}
			ch.Lineage = []LineageEdge{{BatchID: b.ID, Parent: "batch:" + b.ID, Child: "rows:" + b.ID, Via: "transform " + version, At: now}}
			e.event(b, now, stage, "checkpoint", fmt.Sprintf("Transformed %d rows with %s; checkpoint written", total, version), started, attempt)
		}
	case StageTransfer:
		if e.Hooks.Transfer != nil {
			runErr = e.transferAll(hctx, src, b, cp, tally)
			e.circuitRecord(ctx, src.Destination, now, runErr, tally)
		}
		if runErr == nil {
			next := *cp
			next.Stage, next.At = stage, now
			ch.Checkpoints = []Checkpoint{next}
			e.event(b, now, stage, "checkpoint", "Transfer acknowledged by the receiver; checkpoint written", started, attempt)
		}
	case StageDelivery:
		recovered := 0
		if e.Hooks.Deliver != nil {
			recovered, runErr = e.deliverAll(hctx, src, b, cp, tally, now)
		} else {
			b.Delivered, b.ChunkDone, b.Ref = cp.Count, numChunks(cp), b.ID
			if len(cp.Chunks) == 0 {
				b.Delivered = len(cp.Rows)
			}
		}
		if runErr == nil {
			ch.Lineage = []LineageEdge{{BatchID: b.ID, Parent: "rows:" + b.ID, Child: "dest:" + src.Destination + "/" + b.Ref, Via: "deliver", At: now}}
			msg := fmt.Sprintf("Delivered %d rows to %s as %s", b.Delivered, src.Destination, b.Ref)
			if b.Chunks > 1 {
				msg = fmt.Sprintf("Delivered %d rows in %d chunks to %s (last reference %s)", b.Delivered, b.Chunks, src.Destination, b.Ref)
			}
			if recovered > 0 {
				msg += fmt.Sprintf("; %d chunk(s) were found already delivered after an interruption", recovered)
			}
			e.event(b, now, stage, "info", msg, started, attempt)
		}
	case StageAudit:
		// The books must balance before a batch may count as delivered: every
		// row that came in was either quarantined or delivered, and what was
		// delivered is what the transform produced.
		want := b.RowsIn - b.Quarantined
		if src.AllowRowChange {
			want = b.RowsOut
		}
		if b.Delivered != want || b.RowsOut != b.Delivered {
			runErr = Permanent(fmt.Errorf("reconciliation failed: %d rows in, %d quarantined, %d produced, %d delivered", b.RowsIn, b.Quarantined, b.RowsOut, b.Delivered))
			tally.Add("etl_reconciliation_failures_total", 1, "source", b.SourceID)
		} else {
			e.event(b, now, stage, "info", fmt.Sprintf("Reconciled: %d in = %d quarantined + %d delivered; recorded in the audit trail", b.RowsIn, b.Quarantined, b.Delivered), started, attempt)
			ch.Audit = append(ch.Audit, e.audit(actorID, "batch.deliver", b.ID, b.SourceID, b.TraceID, fmt.Sprintf("%d rows to %s", b.Delivered, src.Destination)))
		}
	case StageMonitor:
		e.event(b, now, stage, "delivered", "Lineage complete: source to destination recorded", started, attempt)
		b.Status, b.FinishedAt = StatusDelivered, now
	default:
		return b, fmt.Errorf("%w: batch has no stage to run", ErrState)
	}
	took := time.Since(started)
	stageLabel := fmt.Sprint(stage)
	b.UpdatedAt = now
	if runErr != nil {
		e.fail(b, src, stage, runErr, now, &ch, actorID, started, attempt)
		tally.Add("etl_stage_runs_total", 1, "stage", stageLabel, "outcome", map[string]string{StatusRetrying: "retry", StatusHeld: "held"}[b.Status])
	} else {
		b.Attempts, b.LastError = 0, ""
		if b.Status != StatusDelivered {
			b.Status, b.NextAttemptAt = StatusInFlight, now.Add(e.StageDelay)
		}
		b.Stage = stage + 1
		tally.Add("etl_stage_runs_total", 1, "stage", stageLabel, "outcome", "ok")
	}
	tally.Observe("etl_stage_duration_seconds", took.Seconds(), "stage", stageLabel)
	hour := now.UTC().Format("2006010215")
	tally.Add(HourlyPrefix+"stage_runs", 1, "stage", stageLabel, "hour", hour)
	tally.Add(HourlyPrefix+"stage_ms", float64(took.Milliseconds()), "stage", stageLabel, "hour", hour)
	if runErr != nil {
		tally.Add(HourlyPrefix+"stage_failures", 1, "stage", stageLabel, "hour", hour)
	}
	if b.Status == StatusDelivered {
		tally.Add("etl_rows_total", float64(b.Delivered), "source", b.SourceID, "kind", "delivered")
		tally.Observe("etl_batch_duration_seconds", b.FinishedAt.Sub(b.CreatedAt).Seconds(), "source", b.SourceID)
	}
	ch.Batch, ch.Counters = b, tally.Deltas()
	if err := e.commit(ctx, ch); err != nil {
		return nil, err
	}
	held = false // the commit cleared the lease
	switch {
	case runErr != nil && b.Status == StatusHeld:
		e.log("error", "batch held", b, stage, "error", runErr.Error(), "attempt", attempt, "duration_ms", took.Milliseconds())
	case runErr != nil:
		e.log("warn", "stage failed, will retry", b, stage, "error", runErr.Error(), "attempt", attempt, "retry_at", b.NextAttemptAt.Format(time.RFC3339), "duration_ms", took.Milliseconds())
	default:
		e.log("debug", "stage done", b, stage, "attempt", attempt, "duration_ms", took.Milliseconds())
	}
	if b.Status == StatusDelivered {
		e.log("info", "batch delivered", b, stage, "rows", b.Delivered, "seconds", b.FinishedAt.Sub(b.CreatedAt).Seconds())
	}
	return b, nil
}

// commit writes the outcome of a stage that already ran. It uses a context
// that outlives a shutdown: the side effect has happened, so the record of it
// must be written even while the process is stopping.
func (e *Engine) commit(ctx context.Context, ch Change) error {
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	return e.Store.Commit(cctx, ch)
}

// checkContract holds a transform that changed the row count or broke the
// source's output rules, instead of letting wrong data travel on.
func (e *Engine) checkContract(src *Source, in, out []Row, t *Tally) error {
	if !src.AllowRowChange && len(out) != len(in) {
		t.Add("etl_contract_violations_total", 1, "source", src.ID, "rule", "row_count")
		return Permanent(fmt.Errorf("the transform returned %d rows for the %d it was given; it may not add or drop rows", len(out), len(in)))
	}
	if len(src.OutputRules) == 0 {
		return nil
	}
	_, rejects, err := Validate(&Source{Rules: src.OutputRules}, out, e.Eval)
	if err != nil {
		return Permanent(err)
	}
	if len(rejects) > 0 {
		t.Add("etl_contract_violations_total", 1, "source", src.ID, "rule", "output_rules")
		return Permanent(fmt.Errorf("the transform broke the output contract on %d rule checks; first: row %d, %s", len(rejects), rejects[0].RowNo, rejects[0].Reason))
	}
	return nil
}

func (e *Engine) event(b *Batch, at time.Time, stage int, kind, msg string, started time.Time, attempt int) {
	b.Events = append(b.Events, Event{At: at, Stage: stage, Kind: kind, Message: msg, DurationMs: time.Since(started).Milliseconds(), Attempt: attempt})
}

// fail records a stage failure: schedule a retry, or hold the batch. Every
// attempt is kept in b.Failures with its stage, error and what was decided.
func (e *Engine) fail(b *Batch, src *Source, stage int, err error, now time.Time, ch *Change, actor string, started time.Time, attempt int) {
	pol := defaults(src.Retry)
	b.Attempts++
	b.LastError = err.Error()
	f := Failure{At: now, Stage: stage, StageName: StageNames[stage], Attempt: b.Attempts, Max: pol.MaxAttempts, Error: err.Error()}
	var perm permanent
	switch {
	case errors.As(err, &perm):
		f.Outcome, f.Permanent, f.Reason = "held", true, "the error cannot be fixed by retrying"
	case b.Attempts >= pol.MaxAttempts:
		f.Outcome, f.Reason = "held", fmt.Sprintf("all %d attempts used", pol.MaxAttempts)
	}
	span := Event{At: now, Stage: stage, DurationMs: time.Since(started).Milliseconds(), Attempt: attempt}
	if f.Outcome == "held" {
		b.Status = StatusHeld
		b.Failures = append(b.Failures, f)
		span.Kind, span.Message = "held", fmt.Sprintf("Attempt %d of %d failed: %v. Held at the last checkpoint (%s); replay once the cause is fixed", b.Attempts, pol.MaxAttempts, err, f.Reason)
		b.Events = append(b.Events, span)
		ch.Audit = append(ch.Audit, e.audit(actor, "batch.hold", b.ID, b.SourceID, b.TraceID, fmt.Sprintf("stage %d: %v (%s)", stage, err, f.Reason)))
		return
	}
	bo := e.Backoff
	if bo == nil {
		bo = backoff.FullJitter
	}
	d := bo(pol.Base, b.Attempts, pol.Cap)
	b.Status, b.NextAttemptAt = StatusRetrying, now.Add(d)
	f.Outcome, f.RetryAt, f.Reason = "retry", b.NextAttemptAt, fmt.Sprintf("backing off %s before attempt %d of %d", d.Round(time.Millisecond), b.Attempts+1, pol.MaxAttempts)
	b.Failures = append(b.Failures, f)
	span.Kind, span.Message = "retry", fmt.Sprintf("Attempt %d of %d failed: %v. Retrying in %s", b.Attempts, pol.MaxAttempts, err, d.Round(time.Millisecond))
	b.Events = append(b.Events, span)
}

// ---------------------------------------------------------------------------
// Circuit breaker
// ---------------------------------------------------------------------------

// The breaker's state lives in the store, so every process sharing it learns of
// a sick destination from the first one to find out. Each process keeps a
// one-second copy so the check before a call costs no query.

type cachedBreaker struct {
	state BreakerState
	at    time.Time
}

func (e *Engine) breakerSettings() (int, time.Duration) {
	n, d := e.BreakerThreshold, e.BreakerCooldown
	if n <= 0 {
		n = 5
	}
	if d <= 0 {
		d = 30 * time.Second
	}
	return n, d
}

func (e *Engine) breakerState(ctx context.Context, dest string) BreakerState {
	e.brMu.Lock()
	c, ok := e.brCache[dest]
	e.brMu.Unlock()
	if ok && time.Since(c.at) < time.Second {
		return c.state
	}
	states, err := e.Store.Breakers(ctx)
	if err != nil {
		e.log("warn", "could not read circuit breakers", nil, 0, "error", err.Error())
		return c.state // the last known state; closed if there is none
	}
	e.brMu.Lock()
	defer e.brMu.Unlock()
	for _, s := range states {
		e.brCache[s.Destination] = cachedBreaker{s, time.Now()}
	}
	if _, found := e.brCache[dest]; !found {
		e.brCache[dest] = cachedBreaker{BreakerState{Destination: dest}, time.Now()}
	}
	return e.brCache[dest].state
}

// circuitOpen reports whether calls to a destination are paused, and until when.
// After the cool-down one call is let through (half open); its result decides.
func (e *Engine) circuitOpen(ctx context.Context, dest string, now time.Time) (time.Time, bool) {
	st := e.breakerState(ctx, dest)
	if st.OpenUntil.IsZero() || !now.Before(st.OpenUntil) {
		return time.Time{}, false
	}
	return st.OpenUntil, true
}

// circuitRecord counts a result against a destination. Permanent errors are
// data problems, not a sick destination, and do not count.
func (e *Engine) circuitRecord(ctx context.Context, dest string, now time.Time, err error, t *Tally) {
	var perm permanent
	if errors.As(err, &perm) {
		return
	}
	threshold, cooldown := e.breakerSettings()
	e.brMu.Lock()
	before := e.brCache[dest].state
	e.brMu.Unlock()
	if err == nil && before.Failures == 0 {
		return // nothing to close: skip the write on the success path
	}
	st, serr := e.Store.RecordBreaker(context.WithoutCancel(ctx), dest, err == nil, now, threshold, cooldown)
	if serr != nil {
		e.log("warn", "could not record a circuit breaker result", nil, 0, "destination", dest, "error", serr.Error())
		return
	}
	e.brMu.Lock()
	e.brCache[dest] = cachedBreaker{st, time.Now()}
	e.brMu.Unlock()
	wasOpen := !before.OpenUntil.IsZero() && now.Before(before.OpenUntil)
	if !st.OpenUntil.IsZero() && !wasOpen {
		t.Add("etl_circuit_opened_total", 1, "destination", dest)
		e.log("warn", "circuit opened", nil, 0, "destination", dest, "failures", st.Failures, "until", st.OpenUntil.Format(time.RFC3339))
	}
}

// Circuit is the state of one destination's breaker.
type Circuit struct {
	Destination string    `json:"destination"`
	State       string    `json:"state"` // closed, open, half_open
	Failures    int       `json:"failures"`
	OpenUntil   time.Time `json:"open_until,omitempty"`
}

// Circuits lists the breakers that have seen a failure.
func (e *Engine) Circuits(ctx context.Context) []Circuit {
	e.init()
	now := e.now()
	states, err := e.Store.Breakers(ctx)
	out := []Circuit{}
	if err != nil {
		return out
	}
	for _, br := range states {
		if br.Failures == 0 {
			continue
		}
		c := Circuit{Destination: br.Destination, State: "closed", Failures: br.Failures, OpenUntil: br.OpenUntil}
		if !br.OpenUntil.IsZero() {
			c.State = "open"
			if !now.Before(br.OpenUntil) {
				c.State = "half_open"
			}
		}
		out = append(out, c)
	}
	return out
}

// Prune removes the checkpoints of batches that finished more than `age` ago.
// Batch records, quarantine, lineage and the audit trail are kept; held
// batches keep their checkpoints because a replay needs them.
func (e *Engine) Prune(ctx context.Context, age time.Duration) (int, error) {
	e.init()
	if _, cerr := e.Store.PruneCounters(ctx, e.now().Add(-14*24*time.Hour).UTC().Format("2006010215")); cerr != nil {
		e.log("warn", "could not prune hourly counters", nil, 0, "error", cerr.Error())
	}
	n, keys, err := e.Store.Prune(ctx, e.now().Add(-age))
	e.deleteBlobs(context.WithoutCancel(ctx), keys)
	if err == nil && n > 0 {
		t := NewTally()
		t.Add("etl_pruned_checkpoints_total", float64(n))
		e.bump(ctx, t)
		e.log("info", "retention removed checkpoints", nil, 0, "count", n)
	}
	return n, err
}

// Replay restarts a held batch from its last checkpoint with the same
// idempotency key, so a destination that already took part of it does not
// take it twice.
func (e *Engine) Replay(ctx context.Context, actor Actor, id string) (*Batch, error) {
	e.init()
	b, err := e.Store.GetBatch(ctx, id)
	if err != nil {
		if _, perr := e.need(ctx, actor, PermReplay, ""); perr != nil {
			return nil, perr
		}
		return nil, err
	}
	if _, err := e.need(ctx, actor, PermReplay, b.SourceID); err != nil {
		return nil, err
	}
	if b.Status != StatusHeld {
		return b, fmt.Errorf("%w: only held batches can be replayed; this one is %s", ErrState, b.Status)
	}
	cp, err := e.Store.LatestCheckpoint(ctx, id)
	if err != nil {
		return nil, err
	}
	now := e.now()
	b.Status, b.Attempts, b.LastError, b.Stage, b.UpdatedAt, b.NextAttemptAt = StatusInFlight, 0, "", cp.Stage+1, now, time.Time{}
	b.Events = append(b.Events, Event{At: now, Stage: cp.Stage + 1, Kind: "replay", Message: fmt.Sprintf("Replayed from the stage %d checkpoint by %s", cp.Stage, actor.ID)})
	t := NewTally()
	t.Add("etl_replays_total", 1, "source", b.SourceID)
	err = e.Store.Commit(ctx, Change{Batch: b, Counters: t.Deltas(), Audit: []AuditEntry{e.audit(actor.ID, "batch.replay", id, b.SourceID, b.TraceID, fmt.Sprintf("from checkpoint of stage %d", cp.Stage))}})
	if err == nil {
		e.log("info", "batch replayed", b, cp.Stage+1, "actor", actor.ID, "checkpoint_stage", cp.Stage)
	}
	return b, err
}

// RunAll advances a batch until it is delivered, waiting, held or failed.
func (e *Engine) RunAll(ctx context.Context, actor Actor, id string) (*Batch, error) {
	for {
		before := 0
		if cur, err := e.Store.GetBatch(ctx, id); err == nil {
			before = cur.Stage
		}
		b, err := e.Advance(ctx, actor, id)
		if err != nil {
			return b, err
		}
		// Stop when it is done, or when a stage did not move it on (it is
		// waiting for an open circuit, for example).
		if b.Status != StatusInFlight || b.Stage == before {
			return b, nil
		}
	}
}

// Sweep advances every batch that is due (in flight, or retrying whose time has
// come) one stage and returns how many it touched. Run it on a timer until it
// returns 0; each call records a heartbeat.
func (e *Engine) Sweep(ctx context.Context, limit int) (int, error) {
	e.init()
	e.heartbeat.Store(time.Now().UnixNano())
	e.checkWedged()
	due, err := e.Store.Due(ctx, e.now(), limit)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, b := range due {
		if _, err := e.advance(ctx, "system:sweeper", b); err == nil || errors.Is(err, ErrConflict) {
			n++
		}
	}
	return n, nil
}

// Beat records that a sweeper is alive without sweeping.
func (e *Engine) Beat() { e.heartbeat.Store(time.Now().UnixNano()) }

// ---------------------------------------------------------------------------
// Reads
// ---------------------------------------------------------------------------

func (e *Engine) Summary(ctx context.Context, actor Actor) (Summary, error) {
	acc, err := e.need(ctx, actor, PermRead, "")
	if err != nil {
		return Summary{}, err
	}
	return e.Store.Summary(ctx, acc.Sources(PermRead))
}

func (e *Engine) Batches(ctx context.Context, actor Actor, q Query) ([]*Batch, error) {
	acc, err := e.need(ctx, actor, PermRead, "")
	if err != nil {
		return nil, err
	}
	ids, none := intersect(q.SourceID, acc.Sources(PermRead))
	if none {
		return []*Batch{}, nil
	}
	q.SourceIDs, q.SourceID = ids, ""
	return e.Store.ListBatches(ctx, q)
}

func (e *Engine) Sources(ctx context.Context, actor Actor) ([]*Source, error) {
	acc, err := e.Access(ctx, actor)
	if err != nil {
		return nil, err
	}
	var all []*Source
	if all, err = e.Store.ListSources(ctx); err != nil {
		return nil, err
	}
	out := []*Source{}
	for _, s := range all {
		for _, p := range []string{PermRead, PermIngest, PermManage, PermMonitor} {
			if acc.Allows(p, s.ID) && acc.Has(p) {
				out = append(out, s)
				break
			}
		}
	}
	if len(out) == 0 && !acc.Has(PermRead) && !acc.Has(PermIngest) && !acc.Has(PermManage) && !acc.Has(PermMonitor) {
		return nil, ErrForbidden
	}
	return out, nil
}

// Resume says where a replay of the batch would pick up: after Stage, with Rows
// rows that match Hash.
type Resume struct {
	Stage     int       `json:"stage"`
	NextStage int       `json:"next_stage"`
	Rows      int       `json:"rows"`
	Hash      string    `json:"hash"`
	At        time.Time `json:"at"`
}

// Trace is a batch with everything known about its journey.
type Trace struct {
	Batch      *Batch
	Resume     *Resume // where a replay would pick up; nil before the first checkpoint
	Lineage    []LineageEdge
	Quarantine []Quarantine
	Audit      []AuditEntry
	Logs       []LogEntry
}

func (e *Engine) Trace(ctx context.Context, actor Actor, id string) (*Trace, error) {
	e.init()
	b, err := e.Store.GetBatch(ctx, id)
	if err != nil {
		if _, perr := e.need(ctx, actor, PermRead, ""); perr != nil {
			return nil, perr
		}
		return nil, err
	}
	if _, err := e.need(ctx, actor, PermRead, b.SourceID); err != nil {
		return nil, err
	}
	t := &Trace{Batch: b}
	if cp, cerr := e.Store.LatestCheckpoint(ctx, id); cerr == nil {
		rows := len(cp.Rows)
		if len(cp.Chunks) > 0 {
			rows = cp.Count
		}
		t.Resume = &Resume{Stage: cp.Stage, NextStage: cp.Stage + 1, Rows: rows, Hash: cp.Hash, At: cp.At}
	}
	if t.Lineage, err = e.Store.Lineage(ctx, id); err != nil {
		return nil, err
	}
	if t.Quarantine, err = e.Store.ListQuarantine(ctx, id, nil, 500); err != nil {
		return nil, err
	}
	if t.Audit, err = e.Store.ListAudit(ctx, id, nil, 200, 0); err != nil {
		return nil, err
	}
	t.Logs = e.Logs.Recent(LogFilter{BatchID: id, Limit: 100})
	return t, nil
}

func (e *Engine) Quarantined(ctx context.Context, actor Actor, limit int) ([]Quarantine, error) {
	acc, err := e.need(ctx, actor, PermRead, "")
	if err != nil {
		return nil, err
	}
	return e.Store.ListQuarantine(ctx, "", acc.Sources(PermRead), limit)
}

func (e *Engine) Audit(ctx context.Context, actor Actor, limit, offset int) ([]AuditEntry, error) {
	acc, err := e.need(ctx, actor, PermAudit, "")
	if err != nil {
		return nil, err
	}
	return e.Store.ListAudit(ctx, "", acc.Sources(PermAudit), limit, offset)
}

// VerifyAudit checks the whole chain, so it needs the audit permission on every source.
func (e *Engine) VerifyAudit(ctx context.Context, actor Actor) (badSeq, total int64, err error) {
	acc, err := e.need(ctx, actor, PermAudit, "")
	if err != nil {
		return 0, 0, err
	}
	if acc.Sources(PermAudit) != nil {
		return 0, 0, ErrForbidden
	}
	return e.Store.VerifyAudit(ctx)
}

// wedgedFor says how long hooks have been stuck (zero when none are).
func (e *Engine) wedgedFor() time.Duration {
	at := e.abandonAt.Load()
	if at == 0 || e.abandon.Load() == 0 {
		return 0
	}
	return time.Since(time.Unix(0, at))
}

func (e *Engine) wedgeAfter() time.Duration {
	if e.WedgeAfter > 0 {
		return e.WedgeAfter
	}
	return 2 * time.Minute
}

// checkWedged tells OnWedged, once per episode, that hooks have been stuck too long.
func (e *Engine) checkWedged() {
	if d := e.wedgedFor(); d >= e.wedgeAfter() && e.wedged.CompareAndSwap(false, true) {
		n := int(e.abandon.Load())
		e.log("error", "hooks are stuck and cannot be stopped; the process should be restarted", nil, 0, "abandoned", n, "for", d.String())
		if e.OnWedged != nil {
			e.OnWedged(n, d)
		}
	}
}
