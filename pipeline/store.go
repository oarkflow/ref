package pipeline

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Query filters a case listing.
type Query struct {
	Pipeline  string
	TenantID  string
	CreatedBy string
	// Stages keeps cases whose current stage is one of these (a work queue).
	Stages   []string
	Statuses []string
	OrgUnits []string
	Limit    int
	Offset   int
}

// Store persists cases. Update must fail with ErrConflict when the stored
// revision differs from the case's, so concurrent edits never overwrite
// each other silently.
type Store interface {
	Create(ctx context.Context, c *Case) error
	Get(ctx context.Context, id string) (*Case, error)
	Update(ctx context.Context, c *Case) error
	List(ctx context.Context, q Query) ([]*Case, error)
	NextSeq(ctx context.Context, pipeline string) (int64, error)
	// FindCertificate looks a certificate up by id, number or code.
	FindCertificate(ctx context.Context, key string) (*Certificate, error)
}

func (q Query) matches(c *Case) bool {
	return (q.Pipeline == "" || c.Pipeline == q.Pipeline) &&
		(q.TenantID == "" || c.TenantID == q.TenantID) &&
		(q.CreatedBy == "" || c.CreatedBy == q.CreatedBy) &&
		(len(q.Stages) == 0 || slices.Contains(q.Stages, c.Stage)) &&
		(len(q.Statuses) == 0 || slices.Contains(q.Statuses, c.Status)) &&
		(len(q.OrgUnits) == 0 || slices.Contains(q.OrgUnits, c.OrgUnit))
}

// ---------------------------------------------------------------------------
// Memory
// ---------------------------------------------------------------------------

// MemoryStore keeps cases in memory, for tests and single-process demos.
type MemoryStore struct {
	mu    sync.Mutex
	cases map[string]*Case
	seq   map[string]int64
}

// NewMemoryStore returns an empty store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{cases: map[string]*Case{}, seq: map[string]int64{}}
}

func (s *MemoryStore) Create(_ context.Context, c *Case) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, dup := s.cases[c.ID]; dup {
		return fmt.Errorf("pipeline: case %q already exists", c.ID)
	}
	c.Revision = 1
	s.cases[c.ID] = c.Clone()
	return nil
}

func (s *MemoryStore) Get(_ context.Context, id string) (*Case, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.cases[id]
	if !ok {
		return nil, fmt.Errorf("%w: case %q", ErrNotFound, id)
	}
	return c.Clone(), nil
}

func (s *MemoryStore) Update(_ context.Context, c *Case) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, ok := s.cases[c.ID]
	if !ok {
		return fmt.Errorf("%w: case %q", ErrNotFound, c.ID)
	}
	if cur.Revision != c.Revision {
		return ErrConflict
	}
	c.Revision++
	s.cases[c.ID] = c.Clone()
	return nil
}

