package deploy

import (
	"context"
	"errors"
	"testing"

	"github.com/oarkflow/ref/signing"
)

// TestRevisionApprovalIsSigned pins that the approval state is signed: a
// status or approvals list edited in the store cannot be activated.
func TestRevisionApprovalIsSigned(t *testing.T) {
	ctx := context.Background()
	key, _ := signing.GenerateEd25519("deploy")
	keys, _ := signing.NewKeySet(key)
	for name, configure := range map[string]func(*Manager){
		"hmac": func(*Manager) {},
		"key":  func(m *Manager) { m.Secret, m.Signer = nil, keys },
	} {
		t.Run(name, func(t *testing.T) {
			store := NewMemoryStore()
			m := newManager(store)
			m.Approvals = 2
			configure(m)

			// A pending revision marked approved in the store is refused.
			r, err := m.Propose(ctx, []byte(appSource("1")), "alice", "")
			if err != nil {
				t.Fatal(err)
			}
			forged := *r
			forged.Status = StatusApproved
			if err := store.Update(ctx, &forged); err != nil {
				t.Fatal(err)
			}
			if _, err := m.Activate(ctx, r.ID, "mallory"); !errors.Is(err, ErrTampered) {
				t.Fatalf("activate a status-only approval: %v", err)
			}
			// Even with a fabricated approvals list.
			forged.Approvals = []Decision{{By: "bob"}, {By: "carol"}}
			if err := store.Update(ctx, &forged); err != nil {
				t.Fatal(err)
			}
			if _, err := m.Activate(ctx, r.ID, "mallory"); !errors.Is(err, ErrTampered) {
				t.Fatalf("activate fabricated approvals: %v", err)
			}

			// The real flow works.
			forged.Status, forged.Approvals = StatusPending, nil
			if err := store.Update(ctx, &forged); err != nil {
				t.Fatal(err)
			}
			if _, err := m.Approve(ctx, r.ID, "bob", ""); err != nil {
				t.Fatal(err)
			}
			approved, err := m.Approve(ctx, r.ID, "carol", "")
			if err != nil || approved.Status != StatusApproved {
				t.Fatalf("approve: %v %+v", err, approved)
			}
			if approved.ApprovalSignature == "" && approved.ApprovalKeySignature == nil {
				t.Fatal("no approval signature")
			}
			// Tampering with the signed approvals list is refused.
			for _, approvals := range [][]Decision{
				{{By: "bob"}, {By: "mallory"}},            // swapped approver
				{{By: "bob"}},                             // below the threshold
				{{By: "bob"}, {By: "bob"}},                // duplicate
				{{By: "alice"}, {By: "bob"}},              // the author
				{{By: "bob"}, {By: "carol"}, {By: "dan"}}, // extra approver
			} {
				tampered := *approved
				tampered.Approvals = approvals
				if err := store.Update(ctx, &tampered); err != nil {
					t.Fatal(err)
				}
				if _, err := m.Activate(ctx, r.ID, "bob"); !errors.Is(err, ErrTampered) {
					t.Fatalf("activate with approvals %v: %v", approvals, err)
				}
			}
			if err := store.Update(ctx, approved); err != nil {
				t.Fatal(err)
			}
			if _, err := m.Activate(ctx, r.ID, "bob"); err != nil {
				t.Fatalf("activate: %v", err)
			}

			// A superseded revision whose approval signature was stripped
			// cannot be rolled back to.
			r2, _ := m.Propose(ctx, []byte(appSource("2")), "alice", "")
			_, _ = m.Approve(ctx, r2.ID, "bob", "")
			_, _ = m.Approve(ctx, r2.ID, "carol", "")
			if _, err := m.Activate(ctx, r2.ID, "bob"); err != nil {
				t.Fatalf("activate second: %v", err)
			}
			old, _ := store.Get(ctx, r.ID)
			stripped := *old
			stripped.ApprovalSignature, stripped.ApprovalKeySignature = "", nil
			if err := store.Update(ctx, &stripped); err != nil {
				t.Fatal(err)
			}
			if _, err := m.Rollback(ctx, r.ID, "bob", "test"); !errors.Is(err, ErrTampered) {
				t.Fatalf("rollback to an unsigned approval: %v", err)
			}
			if err := store.Update(ctx, old); err != nil {
				t.Fatal(err)
			}
			if _, err := m.Rollback(ctx, r.ID, "bob", "test"); err != nil {
				t.Fatalf("rollback: %v", err)
			}
		})
	}
}
