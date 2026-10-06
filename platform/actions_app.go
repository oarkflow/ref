package platform

import (
	"fmt"
	"strings"
)

// registerAppActions installs application lifecycle actions.
func registerAppActions(r *Registry) {
	mustAction(r, "app.list", appListAction, ActionInfo{
		Family:       "application",
		Summary:      "List registered applications and their operational status and block counts",
		ResourceKind: "app.manager",
		Provides:     "{ apps: [...] }",
		Kind:         "read",
		Config: []ConfigField{
			{Name: "status", Type: "string", Summary: "draft, active, inactive, archived, etc."},
			{Name: "search", Type: "string"},
			{Name: "limit", Type: "int", Default: "50"},
			{Name: "offset", Type: "int", Default: "0"},
		},
	})

	mustAction(r, "app.get", appGetAction, ActionInfo{
		Family:       "application",
		Summary:      "Get full details and BCL source of an application",
		ResourceKind: "app.manager",
		Provides:     "{ app: { id, name, version, status, source_bcl, ... } }",
		Kind:         "read",
		Config: []ConfigField{
			{Name: "id", Type: "template", Summary: "App ID"},
			{Name: "id_fact", Type: "fact"},
		},
	})

	mustAction(r, "app.create", appCreateAction, ActionInfo{
		Family:       "application",
		Summary:      "Create a new application from BCL source or a template",
		ResourceKind: "app.manager",
		Provides:     "{ app: { id, name, version, status, revision_id, ... } }",
		Kind:         "effect",
		Config: []ConfigField{
			{Name: "id", Type: "template"},
			{Name: "name", Type: "template", Required: true},
			{Name: "description", Type: "template"},
			{Name: "version", Type: "template", Default: "0.1.0"},
			{Name: "source_bcl", Type: "template"},
			{Name: "source_fact", Type: "fact"},
			{Name: "status", Type: "string", Default: "draft"},
			{Name: "metadata_fact", Type: "fact"},
		},
	})

	mustAction(r, "app.update", appUpdateAction, ActionInfo{
		Family:       "application",
		Summary:      "Update an application definition and stage a new revision",
		ResourceKind: "app.manager",
		Provides:     "{ app: { id, name, version, status, revision_id, ... } }",
		Kind:         "effect",
		Config: []ConfigField{
			{Name: "id", Type: "template"},
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

	mustAction(r, "app.activate", appActivateAction, ActionInfo{
		Family:       "application",
		Summary:      "Promote an application revision to active status (hot-swap)",
		ResourceKind: "app.manager",
		Provides:     "{ activated: true, id: '...', revision_id: 1 }",
		Kind:         "effect",
		Config: []ConfigField{
			{Name: "id", Type: "template"},
			{Name: "id_fact", Type: "fact"},
			{Name: "revision_id", Type: "int"},
			{Name: "revision_fact", Type: "fact"},
		},
	})

	mustAction(r, "app.deactivate", appDeactivateAction, ActionInfo{
		Family:       "application",
		Summary:      "Deactivate an application gracefully",
		ResourceKind: "app.manager",
		Provides:     "{ deactivated: true, id: '...' }",
		Kind:         "effect",
		Config: []ConfigField{
			{Name: "id", Type: "template"},
			{Name: "id_fact", Type: "fact"},
		},
	})

	mustAction(r, "app.delete", appDeleteAction, ActionInfo{
		Family:       "application",
		Summary:      "Archive an application instance",
		ResourceKind: "app.manager",
		Provides:     "{ deleted: true, id: '...' }",
		Kind:         "effect",
		Config: []ConfigField{
			{Name: "id", Type: "template"},
			{Name: "id_fact", Type: "fact"},
		},
	})

	mustAction(r, "app.rollback", appRollbackAction, ActionInfo{
		Family:       "application",
		Summary:      "Roll back an application to an earlier stable revision",
		ResourceKind: "app.manager",
		Provides:     "{ rolled_back: true, id: '...', revision_id: 1 }",
		Kind:         "effect",
		Config: []ConfigField{
			{Name: "id", Type: "template"},
			{Name: "id_fact", Type: "fact"},
			{Name: "revision_id", Type: "int", Required: true},
			{Name: "revision_fact", Type: "fact"},
		},
	})

	mustAction(r, "app.health", appHealthAction, ActionInfo{
		Family:       "application",
		Summary:      "Get health status, components, and uptime for an application",
		ResourceKind: "app.manager",
		Provides:     "{ health: { app_id, status, healthy, uptime_sec, components } }",
		Kind:         "read",
		Config: []ConfigField{
			{Name: "id", Type: "template"},
			{Name: "id_fact", Type: "fact"},
		},
	})

	mustAction(r, "app.metrics", appMetricsAction, ActionInfo{
		Family:       "application",
		Summary:      "Get operational and Prometheus metrics for an application",
		ResourceKind: "app.manager",
		Provides:     "{ metrics: { app_id, requests_total, requests_failed, avg_latency_ms, prometheus } }",
		Kind:         "read",
		Config: []ConfigField{
			{Name: "id", Type: "template"},
			{Name: "id_fact", Type: "fact"},
		},
	})

	mustAction(r, "app.logs", appLogsAction, ActionInfo{
		Family:       "application",
		Summary:      "Query recent structured log events for an application",
		ResourceKind: "app.manager",
		Provides:     "{ logs: [...] }",
		Kind:         "read",
		Config: []ConfigField{
			{Name: "id", Type: "template"},
			{Name: "id_fact", Type: "fact"},
			{Name: "limit", Type: "int", Default: "100"},
		},
	})

	mustAction(r, "app.preview", appPreviewAction, ActionInfo{
		Family:       "application",
		Summary:      "Preview an application's BCL bundle structure and diagnostic findings",
		ResourceKind: "app.manager",
		Provides:     "{ preview: { id, intents, routes, resources, workers } }",
		Kind:         "read",
		Config: []ConfigField{
			{Name: "id", Type: "template"},
			{Name: "id_fact", Type: "fact"},
		},
	})

	mustAction(r, "app.export", appExportAction, ActionInfo{
		Family:       "application",
		Summary:      "Export canonical BCL source of an application",
		ResourceKind: "app.manager",
		Provides:     "{ source: '...' }",
		Kind:         "read",
		Config: []ConfigField{
			{Name: "id", Type: "template"},
			{Name: "id_fact", Type: "fact"},
		},
	})

	mustAction(r, "app.import", appImportAction, ActionInfo{
		Family:       "application",
		Summary:      "Import a full BCL application bundle",
		ResourceKind: "app.manager",
		Provides:     "{ app: { id, name, ... } }",
		Kind:         "effect",
		Config: []ConfigField{
			{Name: "id", Type: "template", Required: true},
			{Name: "bundle_fact", Type: "fact"},
			{Name: "bundle", Type: "template"},
		},
	})

	mustAction(r, "app.clone", appCloneAction, ActionInfo{
		Family:       "application",
		Summary:      "Clone an application into a new draft instance",
		ResourceKind: "app.manager",
		Provides:     "{ app: { id, name, ... } }",
		Kind:         "effect",
		Config: []ConfigField{
			{Name: "id", Type: "template"},
			{Name: "id_fact", Type: "fact"},
			{Name: "new_id", Type: "template"},
			{Name: "new_name", Type: "template"},
		},
	})

	mustAction(r, "app.run", appRunAction, ActionInfo{
		Family:       "application",
		Summary:      "Dispatch an intent in the context of an application",
		ResourceKind: "app.manager",
		Provides:     "{ result: {...} }",
		Kind:         "effect",
		Config: []ConfigField{
			{Name: "app_id", Type: "template"},
			{Name: "app_id_fact", Type: "fact"},
			{Name: "intent", Type: "template", Required: true},
			{Name: "inputs_fact", Type: "fact"},
		},
	})
}

var appListAction = ActionFactoryFunc(func(build BuildContext, spec NodeSpec) (Action, error) {
	if err := exactlyOneOutput(spec); err != nil {
		return nil, err
	}
	provides := spec.Provides[0]

	am, err := requireResource[AppManager](build, spec, "an app.manager resource")
	if err != nil {
		return nil, err
	}

	status := configString(spec.Config, "status", "")
	search := configString(spec.Config, "search", "")
	limit, _ := configInt(spec.Config, "limit", 50)
	offset, _ := configInt(spec.Config, "offset", 0)

	return ActionFunc(func(ctx *ActionContext) (ActionResult, error) {
		apps, err := am.ListApps(ctx.Context, AppFilter{
			Status: status,
			Search: search,
			Limit:  limit,
			Offset: offset,
		})
		if err != nil {
			return ActionResult{}, err
		}
		return ActionResult{Outputs: map[string]any{provides: map[string]any{"apps": apps}}}, nil
	}), nil
})

var appGetAction = ActionFactoryFunc(func(build BuildContext, spec NodeSpec) (Action, error) {
	if err := exactlyOneOutput(spec); err != nil {
		return nil, err
	}
	provides := spec.Provides[0]

	am, err := requireResource[AppManager](build, spec, "an app.manager resource")
	if err != nil {
		return nil, err
	}

	idTmpl, _ := configTemplate(spec.Config, "id", "")
	idFact := configString(spec.Config, "id_fact", "")

	return ActionFunc(func(ctx *ActionContext) (ActionResult, error) {
		id := resolveID(ctx, idFact, idTmpl)
		if id == "" {
			return ActionResult{}, invalidInput("app.get requires 'id' or 'id_fact'")
		}
		app, err := am.GetApp(ctx.Context, id)
		if err != nil {
			return ActionResult{}, notFound("app", id)
		}
		return ActionResult{Outputs: map[string]any{provides: map[string]any{"app": app}}}, nil
	}), nil
})

var appCreateAction = ActionFactoryFunc(func(build BuildContext, spec NodeSpec) (Action, error) {
	if err := exactlyOneOutput(spec); err != nil {
		return nil, err
	}
	provides := spec.Provides[0]

	am, err := requireResource[AppManager](build, spec, "an app.manager resource")
	if err != nil {
		return nil, err
	}

	idTmpl, _ := configTemplate(spec.Config, "id", "")
	nameTmpl, _ := configTemplate(spec.Config, "name", "")
	descTmpl, _ := configTemplate(spec.Config, "description", "")
	verTmpl, _ := configTemplate(spec.Config, "version", "0.1.0")
	sourceTmpl, _ := configTemplate(spec.Config, "source_bcl", "")
	sourceFact := configString(spec.Config, "source_fact", "")
	status := configString(spec.Config, "status", string(AppStatusDraft))
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

		created, err := am.CreateApp(ctx.Context, AppDef{
			ID:          strings.TrimSpace(id),
			Name:        strings.TrimSpace(name),
			Description: strings.TrimSpace(desc),
			Version:     strings.TrimSpace(version),
			Status:      AppStatus(status),
			SourceBCL:   source,
			Metadata:    metadata,
		})
		if err != nil {
			return ActionResult{}, err
		}
		return ActionResult{Outputs: map[string]any{provides: map[string]any{"app": created}}}, nil
	}), nil
})

var appUpdateAction = ActionFactoryFunc(func(build BuildContext, spec NodeSpec) (Action, error) {
	if err := exactlyOneOutput(spec); err != nil {
		return nil, err
	}
	provides := spec.Provides[0]

	am, err := requireResource[AppManager](build, spec, "an app.manager resource")
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

	return ActionFunc(func(ctx *ActionContext) (ActionResult, error) {
		env := actionEnv(ctx)
		id := resolveID(ctx, idFact, idTmpl)
		if id == "" {
			return ActionResult{}, invalidInput("app.update requires 'id' or 'id_fact'")
		}

		existing, err := am.GetApp(ctx.Context, id)
		if err != nil {
			return ActionResult{}, notFound("app", id)
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
			existing.Status = AppStatus(status)
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

		updated, err := am.UpdateApp(ctx.Context, id, existing)
		if err != nil {
			return ActionResult{}, err
		}
		return ActionResult{Outputs: map[string]any{provides: map[string]any{"app": updated}}}, nil
	}), nil
})

var appActivateAction = ActionFactoryFunc(func(build BuildContext, spec NodeSpec) (Action, error) {
	if err := exactlyOneOutput(spec); err != nil {
		return nil, err
	}
	provides := spec.Provides[0]

	am, err := requireResource[AppManager](build, spec, "an app.manager resource")
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
			return ActionResult{}, invalidInput("app.activate requires 'id' or 'id_fact'")
		}
		revision := int64(revID)
		if revFact != "" {
			if val, ok := resolvePath(ctx.Inputs, revFact); ok {
				revision = int64(toInt(val))
			}
		}
		if err := am.ActivateApp(ctx.Context, id, revision); err != nil {
			return ActionResult{}, err
		}
		return ActionResult{Outputs: map[string]any{provides: map[string]any{"activated": true, "id": id, "revision_id": revision}}}, nil
	}), nil
})

