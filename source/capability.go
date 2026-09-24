package source

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/oarkflow/ref/execution"
	"github.com/oarkflow/ref/fact"
	"github.com/oarkflow/ref/graph"
)

// Registration describes a capability for the graph compiler.
// Duplicated type alias to avoid import cycle with capability package.
// The actual wiring happens at the call site via RegistrationAdapter.
type Registration struct {
	Name        string
	Requires    []fact.AnyKey
	Provides    []fact.AnyKey
	Kind        graph.NodeKind
	Speculation graph.SpeculationClass
	Cacheable   bool
	Idempotent  bool
	Source      *Spec
	Run         func(nc *execution.NodeContext) error
}

// SourceOption configures source capability behaviour.
type SourceOption func(*sourceConfig)

type sourceConfig struct {
	cache     Cache
	coalescer *Coalescer
	observers []func(Metrics)
	keyFunc   func(nc *execution.NodeContext) string
	clone     func(any) any
	breaker   *CircuitBreaker
}

// WithCache sets the L1/L2 cache for the source capability.
func WithCache(c Cache) SourceOption {
	return func(sc *sourceConfig) { sc.cache = c }
}

// WithCoalescer enables in-flight request deduplication.
func WithCoalescer(c *Coalescer) SourceOption {
	return func(sc *sourceConfig) { sc.coalescer = c }
}

// WithMetricsObserver adds a metrics callback for every source operation.
func WithMetricsObserver(fn func(Metrics)) SourceOption {
	return func(sc *sourceConfig) { sc.observers = append(sc.observers, fn) }
}

func WithCircuitBreaker(breaker *CircuitBreaker) SourceOption {
	return func(sc *sourceConfig) { sc.breaker = breaker }
}

// WithKeyFunc sets the cache/coalesce key extractor.
// The function should return a string that uniquely identifies the operation + params.
func WithKeyFunc(fn func(nc *execution.NodeContext) string) SourceOption {
	return func(sc *sourceConfig) { sc.keyFunc = fn }
}

func WithCacheValueCloner(fn func(any) any) SourceOption {
	return func(sc *sourceConfig) { sc.clone = fn }
}

func hashBytes(value []byte) uint64 {
	sum := sha256.Sum256(value)
	return binary.LittleEndian.Uint64(sum[:8])
}

func hashString(value string) uint64 {
	return hashBytes([]byte(value))
}

// NewFetchCapability creates a ReadNode capability for a source that fetches
// a single item per call. Automatically integrates caching, coalescing,
// and observability based on the Spec and options.
//
// The fetchFn receives the NodeContext and returns the result. The caller
// is responsible for reading input parameters from facts/invocation.
func NewFetchCapability(
	name string,
	spec Spec,
	outputKey fact.AnyKey,
	fetchFn func(nc *execution.NodeContext) (any, error),
	opts ...SourceOption,
) Registration {
	cfg := &sourceConfig{}
	for _, opt := range opts {
		opt(cfg)
	}

	reg := Registration{
		Name:     name,
		Kind:     graph.ReadNode,
		Provides: []fact.AnyKey{outputKey},
		Source:   &spec,
	}

	reg.Run = func(nc *execution.NodeContext) error {
		if fetchFn == nil {
			return fmt.Errorf("ref: source fetch function is nil")
		}
		start := time.Now()
		metrics := Metrics{
			SourceName: spec.Name,
			SourceKind: spec.Kind,
			Operation:  "fetch",
			QueryHash:  hashString(spec.Name),
		}

		cacheKey, cacheEnabled := buildCacheKey(nc, spec, cfg)
		metrics.QueryHash = cacheKey.QueryHash
		if spec.Cacheable && cfg.cache != nil && cacheEnabled {
			if val, hit, err := cfg.cache.Get(cacheKey); err == nil && hit {
				metrics.CacheHit = true
				metrics.CacheLevel = cacheLevel(spec.CacheScope)
				metrics.TotalTime = time.Since(start)
				emitMetrics(cfg, metrics)
				if val != nil {
					publishAny(nc, outputKey, cloneSourceValue(cfg, val))
				}
				return nil
			}
		}

		var val any
		var fetchErr error
		fetch := func() (any, error) {
			if cfg.breaker == nil {
				return fetchFn(nc)
			}
			return cfg.breaker.Execute(nc, func(context.Context) (any, error) {
				return fetchFn(nc)
			})
		}
		if spec.Coalescible && cfg.coalescer != nil && cacheEnabled {
			metrics.Coalesced = true
			val, fetchErr = cfg.coalescer.DoContext(nc, cacheKey.String(), fetch)
		} else {
			val, fetchErr = fetch()
		}

		metrics.ExecTime = time.Since(start)
		metrics.TotalTime = time.Since(start)
		if fetchErr != nil {
			metrics.Error = true
			emitMetrics(cfg, metrics)
			return fetchErr
		}

		if spec.Cacheable && cfg.cache != nil && cacheEnabled && val != nil {
			_ = cfg.cache.Set(cacheKey, cloneSourceValue(cfg, val), spec.CacheTTL)
		}

		emitMetrics(cfg, metrics)
		publishAny(nc, outputKey, val)
		return nil
	}

	return reg
}

