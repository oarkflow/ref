package etl

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// SQLStore persists everything in a database/sql database (PostgreSQL, MySQL
// or SQLite). Documents are JSON; the columns that are filtered, joined or
// summed are stored next to them and indexed.
type SQLStore struct {
	db      *sql.DB
	dialect string // postgres, mysql or sqlite
	prefix  string
	retries atomic.Int64
}

// Retries is how many writes were retried after a transient database error.
func (s *SQLStore) Retries() int64 { return s.retries.Load() }

var identRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// NewSQLStore returns a store using tables named <prefix>sources, <prefix>batches and so on.
func NewSQLStore(db *sql.DB, dialect, prefix string) (*SQLStore, error) {
	if prefix == "" {
		prefix = "etl_"
	}
	if !identRe.MatchString(prefix) {
		return nil, fmt.Errorf("etl: invalid table prefix %q", prefix)
	}
	switch dialect {
	case "postgres", "mysql", "sqlite":
	default:
		return nil, fmt.Errorf("etl: unsupported dialect %q", dialect)
	}
	return &SQLStore{db: db, dialect: dialect, prefix: prefix}, nil
}

func (s *SQLStore) q(statement string) string {
	statement = strings.ReplaceAll(statement, "{p}", s.prefix)
	if s.dialect != "postgres" {
		return statement
	}
	var b strings.Builder
	n := 0
	for _, r := range statement {
		if r == '?' {
			n++
			b.WriteString("$" + strconv.Itoa(n))
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// Migrate creates the tables. It is safe to run on every start.
func (s *SQLStore) Migrate(ctx context.Context) error {
	key, text := "TEXT", "TEXT"
	if s.dialect == "mysql" {
		key, text = "VARCHAR(191)", "LONGTEXT"
	}
	stmts := []string{
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS {p}sources (id %[1]s PRIMARY KEY, doc %[2]s NOT NULL, updated_at BIGINT NOT NULL)`, key, text),
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS {p}batches (
			id %[1]s PRIMARY KEY, source_id %[1]s NOT NULL, bkey %[1]s NOT NULL, status %[1]s NOT NULL, stage INTEGER NOT NULL,
			next_at BIGINT NOT NULL, rows_in INTEGER NOT NULL, quarantined INTEGER NOT NULL, delivered INTEGER NOT NULL,
			content_hash %[1]s NOT NULL DEFAULT '', finished_at BIGINT NOT NULL DEFAULT 0,
			lease_owner %[1]s NOT NULL DEFAULT '', lease_until BIGINT NOT NULL DEFAULT 0,
			revision BIGINT NOT NULL, doc %[2]s NOT NULL, created_at BIGINT NOT NULL, UNIQUE (source_id, bkey),
			CONSTRAINT {p}batches_status_ok CHECK (status IN ('in_flight', 'retrying', 'held', 'delivered', 'failed')),
			CONSTRAINT {p}batches_stage_ok CHECK (stage BETWEEN 1 AND 7),
			CONSTRAINT {p}batches_rows_ok CHECK (rows_in >= 0 AND quarantined >= 0 AND delivered >= 0 AND quarantined <= rows_in))`, key, text),
		`CREATE INDEX {p}batches_hash_idx ON {p}batches (source_id, content_hash)`,
		`CREATE INDEX {p}batches_status_idx ON {p}batches (status, next_at)`,
		`CREATE INDEX {p}batches_created_idx ON {p}batches (created_at)`,
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS {p}roles (id %[1]s PRIMARY KEY, doc %[2]s NOT NULL, updated_at BIGINT NOT NULL)`, key, text),
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS {p}checkpoints (
			batch_id %[1]s NOT NULL, stage INTEGER NOT NULL, hash %[1]s NOT NULL, doc %[2]s NOT NULL, PRIMARY KEY (batch_id, stage))`, key, text),
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS {p}quarantine (
			batch_id %[1]s NOT NULL, source_id %[1]s NOT NULL, row_no INTEGER NOT NULL, rule %[1]s NOT NULL, doc %[2]s NOT NULL, at BIGINT NOT NULL)`, key, text),
		`CREATE INDEX {p}quarantine_batch_idx ON {p}quarantine (batch_id, row_no)`,
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS {p}lineage (
			batch_id %[1]s NOT NULL, ord INTEGER NOT NULL, doc %[2]s NOT NULL, PRIMARY KEY (batch_id, ord))`, key, text),
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS {p}audit (
			seq BIGINT PRIMARY KEY, batch_id %[1]s NOT NULL, source_id %[1]s NOT NULL, doc %[2]s NOT NULL)`, key, text),
		`CREATE INDEX {p}audit_batch_idx ON {p}audit (batch_id, seq)`,
		`CREATE INDEX {p}audit_source_idx ON {p}audit (source_id, seq)`,
		`CREATE INDEX {p}quarantine_source_idx ON {p}quarantine (source_id, at)`,
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS {p}counters (name %[1]s NOT NULL, labels %[1]s NOT NULL, value DOUBLE PRECISION NOT NULL, PRIMARY KEY (name, labels))`, key),
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS {p}audit_head (id INTEGER PRIMARY KEY, seq BIGINT NOT NULL, hash %s NOT NULL)`, key),
	}
	for _, st := range stmts {
		if _, err := s.db.ExecContext(ctx, s.q(st)); err != nil {
			if isExists(err) {
				continue
			}
			return err
		}
	}
	// The audit trail is append-only in the database itself, not only in the
	// code: an UPDATE or DELETE of an entry is refused whoever issues it.
	for _, st := range s.auditGuards() {
		if _, err := s.db.ExecContext(ctx, s.q(st)); err != nil && !isExists(err) {
			return err
		}
	}
	ins := `INSERT INTO {p}audit_head (id, seq, hash) VALUES (1, 0, '') ON CONFLICT (id) DO NOTHING`
	if s.dialect == "mysql" {
		ins = `INSERT IGNORE INTO {p}audit_head (id, seq, hash) VALUES (1, 0, '')`
	}
	_, err := s.db.ExecContext(ctx, s.q(ins))
	return err
}

func (s *SQLStore) auditGuards() []string {
	switch s.dialect {
	case "postgres":
		return []string{
			`CREATE OR REPLACE FUNCTION {p}audit_guard() RETURNS trigger AS $$ BEGIN RAISE EXCEPTION 'audit entries are append-only'; END $$ LANGUAGE plpgsql`,
			`DROP TRIGGER IF EXISTS {p}audit_guard_trg ON {p}audit`,
			`CREATE TRIGGER {p}audit_guard_trg BEFORE UPDATE OR DELETE ON {p}audit FOR EACH ROW EXECUTE FUNCTION {p}audit_guard()`,
		}
	case "mysql":
		return []string{
			`CREATE TRIGGER {p}audit_no_update BEFORE UPDATE ON {p}audit FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'audit entries are append-only'`,
			`CREATE TRIGGER {p}audit_no_delete BEFORE DELETE ON {p}audit FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'audit entries are append-only'`,
		}
	}
	return []string{
		`CREATE TRIGGER IF NOT EXISTS {p}audit_no_update BEFORE UPDATE ON {p}audit BEGIN SELECT RAISE(ABORT, 'audit entries are append-only'); END`,
		`CREATE TRIGGER IF NOT EXISTS {p}audit_no_delete BEFORE DELETE ON {p}audit BEGIN SELECT RAISE(ABORT, 'audit entries are append-only'); END`,
	}
}

// transient reports errors worth retrying: a locked database, a deadlock, a
// serialization failure or a dropped connection.
func transient(err error) bool {
	if err == nil {
		return false
	}
	m := strings.ToLower(err.Error())
	for _, s := range []string{"database is locked", "sqlite_busy", "deadlock", "could not serialize", "40001", "40p01", "connection reset", "bad connection", "broken pipe", "too many connections"} {
		if strings.Contains(m, s) {
			return true
		}
	}
	return false
}

// withRetry runs a write again after a transient error, a few times with a
// growing pause. The write is a single transaction, so a retry is safe.
func (s *SQLStore) withRetry(ctx context.Context, fn func() error) error {
	var err error
	for attempt := 0; attempt < 4; attempt++ {
		if err = fn(); err == nil || !transient(err) || ctx.Err() != nil {
			return err
		}
		s.retries.Add(1)
		select {
		case <-ctx.Done():
			return err
		case <-time.After(time.Duration(20<<attempt) * time.Millisecond):
		}
	}
	return err
}

func isExists(err error) bool {
	m := strings.ToLower(err.Error())
	return strings.Contains(m, "already exists") || strings.Contains(m, "duplicate key name")
}

func isDuplicate(err error) bool {
	m := strings.ToLower(err.Error())
	return strings.Contains(m, "unique") || strings.Contains(m, "duplicate") || strings.Contains(m, "23505")
}

func ns(t time.Time) int64 { return t.UnixNano() }

func doc(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func (s *SQLStore) Commit(ctx context.Context, c Change) error {
	rev := int64(0)
	if c.Batch != nil {
		rev = c.Batch.Revision
	}
	return s.withRetry(ctx, func() error {
		if c.Batch != nil {
			c.Batch.Revision = rev // a failed attempt must not leave a bumped revision behind
		}
		return s.commit(ctx, c)
	})
}

func (s *SQLStore) commit(ctx context.Context, c Change) (err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	exec := func(q string, args ...any) (sql.Result, error) { return tx.ExecContext(ctx, s.q(q), args...) }
	if src := c.Source; src != nil {
		if _, err = exec(`DELETE FROM {p}sources WHERE id = ?`, src.ID); err != nil {
			return err
		}
		if _, err = exec(`INSERT INTO {p}sources (id, doc, updated_at) VALUES (?, ?, ?)`, src.ID, doc(src), ns(src.UpdatedAt)); err != nil {
			return err
		}
	}
	if r := c.Role; r != nil {
		if _, err = exec(`DELETE FROM {p}roles WHERE id = ?`, r.ID); err != nil {
			return err
		}
		if _, err = exec(`INSERT INTO {p}roles (id, doc, updated_at) VALUES (?, ?, ?)`, r.ID, doc(r), ns(r.UpdatedAt)); err != nil {
			return err
		}
	}
	if c.DeleteRole != "" {
		if _, err = exec(`DELETE FROM {p}roles WHERE id = ?`, c.DeleteRole); err != nil {
			return err
		}
	}
	nextRev := int64(0)
	if b := c.Batch; b != nil {
		nextRev = b.Revision + 1
		snap := *b
		snap.Revision = nextRev
		if b.Revision == 0 {
			_, err = exec(`INSERT INTO {p}batches (id, source_id, bkey, status, stage, next_at, rows_in, quarantined, delivered, content_hash, finished_at, revision, doc, created_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, b.ID, b.SourceID, b.Key, b.Status, b.Stage, ns(b.NextAttemptAt), b.RowsIn, b.Quarantined,
				b.Delivered, b.ContentHash, ns(b.FinishedAt), nextRev, doc(&snap), ns(b.CreatedAt))
			if err != nil {
				if isDuplicate(err) {
					err = ErrConflict
				}
				return err
			}
		} else {
			var res sql.Result
			res, err = exec(`UPDATE {p}batches SET status = ?, stage = ?, next_at = ?, rows_in = ?, quarantined = ?, delivered = ?, finished_at = ?, lease_owner = '', lease_until = 0, revision = ?, doc = ?
				WHERE id = ? AND revision = ?`, b.Status, b.Stage, ns(b.NextAttemptAt), b.RowsIn, b.Quarantined, b.Delivered, ns(b.FinishedAt), nextRev, doc(&snap), b.ID, b.Revision)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n == 0 {
				return ErrConflict
			}
		}
	}
	for _, cp := range c.Checkpoints {
		if _, err = exec(`DELETE FROM {p}checkpoints WHERE batch_id = ? AND stage = ?`, cp.BatchID, cp.Stage); err != nil {
			return err
		}
		if _, err = exec(`INSERT INTO {p}checkpoints (batch_id, stage, hash, doc) VALUES (?, ?, ?, ?)`, cp.BatchID, cp.Stage, cp.Hash, doc(&cp)); err != nil {
			return err
		}
	}
	for _, q := range c.Quarantine {
		if _, err = exec(`INSERT INTO {p}quarantine (batch_id, source_id, row_no, rule, doc, at) VALUES (?, ?, ?, ?, ?, ?)`, q.BatchID, q.SourceID, q.RowNo, q.Rule, doc(&q), ns(q.At)); err != nil {
			return err
		}
	}
	for _, l := range c.Lineage {
		var n int
		if err = tx.QueryRowContext(ctx, s.q(`SELECT COUNT(*) FROM {p}lineage WHERE batch_id = ?`), l.BatchID).Scan(&n); err != nil {
			return err
		}
		if _, err = exec(`INSERT INTO {p}lineage (batch_id, ord, doc) VALUES (?, ?, ?)`, l.BatchID, n+1, doc(&l)); err != nil {
			return err
		}
	}
	for _, d := range c.Counters {
		upsert := `INSERT INTO {p}counters (name, labels, value) VALUES (?, ?, ?) ON CONFLICT (name, labels) DO UPDATE SET value = {p}counters.value + excluded.value`
		if s.dialect == "mysql" {
			upsert = `INSERT INTO {p}counters (name, labels, value) VALUES (?, ?, ?) ON DUPLICATE KEY UPDATE value = value + VALUES(value)`
		}
		if _, err = exec(upsert, d.Name, d.Labels, d.Value); err != nil {
			return err
		}
	}
	if len(c.Audit) > 0 {
		lock := ""
		if s.dialect != "sqlite" {
			lock = " FOR UPDATE"
		}
		var seq int64
		var prev string
		if err = tx.QueryRowContext(ctx, s.q(`SELECT seq, hash FROM {p}audit_head WHERE id = 1`+lock)).Scan(&seq, &prev); err != nil {
			return err
		}
		for _, e := range c.Audit {
			seq++
			e.Seq, e.PrevHash, e.At = seq, prev, e.At.UTC()
			e.Hash = ChainHash(e)
			prev = e.Hash
			if _, err = exec(`INSERT INTO {p}audit (seq, batch_id, source_id, doc) VALUES (?, ?, ?, ?)`, e.Seq, e.BatchID, e.SourceID, doc(&e)); err != nil {
				return err
			}
		}
		if _, err = exec(`UPDATE {p}audit_head SET seq = ?, hash = ? WHERE id = 1`, seq, prev); err != nil {
			return err
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	if c.Batch != nil {
		c.Batch.Revision = nextRev
	}
	return nil
}

func load[T any](raw string, err error) (*T, error) {
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	out := new(T)
	if err := json.Unmarshal([]byte(raw), out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *SQLStore) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

func (s *SQLStore) GetRole(ctx context.Context, id string) (*Role, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, s.q(`SELECT doc FROM {p}roles WHERE id = ?`), id).Scan(&raw)
	return load[Role](raw, err)
}

func (s *SQLStore) ListRoles(ctx context.Context) ([]*Role, error) {
	list, err := docs[Role](ctx, s, `SELECT doc FROM {p}roles ORDER BY id`)
	return ptrs(list), err
}

// in builds "col IN (?,?,...)" for a source list; an empty list matches nothing.
func in(col string, ids []string) (string, []any) {
	if len(ids) == 0 {
		return "1 = 0", nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	return col + " IN (" + strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",") + ")", args
}

func (s *SQLStore) GetSource(ctx context.Context, id string) (*Source, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, s.q(`SELECT doc FROM {p}sources WHERE id = ?`), id).Scan(&raw)
	return load[Source](raw, err)
}

