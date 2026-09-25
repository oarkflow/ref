package platform

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/oarkflow/ref/hierarchy"
	"github.com/oarkflow/ref/pipeline"
)

// PipelineCases is the pipeline.cases resource: it runs the document's
// `pipeline` blocks (multi-stage data verification workflows such as a
// passport request) and persists their cases.
//
// A case moves through stages, each with its own page of form groups, its own
// people and roles, its own nodes (reviews, approvals, checks, automations,
// certificates) and its own status. The rules live in the pipeline package;
// this resource wires them to the platform: storage (memory or database.sql),
// the expression language, intents as automation hooks, org.hierarchy lookups
// as field options, jurisdiction-scoped work queues, and signed certificates.
type PipelineCases struct {
	name           string
	store          pipeline.Store
	engines        map[string]*pipeline.Engine
	order          []string
	org            *OrgHierarchy
	allowAnonymous bool
}

// pipelineDefinitionsKey is the config key the compiler injects the
// document's pipeline blocks under. It is not something an author writes.
const pipelineDefinitionsKey = "__pipelines"

func registerPipelineResources(r *Registry) {
	mustResource(r, "pipeline.cases", ResourceFactoryFunc(openPipelineCases), ResourceKindInfo{
		Family:   "workflow",
		Summary:  "Multi-stage data verification pipelines (application → review → approval → certificate) and their cases",
		Provides: []string{"PipelineCases"},
		Config: []ConfigField{
			{Name: "pipelines", Type: "[]string", Summary: "Pipeline blocks this resource runs (default: every pipeline in the document)"},
			{Name: "database", Type: "string", Summary: "database.sql resource that stores cases and certificates; omit for in-memory storage"},
			{Name: "table_prefix", Type: "string", Default: "pipeline_"},
			{Name: "migrate", Type: "bool", Default: "true", Summary: "Create the tables at startup"},
			{Name: "signing_secret", Type: "string", Summary: "HMAC key that signs issued certificates (use env.required(...)); without it certificates carry a content hash only"},
			{Name: "org_resource", Type: "string", Summary: "org.hierarchy resource: resolves `lookup` inputs and scopes work queues to the caller's jurisdiction"},
			{Name: "allow_anonymous", Type: "bool", Default: "false", Summary: "Let anonymous callers start public pipelines; they get an access key to return to their case"},
		},
	})
}

func openPipelineCases(ctx context.Context, spec ResourceSpec) (Resource, io.Closer, error) {
	if err := rejectUnknownConfig("pipeline.cases", spec.Config,
		"pipelines", "database", "table_prefix", "migrate", "signing_secret", "org_resource", "allow_anonymous",
		pipelineDefinitionsKey); err != nil {
		return nil, nil, err
	}
	defs, _ := spec.Config[pipelineDefinitionsKey].([]pipeline.Definition)
	byName := make(map[string]pipeline.Definition, len(defs))
	for _, d := range defs {
		byName[d.Name] = d
	}
	names := configStrings(spec.Config, "pipelines")
	if len(names) == 0 {
		for _, d := range defs {
			names = append(names, d.Name)
		}
	}
	if len(names) == 0 {
		return nil, nil, fmt.Errorf("pipeline.cases %q: the document declares no pipeline blocks", spec.Name)
	}
	p := &PipelineCases{
		name:           spec.Name,
		engines:        make(map[string]*pipeline.Engine, len(names)),
		allowAnonymous: configBool(spec.Config, "allow_anonymous", false),
	}
	if orgName := configString(spec.Config, "org_resource", ""); orgName != "" {
		org, ok := spec.resolved[orgName].(*OrgHierarchy)
		if !ok {
			return nil, nil, fmt.Errorf("pipeline.cases %q: org_resource %q is not an org.hierarchy resource", spec.Name, orgName)
		}
		p.org = org
	}
	key := []byte(configString(spec.Config, "signing_secret", ""))
	for _, name := range names {
		def, ok := byName[name]
		if !ok {
			return nil, nil, fmt.Errorf("pipeline.cases %q: unknown pipeline %q", spec.Name, name)
		}
		compiled, err := pipeline.Compile(&def)
		if err != nil {
			return nil, nil, fmt.Errorf("pipeline.cases %q: %w", spec.Name, err)
		}
		// Every expression compiles now, so a typo stops the deployment rather
		// than the one request that reaches it.
		where := make([]string, 0)
		for w := range compiled.Expressions() {
			where = append(where, w)
		}
		sort.Strings(where)
		for _, w := range where {
			if _, err := CompileExpr(compiled.Expressions()[w]); err != nil {
				return nil, nil, fmt.Errorf("pipeline.cases %q: pipeline %q: %s: %w", spec.Name, name, w, err)
			}
		}
		engine := pipeline.NewEngine(compiled)
		engine.Eval = &pipelineEvaluator{}
		engine.Automation = pipelineAutomation{}
		engine.StageHook = pipelineStageHook
		engine.SigningKey = key
		if p.org != nil {
			engine.Lookup = p.lookup
		}
		p.engines[name] = engine
		p.order = append(p.order, name)
	}

	if configString(spec.Config, "database", "") == "" {
		p.store = pipeline.NewMemoryStore()
		return p, nil, nil
	}
	db, err := requireSQLHandle(spec, "database")
	if err != nil {
		return nil, nil, err
	}
	store, err := pipeline.NewSQLStore(db.DB, db.Dialect, configString(spec.Config, "table_prefix", "pipeline_"))
	if err != nil {
		return nil, nil, fmt.Errorf("pipeline.cases %q: %w", spec.Name, err)
	}
	if configBool(spec.Config, "migrate", true) {
		if err := store.Migrate(ctx); err != nil {
			return nil, nil, fmt.Errorf("pipeline.cases %q: migrate: %w", spec.Name, err)
		}
	}
	p.store = store
	return p, nil, nil
}

