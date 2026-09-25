package platform

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/oarkflow/ref/hierarchy"
)

// Organisation actions: scope checks, tree queries and administration for an
// org.hierarchy resource, plus resolution and administration of the reference
// data configured along it.
//
// Every action is scope-aware by default. A principal assigned to a district
// sees that district and everything below it — never a sibling district or the
// state above — unless it holds one of the resource's global_roles. Checking
// scope here, at the action, rather than trusting a UI to hide what a user
// should not reach, is what makes the hierarchy a security boundary.

func registerOrgActions(r *Registry) {
	mustAction(r, "org.scope", ActionFactoryFunc(buildOrgScope), ActionInfo{
		Family:  "organisation",
		Summary: "Resolve the caller's organisational scope and optionally require a target unit to be inside it",
		Config: []ConfigField{
			{Name: "target", Type: "string", Summary: "Fact path of the unit the request acts on, e.g. input.org_unit_id; it must be within the caller's scope"},
			{Name: "require_target", Type: "bool", Default: "false", Summary: "Fail when the target path is missing"},
			{Name: "include_ids", Type: "bool", Default: "false", Summary: "Publish every unit id in scope (can be large)"},
		},
		Kind: "read",
	})
	mustAction(r, "org.query", ActionFactoryFunc(buildOrgQuery), ActionInfo{
		Family:  "organisation",
		Summary: "Read units: get, children, descendants, ancestors, tree, level or roots",
		Config: []ConfigField{
			{Name: "operation", Type: "string", Required: true, Summary: "get | children | descendants | ancestors | tree | level | roots"},
			{Name: "id_fact", Type: "string", Summary: "Fact path of the unit id; defaults to the caller's scope roots for tree/level/roots"},
			{Name: "level", Type: "string", Summary: "Level name for operation level (or level_fact)"},
			{Name: "level_fact", Type: "string"},
			{Name: "depth", Type: "int", Default: "-1", Summary: "Levels below the unit to render for operation tree (-1 = all)"},
			{Name: "scoped", Type: "bool", Default: "true", Summary: "Restrict results to the caller's scope"},
		},
		Kind: "read",
	})
	mustAction(r, "org.write", ActionFactoryFunc(buildOrgWrite), ActionInfo{
		Family:  "organisation",
		Summary: "Create, update, move or delete a unit within the caller's scope",
		Config: []ConfigField{
			{Name: "operation", Type: "string", Required: true, Summary: "upsert | move | delete"},
			{Name: "input_fact", Type: "string", Default: "input", Summary: "Fact holding { id parent_id level code name attrs }"},
			{Name: "cascade", Type: "bool", Default: "false", Summary: "delete: remove the whole subtree instead of refusing"},
		},
		Kind: "effect",
	})
	mustAction(r, "lookup.resolve", ActionFactoryFunc(buildLookupResolve), ActionInfo{
		Family:  "organisation",
		Summary: "Resolve a reference-data set for a unit, workspace and department, applying inherited overrides",
		Config: []ConfigField{
			{Name: "set", Type: "string", Summary: "Lookup set name (or set_fact)"},
			{Name: "set_fact", Type: "string"},
			{Name: "node_fact", Type: "string", Summary: "Fact path of the unit; defaults to the caller's single assigned unit"},
			{Name: "workspace_fact", Type: "string", Default: "principal.claims.workspace"},
			{Name: "department_fact", Type: "string", Default: "principal.claims.department"},
			{Name: "include_disabled", Type: "bool", Default: "false"},
			{Name: "scoped", Type: "bool", Default: "true"},
			{Name: "require_code", Type: "string", Summary: "Fact path of a submitted value that must be one of the resolved codes, e.g. input.service"},
		},
		Kind: "read",
	})
	mustAction(r, "lookup.write", ActionFactoryFunc(buildLookupWrite), ActionInfo{
		Family:  "organisation",
		Summary: "Upsert or delete a reference-data definition at a unit the caller administers",
		Config: []ConfigField{
			{Name: "operation", Type: "string", Required: true, Summary: "upsert | delete"},
			{Name: "input_fact", Type: "string", Default: "input", Summary: "Fact holding { set code label node_id workspace department sort disabled attrs }"},
		},
		Kind: "effect",
	})
}

