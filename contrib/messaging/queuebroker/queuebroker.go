// Package queuebroker plugs github.com/oarkflow/broker into REF as a queue
// resource, kind "queue.broker".
//
// The resource satisfies the shape REF's `queue.publish` action, `worker`
// blocks and schedules need (Enqueue, Register, Start), so any BCL that works
// against queue.file or queue.sql works against the broker unchanged. On top of
// that it exposes what the broker has and fh's queue does not:
//
//   - one broker queue per job type, declared on first use with its own
//     dead-letter queue, retry policy, visibility timeout and consumer
//     concurrency, tuned from BCL by glob pattern;
//   - delayed jobs, used for retry backoff;
//   - a write-ahead log, so queued work survives a restart.
//
// Delivery is at least once. A handler that returns an error is retried with
// the queue's backoff and, after max_attempts, moved to <job type>.dlq.
package queuebroker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"sync"
	"time"

	"github.com/oarkflow/broker"
	"github.com/oarkflow/fh"
	"github.com/oarkflow/ref/platform"

	cfgdec "github.com/oarkflow/ref/contrib/messaging/internal/cfg"
)

// Kind is the BCL resource kind this package registers.
const Kind = "queue.broker"

// Retry is a retry policy as written in BCL.
type Retry struct {
	MaxAttempts int             `json:"max_attempts"`
	Initial     cfgdec.Duration `json:"initial"`
	Max         cfgdec.Duration `json:"max"`
	Factor      float64         `json:"factor"`
	Jitter      bool            `json:"jitter"`
}

// Consumer tunes the consumers of every job type matching ID, a path.Match glob.
// The first matching rule wins.
type Consumer struct {
	ID                string          `json:"id"`
	Concurrency       int             `json:"concurrency"`
	MaxAttempts       int             `json:"max_attempts"`
	VisibilityTimeout cfgdec.Duration `json:"visibility_timeout"`
	Retry             *Retry          `json:"retry"`
	MaxDepth          int             `json:"max_depth"`
}

// Config is the resource's BCL config.
type Config struct {
	// Dir is the write-ahead log directory. Empty keeps the queue in memory.
	Dir               string          `json:"dir"`
	WALFormat         string          `json:"wal_format"` // binary (default) or json
	NodeID            string          `json:"node_id"`
	Concurrency       int             `json:"concurrency"`
	VisibilityTimeout cfgdec.Duration `json:"visibility_timeout"`
	Retry             Retry           `json:"retry"`
	DLQSuffix         string          `json:"dlq_suffix"`
	MaxDepth          int             `json:"max_depth"`
	PollInterval      cfgdec.Duration `json:"poll_interval"`
	DrainTimeout      cfgdec.Duration `json:"drain_timeout"`
	Consumers         []Consumer      `json:"consumer"`
}

// Register installs the queue.broker resource kind. It is idempotent so a test
// binary and a main package can both call it.
func Register() {
	registerOnce.Do(func() {
		platform.RegisterResourceDriver(Kind, platform.ResourceFactoryFunc(openResource), platform.ResourceKindInfo{
			Family:   "queue",
			Summary:  "Durable queue on github.com/oarkflow/broker: write-ahead log, per-job-type consumers, delayed and broadcast jobs.",
			Provides: []string{"JobQueue", "QueueDelay", "QueueConsume"},
			Config: []platform.ConfigField{
				{Name: "dir", Type: "string", Summary: "WAL directory; empty keeps the queue in memory"},
				{Name: "concurrency", Type: "int", Default: "4", Summary: "Default consumers per job type"},
				{Name: "visibility_timeout", Type: "duration", Default: "30s"},
				{Name: "retry", Type: "block", Summary: "max_attempts, initial, max, factor, jitter"},
				{Name: "consumer", Type: "block", Summary: `consumer "sms.dispatch.*" { concurrency 8 } tunes job types by glob`},
			},
		})
	})
}

var registerOnce sync.Once

