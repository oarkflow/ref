package control

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"
)

// AuditLogger records, streams, and queries structured security events including
// authorization grant issuance, denials, revocations, and integrity signals.
type AuditLogger struct {
	mu        sync.RWMutex
	writer    io.Writer
	events    []SecurityEvent
	maxStored int
	listeners []func(SecurityEvent)
}

// NewAuditLogger creates a new AuditLogger that writes structured JSON event lines
// to writer (if non-nil) and retains up to 1000 recent events in memory for queries.
func NewAuditLogger(writer io.Writer) *AuditLogger {
	return &AuditLogger{
		writer:    writer,
		events:    make([]SecurityEvent, 0, 128),
		maxStored: 1000,
	}
}

// Log records a security event, assigns an ID and timestamp if empty, writes it to
// the underlying stream, and stores it in memory.
func (al *AuditLogger) Log(ctx context.Context, event SecurityEvent) {
	if event.ID == "" {
		event.ID = newEventID()
	}
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
	}

	al.mu.Lock()
	defer al.mu.Unlock()

	// Append and trim in-memory history if exceeding maxStored
	al.events = append(al.events, event)
	if len(al.events) > al.maxStored {
		al.events = al.events[len(al.events)-al.maxStored:]
	}

	// Stream to writer as JSON line
	if al.writer != nil {
		if data, err := json.Marshal(event); err == nil {
			_, _ = fmt.Fprintf(al.writer, "%s\n", data)
		}
	}

	// Notify active listeners
	for _, listener := range al.listeners {
		if listener != nil {
			listener(event)
		}
	}
}

// Subscribe registers a callback invoked on each recorded security event.
func (al *AuditLogger) Subscribe(listener func(SecurityEvent)) {
	al.mu.Lock()
	defer al.mu.Unlock()
	al.listeners = append(al.listeners, listener)
}

// Events returns a copy of all retained security events in chronological order.
func (al *AuditLogger) Events() []SecurityEvent {
	al.mu.RLock()
	defer al.mu.RUnlock()

	result := make([]SecurityEvent, len(al.events))
	copy(result, al.events)
	return result
}

// EventsForPackage returns all security events matching the given package ID.
func (al *AuditLogger) EventsForPackage(packageID string) []SecurityEvent {
	al.mu.RLock()
	defer al.mu.RUnlock()

	var result []SecurityEvent
	for _, ev := range al.events {
		if ev.PackageID == packageID {
			result = append(result, ev)
		}
	}
	return result
}

// EventsForSession returns all security events matching the given session ID.
func (al *AuditLogger) EventsForSession(sessionID string) []SecurityEvent {
	al.mu.RLock()
	defer al.mu.RUnlock()

	var result []SecurityEvent
	for _, ev := range al.events {
		if ev.SessionID == sessionID {
			result = append(result, ev)
		}
	}
	return result
}

// EventsByType returns all security events matching the given event type.
func (al *AuditLogger) EventsByType(eventType string) []SecurityEvent {
	al.mu.RLock()
	defer al.mu.RUnlock()

	var result []SecurityEvent
	for _, ev := range al.events {
		if ev.EventType == eventType {
			result = append(result, ev)
		}
	}
	return result
}

// Clear flushes all in-memory events.
func (al *AuditLogger) Clear() {
	al.mu.Lock()
	defer al.mu.Unlock()
	al.events = al.events[:0]
}

// newEventID generates a random 16-character hexadecimal event identifier.
func newEventID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("evt-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}