// NewBatchCapability creates a ReadNode capability where the underlying source
// supports batch fetching natively.
//
// The batchFn receives a list of keys and returns results for all of them.
// REF automatically batches multiple concurrent requests (from parallel nodes
// or repeated access patterns) into a single batchFn call via the DataLoader.
//
// The loaderKeyFact is used to read the lookup key(s) from the fact store.
// The outputKey is where the result is published.
func NewBatchCapability[K comparable, V any](
	name string,
	spec Spec,
	outputKey fact.AnyKey,
	batchFn BatchFunc[K, V],
	keyExtractor func(nc *execution.NodeContext) K,
	loaderCfg LoaderConfig,
	opts ...SourceOption,
) Registration {
	cfg := &sourceConfig{}
	for _, opt := range opts {
		opt(cfg)
	}

	loader := NewLoader[scopedBatchKey[K], V](func(ctx context.Context, keys []scopedBatchKey[K]) (map[scopedBatchKey[K]]V, error) {
		rawKeys := make([]K, len(keys))
		for i, key := range keys {
			rawKeys[i] = key.key
		}
		values, err := func() (map[K]V, error) {
			if cfg.breaker == nil {
				return batchFn(ctx, rawKeys)
			}
			value, callErr := cfg.breaker.Execute(ctx, func(callCtx context.Context) (any, error) {
				return batchFn(callCtx, rawKeys)
			})
			if callErr != nil {
				return nil, callErr
			}
			typed, ok := value.(map[K]V)
			if !ok {
				return nil, fmt.Errorf("ref: source batch returned unexpected result type")
			}
			return typed, nil
		}()
		if err != nil {
			return nil, err
		}
		result := make(map[scopedBatchKey[K]]V, len(values))
		for _, key := range keys {
			if value, ok := values[key.key]; ok {
				result[key] = value
			}
		}
		return result, nil
	}, loaderCfg)

	reg := Registration{
		Name:     name,
		Kind:     graph.ReadNode,
		Provides: []fact.AnyKey{outputKey},
		Source:   &spec,
	}

	reg.Run = func(nc *execution.NodeContext) error {
		if keyExtractor == nil {
			return fmt.Errorf("ref: source batch key extractor is nil")
		}
		start := time.Now()
		metrics := Metrics{
			SourceName: spec.Name,
			SourceKind: spec.Kind,
			Operation:  "batch_fetch",
			Batched:    true,
		}

		key := keyExtractor(nc)
		cacheKey, cacheEnabled := buildCacheKeyForParam(spec, cfg, nc, key)
		metrics.QueryHash = cacheKey.QueryHash
		if spec.Cacheable && cfg.cache != nil && cacheEnabled {
			if val, hit, err := cfg.cache.Get(cacheKey); err == nil && hit {
				metrics.CacheHit = true
				metrics.CacheLevel = cacheLevel(spec.CacheScope)
				metrics.TotalTime = time.Since(start)
				emitMetrics(cfg, metrics)
				if val != nil {
					publishAny(nc, outputKey, cloneSourceValue(cfg, val))
				}
				return nil
			}
		}

		scope := cacheKey.ScopeString()
		if !cacheEnabled {
			scope = invocationScope(nc)
		}
		val, err := loader.Load(nc, scopedBatchKey[K]{scope: scope, key: key})

		metrics.ExecTime = time.Since(start)
		metrics.TotalTime = time.Since(start)
		if err != nil {
			metrics.Error = true
			emitMetrics(cfg, metrics)
			return err
		}

		if spec.Cacheable && cfg.cache != nil && cacheEnabled {
			_ = cfg.cache.Set(cacheKey, cloneSourceValue(cfg, val), spec.CacheTTL)
		}

		emitMetrics(cfg, metrics)
		publishAny(nc, outputKey, val)
		return nil
	}

	return reg
}

type scopedBatchKey[K comparable] struct {
	scope string
	key   K
}

