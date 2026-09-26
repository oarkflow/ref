package eventbus

import (
	"runtime"
	"sync/atomic"
)

// RingBuffer is a lock-free, zero-allocation circular buffer based on the LMAX Disruptor pattern.
// It uses atomic Compare-And-Swap (CAS) instead of sync.Mutex to avoid CPU cache-line contention.
type RingBuffer struct {
	buffer []Event
	mask   uint64

	// Cacheline padding to prevent False Sharing (assuming 64-byte cache lines)
	_pad1 [7]uint64
	read  atomic.Uint64
	_pad2 [7]uint64
	write atomic.Uint64
	_pad3 [7]uint64
}

// NewRingBuffer allocates a fixed-size ring. Capacity MUST be a power of 2 (e.g., 1024, 4096).
func NewRingBuffer(capacity uint64) *RingBuffer {
	return &RingBuffer{
		buffer: make([]Event, capacity),
		mask:   capacity - 1,
	}
}

// Push adds an event to the ring buffer lock-free.
func (r *RingBuffer) Push(e Event) {
	for {
		w := r.write.Load()
		rd := r.read.Load()

		// If buffer is full, yield the CPU thread
		if w-rd >= r.mask {
			runtime.Gosched()
			continue
		}

		// Attempt to claim the write slot
		if r.write.CompareAndSwap(w, w+1) {
			// Write the data into the pre-allocated slot (Zero Allocation)
			r.buffer[w&r.mask] = e
			return
		}
	}
}

// Pop removes an event from the ring buffer lock-free.
func (r *RingBuffer) Pop() (Event, bool) {
	for {
		rd := r.read.Load()
		w := r.write.Load()

		// If buffer is empty
		if rd == w {
			return Event{}, false
		}

		// Read the data
		e := r.buffer[rd&r.mask]

		// Attempt to claim the read slot
		if r.read.CompareAndSwap(rd, rd+1) {
			return e, true
		}
	}
}