func openResource(_ context.Context, spec platform.ResourceSpec) (platform.Resource, io.Closer, error) {
	var cfg Config
	if err := cfgdec.Decode(spec.Config, &cfg); err != nil {
		return nil, nil, fmt.Errorf("queue.broker %q: %w", spec.Name, err)
	}
	q, err := Open(cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("queue.broker %q: %w", spec.Name, err)
	}
	return q, q, nil
}

// Queue is the broker-backed queue.
type Queue struct {
	cfg    Config
	b      *broker.Broker
	ctx    context.Context
	cancel context.CancelFunc

	mu       sync.Mutex
	started  bool
	tuneMu   sync.RWMutex
	tuned    map[string]Options
	declared map[string]bool
	handlers []pending
	subs     []*broker.Subscription
	closed   bool
}

type pending struct {
	jobType string
	queue   string
	handler fh.QueueHandler
}

// Open builds a queue and starts its broker.
func Open(cfg Config) (*Queue, error) {
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = 4
	}
	if cfg.VisibilityTimeout == 0 {
		cfg.VisibilityTimeout = cfgdec.Duration(30 * time.Second)
	}
	if cfg.Retry.MaxAttempts <= 0 {
		cfg.Retry.MaxAttempts = 6
	}
	if cfg.Retry.Initial == 0 {
		cfg.Retry.Initial = cfgdec.Duration(500 * time.Millisecond)
	}
	if cfg.Retry.Max == 0 {
		cfg.Retry.Max = cfgdec.Duration(time.Minute)
	}
	if cfg.Retry.Factor <= 0 {
		cfg.Retry.Factor = 2
	}
	if cfg.DLQSuffix == "" {
		cfg.DLQSuffix = ".dlq"
	}
	if cfg.NodeID == "" {
		cfg.NodeID = "node"
	}
	if cfg.PollInterval == 0 {
		cfg.PollInterval = cfgdec.Duration(25 * time.Millisecond)
	}
	if cfg.DrainTimeout == 0 {
		cfg.DrainTimeout = cfgdec.Duration(10 * time.Second)
	}
	for _, c := range cfg.Consumers {
		if _, err := path.Match(c.ID, ""); err != nil {
			return nil, fmt.Errorf("consumer %q is not a valid glob: %w", c.ID, err)
		}
	}

	bc := broker.Config{
		Name:              "sms-" + cfg.NodeID,
		DefaultQueue:      "default",
		DefaultVisibility: cfg.VisibilityTimeout.D(),
		DefaultRetry:      retryPolicy(cfg.Retry, cfg.Retry.MaxAttempts),
		DefaultDLQ:        "default" + cfg.DLQSuffix,
		PollInterval:      cfg.PollInterval.D(),
		DrainTimeout:      cfg.DrainTimeout.D(),
	}
	if cfg.Dir != "" {
		var (
			store broker.Store
			err   error
		)
		switch cfg.WALFormat {
		case "", "binary":
			store, err = broker.NewBinaryFileWALStore(cfg.Dir)
		case "json":
			store, err = broker.NewFileWALStore(cfg.Dir)
		default:
			return nil, fmt.Errorf("wal_format must be binary or json")
		}
		if err != nil {
			return nil, fmt.Errorf("open write-ahead log %s: %w", cfg.Dir, err)
		}
		bc.Store = store
	}
	b, err := broker.New(bc)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := b.Start(ctx); err != nil {
		cancel()
		return nil, fmt.Errorf("start broker: %w", err)
	}
	return &Queue{cfg: cfg, b: b, ctx: ctx, cancel: cancel, declared: map[string]bool{}, tuned: map[string]Options{}}, nil
}

func retryPolicy(r Retry, attempts int) broker.RetryPolicy {
	return broker.RetryPolicy{MaxAttempts: attempts, Initial: r.Initial.D(), Max: r.Max.D(), Factor: r.Factor, Jitter: r.Jitter}
}

// Broker exposes the underlying broker for operators and tests.
func (q *Queue) Broker() *broker.Broker { return q.b }

// Options are consumer settings applied from Go, for job types the BCL cannot
// name in advance (one queue per provider, providers created at runtime).
type Options struct {
	Concurrency int
	MaxAttempts int
}

