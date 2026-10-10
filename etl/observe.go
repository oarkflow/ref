package etl

import (
	"strings"
	"sync"
	"time"
)

// LogEntry is one structured log record, kept in memory so the console can show
// recent activity and filter it by batch, source or trace.
type LogEntry struct {
	At       time.Time      `json:"at"`
	Level    string         `json:"level"` // debug, info, warn, error
	Message  string         `json:"message"`
	BatchID  string         `json:"batch_id,omitempty"`
	SourceID string         `json:"source_id,omitempty"`
	TraceID  string         `json:"trace_id,omitempty"`
	Stage    int            `json:"stage,omitempty"`
	Attrs    map[string]any `json:"attrs,omitempty"`
}

// Logbook is a fixed-size ring of recent log entries.
type Logbook struct {
	mu   sync.Mutex
	ring []LogEntry
	next int
	full bool
}

func NewLogbook(size int) *Logbook { return &Logbook{ring: make([]LogEntry, max(size, 16))} }

func (l *Logbook) Add(e LogEntry) {
	l.mu.Lock()
	l.ring[l.next] = e
	l.next = (l.next + 1) % len(l.ring)
	if l.next == 0 {
		l.full = true
	}
	l.mu.Unlock()
}

// LogFilter selects entries; empty fields match everything.
type LogFilter struct {
	Level    string // minimum level
	BatchID  string
	TraceID  string
	Sources  []string // nil: all
	Contains string
	Limit    int
}

var levelRank = map[string]int{"debug": 0, "info": 1, "warn": 2, "error": 3}

// Recent returns matching entries, newest first.
func (l *Logbook) Recent(f LogFilter) []LogEntry {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := l.next
	if l.full {
		n = len(l.ring)
	}
	var out []LogEntry
	for i := 1; i <= n; i++ {
		e := l.ring[(l.next-i+len(l.ring))%len(l.ring)]
		if levelRank[e.Level] < levelRank[f.Level] || (f.BatchID != "" && e.BatchID != f.BatchID) || (f.TraceID != "" && e.TraceID != f.TraceID) {
			continue
		}
		if f.Sources != nil && !contains(f.Sources, e.SourceID) {
			continue
		}
		if f.Contains != "" && !strings.Contains(strings.ToLower(e.Message+" "+e.BatchID), strings.ToLower(f.Contains)) {
			continue
		}
		out = append(out, e)
		if f.Limit > 0 && len(out) >= f.Limit {
			break
		}
	}
	return out
}
