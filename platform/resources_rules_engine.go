package platform

import (
	"context"
	"fmt"
	"io"

	"github.com/oarkflow/rules"
	rulesStorage "github.com/oarkflow/rules/pkg/storage"
)

// ---------------------------------------------------------------------------
// rules.engine — Compiled condition and policy evaluation via oarkflow/rules
// ---------------------------------------------------------------------------
//
// This resource integrates the oarkflow/rules decision engine into REF,
// providing compiled BCL-based condition evaluation, event-driven rule chains,
// blast-radius analysis, approval gates, counterfactual "what-if" queries,
// shadow comparison, and governance overlays.
//
// Where authz.engine handles identity-based access control (who can do what),
// rules.engine handles business-logic policy decisions (should this action be
// taken given the current state of the world).
//
// Examples of what rules.engine solves that authz cannot:
//
//   - "Approve this loan only if credit_score >= 700 AND debt_ratio < 0.4"
//   - "Route this support ticket to tier-2 if severity >= 'high'"
//   - "Block this transaction if amount > 10000 AND country IN ['XX', 'YY']"
//   - "Dynamically price this product based on demand, inventory and region"
//   - "Apply a 15% discount if customer_tier == 'gold' AND cart_total > 100"
//
// BCL usage:
//
//	resource "policy_engine" {
//	  kind "rules.engine"
//	  config {
//	    environment "production"
//	    default_tenant "default"
//	    strict_validation true
//	    request_timeout 5s
//	  }
//	}
//
// Then use it as a decision node:
//
//	node "evaluate-policy" {
//	  uses "rules.evaluate"
//	  resource "policy_engine"
//	  kind decision
//	  requires [input]
//	  provides [policy_decision]
//	  config {
//	    decision "loan.eligibility"
//	    bundle "loan-rules"
//	    strict true
//	  }
//	}

// Registration is called from resources.go and builtins.go — no init() needed.

func registerRulesEngineResource(r *Registry) {
	mustResource(r, "rules.engine", ResourceFactoryFunc(openRulesEngine), ResourceKindInfo{
		Family:   "rules",
		Summary:  "Compiled condition and policy evaluation engine. Evaluates BCL decision programs with typed inputs, counterfactuals, governance overlays, and audit trails.",
		Provides: []string{"RulesEngine"},
		Config: []ConfigField{
			{Name: "environment", Type: "string", Default: "development", Summary: "Runtime environment (development, staging, production)"},
			{Name: "default_tenant", Type: "string", Default: "default", Summary: "Default tenant for rule evaluation"},
			{Name: "strict_validation", Type: "bool", Default: "true", Summary: "Reject unknown fields in rule definitions"},
			{Name: "strict_evaluation", Type: "bool", Default: "false", Summary: "Fail on undefined variables instead of treating as nil"},
			{Name: "request_timeout", Type: "duration", Default: "5s", Summary: "Maximum time for a single rule evaluation"},
			{Name: "max_request_bytes", Type: "int", Default: "1048576", Summary: "Maximum request body size"},
		},
	})
}

// rulesEngineWrapper wraps oarkflow/rules.Service as a REF platform resource.
type rulesEngineWrapper struct {
	service *rules.Service
}

func openRulesEngine(_ context.Context, spec ResourceSpec) (Resource, io.Closer, error) {
	if err := rejectUnknownConfig("rules.engine", spec.Config,
		"environment", "default_tenant", "strict_validation", "strict_evaluation",
		"request_timeout", "max_request_bytes"); err != nil {
		return nil, nil, err
	}

	requestTimeout, err := configDuration(spec.Config, "request_timeout", 0)
	if err != nil {
		return nil, nil, fmt.Errorf("resource %q: invalid request_timeout: %w", spec.Name, err)
	}
	maxRequestBytes, err := configInt(spec.Config, "max_request_bytes", 1<<20)
	if err != nil {
		return nil, nil, fmt.Errorf("resource %q: invalid max_request_bytes: %w", spec.Name, err)
	}

	cfg := rules.Config{
		Environment:      configString(spec.Config, "environment", "development"),
		DefaultTenant:    configString(spec.Config, "default_tenant", "default"),
		StrictValidation: configBool(spec.Config, "strict_validation", true),
		StrictEvaluation: configBool(spec.Config, "strict_evaluation", false),
		RequestTimeout:   requestTimeout,
		MaxRequestBytes:  int64(maxRequestBytes),
	}

	store := rulesStorage.NewMemoryStore()
	svc := rules.NewService(store, cfg)

	wrapper := &rulesEngineWrapper{service: svc}
	return wrapper, noopCloser{}, nil
}