// orgRoot exposes the node's facts plus the principal for path resolution, so
// a config path can read either `input.org_unit_id` or `principal.claims.x`.
func orgRoot(ctx *ActionContext) map[string]any {
	root := make(map[string]any, len(ctx.Inputs)+1)
	for k, v := range ctx.Inputs {
		root[k] = v
	}
	if _, shadowed := root["principal"]; !shadowed {
		root["principal"] = principalMap(ctx.Principal)
	}
	return root
}

func orgPathString(ctx *ActionContext, path string) (string, bool) {
	if path == "" {
		return "", false
	}
	v, ok := resolvePath(orgRoot(ctx), path)
	if !ok || v == nil {
		return "", false
	}
	s := strings.TrimSpace(Stringify(v))
	return s, s != ""
}

func orgResource(build BuildContext, spec NodeSpec) (*OrgHierarchy, error) {
	return requireResource[*OrgHierarchy](build, spec, "an org.hierarchy resource")
}

// orgFailure maps hierarchy errors onto platform failures.
func orgFailure(err error) error {
	switch {
	case errors.Is(err, hierarchy.ErrNotFound), errors.Is(err, hierarchy.ErrMissingParent):
		return notFoundOrMessage(err.Error())
	case errors.Is(err, hierarchy.ErrDuplicate), errors.Is(err, hierarchy.ErrHasChildren), errors.Is(err, hierarchy.ErrCycle):
		return conflict("%s", err.Error())
	case errors.Is(err, hierarchy.ErrLevel), errors.Is(err, hierarchy.ErrInvalidID):
		return invalidInput("%s", err.Error())
	}
	return databaseFailure(err)
}

func outOfScope(id string) error {
	// Same message whether the unit is missing or merely out of scope: telling
	// a caller that a unit exists outside its jurisdiction is a disclosure.
	return intentPermission(fmt.Sprintf("organisational unit %q is outside your scope", id))
}

func intentPermission(message string) error { return permissionDenied(message) }

func nodeView(tree *hierarchy.Tree, n hierarchy.Node) map[string]any {
	out := map[string]any{
		"id": n.ID, "parent_id": n.ParentID, "level": n.Level, "code": n.Code, "name": n.Name,
		"depth": tree.Depth(n.ID), "path": tree.MaterializedPath(n.ID),
		"has_children": len(tree.Children(n.ID)) > 0,
	}
	if n.Attrs != nil {
		out["attrs"] = n.Attrs
	}
	return out
}

func nodeViews(tree *hierarchy.Tree, ids []string) []any {
	out := make([]any, 0, len(ids))
	for _, id := range ids {
		if n, ok := tree.Get(id); ok {
			out = append(out, nodeView(tree, n))
		}
	}
	return out
}

func buildOrgScope(build BuildContext, spec NodeSpec) (Action, error) {
	h, err := orgResource(build, spec)
	if err != nil {
		return nil, err
	}
	if err := rejectUnknownConfig("org.scope", spec.Config, "target", "require_target", "include_ids"); err != nil {
		return nil, fmt.Errorf("node %q: %w", spec.Name, err)
	}
	target := configString(spec.Config, "target", "")
	requireTarget := configBool(spec.Config, "require_target", false)
	includeIDs := configBool(spec.Config, "include_ids", false)
	return ActionFunc(func(ctx *ActionContext) (ActionResult, error) {
		snap := h.Snapshot(ctx.TenantID)
		scope := h.ScopeFor(ctx.Principal, snap.Tree)
		if !scope.Global && len(scope.Assigned) == 0 {
			return ActionResult{}, permissionDenied("you are not assigned to any organisational unit")
		}
		out := map[string]any{"global": scope.Global, "assigned": nodeViews(snap.Tree, scope.Assigned)}
		assignedIDs := make([]any, 0, len(scope.Assigned))
		for _, id := range scope.Assigned {
			assignedIDs = append(assignedIDs, id)
		}
		out["assigned_ids"] = assignedIDs
		if id, ok := orgPathString(ctx, target); ok {
			if !scope.Covers(snap.Tree, id) {
				return ActionResult{}, outOfScope(id)
			}
			n, _ := snap.Tree.Get(id)
			view := nodeView(snap.Tree, n)
			var ancestors []any
			for _, a := range snap.Tree.Ancestors(id) {
				ancestors = append(ancestors, nodeView(snap.Tree, a))
			}
			view["ancestors"] = ancestors
			out["target"] = view
		} else if requireTarget {
			return ActionResult{}, invalidInput("an organisational unit is required (%s)", target)
		}
		if includeIDs {
			ids := snap.Tree.ScopeIDs(scope.Assigned)
			list := make([]any, len(ids))
			for i, id := range ids {
				list[i] = id
			}
			out["ids"] = list
		}
		return acknowledgement(spec, out), nil
	}), nil
}

