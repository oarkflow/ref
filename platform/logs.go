package platform

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// Structured logs as a node.
//
// log.write records an entry in a LogSink: a resource that stores entries and
// can be queried. Sinks are pluggable: this file ships log.memory, log.file,
// log.sql and log.tee (fan-out), and a host adds another (Loki, Elasticsearch,
// a vendor API) with RegisterResourceDriver for any kind whose resource
// implements LogSink.
//
// Two rules govern the node, both learned from the cache actions:
//   - Writing a log never fails the request. A sink outage is reported through
//     the node's output, not as an error.
//   - Fields named in `redact` are replaced before the entry reaches any sink,
//     so a secret that a template happened to include is never persisted.

// LogEntry is one structured log record.
type LogEntry struct {
	Time      time.Time      `json:"time"`
	Level     string         `json:"level"`
	Message   string         `json:"message"`
	Fields    map[string]any `json:"fields,omitempty"`
	RequestID string         `json:"request_id,omitempty"`
	Intent    string         `json:"intent,omitempty"`
	Principal string         `json:"principal,omitempty"`
	Tenant    string         `json:"tenant,omitempty"`
}

// LogQuery filters entries. Zero fields match everything.
type LogQuery struct {
	MinLevel  string
	Since     time.Time
	Until     time.Time
	Contains  string
	RequestID string
	Tenant    string
	Limit     int
}

// LogSink stores and retrieves log entries. Implementations must be safe for
// concurrent use.
type LogSink interface {
	Write(ctx context.Context, e LogEntry) error
	// Query returns matching entries, newest first. A write-only sink returns
	// ErrLogQueryUnsupported.
	Query(ctx context.Context, q LogQuery) ([]LogEntry, error)
}

// ErrLogQueryUnsupported is returned by a sink that cannot be read back.
var ErrLogQueryUnsupported = fmt.Errorf("this log sink cannot be queried")

var logLevels = map[string]int{"debug": 0, "info": 1, "warn": 2, "error": 3}

func levelRank(l string) int {
	if r, ok := logLevels[strings.ToLower(l)]; ok {
		return r
	}
	return 1
}

func (q LogQuery) match(e LogEntry) bool {
	if q.MinLevel != "" && levelRank(e.Level) < levelRank(q.MinLevel) {
		return false
	}
	if !q.Since.IsZero() && e.Time.Before(q.Since) {
		return false
	}
	if !q.Until.IsZero() && !e.Time.Before(q.Until) {
		return false
	}
	if q.RequestID != "" && e.RequestID != q.RequestID {
		return false
	}
	if q.Tenant != "" && e.Tenant != q.Tenant {
		return false
	}
	if q.Contains != "" {
		hay := strings.ToLower(e.Message)
		if len(e.Fields) > 0 {
			b, _ := json.Marshal(e.Fields)
			hay += " " + strings.ToLower(string(b))
		}
		if !strings.Contains(hay, strings.ToLower(q.Contains)) {
			return false
		}
	}
	return true
}

func (q LogQuery) limit() int {
	if q.Limit <= 0 || q.Limit > 1000 {
		return 100
	}
	return q.Limit
}

// ---------------------------------------------------------------------------
// log.memory
// ---------------------------------------------------------------------------

type memoryLog struct {
	mu   sync.Mutex
	buf  []LogEntry
	next int
	full bool
}

func (m *memoryLog) Write(_ context.Context, e LogEntry) error {
	m.mu.Lock()
	m.buf[m.next] = e
	m.next = (m.next + 1) % len(m.buf)
	if m.next == 0 {
		m.full = true
	}
	m.mu.Unlock()
	return nil
}

func (m *memoryLog) Query(_ context.Context, q LogQuery) ([]LogEntry, error) {
	m.mu.Lock()
	var ordered []LogEntry
	if m.full {
		ordered = append(append(ordered, m.buf[m.next:]...), m.buf[:m.next]...)
	} else {
		ordered = append(ordered, m.buf[:m.next]...)
	}
	m.mu.Unlock()
	return newestFirst(ordered, q), nil
}

