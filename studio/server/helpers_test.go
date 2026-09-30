package server

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// callFull is env.call with request headers, returning the response headers.
func (e *env) callFull(method, path, token string, body any, hdr map[string]string) (int, http.Header, []byte) {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		var raw []byte
		switch b := body.(type) {
		case []byte:
			raw = b
		default:
			var err error
			if raw, err = json.Marshal(body); err != nil {
				e.t.Fatal(err)
			}
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, e.ts.URL+path, rd)
	if err != nil {
		e.t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, out
}

// openTestDB opens (creating) a sqlite database file.
func openTestDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	return db
}

// withSQL makes a Config use SQL for drafts, audit and comments in a fresh
// database file, and returns the file's path.
func withSQL(t *testing.T) (func(*Config), string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "studio.db")
	return sqlMutator(t, path), path
}

func sqlMutator(t *testing.T, path string) func(*Config) {
	t.Helper()
	db := openTestDB(t, path)
	store, sink, err := OpenSQL(context.Background(), db, "sqlite", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(store.Skipped) != 0 {
		t.Fatalf("skipped drafts: %v", store.Skipped)
	}
	return func(c *Config) { c.Store, c.AuditStore, c.Comments = store, sink, sink }
}

// sseEvent is one server-sent event.
type sseEvent struct{ name, data string }

// sseStream reads a text/event-stream response in the background.
type sseStream struct {
	t      *testing.T
	resp   *http.Response
	events chan sseEvent
}

func (e *env) openStream(id, token string) *sseStream {
	e.t.Helper()
	req, _ := http.NewRequest("GET", e.ts.URL+"/api/v1/drafts/"+id+"/events?access_token="+token, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	e.t.Cleanup(cancel)
	resp, err := http.DefaultClient.Do(req.WithContext(ctx))
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { resp.Body.Close() })
	if resp.StatusCode != 200 {
		e.t.Fatalf("events: status %d", resp.StatusCode)
	}
	s := &sseStream{t: e.t, resp: resp, events: make(chan sseEvent, 64)}
	go func() {
		sc := bufio.NewScanner(resp.Body)
		var name string
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, "event: "):
				name = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				s.events <- sseEvent{name, strings.TrimPrefix(line, "data: ")}
			}
		}
		close(s.events)
	}()
	return s
}

// next returns the next event, or ok=false when the stream ended.
func (s *sseStream) next(timeout time.Duration) (ev sseEvent, ok bool) {
	s.t.Helper()
	select {
	case ev, ok = <-s.events:
		return ev, ok
	case <-time.After(timeout):
		s.t.Fatal("no event within", timeout)
	}
	return sseEvent{}, false
}

// waitFor skips events until one named name arrives.
func (s *sseStream) waitFor(name string, timeout time.Duration) sseEvent {
	s.t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		ev, ok := s.next(time.Until(deadline))
		if !ok {
			s.t.Fatalf("stream ended before a %q event", name)
		}
		if ev.name == name {
			return ev
		}
	}
}

func io_reader(s string) *strings.Reader { return strings.NewReader(s) }
