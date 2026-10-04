package platform

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/oarkflow/bcl"

	"github.com/oarkflow/ref/intent"
)

// rules.decide and rules.rank run the decision tables and rankings of a
// published rules.engine definition (github.com/oarkflow/rules) as ordinary
// intent nodes.
//
// rules.evaluate is the policy gate: it turns a deny into a REF decision that
// blocks the effects downstream. These two are for the other use of a decision
// table, as data: a price list, a numbering plan, an error classification, a
// routing policy. They publish the outcome as a plain map, so every later node
// reads it with the expression language, and a table's attributes are how a
// rule file hands values to the application:
//
//	row "nepal" {
//	  when { message.country == "NP" }
//	  then { outcome { decision allow attributes { per_segment 0.015 } } }
//	}
//
// They evaluate the compiled program directly, so a decision made per message
// does not append an audit record to the engine's store each time.

func registerRulesDecideActions(r *Registry) {
	mustAction(r, "rules.decide", rulesDecideAction, ActionInfo{
		Family:       "rules",
		Summary:      "Evaluate a decision of a rules definition against the node's facts and publish the outcome as a map",
		ResourceKind: "rules.engine",
		Provides:     "{ effect, allowed, rule, reason, reason_code, tags, score, attributes, rank } with the attributes also at the top level",
		Kind:         "read",
		Config: []ConfigField{
			{Name: "definition", Type: "string", Required: true, Summary: "Published definition name (the rule file's path, dots for slashes)"},
			{Name: "decision", Type: "string", Required: true},
			{Name: "fail_on", Type: "[]string", Summary: `Effects that fail the intent, e.g. ["deny"]. The failure takes its code, HTTP-style status and message from the row: attributes { code "…" status 422 } and reason "…"`},
			{Name: "explain", Type: "bool", Default: "false", Summary: "Include the evaluation trace"},
			{Name: "inputs", Type: "[]string", Summary: "Facts passed to the rules; default all"},
		},
	})

	mustAction(r, "rules.rank", rulesRankAction, ActionInfo{
		Family:       "rules",
		Summary:      "Filter candidates with an eligibility decision and order the survivors with a ranking, best first",
		ResourceKind: "rules.engine",
		Provides:     "{ chain, rejected, empty, transient }",
		Kind:         "read",
		Config: []ConfigField{
			{Name: "definition", Type: "string", Required: true},
			{Name: "candidates_fact", Type: "fact", Default: "candidates", Summary: "A list of { id, facts }, or of rows with an id (a query result)"},
			{Name: "eligibility", Type: "string", Summary: `Decision run once per candidate with the candidate as "provider"; a deny rejects it, and an allow's attributes (priority, tier, exclusive, …) are merged into the candidate's facts`},
			{Name: "ranking", Type: "string", Summary: "Ranking decision that orders the eligible candidates"},
			{Name: "eligibility_extra", Type: "[]string", Summary: `Further per-candidate decisions, "definition/decision", run after eligibility (usually in a definition generated at runtime). A decision name may hold {id}, the candidate's id: "custom/for_{id}" runs the decision written for that candidate alone, and a candidate without one is simply not affected, so a large rule set is searched per candidate, not in full. A deny rejects the candidate; an allow that matched a rule sets the candidate's fact granted for the main decision to honour, and its attributes (priority, exclusive, tier) override the main decision's`},
			{Name: "ranking_fact", Type: "fact", Summary: "Fact path holding the ranking name, to choose it per request"},
			{Name: "limit", Type: "int", Summary: "Keep at most this many"},
		},
	})
}

// decisionFor resolves the compiled program of a definition.
func decisionFor(build BuildContext, spec NodeSpec, definition string) (*bcl.DecisionProgram, error) {
	engine, err := requireResource[*rulesEngineWrapper](build, spec, "a rules.engine resource")
	if err != nil {
		return nil, err
	}
	record, err := engine.service.GetDefinition(context.Background(), definition)
	if err != nil {
		return nil, fmt.Errorf("node %q: rules definition %q: %w", spec.Name, definition, err)
	}
	if record.Program == nil {
		return nil, fmt.Errorf("node %q: rules definition %q has no program", spec.Name, definition)
	}
	return record.Program, nil
}

func evaluateDecision(program *bcl.DecisionProgram, decision string, input map[string]any, explain bool) (*bcl.DecisionResult, error) {
	engine := bcl.NewDecisionEngine(program, optionsFor(program))
	return engine.EvaluateWithOptions(decision, input, bcl.DecisionEvaluateOptions{Explain: explain})
}

