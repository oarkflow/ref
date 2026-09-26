package deploy

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// MemoryStore keeps revisions in memory (tests, single-process demos).
type MemoryStore struct {
	mu   sync.Mutex
	revs map[string]*Revision
	seq  map[string]int64
}

// NewMemoryStore returns an empty store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{revs: map[string]*Revision{}, seq: map[string]int64{}}
}

func clone(r *Revision) *Revision {
	raw, _ := json.Marshal(r)
	var out Revision
	_ = json.Unmarshal(raw, &out)
	return &out
}

func (s *MemoryStore) Create(_ context.Context, r *Revision) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.revs[r.ID] = clone(r)
	return nil
}

func (s *MemoryStore) Get(_ context.Context, id string) (*Revision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.revs[id]
	if !ok {
		return nil, fmt.Errorf("%w: revision %q", ErrNotFound, id)
	}
	return clone(r), nil
}

func (s *MemoryStore) Update(_ context.Context, r *Revision) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.revs[r.ID]; !ok {
		return fmt.Errorf("%w: revision %q", ErrNotFound, r.ID)
	}
	s.revs[r.ID] = clone(r)
	return nil
}

func (s *MemoryStore) List(_ context.Context, app string, limit int) ([]*Revision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*Revision
	for _, r := range s.revs {
		if r.App == app {
			out = append(out, clone(r))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seq > out[j].Seq })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *MemoryStore) NextSeq(_ context.Context, app string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq[app]++
	return s.seq[app], nil
}

// SQLStore keeps revisions in a database/sql database (PostgreSQL, MySQL,
// SQLite): one row per revision holding its JSON.
type SQLStore struct {
	db      *sql.DB
	dialect string
	table   string
}

var tableRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// NewSQLStore returns a store over table (default ref_revisions).
func NewSQLStore(db *sql.DB, dialect, table string) (*SQLStore, error) {
	if table == "" {
		table = "ref_revisions"
	}
	if !tableRe.MatchString(table) {
		return nil, fmt.Errorf("deploy: invalid table %q", table)
	}
	switch dialect {
	case "postgres", "mysql", "sqlite":
	default:
		return nil, fmt.Errorf("deploy: unsupported dialect %q", dialect)
	}
	return &SQLStore{db: db, dialect: dialect, table: table}, nil
}

func (s *SQLStore) q(stmt string) string {
	stmt = strings.ReplaceAll(stmt, "{t}", s.table)
	if s.dialect != "postgres" {
		return stmt
	}
	var b strings.Builder
	n := 0
	for _, c := range stmt {
		if c == '?' {
			n++
			fmt.Fprintf(&b, "$%d", n)
			continue
		}
		b.WriteRune(c)
	}
	return b.String()
}

// Migrate creates the table.
func (s *SQLStore) Migrate(ctx context.Context) error {
	key, text := "TEXT", "TEXT"
	if s.dialect == "mysql" {
		key, text = "VARCHAR(191)", "LONGTEXT"
	}
	_, err := s.db.ExecContext(ctx, s.q(fmt.Sprintf(`CREATE TABLE IF NOT EXISTS {t} (
		id %[1]s PRIMARY KEY, app %[1]s NOT NULL, seq BIGINT NOT NULL, status %[1]s NOT NULL, doc %[2]s NOT NULL,
		UNIQUE (app, seq))`, key, text)))
	return err
}

func (s *SQLStore) Create(ctx context.Context, r *Revision) error {
	doc, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, s.q(`INSERT INTO {t} (id, app, seq, status, doc) VALUES (?, ?, ?, ?, ?)`), r.ID, r.App, r.Seq, r.Status, string(doc))
	return err
}

func (s *SQLStore) Get(ctx context.Context, id string) (*Revision, error) {
	var doc string
	err := s.db.QueryRowContext(ctx, s.q(`SELECT doc FROM {t} WHERE id = ?`), id).Scan(&doc)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: revision %q", ErrNotFound, id)
	}
	if err != nil {
		return nil, err
	}
	var r Revision
	return &r, json.Unmarshal([]byte(doc), &r)
}

func (s *SQLStore) Update(ctx context.Context, r *Revision) error {
	doc, err := json.Marshal(r)
	if err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, s.q(`UPDATE {t} SET status = ?, doc = ? WHERE id = ?`), r.Status, string(doc), r.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: revision %q", ErrNotFound, r.ID)
	}
	return nil
}

func (s *SQLStore) List(ctx context.Context, app string, limit int) ([]*Revision, error) {
	stmt := `SELECT doc FROM {t} WHERE app = ? ORDER BY seq DESC`
	args := []any{app}
	if limit > 0 {
		stmt += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, s.q(stmt), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Revision
	for rows.Next() {
		var doc string
		if err := rows.Scan(&doc); err != nil {
			return nil, err
		}
		var r Revision
		if err := json.Unmarshal([]byte(doc), &r); err != nil {
			return nil, err
		}
		out = append(out, &r)
	}
	return out, rows.Err()
}

// NextSeq is one more than the highest sequence (the UNIQUE (app, seq)
// constraint rejects a concurrent duplicate).
func (s *SQLStore) NextSeq(ctx context.Context, app string) (int64, error) {
	var n sql.NullInt64
	if err := s.db.QueryRowContext(ctx, s.q(`SELECT MAX(seq) FROM {t} WHERE app = ?`), app).Scan(&n); err != nil {
		return 0, err
	}
	return n.Int64 + 1, nil
}
