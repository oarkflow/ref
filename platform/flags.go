package platform

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"slices"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// Feature flags: declared once, evaluated everywhere.
//
//	flag "new_checkout" {
//	  description "The redesigned checkout"
//	  default false
//	  rule "staff" { roles ["staff"]  value true }
//	  rule "pilot" { tenants ["acme"]  rollout 25  value true }   # 25% of acme's users, sticky
//	}
//	flag "pricing_page" {
//	  default "control"
//	  variant "control" { weight 50 }
//	  variant "annual_first" { weight 50 }
//	}
//
// A flag's value is available to every expression as flags.<name>, to
// intents through flag.evaluate / flag.all, and gates a route with
// `flag "new_checkout"` (the route answers 404 while the flag is off).
// Operators override flags at run time with flag.set; overrides are shared
// through a cache resource so every replica sees them.

// FlagSpec declares a flag.
type FlagSpec struct {
	Name        string `bcl:",id"`
	Description string `bcl:"description"`
	// Default is the value when no rule matches (default false).
	Default any `bcl:"default"`
	// Disabled forces the default for everyone (a kill switch in BCL).
	Disabled bool `bcl:"disabled"`
	// Rules are tried in order; the first that matches decides.
	Rules []FlagRule `bcl:"rule,block"`
	// Variants split callers by weight when no rule matched (A/B tests).
	Variants []FlagVariant `bcl:"variant,block"`
}

// FlagRule targets a slice of callers. Every criterion set must match.
type FlagRule struct {
	Name         string   `bcl:",id"`
	Roles        []string `bcl:"roles"`
	Users        []string `bcl:"users"`
	Tenants      []string `bcl:"tenants"`
	Environments []string `bcl:"environments"`
	Condition    string   `bcl:"condition"`
	// Rollout admits this percentage (0-100) of matching callers, stably by
	// user (or tenant when anonymous).
	Rollout *float64 `bcl:"rollout"`
	Value   any      `bcl:"value"`
	// Variant picks one of the flag's variants instead of Value.
	Variant string `bcl:"variant"`
}

// FlagVariant is one arm of an experiment.
type FlagVariant struct {
	Name   string  `bcl:",id"`
	Weight float64 `bcl:"weight"`
	// Value defaults to the variant name.
	Value any `bcl:"value"`
}

// FlagOverride is a run-time override set by an operator.
type FlagOverride struct {
	// Value forces the flag's value for everyone.
	Value any `json:"value,omitempty"`
	// Off forces the default (kill switch).
	Off       bool      `json:"off,omitempty"`
	SetBy     string    `json:"set_by,omitempty"`
	SetAt     time.Time `json:"set_at"`
	Reason    string    `json:"reason,omitempty"`
	HasValue  bool      `json:"has_value,omitempty"`
	ExpiresAt time.Time `json:"expires_at,omitzero"`
}

type compiledFlag struct {
	spec  FlagSpec
	rules []compiledFlagRule
}

type compiledFlagRule struct {
	FlagRule
	cond *Expression
}

// flagRegistry evaluates flags; overrides live in an atomic snapshot that
// flag.set updates locally and a refresh loop reloads from the cache.
type flagRegistry struct {
	flags       map[string]*compiledFlag
	order       []string
	environment string
	cache       Cache
	overrides   atomic.Pointer[map[string]FlagOverride]
	mu          sync.Mutex // serialises writes
	stop        chan struct{}
	done        chan struct{}
}

const flagOverridesKey = "ref:flags:overrides"

