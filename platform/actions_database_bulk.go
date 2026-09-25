package platform

import (
	"database/sql"
	"fmt"
	"slices"
	"sort"
	"strings"
)

// database.insert_many writes a list of rows — the line items of an order, the
// per-day lines dos.expand produced for a multi-DOS encounter — in one
// transaction. database.transaction runs a fixed list of statements and cannot
// loop over a variable-length array, which is the gap this fills.
//
// As with database.crud, every identifier is validated at load time and every
// value is bound, so request data never reaches the SQL text.

func registerBulkDatabaseActions(r *Registry) {
	mustAction(r, "database.insert_many", ActionFactoryFunc(buildInsertMany), ActionInfo{
		Family:  "database",
		Summary: "Insert a list of rows in one transaction, chunked under the driver's parameter limit",
		Config: []ConfigField{
			{Name: "table", Type: "string", Required: true},
			{Name: "columns", Type: "[]string", Required: true, Summary: "Columns read from each row; a missing key inserts NULL"},
			{Name: "rows", Type: "fact", Required: true, Summary: "Fact path of the row list"},
			{Name: "set", Type: "object", Summary: "Columns filled from a fact for every row, e.g. { encounter_id \"encounter.id\" }"},
			{Name: "tenant_column", Type: "string", Summary: "Stamp every row with the request's tenant"},
			{Name: "max_rows", Type: "int", Default: "10000", Summary: "Reject larger lists"},
			{Name: "allow_empty", Type: "bool", Default: "true", Summary: "An empty list is a no-op rather than an error"},
			{Name: "parent", Type: "object", Summary: "Insert a header row first, in the same transaction: { table values { column \"fact.path\" } key \"id\" link \"parent_id\" returning [..] }"},
		},
		Kind: "effect",
	})
}

type insertManyPlan struct {
	db         *Database
	table      string
	columns    []string
	rowsPath   string
	set        map[string]string // column -> fact path
	setColumns []string
	tenant     string
	maxRows    int
	allowEmpty bool
	parent     *parentInsert
}

// parentInsert is the optional header row written before the children, so an
// order and its items (or an encounter and its lines) commit or fail together.
type parentInsert struct {
	table     string
	columns   []string
	paths     []string
	key       string
	link      string
	returning []string
}

