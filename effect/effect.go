package effect

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// Effect is an externally observable mutation.
type Effect interface {
	Name() string
	Kind() EffectKind
	Commit(context.Context) error
}

type EffectEncoder interface {
	EncodeEffect() (payload []byte, idempotencyKey string, idempotent bool, err error)
}

type TransactionalEffect interface {
	CommitTransaction(context.Context, any) error
}

type TransactionProvider interface {
	Transaction(context.Context, string) (any, error)
}

// EffectKind classifies effects for commit and delivery strategy.
type EffectKind uint8

const (
	// LocalTransactional effects commit atomically in a single DB transaction.
	// DB writes + transactional outbox entries.
	LocalTransactional EffectKind = iota

	// DurableDelivery effects are recorded in a journal and delivered with
	// retry and idempotency. Webhooks, emails, external API calls.
	DurableDelivery

	// FireAndForget effects are best-effort. Metrics, telemetry.
	FireAndForget
)

func (k EffectKind) String() string {
	switch k {
	case LocalTransactional:
		return "local_transactional"
	case DurableDelivery:
		return "durable_delivery"
	case FireAndForget:
		return "fire_and_forget"
	default:
		return "unknown"
	}
}

// CompensatingEffect is an effect that can be compensated (forward recovery).
type CompensatingEffect interface {
	Effect
	Compensate(context.Context) error
}

// EffectPlan groups planned mutations by delivery tier.
type EffectPlan struct {
	LocalTx       []Effect
	Durable       []Effect
	FireAndForget []Effect
}

// All returns all effects in this plan in order of execution.
func (ep EffectPlan) All() []Effect {
	total := len(ep.LocalTx) + len(ep.Durable) + len(ep.FireAndForget)
	if total == 0 {
		return nil
	}
	res := make([]Effect, 0, total)
	res = append(res, ep.LocalTx...)
	res = append(res, ep.Durable...)
	res = append(res, ep.FireAndForget...)
	return res
}

func (ep EffectPlan) Validate() error {
	groups := []struct {
		effects []Effect
		kind    EffectKind
		name    string
	}{
		{ep.LocalTx, LocalTransactional, "local"},
		{ep.Durable, DurableDelivery, "durable"},
		{ep.FireAndForget, FireAndForget, "fire-and-forget"},
	}
	for _, group := range groups {
		for _, e := range group.effects {
			if e == nil {
				return errors.New("ref: effect plan contains nil effect")
			}
			if e.Kind() != group.kind {
				return fmt.Errorf("ref: %s effect %q has kind %s", group.name, e.Name(), e.Kind())
			}
		}
	}
	return nil
}

// IsEmpty reports whether the plan contains zero effects.
func (ep EffectPlan) IsEmpty() bool {
	return len(ep.LocalTx) == 0 && len(ep.Durable) == 0 && len(ep.FireAndForget) == 0
}

// EffectRecord is the durable representation of an effect in a store.
type EffectRecord struct {
	ID             string
	TxID           string
	ExecutionID    string
	Name           string
	Kind           EffectKind
	Payload        []byte
	IdempotencyKey string
	Idempotent     bool
	Attempts       int
	NextAttempt    time.Time
	LastError      string
	State          string
}

// PendingTransaction represents an incomplete transaction found during recovery.
type PendingTransaction struct {
	TxID        string
	ExecutionID string
	Effects     []EffectRecord
}

type EffectDelivery struct {
	Record     EffectRecord
	ClaimToken string
	LeaseUntil time.Time
}

type AbortableEffectStore interface {
	Abort(ctx context.Context, txID string) error
}

type DeliveryStore interface {
	ClaimDeliveries(ctx context.Context, owner string, limit int, lease time.Duration) ([]EffectDelivery, error)
	AckDelivery(ctx context.Context, id, claimToken string) error
	RetryDelivery(ctx context.Context, id, claimToken string, nextAttempt time.Time, cause error) error
	DeadLetterDelivery(ctx context.Context, id, claimToken, reason string) error
}