// decisionMap renders a result as the plain map expressions read.
func decisionMap(res *bcl.DecisionResult, explain bool) map[string]any {
	out := map[string]any{
		"decision":    res.DecisionID,
		"effect":      res.Effect,
		"allowed":     res.Allowed,
		"rule":        res.PolicyID,
		"reason":      res.Reason,
		"reason_code": res.ReasonCode,
		"score":       res.Score,
		"tags":        stringsToAny(res.Tags),
	}
	attrs := map[string]any{}
	for k, v := range res.Attributes {
		attrs[k] = v
	}
	out["attributes"] = attrs
	for k, v := range attrs {
		if _, taken := out[k]; !taken {
			out[k] = v
		}
	}
	if res.Rank != nil {
		out["rank"] = map[string]any{"id": res.Rank.ID, "score": res.Rank.Score, "facts": res.Rank.Facts}
	}
	if explain {
		out["trace"] = stringsToAny(res.Trace)
	}
	return out
}

func stringsToAny(in []string) []any {
	out := make([]any, len(in))
	for i, s := range in {
		out[i] = s
	}
	return out
}

// statusFailure maps an HTTP-style status written in a rule onto a failure.
func statusFailure(code string, status int, message string, meta map[string]any) intent.Failure {
	cat := intent.CategoryInvalidInput
	switch {
	case status == 401:
		cat = intent.CategoryAuth
	case status == 403:
		cat = intent.CategoryPermission
	case status == 404:
		cat = intent.CategoryNotFound
	case status == 409:
		cat = intent.CategoryConflict
	case status == 429:
		cat = intent.CategoryRateLimit
	case status == 503:
		cat = intent.CategoryUnavailable
	case status == 504:
		cat = intent.CategoryTimeout
	case status >= 500:
		cat = intent.CategoryInternal
	}
	if message == "" {
		message = "this request was declined by policy"
	}
	return intent.Failure{Code: code, Category: cat, Message: message, Meta: meta}
}

