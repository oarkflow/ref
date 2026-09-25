package platform

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/oarkflow/ref/pipeline"
)

// Pipeline operations that are not "load one case, apply, save": external
// links (the case comes from the token), the time-based sweep, analytics,
// right-to-erasure and bulk operations.

// link serves pipeline.link_view and pipeline.link_submit. The token names
// the case; it is checked before the case is even loaded.
func (h *pipelineHandler) link(ctx *ActionContext, hookCtx context.Context) (ActionResult, error) {
	token := h.param(ctx, "token")
	if token == "" {
		return ActionResult{}, invalidInput("a link token is required")
	}
	for _, name := range h.res.order {
		e := h.res.engines[name]
		caseID, _, err := e.ParseLinkToken(token)
		if err != nil {
			continue
		}
		c, err := h.res.store.Get(ctx.Context, caseID)
		if err != nil {
			return ActionResult{}, permissionDenied("the link is not valid")
		}
		if c.Pipeline != name {
			continue
		}
		actor, err := e.LinkActor(c, token)
		if err != nil {
			return ActionResult{}, pipelineFailure(err)
		}
		if h.op == "link_view" {
			v, err := e.View(c, actor, actor.Link.Stage)
			if err != nil {
				return ActionResult{}, pipelineFailure(err)
			}
			return h.out(v)
		}
		data, _ := h.body(ctx, "data_fact", "input.data").(map[string]any)
		if data == nil {
			return ActionResult{}, invalidInput("input.data must be an object of forms")
		}
		next, err := e.LinkSubmit(hookCtx, c, actor, data)
		if err != nil {
			return ActionResult{}, pipelineFailure(err)
		}
		if err := h.commit(ctx, hookCtx, e, next); err != nil {
			return ActionResult{}, err
		}
		return h.out(map[string]any{"submitted": true, "case": map[string]any{"number": next.Number}})
	}
	return ActionResult{}, permissionDenied("the link is not valid")
}

// sweep applies time-based rules to every open case (and retention to
// finished ones). A case changed concurrently is skipped: the next sweep
// picks it up.
func (h *pipelineHandler) sweep(ctx *ActionContext, hookCtx context.Context) (ActionResult, error) {
	scanned, changed, purged, conflicts, failed := 0, 0, 0, 0, 0
	for _, name := range h.res.order {
		if want := configString(h.spec.Config, "pipeline", ""); want != "" && want != name {
			continue
		}
		e := h.res.engines[name]
		cases, err := h.res.store.List(ctx.Context, pipeline.Query{Pipeline: name, TenantID: ctx.TenantID, Limit: 5000})
		if err != nil {
			return ActionResult{}, pipelineFailure(err)
		}
		for _, c := range cases {
			if c.Terminal() && (e.C.Def.Retention == nil || c.Erased != nil || c.Hold != nil) {
				continue
			}
			scanned++
			next, res, err := e.Sweep(hookCtx, c)
			switch {
			case err != nil:
				failed++
				continue
			case res.Purge:
				if err := h.res.store.Delete(ctx.Context, c.ID); err == nil {
					purged++
				}
				continue
			case !res.Changed:
				continue
			}
			if err := h.res.store.Update(ctx.Context, next); err != nil {
				if errors.Is(err, pipeline.ErrConflict) {
					conflicts++
					continue
				}
				return ActionResult{}, pipelineFailure(err)
			}
			h.res.dispatch(hookCtx, e, next)
			changed++
		}
	}
	return h.out(map[string]any{"scanned": scanned, "changed": changed, "purged": purged, "conflicts": conflicts, "failed": failed})
}

// analytics reports on the cases the caller may see: its jurisdiction, and
// optionally one stage's cases or a created-at window (?from= ?to=).
func (h *pipelineHandler) analytics(ctx *ActionContext) (ActionResult, error) {
	e, err := h.engine("")
	if err != nil {
		return ActionResult{}, err
	}
	q := pipeline.Query{Pipeline: e.C.Def.Name, TenantID: ctx.TenantID, Limit: 5000}
	units, ok := h.res.jurisdiction(ctx.TenantID, ctx.Principal)
	if !ok {
		return ActionResult{}, permissionDenied("you are not assigned to any organisational unit")
	}
	q.OrgUnits = units
	cases, err := h.res.store.List(ctx.Context, q)
	if err != nil {
		return ActionResult{}, pipelineFailure(err)
	}
	var from, to time.Time
	if s := h.param(ctx, "from"); s != "" {
		if from, err = time.Parse(time.RFC3339, s); err != nil {
			return ActionResult{}, invalidInput("from must be an RFC 3339 time")
		}
	}
	if s := h.param(ctx, "to"); s != "" {
		if to, err = time.Parse(time.RFC3339, s); err != nil {
			return ActionResult{}, invalidInput("to must be an RFC 3339 time")
		}
	}
	kept := cases[:0]
	for _, c := range cases {
		if (!from.IsZero() && c.CreatedAt.Before(from)) || (!to.IsZero() && !c.CreatedAt.Before(to)) {
			continue
		}
		kept = append(kept, c)
	}
	return h.out(e.Analyze(kept, time.Now()))
}

