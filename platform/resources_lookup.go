package platform

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/oarkflow/ref/pipeline"
)

// lookup.sources serves reference data (the choices of `lookup "name"` inputs)
// from more than one kind of source, so a field's options can come from a
// fixed list, a database query or an HTTP API, and be narrowed by the fields it
// depends_on. A pipeline.cases resource names one with lookup_resource; a set it
// does not define falls through to the org.hierarchy lookups, if any.
//
//	resource "catalog" {
//	  kind "lookup.sources"
//	  config {
//	    database "database"            # for sql sets
//	    service  "payer_api"           # a service.http resource, for http sets
//	    sets [
//	      { name "sex"    type "static" entries [ { value "F" label "Female" } ] },
//	      { name "em_codes" type "sql"
//	        query "SELECT code AS value, label FROM em_codes WHERE mdm_level = ?"
//	        args ["mdm_level"] ttl 10m },
//	      { name "payers" type "http" path "/v1/payers" items_path "data"
//	        value_field "id" label_field "name" params { region "{region}" } ttl 15m }
//	    ]
//	  }
//	}
type LookupSources struct {
	name string
	sets map[string]*lookupSet
	db   *Database
	svc  *HTTPService

	mu    sync.Mutex
	cache map[string]lookupCached
}

type lookupSet struct {
	name, kind string
	// static
	entries []lookupEntry
	// sql
	query string
	args  []string
	// http
	method, path, itemsPath, valueField, labelField string
	params                                          map[string]string
	// all kinds: which source fields become an option's value and label
	// (valueField/labelField), which become attributes that depends_on and
	// lookup_where match (attrFields), which rows are kept (filters), how they
	// are ordered (sortField) and whether the workspace's own entries are added.
	attrFields map[string]string
	// ownerField is the source field naming the workspace an entry belongs to,
	// shown to administrators as where it is defined.
	ownerField string
	// upsert and remove are the statements that let an administrator change
	// the set's rows (args name: code, label, sort, workspace, attrs.<name>).
	upsert, remove         string
	upsertArgs, removeArgs []string
	filters                []lookupFilter
	sortField              string
	merge                  bool
	ttl, timeout           time.Duration
	maxRows                int
}

// lookupFilter keeps the rows whose field satisfies op against value. The value
// may name placeholders: {specialty} is a depends_on value, {facility} (or
// {workspace}, or any level name of the hierarchy) is the unit a case is in.
type lookupFilter struct{ field, op, value string }

type lookupEntry struct {
	option pipeline.Option
	attrs  map[string]any
}

type lookupCached struct {
	options []pipeline.Option
	at      time.Time
}

func registerLookupResources(r *Registry) {
	mustResource(r, "lookup.sources", ResourceFactoryFunc(openLookupSources), ResourceKindInfo{
		Family:   "reference data",
		Summary:  "Named lookup sets for `lookup` inputs, from a static list, a SQL query or an HTTP API, cached and narrowed by depends_on fields",
		Provides: []string{"LookupSources"},
		Config: []ConfigField{
			{Name: "database", Type: "resource", Summary: "database.sql resource that sql sets query (read-only SELECTs)"},
			{Name: "service", Type: "resource", Summary: "service.http resource that http sets call (its host allowlist applies)"},
			{Name: "sets", Type: "[]object", Required: true, Summary: "{ name type static|sql|http, entries | query args | path items_path params method; all kinds: value_field label_field attrs{name field} filters[{on op value}] sort merge ttl timeout max_rows }. Placeholders {name} in args, params, path and filter values are depends_on values, or the case's workspace: {workspace} and one per hierarchy level, e.g. {facility}"},
		},
	})
}