func (s *SQLStore) GetBatch(ctx context.Context, id string) (*Batch, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, s.q(`SELECT doc FROM {p}batches WHERE id = ?`), id).Scan(&raw)
	return load[Batch](raw, err)
}

func (s *SQLStore) FindBatch(ctx context.Context, sourceID, key string) (*Batch, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, s.q(`SELECT doc FROM {p}batches WHERE source_id = ? AND bkey = ?`), sourceID, key).Scan(&raw)
	return load[Batch](raw, err)
}

// docs runs a query that selects one doc column and decodes every row.
func docs[T any](ctx context.Context, s *SQLStore, query string, args ...any) ([]T, error) {
	rows, err := s.db.QueryContext(ctx, s.q(query), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []T
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var v T
		if err := json.Unmarshal([]byte(raw), &v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *SQLStore) ListSources(ctx context.Context) ([]*Source, error) {
	list, err := docs[Source](ctx, s, `SELECT doc FROM {p}sources ORDER BY id`)
	return ptrs(list), err
}

func ptrs[T any](l []T) []*T {
	out := make([]*T, len(l))
	for i := range l {
		out[i] = &l[i]
	}
	return out
}

func limitOf(n int) int {
	if n <= 0 {
		return 1000
	}
	return n
}

func (s *SQLStore) ListBatches(ctx context.Context, q Query) ([]*Batch, error) {
	where, args := []string{"1 = 1"}, []any{}
	if q.SourceID != "" {
		where, args = append(where, "source_id = ?"), append(args, q.SourceID)
	}
	if len(q.Statuses) > 0 {
		ph := strings.TrimSuffix(strings.Repeat("?,", len(q.Statuses)), ",")
		where = append(where, "status IN ("+ph+")")
		for _, st := range q.Statuses {
			args = append(args, st)
		}
	}
	if q.SourceIDs != nil {
		clause, a := in("source_id", q.SourceIDs)
		where, args = append(where, clause), append(args, a...)
	}
	args = append(args, limitOf(q.Limit), q.Offset)
	list, err := docs[Batch](ctx, s, `SELECT doc FROM {p}batches WHERE `+strings.Join(where, " AND ")+` ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?`, args...)
	return ptrs(list), err
}

func (s *SQLStore) LatestCheckpoint(ctx context.Context, batchID string) (*Checkpoint, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, s.q(`SELECT doc FROM {p}checkpoints WHERE batch_id = ? ORDER BY stage DESC LIMIT 1`), batchID).Scan(&raw)
	return load[Checkpoint](raw, err)
}

func (s *SQLStore) ListQuarantine(ctx context.Context, batchID string, sources []string, limit int) ([]Quarantine, error) {
	if batchID != "" {
		return docs[Quarantine](ctx, s, `SELECT doc FROM {p}quarantine WHERE batch_id = ? ORDER BY row_no LIMIT ?`, batchID, limitOf(limit))
	}
	where, args := "1 = 1", []any{}
	if sources != nil {
		where, args = in("source_id", sources)
	}
	return docs[Quarantine](ctx, s, `SELECT doc FROM {p}quarantine WHERE `+where+` ORDER BY at DESC, row_no LIMIT ?`, append(args, limitOf(limit))...)
}

func (s *SQLStore) ListAudit(ctx context.Context, batchID string, sources []string, limit, offset int) ([]AuditEntry, error) {
	where, args := []string{"1 = 1"}, []any{}
	if batchID != "" {
		where, args = append(where, "batch_id = ?"), append(args, batchID)
	}
	if sources != nil {
		clause, a := in("source_id", sources)
		where, args = append(where, clause), append(args, a...)
	}
	return docs[AuditEntry](ctx, s, `SELECT doc FROM {p}audit WHERE `+strings.Join(where, " AND ")+` ORDER BY seq DESC LIMIT ? OFFSET ?`, append(args, limitOf(limit), offset)...)
}

func (s *SQLStore) VerifyAudit(ctx context.Context) (int64, int64, error) {
	rows, err := s.db.QueryContext(ctx, s.q(`SELECT doc FROM {p}audit ORDER BY seq`))
	if err != nil {
		return 0, 0, err
	}
	defer rows.Close()
	var total, expect int64
	prev := ""
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return 0, 0, err
		}
		var e AuditEntry
		if err := json.Unmarshal([]byte(raw), &e); err != nil {
			return 0, 0, err
		}
		total++
		expect++
		if e.Seq != expect || e.PrevHash != prev || ChainHash(e) != e.Hash {
			return e.Seq, total, nil
		}
		prev = e.Hash
	}
	return 0, total, rows.Err()
}

