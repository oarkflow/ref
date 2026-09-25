package platform

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/oarkflow/ref/hierarchy"
)

// OrgHierarchy is the org.hierarchy resource: an organisational tree (state →
// district → municipality, company → division → department, ...) plus the
// reference data configured along it.
//
// Reads go to an immutable per-tenant snapshot held in an atomic pointer, so
// scope checks on every request cost a map lookup and no lock. Writes persist
// to SQL (when a database is configured), then publish a new snapshot.
//
// Tenancy: rows carry a tenant id. The "" tenant is the shared tree — inline
// `nodes`/`lookups` config lands there — and is what a tenant without its own
// tree sees. A tenant that writes gets its own tree from then on.
type OrgHierarchy struct {
	name            string
	opts            hierarchy.Options
	db              *Database
	table           string
	lookupTable     string
	assignmentClaim string
	globalRoles     []string
	seedNodes       []hierarchy.Node
	seedLookups     []hierarchy.LookupEntry

	writeMu sync.Mutex
	state   atomic.Pointer[orgState]
	stop    chan struct{}
	stopped sync.Once
	wg      sync.WaitGroup
}

type orgState struct {
	byTenant map[string]*OrgSnapshot
}

// OrgSnapshot is one tenant's tree and lookups.
type OrgSnapshot struct {
	Tree    *hierarchy.Tree
	Lookups *hierarchy.Lookups
}

// OrgScope is what a principal may see: the roots of the subtrees assigned to
// it, or everything when Global.
type OrgScope struct {
	Global   bool     `json:"global"`
	Assigned []string `json:"assigned"`
}

// Covers reports whether the scope includes node id.
func (s OrgScope) Covers(tree *hierarchy.Tree, id string) bool {
	if !tree.Has(id) {
		return false
	}
	return s.Global || tree.Covers(s.Assigned, id)
}

func registerOrgResources(r *Registry) {
	mustResource(r, "org.hierarchy", ResourceFactoryFunc(openOrgHierarchy), ResourceKindInfo{
		Family:   "organisation",
		Summary:  "Organisational hierarchy (e.g. state → district → municipality) with inherited, scoped reference data",
		Provides: []string{"OrgHierarchy"},
		Config: []ConfigField{
			{Name: "levels", Type: "[]string", Summary: "Tiers from the top down, e.g. [country state district municipality]; empty disables level checks"},
			{Name: "strict_levels", Type: "bool", Summary: "A child must sit exactly one level below its parent", Default: "false"},
			{Name: "root_levels", Type: "[]string", Summary: "Levels allowed as roots (default: the first level)"},
			{Name: "database", Type: "string", Summary: "database.sql resource that persists units and lookups; omit for a config-only hierarchy"},
			{Name: "table", Type: "string", Default: "org_units"},
			{Name: "lookup_table", Type: "string", Default: "org_lookups"},
			{Name: "migrate", Type: "bool", Default: "true", Summary: "Create the tables at startup"},
			{Name: "refresh_interval", Type: "duration", Summary: "Reload from the database periodically, for multi-replica deployments"},
			{Name: "assignment_claim", Type: "string", Default: "org_units", Summary: "Principal claim listing the units a user is assigned to (string, comma list or array)"},
			{Name: "global_roles", Type: "[]string", Summary: "Roles that see the whole tree regardless of assignment"},
			{Name: "nodes", Type: "[]object", Summary: "Inline units: { id parent level code name attrs {} }"},
			{Name: "lookups", Type: "[]object", Summary: "Inline reference data: { set code label node workspace department sort disabled attrs {} }"},
		},
	})
}

