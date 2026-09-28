package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestWebhookWriterDeliversLines(t *testing.T) {
	received := make(chan string, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(buf)
		received <- string(buf)
		w.WriteHeader(200)
	}))
	defer srv.Close()

	w := newWebhookWriter(srv.URL, "")
	if _, err := w.Write([]byte(`{"msg":"hello"}`)); err != nil {
		t.Fatal(err)
	}

	select {
	case line := <-received:
		if line != `{"msg":"hello"}` {
			t.Fatalf("delivered line = %q", line)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for delivery")
	}
	if w.Dropped() != 0 || w.Failed() != 0 {
		t.Fatalf("Dropped=%d Failed=%d, want both 0 for a successful delivery", w.Dropped(), w.Failed())
	}
}

func TestWebhookWriterCountsFailedDeliveries(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer srv.Close()

	w := newWebhookWriter(srv.URL, "")
	if _, err := w.Write([]byte(`{}`)); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && w.Failed() == 0 {
		time.Sleep(20 * time.Millisecond)
	}
	if w.Failed() != 1 {
		t.Fatalf("Failed() = %d, want 1 after a 500 response", w.Failed())
	}
}

func TestWebhookWriterCountsDroppedWhenQueueFull(t *testing.T) {
	// A URL nothing serves, so the single in-flight request never
	// completes quickly and the queue backs up behind it.
	w := &webhookWriter{
		url:    "http://127.0.0.1:1/unreachable",
		client: &http.Client{Timeout: 5 * time.Second},
		queue:  make(chan []byte, 1), // capacity 1 forces an early drop
	}
	go w.run()

	// The first Write may or may not be picked up by run() before the
	// second arrives; sending enough writes guarantees at least one drop
	// regardless of that race.
	for range 10 {
		_, _ = w.Write([]byte(`{}`))
	}
	if w.Dropped() == 0 {
		t.Fatalf("Dropped() = 0, want at least one drop from a queue of capacity 1 given 10 writes")
	}
}
