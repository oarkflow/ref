package sms

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/oarkflow/smppflow/pkg/message"
	"github.com/oarkflow/smppflow/pkg/provider"
	"github.com/oarkflow/smppflow/pkg/routing"
)

// Route tiers. A lower priority wins, and the chain a message follows is the
// eligible candidates ordered tier by tier, so an account's own provider is
// tried before a country provider, which is tried before the platform default.
const (
	prioUserCountry = 10
	prioUser        = 20
	prioTenant      = 30
	prioCountry     = 100
	prioPlatform    = 1000
)

func tierOf(priority int) string {
	switch {
	case priority <= prioUser:
		return "user"
	case priority <= prioTenant:
		return "tenant"
	case priority < prioPlatform:
		return "country"
	default:
		return "platform"
	}
}

// Objectives are the routing goals a message can have.
const (
	ObjectiveBalanced = "balanced"
	ObjectiveCost     = "lowest_cost"
	ObjectiveDelivery = "highest_delivery"
	ObjectiveLatency  = "lowest_latency"
)

// RoutingPolicy is the routing section of the hub's BCL config.
type RoutingPolicy struct {
	// Objectives maps a message type to the goal its route is ordered by.
	Objectives map[string]string `json:"objectives"`
	// Default is the objective for message types not listed.
	Default string `json:"default"`
	// QualityFloor rejects providers whose quality score is below the floor for
	// a message type: an OTP should not ride a provider that drops messages.
	QualityFloor map[string]float64 `json:"quality_floor"`
	// DeliveryFloor does the same for observed delivery rate (0..1).
	DeliveryFloor map[string]float64 `json:"delivery_floor"`
	// BreakerFailures and BreakerOpen configure the per-route circuit breaker.
	BreakerFailures int      `json:"breaker_failures"`
	BreakerOpen     Duration `json:"breaker_open"`
}

// ruleInfo is what the planner remembers about a rule the router knows.
type ruleInfo struct {
	tier      string
	exclusive bool
}

// Router plans routes. It owns an smppflow router, rebuilt from the directory
// whenever configuration changes, and keeps the router's health state (circuit
// breaker, provider statistics) across rebuilds.
type Router struct {
	mu      sync.RWMutex
	senders map[string][]string // provider -> sender countries
	r       *routing.Router
	cb      *routing.CircuitBreaker
	sla     *routing.SLAStats
	cov     *routing.CoverageMatrix
	policy  RoutingPolicy
	rules   map[string]ruleInfo
	costs   map[string]*providerCost
	version int64
}

// providerCost answers "what does this provider charge me for a message".
type providerCost struct {
	perSegment map[string]int64 // country -> micros per segment
	fallback   int64
	currency   string
}

func (c *providerCost) micros(country string, segments int) int64 {
	if segments < 1 {
		segments = 1
	}
	per, ok := c.perSegment[strings.ToUpper(country)]
	if !ok {
		per = c.fallback
	}
	return per * int64(segments)
}

// NewRouter builds an empty router.
func NewRouter(policy RoutingPolicy) *Router {
	cb := routing.NewCircuitBreaker()
	if policy.BreakerFailures > 0 {
		cb.FailureThreshold = policy.BreakerFailures
	}
	if policy.BreakerOpen > 0 {
		cb.OpenDuration = policy.BreakerOpen.D()
	}
	rt := &Router{cb: cb, sla: routing.NewSLAStats(), cov: routing.NewCoverageMatrix(), policy: policy,
		rules: map[string]ruleInfo{}, costs: map[string]*providerCost{}}
	rt.r = rt.newEngine(rt.cov)
	return rt
}

func (rt *Router) newEngine(cov *routing.CoverageMatrix) *routing.Router {
	r := routing.New()
	r.AllowGlobalRulesForTenants(true)
	r.SetCircuitBreaker(rt.cb)
	r.SetSLAStats(rt.sla)
	r.SetCoverageMatrix(cov)
	r.SetRatingFilter(ratingFunc(rt.rate))
	return r
}

