package platform

import (
	"context"
	"fmt"
	"log/slog"
	"path"
	"slices"
	"strings"
)

// Data residency.
//
// Every database, storage or outbound service resource may declare the
// region its data lives in (or, for a service, is sent to):
//
//	resource "db_eu" { kind "database.sql"  region "eu-west-1"  config { ... } }
//
// A resource without a region inherits the region of the database (or store)
// it names, so a pipeline.cases resource on db_eu is in eu-west-1 too.
//
// A `residency` block then says where data may live, and is enforced twice:
//
//	residency {
//	  claim "data_region"            # principal claim naming the tenant's home
//	  audit "db_eu"                  # hash-chained audit table for refusals
//	  zone "eu" {
//	    regions ["eu-west-1", "eu-central-1"]
//	    hosts   ["*.eu.example.com"]  # outbound hosts for tenants homed here
//	  }
//	  policy "pii" {
//	    regions   ["eu"]              # zone names or region globs
//	    entities  ["customer"]
//	    pipelines ["kyc"]
//	    intents   ["customer.sync"]
//	    hosts     ["crm.eu.example.com"]
//	  }
//	}
//
// At compile time, an entity, pipeline, resource or intent a policy covers
// must be bound only to resources in the policy's regions — a customer entity
// on a US database is a deployment that refuses to start. The hosts of a
// policy restrict the service.http resources its intents call, both
// statically (their allowed_hosts) and on every request.
//
// At run time, the tenant's home region — the tenant block's `region`, else
// the principal claim named by `claim` — limits where that tenant's writes
// may land: an effect node on a resource in another region, or any call to an
// outbound service in another region, fails with 403 PERMISSION_DENIED, a deny
// decision in the plan, a structured warning log and, when `audit` is set, a
// hash-chained audit entry. A zone's hosts further limit which hosts that
// tenant's outbound HTTP calls may reach.

// ResidencySpec is the `residency` block.
type ResidencySpec struct {
	// Claim is the principal claim path holding a tenant's home region or
	// zone, used when the tenant block does not set one.
	Claim string `bcl:"claim"`
	// RequireRegion refuses a regioned tenant's writes to a resource that
	// declares no region, instead of letting them through.
	RequireRegion bool `bcl:"require_region"`
	// Audit names a database.sql resource whose audit table (AuditTable,
	// default platform_audit) records every refusal.
	Audit      string `bcl:"audit"`
	AuditTable string `bcl:"audit_table"`

	Zones    []ResidencyZone   `bcl:"zone,block"`
	Policies []ResidencyPolicy `bcl:"policy,block"`
}

// ResidencyZone names a group of regions — "eu" for eu-west-1 and
// eu-central-1 — and the outbound hosts tenants homed in it may reach.
type ResidencyZone struct {
	Name    string   `bcl:",id"`
	Regions []string `bcl:"regions"`
	Hosts   []string `bcl:"hosts"`
}

// ResidencyPolicy pins what it covers to its regions.
type ResidencyPolicy struct {
	Name        string   `bcl:",id"`
	Description string   `bcl:"description"`
	Regions     []string `bcl:"regions"`
	Entities    []string `bcl:"entities"`
	Pipelines   []string `bcl:"pipelines"`
	Resources   []string `bcl:"resources"`
	Intents     []string `bcl:"intents"`
	// Hosts are the outbound destinations the policy's intents may call.
	Hosts []string `bcl:"hosts"`
}

// regionMatch reports whether a region matches any allowed pattern. Patterns
// are exact names or path.Match globs ("eu-*").
func regionMatch(region string, allowed []string) bool {
	region = strings.ToLower(strings.TrimSpace(region))
	for _, pattern := range allowed {
		pattern = strings.ToLower(strings.TrimSpace(pattern))
		if pattern == region {
			return true
		}
		if ok, _ := path.Match(pattern, region); ok {
			return true
		}
	}
	return false
}

// hostMatch reports whether a host matches any allowed pattern: an exact
// name, or "*.example.com" for any subdomain.
func hostMatch(host string, allowed []string) bool {
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	for _, pattern := range allowed {
		pattern = strings.ToLower(strings.TrimSpace(pattern))
		if pattern == host || pattern == "*" {
			return true
		}
		if suffix, ok := strings.CutPrefix(pattern, "*."); ok && strings.HasSuffix(host, "."+suffix) {
			return true
		}
	}
	return false
}

// expandRegions replaces zone names with their regions.
func expandRegions(names []string, zones map[string]ResidencyZone) []string {
	var out []string
	for _, n := range names {
		if z, ok := zones[strings.ToLower(n)]; ok {
			out = append(out, z.Regions...)
		} else {
			out = append(out, n)
		}
	}
	return out
}

