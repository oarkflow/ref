package etl

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Blobs holds what is too big for the database: the chunks of a large batch's
// checkpoints, and the files that are ingested by name. A directory (FileBlobs)
// and any object store (the platform adapts its storage resources) provide it.
type Blobs interface {
	Put(ctx context.Context, key string, data []byte) error
	Get(ctx context.Context, key string) ([]byte, error)
	Delete(ctx context.Context, key string) error
	// Open streams a stored object, for a file too large to read whole.
	Open(ctx context.Context, key string) (io.ReadCloser, error)
}

// ValidBlobKey says whether a key is safe to use as a path: letters, digits and
// _ . - / only, no empty or dot segments.
func ValidBlobKey(key string) bool {
	if key == "" || len(key) > 512 || strings.HasPrefix(key, "/") {
		return false
	}
	for _, seg := range strings.Split(key, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	for i := 0; i < len(key); i++ {
		c := key[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '.' || c == '-' || c == '/') {
			return false
		}
	}
	return true
}

// FileBlobs keeps blobs as files under a directory. A write goes to a temporary
// file that is renamed into place, so a reader never sees half a blob.
type FileBlobs struct{ dir string }

func NewFileBlobs(dir string) (*FileBlobs, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &FileBlobs{dir: dir}, nil
}

func (f *FileBlobs) path(key string) (string, error) {
	if !ValidBlobKey(key) {
		return "", fmt.Errorf("%w: bad blob key %q", ErrInvalid, key)
	}
	return filepath.Join(f.dir, filepath.FromSlash(key)), nil
}

func (f *FileBlobs) Put(ctx context.Context, key string, data []byte) error {
	p, err := f.path(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".blob-*")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(data)
	serr := tmp.Sync()
	cerr := tmp.Close()
	if err := errors.Join(werr, serr, cerr); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), p)
}

func (f *FileBlobs) Get(ctx context.Context, key string) ([]byte, error) {
	p, err := f.path(key)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	return b, err
}

func (f *FileBlobs) Delete(ctx context.Context, key string) error {
	p, err := f.path(key)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (f *FileBlobs) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	p, err := f.path(key)
	if err != nil {
		return nil, err
	}
	r, err := os.Open(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	return r, err
}

// MemBlobs is an in-process Blobs for tests.
type MemBlobs struct {
	mu sync.Mutex
	m  map[string][]byte
}

func NewMemBlobs() *MemBlobs { return &MemBlobs{m: map[string][]byte{}} }

func (b *MemBlobs) Put(ctx context.Context, key string, data []byte) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.m[key] = append([]byte(nil), data...)
	return nil
}

func (b *MemBlobs) Get(ctx context.Context, key string) ([]byte, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	d, ok := b.m[key]
	if !ok {
		return nil, ErrNotFound
	}
	return append([]byte(nil), d...), nil
}

func (b *MemBlobs) Delete(ctx context.Context, key string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.m, key)
	return nil
}

func (b *MemBlobs) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	d, err := b.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	return io.NopCloser(bytes.NewReader(d)), nil
}

// Keys lists what is stored (tests).
func (b *MemBlobs) Keys() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]string, 0, len(b.m))
	for k := range b.m {
		out = append(out, k)
	}
	return out
}

func (e *Engine) chunkRows() int {
	if e.ChunkRows > 0 {
		return e.ChunkRows
	}
	return 5000
}

func packRows(rows []Row) ([]byte, error) {
	raw, err := json.Marshal(rows)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(raw); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func unpackRows(b []byte) ([]Row, error) {
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	raw, err := io.ReadAll(zr)
	if err != nil {
		return nil, err
	}
	var rows []Row
	return rows, json.Unmarshal(raw, &rows)
}

// putChunk stores one chunk of rows and returns its reference.
func (e *Engine) putChunk(ctx context.Context, batchID string, stage, i int, rows []Row) (ChunkRef, error) {
	if e.Blobs == nil {
		return ChunkRef{}, errors.New("etl: a large batch needs a blob store")
	}
	data, err := packRows(rows)
	if err != nil {
		return ChunkRef{}, err
	}
	key := fmt.Sprintf("batches/%s/%d/%06d.json.gz", batchID, stage, i)
	if err := e.Blobs.Put(ctx, key, data); err != nil {
		return ChunkRef{}, fmt.Errorf("etl: storing chunk %d: %w", i, err)
	}
	return ChunkRef{Key: key, Rows: len(rows), Hash: RowsHash(rows)}, nil
}

// numChunks is how many pieces a checkpoint's rows are in (an inline
// checkpoint is one).
func numChunks(cp *Checkpoint) int {
	if len(cp.Chunks) == 0 {
		return 1
	}
	return len(cp.Chunks)
}

// loadChunk reads chunk i of a checkpoint and checks it against its hash, so a
// damaged or swapped blob is caught instead of being delivered.
func (e *Engine) loadChunk(ctx context.Context, cp *Checkpoint, i int) ([]Row, error) {
	if len(cp.Chunks) == 0 {
		return cp.Rows, nil
	}
	if e.Blobs == nil {
		return nil, errors.New("etl: this batch's checkpoint is in a blob store that is not configured")
	}
	ref := cp.Chunks[i]
	data, err := e.Blobs.Get(ctx, ref.Key)
	if err != nil {
		return nil, fmt.Errorf("etl: reading chunk %d of batch %s: %w", i, cp.BatchID, err)
	}
	rows, err := unpackRows(data)
	if err != nil {
		return nil, fmt.Errorf("etl: chunk %d of batch %s is damaged: %w", i, cp.BatchID, err)
	}
	if RowsHash(rows) != ref.Hash {
		return nil, fmt.Errorf("etl: chunk %d of batch %s does not match its hash", i, cp.BatchID)
	}
	return rows, nil
}

// chunkView is the batch as one hook call sees it: which chunk, of how many,
// and the idempotency key for that call.
func chunkView(b *Batch, i, n int) *Batch {
	c := *b
	c.Chunk, c.ChunkCount, c.DeliveryKey = i, n, b.Key
	if n > 1 {
		c.DeliveryKey = fmt.Sprintf("%s#%d", b.Key, i)
	}
	return &c
}

func (e *Engine) deleteBlobs(ctx context.Context, keys []string) {
	if e.Blobs == nil {
		return
	}
	for _, k := range keys {
		if err := e.Blobs.Delete(ctx, k); err != nil {
			e.log("warn", "could not delete a blob", nil, 0, "key", k, "error", err.Error())
		}
	}
}

// checkpointKeys lists the blob keys a checkpoint refers to.
func checkpointKeys(cp *Checkpoint) []string {
	keys := make([]string, 0, len(cp.Chunks))
	for _, c := range cp.Chunks {
		keys = append(keys, c.Key)
	}
	return keys
}
