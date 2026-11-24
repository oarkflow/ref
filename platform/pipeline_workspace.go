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
	// A resource may serve several pipelines: settings belong to the one named.
	e, err := h.engine(h.param(ctx, "pipeline"))
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

// lookupSources shows what the lookup_resource data source yields for a
// workspace, one entry per set it defines, so an administrator sees which
// reference data is managed in a database or API rather than in the workspace.
// A caller may look only at units inside its org scope.
func (h *pipelineHandler) lookupSources(ctx *ActionContext) (ActionResult, error) {
	res := h.res
	unit, err := h.sourceUnit(ctx)
	if err != nil {
		return ActionResult{}, err
	}
	sets := []map[string]any{}
	if res.lookups != nil {
		c := &pipeline.Case{TenantID: ctx.TenantID, OrgUnit: unit}
		for _, name := range res.lookups.Sets() {
			row := map[string]any{"set": name, "kind": res.lookups.Kind(name), "merge": res.lookups.Merges(name), "writable": res.lookups.Writable(name), "entries": []map[string]any{}}
			entries := []map[string]any{}
			items, err := res.lookups.Entries(ctx.Context, name, res.workspaceFacts(c))
			if err != nil {
				row["error"] = err.Error()
			}
			for _, it := range items {
				entries = append(entries, map[string]any{"code": it.Option.Value, "label": it.Option.Label, "attrs": it.Attrs, "source": it.Source})
			}
			row["entries"] = entries
			sets = append(sets, row)
		}
	}
	return h.out(map[string]any{"unit": unit, "sets": sets})
}

// sourceUnit is the unit a data-source operation acts on, checked against the caller's org scope.
func (h *pipelineHandler) sourceUnit(ctx *ActionContext) (string, error) {
	if h.res.org == nil {
		return "", invalidInput("this pipeline resource has no org_resource, so there are no workspaces")
	}
	unit := h.param(ctx, "unit")
	if unit == "" {
		return "", invalidInput("a unit is required")
	}
	snap := h.res.org.Snapshot(ctx.TenantID)
	if !h.res.org.ScopeFor(ctx.Principal, snap.Tree).Covers(snap.Tree, unit) {
		return "", outOfScope(unit)
	}
	return unit, nil
}

// lookupSourceWrite adds or removes a row of a data-source set that declares
// upsert/remove statements, for a unit inside the caller's org scope.
func (h *pipelineHandler) lookupSourceWrite(ctx *ActionContext) (ActionResult, error) {
	if err := requireOperator(ctx, configStrings(h.spec.Config, "roles"), "pipeline.lookup_source_write"); err != nil {
		return ActionResult{}, err
	}
	unit, err := h.sourceUnit(ctx)
	if err != nil {
		return ActionResult{}, err
	}
	res := h.res
	raw, _ := resolvePath(ctx.Inputs, configString(h.spec.Config, "data_fact", "input"))
	in, _ := raw.(map[string]any)
	set := strings.TrimSpace(Stringify(in["set"]))
	if res.lookups == nil || !res.lookups.Has(set) {
		return ActionResult{}, invalidInput("%q is not a data-source set", set)
	}
	op := strings.ToLower(configString(h.spec.Config, "operation", "upsert"))
	code := strings.TrimSpace(Stringify(in["code"]))
	if code == "" {
		return ActionResult{}, invalidInput("a code is required")
	}
	vals := map[string]any{"code": code, "label": strings.TrimSpace(Stringify(in["label"])), "workspace": unit, "attrs": in["attrs"]}
	if n, ok := in["sort"].(float64); ok {
		vals["sort"] = int64(n)
	} else {
		vals["sort"] = int64(0)
	}
	if op == "upsert" && Stringify(vals["label"]) == "" {
		return ActionResult{}, invalidInput("a label is required")
	}
	if err := res.lookups.Write(ctx.Context, set, op, vals); err != nil {
		return ActionResult{}, invalidInput("%s", err.Error())
	}
	return h.out(map[string]any{"set": set, "unit": unit, "code": code, "operation": op})
}