func openLookupSources(_ context.Context, spec ResourceSpec) (Resource, io.Closer, error) {
	if err := rejectUnknownConfig("lookup.sources", spec.Config, "database", "service", "sets"); err != nil {
		return nil, nil, err
	}
	l := &LookupSources{name: spec.Name, sets: map[string]*lookupSet{}, cache: map[string]lookupCached{}}
	if configString(spec.Config, "database", "") != "" {
		db, err := requireSQLHandle(spec, "database")
		if err != nil {
			return nil, nil, err
		}
		l.db = db
	}
	if name := configString(spec.Config, "service", ""); name != "" {
		svc, ok := spec.resolved[name].(*HTTPService)
		if !ok {
			return nil, nil, fmt.Errorf("lookup.sources %q: service %q is not a service.http resource", spec.Name, name)
		}
		l.svc = svc
	}
	blocks := configBlocks(spec.Config, "sets")
	if len(blocks) == 0 {
		return nil, nil, fmt.Errorf("lookup.sources %q: sets is required", spec.Name)
	}
	for i, b := range blocks {
		set, err := lookupSetFromMap(b)
		if err != nil {
			return nil, nil, fmt.Errorf("lookup.sources %q: sets[%d]: %w", spec.Name, i, err)
		}
		if _, dup := l.sets[set.name]; dup {
			return nil, nil, fmt.Errorf("lookup.sources %q: set %q is defined twice", spec.Name, set.name)
		}
		switch {
		case set.kind == "sql" && l.db == nil:
			return nil, nil, fmt.Errorf("lookup.sources %q: set %q is sql but the resource has no database", spec.Name, set.name)
		case set.kind == "http" && l.svc == nil:
			return nil, nil, fmt.Errorf("lookup.sources %q: set %q is http but the resource has no service", spec.Name, set.name)
		}
		l.sets[set.name] = set
	}
	return l, nil, nil
}

func lookupSetFromMap(m map[string]any) (*lookupSet, error) {
	name := strings.TrimSpace(configString(m, "name", ""))
	if name == "" {
		return nil, fmt.Errorf("name is required")
	}
	s := &lookupSet{name: name, kind: strings.ToLower(configString(m, "type", "static")), maxRows: 1000}
	var err error
	if s.ttl, err = configDuration(m, "ttl", 5*time.Minute); err != nil {
		return nil, fmt.Errorf("%s: ttl: %w", name, err)
	}
	if s.timeout, err = configDuration(m, "timeout", 3*time.Second); err != nil {
		return nil, fmt.Errorf("%s: timeout: %w", name, err)
	}
	if s.maxRows, err = configInt(m, "max_rows", 1000); err != nil || s.maxRows < 1 {
		return nil, fmt.Errorf("%s: max_rows must be a positive number", name)
	}
	s.valueField = configString(m, "value_field", "")
	s.labelField = configString(m, "label_field", "")
	s.sortField = configString(m, "sort", "")
	s.merge = configBool(m, "merge", false)
	s.ownerField = configString(m, "owner_field", "")
	if s.kind == "sql" {
		s.upsert, s.remove = strings.TrimSpace(configString(m, "upsert", "")), strings.TrimSpace(configString(m, "remove", ""))
		s.upsertArgs, s.removeArgs = configStrings(m, "upsert_args"), configStrings(m, "remove_args")
		for what, st := range map[string]string{"upsert": s.upsert, "remove": s.remove} {
			if st == "" {
				continue
			}
			lower := strings.ToLower(st)
			if strings.Contains(strings.TrimRight(lower, "; \n\t"), ";") {
				return nil, fmt.Errorf("%s: %s must be a single statement", name, what)
			}
			ok := strings.HasPrefix(lower, "insert") || strings.HasPrefix(lower, "replace") || strings.HasPrefix(lower, "update") || strings.HasPrefix(lower, "delete")
			if !ok {
				return nil, fmt.Errorf("%s: %s must be an INSERT, REPLACE, UPDATE or DELETE", name, what)
			}
		}
	}
	if raw, ok := m["attrs"].(map[string]any); ok && s.kind != "static" {
		s.attrFields = make(map[string]string, len(raw))
		for k, v := range raw {
			s.attrFields[k] = Stringify(v)
		}
	}
	for i, f := range configBlocks(m, "filters") {
		lf := lookupFilter{field: configString(f, "on", ""), op: strings.ToLower(configString(f, "op", "eq")), value: configString(f, "value", "")}
		if lf.field == "" {
			return nil, fmt.Errorf("%s: filters[%d] needs `on` (the field to test)", name, i)
		}
		if !slices.Contains([]string{"eq", "ne", "in", "not_in", "contains", "prefix", "gt", "ge", "lt", "le"}, lf.op) {
			return nil, fmt.Errorf("%s: filters[%d] op %q is not eq, ne, in, not_in, contains, prefix, gt, ge, lt or le", name, i, lf.op)
		}
		s.filters = append(s.filters, lf)
	}
	switch s.kind {
	case "static":
		for i, e := range configBlocks(m, "entries") {
			value := configString(e, "value", configString(e, "code", ""))
			if value == "" {
				return nil, fmt.Errorf("%s: entries[%d] needs a value", name, i)
			}
			attrs, _ := e["attrs"].(map[string]any)
			s.entries = append(s.entries, lookupEntry{option: pipeline.Option{Value: value, Label: orDefaultString(configString(e, "label", ""), value)}, attrs: attrs})
		}
	case "sql":
		s.query = strings.TrimSpace(configString(m, "query", ""))
		if err := checkReadOnlyQuery(s.query); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		s.args = configStrings(m, "args")
	case "http":
		s.method = strings.ToUpper(configString(m, "method", "GET"))
		if s.method != "GET" && s.method != "POST" {
			return nil, fmt.Errorf("%s: method must be GET or POST", name)
		}
		if s.path = configString(m, "path", ""); s.path == "" {
			return nil, fmt.Errorf("%s: path is required", name)
		}
		s.itemsPath = configString(m, "items_path", "")
		s.valueField = orDefaultString(s.valueField, "value")
		s.labelField = orDefaultString(s.labelField, "label")
		if raw, ok := m["params"].(map[string]any); ok {
			s.params = make(map[string]string, len(raw))
			for k, v := range raw {
				s.params[k] = Stringify(v)
			}
		}
	default:
		return nil, fmt.Errorf("%s: type %q is not static, sql or http", name, s.kind)
	}
	return s, nil
}

