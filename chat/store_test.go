package chat

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	_ "modernc.org/sqlite"
)

type recorder struct {
	mu     sync.Mutex
	events []string
}

func (r *recorder) Notify(_ context.Context, topic, event string, _ json.RawMessage, _ string) error {
	r.mu.Lock()
	r.events = append(r.events, topic+" "+event)
	r.mu.Unlock()
	return nil
}

func (r *recorder) count(s string) (n int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.events {
		if e == s {
			n++
		}
	}
	return
}

func newStore(t *testing.T) (*Store, *recorder) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "chat.db")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	rec := &recorder{}
	s, err := Open(context.Background(), db, Options{Dialect: "sqlite", Notifier: rec})
	if err != nil {
		t.Fatal(err)
	}
	return s, rec
}

var ctx = context.Background()

func TestDMIsSymmetricAndIdempotent(t *testing.T) {
	s, _ := newStore(t)
	a, b := Actor{ID: "a"}, Actor{ID: "b"}
	c1, err := s.OpenDM(ctx, a, "b")
	if err != nil {
		t.Fatal(err)
	}
	c2, err := s.OpenDM(ctx, b, "a")
	if err != nil {
		t.Fatal(err)
	}
	if c1.ID != c2.ID || c1.ID != DMID("a", "b") {
		t.Fatalf("dm ids differ: %s %s", c1.ID, c2.ID)
	}
	if _, err := s.OpenDM(ctx, a, "a"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("self dm: %v", err)
	}
	// An outsider cannot read or post into the DM.
	if _, err := s.Send(ctx, Actor{ID: "eve"}, c1.ID, "hi", nil); !errors.Is(err, ErrForbidden) {
		t.Fatalf("outsider send: %v", err)
	}
}

func TestSendHistoryAndSeq(t *testing.T) {
	s, rec := newStore(t)
	owner := Actor{ID: "o"}
	ch, err := s.CreateChannel(ctx, owner, "general", []string{"m"})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.Send(ctx, owner, ch.ID, "hello", nil); err != nil {
				t.Errorf("send: %v", err)
			}
		}()
	}
	wg.Wait()
	all, err := s.History(ctx, Actor{ID: "m"}, ch.ID, 0, 100)
	if err != nil || len(all) != 20 {
		t.Fatalf("history: %d %v", len(all), err)
	}
	for i, m := range all {
		if m.Seq != int64(i+1) {
			t.Fatalf("seq gap at %d: %d", i, m.Seq)
		}
	}
	page, _ := s.History(ctx, Actor{ID: "m"}, ch.ID, 11, 5)
	if len(page) != 5 || page[0].Seq != 6 || page[4].Seq != 10 {
		t.Fatalf("page %+v", page)
	}
	since, _ := s.Since(ctx, Actor{ID: "m"}, ch.ID, 18, 0)
	if len(since) != 2 {
		t.Fatalf("since: %d", len(since))
	}
	if rec.count("chat:"+ch.ID+" message") != 20 {
		t.Fatalf("announcements: %v", rec.events)
	}
	if _, err := s.Send(ctx, owner, ch.ID, "  ", nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("blank body: %v", err)
	}
}

func TestUnreadAndReceipts(t *testing.T) {
	s, rec := newStore(t)
	a, b := Actor{ID: "a"}, Actor{ID: "b"}
	ch, _ := s.OpenDM(ctx, a, "b")
	for i := 0; i < 3; i++ {
		_, _ = s.Send(ctx, a, ch.ID, "x", nil)
	}
	list, _ := s.Channels(ctx, b)
	if len(list) != 1 || list[0].Unread != 3 {
		t.Fatalf("b unread: %+v", list)
	}
	if list, _ = s.Channels(ctx, a); list[0].Unread != 0 {
		t.Fatalf("sender unread: %+v", list)
	}
	if err := s.MarkRead(ctx, b, ch.ID, 2); err != nil {
		t.Fatal(err)
	}
	if list, _ = s.Channels(ctx, b); list[0].Unread != 1 {
		t.Fatalf("after read 2: %+v", list)
	}
	_ = s.MarkRead(ctx, b, ch.ID, 1) // never backwards
	r, _ := s.Receipts(ctx, a, ch.ID)
	if r["b"] != 2 {
		t.Fatalf("receipts %v", r)
	}
	if rec.count("chat:"+ch.ID+" receipt") != 1 {
		t.Fatalf("receipt events: %v", rec.events)
	}
	_ = s.MarkRead(ctx, b, ch.ID, 0) // everything, including a seq not yet written
	if list, _ = s.Channels(ctx, b); list[0].Unread != 0 {
		t.Fatalf("after read all: %+v", list)
	}
}

func TestMembershipRules(t *testing.T) {
	s, _ := newStore(t)
	owner, m, out := Actor{ID: "o"}, Actor{ID: "m"}, Actor{ID: "x"}
	ch, _ := s.CreateChannel(ctx, owner, "team", []string{"m"})
	if err := s.AddMember(ctx, m, ch.ID, "x"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("member adding: %v", err)
	}
	if err := s.AddMember(ctx, owner, ch.ID, "x"); err != nil {
		t.Fatal(err)
	}
	if !s.IsMember(ctx, out, ch.ID) {
		t.Fatal("x should be a member")
	}
	if err := s.RemoveMember(ctx, m, ch.ID, "x"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("member removing another: %v", err)
	}
	if err := s.RemoveMember(ctx, out, ch.ID, "x"); err != nil { // leaving is allowed
		t.Fatal(err)
	}
	if s.IsMember(ctx, out, ch.ID) || s.IsMember(ctx, out, "no-such-channel") {
		t.Fatal("membership leaked")
	}
	if err := s.RemoveMember(ctx, owner, ch.ID, "o"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("owner abandoning channel: %v", err)
	}
}

func TestTenantIsolation(t *testing.T) {
	s, _ := newStore(t)
	acme := Actor{ID: "a", Tenant: "acme"}
	ch, _ := s.CreateChannel(ctx, acme, "ops", nil)
	// Same user id, different tenant: not a member, even though the row exists.
	if _, err := s.Send(ctx, Actor{ID: "a", Tenant: "globex"}, ch.ID, "hi", nil); !errors.Is(err, ErrForbidden) {
		t.Fatalf("cross-tenant send: %v", err)
	}
	if list, _ := s.Channels(ctx, Actor{ID: "a", Tenant: "globex"}); len(list) != 0 {
		t.Fatalf("cross-tenant list: %+v", list)
	}
}
