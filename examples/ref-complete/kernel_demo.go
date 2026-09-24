package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/oarkflow/ref/ai"
	"github.com/oarkflow/ref/capability"
	"github.com/oarkflow/ref/domain"
	"github.com/oarkflow/ref/effect"
	"github.com/oarkflow/ref/execution"
	"github.com/oarkflow/ref/fact"
	"github.com/oarkflow/ref/intent"
	"github.com/oarkflow/ref/invocation"
	"github.com/oarkflow/ref/runtime"
	"github.com/oarkflow/ref/source"
	"github.com/oarkflow/ref/transport/stream"
)

type demoInput struct {
	Query string `json:"query"`
}

type demoProfile struct {
	Label string `json:"label"`
}

type demoOutput struct {
	Query   string `json:"query"`
	Label   string `json:"label"`
	Tenant  string `json:"tenant"`
	Version string `json:"version"`
}

var (
	demoInputKey   = fact.NewKey[demoInput]("demo.input")
	demoProfileKey = fact.NewKey[demoProfile]("demo.profile")
)

type demoState struct {
	fetched   atomic.Int64
	local     atomic.Int64
	delivered chan string
}

type demoLocalEffect struct {
	state *demoState
}

func (e *demoLocalEffect) Name() string            { return "demo.local" }
func (e *demoLocalEffect) Kind() effect.EffectKind { return effect.LocalTransactional }
func (e *demoLocalEffect) Commit(context.Context) error {
	e.state.local.Add(1)
	return nil
}

type demoNotifyEffect struct {
	Query     string
	Delivered chan<- string
}

func (e *demoNotifyEffect) Name() string            { return "demo.notify" }
func (e *demoNotifyEffect) Kind() effect.EffectKind { return effect.DurableDelivery }
func (e *demoNotifyEffect) Commit(context.Context) error {
	if e.Delivered != nil {
		e.Delivered <- e.Query
	}
	return nil
}

func (e *demoNotifyEffect) EncodeEffect() ([]byte, string, bool, error) {
	payload, err := json.Marshal(struct {
		Query string `json:"query"`
	}{Query: e.Query})
	return payload, "demo-notify:" + e.Query, true, err
}

