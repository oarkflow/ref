package platform

import (
	"context"
	"fmt"
	"io"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/oarkflow/bcl"
	"github.com/oarkflow/ref/platform/spi"
)

// registerSMSRouterResources installs the sms.router resource provider.
func registerSMSRouterResources(r *Registry) {
	mustResource(r, "sms.router", ResourceFactoryFunc(openSMSRouter), ResourceKindInfo{
		Family:   "service",
		Summary:  "Parameter-based SMS router with rules-based provider ranking, health-aware filtering, and automatic failover.",
		Provides: []string{"SMSProvider", "SMSRouter", "Notifier"},
		Config: []ConfigField{
			{Name: "providers", Type: "any", Required: true, Summary: "List or map of provider configurations (name, resource, priority, cost, tier)"},
			{Name: "routing_rules", Type: "string", Summary: "Published rules.engine definition name for parameter-based ranking"},
			{Name: "rules_decision", Type: "string", Default: "provider_chain", Summary: "Decision name in the routing_rules definition"},
			{Name: "rules_engine", Type: "resource", Summary: "Specific rules.engine resource name; default first available"},
			{Name: "failover", Type: "bool", Default: "true", Summary: "Automatically try next candidate on retryable failure"},
			{Name: "max_retries", Type: "int", Default: "3", Summary: "Max provider attempts per message"},
			{Name: "retry_budget", Type: "duration", Default: "0s", Summary: "Pause duration between failover attempts"},
			{Name: "min_success_rate", Type: "float", Default: "0.80", Summary: "Exclude or de-prioritize providers below this threshold (0.0 - 1.0)"},
			{Name: "health_check_interval", Type: "duration", Default: "30s"},
			{Name: "dlr_feedback_queue", Type: "resource", Summary: "Queue to listen on for adaptive DLR feedback"},
		},
	})
}

// SMSRouterProviderConfig configures a single provider in the router.
type SMSRouterProviderConfig struct {
	Name           string  `json:"name"`
	Resource       string  `json:"resource"`
	Priority       int     `json:"priority"`
	Cost           float64 `json:"cost"`
	Tier           int     `json:"tier"`
	Weight         float64 `json:"weight"`
	Enabled        bool    `json:"enabled"`
	CircuitBreaker string  `json:"circuit_breaker"`
	RateLimiter    string  `json:"rate_limiter"`
}

// SMSRouterConfig configures the sms.router resource.
type SMSRouterConfig struct {
	Providers           []SMSRouterProviderConfig `json:"providers"`
	RoutingRules        string                    `json:"routing_rules"`
	RulesDecision       string                    `json:"rules_decision"`
	RulesEngine         string                    `json:"rules_engine"`
	Failover            bool                      `json:"failover"`
	MaxRetries          int                       `json:"max_retries"`
	RetryBudget         time.Duration             `json:"retry_budget"`
	MinSuccessRate      float64                   `json:"min_success_rate"`
	HealthCheckInterval time.Duration             `json:"health_check_interval"`
	DLRFeedbackQueue    string                    `json:"dlr_feedback_queue"`
}

type routerProvider struct {
	name     string
	provider spi.SMSProvider
	health   spi.SMSProviderHealth
	cb       spi.CircuitBreaker
	rl       spi.RateLimiter
	priority int
	cost     float64
	tier     int
	weight   float64
	enabled  bool

	// Provider metrics
	submitsTotal   atomic.Int64
	submitsSuccess atomic.Int64
	submitsFailed  atomic.Int64
	totalLatencyMs atomic.Int64
}

// SMSRouter is the router resource instance.
type SMSRouter struct {
	name        string
	cfg         SMSRouterConfig
	providers   map[string]*routerProvider
	rulesEngine *rulesEngineWrapper
	ruleProgram *bcl.DecisionProgram
	mu          sync.RWMutex

	failoversTotal atomic.Int64
}

var (
	_ spi.SMSProvider       = (*SMSRouter)(nil)
	_ spi.SMSProviderHealth = (*SMSRouter)(nil)
	_ spi.Notifier          = (*SMSRouter)(nil)
	_ io.Closer             = (*SMSRouter)(nil)
)