func buildInsertMany(build BuildContext, spec NodeSpec) (Action, error) {
	db, err := requireResource[*Database](build, spec, "a database.sql resource")
	if err != nil {
		return nil, err
	}
	if err := rejectUnknownConfig("database.insert_many", spec.Config,
		"table", "columns", "rows", "set", "tenant_column", "max_rows", "allow_empty", "parent"); err != nil {
		return nil, fmt.Errorf("node %q: %w", spec.Name, err)
	}
	p := &insertManyPlan{db: db, rowsPath: configString(spec.Config, "rows", ""), set: map[string]string{},
		allowEmpty: configBool(spec.Config, "allow_empty", true)}
	if p.rowsPath == "" {
		return nil, fmt.Errorf("node %q: database.insert_many needs config.rows", spec.Name)
	}
	if p.table, err = safeIdentifier(configString(spec.Config, "table", "")); err != nil {
		return nil, fmt.Errorf("node %q: %w", spec.Name, err)
	}
	if p.columns, err = safeIdentifiers(configStrings(spec.Config, "columns")); err != nil {
		return nil, fmt.Errorf("node %q: columns: %w", spec.Name, err)
	}
	if len(p.columns) == 0 {
		return nil, fmt.Errorf("node %q: database.insert_many needs a columns list", spec.Name)
	}
	for column, path := range configMap(spec.Config, "set") {
		safe, err := safeIdentifier(column)
		if err != nil {
			return nil, fmt.Errorf("node %q: set: %w", spec.Name, err)
		}
		p.set[safe] = Stringify(path)
		p.setColumns = append(p.setColumns, safe)
	}
	sort.Strings(p.setColumns)
	if raw := configString(spec.Config, "tenant_column", ""); raw != "" {
		if p.tenant, err = safeIdentifier(raw); err != nil {
			return nil, fmt.Errorf("node %q: tenant_column: %w", spec.Name, err)
		}
	}
	if p.maxRows, err = configInt(spec.Config, "max_rows", 10000); err != nil {
		return nil, fmt.Errorf("node %q: %w", spec.Name, err)
	}
	if block := configMap(spec.Config, "parent"); block != nil {
		if err := rejectUnknownConfig("database.insert_many parent", block, "table", "values", "key", "link", "returning"); err != nil {
			return nil, fmt.Errorf("node %q: %w", spec.Name, err)
		}
		parent := &parentInsert{}
		if parent.table, err = safeIdentifier(configString(block, "table", "")); err != nil {
			return nil, fmt.Errorf("node %q: parent.table: %w", spec.Name, err)
		}
		if parent.key, err = safeIdentifier(configString(block, "key", "id")); err != nil {
			return nil, fmt.Errorf("node %q: parent.key: %w", spec.Name, err)
		}
		if parent.link, err = safeIdentifier(configString(block, "link", "")); err != nil {
			return nil, fmt.Errorf("node %q: parent.link names the child column that receives the parent key: %w", spec.Name, err)
		}
		values := configMap(block, "values")
		for column := range values {
			if _, err := safeIdentifier(column); err != nil {
				return nil, fmt.Errorf("node %q: parent.values: %w", spec.Name, err)
			}
			parent.columns = append(parent.columns, column)
		}
		sort.Strings(parent.columns)
		for _, column := range parent.columns {
			parent.paths = append(parent.paths, Stringify(values[column]))
		}
		if p.tenant != "" {
			parent.columns = append(parent.columns, p.tenant)
		}
		if len(parent.columns) == 0 {
			return nil, fmt.Errorf("node %q: parent.values is empty", spec.Name)
		}
		if parent.returning, err = safeIdentifiers(configStrings(block, "returning")); err != nil {
			return nil, fmt.Errorf("node %q: parent.returning: %w", spec.Name, err)
		}
		if !slices.Contains(parent.returning, parent.key) {
			parent.returning = append([]string{parent.key}, parent.returning...)
		}
		p.parent = parent
	}
	seen := map[string]bool{}
	for _, c := range p.allColumns() {
		if seen[c] {
			return nil, fmt.Errorf("node %q: column %q appears more than once across columns, set and tenant_column", spec.Name, c)
		}
		seen[c] = true
	}
	return ActionFunc(p.run(spec)), nil
}

func (p *insertManyPlan) allColumns() []string {
	all := append(append([]string{}, p.columns...), p.setColumns...)
	if p.tenant != "" {
		all = append(all, p.tenant)
	}
	if p.parent != nil {
		all = append(all, p.parent.link)
	}
	return all
}

// paramLimit is a conservative bind-parameter budget per statement.
func paramLimit(dialect string) int {
	switch dialect {
	case "sqlite":
		return 900 // SQLite builds before 3.32 cap at 999
	default:
		return 30000 // PostgreSQL and MySQL allow 65535
	}
}