func runKernelDemo(ctx context.Context) error {
	state := &demoState{delivered: make(chan string, 4)}
	effectStore := effect.NewMemoryEffectStore()
	breaker := source.NewCircuitBreaker(source.CircuitConfig{FailureThreshold: 5, Cooldown: 50 * time.Millisecond})
	cache := source.NewProcessCache(source.ProcessCacheConfig{MaxEntries: 256})
	coalescer := source.NewCoalescer()

	auth := capability.NewAuthCapability("demo.auth", func(hint invocation.PrincipalHint) (capability.PrincipalFact, error) {
		if hint.BearerToken != "demo-token" {
			return capability.PrincipalFact{}, errors.New("invalid demo token")
		}
		return capability.PrincipalFact{ID: "user-demo", Username: "Demo Operator", Roles: []string{"operator"}}, nil
	})
	tenant := capability.NewTenantCapability("demo.tenant", func(_ *invocation.Invocation, principal *capability.PrincipalFact) (capability.TenantFact, error) {
		if principal == nil || principal.ID != "user-demo" {
			return capability.TenantFact{}, errors.New("demo principal is missing")
		}
		return capability.TenantFact{ID: "tenant-demo", Name: "Demo Tenant", Tier: "pro"}, nil
	}, true)
	profile := source.NewFetchCapability("demo.profile-source", source.Spec{
		Name:        "demo-profile",
		Kind:        source.KindAPI,
		ReadOnly:    true,
		Cacheable:   true,
		Coalescible: true,
		Consistency: source.Eventual,
		CacheScope:  source.ScopeProcess,
		CacheTTL:    time.Minute,
		Security:    source.SecurityTenant,
		CostWeight:  1,
	}, demoProfileKey.Any(), func(nc *execution.NodeContext) (any, error) {
		input, err := execution.Require[demoInput](nc, demoInputKey)
		if err != nil {
			return nil, err
		}
		state.fetched.Add(1)
		return demoProfile{Label: "profile-for-" + input.Query}, nil
	}, source.WithCache(cache), source.WithCoalescer(coalescer), source.WithCircuitBreaker(breaker), source.WithKeyFunc(func(nc *execution.NodeContext) string {
		input, _ := execution.Require[demoInput](nc, demoInputKey)
		return input.Query
	}))
	profile.Requires = []fact.AnyKey{demoInputKey.Any(), capability.PrincipalKey.Any(), capability.TenantKey.Any()}

	pack, err := domain.NewPack("demo-domain", "1")
	if err != nil {
		return err
	}
	if err := pack.Add(auth, tenant, sourceAsCapability(profile)); err != nil {
		return err
	}
	engine := runtime.NewEngine(
		runtime.WithEffectStore(effectStore),
		runtime.WithEffectResolver("demo.notify", func(record effect.EffectRecord) (effect.Effect, error) {
			var payload struct {
				Query string `json:"query"`
			}
			if err := json.Unmarshal(record.Payload, &payload); err != nil {
				return nil, err
			}
			return &demoNotifyEffect{Query: payload.Query, Delivered: state.delivered}, nil
		}),
	)
	defer engine.Close()
	if err := pack.Register(engine.Capabilities()); err != nil {
		return err
	}
	definition := demoDefinition(state)
	if err := engine.RegisterDefinition(definition); err != nil {
		return err
	}
	if err := engine.Compile(); err != nil {
		return err
	}

	input, err := json.Marshal(demoInput{Query: "ref"})
	if err != nil {
		return err
	}
	invocationValue := &invocation.Invocation{
		ID:        "kernel-demo",
		Intent:    "demo.lookup",
		Input:     invocation.NewInput(input, "application/json"),
		Principal: invocation.PrincipalHint{BearerToken: "demo-token"},
		Transport: invocation.Transport{Protocol: "demo"},
	}
	result, trace, err := engine.DispatchTraced(ctx, invocationValue)
	if err != nil {
		return err
	}
	output, ok := result.Value.(demoOutput)
	if !ok {
		runtime.ReleaseDispatchResult(result)
		return fmt.Errorf("kernel demo returned %T", result.Value)
	}
	runtime.ReleaseDispatchResult(result)
	select {
	case delivered := <-state.delivered:
		fmt.Printf("kernel: durable effect delivered query=%s local_commits=%d\n", delivered, state.local.Load())
	case <-time.After(2 * time.Second):
		return errors.New("kernel demo durable effect timed out")
	}

	plan, _ := engine.Plan("demo.lookup")
	fmt.Printf("kernel: result=%s tenant=%s fetched=%d cache_hits=%d\n", output.Query, output.Tenant, state.fetched.Load(), cache.Stats().Hits)
	fmt.Printf("kernel: simulation stages=%d width=%d critical_path=%d\n", plan.Simulation().Stages, plan.Simulation().MaxParallelism, plan.Simulation().CriticalPath)
	if digest, err := trace.Digest(); err == nil {
		fmt.Printf("kernel: provenance_digest=%s facts=%d decisions=%d\n", digest, len(trace.Snapshot().Facts), len(trace.Snapshot().Decisions))
	}
	if replay, replayTrace, err := engine.Replay(ctx, invocationValue, trace); err != nil {
		return fmt.Errorf("replay: %w", err)
	} else {
		fmt.Printf("kernel: replay matched nodes=%d effects=%d\n", len(replayTrace.Snapshot().Nodes), len(replayTrace.Snapshot().Effects))
		runtime.ReleaseDispatchResult(replay)
	}

	secondInput := &invocation.Invocation{ID: "kernel-demo-2", Intent: invocationValue.Intent, Input: invocationValue.Input, Principal: invocationValue.Principal}
	second, err := engine.Dispatch(ctx, secondInput)
	if err != nil {
		return err
	}
	runtime.ReleaseDispatchResult(second)
	select {
	case <-state.delivered:
	case <-time.After(2 * time.Second):
		return errors.New("second durable effect timed out")
	}
	fmt.Printf("kernel: second request served from scoped cache=%t\n", cache.Stats().Hits > 0)

	cache.InvalidateSource("demo-profile")
	beforeConcurrent := state.fetched.Load()
	var concurrent sync.WaitGroup
	for i := 0; i < 2; i++ {
		concurrent.Add(1)
		go func(index int) {
			defer concurrent.Done()
			value := &invocation.Invocation{ID: invocation.ID(fmt.Sprintf("kernel-concurrent-%d", index)), Intent: invocationValue.Intent, Input: invocationValue.Input, Principal: invocationValue.Principal}
			result, err := engine.Dispatch(ctx, value)
			if err == nil {
				runtime.ReleaseDispatchResult(result)
			}
		}(i)
	}
	concurrent.Wait()
	fmt.Printf("kernel: coalesced concurrent fetches=%d\n", state.fetched.Load()-beforeConcurrent)
	for i := 0; i < 2; i++ {
		select {
		case <-state.delivered:
		case <-time.After(2 * time.Second):
			return errors.New("concurrent durable effects timed out")
		}
	}

	probe := source.NewCircuitBreaker(source.CircuitConfig{FailureThreshold: 2, Cooldown: 5 * time.Millisecond})
	for i := 0; i < 2; i++ {
		_, _ = probe.Execute(ctx, func(context.Context) (any, error) { return nil, errors.New("dependency unavailable") })
	}
	fmt.Printf("kernel: adaptive circuit=%v\n", probe.State())
	time.Sleep(6 * time.Millisecond)
	_, _ = probe.Execute(ctx, func(context.Context) (any, error) { return "recovered", nil })
	fmt.Printf("kernel: circuit_after_probe=%v\n", probe.State())

	prompt, err := ai.BuildGroundedPrompt("Answer only from the supplied documents", []ai.Document{
		{ID: "doc-1", Title: "Runtime", Text: "REF compiles typed facts into a dependency graph."},
		{ID: "doc-2", Title: "Durability", Text: "Process rows and effect journals survive retries."},
	}, ai.GroundingPolicy{RequireCitations: true})
	if err != nil {
		return err
	}
	answer, err := prompt.ValidateAnswer("REF compiles facts [doc-1] and persists work [doc-2].")
	if err != nil {
		return err
	}
	fmt.Printf("kernel: grounded citations=%v\n", answer.Citations)

	events := make(chan stream.Event, 2)
	events <- stream.Event{Event: "progress", Data: map[string]string{"stage": "compiled"}}
	events <- stream.Event{Event: "result", Data: map[string]string{"status": "ok"}}
	close(events)
	recorder := httptest.NewRecorder()
	if err := stream.Write(ctx, recorder, events, stream.Options{Format: stream.SSE}); err != nil {
		return err
	}
	fmt.Printf("kernel: stream=%q\n", strings.TrimSpace(recorder.Body.String()))
	return nil
}

