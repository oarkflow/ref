package stream

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWriteNDJSON(t *testing.T) {
	events := make(chan Event, 2)
	events <- Event{Data: map[string]int{"value": 1}}
	events <- Event{Data: map[string]int{"value": 2}}
	close(events)
	recorder := httptest.NewRecorder()
	if err := Write(context.Background(), recorder, events, Options{}); err != nil {
		t.Fatal(err)
	}
	if got := recorder.Body.String(); got != "{\"value\":1}\n{\"value\":2}\n" {
		t.Fatalf("unexpected body: %q", got)
	}
	if recorder.Header().Get("Content-Type") != string(NDJSON) {
		t.Fatalf("unexpected content type: %q", recorder.Header().Get("Content-Type"))
	}
}

func TestWriteSSE(t *testing.T) {
	events := make(chan Event, 1)
	events <- Event{ID: "1", Event: "update", Data: map[string]string{"status": "ok"}}
	close(events)
	recorder := httptest.NewRecorder()
	if err := Write(context.Background(), recorder, events, Options{Format: SSE}); err != nil {
		t.Fatal(err)
	}
	body := recorder.Body.String()
	for _, expected := range []string{"id: 1\n", "event: update\n", "data: {\"status\":\"ok\"}\n", "\n"} {
		if !strings.Contains(body, expected) {
			t.Fatalf("missing %q in %q", expected, body)
		}
	}
}
