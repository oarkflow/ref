package effect

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/oarkflow/ref/backoff"
)

type EffectResolver func(EffectRecord) (Effect, error)

type Runner struct {
	store       EffectStore
	onErr       EffectErrorFunc
	delivery    DeliveryStore
	resolversMu sync.RWMutex
	resolvers   map[string]EffectResolver
	owner       string
	startOnce   sync.Once
	closeOnce   sync.Once
	cancel      context.CancelFunc
	wg          sync.WaitGroup
	closed      atomic.Bool
}

func NewRunner(store EffectStore, opts ...RunnerOption) *Runner {
	if store == nil {
		store = NewMemoryEffectStore()
	}
	r := &Runner{
		store:     store,
		resolvers: make(map[string]EffectResolver),
		owner:     fmt.Sprintf("runner-%d", time.Now().UnixNano()),
	}
	if delivery, ok := store.(DeliveryStore); ok {
		r.delivery = delivery
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

type RunnerOption func(*Runner)

func WithEffectErrorHandler(fn EffectErrorFunc) RunnerOption {
	return func(r *Runner) { r.onErr = fn }
}

func WithEffectResolver(name string, resolver EffectResolver) RunnerOption {
	return func(r *Runner) {
		if name != "" && resolver != nil {
			r.resolvers[name] = resolver
		}
	}
}

func (r *Runner) SetErrorHandler(fn EffectErrorFunc) {
	r.onErr = fn
}

func (r *Runner) RegisterEffectResolver(name string, resolver EffectResolver) {
	if name == "" || resolver == nil {
		return
	}
	r.resolversMu.Lock()
	r.resolvers[name] = resolver
	r.resolversMu.Unlock()
}

func (r *Runner) Store() EffectStore {
	return r.store
}

func (r *Runner) Start(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if r.closed.Load() {
		return fmt.Errorf("ref: effect runner is closed")
	}
	if r.delivery == nil {
		return nil
	}
	r.startOnce.Do(func() {
		workerCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
		r.cancel = cancel
		if pending, err := r.store.Recover(workerCtx); err == nil {
			for _, tx := range pending {
				if err := r.store.ScheduleDelivery(workerCtx, tx.TxID); err != nil && r.onErr != nil {
					r.onErr(EffectError{Phase: "recover_schedule", Err: err})
				}
			}
		} else if r.onErr != nil {
			r.onErr(EffectError{Phase: "recover", Err: err})
		}
		r.wg.Add(1)
		go r.deliveryLoop(workerCtx)
	})
	return nil
}

func (r *Runner) RunEffects(ctx context.Context, executionID string, effects []Effect, onErr ...EffectErrorFunc) error {
	plan := EffectPlan{}
	for _, e := range effects {
		if e == nil {
			return fmt.Errorf("ref: effect plan contains nil effect")
		}
		switch e.Kind() {
		case LocalTransactional:
			plan.LocalTx = append(plan.LocalTx, e)
		case DurableDelivery:
			plan.Durable = append(plan.Durable, e)
		case FireAndForget:
			plan.FireAndForget = append(plan.FireAndForget, e)
		default:
			return fmt.Errorf("ref: unsupported effect kind %d", e.Kind())
		}
	}
	return r.runPlan(ctx, executionID, plan, onErr...)
}

func (r *Runner) Run(ctx context.Context, executionID string, plan EffectPlan) error {
	return r.runPlan(ctx, executionID, plan)
}

func (r *Runner) runPlan(ctx context.Context, executionID string, plan EffectPlan, onErr ...EffectErrorFunc) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if r.closed.Load() {
		return fmt.Errorf("ref: effect runner is closed")
	}
	if plan.IsEmpty() {
		return nil
	}
	if r.delivery != nil {
		if err := r.Start(ctx); err != nil {
			return fmt.Errorf("ref: effect runner start failed: %w", err)
		}
	}
	reporter := r.onErr
	if len(onErr) > 0 && onErr[0] != nil {
		reporter = onErr[0]
	}
	if err := CommitEffectPlanWithMode(ctx, r.store, executionID, plan, r.delivery == nil, reporter); err != nil {
		return fmt.Errorf("ref: effect runner failed: %w", err)
	}
	return nil
}

func (r *Runner) Close() error {
	var err error
	r.closeOnce.Do(func() {
		r.closed.Store(true)
		if r.cancel != nil {
			r.cancel()
		}
		r.wg.Wait()
		err = r.store.Close()
	})
	return err
}

func (r *Runner) deliveryLoop(ctx context.Context) {
	defer r.wg.Done()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if !r.processDeliveries(ctx) {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (r *Runner) processDeliveries(ctx context.Context) bool {
	deliveries, err := r.delivery.ClaimDeliveries(ctx, r.owner, 32, 30*time.Second)
	if err != nil {
		if ctx.Err() == nil && r.onErr != nil {
			r.onErr(EffectError{Phase: "delivery_claim", Err: err})
		}
		return ctx.Err() == nil
	}
	for _, delivery := range deliveries {
		r.resolversMu.RLock()
		resolver := r.resolvers[delivery.Record.Name]
		r.resolversMu.RUnlock()
		if resolver == nil {
			if err := r.delivery.DeadLetterDelivery(ctx, delivery.Record.ID, delivery.ClaimToken, "no effect resolver registered"); err != nil && r.onErr != nil {
				r.onErr(EffectError{Phase: "dead_letter", Name: delivery.Record.Name, Err: err})
			}
			continue
		}
		effectValue, err := resolver(delivery.Record)
		if err == nil {
			err = invokeEffect(ctx, effectValue)
		}
		if err == nil {
			if err = r.delivery.AckDelivery(ctx, delivery.Record.ID, delivery.ClaimToken); err == nil {
				continue
			}
		}
		if delivery.Record.Attempts >= 8 {
			if deadErr := r.delivery.DeadLetterDelivery(ctx, delivery.Record.ID, delivery.ClaimToken, err.Error()); deadErr != nil && r.onErr != nil {
				r.onErr(EffectError{Phase: "dead_letter", Name: delivery.Record.Name, Err: deadErr})
			}
			continue
		}
		// Full-jitter exponential backoff (same algorithm/cap as before:
		// base 1s, doubling per attempt, capped at 64s) — jitter avoids a
		// dead-letter retry storm when many deliveries fail together.
		delay := backoff.FullJitter(time.Second, min(delivery.Record.Attempts-1, 6), 64*time.Second)
		if retryErr := r.delivery.RetryDelivery(ctx, delivery.Record.ID, delivery.ClaimToken, time.Now().Add(delay), err); retryErr != nil && r.onErr != nil {
			r.onErr(EffectError{Phase: "delivery_retry", Name: delivery.Record.Name, Err: retryErr})
		}
	}
	return ctx.Err() == nil
}

func invokeEffect(ctx context.Context, e Effect) (err error) {
	if e == nil {
		return fmt.Errorf("resolver returned nil effect")
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("effect panic: %v", recovered)
		}
	}()
	callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return e.Commit(callCtx)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