func (s *MemoryStore) List(_ context.Context, q Query) ([]*Case, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*Case
	for _, c := range s.cases {
		if q.matches(c) {
			out = append(out, c.Clone())
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return page(out, q), nil
}

func page(cases []*Case, q Query) []*Case {
	if q.Offset > 0 {
		if q.Offset >= len(cases) {
			return nil
		}
		cases = cases[q.Offset:]
	}
	if q.Limit > 0 && len(cases) > q.Limit {
		cases = cases[:q.Limit]
	}
	return cases
}

func (s *MemoryStore) NextSeq(_ context.Context, pipeline string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq[pipeline]++
	return s.seq[pipeline], nil
}

func (s *MemoryStore) FindCertificate(_ context.Context, key string) (*Certificate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.cases {
		for _, cert := range c.Certificates {
			if cert.ID == key || cert.Number == key || cert.Code == key {
				out := cert
				return &out, nil
			}
		}
	}
	return nil, fmt.Errorf("%w: certificate %q", ErrNotFound, key)
}

// ---------------------------------------------------------------------------
// SQL
// ---------------------------------------------------------------------------

// SQLStore persists cases in any database/sql database (PostgreSQL, MySQL,
// SQLite). The case document is stored as JSON; the columns a work queue
// filters on are denormalised next to it and indexed.
type SQLStore struct {
	db      *sql.DB
	dialect string // postgres, mysql or sqlite
	prefix  string
}

var identRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// NewSQLStore returns a store using tables named <prefix>cases,
// <prefix>certificates and <prefix>sequences.
func NewSQLStore(db *sql.DB, dialect, prefix string) (*SQLStore, error) {
	if prefix == "" {
		prefix = "pipeline_"
	}
	if !identRe.MatchString(prefix) {
		return nil, fmt.Errorf("pipeline: invalid table prefix %q", prefix)
	}
	switch dialect {
	case "postgres", "mysql", "sqlite":
	default:
		return nil, fmt.Errorf("pipeline: unsupported dialect %q", dialect)
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

// Migrate creates the tables.
func (s *SQLStore) Migrate(ctx context.Context) error {
	key, text := "TEXT", "TEXT"
	if s.dialect == "mysql" {
		key, text = "VARCHAR(191)", "LONGTEXT"
	}
	statements := []string{
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS {p}cases (
			id %[1]s PRIMARY KEY, pipeline %[1]s NOT NULL, number %[1]s NOT NULL, tenant_id %[1]s NOT NULL DEFAULT '',
			org_unit %[1]s NOT NULL DEFAULT '', status %[1]s NOT NULL, stage %[1]s NOT NULL, created_by %[1]s NOT NULL DEFAULT '',
			revision BIGINT NOT NULL, doc %[2]s NOT NULL, created_at BIGINT NOT NULL, updated_at BIGINT NOT NULL)`, key, text),
		`CREATE INDEX IF NOT EXISTS {p}cases_queue_idx ON {p}cases (pipeline, stage, status)`,
		`CREATE INDEX IF NOT EXISTS {p}cases_owner_idx ON {p}cases (created_by)`,
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS {p}certificates (
			id %[1]s PRIMARY KEY, number %[1]s NOT NULL, code %[1]s NOT NULL, case_id %[1]s NOT NULL, doc %[2]s NOT NULL)`, key, text),
		`CREATE INDEX IF NOT EXISTS {p}certificates_number_idx ON {p}certificates (number)`,
		`CREATE INDEX IF NOT EXISTS {p}certificates_code_idx ON {p}certificates (code)`,
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS {p}sequences (pipeline %s PRIMARY KEY, seq BIGINT NOT NULL)`, key),
	}
	if s.dialect == "mysql" {
		// MySQL has no CREATE INDEX IF NOT EXISTS; the tables' creation above is
		// idempotent and indexes are created once, ignoring "duplicate key".
		for i, st := range statements {
			statements[i] = strings.Replace(st, "CREATE INDEX IF NOT EXISTS", "CREATE INDEX", 1)
		}
	}
	for _, st := range statements {
		if _, err := s.db.ExecContext(ctx, s.q(st)); err != nil {
			if s.dialect == "mysql" && strings.Contains(err.Error(), "Duplicate key name") {
				continue
			}
			return err
		}
	}
	return nil
}

func (s *SQLStore) Create(ctx context.Context, c *Case) error {
	c.Revision = 1
	doc, err := json.Marshal(c)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, s.q(`INSERT INTO {p}cases (id, pipeline, number, tenant_id, org_unit, status, stage, created_by, revision, doc, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		c.ID, c.Pipeline, c.Number, c.TenantID, c.OrgUnit, c.Status, c.Stage, c.CreatedBy, c.Revision, string(doc),
		c.CreatedAt.UnixNano(), c.UpdatedAt.UnixNano()); err != nil {
		return err
	}
	if err := s.syncCertificates(ctx, tx, c); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLStore) Get(ctx context.Context, id string) (*Case, error) {
	var doc string
	err := s.db.QueryRowContext(ctx, s.q(`SELECT doc FROM {p}cases WHERE id = ?`), id).Scan(&doc)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: case %q", ErrNotFound, id)
	}
	if err != nil {
		return nil, err
	}
	var c Case
	if err := json.Unmarshal([]byte(doc), &c); err != nil {
		return nil, fmt.Errorf("pipeline: decode case %q: %w", id, err)
	}
	return &c, nil
}

func (s *SQLStore) Update(ctx context.Context, c *Case) error {
	expected := c.Revision
	c.Revision++
	doc, err := json.Marshal(c)
	if err != nil {
		c.Revision = expected
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		c.Revision = expected
		return err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, s.q(`UPDATE {p}cases SET status = ?, stage = ?, org_unit = ?, revision = ?, doc = ?, updated_at = ?
		WHERE id = ? AND revision = ?`),
		c.Status, c.Stage, c.OrgUnit, c.Revision, string(doc), c.UpdatedAt.UnixNano(), c.ID, expected)
	if err != nil {
		c.Revision = expected
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		c.Revision = expected
		if _, getErr := s.Get(ctx, c.ID); errors.Is(getErr, ErrNotFound) {
			return getErr
		}
		return ErrConflict
	}
	if err := s.syncCertificates(ctx, tx, c); err != nil {
		c.Revision = expected
		return err
	}
	if err := tx.Commit(); err != nil {
		c.Revision = expected
		return err
	}
	return nil
}

func (s *SQLStore) syncCertificates(ctx context.Context, tx *sql.Tx, c *Case) error {
	for _, cert := range c.Certificates {
		doc, err := json.Marshal(cert)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, s.q(`DELETE FROM {p}certificates WHERE id = ?`), cert.ID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, s.q(`INSERT INTO {p}certificates (id, number, code, case_id, doc) VALUES (?, ?, ?, ?, ?)`),
			cert.ID, cert.Number, cert.Code, c.ID, string(doc)); err != nil {
			return err
		}
	}
	return nil
}

func (s *SQLStore) List(ctx context.Context, q Query) ([]*Case, error) {
	where := []string{"1 = 1"}
	var args []any
	add := func(column string, values []string) {
		if len(values) == 0 {
			return
		}
		ph := make([]string, len(values))
		for i, v := range values {
			ph[i] = "?"
			args = append(args, v)
		}
		where = append(where, column+" IN ("+strings.Join(ph, ", ")+")")
	}
	for column, value := range map[string]string{"pipeline": q.Pipeline, "tenant_id": q.TenantID, "created_by": q.CreatedBy} {
		if value != "" {
			add(column, []string{value})
		}
	}
	add("stage", q.Stages)
	add("status", q.Statuses)
	add("org_unit", q.OrgUnits)
	limit := q.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	statement := "SELECT doc FROM {p}cases WHERE " + strings.Join(where, " AND ") + " ORDER BY created_at DESC LIMIT ? OFFSET ?"
	args = append(args, limit, max(0, q.Offset))
	rows, err := s.db.QueryContext(ctx, s.q(statement), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Case
	for rows.Next() {
		var doc string
		if err := rows.Scan(&doc); err != nil {
			return nil, err
		}
		var c Case
		if err := json.Unmarshal([]byte(doc), &c); err != nil {
			return nil, err
		}
		out = append(out, &c)
	}
	return out, rows.Err()
}

func (s *SQLStore) NextSeq(ctx context.Context, pipeline string) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, s.q(`UPDATE {p}sequences SET seq = seq + 1 WHERE pipeline = ?`), pipeline)
	if err != nil {
		return 0, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if _, err := tx.ExecContext(ctx, s.q(`INSERT INTO {p}sequences (pipeline, seq) VALUES (?, 1)`), pipeline); err != nil {
			// Lost a race to create the row: retry the increment once.
			_ = tx.Rollback()
			return s.nextSeqRetry(ctx, pipeline)
		}
	}
	var seq int64
	if err := tx.QueryRowContext(ctx, s.q(`SELECT seq FROM {p}sequences WHERE pipeline = ?`), pipeline).Scan(&seq); err != nil {
		return 0, err
	}
	return seq, tx.Commit()
}

func (s *SQLStore) nextSeqRetry(ctx context.Context, pipeline string) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, s.q(`UPDATE {p}sequences SET seq = seq + 1 WHERE pipeline = ?`), pipeline); err != nil {
		return 0, err
	}
	var seq int64
	if err := tx.QueryRowContext(ctx, s.q(`SELECT seq FROM {p}sequences WHERE pipeline = ?`), pipeline).Scan(&seq); err != nil {
		return 0, err
	}
	return seq, tx.Commit()
}

func (s *SQLStore) FindCertificate(ctx context.Context, key string) (*Certificate, error) {
	var doc string
	err := s.db.QueryRowContext(ctx, s.q(`SELECT doc FROM {p}certificates WHERE id = ? OR number = ? OR code = ? LIMIT 1`), key, key, key).Scan(&doc)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: certificate %q", ErrNotFound, key)
	}
	if err != nil {
		return nil, err
	}
	var cert Certificate
	if err := json.Unmarshal([]byte(doc), &cert); err != nil {
		return nil, err
	}
	return &cert, nil
}
