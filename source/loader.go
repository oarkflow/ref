package source

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// BatchFunc is the user-provided function that fetches multiple keys in one call.
// Implementations are source-specific: SQL IN clause, API batch endpoint, Redis MGET, etc.
type BatchFunc[K comparable, V any] func(ctx context.Context, keys []K) (map[K]V, error)

// LoaderConfig configures a DataLoader.
type LoaderConfig struct {
	// MaxBatch is the maximum number of keys to batch into a single call.
	// Zero means unlimited.
	MaxBatch int

	// Wait is how long to collect keys before dispatching a batch.
	// Zero means dispatch immediately when the first caller arrives (no windowing).
	Wait            time.Duration
	CacheTTL        time.Duration
	MaxCacheEntries int
	BatchTimeout    time.Duration
}

// Loader batches and deduplicates data fetches within a single invocation.
//
// When multiple goroutines call Load concurrently, the loader collects keys
// within a configurable time window and dispatches them in a single BatchFunc
// call. Identical keys within the same batch are deduplicated automatically.
//
// Loader is request-scoped: create one per invocation, let it be garbage collected
// when the request ends. No global state, no cross-request leaks.
type Loader[K comparable, V any] struct {
	batchFn BatchFunc[K, V]
	cfg     LoaderConfig

	mu      sync.Mutex
	cache   map[K]*loaderResult[V]
	pending *loaderBatch[K, V]
}

type loaderResult[V any] struct {
	value     V
	err       error
	ready     chan struct{}
	expiresAt time.Time
}

type loaderBatch[K comparable, V any] struct {
	keys    []K
	keySet  map[K]struct{}
	results map[K]*loaderResult[V]
	done    chan struct{} // closed when batch completes
	once    sync.Once
}

// NewLoader creates a new DataLoader with the given batch function and config.
func NewLoader[K comparable, V any](batchFn BatchFunc[K, V], cfg LoaderConfig) *Loader[K, V] {
	if cfg.CacheTTL <= 0 {
		cfg.CacheTTL = time.Second
	}
	if cfg.MaxCacheEntries <= 0 {
		cfg.MaxCacheEntries = 4096
	}
	return &Loader[K, V]{
		batchFn: batchFn,
		cfg:     cfg,
		cache:   make(map[K]*loaderResult[V]),
	}
}

// Load requests a single key. If other goroutines request keys within the
// wait window, all keys are batched into a single batchFn call.
// Results are cached for the lifetime of the Loader (i.e. one invocation).
func (l *Loader[K, V]) Load(ctx context.Context, key K) (V, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	l.mu.Lock()
	l.cleanupLocked(time.Now())

	// Check invocation-scoped cache
	if res, ok := l.cache[key]; ok {
		l.mu.Unlock()
		select {
		case <-res.ready:
			return res.value, res.err
		case <-ctx.Done():
			var zero V
			return zero, ctx.Err()
		}
	}

	if l.cfg.MaxCacheEntries > 0 && len(l.cache) >= l.cfg.MaxCacheEntries {
		l.removeOneLocked()
	}
	res := &loaderResult[V]{ready: make(chan struct{}), expiresAt: time.Now().Add(l.cfg.CacheTTL)}
	l.cache[key] = res

	batch := l.getOrCreateBatch(key, res)
	shouldDispatch := l.shouldDispatch(ctx, batch)
	l.mu.Unlock()

	if shouldDispatch {
		l.dispatch(ctx, batch)
	}

	select {
	case <-res.ready:
		return res.value, res.err
	case <-ctx.Done():
		var zero V
		return zero, ctx.Err()
	}
}

// LoadMany requests multiple keys in one call.
func (l *Loader[K, V]) LoadMany(ctx context.Context, keys []K) (map[K]V, error) {
	results := make(map[K]V, len(keys))
	var firstErr error

	for _, key := range keys {
		val, err := l.Load(ctx, key)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		results[key] = val
	}

	return results, firstErr
}

