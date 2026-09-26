package platform

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

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
	name  string
	store pipeline.Store
	// outbox is the store's durable event outbox when any pipeline declares
	// event hooks; nudge wakes the dispatcher after a commit.
	outbox pipeline.Outbox
	nudge  chan struct{}
	// maxAttempts and retryBase tune hook retries (default 10 attempts,
	// backoff 2s doubling to at most 10 minutes).
	maxAttempts    int
	retryBase      time.Duration
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
			{Name: "signing_secret", Type: "string", Summary: "HMAC key that signs issued certificates and external links (use env.required(...)); without it certificates carry a content hash only"},
			{Name: "signer", Type: "resource", Summary: "crypto.signer that also signs certificates asymmetrically (Ed25519/RSA), verifiable offline against its JWKS"},
			{Name: "seal_secret", Type: "string", Summary: "Key that encrypts sealed inputs (AES-256-GCM); required when a pipeline declares sealed inputs"},
			{Name: "org_resource", Type: "string", Summary: "org.hierarchy resource: resolves `lookup` inputs and scopes work queues to the caller's jurisdiction"},
			{Name: "allow_anonymous", Type: "bool", Default: "false", Summary: "Let anonymous callers start public pipelines; they get an access key to return to their case"},
			{Name: "event_max_attempts", Type: "int", Default: "10", Summary: "Attempts before an event hook is dead-lettered"},
			{Name: "event_retry_base", Type: "duration", Default: "2s", Summary: "First retry delay of a failed event hook (doubles, at most 10m)"},
		},
	})
}