func (s *SQLStore) Lineage(ctx context.Context, batchID string) ([]LineageEdge, error) {
	return docs[LineageEdge](ctx, s, `SELECT doc FROM {p}lineage WHERE batch_id = ? ORDER BY ord`, batchID)
}

func (s *SQLStore) Due(ctx context.Context, now time.Time, limit int) ([]*Batch, error) {
	list, err := docs[Batch](ctx, s, `SELECT doc FROM {p}batches WHERE status IN (?, ?) AND next_at <= ? AND lease_until <= ? ORDER BY next_at LIMIT ?`, StatusInFlight, StatusRetrying, ns(now), ns(now), limitOf(limit))
	return ptrs(list), err
}

func (s *SQLStore) Summary(ctx context.Context, sources []string) (Summary, error) {
	where, args := "1 = 1", []any{}
	if sources != nil {
		where, args = in("source_id", sources)
	}
	rows, err := s.db.QueryContext(ctx, s.q(`SELECT status, stage, COUNT(*), SUM(rows_in), SUM(quarantined), SUM(delivered) FROM {p}batches WHERE `+where+` GROUP BY status, stage`), args...)
	if err != nil {
		return Summary{}, err
	}
	defer rows.Close()
	var groups []group
	for rows.Next() {
		var g group
		if err := rows.Scan(&g.Status, &g.Stage, &g.Count, &g.RowsIn, &g.Quarantined, &g.Delivered); err != nil {
			return Summary{}, err
		}
		groups = append(groups, g)
	}
	return summarize(groups), rows.Err()
}

