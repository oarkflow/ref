package platform

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"
)

// Durable entity hooks. A hook declared `durable true` is not called after
// the change: it is recorded in the entity's database in the same
// transaction as the change, and a background dispatcher delivers it with
// retries and exponential backoff until it succeeds or is dead-lettered. A
// change is therefore never committed without its hook, and a hook never
// runs for a change that rolled back. Delivery is at-least-once, so a
// durable hook must be idempotent (its input carries a stable event_id).

const entityEventsTable = "ref_entity_events"

// entityEventsDDL creates the outbox table on a dialect.
func entityEventsDDL(dialect string) []string {
	key, text, ifNotExists := "TEXT", "TEXT", "IF NOT EXISTS "
	if dialect == "mysql" {
		key, text, ifNotExists = "VARCHAR(191)", "LONGTEXT", ""
	}
	t := entityEventsTable
	return []string{
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (id %s PRIMARY KEY, entity %[2]s NOT NULL, event %[2]s NOT NULL,
			hook %[2]s NOT NULL, payload %s NOT NULL, attempts INTEGER NOT NULL DEFAULT 0, max_attempts INTEGER NOT NULL,
			retry_base_ms BIGINT NOT NULL, next_at BIGINT NOT NULL, lease_token %[2]s, lease_until BIGINT NOT NULL DEFAULT 0,
			dead INTEGER NOT NULL DEFAULT 0, last_error %[3]s, created_at BIGINT NOT NULL)`, t, key, text),
		fmt.Sprintf("CREATE INDEX %s%s_due_idx ON %s (dead, next_at)", ifNotExists, t, t),
	}
}

// EntityEvent is one recorded durable hook call.
type EntityEvent struct {
	ID          string         `json:"id"`
	Entity      string         `json:"entity"`
	Event       string         `json:"event"`
	Hook        string         `json:"hook"`
	Input       map[string]any `json:"input"`
	Attempts    int            `json:"attempts"`
	MaxAttempts int            `json:"max_attempts"`
	NextAt      time.Time      `json:"next_at"`
	Dead        bool           `json:"dead"`
	LastError   string         `json:"last_error,omitempty"`
	CreatedAt   time.Time      `json:"created_at"`

	retryBase time.Duration
	lease     string
}

// entityEvents is a database's durable hook outbox and its dispatcher.
type entityEvents struct {
	db    *Database
	nudge chan struct{}
}

// enableEntityEvents turns on the outbox of a database (idempotent).
func (d *Database) enableEntityEvents() *entityEvents {
	d.eventsMu.Lock()
	defer d.eventsMu.Unlock()
	if d.events == nil {
		d.events = &entityEvents{db: d, nudge: make(chan struct{}, 1)}
	}
	return d.events
}

func (o *entityEvents) poke() {
	select {
	case o.nudge <- struct{}{}:
	default:
	}
}

// execer is what a durable write runs its statements on: the pool or a
// transaction.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// record inserts events inside the caller's transaction. createdAt orders
// them (commit time plus position).
func (o *entityEvents) record(ctx context.Context, tx execer, events []EntityEvent) error {
	base := time.Now().UnixNano()
	for i, ev := range events {
		payload, err := json.Marshal(ev.Input)
		if err != nil {
			return fmt.Errorf("the hook input cannot be serialised: %w", err)
		}
		stmt := fmt.Sprintf(`INSERT INTO %s (id, entity, event, hook, payload, attempts, max_attempts, retry_base_ms, next_at,
			lease_until, dead, created_at) VALUES ($1, $2, $3, $4, $5, 0, $6, $7, $8, 0, 0, $9)`, entityEventsTable)
		if _, err := tx.ExecContext(ctx, rebind(o.db.Dialect, stmt), ev.ID, ev.Entity, ev.Event, ev.Hook, string(payload),
			ev.MaxAttempts, ev.retryBase.Milliseconds(), base, base+int64(i)); err != nil {
			return err
		}
	}
	return nil
}

const entityEventColumns = "id, entity, event, hook, payload, attempts, max_attempts, retry_base_ms, next_at, dead, last_error, created_at"

func scanEntityEvents(rows *sql.Rows) ([]EntityEvent, error) {
	defer rows.Close()
	var out []EntityEvent
	for rows.Next() {
		var (
			ev             EntityEvent
			payload        string
			base, next, at int64
			dead           int
			lastError      sql.NullString
		)
		if err := rows.Scan(&ev.ID, &ev.Entity, &ev.Event, &ev.Hook, &payload, &ev.Attempts, &ev.MaxAttempts, &base,
			&next, &dead, &lastError, &at); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(payload), &ev.Input); err != nil {
			return nil, err
		}
		ev.retryBase = time.Duration(base) * time.Millisecond
		ev.NextAt, ev.CreatedAt = time.Unix(0, next).UTC(), time.Unix(0, at).UTC()
		ev.Dead, ev.LastError = dead != 0, lastError.String
		out = append(out, ev)
	}
	return out, rows.Err()
}

// claim leases up to limit due events to this dispatcher: another replica
// skips them until the lease expires.
func (o *entityEvents) claim(ctx context.Context, limit int, lease time.Duration, now time.Time) ([]EntityEvent, error) {
	token := newPrefixedID("lease")
	t := entityEventsTable
	// The outer condition repeats the lease test: PostgreSQL re-checks it on
	// a row another dispatcher leased meanwhile (the IN list alone is not),
	// so two replicas never lease the same event.
	stmt := fmt.Sprintf(`UPDATE %s SET lease_token = $1, lease_until = $2 WHERE id IN (SELECT id FROM (SELECT id FROM %s
		WHERE dead = 0 AND next_at <= $3 AND lease_until <= $4 ORDER BY created_at, id LIMIT $5) due) AND dead = 0 AND lease_until <= $6`, t, t)
	// Each placeholder is used once: rebind turns $n into MySQL's positional
	// ?, so a repeated $n would need its argument repeated too. The outer
	// condition repeats the lease check: PostgreSQL re-checks only it on a row
	// another dispatcher leased while this UPDATE waited.
	if _, err := o.db.ExecContext(ctx, rebind(o.db.Dialect, stmt), token, now.Add(lease).UnixNano(), now.UnixNano(), now.UnixNano(), limit, now.UnixNano()); err != nil {
		return nil, err
	}
	rows, err := o.db.QueryContext(ctx, rebind(o.db.Dialect, fmt.Sprintf(
		"SELECT %s FROM %s WHERE lease_token = $1 ORDER BY created_at, id", entityEventColumns, t)), token)
	if err != nil {
		return nil, err
	}
	events, err := scanEntityEvents(rows)
	for i := range events {
		events[i].lease = token
	}
	return events, err
}

func (o *entityEvents) ack(ctx context.Context, ev EntityEvent) error {
	_, err := o.db.ExecContext(ctx, rebind(o.db.Dialect, fmt.Sprintf(
		"DELETE FROM %s WHERE id = $1 AND lease_token = $2", entityEventsTable)), ev.ID, ev.lease)
	return err
}

func (o *entityEvents) retry(ctx context.Context, ev EntityEvent, next time.Time, dead bool, lastError string) error {
	d := 0
	if dead {
		d = 1
	}
	_, err := o.db.ExecContext(ctx, rebind(o.db.Dialect, fmt.Sprintf(`UPDATE %s SET attempts = attempts + 1, next_at = $1,
		dead = $2, last_error = $3, lease_until = 0 WHERE id = $4 AND lease_token = $5`, entityEventsTable)),
		next.UnixNano(), d, lastError, ev.ID, ev.lease)
	return err
}

// dead lists dead-lettered events, oldest first.
func (o *entityEvents) dead(ctx context.Context, entity string, limit int) ([]EntityEvent, error) {
	stmt := fmt.Sprintf("SELECT %s FROM %s WHERE dead = 1", entityEventColumns, entityEventsTable)
	args := []any{}
	if entity != "" {
		args = append(args, entity)
		stmt += " AND entity = $1"
	}
	args = append(args, limit)
	stmt += fmt.Sprintf(" ORDER BY created_at, id LIMIT $%d", len(args))
	rows, err := o.db.QueryContext(ctx, rebind(o.db.Dialect, stmt), args...)
	if err != nil {
		return nil, err
	}
	return scanEntityEvents(rows)
}

// requeue gives a dead-lettered event a fresh set of attempts, now.
func (o *entityEvents) requeue(ctx context.Context, entity, id string) (bool, error) {
	stmt := fmt.Sprintf(`UPDATE %s SET dead = 0, attempts = 0, next_at = $1, lease_until = 0, last_error = NULL
		WHERE id = $2 AND dead = 1`, entityEventsTable)
	args := []any{time.Now().UnixNano(), id}
	if entity != "" {
		stmt += " AND entity = $3"
		args = append(args, entity)
	}
	res, err := o.db.ExecContext(ctx, rebind(o.db.Dialect, stmt), args...)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		o.poke()
	}
	return n > 0, nil
}

// runBackground backfills empty entity search indexes and delivers the
// database's durable entity hooks until ctx ends: woken by commits, and
// polling so retries and other replicas' events are picked up. A database
// without either returns at once.
func (d *Database) runBackground(ctx context.Context, p *Platform) {
	defer d.backfillSearchIndexes(ctx)()
	d.eventsMu.Lock()
	o := d.events
	d.eventsMu.Unlock()
	if o == nil {
		return
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		for o.deliver(ctx, p) > 0 {
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-o.nudge:
		}
	}
}

func (o *entityEvents) deliver(ctx context.Context, p *Platform) int {
	now := time.Now()
	events, err := o.claim(ctx, 50, time.Minute, now)
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("entity hook outbox claim failed", "error", err)
		}
		return 0
	}
	for _, ev := range events {
		if ctx.Err() != nil {
			break // shutting down: the lease lapses and another dispatcher takes the rest
		}
		input := make(map[string]any, len(ev.Input)+2)
		for k, v := range ev.Input {
			input[k] = v
		}
		input["event_id"], input["attempt"] = ev.ID, ev.Attempts+1
		hctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		_, err := p.CallIntent(hctx, ev.Hook, input, nil)
		cancel()
		if err != nil && ctx.Err() != nil {
			break // interrupted by shutdown, not a failed attempt
		}
		// Record the outcome even when shutdown began meanwhile: a hook
		// that ran must be acknowledged, or it runs again after the lease.
		sctx, scancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		switch {
		case err == nil:
			err = o.ack(sctx, ev)
		case ev.Attempts+1 >= ev.MaxAttempts:
			slog.Error("entity hook dead-lettered", "entity", ev.Entity, "event", ev.Event, "hook", ev.Hook, "id", ev.ID, "error", err)
			err = o.retry(sctx, ev, now, true, err.Error())
		default:
			err = o.retry(sctx, ev, now.Add(entityBackoff(ev.retryBase, ev.Attempts+1)), false, err.Error())
		}
		scancel()
		if err != nil {
			slog.Warn("entity hook outbox update failed", "id", ev.ID, "error", err)
		}
	}
	return len(events)
}

// entityBackoff is the delay before retry n: base doubling, at most 10m.
func entityBackoff(base time.Duration, n int) time.Duration {
	if base <= 0 {
		base = 2 * time.Second
	}
	d := base
	for i := 1; i < n && d < 10*time.Minute; i++ {
		d *= 2
	}
	return min(d, 10*time.Minute)
}

// ---------------------------------------------------------------------------
// Writes with durable hooks
// ---------------------------------------------------------------------------

// durable returns the durable hooks an event triggers.
func (rt *entityRuntime) durable(event string) []EntityHook {
	var out []EntityHook
	for _, h := range rt.plan.spec.On {
		if h.Durable && (h.Event == event || h.Event == "*") {
			out = append(out, h)
		}
	}
	return out
}

// entityChange is one prepared create, update or delete: its statement and
// what applying it needs.
type entityChange struct {
	event string // created, updated or deleted
	id    string
	stmt  string
	args  []any
	prev  map[string]any // the record before an update or delete
}

// needsTx reports whether a change of this event must run in a transaction:
// its durable hooks and search tokens commit with it.
func (rt *entityRuntime) needsTx(event string) bool {
	return rt.plan.spec.SearchIndex || len(rt.durable(event)) > 0
}

// transact runs fn on a transaction (or the pool when useTx is false) and
// commits it together with the durable hook events fn returns.
func (rt *entityRuntime) transact(ctx *ActionContext, useTx bool, fn func(q execer) ([]EntityEvent, error)) error {
	if !useTx {
		_, err := fn(rt.db.DB)
		return err
	}
	tx, err := rt.db.BeginTx(ctx.Context, nil)
	if err != nil {
		return databaseFailure(err)
	}
	defer tx.Rollback() //nolint:errcheck // a no-op after commit
	events, err := fn(tx)
	if err != nil {
		return err
	}
	var o *entityEvents
	if len(events) > 0 {
		o = rt.db.enableEntityEvents()
		if err := o.record(ctx.Context, tx, events); err != nil {
			return databaseFailure(err)
		}
	}
	if err := tx.Commit(); err != nil {
		return databaseFailure(err)
	}
	if o != nil {
		o.poke()
	}
	return nil
}

// apply runs a prepared change on q (the pool or a transaction) and returns
// the record afterwards (read on q), having rewritten its search tokens, and
// the durable hook events the change triggers, for transact to record.
func (rt *entityRuntime) apply(ctx *ActionContext, q execer, ch entityChange) (map[string]any, []EntityEvent, error) {
	p := rt.plan
	res, err := q.ExecContext(ctx.Context, rebind(rt.db.Dialect, ch.stmt), ch.args...)
	if err != nil {
		return nil, nil, databaseFailure(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if ch.event == "updated" && p.spec.Versioned {
			return nil, nil, conflict("%s %s was changed by someone else; reload and try again", p.spec.Name, ch.id)
		}
		return nil, nil, notFound(p.spec.Name, ch.id)
	}
	record := ch.prev
	if ch.event != "deleted" {
		if record, err = rt.loadOn(ctx, q, ch.id); err != nil {
			return nil, nil, err
		}
	}
	if p.spec.SearchIndex {
		if err := p.reindexRow(ctx.Context, q, ch.id, ch.event == "deleted"); err != nil {
			return nil, nil, databaseFailure(err)
		}
	}
	hooks := rt.durable(ch.event)
	if len(hooks) == 0 {
		return record, nil, nil
	}
	input := map[string]any{"entity": p.spec.Name, "event": ch.event, "record": record,
		"actor": ctx.Principal.ID, "tenant_id": ctx.TenantID}
	if ch.prev != nil {
		input["previous"] = ch.prev
	}
	events := make([]EntityEvent, len(hooks))
	for i, h := range hooks {
		events[i] = EntityEvent{ID: newPrefixedID("evt"), Entity: p.spec.Name, Event: ch.event, Hook: h.Hook,
			Input: input, MaxAttempts: h.MaxAttempts, retryBase: h.retryBase}
	}
	return record, events, nil
}

// commitOne applies one change in its own transaction when it needs one,
// then runs the plain (non-durable) hooks.
func (rt *entityRuntime) commitOne(ctx *ActionContext, ch entityChange) (map[string]any, error) {
	var record map[string]any
	err := rt.transact(ctx, rt.needsTx(ch.event), func(q execer) ([]EntityEvent, error) {
		var (
			events []EntityEvent
			err    error
		)
		record, events, err = rt.apply(ctx, q, ch)
		return events, err
	})
	if err != nil {
		return nil, err
	}
	rt.fire(ctx, ch.event, record, ch.prev)
	return record, nil
}

// loadOn reads a record by id through q (the pool or a transaction), without
// the caller's scope: the write that precedes it already applied it.
func (rt *entityRuntime) loadOn(ctx *ActionContext, q execer, id string) (map[string]any, error) {
	stmt := fmt.Sprintf("SELECT %s FROM %s WHERE id = $1", rt.selectList(), rt.plan.table)
	rows, err := queryRows(ctx.Context, q, rebind(rt.db.Dialect, stmt), []any{id})
	if err != nil {
		return nil, databaseFailure(err)
	}
	if len(rows) == 0 {
		return nil, notFound(rt.plan.spec.Name, id)
	}
	return rt.decode(rows[0]), nil
}

// ---------------------------------------------------------------------------
// The entity.events action
// ---------------------------------------------------------------------------

func registerEntityEventActions(r *Registry) {
	mustAction(r, "entity.events", ActionFactoryFunc(buildEntityEvents), ActionInfo{
		Family: "data", Kind: "effect",
		Summary:  "Operate the durable entity hook outbox: list dead-lettered hook events, or requeue one (:event_id)",
		Provides: "{dead: [...]} or {requeued: id}",
		Config: []ConfigField{
			{Name: "op", Type: "string", Summary: "dead (default) | requeue"},
			{Name: "entity", Type: "string", Summary: "Limit to one entity"},
			{Name: "roles", Type: "[]string", Summary: "Only principals holding one of these roles may call it"},
			{Name: "limit", Type: "int", Summary: "Most events listed (default 100)"},
		},
	})
}

func buildEntityEvents(build BuildContext, spec NodeSpec) (Action, error) {
	db, err := requireResource[*Database](build, spec, "a database.sql resource")
	if err != nil {
		return nil, err
	}
	if err := rejectUnknownConfig("entity.events", spec.Config, "op", "entity", "roles", "limit"); err != nil {
		return nil, fmt.Errorf("node %q: %w", spec.Name, err)
	}
	if len(spec.Provides) != 1 {
		return nil, fmt.Errorf("node %q: entity.events provides exactly one fact", spec.Name)
	}
	roles, entity := configStrings(spec.Config, "roles"), configString(spec.Config, "entity", "")
	limit, err := configInt(spec.Config, "limit", 100)
	if err != nil || limit < 1 {
		return nil, fmt.Errorf("node %q: limit must be a positive integer", spec.Name)
	}
	return ActionFunc(func(ctx *ActionContext) (ActionResult, error) {
		if len(roles) > 0 && !slices.ContainsFunc(roles, ctx.Principal.HasRole) {
			return ActionResult{}, permissionDenied("operating hook events needs one of the roles " + strings.Join(roles, ", "))
		}
		o := db.enableEntityEvents()
		op := configString(spec.Config, "op", "")
		for _, from := range []string{"path", "query"} {
			if v, ok := requestValue(ctx, from, "op"); op == "" && ok {
				op = v
			}
		}
		if op == "requeue" {
			id, _ := requestValue(ctx, "path", "event_id")
			if id == "" {
				if v, ok := resolvePath(ctx.Inputs, "input.event_id"); ok {
					id = Stringify(v)
				}
			}
			if id == "" {
				return ActionResult{}, invalidInput("an event_id is required")
			}
			ok, err := o.requeue(ctx.Context, entity, id)
			if err != nil {
				return ActionResult{}, databaseFailure(err)
			}
			if !ok {
				return ActionResult{}, notFound("dead-lettered event", id)
			}
			return singleOutput(spec, map[string]any{"requeued": id}), nil
		}
		dead, err := o.dead(ctx.Context, entity, limit)
		if err != nil {
			return ActionResult{}, databaseFailure(err)
		}
		list := make([]any, len(dead))
		for i, ev := range dead {
			raw, _ := json.Marshal(ev)
			var m map[string]any
			_ = json.Unmarshal(raw, &m)
			list[i] = m
		}
		return singleOutput(spec, map[string]any{"dead": list}), nil
	}), nil
}
