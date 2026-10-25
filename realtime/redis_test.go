package realtime

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// Two hubs that share only a Redis server behave as one: messages and presence
// cross, and a hub that is closed stops receiving.
func TestRedisBackplaneTwoNodes(t *testing.T) {
	srv := miniredis.RunT(t)
	newNode := func(id string) *Hub {
		client := redis.NewClient(&redis.Options{Addr: srv.Addr()})
		t.Cleanup(func() { _ = client.Close() })
		return newHub(t, Options{NodeID: id, Backplane: NewRedisPlane(client, "test"), PresenceTTL: 300 * time.Millisecond})
	}
	h1, h2 := newNode("n1"), newNode("n2")
	a, _ := h1.Connect(Subject{ID: "a"})
	b, _ := h2.Connect(Subject{ID: "b"})
	_ = h1.Subscribe(a, "room", true, nil)
	_ = h2.Subscribe(b, "room", true, nil)

	if _, err := h2.Publish(context.Background(), Subject{ID: "b"}, "room", "hi", nil); err != nil {
		t.Fatal(err)
	}
	for {
		if m := next(t, a); m["type"] == "message" {
			if m["from"] != "b" {
				t.Fatalf("got %v", m)
			}
			break
		}
	}
	waitFor(t, func() bool { return h1.Online("room", "b") && h2.Online("room", "a") })

	// A message is delivered once per node, not echoed back to its sender's node.
	if got := h2.Stats().Delivered; got != 1 {
		t.Fatalf("n2 delivered %d messages, want 1", got)
	}

	// SendToUser reaches a user connected to a different node.
	if _, err := h1.SendToUser(context.Background(), "b", "ping", nil, ""); err != nil {
		t.Fatal(err)
	}
	for {
		if m := next(t, b); m["event"] == "ping" {
			break
		}
	}
}
