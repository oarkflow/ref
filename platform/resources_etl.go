package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/oarkflow/ref/etl"
	"github.com/oarkflow/ref/intent"
	"github.com/oarkflow/ref/platform/spi"
)

// ETLEngine is the etl.engine resource: registered data sources, the six-stage
// run engine (validate, transform, transfer, deliver, audit, lineage) with
// checkpoints, retry, hold and replay, a hash-chained audit trail, and the
// store that keeps it all (memory, or any database.sql: SQLite, PostgreSQL or
// MySQL).
//
// What a transform, a transfer or a delivery does is not decided here. Each is
// an intent named in the resource's config; the engine calls it with the rows
// and treats an error, or a result with ok:false, as a failed attempt.
type ETLEngine struct {
	name      string
	engine    *etl.Engine
	roleMap   map[string][]string
	poll      time.Duration
	retention time.Duration
	alertPoll time.Duration
	platform  atomic.Pointer[Platform]
	// db and principalQuery re-read a person's roles and status from the
	// application's user table on every request (cached for a moment), so a
	// disabled account or a changed role takes effect without waiting for the
	// session to end.
	db             *Database
	principalQuery string
	liveMu         sync.Mutex
	live           map[string]liveUser
}

type liveUser struct {
	roles  []string
	active bool
	pwv    string // the account's password version: it changes when the password does
	at     time.Time
}

// Why an identity was refused.
const (
	idOK       = iota
	idDisabled // the account is disabled, removed, or its password changed since this session began
	idMFA      // signed in with a password but the second factor has not been given yet
)

const liveTTL = 2 * time.Second

func registerETLResources(r *Registry) {
	mustResource(r, "etl.engine", ResourceFactoryFunc(openETLEngine), ResourceKindInfo{
		Family:   "data",
		Summary:  "Data pipelines: source registry and validation, staged runs with checkpoints, retry, hold and replay, roles, audit trail and lineage",
		Provides: []string{"ETLEngine"},
		Config: []ConfigField{
			{Name: "database", Type: "string", Summary: "database.sql resource (SQLite, PostgreSQL or MySQL); omit for in-memory storage"},
			{Name: "table_prefix", Type: "string", Default: "etl_"},
			{Name: "migrate", Type: "bool", Default: "true", Summary: "Create the tables at startup"},
			{Name: "sources", Type: "[]map", Summary: "Sources to register at startup when they do not exist yet (id, name, owner, format, destination, rules, max_reject_rate, retry)"},
			{Name: "transform", Type: "intent", Summary: "Intent that transforms rows: input {source, batch, rows}, output {rows, version}"},
			{Name: "transfer", Type: "intent", Summary: "Intent that moves rows to the receiving system: input {source, batch, rows}; ok:false or an error fails the attempt"},
			{Name: "deliver", Type: "intent", Summary: "Intent that puts rows on the destination: input {source, batch, rows}, output {ref}. batch.delivery_key is the idempotency key of the call (the batch key, or key#chunk for a chunked batch) and batch.epoch a fencing token that only rises: a destination that records the highest epoch per key can refuse a late call from a worker that lost its lease"},
			{Name: "verify", Type: "intent", Summary: "Intent that asks the destination whether it already has batch.delivery_key: input as deliver, output {found, ref}. Called only when a delivery call was interrupted and its outcome is unknown, so a batch that did arrive is recorded and not sent again. Without it an interrupted call is repeated"},
			{Name: "blobs", Type: "string", Summary: "storage.fs / storage.sql resource that holds the chunks of large batches and the files ingested by name; without it a batch is held whole in the database and capped by max_rows"},
			{Name: "chunk_rows", Type: "int", Default: "5000", Summary: "Rows per chunk when blobs is set: the most a hook sees in one call, and so the memory a batch needs however big it is"},
			{Name: "inbox_prefix", Type: "string", Default: "inbox/", Summary: "Where files to ingest by name (object) must be in the blob store"},
			{Name: "stage_grace", Type: "duration", Default: "2s", Summary: "How long a hook whose timeout passed is given to unwind after its context is cancelled before it is counted as abandoned"},
			{Name: "wedge_after", Type: "duration", Default: "2m", Summary: "How long an abandoned hook may keep running before health reports the process as down so it is restarted"},
			{Name: "principal_query", Type: "sql", Summary: "Takes the principal id as $1 and returns (roles, status[, version]): roles (comma separated role ids), status ('active' to allow) and optionally a password version; a session whose pw_changed_ms claim differs from the version is ended. Without it the roles in the session or key are trusted for the life of the session"},
			{Name: "role_map", Type: "map", Summary: "Maps a principal role to etl roles (admin, ingest, operate, replay, read); roles not listed are used as they are"},
			{Name: "poll", Type: "duration", Default: "1s", Summary: "How often the sweeper looks for batches that are due"},
			{Name: "stage_delay", Type: "duration", Default: "0s", Summary: "Pause between stages of one batch (makes progress visible in a demo)"},
			{Name: "stage_timeout", Type: "duration", Default: "30s", Summary: "Longest one hook call may take before it counts as a failed attempt"},
			{Name: "lease_ttl", Type: "duration", Default: "90s", Summary: "How long a worker owns a batch's stage; the time to recover from a worker that died mid-stage"},
			{Name: "breaker_threshold", Type: "int", Default: "5", Summary: "Consecutive failures that open a destination's circuit"},
			{Name: "breaker_cooldown", Type: "duration", Default: "30s", Summary: "How long an open circuit pauses calls to its destination"},
			{Name: "notify", Type: "intent", Summary: "Intent told when an alert opens or clears: input {event, alert{id, severity, kind, source_id, batch_id, title, message, since}}; a failure is retried"},
			{Name: "alert_poll", Type: "duration", Default: "10s", Summary: "How often alerts are worked out, recorded and announced"},
			{Name: "retention", Type: "duration", Default: "720h", Summary: "Checkpoints of finished batches are deleted after this (0 keeps them); held batches keep theirs"},
		},
	})
}