func compileFlags(doc Document, resources map[string]Resource) (*flagRegistry, error) {
	r := &flagRegistry{flags: map[string]*compiledFlag{}, environment: doc.Environment}
	empty := map[string]FlagOverride{}
	r.overrides.Store(&empty)
	for _, spec := range doc.Flags {
		if !identRe.MatchString(spec.Name) {
			return nil, fmt.Errorf("ref/platform: flag %q: the name must be lower_snake_case", spec.Name)
		}
		if r.flags[spec.Name] != nil {
			return nil, fmt.Errorf("ref/platform: flag %q declared twice", spec.Name)
		}
		cf := &compiledFlag{spec: spec}
		if cf.spec.Default == nil {
			cf.spec.Default = false
		}
		variants := map[string]bool{}
		total := 0.0
		for _, v := range spec.Variants {
			if v.Weight < 0 {
				return nil, fmt.Errorf("ref/platform: flag %q variant %q: negative weight", spec.Name, v.Name)
			}
			variants[v.Name] = true
			total += v.Weight
		}
		if len(spec.Variants) > 0 && total <= 0 {
			return nil, fmt.Errorf("ref/platform: flag %q: variants need positive weights", spec.Name)
		}
		for _, rule := range spec.Rules {
			if rule.Rollout != nil && (*rule.Rollout < 0 || *rule.Rollout > 100) {
				return nil, fmt.Errorf("ref/platform: flag %q rule %q: rollout must be 0-100", spec.Name, rule.Name)
			}
			if rule.Variant != "" && !variants[rule.Variant] {
				return nil, fmt.Errorf("ref/platform: flag %q rule %q: unknown variant %q", spec.Name, rule.Name, rule.Variant)
			}
			expr, err := CompileExpr(rule.Condition)
			if err != nil {
				return nil, fmt.Errorf("ref/platform: flag %q rule %q: %w", spec.Name, rule.Name, err)
			}
			cf.rules = append(cf.rules, compiledFlagRule{FlagRule: rule, cond: expr})
		}
		r.flags[spec.Name] = cf
		r.order = append(r.order, spec.Name)
	}
	for _, route := range doc.Routes {
		if route.Flag != "" && r.flags[route.Flag] == nil {
			return nil, fmt.Errorf("ref/platform: route %q is gated by unknown flag %q", route.Name, route.Flag)
		}
	}
	if doc.FlagStore != "" {
		res, ok := resources[doc.FlagStore]
		if !ok {
			return nil, fmt.Errorf("ref/platform: flag_store %q is not a declared resource", doc.FlagStore)
		}
		cache, ok := asCache(res)
		if !ok {
			return nil, fmt.Errorf("ref/platform: flag_store %q is not a cache resource", doc.FlagStore)
		}
		r.cache = cache
		r.reload()
		r.stop, r.done = make(chan struct{}), make(chan struct{})
		go r.refreshLoop(5 * time.Second)
	}
	return r, nil
}

func (r *flagRegistry) refreshLoop(every time.Duration) {
	defer close(r.done)
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-r.stop:
			return
		case <-t.C:
			r.reload()
		}
	}
}

func (r *flagRegistry) Close() error {
	if r.stop != nil {
		close(r.stop)
		<-r.done
	}
	return nil
}

func (r *flagRegistry) reload() {
	if r.cache == nil {
		return
	}
	raw, ok, err := r.cache.Get(flagOverridesKey)
	if err != nil {
		return // keep serving the last good snapshot
	}
	next := map[string]FlagOverride{}
	if ok {
		if err := json.Unmarshal(raw, &next); err != nil {
			return
		}
	}
	r.overrides.Store(&next)
}

