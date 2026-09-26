package pipeline

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

// storeConformance runs every Store and Outbox contract against a store.
func storeConformance(t *testing.T, s interface {
	Store
	Outbox
}) {
	ctx := context.Background()
	at := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	mk := func(id, stage, status, assignee, unit string) *Case {
		c := &Case{ID: id, Number: "N-" + id, Pipeline: "p", Status: status, Stage: stage, TenantID: "t1", OrgUnit: unit,
			CreatedBy: "u-" + id, Data: map[string]any{"a": map[string]any{"b": id}},
			Stages: map[string]*StageState{stage: {Status: StageActive, Assignee: assignee}}, CreatedAt: at, UpdatedAt: at}
		at = at.Add(time.Minute)
		return c
	}
	c1 := mk("c1", "review", CaseInProgress, "ana", "ktm")
	c1.emit("stage.entered", "review", "", at, map[string]any{"k": "v"})
	c1.emit("assigned", "review", "", at, nil)
	if err := s.Create(ctx, c1); err != nil {
		t.Fatal(err)
	}
	for _, c := range []*Case{mk("c2", "review", CaseInProgress, "", "ktm"), mk("c3", "done", CaseCompleted, "", "ltp")} {
		if err := s.Create(ctx, c); err != nil {
			t.Fatal(err)
		}
	}

	// Get / Update with optimistic concurrency.
	got, err := s.Get(ctx, "c1")
	if err != nil || got.Revision != 1 || got.Data["a"].(map[string]any)["b"] != "c1" {
		t.Fatalf("get: %v %+v", err, got)
	}
	stale := got.Clone()
	got.Status = CaseReturned
	got.emit("case.returned", "review", "x", at, nil)
	if err := s.Update(ctx, got); err != nil || got.Revision != 2 {
		t.Fatalf("update: %v rev=%d", err, got.Revision)
	}
	stale.emit("case.rejected", "review", "x", at, nil)
	if err := s.Update(ctx, stale); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale update: %v", err)
	}
	if _, err := s.Get(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing get: %v", err)
	}

	// List filters and paging.
	for name, tc := range map[string]struct {
		q    Query
		want int
	}{
		"stage":      {Query{Pipeline: "p", Stages: []string{"review"}}, 2},
		"status":     {Query{Statuses: []string{CaseCompleted}}, 1},
		"assignee":   {Query{Assignees: []string{"ana"}}, 1},
		"unassigned": {Query{Assignees: []string{""}, Statuses: []string{CaseInProgress}}, 1},
		"org":        {Query{OrgUnits: []string{"ltp"}}, 1},
		"creator":    {Query{CreatedBy: "u-c2"}, 1},
		"page":       {Query{Pipeline: "p", Limit: 2, Offset: 2}, 1},
		"tenant":     {Query{TenantID: "other"}, 0},
	} {
		cases, err := s.List(ctx, tc.q)
		if err != nil || len(cases) != tc.want {
			t.Errorf("list %s: %d cases, err %v", name, len(cases), err)
		}
	}

	// Triage: priority order (1 first, untriaged last), queue filter.
	c2, err := s.Get(ctx, "c2")
	if err != nil {
		t.Fatal(err)
	}
	c2.Triage = &Triage{Stage: "review", Priority: 2, Queue: "std"}
	got.Triage = &Triage{Stage: "review", Priority: 1, Queue: "urgent"}
	for _, c := range []*Case{c2, got} {
		if err := s.Update(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	ordered, err := s.List(ctx, Query{Pipeline: "p", Order: OrderPriority})
	if err != nil || len(ordered) != 3 || ordered[0].ID != "c1" || ordered[1].ID != "c2" || ordered[2].ID != "c3" {
		t.Fatalf("priority order: %v %v", err, ordered)
	}
	if std, _ := s.List(ctx, Query{Queues: []string{"std"}}); len(std) != 1 || std[0].ID != "c2" {
		t.Fatalf("queue filter: %v", std)
	}

	// Sequences and certificates.
	for want := int64(1); want <= 3; want++ {
		if n, err := s.NextSeq(ctx, "p"); err != nil || n != want {
			t.Fatalf("seq: %d %v", n, err)
		}
	}
	got.Certificates = []Certificate{{ID: "cert1", Number: "PPA-1", Code: "AB-CD", CaseID: "c1", Name: "x", Hash: "h"}}
	if err := s.Update(ctx, got); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"cert1", "PPA-1", "AB-CD"} {
		if cert, err := s.FindCertificate(ctx, key); err != nil || cert.Number != "PPA-1" {
			t.Fatalf("find certificate %s: %v", key, err)
		}
	}

	// Outbox: only committed changes' events, leased, acked, retried, dead-lettered.
	now := at.Add(time.Hour)
	claimed, err := s.ClaimEvents(ctx, 10, time.Minute, now)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, ev := range claimed {
		names[ev.Event.Name] = true
	}
	if len(claimed) != 3 || !names["stage.entered"] || !names["case.returned"] || names["case.rejected"] {
		t.Fatalf("claimed %d events: %v (the rejected update must not leave an event)", len(claimed), names)
	}
	if claimed[0].Event.Detail["k"] != "v" || claimed[0].CaseID != "c1" {
		t.Fatalf("event payload: %+v", claimed[0])
	}
	if again, _ := s.ClaimEvents(ctx, 10, time.Minute, now); len(again) != 0 {
		t.Fatalf("leased events claimed twice: %d", len(again))
	}
	if err := s.AckEvent(ctx, claimed[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := s.RetryEvent(ctx, claimed[1].ID, now.Add(time.Minute), false, "boom"); err != nil {
		t.Fatal(err)
	}
	if err := s.RetryEvent(ctx, claimed[2].ID, now, true, "gave up"); err != nil {
		t.Fatal(err)
	}
	if due, _ := s.ClaimEvents(ctx, 10, time.Minute, now.Add(30*time.Second)); len(due) != 0 {
		t.Fatalf("retry claimed before its time: %d", len(due))
	}
	due, _ := s.ClaimEvents(ctx, 10, time.Minute, now.Add(2*time.Minute))
	if len(due) != 1 || due[0].Attempts != 1 || due[0].LastError != "boom" {
		t.Fatalf("retry: %+v", due)
	}
	if err := s.AckEvent(ctx, due[0].ID); err != nil {
		t.Fatal(err)
	}
	dead, err := s.DeadEvents(ctx, 10)
	if err != nil || len(dead) != 1 || !dead[0].Dead || dead[0].LastError != "gave up" {
		t.Fatalf("dead: %v %+v", err, dead)
	}
	if err := s.RequeueEvent(ctx, dead[0].ID); err != nil {
		t.Fatal(err)
	}
	if revived, _ := s.ClaimEvents(ctx, 10, time.Minute, now.Add(3*time.Minute)); len(revived) != 1 {
		t.Fatalf("requeued event not claimable: %d", len(revived))
	}

	// Delete removes the case and its certificates.
	if err := s.Delete(ctx, "c1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FindCertificate(ctx, "PPA-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("certificate after delete: %v", err)
	}
	if err := s.Delete(ctx, "c1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("double delete: %v", err)
	}
}

func recordAll(Event) bool { return true }

func TestMemoryStoreConformance(t *testing.T) {
	s := NewMemoryStore()
	s.Record = recordAll
	storeConformance(t, s)
	notifyConformance(t, s)
}

func TestSQLiteStoreConformance(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "p.db")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s, err := NewSQLStore(db, "sqlite", "t_")
	if err != nil {
		t.Fatal(err)
	}
	s.Record = recordAll
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate twice: %v", err)
	}
	storeConformance(t, s)
	notifyConformance(t, s)
}

