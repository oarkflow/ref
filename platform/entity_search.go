package platform

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// Entity search indexing. An entity declared `search_index true` keeps a
// token table beside its own (<table>_search): one row per record and word
// of its search columns, lower-cased and accent-folded. The rows are written
// in the same transaction as the change, so the index never disagrees with a
// committed record, and ?q= matches every query word as a prefix of some
// indexed word of the row ("bri rep" finds "Bridge repair", "zurich" finds
// "Zürich") through an ordinary indexed LIKE 'prefix%' on every dialect.
//
// An existing table is indexed at startup when its token table is empty, and
// the entity.reindex action rebuilds it on demand.

const (
	// searchTokenRunes bounds one indexed word; a longer one is cut, and a
	// query word is cut the same way so it still matches.
	searchTokenRunes = 64
	// searchRowTokens bounds the distinct words indexed per record.
	searchRowTokens = 512
	// searchQueryTokens bounds the words of one ?q=.
	searchQueryTokens = 8
	// searchPage is how many records a backfill or rebuild reads at a time.
	searchPage = 500
)

// searchFolds are the letters an NFKD decomposition leaves alone but a reader
// treats as plain ones.
var searchFolds = map[rune]string{
	'ß': "ss", 'æ': "ae", 'œ': "oe", 'ø': "o", 'đ': "d", 'ð': "d", 'ł': "l", 'þ': "th", 'ı': "i", 'ħ': "h",
}

