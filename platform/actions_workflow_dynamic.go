package platform

import (
	"fmt"
	"strings"
)

// registerDynamicWorkflowActions installs dynamic workflow authoring and lifecycle actions.
func registerDynamicWorkflowActions(r *Registry) {
	mustAction(r, "workflow.list", workflowListAction, ActionInfo{
		Family:       "workflow",
		Summary:      "List defined workflows and their status, versions, and block counts",
		ResourceKind: "workflow.manager",
		Provides:     "{ workflows: [...] }",
		Kind:         "read",
		Config: []ConfigField{
			{Name: "status", Type: "string", Summary: "Filter by status: draft, active, deprecated, archived"},
			{Name: "search", Type: "string", Summary: "Search term matching workflow ID or name"},
			{Name: "limit", Type: "int", Default: "50"},
			{Name: "offset", Type: "int", Default: "0"},
		},
	})

	mustAction(r, "workflow.get", workflowGetAction, ActionInfo{
		Family:       "workflow",
		Summary:      "Fetch full workflow definition including canonical BCL source",
		ResourceKind: "workflow.manager",
		Provides:     "{ workflow: { id, name, version, status, source_bcl, ... } }",
		Kind:         "read",
		Config: []ConfigField{
			{Name: "id", Type: "template", Summary: "Workflow ID"},
			{Name: "id_fact", Type: "fact", Summary: "Fact holding workflow ID"},
		},
	})

	mustAction(r, "workflow.create", workflowCreateAction, ActionInfo{
		Family:       "workflow",
		Summary:      "Create a new workflow definition from a BCL fragment with syntax validation",
		ResourceKind: "workflow.manager",
		Provides:     "{ workflow: { id, name, version, status, revision_id, ... } }",
		Kind:         "effect",
		Config: []ConfigField{
			{Name: "id", Type: "template", Summary: "Unique workflow ID; generated if empty"},
			{Name: "name", Type: "template", Required: true, Summary: "Display name"},
			{Name: "description", Type: "template"},
			{Name: "version", Type: "template", Default: "0.1.0"},
			{Name: "source_bcl", Type: "template", Summary: "BCL fragment source code"},
			{Name: "source_fact", Type: "fact", Summary: "Fact holding BCL fragment source"},
			{Name: "status", Type: "string", Default: "draft"},
			{Name: "metadata_fact", Type: "fact"},
		},
	})

	mustAction(r, "workflow.update", workflowUpdateAction, ActionInfo{
		Family:       "workflow",
		Summary:      "Update an existing workflow definition and validate its BCL fragment",
		ResourceKind: "workflow.manager",
		Provides:     "{ workflow: { id, name, version, status, revision_id, ... } }",
		Kind:         "effect",
		Config: []ConfigField{
			{Name: "id", Type: "template", Summary: "Workflow ID to update"},
			{Name: "id_fact", Type: "fact"},
			{Name: "name", Type: "template"},
			{Name: "description", Type: "template"},
			{Name: "version", Type: "template"},
			{Name: "source_bcl", Type: "template"},
			{Name: "source_fact", Type: "fact"},
			{Name: "status", Type: "string"},
			{Name: "metadata_fact", Type: "fact"},
		},
	})

	mustAction(r, "workflow.activate", workflowActivateAction, ActionInfo{
		Family:       "workflow",
		Summary:      "Promote a workflow revision to active status (triggers hot reload)",
		ResourceKind: "workflow.manager",
		Provides:     "{ activated: true, id: '...', revision_id: 1 }",
		Kind:         "effect",
		Config: []ConfigField{
			{Name: "id", Type: "template", Summary: "Workflow ID"},
			{Name: "id_fact", Type: "fact"},
			{Name: "revision_id", Type: "int", Summary: "Specific revision ID to activate (0 for latest)"},
			{Name: "revision_fact", Type: "fact"},
		},
	})

	mustAction(r, "workflow.validate", workflowValidateAction, ActionInfo{
		Family:       "workflow",
		Summary:      "Validate a BCL fragment and report syntax or schema diagnostics without modifying state",
		ResourceKind: "workflow.manager",
		Provides:     "{ valid: bool, diagnostics: [...], intents: [...], pipelines: [...], routes: [...] }",
		Kind:         "read",
		Config: []ConfigField{
			{Name: "source_bcl", Type: "template"},
			{Name: "source_fact", Type: "fact"},
		},
	})

	mustAction(r, "workflow.rollback", workflowRollbackAction, ActionInfo{
		Family:       "workflow",
		Summary:      "Roll back a workflow to a previous revision",
		ResourceKind: "workflow.manager",
		Provides:     "{ rolled_back: true, id: '...', revision_id: 1 }",
		Kind:         "effect",
		Config: []ConfigField{
			{Name: "id", Type: "template", Summary: "Workflow ID"},
			{Name: "id_fact", Type: "fact"},
			{Name: "revision_id", Type: "int", Required: true, Summary: "Target revision to roll back to"},
			{Name: "revision_fact", Type: "fact"},
		},
	})

	mustAction(r, "workflow.export", workflowExportAction, ActionInfo{
		Family:       "workflow",
		Summary:      "Export canonical BCL source for a workflow",
		ResourceKind: "workflow.manager",
		Provides:     "{ source: '...' }",
		Kind:         "read",
		Config: []ConfigField{
			{Name: "id", Type: "template", Summary: "Workflow ID"},
			{Name: "id_fact", Type: "fact"},
		},
	})

	mustAction(r, "workflow.clone", workflowCloneAction, ActionInfo{
		Family:       "workflow",
		Summary:      "Clone an existing workflow into a new draft workflow",
		ResourceKind: "workflow.manager",
		Provides:     "{ workflow: { id, name, version, status, revision_id, ... } }",
		Kind:         "effect",
		Config: []ConfigField{
			{Name: "id", Type: "template", Summary: "Source workflow ID"},
			{Name: "id_fact", Type: "fact"},
			{Name: "new_id", Type: "template", Summary: "ID for the clone"},
			{Name: "new_name", Type: "template", Summary: "Display name for the clone"},
		},
	})
}