func openSMSRouter(_ context.Context, spec ResourceSpec) (Resource, io.Closer, error) {
	var cfg SMSRouterConfig
	cfg.Failover = configBool(spec.Config, "failover", true)
	maxRetries, _ := configInt(spec.Config, "max_retries", 3)
	cfg.MaxRetries = maxRetries
	rb, _ := configDuration(spec.Config, "retry_budget", 0)
	cfg.RetryBudget = rb
	minRate, _ := configFloat(spec.Config, "min_success_rate", 0.80)
	cfg.MinSuccessRate = minRate
	cfg.RoutingRules = configString(spec.Config, "routing_rules", "")
	cfg.RulesDecision = configString(spec.Config, "rules_decision", "provider_chain")
	cfg.RulesEngine = configString(spec.Config, "rules_engine", "")
	cfg.DLRFeedbackQueue = configString(spec.Config, "dlr_feedback_queue", "")

	rawProviders, _ := spec.Config["providers"]
	parsedProviders, err := parseRouterProviders(rawProviders)
	if err != nil {
		return nil, nil, fmt.Errorf("sms.router %q: %w", spec.Name, err)
	}
	cfg.Providers = parsedProviders

	if len(cfg.Providers) == 0 {
		return nil, nil, fmt.Errorf("sms.router %q: at least one provider must be configured", spec.Name)
	}

	router := &SMSRouter{
		name:      spec.Name,
		cfg:       cfg,
		providers: make(map[string]*routerProvider),
	}

	// Resolve providers
	for _, pcfg := range cfg.Providers {
		resName := pcfg.Resource
		if resName == "" {
			resName = pcfg.Name
		}
		res, ok := spec.resolved[resName]
		if !ok {
			return nil, nil, fmt.Errorf("sms.router %q: provider resource %q not found", spec.Name, resName)
		}

		smsProv, ok := res.(spi.SMSProvider)
		if !ok {
			// Check if it's a Notifier we can wrap
			if notif, isNotif := res.(spi.Notifier); isNotif {
				smsProv = &notifierSMSAdapter{name: pcfg.Name, notifier: notif}
			} else {
				return nil, nil, fmt.Errorf("sms.router %q: resource %q does not satisfy spi.SMSProvider or spi.Notifier", spec.Name, resName)
			}
		}

		rp := &routerProvider{
			name:     pcfg.Name,
			provider: smsProv,
			priority: pcfg.Priority,
			cost:     pcfg.Cost,
			tier:     pcfg.Tier,
			weight:   pcfg.Weight,
			enabled:  pcfg.Enabled,
		}
		if pcfg.Tier == 0 {
			rp.tier = 1
		}
		if pcfg.Priority == 0 {
			rp.priority = 10
		}

		if h, ok := smsProv.(spi.SMSProviderHealth); ok {
			rp.health = h
		}

		if pcfg.CircuitBreaker != "" {
			if cbRes, ok := spec.resolved[pcfg.CircuitBreaker]; ok {
				if cb, ok := cbRes.(spi.CircuitBreaker); ok {
					rp.cb = cb
				}
			}
		}

		if pcfg.RateLimiter != "" {
			if rlRes, ok := spec.resolved[pcfg.RateLimiter]; ok {
				if rl, ok := rlRes.(spi.RateLimiter); ok {
					rp.rl = rl
				}
			}
		}

		router.providers[pcfg.Name] = rp
	}

	// Resolve optional rules engine
	if cfg.RulesEngine != "" {
		if re, ok := spec.resolved[cfg.RulesEngine].(*rulesEngineWrapper); ok {
			router.rulesEngine = re
		}
	} else if cfg.RoutingRules != "" {
		// Auto-discover rules engine in resolved resources
		for _, r := range spec.resolved {
			if re, ok := r.(*rulesEngineWrapper); ok {
				router.rulesEngine = re
				break
			}
		}
	}

	if router.rulesEngine != nil && cfg.RoutingRules != "" {
		record, err := router.rulesEngine.service.GetDefinition(context.Background(), cfg.RoutingRules)
		if err == nil && record.Program != nil {
			router.ruleProgram = record.Program
		}
	}
	if router.ruleProgram == nil && cfg.RoutingRules != "" {
		if prog, err := bcl.CompileDecisionFile(cfg.RoutingRules, &bcl.Options{AllowTime: true}); err == nil {
			router.ruleProgram = prog
		}
	}

	return router, router, nil
}

