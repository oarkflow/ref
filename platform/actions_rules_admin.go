package platform

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/oarkflow/bcl"
	"github.com/oarkflow/rules"
)

// Runtime rule management. A rules.engine publishes the definitions it finds
// on disk when it opens; these let an operator change one while the application
// runs, with the change checked first, effective on the next evaluation, kept
// across restarts (when the engine names an `overrides` database), and undone
// by going back to the file.

var (
	decisionNames = regexp.MustCompile(`decision_table\s+"([^"]+)"`)
	rankingNames  = regexp.MustCompile(`ranking\s+"([^"]+)"`)
)

// program returns the active compiled program of a definition.
func (w *rulesEngineWrapper) program(definition string) (*bcl.DecisionProgram, error) {
	record, err := w.service.GetDefinition(context.Background(), definition)
	if err != nil {
		return nil, err
	}
	if record.Program == nil {
		return nil, fmt.Errorf("definition %q has no program", definition)
	}
	return record.Program, nil
}

// source returns the text of the active version of a definition.
func (w *rulesEngineWrapper) source(definition string) (text, version string, err error) {
	record, err := w.service.GetDefinition(context.Background(), definition)
	if err != nil {
		return "", "", err
	}
	text = record.Source
	if text == "" {
		path := record.SourcePath
		if path == "" {
			path = w.origins[definition]
		}
		if path != "" {
			raw, rerr := os.ReadFile(path)
			if rerr != nil {
				return "", "", rerr
			}
			text = string(raw)
		}
	}
	return text, record.Version, nil
}

var (
	whenBlock   = regexp.MustCompile(`\bwhen\s*\{`)
	quoted      = regexp.MustCompile(`"(?:[^"\\]|\\.)*"`)
	numericName = regexp.MustCompile(`(?:_len|_count|_s|_ms|total|segments|price|limit|max|min|rate)$`)
	factPath    = regexp.MustCompile(`\b[a-z_][a-z0-9_]*(?:\.[a-z_][a-z0-9_]*)+\b`)
)

// factHints lists the facts a definition reads, found in its `when` blocks,
// each with a kind (text, number, bool) and a sample value inferred from what
// the rules compare it with. A form built from it lets an operator try a
// decision without writing JSON.
func factHints(source string) []any {
	type hint struct{ kind, sample string }
	found := map[string]*hint{}
	var order []string
	for _, loc := range whenBlock.FindAllStringIndex(source, -1) {
		depth, end := 0, -1
		for i := loc[1] - 1; i < len(source); i++ {
			switch source[i] {
			case '{':
				depth++
			case '}':
				depth--
				if depth == 0 {
					end = i
				}
			}
			if end >= 0 {
				break
			}
		}
		if end < 0 {
			continue
		}
		body := source[loc[1]:end]
		for _, path := range factPath.FindAllString(quoted.ReplaceAllString(body, `""`), -1) {
			if found[path] == nil {
				found[path] = &hint{kind: "text"}
				order = append(order, path)
			}
		}
		for path, h := range found {
			cmp := regexp.MustCompile(regexp.QuoteMeta(path) + `\s*(==|!=|<=|>=|<|>)\s*(true|false|-?\d+(?:\.\d+)?|"([^"]*)")`)
			if m := cmp.FindStringSubmatch(body); m != nil {
				literal := m[2]
				switch {
				case literal == "true" || literal == "false":
					// A condition that fires on true is tried with false, and the other way round.
					h.kind, h.sample = "bool", "false"
				case strings.HasPrefix(literal, `"`):
					h.kind, h.sample = "text", m[3]
				default:
					h.kind, h.sample = "number", "0"
					if m[1] == "==" {
						h.sample = literal
					}
				}
			} else if numericName.MatchString(path) {
				h.kind, h.sample = "number", "0"
			}
		}
	}
	sort.Strings(order)
	out := make([]any, 0, len(order))
	for _, path := range order {
		h := found[path]
		out = append(out, map[string]any{"path": path, "kind": h.kind, "sample": h.sample})
	}
	return out
}