// Tune sets the consumer settings of a job type. It must be called before the
// job type is first enqueued or registered; it overrides the BCL consumer rules.
func (q *Queue) Tune(jobType string, concurrency, maxAttempts int) {
	q.tuneMu.Lock()
	q.tuned[jobType] = Options{Concurrency: concurrency, MaxAttempts: maxAttempts}
	q.tuneMu.Unlock()
}

// settings resolves the effective consumer settings of a job type.
func (q *Queue) settings(jobType string) (conc, attempts, maxDepth int, vis time.Duration, retry Retry) {
	conc, attempts, maxDepth, vis, retry = q.cfg.Concurrency, q.cfg.Retry.MaxAttempts, q.cfg.MaxDepth, q.cfg.VisibilityTimeout.D(), q.cfg.Retry
	defer func() {
		q.tuneMu.RLock()
		o, ok := q.tuned[jobType]
		q.tuneMu.RUnlock()
		if ok {
			if o.Concurrency > 0 {
				conc = o.Concurrency
			}
			if o.MaxAttempts > 0 {
				attempts = o.MaxAttempts
			}
		}
	}()
	for _, c := range q.cfg.Consumers {
		if ok, _ := path.Match(c.ID, jobType); !ok {
			continue
		}
		if c.Concurrency > 0 {
			conc = c.Concurrency
		}
		if c.MaxAttempts > 0 {
			attempts = c.MaxAttempts
		}
		if c.MaxDepth > 0 {
			maxDepth = c.MaxDepth
		}
		if c.VisibilityTimeout > 0 {
			vis = c.VisibilityTimeout.D()
		}
		if c.Retry != nil {
			retry = *c.Retry
			if retry.Initial == 0 {
				retry.Initial = q.cfg.Retry.Initial
			}
			if retry.Max == 0 {
				retry.Max = q.cfg.Retry.Max
			}
			if retry.Factor <= 0 {
				retry.Factor = q.cfg.Retry.Factor
			}
			if retry.MaxAttempts > 0 {
				attempts = retry.MaxAttempts
			}
		}
		break
	}
	return
}

func (q *Queue) declare(name, jobType string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.declared[name] {
		return nil
	}
	conc, attempts, depth, vis, retry := q.settings(jobType)
	err := q.b.DeclareQueue(q.ctx, broker.QueueConfig{
		Name: name, Workers: conc, MaxDepth: depth, VisibilityTimeout: vis,
		RetryPolicy: retryPolicy(retry, attempts), DLQ: name + q.cfg.DLQSuffix,
	})
	if err != nil {
		return fmt.Errorf("declare queue %s: %w", name, err)
	}
	q.declared[name] = true
	return nil
}

func encodePayload(payload any) (json.RawMessage, error) {
	switch p := payload.(type) {
	case nil:
		return json.RawMessage("null"), nil
	case json.RawMessage:
		return p, nil
	case []byte:
		if json.Valid(p) {
			return p, nil
		}
		return json.Marshal(string(p))
	default:
		return json.Marshal(p)
	}
}

func publishOptions(queue string, headers []map[string]string) []broker.PublishOption {
	opts := []broker.PublishOption{broker.WithQueue(queue), broker.WithRequireDestination(true)}
	merged := map[string]string{}
	for _, h := range headers {
		for k, v := range h {
			merged[k] = v
		}
	}
	if len(merged) > 0 {
		opts = append(opts, broker.WithHeaders(merged))
	}
	return opts
}

// Enqueue implements the REF queue contract.
func (q *Queue) Enqueue(jobType string, payload any, headers ...map[string]string) (string, error) {
	return q.enqueue(jobType, payload, 0, time.Time{}, headers...)
}

// EnqueueDelayed implements spi.QueueDelay.
func (q *Queue) EnqueueDelayed(jobType string, payload any, runAt time.Time, headers ...map[string]string) (string, error) {
	return q.enqueue(jobType, payload, 0, runAt, headers...)
}

// EnqueueAfter schedules a job after a relative delay.
func (q *Queue) EnqueueAfter(jobType string, payload any, delay time.Duration, headers ...map[string]string) (string, error) {
	if delay <= 0 {
		return q.enqueue(jobType, payload, 0, time.Time{}, headers...)
	}
	return q.enqueue(jobType, payload, 0, time.Now().Add(delay), headers...)
}

