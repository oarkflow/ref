package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/oarkflow/ref/examples/smsgateway/internal/cfgdec"
)

func init() { Register("mock", openMock) }

type mockConfig struct {
	Latency        cfgdec.Duration `json:"latency"`
	Down           bool            `json:"down"`
	FailFirst      int             `json:"fail_first"`
	RejectPrefixes []string        `json:"reject_prefixes"`
	DLR            string          `json:"dlr"` // delivered (default), failed, none
	DLRDelay       cfgdec.Duration `json:"dlr_delay"`
}

// Mock is an in-process gateway: a sandbox provider for demos and the test
// double for the whole pipeline. It records everything it accepts, can be told
// to fail in each of the ways a real upstream fails, and emits delivery
// receipts on a timer.
type Mock struct {
	name string
	cfg  mockConfig

	seq  atomic.Uint64
	down atomic.Bool

	mu        sync.Mutex
	failNext  []*Error
	loseNext  int
	sent      []Message
	byID      map[string]int
	sink      DLRSink
	delivered map[string]string // provider id -> message id
}

func openMock(_ context.Context, name string, config map[string]any) (Gateway, error) {
	var cfg mockConfig
	if err := cfgdec.Decode(config, &cfg); err != nil {
		return nil, fmt.Errorf("mock gateway %q: %w", name, err)
	}
	switch cfg.DLR {
	case "", "delivered", "failed", "none":
	default:
		return nil, fmt.Errorf("mock gateway %q: dlr must be delivered, failed or none", name)
	}
	m := &Mock{name: name, cfg: cfg, byID: map[string]int{}, delivered: map[string]string{}}
	m.down.Store(cfg.Down)
	for i := 0; i < cfg.FailFirst; i++ {
		m.failNext = append(m.failNext, Errorf(ClassRetryable, "mock_fail_first", "mock gateway scripted failure"))
	}
	return m, nil
}

// SetDLRSink implements DLRSource.
func (m *Mock) SetDLRSink(s DLRSink) {
	m.mu.Lock()
	m.sink = s
	m.mu.Unlock()
}

// SetDown makes every Send fail with a retryable outage until reversed.
func (m *Mock) SetDown(v bool) { m.down.Store(v) }

// FailNext scripts the next n sends to fail with err.
func (m *Mock) FailNext(n int, err *Error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := 0; i < n; i++ {
		m.failNext = append(m.failNext, err)
	}
}

// LoseResponseNext makes the next n sends deliver upstream but report a
// timeout, the one case that produces a duplicate under at-least-once delivery.
func (m *Mock) LoseResponseNext(n int) {
	m.mu.Lock()
	m.loseNext += n
	m.mu.Unlock()
}

// Sent returns every message the upstream accepted, in order.
func (m *Mock) Sent() []Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Message(nil), m.sent...)
}

// Count reports how many times the upstream accepted message id.
func (m *Mock) Count(id string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.byID[id]
}

// Send implements Gateway.
func (m *Mock) Send(ctx context.Context, msg Message) (Receipt, error) {
	start := time.Now()
	if d := m.cfg.Latency.D(); d > 0 {
		select {
		case <-time.After(d):
		case <-ctx.Done():
			return Receipt{}, ctx.Err()
		}
	}
	if m.down.Load() {
		return Receipt{}, Errorf(ClassRetryable, "mock_down", "mock gateway %s is down", m.name)
	}
	for _, p := range m.cfg.RejectPrefixes {
		if strings.HasPrefix(msg.To, p) {
			return Receipt{}, Errorf(ClassPermanent, "invalid_destination", "mock gateway rejects destination %s", msg.To)
		}
	}
	m.mu.Lock()
	if len(m.failNext) > 0 {
		err := m.failNext[0]
		m.failNext = m.failNext[1:]
		m.mu.Unlock()
		return Receipt{}, err
	}
	lose := m.loseNext > 0
	if lose {
		m.loseNext--
	}
	pid := fmt.Sprintf("%s-%06d", m.name, m.seq.Add(1))
	m.sent = append(m.sent, msg)
	m.byID[msg.ID]++
	m.delivered[pid] = msg.ID
	sink := m.sink
	m.mu.Unlock()

	if lose {
		return Receipt{}, Errorf(ClassRetryable, "mock_timeout", "mock gateway lost the response")
	}
	if sink != nil && m.cfg.DLR != "none" && msg.WantDLR {
		status := StatusDelivered
		if m.cfg.DLR == "failed" {
			status = StatusFailed
		}
		delay := m.cfg.DLRDelay.D()
		go func() {
			time.Sleep(delay)
			sink(context.Background(), DLR{Provider: m.name, ProviderMessageID: pid, Status: status, At: time.Now(), Raw: "mock"})
		}()
	}
	return Receipt{ProviderMessageID: pid, Latency: time.Since(start)}, nil
}

// ParseDLR implements DLRParser for {"id": "...", "status": "delivered"}.
func (m *Mock) ParseDLR(_ map[string][]string, body []byte) ([]DLR, error) {
	var in struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		return nil, err
	}
	st := StatusUnknown
	switch strings.ToLower(in.Status) {
	case "delivered":
		st = StatusDelivered
	case "failed":
		st = StatusFailed
	}
	return []DLR{{Provider: m.name, ProviderMessageID: in.ID, Status: st, At: time.Now(), Raw: string(body)}}, nil
}

// Ping implements Pinger.
func (m *Mock) Ping(context.Context) error {
	if m.down.Load() {
		return fmt.Errorf("mock gateway %s is down", m.name)
	}
	return nil
}

// Close implements Gateway.
func (m *Mock) Close() error { return nil }
