package platform

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/oarkflow/ref/intent"
	"github.com/oarkflow/ref/invocation"
)

// Entities are declarative data resources: one `entity` block becomes a
// table (migrated at startup) and a validated REST API — list with filters,
// search, sorting and totals; get; create; update (optimistically versioned);
// delete (soft or hard); CSV export; and aggregates — with per-operation
// access rules, tenant/owner/org scoping and post-commit hooks.
//
// They are expanded into ordinary intents and routes when the document
// loads, so they compose with everything else: an intent can call
// entity.<name>.create, a route can be overridden, a process step can read a
// record.
//
//	entity "project" {
//	  database "db"
//	  path "/api/projects"
//	  auth "jwt"
//	  tenant_scoped true
//	  soft_delete true
//	  versioned true
//	  search ["name", "code"]
//	  column "name" { kind text  required true  max_length 200 }
//	  column "status" { kind text  options ["draft", "active", "closed"]  default "draft" }
//	  column "budget" { kind number  min 0 }
//	  allow "*" { roles ["staff"] }
//	  allow "delete" { roles ["admin"] }
//	  on "created" { hook "project.notify" }
//	}

// EntitySpec declares one entity.
type EntitySpec struct {
	Name        string `bcl:",id"`
	Title       string `bcl:"title"`
	Description string `bcl:"description"`
	// Database names the database.sql resource; Table defaults to the name.
	Database string `bcl:"database"`
	Table    string `bcl:"table"`
	// Path is the collection URL (default /api/<name>s). Auth names the
	// authenticator for its routes; AllowAnonymous makes auth optional.
	Path           string `bcl:"path"`
	Auth           string `bcl:"auth"`
	AllowAnonymous bool   `bcl:"allow_anonymous"`

	Columns []EntityColumn `bcl:"column,block"`

	// TenantScoped stamps and filters tenant_id; OwnerScoped limits every
	// operation to rows the caller created unless they hold OwnerBypassRoles.
	TenantScoped     bool     `bcl:"tenant_scoped"`
	OwnerScoped      bool     `bcl:"owner_scoped"`
	OwnerBypassRoles []string `bcl:"owner_bypass_roles"`
	// OrgResource (org.hierarchy) + OrgColumn scope rows to the caller's
	// organisational units.
	OrgResource string `bcl:"org_resource"`
	OrgColumn   string `bcl:"org_column"`

	SoftDelete bool `bcl:"soft_delete"`
	// Versioned adds a version column; updates must send the version they
	// read and get 409 when it moved.
	Versioned bool `bcl:"versioned"`
	// Search lists text columns ?q= matches (case-insensitive substring).
	Search []string `bcl:"search"`
	// SearchIndex keeps a token index of the Search columns (lower-cased,
	// accent-folded words in <table>_search), written in each change's
	// transaction; ?q= then matches every query word as a word prefix.
	SearchIndex bool   `bcl:"search_index"`
	DefaultSort string `bcl:"default_sort"` // e.g. "-created_at"
	Limit       int    `bcl:"limit"`
	MaxLimit    int    `bcl:"max_limit"`
	Export      bool   `bcl:"export"`
	Aggregate   bool   `bcl:"aggregate"`
	// Analytics enables GET {path}/-/analytics: several metrics, up to two
	// group-by columns and a day/week/month time bucket in one call.
	Analytics bool `bcl:"analytics"`
	// Bulk enables POST {path}/-/bulk: many creates, updates and deletes in
	// one request, all-or-nothing by default; BulkMax caps its items
	// (default 500).
	Bulk    bool `bcl:"bulk"`
	BulkMax int  `bcl:"bulk_max"`
	// Migrate creates the table and indexes at startup (default true).
	Migrate *bool `bcl:"migrate"`

	Allow []EntityAccess `bcl:"allow,block"`
	On    []EntityHook   `bcl:"on,block"`
}

// EntityColumn is one field of an entity.
type EntityColumn struct {
	Name  string `bcl:",id"`
	Label string `bcl:"label"`
	// Kind: text (default), integer, number, decimal, boolean, date,
	// datetime, email, json. A decimal (money) is exact: stored as integer
	// minor units with Scale decimals (default 2) and returned as a string
	// such as "1250.50".
	Kind      string   `bcl:"kind,ident"`
	Scale     int      `bcl:"scale"`
	Required  bool     `bcl:"required"`
	Unique    bool     `bcl:"unique"`
	Index     bool     `bcl:"index"`
	MinLength int      `bcl:"min_length"`
	MaxLength int      `bcl:"max_length"`
	Min       *float64 `bcl:"min"`
	Max       *float64 `bcl:"max"`
	Pattern   string   `bcl:"pattern"`
	Options   []string `bcl:"options"`
	Default   any      `bcl:"default"`
	// ReadOnly columns are set by hooks or the server, never by clients;
	// Immutable ones can be set on create only; Hidden ones are never
	// returned.
	ReadOnly  bool `bcl:"read_only"`
	Immutable bool `bcl:"immutable"`
	Hidden    bool `bcl:"hidden"`
}

// EntityAccess allows an operation: list, get, create, update, delete,
// export, aggregate, analytics or "*" (the default for ops without their own blocks —
// an op's own blocks replace it). Roles restrict it to role holders; Condition is
// checked against the record (and principal) for get/update/delete and
// against the submitted record for create.
type EntityAccess struct {
	Op        string   `bcl:",id"`
	Roles     []string `bcl:"roles"`
	Condition string   `bcl:"condition"`
}

// EntityHook runs an intent after a committed change: created, updated,
// deleted or "*".
//
// A durable hook is recorded in the same transaction as the change and
// delivered by a background dispatcher with retries (MaxAttempts, default
// 10; RetryBase doubling, default 2s) and dead-lettering, so it survives a
// crash and a failing downstream. Delivery is at-least-once: its input
// carries a stable event_id for idempotency.
type EntityHook struct {
	Event       string `bcl:",id"`
	Hook        string `bcl:"hook"`
	Durable     bool   `bcl:"durable"`
	MaxAttempts int    `bcl:"max_attempts"`
	RetryBase   string `bcl:"retry_base"`

	retryBase time.Duration
}

// entityOps are the operations allow blocks govern. A bulk request is not
// one of them: each of its items is checked as the create, update or delete
// it is.
var entityOps = []string{"list", "get", "create", "update", "delete", "export", "aggregate", "analytics"}

// entityPlan is a compiled entity.
type entityPlan struct {
	spec      EntitySpec
	table     string
	dialect   string
	columns   map[string]*EntityColumn
	order     []string // declared columns in order
	system    []string // server-managed columns
	patterns  map[string]*regexp.Regexp
	access    map[string][]compiledAccess
	anyAccess bool
}

type compiledAccess struct {
	roles []string
	cond  *Expression
}

var identRe = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