func openOrgHierarchy(ctx context.Context, spec ResourceSpec) (Resource, io.Closer, error) {
	if err := rejectUnknownConfig("org.hierarchy", spec.Config,
		"levels", "strict_levels", "root_levels", "database", "table", "lookup_table", "migrate",
		"refresh_interval", "assignment_claim", "global_roles", "nodes", "lookups"); err != nil {
		return nil, nil, err
	}
	h := &OrgHierarchy{
		name: spec.Name,
		opts: hierarchy.Options{
			Levels:       configStrings(spec.Config, "levels"),
			StrictLevels: configBool(spec.Config, "strict_levels", false),
			RootLevels:   configStrings(spec.Config, "root_levels"),
		},
		assignmentClaim: configString(spec.Config, "assignment_claim", "org_units"),
		globalRoles:     configStrings(spec.Config, "global_roles"),
		stop:            make(chan struct{}),
	}
	for i, block := range configBlocks(spec.Config, "nodes") {
		n, err := orgNodeFromMap(block)
		if err != nil {
			return nil, nil, fmt.Errorf("org.hierarchy %q: nodes[%d]: %w", spec.Name, i, err)
		}
		h.seedNodes = append(h.seedNodes, n)
	}
	for i, block := range configBlocks(spec.Config, "lookups") {
		e, err := lookupEntryFromMap(block)
		if err != nil {
			return nil, nil, fmt.Errorf("org.hierarchy %q: lookups[%d]: %w", spec.Name, i, err)
		}
		h.seedLookups = append(h.seedLookups, e)
	}
	if configString(spec.Config, "database", "") != "" {
		db, err := requireSQLHandle(spec, "database")
		if err != nil {
			return nil, nil, err
		}
		h.db = db
		if h.table, err = safeIdentifier(configString(spec.Config, "table", "org_units")); err != nil {
			return nil, nil, fmt.Errorf("org.hierarchy %q: table: %w", spec.Name, err)
		}
		if h.lookupTable, err = safeIdentifier(configString(spec.Config, "lookup_table", "org_lookups")); err != nil {
			return nil, nil, fmt.Errorf("org.hierarchy %q: lookup_table: %w", spec.Name, err)
		}
		if configBool(spec.Config, "migrate", true) {
			if err := h.migrate(ctx); err != nil {
				return nil, nil, fmt.Errorf("org.hierarchy %q: migrate: %w", spec.Name, err)
			}
		}
	}
	if err := h.reload(ctx); err != nil {
		return nil, nil, fmt.Errorf("org.hierarchy %q: %w", spec.Name, err)
	}
	interval, err := configDuration(spec.Config, "refresh_interval", 0)
	if err != nil {
		return nil, nil, err
	}
	if interval > 0 && h.db != nil {
		h.wg.Add(1)
		go h.refreshLoop(interval)
	}
	return h, closerFunc(h.close), nil
}

func (h *OrgHierarchy) close() error {
	h.stopped.Do(func() { close(h.stop) })
	h.wg.Wait()
	return nil
}

func (h *OrgHierarchy) refreshLoop(interval time.Duration) {
	defer h.wg.Done()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-h.stop:
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), interval)
			_ = h.reload(ctx) // keep serving the last good snapshot on failure
			cancel()
		}
	}
}

