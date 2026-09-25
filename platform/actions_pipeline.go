package platform

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/oarkflow/ref/intent"
	"github.com/oarkflow/ref/pipeline"
)

// Pipeline actions drive the cases of a pipeline.cases resource.
//
// Every action reads its parameters the same way, so routes need no plumbing
// nodes: for a parameter such as `id` it uses config.id_fact (a fact path) if
// set, else a literal config.id, else the path parameter :id, else the query
// parameter ?id=, else a fact or input field of that name. The request body
// is read from the `input` fact, so nodes that take a body declare
// `requires [input]`.
//
// Anonymous applicants (allow_anonymous on the resource) receive an access
// key when they start a case; they send it back as the X-Access-Key header,
// the access_key query parameter or input.access_key.

func registerPipelineActions(r *Registry) {
	common := []ConfigField{
		{Name: "pipeline", Type: "string", Summary: "Pipeline name (default: the resource's first pipeline, or the case's own)"},
	}
	caseParam := ConfigField{Name: "id_fact", Type: "fact", Summary: "Fact path of the case id (default: :id path parameter)"}
	stageParam := ConfigField{Name: "stage_fact", Type: "fact", Summary: "Fact path of the stage (default: :stage path parameter, else the case's current stage)"}
	with := func(fields ...ConfigField) []ConfigField { return append(slices.Clone(common), fields...) }

	mustAction(r, "pipeline.describe", pipelineAction("describe"), ActionInfo{
		Family: "workflow", Kind: "read",
		Summary:  "Describe a pipeline: its stages, forms, inputs and page layout (for rendering a public start page)",
		Provides: "The pipeline definition",
		Config:   common,
	})
	mustAction(r, "pipeline.start", pipelineAction("start"), ActionInfo{
		Family: "workflow", Kind: "effect",
		Summary:  "Start a case (optionally saving input.data into the first stage)",
		Provides: "The case view; access_key when started anonymously",
		Config: with(
			ConfigField{Name: "org_unit_fact", Type: "fact", Default: "input.org_unit", Summary: "Fact path of the org unit the case belongs to"},
			ConfigField{Name: "data_fact", Type: "fact", Default: "input.data"},
		),
	})
	mustAction(r, "pipeline.view", pipelineAction("view"), ActionInfo{
		Family: "workflow", Kind: "read",
		Summary:  "Render a case stage for the caller: page groups with their modes and layouts, masked values, nodes and actions",
		Provides: "The view",
		Config:   with(caseParam, stageParam),
	})
	mustAction(r, "pipeline.save", pipelineAction("save"), ActionInfo{
		Family: "workflow", Kind: "effect",
		Summary:  "Save form data into an open stage (only inputs editable for the caller are accepted)",
		Provides: "The updated view",
		Config:   with(caseParam, stageParam, ConfigField{Name: "data_fact", Type: "fact", Default: "input.data"}),
	})
	mustAction(r, "pipeline.act", pipelineAction("act"), ActionInfo{
		Family: "workflow", Kind: "effect",
		Summary:  "Take a stage action: submit/advance, return for correction, reject, approve, withdraw or hold",
		Provides: "The updated view",
		Config:   with(caseParam, stageParam, ConfigField{Name: "action_fact", Type: "fact", Summary: "Fact path of the action (default: :action path parameter or input.action)"}),
	})
	mustAction(r, "pipeline.node", pipelineAction("node"), ActionInfo{
		Family: "workflow", Kind: "effect",
		Summary:  "Operate a stage node: verify/complete a review, approve/reject an approval, complete a task, re-run an automation, issue a certificate, waive",
		Provides: "The updated view",
		Config: with(caseParam, stageParam,
			ConfigField{Name: "node_fact", Type: "fact", Summary: "Fact path of the node (default: :node)"},
			ConfigField{Name: "verb_fact", Type: "fact", Summary: "Fact path of the verb (default: :verb or input.verb)"},
		),
	})
	mustAction(r, "pipeline.list", pipelineAction("list"), ActionInfo{
		Family: "workflow", Kind: "read",
		Summary:  "List cases: the caller's own (mine), their work queue (queue) or everything they may view (all), scoped to their jurisdiction",
		Provides: "The case summaries",
		Config: with(
			ConfigField{Name: "scope", Type: "string", Default: "queue", Summary: "mine | queue | all (or ?scope=)"},
			ConfigField{Name: "limit", Type: "int", Default: "50"},
		),
	})
	mustAction(r, "pipeline.get", pipelineAction("get"), ActionInfo{
		Family: "workflow", Kind: "read",
		Summary:  "Read a case's header, stage progress, history and certificates",
		Provides: "The case",
		Config:   with(caseParam),
	})
	mustAction(r, "pipeline.verify", pipelineAction("verify"), ActionInfo{
		Family: "workflow", Kind: "read",
		Summary:  "Verify an issued certificate by number or verification code (integrity, signature, expiry, revocation)",
		Provides: "The verification result",
		Config:   with(ConfigField{Name: "key_fact", Type: "fact", Summary: "Fact path of the number or code (default: :key, ?key= or input.key)"}),
	})
	roles := ConfigField{Name: "roles", Type: "[]string", Summary: "Only principals holding one of these roles may call it"}
	mustAction(r, "pipeline.certificate_pdf", pipelineAction("certificate_pdf"), ActionInfo{
		Family: "workflow", Kind: "read",
		Summary:  "Render an issued certificate of a case as a PDF with its verification code (:number, default the first valid one)",
		Provides: "The PDF download",
		Config:   with(caseParam, ConfigField{Name: "verify_url", Type: "string", Summary: "Public verification URL prefix printed with the code"}),
	})
	mustAction(r, "pipeline.work", pipelineAction("work"), ActionInfo{
		Family: "workflow", Kind: "effect",
		Summary:  "Work operations on a stage: claim, release, assign, delegate, suspend (put on hold) or resume",
		Provides: "The updated view",
		Config:   with(caseParam, stageParam, ConfigField{Name: "op_fact", Type: "fact", Summary: "Fact path of the op (default: :op or input.op)"}),
	})
	mustAction(r, "pipeline.note", pipelineAction("note"), ActionInfo{
		Family: "workflow", Kind: "effect",
		Summary:  "Add a note to a case (internal notes are visible to staff only)",
		Provides: "The updated view",
		Config:   with(caseParam),
	})
	mustAction(r, "pipeline.link", pipelineAction("link"), ActionInfo{
		Family: "workflow", Kind: "effect",
		Summary:  "Issue a signed, single-use link for an outside party to fill part of the current stage ({party, scope, ttl})",
		Provides: "The updated view with link_token",
		Config:   with(caseParam, stageParam),
	})
	mustAction(r, "pipeline.link_view", pipelineAction("link_view"), ActionInfo{
		Family: "workflow", Kind: "read",
		Summary:  "Render the page an external link opens (:token)",
		Provides: "The view",
		Config:   common,
	})
	mustAction(r, "pipeline.link_submit", pipelineAction("link_submit"), ActionInfo{
		Family: "workflow", Kind: "effect",
		Summary:  "Submit an outside party's inputs through a link (:token, input.data); the link is consumed",
		Provides: "{submitted, case}",
		Config:   with(ConfigField{Name: "data_fact", Type: "fact", Default: "input.data"}),
	})
	mustAction(r, "pipeline.seal_open", pipelineAction("seal_open"), ActionInfo{
		Family: "workflow", Kind: "effect",
		Summary:  "Approve opening a case's sealed values; they open once the quorum of distinct approvers is reached after the opening time",
		Provides: "The updated view",
		Config:   with(caseParam),
	})
	mustAction(r, "pipeline.hold", pipelineAction("hold"), ActionInfo{
		Family: "workflow", Kind: "effect",
		Summary:  "Place (op place, reason) or release (op release) a legal hold that blocks erasure and retention",
		Provides: "The updated view",
		Config:   with(caseParam, roles),
	})
	mustAction(r, "pipeline.erase", pipelineAction("erase"), ActionInfo{
		Family: "workflow", Kind: "effect",
		Summary:  "Right to erasure: find a data subject's cases by identifiers and anonymise (or purge) them, returning receipts; dry_run lists only",
		Provides: "{subject_ref, matched, receipts}",
		Config:   with(roles),
	})
	mustAction(r, "pipeline.sweep", pipelineAction("sweep"), ActionInfo{
		Family: "workflow", Kind: "effect",
		Summary:  "Apply time-based rules to every case: SLA warnings, breaches, escalation, hold expiry and retention (run it on a schedule)",
		Provides: "{scanned, changed, purged, conflicts}",
		Config:   with(roles),
	})
	mustAction(r, "pipeline.analytics", pipelineAction("analytics"), ActionInfo{
		Family: "workflow", Kind: "read",
		Summary:  "Process analytics: cycle and dwell times, rework, SLA attainment, bottlenecks, throughput and per-assignee load",
		Provides: "The analytics report",
		Config:   with(roles),
	})
	mustAction(r, "pipeline.bulk", pipelineAction("bulk"), ActionInfo{
		Family: "workflow", Kind: "effect",
		Summary:  "Apply one operation (act, node, work, note, hold) to many cases ({ids, op, stage, ...}); each case is authorised and saved on its own",
		Provides: "{applied, failed, results}",
		Config:   with(ConfigField{Name: "max_items", Type: "int", Default: "200"}),
	})
}