var workflowListAction = ActionFactoryFunc(func(build BuildContext, spec NodeSpec) (Action, error) {
	if err := exactlyOneOutput(spec); err != nil {
		return nil, err
	}
	provides := spec.Provides[0]

	wm, err := requireResource[WorkflowManager](build, spec, "a workflow.manager resource")
	if err != nil {
		return nil, err
	}

	status := configString(spec.Config, "status", "")
	search := configString(spec.Config, "search", "")
	limit, _ := configInt(spec.Config, "limit", 50)
	offset, _ := configInt(spec.Config, "offset", 0)

	return ActionFunc(func(ctx *ActionContext) (ActionResult, error) {
		summaries, err := wm.ListWorkflows(ctx.Context, WorkflowFilter{
			Status: status,
			Search: search,
			Limit:  limit,
			Offset: offset,
		})
		if err != nil {
			return ActionResult{}, err
		}
		return ActionResult{Outputs: map[string]any{provides: map[string]any{"workflows": summaries}}}, nil
	}), nil
})

var workflowGetAction = ActionFactoryFunc(func(build BuildContext, spec NodeSpec) (Action, error) {
	if err := exactlyOneOutput(spec); err != nil {
		return nil, err
	}
	provides := spec.Provides[0]

	wm, err := requireResource[WorkflowManager](build, spec, "a workflow.manager resource")
	if err != nil {
		return nil, err
	}

	idTmpl, _ := configTemplate(spec.Config, "id", "")
	idFact := configString(spec.Config, "id_fact", "")

	return ActionFunc(func(ctx *ActionContext) (ActionResult, error) {
		id := resolveID(ctx, idFact, idTmpl)
		if id == "" {
			return ActionResult{}, invalidInput("workflow.get requires 'id' or 'id_fact'")
		}
		wf, err := wm.GetWorkflow(ctx.Context, id)
		if err != nil {
			return ActionResult{}, notFound("workflow", id)
		}
		return ActionResult{Outputs: map[string]any{provides: map[string]any{"workflow": wf}}}, nil
	}), nil
})

