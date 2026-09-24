package stream

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

type Format string

const (
	NDJSON Format = "application/x-ndjson"
	SSE    Format = "text/event-stream"
)

type Event struct {
	ID    string
	Event string
	Data  any
}

type Options struct {
	Format      Format
	ContentType string
}

func Write(ctx context.Context, w http.ResponseWriter, events <-chan Event, opts Options) error {
	if ctx == nil {
		ctx = context.Background()
	}
	format := opts.Format
	if format == "" {
		format = NDJSON
	}
	contentType := opts.ContentType
	if contentType == "" {
		contentType = string(format)
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case event, ok := <-events:
			if !ok {
				return nil
			}
			if err := writeEvent(w, format, event); err != nil {
				return err
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
	}
}

func writeEvent(w http.ResponseWriter, format Format, event Event) error {
	if format == SSE {
		if event.ID != "" {
			if _, err := fmt.Fprintf(w, "id: %s\n", sanitizeSSE(event.ID)); err != nil {
				return err
			}
		}
		if event.Event != "" {
			if _, err := fmt.Fprintf(w, "event: %s\n", sanitizeSSE(event.Event)); err != nil {
				return err
			}
		}
		data, err := json.Marshal(event.Data)
		if err != nil {
			return err
		}
		for _, line := range strings.Split(string(data), "\n") {
			if _, err := fmt.Fprintf(w, "data: %s\n", line); err != nil {
				return err
			}
		}
		_, err = ioWriteString(w, "\n")
		return err
	}
	data, err := json.Marshal(event.Data)
	if err != nil {
		return err
	}
	if _, err := w.Write(data); err != nil {
		return err
	}
	_, err = ioWriteString(w, "\n")
	return err
}

func ioWriteString(w http.ResponseWriter, value string) (int, error) {
	return w.Write([]byte(value))
}

func sanitizeSSE(value string) string {
	value = strings.ReplaceAll(value, "\r", "")
	return strings.ReplaceAll(value, "\n", "")
}