var pipelineConfigKeys = []string{
	"pipeline", "id", "id_fact", "stage", "stage_fact", "action", "action_fact", "node", "node_fact",
	"verb", "verb_fact", "org_unit_fact", "data_fact", "scope", "scope_fact", "limit", "key", "key_fact",
	"access_key_fact", "op", "op_fact", "roles", "max_items", "token", "token_fact",
	"number", "number_fact", "verify_url",
}

func pipelineAction(op string) ActionFactory {
	return ActionFactoryFunc(func(build BuildContext, spec NodeSpec) (Action, error) {
		res, err := requireResource[*PipelineCases](build, spec, "a pipeline.cases resource")
		if err != nil {
			return nil, err
		}
		if err := rejectUnknownConfig("pipeline."+op, spec.Config, pipelineConfigKeys...); err != nil {
			return nil, fmt.Errorf("node %q: %w", spec.Name, err)
		}
		if name := configString(spec.Config, "pipeline", ""); name != "" {
			if _, ok := res.engines[name]; !ok {
				return nil, fmt.Errorf("node %q: resource %q does not run pipeline %q", spec.Name, spec.Resource, name)
			}
		}
		limit, err := configInt(spec.Config, "limit", 50)
		if err != nil {
			return nil, fmt.Errorf("node %q: %w", spec.Name, err)
		}
		maxItems, err := configInt(spec.Config, "max_items", 200)
		if err != nil {
			return nil, fmt.Errorf("node %q: %w", spec.Name, err)
		}
		h := &pipelineHandler{res: res, spec: spec, op: op, limit: limit, maxItems: maxItems, roles: configStrings(spec.Config, "roles")}
		return ActionFunc(h.run), nil
	})
}

