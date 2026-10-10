package etl

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// maxQuarantineEntries bounds how many refused-row entries one batch keeps; the
// count of refused rows is exact, the listing of them is capped.
const maxQuarantineEntries = 2000

// Ingest validates rows from a source and creates a batch. A repeated key with
// the same content returns the original batch; with different content it is
// ErrKeyReused. Rows that break a rule are quarantined with the reason; if
// they exceed the source's reject rate the whole batch is failed and nothing
// moves. Oversize uploads and (when the source says so) duplicate content are
// refused before any row is stored.
func (e *Engine) Ingest(ctx context.Context, actor Actor, sourceID, key string, rows []Row) (*IngestResult, error) {
	hash := RowsHash(rows)
	return e.ingest(ctx, actor, sourceID, key, SliceIter(rows), len(rows), func() string { return hash })
}

// IngestStream is Ingest for a file too large to hold: rows are read, checked
// and stored a chunk at a time, so memory stays at one chunk whatever the size.
// The content hash is the SHA-256 of the bytes read. Chunks need a Blobs store.
func (e *Engine) IngestStream(ctx context.Context, actor Actor, sourceID, key string, r io.Reader) (*IngestResult, error) {
	src, err := e.Store.GetSource(ctx, sourceID)
	if err != nil {
		if _, perr := e.need(ctx, actor, PermIngest, ""); perr != nil {
			return nil, perr
		}
		return nil, err
	}
	h := sha256.New()
	next, err := NewRowIter(src.Format, io.TeeReader(r, h))
	if err != nil {
		return nil, err
	}
	return e.ingest(ctx, actor, sourceID, key, next, -1, func() string { return hex.EncodeToString(h.Sum(nil)) })
}

// IngestObject ingests a file that is already in the blob store, by name. Only
// names under the inbox prefix (default "inbox/") are readable this way, so it
// cannot be used to read another batch's checkpoints.
func (e *Engine) IngestObject(ctx context.Context, actor Actor, sourceID, key, object string) (*IngestResult, error) {
	if _, err := e.need(ctx, actor, PermIngest, sourceID); err != nil {
		return nil, err
	}
	if e.Blobs == nil {
		return nil, fmt.Errorf("%w: no object store is configured", ErrInvalid)
	}
	prefix := e.InboxPrefix
	if prefix == "" {
		prefix = "inbox/"
	}
	if !ValidBlobKey(object) || !strings.HasPrefix(object, prefix) {
		return nil, fmt.Errorf("%w: an object to ingest must be named under %s", ErrInvalid, prefix)
	}
	rc, err := e.Blobs.Open(ctx, object)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return e.IngestStream(ctx, actor, sourceID, key, rc)
}