func (s *SQLStore) Series(ctx context.Context, since time.Time, bucket time.Duration, sources []string) ([]Point, error) {
	where, args := "1 = 1", []any{}
	if sources != nil {
		where, args = in("source_id", sources)
	}
	div := "/"
	if s.dialect == "mysql" {
		div = " DIV "
	}
	args = append([]any{int64(bucket)}, append(args, ns(since))...)
	rows, err := s.db.QueryContext(ctx, s.q(`SELECT created_at `+div+` ?, COUNT(*), SUM(rows_in), SUM(delivered), SUM(quarantined),
		SUM(CASE WHEN status = 'held' THEN 1 ELSE 0 END), SUM(CASE WHEN status = 'failed' THEN 1 ELSE 0 END)
		FROM {p}batches WHERE `+where+` AND created_at >= ? GROUP BY 1`), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	agg := map[int64]*Point{}
	for rows.Next() {
		var k int64
		p := &Point{}
		if err := rows.Scan(&k, &p.Batches, &p.RowsIn, &p.Delivered, &p.Quarantined, &p.Held, &p.Failed); err != nil {
			return nil, err
		}
		p.At = time.Unix(0, k*int64(bucket)).UTC()
		agg[k] = p
	}
	return fillSeries(agg, since, bucket), rows.Err()
}

func (s *SQLStore) Claim(ctx context.Context, id, owner string, now time.Time, ttl time.Duration) (*Batch, error) {
	var claimed bool
	err := s.withRetry(ctx, func() error {
		res, err := s.db.ExecContext(ctx, s.q(`UPDATE {p}batches SET lease_owner = ?, lease_until = ? WHERE id = ? AND (lease_until <= ? OR lease_owner = ?)`),
			owner, ns(now.Add(ttl)), id, ns(now), owner)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		claimed = n > 0
		return nil
	})
	if err != nil {
		return nil, err
	}
	b, gerr := s.GetBatch(ctx, id)
	if gerr != nil {
		return nil, gerr
	}
	if !claimed {
		return nil, ErrLeased
	}
	return b, nil
}

func (s *SQLStore) Release(ctx context.Context, id, owner string) error {
	return s.withRetry(ctx, func() error {
		_, err := s.db.ExecContext(ctx, s.q(`UPDATE {p}batches SET lease_owner = '', lease_until = 0 WHERE id = ? AND lease_owner = ?`), id, owner)
		return err
	})
}

func (s *SQLStore) FindByHash(ctx context.Context, sourceID, hash string) (*Batch, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, s.q(`SELECT doc FROM {p}batches WHERE source_id = ? AND content_hash = ? ORDER BY created_at LIMIT 1`), sourceID, hash).Scan(&raw)
	return load[Batch](raw, err)
}

func (s *SQLStore) Prune(ctx context.Context, before time.Time) (int, error) {
	var n int64
	err := s.withRetry(ctx, func() error {
		res, err := s.db.ExecContext(ctx, s.q(`DELETE FROM {p}checkpoints WHERE batch_id IN
			(SELECT id FROM {p}batches WHERE status IN ('delivered', 'failed') AND finished_at > 0 AND finished_at < ?)`), ns(before))
		if err != nil {
			return err
		}
		n, _ = res.RowsAffected()
		return nil
	})
	return int(n), err
}

func (s *SQLStore) Counters(ctx context.Context) ([]CounterRow, error) {
	rows, err := s.db.QueryContext(ctx, s.q(`SELECT name, labels, value FROM {p}counters ORDER BY name, labels`))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CounterRow{}
	for rows.Next() {
		var r CounterRow
		if err := rows.Scan(&r.Name, &r.Labels, &r.Value); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
