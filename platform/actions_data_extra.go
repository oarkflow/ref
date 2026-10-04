package platform

import (
	"fmt"
	"sort"
	"strings"
)

func registerDataExtraActions(r *Registry) {
	mustAction(r, "data.augment", dataAugmentAction, ActionInfo{
		Family:   "data",
		Summary:  "Add computed fields to every element of a list: each field is an expression that sees `item`, `index` and the node's facts. A list element that is not an object becomes an object holding only the new fields",
		Provides: "A list of objects",
		Kind:     "pure",
		Config: []ConfigField{
			{Name: "source_fact", Type: "fact", Required: true, Summary: "A list; a missing one is an empty list"},
			{Name: "fields", Type: "map", Required: true, Summary: `fields { name "expression" … }, evaluated in name order, later ones seeing earlier ones as item.<name>`},
		},
	})
	mustAction(r, "data.concat", dataConcatAction, ActionInfo{
		Family:   "data",
		Summary:  "Join several lists into one, in order; a missing fact counts as an empty list",
		Provides: "A list",
		Kind:     "pure",
		Config: []ConfigField{
			{Name: "sources", Type: "[]fact", Required: true},
		},
	})
	mustAction(r, "text.render", textRenderAction, ActionInfo{
		Family:   "data",
		Summary:  "Render text that holds {{ placeholder }}s, taken from a fact, over another fact's fields; an unknown placeholder renders empty",
		Provides: "The rendered text",
		Kind:     "pure",
		Config: []ConfigField{
			{Name: "text_fact", Type: "fact", Required: true},
			{Name: "vars_fact", Type: "fact", Summary: "Fact whose fields are the placeholders"},
		},
	})
}

var dataAugmentAction = ActionFactoryFunc(func(_ BuildContext, spec NodeSpec) (Action, error) {
	if err := exactlyOneOutput(spec); err != nil {
		return nil, err
	}
	source, err := requiredString(spec.Config, "source_fact")
	if err != nil {
		return nil, fmt.Errorf("node %q: %w", spec.Name, err)
	}
	raw := stringMap(spec.Config["fields"])
	if len(raw) == 0 {
		return nil, fmt.Errorf("node %q: data.augment needs config.fields", spec.Name)
	}
	names := make([]string, 0, len(raw))
	for name := range raw {
		names = append(names, name)
	}
	sort.Strings(names)
	exprs := make([]*Expression, len(names))
	for i, name := range names {
		if exprs[i], err = CompileExpr(raw[name]); err != nil {
			return nil, fmt.Errorf("node %q: field %q: %w", spec.Name, name, err)
		}
	}
	return ActionFunc(func(ctx *ActionContext) (ActionResult, error) {
		value, _ := resolvePath(ctx.Inputs, source)
		items, err := requiredList(value, source)
		if err != nil {
			return ActionResult{}, err
		}
		base := actionEnv(ctx)
		out := make([]any, 0, len(items))
		for index, item := range items {
			row := map[string]any{}
			seen := item // what expressions call item: the row itself for an object, the value for anything else
			if m, ok := item.(map[string]any); ok {
				row = copyMap(m)
				seen = row
			}
			for i, name := range names {
				env := make(Env, len(base)+2)
				for k, v := range base {
					env[k] = v
				}
				env["item"], env["index"] = seen, index
				result, err := exprs[i].Eval(env)
				if err != nil {
					return ActionResult{}, fmt.Errorf("data.augment %s[%d].%s: %w", source, index, name, err)
				}
				if m, ok := result.(map[string]any); ok {
					result = copyMap(m) // a snapshot, so a later field cannot make the row contain itself
				}
				row[name] = result
			}
			out = append(out, row)
		}
		return singleOutput(spec, out), nil
	}), nil
})

var dataConcatAction = ActionFactoryFunc(func(_ BuildContext, spec NodeSpec) (Action, error) {
	if err := exactlyOneOutput(spec); err != nil {
		return nil, err
	}
	sources := configStrings(spec.Config, "sources")
	if len(sources) == 0 {
		return nil, fmt.Errorf("node %q: data.concat needs config.sources", spec.Name)
	}
	return ActionFunc(func(ctx *ActionContext) (ActionResult, error) {
		out := []any{}
		for _, path := range sources {
			value, _ := resolvePath(ctx.Inputs, path)
			items, err := requiredList(value, path)
			if err != nil {
				return ActionResult{}, err
			}
			out = append(out, items...)
		}
		return singleOutput(spec, out), nil
	}), nil
})

var textRenderAction = ActionFactoryFunc(func(_ BuildContext, spec NodeSpec) (Action, error) {
	if err := exactlyOneOutput(spec); err != nil {
		return nil, err
	}
	textFact, err := requiredString(spec.Config, "text_fact")
	if err != nil {
		return nil, fmt.Errorf("node %q: %w", spec.Name, err)
	}
	varsFact := configString(spec.Config, "vars_fact", "")
	return ActionFunc(func(ctx *ActionContext) (ActionResult, error) {
		value, _ := resolvePath(ctx.Inputs, textFact)
		text := Stringify(value)
		if !strings.Contains(text, "{{") {
			return singleOutput(spec, text), nil
		}
		tmpl, err := CompileTemplate(text)
		if err != nil {
			return ActionResult{}, invalidInput("the text has a placeholder that cannot be read: %v", err)
		}
		env := Env{}
		if varsFact != "" {
			if v, ok := resolvePath(ctx.Inputs, varsFact); ok {
				if m, ok := v.(map[string]any); ok {
					for k, val := range m {
						env[k] = val
					}
				}
			}
		}
		out, err := tmpl.Render(env)
		if err != nil {
			// A placeholder with no value is not an error for a mailing: it renders empty.
			return singleOutput(spec, stripPlaceholders(text, env)), nil
		}
		return singleOutput(spec, out), nil
	}), nil
})

// stripPlaceholders renders what it can when Render refuses a missing name: each
// {{ name }} becomes its value, or nothing.
func stripPlaceholders(text string, env Env) string {
	var b strings.Builder
	for {
		i := strings.Index(text, "{{")
		if i < 0 {
			b.WriteString(text)
			return b.String()
		}
		j := strings.Index(text[i:], "}}")
		if j < 0 {
			b.WriteString(text)
			return b.String()
		}
		b.WriteString(text[:i])
		name := strings.TrimSpace(text[i+2 : i+j])
		if v, ok := env[name]; ok {
			b.WriteString(Stringify(v))
		}
		text = text[i+j+2:]
	}
}