var workflowCreateAction = ActionFactoryFunc(func(build BuildContext, spec NodeSpec) (Action, error) {
	if err := exactlyOneOutput(spec); err != nil {
		return nil, err
	}
	provides := spec.Provides[0]

	wm, err := requireResource[WorkflowManager](build, spec, "a workflow.manager resource")
	if err != nil {
		return nil, err
	}

	idTmpl, _ := configTemplate(spec.Config, "id", "")
	nameTmpl, _ := configTemplate(spec.Config, "name", "")
	descTmpl, _ := configTemplate(spec.Config, "description", "")
	verTmpl, _ := configTemplate(spec.Config, "version", "0.1.0")
	sourceTmpl, _ := configTemplate(spec.Config, "source_bcl", "")
	sourceFact := configString(spec.Config, "source_fact", "")
	status := configString(spec.Config, "status", "draft")
	metaFact := configString(spec.Config, "metadata_fact", "")

	return ActionFunc(func(ctx *ActionContext) (ActionResult, error) {
		env := actionEnv(ctx)
		id := ""
		if idTmpl != nil {
			id, _ = idTmpl.Render(env)
		}
		name := ""
		if nameTmpl != nil {
			name, _ = nameTmpl.Render(env)
		}
		desc := ""
		if descTmpl != nil {
			desc, _ = descTmpl.Render(env)
		}
		version := "0.1.0"
		if verTmpl != nil {
			if v, err := verTmpl.Render(env); err == nil && v != "" {
				version = v
			}
		}

		source := ""
		if sourceFact != "" {
			if val, ok := resolvePath(ctx.Inputs, sourceFact); ok {
				source = fmt.Sprintf("%v", val)
			}
		}
		if source == "" && sourceTmpl != nil {
			source, _ = sourceTmpl.Render(env)
		}

		var metadata map[string]any
		if metaFact != "" {
			if val, ok := resolvePath(ctx.Inputs, metaFact); ok {
				if m, ok := val.(map[string]any); ok {
					metadata = m
				}
			}
		}

		created, err := wm.CreateWorkflow(ctx.Context, WorkflowDef{
			ID:          strings.TrimSpace(id),
			Name:        strings.TrimSpace(name),
			Description: strings.TrimSpace(desc),
			Version:     strings.TrimSpace(version),
			Status:      status,
			SourceBCL:   source,
			Metadata:    metadata,
		})
		if err != nil {
			return ActionResult{}, err
		}
		return ActionResult{Outputs: map[string]any{provides: map[string]any{"workflow": created}}}, nil
	}), nil
})

var workflowUpdateAction = ActionFactoryFunc(func(build BuildContext, spec NodeSpec) (Action, error) {
	if err := exactlyOneOutput(spec); err != nil {
		return nil, err
	}
	provides := spec.Provides[0]

	wm, err := requireResource[WorkflowManager](build, spec, "a workflow.manager resource")
	if err != nil {
		return nil, err
	}

	idTmpl, _ := configTemplate(spec.Config, "id", "")
	idFact := configString(spec.Config, "id_fact", "")
	nameTmpl, _ := configTemplate(spec.Config, "name", "")
	descTmpl, _ := configTemplate(spec.Config, "description", "")
	verTmpl, _ := configTemplate(spec.Config, "version", "")
	sourceTmpl, _ := configTemplate(spec.Config, "source_bcl", "")
	sourceFact := configString(spec.Config, "source_fact", "")
	status := configString(spec.Config, "status", "")
	metaFact := configString(spec.Config, "metadata_fact", "")

	return ActionFunc(func(ctx *ActionContext) (ActionResult, error) {
		env := actionEnv(ctx)
		id := resolveID(ctx, idFact, idTmpl)
		if id == "" {
			return ActionResult{}, invalidInput("workflow.update requires 'id' or 'id_fact'")
		}

		existing, err := wm.GetWorkflow(ctx.Context, id)
		if err != nil {
			return ActionResult{}, notFound("workflow", id)
		}

		if nameTmpl != nil {
			if n, err := nameTmpl.Render(env); err == nil && n != "" {
				existing.Name = strings.TrimSpace(n)
			}
		}
		if descTmpl != nil {
			if d, err := descTmpl.Render(env); err == nil && d != "" {
				existing.Description = strings.TrimSpace(d)
			}
		}
		if verTmpl != nil {
			if v, err := verTmpl.Render(env); err == nil && v != "" {
				existing.Version = strings.TrimSpace(v)
			}
		}
		if status != "" {
			existing.Status = status
		}

		source := ""
		if sourceFact != "" {
			if val, ok := resolvePath(ctx.Inputs, sourceFact); ok {
				source = fmt.Sprintf("%v", val)
			}
		}
		if source == "" && sourceTmpl != nil {
			source, _ = sourceTmpl.Render(env)
		}
		if source != "" {
			existing.SourceBCL = source
		}

		if metaFact != "" {
			if val, ok := resolvePath(ctx.Inputs, metaFact); ok {
				if m, ok := val.(map[string]any); ok {
					existing.Metadata = m
				}
			}
		}

		updated, err := wm.UpdateWorkflow(ctx.Context, id, existing)
		if err != nil {
			return ActionResult{}, err
		}
		return ActionResult{Outputs: map[string]any{provides: map[string]any{"workflow": updated}}}, nil
	}), nil
})