// ProviderID returns the router's resource name.
func (r *SMSRouter) ProviderID() string {
	return r.name
}

// CandidateInfo contains ranking details for a provider candidate.
type CandidateInfo struct {
	Name     string  `json:"name"`
	Tier     int     `json:"tier"`
	Priority int     `json:"priority"`
	Cost     float64 `json:"cost"`
}

// SelectAndRankCandidates returns the evaluated and ranked candidates for inspection or simulation.
func (r *SMSRouter) SelectAndRankCandidates(ctx context.Context, msg spi.SMSMessage, hints map[string]any) []CandidateInfo {
	ranked := r.selectAndRankCandidates(ctx, msg, hints)
	out := make([]CandidateInfo, len(ranked))
	for i, c := range ranked {
		out[i] = CandidateInfo{
			Name:     c.name,
			Tier:     c.tier,
			Priority: c.priority,
			Cost:     c.cost,
		}
	}
	return out
}

// NewSMSRouter creates an SMSRouter instance directly with providers and optional rule program.
func NewSMSRouter(name string, cfg SMSRouterConfig, providers map[string]spi.SMSProvider, ruleProgram *bcl.DecisionProgram) *SMSRouter {
	router := &SMSRouter{
		name:        name,
		cfg:         cfg,
		providers:   make(map[string]*routerProvider),
		ruleProgram: ruleProgram,
	}
	for _, pcfg := range cfg.Providers {
		pName := pcfg.Name
		if p, ok := providers[pName]; ok {
			rp := &routerProvider{
				name:     pName,
				provider: p,
				priority: pcfg.Priority,
				cost:     pcfg.Cost,
				tier:     pcfg.Tier,
				weight:   pcfg.Weight,
				enabled:  pcfg.Enabled,
			}
			if rp.tier == 0 {
				rp.tier = 1
			}
			if rp.priority == 0 {
				rp.priority = 10
			}
			if h, ok := p.(spi.SMSProviderHealth); ok {
				rp.health = h
			}
			router.providers[pName] = rp
		}
	}
	return router
}

// Close closes the router.
func (r *SMSRouter) Close() error {
	return nil
}

// Submit routes and sends one message across the provider chain with automatic failover.
func (r *SMSRouter) Submit(ctx context.Context, msg spi.SMSMessage) (spi.SMSResult, error) {
	return r.RouteAndSubmit(ctx, msg, nil)
}

// RouteAndSubmit performs parameter-based routing with optional routing hints (country, mno, tier, etc.).
func (r *SMSRouter) RouteAndSubmit(ctx context.Context, msg spi.SMSMessage, hints map[string]any) (spi.SMSResult, error) {
	candidates := r.selectAndRankCandidates(ctx, msg, hints)
	if len(candidates) == 0 {
		return spi.SMSResult{
			OK:         false,
			ProviderID: r.name,
			Attempts:   0,
			Error: &spi.SMSError{
				Kind:      "no_route",
				Message:   "no eligible or healthy SMS provider available",
				Retryable: false,
			},
		}, nil
	}

	maxRetries := r.cfg.MaxRetries
	if maxRetries <= 0 {
		maxRetries = 1
	}

	var lastResult spi.SMSResult
	var attempts int

	for i, c := range candidates {
		if attempts >= maxRetries {
			break
		}
		attempts++

		// Check rate limiter if configured
		if c.rl != nil {
			allowed, _, _, err := c.rl.Allow(ctx, c.name, 1000, time.Second)
			if err == nil && !allowed {
				continue
			}
		}

		started := time.Now()
		c.submitsTotal.Add(1)

		// Set failed provider tag for downstream rules awareness
		if i > 0 && msg.Tags == nil {
			msg.Tags = make(map[string]string)
		}
		if i > 0 {
			msg.Tags["provider_failed"] = candidates[i-1].name
		}

		res, err := c.provider.Submit(ctx, msg)
		latency := time.Since(started).Milliseconds()
		c.totalLatencyMs.Add(latency)

		if err != nil {
			c.submitsFailed.Add(1)
			if c.cb != nil {
				_ = c.cb.Record(ctx, c.name, false)
			}
			lastResult = spi.SMSResult{
				OK:         false,
				ProviderID: c.name,
				LatencyMs:  latency,
				Attempts:   attempts,
				Error: &spi.SMSError{
					Kind:      "internal",
					Message:   err.Error(),
					Retryable: true,
				},
			}
		} else {
			lastResult = res
			lastResult.Attempts = attempts
			if res.OK {
				c.submitsSuccess.Add(1)
				if c.cb != nil {
					_ = c.cb.Record(ctx, c.name, true)
				}
				return lastResult, nil
			}
			c.submitsFailed.Add(1)
			if c.cb != nil {
				_ = c.cb.Record(ctx, c.name, false)
			}
		}

		// Failover decision
		if !r.cfg.Failover || (lastResult.Error != nil && !lastResult.Error.Retryable) {
			break
		}

		if attempts < maxRetries && i+1 < len(candidates) {
			r.failoversTotal.Add(1)
			if r.cfg.RetryBudget > 0 {
				select {
				case <-time.After(r.cfg.RetryBudget):
				case <-ctx.Done():
					lastResult.Error = &spi.SMSError{
						Kind:      "timeout",
						Message:   ctx.Err().Error(),
						Retryable: false,
					}
					return lastResult, nil
				}
			}
		}
	}

	return lastResult, nil
}

