package serve_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/oarkflow/ref/aegis/vfs"
	"github.com/oarkflow/ref/serve"
)

func TestServe_PureInMemoryVFS(t *testing.T) {
	mem := vfs.New()

	appBCL := `
name "vfs-test"
version "1.0.0"
environment "testing"

intent "hello" {
  response "result"
  node "resp" {
    uses "data.transform"
    kind pure
    provides [result]
    config {
      data {
        extract {
          message "'Hello from Pure In-Memory VFS!'"
          status "'secure'"
        }
      }
    }
  }
}

route "get_hello" {
  method "GET"
  path "/hello"
  intent "hello"
}
`
	mem.WriteFile("config/app.bcl", []byte(appBCL), 0o644)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	readyCh := make(chan string, 1)
	app, err := serve.Start(ctx, serve.Options{
		FS:    mem,
		Addr:  "127.0.0.1:0",
		Env:   "testing",
		Ready: func(addr string) { readyCh <- addr },
	})
	if err != nil {
		t.Fatalf("serve.Start with VFS failed: %v", err)
	}
	defer func() {
		_ = app.Stop(2 * time.Second)
	}()

	var addr string
	select {
	case addr = <-readyCh:
	case <-time.After(3 * time.Second):
		t.Fatal("server timed out waiting for ready")
	}

	baseURL := "http://" + addr

	// 1. Check /livez
	respLivez, err := http.Get(baseURL + "/livez")
	if err != nil {
		t.Fatalf("GET /livez failed: %v", err)
	}
	defer respLivez.Body.Close()
	if respLivez.StatusCode != http.StatusOK {
		t.Fatalf("GET /livez status = %d, expected 200", respLivez.StatusCode)
	}

	// 2. Check /hello
	respHello, err := http.Get(baseURL + "/hello")
	if err != nil {
		t.Fatalf("GET /hello failed: %v", err)
	}
	defer respHello.Body.Close()
	if respHello.StatusCode != http.StatusOK {
		t.Fatalf("GET /hello status = %d, expected 200", respHello.StatusCode)
	}

	body, err := io.ReadAll(respHello.Body)
	if err != nil {
		t.Fatalf("read /hello response failed: %v", err)
	}

	var data map[string]any
	if err := json.Unmarshal(body, &data); err != nil {
		t.Fatalf("unmarshal json failed: %v, raw: %s", err, string(body))
	}
	if data["message"] != "Hello from Pure In-Memory VFS!" {
		t.Errorf("unexpected message: %v", data["message"])
	}
	if data["status"] != "secure" {
		t.Errorf("unexpected status: %v", data["status"])
	}
}