// checkReadOnlyQuery accepts one SELECT (or WITH … SELECT) statement.
func checkReadOnlyQuery(q string) error {
	lower := strings.ToLower(strings.TrimSpace(q))
	if lower == "" {
		return fmt.Errorf("query is required")
	}
	if !strings.HasPrefix(lower, "select") && !strings.HasPrefix(lower, "with") {
		return fmt.Errorf("query must be a SELECT")
	}
	if strings.Contains(strings.TrimRight(lower, "; \n\t"), ";") {
		return fmt.Errorf("query must be a single statement")
	}
	for _, word := range []string{"insert ", "update ", "delete ", "drop ", "alter ", "create ", "attach ", "pragma "} {
		if strings.Contains(lower, word) {
			return fmt.Errorf("query may only read (found %q)", strings.TrimSpace(word))
		}
	}
	return nil
}

// Has reports whether the resource defines the set.
func (l *LookupSources) Has(set string) bool { _, ok := l.sets[set]; return ok }

// Sets lists the defined set names.
func (l *LookupSources) Sets() []string {
	out := make([]string, 0, len(l.sets))
	for n := range l.sets {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Kind is how a set gets its data (static, sql or http); empty when undefined.
func (l *LookupSources) Kind(set string) string {
	if s, ok := l.sets[set]; ok {
		return s.kind
	}
	return ""
}

// Merges reports whether the set adds the workspace's own entries to what its
// source returns, instead of replacing them.
func (l *LookupSources) Merges(set string) bool { s, ok := l.sets[set]; return ok && s.merge }

// splitLookupDeps separates the values of depends_on fields from the context a
// caller adds under "@name" keys (the workspace a case is in and its levels).
func splitLookupDeps(deps map[string]any) (form, scope map[string]any) {
	form, scope = map[string]any{}, map[string]any{}
	for k, v := range deps {
		if strings.HasPrefix(k, "@") {
			scope[k[1:]] = v
		} else {
			form[k] = v
		}
	}
	return form, scope
}

// Resolve returns the options of a set for the given dependency values. Results
// are cached for the set's ttl; when a refresh fails the last good result is
// served rather than an empty list. Keys of deps starting with "@" are context
// (the case's workspace), usable in args, params and filters but never matched
// against entry attributes.
func (l *LookupSources) Resolve(ctx context.Context, set string, deps map[string]any) ([]pipeline.Option, error) {
	s, ok := l.sets[set]
	if !ok {
		return nil, fmt.Errorf("lookup set %q is not defined", set)
	}
	form, scope := splitLookupDeps(deps)
	if s.kind == "static" {
		out := make([]pipeline.Option, 0, len(s.entries))
		for _, e := range s.entries {
			if len(form) > 0 && !lookupMatchesDeps(e.attrs, form) {
				continue
			}
			if len(s.filters) > 0 {
				row := map[string]any{"value": e.option.Value, "label": e.option.Label}
				for k, v := range e.attrs {
					row[k] = v
				}
				if keep, _ := s.keep(row, form, scope); !keep {
					continue
				}
			}
			out = append(out, e.option)
		}
		return out, nil
	}
	key := cacheKey(set, deps)
	l.mu.Lock()
	hit, cached := l.cache[key]
	l.mu.Unlock()
	if cached && time.Since(hit.at) < s.ttl {
		return slices.Clone(hit.options), nil
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	var rows []map[string]any
	var err error
	if s.kind == "sql" {
		rows, err = l.querySQL(ctx, s, form, scope)
	} else {
		rows, err = l.callHTTP(ctx, s, form, scope)
	}
	if err != nil {
		if cached {
			return slices.Clone(hit.options), nil // stale beats empty
		}
		return nil, fmt.Errorf("lookup %q: %w", set, err)
	}
	options := optionsOf(s.shape(rows, form, scope))
	l.mu.Lock()
	l.cache[key] = lookupCached{options: options, at: time.Now()}
	l.mu.Unlock()
	return slices.Clone(options), nil
}

// fieldOf reads a field of a source row: a dotted path, matched exactly and
// then without regard to case (SQL drivers fold column names).
func fieldOf(row map[string]any, field string) (any, bool) {
	var cur any = row
	for _, part := range strings.Split(field, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		v, found := m[part]
		if !found {
			for k, x := range m {
				if strings.EqualFold(k, part) {
					v, found = x, true
					break
				}
			}
		}
		if !found {
			return nil, false
		}
		cur = v
	}
	return cur, true
}

// expandLookup replaces {name} placeholders from the form values, then the
// context; ok is false when one is left unset. {name?} is optional: unset, it
// expands to nothing (an empty item of an "in" list is ignored).
func expandLookup(tmpl string, form, scope map[string]any) (string, bool) {
	out := tmpl
	for {
		i := strings.Index(out, "{")
		if i < 0 {
			return out, true
		}
		j := strings.Index(out[i:], "}")
		if j < 0 {
			return out, true
		}
		name := out[i+1 : i+j]
		optional := strings.HasSuffix(name, "?")
		v, found := lookupValue(form, scope, strings.TrimSuffix(name, "?"))
		if !found && !optional {
			return "", false
		}
		out = out[:i] + v + out[i+j+1:]
	}
}

func lookupValue(form, scope map[string]any, name string) (string, bool) {
	name = strings.TrimPrefix(name, "@")
	for _, m := range []map[string]any{form, scope} {
		if v, ok := m[name]; ok && v != nil && Stringify(v) != "" {
			return Stringify(v), true
		}
	}
	return "", false
}

// keep applies the set's filters to a row. resolved is false when a
// placeholder a filter needs is not set yet, so there are no choices to offer.
func (s *lookupSet) keep(row, form, scope map[string]any) (keep, resolved bool) {
	for _, f := range s.filters {
		want, ok := expandLookup(f.value, form, scope)
		if !ok {
			return false, false
		}
		raw, _ := fieldOf(row, f.field)
		if !filterMatches(f.op, raw, want) {
			return false, true
		}
	}
	return true, true
}

func filterMatches(op string, have any, want string) bool {
	vals := []string{Stringify(have)}
	switch v := have.(type) {
	case []any:
		vals = vals[:0]
		for _, x := range v {
			vals = append(vals, Stringify(x))
		}
	case []string:
		vals = v
	}
	some := func(f func(string) bool) bool { return slices.ContainsFunc(vals, f) }
	switch op {
	case "eq":
		return some(func(x string) bool { return strings.EqualFold(x, want) })
	case "ne":
		return !some(func(x string) bool { return strings.EqualFold(x, want) })
	case "in", "not_in":
		list := strings.Split(want, ",")
		hit := some(func(x string) bool {
			return slices.ContainsFunc(list, func(w string) bool { w = strings.TrimSpace(w); return w != "" && strings.EqualFold(w, x) })
		})
		return hit == (op == "in")
	case "contains":
		return some(func(x string) bool { return strings.Contains(strings.ToLower(x), strings.ToLower(want)) })
	case "prefix":
		return some(func(x string) bool { return strings.HasPrefix(strings.ToLower(x), strings.ToLower(want)) })
	}
	return some(func(x string) bool {
		c := strings.Compare(x, want)
		a, e1 := strconv.ParseFloat(x, 64)
		b, e2 := strconv.ParseFloat(want, 64)
		if e1 == nil && e2 == nil {
			c = 0
			if a < b {
				c = -1
			} else if a > b {
				c = 1
			}
		}
		switch op {
		case "gt":
			return c > 0
		case "ge":
			return c >= 0
		case "lt":
			return c < 0
		}
		return c <= 0
	})
}

// attrValue turns a mapped source field into an attribute: a list stays a
// list, and text holding several values ("CARD|EM" or "CARD,EM") becomes one.
func attrValue(v any) any {
	if t, ok := v.(string); ok && strings.ContainsAny(t, "|,") {
		parts := strings.FieldsFunc(t, func(r rune) bool { return r == '|' || r == ',' })
		out := make([]any, 0, len(parts))
		for _, p := range parts {
			out = append(out, strings.TrimSpace(p))
		}
		return out
	}
	return v
}

// lookupItem is an entry of a datasource set with what an administrator sees.
type lookupItem struct {
	Option pipeline.Option
	Attrs  map[string]any
	// Source is the workspace the entry is defined at, when the set names an owner field.
	Source string
	key    string
}

func optionsOf(items []lookupItem) []pipeline.Option {
	out := make([]pipeline.Option, 0, len(items))
	for _, it := range items {
		out = append(out, it.Option)
	}
	return out
}

// shape maps source rows to entries: value and label fields, filters, the
// attributes that depends_on and lookup_where narrow by, then ordering.
func (s *lookupSet) shape(rows []map[string]any, form, scope map[string]any) []lookupItem {
	narrow := map[string]any{}
	for k, v := range form {
		bare := k
		if _, b, dotted := strings.Cut(k, "."); dotted {
			bare = b
		}
		if _, mapped := s.attrFields[k]; mapped {
			narrow[k] = v
		} else if _, mapped := s.attrFields[bare]; mapped {
			narrow[bare] = v
		}
	}
	q := strings.ToLower(Stringify(scope["q"])) // type-ahead text, narrows before the row cap
	var items []lookupItem
	for _, row := range rows {
		keep, resolved := s.keep(row, form, scope)
		if !resolved {
			return nil
		}
		if !keep {
			continue
		}
		raw, _ := fieldOf(row, orDefaultString(s.valueField, "value"))
		value := Stringify(raw)
		if value == "" {
			continue
		}
		label := value
		if lraw, ok := fieldOf(row, orDefaultString(s.labelField, "label")); ok && Stringify(lraw) != "" {
			label = Stringify(lraw)
		}
		if q != "" && !strings.Contains(strings.ToLower(label+" "+value), q) {
			continue
		}
		attrs := map[string]any{}
		for name, field := range s.attrFields {
			if v, ok := fieldOf(row, field); ok && v != nil && Stringify(v) != "" {
				attrs[name] = attrValue(v)
			}
		}
		if len(narrow) > 0 && !lookupMatchesDeps(attrs, narrow) {
			continue
		}
		it := lookupItem{Option: pipeline.Option{Value: value, Label: label}, Attrs: attrs}
		if s.ownerField != "" {
			v, _ := fieldOf(row, s.ownerField)
			it.Source = Stringify(v)
		}
		if s.sortField != "" {
			v, _ := fieldOf(row, s.sortField)
			it.key = Stringify(v)
		}
		items = append(items, it)
	}
	if s.sortField != "" {
		sort.SliceStable(items, func(i, j int) bool {
			x, e1 := strconv.ParseFloat(items[i].key, 64)
			y, e2 := strconv.ParseFloat(items[j].key, 64)
			if e1 == nil && e2 == nil {
				return x < y
			}
			return items[i].key < items[j].key
		})
	}
	if len(items) > s.maxRows {
		items = items[:s.maxRows]
	}
	return items
}

// Entries evaluates a set once, uncached, with the attributes and owner of
// each entry, for an administrator's view of what the source holds.
func (l *LookupSources) Entries(ctx context.Context, set string, deps map[string]any) ([]lookupItem, error) {
	s, ok := l.sets[set]
	if !ok {
		return nil, fmt.Errorf("lookup set %q is not defined", set)
	}
	form, scope := splitLookupDeps(deps)
	if s.kind == "static" {
		out := []lookupItem{}
		for _, e := range s.entries {
			out = append(out, lookupItem{Option: e.option, Attrs: e.attrs})
		}
		return out, nil
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	var rows []map[string]any
	var err error
	if s.kind == "sql" {
		rows, err = l.querySQL(ctx, s, form, scope)
	} else {
		rows, err = l.callHTTP(ctx, s, form, scope)
	}
	if err != nil {
		return nil, err
	}
	return s.shape(rows, form, scope), nil
}

// Writable reports whether administrators can add and remove the set's rows.
func (l *LookupSources) Writable(set string) bool {
	s, ok := l.sets[set]
	return ok && s.kind == "sql" && s.upsert != ""
}

// Write adds (upsert) or removes (delete) a row of a SQL set with the
// statements the set declares, then drops its cached results. vals holds code,
// label, sort, workspace and attrs (name -> list).
func (l *LookupSources) Write(ctx context.Context, set, op string, vals map[string]any) error {
	s, ok := l.sets[set]
	if !ok {
		return fmt.Errorf("lookup set %q is not defined", set)
	}
	statement, names := s.upsert, s.upsertArgs
	if op == "delete" {
		statement, names = s.remove, s.removeArgs
	}
	if statement == "" {
		return fmt.Errorf("lookup set %q cannot be changed here: it declares no %s statement", set, map[bool]string{true: "remove", false: "upsert"}[op == "delete"])
	}
	args := make([]any, len(names))
	for i, n := range names {
		if attr, isAttr := strings.CutPrefix(n, "attrs."); isAttr {
			attrs, _ := vals["attrs"].(map[string]any)
			args[i] = joinAttr(attrs[attr])
			continue
		}
		args[i] = vals[n]
	}
	if _, err := l.db.ExecContext(ctx, statement, args...); err != nil {
		return err
	}
	l.mu.Lock()
	for k := range l.cache {
		if k == set || strings.HasPrefix(k, set+"|") {
			delete(l.cache, k)
		}
	}
	l.mu.Unlock()
	return nil
}

func joinAttr(v any) any {
	switch t := v.(type) {
	case nil:
		return ""
	case []any:
		parts := make([]string, len(t))
		for i, x := range t {
			parts[i] = Stringify(x)
		}
		return strings.Join(parts, "|")
	case []string:
		return strings.Join(t, "|")
	}
	return Stringify(v)
}

func cacheKey(set string, deps map[string]any) string {
	keys := make([]string, 0, len(deps))
	for k := range deps {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString(set)
	for _, k := range keys {
		b.WriteString("|" + k + "=" + Stringify(deps[k]))
	}
	return b.String()
}

func (l *LookupSources) querySQL(ctx context.Context, s *lookupSet, form, scope map[string]any) ([]map[string]any, error) {
	args := make([]any, len(s.args))
	for i, name := range s.args {
		v, ok := lookupValue(form, scope, name)
		if !ok {
			return nil, nil // not enough to narrow by yet: no choices
		}
		args[i] = v
	}
	rows, err := l.db.Reader().QueryContext(ctx, s.query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	// Without a value_field/label_field the columns named value|code and
	// label|name (else the first two) are the value and label.
	vi, li := 0, -1
	for i, c := range cols {
		switch strings.ToLower(c) {
		case "value", "code":
			vi = i
		case "label", "name":
			li = i
		}
	}
	if li < 0 && len(cols) > 1 && vi == 0 {
		li = 1
	}
	var out []map[string]any
	for rows.Next() {
		raw := make([]sql.NullString, len(cols))
		ptrs := make([]any, len(cols))
		for i := range raw {
			ptrs[i] = &raw[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		row := make(map[string]any, len(cols))
		for i, c := range cols {
			row[c] = raw[i].String
		}
		if s.valueField == "" && len(cols) > 0 {
			row["value"] = raw[vi].String
		}
		if s.labelField == "" && li >= 0 {
			row["label"] = raw[li].String
		}
		out = append(out, row)
		if len(out) >= 10*s.maxRows {
			break
		}
	}
	return out, rows.Err()
}

func (l *LookupSources) callHTTP(ctx context.Context, s *lookupSet, form, scope map[string]any) ([]map[string]any, error) {
	query := make(map[string]string, len(s.params))
	for k, tmpl := range s.params {
		v, ok := expandLookup(tmpl, form, scope)
		if !ok {
			return nil, nil // a parameter needs a dependency that is not set yet
		}
		query[k] = v
	}
	path, ok := expandLookup(s.path, form, scope)
	if !ok {
		return nil, nil
	}
	resp, err := l.svc.Do(ctx, HTTPRequest{Method: s.method, URL: path, Query: query})
	if err != nil {
		return nil, err
	}
	if resp.Status < 200 || resp.Status > 299 {
		return nil, fmt.Errorf("the API answered %d", resp.Status)
	}
	items := resp.JSON
	if items == nil && len(resp.Body) > 0 {
		if err := json.Unmarshal(resp.Body, &items); err != nil {
			return nil, fmt.Errorf("the API did not return JSON")
		}
	}
	for _, part := range strings.Split(s.itemsPath, ".") {
		if part == "" {
			continue
		}
		m, ok := items.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("items_path %q not found in the response", s.itemsPath)
		}
		items = m[part]
	}
	list, ok := items.([]any)
	if !ok {
		return nil, fmt.Errorf("the response has no list at items_path %q", s.itemsPath)
	}
	var out []map[string]any
	for _, item := range list {
		switch v := item.(type) {
		case map[string]any:
			out = append(out, v)
		case nil:
		default:
			out = append(out, map[string]any{s.valueField: Stringify(v), s.labelField: Stringify(v)})
		}
		if len(out) >= 10*s.maxRows {
			break
		}
	}
	return out, nil
}

// LookupTestRequest asks for a lookup set to be evaluated as written, against
// the application's live connections, so an author can see its choices before
// the change goes live.
type LookupTestRequest struct {
	// Database and Service name live database.sql and service.http resources.
	Database string
	Service  string
	// Set is the set as it appears in a lookup.sources `sets` list.
	Set map[string]any
	// Deps are values for the fields the set depends on.
	Deps map[string]any
}

// LookupTestResult is what the set produced. Options is capped at 50.
type LookupTestResult struct {
	Options    []pipeline.Option `json:"options"`
	Total      int               `json:"total"`
	DurationMS int64             `json:"duration_ms"`
}

// TestLookup evaluates a lookup set once, without the cache, and reports its
// choices or why it failed. Only reads happen: sql sets are limited to one
// SELECT and http sets go through a service.http resource, so its host
// allowlist applies.
func (p *Platform) TestLookup(ctx context.Context, req LookupTestRequest) (LookupTestResult, error) {
	var res LookupTestResult
	if req.Set == nil {
		return res, fmt.Errorf("a set is required")
	}
	if _, ok := req.Set["name"]; !ok {
		req.Set = cloneMap(req.Set)
		req.Set["name"] = "test"
	}
	set, err := lookupSetFromMap(req.Set)
	if err != nil {
		return res, err
	}
	l := &LookupSources{name: "test", sets: map[string]*lookupSet{set.name: set}, cache: map[string]lookupCached{}}
	switch set.kind {
	case "sql":
		r, ok := p.Resource(req.Database)
		db, isDB := r.(*Database)
		if req.Database == "" || !ok || !isDB {
			return res, fmt.Errorf("%q is not a database connection of the running application (is it new? make it live first)", req.Database)
		}
		l.db = db
	case "http":
		r, ok := p.Resource(req.Service)
		svc, isSvc := r.(*HTTPService)
		if req.Service == "" || !ok || !isSvc {
			return res, fmt.Errorf("%q is not an API connection of the running application (is it new? make it live first)", req.Service)
		}
		l.svc = svc
	}
	start := time.Now()
	options, err := l.Resolve(ctx, set.name, req.Deps)
	res.DurationMS = time.Since(start).Milliseconds()
	if err != nil {
		return res, err
	}
	res.Total = len(options)
	if len(options) > 50 {
		options = options[:50]
	}
	res.Options = options
	if res.Options == nil {
		res.Options = []pipeline.Option{}
	}
	return res, nil
}

func cloneMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m)+1)
	for k, v := range m {
		out[k] = v
	}
	return out
}