type rankedCandidate struct {
	provider  *routerProvider
	score     float64
	tier      int
	healthy   bool
	preferred bool
}

func (r *SMSRouter) selectAndRankCandidates(ctx context.Context, msg spi.SMSMessage, hints map[string]any) []*routerProvider {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var ranked []rankedCandidate

	for _, p := range r.providers {
		if !p.enabled {
			continue
		}

		// 1. Circuit Breaker check
		if p.cb != nil {
			allowed, err := p.cb.Allow(ctx, p.name)
			if err == nil && !allowed {
				continue // circuit open, skip provider
			}
		}

		// 2. Health check
		healthy := true
		successRate := 1.0
		if p.health != nil {
			h := p.health.Health(ctx)
			if !h.Available || h.SuccessRate < r.cfg.MinSuccessRate {
				healthy = false
			}
			successRate = h.SuccessRate
		}

		// Base score calculation: priority * 10 + health * 20 - cost * 50
		score := float64(p.priority)*10.0 + (successRate * 20.0) - (p.cost * 50.0)

		ranked = append(ranked, rankedCandidate{
			provider: p,
			score:    score,
			tier:     p.tier,
			healthy:  healthy,
		})
	}

	// 3. Rules Engine pre-filter and attribute overrides
	if r.ruleProgram != nil {
		msgMap := map[string]any{
			"to":   msg.To,
			"from": msg.From,
			"text": msg.Text,
			"dlr":  msg.DLR,
			"tags": msg.Tags,
		}
		if hints != nil {
			for k, v := range hints {
				msgMap[k] = v
			}
		}
		ruleInput := map[string]any{
			"message": msgMap,
		}
		if hints != nil {
			for k, v := range hints {
				ruleInput[k] = v
			}
		}

		engine := bcl.NewDecisionEngine(r.ruleProgram, optionsFor(r.ruleProgram))
		res, err := engine.EvaluateWithOptions(r.cfg.RulesDecision, ruleInput, bcl.DecisionEvaluateOptions{})
		if err == nil && res != nil {
			// Preferred provider(s)
			preferredProviders := make(map[string]float64)
			if pName, ok := res.Attributes["provider"].(string); ok && pName != "" {
				preferredProviders[pName] = 1000.0
			}
			if pList, ok := res.Attributes["providers"].([]any); ok {
				boost := 1000.0
				for _, p := range pList {
					if pStr, ok := p.(string); ok && pStr != "" {
						if _, exists := preferredProviders[pStr]; !exists {
							preferredProviders[pStr] = boost
							if boost > 100.0 {
								boost -= 200.0
							}
						}
					}
				}
			} else if pListStr, ok := res.Attributes["providers"].([]string); ok {
				boost := 1000.0
				for _, pStr := range pListStr {
					if pStr != "" {
						if _, exists := preferredProviders[pStr]; !exists {
							preferredProviders[pStr] = boost
							if boost > 100.0 {
								boost -= 200.0
							}
						}
					}
				}
			}

			// Excluded provider(s)
			excludedProviders := make(map[string]bool)
			if ex, ok := res.Attributes["exclude_provider"].(string); ok && ex != "" {
				excludedProviders[ex] = true
			}
			if exList, ok := res.Attributes["exclude_providers"].([]any); ok {
				for _, p := range exList {
					if pStr, ok := p.(string); ok {
						excludedProviders[pStr] = true
					}
				}
			} else if exListStr, ok := res.Attributes["exclude_providers"].([]string); ok {
				for _, pStr := range exListStr {
					excludedProviders[pStr] = true
				}
			}

			// Max cost limit filter
			maxCost := 0.0
			if mc, ok := res.Attributes["max_cost"]; ok {
				maxCost = toFloat(mc)
			} else if hints != nil {
				if mc, ok := hints["max_cost"]; ok {
					maxCost = toFloat(mc)
				}
			}

			ruleTier := 0
			if t, ok := res.Attributes["tier"]; ok {
				ruleTier = toInt(t)
			}
			rulePriority := 0
			if p, ok := res.Attributes["priority"]; ok {
				rulePriority = toInt(p)
			}

			var filteredRanked []rankedCandidate
			for i := range ranked {
				pName := ranked[i].provider.name
				if excludedProviders[pName] {
					continue
				}
				if maxCost > 0 && ranked[i].provider.cost > maxCost {
					continue
				}
				if boost, ok := preferredProviders[pName]; ok {
					ranked[i].preferred = true
					ranked[i].score += boost
					if ruleTier > 0 {
						ranked[i].tier = ruleTier
					}
					if rulePriority > 0 {
						ranked[i].score += float64(rulePriority * 10)
					}
				}
				filteredRanked = append(filteredRanked, ranked[i])
			}
			if len(filteredRanked) > 0 {
				ranked = filteredRanked
			}
		}
	}

	// 4. Sort candidates:
	// - Healthy providers first
	// - Explicitly preferred by rules policy first
	// - Lower Tier first (e.g. tier 1 before tier 2)
	// - Higher score first
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].healthy != ranked[j].healthy {
			return ranked[i].healthy // healthy comes before unhealthy
		}
		if ranked[i].preferred != ranked[j].preferred {
			return ranked[i].preferred // preferred comes before non-preferred
		}
		if ranked[i].tier != ranked[j].tier {
			return ranked[i].tier < ranked[j].tier // lower tier comes first
		}
		return ranked[i].score > ranked[j].score // higher score comes first
	})


	out := make([]*routerProvider, len(ranked))
	for i, rc := range ranked {
		out[i] = rc.provider
	}
	return out
}

