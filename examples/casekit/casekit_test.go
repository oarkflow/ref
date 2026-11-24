package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/oarkflow/ref/pipeline"
	"github.com/oarkflow/ref/platform"
)

// The UI ships its own copy of the capture module; it must not drift from the
// one the pipeline package embeds.
func TestStaticCaptureModuleMatchesEmbedded(t *testing.T) {
	got, err := os.ReadFile("static/liveness-capture.js")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, pipeline.LivenessCaptureJS) {
		t.Fatal("static/liveness-capture.js differs from pipeline/assets/liveness-capture.js; copy it over")
	}
}

func TestApplicationCompilesAndServesEveryPipeline(t *testing.T) {
	url, stop := startStubFaceworker()
	defer stop()
	t.Setenv("CASEKIT_JWT_SECRET", "test-jwt-secret-0123456789abcdef-xyz")
	t.Setenv("CASEKIT_SIGNING_SECRET", "test-signing-secret-0123456789abcdef")
	t.Setenv("CASEKIT_LIVENESS_URL", url)
	t.Setenv("CASEKIT_LIVENESS_TOKEN", "demo-token")
	t.Setenv("CASEKIT_DSN", "file:"+filepath.Join(t.TempDir(), "c.db")+"?_pragma=busy_timeout(5000)")
	app, err := platform.LoadFile(context.Background(), "app.bcl", platform.DefaultLoadOptions())
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	defer app.Close()
}
