package etl

import (
	"context"
	"fmt"
	"time"
)

// The stages that move rows work a chunk at a time: a small batch is one chunk
// kept inline in the checkpoint, a large one is many chunks in the blob store.
// Either way a hook sees at most one chunk, so memory is bounded by the chunk
// size and not by the size of the batch.

// transformAll runs the transform over every chunk, checks each against the
// source's contract, and returns the new checkpoint (not yet stamped) with the
// transform's version and the number of rows produced.
func (e *Engine) transformAll(ctx context.Context, src *Source, b *Batch, cp *Checkpoint, t *Tally) (*Checkpoint, string, int, error) {
	n, version, total := numChunks(cp), "none", 0
	out := &Checkpoint{}
	var inline []Row
	for i := 0; i < n; i++ {
		rows, err := e.loadChunk(ctx, cp, i)
		if err != nil {
			return nil, "", 0, err
		}
		res := rows
		if e.Hooks.Transform != nil {
			bc := chunkView(b, i, n)
			err = e.guard(ctx, "transform", t, func(c context.Context) (err error) {
				var v string
				res, v, err = e.Hooks.Transform(c, src, bc, rows)
				if v != "" {
					version = v
				}
				return err
			})
			if err != nil {
				return nil, "", 0, err
			}
		}
		if err := e.checkContract(src, rows, res, t); err != nil {
			return nil, "", 0, err
		}
		total += len(res)
		if len(cp.Chunks) == 0 {
			inline = append(inline, res...)
			continue
		}
		ref, err := e.putChunk(ctx, b.ID, StageTransform, i, res)
		if err != nil {
			return nil, "", 0, err
		}
		out.Chunks = append(out.Chunks, ref)
	}
	out.Count = total
	if len(cp.Chunks) == 0 {
		out.Rows, out.Hash = inline, RowsHash(inline)
	} else {
		out.Hash = chunksHash(out.Chunks)
	}
	return out, version, total, nil
}

// transferAll offers every chunk to the receiving system.
func (e *Engine) transferAll(ctx context.Context, src *Source, b *Batch, cp *Checkpoint, t *Tally) error {
	n := numChunks(cp)
	for i := 0; i < n; i++ {
		rows, err := e.loadChunk(ctx, cp, i)
		if err != nil {
			return err
		}
		bc := chunkView(b, i, n)
		if err := e.guard(ctx, "transfer", t, func(c context.Context) error { return e.Hooks.Transfer(c, src, bc, rows) }); err != nil {
			return err
		}
	}
	return nil
}

// deliverAll delivers chunk by chunk and records its progress, so an
// interruption resumes at the next chunk and never repeats one that is done.
//
// A call to the destination can be cut off after the destination has acted and
// before this process has written down that it did. That is made safe in three
// steps. Just before each call the batch is marked as delivering and given a new
// epoch (and that is committed). If the call's outcome never gets recorded, the
// next run finds the batch still marked, asks the destination whether it has the
// chunk (Hooks.Verify) and records it as delivered if so, without sending it
// again. The epoch goes with every call, so a destination can refuse a late call
// from a worker that has since lost its lease. Together with the idempotency key
// this makes each chunk take effect once.
func (e *Engine) deliverAll(ctx context.Context, src *Source, b *Batch, cp *Checkpoint, t *Tally, now time.Time) (recovered int, err error) {
	n := numChunks(cp)
	if b.Chunks > 0 && b.ChunkDone == 0 {
		b.Delivered = 0
	}
	for i := b.ChunkDone; i < n; i++ {
		rows, lerr := e.loadChunk(ctx, cp, i)
		if lerr != nil {
			return recovered, lerr
		}
		bc := chunkView(b, i, n)
		if b.Delivering && b.DeliveringChunk == i && e.Hooks.Verify != nil {
			var ref string
			var found bool
			verr := e.guard(ctx, "verify", t, func(c context.Context) (err error) {
				ref, found, err = e.Hooks.Verify(c, src, bc, rows)
				return err
			})
			e.circuitRecord(ctx, src.Destination, now, verr, t)
			if verr != nil {
				return recovered, verr
			}
			if found {
				recovered++
				b.Delivered += len(rows)
				b.Ref, b.ChunkDone, b.Delivering = ref, i+1, false
				b.Events = append(b.Events, Event{At: now, Stage: StageDelivery, Kind: "info", Attempt: b.Attempts + 1,
					Message: fmt.Sprintf("Chunk %d of %d was already at %s (a delivery was interrupted before it was recorded); not sent again", i+1, n, src.Destination)})
				t.Add("etl_deliveries_recovered_total", 1, "source", b.SourceID)
				e.log("warn", "an interrupted delivery was found at the destination and not repeated", b, StageDelivery, "chunk", i, "key", bc.DeliveryKey)
				continue
			}
		}
		// About to call: record that, with a new epoch, before calling.
		b.Epoch++
		b.Delivering, b.DeliveringChunk = true, i
		bc.Epoch = b.Epoch
		if cerr := e.commit(ctx, Change{Batch: b, KeepLease: true}); cerr != nil {
			return recovered, cerr
		}
		var ref string
		derr := e.guard(ctx, "deliver", t, func(c context.Context) (err error) {
			ref, err = e.Hooks.Deliver(c, src, bc, rows)
			return err
		})
		e.circuitRecord(ctx, src.Destination, now, derr, t)
		if derr != nil {
			return recovered, derr // still marked delivering: the outcome is not known
		}
		b.Delivered += len(rows)
		b.Ref, b.ChunkDone, b.Delivering = ref, i+1, false
		if i+1 < n {
			if cerr := e.commit(ctx, Change{Batch: b, KeepLease: true}); cerr != nil {
				return recovered, cerr
			}
		}
	}
	return recovered, nil
}
