package platform

import (
	"encoding/json"
	"maps"
	"strings"

	"github.com/oarkflow/ref/pipeline"
)

// Workspace settings of a pipeline live on the org unit that owns them, in
// the unit's attrs under "pipeline:<name>": an Overrides document. A case
// resolves them from its unit up to the root, the nearest unit winning per
// stage and per field, so a company sets a default, a facility refines it and
// a work item refines it again.

func workspaceKey(pipelineName string) string { return "pipeline:" + pipelineName }

// workspaceOverrides resolves the overrides in force for a case.
func (p *PipelineCases) workspaceOverrides(name string, c *pipeline.Case) *pipeline.Overrides {
	if p.org == nil || c == nil || c.OrgUnit == "" {
		return nil
	}
	snap := p.org.Snapshot(c.TenantID)
	var merged *pipeline.Overrides
	for _, id := range snap.Tree.PathIDs(c.OrgUnit) { // root first, so deeper units win
		n, ok := snap.Tree.Get(id)
		if !ok {
			continue
		}
		own := decodeOverrides(n.Attrs[workspaceKey(name)])
		if own == nil {
			continue
		}
		merged = mergeOverrides(merged, own)
	}
	return merged
}

func decodeOverrides(raw any) *pipeline.Overrides {
	if raw == nil {
		return nil
	}
	var data []byte
	switch v := raw.(type) {
	case string:
		data = []byte(v)
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return nil
		}
		data = b
	}
	var ov pipeline.Overrides
	if err := json.Unmarshal(data, &ov); err != nil {
		return nil
	}
	return &ov
}

// mergeOverrides lays child over base: a stage's or a field's entry in child
// replaces the base entry for that key, value by value for stages.
func mergeOverrides(base, child *pipeline.Overrides) *pipeline.Overrides {
	out := &pipeline.Overrides{}
	if base != nil {
		out.Stages = maps.Clone(base.Stages)
		out.Fields = maps.Clone(base.Fields)
	}
	if out.Stages == nil && len(child.Stages) > 0 {
		out.Stages = map[string]pipeline.StageOverride{}
	}
	for k, v := range child.Stages {
		cur := out.Stages[k]
		if v.Due != "" {
			cur.Due = v.Due
		}
		if v.Duration != "" {
			cur.Duration = v.Duration
		}
		if v.WarnBefore != "" {
			cur.WarnBefore = v.WarnBefore
		}
		if v.Calendar != "" {
			cur.Calendar = v.Calendar
		}
		out.Stages[k] = cur
	}
	if out.Fields == nil && len(child.Fields) > 0 {
		out.Fields = map[string]pipeline.FieldOverride{}
	}
	maps.Copy(out.Fields, child.Fields)
	return out
}

// workspace handles the workspace_get and workspace_set operations: read or
// replace the overrides a unit holds for a pipeline. A caller may touch only
// units inside its scope: the units it is assigned to, or those in the claim
// scope_claim names. Unlike org.write, a caller may edit the settings of the
// unit it is assigned to, because that is where a company or facility sets its
// defaults; it can never reach the units above it.
func (h *pipelineHandler) workspace(ctx *ActionContext) (ActionResult, error) {
	e, err := h.engine("")
	if err != nil {
		return ActionResult{}, err
	}
	if h.res.org == nil {
		return ActionResult{}, invalidInput("this pipeline resource has no org_resource, so there are no workspaces")
	}
	unit := h.param(ctx, "unit")
	if unit == "" {
		return ActionResult{}, invalidInput("a unit is required")
	}
	snap := h.res.org.Snapshot(ctx.TenantID)
	scope := h.res.org.ScopeFor(ctx.Principal, snap.Tree)
	if claim := configString(h.spec.Config, "scope_claim", ""); claim != "" {
		// Editing settings is an administrator's act, so its scope can come
		// from its own claim (the units the caller administers) instead of
		// the units the caller merely works in.
		var assigned []string
		if ctx.Principal.Claims != nil {
			assigned = stringList(ctx.Principal.Claims[claim])
		}
		scope = OrgScope{Global: scope.Global, Assigned: snap.Tree.NormalizeScope(assigned)}
	}
	if !scope.Covers(snap.Tree, unit) {
		return ActionResult{}, outOfScope(unit)
	}
	key := workspaceKey(e.C.Def.Name)
	node, _ := snap.Tree.Get(unit)

	if h.op == "workspace_get" {
		view := map[string]any{"unit": unit, "own": decodeOverrides(node.Attrs[key])}
		view["effective"] = h.res.workspaceOverrides(e.C.Def.Name, &pipeline.Case{TenantID: ctx.TenantID, OrgUnit: unit})
		return h.out(view)
	}

	raw, ok := resolvePath(ctx.Inputs, configString(h.spec.Config, "data_fact", "input.overrides"))
	if !ok {
		return ActionResult{}, invalidInput("overrides are required")
	}
	ov := decodeOverrides(raw)
	if ov == nil {
		return ActionResult{}, invalidInput("overrides must be an object with stages and fields")
	}
	if problems := e.C.CheckOverrides(ov); len(problems) > 0 {
		return ActionResult{}, invalidInput("%s", strings.Join(problems, "; "))
	}
	attrs := maps.Clone(node.Attrs)
	if attrs == nil {
		attrs = map[string]any{}
	}
	if len(ov.Stages) == 0 && len(ov.Fields) == 0 {
		delete(attrs, key)
	} else {
		attrs[key] = ov
	}
	node.Attrs = attrs
	next, err := h.res.org.UpsertNode(ctx.Context, ctx.TenantID, node)
	if err != nil {
		return ActionResult{}, orgFailure(err)
	}
	saved, _ := next.Get(unit)
	return h.out(map[string]any{"unit": unit, "own": decodeOverrides(saved.Attrs[key])})
}
