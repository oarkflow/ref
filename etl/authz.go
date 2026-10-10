package etl

import (
	"slices"
	"sort"
	"time"
)

// Permissions. A role is a set of these, optionally limited to some sources.
const (
	PermIngest  = "ingest"         // send data in
	PermRead    = "read"           // see batches, sources, quarantine, lineage
	PermAdvance = "advance"        // run a batch's next stage by hand
	PermReplay  = "replay"         // replay held batches
	PermManage  = "sources.manage" // create, edit and pause sources
	PermAudit   = "audit.read"     // read and verify the audit trail
	PermMonitor = "monitor.read"   // monitoring, alerts, logs, health detail
	PermUsers   = "users.manage"   // users, API keys (checked by the application)
	PermRoles   = "roles.manage"   // roles and what they allow
	PermOps     = "ops.manage"     // operational switches such as taking a destination offline
)

// PermissionInfo describes one permission for the roles screen.
type PermissionInfo struct {
	ID      string `json:"id"`
	Group   string `json:"group"`
	Summary string `json:"summary"`
	// Scoped permissions can be limited to some sources; the rest apply everywhere.
	Scoped bool `json:"scoped"`
}

// Permissions is the catalogue.
var Permissions = []PermissionInfo{
	{PermIngest, "Data", "Send data into a source", true},
	{PermRead, "Data", "See batches, quarantined rows and lineage", true},
	{PermAdvance, "Data", "Run a batch's next stage by hand", true},
	{PermReplay, "Data", "Replay held batches from their checkpoint", true},
	{PermManage, "Sources", "Create, edit and pause sources", true},
	{PermMonitor, "Operations", "See monitoring, alerts, logs and health", true},
	{PermOps, "Operations", "Take receiving systems offline and online", false},
	{PermAudit, "Governance", "Read and verify the audit trail", true},
	{PermUsers, "Governance", "Manage users, service accounts and API keys", false},
	{PermRoles, "Governance", "Manage roles and permissions", false},
}

// Role is a named set of permissions. Sources limits where the data
// permissions apply (empty or "*": every source).
type Role struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Permissions []string  `json:"permissions"`
	Sources     []string  `json:"sources"`
	System      bool      `json:"system"` // built in: cannot be deleted
	UpdatedAt   time.Time `json:"updated_at"`
}

// RoleAdmin always holds every permission on every source, whatever is stored.
const RoleAdmin = "admin"

func allPermissions() []string {
	out := make([]string, len(Permissions))
	for i, p := range Permissions {
		out[i] = p.ID
	}
	return out
}

// BuiltinRoles are created on first start and can be edited, but not deleted.
func BuiltinRoles() []Role {
	return []Role{
		{ID: RoleAdmin, Name: "Administrator", Description: "Everything, on every source", Permissions: allPermissions(), System: true},
		{ID: "operate", Name: "Operator", Description: "Runs the pipeline day to day", System: true,
			Permissions: []string{PermRead, PermAdvance, PermReplay, PermManage, PermMonitor, PermOps}},
		{ID: "ingest", Name: "Data feeder", Description: "Sends data in and nothing else", System: true, Permissions: []string{PermIngest}},
		{ID: "replay", Name: "Replayer", Description: "Reads and replays held batches", System: true, Permissions: []string{PermRead, PermReplay}},
		{ID: "monitor", Name: "Monitor", Description: "Reads monitoring, health and metrics; for dashboards and scrapers", System: true,
			Permissions: []string{PermMonitor}},
		{ID: "auditor", Name: "Auditor", Description: "Reads data and the audit trail", System: true, Permissions: []string{PermRead, PermAudit}},
		{ID: "read", Name: "Analyst", Description: "Reads everything, including the audit trail and monitoring", System: true,
			Permissions: []string{PermRead, PermMonitor, PermAudit}},
	}
}

// Actor is who is asking. Roles are role ids.
type Actor struct {
	ID    string
	Roles []string
}

// Access is what an actor may do, resolved from their roles.
type Access struct {
	perms map[string]*scope
}

type scope struct {
	all     bool
	sources map[string]bool
}

func newAccess(roles []*Role, ids []string) *Access {
	a := &Access{perms: map[string]*scope{}}
	for _, id := range ids {
		for _, r := range roles {
			if r.ID != id {
				continue
			}
			perms := r.Permissions
			everywhere := len(r.Sources) == 0 || slices.Contains(r.Sources, "*")
			if r.ID == RoleAdmin {
				perms, everywhere = allPermissions(), true
			}
			for _, p := range perms {
				s := a.perms[p]
				if s == nil {
					s = &scope{sources: map[string]bool{}}
					a.perms[p] = s
				}
				if everywhere {
					s.all = true
				}
				for _, src := range r.Sources {
					s.sources[src] = true
				}
			}
		}
	}
	return a
}

// Has reports whether the permission is held at all, on any source.
func (a *Access) Has(perm string) bool { return a != nil && a.perms[perm] != nil }

// Allows reports whether the permission is held for a source ("" asks about
// the permission with no source in play, which any holder passes).
func (a *Access) Allows(perm, source string) bool {
	s := a.perms[perm]
	if s == nil {
		return false
	}
	return s.all || source == "" || s.sources[source]
}

// Sources returns the sources a permission is limited to; nil means all of them.
// A permission that is not held returns an empty, non-nil list.
func (a *Access) Sources(perm string) []string {
	s := a.perms[perm]
	if s == nil {
		return []string{}
	}
	if s.all {
		return nil
	}
	out := make([]string, 0, len(s.sources))
	for id := range s.sources {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// Summary lists the permissions held with their source limits, for display.
func (a *Access) Summary() map[string][]string {
	out := map[string][]string{}
	for p := range a.perms {
		out[p] = a.Sources(p)
	}
	return out
}