type pipelineHandler struct {
	res      *PipelineCases
	spec     NodeSpec
	op       string
	limit    int
	maxItems int
	roles    []string
}

// param resolves a named parameter (see the file comment for the order).
func (h *pipelineHandler) param(ctx *ActionContext, key string) string {
	if path := configString(h.spec.Config, key+"_fact", ""); path != "" {
		if v, ok := resolvePath(orgRoot(ctx), path); ok && v != nil {
			return strings.TrimSpace(Stringify(v))
		}
		return ""
	}
	if lit := configString(h.spec.Config, key, ""); lit != "" {
		return lit
	}
	for _, from := range []string{"path", "query"} {
		if v, ok := requestValue(ctx, from, key); ok && v != "" {
			return v
		}
	}
	for _, path := range []string{key, "input." + key} {
		if v, ok := resolvePath(ctx.Inputs, path); ok && v != nil {
			if s := strings.TrimSpace(Stringify(v)); s != "" {
				return s
			}
		}
	}
	return ""
}

func (h *pipelineHandler) body(ctx *ActionContext, factKey, fallback string) any {
	path := configString(h.spec.Config, factKey, fallback)
	v, _ := resolvePath(ctx.Inputs, path)
	return v
}

func (h *pipelineHandler) accessKey(ctx *ActionContext) string {
	if path := configString(h.spec.Config, "access_key_fact", ""); path != "" {
		v, _ := resolvePath(orgRoot(ctx), path)
		return Stringify(v)
	}
	if v, ok := requestValue(ctx, "header", "X-Access-Key"); ok {
		return v
	}
	if v, ok := requestValue(ctx, "query", "access_key"); ok {
		return v
	}
	if v, ok := resolvePath(ctx.Inputs, "input.access_key"); ok && v != nil {
		return Stringify(v)
	}
	return ""
}