// Service returns the underlying rules service for action handlers.
func (w *rulesEngineWrapper) Service() *rules.Service {
	return w.service
}

// ---------------------------------------------------------------------------
// rules.evaluate — Decision node action for evaluating a published rule
// ---------------------------------------------------------------------------

func registerRulesActions(r *Registry) {
	mustAction(r, "rules.evaluate", rulesEvaluateAction, ActionInfo{
		Family:       "rules",
		Summary:      "Evaluate a published rule definition against the current request input. Returns the decision report as a fact.",
		ResourceKind: "rules.engine",
		Kind:         "decision",
		Config: []ConfigField{
			{Name: "definition", Type: "string", Summary: "The published definition name to evaluate"},
			{Name: "decision", Type: "string", Required: true, Summary: "The decision name to evaluate (e.g. 'loan.eligibility')"},
			{Name: "bundle", Type: "string", Summary: "Named bundle to evaluate against. Uses latest if empty."},
			{Name: "tenant_id", Type: "string", Summary: "Override the engine's default tenant"},
			{Name: "strict", Type: "bool", Default: "false", Summary: "Fail on unknown input fields"},
			{Name: "counterfactuals", Type: "bool", Default: "false", Summary: "Include what-if analysis in the response"},
			{Name: "include_gates", Type: "bool", Default: "false", Summary: "Include decision gate details"},
		},
	})

	mustAction(r, "rules.publish", rulesPublishAction, ActionInfo{
		Family:       "rules",
		Summary:      "Publish a rule definition from source (inline BCL or file path). Makes it available for evaluation.",
		ResourceKind: "rules.engine",
		Kind:         "effect",
		Config: []ConfigField{
			{Name: "name", Type: "string", Required: true, Summary: "Rule name"},
			{Name: "version", Type: "string", Default: "1", Summary: "Rule version"},
			{Name: "source", Type: "string", Summary: "Inline BCL source for the rule definition"},
			{Name: "path", Type: "string", Summary: "File path to the BCL rule definition"},
			{Name: "tenant_id", Type: "string"},
			{Name: "run_tests", Type: "bool", Default: "true", Summary: "Run embedded tests before activating"},
		},
	})

	mustAction(r, "rules.chain", rulesChainAction, ActionInfo{
		Family:       "rules",
		Summary:      "Evaluate a stateful event chain — processes event-driven rule sequences with entity state.",
		ResourceKind: "rules.engine",
		Kind:         "decision",
		Config: []ConfigField{
			{Name: "definition", Type: "string", Required: true, Summary: "Published definition name"},
			{Name: "chain", Type: "string", Required: true, Summary: "Chain ID within the definition"},
			{Name: "event", Type: "string", Summary: "Event name to trigger"},
			{Name: "entity_key", Type: "string", Summary: "Entity key for stateful chains"},
			{Name: "tenant_id", Type: "string"},
		},
	})
}

// rulesEvaluateAction evaluates a decision and maps the result to a REF decision.
var rulesEvaluateAction = ActionFactoryFunc(func(build BuildContext, spec NodeSpec) (Action, error) {
	decisionName := configString(spec.Config, "decision", "")
	if decisionName == "" {
		return nil, fmt.Errorf("node %s: rules.evaluate requires a 'decision' config", spec.Name)
	}
	definitionName := configString(spec.Config, "definition", "")
	bundle := configString(spec.Config, "bundle", "")
	tenantOverride := configString(spec.Config, "tenant_id", "")
	strict := configBool(spec.Config, "strict", false)
	counterfactuals := configBool(spec.Config, "counterfactuals", false)
	includeGates := configBool(spec.Config, "include_gates", false)

	return ActionFunc(func(ctx *ActionContext) (ActionResult, error) {
		resource, ok := ctx.Resource.(*rulesEngineWrapper)
		if !ok {
			return ActionResult{Decision: &Decision{Allow: false, Message: "rules engine resource not available"}},
				fmt.Errorf("node %s: expected rules.engine resource", spec.Name)
		}

		// Build the input map from all available facts
		input := make(map[string]any)
		if ctx.Inputs != nil {
			input = ctx.Inputs
		}

		tenantID := tenantOverride
		if tenantID == "" {
			tenantID = resource.service.Config().DefaultTenant
		}

		req := rules.EvaluateRequest{
			TenantID:        tenantID,
			Decision:        decisionName,
			Input:           input,
			Bundle:          bundle,
			Strict:          strict,
			Counterfactuals: counterfactuals,
			IncludeGates:    includeGates,
		}

		resp, err := resource.service.Evaluate(ctx.Context, definitionName, req)
		if err != nil {
			return ActionResult{Decision: &Decision{Allow: false, Message: fmt.Sprintf("rule evaluation failed: %v", err)}}, err
		}

		// Map the rules decision to REF's decision algebra
		allowed := true
		message := ""
		if resp.Report != nil && resp.Report.Decision != nil {
			allowed = resp.Report.Decision.Allowed
			message = resp.Report.Decision.Reason
		}

		result := ActionResult{
			Decision: &Decision{Allow: allowed, Message: message},
			Outputs:  map[string]any{"report": resp},
		}
		return result, nil
	}), nil
})

