package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
)

// sqlBase is what the SQL-backed pieces share: the handle, the dialect and
// the table-name prefix. Dialect handling follows deploy.SQLStore: "sqlite",
// "postgres" or "mysql", with ? placeholders rewritten for postgres.
type sqlBase struct {
	db      *sql.DB
	dialect string
	prefix  string
}

var identRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func newSQLBase(db *sql.DB, dialect, prefix string) (sqlBase, error) {
	if db == nil {
		return sqlBase{}, errors.New("studio/server: a database is required")
	}
	switch dialect {
	case "postgres", "mysql", "sqlite":
	default:
		return sqlBase{}, fmt.Errorf("studio/server: unsupported dialect %q", dialect)
	}
	if prefix == "" {
		prefix = "studio_"
	}
	if !identRe.MatchString(prefix) {
		return sqlBase{}, fmt.Errorf("studio/server: invalid table prefix %q", prefix)
	}
	return sqlBase{db: db, dialect: dialect, prefix: prefix}, nil
}

// q expands {p} to the table prefix and, for postgres, numbers the
// placeholders.
func (b sqlBase) q(stmt string) string {
	stmt = strings.ReplaceAll(stmt, "{p}", b.prefix)
	if b.dialect != "postgres" {
		return stmt
	}
	var sb strings.Builder
	n := 0
	for _, c := range stmt {
		if c == '?' {
			n++
			fmt.Fprintf(&sb, "$%d", n)
			continue
		}
		sb.WriteRune(c)
	}
	return sb.String()
}

