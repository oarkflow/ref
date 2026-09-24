package effect

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// EffectStore is the dedicated persistence interface for durable effect delivery.
type EffectStore interface {
	// Begin starts a new effect transaction.
	Begin(ctx context.Context, executionID string) (txID string, err error)

	// Record persists a pending effect within a transaction.
	Record(ctx context.Context, txID string, record EffectRecord) error

	// Commit finalizes all LocalTransactional effects atomically.
	Commit(ctx context.Context, txID string) error

	// ScheduleDelivery enqueues DurableDelivery effects for retry.
	ScheduleDelivery(ctx context.Context, txID string) error

	// Recover re-processes incomplete transactions after a crash.
	Recover(ctx context.Context) ([]PendingTransaction, error)

	// Close shuts down the store.
	Close() error
}

type memTx struct {
	executionID string
	effects     []EffectRecord
	deliveries  map[string]*memDelivery
	committed   bool
	scheduled   bool
}

type memDelivery struct {
	record    EffectRecord
	claim     string
	leaseTill time.Time
}

// MemoryEffectStore is an in-memory implementation of EffectStore for testing and single-node setups.
type MemoryEffectStore struct {
	mu              sync.RWMutex
	counter         atomic.Uint64
	deliveryCounter atomic.Uint64
	txs             map[string]*memTx
	maxTxs          int
}

// NewMemoryEffectStore creates a new in-memory effect store.
func NewMemoryEffectStore() *MemoryEffectStore {
	return &MemoryEffectStore{
		txs:    make(map[string]*memTx),
		maxTxs: 10000,
	}
}

// cleanup removes old committed transactions to prevent unbounded growth.
func (s *MemoryEffectStore) cleanup() {
	if len(s.txs) <= s.maxTxs {
		return
	}
	for id, tx := range s.txs {
		if !tx.committed || !s.allDeliveriesTerminal(tx) {
			continue
		}
		delete(s.txs, id)
		if len(s.txs) <= s.maxTxs {
			return
		}
	}
}

func (s *MemoryEffectStore) allDeliveriesTerminal(tx *memTx) bool {
	for _, delivery := range tx.deliveries {
		if delivery.record.State != "delivered" && delivery.record.State != "dead_letter" {
			return false
		}
	}
	return true
}

func (s *MemoryEffectStore) Begin(ctx context.Context, executionID string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Cleanup old transactions to prevent unbounded growth
	s.cleanup()

	id := fmt.Sprintf("tx-%d", s.counter.Add(1))
	s.txs[id] = &memTx{
		executionID: executionID,
		deliveries:  make(map[string]*memDelivery),
	}
	return id, nil
}

func (s *MemoryEffectStore) Transaction(ctx context.Context, txID string) (any, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.txs[txID]; !ok {
		return nil, fmt.Errorf("ref: effect tx %q not found", txID)
	}
	return txID, nil
}

func (s *MemoryEffectStore) Record(ctx context.Context, txID string, record EffectRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, ok := s.txs[txID]
	if !ok {
		return fmt.Errorf("ref: effect tx %q not found", txID)
	}
	if tx.committed {
		return fmt.Errorf("ref: effect tx %q is committed", txID)
	}
	if record.ID == "" {
		record.ID = fmt.Sprintf("delivery-%d", s.deliveryCounter.Add(1))
	}
	if record.State == "" {
		record.State = "pending"
	}
	record.TxID = txID
	record.ExecutionID = tx.executionID
	tx.effects = append(tx.effects, record)
	if record.Kind == DurableDelivery {
		tx.deliveries[record.ID] = &memDelivery{record: record}
	}
	return nil
}

func (s *MemoryEffectStore) Commit(ctx context.Context, txID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, ok := s.txs[txID]
	if !ok {
		return fmt.Errorf("ref: effect tx %q not found", txID)
	}
	if tx.committed {
		return fmt.Errorf("ref: effect tx %q is already committed", txID)
	}
	tx.committed = true
	return nil
}

func (s *MemoryEffectStore) Abort(ctx context.Context, txID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.txs[txID]; !ok {
		return fmt.Errorf("ref: effect tx %q not found", txID)
	}
	delete(s.txs, txID)
	return nil
}

