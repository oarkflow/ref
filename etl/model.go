// Package etl is a data-pipeline control plane: registered sources with an
// owner, a format and validation rules; a six-stage run engine (ingest and
// validate, transform, transfer, delivery, access and audit, monitoring and
// lineage) with checkpoints, retries, hold and replay; role checks; a
// hash-chained audit trail; and lineage from source to destination.
//
// The engine decides nothing about what a transform or a delivery does. It
// calls Hooks, which the platform wires to intents, and it makes sure that a
// failure at any point leaves the batch held at a checkpoint rather than lost.
package etl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

var (
	ErrNotFound  = errors.New("etl: not found")
	ErrConflict  = errors.New("etl: the record was changed by someone else; reload and retry")
	ErrForbidden = errors.New("etl: not allowed")
	ErrInvalid   = errors.New("etl: invalid")
	ErrKeyReused = errors.New("etl: idempotency key reused with different content")
	ErrNotDue    = errors.New("etl: the batch is not due yet")
	ErrState     = errors.New("etl: the batch is not in a state that allows this")
	ErrDuplicate = errors.New("etl: the same content was already sent")
	ErrLeased    = errors.New("etl: another worker is running this batch")
)

// Batch statuses.
const (
	StatusInFlight  = "in_flight"
	StatusRetrying  = "retrying"
	StatusHeld      = "held"
	StatusDelivered = "delivered"
	StatusFailed    = "failed"
)

// Stages, in order. A batch's Stage is the next stage to run; StageDone means
// every stage has run.
const (
	StageIngest    = 1
	StageTransform = 2
	StageTransfer  = 3
	StageDelivery  = 4
	StageAudit     = 5
	StageMonitor   = 6
	StageDone      = 7
)

// StageNames name the six stages for display.
var StageNames = [...]string{"", "Source ingestion and validation", "Transformation and enrichment",
	"Reliable system-to-system transfer", "Delivery queues and retry paths", "Access controls and audit trails",
	"Monitoring and data lineage"}

// Row is one record.
type Row map[string]any

// Rule is one validation rule of a source.
type Rule struct {
	Name    string   `json:"name"`
	Field   string   `json:"column,omitempty"`
	Kind    string   `json:"check"` // required, type, regex, range, enum, unique, expr
	Type    string   `json:"type,omitempty"`
	Pattern string   `json:"pattern,omitempty"`
	Min     *float64 `json:"min,omitempty"`
	Max     *float64 `json:"max,omitempty"`
	Values  []string `json:"values,omitempty"`
	Expr    string   `json:"expr,omitempty"`
	Message string   `json:"message,omitempty"`
}

// RetryPolicy controls retries of a failed stage.
type RetryPolicy struct {
	MaxAttempts int           `json:"max_attempts"`
	Base        time.Duration `json:"base"`
	Cap         time.Duration `json:"cap"`
}

// Source is a registered origin of data: it has an owner, a format and rules.
type Source struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Owner       string `json:"owner"`
	Format      string `json:"format"` // csv, jsonl, json
	Destination string `json:"destination"`
	Description string `json:"description,omitempty"`
	Rules       []Rule `json:"rules"`
	// ExpectEvery is how often a batch should arrive (zero: no expectation). A
	// source that goes quiet for longer raises a freshness alert.
	ExpectEvery Dur `json:"expect_every"`
	// MaxRows refuses a batch with more rows than this (default 100000), so one
	// upload cannot exhaust memory or the checkpoint store.
	MaxRows int `json:"max_rows"`
	// RejectDuplicates refuses content that was already accepted under another
	// key, which is how a retried upload with a fresh key is caught.
	RejectDuplicates bool `json:"reject_duplicates"`
	// OutputRules are the contract the transform's output must meet. A
	// transform that breaks it holds the batch instead of passing bad rows on.
	OutputRules []Rule `json:"output_rules"`
	// AllowRowChange lets the transform add or drop rows; by default it must
	// return exactly the rows it was given.
	AllowRowChange bool        `json:"allow_row_change"`
	MaxRejectRate  float64     `json:"max_reject_rate"` // 0..1; above it the batch fails and nothing moves
	Retry          RetryPolicy `json:"retry"`
	Version        int         `json:"version"`
	Paused         bool        `json:"paused"`
	CreatedAt      time.Time   `json:"created_at"`
	UpdatedAt      time.Time   `json:"updated_at"`
}