// Engine returns the engine for a pipeline ("" = the only/first one).
func (p *PipelineCases) Engine(name string) (*pipeline.Engine, bool) {
	if name == "" && len(p.order) > 0 {
		name = p.order[0]
	}
	e, ok := p.engines[name]
	return e, ok
}

// Store exposes the case store, for hosts that report on cases directly.
func (p *PipelineCases) Store() pipeline.Store { return p.store }

// Pipelines lists the pipelines this resource runs.
func (p *PipelineCases) Pipelines() []string { return slices.Clone(p.order) }

// lookup resolves an input's lookup set against the case's org unit.
func (p *PipelineCases) lookup(set string, c *pipeline.Case) []pipeline.Option {
	snap := p.org.Snapshot(c.TenantID)
	items := snap.Lookups.Resolve(snap.Tree, hierarchy.LookupQuery{Set: set, NodeID: c.OrgUnit})
	out := make([]pipeline.Option, len(items))
	for i, it := range items {
		out[i] = pipeline.Option{Value: it.Code, Label: orDefaultString(it.Label, it.Code)}
	}
	return out
}

// jurisdiction returns the org units a principal's queue covers, or nil for
// "no restriction" (no org resource, or a global role).
func (p *PipelineCases) jurisdiction(tenant string, principal Principal) ([]string, bool) {
	if p.org == nil {
		return nil, true
	}
	snap := p.org.Snapshot(tenant)
	scope := p.org.ScopeFor(principal, snap.Tree)
	if scope.Global {
		return nil, true
	}
	if len(scope.Assigned) == 0 {
		return nil, false
	}
	return snap.Tree.ScopeIDs(scope.Assigned), true
}

// inScope reports whether a case's org unit is inside the principal's
// jurisdiction. Cases without a unit, and deployments without an org
// resource, are unrestricted.
func (p *PipelineCases) inScope(tenant string, principal Principal, c *pipeline.Case) bool {
	if p.org == nil || c.OrgUnit == "" {
		return true
	}
	snap := p.org.Snapshot(tenant)
	return p.org.ScopeFor(principal, snap.Tree).Covers(snap.Tree, c.OrgUnit)
}

func hashAccessKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

func accessKeyMatches(c *pipeline.Case, key string) bool {
	if c.AccessKeyHash == "" || key == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(c.AccessKeyHash), []byte(hashAccessKey(key))) == 1
}

func orDefaultString(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

// ---------------------------------------------------------------------------
// Adapters: expressions, automation and stage hooks
// ---------------------------------------------------------------------------

// pipelineEvaluator runs pipeline expressions in the platform's expression
// language, compiling each distinct expression once.
type pipelineEvaluator struct {
	cache sync.Map // string -> *Expression
}

func (ev *pipelineEvaluator) Eval(expr string, env map[string]any) (any, error) {
	compiled, ok := ev.cache.Load(expr)
	if !ok {
		c, err := CompileExpr(expr)
		if err != nil {
			return nil, err
		}
		compiled, _ = ev.cache.LoadOrStore(expr, c)
	}
	return compiled.(*Expression).Eval(Env(env))
}

// pipelineCallKey carries the running action into engine hooks, so an
// automation can invoke an intent as part of the same request.
type pipelineCallKey struct{}

func pipelineCaller(ctx context.Context) (*ActionContext, bool) {
	ac, ok := ctx.Value(pipelineCallKey{}).(*ActionContext)
	return ac, ok && ac != nil && ac.Platform != nil
}

// hookInput is what an automation or stage hook intent receives.
func hookInput(c *pipeline.Case, stage, node string) map[string]any {
	return map[string]any{
		"case": map[string]any{
			"id": c.ID, "number": c.Number, "pipeline": c.Pipeline, "status": c.Status,
			"stage": c.Stage, "org_unit": c.OrgUnit, "tenant_id": c.TenantID, "created_by": c.CreatedBy,
		},
		"data":  c.Data,
		"stage": stage,
		"node":  node,
	}
}

// pipelineAutomation runs an automated node by invoking the intent its hook
// names. The intent's output decides the result: a map with `passed` (or
// `ok`) false fails the node; anything else passes it.
type pipelineAutomation struct{}

func (pipelineAutomation) RunNode(ctx context.Context, hook string, c *pipeline.Case, stage, node string) (map[string]any, bool, error) {
	caller, ok := pipelineCaller(ctx)
	if !ok {
		return nil, false, errors.New("automations run only inside a request")
	}
	out, err := caller.Platform.CallIntent(ctx, hook, hookInput(c, stage, node), caller)
	if err != nil {
		return nil, false, err
	}
	if b, ok := out.(bool); ok {
		return map[string]any{"passed": b}, b, nil
	}
	result, _ := out.(map[string]any)
	if result == nil {
		result = map[string]any{"value": out}
	}
	passed := true
	for _, key := range []string{"passed", "ok"} {
		if v, ok := result[key]; ok {
			passed = Truthy(v)
			break
		}
	}
	return result, passed, nil
}

// pipelineStageHook runs a stage's on_enter / on_complete intent. A failing
// hook aborts the operation, so nothing is saved.
func pipelineStageHook(ctx context.Context, hook string, c *pipeline.Case, stage string) error {
	caller, ok := pipelineCaller(ctx)
	if !ok {
		return errors.New("stage hooks run only inside a request")
	}
	_, err := caller.Platform.CallIntent(ctx, hook, hookInput(c, stage, ""), caller)
	return err
}
