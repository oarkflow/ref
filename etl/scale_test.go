package etl

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// flakyStore loses the commit that records a delivery's outcome, once: the
// destination has the data and this process never writes that down.
type flakyStore struct {
	Store
	lose atomic.Bool
}

func (f *flakyStore) Commit(ctx context.Context, c Change) error {
	if c.Batch != nil && c.Batch.Stage == StageAudit && f.lose.CompareAndSwap(true, false) {
		return errors.New("connection lost after the destination answered")
	}
	return f.Store.Commit(ctx, c)
}

// destination is a receiver that deduplicates on the delivery key and honours the fencing epoch.
type destination struct {
	mu      sync.Mutex
	data    map[string]int   // delivery key -> rows
	epochs  map[string]int64 // delivery key -> highest epoch accepted
	calls   int
	refused int
}

func newDestination() *destination {
	return &destination{data: map[string]int{}, epochs: map[string]int64{}}
}

func (d *destination) deliver(key string, epoch int64, rows int) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls++
	if epoch < d.epochs[key] {
		d.refused++
		return Permanent(errors.New("stale writer: a later delivery already took this"))
	}
	d.epochs[key] = epoch
	d.data[key] = rows
	return nil
}

func (d *destination) has(key string) (string, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, ok := d.data[key]
	return "dest/" + key, ok
}

func (d *destination) totalRows() (n int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, r := range d.data {
		n += r
	}
	return
}

func TestADeliveryCutOffBeforeItWasRecordedIsFoundNotRepeated(t *testing.T) {
	eachStore(t, func(t *testing.T, e *Engine, _ *clock) {
		ctx := context.Background()
		flaky := &flakyStore{Store: e.Store}
		e.Store = flaky
		dest := newDestination()
		e.Hooks.Deliver = func(_ context.Context, _ *Source, b *Batch, r []Row) (string, error) {
			if err := dest.deliver(b.DeliveryKey, b.Epoch, len(r)); err != nil {
				return "", err
			}
			return "dest/" + b.DeliveryKey, nil
		}
		verifies := 0
		e.Hooks.Verify = func(_ context.Context, _ *Source, b *Batch, r []Row) (string, bool, error) {
			verifies++
			ref, ok := dest.has(b.DeliveryKey)
			return ref, ok, nil
		}
		res, _ := e.Ingest(ctx, admin, "orders", "cut", rows(t, goodCSV))
		id := res.Batch.ID
		flaky.lose.Store(true)
		if _, err := e.RunAll(ctx, admin, id); err == nil {
			t.Fatal("the lost commit should surface as an error")
		}
		if dest.calls != 1 {
			t.Fatalf("destination calls %d", dest.calls)
		}
		b, _ := e.Store.GetBatch(ctx, id)
		if !b.Delivering || b.Epoch != 1 || b.Stage != StageDelivery {
			t.Fatalf("the interrupted call must be remembered as in doubt: %+v", b)
		}
		b, err := e.RunAll(ctx, admin, id)
		if err != nil || b.Status != StatusDelivered || b.Delivered != 4 {
			t.Fatalf("after recovery: %+v %v", b, err)
		}
		if dest.calls != 1 || verifies != 1 || dest.totalRows() != 4 {
			t.Fatalf("the destination must not be sent the batch twice: calls=%d verifies=%d rows=%d", dest.calls, verifies, dest.totalRows())
		}
		if counter(t, e.Store, "etl_deliveries_recovered_total", "*") != 1 {
			t.Fatal("the recovery should be counted")
		}
		var said bool
		for _, ev := range b.Events {
			said = said || strings.Contains(ev.Message, "not sent again")
		}
		if !said {
			t.Fatalf("the timeline should say so: %+v", b.Events)
		}
	})
}

func TestIfTheDestinationDoesNotHaveItTheInterruptedCallIsRepeatedWithANewEpoch(t *testing.T) {
	eachStore(t, func(t *testing.T, e *Engine, _ *clock) {
		ctx := context.Background()
		flaky := &flakyStore{Store: e.Store}
		e.Store = flaky
		dest := newDestination()
		drop := true
		e.Hooks.Deliver = func(_ context.Context, _ *Source, b *Batch, r []Row) (string, error) {
			if drop { // the request never reached it
				drop = false
				return "", errors.New("connection reset")
			}
			return "dest/" + b.DeliveryKey, dest.deliver(b.DeliveryKey, b.Epoch, len(r))
		}
		e.Hooks.Verify = func(_ context.Context, _ *Source, b *Batch, r []Row) (string, bool, error) {
			ref, ok := dest.has(b.DeliveryKey)
			return ref, ok, nil
		}
		res, _ := e.Ingest(ctx, admin, "orders", "lost", rows(t, goodCSV))
		b, _ := e.RunAll(ctx, admin, res.Batch.ID) // fails once, retrying
		for i := 0; i < 5 && b.Status != StatusDelivered; i++ {
			e.now() // clock is fixed: make the retry due
			b.NextAttemptAt = time.Time{}
			e.Store.Commit(ctx, Change{Batch: b})
			b, _ = e.RunAll(ctx, admin, res.Batch.ID)
		}
		if b.Status != StatusDelivered || dest.calls != 1 || b.Epoch != 2 {
			t.Fatalf("a call that never arrived is made again, with the next epoch: %+v calls=%d", b, dest.calls)
		}
	})
}