var workflowActivateAction = ActionFactoryFunc(func(build BuildContext, spec NodeSpec) (Action, error) {
	if err := exactlyOneOutput(spec); err != nil {
		return nil, err
	}
	provides := spec.Provides[0]

	wm, err := requireResource[WorkflowManager](build, spec, "a workflow.manager resource")
	if err != nil {
		return nil, err
	}

	idTmpl, _ := configTemplate(spec.Config, "id", "")
	idFact := configString(spec.Config, "id_fact", "")
	revID, _ := configInt(spec.Config, "revision_id", 0)
	revFact := configString(spec.Config, "revision_fact", "")

	return ActionFunc(func(ctx *ActionContext) (ActionResult, error) {
		id := resolveID(ctx, idFact, idTmpl)
		if id == "" {
			return ActionResult{}, invalidInput("workflow.activate requires 'id' or 'id_fact'")
		}
		revision := int64(revID)
		if revFact != "" {
			if val, ok := resolvePath(ctx.Inputs, revFact); ok {
				revision = int64(toInt(val))
			}
		}
		if err := wm.ActivateWorkflow(ctx.Context, id, revision); err != nil {
			return ActionResult{}, err
		}
		return ActionResult{Outputs: map[string]any{provides: map[string]any{"activated": true, "id": id, "revision_id": revision}}}, nil
	}), nil
})

var workflowValidateAction = ActionFactoryFunc(func(build BuildContext, spec NodeSpec) (Action, error) {
	if err := exactlyOneOutput(spec); err != nil {
		return nil, err
	}
	provides := spec.Provides[0]

	wm, err := requireResource[WorkflowManager](build, spec, "a workflow.manager resource")
	if err != nil {
		return nil, err
	}

	sourceTmpl, _ := configTemplate(spec.Config, "source_bcl", "")
	sourceFact := configString(spec.Config, "source_fact", "")

	return ActionFunc(func(ctx *ActionContext) (ActionResult, error) {
		env := actionEnv(ctx)
		source := ""
		if sourceFact != "" {
			if val, ok := resolvePath(ctx.Inputs, sourceFact); ok {
				source = fmt.Sprintf("%v", val)
			}
		}
		if source == "" && sourceTmpl != nil {
			source, _ = sourceTmpl.Render(env)
		}

		diags, err := wm.ValidateWorkflow(ctx.Context, WorkflowDef{SourceBCL: source})
		if err != nil {
			return ActionResult{}, err
		}

		valid := true
		for _, d := range diags {
			if d.Severity == SeverityError {
				valid = false
				break
			}
		}

		intents, pipelines, routes := extractWorkflowBlocks(source)

		return ActionResult{
			Outputs: map[string]any{
				provides: map[string]any{
					"valid":       valid,
					"diagnostics": diags,
					"intents":     intents,
					"pipelines":   pipelines,
					"routes":      routes,
				},
			},
		}, nil
	}), nil
})

