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

	"github.com/oarkflow/ref/platform"
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

// Migrate creates the table, and adds the files and assets columns to a table
// created before bundle revisions (files) or assets existed. It is safe to run
// repeatedly.
func (s *SQLStore) Migrate(ctx context.Context) error {
	key, text := "TEXT", "TEXT"
	if s.dialect == "mysql" {
		key, text = "VARCHAR(191)", "LONGTEXT"
	}
	// files holds a bundle revision's files (JSON) and assets its templates and
	// static files (JSON); each is NULL for a revision with none, which is
	// every row written before they existed.
	_, err := s.db.ExecContext(ctx, s.q(fmt.Sprintf(`CREATE TABLE IF NOT EXISTS {t} (
		id %[1]s PRIMARY KEY, app %[1]s NOT NULL, seq BIGINT NOT NULL, status %[1]s NOT NULL, doc %[2]s NOT NULL,
		files %[2]s NULL,
		assets %[2]s NULL,
		UNIQUE (app, seq))`, key, text)))
	if err != nil {
		return err
	}
	for _, col := range []string{"files", "assets"} {
		has, err := s.hasColumn(ctx, col)
		if err != nil {
			return err
		}
		if has {
			continue
		}
		if _, err := s.db.ExecContext(ctx, s.q(fmt.Sprintf(`ALTER TABLE {t} ADD COLUMN %s %s NULL`, col, text))); err != nil {
			return err
		}
	}
	return nil
}

// hasColumn asks the catalog whether the table already has the column, so an
// ALTER runs only for a table that predates it. col is one of this package's
// own column names, never caller input.
func (s *SQLStore) hasColumn(ctx context.Context, col string) (bool, error) {
	var stmt string
	var args []any
	switch s.dialect {
	case "sqlite":
		stmt, args = `SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`, []any{s.table, col}
	case "postgres":
		stmt, args = `SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = lower(?) AND column_name = ?`, []any{s.table, col}
	default: // mysql
		stmt, args = `SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = ? AND column_name = ?`, []any{s.table, col}
	}
	var n int
	if err := s.db.QueryRowContext(ctx, s.q(stmt), args...).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}

// encodeRevision splits a revision into its JSON document and its files and
// assets columns. Files and assets live only in their columns, so a bundle's
// content is stored once beside Source rather than twice inside the document.
func encodeRevision(r *Revision) (doc string, files, assets sql.NullString, err error) {
	stripped := *r
	stripped.Files, stripped.Assets = nil, nil
	raw, err := json.Marshal(&stripped)
	if err != nil {
		return "", files, assets, err
	}
	column := func(v []platform.BundleFile) (sql.NullString, error) {
		if len(v) == 0 {
			return sql.NullString{}, nil
		}
		b, err := json.Marshal(v)
		if err != nil {
			return sql.NullString{}, err
		}
		return sql.NullString{String: string(b), Valid: true}, nil
	}
	if files, err = column(r.Files); err != nil {
		return "", files, assets, err
	}
	if assets, err = column(r.Assets); err != nil {
		return "", files, assets, err
	}
	return string(raw), files, assets, nil
}

func decodeRevision(doc string, files, assets sql.NullString) (*Revision, error) {
	var r Revision
	if err := json.Unmarshal([]byte(doc), &r); err != nil {
		return nil, err
	}
	if files.Valid && files.String != "" {
		if err := json.Unmarshal([]byte(files.String), &r.Files); err != nil {
			return nil, err
		}
	}
	if assets.Valid && assets.String != "" {
		if err := json.Unmarshal([]byte(assets.String), &r.Assets); err != nil {
			return nil, err
		}
	}
	return &r, nil
}

func (s *SQLStore) Create(ctx context.Context, r *Revision) error {
	doc, files, assets, err := encodeRevision(r)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, s.q(`INSERT INTO {t} (id, app, seq, status, doc, files, assets) VALUES (?, ?, ?, ?, ?, ?, ?)`), r.ID, r.App, r.Seq, r.Status, doc, files, assets)
	return err
}

func (s *SQLStore) Get(ctx context.Context, id string) (*Revision, error) {
	var (
		doc           string
		files, assets sql.NullString
	)
	err := s.db.QueryRowContext(ctx, s.q(`SELECT doc, files, assets FROM {t} WHERE id = ?`), id).Scan(&doc, &files, &assets)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: revision %q", ErrNotFound, id)
	}
	if err != nil {
		return nil, err
	}
	return decodeRevision(doc, files, assets)
}

func (s *SQLStore) Update(ctx context.Context, r *Revision) error {
	doc, files, assets, err := encodeRevision(r)
	if err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, s.q(`UPDATE {t} SET status = ?, doc = ?, files = ?, assets = ? WHERE id = ?`), r.Status, doc, files, assets, r.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: revision %q", ErrNotFound, r.ID)
	}
	return nil
}

func (s *SQLStore) List(ctx context.Context, app string, limit int) ([]*Revision, error) {
	stmt := `SELECT doc, files, assets FROM {t} WHERE app = ? ORDER BY seq DESC`
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
		var (
			doc           string
			files, assets sql.NullString
		)
		if err := rows.Scan(&doc, &files, &assets); err != nil {
			return nil, err
		}
		r, err := decodeRevision(doc, files, assets)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
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