func demoDefinition(state *demoState) *intent.Definition {
	return &intent.Definition{
		Name:    "demo.lookup",
		Version: 1,
		Spec: intent.Spec{Requires: []fact.AnyKey{
			demoProfileKey.Any(),
			capability.TenantKey.Any(),
		}},
		InputKey: demoInputKey.Any(),
		DecodeNode: func(nc *execution.NodeContext) error {
			var value demoInput
			if err := json.Unmarshal(nc.Invocation().Input.RawBytes(), &value); err != nil {
				return err
			}
			execution.Publish(nc, demoInputKey, value)
			return nil
		},
		Run: func(nc *execution.NodeContext) (any, effect.EffectPlan, intent.OutcomeMeta, error) {
			input, err := execution.Require[demoInput](nc, demoInputKey)
			if err != nil {
				return nil, effect.EffectPlan{}, intent.OutcomeMeta{}, err
			}
			profile, err := execution.Require(nc, demoProfileKey)
			if err != nil {
				return nil, effect.EffectPlan{}, intent.OutcomeMeta{}, err
			}
			tenant, err := execution.Require(nc, capability.TenantKey)
			if err != nil {
				return nil, effect.EffectPlan{}, intent.OutcomeMeta{}, err
			}
			output := demoOutput{Query: input.Query, Label: profile.Label, Tenant: tenant.ID, Version: "v1"}
			plan := effect.EffectPlan{
				LocalTx: []effect.Effect{&demoLocalEffect{state: state}},
				Durable: []effect.Effect{&demoNotifyEffect{Query: input.Query, Delivered: state.delivered}},
			}
			return output, plan, intent.OutcomeMeta{Tags: map[string]string{"demo": "true"}}, nil
		},
	}
}

func sourceAsCapability(registration source.Registration) capability.Registration {
	return capability.Registration{
		Name:        registration.Name,
		Requires:    registration.Requires,
		Provides:    registration.Provides,
		Kind:        registration.Kind,
		Speculation: registration.Speculation,
		Source:      registration.Source,
		Run:         registration.Run,
	}
}