var workflowRollbackAction = ActionFactoryFunc(func(build BuildContext, spec NodeSpec) (Action, error) {
	if err := exactlyOneOutput(spec); err != nil {
		return nil, err
	}
	provides := spec.Provides[0]

	wm, err := requireResource[WorkflowManager](build, spec, "a workflow.manager resource")
	if err != nil {
		return nil, err
	}

	idTmpl, _ := configTemplate(spec.Config, "id", "")
	idFact := configString(spec.Config, "id_fact", "")
	revID, _ := configInt(spec.Config, "revision_id", 0)
	revFact := configString(spec.Config, "revision_fact", "")

	return ActionFunc(func(ctx *ActionContext) (ActionResult, error) {
		id := resolveID(ctx, idFact, idTmpl)
		if id == "" {
			return ActionResult{}, invalidInput("workflow.rollback requires 'id' or 'id_fact'")
		}
		revision := int64(revID)
		if revFact != "" {
			if val, ok := resolvePath(ctx.Inputs, revFact); ok {
				revision = int64(toInt(val))
			}
		}
		if revision <= 0 {
			return ActionResult{}, invalidInput("workflow.rollback requires positive revision_id")
		}
		if err := wm.RollbackWorkflow(ctx.Context, id, revision); err != nil {
			return ActionResult{}, err
		}
		return ActionResult{Outputs: map[string]any{provides: map[string]any{"rolled_back": true, "id": id, "revision_id": revision}}}, nil
	}), nil
})

var workflowExportAction = ActionFactoryFunc(func(build BuildContext, spec NodeSpec) (Action, error) {
	if err := exactlyOneOutput(spec); err != nil {
		return nil, err
	}
	provides := spec.Provides[0]

	wm, err := requireResource[WorkflowManager](build, spec, "a workflow.manager resource")
	if err != nil {
		return nil, err
	}

	idTmpl, _ := configTemplate(spec.Config, "id", "")
	idFact := configString(spec.Config, "id_fact", "")

	return ActionFunc(func(ctx *ActionContext) (ActionResult, error) {
		id := resolveID(ctx, idFact, idTmpl)
		if id == "" {
			return ActionResult{}, invalidInput("workflow.export requires 'id' or 'id_fact'")
		}
		src, err := wm.ExportWorkflow(ctx.Context, id)
		if err != nil {
			return ActionResult{}, err
		}
		return ActionResult{Outputs: map[string]any{provides: map[string]any{"source": src}}}, nil
	}), nil
})

var workflowCloneAction = ActionFactoryFunc(func(build BuildContext, spec NodeSpec) (Action, error) {
	if err := exactlyOneOutput(spec); err != nil {
		return nil, err
	}
	provides := spec.Provides[0]

	wm, err := requireResource[WorkflowManager](build, spec, "a workflow.manager resource")
	if err != nil {
		return nil, err
	}

	idTmpl, _ := configTemplate(spec.Config, "id", "")
	idFact := configString(spec.Config, "id_fact", "")
	newIDTmpl, _ := configTemplate(spec.Config, "new_id", "")
	newNameTmpl, _ := configTemplate(spec.Config, "new_name", "")

	return ActionFunc(func(ctx *ActionContext) (ActionResult, error) {
		env := actionEnv(ctx)
		id := resolveID(ctx, idFact, idTmpl)
		if id == "" {
			return ActionResult{}, invalidInput("workflow.clone requires 'id' or 'id_fact'")
		}

		newID := ""
		if newIDTmpl != nil {
			newID, _ = newIDTmpl.Render(env)
		}
		newName := ""
		if newNameTmpl != nil {
			newName, _ = newNameTmpl.Render(env)
		}

		cloned, err := wm.CloneWorkflow(ctx.Context, id, strings.TrimSpace(newID), strings.TrimSpace(newName))
		if err != nil {
			return ActionResult{}, err
		}
		return ActionResult{Outputs: map[string]any{provides: map[string]any{"workflow": cloned}}}, nil
	}), nil
})

func resolveID(ctx *ActionContext, idFact string, idTmpl *Template) string {
	if idFact != "" {
		if val, ok := resolvePath(ctx.Inputs, idFact); ok {
			return fmt.Sprintf("%v", val)
		}
	}
	if idTmpl != nil {
		rendered, err := idTmpl.Render(actionEnv(ctx))
		if err == nil {
			return strings.TrimSpace(rendered)
		}
	}
	return ""
}