func compileEntity(spec EntitySpec, dialect string) (*entityPlan, error) {
	where := fmt.Sprintf("entity %q", spec.Name)
	if !identRe.MatchString(spec.Name) {
		return nil, fmt.Errorf("%s: the name must be lower_snake_case", where)
	}
	p := &entityPlan{spec: spec, table: orDefault(spec.Table, spec.Name), dialect: dialect,
		columns: map[string]*EntityColumn{}, patterns: map[string]*regexp.Regexp{}, access: map[string][]compiledAccess{}}
	if !identRe.MatchString(p.table) {
		return nil, fmt.Errorf("%s: invalid table %q", where, p.table)
	}
	p.system = []string{"id", "created_by", "created_at", "updated_at"}
	if spec.TenantScoped {
		p.system = append(p.system, "tenant_id")
	}
	if spec.SoftDelete {
		p.system = append(p.system, "deleted_at")
	}
	if spec.Versioned {
		p.system = append(p.system, "version")
	}
	if len(spec.Columns) == 0 {
		return nil, fmt.Errorf("%s: declare at least one column", where)
	}
	for i := range spec.Columns {
		col := &spec.Columns[i]
		if !identRe.MatchString(col.Name) || slices.Contains(p.system, col.Name) || p.columns[col.Name] != nil {
			return nil, fmt.Errorf("%s: column %q: invalid, reserved or duplicate name", where, col.Name)
		}
		if col.Kind == "" {
			col.Kind = "text"
		}
		if !slices.Contains([]string{"text", "integer", "number", "decimal", "boolean", "date", "datetime", "email", "json"}, col.Kind) {
			return nil, fmt.Errorf("%s: column %q: kind %q is not text, integer, number, decimal, boolean, date, datetime, email or json", where, col.Name, col.Kind)
		}
		if col.Kind == "decimal" {
			if col.Scale == 0 {
				col.Scale = 2
			}
			if col.Scale < 0 || col.Scale > 8 {
				return nil, fmt.Errorf("%s: column %q: scale must be 0-8", where, col.Name)
			}
		}
		if col.Pattern != "" {
			re, err := regexp.Compile(col.Pattern)
			if err != nil {
				return nil, fmt.Errorf("%s: column %q: pattern: %w", where, col.Name, err)
			}
			p.patterns[col.Name] = re
		}
		p.columns[col.Name] = col
		p.order = append(p.order, col.Name)
	}
	for _, s := range spec.Search {
		if c := p.columns[s]; c == nil || (c.Kind != "text" && c.Kind != "email") {
			return nil, fmt.Errorf("%s: search column %q must be a text column", where, s)
		}
	}
	if spec.SearchIndex && len(spec.Search) == 0 {
		return nil, fmt.Errorf("%s: search_index needs search columns", where)
	}
	if spec.OrgResource != "" && p.columns[spec.OrgColumn] == nil {
		return nil, fmt.Errorf("%s: org_column %q must be a declared column", where, spec.OrgColumn)
	}
	if spec.DefaultSort != "" && !p.sortable(strings.TrimPrefix(spec.DefaultSort, "-")) {
		return nil, fmt.Errorf("%s: default_sort %q is not a column", where, spec.DefaultSort)
	}
	for _, a := range spec.Allow {
		if a.Op != "*" && !slices.Contains(entityOps, a.Op) {
			return nil, fmt.Errorf("%s: allow %q: op must be one of %s or *", where, a.Op, strings.Join(entityOps, ", "))
		}
		expr, err := CompileExpr(a.Condition)
		if err != nil {
			return nil, fmt.Errorf("%s: allow %q condition: %w", where, a.Op, err)
		}
		p.access[a.Op] = append(p.access[a.Op], compiledAccess{roles: a.Roles, cond: expr})
		p.anyAccess = true
	}
	p.spec.On = slices.Clone(spec.On)
	for i := range p.spec.On {
		h := &p.spec.On[i]
		if !slices.Contains([]string{"created", "updated", "deleted", "*"}, h.Event) || h.Hook == "" {
			return nil, fmt.Errorf("%s: on %q: event must be created, updated, deleted or * and needs a hook", where, h.Event)
		}
		if !h.Durable && (h.MaxAttempts != 0 || h.RetryBase != "") {
			return nil, fmt.Errorf("%s: on %q: max_attempts and retry_base apply to durable hooks only", where, h.Event)
		}
		if h.MaxAttempts == 0 {
			h.MaxAttempts = 10
		}
		if h.MaxAttempts < 1 {
			return nil, fmt.Errorf("%s: on %q: max_attempts must be at least 1", where, h.Event)
		}
		h.retryBase = 2 * time.Second
		if h.RetryBase != "" {
			d, err := time.ParseDuration(h.RetryBase)
			if err != nil || d <= 0 {
				return nil, fmt.Errorf("%s: on %q: retry_base %q is not a positive duration", where, h.Event, h.RetryBase)
			}
			h.retryBase = d
		}
	}
	if p.spec.Limit <= 0 {
		p.spec.Limit = 50
	}
	if p.spec.MaxLimit <= 0 {
		p.spec.MaxLimit = 500
	}
	if p.spec.BulkMax < 0 || (p.spec.BulkMax > 0 && !p.spec.Bulk) {
		return nil, fmt.Errorf("%s: bulk_max must be positive and needs bulk true", where)
	}
	if p.spec.BulkMax == 0 {
		p.spec.BulkMax = 500
	}
	return p, nil
}

func (p *entityPlan) sortable(col string) bool {
	return p.columns[col] != nil || slices.Contains(p.system, col)
}

func (p *entityPlan) path() string {
	if p.spec.Path != "" {
		return strings.TrimRight(p.spec.Path, "/")
	}
	return "/api/" + p.spec.Name + "s"
}