func actorOf(principal Principal) pipeline.Actor {
	return pipeline.Actor{ID: principal.ID, Roles: principal.Roles, Claims: principal.Claims}
}

// actorFor is the caller as the case sees it: the principal, or — for an
// anonymous caller holding the case's access key — its applicant.
func (h *pipelineHandler) actorFor(ctx *ActionContext, c *pipeline.Case) pipeline.Actor {
	if ctx.Principal.ID == "" && accessKeyMatches(c, h.accessKey(ctx)) {
		return pipeline.Actor{ID: c.CreatedBy}
	}
	return actorOf(ctx.Principal)
}

func (h *pipelineHandler) engine(name string) (*pipeline.Engine, error) {
	if name == "" {
		name = configString(h.spec.Config, "pipeline", "")
	}
	e, ok := h.res.Engine(name)
	if !ok {
		return nil, notFound("pipeline", name)
	}
	return e, nil
}

// load reads the case named by the id parameter and checks the caller may
// reach it at all. A case outside the caller's tenant or jurisdiction reads
// as not found, so its existence is not disclosed.
func (h *pipelineHandler) load(ctx *ActionContext) (*pipeline.Case, *pipeline.Engine, pipeline.Actor, error) {
	id := h.param(ctx, "id")
	if id == "" {
		return nil, nil, pipeline.Actor{}, invalidInput("a case id is required")
	}
	c, err := h.res.store.Get(ctx.Context, id)
	if err != nil {
		return nil, nil, pipeline.Actor{}, pipelineFailure(err)
	}
	if c.TenantID != ctx.TenantID {
		return nil, nil, pipeline.Actor{}, notFound("case", id)
	}
	if want := configString(h.spec.Config, "pipeline", ""); want != "" && c.Pipeline != want {
		return nil, nil, pipeline.Actor{}, notFound("case", id)
	}
	e, err := h.engine(c.Pipeline)
	if err != nil {
		return nil, nil, pipeline.Actor{}, err
	}
	actor := h.actorFor(ctx, c)
	if actor.ID != c.CreatedBy && !h.res.inScope(ctx.TenantID, ctx.Principal, c) {
		return nil, nil, pipeline.Actor{}, notFound("case", id)
	}
	return c, e, actor, nil
}

func (h *pipelineHandler) run(ctx *ActionContext) (ActionResult, error) {
	// Hooks and automations invoke intents as part of this request.
	hookCtx := context.WithValue(ctx.Context, pipelineCallKey{}, ctx)
	if len(h.roles) > 0 && !slices.ContainsFunc(h.roles, ctx.Principal.HasRole) {
		return ActionResult{}, permissionDenied("you may not do that")
	}
	switch h.op {
	case "link_view", "link_submit":
		return h.link(ctx, hookCtx)
	case "certificate_pdf":
		return h.certificatePDF(ctx)
	case "sweep":
		return h.sweep(ctx, hookCtx)
	case "analytics":
		return h.analytics(ctx)
	case "erase":
		return h.erase(ctx, hookCtx)
	case "bulk":
		return h.bulk(ctx, hookCtx)
	case "describe":
		e, err := h.engine("")
		if err != nil {
			return ActionResult{}, err
		}
		return h.out(e.C.Def)
	case "start":
		return h.start(ctx, hookCtx)
	case "verify":
		return h.verify(ctx)
	case "list":
		return h.list(ctx)
	}

	c, e, actor, err := h.load(ctx)
	if err != nil {
		return ActionResult{}, err
	}
	if h.op == "view" {
		v, err := e.View(c, actor, h.param(ctx, "stage"))
		if err != nil {
			return ActionResult{}, pipelineFailure(err)
		}
		return h.out(v)
	}
	if h.op == "get" {
		return h.get(c, e, actor)
	}

	// Mutations: a client that sends the revision it rendered gets a clean
	// conflict instead of acting on a case that moved under it.
	if rev := h.param(ctx, "revision"); rev != "" {
		if n, err := strconv.ParseInt(rev, 10, 64); err == nil && n != c.Revision {
			return ActionResult{}, pipelineFailure(pipeline.ErrConflict)
		}
	}
	stage := h.param(ctx, "stage")
	if stage == "" {
		stage = c.Stage
	}
	next, extra, err := h.apply(ctx, hookCtx, c, e, actor, stage, h.op, h.body(ctx, "", "input"))
	if err != nil {
		return ActionResult{}, err
	}
	if err := h.commit(ctx, hookCtx, e, next); err != nil {
		return ActionResult{}, err
	}
	v, err := e.View(next, actor, "")
	if err != nil {
		// The operation succeeded but the case moved beyond what the caller
		// may see (an officer who just forwarded it): report the header only.
		return h.out(mergeExtra(map[string]any{"case": map[string]any{
			"id": next.ID, "number": next.Number, "status": next.Status, "stage": next.Stage, "revision": next.Revision,
		}}, extra))
	}
	if len(extra) > 0 {
		m, err := toMap(v)
		if err != nil {
			return ActionResult{}, err
		}
		return h.out(mergeExtra(m, extra))
	}
	return h.out(v)
}

