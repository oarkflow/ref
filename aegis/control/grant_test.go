package control

import (
	"context"
	"testing"

	"github.com/oarkflow/ref/aegis/runtime"
)

// TestRawMasterKeyGrantsReturnsTheExactMasterKey proves WithRawMasterKeyGrants
// does what aegis run -grant-server and aegis register both need: the
// grant's Key is the package's exact master key, unmodified, not an
// HKDF-derived per-session key (the default — see
// TestRuntimeIntegration_EndToEnd in control_test.go for that mode).
func TestRawMasterKeyGrantsReturnsTheExactMasterKey(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	masterKey := make([]byte, 32)
	for i := range masterKey {
		masterKey[i] = byte(i + 1)
	}
	if err := store.StoreMasterKey(ctx, "pkg-raw", masterKey); err != nil {
		t.Fatal(err)
	}

	gs := NewGrantService(store, NewKeyManager(store), WithRawMasterKeyGrants())
	grant, err := gs.RequestGrant(ctx, runtime.GrantRequest{PackageID: "pkg-raw"})
	if err != nil {
		t.Fatal(err)
	}
	if string(grant.Key) != string(masterKey) {
		t.Fatalf("grant key = %x, want the exact master key %x", grant.Key, masterKey)
	}
}

// TestDefaultGrantsDeriveASessionKey proves the default (no
// WithRawMasterKeyGrants) still derives a per-session key different from
// the master key, so WithRawMasterKeyGrants did not change the default and
// did not change TestRuntimeIntegration_EndToEnd's assumptions.
func TestDefaultGrantsDeriveASessionKey(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	masterKey := make([]byte, 32)
	for i := range masterKey {
		masterKey[i] = byte(i + 1)
	}
	if err := store.StoreMasterKey(ctx, "pkg-default", masterKey); err != nil {
		t.Fatal(err)
	}

	gs := NewGrantService(store, NewKeyManager(store))
	grant, err := gs.RequestGrant(ctx, runtime.GrantRequest{PackageID: "pkg-default", Version: "1.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	if string(grant.Key) == string(masterKey) {
		t.Fatal("expected a derived session key different from the master key by default")
	}

	wantKey, err := DeriveSessionKey(masterKey, grant.SessionID, "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if string(grant.Key) != string(wantKey) {
		t.Fatalf("grant key does not match DeriveSessionKey's own output: got %x, want %x", grant.Key, wantKey)
	}
}

// TestRawMasterKeyGrantsAppliesToRefresh proves RefreshGrant (used when a
// long-running client extends its session) honours the same mode as the
// initial grant, so a refreshed grant does not suddenly hand back a
// different kind of key than the one the client started with.
func TestRawMasterKeyGrantsAppliesToRefresh(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	masterKey := make([]byte, 32)
	for i := range masterKey {
		masterKey[i] = byte(i + 2)
	}
	if err := store.StoreMasterKey(ctx, "pkg-refresh", masterKey); err != nil {
		t.Fatal(err)
	}
	if err := store.StorePackage(ctx, PackageRecord{PackageID: "pkg-refresh", Version: "1.0.0"}); err != nil {
		t.Fatal(err)
	}

	gs := NewGrantService(store, NewKeyManager(store), WithRawMasterKeyGrants())
	grant, err := gs.RequestGrant(ctx, runtime.GrantRequest{PackageID: "pkg-refresh", Version: "1.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	refreshed, err := gs.RefreshGrant(ctx, grant.SessionID, "pkg-refresh")
	if err != nil {
		t.Fatal(err)
	}
	if string(refreshed.Key) != string(masterKey) {
		t.Fatalf("refreshed grant key = %x, want the exact master key %x", refreshed.Key, masterKey)
	}
}