func (p *insertManyPlan) run(spec NodeSpec) func(*ActionContext) (ActionResult, error) {
	return func(ctx *ActionContext) (ActionResult, error) {
		raw, _ := resolvePath(ctx.Inputs, p.rowsPath)
		var rows []map[string]any
		switch v := raw.(type) {
		case nil:
		case []map[string]any:
			rows = v
		case []any:
			for i, item := range v {
				m, ok := item.(map[string]any)
				if !ok {
					return ActionResult{}, invalidInput("row %d must be an object", i+1)
				}
				rows = append(rows, m)
			}
		default:
			return ActionResult{}, invalidInput("%s must be a list of rows", p.rowsPath)
		}
		if len(rows) == 0 {
			if !p.allowEmpty {
				return ActionResult{}, invalidInput("at least one row is required")
			}
			if p.parent == nil {
				return acknowledgement(spec, map[string]any{"inserted": 0}), nil
			}
		}
		if len(rows) > p.maxRows {
			return ActionResult{}, invalidInput("%d rows exceeds the limit of %d", len(rows), p.maxRows)
		}
		if p.tenant != "" && ctx.TenantID == "" {
			return ActionResult{}, permissionDenied("this table is tenant-scoped but no tenant could be determined for the request")
		}
		shared := make([]any, 0, len(p.setColumns)+1)
		for _, column := range p.setColumns {
			value, _ := resolvePath(ctx.Inputs, p.set[column])
			shared = append(shared, normalizeSQLArg(value))
		}
		if p.tenant != "" {
			shared = append(shared, ctx.TenantID)
		}

		columns := p.allColumns()
		perRow := len(columns)
		chunk := max(1, paramLimit(p.db.Dialect)/perRow)
		prefix := fmt.Sprintf("INSERT INTO %s (%s) VALUES ", p.table, strings.Join(columns, ", "))

		tx, err := p.db.BeginTx(ctx.Context, nil)
		if err != nil {
			return ActionResult{}, databaseFailure(err)
		}
		defer func() { _ = tx.Rollback() }()
		var parentRow map[string]any
		if p.parent != nil {
			if parentRow, err = p.insertParent(ctx, tx); err != nil {
				return ActionResult{}, err
			}
			shared = append(shared, normalizeSQLArg(parentRow[p.parent.key]))
		}
		for start := 0; start < len(rows); start += chunk {
			batch := rows[start:min(start+chunk, len(rows))]
			var sb strings.Builder
			sb.WriteString(prefix)
			args := make([]any, 0, len(batch)*perRow)
			for i, row := range batch {
				if i > 0 {
					sb.WriteString(", ")
				}
				sb.WriteByte('(')
				for j, column := range p.columns {
					if j > 0 {
						sb.WriteString(", ")
					}
					args = append(args, normalizeSQLArg(row[column]))
					fmt.Fprintf(&sb, "$%d", len(args))
				}
				for _, value := range shared {
					args = append(args, value)
					fmt.Fprintf(&sb, ", $%d", len(args))
				}
				sb.WriteByte(')')
			}
			if _, err := tx.ExecContext(ctx.Context, rebind(p.db.Dialect, sb.String()), args...); err != nil {
				return ActionResult{}, databaseFailure(err)
			}
		}
		if err := tx.Commit(); err != nil {
			return ActionResult{}, databaseFailure(err)
		}
		out := map[string]any{"inserted": len(rows)}
		if parentRow != nil {
			out["parent"] = parentRow
		}
		return acknowledgement(spec, out), nil
	}
}

// insertParent writes the header row and returns its key (and any requested
// columns). Dialects with RETURNING read them back in the same statement;
// MySQL uses LastInsertId and re-reads the row inside the transaction.
func (p *insertManyPlan) insertParent(ctx *ActionContext, tx *sql.Tx) (map[string]any, error) {
	parent := p.parent
	args := make([]any, 0, len(parent.columns))
	placeholders := make([]string, 0, len(parent.columns))
	for _, path := range parent.paths {
		value, _ := resolvePath(ctx.Inputs, path)
		args = append(args, normalizeSQLArg(value))
		placeholders = append(placeholders, fmt.Sprintf("$%d", len(args)))
	}
	if p.tenant != "" {
		args = append(args, ctx.TenantID)
		placeholders = append(placeholders, fmt.Sprintf("$%d", len(args)))
	}
	statement := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", parent.table,
		strings.Join(parent.columns, ", "), strings.Join(placeholders, ", "))
	if p.db.Dialect != "mysql" {
		rows, err := txQueryRows(ctx.Context, tx, rebind(p.db.Dialect, statement+" RETURNING "+strings.Join(parent.returning, ", ")), args)
		if err != nil {
			return nil, databaseFailure(err)
		}
		if len(rows) == 0 {
			return nil, databaseFailure(fmt.Errorf("parent insert returned no row"))
		}
		return rows[0], nil
	}
	result, err := tx.ExecContext(ctx.Context, rebind(p.db.Dialect, statement), args...)
	if err != nil {
		return nil, databaseFailure(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return nil, databaseFailure(err)
	}
	rows, err := txQueryRows(ctx.Context, tx, rebind(p.db.Dialect, fmt.Sprintf("SELECT %s FROM %s WHERE %s = $1",
		strings.Join(parent.returning, ", "), parent.table, parent.key)), []any{id})
	if err != nil || len(rows) == 0 {
		return map[string]any{parent.key: id}, nil
	}
	return rows[0], nil
}