func newestFirst(all []LogEntry, q LogQuery) []LogEntry {
	out := []LogEntry{}
	for i := len(all) - 1; i >= 0 && len(out) < q.limit(); i-- {
		if q.match(all[i]) {
			out = append(out, all[i])
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// log.file: JSON lines, rotated by size
// ---------------------------------------------------------------------------

type fileLog struct {
	mu       sync.Mutex
	path     string
	maxBytes int64
	f        *os.File
	size     int64
}

func openFileLog(path string, maxBytes int64) (*fileLog, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, err
	}
	l := &fileLog{path: path, maxBytes: maxBytes}
	return l, l.open()
}

func (l *fileLog) open() error {
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return err
	}
	l.f, l.size = f, st.Size()
	return nil
}

func (l *fileLog) Write(_ context.Context, e LogEntry) error {
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	line = append(line, '\n')
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.maxBytes > 0 && l.size+int64(len(line)) > l.maxBytes && l.size > 0 {
		_ = l.f.Close()
		if err := os.Rename(l.path, l.path+".1"); err != nil {
			return err
		}
		if err := l.open(); err != nil {
			return err
		}
	}
	n, err := l.f.Write(line)
	l.size += int64(n)
	return err
}

func (l *fileLog) Query(_ context.Context, q LogQuery) ([]LogEntry, error) {
	l.mu.Lock()
	_ = l.f.Sync()
	l.mu.Unlock()
	var all []LogEntry
	for _, p := range []string{l.path + ".1", l.path} {
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			var e LogEntry
			if json.Unmarshal(sc.Bytes(), &e) == nil {
				all = append(all, e)
			}
		}
		f.Close()
	}
	return newestFirst(all, q), nil
}

func (l *fileLog) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.f.Close()
}

// logTimeLayout is fixed-width so stored timestamps order correctly as text.
const logTimeLayout = "2006-01-02T15:04:05.000000000Z"

// ---------------------------------------------------------------------------
// log.sql
// ---------------------------------------------------------------------------

type sqlLog struct {
	db    *Database
	table string
}

func openSQLLog(ctx context.Context, db *Database, table string) (*sqlLog, error) {
	text, key := "TEXT", "VARCHAR(191)"
	if db.Dialect == "mysql" {
		text = "LONGTEXT"
	}
	idType := "BIGSERIAL PRIMARY KEY"
	switch db.Dialect {
	case "mysql":
		idType = "BIGINT AUTO_INCREMENT PRIMARY KEY"
	case "sqlite":
		idType = "INTEGER PRIMARY KEY AUTOINCREMENT"
	}
	stmt := `CREATE TABLE IF NOT EXISTS ` + table + ` (id ` + idType + `, ts ` + key + ` NOT NULL, level ` + key + ` NOT NULL,
		message ` + text + ` NOT NULL, fields ` + text + ` NOT NULL, request_id ` + key + ` NOT NULL DEFAULT '',
		intent ` + key + ` NOT NULL DEFAULT '', principal ` + key + ` NOT NULL DEFAULT '', tenant ` + key + ` NOT NULL DEFAULT '')`
	if _, err := db.ExecContext(ctx, stmt); err != nil {
		return nil, err
	}
	return &sqlLog{db: db, table: table}, nil
}

func (l *sqlLog) Write(ctx context.Context, e LogEntry) error {
	fields, _ := json.Marshal(e.Fields)
	_, err := l.db.ExecContext(ctx, rebind(l.db.Dialect, `INSERT INTO `+l.table+
		` (ts, level, message, fields, request_id, intent, principal, tenant) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`),
		e.Time.UTC().Format(logTimeLayout), e.Level, e.Message, string(fields), e.RequestID, e.Intent, e.Principal, e.Tenant)
	return err
}

