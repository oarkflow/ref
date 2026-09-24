package platform

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oarkflow/fh/pkg/storage/kv"
	"github.com/oarkflow/ref/platform/spi"
	_ "modernc.org/sqlite"
)

func TestAtomicCacheIdempotencyClaimIsLinearizable(t *testing.T) {
	backend := kv.NewMemoryStore()
	defer backend.Close()
	handle, err := wrapCache("cache", backend)
	if err != nil {
		t.Fatal(err)
	}
	store := newAtomicCacheIdempotencyStore(handle, handle.mutator)
	const workers = 32
	var acquired atomic.Int32
	var unexpected atomic.Int32
	var wait sync.WaitGroup
	wait.Add(workers)
	for i := 0; i < workers; i++ {
		go func(index int) {
			defer wait.Done()
			claim, claimErr := store.Claim(context.Background(), "key", "fingerprint", "owner-"+string(rune(index+1)), time.Minute)
			if claimErr != nil {
				t.Errorf("claim: %v", claimErr)
				return
			}
			switch claim.State {
			case spi.IdempotencyAcquired:
				acquired.Add(1)
			case spi.IdempotencyInProgress:
			default:
				unexpected.Add(1)
			}
		}(i)
	}
	wait.Wait()
	if acquired.Load() != 1 || unexpected.Load() != 0 {
		t.Fatalf("expected one acquired claim, got acquired=%d unexpected=%d", acquired.Load(), unexpected.Load())
	}
}

func TestAtomicCacheIdempotencyFingerprintAndReplay(t *testing.T) {
	backend := kv.NewMemoryStore()
	defer backend.Close()
	handle, err := wrapCache("cache", backend)
	if err != nil {
		t.Fatal(err)
	}
	store := newAtomicCacheIdempotencyStore(handle, handle.mutator)
	ctx := context.Background()
	claim, err := store.Claim(ctx, "key", "fingerprint-a", "owner-a", time.Minute)
	if err != nil || claim.State != spi.IdempotencyAcquired {
		t.Fatalf("claim = %+v, %v", claim, err)
	}
	claim, err = store.Claim(ctx, "key", "fingerprint-b", "owner-b", time.Minute)
	if err != nil || claim.State != spi.IdempotencyConflict {
		t.Fatalf("conflicting fingerprint = %+v, %v", claim, err)
	}
	response := spi.IdempotencyResponse{Status: 201, Body: []byte(`{"ok":true}`)}
	if err := store.Complete(ctx, "key", "fingerprint-a", "owner-a", response, time.Minute); err != nil {
		t.Fatal(err)
	}
	claim, err = store.Claim(ctx, "key", "fingerprint-a", "owner-c", time.Minute)
	if err != nil || claim.State != spi.IdempotencyReplay || claim.Response.Status != 201 || string(claim.Response.Body) != string(response.Body) {
		t.Fatalf("replay = %+v, %v", claim, err)
	}
	if err := store.Release(ctx, "key", "owner-a"); err != nil {
		t.Fatal(err)
	}
}

func TestAtomicCacheIdempotencyReleaseAllowsRetry(t *testing.T) {
	backend := kv.NewMemoryStore()
	defer backend.Close()
	handle, err := wrapCache("cache", backend)
	if err != nil {
		t.Fatal(err)
	}
	store := newAtomicCacheIdempotencyStore(handle, handle.mutator)
	ctx := context.Background()
	claim, err := store.Claim(ctx, "key", "fingerprint", "owner-a", time.Minute)
	if err != nil || claim.State != spi.IdempotencyAcquired {
		t.Fatal(err)
	}
	if err := store.Release(ctx, "key", "wrong-owner"); err != nil {
		t.Fatal(err)
	}
	claim, err = store.Claim(ctx, "key", "fingerprint", "owner-b", time.Minute)
	if err != nil || claim.State != spi.IdempotencyInProgress {
		t.Fatalf("wrong owner released claim: %+v, %v", claim, err)
	}
	if err := store.Release(ctx, "key", "owner-a"); err != nil {
		t.Fatal(err)
	}
	claim, err = store.Claim(ctx, "key", "fingerprint", "owner-b", time.Minute)
	if err != nil || claim.State != spi.IdempotencyAcquired {
		t.Fatalf("released claim could not be retried: %+v, %v", claim, err)
	}
}

func TestSQLCacheIdempotencyLifecycle(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	cache := &sqlCache{db: &Database{DB: db, Dialect: "sqlite"}, table: "cache", done: make(chan struct{})}
	if err := cache.migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	claim, err := cache.Claim(ctx, "key", "fingerprint", "owner", time.Minute)
	if err != nil || claim.State != spi.IdempotencyAcquired {
		t.Fatalf("claim = %+v, %v", claim, err)
	}
	if err := cache.Complete(ctx, "key", "fingerprint", "owner", spi.IdempotencyResponse{Status: 202, Body: []byte("queued")}, time.Minute); err != nil {
		t.Fatal(err)
	}
	claim, err = cache.Claim(ctx, "key", "fingerprint", "other", time.Minute)
	if err != nil || claim.State != spi.IdempotencyReplay || claim.Response.Status != 202 {
		t.Fatalf("replay = %+v, %v", claim, err)
	}
	if err := cache.Release(ctx, "missing", "owner"); err != nil && !errors.Is(err, spi.ErrConflict) {
		t.Fatalf("release missing key: %v", err)
	}
}