func (q *Queue) enqueue(jobType string, payload any, _ int, runAt time.Time, headers ...map[string]string) (string, error) {
	if jobType == "" {
		return "", errors.New("queue.broker: a job type is required")
	}
	if err := q.declare(jobType, jobType); err != nil {
		return "", err
	}
	raw, err := encodePayload(payload)
	if err != nil {
		return "", fmt.Errorf("queue.broker: encode payload: %w", err)
	}
	opts := publishOptions(jobType, headers)
	if !runAt.IsZero() && runAt.After(time.Now()) {
		opts = append(opts, broker.WithRunAt(runAt))
	}
	res, err := q.b.Publisher().Publish(q.ctx, jobType, raw, opts...)
	if err != nil {
		return "", fmt.Errorf("queue.broker: publish %s: %w", jobType, err)
	}
	return res.MessageID, nil
}

// Register implements the REF queue contract: it binds handler to every job of
// jobType. Consumption starts at Start, or at once when already started.
func (q *Queue) Register(jobType string, handler fh.QueueHandler) {
	q.add(pending{jobType: jobType, queue: jobType, handler: handler})
}

func (q *Queue) add(p pending) {
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return
	}
	if !q.started {
		q.handlers = append(q.handlers, p)
		q.mu.Unlock()
		return
	}
	q.mu.Unlock()
	if err := q.subscribe(p); err != nil {
		// Register has no error return in the REF contract; a queue that cannot
		// be consumed is loud, not silent.
		panic(fmt.Sprintf("queue.broker: cannot consume %s: %v", p.jobType, err))
	}
}

// Start begins consuming every registered job type. It is idempotent.
func (q *Queue) Start() error {
	q.mu.Lock()
	if q.started {
		q.mu.Unlock()
		return nil
	}
	q.started = true
	todo := q.handlers
	q.handlers = nil
	q.mu.Unlock()
	for _, p := range todo {
		if err := q.subscribe(p); err != nil {
			return err
		}
	}
	return nil
}

func (q *Queue) subscribe(p pending) error {
	if err := q.declare(p.queue, p.jobType); err != nil {
		return err
	}
	conc, attempts, _, vis, retry := q.settings(p.jobType)
	sub, err := q.b.Subscribe(q.ctx, broker.ConsumerConfig{
		Name:              q.cfg.NodeID + "/" + p.queue,
		Group:             p.queue,
		Queue:             p.queue,
		Concurrency:       conc,
		VisibilityTimeout: vis,
		RetryPolicy:       retryPolicy(retry, attempts),
		DLQ:               p.queue + q.cfg.DLQSuffix,
		AckMode:           broker.AckAuto,
	}, func(ctx context.Context, d broker.Delivery) error {
		m := d.Message
		job := &fh.QueueJob{
			ID: m.ID, Type: p.jobType, Payload: json.RawMessage(m.Payload), Headers: m.Headers,
			Attempts: m.Attempts + 1, MaxAttempts: attempts, CreatedAt: m.CreatedAt, LastError: m.LastError,
		}
		return p.handler(ctx, job)
	})
	if err != nil {
		return err
	}
	q.mu.Lock()
	q.subs = append(q.subs, sub)
	q.mu.Unlock()
	return nil
}

// DLQDepth reports how many messages sit in the dead-letter queue of jobType.
func (q *Queue) DLQDepth(jobType string) int {
	return q.depth(jobType + q.cfg.DLQSuffix)
}

func (q *Queue) depth(name string) int {
	msgs, err := q.b.Store().ListQueue(q.ctx, name, 100000)
	if err != nil {
		return 0
	}
	return len(msgs)
}

// Close stops consumers and the broker.
func (q *Queue) Close() error {
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return nil
	}
	q.closed = true
	subs := q.subs
	q.mu.Unlock()
	for _, s := range subs {
		s.Stop()
	}
	q.cancel()
	ctx, cancel := context.WithTimeout(context.Background(), q.cfg.DrainTimeout.D())
	defer cancel()
	return q.b.Stop(ctx)
}