func (e *Engine) ingest(ctx context.Context, actor Actor, sourceID, key string, next RowIter, known int, hash func() string) (*IngestResult, error) {
	e.init()
	if _, err := e.need(ctx, actor, PermIngest, sourceID); err != nil {
		return nil, err
	}
	if strings.TrimSpace(key) == "" {
		return nil, fmt.Errorf("%w: an idempotency key is required", ErrInvalid)
	}
	src, err := e.Store.GetSource(ctx, sourceID)
	if err != nil {
		return nil, err
	}
	if src.Paused {
		return nil, fmt.Errorf("%w: source %q is paused", ErrState, sourceID)
	}
	limit := src.MaxRows
	if limit <= 0 {
		limit = 100000
		if e.Blobs != nil {
			limit = 10000000
		}
	}
	if known > limit {
		e.refuse(ctx, sourceID, "too_many_rows", actor.ID)
		return nil, fmt.Errorf("%w: %d rows is more than the %d this source accepts in one batch", ErrInvalid, known, limit)
	}
	if known < 0 && e.Blobs == nil {
		return nil, fmt.Errorf("%w: ingesting a stream needs a blob store for its chunks", ErrInvalid)
	}

	// The same key again: the original batch if the content is the same. For a
	// stream the content is only known once it has been read, so it is read and
	// dropped.
	if prior, err := e.Store.FindBatch(ctx, sourceID, key); err == nil {
		if known < 0 {
			for {
				if _, err := next(); err != nil {
					if !errors.Is(err, io.EOF) {
						return nil, err
					}
					break
				}
			}
		}
		if prior.ContentHash != hash() {
			return nil, ErrKeyReused
		}
		return &IngestResult{Batch: prior, Duplicate: true}, nil
	} else if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	if src.RejectDuplicates && known >= 0 {
		if other, err := e.Store.FindByHash(ctx, sourceID, hash()); err == nil {
			e.refuse(ctx, sourceID, "duplicate_content", actor.ID)
			return nil, fmt.Errorf("%w: batch %s already carries exactly this content (sent under key %q)", ErrDuplicate, other.ID, other.Key)
		} else if !errors.Is(err, ErrNotFound) {
			return nil, err
		}
	}

	started := time.Now()
	val, err := NewValidator(src, e.Eval)
	if err != nil {
		return nil, err
	}
	now := e.now()
	trace, _ := ctx.Value(traceKey{}).(string)
	if trace == "" {
		trace = newID("t_")
	}
	id := newID("b_")

	var (
		buf      []Row
		refs     []ChunkRef
		stored   []string
		rejects  []Quarantine
		rowsIn   int
		refused  int
		accepted int
	)
	cleanup := func() { e.deleteBlobs(context.WithoutCancel(ctx), stored) }
	flush := func() error {
		ref, err := e.putChunk(ctx, id, StageIngest, len(refs), buf)
		if err != nil {
			return err
		}
		refs, stored, buf = append(refs, ref), append(stored, ref.Key), nil
		return nil
	}
	for {
		row, err := next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			cleanup()
			return nil, err
		}
		rowsIn++
		if rowsIn > limit {
			cleanup()
			e.refuse(ctx, sourceID, "too_many_rows", actor.ID)
			return nil, fmt.Errorf("%w: more than the %d rows this source accepts in one batch", ErrInvalid, limit)
		}
		if broke := val.Check(rowsIn, row); len(broke) > 0 {
			refused++
			for _, q := range broke {
				if len(rejects) < maxQuarantineEntries {
					q.BatchID, q.SourceID, q.At = id, sourceID, now
					rejects = append(rejects, q)
				}
			}
			continue
		}
		accepted++
		buf = append(buf, row)
		if e.Blobs != nil && len(buf) >= e.chunkRows() {
			if err := flush(); err != nil {
				cleanup()
				return nil, err
			}
		}
	}
	if len(refs) > 0 && len(buf) > 0 {
		if err := flush(); err != nil {
			cleanup()
			return nil, err
		}
	}
	took := time.Since(started)
	contentHash := hash()
	if src.RejectDuplicates && known < 0 {
		if other, err := e.Store.FindByHash(ctx, sourceID, contentHash); err == nil {
			cleanup()
			e.refuse(ctx, sourceID, "duplicate_content", actor.ID)
			return nil, fmt.Errorf("%w: batch %s already carries exactly this content (sent under key %q)", ErrDuplicate, other.ID, other.Key)
		} else if !errors.Is(err, ErrNotFound) {
			cleanup()
			return nil, err
		}
	}

	b := &Batch{ID: id, SourceID: sourceID, SourceVersion: src.Version, Key: key, ContentHash: contentHash, TraceID: trace, Actor: actor.ID, RowsIn: rowsIn,
		Quarantined: refused, Stage: StageTransform, Status: StatusInFlight, Chunks: len(refs), CreatedAt: now, UpdatedAt: now}
	ch := Change{Batch: b, Quarantine: rejects}
	note := func(kind, msg string) {
		b.Events = append(b.Events, Event{At: now, Stage: StageIngest, Kind: kind, Message: msg, DurationMs: took.Milliseconds(), Attempt: 1})
	}
	note("info", fmt.Sprintf("Received %d rows from %s", rowsIn, sourceID))
	failed := rowsIn > 0 && refused > 0 && float64(refused)/float64(rowsIn) > src.MaxRejectRate
	if failed {
		cleanup()
		b.Status, b.Stage, b.FinishedAt, b.Chunks = StatusFailed, StageIngest, now, 0
		b.LastError = fmt.Sprintf("%d of %d rows were refused, above the limit of %.0f%%", refused, rowsIn, src.MaxRejectRate*100)
		note("failed", b.LastError+"; nothing was moved")
		ch.Audit = append(ch.Audit, e.audit(actor.ID, "batch.fail", b.ID, sourceID, trace, b.LastError))
	} else {
		cp := Checkpoint{BatchID: b.ID, Stage: StageIngest, Count: accepted, At: now}
		if len(refs) > 0 {
			cp.Chunks, cp.Hash = refs, chunksHash(refs)
			note("checkpoint", fmt.Sprintf("Validated: %d passed, %d quarantined; checkpoint written (%d chunks)", accepted, refused, len(refs)))
		} else {
			cp.Rows, cp.Hash = buf, RowsHash(buf)
			note("checkpoint", fmt.Sprintf("Validated: %d passed, %d quarantined; checkpoint written", accepted, refused))
		}
		ch.Checkpoints = []Checkpoint{cp}
		ch.Lineage = []LineageEdge{{BatchID: b.ID, Parent: "source:" + sourceID, Child: "batch:" + b.ID, Via: "ingest", At: now}}
		ch.Audit = append(ch.Audit, e.audit(actor.ID, "batch.ingest", b.ID, sourceID, trace, fmt.Sprintf("%d rows, %d quarantined", rowsIn, refused)))
	}
	t := NewTally()
	t.Add("etl_batches_ingested_total", 1, "source", sourceID)
	t.Add("etl_rows_total", float64(rowsIn), "source", sourceID, "kind", "in")
	t.Add("etl_rows_total", float64(refused), "source", sourceID, "kind", "quarantined")
	t.Observe("etl_stage_duration_seconds", took.Seconds(), "stage", "1")
	ch.Counters = t.Deltas()
	if err := e.Store.Commit(ctx, ch); err != nil {
		cleanup()
		if errors.Is(err, ErrConflict) { // a concurrent ingest with the same key won
			if prior, ferr := e.Store.FindBatch(ctx, sourceID, key); ferr == nil && prior.ContentHash == contentHash {
				return &IngestResult{Batch: prior, Duplicate: true}, nil
			}
		}
		return nil, err
	}
	if failed {
		e.log("warn", "batch refused at intake", b, StageIngest, "error", b.LastError, "rows", rowsIn, "quarantined", refused)
	} else {
		e.log("info", "batch accepted", b, StageIngest, "rows", rowsIn, "quarantined", refused, "chunks", len(refs), "actor", actor.ID)
	}
	return &IngestResult{Batch: b}, nil
}

// chunksHash is the hash of a chunked checkpoint: the hashes of its chunks, in order.
func chunksHash(refs []ChunkRef) string {
	hs := make([]string, len(refs))
	for i, r := range refs {
		hs[i] = r.Hash
	}
	sum := sha256.Sum256([]byte(strings.Join(hs, ",")))
	return hex.EncodeToString(sum[:])
}
