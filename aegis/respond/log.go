package respond

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"
	"time"
)

// Entry is one record in the evidence log. Each hash covers the previous
// one, so editing or dropping any record breaks every later hash.
type Entry struct {
	Seq    int
	Time   time.Time
	Event  string
	Detail string
	Prev   string
	Hash   string
}

// Log is an append-only hash chain. In memory it only detects tampering by
// whoever can reach the process; ship entries (and the latest hash) to
// storage the application cannot write for the guarantee to mean anything.
type Log struct {
	mu      sync.Mutex
	entries []Entry
	sink    func(Entry)
}

// SetSink streams each new entry to fn (stderr, a file, a remote collector)
// as it is appended. fn must not call back into the Log.
func (l *Log) SetSink(fn func(Entry)) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sink = fn
}

func NewLog() *Log { return &Log{} }

func (l *Log) Append(at time.Time, event, detail string) Entry {
	l.mu.Lock()
	defer l.mu.Unlock()
	e := Entry{Seq: len(l.entries), Time: at.UTC(), Event: event, Detail: detail}
	if n := len(l.entries); n > 0 {
		e.Prev = l.entries[n-1].Hash
	}
	e.Hash = hashEntry(e)
	l.entries = append(l.entries, e)
	if l.sink != nil {
		l.sink(e)
	}
	return e
}

// Entries returns a copy.
func (l *Log) Entries() []Entry {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]Entry(nil), l.entries...)
}

// Verify walks the chain and reports the first broken link.
func Verify(entries []Entry) error {
	prev := ""
	for i, e := range entries {
		if e.Seq != i || e.Prev != prev || e.Hash != hashEntry(e) {
			return fmt.Errorf("respond: evidence log broken at entry %d", i)
		}
		prev = e.Hash
	}
	return nil
}

func hashEntry(e Entry) string {
	h := sha256.New()
	fmt.Fprintf(h, "%d|%d|%s|%s|%s", e.Seq, e.Time.UnixNano(), e.Event, e.Detail, e.Prev)
	return hex.EncodeToString(h.Sum(nil))
}
