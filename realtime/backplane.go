package realtime

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/redis/go-redis/v9"
)

// Envelope is what crosses between nodes. Hubs ignore envelopes they sent
// themselves, so a backplane may echo freely.
type Envelope struct {
	Node string `json:"node"`
	Kind string `json:"kind"` // "message", "presence"
	// Message is set for kind "message".
	Message *Message `json:"message,omitempty"`
	// Presence is set for kind "presence".
	Presence *PresenceEvent `json:"presence,omitempty"`
}

// Backplane connects the hubs of several nodes. A single-node deployment uses
// no backplane at all.
type Backplane interface {
	// Publish sends an envelope to every other node.
	Publish(ctx context.Context, env Envelope) error
	// Subscribe delivers envelopes from other nodes to fn until Close.
	Subscribe(fn func(Envelope)) error
	Close() error
}

// MemoryBus is an in-process backplane shared by several hubs. It is what a
// multi-node test uses, and a way to run several hubs in one process.
type MemoryBus struct {
	mu   sync.RWMutex
	subs map[*memoryPlane]struct{}
}

// NewMemoryBus returns an empty bus.
func NewMemoryBus() *MemoryBus { return &MemoryBus{subs: map[*memoryPlane]struct{}{}} }

// Plane returns a Backplane attached to the bus, one per hub.
func (b *MemoryBus) Plane() Backplane { return &memoryPlane{bus: b} }

type memoryPlane struct {
	bus *MemoryBus
	fn  func(Envelope)
}

func (p *memoryPlane) Publish(_ context.Context, env Envelope) error {
	p.bus.mu.RLock()
	defer p.bus.mu.RUnlock()
	for other := range p.bus.subs {
		if other != p && other.fn != nil {
			other.fn(env)
		}
	}
	return nil
}

func (p *memoryPlane) Subscribe(fn func(Envelope)) error {
	p.bus.mu.Lock()
	p.fn = fn
	p.bus.subs[p] = struct{}{}
	p.bus.mu.Unlock()
	return nil
}

func (p *memoryPlane) Close() error {
	p.bus.mu.Lock()
	delete(p.bus.subs, p)
	p.bus.mu.Unlock()
	return nil
}

// RedisPlane is a Backplane over one Redis pub/sub channel.
type RedisPlane struct {
	client  redis.UniversalClient
	channel string
	cancel  context.CancelFunc
	sub     *redis.PubSub
}

// NewRedisPlane publishes and subscribes on channel using client. The caller
// keeps ownership of client.
func NewRedisPlane(client redis.UniversalClient, channel string) *RedisPlane {
	if channel == "" {
		channel = "ref:realtime"
	}
	return &RedisPlane{client: client, channel: channel}
}

func (p *RedisPlane) Publish(ctx context.Context, env Envelope) error {
	raw, err := json.Marshal(env)
	if err != nil {
		return err
	}
	return p.client.Publish(ctx, p.channel, raw).Err()
}

func (p *RedisPlane) Subscribe(fn func(Envelope)) error {
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	p.sub = p.client.Subscribe(ctx, p.channel)
	// Wait for the subscription to be live so nothing published right after
	// Subscribe returns is lost.
	if _, err := p.sub.Receive(ctx); err != nil {
		cancel()
		return err
	}
	ch := p.sub.Channel()
	go func() {
		for m := range ch {
			var env Envelope
			if json.Unmarshal([]byte(m.Payload), &env) == nil {
				fn(env)
			}
		}
	}()
	return nil
}

func (p *RedisPlane) Close() error {
	if p.cancel != nil {
		p.cancel()
	}
	if p.sub != nil {
		return p.sub.Close()
	}
	return nil
}