// ddl returns the migration statements for the entity's table.
func (p *entityPlan) ddl() []string {
	text, key, num, boolean := "TEXT", "TEXT", "DOUBLE PRECISION", "BOOLEAN"
	switch p.dialect {
	case "mysql":
		key, num = "VARCHAR(191)", "DOUBLE"
	case "sqlite":
		num, boolean = "REAL", "INTEGER"
	}
	colType := func(c *EntityColumn) string {
		switch c.Kind {
		case "integer", "decimal":
			return "BIGINT"
		case "number":
			return num
		case "boolean":
			return boolean
		}
		if c.Unique || c.Index || p.spec.OrgColumn == c.Name {
			return key
		}
		return text
	}
	defs := []string{"id " + key + " PRIMARY KEY", "created_by " + key + " NOT NULL DEFAULT ''",
		"created_at " + key + " NOT NULL", "updated_at " + key + " NOT NULL"}
	if p.spec.TenantScoped {
		defs = append(defs, "tenant_id "+key+" NOT NULL DEFAULT ''")
	}
	if p.spec.SoftDelete {
		defs = append(defs, "deleted_at "+key)
	}
	if p.spec.Versioned {
		defs = append(defs, "version BIGINT NOT NULL DEFAULT 1")
	}
	for _, name := range p.order {
		defs = append(defs, name+" "+colType(p.columns[name]))
	}
	stmts := []string{fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s (%s)", p.table, strings.Join(defs, ", "))}
	ifNotExists := "IF NOT EXISTS "
	if p.dialect == "mysql" {
		ifNotExists = ""
	}
	index := func(unique bool, cols ...string) {
		kind := "INDEX"
		if unique {
			kind = "UNIQUE INDEX"
		}
		name := p.table + "_" + strings.Join(cols, "_") + "_idx"
		stmts = append(stmts, fmt.Sprintf("CREATE %s %s%s ON %s (%s)", kind, ifNotExists, name, p.table, strings.Join(cols, ", ")))
	}
	for _, name := range p.order {
		c := p.columns[name]
		switch {
		case c.Unique && p.spec.TenantScoped:
			index(true, "tenant_id", name)
		case c.Unique:
			index(true, name)
		case c.Index || name == p.spec.OrgColumn:
			index(false, name)
		}
	}
	if p.spec.TenantScoped {
		index(false, "tenant_id", "created_at")
	}
	if p.spec.SearchIndex {
		stmts = append(stmts, p.searchDDL()...)
	}
	return stmts
}

// ---------------------------------------------------------------------------
// Document expansion
// ---------------------------------------------------------------------------

// expandEntities turns entity blocks into migrations on their database
// resource plus intents and routes. It runs before the document is
// validated, so what it generates is checked like hand-written blocks.
func expandEntities(doc Document) (Document, error) {
	if len(doc.Entities) == 0 {
		return doc, nil
	}
	resources := slices.Clone(doc.Resources)
	byName := map[string]int{}
	for i, r := range resources {
		byName[r.Name] = i
	}
	intents := map[string]bool{}
	for _, it := range doc.Intents {
		intents[it.Name] = true
	}
	routes := map[string]bool{}
	for _, r := range doc.Routes {
		routes[r.Name] = true
	}
	seen, outboxes := map[string]bool{}, map[string]bool{}
	for _, spec := range doc.Entities {
		if seen[spec.Name] {
			return doc, fmt.Errorf("ref/platform: entity %q declared twice", spec.Name)
		}
		seen[spec.Name] = true
		i, ok := byName[spec.Database]
		if !ok || resources[i].Kind != "database.sql" {
			return doc, fmt.Errorf("ref/platform: entity %q: database %q is not a database.sql resource", spec.Name, spec.Database)
		}
		dialect := dialectOf(configString(resources[i].Config, "driver", ""))
		plan, err := compileEntity(spec, dialect)
		if err != nil {
			return doc, fmt.Errorf("ref/platform: %w", err)
		}
		if spec.Migrate == nil || *spec.Migrate {
			config := maps.Clone(resources[i].Config)
			if config == nil {
				config = map[string]any{}
			}
			var migrations []any
			for _, m := range configStrings(config, "migrations") {
				migrations = append(migrations, m)
			}
			for _, m := range plan.ddl() {
				migrations = append(migrations, m)
			}
			if slices.ContainsFunc(spec.On, func(h EntityHook) bool { return h.Durable }) && !outboxes[spec.Database] {
				outboxes[spec.Database] = true
				for _, m := range entityEventsDDL(dialect) {
					migrations = append(migrations, m)
				}
			}
			config["migrations"] = migrations
			resources[i].Config = config
		}
		base := plan.path()
		type opRoute struct {
			op, method, path string
			status           int
			body             bool
		}
		ops := []opRoute{
			{"list", "GET", base, 200, false},
			{"create", "POST", base, 201, true},
			{"get", "GET", base + "/:id", 200, false},
			{"update", "PATCH", base + "/:id", 200, true},
			{"update", "PUT", base + "/:id", 200, true},
			{"delete", "DELETE", base + "/:id", 200, false},
		}
		if spec.Export {
			ops = append(ops, opRoute{"export", "GET", base + "/-/export", 200, false})
		}
		if spec.Aggregate {
			ops = append(ops, opRoute{"aggregate", "GET", base + "/-/aggregate", 200, false})
		}
		if spec.Analytics {
			ops = append(ops, opRoute{"analytics", "GET", base + "/-/analytics", 200, false})
		}
		if spec.Bulk {
			ops = append(ops, opRoute{"bulk", "POST", base + "/-/bulk", 200, true})
		}

		for _, o := range ops {
			intent := "entity." + spec.Name + "." + o.op
			if !intents[intent] {
				intents[intent] = true
				node := NodeSpec{Name: "result", Uses: "entity.op", Resource: spec.Database, Provides: []string{"result"},
					Config: map[string]any{"entity": spec.Name, "op": o.op}}
				if !slices.Contains([]string{"list", "get", "export", "aggregate", "analytics"}, o.op) {
					node.Kind = "effect"
				}
				if o.body {
					node.Requires = []string{"input"}
				}
				doc.Intents = append(doc.Intents, IntentSpec{Name: intent, Description: fmt.Sprintf("%s %s", o.op, spec.Name),
					Response: "result", Nodes: []NodeSpec{node}})
			}
			name := "entity." + spec.Name + "." + strings.ToLower(o.method) + "." + o.op
			if routes[name] {
				continue
			}
			routes[name] = true
			doc.Routes = append(doc.Routes, RouteSpec{Name: name, Method: o.method, Path: o.path, Intent: intent,
				Auth: spec.Auth, AllowAnonymous: spec.AllowAnonymous, Status: o.status, Tags: []string{spec.Name}})
		}
	}
	doc.Resources = resources
	return doc, nil
}

// ---------------------------------------------------------------------------
// The entity.op action
// ---------------------------------------------------------------------------

func registerEntityActions(r *Registry) {
	mustAction(r, "entity.op", ActionFactoryFunc(buildEntityOp), ActionInfo{
		Family:  "data",
		Summary: "Run an operation of a declared entity: list, get, create, update, delete, export, aggregate, analytics or bulk",
		Config: []ConfigField{
			{Name: "entity", Type: "string", Required: true},
			{Name: "op", Type: "string", Required: true},
		},
	})
}

func buildEntityOp(build BuildContext, spec NodeSpec) (Action, error) {
	db, err := requireResource[*Database](build, spec, "a database.sql resource")
	if err != nil {
		return nil, err
	}
	if err := rejectUnknownConfig("entity.op", spec.Config, "entity", "op"); err != nil {
		return nil, fmt.Errorf("node %q: %w", spec.Name, err)
	}
	name, op := configString(spec.Config, "entity", ""), configString(spec.Config, "op", "")
	if build.Document == nil {
		return nil, fmt.Errorf("node %q: entity.op needs the document", spec.Name)
	}
	var es *EntitySpec
	for i := range build.Document.Entities {
		if build.Document.Entities[i].Name == name {
			es = &build.Document.Entities[i]
		}
	}
	if es == nil {
		return nil, fmt.Errorf("node %q: unknown entity %q", spec.Name, name)
	}
	if !slices.Contains(entityOps, op) && op != "bulk" {
		return nil, fmt.Errorf("node %q: unknown entity op %q", spec.Name, op)
	}
	plan, err := compileEntity(*es, db.Dialect)
	if err != nil {
		return nil, err
	}
	rt := &entityRuntime{plan: plan, db: db, op: op, spec: spec}
	if slices.ContainsFunc(plan.spec.On, func(h EntityHook) bool { return h.Durable }) {
		db.enableEntityEvents() // before the background loops start
	}
	if plan.spec.SearchIndex {
		db.registerSearchIndex(plan)
	}
	if es.OrgResource != "" {
		res, ok := build.Resource(es.OrgResource)
		if !ok {
			return nil, fmt.Errorf("node %q: entity %q org_resource %q is unknown", spec.Name, name, es.OrgResource)
		}
		if rt.org, ok = res.(*OrgHierarchy); !ok {
			return nil, fmt.Errorf("node %q: entity %q org_resource %q is not an org.hierarchy resource", spec.Name, name, es.OrgResource)
		}
	}
	return ActionFunc(rt.run), nil
}

type entityRuntime struct {
	plan *entityPlan
	db   *Database
	org  *OrgHierarchy
	op   string
	spec NodeSpec
}

func (rt *entityRuntime) run(ctx *ActionContext) (ActionResult, error) {
	if rt.op != "bulk" { // checked per item
		if err := rt.allowed(ctx, rt.op, nil); err != nil {
			return ActionResult{}, err
		}
	}
	var (
		out any
		err error
	)
	switch rt.op {
	case "list":
		out, err = rt.list(ctx)
	case "get":
		out, err = rt.getOne(ctx, rt.db.Reader(), rt.id(ctx), true)
	case "create":
		out, err = rt.create(ctx)
	case "update":
		out, err = rt.update(ctx)
	case "delete":
		out, err = rt.delete(ctx)
	case "export":
		out, err = rt.export(ctx)
	case "aggregate":
		out, err = rt.aggregate(ctx)
	case "analytics":
		out, err = rt.analytics(ctx)
	case "bulk":
		out, err = rt.bulk(ctx)
	}
	if err != nil {
		return ActionResult{}, err
	}
	return singleOutput(rt.spec, out), nil
}

// allowed applies the entity's allow blocks. With none declared every
// operation is allowed (route auth still applies); with some, an operation
// needs a matching block — fail closed.
func (rt *entityRuntime) allowed(ctx *ActionContext, op string, record map[string]any) error {
	p := rt.plan
	if !p.anyAccess {
		return nil
	}
	rules := p.rules(op)
	if len(rules) == 0 {
		return permissionDenied(fmt.Sprintf("%s is not allowed on %s", op, p.spec.Name))
	}
	for _, r := range rules {
		if len(r.roles) > 0 && !slices.ContainsFunc(r.roles, ctx.Principal.HasRole) {
			continue
		}
		if r.cond != nil {
			if record == nil {
				// Row conditions are checked once the record is known.
				if op == "get" || op == "update" || op == "delete" || op == "create" {
					return nil
				}
				continue
			}
			env := actionEnv(ctx)
			env["record"] = record
			ok, err := r.cond.Bool(env)
			if err != nil || !ok {
				continue
			}
		}
		return nil
	}
	return permissionDenied(fmt.Sprintf("you may not %s this %s", op, p.spec.Name))
}

// rules are the allow blocks for op: its own blocks replace the "*" ones.
func (p *entityPlan) rules(op string) []compiledAccess {
	if len(p.access[op]) > 0 {
		return p.access[op]
	}
	return p.access["*"]
}

func (rt *entityRuntime) hasRowConditions(op string) bool {
	for _, r := range rt.plan.rules(op) {
		if r.cond != nil {
			return true
		}
	}
	return false
}

func (rt *entityRuntime) id(ctx *ActionContext) string {
	if v, ok := requestValue(ctx, "path", "id"); ok && v != "" {
		return v
	}
	if v, ok := resolvePath(ctx.Inputs, "input.id"); ok {
		return Stringify(v)
	}
	return ""
}

// scope returns the WHERE conditions every operation shares.
func (rt *entityRuntime) scope(ctx *ActionContext, args []any) ([]string, []any, error) {
	p := rt.plan
	var conds []string
	if p.spec.TenantScoped {
		if ctx.TenantID == "" {
			return nil, nil, permissionDenied("this data is tenant-scoped but no tenant could be determined for the request")
		}
		args = append(args, ctx.TenantID)
		conds = append(conds, fmt.Sprintf("tenant_id = $%d", len(args)))
	}
	if p.spec.OwnerScoped && !slices.ContainsFunc(p.spec.OwnerBypassRoles, ctx.Principal.HasRole) {
		if ctx.Principal.ID == "" {
			return nil, nil, permissionDenied("sign in to see your records")
		}
		args = append(args, ctx.Principal.ID)
		conds = append(conds, fmt.Sprintf("created_by = $%d", len(args)))
	}
	if p.spec.SoftDelete {
		conds = append(conds, "deleted_at IS NULL")
	}
	if rt.org != nil {
		snap := rt.org.Snapshot(ctx.TenantID)
		scope := rt.org.ScopeFor(ctx.Principal, snap.Tree)
		if !scope.Global {
			if len(scope.Assigned) == 0 {
				return nil, nil, permissionDenied("you are not assigned to any organisational unit")
			}
			ids := snap.Tree.ScopeIDs(scope.Assigned)
			if len(ids) > maxOrgScopeIDs {
				return nil, nil, unavailable("organisational scope covers %d units", len(ids))
			}
			ph := make([]string, len(ids))
			for i, id := range ids {
				args = append(args, id)
				ph[i] = fmt.Sprintf("$%d", len(args))
			}
			conds = append(conds, fmt.Sprintf("%s IN (%s)", p.spec.OrgColumn, strings.Join(ph, ", ")))
		}
	}
	return conds, args, nil
}

func (rt *entityRuntime) selectList() string {
	cols := []string{"id", "created_by", "created_at", "updated_at"}
	if rt.plan.spec.Versioned {
		cols = append(cols, "version")
	}
	for _, name := range rt.plan.order {
		if !rt.plan.columns[name].Hidden {
			cols = append(cols, name)
		}
	}
	return strings.Join(cols, ", ")
}

func queryParams(ctx *ActionContext) map[string][]string {
	if ctx.Invocation == nil {
		return nil
	}
	if meta, ok := ctx.Invocation.Metadata.(invocation.HTTPMeta); ok {
		return meta.Query
	}
	return nil
}

var filterOps = map[string]string{"": "=", "ne": "<>", "gt": ">", "gte": ">=", "lt": "<", "lte": "<="}

// filters turns query parameters into conditions: col=v, col__ne, __gt,
// __gte, __lt, __lte, __in=a,b, __like (substring), __null=true|false, and
// q= across the search columns. Unknown parameters are an error, so a typo
// never silently returns everything.
func (rt *entityRuntime) filters(ctx *ActionContext, args []any, reserved ...string) ([]string, []any, error) {
	p := rt.plan
	var conds []string
	params := queryParams(ctx)
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, key := range keys {
		values := params[key]
		if len(values) == 0 || slices.Contains(reserved, key) {
			continue
		}
		raw := values[0]
		if key == "q" {
			if len(p.spec.Search) == 0 || strings.TrimSpace(raw) == "" {
				continue
			}
			if p.spec.SearchIndex {
				var more []string
				more, args = p.searchCondition(raw, args)
				conds = append(conds, more...)
				continue
			}
			var ors []string
			for _, col := range p.spec.Search {
				// A placeholder each: MySQL's are positional.
				args = append(args, "%"+strings.ToLower(strings.TrimSpace(raw))+"%")
				ors = append(ors, fmt.Sprintf("LOWER(%s) LIKE $%d", col, len(args)))
			}
			conds = append(conds, "("+strings.Join(ors, " OR ")+")")
			continue
		}
		col, op, _ := strings.Cut(key, "__")
		c := p.columns[col]
		if (c == nil || c.Hidden || c.Kind == "json") && !slices.Contains([]string{"created_by", "created_at", "updated_at"}, col) {
			return nil, nil, invalidInput("unknown filter %q", key)
		}
		kind, scale := "text", 0
		if c != nil {
			kind, scale = c.Kind, c.Scale
		}
		switch op {
		case "in":
			parts := strings.Split(raw, ",")
			ph := make([]string, 0, len(parts))
			for _, part := range parts {
				v, err := filterValue(kind, scale, strings.TrimSpace(part))
				if err != nil {
					return nil, nil, invalidInput("filter %s: %v", key, err)
				}
				args = append(args, sqlValue(rt.db.Dialect, v))
				ph = append(ph, fmt.Sprintf("$%d", len(args)))
			}
			conds = append(conds, fmt.Sprintf("%s IN (%s)", col, strings.Join(ph, ", ")))
		case "like":
			args = append(args, "%"+strings.ToLower(raw)+"%")
			conds = append(conds, fmt.Sprintf("LOWER(%s) LIKE $%d", col, len(args)))
		case "null":
			if raw == "true" {
				conds = append(conds, col+" IS NULL")
			} else {
				conds = append(conds, col+" IS NOT NULL")
			}
		default:
			sqlOp, ok := filterOps[op]
			if !ok {
				return nil, nil, invalidInput("unknown filter operator %q", op)
			}
			v, err := filterValue(kind, scale, raw)
			if err != nil {
				return nil, nil, invalidInput("filter %s: %v", key, err)
			}
			args = append(args, sqlValue(rt.db.Dialect, v))
			conds = append(conds, fmt.Sprintf("%s %s $%d", col, sqlOp, len(args)))
		}
	}
	return conds, args, nil
}

func filterValue(kind string, scale int, raw string) (any, error) {
	switch kind {
	case "decimal":
		return parseDecimal(raw, scale)
	case "integer":
		return strconv.ParseInt(raw, 10, 64)
	case "number":
		return strconv.ParseFloat(raw, 64)
	case "boolean":
		return strconv.ParseBool(raw)
	}
	return raw, nil
}

func (rt *entityRuntime) orderBy(ctx *ActionContext) (string, error) {
	sort := rt.plan.spec.DefaultSort
	if params := queryParams(ctx); len(params["sort"]) > 0 {
		sort = params["sort"][0]
	}
	if sort == "" {
		sort = "-created_at"
	}
	var parts []string
	for _, s := range strings.Split(sort, ",") {
		s = strings.TrimSpace(s)
		desc := strings.HasPrefix(s, "-")
		col := strings.TrimPrefix(s, "-")
		if !rt.plan.sortable(col) || (rt.plan.columns[col] != nil && rt.plan.columns[col].Hidden) {
			return "", invalidInput("cannot sort by %q", col)
		}
		if desc {
			col += " DESC"
		}
		parts = append(parts, col)
	}
	return strings.Join(append(parts, "id"), ", "), nil
}

func intParam(ctx *ActionContext, name string, def int) int {
	if params := queryParams(ctx); len(params[name]) > 0 {
		if n, err := strconv.Atoi(params[name][0]); err == nil && n >= 0 {
			return n
		}
	}
	return def
}

func (rt *entityRuntime) where(ctx *ActionContext, reserved ...string) (string, []any, error) {
	conds, args, err := rt.scope(ctx, nil)
	if err != nil {
		return "", nil, err
	}
	more, args, err := rt.filters(ctx, args, reserved...)
	if err != nil {
		return "", nil, err
	}
	conds = append(conds, more...)
	if len(conds) == 0 {
		return "", args, nil
	}
	return " WHERE " + strings.Join(conds, " AND "), args, nil
}

func (rt *entityRuntime) list(ctx *ActionContext) (any, error) {
	where, args, err := rt.where(ctx, "limit", "offset", "sort")
	if err != nil {
		return nil, err
	}
	order, err := rt.orderBy(ctx)
	if err != nil {
		return nil, err
	}
	limit := min(max(intParam(ctx, "limit", rt.plan.spec.Limit), 1), rt.plan.spec.MaxLimit)
	offset := intParam(ctx, "offset", 0)
	countRows, err := queryRows(ctx.Context, rt.db.Reader(), rebind(rt.db.Dialect, "SELECT COUNT(*) AS n FROM "+rt.plan.table+where), args)
	if err != nil {
		return nil, databaseFailure(err)
	}
	total, _ := ToFloat(countRows[0]["n"])
	args = append(args, limit, offset)
	stmt := fmt.Sprintf("SELECT %s FROM %s%s ORDER BY %s LIMIT $%d OFFSET $%d", rt.selectList(), rt.plan.table, where, order, len(args)-1, len(args))
	rows, err := queryRows(ctx.Context, rt.db.Reader(), rebind(rt.db.Dialect, stmt), args)
	if err != nil {
		return nil, databaseFailure(err)
	}
	items := make([]any, len(rows))
	for i, row := range rows {
		items[i] = rt.decode(row)
	}
	return map[string]any{"items": items, "total": int(total), "limit": limit, "offset": offset}, nil
}

// load reads one row within scope through q (nil, nil when absent).
func (rt *entityRuntime) load(ctx *ActionContext, q execer, id string, columns string) (map[string]any, error) {
	if id == "" {
		return nil, invalidInput("a record id is required")
	}
	conds, args, err := rt.scope(ctx, []any{id})
	if err != nil {
		return nil, err
	}
	stmt := fmt.Sprintf("SELECT %s FROM %s WHERE %s", columns, rt.plan.table, strings.Join(append([]string{"id = $1"}, conds...), " AND "))
	rows, err := queryRows(ctx.Context, q, rebind(rt.db.Dialect, stmt), args)
	if err != nil {
		return nil, databaseFailure(err)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return rt.decode(rows[0]), nil
}

func (rt *entityRuntime) getOne(ctx *ActionContext, q execer, id string, check bool) (map[string]any, error) {
	rec, err := rt.load(ctx, q, id, rt.selectList())
	if err != nil {
		return nil, err
	}
	if rec == nil {
		return nil, notFound(rt.plan.spec.Name, id)
	}
	if check && rt.hasRowConditions("get") {
		if err := rt.allowed(ctx, "get", rec); err != nil {
			return nil, notFound(rt.plan.spec.Name, id) // do not disclose existence
		}
	}
	return rec, nil
}

// validate checks and coerces a submitted record. create requires the
// required columns; update validates only what was sent.
func (rt *entityRuntime) validate(body map[string]any, create bool) (map[string]any, error) {
	p := rt.plan
	clean := map[string]any{}
	var details []any
	fail := func(path, rule, msg string) {
		details = append(details, map[string]any{"path": path, "rule": rule, "message": msg})
	}
	for key, raw := range body {
		if key == "version" && p.spec.Versioned {
			continue
		}
		c := p.columns[key]
		switch {
		case c == nil:
			if slices.Contains(p.system, key) {
				fail(key, "read_only", key+" is managed by the server")
			} else {
				fail(key, "unknown", "unknown field "+key)
			}
			continue
		case c.ReadOnly:
			fail(key, "read_only", key+" cannot be set")
			continue
		case c.Immutable && !create:
			fail(key, "immutable", key+" cannot be changed after creation")
			continue
		}
		v, rule, msg := coerceEntityValue(c, raw, p.patterns[key])
		if msg != "" {
			fail(key, rule, msg)
			continue
		}
		clean[key] = v
	}
	if create {
		for _, name := range p.order {
			c := p.columns[name]
			if _, set := clean[name]; !set && c.Default != nil {
				v, _, msg := coerceEntityValue(c, c.Default, p.patterns[name])
				if msg == "" {
					clean[name] = v
				}
			}
			if v, set := clean[name]; c.Required && (!set || v == nil) {
				fail(name, "required", orDefault(c.Label, name)+" is required")
			}
		}
	} else {
		for key, v := range clean {
			if p.columns[key].Required && v == nil {
				fail(key, "required", orDefault(p.columns[key].Label, key)+" is required")
			}
		}
	}
	if len(details) > 0 {
		slices.SortFunc(details, func(a, b any) int {
			return strings.Compare(a.(map[string]any)["path"].(string), b.(map[string]any)["path"].(string))
		})
		first := details[0].(map[string]any)["message"].(string)
		return nil, entityInvalid(first, details)
	}
	return clean, nil
}

func entityInvalid(message string, details []any) error {
	return intent.Failure{Code: "VALIDATION_FAILED", Category: intent.CategoryInvalidInput, Message: message, Meta: map[string]any{"details": details}}
}

var entityEmailRe = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)

func coerceEntityValue(c *EntityColumn, raw any, re *regexp.Regexp) (any, string, string) {
	label := orDefault(c.Label, c.Name)
	if raw == nil {
		return nil, "", ""
	}
	switch c.Kind {
	case "decimal":
		minor, err := parseDecimal(raw, c.Scale)
		if err != nil {
			return nil, "type", label + " " + err.Error()
		}
		f := float64(minor) / math.Pow10(c.Scale)
		if c.Min != nil && f < *c.Min {
			return nil, "min", fmt.Sprintf("%s must be at least %v", label, *c.Min)
		}
		if c.Max != nil && f > *c.Max {
			return nil, "max", fmt.Sprintf("%s must be at most %v", label, *c.Max)
		}
		return minor, "", ""
	case "integer", "number":
		f, ok := ToFloat(raw)
		if s, isStr := raw.(string); isStr {
			var err error
			f, err = strconv.ParseFloat(strings.TrimSpace(s), 64)
			ok = err == nil
		}
		if !ok || math.IsNaN(f) || math.IsInf(f, 0) {
			return nil, "type", label + " must be a number"
		}
		if c.Kind == "integer" && f != math.Trunc(f) {
			return nil, "type", label + " must be a whole number"
		}
		if c.Min != nil && f < *c.Min {
			return nil, "min", fmt.Sprintf("%s must be at least %v", label, *c.Min)
		}
		if c.Max != nil && f > *c.Max {
			return nil, "max", fmt.Sprintf("%s must be at most %v", label, *c.Max)
		}
		if c.Kind == "integer" {
			return int64(f), "", ""
		}
		return f, "", ""
	case "boolean":
		switch v := raw.(type) {
		case bool:
			return v, "", ""
		case string:
			if b, err := strconv.ParseBool(v); err == nil {
				return b, "", ""
			}
		}
		return nil, "type", label + " must be true or false"
	case "json":
		encoded, err := json.Marshal(raw)
		if err != nil {
			return nil, "type", label + " must be JSON"
		}
		return string(encoded), "", ""
	}
	s, ok := raw.(string)
	if !ok {
		return nil, "type", label + " must be text"
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, "", ""
	}
	n := len([]rune(s))
	if c.MinLength > 0 && n < c.MinLength {
		return nil, "min_length", fmt.Sprintf("%s must be at least %d characters", label, c.MinLength)
	}
	if c.MaxLength > 0 && n > c.MaxLength {
		return nil, "max_length", fmt.Sprintf("%s must be at most %d characters", label, c.MaxLength)
	}
	if re != nil && !re.MatchString(s) {
		return nil, "pattern", label + " has an invalid format"
	}
	if len(c.Options) > 0 && !slices.Contains(c.Options, s) {
		return nil, "option", fmt.Sprintf("%q is not a valid %s", s, label)
	}
	switch c.Kind {
	case "email":
		if !entityEmailRe.MatchString(s) {
			return nil, "email", label + " must be an email address"
		}
	case "date":
		if _, err := time.Parse("2006-01-02", s); err != nil {
			return nil, "date", label + " must be a date (YYYY-MM-DD)"
		}
	case "datetime":
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			return nil, "datetime", label + " must be an RFC 3339 timestamp"
		}
		s = t.UTC().Format(time.RFC3339)
	}
	return s, "", ""
}