func openETLEngine(ctx context.Context, spec ResourceSpec) (Resource, io.Closer, error) {
	if err := rejectUnknownConfig("etl.engine", spec.Config, "database", "table_prefix", "migrate", "sources", "transform", "transfer",
		"deliver", "verify", "blobs", "chunk_rows", "inbox_prefix", "stage_grace", "wedge_after", "principal_query", "role_map", "poll", "stage_delay", "stage_timeout", "lease_ttl", "breaker_threshold", "breaker_cooldown", "retention", "notify", "alert_poll"); err != nil {
		return nil, nil, err
	}
	poll, err := configDuration(spec.Config, "poll", time.Second)
	if err != nil {
		return nil, nil, err
	}
	delay, err := configDuration(spec.Config, "stage_delay", 0)
	if err != nil {
		return nil, nil, err
	}
	stageTimeout, err := configDuration(spec.Config, "stage_timeout", 30*time.Second)
	if err != nil {
		return nil, nil, err
	}
	leaseTTL, err := configDuration(spec.Config, "lease_ttl", 0)
	if err != nil {
		return nil, nil, err
	}
	cooldown, err := configDuration(spec.Config, "breaker_cooldown", 30*time.Second)
	if err != nil {
		return nil, nil, err
	}
	threshold, err := configInt(spec.Config, "breaker_threshold", 5)
	if err != nil {
		return nil, nil, err
	}
	retention, err := configDuration(spec.Config, "retention", 720*time.Hour)
	if err != nil {
		return nil, nil, err
	}
	alertPoll, err := configDuration(spec.Config, "alert_poll", 10*time.Second)
	if err != nil {
		return nil, nil, err
	}
	res := &ETLEngine{alertPoll: alertPoll, retention: retention, name: spec.Name, poll: max(poll, 10*time.Millisecond), roleMap: map[string][]string{}}
	if m, ok := spec.Config["role_map"].(map[string]any); ok {
		for from, to := range m {
			switch v := to.(type) {
			case string:
				res.roleMap[from] = []string{v}
			case []any:
				for _, x := range v {
					res.roleMap[from] = append(res.roleMap[from], Stringify(x))
				}
			}
		}
	}

	var store etl.Store
	if q := configString(spec.Config, "principal_query", ""); q != "" {
		if configString(spec.Config, "database", "") == "" {
			return nil, nil, fmt.Errorf("etl.engine %q: principal_query needs a database", spec.Name)
		}
		pdb, err := requireSQLHandle(spec, "database")
		if err != nil {
			return nil, nil, err
		}
		if err := pdb.CheckStatement(q); err != nil {
			return nil, nil, fmt.Errorf("etl.engine %q: principal_query: %w", spec.Name, err)
		}
		res.db, res.principalQuery, res.live = pdb, q, map[string]liveUser{}
	}
	if configString(spec.Config, "database", "") == "" {
		store = etl.NewMemoryStore()
	} else {
		db, err := requireSQLHandle(spec, "database")
		if err != nil {
			return nil, nil, err
		}
		sqlStore, err := etl.NewSQLStore(db.DB, db.Dialect, configString(spec.Config, "table_prefix", "etl_"))
		if err != nil {
			return nil, nil, fmt.Errorf("etl.engine %q: %w", spec.Name, err)
		}
		if configBool(spec.Config, "migrate", true) {
			if err := sqlStore.Migrate(ctx); err != nil {
				return nil, nil, fmt.Errorf("etl.engine %q: migrate: %w", spec.Name, err)
			}
		}
		store = sqlStore
	}
	grace, err := configDuration(spec.Config, "stage_grace", 2*time.Second)
	if err != nil {
		return nil, nil, err
	}
	wedge, err := configDuration(spec.Config, "wedge_after", 2*time.Minute)
	if err != nil {
		return nil, nil, err
	}
	chunkRows, err := configInt(spec.Config, "chunk_rows", 5000)
	if err != nil {
		return nil, nil, err
	}
	var blobs etl.Blobs
	if name := configString(spec.Config, "blobs", ""); name != "" {
		resolved, ok := spec.resolved[name]
		if !ok {
			return nil, nil, fmt.Errorf("etl.engine %q: config.blobs names unknown resource %q", spec.Name, name)
		}
		os, ok := resolved.(spi.ObjectStore)
		if !ok {
			return nil, nil, fmt.Errorf("etl.engine %q: resource %q is not a storage resource", spec.Name, name)
		}
		blobs = objectBlobs{os}
	}
	eval := &pipelineEvaluator{}
	res.engine = &etl.Engine{Store: store, Blobs: blobs, ChunkRows: chunkRows, InboxPrefix: configString(spec.Config, "inbox_prefix", "inbox/"), Grace: grace, WedgeAfter: wedge, StageDelay: delay, StageTimeout: stageTimeout, LeaseTTL: leaseTTL, BreakerThreshold: threshold, BreakerCooldown: cooldown, Eval: func(expr string, row etl.Row) (bool, error) {
		// Text that reads as a number is a number in expressions, so a CSV
		// column can be compared without a cast: amount < 1000.
		env := make(map[string]any, len(row))
		for k, v := range row {
			if text, ok := v.(string); ok {
				if f, err := strconv.ParseFloat(strings.TrimSpace(text), 64); err == nil {
					v = f
				}
			}
			env[k] = v
		}
		v, err := eval.Eval(expr, env)
		return Truthy(v), err
	}}
	if intentName := configString(spec.Config, "notify", ""); intentName != "" {
		res.engine.Notify = func(ctx context.Context, event string, a etl.AlertRecord) error {
			p := res.platform.Load()
			if p == nil {
				return errors.New("the platform is not running")
			}
			nctx, cancel := context.WithTimeout(ctx, 20*time.Second)
			defer cancel()
			_, err := p.CallIntent(nctx, intentName, map[string]any{"event": event, "alert": map[string]any{
				"id": a.ID, "severity": a.Severity, "kind": a.Kind, "source_id": a.SourceID, "batch_id": a.BatchID,
				"title": a.Title, "message": a.Message, "since": a.Since.Format(time.RFC3339), "rid": a.RID}}, nil)
			return err
		}
	}
	if err := res.engine.EnsureRoles(ctx); err != nil {
		return nil, nil, fmt.Errorf("etl.engine %q: roles: %w", spec.Name, err)
	}
	res.engine.CheckExpr = func(expr string) error { _, err := CompileExpr(expr); return err }
	res.engine.Hooks = res.hooks(configString(spec.Config, "transform", ""), configString(spec.Config, "transfer", ""), configString(spec.Config, "deliver", ""))
	res.engine.Hooks.Verify = res.verifyHook(configString(spec.Config, "verify", ""))

	if raw, ok := spec.Config["sources"].([]any); ok {
		system := etl.Actor{ID: "system:" + spec.Name, Roles: []string{etl.RoleAdmin}}
		for i, item := range raw {
			b, _ := json.Marshal(item)
			var s etl.Source
			if err := json.Unmarshal(b, &s); err != nil {
				return nil, nil, fmt.Errorf("etl.engine %q: sources[%d]: %w", spec.Name, i, err)
			}
			if _, err := store.GetSource(ctx, s.ID); err == nil {
				continue // an operator may have edited it since
			} else if !errors.Is(err, etl.ErrNotFound) {
				return nil, nil, err
			}
			if _, err := res.engine.PutSource(ctx, system, s); err != nil {
				return nil, nil, fmt.Errorf("etl.engine %q: source %q: %w", spec.Name, s.ID, err)
			}
		}
	}
	return res, nil, nil
}