// RecordDLR updates routing feedback when a delivery receipt is processed.
func (r *SMSRouter) RecordDLR(ctx context.Context, providerID string, status string, latencyMs int64) {
	r.mu.RLock()
	p, ok := r.providers[providerID]
	r.mu.RUnlock()

	if !ok || p == nil {
		return
	}

	isDelivered := status == "DELIVRD" || status == "delivered" || status == "success" || status == "sent"
	if isDelivered {
		p.submitsSuccess.Add(1)
	} else {
		p.submitsFailed.Add(1)
	}
}

// Health reports aggregate health for the router.
func (r *SMSRouter) Health(ctx context.Context) spi.SMSHealthReport {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var totalSuccess, totalFailed int64
	var activeProviders int

	for _, p := range r.providers {
		if !p.enabled {
			continue
		}
		activeProviders++
		totalSuccess += p.submitsSuccess.Load()
		totalFailed += p.submitsFailed.Load()
	}

	total := totalSuccess + totalFailed
	if total == 0 {
		return spi.SMSHealthReport{
			Available:   activeProviders > 0,
			SuccessRate: 1.0,
		}
	}

	successRate := float64(totalSuccess) / float64(total)
	return spi.SMSHealthReport{
		Available:   activeProviders > 0 && successRate > 0.2,
		SuccessRate: successRate,
	}
}