func mergeExtra(m, extra map[string]any) map[string]any {
	for k, v := range extra {
		m[k] = v
	}
	return m
}

// apply runs one mutating operation on a loaded case. body is the request
// body (the node's input fact). extra carries values to return beside the
// view (an issued link token).
func (h *pipelineHandler) apply(ctx *ActionContext, hookCtx context.Context, c *pipeline.Case, e *pipeline.Engine, actor pipeline.Actor, stage, op string, rawBody any) (*pipeline.Case, map[string]any, error) {
	body, _ := rawBody.(map[string]any)
	str := func(key string) string {
		if v, ok := body[key]; ok && v != nil {
			return strings.TrimSpace(Stringify(v))
		}
		return h.param(ctx, key)
	}
	var (
		next  *pipeline.Case
		extra map[string]any
		err   error
	)
	switch op {
	case "save":
		data, _ := h.body(ctx, "data_fact", "input.data").(map[string]any)
		if data == nil {
			data, _ = body["data"].(map[string]any)
		}
		if data == nil {
			return nil, nil, invalidInput("input.data must be an object of forms")
		}
		next, err = e.Save(hookCtx, c, actor, stage, data)
	case "act":
		action := str("action")
		if action == "" {
			return nil, nil, invalidInput("an action is required")
		}
		var in pipeline.ActInput
		if err := decodeInto(body, &in); err != nil {
			return nil, nil, invalidInput("the action body is invalid: %v", err)
		}
		next, err = e.Act(hookCtx, c, actor, stage, action, in)
	case "node":
		node, verb := str("node"), str("verb")
		if node == "" || verb == "" {
			return nil, nil, invalidInput("a node and a verb are required")
		}
		var in pipeline.NodeInput
		if err := decodeInto(body, &in); err != nil {
			return nil, nil, invalidInput("the node body is invalid: %v", err)
		}
		next, err = e.NodeAct(hookCtx, c, actor, stage, node, verb, in)
	case "work":
		comment := str("comment")
		switch verb := str("op"); verb {
		case "claim":
			next, err = e.Claim(hookCtx, c, actor, stage)
		case "release":
			next, err = e.Release(hookCtx, c, actor, stage, comment)
		case "assign":
			next, err = e.Assign(hookCtx, c, actor, stage, str("to"), comment)
		case "delegate":
			next, err = e.Delegate(hookCtx, c, actor, stage, str("to"), comment)
		case "suspend":
			var until *time.Time
			if u := str("until"); u != "" {
				t, perr := time.Parse(time.RFC3339, u)
				if perr != nil {
					return nil, nil, invalidInput("until must be an RFC 3339 time")
				}
				until = &t
			}
			reason := str("reason")
			if reason == "" {
				reason = comment
			}
			next, err = e.Suspend(hookCtx, c, actor, stage, reason, until)
		case "resume":
			next, err = e.Resume(hookCtx, c, actor, stage, comment)
		default:
			return nil, nil, invalidInput("work op must be claim, release, assign, delegate, suspend or resume")
		}
	case "note":
		internal, _ := body["internal"].(bool)
		next, err = e.AddNote(hookCtx, c, actor, str("body"), internal, str("parent_id"))
	case "link":
		var scope []string
		if list, ok := body["scope"].([]any); ok {
			for _, s := range list {
				scope = append(scope, Stringify(s))
			}
		}
		ttl := 7 * 24 * time.Hour
		if t := str("ttl"); t != "" {
			d, perr := time.ParseDuration(t)
			if perr != nil {
				return nil, nil, invalidInput("ttl must be a duration like 72h")
			}
			ttl = d
		}
		var token string
		next, token, err = e.IssueLink(hookCtx, c, actor, stage, str("party"), scope, ttl)
		if err == nil {
			extra = map[string]any{"link_token": token}
		}
	case "seal_open":
		next, err = e.ApproveOpening(hookCtx, c, actor, str("comment"))
	case "hold":
		if str("op") == "release" {
			next, err = e.ReleaseHold(hookCtx, c, actor, str("comment"))
		} else {
			next, err = e.PlaceHold(hookCtx, c, actor, str("reason"))
		}
	default:
		return nil, nil, fmt.Errorf("pipeline: unknown operation %q", op)
	}
	if err != nil {
		return nil, nil, pipelineFailure(err)
	}
	return next, extra, nil
}