// hooks wires the engine's stage hooks to intents. An empty name passes rows through.
func (r *ETLEngine) hooks(transform, transfer, deliver string) etl.Hooks {
	call := func(ctx context.Context, name string, src *etl.Source, b *etl.Batch, rows []etl.Row) (map[string]any, error) {
		p := r.platform.Load()
		if p == nil {
			return nil, errors.New("the platform is not running")
		}
		hctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		out, err := p.CallIntent(hctx, name, map[string]any{
			"source": map[string]any{"id": src.ID, "name": src.Name, "owner": src.Owner, "destination": src.Destination, "version": src.Version},
			"batch": map[string]any{"id": b.ID, "key": b.Key, "source_id": b.SourceID, "attempt": b.Attempts + 1, "trace_id": b.TraceID,
				"delivery_key": b.DeliveryKey, "epoch": b.Epoch, "chunk": b.Chunk, "chunks": max(b.ChunkCount, 1)},
			"rows": rows,
		}, nil)
		if err != nil {
			var f intent.Failure
			if errors.As(err, &f) && f.Code != "" {
				return nil, fmt.Errorf("%s: %s [%s]", name, f.Message, f.Code)
			}
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		m, _ := out.(map[string]any)
		if m == nil {
			m = map[string]any{}
		}
		for _, k := range []string{"ok", "passed"} {
			if v, present := m[k]; present && !Truthy(v) {
				msg := Stringify(m["error"])
				if msg == "" || msg == "<nil>" {
					msg = "refused"
				}
				err := fmt.Errorf("%s: %s", name, msg)
				if Truthy(m["permanent"]) {
					return nil, etl.Permanent(err)
				}
				return nil, err
			}
		}
		return m, nil
	}
	var h etl.Hooks
	if transform != "" {
		h.Transform = func(ctx context.Context, src *etl.Source, b *etl.Batch, rows []etl.Row) ([]etl.Row, string, error) {
			m, err := call(ctx, transform, src, b, rows)
			if err != nil {
				return nil, "", err
			}
			version := Stringify(m["version"])
			if version == "" || version == "<nil>" {
				version = transform
			}
			raw, ok := m["rows"].([]any)
			if !ok {
				return rows, version, nil
			}
			out := make([]etl.Row, 0, len(raw))
			for _, item := range raw {
				row, ok := item.(map[string]any)
				if !ok {
					return nil, "", etl.Permanent(fmt.Errorf("%s: rows must be objects", transform))
				}
				out = append(out, etl.Row(row))
			}
			return out, version, nil
		}
	}
	if transfer != "" {
		h.Transfer = func(ctx context.Context, src *etl.Source, b *etl.Batch, rows []etl.Row) error {
			_, err := call(ctx, transfer, src, b, rows)
			return err
		}
	}
	if deliver != "" {
		h.Deliver = func(ctx context.Context, src *etl.Source, b *etl.Batch, rows []etl.Row) (string, error) {
			m, err := call(ctx, deliver, src, b, rows)
			if err != nil {
				return "", err
			}
			if ref := Stringify(m["ref"]); ref != "" && ref != "<nil>" {
				return ref, nil
			}
			return b.ID, nil
		}
	}
	return h
}

// identity is the caller as the engine sees it: who they are and the roles
// they hold right now. The second result says why access is refused:
//
//   - the account was disabled or removed, or the person changed their password
//     after this session began (every other session of theirs ends then);
//   - the person has a second factor and has not given it in this session.
func (r *ETLEngine) identity(ctx context.Context, p Principal) (etl.Actor, int) {
	if p.ID == "" {
		return etl.Actor{}, idOK
	}
	roles := p.Roles
	if r.principalQuery != "" {
		u, err := r.lookup(ctx, p.ID)
		if err != nil {
			slog.Warn("etl principal lookup failed", "resource", r.name, "error", err)
			return etl.Actor{ID: p.ID}, idOK // no roles: fail closed, but do not log the person out over a database blip
		}
		if !u.active {
			return etl.Actor{}, idDisabled
		}
		if claim, has := p.Claims["pw_changed_ms"]; has && u.pwv != "" && versionText(claim) != u.pwv {
			return etl.Actor{}, idDisabled
		}
		roles = u.roles
	}
	if Truthy(p.Claims["mfa_enabled"]) && !Truthy(p.Claims["mfa_ok"]) {
		return etl.Actor{ID: p.ID}, idMFA
	}
	a := etl.Actor{ID: p.ID}
	for _, role := range roles {
		if mapped, found := r.roleMap[role]; found {
			a.Roles = append(a.Roles, mapped...)
		} else {
			a.Roles = append(a.Roles, role)
		}
	}
	return a, idOK
}

// versionText renders a stored version (a number that went through JSON as a
// float, or text) the same way however it arrived.
func versionText(v any) string {
	switch x := v.(type) {
	case float64:
		return strconv.FormatInt(int64(x), 10)
	case int64:
		return strconv.FormatInt(x, 10)
	case int:
		return strconv.Itoa(x)
	case nil:
		return ""
	}
	return strings.TrimSpace(Stringify(v))
}

func (r *ETLEngine) lookup(ctx context.Context, id string) (liveUser, error) {
	r.liveMu.Lock()
	if u, ok := r.live[id]; ok && time.Since(u.at) < liveTTL {
		r.liveMu.Unlock()
		return u, nil
	}
	r.liveMu.Unlock()
	rows, err := r.db.QueryContext(ctx, r.principalQuery, id)
	if err != nil {
		return liveUser{}, err
	}
	defer rows.Close()
	u := liveUser{at: time.Now()}
	if rows.Next() {
		var roles, status string
		var pwv any
		var err error
		if cols, _ := rows.Columns(); len(cols) >= 3 {
			err = rows.Scan(&roles, &status, &pwv)
		} else {
			err = rows.Scan(&roles, &status)
		}
		if err != nil {
			return liveUser{}, err
		}
		u.pwv = versionText(pwv)
		if b, ok := pwv.([]byte); ok {
			u.pwv = strings.TrimSpace(string(b))
		}
		u.active = status == "active"
		for _, role := range strings.Split(roles, ",") {
			if role = strings.TrimSpace(role); role != "" {
				u.roles = append(u.roles, role)
			}
		}
	}
	if err := rows.Err(); err != nil {
		return liveUser{}, err
	}
	r.liveMu.Lock()
	r.live[id] = u
	r.liveMu.Unlock()
	return u, nil
}

// runBackground sweeps due batches: it is what moves a batch from one stage to
// the next and what retries a failed one when its backoff has passed.
func (r *ETLEngine) runBackground(ctx context.Context, p *Platform) {
	r.platform.Store(p)
	ticker := time.NewTicker(r.poll)
	defer ticker.Stop()
	lastPrune, lastAlerts := time.Time{}, time.Time{}
	for {
		if time.Since(lastAlerts) >= r.alertPoll {
			lastAlerts = time.Now()
			if err := r.engine.EvaluateAlerts(ctx); err != nil && ctx.Err() == nil {
				slog.Warn("etl alert evaluation failed", "resource", r.name, "error", err)
			}
		}
		if r.retention > 0 && time.Since(lastPrune) > time.Hour {
			lastPrune = time.Now()
			if _, err := r.engine.Prune(ctx, r.retention); err != nil && ctx.Err() == nil {
				slog.Warn("etl retention failed", "resource", r.name, "error", err)
			}
		}
		for {
			n, err := r.engine.Sweep(ctx, 50)
			if err != nil && ctx.Err() == nil {
				slog.Warn("etl sweep failed", "resource", r.name, "error", err)
			}
			if n == 0 || err != nil || ctx.Err() != nil {
				break
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// objectBlobs lets any storage resource hold the engine's blobs.
type objectBlobs struct{ store spi.ObjectStore }

func (o objectBlobs) Put(ctx context.Context, key string, data []byte) error {
	_, err := o.store.Put(ctx, key, bytes.NewReader(data), "application/octet-stream")
	return err
}

func (o objectBlobs) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	rc, _, err := o.store.Get(ctx, key)
	if err != nil {
		return nil, etlNotFound(err)
	}
	return rc, nil
}

func (o objectBlobs) Get(ctx context.Context, key string) ([]byte, error) {
	rc, err := o.Open(ctx, key)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

func (o objectBlobs) Delete(ctx context.Context, key string) error { return o.store.Delete(ctx, key) }

// etlNotFound maps a storage "not found" to the engine's.
func etlNotFound(err error) error {
	var f intent.Failure
	if errors.As(err, &f) && f.Category == intent.CategoryNotFound {
		return etl.ErrNotFound
	}
	return err
}

// verifyHook asks the verify intent whether the destination already has what an
// interrupted delivery call was sending.
func (r *ETLEngine) verifyHook(name string) func(context.Context, *etl.Source, *etl.Batch, []etl.Row) (string, bool, error) {
	if name == "" {
		return nil
	}
	return func(ctx context.Context, src *etl.Source, b *etl.Batch, rows []etl.Row) (string, bool, error) {
		p := r.platform.Load()
		if p == nil {
			return "", false, errors.New("the platform is not running")
		}
		hctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		out, err := p.CallIntent(hctx, name, map[string]any{
			"source": map[string]any{"id": src.ID, "destination": src.Destination, "version": src.Version},
			"batch": map[string]any{"id": b.ID, "key": b.Key, "source_id": b.SourceID, "trace_id": b.TraceID,
				"delivery_key": b.DeliveryKey, "epoch": b.Epoch, "chunk": b.Chunk, "chunks": max(b.ChunkCount, 1)},
			"rows": len(rows),
		}, nil)
		if err != nil {
			return "", false, fmt.Errorf("%s: %w", name, err)
		}
		m, _ := out.(map[string]any)
		if m == nil || !Truthy(m["found"]) {
			return "", false, nil
		}
		ref := Stringify(m["ref"])
		if ref == "" || ref == "<nil>" {
			ref = b.ID
		}
		return ref, true, nil
	}
}