func openPipelineCases(ctx context.Context, spec ResourceSpec) (Resource, io.Closer, error) {
	if err := rejectUnknownConfig("pipeline.cases", spec.Config,
		"pipelines", "database", "table_prefix", "migrate", "signing_secret", "signer", "seal_secret", "org_resource", "allow_anonymous",
		"event_max_attempts", "event_retry_base",
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
	maxAttempts, err := configInt(spec.Config, "event_max_attempts", pipeline.MaxAttempts)
	if err != nil {
		return nil, nil, err
	}
	retryBase, err := configDuration(spec.Config, "event_retry_base", 2*time.Second)
	if err != nil {
		return nil, nil, err
	}
	p := &PipelineCases{
		maxAttempts:    max(1, maxAttempts),
		retryBase:      retryBase,
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
	signer, err := requireSigner(spec, "signer")
	if err != nil {
		return nil, nil, err
	}
	sealKey := []byte(configString(spec.Config, "seal_secret", ""))
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
		if signer != nil {
			engine.Signer = signer.keys
		}
		engine.SealKey = sealKey
		engine.Workload = p.workload
		if p.org != nil {
			engine.Lookup = p.lookup
			engine.OrgCovers = p.orgCovers
		}
		if hasSealed(&def) && len(sealKey) == 0 {
			return nil, nil, fmt.Errorf("pipeline.cases %q: pipeline %q has sealed inputs: set seal_secret", spec.Name, name)
		}
		p.engines[name] = engine
		p.order = append(p.order, name)
	}

	if configString(spec.Config, "database", "") == "" {
		mem := pipeline.NewMemoryStore()
		mem.Record = p.recordFilter()
		p.store = mem
		p.useOutbox(mem, mem.Record != nil)
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
	store.Record = p.recordFilter()
	p.store = store
	p.useOutbox(store, store.Record != nil)
	return p, nil, nil
}

// recordFilter selects the events some pipeline hook listens to (nil when no
// pipeline declares hooks, so nothing is written to the outbox).
func (p *PipelineCases) recordFilter() func(pipeline.Event) bool {
	var patterns []string
	for _, name := range p.order {
		for _, h := range p.engines[name].C.Def.On {
			patterns = append(patterns, h.Event)
		}
	}
	if len(patterns) == 0 {
		return nil
	}
	return func(ev pipeline.Event) bool {
		return slices.ContainsFunc(patterns, func(pat string) bool { return eventMatches(pat, ev.Name) })
	}
}

func (p *PipelineCases) useOutbox(o pipeline.Outbox, enabled bool) {
	if enabled {
		p.outbox = o
		p.nudge = make(chan struct{}, 1)
	}
}

// runBackground delivers outbox events until ctx ends: woken by commits,
// and polling so retries and other replicas' events are picked up.
func (p *PipelineCases) runBackground(ctx context.Context, platform *Platform) {
	if p.outbox == nil {
		return
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		for p.deliver(ctx, platform) > 0 {
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-p.nudge:
		}
	}
}

// deliver runs one batch of due events and returns how many it claimed.
func (p *PipelineCases) deliver(ctx context.Context, platform *Platform) int {
	now := time.Now()
	events, err := p.outbox.ClaimEvents(ctx, 50, time.Minute, now)
	if err != nil {
		if ctx.Err() == nil { // shutting down is not a failure
			slog.Warn("pipeline outbox claim failed", "resource", p.name, "error", err)
		}
		return 0
	}
	for _, ev := range events {
		if ctx.Err() != nil {
			break // shutting down: the lease lapses and another dispatcher takes the rest
		}
		err := p.deliverOne(ctx, platform, ev)
		if err != nil && ctx.Err() != nil {
			break // interrupted by shutdown, not a failed attempt
		}
		// Record the outcome even when shutdown began meanwhile: hooks that
		// ran must be acknowledged, or they run again after the lease.
		sctx, scancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		switch {
		case err == nil:
			err = p.outbox.AckEvent(sctx, ev.ID)
		case ev.Attempts+1 >= p.maxAttempts:
			slog.Error("pipeline event dead-lettered", "resource", p.name, "event", ev.Event.Name, "case", ev.CaseID, "error", err)
			err = p.outbox.RetryEvent(sctx, ev.ID, now, true, err.Error())
		default:
			err = p.outbox.RetryEvent(sctx, ev.ID, now.Add(p.backoff(ev.Attempts+1)), false, err.Error())
		}
		scancel()
		if err != nil {
			slog.Warn("pipeline outbox update failed", "resource", p.name, "event", ev.ID, "error", err)
		}
	}
	return len(events)
}

// backoff is the delay before retry n: retryBase doubling, at most 10m.
func (p *PipelineCases) backoff(n int) time.Duration {
	d := p.retryBase
	for i := 1; i < n && d < 10*time.Minute; i++ {
		d *= 2
	}
	return min(d, 10*time.Minute)
}

// deliverOne runs the hooks of one event against the case's current state.
// Every matching hook must succeed for the event to be acknowledged, so a
// hook should be idempotent (it may run again after a partial failure).
func (p *PipelineCases) deliverOne(ctx context.Context, platform *Platform, ev pipeline.OutboxEvent) error {
	e, ok := p.engines[ev.Pipeline]
	if !ok {
		return nil // pipeline removed: nothing to do
	}
	c, err := p.store.Get(ctx, ev.CaseID)
	if errors.Is(err, pipeline.ErrNotFound) {
		return nil // case purged
	}
	if err != nil {
		return err
	}
	for _, hook := range e.C.Def.On {
		if !eventMatches(hook.Event, ev.Event.Name) || (hook.Stage != "" && hook.Stage != ev.Event.Stage) {
			continue
		}
		input := hookInput(c, ev.Event.Stage, "")
		input["event"] = map[string]any{"id": ev.ID, "name": ev.Event.Name, "stage": ev.Event.Stage, "actor": ev.Event.Actor,
			"at": ev.Event.At.Format(time.RFC3339), "detail": ev.Event.Detail, "attempt": ev.Attempts + 1}
		if hook.When != "" {
			env := map[string]any{}
			maps.Copy(env, c.Data)
			maps.Copy(env, input)
			ok, err := e.Eval.Eval(hook.When, env)
			if err != nil || !Truthy(ok) {
				continue
			}
		}
		hctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		_, err := platform.CallIntent(hctx, hook.Hook, input, nil)
		cancel()
		if err != nil {
			return fmt.Errorf("hook %s: %w", hook.Hook, err)
		}
	}
	return nil
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

func hasSealed(def *pipeline.Definition) bool {
	check := func(inputs []pipeline.Input) bool {
		return slices.ContainsFunc(inputs, func(in pipeline.Input) bool { return in.Sealed })
	}
	if check(def.Inputs) {
		return true
	}
	for _, f := range def.Forms {
		if check(f.Inputs) {
			return true
		}
	}
	for _, st := range def.Stages {
		for _, f := range st.Forms {
			if check(f.Inputs) {
				return true
			}
		}
	}
	return false
}

// workload counts each worker's open assignments across the pipeline's open
// cases, from the store — so capacity holds across replicas.
func (p *PipelineCases) workload(ctx context.Context, name string) (map[string]pipeline.Load, error) {
	if counter, ok := p.store.(pipeline.WorkloadCounter); ok {
		return counter.Workload(ctx, name)
	}
	out := map[string]pipeline.Load{}
	err := p.eachCase(ctx, pipeline.Query{Pipeline: name, Statuses: []string{pipeline.CaseDraft, pipeline.CaseInProgress, pipeline.CaseReturned}}, func(c *pipeline.Case) bool {
		for _, ss := range c.Stages {
			if ss.Assignee == "" || ss.AssignedAt == nil {
				continue
			}
			l := out[ss.Assignee]
			l.Open++
			if ss.AssignedAt.After(l.LastAssigned) {
				l.LastAssigned = *ss.AssignedAt
			}
			out[ss.Assignee] = l
		}
		return true
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// orgCovers reports whether any of a worker's units is the case's unit or
// one of its ancestors.
func (p *PipelineCases) orgCovers(units []string, unit string) bool {
	return p.org.Snapshot("").Tree.Covers(units, unit)
}

// dispatch runs the pipeline's `on` hooks for the events a committed
// operation emitted. Hooks run after the save; a failing hook is logged and
// never undoes the change.
func (p *PipelineCases) dispatch(ctx context.Context, e *pipeline.Engine, c *pipeline.Case) {
	hooks := e.C.Def.On
	if len(hooks) == 0 {
		return
	}
	if p.outbox != nil {
		// The events were written with the change; wake the dispatcher.
		select {
		case p.nudge <- struct{}{}:
		default:
		}
		return
	}
	caller, ok := pipelineCaller(ctx)
	if !ok {
		return
	}
	for _, ev := range c.Events() {
		for _, hook := range hooks {
			if !eventMatches(hook.Event, ev.Name) || (hook.Stage != "" && hook.Stage != ev.Stage) {
				continue
			}
			input := hookInput(c, ev.Stage, "")
			input["event"] = map[string]any{"name": ev.Name, "stage": ev.Stage, "actor": ev.Actor, "at": ev.At.Format(time.RFC3339), "detail": ev.Detail}
			if hook.When != "" {
				env := map[string]any{}
				maps.Copy(env, c.Data)
				maps.Copy(env, input)
				ok, err := e.Eval.Eval(hook.When, env)
				if err != nil || !Truthy(ok) {
					continue
				}
			}
			if _, err := caller.Platform.CallIntent(ctx, hook.Hook, input, caller); err != nil {
				slog.Warn("pipeline event hook failed", "pipeline", c.Pipeline, "case", c.ID, "event", ev.Name, "hook", hook.Hook, "error", err)
			}
		}
	}
}

// eventMatches supports exact names, "*" and prefix patterns like "sla.*".
func eventMatches(pattern, name string) bool {
	if pattern == "*" || pattern == name {
		return true
	}
	return strings.HasSuffix(pattern, ".*") && strings.HasPrefix(name, strings.TrimSuffix(pattern, "*"))
}

// eachCase calls fn for every case matching q, a page at a time, so scans
// (sweep, analytics, erasure) are not capped. fn returning false stops.
func (p *PipelineCases) eachCase(ctx context.Context, q pipeline.Query, fn func(*pipeline.Case) bool) error {
	const page = 500
	q.Limit = page
	for offset := 0; ; offset += page {
		q.Offset = offset
		cases, err := p.store.List(ctx, q)
		if err != nil {
			return err
		}
		for _, c := range cases {
			if !fn(c) {
				return nil
			}
		}
		if len(cases) < page {
			return nil
		}
	}
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