var appDeactivateAction = ActionFactoryFunc(func(build BuildContext, spec NodeSpec) (Action, error) {
	if err := exactlyOneOutput(spec); err != nil {
		return nil, err
	}
	provides := spec.Provides[0]

	am, err := requireResource[AppManager](build, spec, "an app.manager resource")
	if err != nil {
		return nil, err
	}

	idTmpl, _ := configTemplate(spec.Config, "id", "")
	idFact := configString(spec.Config, "id_fact", "")

	return ActionFunc(func(ctx *ActionContext) (ActionResult, error) {
		id := resolveID(ctx, idFact, idTmpl)
		if id == "" {
			return ActionResult{}, invalidInput("app.deactivate requires 'id' or 'id_fact'")
		}
		if err := am.DeactivateApp(ctx.Context, id); err != nil {
			return ActionResult{}, err
		}
		return ActionResult{Outputs: map[string]any{provides: map[string]any{"deactivated": true, "id": id}}}, nil
	}), nil
})

var appDeleteAction = ActionFactoryFunc(func(build BuildContext, spec NodeSpec) (Action, error) {
	if err := exactlyOneOutput(spec); err != nil {
		return nil, err
	}
	provides := spec.Provides[0]

	am, err := requireResource[AppManager](build, spec, "an app.manager resource")
	if err != nil {
		return nil, err
	}

	idTmpl, _ := configTemplate(spec.Config, "id", "")
	idFact := configString(spec.Config, "id_fact", "")

	return ActionFunc(func(ctx *ActionContext) (ActionResult, error) {
		id := resolveID(ctx, idFact, idTmpl)
		if id == "" {
			return ActionResult{}, invalidInput("app.delete requires 'id' or 'id_fact'")
		}
		if err := am.DeleteApp(ctx.Context, id); err != nil {
			return ActionResult{}, err
		}
		return ActionResult{Outputs: map[string]any{provides: map[string]any{"deleted": true, "id": id}}}, nil
	}), nil
})