// Event is one entry in a batch's own timeline.
type Event struct {
	At      time.Time `json:"at"`
	Stage   int       `json:"stage"`
	Kind    string    `json:"kind"` // info, checkpoint, retry, held, replay, delivered, failed
	Message string    `json:"message"`
	// DurationMs is how long the stage ran (a span's length); Attempt is the
	// try it belongs to. Zero for events that are not a stage run.
	DurationMs int64 `json:"duration_ms,omitempty"`
	Attempt    int   `json:"attempt,omitempty"`
}

// Failure is one failed attempt, kept for the life of the batch so the whole
// story is there when someone has to decide what to do.
type Failure struct {
	At        time.Time `json:"at"`
	Stage     int       `json:"stage"`
	StageName string    `json:"stage_name"`
	Attempt   int       `json:"attempt"`
	Max       int       `json:"max_attempts"`
	Error     string    `json:"error"`
	Outcome   string    `json:"outcome"` // retry, held
	Reason    string    `json:"reason"`  // why that outcome: the backoff, the attempt limit, a permanent error
	RetryAt   time.Time `json:"retry_at,omitempty"`
	Permanent bool      `json:"permanent,omitempty"`
}

// Batch is one transfer from a source.
type Batch struct {
	ID            string    `json:"id"`
	SourceID      string    `json:"source_id"`
	Key           string    `json:"key"`
	ContentHash   string    `json:"content_hash"`
	TraceID       string    `json:"trace_id"`
	SourceVersion int       `json:"source_version"`
	FinishedAt    time.Time `json:"finished_at,omitempty"`
	Actor         string    `json:"actor"`
	RowsIn        int       `json:"rows_in"`
	Quarantined   int       `json:"quarantined"`
	RowsOut       int       `json:"rows_out"`
	Delivered     int       `json:"delivered"`
	// A large batch is processed in chunks: Chunks is how many, ChunkDone how
	// many the delivery stage has finished (so an interruption resumes there).
	Chunks    int `json:"chunks,omitempty"`
	ChunkDone int `json:"chunk_done,omitempty"`
	// Epoch counts delivery calls: it is sent with each one as a fencing token,
	// so a receiver can refuse a late call from a worker that lost its lease.
	// Delivering is true from just before a delivery call until its outcome is
	// recorded; if it is still true when the stage runs again, the call is in
	// doubt and the receiver is asked (Hooks.Verify) before anything is re-sent.
	Epoch           int64 `json:"epoch,omitempty"`
	Delivering      bool  `json:"delivering,omitempty"`
	DeliveringChunk int   `json:"delivering_chunk,omitempty"`
	// Set on the copy of the batch a hook receives, never stored: which chunk
	// this call is for, how many there are, and the idempotency key of this
	// call (the batch key, or the batch key and the chunk number).
	Chunk         int       `json:"chunk,omitempty"`
	ChunkCount    int       `json:"chunk_count,omitempty"`
	DeliveryKey   string    `json:"delivery_key,omitempty"`
	Stage         int       `json:"stage"`
	Status        string    `json:"status"`
	Attempts      int       `json:"attempts"`
	NextAttemptAt time.Time `json:"next_attempt_at"`
	LastError     string    `json:"last_error,omitempty"`
	Ref           string    `json:"ref,omitempty"` // destination reference once delivered
	Events        []Event   `json:"events"`
	Failures      []Failure `json:"failures,omitempty"`
	Revision      int64     `json:"revision"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// Checkpoint is the rows a stage handed on, kept so a failure can resume from
// it. Hash is the SHA-256 of the rows, checked when they are read back.
type Checkpoint struct {
	BatchID string `json:"batch_id"`
	Stage   int    `json:"stage"`
	// Rows holds a small batch inline. A large one is kept as Chunks in the
	// blob store, and the database holds only these references.
	Rows   []Row      `json:"rows,omitempty"`
	Chunks []ChunkRef `json:"chunks,omitempty"`
	Count  int        `json:"count"` // rows in all, inline or chunked
	Hash   string     `json:"hash"`
	At     time.Time  `json:"at"`
}

// ChunkRef points at one chunk of rows in the blob store.
type ChunkRef struct {
	Key  string `json:"key"`
	Rows int    `json:"rows"`
	Hash string `json:"hash"`
}

// Quarantine is one row refused by a rule, kept with the reason.
type Quarantine struct {
	BatchID  string    `json:"batch_id"`
	SourceID string    `json:"source_id"`
	RowNo    int       `json:"row_no"`
	Rule     string    `json:"rule"`
	Field    string    `json:"field,omitempty"`
	Reason   string    `json:"reason"`
	Row      Row       `json:"row"`
	At       time.Time `json:"at"`
}

// AuditEntry is one append-only, hash-chained record of who did what.
type AuditEntry struct {
	Seq      int64     `json:"seq"`
	At       time.Time `json:"at"`
	Actor    string    `json:"actor"`
	Action   string    `json:"action"`
	BatchID  string    `json:"batch_id,omitempty"`
	SourceID string    `json:"source_id,omitempty"`
	Detail   string    `json:"detail,omitempty"`
	TraceID  string    `json:"trace_id,omitempty"`
	PrevHash string    `json:"prev_hash"`
	Hash     string    `json:"hash"`
}

// LineageEdge says that Child was produced from Parent by Via.
type LineageEdge struct {
	BatchID string    `json:"batch_id"`
	Parent  string    `json:"parent"`
	Child   string    `json:"child"`
	Via     string    `json:"via"`
	At      time.Time `json:"at"`
}

// Query filters a batch listing.
type Query struct {
	SourceID string
	// SourceIDs limits the listing to these sources (nil: all). It is how a
	// role scoped to some sources sees only those.
	SourceIDs []string
	Statuses  []string
	Limit     int
	Offset    int
}

// Summary is the dashboard's one-query overview.
type Summary struct {
	Batches     int            `json:"batches"`
	ByStatus    map[string]int `json:"by_status"`
	ByStage     [8]int         `json:"by_stage"` // active batches waiting at each stage, index = stage
	RowsIn      int            `json:"rows_in"`
	Quarantined int            `json:"quarantined"`
	Delivered   int            `json:"delivered"`
	// RowsLost is rows in minus quarantined minus delivered minus rows still
	// travelling. It is zero unless something is wrong.
	RowsLost int `json:"rows_lost"`
}

// Change is everything one step writes, committed atomically.
type Change struct {
	Source     *Source
	Role       *Role
	DeleteRole string
	// Counters are added to the durable counters in the same transaction, so a
	// counter and the state it counts can never disagree.
	Counters []CounterDelta
	Batch    *Batch // Revision 0 creates; otherwise updates with a revision check
	// KeepLease writes the batch without ending the worker's lease on it, for a
	// step that is not the end of the stage's work (recording that a delivery is
	// about to be attempted, or that one chunk of several is done).
	KeepLease   bool
	Checkpoints []Checkpoint
	Quarantine  []Quarantine
	Lineage     []LineageEdge
	Audit       []AuditEntry // Seq, PrevHash and Hash are filled in by the store
}

// Store persists everything. Commit is atomic and fails with ErrConflict when
// the batch's stored revision differs from the one passed in. Where a method
// takes sources, nil means every source and a list limits the answer to those.
type Store interface {
	Commit(ctx context.Context, c Change) error
	Ping(ctx context.Context) error
	GetSource(ctx context.Context, id string) (*Source, error)
	ListSources(ctx context.Context) ([]*Source, error)
	GetRole(ctx context.Context, id string) (*Role, error)
	ListRoles(ctx context.Context) ([]*Role, error)
	GetBatch(ctx context.Context, id string) (*Batch, error)
	FindBatch(ctx context.Context, sourceID, key string) (*Batch, error)
	ListBatches(ctx context.Context, q Query) ([]*Batch, error)
	// LatestCheckpoint returns the newest checkpoint of a batch, or ErrNotFound.
	LatestCheckpoint(ctx context.Context, batchID string) (*Checkpoint, error)
	ListQuarantine(ctx context.Context, batchID string, sources []string, limit int) ([]Quarantine, error)
	// ListAudit returns newest first. With sources set, entries about other
	// sources and entries about no source at all are left out.
	ListAudit(ctx context.Context, batchID string, sources []string, limit, offset int) ([]AuditEntry, error)
	// VerifyAudit walks the whole chain and returns the first bad sequence
	// number, or 0 when the chain is intact.
	VerifyAudit(ctx context.Context) (badSeq int64, total int64, err error)
	Lineage(ctx context.Context, batchID string) ([]LineageEdge, error)
	// Due lists in-flight and retrying batches whose next attempt is at or before now.
	Due(ctx context.Context, now time.Time, limit int) ([]*Batch, error)
	Summary(ctx context.Context, sources []string) (Summary, error)
	// Claim leases a batch to one worker for ttl, so its stage runs once at a
	// time across every process sharing the store. ErrLeased: someone has it.
	Claim(ctx context.Context, id, owner string, now time.Time, ttl time.Duration) (*Batch, error)
	// Release gives a lease back without committing (the stage did not run).
	Release(ctx context.Context, id, owner string) error
	// FindByHash returns an earlier batch of the source with the same content.
	FindByHash(ctx context.Context, sourceID, hash string) (*Batch, error)
	// Prune deletes the checkpoints of batches that finished (delivered or
	// failed) before the given time and returns how many it removed, with the
	// blob keys they referenced (for the caller to delete). Held batches keep
	// theirs: a replay needs them.
	Prune(ctx context.Context, before time.Time) (int, []string, error)
	// Counters returns every durable counter.
	Counters(ctx context.Context) ([]CounterRow, error)
	// RecordBreaker counts the result of a call to a destination and returns the
	// breaker's state: Threshold consecutive failures open it for Cooldown. The
	// state is shared by every process using the store.
	RecordBreaker(ctx context.Context, destination string, ok bool, now time.Time, threshold int, cooldown time.Duration) (BreakerState, error)
	Breakers(ctx context.Context) ([]BreakerState, error)
	// SyncAlerts makes the stored open alerts match the current ones: new ones
	// are opened, ones no longer true are cleared, the rest are updated. It
	// returns what changed so the caller can notify.
	SyncAlerts(ctx context.Context, current []Alert, now time.Time) (opened, cleared []AlertRecord, err error)
	// ListAlerts returns alert records, newest first; openOnly limits it to open ones.
	ListAlerts(ctx context.Context, openOnly bool, limit int) ([]AlertRecord, error)
	AckAlert(ctx context.Context, id, by, note string, until time.Time) error
	MarkNotified(ctx context.Context, rid string, at time.Time) error
	// SourceStats aggregates batches created since `since`, per source.
	SourceStats(ctx context.Context, since time.Time, sources []string) ([]SourceStat, error)
	// LatencySample returns up to limit end-to-end seconds of recently delivered batches of a source.
	LatencySample(ctx context.Context, sourceID string, since time.Time, limit int) ([]float64, error)
	// PruneCounters removes hourly counters older than the given hour (UTC, YYYYMMDDHH).
	PruneCounters(ctx context.Context, beforeHour string) (int, error)
	// Series buckets batches created since `since` into windows of `bucket`.
	Series(ctx context.Context, since time.Time, bucket time.Duration, sources []string) ([]Point, error)
}

// BreakerState is one destination's circuit breaker.
type BreakerState struct {
	Destination string    `json:"destination"`
	Failures    int       `json:"failures"`
	OpenUntil   time.Time `json:"open_until"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// AlertRecord is an alert with its life: when it opened and cleared, who
// acknowledged it, and whether anyone was told.
type AlertRecord struct {
	Alert
	RID        string    `json:"rid"`
	OpenedAt   time.Time `json:"opened_at"`
	ClearedAt  time.Time `json:"cleared_at"`
	AckedBy    string    `json:"acked_by,omitempty"`
	AckedNote  string    `json:"acked_note,omitempty"`
	AckedUntil time.Time `json:"acked_until"`
	NotifiedAt time.Time `json:"notified_at"`
}

// Acknowledged reports whether the alert is silenced at the given time.
func (a AlertRecord) Acknowledged(now time.Time) bool {
	return a.AckedBy != "" && a.AckedUntil.After(now)
}

// SourceStat is one source's batches over a window.
type SourceStat struct {
	SourceID    string    `json:"source_id"`
	Batches     int       `json:"batches"`
	RowsIn      int       `json:"rows_in"`
	Delivered   int       `json:"delivered"`
	Quarantined int       `json:"quarantined"`
	Held        int       `json:"held"`
	Retrying    int       `json:"retrying"`
	LastBatchAt time.Time `json:"last_batch_at"`
	AvgSeconds  float64   `json:"avg_seconds"`
}

// CounterDelta is an amount to add to a durable counter.
type CounterDelta struct {
	Name   string
	Labels string // sorted key="value" pairs, comma separated
	Value  float64
}

// CounterRow is a durable counter as stored.
type CounterRow struct {
	Name   string  `json:"name"`
	Labels string  `json:"labels"`
	Value  float64 `json:"value"`
}

// Point is one window of the throughput series.
type Point struct {
	At          time.Time `json:"at"`
	Batches     int       `json:"batches"`
	RowsIn      int       `json:"rows_in"`
	Delivered   int       `json:"delivered"`
	Quarantined int       `json:"quarantined"`
	Held        int       `json:"held"`
	Failed      int       `json:"failed"`
}

// Dur is a duration that reads and writes as text such as "1h" (a number is seconds).
type Dur time.Duration

func (d Dur) MarshalJSON() ([]byte, error) { return json.Marshal(time.Duration(d).String()) }

func (d *Dur) UnmarshalJSON(b []byte) error {
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	switch x := v.(type) {
	case nil:
		*d = 0
	case float64:
		*d = Dur(time.Duration(x * float64(time.Second)))
	case string:
		p, err := time.ParseDuration(x)
		if err != nil {
			return fmt.Errorf("%w: bad duration %q", ErrInvalid, x)
		}
		*d = Dur(p)
	default:
		return fmt.Errorf("%w: bad duration %v", ErrInvalid, v)
	}
	return nil
}

// retryJSON is RetryPolicy on the wire: durations as strings such as "2s".
type retryJSON struct {
	MaxAttempts int `json:"max_attempts"`
	Base        any `json:"base"`
	Cap         any `json:"cap"`
}

func (r RetryPolicy) MarshalJSON() ([]byte, error) {
	return json.Marshal(retryJSON{r.MaxAttempts, r.Base.String(), r.Cap.String()})
}

// UnmarshalJSON accepts durations as "2s" strings or as seconds.
func (r *RetryPolicy) UnmarshalJSON(b []byte) error {
	var w retryJSON
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	conv := func(v any) (time.Duration, error) {
		switch x := v.(type) {
		case nil:
			return 0, nil
		case float64:
			return time.Duration(x * float64(time.Second)), nil
		case string:
			return time.ParseDuration(x)
		}
		return 0, fmt.Errorf("%w: bad duration %v", ErrInvalid, v)
	}
	var err error
	r.MaxAttempts = w.MaxAttempts
	if r.Base, err = conv(w.Base); err != nil {
		return err
	}
	r.Cap, err = conv(w.Cap)
	return err
}
