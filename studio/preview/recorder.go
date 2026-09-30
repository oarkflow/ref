package preview

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/oarkflow/ref/platform/spi"
)

// RequestEntry is one request served by a preview generation (the detailed
// form; Requests converts to the shared studio.RecordedRequest).
type RequestEntry struct {
	Time       time.Time `json:"time"`
	Method     string    `json:"method"`
	Path       string    `json:"path"` // as the app saw it, without the /preview/{id} prefix
	Query      string    `json:"query,omitempty"`
	Status     int       `json:"status"`
	DurationMs float64   `json:"duration_ms"`
	Route      string    `json:"route,omitempty"`  // matched route block, derived from method+path
	Intent     string    `json:"intent,omitempty"` // that route's intent or process
	Version    int64     `json:"version"`          // draft version the generation was built from
}

// OutboundCall is a call the application made to the outside world that the
// preview answered itself.
type OutboundCall struct {
	Time    time.Time `json:"time"`
	Kind    string    `json:"kind"`             // "http" or "smtp"
	Target  string    `json:"target"`           // URL, or the recipients
	Method  string    `json:"method,omitempty"` // http only
	Preview string    `json:"preview,omitempty"`
	Bytes   int       `json:"bytes"`
}

const previewLimit = 512

type ring[T any] struct {
	mu   sync.Mutex
	max  int
	buf  []T
	next int
	full bool
}

func newRing[T any](max int) *ring[T] { return &ring[T]{max: max, buf: make([]T, 0, min(max, 64))} }

func (r *ring[T]) add(v T) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.buf) < r.max {
		r.buf = append(r.buf, v)
		return
	}
	r.buf[r.next] = v
	r.next = (r.next + 1) % r.max
	r.full = true
}

// snapshot returns the entries oldest first.
func (r *ring[T]) snapshot() []T {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.full {
		return append([]T(nil), r.buf...)
	}
	out := make([]T, 0, r.max)
	out = append(out, r.buf[r.next:]...)
	out = append(out, r.buf[:r.next]...)
	return out
}

type recorder struct {
	requests *ring[RequestEntry]
	outbound *ring[OutboundCall]
	seq      atomic.Int64
}

func newRecorder(n int) *recorder {
	return &recorder{requests: newRing[RequestEntry](n), outbound: newRing[OutboundCall](n)}
}

func truncate(b []byte) string {
	if len(b) <= previewLimit {
		return string(b)
	}
	return string(b[:previewLimit]) + fmt.Sprintf("… (%d more bytes)", len(b)-previewLimit)
}

// stubTransport answers every outbound HTTP call itself.
type stubTransport struct{ rec *recorder }

func (t stubTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	var body []byte
	if req.Body != nil {
		body, _ = io.ReadAll(io.LimitReader(req.Body, 1<<20))
		_ = req.Body.Close()
	}
	t.rec.outbound.add(OutboundCall{
		Time: time.Now(), Kind: "http", Method: req.Method, Target: req.URL.String(),
		Preview: truncate(body), Bytes: len(body),
	})
	payload := `{"preview":true,"stubbed":true}`
	return &http.Response{
		Status: "200 OK", StatusCode: http.StatusOK, Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
		Header:        http.Header{"Content-Type": {"application/json"}, "X-Preview-Stub": {"1"}},
		Body:          io.NopCloser(strings.NewReader(payload)),
		ContentLength: int64(len(payload)),
		Request:       req,
	}, nil
}

// stubMailer records mail instead of sending it.
type stubMailer struct{ rec *recorder }

func (m stubMailer) Send(_ context.Context, msg spi.Mail) (string, error) {
	if len(msg.To) == 0 {
		return "", fmt.Errorf("a message needs at least one recipient")
	}
	text := msg.Body
	if text == "" {
		text = msg.HTML
	}
	m.rec.outbound.add(OutboundCall{
		Time: time.Now(), Kind: "smtp", Target: strings.Join(msg.To, ", "),
		Preview: truncate([]byte("Subject: " + msg.Subject + "\n" + text)), Bytes: len(text),
	})
	return fmt.Sprintf("preview-msg-%d", m.rec.seq.Add(1)), nil
}