func (h *OrgHierarchy) migrate(ctx context.Context) error {
	text := "TEXT"
	key := "TEXT"
	if h.db.Dialect == "mysql" {
		key = "VARCHAR(191)"
	}
	statements := []string{
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
			tenant_id %s NOT NULL DEFAULT '',
			id        %s NOT NULL,
			parent_id %s,
			level     %s,
			code      %s,
			name      %s,
			path      %s,
			attrs     %s,
			PRIMARY KEY (tenant_id, id))`, h.table, key, key, key, text, text, text, text, text),
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
			tenant_id  %s NOT NULL DEFAULT '',
			set_name   %s NOT NULL,
			code       %s NOT NULL,
			node_id    %s NOT NULL DEFAULT '',
			workspace  %s NOT NULL DEFAULT '',
			department %s NOT NULL DEFAULT '',
			label      %s,
			sort_order INTEGER NOT NULL DEFAULT 0,
			disabled   INTEGER NOT NULL DEFAULT 0,
			attrs      %s,
			PRIMARY KEY (tenant_id, set_name, code, node_id, workspace, department))`,
			h.lookupTable, key, key, key, key, key, key, text, text),
	}
	for _, statement := range statements {
		if _, err := h.db.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

// reload rebuilds every tenant's snapshot from the inline seed plus the
// database. Stored rows override seed rows with the same id.
func (h *OrgHierarchy) reload(ctx context.Context) error {
	nodes := map[string][]hierarchy.Node{"": slices.Clone(h.seedNodes)}
	lookups := map[string][]hierarchy.LookupEntry{"": slices.Clone(h.seedLookups)}
	if h.db != nil {
		rows, err := queryRows(ctx, h.db.DB, fmt.Sprintf(
			"SELECT tenant_id, id, parent_id, level, code, name, attrs FROM %s", h.table), nil)
		if err != nil {
			return fmt.Errorf("load units: %w", err)
		}
		for _, row := range rows {
			tenant := Stringify(row["tenant_id"])
			n := hierarchy.Node{
				ID: Stringify(row["id"]), ParentID: Stringify(row["parent_id"]), Level: Stringify(row["level"]),
				Code: Stringify(row["code"]), Name: Stringify(row["name"]), Attrs: decodeJSONObject(row["attrs"]),
			}
			nodes[tenant] = replaceNode(nodes[tenant], n)
		}
		rows, err = queryRows(ctx, h.db.DB, fmt.Sprintf(
			"SELECT tenant_id, set_name, code, node_id, workspace, department, label, sort_order, disabled, attrs FROM %s", h.lookupTable), nil)
		if err != nil {
			return fmt.Errorf("load lookups: %w", err)
		}
		for _, row := range rows {
			sortOrder, _ := ToFloat(row["sort_order"])
			disabled, _ := ToFloat(row["disabled"])
			tenant := Stringify(row["tenant_id"])
			lookups[tenant] = append(lookups[tenant], hierarchy.LookupEntry{
				Set: Stringify(row["set_name"]), Code: Stringify(row["code"]), NodeID: Stringify(row["node_id"]),
				Workspace: Stringify(row["workspace"]), Department: Stringify(row["department"]),
				Label: Stringify(row["label"]), Sort: int(sortOrder), Disabled: disabled != 0,
				Attrs: decodeJSONObject(row["attrs"]),
			})
		}
	}
	next := &orgState{byTenant: map[string]*OrgSnapshot{}}
	tenants := map[string]bool{}
	for t := range nodes {
		tenants[t] = true
	}
	for t := range lookups {
		tenants[t] = true
	}
	for tenant := range tenants {
		tree, err := hierarchy.Build(nodes[tenant], h.opts)
		if err != nil {
			return fmt.Errorf("tenant %q: %w", tenant, err)
		}
		lk, err := hierarchy.NewLookups(lookups[tenant])
		if err != nil {
			return fmt.Errorf("tenant %q: %w", tenant, err)
		}
		next.byTenant[tenant] = &OrgSnapshot{Tree: tree, Lookups: lk}
	}
	h.state.Store(next)
	return nil
}

func replaceNode(nodes []hierarchy.Node, n hierarchy.Node) []hierarchy.Node {
	for i := range nodes {
		if nodes[i].ID == n.ID {
			nodes[i] = n
			return nodes
		}
	}
	return append(nodes, n)
}

// Snapshot returns the tree and lookups a tenant sees: its own when it has
// one, otherwise the shared ("") tree.
func (h *OrgHierarchy) Snapshot(tenant string) *OrgSnapshot {
	state := h.state.Load()
	if snap, ok := state.byTenant[tenant]; ok {
		return snap
	}
	return state.byTenant[""]
}

// ScopeFor resolves the principal's organisational scope.
func (h *OrgHierarchy) ScopeFor(principal Principal, tree *hierarchy.Tree) OrgScope {
	for _, role := range principal.Roles {
		if slices.Contains(h.globalRoles, role) {
			return OrgScope{Global: true, Assigned: tree.Roots()}
		}
	}
	var assigned []string
	if principal.Claims != nil {
		assigned = stringList(principal.Claims[h.assignmentClaim])
	}
	return OrgScope{Assigned: tree.NormalizeScope(assigned)}
}

// stringList accepts a string (comma separated), []string or []any.
func stringList(value any) []string {
	var out []string
	switch v := value.(type) {
	case nil:
	case string:
		for _, part := range strings.Split(v, ",") {
			if part = strings.TrimSpace(part); part != "" {
				out = append(out, part)
			}
		}
	case []string:
		for _, s := range v {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
	case []any:
		for _, item := range v {
			if s := strings.TrimSpace(Stringify(item)); s != "" {
				out = append(out, s)
			}
		}
	default:
		if s := strings.TrimSpace(Stringify(v)); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// UpsertNode persists and publishes a unit.
func (h *OrgHierarchy) UpsertNode(ctx context.Context, tenant string, n hierarchy.Node) (*hierarchy.Tree, error) {
	h.writeMu.Lock()
	defer h.writeMu.Unlock()
	snap := h.Snapshot(tenant)
	next, err := snap.Tree.Upsert(n)
	if err != nil {
		return nil, err
	}
	if err := h.persistTree(ctx, tenant, next, nil); err != nil {
		return nil, err
	}
	h.publish(tenant, &OrgSnapshot{Tree: next, Lookups: snap.Lookups})
	return next, nil
}

// RemoveNode persists and publishes a removal, returning the removed ids.
func (h *OrgHierarchy) RemoveNode(ctx context.Context, tenant, id string, cascade bool) ([]string, error) {
	h.writeMu.Lock()
	defer h.writeMu.Unlock()
	snap := h.Snapshot(tenant)
	next, removed, err := snap.Tree.Remove(id, cascade)
	if err != nil {
		return nil, err
	}
	if err := h.persistTree(ctx, tenant, next, removed); err != nil {
		return nil, err
	}
	h.publish(tenant, &OrgSnapshot{Tree: next, Lookups: snap.Lookups})
	return removed, nil
}

// persistTree writes the whole tenant tree. Rewriting every row keeps stored
// materialised paths correct after a move, which changes the path of a whole
// subtree, and the tree is small next to the rows scoped by it.
func (h *OrgHierarchy) persistTree(ctx context.Context, tenant string, tree *hierarchy.Tree, removed []string) error {
	if h.db == nil {
		return nil
	}
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, rebind(h.db.Dialect, fmt.Sprintf("DELETE FROM %s WHERE tenant_id = $1", h.table)), tenant); err != nil {
		return err
	}
	insert := rebind(h.db.Dialect, fmt.Sprintf(
		"INSERT INTO %s (tenant_id, id, parent_id, level, code, name, path, attrs) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)", h.table))
	for _, n := range tree.Nodes() {
		var parent any
		if n.ParentID != "" {
			parent = n.ParentID
		}
		if _, err := tx.ExecContext(ctx, insert, tenant, n.ID, parent, n.Level, n.Code, n.Name,
			tree.MaterializedPath(n.ID), encodeJSONObject(n.Attrs)); err != nil {
			return err
		}
	}
	if len(removed) > 0 {
		del := rebind(h.db.Dialect, fmt.Sprintf("DELETE FROM %s WHERE tenant_id = $1 AND node_id = $2", h.lookupTable))
		for _, id := range removed {
			if _, err := tx.ExecContext(ctx, del, tenant, id); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

// UpsertLookup persists and publishes a lookup entry.
func (h *OrgHierarchy) UpsertLookup(ctx context.Context, tenant string, e hierarchy.LookupEntry) error {
	h.writeMu.Lock()
	defer h.writeMu.Unlock()
	snap := h.Snapshot(tenant)
	next, err := snap.Lookups.Upsert(e)
	if err != nil {
		return err
	}
	if h.db != nil {
		if err := h.writeLookup(ctx, tenant, e, false); err != nil {
			return err
		}
	}
	h.publish(tenant, &OrgSnapshot{Tree: snap.Tree, Lookups: next})
	return nil
}

// DeleteLookup removes one lookup definition, reporting whether it existed.
func (h *OrgHierarchy) DeleteLookup(ctx context.Context, tenant string, e hierarchy.LookupEntry) (bool, error) {
	h.writeMu.Lock()
	defer h.writeMu.Unlock()
	snap := h.Snapshot(tenant)
	next, found := snap.Lookups.Delete(e.Key())
	if !found {
		return false, nil
	}
	if h.db != nil {
		if err := h.writeLookup(ctx, tenant, e, true); err != nil {
			return false, err
		}
	}
	h.publish(tenant, &OrgSnapshot{Tree: snap.Tree, Lookups: next})
	return true, nil
}

func (h *OrgHierarchy) writeLookup(ctx context.Context, tenant string, e hierarchy.LookupEntry, deleteOnly bool) error {
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	del := rebind(h.db.Dialect, fmt.Sprintf(`DELETE FROM %s WHERE tenant_id = $1 AND set_name = $2 AND code = $3
		AND node_id = $4 AND workspace = $5 AND department = $6`, h.lookupTable))
	if _, err := tx.ExecContext(ctx, del, tenant, e.Set, e.Code, e.NodeID, e.Workspace, e.Department); err != nil {
		return err
	}
	if !deleteOnly {
		disabled := 0
		if e.Disabled {
			disabled = 1
		}
		insert := rebind(h.db.Dialect, fmt.Sprintf(`INSERT INTO %s
			(tenant_id, set_name, code, node_id, workspace, department, label, sort_order, disabled, attrs)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`, h.lookupTable))
		if _, err := tx.ExecContext(ctx, insert, tenant, e.Set, e.Code, e.NodeID, e.Workspace, e.Department,
			e.Label, e.Sort, disabled, encodeJSONObject(e.Attrs)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (h *OrgHierarchy) publish(tenant string, snap *OrgSnapshot) {
	current := h.state.Load()
	next := &orgState{byTenant: make(map[string]*OrgSnapshot, len(current.byTenant)+1)}
	for k, v := range current.byTenant {
		next.byTenant[k] = v
	}
	next.byTenant[tenant] = snap
	h.state.Store(next)
}

func orgNodeFromMap(m map[string]any) (hierarchy.Node, error) {
	n := hierarchy.Node{
		ID:       strings.TrimSpace(Stringify(m["id"])),
		ParentID: strings.TrimSpace(firstString(m, "parent_id", "parent")),
		Level:    strings.TrimSpace(Stringify(m["level"])),
		Code:     Stringify(m["code"]),
		Name:     Stringify(m["name"]),
	}
	if attrs, ok := m["attrs"].(map[string]any); ok {
		n.Attrs = attrs
	}
	if n.ID == "" {
		return n, fmt.Errorf("a unit needs an id")
	}
	return n, nil
}

func lookupEntryFromMap(m map[string]any) (hierarchy.LookupEntry, error) {
	sortOrder, _ := ToFloat(firstValue(m, "sort", "sort_order"))
	e := hierarchy.LookupEntry{
		Set:        strings.TrimSpace(firstString(m, "set", "set_name")),
		Code:       strings.TrimSpace(Stringify(m["code"])),
		Label:      Stringify(m["label"]),
		NodeID:     strings.TrimSpace(firstString(m, "node_id", "node")),
		Workspace:  strings.TrimSpace(Stringify(m["workspace"])),
		Department: strings.TrimSpace(Stringify(m["department"])),
		Sort:       int(sortOrder),
		Disabled:   truthy(m["disabled"]),
	}
	if attrs, ok := m["attrs"].(map[string]any); ok {
		e.Attrs = attrs
	}
	if e.Set == "" || e.Code == "" {
		return e, fmt.Errorf("a lookup entry needs a set and a code")
	}
	return e, nil
}

func firstValue(m map[string]any, keys ...string) any {
	for _, k := range keys {
		if v, ok := m[k]; ok && v != nil {
			return v
		}
	}
	return nil
}

func firstString(m map[string]any, keys ...string) string {
	if v := firstValue(m, keys...); v != nil {
		return Stringify(v)
	}
	return ""
}

func truthy(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return strings.EqualFold(t, "true") || t == "1" || strings.EqualFold(t, "yes")
	default:
		f, ok := ToFloat(v)
		return ok && f != 0
	}
}

func encodeJSONObject(m map[string]any) any {
	if len(m) == 0 {
		return nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return nil
	}
	return string(b)
}

func decodeJSONObject(v any) map[string]any {
	var raw []byte
	switch t := v.(type) {
	case string:
		raw = []byte(t)
	case []byte:
		raw = t
	case map[string]any:
		return t
	default:
		return nil
	}
	if len(raw) == 0 {
		return nil
	}
	var out map[string]any
	if json.Unmarshal(raw, &out) != nil {
		return nil
	}
	return out
}