// decode converts a row to API values (booleans, JSON, numbers).
func (rt *entityRuntime) decode(row map[string]any) map[string]any {
	for name, v := range row {
		if b, ok := v.([]byte); ok {
			v = string(b)
			row[name] = v
		}
		c := rt.plan.columns[name]
		switch {
		case name == "version":
			if f, ok := ToFloat(v); ok {
				row[name] = int64(f)
			}
		case c == nil || v == nil:
		case c.Kind == "boolean":
			switch t := v.(type) {
			case int64:
				row[name] = t != 0
			case float64:
				row[name] = t != 0
			}
		case c.Kind == "json":
			if s, ok := v.(string); ok {
				var decoded any
				if json.Unmarshal([]byte(s), &decoded) == nil {
					row[name] = decoded
				}
			}
		case c.Kind == "integer":
			if f, ok := ToFloat(v); ok {
				row[name] = int64(f)
			}
		case c.Kind == "decimal":
			if f, ok := ToFloat(v); ok {
				row[name] = formatDecimal(int64(f), c.Scale)
			}
		case c.Kind == "number":
			if f, ok := ToFloat(v); ok {
				row[name] = f
			}
		}
	}
	return row
}

func sqlValue(dialect string, v any) any {
	if b, ok := v.(bool); ok && dialect == "sqlite" {
		if b {
			return 1
		}
		return 0
	}
	return v
}