// commit saves a changed case and then runs the pipeline's event hooks for
// the events the operation emitted. Hooks run after the save, so they only
// ever see committed changes; a failing hook is recorded, never rolled back.
func (h *pipelineHandler) commit(ctx *ActionContext, hookCtx context.Context, e *pipeline.Engine, next *pipeline.Case) error {
	if err := h.res.store.Update(ctx.Context, next); err != nil {
		return pipelineFailure(err)
	}
	h.res.dispatch(hookCtx, e, next)
	return nil
}

func (h *pipelineHandler) start(ctx *ActionContext, hookCtx context.Context) (ActionResult, error) {
	e, err := h.engine("")
	if err != nil {
		return ActionResult{}, err
	}
	actor := actorOf(ctx.Principal)
	accessKey := ""
	if actor.ID == "" {
		if !h.res.allowAnonymous || !e.C.Def.Stages[0].Public {
			return ActionResult{}, permissionDenied("sign in to start a " + orDefaultString(e.C.Def.Title, e.C.Def.Name))
		}
		accessKey = randomHex(24)
		actor.ID = "anon_" + randomHex(8)
	}
	orgUnit := ""
	if v, ok := resolvePath(orgRoot(ctx), configString(h.spec.Config, "org_unit_fact", "input.org_unit")); ok && v != nil {
		orgUnit = strings.TrimSpace(Stringify(v))
	}
	if orgUnit != "" && h.res.org != nil && !h.res.org.Snapshot(ctx.TenantID).Tree.Has(orgUnit) {
		return ActionResult{}, notFound("organisational unit", orgUnit)
	}
	data, _ := h.body(ctx, "data_fact", "input.data").(map[string]any)
	seq, err := h.res.store.NextSeq(ctx.Context, e.C.Def.Name)
	if err != nil {
		return ActionResult{}, pipelineFailure(err)
	}
	c, err := e.Start(hookCtx, actor, pipeline.StartOptions{
		Number:   pipeline.FormatNumber(e.C.Def.NumberFormat, seq, time.Now()),
		TenantID: ctx.TenantID,
		OrgUnit:  orgUnit,
		Data:     data,
	})
	if err != nil {
		return ActionResult{}, pipelineFailure(err)
	}
	if accessKey != "" {
		c.AccessKeyHash = hashAccessKey(accessKey)
	}
	if err := h.res.store.Create(ctx.Context, c); err != nil {
		return ActionResult{}, pipelineFailure(err)
	}
	h.res.dispatch(hookCtx, e, c)
	v, err := e.View(c, actor, "")
	if err != nil {
		return ActionResult{}, pipelineFailure(err)
	}
	out, err := toMap(v)
	if err != nil {
		return ActionResult{}, err
	}
	if accessKey != "" {
		out["access_key"] = accessKey
	}
	return acknowledgement(h.spec, out), nil
}