// EffectError captures a non-fatal error during effect delivery scheduling
// or best-effort commit. These errors do not abort the transaction but should
// be observable for operational health.
type EffectError struct {
	Phase string
	Name  string
	Err   error
}

func (e EffectError) Error() string {
	if e.Name != "" {
		return fmt.Sprintf("ref: effect %s %q: %v", e.Phase, e.Name, e.Err)
	}
	return fmt.Sprintf("ref: effect %s: %v", e.Phase, e.Err)
}

func (e EffectError) Unwrap() error { return e.Err }

// EffectErrorFunc is called when a non-fatal effect error occurs during
// delivery scheduling or best-effort commit. The function must be safe for
// concurrent use.
type EffectErrorFunc func(err EffectError)

// CommitPlan executes the crash-safe two-phase effect commit strategy:
//  1. Begin effect transaction
//  2. Record all DurableDelivery effects into the open transaction (outbox pattern)
//  3. Commit LocalTransactional effects atomically inside the same transaction
//  4. Atomic Commit of transaction (domain writes + durable records commit together)
//  5. Schedule async delivery worker for durable records
//  6. Execute FireAndForget effects best-effort
//
// If local transactional commit fails, compensating effects run and tx is aborted.
// Non-fatal errors (delivery scheduling, best-effort commit) are reported via onErr
// when non-nil, instead of being silently discarded.
func CommitPlan(ctx context.Context, store EffectStore, executionID string, effects []Effect, onErr ...EffectErrorFunc) error {
	var reportErr EffectErrorFunc
	if len(onErr) > 0 {
		reportErr = onErr[0]
	}
	var localBuf, durableBuf, fireBuf [8]Effect
	local, durable, fire := localBuf[:0], durableBuf[:0], fireBuf[:0]
	for _, e := range effects {
		if e == nil {
			return errors.New("ref: effect plan contains nil effect")
		}
		switch e.Kind() {
		case LocalTransactional:
			local = append(local, e)
		case DurableDelivery:
			durable = append(durable, e)
		case FireAndForget:
			fire = append(fire, e)
		default:
			return fmt.Errorf("ref: unsupported effect kind %d", e.Kind())
		}
	}
	txID, err := beginEffectTransaction(ctx, store, executionID)
	if err != nil {
		return err
	}
	return commitEffectGroups(ctx, store, txID, local, durable, fire, reportErr, true)
}

// CommitEffectPlan commits effects already grouped by delivery semantics. It
// avoids flattening the plan and re-classifying each effect at runtime.
func CommitEffectPlan(ctx context.Context, store EffectStore, executionID string, plan EffectPlan, onErr ...EffectErrorFunc) error {
	return CommitEffectPlanWithMode(ctx, store, executionID, plan, true, onErr...)
}

func CommitEffectPlanWithMode(ctx context.Context, store EffectStore, executionID string, plan EffectPlan, deliverDurable bool, onErr ...EffectErrorFunc) error {
	if err := plan.Validate(); err != nil {
		return err
	}
	for _, group := range [][]Effect{plan.LocalTx, plan.Durable, plan.FireAndForget} {
		for _, e := range group {
			if e == nil {
				return errors.New("ref: effect plan contains nil effect")
			}
		}
	}
	txID, err := beginEffectTransaction(ctx, store, executionID)
	if err != nil {
		return err
	}
	var reportErr EffectErrorFunc
	if len(onErr) > 0 {
		reportErr = onErr[0]
	}
	return commitEffectGroups(ctx, store, txID, plan.LocalTx, plan.Durable, plan.FireAndForget, reportErr, deliverDurable)
}

func beginEffectTransaction(ctx context.Context, store EffectStore, executionID string) (string, error) {
	if store == nil {
		return "", nil
	}
	txID, err := store.Begin(ctx, executionID)
	if err != nil {
		return "", fmt.Errorf("ref: effect store begin error: %w", err)
	}
	if txID == "" {
		return "", errors.New("ref: effect store returned an empty transaction id")
	}
	return txID, nil
}