func (rt *entityRuntime) body(ctx *ActionContext) (map[string]any, error) {
	v, _ := resolvePath(ctx.Inputs, "input")
	body, ok := v.(map[string]any)
	if !ok {
		return nil, invalidInput("the request body must be a JSON object")
	}
	return body, nil
}

func (rt *entityRuntime) checkOrg(ctx *ActionContext, rec map[string]any) error {
	if rt.org == nil {
		return nil
	}
	unit := Stringify(rec[rt.plan.spec.OrgColumn])
	if unit == "" {
		return nil
	}
	snap := rt.org.Snapshot(ctx.TenantID)
	if !rt.org.ScopeFor(ctx.Principal, snap.Tree).Covers(snap.Tree, unit) {
		return outOfScope(unit)
	}
	return nil
}

func (rt *entityRuntime) create(ctx *ActionContext) (any, error) {
	body, err := rt.body(ctx)
	if err != nil {
		return nil, err
	}
	ch, err := rt.prepareCreate(ctx, body)
	if err != nil {
		return nil, err
	}
	return rt.commitOne(ctx, ch)
}

// prepareCreate validates a submitted record and builds its insert.
func (rt *entityRuntime) prepareCreate(ctx *ActionContext, body map[string]any) (entityChange, error) {
	rec, err := rt.validate(body, true)
	if err != nil {
		return entityChange{}, err
	}
	if err := rt.checkOrg(ctx, rec); err != nil {
		return entityChange{}, err
	}
	if rt.hasRowConditions("create") {
		if err := rt.allowed(ctx, "create", rec); err != nil {
			return entityChange{}, err
		}
	}
	p := rt.plan
	if p.spec.TenantScoped && ctx.TenantID == "" {
		return entityChange{}, permissionDenied("this data is tenant-scoped but no tenant could be determined for the request")
	}
	now := ctx.Now.UTC().Format(time.RFC3339Nano)
	if ctx.Now.IsZero() {
		now = time.Now().UTC().Format(time.RFC3339Nano)
	}
	id := newPrefixedID("")
	cols := []string{"id", "created_by", "created_at", "updated_at"}
	args := []any{id, ctx.Principal.ID, now, now}
	if p.spec.TenantScoped {
		cols, args = append(cols, "tenant_id"), append(args, ctx.TenantID)
	}
	if p.spec.Versioned {
		cols, args = append(cols, "version"), append(args, 1)
	}
	for _, name := range p.order {
		if v, ok := rec[name]; ok {
			cols, args = append(cols, name), append(args, sqlValue(rt.db.Dialect, v))
		}
	}
	ph := make([]string, len(cols))
	for i := range cols {
		ph[i] = fmt.Sprintf("$%d", i+1)
	}
	stmt := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", p.table, strings.Join(cols, ", "), strings.Join(ph, ", "))
	return entityChange{event: "created", id: id, stmt: stmt, args: args}, nil
}

