package pipeline

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"time"
)

// The outbox makes event hooks durable. A store with a Record filter writes
// the events an operation emitted in the same transaction as the case
// change, so an event exists if and only if its change was committed. A
// dispatcher claims due events under a lease, runs their hooks and
// acknowledges them; a failed hook is retried with exponential backoff and,
// after MaxAttempts, dead-lettered for an operator to inspect.

// OutboxEvent is a committed event awaiting delivery.
type OutboxEvent struct {
	ID        string    `json:"id"`
	CaseID    string    `json:"case_id"`
	Pipeline  string    `json:"pipeline"`
	Event     Event     `json:"event"`
	Attempts  int       `json:"attempts"`
	NextAt    time.Time `json:"next_at"`
	Dead      bool      `json:"dead,omitempty"`
	LastError string    `json:"last_error,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// Outbox is implemented by stores that persist events with their changes.
type Outbox interface {
	// ClaimEvents leases up to limit due events for lease.
	ClaimEvents(ctx context.Context, limit int, lease time.Duration, now time.Time) ([]OutboxEvent, error)
	// AckEvent removes a delivered event.
	AckEvent(ctx context.Context, id string) error
	// RetryEvent records a failure: the event is retried at next, or
	// dead-lettered when dead is set.
	RetryEvent(ctx context.Context, id string, next time.Time, dead bool, lastError string) error
	// DeadEvents lists dead-lettered events.
	DeadEvents(ctx context.Context, limit int) ([]OutboxEvent, error)
	// RequeueEvent revives a dead-lettered event for immediate delivery.
	RequeueEvent(ctx context.Context, id string) error
}

// clearEvents drops events once they are durably in the outbox, so saving
// the same case value again never enqueues them twice.
func (s *MemoryStore) clearEvents(c *Case) {
	if s.Record != nil {
		c.events = nil
	}
}

func (s *SQLStore) clearEvents(c *Case) {
	if s.Record != nil {
		c.events = nil
	}
}

// MaxAttempts is how many times a hook is tried before dead-lettering.
const MaxAttempts = 10

// Backoff is the delay before retry attempt n (1-based): 2^n seconds, at
// most ten minutes.
func Backoff(n int) time.Duration {
	return time.Duration(math.Min(math.Pow(2, float64(n)), 600)) * time.Second
}

// ---------------------------------------------------------------------------
// Memory
// ---------------------------------------------------------------------------

func (s *MemoryStore) enqueue(c *Case) {
	if s.Record == nil {
		return
	}
	for _, ev := range c.Events() {
		if s.Record(ev) {
			s.outbox = append(s.outbox, &OutboxEvent{ID: "evt_" + randomID(), CaseID: c.ID, Pipeline: c.Pipeline,
				Event: ev, NextAt: ev.At, CreatedAt: ev.At})
		}
	}
}

func (s *MemoryStore) ClaimEvents(_ context.Context, limit int, lease time.Duration, now time.Time) ([]OutboxEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.leases == nil {
		s.leases = map[string]time.Time{}
	}
	var out []OutboxEvent
	for _, ev := range s.outbox {
		if len(out) >= limit {
			break
		}
		if ev.Dead || ev.NextAt.After(now) || s.leases[ev.ID].After(now) {
			continue
		}
		s.leases[ev.ID] = now.Add(lease)
		out = append(out, *ev)
	}
	return out, nil
}

func (s *MemoryStore) AckEvent(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, ev := range s.outbox {
		if ev.ID == id {
			s.outbox = append(s.outbox[:i], s.outbox[i+1:]...)
			break
		}
	}
	delete(s.leases, id)
	return nil
}

func (s *MemoryStore) RetryEvent(_ context.Context, id string, next time.Time, dead bool, lastError string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ev := range s.outbox {
		if ev.ID == id {
			ev.Attempts++
			ev.NextAt, ev.Dead, ev.LastError = next, dead, lastError
		}
	}
	delete(s.leases, id)
	return nil
}

func (s *MemoryStore) DeadEvents(_ context.Context, limit int) ([]OutboxEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []OutboxEvent
	for _, ev := range s.outbox {
		if ev.Dead && len(out) < limit {
			out = append(out, *ev)
		}
	}
	return out, nil
}

func (s *MemoryStore) RequeueEvent(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ev := range s.outbox {
		if ev.ID == id {
			ev.Dead, ev.NextAt = false, time.Time{}
			return nil
		}
	}
	return fmt.Errorf("%w: event %q", ErrNotFound, id)
}

// ---------------------------------------------------------------------------
// SQL
// ---------------------------------------------------------------------------

func (s *SQLStore) enqueue(ctx context.Context, tx *sql.Tx, c *Case) error {
	if s.Record == nil {
		return nil
	}
	// created_at orders delivery: the commit time plus the event's position,
	// so events of one change keep the order they happened in.
	base := time.Now().UnixNano()
	for i, ev := range c.Events() {
		if !s.Record(ev) {
			continue
		}
		raw, err := json.Marshal(ev)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, s.q(`INSERT INTO {p}outbox (id, case_id, pipeline, event, next_at, created_at) VALUES (?, ?, ?, ?, ?, ?)`),
			"evt_"+randomID(), c.ID, c.Pipeline, string(raw), ev.At.UnixNano(), base+int64(i)); err != nil {
			return err
		}
	}
	return nil
}

func (s *SQLStore) ClaimEvents(ctx context.Context, limit int, lease time.Duration, now time.Time) ([]OutboxEvent, error) {
	token := randomID()
	// The derived table keeps MySQL happy (it cannot select from the table
	// it updates); the lease token identifies what this caller claimed. The
	// outer condition repeats the lease test: PostgreSQL re-checks it on a
	// row another dispatcher leased meanwhile (the IN list alone is not), so
	// two replicas never lease the same event.
	res, err := s.db.ExecContext(ctx, s.q(`UPDATE {p}outbox SET lease_token = ?, lease_until = ?
		WHERE id IN (SELECT id FROM (SELECT id FROM {p}outbox WHERE dead = 0 AND next_at <= ? AND lease_until <= ? ORDER BY created_at LIMIT ?) due)
		AND dead = 0 AND lease_until <= ?`),
		token, now.Add(lease).UnixNano(), now.UnixNano(), now.UnixNano(), limit, now.UnixNano())
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, nil
	}
	return s.scanEvents(ctx, `SELECT id, case_id, pipeline, event, attempts, next_at, dead, last_error, created_at FROM {p}outbox WHERE lease_token = ? ORDER BY created_at`, token)
}

func (s *SQLStore) scanEvents(ctx context.Context, query string, args ...any) ([]OutboxEvent, error) {
	rows, err := s.db.QueryContext(ctx, s.q(query), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OutboxEvent
	for rows.Next() {
		var (
			ev            OutboxEvent
			raw           string
			next, created int64
			dead          int
			lastErr       sql.NullString
		)
		if err := rows.Scan(&ev.ID, &ev.CaseID, &ev.Pipeline, &raw, &ev.Attempts, &next, &dead, &lastErr, &created); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(raw), &ev.Event); err != nil {
			return nil, err
		}
		ev.NextAt, ev.CreatedAt = time.Unix(0, next).UTC(), time.Unix(0, created).UTC()
		ev.Dead, ev.LastError = dead != 0, lastErr.String
		out = append(out, ev)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, rows.Err()
}

func (s *SQLStore) AckEvent(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, s.q(`DELETE FROM {p}outbox WHERE id = ?`), id)
	return err
}

func (s *SQLStore) RetryEvent(ctx context.Context, id string, next time.Time, dead bool, lastError string) error {
	d := 0
	if dead {
		d = 1
	}
	_, err := s.db.ExecContext(ctx, s.q(`UPDATE {p}outbox SET attempts = attempts + 1, next_at = ?, dead = ?, last_error = ?, lease_token = '', lease_until = 0 WHERE id = ?`),
		next.UnixNano(), d, lastError, id)
	return err
}

func (s *SQLStore) DeadEvents(ctx context.Context, limit int) ([]OutboxEvent, error) {
	return s.scanEvents(ctx, `SELECT id, case_id, pipeline, event, attempts, next_at, dead, last_error, created_at FROM {p}outbox WHERE dead = 1 ORDER BY created_at LIMIT ?`, limit)
}

func (s *SQLStore) RequeueEvent(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, s.q(`UPDATE {p}outbox SET dead = 0, next_at = 0, lease_until = 0 WHERE id = ?`), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: event %q", ErrNotFound, id)
	}
	return nil
}