// searchTokens splits text into distinct normalised words: compatibility
// decomposed, combining marks dropped, lower-cased, split on anything that is
// not a letter or digit. At most limit words are returned.
func searchTokens(text string, limit int) []string {
	var (
		out  []string
		seen = map[string]bool{}
		word strings.Builder
	)
	flush := func() {
		if word.Len() == 0 {
			return
		}
		tok := word.String()
		word.Reset()
		if r := []rune(tok); len(r) > searchTokenRunes {
			tok = string(r[:searchTokenRunes])
		}
		if !seen[tok] {
			seen[tok] = true
			out = append(out, tok)
		}
	}
	for _, r := range norm.NFKD.String(text) {
		switch {
		case unicode.Is(unicode.Mn, r):
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			r = unicode.ToLower(r)
			if s, ok := searchFolds[r]; ok {
				word.WriteString(s)
			} else {
				word.WriteRune(r)
			}
		default:
			flush()
			if len(out) >= limit {
				return out[:limit]
			}
		}
	}
	flush()
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

func (p *entityPlan) searchTable() string { return p.table + "_search" }

// searchDDL creates the token table. The token index serves LIKE 'prefix%':
// PostgreSQL needs text_pattern_ops for that under a non-C collation, and
// SQLite a NOCASE column (tokens are lower-case either way).
func (p *entityPlan) searchDDL() []string {
	key, tok, ifNotExists := "TEXT", "TEXT", "IF NOT EXISTS "
	switch p.dialect {
	case "mysql":
		key, tok, ifNotExists = "VARCHAR(191)", "VARCHAR(191)", ""
	case "sqlite":
		tok = "TEXT COLLATE NOCASE"
	}
	t := p.searchTable()
	ops := ""
	if p.dialect == "postgres" {
		ops = " text_pattern_ops"
	}
	return []string{
		fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s (record_id %s NOT NULL, token %s NOT NULL, PRIMARY KEY (record_id, token))", t, key, tok),
		fmt.Sprintf("CREATE INDEX %s%s_token_idx ON %s (token%s)", ifNotExists, t, t, ops),
	}
}

// searchCondition is the ?q= condition over the token table: every query
// word must prefix an indexed word of the row. Words are letters and digits
// only, so they carry no LIKE wildcard.
func (p *entityPlan) searchCondition(raw string, args []any) ([]string, []any) {
	var conds []string
	for _, tok := range searchTokens(raw, searchQueryTokens) {
		args = append(args, tok+"%")
		conds = append(conds, fmt.Sprintf("id IN (SELECT record_id FROM %s WHERE token LIKE $%d)", p.searchTable(), len(args)))
	}
	return conds, args
}

// rowTokens are the words of a stored row's search columns.
func (p *entityPlan) rowTokens(row map[string]any) []string {
	var text []string
	for _, col := range p.spec.Search {
		if v := row[col]; v != nil {
			text = append(text, Stringify(v))
		}
	}
	return searchTokens(strings.Join(text, " "), searchRowTokens)
}

// insertTokens writes (record, token) pairs, ignoring pairs already present
// (a concurrent backfill may have written them), in multi-row chunks.
func (p *entityPlan) insertTokens(ctx context.Context, q execer, pairs [][2]string) error {
	prefix, suffix := "INSERT INTO ", " ON CONFLICT DO NOTHING"
	if p.dialect == "mysql" {
		prefix, suffix = "INSERT IGNORE INTO ", ""
	}
	for start := 0; start < len(pairs); start += 400 {
		chunk := pairs[start:min(start+400, len(pairs))]
		values := make([]string, len(chunk))
		args := make([]any, 0, 2*len(chunk))
		for i, pair := range chunk {
			args = append(args, pair[0], pair[1])
			values[i] = fmt.Sprintf("($%d, $%d)", len(args)-1, len(args))
		}
		stmt := prefix + p.searchTable() + " (record_id, token) VALUES " + strings.Join(values, ", ") + suffix
		if _, err := q.ExecContext(ctx, rebind(p.dialect, stmt), args...); err != nil {
			return err
		}
	}
	return nil
}

// reindexRow replaces a record's tokens from its stored row, read through q
// (the write's transaction); remove drops them (a delete).
func (p *entityPlan) reindexRow(ctx context.Context, q execer, id string, remove bool) error {
	if _, err := q.ExecContext(ctx, rebind(p.dialect, "DELETE FROM "+p.searchTable()+" WHERE record_id = $1"), id); err != nil {
		return err
	}
	if remove {
		return nil
	}
	stmt := fmt.Sprintf("SELECT %s FROM %s WHERE id = $1", strings.Join(p.spec.Search, ", "), p.table)
	rows, err := queryRows(ctx, q, rebind(p.dialect, stmt), []any{id})
	if err != nil || len(rows) == 0 {
		return err
	}
	var pairs [][2]string
	for _, tok := range p.rowTokens(rows[0]) {
		pairs = append(pairs, [2]string{id, tok})
	}
	return p.insertTokens(ctx, q, pairs)
}

// ---------------------------------------------------------------------------
// Backfill and rebuild
// ---------------------------------------------------------------------------

// entitySearchIndex is one entity's token table on its database, registered
// when the entity's actions are built so the database can backfill it.
type entitySearchIndex struct {
	plan *entityPlan
	db   *Database
}

// registerSearchIndex records an entity's index for the startup backfill
// (idempotent per table).
func (d *Database) registerSearchIndex(plan *entityPlan) {
	d.eventsMu.Lock()
	defer d.eventsMu.Unlock()
	if d.searchIndexes == nil {
		d.searchIndexes = map[string]*entitySearchIndex{}
	}
	if d.searchIndexes[plan.table] == nil {
		d.searchIndexes[plan.table] = &entitySearchIndex{plan: plan, db: d}
	}
}

// backfill indexes every record when the token table is empty: an entity
// that just turned search_index on, or a restored table.
func (s *entitySearchIndex) backfill(ctx context.Context) {
	rows, err := queryRows(ctx, s.db.DB, "SELECT record_id FROM "+s.plan.searchTable()+" LIMIT 1", nil)
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("entity search index check failed", "entity", s.plan.spec.Name, "error", err)
		}
		return
	}
	if len(rows) > 0 {
		return
	}
	n, err := s.rebuild(ctx, false)
	switch {
	case err != nil && ctx.Err() == nil:
		slog.Warn("entity search index backfill failed", "entity", s.plan.spec.Name, "error", err)
	case n > 0:
		slog.Info("entity search index backfilled", "entity", s.plan.spec.Name, "records", n)
	}
}

// rebuild re-derives the tokens of every live record, a page per
// transaction, and returns how many records it indexed. With prune it also
// drops tokens whose record is gone or soft-deleted.
func (s *entitySearchIndex) rebuild(ctx context.Context, prune bool) (int, error) {
	p := s.plan
	live := ""
	if p.spec.SoftDelete {
		live = " AND deleted_at IS NULL"
	}
	cols := strings.Join(append([]string{"id"}, p.spec.Search...), ", ")
	after, total := "", 0
	for {
		stmt := fmt.Sprintf("SELECT %s FROM %s WHERE id > $1%s ORDER BY id LIMIT %d", cols, p.table, live, searchPage)
		rows, err := queryRows(ctx, s.db.DB, rebind(p.dialect, stmt), []any{after})
		if err != nil {
			return total, err
		}
		if len(rows) == 0 {
			break
		}
		if err := s.indexPage(ctx, rows); err != nil {
			return total, err
		}
		total += len(rows)
		after = Stringify(rows[len(rows)-1]["id"])
		if len(rows) < searchPage {
			break
		}
	}
	if prune {
		where := ""
		if p.spec.SoftDelete {
			where = " WHERE deleted_at IS NULL"
		}
		stmt := fmt.Sprintf("DELETE FROM %s WHERE record_id NOT IN (SELECT id FROM %s%s)", p.searchTable(), p.table, where)
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return total, err
		}
	}
	return total, nil
}