func (rt *entityRuntime) update(ctx *ActionContext) (any, error) {
	body, err := rt.body(ctx)
	if err != nil {
		return nil, err
	}
	ch, err := rt.prepareUpdate(ctx, rt.db.Reader(), rt.id(ctx), body)
	if err != nil {
		return nil, err
	}
	return rt.commitOne(ctx, ch)
}

// prepareUpdate reads the record through q, checks the caller may change it,
// validates the changes and builds the update. A versioned update matches
// the version the caller read, so a concurrent change affects no row.
func (rt *entityRuntime) prepareUpdate(ctx *ActionContext, q execer, id string, body map[string]any) (entityChange, error) {
	prev, err := rt.getOne(ctx, q, id, false)
	if err != nil {
		return entityChange{}, err
	}
	if rt.hasRowConditions("update") {
		if err := rt.allowed(ctx, "update", prev); err != nil {
			return entityChange{}, err
		}
	}
	changes, err := rt.validate(body, false)
	if err != nil {
		return entityChange{}, err
	}
	if err := rt.checkOrg(ctx, changes); err != nil {
		return entityChange{}, err
	}
	p := rt.plan
	now := time.Now().UTC().Format(time.RFC3339Nano)
	sets := []string{"updated_at = $1"}
	args := []any{now}
	for _, name := range p.order {
		if v, ok := changes[name]; ok {
			args = append(args, sqlValue(rt.db.Dialect, v))
			sets = append(sets, fmt.Sprintf("%s = $%d", name, len(args)))
		}
	}
	args = append(args, id)
	conds := []string{fmt.Sprintf("id = $%d", len(args))}
	if p.spec.Versioned {
		want, ok := ToFloat(body["version"])
		if !ok {
			return entityChange{}, entityInvalid("version is required", []any{map[string]any{"path": "version", "rule": "required", "message": "send the version you read"}})
		}
		sets = append(sets, "version = version + 1")
		args = append(args, int64(want))
		conds = append(conds, fmt.Sprintf("version = $%d", len(args)))
	}
	scoped, args, err := rt.scope(ctx, args)
	if err != nil {
		return entityChange{}, err
	}
	conds = append(conds, scoped...)
	stmt := fmt.Sprintf("UPDATE %s SET %s WHERE %s", p.table, strings.Join(sets, ", "), strings.Join(conds, " AND "))
	return entityChange{event: "updated", id: id, stmt: stmt, args: args, prev: prev}, nil
}