func buildOrgQuery(build BuildContext, spec NodeSpec) (Action, error) {
	h, err := orgResource(build, spec)
	if err != nil {
		return nil, err
	}
	if err := rejectUnknownConfig("org.query", spec.Config, "operation", "id_fact", "level", "level_fact", "depth", "scoped"); err != nil {
		return nil, fmt.Errorf("node %q: %w", spec.Name, err)
	}
	operation := strings.ToLower(configString(spec.Config, "operation", ""))
	switch operation {
	case "get", "children", "descendants", "ancestors", "tree", "level", "roots":
	default:
		return nil, fmt.Errorf("node %q: org.query operation must be get, children, descendants, ancestors, tree, level or roots", spec.Name)
	}
	if err := exactlyOneOutput(spec); err != nil {
		return nil, err
	}
	idFact := configString(spec.Config, "id_fact", "")
	if idFact == "" && (operation == "get" || operation == "children" || operation == "descendants" || operation == "ancestors") {
		return nil, fmt.Errorf("node %q: org.query %s needs config.id_fact", spec.Name, operation)
	}
	level := configString(spec.Config, "level", "")
	levelFact := configString(spec.Config, "level_fact", "")
	if operation == "level" && level == "" && levelFact == "" {
		return nil, fmt.Errorf("node %q: org.query level needs config.level or config.level_fact", spec.Name)
	}
	depth, err := configInt(spec.Config, "depth", -1)
	if err != nil {
		return nil, fmt.Errorf("node %q: %w", spec.Name, err)
	}
	scoped := configBool(spec.Config, "scoped", true)

	return ActionFunc(func(ctx *ActionContext) (ActionResult, error) {
		snap := h.Snapshot(ctx.TenantID)
		tree := snap.Tree
		scope := OrgScope{Global: true, Assigned: tree.Roots()}
		if scoped {
			scope = h.ScopeFor(ctx.Principal, tree)
		}
		id, hasID := orgPathString(ctx, idFact)
		if hasID && !scope.Covers(tree, id) {
			if !tree.Has(id) && !scoped {
				return ActionResult{}, notFound("organisational unit", id)
			}
			return ActionResult{}, outOfScope(id)
		}
		roots := scope.Assigned
		if hasID {
			roots = []string{id}
		}
		switch operation {
		case "get":
			n, _ := tree.Get(id)
			return singleOutput(spec, nodeView(tree, n)), nil
		case "children":
			return singleOutput(spec, nodeViews(tree, tree.Children(id))), nil
		case "descendants":
			return singleOutput(spec, nodeViews(tree, tree.Descendants(id, false))), nil
		case "ancestors":
			// Ancestors above a user's own scope are still returned: a
			// municipality clerk needs to see "Kathmandu > Bagmati > Nepal" as
			// breadcrumbs. They are names, not data.
			var out []any
			for _, a := range tree.Ancestors(id) {
				out = append(out, nodeView(tree, a))
			}
			return singleOutput(spec, out), nil
		case "roots":
			return singleOutput(spec, nodeViews(tree, roots)), nil
		case "level":
			name := level
			if v, ok := orgPathString(ctx, levelFact); ok {
				name = v
			}
			var ids []string
			for _, r := range roots {
				ids = append(ids, tree.AtLevel(name, r)...)
			}
			return singleOutput(spec, nodeViews(tree, ids)), nil
		default: // tree
			views := make([]any, 0, len(roots))
			for _, r := range roots {
				if v, ok := tree.View(r, depth); ok {
					views = append(views, v)
				}
			}
			if hasID && len(views) == 1 {
				return singleOutput(spec, views[0]), nil
			}
			return singleOutput(spec, views), nil
		}
	}), nil
}