func (w *rulesEngineWrapper) overridden(ctx context.Context) map[string]bool {
	out := map[string]bool{}
	if w.overrides == nil {
		return out
	}
	rows, err := w.overrides.QueryContext(ctx, fmt.Sprintf("SELECT name FROM %s", w.table))
	if err != nil {
		return out
	}
	if rows.Err() != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if rows.Scan(&name) == nil {
			out[name] = true
		}
	}
	return out
}

func (w *rulesEngineWrapper) ensureTable(ctx context.Context) error {
	_, err := w.overrides.ExecContext(ctx, fmt.Sprintf(
		"CREATE TABLE IF NOT EXISTS %s (name VARCHAR(191) PRIMARY KEY, source TEXT NOT NULL, version VARCHAR(64) NOT NULL, updated_by VARCHAR(191) NOT NULL DEFAULT '', updated_ms BIGINT NOT NULL)", w.table))
	return err
}

// loadOverrides publishes the stored definitions over the file versions. One
// that no longer compiles (a file changed underneath it) is skipped and logged:
// the engine must still open.
func (w *rulesEngineWrapper) loadOverrides(ctx context.Context) error {
	if err := w.ensureTable(ctx); err != nil {
		return err
	}
	rows, err := w.overrides.QueryContext(ctx, fmt.Sprintf("SELECT name, source, version FROM %s", w.table))
	if err != nil {
		return err
	}
	type row struct{ name, source, version string }
	var stored []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.name, &r.source, &r.version); err != nil {
			rows.Close()
			return err
		}
		stored = append(stored, r)
	}
	rows.Close()
	for _, r := range stored {
		if _, err := w.service.Publish(ctx, rules.PublishRequest{TenantID: w.cfg.DefaultTenant, Name: r.name, Version: r.version, Source: r.source}); err != nil {
			slog.Warn("rule override skipped", "definition", r.name, "error", err.Error())
		}
	}
	return nil
}

// save validates and publishes new source, then stores it.
func (w *rulesEngineWrapper) save(ctx context.Context, name, source, actor string) (string, error) {
	if _, err := w.service.GetDefinition(ctx, name); err != nil {
		return "", invalidInput("there is no rule definition named %q", name)
	}
	version := fmt.Sprintf("rt-%d", time.Now().UnixMilli())
	req := rules.PublishRequest{TenantID: w.cfg.DefaultTenant, Name: name, Version: version, Source: source}
	if _, err := w.service.Publish(ctx, req); err != nil {
		return "", invalidInput("the rule was not saved: %s%s", err.Error(), rulesDiagnostics(ctx, w.service, req))
	}
	if w.overrides != nil {
		_, err := w.overrides.ExecContext(ctx, rebind(w.overrides.Dialect, fmt.Sprintf("DELETE FROM %s WHERE name = $1", w.table)), name)
		if err != nil {
			return version, err
		}
		_, err = w.overrides.ExecContext(ctx, rebind(w.overrides.Dialect,
			fmt.Sprintf("INSERT INTO %s (name, source, version, updated_by, updated_ms) VALUES ($1, $2, $3, $4, $5)", w.table)),
			name, source, version, actor, time.Now().UnixMilli())
		if err != nil {
			return version, err
		}
	}
	return version, nil
}

// reset goes back to the definition's file.
func (w *rulesEngineWrapper) reset(ctx context.Context, name string) (string, error) {
	path := w.origins[name]
	if path == "" {
		return "", invalidInput("%q has no file to go back to", name)
	}
	version := fmt.Sprintf("file-%d", time.Now().UnixMilli())
	if _, err := w.service.Publish(ctx, rules.PublishRequest{TenantID: w.cfg.DefaultTenant, Name: name, Version: version, Path: path}); err != nil {
		return "", invalidInput("the file version did not publish: %s", err.Error())
	}
	if w.overrides != nil {
		if _, err := w.overrides.ExecContext(ctx, rebind(w.overrides.Dialect, fmt.Sprintf("DELETE FROM %s WHERE name = $1", w.table)), name); err != nil {
			return version, err
		}
	}
	return version, nil
}