var appRollbackAction = ActionFactoryFunc(func(build BuildContext, spec NodeSpec) (Action, error) {
	if err := exactlyOneOutput(spec); err != nil {
		return nil, err
	}
	provides := spec.Provides[0]

	am, err := requireResource[AppManager](build, spec, "an app.manager resource")
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
			return ActionResult{}, invalidInput("app.rollback requires 'id' or 'id_fact'")
		}
		revision := int64(revID)
		if revFact != "" {
			if val, ok := resolvePath(ctx.Inputs, revFact); ok {
				revision = int64(toInt(val))
			}
		}
		if revision <= 0 {
			return ActionResult{}, invalidInput("app.rollback requires positive revision_id")
		}
		if err := am.RollbackApp(ctx.Context, id, revision); err != nil {
			return ActionResult{}, err
		}
		return ActionResult{Outputs: map[string]any{provides: map[string]any{"rolled_back": true, "id": id, "revision_id": revision}}}, nil
	}), nil
})

var appHealthAction = ActionFactoryFunc(func(build BuildContext, spec NodeSpec) (Action, error) {
	if err := exactlyOneOutput(spec); err != nil {
		return nil, err
	}
	provides := spec.Provides[0]

	am, err := requireResource[AppManager](build, spec, "an app.manager resource")
	if err != nil {
		return nil, err
	}

	idTmpl, _ := configTemplate(spec.Config, "id", "")
	idFact := configString(spec.Config, "id_fact", "")

	return ActionFunc(func(ctx *ActionContext) (ActionResult, error) {
		id := resolveID(ctx, idFact, idTmpl)
		if id == "" {
			return ActionResult{}, invalidInput("app.health requires 'id' or 'id_fact'")
		}
		h, err := am.AppHealth(ctx.Context, id)
		if err != nil {
			return ActionResult{}, notFound("app", id)
		}
		return ActionResult{Outputs: map[string]any{provides: map[string]any{"health": h}}}, nil
	}), nil
})