type ratingFunc func(ctx context.Context, req message.SubmitRequest, providerID string, segments int) (routing.RouteRate, error)

func (f ratingFunc) RateRoute(ctx context.Context, req message.SubmitRequest, providerID string, segments int) (routing.RouteRate, error) {
	return f(ctx, req, providerID, segments)
}

// rate prices a route for scoring. SellMicros carries the provider's cost: it
// is the field the smppflow router scores and caps by, and routing on what a
// route costs the platform is what a cost objective means. What the user pays
// is priced separately and does not depend on the provider.
func (rt *Router) rate(_ context.Context, req message.SubmitRequest, providerID string, segments int) (routing.RouteRate, error) {
	rt.mu.RLock()
	c := rt.costs[providerID]
	rt.mu.RUnlock()
	if c == nil {
		return routing.RouteRate{ProviderID: providerID, Billable: true}, nil
	}
	cost := c.micros(req.Meta["country"], segments)
	return routing.RouteRate{ProviderID: providerID, Currency: c.currency, CostMicros: cost, SellMicros: cost, Billable: true}, nil
}

// ProviderView is what the router needs to know about one provider.
type ProviderView struct {
	Name     string
	Config   ProviderConfig
	Owner    string
	Disabled bool
}

// Rebuild replaces the router's providers and rules from the directory
// snapshot. Statistics and circuit state survive.
func (rt *Router) Rebuild(snap Snapshot) error {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	var (
		profiles []provider.Profile
		rules    []routing.Rule
		infos    = map[string]ruleInfo{}
		senders  = map[string][]string{}
		costs    = map[string]*providerCost{}
		saved    = map[string]routing.ProviderStats{}
		states   = map[string]routing.ProviderState{}
	)
	for _, p := range rt.r.Providers() {
		if st, ok := rt.r.ProviderStats(p.ID); ok {
			saved[p.ID] = st
		}
	}

	add := func(rule routing.Rule, info ruleInfo) {
		if rule.Meta == nil {
			rule.Meta = map[string]string{}
		}
		rule.Meta["tier"] = info.tier
		rule.Meta["exclusive"] = strconv.FormatBool(info.exclusive)
		rules = append(rules, rule)
		infos[rule.ID] = info
	}

	cov := routing.NewCoverageMatrix()
	for _, pv := range snap.Providers {
		cfg := pv.Config
		profiles = append(profiles, provider.Profile{
			ID: pv.Name, Name: pv.Name, TPS: max(cfg.TPS, 1), WindowSize: 16,
		})
		if pv.Disabled {
			states[pv.Name] = routing.ProviderSuspended
		}
		pc := &providerCost{perSegment: map[string]int64{}, fallback: FromUnits(cfg.CostPerSegment), currency: cfg.Currency}
		for c, v := range cfg.Cost {
			if strings.EqualFold(c, "default") {
				pc.fallback = FromUnits(v)
				continue
			}
			pc.perSegment[strings.ToUpper(c)] = FromUnits(v)
		}
		costs[pv.Name] = pc
		if len(cfg.SenderCountries) > 0 {
			senders[pv.Name] = cfg.SenderCountries
		}

		caps := cfg.Capabilities
		covCountries := cfg.Countries
		if len(covCountries) == 0 {
			covCountries = []string{""}
		}
		for _, c := range covCountries {
			cov.Upsert(routing.Coverage{
				ProviderID: pv.Name, Country: strings.ToUpper(c), Enabled: true,
				SupportsAlphaSender: caps.alpha(), SupportsNumericSender: caps.numeric(),
				SupportsDLR: caps.dlr(), SupportsUnicode: caps.unicode(), SupportsLongSMS: caps.long(),
			})
		}
	}
	byName := map[string]ProviderView{}
	for _, pv := range snap.Providers {
		byName[pv.Name] = pv
	}

	// Platform rules: a provider serves its configured countries at country
	// priority, or every destination at platform priority when it lists none.
	for _, pv := range snap.Providers {
		if pv.Owner != "" || pv.Disabled || pv.Config.AssignedOnly {
			continue
		}
		cfg := pv.Config
		prio := prioCountry
		if len(cfg.Countries) == 0 && len(cfg.Prefixes) == 0 {
			prio = prioPlatform
		}
		prio += cfg.Priority
		base := routing.Rule{
			ProviderID: pv.Name, MessageTypes: cfg.MessageTypes, Priority: prio, Weight: max(cfg.Weight, 1),
			Objective: objectiveOf(cfg.Objective), Enabled: true,
		}
		info := ruleInfo{tier: tierOf(prio)}
		switch {
		case len(cfg.Prefixes) > 0:
			r := base
			r.ID, r.Prefixes = "platform:"+pv.Name+":prefixes", cfg.Prefixes
			add(r, info)
		case len(cfg.Countries) == 0:
			r := base
			r.ID, r.Prefixes = "platform:"+pv.Name, anyDestination()
			add(r, info)
		default:
			for _, c := range cfg.Countries {
				dial, _ := DialCode(c)
				r := base
				r.ID, r.Country, r.Prefixes = "platform:"+pv.Name+":"+strings.ToUpper(c), strings.ToUpper(c), []string{dial}
				add(r, info)
			}
		}
	}

	// Assignments: a provider made available to a user or a tenant.
	assignments := append([]Assignment(nil), snap.Assignments...)
	for _, pv := range snap.Providers {
		if pv.Owner != "" {
			// A provider the user brought is theirs by definition.
			assignments = append(assignments, Assignment{
				ID: "owner:" + pv.Owner + ":" + pv.Name, UserID: pv.Owner, Provider: pv.Name,
				Priority: 5, Exclusive: pv.Config.Exclusive == nil || *pv.Config.Exclusive,
				Disabled: pv.Disabled,
			})
		}
	}
	for _, a := range assignments {
		if a.Disabled {
			continue
		}
		pv, ok := byName[a.Provider]
		if !ok || pv.Disabled {
			continue
		}
		var tenant, user string
		switch {
		case a.UserID != "":
			u, ok := snap.Users[a.UserID]
			if !ok {
				continue
			}
			tenant, user = u.Tenant, u.ID
		case a.Tenant != "":
			tenant = a.Tenant
		default:
			continue
		}
		countries := a.Countries
		if len(countries) == 0 && len(a.Prefixes) == 0 {
			countries = pv.Config.Countries
		}
		types := a.MessageTypes
		if len(types) == 0 {
			types = pv.Config.MessageTypes
		}
		weight := a.Weight
		if weight <= 0 {
			weight = max(pv.Config.Weight, 1)
		}
		prio := a.Priority
		scoped := len(countries) > 0 || len(a.Prefixes) > 0
		if prio == 0 {
			switch {
			case user != "" && scoped:
				prio = prioUserCountry
			case user != "":
				prio = prioUser
			default:
				prio = prioTenant
			}
		}
		base := routing.Rule{
			TenantID: tenant, UserID: user, ProviderID: a.Provider, MessageTypes: types, Priority: prio,
			Weight: weight, Objective: objectiveOf(pv.Config.Objective), Enabled: true,
		}
		info := ruleInfo{tier: tierOf(prio), exclusive: a.Exclusive}
		// One rule per sender id the assignment is limited to (none: any sender).
		senderList := a.Senders
		if len(senderList) == 0 {
			senderList = []string{""}
		}
		for _, sender := range senderList {
			sfx := ""
			if sender != "" {
				sfx = ":" + sender
			}
			switch {
			case len(a.Prefixes) > 0:
				r := base
				r.ID, r.Prefixes, r.Sender = "assign:"+a.ID+":prefixes"+sfx, a.Prefixes, sender
				add(r, info)
			case len(countries) > 0:
				for _, c := range countries {
					dial, ok := DialCode(c)
					if !ok {
						continue
					}
					r := base
					r.ID, r.Country, r.Prefixes, r.Sender = "assign:"+a.ID+":"+strings.ToUpper(c)+sfx, strings.ToUpper(c), []string{dial}, sender
					add(r, info)
				}
			default:
				r := base
				r.ID, r.Prefixes, r.Sender = "assign:"+a.ID+sfx, anyDestination(), sender
				add(r, info)
			}
		}
	}

	engine := rt.newEngine(cov)
	if err := engine.ApplyConfig(routing.RouterConfig{Providers: profiles, Rules: rules, Version: rt.version + 1}); err != nil {
		return fmt.Errorf("sms: route configuration rejected: %w", err)
	}
	for _, pv := range snap.Providers {
		st, ok := saved[pv.Name]
		if !ok {
			st = routing.ProviderStats{
				ProviderID: pv.Name, QualityScore: orDefault(pv.Config.Quality, 80),
				DeliveryRate: orDefault(pv.Config.DeliveryRate, 0.98), MaxTPS: max(pv.Config.TPS, 1),
			}
		}
		if c := costs[pv.Name]; c != nil {
			st.CostPerSegment = ToUnits(c.fallback)
		}
		engine.UpdateProviderStats(st)
		if state, ok := states[pv.Name]; ok {
			engine.SetProviderState(pv.Name, state)
		}
	}
	rt.r, rt.rules, rt.costs, rt.cov, rt.senders = engine, infos, costs, cov, senders
	rt.version++
	return nil
}

