package realtime

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"
)

func newHub(t *testing.T, o Options) *Hub {
	t.Helper()
	h, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.Close() })
	return h
}

func next(t *testing.T, c *Client) map[string]any {
	t.Helper()
	select {
	case f, ok := <-c.Out():
		if !ok {
			t.Fatal("client closed")
		}
		var m map[string]any
		if err := json.Unmarshal(f, &m); err != nil {
			t.Fatal(err)
		}
		return m
	case <-time.After(2 * time.Second):
		t.Fatal("no frame within 2s")
	}
	return nil
}

func quiet(t *testing.T, c *Client) {
	t.Helper()
	select {
	case f := <-c.Out():
		t.Fatalf("unexpected frame %s", f)
	case <-time.After(60 * time.Millisecond):
	}
}

func TestPublishReachesSubscribersOnly(t *testing.T) {
	h := newHub(t, Options{})
	a, _ := h.Connect(Subject{ID: "a"})
	b, _ := h.Connect(Subject{ID: "b"})
	if err := h.Subscribe(a, "room:1", false, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Publish(context.Background(), Subject{ID: "b"}, "room:1", "chat", json.RawMessage(`{"t":"hi"}`)); err != nil {
		t.Fatal(err)
	}
	m := next(t, a)
	if m["type"] != "message" || m["event"] != "chat" || m["from"] != "b" {
		t.Fatalf("got %v", m)
	}
	quiet(t, b)
}

func TestAuthorization(t *testing.T) {
	h := newHub(t, Options{})
	anon, _ := h.Connect(Subject{})
	if err := h.Subscribe(anon, "room:1", false, nil); err != ErrForbidden {
		t.Fatalf("anonymous subscribe: %v", err)
	}
	a, _ := h.Connect(Subject{ID: "a"})
	if err := h.Subscribe(a, "user:b", false, nil); err != ErrForbidden {
		t.Fatalf("another user's topic: %v", err)
	}
	if _, err := h.Publish(context.Background(), Subject{ID: "a"}, "user:b", "x", nil); err != ErrForbidden {
		t.Fatalf("publish to another user's topic: %v", err)
	}
	if err := h.Subscribe(a, "bad topic", false, nil); err != ErrBadTopic {
		t.Fatalf("topic with space: %v", err)
	}
}

func TestSendToUserReachesEveryDevice(t *testing.T) {
	h := newHub(t, Options{})
	d1, _ := h.Connect(Subject{ID: "u"})
	d2, _ := h.Connect(Subject{ID: "u"})
	other, _ := h.Connect(Subject{ID: "v"})
	if _, err := h.SendToUser(context.Background(), "u", "ping", nil, "system"); err != nil {
		t.Fatal(err)
	}
	next(t, d1)
	next(t, d2)
	quiet(t, other)
}

func TestSlowClientIsDroppedNotBlocking(t *testing.T) {
	var dropped atomic.Int32
	h := newHub(t, Options{SendBuffer: 2, OnDrop: func(*Client) { dropped.Add(1) }})
	slow, _ := h.Connect(Subject{ID: "slow"})
	fast, _ := h.Connect(Subject{ID: "fast"})
	_ = h.Subscribe(slow, "t", false, nil)
	_ = h.Subscribe(fast, "t", false, nil)
	for i := 0; i < 10; i++ {
		_, _ = h.Emit(context.Background(), "t", "e", nil, "")
		for len(fast.Out()) > 0 {
			<-fast.Out()
		}
	}
	select {
	case <-slow.Done():
	case <-time.After(time.Second):
		t.Fatal("slow client was not dropped")
	}
	if dropped.Load() == 0 || h.Stats().Dropped == 0 {
		t.Fatal("drop not counted")
	}
	select {
	case <-fast.Done():
		t.Fatal("fast client must survive")
	default:
	}
}

func TestHistoryAndResume(t *testing.T) {
	h := newHub(t, Options{History: 3})
	var ids []string
	for i := 0; i < 5; i++ {
		m, _ := h.Emit(context.Background(), "t", "e", nil, "")
		ids = append(ids, m.ID)
	}
	got, ok := h.History("t", 0, ids[3])
	if !ok || len(got) != 1 || got[0].ID != ids[4] {
		t.Fatalf("resume after %s: ok=%v %v", ids[3], ok, got)
	}
	got, ok = h.History("t", 0, ids[0]) // aged out of a 3-deep ring
	if ok || len(got) != 3 {
		t.Fatalf("aged-out marker: ok=%v len=%d", ok, len(got))
	}
}

func TestPresenceAndFrames(t *testing.T) {
	h := newHub(t, Options{})
	a, _ := h.Connect(Subject{ID: "a"})
	b, _ := h.Connect(Subject{ID: "b"})
	_ = h.Subscribe(a, "room", true, map[string]any{"name": "Ada"})
	_ = h.Subscribe(b, "room", false, nil) // watches without being listed
	if p := h.Presence("room"); len(p) != 1 || p[0].User != "a" || p[0].Meta["name"] != "Ada" {
		t.Fatalf("presence %v", p)
	}
	a.Close()
	for {
		m := next(t, b)
		if m["type"] == "presence" && m["action"] == "leave" {
			break
		}
	}
	if len(h.Presence("room")) != 0 {
		t.Fatal("presence not cleared")
	}
}

func TestSecondDeviceDoesNotRepeatJoin(t *testing.T) {
	h := newHub(t, Options{})
	watcher, _ := h.Connect(Subject{ID: "w"})
	_ = h.Subscribe(watcher, "room", false, nil)
	d1, _ := h.Connect(Subject{ID: "u"})
	d2, _ := h.Connect(Subject{ID: "u"})
	_ = h.Subscribe(d1, "room", true, nil)
	next(t, watcher) // join
	_ = h.Subscribe(d2, "room", true, nil)
	quiet(t, watcher)
	d1.Close()
	quiet(t, watcher) // still present on d2
	d2.Close()
	if m := next(t, watcher); m["action"] != "leave" {
		t.Fatalf("got %v", m)
	}
}

func TestMultiNodeFanOutAndPresence(t *testing.T) {
	bus := NewMemoryBus()
	h1 := newHub(t, Options{NodeID: "n1", Backplane: bus.Plane(), PresenceTTL: 300 * time.Millisecond})
	h2 := newHub(t, Options{NodeID: "n2", Backplane: bus.Plane(), PresenceTTL: 300 * time.Millisecond})
	a, _ := h1.Connect(Subject{ID: "a"})
	b, _ := h2.Connect(Subject{ID: "b"})
	_ = h1.Subscribe(a, "room", true, nil)
	_ = h2.Subscribe(b, "room", true, nil)

	if _, err := h1.Publish(context.Background(), Subject{ID: "a"}, "room", "hi", nil); err != nil {
		t.Fatal(err)
	}
	for _, c := range []*Client{a, b} {
		for {
			if m := next(t, c); m["type"] == "message" {
				break
			}
		}
	}
	waitFor(t, func() bool { return len(h1.Presence("room")) == 2 && len(h2.Presence("room")) == 2 })
	if !h2.Online("room", "a") {
		t.Fatal("n2 does not see a online")
	}

	// A user on another node leaving is reflected here.
	a.Close()
	waitFor(t, func() bool { return !h2.Online("room", "a") })

	// A node that vanishes without saying goodbye expires.
	_ = h1.Close()
	waitFor(t, func() bool { return len(h2.Presence("room")) == 1 })
}

func TestHandleFrame(t *testing.T) {
	h := newHub(t, Options{Call: func(_ context.Context, c *Client, name string, p json.RawMessage) (any, error) {
		return map[string]any{"name": name, "user": c.Subject.ID}, nil
	}})
	c, _ := h.Connect(Subject{ID: "a"})
	do := func(s string) map[string]any {
		var m map[string]any
		_ = json.Unmarshal(h.HandleFrame(context.Background(), c, []byte(s)), &m)
		return m
	}
	if m := do(`{"id":"1","op":"subscribe","topic":"room","track":true}`); m["ok"] != true || m["id"] != "1" {
		t.Fatalf("subscribe: %v", m)
	}
	if m := do(`{"op":"publish","topic":"room","event":"e"}`); m["ok"] != true {
		t.Fatalf("publish: %v", m)
	}
	if m := do(`{"op":"subscribe","topic":"user:zed"}`); m["code"] != "forbidden" {
		t.Fatalf("forbidden: %v", m)
	}
	if m := do(`{"op":"call","name":"x"}`); m["ok"] != true {
		t.Fatalf("call: %v", m)
	}
	if m := do(`{"op":"nope"}`); m["code"] != "bad_request" {
		t.Fatalf("unknown op: %v", m)
	}
	if m := do(`not json`); m["code"] != "bad_request" {
		t.Fatalf("garbage: %v", m)
	}
	if m := do(`{"op":"ping"}`); m["type"] != "pong" {
		t.Fatalf("ping: %v", m)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not reached within 3s")
}