// rulesPublishAction publishes a rule definition to the engine.
var rulesPublishAction = ActionFactoryFunc(func(build BuildContext, spec NodeSpec) (Action, error) {
	name := configString(spec.Config, "name", "")
	if name == "" {
		return nil, fmt.Errorf("node %s: rules.publish requires a 'name' config", spec.Name)
	}

	return ActionFunc(func(ctx *ActionContext) (ActionResult, error) {
		resource, ok := ctx.Resource.(*rulesEngineWrapper)
		if !ok {
			return ActionResult{}, fmt.Errorf("node %s: expected rules.engine resource", spec.Name)
		}

		req := rules.PublishRequest{
			TenantID:     configString(spec.Config, "tenant_id", resource.service.Config().DefaultTenant),
			Name:         name,
			Version:      configString(spec.Config, "version", "1"),
			Source:       configString(spec.Config, "source", ""),
			Path:         configString(spec.Config, "path", ""),
			RunTests:     configBool(spec.Config, "run_tests", true),
			RequireTests: false,
		}

		resp, err := resource.service.Publish(ctx.Context, req)
		if err != nil {
			return ActionResult{}, fmt.Errorf("node %s: publish failed: %w", spec.Name, err)
		}

		return ActionResult{Outputs: map[string]any{"published": resp}}, nil
	}), nil
})

// rulesChainAction evaluates a stateful event chain.
var rulesChainAction = ActionFactoryFunc(func(build BuildContext, spec NodeSpec) (Action, error) {
	definitionName := configString(spec.Config, "definition", "")
	if definitionName == "" {
		return nil, fmt.Errorf("node %s: rules.chain requires a 'definition' config", spec.Name)
	}
	chainID := configString(spec.Config, "chain", "")
	if chainID == "" {
		return nil, fmt.Errorf("node %s: rules.chain requires a 'chain' config", spec.Name)
	}

	return ActionFunc(func(ctx *ActionContext) (ActionResult, error) {
		resource, ok := ctx.Resource.(*rulesEngineWrapper)
		if !ok {
			return ActionResult{Decision: &Decision{Allow: false, Message: "rules engine resource not available"}},
				fmt.Errorf("node %s: expected rules.engine resource", spec.Name)
		}

		input := make(map[string]any)
		if ctx.Inputs != nil {
			input = ctx.Inputs
		}

		tenantID := configString(spec.Config, "tenant_id", resource.service.Config().DefaultTenant)
		entityKey := configString(spec.Config, "entity_key", "")
		event := configString(spec.Config, "event", "")

		req := rules.ChainEvaluateRequest{
			TenantID:  tenantID,
			Input:     input,
			Event:     event,
			EntityKey: entityKey,
		}

		resp, err := resource.service.EvaluateChain(ctx.Context, definitionName, chainID, req)
		if err != nil {
			return ActionResult{Decision: &Decision{Allow: false, Message: fmt.Sprintf("chain evaluation failed: %v", err)}}, err
		}

		allowed := true
		message := ""
		if resp.Evaluation.FinalDecision != nil {
			allowed = resp.Evaluation.FinalDecision.Allowed
			message = resp.Evaluation.FinalDecision.Reason
		}

		return ActionResult{
			Decision: &Decision{Allow: allowed, Message: message},
			Outputs:  map[string]any{"evaluation": resp},
		}, nil
	}), nil
})

type noopCloser struct{}

func (noopCloser) Close() error { return nil }
