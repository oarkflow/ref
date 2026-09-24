package source

import (
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// CacheKey includes authorization scope to prevent cross-tenant data leaks.
// Every field participates in equality — missing a scope field means a cache
// isolation bug.
type CacheKey struct {
	SourceName   string
	TenantID     string
	PrincipalID  string
	QueryHash    uint64
	ParamHash    uint64
	PolicyHash   uint64
	InvocationID string
}

func (k CacheKey) ScopeString() string {
	return strings.Join([]string{k.TenantID, k.PrincipalID, strconv.FormatUint(k.PolicyHash, 10), k.InvocationID}, "\x00")
}

func (k CacheKey) String() string {
	return strings.Join([]string{k.SourceName, k.ScopeString(), strconv.FormatUint(k.QueryHash, 10), strconv.FormatUint(k.ParamHash, 10)}, "\x00")
}

// cacheEntry pairs a value with its expiry and negative-result flag.
type cacheEntry struct {
	value     any
	expiresAt time.Time
	negative  bool // true = cached "not found"
}

func (e *cacheEntry) expired(now time.Time) bool {
	return !e.expiresAt.IsZero() && now.After(e.expiresAt)
}

// Cache is the interface for any cache level.
type Cache interface {
	// Get returns the cached value and true if present and not expired.
	Get(key CacheKey) (any, bool, error)

	// Set stores a value with a TTL.
	Set(key CacheKey, value any, ttl time.Duration) error

	// Delete removes a cached entry.
	Delete(key CacheKey) error
}

// ProcessCache is an L1 in-process cache with TTL, bounded size, and
// sharded locking for low contention.
//
// Thread-safe. Suitable for configuration, schemas, permission metadata,
// feature flags, and other data that changes infrequently.
type ProcessCache struct {
	shards     [cacheShardCount]cacheShard
	capacityMu sync.Mutex
	maxSize    int64
	curSize    atomic.Int64
	hits       atomic.Uint64
	misses     atomic.Uint64
	evicts     atomic.Uint64
}

const cacheShardCount = 32

type cacheShard struct {
	mu      sync.RWMutex
	entries map[CacheKey]*cacheEntry
}

// ProcessCacheConfig configures a ProcessCache.
type ProcessCacheConfig struct {
	// MaxEntries is the maximum number of entries across all shards.
	// Zero means unbounded (not recommended for production).
	MaxEntries int
}

// NewProcessCache creates a new in-process cache.
func NewProcessCache(cfg ProcessCacheConfig) *ProcessCache {
	pc := &ProcessCache{
		maxSize: int64(cfg.MaxEntries),
	}
	for i := range pc.shards {
		pc.shards[i].entries = make(map[CacheKey]*cacheEntry)
	}
	return pc
}

func (pc *ProcessCache) shard(key CacheKey) *cacheShard {
	h := uint64(14695981039346656037)
	for _, value := range []string{key.SourceName, key.TenantID, key.PrincipalID, key.InvocationID} {
		for _, b := range []byte(value) {
			h ^= uint64(b)
			h *= 1099511628211
		}
	}
	for _, value := range []uint64{key.QueryHash, key.ParamHash, key.PolicyHash} {
		h ^= value
		h *= 1099511628211
	}
	return &pc.shards[h%cacheShardCount]
}

// Get returns the cached value if present and not expired.
func (pc *ProcessCache) Get(key CacheKey) (any, bool, error) {
	s := pc.shard(key)
	s.mu.RLock()
	entry, ok := s.entries[key]
	s.mu.RUnlock()

	if !ok {
		pc.misses.Add(1)
		return nil, false, nil
	}

	if entry.expired(time.Now()) {
		// Lazy expiry: remove on access
		s.mu.Lock()
		if e, stillThere := s.entries[key]; stillThere && e.expired(time.Now()) {
			delete(s.entries, key)
			pc.curSize.Add(-1)
		}
		s.mu.Unlock()
		pc.misses.Add(1)
		return nil, false, nil
	}

	pc.hits.Add(1)

	if entry.negative {
		return nil, true, nil // cached "not found"
	}

	return entry.value, true, nil
}

// Set stores a value with a TTL.
func (pc *ProcessCache) Set(key CacheKey, value any, ttl time.Duration) error {
	entry := &cacheEntry{value: value}
	if ttl > 0 {
		entry.expiresAt = time.Now().Add(ttl)
	}
	return pc.store(key, entry)
}

func (pc *ProcessCache) store(key CacheKey, entry *cacheEntry) error {
	pc.capacityMu.Lock()
	defer pc.capacityMu.Unlock()
	s := pc.shard(key)
	if pc.maxSize > 0 && pc.curSize.Load() >= pc.maxSize {
		pc.sweepShard(s)
		if pc.curSize.Load() >= pc.maxSize {
			pc.evicts.Add(1)
			return nil
		}
	}
	s.mu.Lock()
	_, existed := s.entries[key]
	s.entries[key] = entry
	s.mu.Unlock()
	if !existed {
		pc.curSize.Add(1)
	}
	return nil
}

// SetNegative caches a "not found" result with a short TTL.
// Prevents repeated lookups for nonexistent entities.
func (pc *ProcessCache) SetNegative(key CacheKey, ttl time.Duration) error {
	if ttl <= 0 {
		ttl = 10 * time.Second // default negative TTL
	}

	entry := &cacheEntry{
		negative:  true,
		expiresAt: time.Now().Add(ttl),
	}
	return pc.store(key, entry)
}

// Delete removes a cached entry.
func (pc *ProcessCache) Delete(key CacheKey) error {
	s := pc.shard(key)
	s.mu.Lock()
	if _, ok := s.entries[key]; ok {
		delete(s.entries, key)
		pc.curSize.Add(-1)
	}
	s.mu.Unlock()
	return nil
}

// InvalidateSource removes all entries for a given source name.
func (pc *ProcessCache) InvalidateSource(sourceName string) {
	for i := range pc.shards {
		s := &pc.shards[i]
		s.mu.Lock()
		for k := range s.entries {
			if k.SourceName == sourceName {
				delete(s.entries, k)
				pc.curSize.Add(-1)
			}
		}
		s.mu.Unlock()
	}
}

// InvalidateTenant removes all entries for a given tenant.
func (pc *ProcessCache) InvalidateTenant(tenantID string) {
	for i := range pc.shards {
		s := &pc.shards[i]
		s.mu.Lock()
		for k := range s.entries {
			if k.TenantID == tenantID {
				delete(s.entries, k)
				pc.curSize.Add(-1)
			}
		}
		s.mu.Unlock()
	}
}

// Stats returns cache statistics.
func (pc *ProcessCache) Stats() CacheStats {
	return CacheStats{
		Size:   pc.curSize.Load(),
		Hits:   pc.hits.Load(),
		Misses: pc.misses.Load(),
		Evicts: pc.evicts.Load(),
	}
}

// CacheStats holds cache observability counters.
type CacheStats struct {
	Size   int64
	Hits   uint64
	Misses uint64
	Evicts uint64
}

func (pc *ProcessCache) sweepShard(s *cacheShard) {
	now := time.Now()
	s.mu.Lock()
	for k, e := range s.entries {
		if e.expired(now) {
			delete(s.entries, k)
			pc.curSize.Add(-1)
		}
	}
	s.mu.Unlock()
}