var appMetricsAction = ActionFactoryFunc(func(build BuildContext, spec NodeSpec) (Action, error) {
	if err := exactlyOneOutput(spec); err != nil {
		return nil, err
	}
	provides := spec.Provides[0]

	am, err := requireResource[AppManager](build, spec, "an app.manager resource")
	if err != nil {
		return nil, err
	}

	idTmpl, _ := configTemplate(spec.Config, "id", "")
	idFact := configString(spec.Config, "id_fact", "")

	return ActionFunc(func(ctx *ActionContext) (ActionResult, error) {
		id := resolveID(ctx, idFact, idTmpl)
		if id == "" {
			return ActionResult{}, invalidInput("app.metrics requires 'id' or 'id_fact'")
		}
		metrics, err := am.AppMetrics(ctx.Context, id)
		if err != nil {
			return ActionResult{}, notFound("app", id)
		}
		return ActionResult{Outputs: map[string]any{provides: map[string]any{"metrics": metrics}}}, nil
	}), nil
})

var appLogsAction = ActionFactoryFunc(func(build BuildContext, spec NodeSpec) (Action, error) {
	if err := exactlyOneOutput(spec); err != nil {
		return nil, err
	}
	provides := spec.Provides[0]

	am, err := requireResource[AppManager](build, spec, "an app.manager resource")
	if err != nil {
		return nil, err
	}

	idTmpl, _ := configTemplate(spec.Config, "id", "")
	idFact := configString(spec.Config, "id_fact", "")
	limit, _ := configInt(spec.Config, "limit", 100)

	return ActionFunc(func(ctx *ActionContext) (ActionResult, error) {
		id := resolveID(ctx, idFact, idTmpl)
		if id == "" {
			return ActionResult{}, invalidInput("app.logs requires 'id' or 'id_fact'")
		}
		logs, err := am.AppLogs(ctx.Context, id, limit)
		if err != nil {
			return ActionResult{}, notFound("app", id)
		}
		return ActionResult{Outputs: map[string]any{provides: map[string]any{"logs": logs}}}, nil
	}), nil
})