func (l *sqlLog) Query(ctx context.Context, q LogQuery) ([]LogEntry, error) {
	where, args := []string{"1=1"}, []any{}
	add := func(cond string, v any) {
		args = append(args, v)
		where = append(where, strings.ReplaceAll(cond, "?", fmt.Sprintf("$%d", len(args))))
	}
	if q.RequestID != "" {
		add("request_id = ?", q.RequestID)
	}
	if q.Tenant != "" {
		add("tenant = ?", q.Tenant)
	}
	if !q.Since.IsZero() {
		add("ts >= ?", q.Since.UTC().Format(logTimeLayout))
	}
	if !q.Until.IsZero() {
		add("ts < ?", q.Until.UTC().Format(logTimeLayout))
	}
	if q.MinLevel != "" {
		var levels []string
		for name, r := range logLevels {
			if r >= levelRank(q.MinLevel) {
				levels = append(levels, name)
			}
		}
		slices.Sort(levels)
		ph := make([]string, len(levels))
		for i, v := range levels {
			args = append(args, v)
			ph[i] = fmt.Sprintf("$%d", len(args))
		}
		where = append(where, "level IN ("+strings.Join(ph, ",")+")")
	}
	if q.Contains != "" {
		// "!" is the escape character, so a "%" or "_" a user types is a
		// literal. The pattern is passed twice because MySQL's ? is positional.
		esc := strings.NewReplacer("!", "!!", "%", "!%", "_", "!_").Replace(strings.ToLower(q.Contains))
		args = append(args, "%"+esc+"%", "%"+esc+"%")
		n := len(args)
		where = append(where, fmt.Sprintf(`(LOWER(message) LIKE $%d ESCAPE '!' OR LOWER(fields) LIKE $%d ESCAPE '!')`, n-1, n))
	}
	stmt := fmt.Sprintf(`SELECT ts, level, message, fields, request_id, intent, principal, tenant FROM %s WHERE %s ORDER BY id DESC LIMIT %d`,
		l.table, strings.Join(where, " AND "), q.limit())
	rows, err := l.db.Reader().QueryContext(ctx, rebind(l.db.Dialect, stmt), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []LogEntry{}
	for rows.Next() {
		var e LogEntry
		var ts, fields string
		if err := rows.Scan(&ts, &e.Level, &e.Message, &fields, &e.RequestID, &e.Intent, &e.Principal, &e.Tenant); err != nil {
			return nil, err
		}
		e.Time, _ = time.Parse(time.RFC3339Nano, ts)
		_ = json.Unmarshal([]byte(fields), &e.Fields)
		out = append(out, e)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// log.tee
// ---------------------------------------------------------------------------

type teeLog struct{ sinks []LogSink }

func (t *teeLog) Write(ctx context.Context, e LogEntry) error {
	var first error
	for _, s := range t.sinks {
		if err := s.Write(ctx, e); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func (t *teeLog) Query(ctx context.Context, q LogQuery) ([]LogEntry, error) {
	for _, s := range t.sinks {
		if out, err := s.Query(ctx, q); err == nil {
			return out, nil
		} else if err != ErrLogQueryUnsupported {
			return nil, err
		}
	}
	return nil, ErrLogQueryUnsupported
}

// ---------------------------------------------------------------------------
// Resources
// ---------------------------------------------------------------------------

func registerLogResources(r *Registry) {
	mustResource(r, "log.memory", ResourceFactoryFunc(func(_ context.Context, spec ResourceSpec) (Resource, io.Closer, error) {
		if err := rejectUnknownConfig("log.memory", spec.Config, "capacity"); err != nil {
			return nil, nil, err
		}
		n, err := configInt(spec.Config, "capacity", 5000)
		if err != nil || n < 1 {
			return nil, nil, fmt.Errorf("log.memory %q: capacity must be a positive number", spec.Name)
		}
		return &memoryLog{buf: make([]LogEntry, n)}, nil, nil
	}), ResourceKindInfo{Family: "log", Summary: "In-process ring buffer of recent entries; queryable, lost on restart.",
		Config: []ConfigField{{Name: "capacity", Type: "int", Default: "5000"}}})

	mustResource(r, "log.file", ResourceFactoryFunc(func(_ context.Context, spec ResourceSpec) (Resource, io.Closer, error) {
		if err := rejectUnknownConfig("log.file", spec.Config, "path", "max_bytes"); err != nil {
			return nil, nil, err
		}
		path, err := requiredString(spec.Config, "path")
		if err != nil {
			return nil, nil, fmt.Errorf("log.file %q: %w", spec.Name, err)
		}
		maxBytes, err := configInt64(spec.Config, "max_bytes", 64<<20)
		if err != nil {
			return nil, nil, err
		}
		l, err := openFileLog(path, maxBytes)
		if err != nil {
			return nil, nil, fmt.Errorf("log.file %q: %w", spec.Name, err)
		}
		return l, l, nil
	}), ResourceKindInfo{Family: "log", Summary: "JSON-lines file, rotated once at max_bytes (path.1 keeps the previous generation).",
		Config: []ConfigField{{Name: "path", Type: "string", Required: true}, {Name: "max_bytes", Type: "int", Default: "67108864"}}})

	mustResource(r, "log.sql", ResourceFactoryFunc(func(ctx context.Context, spec ResourceSpec) (Resource, io.Closer, error) {
		if err := rejectUnknownConfig("log.sql", spec.Config, "database", "table"); err != nil {
			return nil, nil, err
		}
		name, err := requiredString(spec.Config, "database")
		if err != nil {
			return nil, nil, fmt.Errorf("log.sql %q: %w", spec.Name, err)
		}
		db, ok := spec.resolved[name].(*Database)
		if !ok {
			return nil, nil, fmt.Errorf("log.sql %q: %q is not a database.sql resource", spec.Name, name)
		}
		table := configString(spec.Config, "table", "app_logs")
		if !identifierPattern.MatchString(table) {
			return nil, nil, fmt.Errorf("log.sql %q: table %q is not a plain identifier", spec.Name, table)
		}
		l, err := openSQLLog(ctx, db, table)
		if err != nil {
			return nil, nil, fmt.Errorf("log.sql %q: %w", spec.Name, err)
		}
		return l, nil, nil
	}), ResourceKindInfo{Family: "log", Summary: "Entries in a SQL table; queryable and shared by every replica.",
		Config: []ConfigField{{Name: "database", Type: "resource", Required: true}, {Name: "table", Type: "string", Default: "app_logs"}}})

	mustResource(r, "log.tee", ResourceFactoryFunc(func(_ context.Context, spec ResourceSpec) (Resource, io.Closer, error) {
		if err := rejectUnknownConfig("log.tee", spec.Config, "sinks"); err != nil {
			return nil, nil, err
		}
		names := configStrings(spec.Config, "sinks")
		if len(names) == 0 {
			return nil, nil, fmt.Errorf("log.tee %q: sinks must name at least one log resource", spec.Name)
		}
		tee := &teeLog{}
		for _, n := range names {
			s, ok := spec.resolved[n].(LogSink)
			if !ok {
				return nil, nil, fmt.Errorf("log.tee %q: %q is not a log sink", spec.Name, n)
			}
			tee.sinks = append(tee.sinks, s)
		}
		return tee, nil, nil
	}), ResourceKindInfo{Family: "log", Summary: "Writes every entry to several sinks; queries the first that can be queried.",
		Config: []ConfigField{{Name: "sinks", Type: "list", Required: true}}})
}

// ---------------------------------------------------------------------------
// Actions
// ---------------------------------------------------------------------------

func registerLogActions(r *Registry) {
	mustAction(r, "log.write", logWriteAction, ActionInfo{
		Family: "log", Summary: "Record a structured entry. Never fails the request; redact names fields to mask.",
		ResourceKind: "log", Kind: "effect", Provides: "true when stored",
		Config: []ConfigField{
			{Name: "message", Type: "template", Required: true},
			{Name: "level", Type: "string", Default: "info", Summary: "debug, info, warn or error"},
			{Name: "fields", Type: "object", Summary: "name -> template"},
			{Name: "redact", Type: "list", Summary: "field names whose values are replaced with [redacted]"},
		},
	})
	mustAction(r, "log.query", logQueryAction, ActionInfo{
		Family: "log", Summary: "Search entries, newest first",
		ResourceKind: "log", Kind: "read", Provides: "{entries}",
		Config: []ConfigField{
			{Name: "min_level_fact", Type: "fact"}, {Name: "contains_fact", Type: "fact"},
			{Name: "request_id_fact", Type: "fact"}, {Name: "since_fact", Type: "fact", Summary: "RFC 3339"},
			{Name: "limit", Type: "int", Default: "100"},
			{Name: "this_tenant", Type: "bool", Default: "true", Summary: "Restrict to the caller's tenant"},
		},
	})
}

func requireLogSink(build BuildContext, spec NodeSpec) (LogSink, error) {
	res, ok := build.Resource(spec.Resource)
	if !ok {
		return nil, fmt.Errorf("node %q: log resource %q is not declared", spec.Name, spec.Resource)
	}
	sink, ok := res.(LogSink)
	if !ok {
		return nil, fmt.Errorf("node %q: resource %q is not a log sink", spec.Name, spec.Resource)
	}
	return sink, nil
}

var logWriteAction = ActionFactoryFunc(func(build BuildContext, spec NodeSpec) (Action, error) {
	sink, err := requireLogSink(build, spec)
	if err != nil {
		return nil, err
	}
	message, err := configTemplate(spec.Config, "message", "")
	if err != nil || message == nil {
		return nil, fmt.Errorf("node %q: message is required", spec.Name)
	}
	level := strings.ToLower(configString(spec.Config, "level", "info"))
	if _, ok := logLevels[level]; !ok {
		return nil, fmt.Errorf("node %q: level %q is not one of debug, info, warn, error", spec.Name, level)
	}
	raw := map[string]string{}
	for k, v := range configMap(spec.Config, "fields") {
		raw[k] = Stringify(v)
	}
	fields, err := compileTemplateMap("log.write "+spec.Name, "field", raw)
	if err != nil {
		return nil, err
	}
	redact := map[string]bool{}
	for _, n := range configStrings(spec.Config, "redact") {
		redact[strings.ToLower(n)] = true
	}
	return ActionFunc(func(ctx *ActionContext) (ActionResult, error) {
		env := actionEnv(ctx)
		msg, err := message.Render(env)
		if err != nil {
			return ActionResult{}, invalidInput("log message: %v", err)
		}
		rendered, err := renderTemplateMap(fields, env)
		if err != nil {
			return ActionResult{}, invalidInput("log fields: %v", err)
		}
		entry := LogEntry{Time: time.Now().UTC(), Level: level, Message: msg, Principal: ctx.Principal.ID, Tenant: ctx.TenantID}
		if ctx.Invocation != nil {
			entry.RequestID, entry.Intent = string(ctx.Invocation.ID), string(ctx.Invocation.Intent)
		}
		if len(rendered) > 0 {
			entry.Fields = make(map[string]any, len(rendered))
			for k, v := range rendered {
				if redact[strings.ToLower(k)] {
					v = "[redacted]"
				}
				entry.Fields[k] = v
			}
		}
		if err := sink.Write(ctx.Context, entry); err != nil {
			return acknowledgement(spec, false), nil
		}
		return acknowledgement(spec, true), nil
	}), nil
})

var logQueryAction = ActionFactoryFunc(func(build BuildContext, spec NodeSpec) (Action, error) {
	sink, err := requireLogSink(build, spec)
	if err != nil {
		return nil, err
	}
	limit, err := configInt(spec.Config, "limit", 100)
	if err != nil {
		return nil, fmt.Errorf("node %q: %w", spec.Name, err)
	}
	scoped := configBool(spec.Config, "this_tenant", true)
	opt := func(ctx *ActionContext, key string) string {
		if fact := configString(spec.Config, key+"_fact", ""); fact != "" {
			if v, ok := resolvePath(ctx.Inputs, fact); ok {
				return Stringify(v)
			}
		}
		return ""
	}
	return ActionFunc(func(ctx *ActionContext) (ActionResult, error) {
		q := LogQuery{
			MinLevel: opt(ctx, "min_level"), Contains: opt(ctx, "contains"), RequestID: opt(ctx, "request_id"), Limit: limit,
		}
		if scoped {
			q.Tenant = ctx.TenantID
		}
		if s := opt(ctx, "since"); s != "" {
			t, err := time.Parse(time.RFC3339, s)
			if err != nil {
				return ActionResult{}, invalidInput("since must be an RFC 3339 time")
			}
			q.Since = t
		}
		entries, err := sink.Query(ctx.Context, q)
		if err == ErrLogQueryUnsupported {
			return ActionResult{}, invalidInput("resource %q cannot be queried", spec.Resource)
		}
		if err != nil {
			return ActionResult{}, unavailable("the log sink could not be read: %v", err)
		}
		return singleOutput(spec, map[string]any{"entries": entries}), nil
	}), nil
})