// Prime manually seeds the loader cache. Useful for data already in hand
// (e.g. a parent entity loaded earlier in the graph whose children also need it).
func (l *Loader[K, V]) Prime(key K, value V) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if _, ok := l.cache[key]; ok {
		return // don't overwrite
	}

	res := &loaderResult[V]{value: value, ready: make(chan struct{}), expiresAt: time.Now().Add(l.cfg.CacheTTL)}
	close(res.ready)
	l.cache[key] = res
}

// Clear removes a cached key, forcing re-fetch on next Load.
func (l *Loader[K, V]) Clear(key K) {
	l.mu.Lock()
	delete(l.cache, key)
	l.mu.Unlock()
}

// ClearAll removes all cached keys.
func (l *Loader[K, V]) ClearAll() {
	l.mu.Lock()
	l.cache = make(map[K]*loaderResult[V])
	l.mu.Unlock()
}

func (l *Loader[K, V]) cleanupLocked(now time.Time) {
	for key, result := range l.cache {
		if !result.expiresAt.IsZero() && now.After(result.expiresAt) {
			delete(l.cache, key)
		}
	}
}

func (l *Loader[K, V]) removeOneLocked() {
	for key := range l.cache {
		delete(l.cache, key)
		return
	}
}

func (l *Loader[K, V]) getOrCreateBatch(key K, res *loaderResult[V]) *loaderBatch[K, V] {
	if l.pending == nil {
		l.pending = &loaderBatch[K, V]{
			keySet:  make(map[K]struct{}),
			results: make(map[K]*loaderResult[V]),
			done:    make(chan struct{}),
		}
	}

	batch := l.pending

	if _, dup := batch.keySet[key]; !dup {
		batch.keys = append(batch.keys, key)
		batch.keySet[key] = struct{}{}
	}
	batch.results[key] = res

	return batch
}

func (l *Loader[K, V]) shouldDispatch(ctx context.Context, batch *loaderBatch[K, V]) bool {
	// No wait configured: dispatch immediately on first key
	if l.cfg.Wait <= 0 {
		return true
	}

	// Dispatch if batch is full
	if l.cfg.MaxBatch > 0 && len(batch.keys) >= l.cfg.MaxBatch {
		return true
	}

	// Schedule deferred dispatch on first key
	if len(batch.keys) == 1 {
		go func() {
			timer := time.NewTimer(l.cfg.Wait)
			defer timer.Stop()

			select {
			case <-timer.C:
				l.mu.Lock()
				b := l.pending
				l.pending = nil
				l.mu.Unlock()
				if b != nil {
					l.dispatch(ctx, b)
				}
			case <-batch.done:
				// Already dispatched by shouldDispatch (MaxBatch hit)
			}
		}()
	}

	return false
}

func (l *Loader[K, V]) dispatch(ctx context.Context, batch *loaderBatch[K, V]) {
	batch.once.Do(func() {
		l.mu.Lock()
		if l.pending == batch {
			l.pending = nil
		}
		l.mu.Unlock()

		var values map[K]V
		var batchErr error
		defer func() {
			if recovered := recover(); recovered != nil {
				batchErr = fmt.Errorf("ref: source batch panic: %v", recovered)
			}
			for key, res := range batch.results {
				if batchErr != nil {
					res.err = batchErr
				} else if value, ok := values[key]; ok {
					res.value = value
				}
				close(res.ready)
			}
			close(batch.done)
		}()

		if l.batchFn == nil {
			batchErr = fmt.Errorf("ref: source batch function is nil")
			return
		}
		batchCtx := context.WithoutCancel(ctx)
		if l.cfg.BatchTimeout > 0 {
			var cancel context.CancelFunc
			batchCtx, cancel = context.WithTimeout(batchCtx, l.cfg.BatchTimeout)
			defer cancel()
		}
		values, batchErr = l.batchFn(batchCtx, batch.keys)
	})
}