// effectiveRegions resolves every resource's region, inheriting through
// config.database and config.store.
func effectiveRegions(resources []ResourceSpec) map[string]string {
	byName := make(map[string]ResourceSpec, len(resources))
	for _, r := range resources {
		byName[r.Name] = r
	}
	out := make(map[string]string, len(resources))
	var resolve func(name string, depth int) string
	resolve = func(name string, depth int) string {
		r, ok := byName[name]
		if !ok || depth > len(resources) {
			return ""
		}
		if r.Region != "" {
			return strings.ToLower(strings.TrimSpace(r.Region))
		}
		for _, key := range []string{"database", "store"} {
			if dep := configString(r.Config, key, ""); dep != "" && dep != name {
				if region := resolve(dep, depth+1); region != "" {
					return region
				}
			}
		}
		return ""
	}
	for _, r := range resources {
		out[r.Name] = resolve(r.Name, 0)
	}
	return out
}

// residentKind reports whether a resource kind holds or sends application
// data, and so must declare a region when a policy covers it.
func residentKind(kind string) bool {
	for _, prefix := range []string{"database.", "storage.", "service.", "pipeline.", "queue.", "search."} {
		if strings.HasPrefix(kind, prefix) {
			return true
		}
	}
	return false
}

// validateResidency is the compile-time half: every binding a policy covers
// must be in one of its regions.
func validateResidency(doc Document, resources map[string]bool, intents map[string]bool) error {
	spec := doc.Residency
	for _, t := range doc.Tenants {
		if t.Region != "" && spec == nil {
			return fmt.Errorf("ref/platform: tenant %q declares region %q, but the document has no residency block to enforce it", t.Name, t.Region)
		}
	}
	for _, r := range doc.Resources {
		if r.Region != "" && strings.ContainsAny(r.Region, "*?[ ") {
			return fmt.Errorf("ref/platform: resource %q: region %q must be a plain name", r.Name, r.Region)
		}
	}
	if spec == nil {
		return nil
	}
	zones := map[string]ResidencyZone{}
	for _, z := range spec.Zones {
		key := strings.ToLower(z.Name)
		if key == "" || zones[key].Name != "" {
			return fmt.Errorf("ref/platform: residency zone %q is unnamed or declared twice", z.Name)
		}
		if len(z.Regions) == 0 {
			return fmt.Errorf("ref/platform: residency zone %q needs regions", z.Name)
		}
		zones[key] = z
	}
	if spec.Audit != "" && !resources[spec.Audit] {
		return fmt.Errorf("ref/platform: residency audit names undeclared resource %q", spec.Audit)
	}
	if spec.AuditTable != "" {
		if _, err := safeIdentifier(spec.AuditTable); err != nil {
			return fmt.Errorf("ref/platform: residency audit_table: %w", err)
		}
	}

	regions := effectiveRegions(doc.Resources)
	kinds := map[string]ResourceSpec{}
	for _, r := range doc.Resources {
		kinds[r.Name] = r
	}
	entities := map[string]EntitySpec{}
	for _, e := range doc.Entities {
		entities[e.Name] = e
	}
	pipelines := map[string]bool{}
	for _, p := range doc.Pipelines {
		pipelines[p.Name] = true
	}
	intentSpecs := map[string]IntentSpec{}
	for _, it := range doc.Intents {
		intentSpecs[it.Name] = it
	}

	seen := map[string]bool{}
	for _, policy := range spec.Policies {
		where := fmt.Sprintf("residency policy %q", policy.Name)
		if policy.Name == "" || seen[policy.Name] {
			return fmt.Errorf("ref/platform: %s is unnamed or declared twice", where)
		}
		seen[policy.Name] = true
		if len(policy.Regions) == 0 {
			return fmt.Errorf("ref/platform: %s needs regions", where)
		}
		allowed := expandRegions(policy.Regions, zones)
		// bound checks one binding of a covered item to a resource.
		bound := func(what, resource string) error {
			region := regions[resource]
			if region == "" {
				return fmt.Errorf("ref/platform: %s covers %s, which is bound to resource %q — that resource declares no region, so residency cannot be proven. Give it a region",
					where, what, resource)
			}
			if !regionMatch(region, allowed) {
				return fmt.Errorf("ref/platform: %s covers %s, which is bound to resource %q in region %q — outside the allowed regions %s",
					where, what, resource, region, strings.Join(allowed, ", "))
			}
			return nil
		}
		for _, name := range policy.Entities {
			e, ok := entities[name]
			if !ok {
				return fmt.Errorf("ref/platform: %s names undeclared entity %q", where, name)
			}
			if err := bound(fmt.Sprintf("entity %q", name), e.Database); err != nil {
				return err
			}
		}
		for _, name := range policy.Pipelines {
			if !pipelines[name] {
				return fmt.Errorf("ref/platform: %s names undeclared pipeline %q", where, name)
			}
			runners := 0
			for _, r := range doc.Resources {
				if r.Kind != "pipeline.cases" {
					continue
				}
				if names := configStrings(r.Config, "pipelines"); len(names) > 0 && !slices.Contains(names, name) {
					continue
				}
				runners++
				if err := bound(fmt.Sprintf("pipeline %q", name), r.Name); err != nil {
					return err
				}
			}
			if runners == 0 {
				return fmt.Errorf("ref/platform: %s names pipeline %q, but no pipeline.cases resource runs it", where, name)
			}
		}
		for _, name := range policy.Resources {
			if !resources[name] {
				return fmt.Errorf("ref/platform: %s names undeclared resource %q", where, name)
			}
			if err := bound(fmt.Sprintf("resource %q", name), name); err != nil {
				return err
			}
		}
		for _, name := range policy.Intents {
			it, ok := intentSpecs[name]
			if !ok || !intents[name] {
				return fmt.Errorf("ref/platform: %s names undeclared intent %q", where, name)
			}
			for _, node := range it.Nodes {
				if node.Resource == "" {
					continue
				}
				r := kinds[node.Resource]
				if regions[node.Resource] == "" && !residentKind(r.Kind) {
					continue // an authenticator or a lock holds no application data
				}
				if err := bound(fmt.Sprintf("intent %q (node %q)", name, node.Name), node.Resource); err != nil {
					return err
				}
				if len(policy.Hosts) > 0 && r.Kind == "service.http" {
					for _, host := range configStrings(r.Config, "allowed_hosts") {
						if !hostMatch(host, policy.Hosts) {
							return fmt.Errorf("ref/platform: %s covers intent %q, whose node %q may call host %q through resource %q — not among the policy's hosts %s",
								where, name, node.Name, host, node.Resource, strings.Join(policy.Hosts, ", "))
						}
					}
				}
			}
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Run time
// ---------------------------------------------------------------------------

// residencyPlan is the compiled run-time half.
type residencyPlan struct {
	spec    ResidencySpec
	claim   []string
	zones   map[string]ResidencyZone
	regions map[string]string // resource → effective region
	kinds   map[string]string // resource → kind
	tenants map[string]string // tenant → home region or zone
	// intentHosts holds each covered intent's allowed outbound hosts.
	intentHosts map[string][][]string
	audit       *auditLog
}

func compileResidency(doc Document, resources map[string]Resource) (*residencyPlan, error) {
	if doc.Residency == nil {
		return nil, nil
	}
	spec := *doc.Residency
	plan := &residencyPlan{
		spec:        spec,
		zones:       map[string]ResidencyZone{},
		regions:     effectiveRegions(doc.Resources),
		kinds:       map[string]string{},
		tenants:     map[string]string{},
		intentHosts: map[string][][]string{},
	}
	if spec.Claim != "" {
		plan.claim = strings.Split(spec.Claim, ".")
	}
	for _, z := range spec.Zones {
		plan.zones[strings.ToLower(z.Name)] = z
	}
	for _, r := range doc.Resources {
		plan.kinds[r.Name] = r.Kind
	}
	for _, t := range doc.Tenants {
		if t.Region != "" {
			plan.tenants[t.Name] = t.Region
		}
	}
	for _, policy := range spec.Policies {
		if len(policy.Hosts) == 0 {
			continue
		}
		for _, name := range policy.Intents {
			plan.intentHosts[name] = append(plan.intentHosts[name], policy.Hosts)
		}
	}
	if spec.Audit != "" {
		db, ok := resources[spec.Audit].(*Database)
		if !ok {
			return nil, fmt.Errorf("ref/platform: residency audit resource %q is not a database.sql resource", spec.Audit)
		}
		plan.audit = sharedAuditLog(db, orDefault(spec.AuditTable, "platform_audit"))
	}
	return plan, nil
}

// residencyGuard is one node's compiled check.
type residencyGuard struct {
	plan     *residencyPlan
	intent   string
	node     string
	resource string
	region   string
	write    bool
	egress   bool
	hosts    [][]string
}

// guardFor returns the check for one node, or nil when there is nothing to
// check.
func (r *residencyPlan) guardFor(intentName string, node NodeSpec, registry *Registry) *residencyGuard {
	if r == nil {
		return nil
	}
	g := &residencyGuard{plan: r, intent: intentName, node: node.Name, hosts: r.intentHosts[intentName]}
	if node.Resource != "" {
		g.resource = node.Resource
		g.region = r.regions[node.Resource]
		g.egress = strings.HasPrefix(r.kinds[node.Resource], "service.")
		kind := strings.ToLower(strings.TrimSpace(node.Kind))
		if kind == "" && registry != nil {
			registry.mu.RLock()
			kind = registry.actionDoc[node.Uses].Kind
			registry.mu.RUnlock()
		}
		g.write = kind == "effect" || kind == "async_effect"
	}
	if !g.write && !g.egress && len(g.hosts) == 0 {
		return nil
	}
	return g
}

// tenantRegion resolves the allowed regions and outbound hosts for the
// caller's tenant. An empty home means the tenant is not region-bound.
func (r *residencyPlan) tenantRegion(principal Principal, tenant string) (home string, regions, hosts []string) {
	home = r.tenants[tenant]
	if home == "" && len(r.claim) > 0 && principal.Claims != nil {
		if value, ok := lookupPath(principal.Claims, r.claim); ok {
			home = Stringify(value)
		}
	}
	home = strings.ToLower(strings.TrimSpace(home))
	if home == "" {
		return "", nil, nil
	}
	if z, ok := r.zones[home]; ok {
		return home, z.Regions, z.Hosts
	}
	return home, []string{home}, nil
}

// check refuses the node when the tenant's residency forbids it, and
// otherwise returns the context carrying the outbound host constraints.
func (g *residencyGuard) check(ctx *ActionContext) (context.Context, error) {
	home, allowed, zoneHosts := g.plan.tenantRegion(ctx.Principal, ctx.TenantID)
	if home != "" && (g.write || g.egress) {
		switch {
		case g.region == "" && g.plan.spec.RequireRegion:
			return nil, g.refuse(ctx, home, fmt.Sprintf("data residency: tenant region %q requires every write to go to a resource with a declared region, and %q has none", home, g.resource))
		case g.region != "" && !regionMatch(g.region, allowed):
			verb := "write to"
			if g.egress && !g.write {
				verb = "send data to"
			}
			return nil, g.refuse(ctx, home, fmt.Sprintf("data residency: tenant region %q may not %s %q in region %q", home, verb, g.resource, g.region))
		}
	}
	hosts := g.hosts
	if len(zoneHosts) > 0 {
		hosts = append(slices.Clone(hosts), zoneHosts)
	}
	if len(hosts) == 0 {
		return ctx.Context, nil
	}
	return context.WithValue(ctx.Context, egressKey{}, &egressPolicy{hosts: hosts, guard: g, action: ctx, home: home}), nil
}

// refuse records and reports one refusal.
func (g *residencyGuard) refuse(ctx *ActionContext, home, message string) error {
	slog.Warn("data residency refused an operation",
		"audit", true, "intent", g.intent, "node", g.node, "resource", g.resource,
		"resource_region", g.region, "tenant", ctx.TenantID, "tenant_region", home, "principal", ctx.Principal.ID)
	if ctx.Node != nil {
		ctx.Node.Decisions().RecordDeny("residency:"+g.node, message)
	}
	if g.plan.audit != nil {
		if err := g.plan.audit.migrate(ctx.Context); err == nil {
			_, err = g.plan.audit.append(ctx.Context, auditEntry{
				ID: newPrefixedID("aud"), Stream: ctx.TenantID, RecordedAt: ctx.Now,
				ActorID: ctx.Principal.ID, ActorName: ctx.Principal.Username, TenantID: ctx.TenantID,
				Action: "residency.denied", Subject: g.intent + "/" + g.node, Outcome: "denied", Detail: message,
			})
			if err != nil {
				slog.Error("data residency audit entry failed", "error", err)
			}
		} else {
			slog.Error("data residency audit table unavailable", "error", err)
		}
	}
	return permissionDenied(message)
}

type egressKey struct{}

// egressPolicy travels in the request context to the HTTP client, which is
// where the final destination host is known.
type egressPolicy struct {
	hosts  [][]string
	guard  *residencyGuard
	action *ActionContext
	home   string
}

// checkEgress enforces the context's outbound host constraints: the host must
// satisfy every list (the intent's policies and the tenant's zone).
func checkEgress(ctx context.Context, host string) error {
	policy, ok := ctx.Value(egressKey{}).(*egressPolicy)
	if !ok || policy == nil {
		return nil
	}
	for _, allowed := range policy.hosts {
		if !hostMatch(host, allowed) {
			return policy.guard.refuse(policy.action, policy.home,
				fmt.Sprintf("data residency: outbound host %q is not among the allowed destinations %s", host, strings.Join(allowed, ", ")))
		}
	}
	return nil
}

// residencyCheck runs a node's residency guard, if it has one, and scopes
// the action's context to the outbound host constraints.
func (p *Platform) residencyCheck(guard *residencyGuard, ctx *ActionContext) error {
	if guard == nil {
		return nil
	}
	scoped, err := guard.check(ctx)
	if err != nil {
		return err
	}
	ctx.Context = scoped
	return nil
}