func buildOrgWrite(build BuildContext, spec NodeSpec) (Action, error) {
	h, err := orgResource(build, spec)
	if err != nil {
		return nil, err
	}
	if err := rejectUnknownConfig("org.write", spec.Config, "operation", "input_fact", "cascade"); err != nil {
		return nil, fmt.Errorf("node %q: %w", spec.Name, err)
	}
	operation := strings.ToLower(configString(spec.Config, "operation", ""))
	if operation != "upsert" && operation != "move" && operation != "delete" {
		return nil, fmt.Errorf("node %q: org.write operation must be upsert, move or delete", spec.Name)
	}
	inputFact := configString(spec.Config, "input_fact", "input")
	cascade := configBool(spec.Config, "cascade", false)

	return ActionFunc(func(ctx *ActionContext) (ActionResult, error) {
		raw, ok := resolvePath(ctx.Inputs, inputFact)
		record, isMap := raw.(map[string]any)
		if !ok || !isMap {
			return ActionResult{}, invalidInput("a unit object is required")
		}
		snap := h.Snapshot(ctx.TenantID)
		tree := snap.Tree
		scope := h.ScopeFor(ctx.Principal, tree)
		id := strings.TrimSpace(Stringify(record["id"]))
		if id == "" {
			return ActionResult{}, invalidInput("a unit id is required")
		}

		// A caller may change a unit only inside its scope, and may not
		// create, move or delete one of its own scope roots — that would let
		// a district officer promote their district out from under the state.
		canTouch := func(unit string) bool {
			if scope.Global {
				return true
			}
			return tree.Covers(scope.Assigned, unit) && !slices.Contains(scope.Assigned, unit)
		}
		switch operation {
		case "delete":
			if !tree.Has(id) {
				return ActionResult{}, outOfScope(id)
			}
			if !canTouch(id) {
				return ActionResult{}, outOfScope(id)
			}
			removed, err := h.RemoveNode(ctx.Context, ctx.TenantID, id, cascade)
			if err != nil {
				return ActionResult{}, orgFailure(err)
			}
			return acknowledgement(spec, map[string]any{"deleted": removed}), nil
		case "move":
			parent := strings.TrimSpace(firstString(record, "parent_id", "parent"))
			if !tree.Has(id) || !canTouch(id) {
				return ActionResult{}, outOfScope(id)
			}
			if parent == "" && !scope.Global {
				return ActionResult{}, permissionDenied("only a global administrator can make a unit a root")
			}
			if parent != "" && !scope.Covers(tree, parent) {
				return ActionResult{}, outOfScope(parent)
			}
			next, err := h.UpsertNode(ctx.Context, ctx.TenantID, withParent(tree, id, parent))
			if err != nil {
				return ActionResult{}, orgFailure(err)
			}
			n, _ := next.Get(id)
			return acknowledgement(spec, nodeView(next, n)), nil
		default: // upsert
			n, err := orgNodeFromMap(record)
			if err != nil {
				return ActionResult{}, invalidInput("%s", err.Error())
			}
			if existing, found := tree.Get(id); found {
				if !canTouch(id) {
					return ActionResult{}, outOfScope(id)
				}
				if _, sent := record["parent_id"]; !sent {
					if _, sent := record["parent"]; !sent {
						n.ParentID = existing.ParentID
					}
				}
				if _, sent := record["attrs"]; !sent {
					n.Attrs = existing.Attrs
				}
			}
			if n.ParentID == "" && !scope.Global {
				return ActionResult{}, permissionDenied("only a global administrator can create a root unit")
			}
			if n.ParentID != "" && !scope.Covers(tree, n.ParentID) {
				return ActionResult{}, outOfScope(n.ParentID)
			}
			next, err := h.UpsertNode(ctx.Context, ctx.TenantID, n)
			if err != nil {
				return ActionResult{}, orgFailure(err)
			}
			stored, _ := next.Get(id)
			return acknowledgement(spec, nodeView(next, stored)), nil
		}
	}), nil
}

func withParent(tree *hierarchy.Tree, id, parent string) hierarchy.Node {
	n, _ := tree.Get(id)
	n.ParentID = parent
	return n
}