func registerRulesAdminActions(r *Registry) {
	cfg := []ConfigField{
		{Name: "name_fact", Type: "fact", Default: "input.name", Summary: "Fact holding the definition name"},
	}
	mustAction(r, "rules.catalog", rulesCatalogAction, ActionInfo{
		Family: "rules", Summary: "List the engine's definitions with their decisions and rankings, and whether each was edited at runtime",
		ResourceKind: "rules.engine", Provides: "A list of { name, version, overridden, resettable, decisions, rankings }", Kind: "read",
	})
	mustAction(r, "rules.source", rulesSourceAction, ActionInfo{
		Family: "rules", Summary: "The source text of a definition's active version",
		ResourceKind: "rules.engine", Provides: "{ name, version, source, overridden }", Kind: "read", Config: cfg,
	})
	mustAction(r, "rules.save", rulesSaveAction, ActionInfo{
		Family: "rules", Summary: "Validate and publish edited source, effective on the next evaluation; kept across restarts when the engine has overrides",
		ResourceKind: "rules.engine", Provides: "{ name, version }", Kind: "effect",
		Config: append(append([]ConfigField{}, cfg...), ConfigField{Name: "source_fact", Type: "fact", Default: "input.source"}, ConfigField{Name: "actor_fact", Type: "fact", Default: "principal.id"}),
	})
	mustAction(r, "rules.reset", rulesResetAction, ActionInfo{
		Family: "rules", Summary: "Discard runtime edits and go back to the definition's file",
		ResourceKind: "rules.engine", Provides: "{ name, version }", Kind: "effect", Config: cfg,
	})
	mustAction(r, "rules.check", rulesCheckAction, ActionInfo{
		Family: "rules", Summary: "Validate rule source without publishing it: { valid, diagnostics: [{severity, message, code, line, column}] }, for an editor to mark",
		ResourceKind: "rules.engine", Provides: "The validation report", Kind: "read",
		Config: append(append([]ConfigField{}, cfg...), ConfigField{Name: "source_fact", Type: "fact", Default: "input.source"}, ConfigField{Name: "fail_on_invalid", Type: "bool", Default: "false", Summary: "Fail the intent (422) with the first diagnostic instead of publishing the report"}),
	})
	mustAction(r, "rules.try", rulesTryAction, ActionInfo{
		Family: "rules", Summary: "Evaluate a decision of a definition against given facts and explain the result, changing nothing",
		ResourceKind: "rules.engine", Provides: "The decision result with its trace", Kind: "read",
		Config: append(append([]ConfigField{}, cfg...), ConfigField{Name: "decision_fact", Type: "fact", Default: "input.decision"}, ConfigField{Name: "facts_fact", Type: "fact", Default: "input.facts"}),
	})
}

func engineOf(build BuildContext, spec NodeSpec) (*rulesEngineWrapper, error) {
	return requireResource[*rulesEngineWrapper](build, spec, "a rules.engine resource")
}

func ruleFact(ctx *ActionContext, path string) string {
	v, _ := resolvePath(ctx.Inputs, path)
	return Stringify(v)
}