func (rt *entityRuntime) delete(ctx *ActionContext) (any, error) {
	id := rt.id(ctx)
	ch, err := rt.prepareDelete(ctx, rt.db.Reader(), id)
	if err != nil {
		return nil, err
	}
	if _, err := rt.commitOne(ctx, ch); err != nil {
		return nil, err
	}
	return map[string]any{"deleted": true, "id": id}, nil
}

// prepareDelete reads the record through q, checks the caller may delete it
// and builds the (soft or hard) delete.
func (rt *entityRuntime) prepareDelete(ctx *ActionContext, q execer, id string) (entityChange, error) {
	prev, err := rt.getOne(ctx, q, id, false)
	if err != nil {
		return entityChange{}, err
	}
	if rt.hasRowConditions("delete") {
		if err := rt.allowed(ctx, "delete", prev); err != nil {
			return entityChange{}, err
		}
	}
	p := rt.plan
	args := []any{id}
	stmt := "DELETE FROM " + p.table + " WHERE id = $1"
	if p.spec.SoftDelete {
		args = []any{time.Now().UTC().Format(time.RFC3339Nano), id}
		stmt = "UPDATE " + p.table + " SET deleted_at = $1 WHERE id = $2"
	}
	scoped, args, err := rt.scope(ctx, args)
	if err != nil {
		return entityChange{}, err
	}
	if len(scoped) > 0 {
		stmt += " AND " + strings.Join(scoped, " AND ")
	}
	return entityChange{event: "deleted", id: id, stmt: stmt, args: args, prev: prev}, nil
}

