package runtime

import (
	"sync"
	"sync/atomic"
	"time"
)

// ExecutionEvent captures telemetry and audit details for a single package function invocation.
type ExecutionEvent struct {
	PackageID     string        `json:"package_id"`
	ApplicationID string        `json:"application_id"`
	BuildID       string        `json:"build_id"`
	Function      string        `json:"function"`
	SessionID     string        `json:"session_id"`
	Duration      time.Duration `json:"duration"`
	FuelUsed      int64         `json:"fuel_used"`
	Error         error         `json:"error,omitempty"`
	Timestamp     time.Time     `json:"timestamp"`
}

// MetricsCollector receives execution audit and telemetry events.
type MetricsCollector interface {
	// RecordExecution records the telemetry event of an execution.
	RecordExecution(event ExecutionEvent)
}

// Metrics is a thread-safe, atomic metrics collector for tracking execution counts, errors,
// fuel consumed, and recent audit events.
type Metrics struct {
	executionCount uint64
	errorCount     uint64
	fuelConsumed   uint64
	totalDuration  int64 // nanoseconds, updated atomically

	mutex     sync.RWMutex
	events    []ExecutionEvent
	maxEvents int
}

// NewMetrics initializes a new Metrics collector retaining up to 1000 recent events.
func NewMetrics() *Metrics {
	return NewMetricsWithLimit(1000)
}

// NewMetricsWithLimit initializes a new Metrics collector retaining up to maxEvents recent events.
func NewMetricsWithLimit(maxEvents int) *Metrics {
	if maxEvents < 0 {
		maxEvents = 0
	}
	initialCap := 64
	if initialCap > maxEvents {
		initialCap = maxEvents
	}
	return &Metrics{
		maxEvents: maxEvents,
		events:    make([]ExecutionEvent, 0, initialCap),
	}
}

// RecordExecution records execution stats atomically and stores the event in the history buffer.
func (m *Metrics) RecordExecution(event ExecutionEvent) {
	if m == nil {
		return
	}

	atomic.AddUint64(&m.executionCount, 1)
	if event.FuelUsed > 0 {
		atomic.AddUint64(&m.fuelConsumed, uint64(event.FuelUsed))
	}
	if event.Error != nil {
		atomic.AddUint64(&m.errorCount, 1)
	}
	if event.Duration > 0 {
		atomic.AddInt64(&m.totalDuration, event.Duration.Nanoseconds())
	}

	if m.maxEvents > 0 {
		m.mutex.Lock()
		if len(m.events) >= m.maxEvents {
			copy(m.events, m.events[1:])
			m.events[len(m.events)-1] = event
		} else {
			m.events = append(m.events, event)
		}
		m.mutex.Unlock()
	}
}

// ExecutionCount returns the total number of function executions observed.
func (m *Metrics) ExecutionCount() uint64 {
	if m == nil {
		return 0
	}
	return atomic.LoadUint64(&m.executionCount)
}

// ErrorCount returns the total number of execution errors observed.
func (m *Metrics) ErrorCount() uint64 {
	if m == nil {
		return 0
	}
	return atomic.LoadUint64(&m.errorCount)
}

// FuelConsumed returns the total fuel consumed across all executions.
func (m *Metrics) FuelConsumed() uint64 {
	if m == nil {
		return 0
	}
	return atomic.LoadUint64(&m.fuelConsumed)
}

// TotalDuration returns the cumulative execution duration across all executions.
func (m *Metrics) TotalDuration() time.Duration {
	if m == nil {
		return 0
	}
	return time.Duration(atomic.LoadInt64(&m.totalDuration))
}

// Events returns a copy of the recorded execution events.
func (m *Metrics) Events() []ExecutionEvent {
	if m == nil {
		return nil
	}
	m.mutex.RLock()
	defer m.mutex.RUnlock()

	result := make([]ExecutionEvent, len(m.events))
	copy(result, m.events)
	return result
}

// Reset clears all counters and recorded events.
func (m *Metrics) Reset() {
	if m == nil {
		return
	}
	atomic.StoreUint64(&m.executionCount, 0)
	atomic.StoreUint64(&m.errorCount, 0)
	atomic.StoreUint64(&m.fuelConsumed, 0)
	atomic.StoreInt64(&m.totalDuration, 0)

	m.mutex.Lock()
	m.events = m.events[:0]
	m.mutex.Unlock()
}