func (h *pipelineHandler) list(ctx *ActionContext) (ActionResult, error) {
	e, err := h.engine("")
	if err != nil {
		return ActionResult{}, err
	}
	actor := actorOf(ctx.Principal)
	scope := h.param(ctx, "scope")
	if scope == "" {
		scope = "queue"
	}
	q := pipeline.Query{Pipeline: e.C.Def.Name, TenantID: ctx.TenantID, Limit: h.limit}
	if n, err := strconv.Atoi(h.param(ctx, "limit")); err == nil && n > 0 && n < h.limit {
		q.Limit = n
	}
	if n, err := strconv.Atoi(h.param(ctx, "offset")); err == nil && n > 0 {
		q.Offset = n
	}
	if s := h.param(ctx, "status"); s != "" {
		q.Statuses = []string{s}
	}
	switch scope {
	case "mine":
		if actor.ID == "" {
			return ActionResult{}, permissionDenied("sign in to list your cases")
		}
		q.CreatedBy = actor.ID
	case "assigned":
		// My work: cases whose current stage is assigned to me.
		if actor.ID == "" {
			return ActionResult{}, permissionDenied("sign in to list your work")
		}
		q.Assignees = []string{actor.ID}
		q.Statuses = []string{pipeline.CaseDraft, pipeline.CaseInProgress, pipeline.CaseReturned}
	case "queue", "all":
		// A work queue holds the stages the caller acts on; "all" adds the
		// stages they may only view.
		for _, st := range e.C.Def.Stages {
			stage := st
			can := actor.HasAnyRole(stage.Roles) || actor.HasAnyRole(stage.AssignRoles)
			for _, n := range stage.Nodes {
				can = can || actor.HasAnyRole(n.Roles)
			}
			if scope == "all" {
				can = can || e.CanView(&pipeline.Case{}, &stage, actor)
			}
			if can {
				q.Stages = append(q.Stages, stage.Name)
			}
		}
		if s := h.param(ctx, "stage"); s != "" {
			if !slices.Contains(q.Stages, s) {
				return h.out([]any{})
			}
			q.Stages = []string{s}
		}
		if len(q.Stages) == 0 {
			return h.out([]any{})
		}
		units, ok := h.res.jurisdiction(ctx.TenantID, ctx.Principal)
		if !ok {
			return h.out([]any{})
		}
		q.OrgUnits = units
		if len(q.Statuses) == 0 && scope == "queue" {
			q.Statuses = []string{pipeline.CaseDraft, pipeline.CaseInProgress, pipeline.CaseReturned}
		}
		switch h.param(ctx, "assignee") {
		case "":
		case "unassigned":
			q.Assignees = []string{""}
		case "me":
			q.Assignees = []string{actor.ID}
		default:
			q.Assignees = []string{h.param(ctx, "assignee")}
		}
	default:
		return ActionResult{}, invalidInput("scope must be mine, assigned, queue or all")
	}
	cases, err := h.res.store.List(ctx.Context, q)
	if err != nil {
		return ActionResult{}, pipelineFailure(err)
	}
	out := make([]any, 0, len(cases))
	for _, c := range cases {
		row := map[string]any{
			"id": c.ID, "number": c.Number, "pipeline": c.Pipeline, "status": c.Status, "stage": c.Stage,
			"org_unit": c.OrgUnit, "created_by": c.CreatedBy, "revision": c.Revision,
			"created_at": c.CreatedAt.Format(time.RFC3339), "updated_at": c.UpdatedAt.Format(time.RFC3339),
		}
		if st, ok := e.C.Stage(c.Stage); ok {
			row["stage_title"] = st.Title
		}
		if ss := c.Stages[c.Stage]; ss != nil {
			row["stage_status"] = ss.Status
			if ss.DueAt != nil {
				row["due_at"] = ss.DueAt.Format(time.RFC3339)
				row["overdue"] = !c.Terminal() && time.Now().After(*ss.DueAt)
			}
			row["open_nodes"] = e.OpenNodes(c, c.Stage)
			if ss.Assignee != "" {
				row["assignee"] = ss.Assignee
			}
			if ss.SLA != nil {
				row["sla_status"] = ss.SLA.Status
			}
			if ss.Suspended != nil {
				row["on_hold"] = ss.Suspended.Reason
			}
		}
		out = append(out, row)
	}
	return h.out(out)
}

