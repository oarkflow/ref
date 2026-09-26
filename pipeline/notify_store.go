package pipeline

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Memory
// ---------------------------------------------------------------------------

type memoryNotify struct {
	prefs  map[string]NotifyPreferences
	queue  []*Notification
	leases map[string]time.Time
}

func (s *MemoryStore) notify() *memoryNotify {
	if s.notices == nil {
		s.notices = &memoryNotify{prefs: map[string]NotifyPreferences{}, leases: map[string]time.Time{}}
	}
	return s.notices
}

func (s *MemoryStore) Preferences(_ context.Context, tenant, user string) (*NotifyPreferences, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.notify().prefs[tenant+"\x00"+user]
	if !ok {
		return nil, nil
	}
	out := clonePrefs(p)
	return &out, nil
}

func (s *MemoryStore) SetPreferences(_ context.Context, p *NotifyPreferences) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.notify().prefs[p.TenantID+"\x00"+p.User] = clonePrefs(*p)
	return nil
}

func (s *MemoryStore) EnqueueNotifications(_ context.Context, items []Notification) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := s.notify()
	for _, item := range items {
		exists := false
		for _, q := range n.queue {
			exists = exists || q.ID == item.ID
		}
		if !exists {
			copied := item
			n.queue = append(n.queue, &copied)
		}
	}
	return nil
}

func (s *MemoryStore) ClaimNotifications(_ context.Context, limit int, lease time.Duration, now time.Time) ([]Notification, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := s.notify()
	sort.SliceStable(n.queue, func(i, j int) bool { return n.queue[i].DeliverAt.Before(n.queue[j].DeliverAt) })
	var out []Notification
	for _, q := range n.queue {
		if len(out) >= limit {
			break
		}
		if q.Dead || q.DeliverAt.After(now) || n.leases[q.ID].After(now) {
			continue
		}
		n.leases[q.ID] = now.Add(lease)
		out = append(out, *q)
	}
	return out, nil
}

func (s *MemoryStore) AckNotifications(_ context.Context, ids []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := s.notify()
	kept := n.queue[:0]
	for _, q := range n.queue {
		if !slices.Contains(ids, q.ID) {
			kept = append(kept, q)
		}
	}
	n.queue = kept
	for _, id := range ids {
		delete(n.leases, id)
	}
	return nil
}

func (s *MemoryStore) RetryNotifications(_ context.Context, ids []string, next time.Time, dead bool, lastError string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := s.notify()
	for _, q := range n.queue {
		if slices.Contains(ids, q.ID) {
			q.Attempts++
			q.DeliverAt, q.Dead, q.LastError = next, dead, lastError
			delete(n.leases, q.ID)
		}
	}
	return nil
}

func (s *MemoryStore) PendingNotifications(_ context.Context, tenant, user string, limit int) ([]Notification, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Notification
	for _, q := range s.notify().queue {
		if q.TenantID == tenant && q.User == user && !q.Dead && len(out) < limit {
			out = append(out, *q)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].DeliverAt.Before(out[j].DeliverAt) })
	return out, nil
}

func clonePrefs(p NotifyPreferences) NotifyPreferences {
	raw, _ := json.Marshal(p)
	var out NotifyPreferences
	_ = json.Unmarshal(raw, &out)
	return out
}

// ---------------------------------------------------------------------------
// SQL
// ---------------------------------------------------------------------------

