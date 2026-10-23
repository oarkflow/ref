package dynamicdefinitions_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/oarkflow/fh"
	"github.com/oarkflow/ref/platform"
	"github.com/oarkflow/ref/signing"
)

const (
	clerkKey  = "dev-clerk-key-change-me-0123456789"
	viewerKey = "dev-viewer-key-change-me-012345678"
)

func serve(t *testing.T) string {
	t.Helper()
	opts := platform.DefaultLoadOptions()
	opts.FS = os.DirFS(".")
	p, err := platform.LoadFS(context.Background(), opts.FS, "config", opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })

	app := fh.NewFast()
	if err := p.Mount(app); err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { defer close(done); _ = app.Serve(l) }()
	t.Cleanup(func() { _ = app.ShutdownWithTimeout(2 * time.Second); _ = l.Close(); <-done })
	return "http://" + l.Addr().String()
}

func call(t *testing.T, method, url, key string, body any) (int, map[string]any) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rd = bytes.NewReader(raw)
	}
	req, _ := http.NewRequest(method, url, rd)
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("X-API-Key", key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	raw, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(raw, &out); err != nil {
		var v any
		_ = json.Unmarshal(raw, &v)
		out = map[string]any{"value": v}
	}
	return resp.StatusCode, out
}

func TestHandlersAndFlowsOverHTTP(t *testing.T) {
	base := serve(t)

	if code, out := call(t, "GET", base+"/live", "", nil); code != 200 || out["ok"] != true {
		t.Fatalf("live: %d %v", code, out)
	}

	quote := map[string]any{"lines": []any{
		map[string]any{"qty": 2, "price": 30}, map[string]any{"qty": 1, "price": 55},
	}}
	code, out := call(t, "POST", base+"/quote", clerkKey, quote)
	if code != 200 || out["subtotal"] != float64(115) || out["quoted_by"] != "clerk-1" {
		t.Fatalf("quote: %d %v", code, out)
	}
	// Closed by default: no key, and a key without the permission.
	if code, _ := call(t, "POST", base+"/quote", "", quote); code != 401 {
		t.Fatalf("no key: %d", code)
	}
	if code, _ := call(t, "POST", base+"/quote", viewerKey, quote); code != 403 {
		t.Fatalf("viewer: %d", code)
	}

	if code, out := call(t, "POST", base+"/shipping", clerkKey, map[string]any{"kg": 4}); code != 200 || out["value"] != float64(12) {
		t.Fatalf("shiplive: %d %v", code, out)
	}

	code, out = call(t, "POST", base+"/orders/review", clerkKey, map[string]any{"total": 800})
	if code != 200 || out["level"] != "high" || out["tax"] != float64(104) || out["total_with_tax"] != float64(904) {
		t.Fatalf("review: %d %v", code, out)
	}
}

func TestSignedDefinitionsOnly(t *testing.T) {
	key, err := signing.GenerateEd25519("platform-team")
	if err != nil {
		t.Fatal(err)
	}
	pem, _ := key.PublicOnly().MarshalPublicKeyPEM()
	fsys := fstest.MapFS{"scripts/live.spl": {Data: []byte("{\"ok\": true}")}}

	spec := platform.HandlerSpec{Name: "live", Method: "GET", Path: "/live", Public: true, File: "scripts/live.spl", Publisher: "platform_team"}
	sig, err := platform.SignDefinition(key, spec, fsys)
	if err != nil {
		t.Fatal(err)
	}
	app := func(signature string) []byte {
		return []byte(`
name "signed"
role "release" { permissions ["definition:publish"] }
publisher "platform_team" {
  public_key ` + "`" + string(pem) + "`" + `
  roles ["release"]
}
definition_policy { require_signature true }
handler "live" {
  method GET
  path "/live"
  public true
  file "scripts/live.spl"
  publisher "platform_team"
  signature "` + signature + `"
}
`)
	}
	opts := platform.DefaultLoadOptions()
	opts.FS = fsys
	p, err := platform.Compile(context.Background(), app(sig), "", opts)
	if err != nil {
		t.Fatalf("signed definition refused: %v", err)
	}
	defer p.Close()
	if d := p.Definitions(); len(d) != 1 || !d[0].Signed {
		t.Fatalf("inventory: %+v", d)
	}

	// The script changes after signing: the application does not start.
	tampered := fstest.MapFS{"scripts/live.spl": {Data: []byte("{\"ok\": false}")}}
	opts.FS = tampered
	if _, err := platform.Compile(context.Background(), app(sig), "", opts); err == nil || !strings.Contains(err.Error(), "signature") {
		t.Fatalf("tampered script accepted: %v", err)
	}
}