func (h *pipelineHandler) get(c *pipeline.Case, e *pipeline.Engine, actor pipeline.Actor) (ActionResult, error) {
	allowed := actor.ID != "" && actor.ID == c.CreatedBy
	for i := range e.C.Def.Stages {
		allowed = allowed || e.CanView(c, &e.C.Def.Stages[i], actor)
	}
	if !allowed {
		return ActionResult{}, permissionDenied("you may not view this case")
	}
	stages := map[string]any{}
	for name, ss := range c.Stages {
		stages[name] = ss
	}
	return h.out(map[string]any{
		"id": c.ID, "number": c.Number, "pipeline": c.Pipeline, "status": c.Status, "stage": c.Stage,
		"org_unit": c.OrgUnit, "created_by": c.CreatedBy, "revision": c.Revision,
		"created_at": c.CreatedAt, "updated_at": c.UpdatedAt,
		"stages": stages, "history": c.History, "certificates": c.Certificates,
	})
}

func (h *pipelineHandler) verify(ctx *ActionContext) (ActionResult, error) {
	key := h.param(ctx, "key")
	if key == "" {
		return ActionResult{}, invalidInput("a certificate number or verification code is required")
	}
	cert, err := h.res.store.FindCertificate(ctx.Context, key)
	if err != nil {
		if errors.Is(err, pipeline.ErrNotFound) {
			return h.out(map[string]any{"valid": false, "reason": "no certificate has that number or code"})
		}
		return ActionResult{}, pipelineFailure(err)
	}
	e, err := h.engine(cert.Pipeline)
	if err != nil {
		return ActionResult{}, err
	}
	status := e.Verify(*cert)
	out := map[string]any{"valid": status.Valid, "certificate": map[string]any{
		"number": cert.Number, "title": cert.Title, "code": cert.Code, "subject": cert.Subject,
		"issued_at": cert.IssuedAt, "expires_at": cert.ExpiresAt, "case_number": cert.CaseNumber,
	}}
	if status.Reason != "" {
		out["reason"] = status.Reason
	}
	return h.out(out)
}

// out publishes a value as plain JSON data (maps and slices), so later nodes
// and expressions can read into it.
func (h *pipelineHandler) out(v any) (ActionResult, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return ActionResult{}, err
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return ActionResult{}, err
	}
	return acknowledgement(h.spec, decoded), nil
}

func toMap(v any) (map[string]any, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	out := map[string]any{}
	return out, json.Unmarshal(raw, &out)
}

func decodeInto(v any, target any) error {
	if v == nil {
		return nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, target)
}

// pipelineFailure maps engine errors onto platform failures.
func pipelineFailure(err error) error {
	var ve *pipeline.ValidationError
	if errors.As(err, &ve) {
		details := make([]any, len(ve.Fields))
		for i, f := range ve.Fields {
			details[i] = map[string]any{"path": f.Path, "rule": f.Rule, "message": f.Message}
		}
		return intent.Failure{Code: "VALIDATION_FAILED", Category: intent.CategoryInvalidInput,
			Message: ve.Error(), Meta: map[string]any{"details": details}}
	}
	message := err.Error()
	for _, prefix := range []error{pipeline.ErrForbidden, pipeline.ErrState, pipeline.ErrNotFound} {
		message = strings.TrimPrefix(message, prefix.Error()+": ")
	}
	switch {
	case errors.Is(err, pipeline.ErrForbidden):
		return permissionDenied(message)
	case errors.Is(err, pipeline.ErrState):
		return intent.Failure{Code: "INVALID_STATE", Category: intent.CategoryConflict, Message: message}
	case errors.Is(err, pipeline.ErrNotFound):
		return notFoundOrMessage(message)
	case errors.Is(err, pipeline.ErrConflict):
		return intent.Failure{Code: "CONFLICT", Category: intent.CategoryConflict, Message: "the case was changed by someone else; reload and try again"}
	}
	return databaseFailure(err)
}