// TestPostgresStoreConformance runs against a real PostgreSQL when
// TEST_POSTGRES_DSN is set (e.g. postgres://postgres@127.0.0.1:5432/reftest).
func TestPostgresStoreConformance(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN not set")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	prefix := fmt.Sprintf("t%d_", time.Now().UnixNano())
	s, err := NewSQLStore(db, "postgres", prefix)
	if err != nil {
		t.Fatal(err)
	}
	s.Record = recordAll
	ctx := context.Background()
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrate twice: %v", err)
	}
	t.Cleanup(func() {
		for _, table := range []string{"cases", "certificates", "sequences", "outbox", "notify_prefs", "notifications"} {
			_, _ = db.Exec("DROP TABLE IF EXISTS " + prefix + table)
		}
	})
	storeConformance(t, s)
	notifyConformance(t, s)
}

// TestPostgresConcurrentClaims races dispatchers on one outbox: each event
// must be leased to exactly one of them (PostgreSQL re-checks only the outer
// condition of the claiming UPDATE on a row leased meanwhile).
func TestPostgresConcurrentClaims(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN not set")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	prefix := fmt.Sprintf("t%d_", time.Now().UnixNano())
	s, err := NewSQLStore(db, "postgres", prefix)
	if err != nil {
		t.Fatal(err)
	}
	s.Record = recordAll
	ctx := context.Background()
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, table := range []string{"cases", "certificates", "sequences", "outbox", "notify_prefs", "notifications"} {
			_, _ = db.Exec("DROP TABLE IF EXISTS " + prefix + table)
		}
	})
	at := time.Now().Add(-time.Minute).UTC()
	const cases, perCase = 50, 4
	for i := range cases {
		c := &Case{ID: fmt.Sprintf("c%d", i), Number: fmt.Sprintf("N-%d", i), Pipeline: "p", Status: CaseInProgress, Stage: "s",
			Stages: map[string]*StageState{"s": {Status: StageActive}}, CreatedAt: at, UpdatedAt: at}
		for range perCase {
			c.emit("stage.entered", "s", "", at, nil)
		}
		if err := s.Create(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	var (
		mu      sync.Mutex
		claimed = map[string]int{}
		wg      sync.WaitGroup
	)
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idle := 0; idle < 3; {
				events, err := s.ClaimEvents(ctx, 5, time.Minute, time.Now())
				if err != nil {
					t.Error(err)
					return
				}
				if len(events) == 0 {
					idle++
					continue
				}
				mu.Lock()
				for _, ev := range events {
					claimed[ev.ID]++
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if len(claimed) != cases*perCase {
		t.Fatalf("claimed %d distinct events, want %d", len(claimed), cases*perCase)
	}
	for id, n := range claimed {
		if n != 1 {
			t.Errorf("event %s leased %d times", id, n)
		}
	}
}