func orDefault(v, d float64) float64 {
	if v == 0 {
		return d
	}
	return v
}

func anyDestination() []string { return []string{"1", "2", "3", "4", "5", "6", "7", "8", "9"} }

func objectiveOf(s string) routing.RoutingObjective {
	switch s {
	case ObjectiveCost:
		return routing.ObjectiveLowestCost
	case ObjectiveDelivery:
		return routing.ObjectiveHighestDelivery
	case ObjectiveLatency:
		return routing.ObjectiveLowestLatency
	default:
		return routing.ObjectiveBalanced
	}
}

// Request is what routing decides on: who sends, from where, to whom, how
// urgently and how large.
type Request struct {
	UserID  string
	Tenant  string
	From    string
	To      string
	Country string
	// SenderCountry is where a numeric sender id is registered, when known.
	SenderCountry string
	Type          string
	Segments      int
	Unicode       bool
	WantDLR       bool
	// Objective overrides the policy for this request (a user's preference).
	Objective string
	// Exclude removes providers from consideration (ones already tried).
	Exclude []string
}

// Candidate is one provider the planner considered.
type Candidate struct {
	Provider   string   `json:"provider"`
	Rule       string   `json:"rule"`
	Tier       string   `json:"tier"`
	Priority   int      `json:"priority"`
	Eligible   bool     `json:"eligible"`
	Score      float64  `json:"score,omitempty"`
	Quality    float64  `json:"quality,omitempty"`
	Delivery   float64  `json:"delivery_rate,omitempty"`
	CostMicros int64    `json:"cost_micros,omitempty"`
	Rejected   []string `json:"rejected_by,omitempty"`
}