var appPreviewAction = ActionFactoryFunc(func(build BuildContext, spec NodeSpec) (Action, error) {
	if err := exactlyOneOutput(spec); err != nil {
		return nil, err
	}
	provides := spec.Provides[0]

	am, err := requireResource[AppManager](build, spec, "an app.manager resource")
	if err != nil {
		return nil, err
	}

	idTmpl, _ := configTemplate(spec.Config, "id", "")
	idFact := configString(spec.Config, "id_fact", "")

	return ActionFunc(func(ctx *ActionContext) (ActionResult, error) {
		id := resolveID(ctx, idFact, idTmpl)
		if id == "" {
			return ActionResult{}, invalidInput("app.preview requires 'id' or 'id_fact'")
		}
		app, err := am.GetApp(ctx.Context, id)
		if err != nil {
			return ActionResult{}, notFound("app", id)
		}
		intents, pipelines, routes, resources, workers := extractAppStructure(app.SourceBCL)
		return ActionResult{
			Outputs: map[string]any{
				provides: map[string]any{
					"preview": map[string]any{
						"id":        app.ID,
						"name":      app.Name,
						"version":   app.Version,
						"status":    app.Status,
						"intents":   intents,
						"pipelines": pipelines,
						"routes":    routes,
						"resources": resources,
						"workers":   workers,
					},
				},
			},
		}, nil
	}), nil
})

var appExportAction = ActionFactoryFunc(func(build BuildContext, spec NodeSpec) (Action, error) {
	if err := exactlyOneOutput(spec); err != nil {
		return nil, err
	}
	provides := spec.Provides[0]

	am, err := requireResource[AppManager](build, spec, "an app.manager resource")
	if err != nil {
		return nil, err
	}

	idTmpl, _ := configTemplate(spec.Config, "id", "")
	idFact := configString(spec.Config, "id_fact", "")

	return ActionFunc(func(ctx *ActionContext) (ActionResult, error) {
		id := resolveID(ctx, idFact, idTmpl)
		if id == "" {
			return ActionResult{}, invalidInput("app.export requires 'id' or 'id_fact'")
		}
		src, err := am.ExportApp(ctx.Context, id)
		if err != nil {
			return ActionResult{}, notFound("app", id)
		}
		return ActionResult{Outputs: map[string]any{provides: map[string]any{"source": src}}}, nil
	}), nil
})