// setOverride writes (or with nil clears) a flag's override.
func (r *flagRegistry) setOverride(name string, o *FlagOverride) error {
	if r.flags[name] == nil {
		return notFound("flag", name)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reload()
	next := map[string]FlagOverride{}
	for k, v := range *r.overrides.Load() {
		next[k] = v
	}
	if o == nil {
		delete(next, name)
	} else {
		next[name] = *o
	}
	if r.cache != nil {
		raw, err := json.Marshal(next)
		if err != nil {
			return err
		}
		if err := r.cache.Set(flagOverridesKey, raw, 0); err != nil {
			return unavailable("the flag store is not available: %v", err)
		}
	}
	r.overrides.Store(&next)
	return nil
}

// flagSubject is who a flag is evaluated for.
type flagSubject struct {
	principal Principal
	tenant    string
	env       Env
}

// FlagResult is a flag's value for a caller and why.
type FlagResult struct {
	Value   any    `json:"value"`
	Reason  string `json:"reason"`
	Rule    string `json:"rule,omitempty"`
	Variant string `json:"variant,omitempty"`
}

func (r *flagRegistry) evaluate(name string, s flagSubject, now time.Time) (FlagResult, bool) {
	cf := r.flags[name]
	if cf == nil {
		return FlagResult{}, false
	}
	if o, ok := (*r.overrides.Load())[name]; ok && (o.ExpiresAt.IsZero() || now.Before(o.ExpiresAt)) {
		if o.Off {
			return FlagResult{Value: cf.spec.Default, Reason: "override_off"}, true
		}
		if o.HasValue {
			return FlagResult{Value: o.Value, Reason: "override"}, true
		}
	}
	if cf.spec.Disabled {
		return FlagResult{Value: cf.spec.Default, Reason: "disabled"}, true
	}
	key := s.principal.ID
	if key == "" {
		key = s.tenant
	}
	for _, rule := range cf.rules {
		if len(rule.Roles) > 0 && !slices.ContainsFunc(rule.Roles, s.principal.HasRole) {
			continue
		}
		if len(rule.Users) > 0 && !slices.Contains(rule.Users, s.principal.ID) {
			continue
		}
		if len(rule.Tenants) > 0 && !slices.Contains(rule.Tenants, s.tenant) {
			continue
		}
		if len(rule.Environments) > 0 && !slices.Contains(rule.Environments, r.environment) {
			continue
		}
		if rule.cond != nil {
			ok, err := rule.cond.Bool(s.env)
			if err != nil || !ok {
				continue
			}
		}
		if rule.Rollout != nil && bucket(name+":"+rule.Name, key) >= *rule.Rollout {
			continue
		}
		if rule.Variant != "" {
			return FlagResult{Value: variantValue(cf, rule.Variant), Reason: "rule", Rule: rule.Name, Variant: rule.Variant}, true
		}
		v := rule.Value
		if v == nil {
			v = true
		}
		return FlagResult{Value: v, Reason: "rule", Rule: rule.Name}, true
	}
	if len(cf.spec.Variants) > 0 {
		total := 0.0
		for _, v := range cf.spec.Variants {
			total += v.Weight
		}
		point := bucket(name+":variants", key) / 100 * total
		acc := 0.0
		for _, v := range cf.spec.Variants {
			acc += v.Weight
			if point < acc {
				return FlagResult{Value: variantValue(cf, v.Name), Reason: "variant", Variant: v.Name}, true
			}
		}
	}
	return FlagResult{Value: cf.spec.Default, Reason: "default"}, true
}

func variantValue(cf *compiledFlag, name string) any {
	for _, v := range cf.spec.Variants {
		if v.Name == name {
			if v.Value != nil {
				return v.Value
			}
			return v.Name
		}
	}
	return name
}

// bucket maps (salt, key) to a stable number in [0, 100).
func bucket(salt, key string) float64 {
	h := fnv.New64a()
	h.Write([]byte(salt))
	h.Write([]byte{0})
	h.Write([]byte(key))
	return float64(h.Sum64()%10000) / 100
}

// all evaluates every flag for a subject.
func (r *flagRegistry) all(s flagSubject, now time.Time) map[string]any {
	out := make(map[string]any, len(r.order))
	for _, name := range r.order {
		res, _ := r.evaluate(name, s, now)
		out[name] = res.Value
	}
	return out
}

// flagsFor evaluates every flag for an action's caller (nil when the
// application declares none).
func flagsFor(ctx *ActionContext, env Env) map[string]any {
	if ctx.Platform == nil || ctx.Platform.flags == nil || len(ctx.Platform.flags.order) == 0 {
		return nil
	}
	return ctx.Platform.flags.all(flagSubject{principal: ctx.Principal, tenant: ctx.TenantID, env: env}, time.Now())
}

// ---------------------------------------------------------------------------
// Actions
// ---------------------------------------------------------------------------

func registerFlagActions(r *Registry) {
	mustAction(r, "flag.evaluate", ActionFactoryFunc(buildFlagEvaluate), ActionInfo{
		Family: "decision", Kind: "read",
		Summary:  "Evaluate one feature flag for the caller; with require true, fail with 404 while it is off",
		Provides: "The flag's value (or {value, reason, rule, variant} with explain true)",
		Config: []ConfigField{
			{Name: "flag", Type: "string", Required: true},
			{Name: "require", Type: "bool", Default: "false"},
			{Name: "explain", Type: "bool", Default: "false"},
		},
	})
	mustAction(r, "flag.all", ActionFactoryFunc(buildFlagAll), ActionInfo{
		Family: "decision", Kind: "read",
		Summary:  "Every feature flag's value for the caller (for a client to configure itself)",
		Provides: "A map of flag name to value",
	})
	mustAction(r, "flag.set", ActionFactoryFunc(buildFlagSet), ActionInfo{
		Family: "decision", Kind: "effect",
		Summary:  "Override a flag at run time for everyone ({flag, value} | {flag, off: true} | {flag, clear: true}, optional ttl and reason)",
		Provides: "The flag's definition and current override",
		Config:   []ConfigField{{Name: "roles", Type: "[]string", Required: true, Summary: "Who may override flags"}},
	})
}

func flagRegistryOf(build BuildContext) *flagRegistry {
	if build.Platform == nil {
		return nil
	}
	return build.Platform.flags
}

func buildFlagEvaluate(build BuildContext, spec NodeSpec) (Action, error) {
	name := configString(spec.Config, "flag", "")
	if build.Document == nil || !slices.ContainsFunc(build.Document.Flags, func(f FlagSpec) bool { return f.Name == name }) {
		return nil, fmt.Errorf("node %q: unknown flag %q", spec.Name, name)
	}
	require := configBool(spec.Config, "require", false)
	explain := configBool(spec.Config, "explain", false)
	return ActionFunc(func(ctx *ActionContext) (ActionResult, error) {
		reg := ctx.Platform.flags
		res, _ := reg.evaluate(name, flagSubject{principal: ctx.Principal, tenant: ctx.TenantID, env: actionEnv(ctx)}, time.Now())
		if require && !Truthy(res.Value) {
			return ActionResult{}, notFoundOrMessage("not found")
		}
		if explain {
			return acknowledgement(spec, map[string]any{"value": res.Value, "reason": res.Reason, "rule": res.Rule, "variant": res.Variant}), nil
		}
		return acknowledgement(spec, res.Value), nil
	}), nil
}

func buildFlagAll(build BuildContext, spec NodeSpec) (Action, error) {
	return ActionFunc(func(ctx *ActionContext) (ActionResult, error) {
		all := flagsFor(ctx, actionEnv(ctx))
		if all == nil {
			all = map[string]any{}
		}
		return acknowledgement(spec, all), nil
	}), nil
}

func buildFlagSet(build BuildContext, spec NodeSpec) (Action, error) {
	roles := configStrings(spec.Config, "roles")
	if len(roles) == 0 {
		return nil, fmt.Errorf("node %q: flag.set needs config.roles", spec.Name)
	}
	return ActionFunc(func(ctx *ActionContext) (ActionResult, error) {
		if !slices.ContainsFunc(roles, ctx.Principal.HasRole) {
			return ActionResult{}, permissionDenied("you may not change feature flags")
		}
		body, _ := ctx.Inputs["input"].(map[string]any)
		name := Stringify(body["flag"])
		reg := ctx.Platform.flags
		if reg.flags[name] == nil {
			return ActionResult{}, notFound("flag", name)
		}
		var o *FlagOverride
		if clear, _ := body["clear"].(bool); !clear {
			o = &FlagOverride{SetBy: ctx.Principal.ID, SetAt: time.Now().UTC(), Reason: Stringify(body["reason"])}
			if o.Reason == "<nil>" {
				o.Reason = ""
			}
			if off, _ := body["off"].(bool); off {
				o.Off = true
			} else if v, ok := body["value"]; ok {
				o.Value, o.HasValue = v, true
			} else {
				return ActionResult{}, invalidInput("send value, off: true or clear: true")
			}
			if ttl := Stringify(body["ttl"]); ttl != "" && ttl != "<nil>" {
				d, err := time.ParseDuration(ttl)
				if err != nil || d <= 0 {
					return ActionResult{}, invalidInput("ttl must be a duration like 2h")
				}
				o.ExpiresAt = o.SetAt.Add(d)
			}
		}
		if err := reg.setOverride(name, o); err != nil {
			return ActionResult{}, err
		}
		return acknowledgement(spec, reg.describe(name)), nil
	}), nil
}

// describe renders a flag's definition and override (for admin screens).
func (r *flagRegistry) describe(name string) map[string]any {
	cf := r.flags[name]
	out := map[string]any{"name": name, "description": cf.spec.Description, "default": cf.spec.Default, "disabled": cf.spec.Disabled}
	var rules []string
	for _, rule := range cf.rules {
		rules = append(rules, rule.Name)
	}
	sort.Strings(rules)
	out["rules"] = rules
	if o, ok := (*r.overrides.Load())[name]; ok {
		out["override"] = o
	}
	return out
}

func asCache(res Resource) (Cache, bool) {
	c, ok := res.(Cache)
	return c, ok
}