// Explanation is a plan together with the candidates that did not make it.
type Explanation struct {
	Objective  string      `json:"objective"`
	Plan       []PlanEntry `json:"plan"`
	Candidates []Candidate `json:"candidates"`
}

// ErrNoRoute means no provider can carry the message.
type ErrNoRoute struct {
	Reasons []string
	// Transient is true when providers exist for the destination but are
	// unavailable right now (down, cooling off, circuit open): trying again
	// later can succeed.
	Transient bool
}

func (e *ErrNoRoute) Error() string {
	if len(e.Reasons) == 0 {
		return "no provider can deliver to this destination"
	}
	return "no provider can deliver to this destination: " + strings.Join(e.Reasons, "; ")
}

func (rt *Router) objectiveFor(req Request) string {
	if req.Objective != "" {
		return req.Objective
	}
	if o, ok := rt.policy.Objectives[req.Type]; ok {
		return o
	}
	if rt.policy.Default != "" {
		return rt.policy.Default
	}
	return ObjectiveBalanced
}

// Plan returns the ordered chain of providers for a request.
func (rt *Router) Plan(ctx context.Context, req Request) (Explanation, error) {
	rt.mu.RLock()
	engine, rules, costs, senders := rt.r, rt.rules, rt.costs, rt.senders
	rt.mu.RUnlock()

	segs := max(req.Segments, 1)
	meta := map[string]string{
		"user_id": req.UserID, "country": req.Country, "message_type": req.Type,
		"segments": strconv.Itoa(segs),
	}
	if req.Unicode {
		meta["unicode"] = "true"
	}
	sr := message.SubmitRequest{TenantID: req.Tenant, From: req.From, To: req.To, Text: "", Meta: meta}
	if req.WantDLR {
		sr.RegisteredDelivery = 1
	}
	plan, err := engine.Plan(ctx, sr, routing.PlanOptions{Mode: routing.RouteInitial, DryRun: true})
	objective := rt.objectiveFor(req)
	ex := Explanation{Objective: objective}
	exclude := map[string]bool{}
	for _, e := range req.Exclude {
		exclude[e] = true
	}

	var eligible []Candidate
	var reasons []string
	transient := false
	seen := map[string]bool{}
	for _, c := range plan.Candidates {
		info := rules[c.RuleID]
		cand := Candidate{Provider: c.ProviderID, Rule: c.RuleID, Tier: info.tier, Priority: c.Priority, Eligible: c.Eligible}
		if cc := costs[c.ProviderID]; cc != nil {
			cand.CostMicros = cc.micros(req.Country, segs)
		}
		cand.Score, cand.Quality, cand.Delivery = c.Score.Total, c.Score.Quality, c.Score.Delivery/100
		if !c.Eligible {
			for _, r := range c.RejectedBy {
				cand.Rejected = append(cand.Rejected, string(r))
				switch r {
				case routing.RejectCircuitOpen, routing.RejectProviderDown, routing.RejectMaintenance,
					routing.RejectCooldown, routing.RejectTPSExceeded, routing.RejectWindowFull,
					routing.RejectDraining, routing.RejectNoBind:
					transient = true
				}
			}
			reasons = append(reasons, c.ProviderID+": "+strings.Join(cand.Rejected, ","))
			ex.Candidates = append(ex.Candidates, cand)
			continue
		}
		if exclude[c.ProviderID] {
			cand.Eligible, cand.Rejected = false, []string{"already_tried"}
			ex.Candidates = append(ex.Candidates, cand)
			continue
		}
		if allowed := senders[c.ProviderID]; len(allowed) > 0 && req.SenderCountry != "" && !containsFold(allowed, req.SenderCountry) {
			cand.Eligible, cand.Rejected = false, []string{"sender_country_unsupported"}
			reasons = append(reasons, c.ProviderID+": cannot send from a "+req.SenderCountry+" number")
			ex.Candidates = append(ex.Candidates, cand)
			continue
		}
		if floor := rt.policy.QualityFloor[req.Type]; floor > 0 && cand.Quality < floor {
			cand.Eligible, cand.Rejected = false, []string{"below_quality_floor"}
			reasons = append(reasons, c.ProviderID+": quality "+strconv.FormatFloat(cand.Quality, 'f', 0, 64)+" below the "+req.Type+" floor")
			ex.Candidates = append(ex.Candidates, cand)
			continue
		}
		if floor := rt.policy.DeliveryFloor[req.Type]; floor > 0 && cand.Delivery < floor {
			cand.Eligible, cand.Rejected = false, []string{"below_delivery_floor"}
			ex.Candidates = append(ex.Candidates, cand)
			continue
		}
		if seen[c.ProviderID+"|"+c.RuleID] {
			continue
		}
		seen[c.ProviderID+"|"+c.RuleID] = true
		eligible = append(eligible, cand)
	}

	sort.SliceStable(eligible, func(i, j int) bool { return less(eligible[i], eligible[j], objective) })

	// An exclusive assignment ends the chain at its tier: the account asked for
	// nothing but its own provider.
	cut := len(eligible)
	for i, c := range eligible {
		if rules[c.Rule].exclusive {
			cut = i + 1
			for cut < len(eligible) && eligible[cut].Priority == c.Priority {
				cut++
			}
			break
		}
	}
	chain := map[string]bool{}
	for i, c := range eligible {
		if i >= cut {
			c.Eligible, c.Rejected = false, []string{"exclusive_assignment"}
			ex.Candidates = append(ex.Candidates, c)
			continue
		}
		ex.Candidates = append(ex.Candidates, c)
		if chain[c.Provider] {
			continue
		}
		chain[c.Provider] = true
		ex.Plan = append(ex.Plan, PlanEntry{Provider: c.Provider, Rule: c.Rule, Tier: c.Tier, Priority: c.Priority, Score: c.Score, CostMicros: c.CostMicros})
	}
	if len(ex.Plan) == 0 {
		if err != nil && len(reasons) == 0 {
			reasons = append(reasons, err.Error())
		}
		return ex, &ErrNoRoute{Reasons: reasons, Transient: transient}
	}
	return ex, nil
}