func TestAStaleWriterIsRefusedByTheEpoch(t *testing.T) {
	dest := newDestination()
	if err := dest.deliver("k", 2, 5); err != nil {
		t.Fatal(err)
	}
	if err := dest.deliver("k", 1, 5); err == nil || dest.refused != 1 {
		t.Fatal("a call with an older epoch must be refused")
	}
}

type genCSV struct {
	i, n  int
	bad   map[int]bool
	buf   []byte
	wrote bool
}

func (g *genCSV) Read(p []byte) (int, error) {
	for len(g.buf) < len(p) && g.i <= g.n {
		if !g.wrote {
			g.buf = append(g.buf, "id,amount,currency,email\n"...)
			g.wrote = true
		}
		cur := "NPR"
		if g.bad[g.i] {
			cur = "XXX"
		}
		g.buf = append(g.buf, fmt.Sprintf("%d,%d.5,%s,c%d@example.com\n", g.i, g.i%900+1, cur, g.i)...)
		g.i++
	}
	if len(g.buf) == 0 {
		return 0, io.EOF
	}
	n := copy(p, g.buf)
	g.buf = g.buf[n:]
	return n, nil
}

func TestALargeBatchIsWorkedAChunkAtATimeAndKeptOutOfTheDatabase(t *testing.T) {
	eachStore(t, func(t *testing.T, e *Engine, clk *clock) {
		ctx := context.Background()
		blobs := NewMemBlobs()
		e.Blobs, e.ChunkRows = blobs, 100
		src := ordersSource()
		src.MaxRejectRate = 0.5
		src.MaxRows = 0
		e.PutSource(ctx, admin, src)
		maxRows, transforms := 0, 0
		var mu sync.Mutex
		note := func(n int) {
			mu.Lock()
			maxRows = max(maxRows, n)
			mu.Unlock()
		}
		e.Hooks.Transform = func(_ context.Context, _ *Source, b *Batch, r []Row) ([]Row, string, error) {
			note(len(r))
			transforms++
			return r, "v1", nil
		}
		dest := newDestination()
		failAt := atomic.Int32{}
		failAt.Store(5) // the sixth chunk's delivery fails once
		e.Hooks.Deliver = func(_ context.Context, _ *Source, b *Batch, r []Row) (string, error) {
			note(len(r))
			if b.Chunk == int(failAt.Load()) && b.Attempts == 0 {
				return "", errors.New("destination hiccup")
			}
			return "dest/" + b.DeliveryKey, dest.deliver(b.DeliveryKey, b.Epoch, len(r))
		}
		e.Hooks.Verify = func(_ context.Context, _ *Source, b *Batch, r []Row) (string, bool, error) {
			ref, ok := dest.has(b.DeliveryKey)
			return ref, ok, nil
		}
		bad := map[int]bool{7: true, 250: true, 777: true}
		res, err := e.IngestStream(ctx, admin, "orders", "big", &genCSV{i: 1, n: 1050, bad: bad})
		if err != nil {
			t.Fatal(err)
		}
		b := res.Batch
		if b.RowsIn != 1050 || b.Quarantined != 3 || b.Chunks != 11 {
			t.Fatalf("ingest: %+v", b)
		}
		if s, ok := e.Store.(*SQLStore); ok {
			var size int
			if err := s.db.QueryRow(s.q(`SELECT SUM(LENGTH(doc)) FROM {p}checkpoints WHERE batch_id = ?`), b.ID).Scan(&size); err != nil {
				t.Fatal(err)
			}
			if size > 6000 {
				t.Fatalf("the database holds %d bytes of checkpoint for 1047 rows: rows must be in the blob store", size)
			}
		}
		if len(blobs.Keys()) != 11 {
			t.Fatalf("blobs: %d", len(blobs.Keys()))
		}
		got, _ := e.RunAll(ctx, admin, b.ID)
		for i := 0; i < 6 && got.Status != StatusDelivered; i++ {
			got.NextAttemptAt = time.Time{}
			e.Store.Commit(ctx, Change{Batch: got})
			got, _ = e.RunAll(ctx, admin, b.ID)
		}
		if got.Status != StatusDelivered || got.Delivered != 1047 || dest.totalRows() != 1047 || len(dest.data) != 11 {
			t.Fatalf("delivery: %+v rows=%d keys=%d", got, dest.totalRows(), len(dest.data))
		}
		if maxRows > 100 {
			t.Fatalf("a hook saw %d rows at once: chunks must bound memory", maxRows)
		}
		// 6 chunks done, the 6th failed once and was retried: 11 chunks, 12 delivery calls... one never arrived.
		if dest.calls != 11 {
			t.Fatalf("each chunk should reach the destination once, got %d calls", dest.calls)
		}
		if transforms != 11 {
			t.Fatalf("transform calls %d", transforms)
		}
		// Retention removes the blobs with the checkpoints.
		clk.advance(40 * 24 * time.Hour)
		if _, err := e.Prune(ctx, 30*24*time.Hour); err != nil {
			t.Fatal(err)
		}
		if left := blobs.Keys(); len(left) != 0 {
			t.Fatalf("blobs left after retention: %d", len(left))
		}
		if sum, _ := e.Summary(ctx, admin); sum.RowsLost != 0 {
			t.Fatalf("summary %+v", sum)
		}
	})
}