var appImportAction = ActionFactoryFunc(func(build BuildContext, spec NodeSpec) (Action, error) {
	if err := exactlyOneOutput(spec); err != nil {
		return nil, err
	}
	provides := spec.Provides[0]

	am, err := requireResource[AppManager](build, spec, "an app.manager resource")
	if err != nil {
		return nil, err
	}

	idTmpl, _ := configTemplate(spec.Config, "id", "")
	bundleFact := configString(spec.Config, "bundle_fact", "")
	bundleTmpl, _ := configTemplate(spec.Config, "bundle", "")

	return ActionFunc(func(ctx *ActionContext) (ActionResult, error) {
		env := actionEnv(ctx)
		id := ""
		if idTmpl != nil {
			id, _ = idTmpl.Render(env)
		}
		if id == "" {
			return ActionResult{}, invalidInput("app.import requires 'id'")
		}

		bundle := ""
		if bundleFact != "" {
			if val, ok := resolvePath(ctx.Inputs, bundleFact); ok {
				bundle = fmt.Sprintf("%v", val)
			}
		}
		if bundle == "" && bundleTmpl != nil {
			bundle, _ = bundleTmpl.Render(env)
		}
		if bundle == "" {
			return ActionResult{}, invalidInput("app.import requires 'bundle' or 'bundle_fact'")
		}

		app, err := am.ImportApp(ctx.Context, id, []byte(bundle))
		if err != nil {
			return ActionResult{}, err
		}
		return ActionResult{Outputs: map[string]any{provides: map[string]any{"app": app}}}, nil
	}), nil
})

var appCloneAction = ActionFactoryFunc(func(build BuildContext, spec NodeSpec) (Action, error) {
	if err := exactlyOneOutput(spec); err != nil {
		return nil, err
	}
	provides := spec.Provides[0]

	am, err := requireResource[AppManager](build, spec, "an app.manager resource")
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
			return ActionResult{}, invalidInput("app.clone requires 'id' or 'id_fact'")
		}
		newID := ""
		if newIDTmpl != nil {
			newID, _ = newIDTmpl.Render(env)
		}
		newName := ""
		if newNameTmpl != nil {
			newName, _ = newNameTmpl.Render(env)
		}

		cloned, err := am.CloneApp(ctx.Context, id, strings.TrimSpace(newID), strings.TrimSpace(newName))
		if err != nil {
			return ActionResult{}, err
		}
		return ActionResult{Outputs: map[string]any{provides: map[string]any{"app": cloned}}}, nil
	}), nil
})

var appRunAction = ActionFactoryFunc(func(build BuildContext, spec NodeSpec) (Action, error) {
	if err := exactlyOneOutput(spec); err != nil {
		return nil, err
	}
	provides := spec.Provides[0]

	am, err := requireResource[AppManager](build, spec, "an app.manager resource")
	if err != nil {
		return nil, err
	}

	appIDTmpl, _ := configTemplate(spec.Config, "app_id", "")
	appIDFact := configString(spec.Config, "app_id_fact", "")
	intentTmpl, _ := configTemplate(spec.Config, "intent", "")
	inputsFact := configString(spec.Config, "inputs_fact", "")

	return ActionFunc(func(ctx *ActionContext) (ActionResult, error) {
		env := actionEnv(ctx)
		appID := resolveID(ctx, appIDFact, appIDTmpl)
		if appID == "" {
			appID = "default"
		}
		intentName := ""
		if intentTmpl != nil {
			intentName, _ = intentTmpl.Render(env)
		}
		if intentName == "" {
			return ActionResult{}, invalidInput("app.run requires 'intent'")
		}

		inputs := map[string]any{}
		if inputsFact != "" {
			if val, ok := resolvePath(ctx.Inputs, inputsFact); ok {
				if m, ok := val.(map[string]any); ok {
					inputs = m
				}
			}
		}

		res, err := am.RunIntent(ctx.Context, appID, intentName, inputs)
		if err != nil {
			return ActionResult{}, err
		}
		return ActionResult{Outputs: map[string]any{provides: map[string]any{"result": res}}}, nil
	}), nil
})