// erase handles a right-to-erasure request: {identifiers: {path: value},
// mode: anonymize|purge, dry_run, reason}. Cases under legal hold are
// reported as blocked. Receipts reference the subject by a SHA-256, never by
// the personal data itself.
func (h *pipelineHandler) erase(ctx *ActionContext, hookCtx context.Context) (ActionResult, error) {
	e, err := h.engine("")
	if err != nil {
		return ActionResult{}, err
	}
	body, _ := h.body(ctx, "", "input").(map[string]any)
	raw, _ := body["identifiers"].(map[string]any)
	ids := map[string]string{}
	for k, v := range raw {
		if s := strings.TrimSpace(Stringify(v)); s != "" {
			ids[k] = s
		}
	}
	if len(ids) == 0 {
		return ActionResult{}, invalidInput("identifiers are required, e.g. {\"applicant.email\": \"...\"}")
	}
	mode := strings.ToLower(Stringify(body["mode"]))
	if mode == "" || mode == "<nil>" {
		mode = "anonymize"
	}
	if mode != "anonymize" && mode != "purge" {
		return ActionResult{}, invalidInput("mode must be anonymize or purge")
	}
	dryRun, _ := body["dry_run"].(bool)
	reason := strings.TrimSpace(Stringify(body["reason"]))
	if reason == "" || reason == "<nil>" {
		reason = "erasure request"
	}
	cases, err := h.res.store.List(ctx.Context, pipeline.Query{Pipeline: e.C.Def.Name, TenantID: ctx.TenantID, Limit: 5000})
	if err != nil {
		return ActionResult{}, pipelineFailure(err)
	}
	actor := actorOf(ctx.Principal)
	var receipts []any
	matched := 0
	for _, c := range cases {
		ok, err := e.MatchesSubject(c, ids)
		if err != nil {
			return ActionResult{}, pipelineFailure(err)
		}
		if !ok {
			continue
		}
		matched++
		receipt := map[string]any{"case_number": c.Number, "status": c.Status}
		switch {
		case c.Hold != nil:
			receipt["outcome"] = "blocked"
			receipt["reason"] = "legal hold: " + c.Hold.Reason
		case dryRun:
			receipt["outcome"] = "would_" + mode
		case mode == "purge":
			if err := h.res.store.Delete(ctx.Context, c.ID); err != nil {
				receipt["outcome"], receipt["reason"] = "failed", err.Error()
			} else {
				receipt["outcome"] = "purged"
			}
		default:
			next, err := e.Anonymize(hookCtx, c, actor, reason)
			if err == nil {
				err = h.res.store.Update(ctx.Context, next)
			}
			if err != nil {
				receipt["outcome"], receipt["reason"] = "failed", err.Error()
			} else {
				receipt["outcome"] = "anonymized"
				h.res.dispatch(hookCtx, e, next)
			}
		}
		receipts = append(receipts, receipt)
	}
	return h.out(map[string]any{
		"subject_ref": pipeline.SubjectRef(ids), "mode": mode, "dry_run": dryRun,
		"matched": matched, "receipts": receipts, "at": time.Now().UTC().Format(time.RFC3339),
	})
}

// bulk applies one operation to many cases: {ids: [...], op: act|node|work|
// note|hold, stage?, action?, node?, verb?, input: {...}}. Every case is
// loaded, authorised and saved on its own, so one failure never blocks the
// rest and nothing is applied that the caller could not do one by one.
func (h *pipelineHandler) bulk(ctx *ActionContext, hookCtx context.Context) (ActionResult, error) {
	body, _ := h.body(ctx, "", "input").(map[string]any)
	rawIDs, _ := body["ids"].([]any)
	if len(rawIDs) == 0 {
		return ActionResult{}, invalidInput("ids are required")
	}
	if len(rawIDs) > h.maxItems {
		return ActionResult{}, invalidInput("at most %d cases per request", h.maxItems)
	}
	op := Stringify(body["op"])
	switch op {
	case "act", "node", "work", "note", "hold":
	default:
		return ActionResult{}, invalidInput("op must be act, node, work, note or hold")
	}
	input, _ := body["input"].(map[string]any)
	if input == nil {
		input = map[string]any{}
	}
	// Route-level parameters (action, node, verb, op for work) may be given
	// at the top level of the bulk body.
	for _, key := range []string{"action", "node", "verb", "to", "reason", "comment"} {
		if v, ok := body[key]; ok {
			if _, set := input[key]; !set {
				input[key] = v
			}
		}
	}
	if v, ok := body["work_op"]; ok {
		input["op"] = v
	}
	applied, failed := 0, 0
	results := make([]any, 0, len(rawIDs))
	for _, raw := range rawIDs {
		id := Stringify(raw)
		result := map[string]any{"id": id}
		next, err := h.bulkOne(ctx, hookCtx, id, op, Stringify(body["stage"]), input)
		if err != nil {
			failed++
			result["outcome"] = "failed"
			result["error"] = err.Error()
		} else {
			applied++
			result["outcome"] = "applied"
			result["stage"], result["status"], result["revision"] = next.Stage, next.Status, next.Revision
		}
		results = append(results, result)
	}
	return h.out(map[string]any{"applied": applied, "failed": failed, "results": results})
}

func (h *pipelineHandler) bulkOne(ctx *ActionContext, hookCtx context.Context, id, op, stage string, input map[string]any) (*pipeline.Case, error) {
	c, err := h.res.store.Get(ctx.Context, id)
	if err != nil || c.TenantID != ctx.TenantID {
		return nil, notFound("case", id)
	}
	e, err := h.engine(c.Pipeline)
	if err != nil {
		return nil, err
	}
	actor := actorOf(ctx.Principal)
	if actor.ID != c.CreatedBy && !h.res.inScope(ctx.TenantID, ctx.Principal, c) {
		return nil, notFound("case", id)
	}
	if stage == "" || stage == "<nil>" {
		stage = c.Stage
	}
	next, _, err := h.apply(ctx, hookCtx, c, e, actor, stage, op, input)
	if err != nil {
		return nil, err
	}
	if err := h.commit(ctx, hookCtx, e, next); err != nil {
		return nil, err
	}
	return next, nil
}
