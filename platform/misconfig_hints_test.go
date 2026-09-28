package platform

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// These tests pin the misconfiguration-hint behavior added alongside the BCL
// starter scaffold: a signer that would silently reset in production, a
// bulkhead that never actually limits anything, and a process-local resource
// used where two replicas need to share state are all now surfaced — as a
// hard compile error for the first two, a warning for the third — instead of
// only being documented in a comment.

const signerApp = `
name "signer-app"
environment %q
resource "keys" {
  kind "crypto.signer"
  config { ephemeral true }
}
`

func TestSignerRefusesEphemeralInProduction(t *testing.T) {
	src := []byte(fmt.Sprintf(signerApp, "production"))
	if _, err := Compile(context.Background(), src, "", DefaultLoadOptions()); err == nil {
		t.Fatal("expected ephemeral true to be refused in production, got nil error")
	} else if !strings.Contains(err.Error(), "ephemeral") {
		t.Fatalf("error should name the offending field: %v", err)
	}
}

func TestSignerAllowsEphemeralOutsideProduction(t *testing.T) {
	src := []byte(fmt.Sprintf(signerApp, "development"))
	p, err := Compile(context.Background(), src, "", DefaultLoadOptions())
	if err != nil {
		t.Fatalf("ephemeral true must still work outside production: %v", err)
	}
	_ = p.Close()
}

func TestBulkheadRejectsNonPositiveLimit(t *testing.T) {
	doc := Document{
		Name: "bh",
		Intents: []IntentSpec{{
			Name:     "do",
			Response: "out",
			Nodes: []NodeSpec{
				{Name: "out", Uses: "collect", Requires: []string{"input"}, Provides: []string{"out"},
					Bulkhead: &BulkheadSpec{Name: "shared", Limit: 0}},
			},
		}},
	}
	registry := NewRegistry()
	if err := validateDocument(doc, registry); err == nil {
		t.Fatal("expected a non-positive bulkhead limit to be rejected")
	} else if !strings.Contains(err.Error(), "bulkhead") {
		t.Fatalf("error should name the bulkhead field: %v", err)
	}
}

func TestBulkheadLimiterCapsConcurrency(t *testing.T) {
	limiter := newBulkheadLimiter("shared", 2)
	if !limiter.tryAcquire() {
		t.Fatal("first acquire should succeed")
	}
	if !limiter.tryAcquire() {
		t.Fatal("second acquire should succeed (limit is 2)")
	}
	if limiter.tryAcquire() {
		t.Fatal("third acquire must fail fast once the limiter is full")
	}
	limiter.release()
	if !limiter.tryAcquire() {
		t.Fatal("acquire should succeed again after a release")
	}
}

func TestCompileBulkheadSharesLimiterByName(t *testing.T) {
	p := &Platform{bulkheads: map[string]*bulkheadLimiter{}}
	a, err := p.compileBulkhead("node a", "intent1", NodeSpec{Name: "a", Bulkhead: &BulkheadSpec{Name: "shared", Limit: 3}})
	if err != nil {
		t.Fatal(err)
	}
	b, err := p.compileBulkhead("node b", "intent2", NodeSpec{Name: "b", Bulkhead: &BulkheadSpec{Name: "shared", Limit: 99}})
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatal("two nodes naming the same bulkhead must share one limiter")
	}
	if cap(a.slot) != 3 {
		t.Fatalf("limiter capacity = %d, want the first-seen limit of 3", cap(a.slot))
	}
}

func TestSingleReplicaResourceWarnings(t *testing.T) {
	doc := Document{
		Name: "warn",
		Resources: []ResourceSpec{
			{Name: "c", Kind: "cache.memory"},
			{Name: "l", Kind: "lock.memory"},
			{Name: "db", Kind: "database.sql"},
		},
	}

	if got := singleReplicaResourceWarnings(doc, LoadOptions{}); len(got) != 0 {
		t.Fatalf("a single-node run with no replica id and no process must not warn, got %v", got)
	}

	warnings := singleReplicaResourceWarnings(doc, LoadOptions{ReplicaID: "node-1"})
	if len(warnings) != 2 {
		t.Fatalf("expected one warning per process-local resource, got %v", warnings)
	}
	for _, w := range warnings {
		if !strings.Contains(w, "process-local") {
			t.Fatalf("warning should say the resource is process-local: %q", w)
		}
	}

	withProcess := doc
	withProcess.Processes = []ProcessSpec{{Name: "p"}}
	if got := singleReplicaResourceWarnings(withProcess, LoadOptions{}); len(got) != 2 {
		t.Fatalf("a declared process should trigger the same warnings even with no replica id, got %v", got)
	}
}
