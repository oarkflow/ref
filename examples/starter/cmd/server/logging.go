package main

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sync/atomic"
	"time"

	"github.com/oarkflow/fh"
	"github.com/oarkflow/zlog"
)

// newLogger builds the one structured logger every part of this process
// writes through: HTTP access logs (httpAccessLog below), the DAG execution
// observer wired in main via zlog.NewSlogHandler, and anything else that
// wants zlog.Logger directly. Production gets JSON output at info level or
// above; anything else gets a readable console logger.
//
// When webhookURL is set, every one of those log lines is ALSO POSTed as
// JSON to it — the "easy plugin for a third-party tool" this starter ships
// for logging: point LOG_WEBHOOK_URL at Datadog's log intake, a Loki push
// gateway, Logtail, or any endpoint that accepts a JSON POST body, and
// nothing else in this file, or anywhere else, changes.
func newLogger(env, level, webhookURL, webhookAuth string) *zlog.Logger {
	var opts zlog.Options
	if env == "production" {
		opts = zlog.ProductionOptions("starter", env)
	} else {
		opts = zlog.Options{Level: zlog.DebugLevel, Sink: zlog.NewWriterSink(os.Stderr, zlog.NewConsoleEncoder(), zlog.TraceLevel)}
	}
	if lvl, ok := parseLevel(level); ok {
		opts.Level = lvl
	}
	if webhookURL != "" {
		webhook := zlog.NewWriterSink(newWebhookWriter(webhookURL, webhookAuth), zlog.NewJSONEncoder(), zlog.TraceLevel)
		opts.Sink = zlog.NewMultiSink(opts.Sink, webhook)
	}
	return zlog.New(opts)
}

// slogDurationFixHandler wraps an slog.Handler, rewriting every
// slog.KindDuration attribute into a pre-formatted string before it reaches
// the wrapped handler.
//
// github.com/oarkflow/zlog's own encoders (encoder_logfmt.go and
// encoder_json.go, both the KindDuration case) render a Duration attr as
// its raw int64 nanosecond count, not a us/ms/s-suffixed string — the same
// gap httpAccessLog above already works around for the HTTP access log
// line. zlog.NewSlogHandler bridges every observer/slog log line (every
// REF node/decision/effect/execution event) through that same broken
// encoder, so the workaround has to sit here too, once, rather than at
// every individual call site that might ever log a Duration.
type slogDurationFixHandler struct {
	slog.Handler
}

func (h slogDurationFixHandler) Handle(ctx context.Context, r slog.Record) error {
	fixed := slog.NewRecord(r.Time, r.Level, r.Message, r.PC)
	r.Attrs(func(a slog.Attr) bool {
		if a.Value.Kind() == slog.KindDuration {
			a = slog.String(a.Key, a.Value.Duration().String())
		}
		fixed.AddAttrs(a)
		return true
	})
	return h.Handler.Handle(ctx, fixed)
}

func (h slogDurationFixHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return slogDurationFixHandler{h.Handler.WithAttrs(attrs)}
}

func (h slogDurationFixHandler) WithGroup(name string) slog.Handler {
	return slogDurationFixHandler{h.Handler.WithGroup(name)}
}

// webhookWriter is an io.Writer where each Write is one already-encoded log
// line (see zlog.WriterSink.WriteRecord, which calls Write exactly once per
// record). It queues lines onto a bounded channel and ships them from one
// background goroutine, so a slow or unreachable third-party collector
// drops log lines rather than blocking request handling.
//
// A drop is silent to the log itself (writing "we dropped a log line" back
// into the pipeline that's already backed up would make things worse), but
// it is never silent to the operator: Dropped/Failed are visible counters,
// and the first drop and every 100th one after it get a line on stderr
// directly — bypassing this sink entirely — so a struggling collector shows
// up somewhere even if nothing is watching a metrics endpoint.
type webhookWriter struct {
	url     string
	auth    string
	client  *http.Client
	queue   chan []byte
	dropped atomic.Uint64
	failed  atomic.Uint64
}

func newWebhookWriter(url, auth string) *webhookWriter {
	w := &webhookWriter{
		url:    url,
		auth:   auth,
		client: &http.Client{Timeout: 5 * time.Second},
		queue:  make(chan []byte, 1024),
	}
	go w.run()
	return w
}

// Dropped is how many log lines never made it onto the delivery queue
// because it was full — the collector, or the network to it, is behind.
func (w *webhookWriter) Dropped() uint64 { return w.dropped.Load() }

// Failed is how many queued deliveries the collector itself rejected or
// never answered (a network error, a non-2xx response, ...).
func (w *webhookWriter) Failed() uint64 { return w.failed.Load() }

func (w *webhookWriter) Write(p []byte) (int, error) {
	line := append([]byte(nil), p...) // WriterSink reuses its buffer; this must outlive the call
	select {
	case w.queue <- line:
	default:
		// The collector is behind; drop rather than stall logging on it.
		n := w.dropped.Add(1)
		if n == 1 || n%100 == 0 {
			fmt.Fprintf(os.Stderr, "starter: log webhook queue full, dropped %d line(s) so far (target %s)\n", n, w.url)
		}
	}
	return len(p), nil
}

func (w *webhookWriter) run() {
	for line := range w.queue {
		if err := w.deliver(line); err != nil {
			n := w.failed.Add(1)
			if n == 1 || n%100 == 0 {
				fmt.Fprintf(os.Stderr, "starter: log webhook delivery failed %d time(s) so far (target %s): %v\n", n, w.url, err)
			}
		}
	}
}

func (w *webhookWriter) deliver(line []byte) error {
	req, err := http.NewRequest(http.MethodPost, w.url, bytes.NewReader(line))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if w.auth != "" {
		req.Header.Set("Authorization", w.auth)
	}
	resp, err := w.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("collector returned %s", resp.Status)
	}
	return nil
}

func parseLevel(level string) (zlog.Level, bool) {
	switch level {
	case "debug":
		return zlog.DebugLevel, true
	case "info":
		return zlog.InfoLevel, true
	case "warn", "warning":
		return zlog.WarnLevel, true
	case "error":
		return zlog.ErrorLevel, true
	default:
		return 0, false
	}
}

// httpAccessLog logs one structured line per request: method, path, status,
// duration and any error the route returned.
func httpAccessLog(logger *zlog.Logger) fh.Handler {
	return func(c fh.Ctx) error {
		start := time.Now()
		err := c.Next()
		attrs := []zlog.Attr{
			zlog.String("method", c.Method()),
			zlog.String("path", c.Path()),
			zlog.Int("status", c.StatusCode()),
			// zlog.Duration(...) would be the natural call here, but zlog's
			// logfmt/console encoder (encoder_logfmt.go, KindDuration case)
			// prints it as a raw int64 nanosecond count, not a unit-suffixed
			// string — a gap in that third-party encoder this starter has
			// no way to patch. time.Duration.String() gives us the
			// us/ns/ms/s/m formatting directly, so log it as a plain string
			// instead.
			zlog.String("duration", time.Since(start).String()),
			zlog.String("ip", c.IP()),
		}
		switch {
		case err != nil:
			logger.Error("http request failed", append(attrs, zlog.Err(err))...)
		case c.StatusCode() >= 500:
			logger.Error("http server error", attrs...)
		case c.StatusCode() >= 400:
			logger.Warn("http client error", attrs...)
		default:
			logger.Info("http request", attrs...)
		}
		return err
	}
}
