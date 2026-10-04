package platform

import (
	"bytes"
	"fmt"
	"io"
)

// TemplateRenderer renders a named template. github.com/oarkflow/template's
// SPL engine satisfies it, and so does anything with the same method.
type TemplateRenderer interface {
	Render(w io.Writer, name string, data any, layout ...string) error
}

func registerTemplateActions(r *Registry) {
	mustAction(r, "template.render", templateRenderAction, ActionInfo{
		Family:   "data",
		Summary:  "Render a named template through the host's template engine and publish the text",
		Provides: "{ ok, text, template, error }",
		Kind:     "pure",
		Config: []ConfigField{
			{Name: "template", Type: "string", Summary: "Template name, e.g. sms/otp_login"},
			{Name: "template_fact", Type: "fact", Summary: "Fact path holding the template name, to choose it per request"},
			{Name: "vars_fact", Type: "fact", Default: "vars", Summary: "Fact whose fields are the template's variables"},
			{Name: "layout", Type: "string"},
			{Name: "capture", Type: "bool", Summary: "Publish a missing or broken template as { ok: false, error } instead of failing the intent"},
		},
	})
}

var templateRenderAction = ActionFactoryFunc(func(build BuildContext, spec NodeSpec) (Action, error) {
	if err := exactlyOneOutput(spec); err != nil {
		return nil, err
	}
	name := configString(spec.Config, "template", "")
	nameFact := configString(spec.Config, "template_fact", "")
	if name == "" && nameFact == "" {
		return nil, fmt.Errorf("node %q: template.render needs config.template or config.template_fact", spec.Name)
	}
	varsFact := configString(spec.Config, "vars_fact", "vars")
	layout := configString(spec.Config, "layout", "")
	capture := configBool(spec.Config, "capture", false)
	if build.Platform == nil || build.Platform.templates == nil {
		return nil, fmt.Errorf("node %q: template.render needs a template engine; the host did not configure one (LoadOptions.Templates)", spec.Name)
	}
	renderer := build.Platform.templates

	return ActionFunc(func(ctx *ActionContext) (ActionResult, error) {
		tpl := name
		if nameFact != "" {
			if v, ok := resolvePath(ctx.Inputs, nameFact); ok && Stringify(v) != "" {
				tpl = Stringify(v)
			}
		}
		fail := func(code, message string) (ActionResult, error) {
			if capture {
				return singleOutput(spec, map[string]any{"ok": false, "template": tpl, "text": "",
					"error": map[string]any{"code": code, "message": message}}), nil
			}
			return ActionResult{}, statusFailure(code, 422, message, nil)
		}
		if tpl == "" {
			return fail("UNKNOWN_TEMPLATE", "no template was named")
		}
		data := map[string]any{}
		if v, ok := resolvePath(ctx.Inputs, varsFact); ok {
			if m, ok := v.(map[string]any); ok {
				for k, e := range m {
					data[k] = e
				}
			}
		}
		// Every fact is visible too, under its own name, so a template may read
		// ${user.name} as well as its variables.
		for k, v := range ctx.Inputs {
			if _, taken := data[k]; !taken {
				data[k] = v
			}
		}
		var buf bytes.Buffer
		var err error
		if layout != "" {
			err = renderer.Render(&buf, tpl, data, layout)
		} else {
			err = renderer.Render(&buf, tpl, data)
		}
		if err != nil {
			return fail("TEMPLATE_ERROR", fmt.Sprintf("template %q: %v", tpl, err))
		}
		return singleOutput(spec, map[string]any{"ok": true, "template": tpl, "text": buf.String()}), nil
	}), nil
})