// export returns the filtered rows as CSV (up to 10000).
func (rt *entityRuntime) export(ctx *ActionContext) (any, error) {
	where, args, err := rt.where(ctx, "sort", "limit")
	if err != nil {
		return nil, err
	}
	order, err := rt.orderBy(ctx)
	if err != nil {
		return nil, err
	}
	stmt := fmt.Sprintf("SELECT %s FROM %s%s ORDER BY %s LIMIT 10000", rt.selectList(), rt.plan.table, where, order)
	rows, err := queryRows(ctx.Context, rt.db.Reader(), rebind(rt.db.Dialect, stmt), args)
	if err != nil {
		return nil, databaseFailure(err)
	}
	cols := strings.Split(rt.selectList(), ", ")
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	_ = w.Write(cols)
	for _, row := range rows {
		rt.decode(row)
		rec := make([]string, len(cols))
		for i, c := range cols {
			if v := row[c]; v != nil {
				rec[i] = csvSafe(Stringify(v))
			}
		}
		_ = w.Write(rec)
	}
	w.Flush()
	return RawResponse{ContentType: "text/csv; charset=utf-8", Filename: rt.plan.spec.Name + ".csv", Body: buf.Bytes()}, nil
}

// csvSafe neutralises spreadsheet formula injection.
func csvSafe(s string) string {
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return "'" + s
	}
	return s
}

// aggregate groups the filtered rows: ?group_by=col&agg=count|sum|avg|min|max&field=col.
func (rt *entityRuntime) aggregate(ctx *ActionContext) (any, error) {
	params := queryParams(ctx)
	get := func(k string) string {
		if len(params[k]) > 0 {
			return params[k][0]
		}
		return ""
	}
	groupBy, agg, field := get("group_by"), orDefault(get("agg"), "count"), get("field")
	if groupBy != "" && (rt.plan.columns[groupBy] == nil || rt.plan.columns[groupBy].Hidden) {
		return nil, invalidInput("cannot group by %q", groupBy)
	}
	expr := "COUNT(*)"
	switch agg {
	case "count":
	case "sum", "avg", "min", "max":
		c := rt.plan.columns[field]
		if c == nil || (c.Kind != "integer" && c.Kind != "number" && c.Kind != "decimal") {
			return nil, invalidInput("%s needs a numeric field", agg)
		}
		expr = strings.ToUpper(agg) + "(" + field + ")"
	default:
		return nil, invalidInput("agg must be count, sum, avg, min or max")
	}
	where, args, err := rt.where(ctx, "group_by", "agg", "field")
	if err != nil {
		return nil, err
	}
	stmt := "SELECT " + expr + " AS value FROM " + rt.plan.table + where
	if groupBy != "" {
		stmt = "SELECT " + groupBy + " AS grp, " + expr + " AS value FROM " + rt.plan.table + where + " GROUP BY " + groupBy + " ORDER BY " + groupBy
	}
	rows, err := queryRows(ctx.Context, rt.db.Reader(), rebind(rt.db.Dialect, stmt), args)
	if err != nil {
		return nil, databaseFailure(err)
	}
	out := make([]any, 0, len(rows))
	for _, row := range rows {
		item := map[string]any{"value": row["value"]}
		if b, ok := row["value"].([]byte); ok {
			item["value"] = string(b)
		}
		if f, ok := ToFloat(item["value"]); ok {
			item["value"] = f
			if c := rt.plan.columns[field]; c != nil && c.Kind == "decimal" && agg != "count" {
				if agg == "avg" {
					item["value"] = f / math.Pow10(c.Scale)
				} else {
					item["value"] = formatDecimal(int64(math.Round(f)), c.Scale)
				}
			}
		}
		if groupBy != "" {
			g := row["grp"]
			if b, ok := g.([]byte); ok {
				g = string(b)
			}
			item["group"] = g
		}
		out = append(out, item)
	}
	return map[string]any{"agg": agg, "field": field, "group_by": groupBy, "rows": out}, nil
}

// fire runs the entity's hooks after a committed change. A failing hook is
// logged; the change stands.
func (rt *entityRuntime) fire(ctx *ActionContext, event string, record, previous map[string]any) {
	if ctx.Platform == nil {
		return
	}
	for _, h := range rt.plan.spec.On {
		if h.Durable || (h.Event != event && h.Event != "*") {
			continue
		}
		input := map[string]any{"entity": rt.plan.spec.Name, "event": event, "record": record}
		if previous != nil {
			input["previous"] = previous
		}
		if _, err := ctx.Platform.CallIntent(ctx.Context, h.Hook, input, ctx); err != nil {
			slog.Warn("entity hook failed", "entity", rt.plan.spec.Name, "event", event, "hook", h.Hook, "error", err)
		}
	}
}

// RawResponse is a non-JSON response body (a CSV export, a PDF, an image).
// A route returns it as-is with its content type.
type RawResponse struct {
	ContentType string
	Filename    string
	Body        []byte
}

// parseDecimal reads an exact decimal ("1250.5", 1250.5, "-3") into integer
// minor units with scale decimals. More decimals than the scale is an error
// rather than a silent rounding.
func parseDecimal(raw any, scale int) (int64, error) {
	var s string
	switch v := raw.(type) {
	case string:
		s = strings.TrimSpace(v)
	case float64:
		s = strconv.FormatFloat(v, 'f', -1, 64)
	case int, int64, int32:
		s = fmt.Sprint(v)
	default:
		return 0, fmt.Errorf("must be a decimal number")
	}
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(strings.TrimPrefix(s, "-"), "+")
	whole, frac, _ := strings.Cut(s, ".")
	if whole == "" {
		whole = "0"
	}
	for _, part := range []string{whole, frac} {
		for _, r := range part {
			if r < '0' || r > '9' {
				return 0, fmt.Errorf("must be a decimal number")
			}
		}
	}
	if len(frac) > scale {
		return 0, fmt.Errorf("allows at most %d decimal places", scale)
	}
	frac += strings.Repeat("0", scale-len(frac))
	if len(whole)+len(frac) > 18 {
		return 0, fmt.Errorf("is too large")
	}
	n, err := strconv.ParseInt(whole+frac, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("must be a decimal number")
	}
	if neg {
		n = -n
	}
	return n, nil
}

// formatDecimal renders minor units as an exact decimal string.
func formatDecimal(minor int64, scale int) string {
	sign := ""
	if minor < 0 {
		sign, minor = "-", -minor
	}
	s := strconv.FormatInt(minor, 10)
	if scale == 0 {
		return sign + s
	}
	if len(s) <= scale {
		s = strings.Repeat("0", scale-len(s)+1) + s
	}
	return sign + s[:len(s)-scale] + "." + s[len(s)-scale:]
}
