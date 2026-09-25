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
}

var pipelineConfigKeys = []string{
	"pipeline", "id", "id_fact", "stage", "stage_fact", "action", "action_fact", "node", "node_fact",
	"verb", "verb_fact", "org_unit_fact", "data_fact", "scope", "scope_fact", "limit", "key", "key_fact",
	"access_key_fact",
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
		h := &pipelineHandler{res: res, spec: spec, op: op, limit: limit}
		return ActionFunc(h.run), nil
	})
}

type pipelineHandler struct {
	res   *PipelineCases
	spec  NodeSpec
	op    string
	limit int
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
	switch h.op {
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
	var next *pipeline.Case
	switch h.op {
	case "save":
		data, _ := h.body(ctx, "data_fact", "input.data").(map[string]any)
		if data == nil {
			return ActionResult{}, invalidInput("input.data must be an object of forms")
		}
		next, err = e.Save(hookCtx, c, actor, stage, data)
	case "act":
		action := h.param(ctx, "action")
		if action == "" {
			return ActionResult{}, invalidInput("an action is required")
		}
		var in pipeline.ActInput
		if err := decodeInto(h.body(ctx, "", "input"), &in); err != nil {
			return ActionResult{}, invalidInput("the action body is invalid: %v", err)
		}
		next, err = e.Act(hookCtx, c, actor, stage, action, in)
	case "node":
		node, verb := h.param(ctx, "node"), h.param(ctx, "verb")
		if node == "" || verb == "" {
			return ActionResult{}, invalidInput("a node and a verb are required")
		}
		var in pipeline.NodeInput
		if err := decodeInto(h.body(ctx, "", "input"), &in); err != nil {
			return ActionResult{}, invalidInput("the node body is invalid: %v", err)
		}
		next, err = e.NodeAct(hookCtx, c, actor, stage, node, verb, in)
	default:
		return ActionResult{}, fmt.Errorf("pipeline: unknown operation %q", h.op)
	}
	if err != nil {
		return ActionResult{}, pipelineFailure(err)
	}
	if err := h.res.store.Update(ctx.Context, next); err != nil {
		return ActionResult{}, pipelineFailure(err)
	}
	v, err := e.View(next, actor, "")
	if err != nil {
		// The operation succeeded but the case moved beyond what the caller
		// may see (an officer who just forwarded it): report the header only.
		return h.out(map[string]any{"case": map[string]any{
			"id": next.ID, "number": next.Number, "status": next.Status, "stage": next.Stage, "revision": next.Revision,
		}})
	}
	return h.out(v)
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
	case "queue", "all":
		// A work queue holds the stages the caller acts on; "all" adds the
		// stages they may only view.
		for _, st := range e.C.Def.Stages {
			stage := st
			can := actor.HasAnyRole(stage.Roles)
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
	default:
		return ActionResult{}, invalidInput("scope must be mine, queue or all")
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