func TestFilesAreIngestedByNameFromTheInboxOnly(t *testing.T) {
	eachStore(t, func(t *testing.T, e *Engine, _ *clock) {
		ctx := context.Background()
		blobs := NewMemBlobs()
		e.Blobs, e.ChunkRows = blobs, 50
		blobs.Put(ctx, "inbox/orders-1.csv", []byte(goodCSV))
		blobs.Put(ctx, "secret/other.csv", []byte(goodCSV))
		if _, err := e.IngestObject(ctx, admin, "orders", "o1", "secret/other.csv"); !errors.Is(err, ErrInvalid) {
			t.Fatalf("outside the inbox: %v", err)
		}
		if _, err := e.IngestObject(ctx, admin, "orders", "o1", "inbox/../secret/other.csv"); !errors.Is(err, ErrInvalid) {
			t.Fatalf("a dot segment: %v", err)
		}
		res, err := e.IngestObject(ctx, admin, "orders", "o1", "inbox/orders-1.csv")
		if err != nil {
			t.Fatal(err)
		}
		if b, err := e.RunAll(ctx, admin, res.Batch.ID); err != nil || b.Delivered != 4 {
			t.Fatalf("%+v %v", b, err)
		}
		// The same file again under the same key is the same batch; changed content is refused.
		again, err := e.IngestObject(ctx, admin, "orders", "o1", "inbox/orders-1.csv")
		if err != nil || !again.Duplicate || again.Batch.ID != res.Batch.ID {
			t.Fatalf("same file, same key: %+v %v", again, err)
		}
		blobs.Put(ctx, "inbox/orders-1.csv", []byte(goodCSV+"9,1,NPR,a@b.c\n"))
		if _, err := e.IngestObject(ctx, admin, "orders", "o1", "inbox/orders-1.csv"); !errors.Is(err, ErrKeyReused) {
			t.Fatalf("a changed file under the same key: %v", err)
		}
		if _, err := e.IngestObject(ctx, reader, "orders", "o2", "inbox/orders-1.csv"); !errors.Is(err, ErrForbidden) {
			t.Fatalf("without the permission: %v", err)
		}
	})
}

func TestBlobKeysCannotEscapeTheirDirectory(t *testing.T) {
	for _, bad := range []string{"", "/etc/passwd", "../x", "a/../b", "a//b", "a/./b", "a b", "a\\b", "a\x00b"} {
		if ValidBlobKey(bad) {
			t.Errorf("%q must be refused", bad)
		}
	}
	for _, ok := range []string{"inbox/orders-1.csv", "batches/b_abc/2/000001.json.gz"} {
		if !ValidBlobKey(ok) {
			t.Errorf("%q must be accepted", ok)
		}
	}
	dir := t.TempDir()
	fb, err := NewFileBlobs(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := fb.Put(ctx, "a/b/c.bin", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if got, err := fb.Get(ctx, "a/b/c.bin"); err != nil || string(got) != "x" {
		t.Fatal(got, err)
	}
	if err := fb.Put(ctx, "../escape", []byte("x")); err == nil {
		t.Fatal("a key that climbs out of the directory must be refused")
	}
	if _, err := fb.Get(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if err := fb.Delete(ctx, "a/b/c.bin"); err != nil {
		t.Fatal(err)
	}
}