func buildLookupResolve(build BuildContext, spec NodeSpec) (Action, error) {
	h, err := orgResource(build, spec)
	if err != nil {
		return nil, err
	}
	if err := rejectUnknownConfig("lookup.resolve", spec.Config,
		"set", "set_fact", "node_fact", "workspace_fact", "department_fact", "include_disabled", "scoped", "require_code"); err != nil {
		return nil, fmt.Errorf("node %q: %w", spec.Name, err)
	}
	if err := exactlyOneOutput(spec); err != nil {
		return nil, err
	}
	set := configString(spec.Config, "set", "")
	setFact := configString(spec.Config, "set_fact", "")
	if set == "" && setFact == "" {
		return nil, fmt.Errorf("node %q: lookup.resolve needs config.set or config.set_fact", spec.Name)
	}
	nodeFact := configString(spec.Config, "node_fact", "")
	workspaceFact := configString(spec.Config, "workspace_fact", "principal.claims.workspace")
	departmentFact := configString(spec.Config, "department_fact", "principal.claims.department")
	includeDisabled := configBool(spec.Config, "include_disabled", false)
	scoped := configBool(spec.Config, "scoped", true)
	requireCode := configString(spec.Config, "require_code", "")

	return ActionFunc(func(ctx *ActionContext) (ActionResult, error) {
		snap := h.Snapshot(ctx.TenantID)
		q := hierarchy.LookupQuery{Set: set, IncludeDisabled: includeDisabled}
		if v, ok := orgPathString(ctx, setFact); ok {
			q.Set = v
		}
		if q.Set == "" {
			return ActionResult{}, invalidInput("a lookup set name is required")
		}
		q.Workspace, _ = orgPathString(ctx, workspaceFact)
		q.Department, _ = orgPathString(ctx, departmentFact)
		node, hasNode := orgPathString(ctx, nodeFact)
		if scoped {
			scope := h.ScopeFor(ctx.Principal, snap.Tree)
			if !hasNode && !scope.Global && len(scope.Assigned) == 1 {
				node, hasNode = scope.Assigned[0], true
			}
			if hasNode && !scope.Covers(snap.Tree, node) {
				return ActionResult{}, outOfScope(node)
			}
		} else if hasNode && !snap.Tree.Has(node) {
			return ActionResult{}, notFound("organisational unit", node)
		}
		if hasNode {
			q.NodeID = node
		}
		items := snap.Lookups.Resolve(snap.Tree, q)
		if requireCode != "" {
			code, _ := orgPathString(ctx, requireCode)
			valid := false
			for _, item := range items {
				if item.Code == code && !(item.Attrs != nil && item.Attrs["disabled"] == true) {
					valid = true
					break
				}
			}
			if !valid {
				return ActionResult{}, invalidInput("%q is not a valid %s here", code, q.Set)
			}
		}
		out := make([]any, len(items))
		for i, item := range items {
			row := map[string]any{"code": item.Code, "label": item.Label, "sort": item.Sort, "source": item.Source}
			if item.Attrs != nil {
				row["attrs"] = item.Attrs
			}
			if item.Workspace != "" {
				row["workspace"] = item.Workspace
			}
			if item.Department != "" {
				row["department"] = item.Department
			}
			out[i] = row
		}
		return singleOutput(spec, out), nil
	}), nil
}

func buildLookupWrite(build BuildContext, spec NodeSpec) (Action, error) {
	h, err := orgResource(build, spec)
	if err != nil {
		return nil, err
	}
	if err := rejectUnknownConfig("lookup.write", spec.Config, "operation", "input_fact"); err != nil {
		return nil, fmt.Errorf("node %q: %w", spec.Name, err)
	}
	operation := strings.ToLower(configString(spec.Config, "operation", ""))
	if operation != "upsert" && operation != "delete" {
		return nil, fmt.Errorf("node %q: lookup.write operation must be upsert or delete", spec.Name)
	}
	inputFact := configString(spec.Config, "input_fact", "input")
	return ActionFunc(func(ctx *ActionContext) (ActionResult, error) {
		raw, _ := resolvePath(ctx.Inputs, inputFact)
		record, ok := raw.(map[string]any)
		if !ok {
			return ActionResult{}, invalidInput("a lookup entry object is required")
		}
		e, err := lookupEntryFromMap(record)
		if err != nil {
			return ActionResult{}, invalidInput("%s", err.Error())
		}
		snap := h.Snapshot(ctx.TenantID)
		scope := h.ScopeFor(ctx.Principal, snap.Tree)
		if e.NodeID == "" && !scope.Global {
			return ActionResult{}, permissionDenied("only a global administrator can change global reference data")
		}
		if e.NodeID != "" && !scope.Covers(snap.Tree, e.NodeID) {
			return ActionResult{}, outOfScope(e.NodeID)
		}
		if operation == "delete" {
			found, err := h.DeleteLookup(ctx.Context, ctx.TenantID, e)
			if err != nil {
				return ActionResult{}, databaseFailure(err)
			}
			if !found {
				return ActionResult{}, notFound("lookup entry", e.Set+"/"+e.Code)
			}
			return acknowledgement(spec, map[string]any{"deleted": true}), nil
		}
		if err := h.UpsertLookup(ctx.Context, ctx.TenantID, e); err != nil {
			return ActionResult{}, databaseFailure(err)
		}
		return acknowledgement(spec, e), nil
	}), nil
}
