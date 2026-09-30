package platform

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/oarkflow/ref/platform/spi"
)

type rtFunc func(*http.Request) (*http.Response, error)

func (f rtFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type mailFunc func(spi.Mail) (string, error)

func (f mailFunc) Send(_ context.Context, m spi.Mail) (string, error) { return f(m) }

func TestOfflineHTTPAndSMTP(t *testing.T) {
	var got *http.Request
	rt := rtFunc(func(r *http.Request) (*http.Response, error) {
		got = r
		return &http.Response{StatusCode: 202, Body: io.NopCloser(strings.NewReader(`{"ok":true}`)),
			Header: http.Header{"Content-Type": {"application/json"}}, Request: r}, nil
	})
	var sent spi.Mail
	mailer := mailFunc(func(m spi.Mail) (string, error) { sent = m; return "id-1", nil })
	ctx := WithOffline(context.Background(), rt, mailer)

	// The host name does not resolve; offline means it is never looked up.
	res, _, err := openHTTPService(ctx, ResourceSpec{Name: "h", Kind: "service.http", Config: map[string]any{
		"allowed_hosts": []any{"nowhere.invalid"}, "base_url": "https://nowhere.invalid",
	}})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := res.(*HTTPService).Do(context.Background(), HTTPRequest{Method: "POST", URL: "/x", Body: []byte("hi")})
	if err != nil || resp.Status != 202 || got == nil || got.URL.Host != "nowhere.invalid" {
		t.Fatalf("resp=%+v err=%v req=%v", resp, err, got)
	}
	// The allowlist still applies.
	if _, err := res.(*HTTPService).Do(context.Background(), HTTPRequest{Method: "GET", URL: "https://other.invalid/"}); err == nil {
		t.Fatal("allowlist not enforced offline")
	}

	m, _, err := openSMTPService(ctx, ResourceSpec{Name: "m", Kind: "service.smtp", Config: map[string]any{
		"host": "smtp.nowhere.invalid", "from": "app@example.test"}})
	if err != nil {
		t.Fatal(err)
	}
	id, err := m.(spi.Mailer).Send(context.Background(), spi.Mail{To: []string{"a@b.test"}, Subject: "s", Body: "b"})
	if err != nil || id != "id-1" || sent.From != "app@example.test" {
		t.Fatalf("id=%q err=%v sent=%+v", id, err, sent)
	}
}

func TestMutateRewritesDocument(t *testing.T) {
	src := []byte(`name "m"
worker "w" { queue "q" }
`)
	called := false
	_, err := Compile(context.Background(), src, "", LoadOptions{Registry: NewRegistry(), Mutate: func(d *Document) error {
		called = true
		d.Workers = nil
		return nil
	}})
	// The document has a worker naming a missing queue; Mutate removed it, so
	// compile must get past that point.
	if !called {
		t.Fatal("Mutate not called")
	}
	if err != nil && strings.Contains(err.Error(), "worker") {
		t.Fatalf("worker survived Mutate: %v", err)
	}
}
