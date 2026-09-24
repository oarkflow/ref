package source

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oarkflow/ref/execution"
	"github.com/oarkflow/ref/fact"
	"github.com/oarkflow/ref/invocation"
)

func TestCacheKeyRequiresCompletedTenantPolicy(t *testing.T) {
	key := fact.NewKey[string]("unused")
	decisions := execution.NewDecisionSet(1)
	nc := execution.NewNodeContext(context.Background(), &invocation.Invocation{ID: "inv-1"}, fact.NewStore(0), nil, decisions, 0, nil, nil, 0)
	spec := Spec{Name: "users", Security: SecurityTenant, CacheScope: ScopeProcess, Consistency: Eventual}
	cfg := &sourceConfig{}
	if _, enabled := buildCacheKey(nc, spec, cfg); enabled {
		t.Fatal("tenant-scoped cache key was enabled before policy completion")
	}
	decisions.RecordAllow("tenant", []execution.Constraint{{Field: "tenant_id", Values: []string{"tenant-a"}}})
	cacheKey, enabled := buildCacheKey(nc, spec, cfg)
	if !enabled || cacheKey.TenantID != "tenant-a" {
		t.Fatalf("completed tenant policy did not scope cache key: %+v enabled=%v", cacheKey, enabled)
	}
	_ = key
}

func TestCacheKeyRejectsIdentityTenantContradiction(t *testing.T) {
	decisions := execution.NewDecisionSet(1)
	decisions.RecordAllow("tenant", []execution.Constraint{{Field: "tenant_id", Values: []string{"tenant-a"}}})
	identity := invocation.NewVerifiedIdentity("user-1", "tenant-b", nil, nil, nil)
	nc := execution.NewNodeContext(context.Background(), &invocation.Invocation{ID: "inv-1", Identity: identity}, fact.NewStore(0), nil, decisions, 0, nil, nil, 0)
	spec := Spec{Name: "users", Security: SecurityTenant, CacheScope: ScopeProcess, Consistency: Eventual}
	if _, enabled := buildCacheKey(nc, spec, &sourceConfig{}); enabled {
		t.Fatal("contradictory tenant identities enabled a shared cache key")
	}
}

func TestPrincipalCacheScopeUsesVerifiedIdentity(t *testing.T) {
	decisions := execution.NewDecisionSet(1)
	decisions.RecordAllow("tenant", []execution.Constraint{{Field: "tenant_id", Values: []string{"tenant-a"}}})
	identity := invocation.NewVerifiedIdentity("user-1", "tenant-a", nil, nil, nil)
	nc := execution.NewNodeContext(context.Background(), &invocation.Invocation{ID: "inv-1", Identity: identity}, fact.NewStore(0), nil, decisions, 0, nil, nil, 0)
	spec := Spec{Name: "profile", Security: SecurityPrincipal, CacheScope: ScopeProcess, Consistency: Eventual}
	cacheKey, enabled := buildCacheKey(nc, spec, &sourceConfig{keyFunc: func(*execution.NodeContext) string { return "profile:1" }})
	if !enabled || cacheKey.PrincipalID != "user-1" {
		t.Fatalf("verified principal missing from cache key: %+v enabled=%v", cacheKey, enabled)
	}
}

func TestStrongConsistencyDisablesCacheAndCoalescing(t *testing.T) {
	nc := execution.NewNodeContext(context.Background(), &invocation.Invocation{ID: "inv-1"}, fact.NewStore(0), nil, execution.NewDecisionSet(), 0, nil, nil, 0)
	spec := Spec{Name: "account", Security: SecurityPublic, CacheScope: ScopeProcess, Consistency: Strong}
	if _, enabled := buildCacheKey(nc, spec, &sourceConfig{}); enabled {
		t.Fatal("strong-consistency source enabled an optimization key")
	}
}

type largeKey struct {
	Value [70]byte
}

func TestGenericKeyHashingIsStableAndBoundsSafe(t *testing.T) {
	nc := execution.NewNodeContext(context.Background(), &invocation.Invocation{ID: "inv-1"}, fact.NewStore(0), nil, execution.NewDecisionSet(), 0, nil, nil, 0)
	spec := Spec{Name: "items", Security: SecurityPublic, CacheScope: ScopeProcess, Consistency: Eventual}
	cfg := &sourceConfig{}
	first, firstEnabled := buildCacheKeyForParam(spec, cfg, nc, true)
	second, secondEnabled := buildCacheKeyForParam(spec, cfg, nc, true)
	if !firstEnabled || !secondEnabled || first.ParamHash != second.ParamHash {
		t.Fatalf("boolean key hash is unstable: %x %x", first.ParamHash, second.ParamHash)
	}
	value := largeKey{Value: [70]byte{1, 2, 3}}
	first, firstEnabled = buildCacheKeyForParam(spec, cfg, nc, value)
	second, secondEnabled = buildCacheKeyForParam(spec, cfg, nc, value)
	if !firstEnabled || !secondEnabled || first.ParamHash != second.ParamHash {
		t.Fatalf("large key hash is unstable: %x %x", first.ParamHash, second.ParamHash)
	}
	first, _ = buildCacheKeyForParam(spec, cfg, nc, "same")
	second, _ = buildCacheKeyForParam(spec, cfg, nc, "same")
	if first.ParamHash != second.ParamHash {
		t.Fatal("string key hash is unstable")
	}
}

func TestProcessCacheSeparatesSecurityScopes(t *testing.T) {
	cache := NewProcessCache(ProcessCacheConfig{MaxEntries: 8})
	first := CacheKey{SourceName: "users", TenantID: "a", PrincipalID: "u1", PolicyHash: 1, QueryHash: 2, ParamHash: 3}
	second := CacheKey{SourceName: "users", TenantID: "b", PrincipalID: "u1", PolicyHash: 1, QueryHash: 2, ParamHash: 3}
	if err := cache.Set(first, "tenant-a", time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := cache.Set(second, "tenant-b", time.Minute); err != nil {
		t.Fatal(err)
	}
	value, _, _ := cache.Get(first)
	if value != "tenant-a" {
		t.Fatalf("tenant cache entry crossed scope: %v", value)
	}
	value, _, _ = cache.Get(second)
	if value != "tenant-b" {
		t.Fatalf("tenant cache entry crossed scope: %v", value)
	}
}

func TestCoalescerWaiterCanCancel(t *testing.T) {
	coalescer := NewCoalescer()
	started := make(chan struct{})
	release := make(chan struct{})
	leaderDone := make(chan struct{})
	go func() {
		_, _ = coalescer.Do("key", func() (any, error) {
			close(started)
			<-release
			return "value", nil
		})
		close(leaderDone)
	}()
	<-started
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := coalescer.DoContext(ctx, "key", func() (any, error) {
		t.Fatal("canceled waiter executed source function")
		return nil, nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
	close(release)
	<-leaderDone
}

func TestLoaderPanicReleasesWaiters(t *testing.T) {
	loader := NewLoader[string, string](func(context.Context, []string) (map[string]string, error) {
		panic("batch failure")
	}, LoaderConfig{})
	var completed atomic.Bool
	go func() {
		_, _ = loader.Load(context.Background(), "a")
		completed.Store(true)
	}()
	select {
	case <-time.After(time.Second):
		if !completed.Load() {
			t.Fatal("loader panic left waiters blocked")
		}
	}
}