func (s *SQLStore) Preferences(ctx context.Context, tenant, user string) (*NotifyPreferences, error) {
	var doc string
	err := s.db.QueryRowContext(ctx, s.q(`SELECT doc FROM {p}notify_prefs WHERE tenant_id = ? AND user_id = ?`), tenant, user).Scan(&doc)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var p NotifyPreferences
	if err := json.Unmarshal([]byte(doc), &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// SetPreferences replaces a person's preferences (delete and insert in one
// transaction, which every dialect supports alike).
func (s *SQLStore) SetPreferences(ctx context.Context, p *NotifyPreferences) error {
	doc, err := json.Marshal(p)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, s.q(`DELETE FROM {p}notify_prefs WHERE tenant_id = ? AND user_id = ?`), p.TenantID, p.User); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, s.q(`INSERT INTO {p}notify_prefs (tenant_id, user_id, doc, updated_at) VALUES (?, ?, ?, ?)`),
		p.TenantID, p.User, string(doc), p.UpdatedAt.UnixNano()); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLStore) EnqueueNotifications(ctx context.Context, items []Notification) error {
	if len(items) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, n := range items {
		var exists int
		if err := tx.QueryRowContext(ctx, s.q(`SELECT COUNT(*) FROM {p}notifications WHERE id = ?`), n.ID).Scan(&exists); err != nil {
			return err
		}
		if exists > 0 {
			continue
		}
		doc, err := json.Marshal(n)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, s.q(`INSERT INTO {p}notifications (id, tenant_id, user_id, channel, digest, deliver_at, doc, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`),
			n.ID, n.TenantID, n.User, n.Channel, n.Digest, n.DeliverAt.UnixNano(), string(doc), n.At.UnixNano()); err != nil {
			return err
		}
	}
	return tx.Commit()
}

const notificationColumns = `doc, attempts, dead, last_error, deliver_at`

func (s *SQLStore) ClaimNotifications(ctx context.Context, limit int, lease time.Duration, now time.Time) ([]Notification, error) {
	token := randomID()
	res, err := s.db.ExecContext(ctx, s.q(`UPDATE {p}notifications SET lease_token = ?, lease_until = ?
		WHERE id IN (SELECT id FROM (SELECT id FROM {p}notifications WHERE dead = 0 AND deliver_at <= ? AND lease_until <= ? ORDER BY deliver_at, created_at LIMIT ?) due)`),
		token, now.Add(lease).UnixNano(), now.UnixNano(), now.UnixNano(), limit)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, nil
	}
	return s.scanNotifications(ctx, `SELECT `+notificationColumns+` FROM {p}notifications WHERE lease_token = ? ORDER BY deliver_at, created_at`, token)
}

func (s *SQLStore) scanNotifications(ctx context.Context, query string, args ...any) ([]Notification, error) {
	rows, err := s.db.QueryContext(ctx, s.q(query), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Notification
	for rows.Next() {
		var (
			doc     string
			n       Notification
			dead    int
			lastErr sql.NullString
			attempt int
			at      int64
		)
		if err := rows.Scan(&doc, &attempt, &dead, &lastErr, &at); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(doc), &n); err != nil {
			return nil, err
		}
		n.Attempts, n.Dead, n.LastError, n.DeliverAt = attempt, dead != 0, lastErr.String, time.Unix(0, at).UTC()
		out = append(out, n)
	}
	return out, rows.Err()
}

func (s *SQLStore) inIDs(statement string, ids []string, args ...any) (string, []any) {
	ph := make([]string, len(ids))
	for i, id := range ids {
		ph[i] = "?"
		args = append(args, id)
	}
	return s.q(statement + " WHERE id IN (" + strings.Join(ph, ", ") + ")"), args
}

func (s *SQLStore) AckNotifications(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	statement, args := s.inIDs(`DELETE FROM {p}notifications`, ids)
	_, err := s.db.ExecContext(ctx, statement, args...)
	return err
}

func (s *SQLStore) RetryNotifications(ctx context.Context, ids []string, next time.Time, dead bool, lastError string) error {
	if len(ids) == 0 {
		return nil
	}
	d := 0
	if dead {
		d = 1
	}
	statement, args := s.inIDs(`UPDATE {p}notifications SET attempts = attempts + 1, deliver_at = ?, dead = ?, last_error = ?, lease_token = '', lease_until = 0`,
		ids, next.UnixNano(), d, lastError)
	_, err := s.db.ExecContext(ctx, statement, args...)
	return err
}

func (s *SQLStore) PendingNotifications(ctx context.Context, tenant, user string, limit int) ([]Notification, error) {
	return s.scanNotifications(ctx, `SELECT `+notificationColumns+` FROM {p}notifications WHERE tenant_id = ? AND user_id = ? AND dead = 0 ORDER BY deliver_at, created_at LIMIT ?`,
		tenant, user, limit)
}