// migrate creates the tables. It is safe to run repeatedly.
func (b sqlBase) migrate(ctx context.Context) error {
	key, text := "TEXT", "TEXT"
	if b.dialect == "mysql" {
		key, text = "VARCHAR(191)", "LONGTEXT"
	}
	stmts := []string{
		// One row per draft; doc is the draftRecord JSON (hashes, not contents).
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS {p}drafts (
			id %[1]s PRIMARY KEY, owner %[1]s NOT NULL, version BIGINT NOT NULL, updated_ns BIGINT NOT NULL, doc %[2]s NOT NULL)`, key, text),
		// File contents, once per distinct hash within a draft.
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS {p}blobs (
			draft_id %[1]s NOT NULL, hash %[1]s NOT NULL, content %[2]s NOT NULL, PRIMARY KEY (draft_id, hash))`, key, text),
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS {p}audit (
			id BIGINT PRIMARY KEY, at_ns BIGINT NOT NULL, who %[1]s NOT NULL, action %[1]s NOT NULL, target %[1]s NOT NULL, detail %[2]s NOT NULL)`, key, text),
	}
	comments := fmt.Sprintf(`CREATE TABLE IF NOT EXISTS {p}comments (
			id %[1]s PRIMARY KEY, revision_id %[1]s NOT NULL, author %[1]s NOT NULL, at_ns BIGINT NOT NULL, body %[2]s NOT NULL, reply_to %[1]s NOT NULL`, key, text)
	if b.dialect == "mysql" {
		comments += `, KEY {p}comments_rev (revision_id, at_ns))`
		stmts = append(stmts, comments)
	} else {
		stmts = append(stmts, comments+`)`,
			`CREATE INDEX IF NOT EXISTS {p}comments_rev ON {p}comments (revision_id, at_ns)`)
	}
	for _, stmt := range stmts {
		if _, err := b.db.ExecContext(ctx, b.q(stmt)); err != nil {
			return fmt.Errorf("studio/server: migrate: %w", err)
		}
	}
	return nil
}

// OpenSQL migrates the studio tables in db and returns the draft store and
// the audit/comment sink over them. The store loads every saved draft, so
// drafts (with their undo history) survive a restart. dialect is "sqlite",
// "postgres" or "mysql"; prefix names the tables (default "studio_").
//
// One server owns a set of tables: the store keeps all drafts in memory and
// writes through, so two servers on the same tables would not see each
// other's edits.
func OpenSQL(ctx context.Context, db *sql.DB, dialect, prefix string) (*SQLStore, *SQLSink, error) {
	b, err := newSQLBase(db, dialect, prefix)
	if err != nil {
		return nil, nil, err
	}
	if err := b.migrate(ctx); err != nil {
		return nil, nil, err
	}
	st := &SQLStore{b: b, drafts: map[string]*Draft{}, known: map[string]map[string]bool{}}
	if err := st.load(ctx); err != nil {
		return nil, nil, err
	}
	return st, &SQLSink{b: b}, nil
}

// ---------------------------------------------------------------------------
// Drafts
// ---------------------------------------------------------------------------

// SQLStore is a Store that writes drafts through to SQL. Use OpenSQL.
type SQLStore struct {
	b sqlBase

	wmu   sync.Mutex // serialises writes; guards known
	known map[string]map[string]bool

	cmu    sync.RWMutex
	drafts map[string]*Draft

	// Skipped names drafts that could not be loaded (corrupt or missing
	// content); they are left in the database untouched.
	Skipped []string
}

var _ Store = (*SQLStore)(nil)

func (s *SQLStore) load(ctx context.Context) error {
	rows, err := s.b.db.QueryContext(ctx, s.b.q(`SELECT id, doc FROM {p}drafts ORDER BY updated_ns`))
	if err != nil {
		return err
	}
	type row struct{ id, doc string }
	var all []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.doc); err != nil {
			rows.Close()
			return err
		}
		all = append(all, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, r := range all {
		var rec draftRecord
		if err := json.Unmarshal([]byte(r.doc), &rec); err != nil {
			s.Skipped = append(s.Skipped, r.id)
			continue
		}
		blobs, err := s.loadBlobs(ctx, r.id)
		if err != nil {
			return err
		}
		d, err := restoreDraft(rec, blobs)
		if err != nil {
			s.Skipped = append(s.Skipped, r.id)
			continue
		}
		known := make(map[string]bool, len(blobs))
		for h := range blobs {
			known[h] = true
		}
		s.drafts[d.id] = d
		s.known[d.id] = known
	}
	return nil
}

func (s *SQLStore) loadBlobs(ctx context.Context, id string) (map[string]string, error) {
	rows, err := s.b.db.QueryContext(ctx, s.b.q(`SELECT hash, content FROM {p}blobs WHERE draft_id = ?`), id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var h, c string
		if err := rows.Scan(&h, &c); err != nil {
			return nil, err
		}
		out[h] = c
	}
	return out, rows.Err()
}

// Create implements Store.
func (s *SQLStore) Create(d *Draft) error {
	s.cmu.RLock()
	_, exists := s.drafts[d.id]
	s.cmu.RUnlock()
	if exists {
		return fmt.Errorf("draft %s exists", d.id)
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	if err := s.write(context.Background(), d); err != nil {
		return err
	}
	s.cmu.Lock()
	s.drafts[d.id] = d
	s.cmu.Unlock()
	return nil
}

// Save implements Store.
func (s *SQLStore) Save(d *Draft) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	return s.write(context.Background(), d)
}

// write stores d's current state. The caller holds wmu, so a later save
// always exports a later state.
func (s *SQLStore) write(ctx context.Context, d *Draft) (err error) {
	ex := d.export(s.known[d.id])
	doc, err := json.Marshal(ex.rec)
	if err != nil {
		return err
	}
	tx, err := s.b.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	var n int
	if err = tx.QueryRowContext(ctx, s.b.q(`SELECT COUNT(*) FROM {p}drafts WHERE id = ?`), d.id).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		_, err = tx.ExecContext(ctx, s.b.q(`INSERT INTO {p}drafts (id, owner, version, updated_ns, doc) VALUES (?, ?, ?, ?, ?)`),
			d.id, ex.rec.Owner, ex.rec.Version, ex.rec.Updated.UnixNano(), string(doc))
	} else {
		_, err = tx.ExecContext(ctx, s.b.q(`UPDATE {p}drafts SET owner = ?, version = ?, updated_ns = ?, doc = ? WHERE id = ?`),
			ex.rec.Owner, ex.rec.Version, ex.rec.Updated.UnixNano(), string(doc), d.id)
	}
	if err != nil {
		return err
	}
	for h, content := range ex.fresh {
		if _, err = tx.ExecContext(ctx, s.b.q(`INSERT INTO {p}blobs (draft_id, hash, content) VALUES (?, ?, ?)`), d.id, h, content); err != nil {
			return err
		}
	}
	for h := range s.known[d.id] {
		if !ex.needed[h] {
			if _, err = tx.ExecContext(ctx, s.b.q(`DELETE FROM {p}blobs WHERE draft_id = ? AND hash = ?`), d.id, h); err != nil {
				return err
			}
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	s.known[d.id] = ex.needed
	return nil
}

// Get implements Store.
func (s *SQLStore) Get(id string) (*Draft, bool) {
	s.cmu.RLock()
	defer s.cmu.RUnlock()
	d, ok := s.drafts[id]
	return d, ok
}

// Delete implements Store.
func (s *SQLStore) Delete(id string) bool {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	s.cmu.RLock()
	_, ok := s.drafts[id]
	s.cmu.RUnlock()
	if !ok {
		return false
	}
	ctx := context.Background()
	tx, err := s.b.db.BeginTx(ctx, nil)
	if err != nil {
		return false
	}
	if _, err = tx.ExecContext(ctx, s.b.q(`DELETE FROM {p}blobs WHERE draft_id = ?`), id); err == nil {
		_, err = tx.ExecContext(ctx, s.b.q(`DELETE FROM {p}drafts WHERE id = ?`), id)
	}
	if err != nil || tx.Commit() != nil {
		_ = tx.Rollback()
		return false
	}
	s.cmu.Lock()
	delete(s.drafts, id)
	s.cmu.Unlock()
	delete(s.known, id)
	return true
}

// List implements Store.
func (s *SQLStore) List() []*Draft {
	s.cmu.RLock()
	all := make(map[string]*Draft, len(s.drafts))
	for k, v := range s.drafts {
		all[k] = v
	}
	s.cmu.RUnlock()
	return sortDrafts(all)
}

// ---------------------------------------------------------------------------
// Audit and comments
// ---------------------------------------------------------------------------

// SQLSink stores the audit log and revision comments. Use OpenSQL.
type SQLSink struct{ b sqlBase }

var (
	_ AuditStore   = (*SQLSink)(nil)
	_ CommentStore = (*SQLSink)(nil)
)

// AppendAudit implements AuditStore.
func (s *SQLSink) AppendAudit(ctx context.Context, e AuditEntry) error {
	_, err := s.b.db.ExecContext(ctx, s.b.q(`INSERT INTO {p}audit (id, at_ns, who, action, target, detail) VALUES (?, ?, ?, ?, ?, ?)`),
		e.ID, e.At.UnixNano(), e.Who, e.Action, e.Target, e.Detail)
	return err
}

// ListAudit implements AuditStore.
func (s *SQLSink) ListAudit(ctx context.Context, before int64, limit int) ([]AuditEntry, error) {
	stmt := `SELECT id, at_ns, who, action, target, detail FROM {p}audit`
	var args []any
	if before > 0 {
		stmt += ` WHERE id < ?`
		args = append(args, before)
	}
	stmt += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.b.db.QueryContext(ctx, s.b.q(stmt), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AuditEntry{}
	for rows.Next() {
		var e AuditEntry
		var ns int64
		if err := rows.Scan(&e.ID, &ns, &e.Who, &e.Action, &e.Target, &e.Detail); err != nil {
			return nil, err
		}
		e.At = time.Unix(0, ns).UTC()
		out = append(out, e)
	}
	return out, rows.Err()
}

// AddComment implements CommentStore.
func (s *SQLSink) AddComment(ctx context.Context, c Comment) error {
	_, err := s.b.db.ExecContext(ctx, s.b.q(`INSERT INTO {p}comments (id, revision_id, author, at_ns, body, reply_to) VALUES (?, ?, ?, ?, ?, ?)`),
		c.ID, c.RevisionID, c.Author, c.At.UnixNano(), c.Body, c.ReplyTo)
	return err
}

// ListComments implements CommentStore.
func (s *SQLSink) ListComments(ctx context.Context, revisionID string) ([]Comment, error) {
	rows, err := s.b.db.QueryContext(ctx, s.b.q(`SELECT id, revision_id, author, at_ns, body, reply_to FROM {p}comments WHERE revision_id = ? ORDER BY at_ns, id`), revisionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Comment{}
	for rows.Next() {
		var c Comment
		var ns int64
		if err := rows.Scan(&c.ID, &c.RevisionID, &c.Author, &ns, &c.Body, &c.ReplyTo); err != nil {
			return nil, err
		}
		c.At = time.Unix(0, ns).UTC()
		out = append(out, c)
	}
	return out, rows.Err()
}
