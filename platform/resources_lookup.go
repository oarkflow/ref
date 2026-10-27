package platform

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"sort"
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
	// all kinds
	ttl, timeout time.Duration
	maxRows      int
}

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
			{Name: "sets", Type: "[]object", Required: true, Summary: "{ name type static|sql|http, entries | query args | path items_path value_field label_field params method, ttl timeout max_rows }"},
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
		s.valueField = configString(m, "value_field", "value")
		s.labelField = configString(m, "label_field", "label")
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

// Resolve returns the options of a set for the given dependency values. Results
// are cached for the set's ttl; when a refresh fails the last good result is
// served rather than an empty list.
func (l *LookupSources) Resolve(ctx context.Context, set string, deps map[string]any) ([]pipeline.Option, error) {
	s, ok := l.sets[set]
	if !ok {
		return nil, fmt.Errorf("lookup set %q is not defined", set)
	}
	if s.kind == "static" {
		out := make([]pipeline.Option, 0, len(s.entries))
		for _, e := range s.entries {
			if len(deps) == 0 || lookupMatchesDeps(e.attrs, deps) {
				out = append(out, e.option)
			}
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
	var options []pipeline.Option
	var err error
	if s.kind == "sql" {
		options, err = l.querySQL(ctx, s, deps)
	} else {
		options, err = l.callHTTP(ctx, s, deps)
	}
	if err != nil {
		if cached {
			return slices.Clone(hit.options), nil // stale beats empty
		}
		return nil, fmt.Errorf("lookup %q: %w", set, err)
	}
	l.mu.Lock()
	l.cache[key] = lookupCached{options: options, at: time.Now()}
	l.mu.Unlock()
	return slices.Clone(options), nil
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

func (l *LookupSources) querySQL(ctx context.Context, s *lookupSet, deps map[string]any) ([]pipeline.Option, error) {
	args := make([]any, len(s.args))
	for i, name := range s.args {
		v, ok := deps[name]
		if !ok || v == nil || Stringify(v) == "" {
			return nil, nil // not enough to narrow by yet: no choices
		}
		args[i] = Stringify(v)
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
	var out []pipeline.Option
	for rows.Next() {
		raw := make([]sql.NullString, len(cols))
		ptrs := make([]any, len(cols))
		for i := range raw {
			ptrs[i] = &raw[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		value := raw[vi].String
		label := value
		if li >= 0 && raw[li].String != "" {
			label = raw[li].String
		}
		out = append(out, pipeline.Option{Value: value, Label: label})
		if len(out) >= s.maxRows {
			break
		}
	}
	return out, rows.Err()
}

func (l *LookupSources) callHTTP(ctx context.Context, s *lookupSet, deps map[string]any) ([]pipeline.Option, error) {
	query := make(map[string]string, len(s.params))
	for k, tmpl := range s.params {
		v := tmpl
		for name, dep := range deps {
			v = strings.ReplaceAll(v, "{"+name+"}", Stringify(dep))
		}
		if strings.Contains(v, "{") && strings.Contains(v, "}") {
			return nil, nil // a parameter needs a dependency that is not set yet
		}
		query[k] = v
	}
	resp, err := l.svc.Do(ctx, HTTPRequest{Method: s.method, URL: s.path, Query: query})
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
	var out []pipeline.Option
	for _, item := range list {
		switch v := item.(type) {
		case map[string]any:
			value := Stringify(v[s.valueField])
			if value == "" {
				continue
			}
			label := Stringify(v[s.labelField])
			out = append(out, pipeline.Option{Value: value, Label: orDefaultString(label, value)})
		case nil:
		default:
			out = append(out, pipeline.Option{Value: Stringify(v), Label: Stringify(v)})
		}
		if len(out) >= s.maxRows {
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