// less orders candidates: lower priority tier first, then by objective.
func less(a, b Candidate, objective string) bool {
	if a.Priority != b.Priority {
		return a.Priority < b.Priority
	}
	switch objective {
	case ObjectiveCost:
		if a.CostMicros != b.CostMicros {
			return a.CostMicros < b.CostMicros
		}
		if a.Quality != b.Quality {
			return a.Quality > b.Quality
		}
	case ObjectiveDelivery:
		if a.Delivery != b.Delivery {
			return a.Delivery > b.Delivery
		}
		if a.Quality != b.Quality {
			return a.Quality > b.Quality
		}
	}
	if a.Score != b.Score {
		return a.Score > b.Score
	}
	return a.Provider < b.Provider
}

// Feedback tells the router how an attempt went, so its circuit breaker and
// statistics steer later routes.
func (rt *Router) Feedback(providerID string, req Request, ok bool, latency time.Duration) {
	rt.mu.RLock()
	engine := rt.r
	rt.mu.RUnlock()
	sr := message.SubmitRequest{Meta: map[string]string{"country": req.Country, "message_type": req.Type}}
	if ok {
		engine.MarkRouteSuccess(sr, providerID, latency)
	} else {
		engine.MarkRouteFailure(sr, providerID, latency)
	}
}

// Allow reports whether the circuit for provider/country/type is closed.
func (rt *Router) Allow(providerID, country, msgType string) bool {
	return rt.cb.Allow(circuitKey(providerID, country, msgType))
}

func circuitKey(pid, country, msgType string) string {
	return pid + "|" + strings.ToUpper(country) + "||" + strings.ToLower(msgType)
}

// SetState sets a provider's operational state: active, down, maintenance,
// draining or suspended.
func (rt *Router) SetState(providerID string, state routing.ProviderState) {
	rt.mu.RLock()
	engine := rt.r
	rt.mu.RUnlock()
	engine.SetProviderState(providerID, state)
}

// Circuits returns the circuit breaker's state.
func (rt *Router) Circuits() []routing.CircuitSnapshot { return rt.cb.Snapshot() }

// Stats returns a provider's routing statistics.
func (rt *Router) Stats(providerID string) (routing.ProviderStats, bool) {
	rt.mu.RLock()
	engine := rt.r
	rt.mu.RUnlock()
	return engine.ProviderStats(providerID)
}