var rulesCatalogAction = ActionFactoryFunc(func(build BuildContext, spec NodeSpec) (Action, error) {
	engine, err := engineOf(build, spec)
	if err != nil {
		return nil, err
	}
	if err := exactlyOneOutput(spec); err != nil {
		return nil, err
	}
	return ActionFunc(func(ctx *ActionContext) (ActionResult, error) {
		records, err := engine.service.ListDefinitions(ctx.Context)
		if err != nil {
			return ActionResult{}, err
		}
		seen := map[string]bool{}
		over := engine.overridden(ctx.Context)
		var out []any
		for _, rec := range records {
			if seen[rec.Name] {
				continue
			}
			seen[rec.Name] = true
			text, version, err := engine.source(rec.Name)
			if err != nil {
				continue
			}
			names := func(re *regexp.Regexp) []any {
				var l []any
				seen := map[string]bool{}
				for _, m := range re.FindAllStringSubmatch(text, -1) {
					if !seen[m[1]] {
						seen[m[1]] = true
						l = append(l, m[1])
					}
				}
				return l
			}
			out = append(out, map[string]any{
				"name": rec.Name, "version": version, "overridden": over[rec.Name],
				"resettable": engine.origins[rec.Name] != "",
				"decisions":  names(decisionNames), "rankings": names(rankingNames),
			})
		}
		sort.Slice(out, func(i, j int) bool {
			return out[i].(map[string]any)["name"].(string) < out[j].(map[string]any)["name"].(string)
		})
		return singleOutput(spec, out), nil
	}), nil
})

var rulesSourceAction = ActionFactoryFunc(func(build BuildContext, spec NodeSpec) (Action, error) {
	engine, err := engineOf(build, spec)
	if err != nil {
		return nil, err
	}
	if err := exactlyOneOutput(spec); err != nil {
		return nil, err
	}
	nameFact := configString(spec.Config, "name_fact", "input.name")
	return ActionFunc(func(ctx *ActionContext) (ActionResult, error) {
		name := ruleFact(ctx, nameFact)
		text, version, err := engine.source(name)
		if err != nil {
			return ActionResult{}, notFound("rule definition", name)
		}
		return singleOutput(spec, map[string]any{"name": name, "version": version, "source": text, "overridden": engine.overridden(ctx.Context)[name], "facts": factHints(text)}), nil
	}), nil
})

var rulesSaveAction = ActionFactoryFunc(func(build BuildContext, spec NodeSpec) (Action, error) {
	engine, err := engineOf(build, spec)
	if err != nil {
		return nil, err
	}
	if err := exactlyOneOutput(spec); err != nil {
		return nil, err
	}
	nameFact := configString(spec.Config, "name_fact", "input.name")
	sourceFact := configString(spec.Config, "source_fact", "input.source")
	return ActionFunc(func(ctx *ActionContext) (ActionResult, error) {
		name := ruleFact(ctx, nameFact)
		source := ruleFact(ctx, sourceFact)
		if strings.TrimSpace(source) == "" {
			return ActionResult{}, invalidInput("the rule source is empty")
		}
		version, err := engine.save(ctx.Context, name, source, ctx.Principal.ID)
		if err != nil {
			return ActionResult{}, err
		}
		return singleOutput(spec, map[string]any{"name": name, "version": version}), nil
	}), nil
})

var rulesResetAction = ActionFactoryFunc(func(build BuildContext, spec NodeSpec) (Action, error) {
	engine, err := engineOf(build, spec)
	if err != nil {
		return nil, err
	}
	if err := exactlyOneOutput(spec); err != nil {
		return nil, err
	}
	nameFact := configString(spec.Config, "name_fact", "input.name")
	return ActionFunc(func(ctx *ActionContext) (ActionResult, error) {
		name := ruleFact(ctx, nameFact)
		version, err := engine.reset(ctx.Context, name)
		if err != nil {
			return ActionResult{}, err
		}
		return singleOutput(spec, map[string]any{"name": name, "version": version}), nil
	}), nil
})