func (s *MemoryEffectStore) ScheduleDelivery(ctx context.Context, txID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, ok := s.txs[txID]
	if !ok {
		return fmt.Errorf("ref: effect tx %q not found", txID)
	}
	if !tx.committed {
		return fmt.Errorf("ref: effect tx %q is not committed", txID)
	}
	tx.scheduled = true
	return nil
}

func (s *MemoryEffectStore) Recover(ctx context.Context) ([]PendingTransaction, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var pending []PendingTransaction
	for id, tx := range s.txs {
		if tx.committed && !tx.scheduled {
			pending = append(pending, PendingTransaction{
				TxID:        id,
				ExecutionID: tx.executionID,
				Effects:     append([]EffectRecord(nil), tx.effects...),
			})
		}
	}
	return pending, nil
}

func (s *MemoryEffectStore) ClaimDeliveries(ctx context.Context, owner string, limit int, lease time.Duration) ([]EffectDelivery, error) {
	if owner == "" {
		return nil, fmt.Errorf("ref: delivery owner is required")
	}
	if limit <= 0 {
		limit = 32
	}
	if lease <= 0 {
		lease = 30 * time.Second
	}
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	claimed := make([]EffectDelivery, 0, limit)
	for _, tx := range s.txs {
		if !tx.committed {
			continue
		}
		for id, delivery := range tx.deliveries {
			if len(claimed) >= limit {
				break
			}
			if delivery.record.State != "pending" && !(delivery.record.State == "inflight" && !delivery.leaseTill.IsZero() && !delivery.leaseTill.After(now)) {
				continue
			}
			if delivery.record.NextAttempt.After(now) {
				continue
			}
			if delivery.record.State == "inflight" && delivery.leaseTill.After(now) {
				continue
			}
			delivery.claim = fmt.Sprintf("%s-%d", owner, s.deliveryCounter.Add(1))
			delivery.leaseTill = now.Add(lease)
			delivery.record.Attempts++
			delivery.record.State = "inflight"
			for i := range tx.effects {
				if tx.effects[i].ID == id {
					tx.effects[i] = delivery.record
				}
			}
			claimed = append(claimed, EffectDelivery{Record: delivery.record, ClaimToken: delivery.claim, LeaseUntil: delivery.leaseTill})
		}
	}
	return claimed, nil
}

func (s *MemoryEffectStore) findDelivery(id, claim string) (*memTx, *memDelivery, error) {
	for _, tx := range s.txs {
		if delivery, ok := tx.deliveries[id]; ok {
			if claim == "" || delivery.claim != claim {
				return nil, nil, fmt.Errorf("ref: effect delivery %q claim mismatch", id)
			}
			return tx, delivery, nil
		}
	}
	return nil, nil, fmt.Errorf("ref: effect delivery %q not found", id)
}

func (s *MemoryEffectStore) AckDelivery(ctx context.Context, id, claimToken string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, delivery, err := s.findDelivery(id, claimToken)
	if err != nil {
		return err
	}
	delivery.record.State = "delivered"
	delivery.claim = ""
	delivery.leaseTill = time.Time{}
	for i := range tx.effects {
		if tx.effects[i].ID == id {
			tx.effects[i] = delivery.record
		}
	}
	return nil
}

func (s *MemoryEffectStore) RetryDelivery(ctx context.Context, id, claimToken string, nextAttempt time.Time, cause error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, delivery, err := s.findDelivery(id, claimToken)
	if err != nil {
		return err
	}
	delivery.record.State = "pending"
	delivery.record.NextAttempt = nextAttempt
	if cause != nil {
		delivery.record.LastError = cause.Error()
	}
	delivery.claim = ""
	delivery.leaseTill = time.Time{}
	for i := range tx.effects {
		if tx.effects[i].ID == id {
			tx.effects[i] = delivery.record
		}
	}
	return nil
}

func (s *MemoryEffectStore) DeadLetterDelivery(ctx context.Context, id, claimToken, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, delivery, err := s.findDelivery(id, claimToken)
	if err != nil {
		return err
	}
	delivery.record.State = "dead_letter"
	delivery.record.LastError = reason
	delivery.claim = ""
	delivery.leaseTill = time.Time{}
	for i := range tx.effects {
		if tx.effects[i].ID == id {
			tx.effects[i] = delivery.record
		}
	}
	return nil
}

func (s *MemoryEffectStore) Close() error {
	return nil
}