func recordDurableEffect(ctx context.Context, store EffectStore, txID string, e Effect, index int) error {
	record := EffectRecord{
		Name:  e.Name(),
		Kind:  DurableDelivery,
		State: "pending",
	}
	if encoder, ok := e.(EffectEncoder); ok {
		payload, key, idempotent, err := encoder.EncodeEffect()
		if err != nil {
			return fmt.Errorf("ref: failed to encode durable effect %q: %w", e.Name(), err)
		}
		record.Payload = append([]byte(nil), payload...)
		record.IdempotencyKey = key
		record.Idempotent = idempotent
	}
	if record.IdempotencyKey == "" && record.Idempotent {
		digest := sha256.Sum256([]byte(fmt.Sprintf("%s:%s:%d", txID, e.Name(), index)))
		record.IdempotencyKey = hex.EncodeToString(digest[:])
	}
	return store.Record(ctx, txID, record)
}

func compensate(committed []CompensatingEffect) error {
	var errs []error
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for i := len(committed) - 1; i >= 0; i-- {
		if err := committed[i].Compensate(ctx); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", committed[i].Name(), err))
		}
	}
	return errors.Join(errs...)
}

func abortTransaction(ctx context.Context, store EffectStore, txID string) {
	if abortable, ok := store.(AbortableEffectStore); ok {
		abortCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		_ = abortable.Abort(abortCtx, txID)
		cancel()
	}
}

func commitLocalEffect(ctx context.Context, store EffectStore, txID string, e Effect) error {
	if transactional, ok := e.(TransactionalEffect); ok {
		provider, ok := store.(TransactionProvider)
		if !ok {
			return errors.New("transactional effect requires a transaction provider")
		}
		tx, err := provider.Transaction(ctx, txID)
		if err != nil {
			return err
		}
		return transactional.CommitTransaction(ctx, tx)
	}
	return e.Commit(ctx)
}

func commitEffectGroups(ctx context.Context, store EffectStore, txID string, local, durable, fire []Effect, reportErr EffectErrorFunc, deliverDurable bool) error {
	if store != nil && txID != "" {
		for i, e := range durable {
			if err := recordDurableEffect(ctx, store, txID, e, i); err != nil {
				abortTransaction(ctx, store, txID)
				return err
			}
		}
	}

	var committed []CompensatingEffect
	for _, e := range local {
		if err := commitLocalEffect(ctx, store, txID, e); err != nil {
			compensateErr := compensate(committed)
			abortTransaction(ctx, store, txID)
			if compensateErr != nil {
				return errors.Join(fmt.Errorf("ref: local transactional effect %q failed: %w", e.Name(), err), compensateErr)
			}
			return fmt.Errorf("ref: local transactional effect %q failed: %w", e.Name(), err)
		}
		if ce, ok := e.(CompensatingEffect); ok {
			committed = append(committed, ce)
		}
	}

	if store != nil && txID != "" {
		if err := store.Commit(ctx, txID); err != nil {
			compensateErr := compensate(committed)
			if compensateErr != nil {
				return errors.Join(fmt.Errorf("ref: effect store commit error: %w", err), compensateErr)
			}
			return fmt.Errorf("ref: effect store commit error: %w", err)
		}
		if err := store.ScheduleDelivery(ctx, txID); err != nil && reportErr != nil {
			reportErr(EffectError{Phase: "schedule_delivery", Err: err})
		}
	}

	if deliverDurable {
		for _, e := range durable {
			if err := e.Commit(ctx); err != nil && reportErr != nil {
				reportErr(EffectError{Phase: "durable_commit", Name: e.Name(), Err: err})
			}
		}
	}
	for _, e := range fire {
		if err := e.Commit(ctx); err != nil && reportErr != nil {
			reportErr(EffectError{Phase: "fire_and_forget", Name: e.Name(), Err: err})
		}
	}
	return nil
}