// indexPage replaces the tokens of a page of records in one transaction.
func (s *entitySearchIndex) indexPage(ctx context.Context, rows []map[string]any) error {
	p := s.plan
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // a no-op after commit
	ph := make([]string, len(rows))
	ids := make([]any, len(rows))
	var pairs [][2]string
	for i, row := range rows {
		id := Stringify(row["id"])
		ids[i], ph[i] = id, fmt.Sprintf("$%d", i+1)
		for _, tok := range p.rowTokens(row) {
			pairs = append(pairs, [2]string{id, tok})
		}
	}
	stmt := fmt.Sprintf("DELETE FROM %s WHERE record_id IN (%s)", p.searchTable(), strings.Join(ph, ", "))
	if _, err := tx.ExecContext(ctx, rebind(p.dialect, stmt), ids...); err != nil {
		return err
	}
	if err := p.insertTokens(ctx, tx, pairs); err != nil {
		return err
	}
	return tx.Commit()
}

// backfillSearchIndexes starts the backfill of every registered index,
// concurrently; wait returns once they have all finished.
func (d *Database) backfillSearchIndexes(ctx context.Context) (wait func()) {
	d.eventsMu.Lock()
	indexes := make([]*entitySearchIndex, 0, len(d.searchIndexes))
	for _, s := range d.searchIndexes {
		indexes = append(indexes, s)
	}
	d.eventsMu.Unlock()
	done := make(chan struct{}, len(indexes))
	for _, s := range indexes {
		go func() {
			defer func() { done <- struct{}{} }()
			s.backfill(ctx)
		}()
	}
	return func() {
		for range indexes {
			<-done
		}
	}
}

// ---------------------------------------------------------------------------
// The entity.reindex action
// ---------------------------------------------------------------------------

func registerEntitySearchActions(r *Registry) {
	mustAction(r, "entity.reindex", ActionFactoryFunc(buildEntityReindex), ActionInfo{
		Family: "data", Kind: "effect",
		Summary:  "Rebuild an entity's search index from its stored records (search_index true)",
		Provides: "{entity, indexed}",
		Config: []ConfigField{
			{Name: "entity", Type: "string", Required: true},
			{Name: "roles", Type: "[]string", Summary: "Only principals holding one of these roles may call it"},
		},
	})
}

func buildEntityReindex(build BuildContext, spec NodeSpec) (Action, error) {
	db, err := requireResource[*Database](build, spec, "a database.sql resource")
	if err != nil {
		return nil, err
	}
	if err := rejectUnknownConfig("entity.reindex", spec.Config, "entity", "roles"); err != nil {
		return nil, fmt.Errorf("node %q: %w", spec.Name, err)
	}
	if len(spec.Provides) != 1 {
		return nil, fmt.Errorf("node %q: entity.reindex provides exactly one fact", spec.Name)
	}
	plan, err := entityPlanFor(build, spec, db, configString(spec.Config, "entity", ""))
	if err != nil {
		return nil, err
	}
	if !plan.spec.SearchIndex {
		return nil, fmt.Errorf("node %q: entity %q does not declare search_index true", spec.Name, plan.spec.Name)
	}
	if plan.spec.Database != spec.Resource {
		return nil, fmt.Errorf("node %q: entity %q lives in database %q, not %q", spec.Name, plan.spec.Name, plan.spec.Database, spec.Resource)
	}
	roles := configStrings(spec.Config, "roles")
	index := &entitySearchIndex{plan: plan, db: db}
	return ActionFunc(func(ctx *ActionContext) (ActionResult, error) {
		if len(roles) > 0 && !slices.ContainsFunc(roles, ctx.Principal.HasRole) {
			return ActionResult{}, permissionDenied("rebuilding the search index needs one of the roles " + strings.Join(roles, ", "))
		}
		n, err := index.rebuild(ctx.Context, true)
		if err != nil {
			return ActionResult{}, databaseFailure(err)
		}
		return singleOutput(spec, map[string]any{"entity": plan.spec.Name, "indexed": n}), nil
	}), nil
}

// entityPlanFor compiles a declared entity for an action node.
func entityPlanFor(build BuildContext, spec NodeSpec, db *Database, name string) (*entityPlan, error) {
	if build.Document == nil {
		return nil, fmt.Errorf("node %q: %s needs the document", spec.Name, spec.Uses)
	}
	for i := range build.Document.Entities {
		if build.Document.Entities[i].Name == name {
			return compileEntity(build.Document.Entities[i], db.Dialect)
		}
	}
	return nil, fmt.Errorf("node %q: unknown entity %q", spec.Name, name)
}