var rulesTryAction = ActionFactoryFunc(func(build BuildContext, spec NodeSpec) (Action, error) {
	engine, err := engineOf(build, spec)
	if err != nil {
		return nil, err
	}
	if err := exactlyOneOutput(spec); err != nil {
		return nil, err
	}
	nameFact := configString(spec.Config, "name_fact", "input.name")
	decisionFact := configString(spec.Config, "decision_fact", "input.decision")
	factsFact := configString(spec.Config, "facts_fact", "input.facts")
	return ActionFunc(func(ctx *ActionContext) (ActionResult, error) {
		name, decision := ruleFact(ctx, nameFact), ruleFact(ctx, decisionFact)
		program, err := engine.program(name)
		if err != nil {
			return ActionResult{}, notFound("rule definition", name)
		}
		if _, ok := program.Decisions[decision]; !ok && program.Rankings[decision] == nil {
			return ActionResult{}, invalidInput("definition %q has no decision %q", name, decision)
		}
		facts := map[string]any{}
		if v, ok := resolvePath(ctx.Inputs, factsFact); ok {
			if m, ok := v.(map[string]any); ok {
				facts = m
			}
		}
		res, err := evaluateDecision(program, decision, facts, true)
		if err != nil {
			return ActionResult{}, invalidInput("the decision could not be evaluated: %s", err.Error())
		}
		return singleOutput(spec, decisionMap(res, true)), nil
	}), nil
})

var diagnosticAt = regexp.MustCompile(`-->\s*\S*?:(\d+):(\d+)`)

// splitDiagnostic takes the position out of a message that carries it as text
// ("error: … --> <input>:186:1 …") and keeps the first sentence.
func splitDiagnostic(message string) (string, int, int) {
	line, col := 1, 1
	if m := diagnosticAt.FindStringSubmatch(message); m != nil {
		line, _ = strconv.Atoi(m[1])
		col, _ = strconv.Atoi(m[2])
	}
	first := strings.TrimSpace(strings.SplitN(message, "\n", 2)[0])
	first = strings.TrimPrefix(first, "error: ")
	return first, line, col
}

var rulesCheckAction = ActionFactoryFunc(func(build BuildContext, spec NodeSpec) (Action, error) {
	engine, err := engineOf(build, spec)
	if err != nil {
		return nil, err
	}
	if err := exactlyOneOutput(spec); err != nil {
		return nil, err
	}
	nameFact := configString(spec.Config, "name_fact", "input.name")
	sourceFact := configString(spec.Config, "source_fact", "input.source")
	failOnInvalid := configBool(spec.Config, "fail_on_invalid", false)
	return ActionFunc(func(ctx *ActionContext) (ActionResult, error) {
		name, source := ruleFact(ctx, nameFact), ruleFact(ctx, sourceFact)
		if name == "" {
			name = "check"
		}
		report, err := engine.service.Validate(ctx.Context, rules.ValidationRequest{
			TenantID: engine.cfg.DefaultTenant, Name: name, Version: fmt.Sprintf("check-%d", time.Now().UnixNano()), Source: source,
		})
		diagnostics := []any{}
		valid := err == nil && report != nil && report.Valid
		if report != nil {
			add := func(severity, message, code string, line, col int) {
				diagnostics = append(diagnostics, map[string]any{"severity": severity, "message": message, "code": code, "line": line, "column": col})
			}
			for _, d := range report.Diagnostics {
				message, line, col := d.Message, d.Span.Start.Line, d.Span.Start.Column
				if line == 0 {
					message, line, col = splitDiagnostic(message)
				}
				add(d.Severity, message, d.Code, line, col)
			}
		}
		if err != nil && len(diagnostics) == 0 {
			diagnostics = append(diagnostics, map[string]any{"severity": "error", "message": err.Error(), "code": "", "line": 1, "column": 1})
		}
		if failOnInvalid && !valid {
			message := "the rule is not valid"
			if len(diagnostics) > 0 {
				message = Stringify(diagnostics[0].(map[string]any)["message"])
			}
			return ActionResult{}, invalidInput("%s", message)
		}
		return singleOutput(spec, map[string]any{"valid": valid, "diagnostics": diagnostics}), nil
	}), nil
})