func numberOf(v any) (float64, bool) {
	switch n := v.(type) {
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case float64:
		return n, true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
}

var rulesDecideAction = ActionFactoryFunc(func(build BuildContext, spec NodeSpec) (Action, error) {
	if err := exactlyOneOutput(spec); err != nil {
		return nil, err
	}
	definition, err := requiredString(spec.Config, "definition")
	if err != nil {
		return nil, fmt.Errorf("node %q: %w", spec.Name, err)
	}
	decision, err := requiredString(spec.Config, "decision")
	if err != nil {
		return nil, fmt.Errorf("node %q: %w", spec.Name, err)
	}
	program, err := decisionFor(build, spec, definition)
	if err != nil {
		return nil, err
	}
	if _, ok := program.Decisions[decision]; !ok && program.Rankings[decision] == nil {
		return nil, fmt.Errorf("node %q: definition %q declares no decision %q", spec.Name, definition, decision)
	}
	failOn := configStrings(spec.Config, "fail_on")
	explain := configBool(spec.Config, "explain", false)
	only := configStrings(spec.Config, "inputs")
	live, _ := requireResource[*rulesEngineWrapper](build, spec, "a rules.engine resource")

	return ActionFunc(func(ctx *ActionContext) (ActionResult, error) {
		// The definition is resolved on every call, so a rule an operator
		// publishes at runtime governs the very next message.
		program := program
		if current, err := live.program(definition); err == nil {
			program = current
		}
		input := decisionInput(ctx, only)
		res, err := evaluateDecision(program, decision, input, explain)
		if err != nil {
			return ActionResult{}, fmt.Errorf("rules.decide %s/%s: %w", definition, decision, err)
		}
		out := decisionMap(res, explain)
		if slices.Contains(failOn, res.Effect) {
			attrs, _ := out["attributes"].(map[string]any)
			code := Stringify(attrs["code"])
			if code == "" {
				code = res.ReasonCode
			}
			if code == "" {
				code = strings.ToUpper(res.Effect)
			}
			status := 422
			if n, ok := numberOf(attrs["status"]); ok {
				status = int(n)
			}
			return ActionResult{}, statusFailure(code, status, res.Reason, attrs)
		}
		return singleOutput(spec, out), nil
	}), nil
})

// decisionInput is what the rules see: the node's facts, plus the caller.
func decisionInput(ctx *ActionContext, only []string) map[string]any {
	input := make(map[string]any, len(ctx.Inputs)+2)
	if len(only) == 0 {
		for k, v := range ctx.Inputs {
			input[k] = v
		}
	} else {
		for _, k := range only {
			if v, ok := ctx.Inputs[k]; ok {
				input[k] = v
			}
		}
	}
	input["principal"] = principalMap(ctx.Principal)
	return input
}

var rulesRankAction = ActionFactoryFunc(func(build BuildContext, spec NodeSpec) (Action, error) {
	if err := exactlyOneOutput(spec); err != nil {
		return nil, err
	}
	definition, err := requiredString(spec.Config, "definition")
	if err != nil {
		return nil, fmt.Errorf("node %q: %w", spec.Name, err)
	}
	program, err := decisionFor(build, spec, definition)
	if err != nil {
		return nil, err
	}
	candidatesFact := configString(spec.Config, "candidates_fact", "candidates")
	eligibility := configString(spec.Config, "eligibility", "")
	ranking := configString(spec.Config, "ranking", "")
	rankingFact := configString(spec.Config, "ranking_fact", "")
	if ranking == "" && rankingFact == "" {
		return nil, fmt.Errorf("node %q: rules.rank needs config.ranking or config.ranking_fact", spec.Name)
	}
	limit, err := configInt(spec.Config, "limit", 0)
	if err != nil {
		return nil, fmt.Errorf("node %q: %w", spec.Name, err)
	}

	live, _ := requireResource[*rulesEngineWrapper](build, spec, "a rules.engine resource")
	// eligibility_extra: further per-candidate decisions, "definition/decision",
	// run after the main one, usually in a definition an operator generates at
	// runtime. A deny rejects the candidate; an allow that matched a rule (not
	// the default) merges its attributes into the candidate's facts.
	type extraRule struct {
		definition, decision string
		perCandidate         bool
	}
	var extras []extraRule
	for _, ref := range configStrings(spec.Config, "eligibility_extra") {
		def, dec, ok := strings.Cut(ref, "/")
		if !ok || def == "" || dec == "" {
			return nil, fmt.Errorf("node %q: eligibility_extra %q must read definition/decision", spec.Name, ref)
		}
		extras = append(extras, extraRule{def, dec, strings.Contains(dec, "{id}")})
	}

	return ActionFunc(func(ctx *ActionContext) (ActionResult, error) {
		program := program
		if current, err := live.program(definition); err == nil {
			program = current
		}
		raw, _ := resolvePath(ctx.Inputs, candidatesFact)
		list := candidateList(raw)
		name := ranking
		if rankingFact != "" {
			if v, ok := resolvePath(ctx.Inputs, rankingFact); ok && Stringify(v) != "" {
				name = Stringify(v)
			}
		}
		if name == "" {
			return ActionResult{}, invalidInput("rules.rank: no ranking was chosen")
		}
		if program.Rankings[name] == nil {
			return ActionResult{}, invalidInput("rules.rank: definition %q has no ranking %q", definition, name)
		}
		base := decisionInput(ctx, nil)
		delete(base, candidatesFact)

		type cand struct {
			id    string
			facts map[string]any
		}
		var (
			eligible []cand
			rejected []any
			trans    bool
		)
		// One environment serves every candidate: only "provider" changes, and an
		// evaluation does not keep a reference to its input.
		env := copyMap(base)
		for _, item := range list {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			// A candidate is { id, facts }, or simply a row with an id (what a
			// database.query returns), whose columns are its facts.
			facts, _ := m["facts"].(map[string]any)
			if facts == nil {
				facts = m
			}
			c := cand{id: Stringify(m["id"]), facts: copyMap(facts)}
			// Extra decisions first: a deny settles it, and an allow that matched a rule
			// marks the candidate as granted, which the main decision may honour (an
			// operator's rule that names a provider admits it).
			type pending struct {
				attrs map[string]any
				rule  string
			}
			var granted []pending
			refused := false
			for _, extra := range extras {
				extraProgram, perr := live.program(extra.definition)
				if perr != nil {
					return ActionResult{}, fmt.Errorf("rules.rank: %w", perr)
				}
				decision := extra.decision
				if extra.perCandidate {
					decision = strings.ReplaceAll(decision, "{id}", c.id)
					if _, found := extraProgram.Decisions[decision]; !found {
						continue
					}
				}
				env["provider"] = c.facts
				res, err := evaluateDecision(extraProgram, decision, env, false)
				if err != nil {
					return ActionResult{}, fmt.Errorf("rules.rank %s/%s for %q: %w", extra.definition, decision, c.id, err)
				}
				if !res.Allowed {
					rejected = append(rejected, map[string]any{
						"id": c.id, "rule": res.PolicyID, "reason": res.Reason, "reason_code": res.ReasonCode,
						"attributes": res.Attributes,
					})
					refused = true
					break
				}
				if res.PolicyID != "" {
					granted = append(granted, pending{attrs: res.Attributes, rule: res.PolicyID})
					c.facts["granted"] = true
				}
			}
			if refused {
				continue
			}
			if eligibility != "" {
				env["provider"] = c.facts
				res, err := evaluateDecision(program, eligibility, env, false)
				if err != nil {
					return ActionResult{}, fmt.Errorf("rules.rank %s/%s for %q: %w", definition, eligibility, c.id, err)
				}
				if !res.Allowed {
					rejected = append(rejected, map[string]any{
						"id": c.id, "rule": res.PolicyID, "reason": res.Reason, "reason_code": res.ReasonCode,
						"attributes": res.Attributes,
					})
					if t, ok := res.Attributes["transient"].(bool); ok && t {
						trans = true
					}
					continue
				}
				for k, v := range res.Attributes {
					c.facts[k] = v
				}
				c.facts["eligibility_rule"] = res.PolicyID
			}
			// A rule's attributes (priority, exclusive, tier) win over the main decision's.
			for _, g := range granted {
				for k, v := range g.attrs {
					c.facts[k] = v
				}
				c.facts["custom_rule"] = g.rule
			}
			eligible = append(eligible, c)
		}

		// An exclusive candidate ends the chain at its priority: nothing ranked
		// below it is kept.
		floor, hasFloor := 0.0, false
		for _, c := range eligible {
			if ex, _ := c.facts["exclusive"].(bool); ex {
				if p, ok := numberOf(c.facts["priority"]); ok && (!hasFloor || p > floor) {
					floor, hasFloor = p, true
				}
			}
		}
		if hasFloor {
			kept := eligible[:0]
			for _, c := range eligible {
				if p, ok := numberOf(c.facts["priority"]); !ok || p >= floor {
					kept = append(kept, c)
				} else {
					rejected = append(rejected, map[string]any{"id": c.id, "rule": "exclusive", "reason": "a higher-priority exclusive candidate ends the chain", "reason_code": "EXCLUSIVE"})
				}
			}
			eligible = kept
		}

		// Best first. A candidate's score does not depend on the others, so each is
		// scored once (n evaluations, where selecting the best of what is left again
		// and again would take n(n+1)/2) and the chain is the stable order by score:
		// equal scores keep the order the candidates arrived in, which is exactly what
		// selecting the first best each time gives.
		type scored struct {
			facts map[string]any
			id    string
			score float64
		}
		ranked := make([]scored, 0, len(eligible))
		delete(env, "provider")
		for _, c := range eligible {
			env["candidates"] = []any{map[string]any{"id": c.id, "facts": c.facts}}
			res, err := evaluateDecision(program, name, env, false)
			if err != nil {
				return ActionResult{}, fmt.Errorf("rules.rank %s/%s for %q: %w", definition, name, c.id, err)
			}
			if res.Rank == nil {
				rejected = append(rejected, map[string]any{"id": c.id, "rule": name, "reason": "no ranking rule accepts this candidate", "reason_code": "UNRANKED"})
				continue
			}
			ranked = append(ranked, scored{facts: c.facts, id: c.id, score: res.Rank.Score})
		}
		sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].score > ranked[j].score })
		if limit > 0 && len(ranked) > limit {
			ranked = ranked[:limit]
		}
		chain := make([]any, 0, len(ranked))
		for _, r := range ranked {
			entry := copyMap(r.facts)
			entry["id"] = r.id
			entry["score"] = r.score
			chain = append(chain, entry)
		}
		sort.SliceStable(rejected, func(i, j int) bool {
			return Stringify(rejected[i].(map[string]any)["id"]) < Stringify(rejected[j].(map[string]any)["id"])
		})
		return singleOutput(spec, map[string]any{
			"chain": chain, "rejected": rejected, "ranking": name,
			"empty": len(chain) == 0, "transient": trans && len(chain) == 0,
		}), nil
	}), nil
})

// candidateList reads a list of candidates however it arrived.
func candidateList(raw any) []any {
	switch list := raw.(type) {
	case []any:
		return list
	case []map[string]any:
		out := make([]any, len(list))
		for i, m := range list {
			out[i] = m
		}
		return out
	}
	return nil
}

func copyMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in)+4)
	for k, v := range in {
		out[k] = v
	}
	return out
}
