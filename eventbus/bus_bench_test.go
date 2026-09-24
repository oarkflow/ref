package eventbus_test

import (
	"sync"
	"testing"
	"time"

	"github.com/oarkflow/ref/eventbus"
)

// MutexBuffer simulates standard Go locking architecture.
type MutexBuffer struct {
	mu     sync.Mutex
	buffer []eventbus.Event
}

func (m *MutexBuffer) Push(e eventbus.Event) {
	m.mu.Lock()
	m.buffer = append(m.buffer, e)
	m.mu.Unlock()
}

func (m *MutexBuffer) Pop() (eventbus.Event, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.buffer) == 0 {
		return eventbus.Event{}, false
	}
	e := m.buffer[0]
	m.buffer = m.buffer[1:]
	return e, true
}

// BenchmarkMutex architecture (Tier-1)
func BenchmarkMutexBuffer(b *testing.B) {
	mb := &MutexBuffer{buffer: make([]eventbus.Event, 0, 1048576)}
	evt := eventbus.Event{ID: "1", Aggregate: "User", Timestamp: time.Now()}

	go func() {
		for {
			mb.Pop()
		}
	}()

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			mb.Push(evt) // Contentious lock acquisition
		}
	})
}

// BenchmarkLockFree architecture (Tier-0 LMAX Disruptor)
func BenchmarkLockFreeRingBuffer(b *testing.B) {
	// Allocate a massive ring buffer
	ring := eventbus.NewRingBuffer(1048576) // Power of 2 (1 Million)
	evt := eventbus.Event{ID: "1", Aggregate: "User", Timestamp: time.Now()}

	go func() {
		for {
			ring.Pop()
		}
	}()

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			ring.Push(evt) // Atomic CAS, zero context switches
		}
	})
}
