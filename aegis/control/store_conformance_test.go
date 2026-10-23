package control

import (
	"context"
	"testing"
	"time"
)

// TestStoreConformance runs the same behavioral checks against every Store
// implementation, so SQLStore is held to exactly the semantics
// MemoryStore already defines (GrantService and Server depend on both
// behaving the same way; see server.go, grant.go).
func TestStoreConformance(t *testing.T) {
	stores := map[string]func(t *testing.T) Store{
		"MemoryStore": func(t *testing.T) Store {
			return NewMemoryStore()
		},
		"SQLStore": func(t *testing.T) Store {
			s, err := OpenSQLite(":memory:")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.Close() })
			return s
		},
	}

	for name, newStore := range stores {
		t.Run(name, func(t *testing.T) {
			t.Run("package round-trips and revokes", func(t *testing.T) {
				s := newStore(t)
				ctx := context.Background()
				if err := s.StorePackage(ctx, PackageRecord{PackageID: "pkg-1", ApplicationID: "app", Version: "1.0.0", BuildID: "b1"}); err != nil {
					t.Fatal(err)
				}
				got, err := s.GetPackage(ctx, "pkg-1")
				if err != nil {
					t.Fatal(err)
				}
				if got.ApplicationID != "app" || got.Version != "1.0.0" || got.Revoked {
					t.Fatalf("got %+v", got)
				}
				if revoked, _, err := s.IsPackageRevoked(ctx, "pkg-1"); err != nil || revoked {
					t.Fatalf("expected not revoked, got %v, %v", revoked, err)
				}

				if err := s.RevokePackage(ctx, "pkg-1", "compromised"); err != nil {
					t.Fatal(err)
				}
				revoked, reason, err := s.IsPackageRevoked(ctx, "pkg-1")
				if err != nil || !revoked || reason != "compromised" {
					t.Fatalf("expected revoked with reason, got %v %q %v", revoked, reason, err)
				}
				got, err = s.GetPackage(ctx, "pkg-1")
				if err != nil || !got.Revoked || got.RevokedAt == nil {
					t.Fatalf("GetPackage should reflect the revocation: %+v, %v", got, err)
				}
			})

			t.Run("getting an unknown package fails", func(t *testing.T) {
				s := newStore(t)
				if _, err := s.GetPackage(context.Background(), "nope"); err == nil {
					t.Fatal("expected ErrPackageNotFound")
				}
			})

			t.Run("revoking an unregistered package still records it", func(t *testing.T) {
				s := newStore(t)
				ctx := context.Background()
				if err := s.RevokePackage(ctx, "never-registered", "preemptive"); err != nil {
					t.Fatal(err)
				}
				revoked, reason, err := s.IsPackageRevoked(ctx, "never-registered")
				if err != nil || !revoked || reason != "preemptive" {
					t.Fatalf("got %v %q %v", revoked, reason, err)
				}
			})

			t.Run("checking an unknown package is not revoked, not an error", func(t *testing.T) {
				s := newStore(t)
				revoked, _, err := s.IsPackageRevoked(context.Background(), "unknown")
				if err != nil || revoked {
					t.Fatalf("got %v, %v", revoked, err)
				}
			})

			t.Run("master key round-trips and rejects the wrong length", func(t *testing.T) {
				s := newStore(t)
				ctx := context.Background()
				key := make([]byte, 32)
				for i := range key {
					key[i] = byte(i)
				}
				if err := s.StoreMasterKey(ctx, "pkg-2", key); err != nil {
					t.Fatal(err)
				}
				got, err := s.GetMasterKey(ctx, "pkg-2")
				if err != nil || string(got) != string(key) {
					t.Fatalf("got %x, want %x, err %v", got, key, err)
				}
				if err := s.StoreMasterKey(ctx, "pkg-3", []byte("too short")); err == nil {
					t.Fatal("expected a 32-byte length check")
				}
			})

			t.Run("getting an unknown master key fails", func(t *testing.T) {
				s := newStore(t)
				if _, err := s.GetMasterKey(context.Background(), "nope"); err == nil {
					t.Fatal("expected ErrMasterKeyNotFound")
				}
			})

			t.Run("grant round-trips, revokes, and session_id falls back to grant_id", func(t *testing.T) {
				s := newStore(t)
				ctx := context.Background()
				now := time.Now().UTC().Truncate(time.Second)
				g := GrantRecord{
					SessionID: "sess-1", PackageID: "pkg-1", ApplicationID: "app",
					Key: []byte("0123456789abcdef0123456789abcdef"), IssuedAt: now, ExpiresAt: now.Add(time.Hour),
					Conditions: []string{"single_host", "enforce_integrity"},
				}
				if err := s.RecordGrant(ctx, g); err != nil {
					t.Fatal(err)
				}
				if revoked, err := s.IsGrantRevoked(ctx, "sess-1"); err != nil || revoked {
					t.Fatalf("got %v, %v", revoked, err)
				}
				if err := s.RevokeGrant(ctx, "sess-1"); err != nil {
					t.Fatal(err)
				}
				if revoked, err := s.IsGrantRevoked(ctx, "sess-1"); err != nil || !revoked {
					t.Fatalf("expected revoked, got %v, %v", revoked, err)
				}
			})

			t.Run("revoking an unknown grant fails", func(t *testing.T) {
				s := newStore(t)
				if err := s.RevokeGrant(context.Background(), "nope"); err == nil {
					t.Fatal("expected ErrGrantNotFound")
				}
			})

			t.Run("checking an unknown grant fails", func(t *testing.T) {
				s := newStore(t)
				if _, err := s.IsGrantRevoked(context.Background(), "nope"); err == nil {
					t.Fatal("expected ErrGrantNotFound")
				}
			})

			t.Run("device round-trips and revokes", func(t *testing.T) {
				s := newStore(t)
				ctx := context.Background()
				if err := s.RecordDevice(ctx, DeviceRecord{DeviceID: "dev-1", Platform: "linux/amd64", Metadata: map[string]string{"owner": "qa"}}); err != nil {
					t.Fatal(err)
				}
				if revoked, err := s.IsDeviceRevoked(ctx, "dev-1"); err != nil || revoked {
					t.Fatalf("got %v, %v", revoked, err)
				}
				if err := s.RecordDevice(ctx, DeviceRecord{DeviceID: "dev-1", Platform: "linux/amd64", Revoked: true, RevokeReason: "stolen"}); err != nil {
					t.Fatal(err)
				}
				if revoked, err := s.IsDeviceRevoked(ctx, "dev-1"); err != nil || !revoked {
					t.Fatalf("expected revoked after re-recording, got %v, %v", revoked, err)
				}
			})

			t.Run("an unknown device is not revoked, not an error", func(t *testing.T) {
				s := newStore(t)
				revoked, err := s.IsDeviceRevoked(context.Background(), "unknown")
				if err != nil || revoked {
					t.Fatalf("got %v, %v", revoked, err)
				}
			})
		})
	}
}