// Notify satisfies spi.Notifier for generic notification routing.
func (r *SMSRouter) Notify(ctx context.Context, msg spi.Notification) (string, error) {
	res, err := r.RouteAndSubmit(ctx, spi.SMSMessage{
		To:   msg.Target,
		Text: msg.Body,
	}, nil)
	if err != nil {
		return "", err
	}
	if !res.OK {
		if res.Error != nil {
			return "", fmt.Errorf("sms router failure: %s", res.Error.Message)
		}
		return "", fmt.Errorf("sms router delivery failed")
	}
	return res.ProviderMsgID, nil
}

// Metrics returns telemetry statistics for all configured providers.
func (r *SMSRouter) Metrics() map[string]any {
	r.mu.RLock()
	defer r.mu.RUnlock()

	providerStats := make(map[string]any)
	for name, p := range r.providers {
		total := p.submitsTotal.Load()
		success := p.submitsSuccess.Load()
		failed := p.submitsFailed.Load()
		var avgLatency int64
		if total > 0 {
			avgLatency = p.totalLatencyMs.Load() / total
		}
		providerStats[name] = map[string]any{
			"total":          total,
			"success":        success,
			"failed":         failed,
			"avg_latency_ms": avgLatency,
			"priority":       p.priority,
			"tier":           p.tier,
			"enabled":        p.enabled,
		}
	}

	return map[string]any{
		"router":          r.name,
		"providers":       providerStats,
		"failovers_total": r.failoversTotal.Load(),
	}
}

// notifierSMSAdapter wraps an existing spi.Notifier into an spi.SMSProvider.
type notifierSMSAdapter struct {
	name     string
	notifier spi.Notifier
}

func (a *notifierSMSAdapter) ProviderID() string {
	return a.name
}

func (a *notifierSMSAdapter) Submit(ctx context.Context, msg spi.SMSMessage) (spi.SMSResult, error) {
	started := time.Now()
	msgID, err := a.notifier.Notify(ctx, spi.Notification{
		Target: msg.To,
		Body:   msg.Text,
	})
	latency := time.Since(started).Milliseconds()
	if err != nil {
		return spi.SMSResult{
			OK:         false,
			ProviderID: a.name,
			LatencyMs:  latency,
			Attempts:   1,
			Error: &spi.SMSError{
				Kind:      "notifier_error",
				Message:   err.Error(),
				Retryable: true,
			},
		}, nil
	}
	return spi.SMSResult{
		OK:            true,
		ProviderMsgID: msgID,
		ProviderID:    a.name,
		LatencyMs:     latency,
		Attempts:      1,
	}, nil
}

func parseRouterProviders(raw any) ([]SMSRouterProviderConfig, error) {
	if raw == nil {
		return nil, nil
	}

	var out []SMSRouterProviderConfig

	switch v := raw.(type) {
	case []any:
		for _, item := range v {
			if m, ok := item.(map[string]any); ok {
				cfg := decodeProviderMap(m)
				if cfg.Name != "" {
					out = append(out, cfg)
				}
			}
		}
	case map[string]any:
		for name, val := range v {
			if m, ok := val.(map[string]any); ok {
				cfg := decodeProviderMap(m)
				if cfg.Name == "" {
					cfg.Name = name
				}
				out = append(out, cfg)
			}
		}
	default:
		return nil, fmt.Errorf("providers must be a list or map of provider configs")
	}

	return out, nil
}

func decodeProviderMap(m map[string]any) SMSRouterProviderConfig {
	name, _ := m["name"].(string)
	res, _ := m["resource"].(string)
	if res == "" {
		res = name
	}
	if name == "" {
		name = res
	}

	enabled := true
	if en, ok := m["enabled"].(bool); ok {
		enabled = en
	}

	return SMSRouterProviderConfig{
		Name:           name,
		Resource:       res,
		Priority:       toInt(m["priority"]),
		Cost:           toFloat(m["cost"]),
		Tier:           toInt(m["tier"]),
		Weight:         toFloat(m["weight"]),
		Enabled:        enabled,
		CircuitBreaker: toString(m["circuit_breaker"]),
		RateLimiter:    toString(m["rate_limiter"]),
	}
}

func toInt(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	default:
		return 0
	}
}

func toFloat(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case int64:
		return float64(n)
	default:
		return 0.0
	}
}

func toString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