func buildCacheKey(nc *execution.NodeContext, spec Spec, cfg *sourceConfig) (CacheKey, bool) {
	tenantID, principalID, policyHash, invocationID, enabled := sourceIdentity(nc, spec)
	queryHash := hashString(spec.Name)
	var paramHash uint64
	if cfg.keyFunc != nil {
		paramHash = hashString(cfg.keyFunc(nc))
	} else if nc != nil && nc.Invocation() != nil {
		input := nc.Invocation().Input
		paramHash = hashString(input.ContentType() + "\x00" + string(input.RawBytes()))
	}
	return CacheKey{
		SourceName:   spec.Name,
		TenantID:     tenantID,
		PrincipalID:  principalID,
		QueryHash:    queryHash,
		ParamHash:    paramHash,
		PolicyHash:   policyHash,
		InvocationID: invocationID,
	}, enabled
}

func buildCacheKeyForParam[K comparable](spec Spec, cfg *sourceConfig, nc *execution.NodeContext, key K) (CacheKey, bool) {
	cacheKey, enabled := buildCacheKey(nc, spec, cfg)
	encoded, err := json.Marshal(struct {
		Type  string
		Value K
	}{Type: fmt.Sprintf("%T", key), Value: key})
	if err != nil {
		return cacheKey, false
	}
	cacheKey.ParamHash = hashBytes(encoded)
	return cacheKey, enabled
}

func sourceIdentity(nc *execution.NodeContext, spec Spec) (string, string, uint64, string, bool) {
	if spec.Consistency == Strong {
		return "", "", 0, "", false
	}
	if nc == nil {
		return "", "", 0, "", spec.Security == SecurityPublic
	}

	inv := nc.Invocation()
	principalID := ""
	identityTenant := ""
	if identity := inv.VerifiedIdentity(); identity != nil {
		principalID = identity.PrincipalID()
		identityTenant = identity.Tenant()
	}

	decisions := nc.Decisions()
	constraints := decisions.Constraints()
	tenantID := constraints.TenantID
	if identityTenant != "" {
		if tenantID != "" && tenantID != identityTenant {
			return "", "", 0, "", false
		}
		tenantID = identityTenant
	}

	if spec.Security != SecurityPublic {
		if decisions == nil || decisions.Required() == 0 || decisions.Verdict() != execution.VerdictAllow {
			return "", "", 0, "", false
		}
		if tenantID == "" {
			return "", "", 0, "", false
		}
	}
	if spec.Security == SecurityPrincipal && principalID == "" {
		return "", "", 0, "", false
	}
	if spec.Consistency == Session && spec.CacheScope != ScopeInvocation {
		return "", "", 0, "", false
	}

	regions := append([]string(nil), constraints.Regions...)
	fields := append([]string(nil), constraints.Fields...)
	sort.Strings(regions)
	sort.Strings(fields)
	policyMaterial := strings.Join([]string{tenantID, strings.Join(regions, ","), strings.Join(fields, ",")}, "\x00")
	invocationID := ""
	if spec.CacheScope == ScopeInvocation && inv != nil {
		invocationID = string(inv.ID)
		if invocationID == "" {
			return "", "", 0, "", false
		}
	}
	return tenantID, principalID, hashString(policyMaterial), invocationID, true
}

func invocationScope(nc *execution.NodeContext) string {
	if nc != nil && nc.Invocation() != nil && nc.Invocation().ID != "" {
		return string(nc.Invocation().ID)
	}
	return fmt.Sprintf("%p", nc)
}

func cloneSourceValue(cfg *sourceConfig, value any) any {
	if cfg != nil && cfg.clone != nil {
		return cfg.clone(value)
	}
	return value
}

func cacheLevel(scope CacheScope) string {
	switch scope {
	case ScopeInvocation:
		return "L0"
	case ScopeProcess:
		return "L1"
	case ScopeDistributed:
		return "L2"
	default:
		return ""
	}
}

func emitMetrics(cfg *sourceConfig, m Metrics) {
	for _, fn := range cfg.observers {
		fn(m)
	}
}

func publishAny(nc *execution.NodeContext, key fact.AnyKey, val any) {
	if nc == nil || nc.Facts() == nil {
		return
	}
	slot, ok := nc.SlotOf(key.DefID)
	if !ok {
		return
	}
	if nc.Facts().ProvenanceEnabled() {
		fact.PutWithProducer(nc.Facts(), slot, val, uint32(nc.NodeID()))
	} else {
		fact.Put(nc.Facts(), slot, val)
	}
}
