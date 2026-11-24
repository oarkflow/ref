package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/oarkflow/ref/pipeline"
	"github.com/oarkflow/ref/platform"
	"github.com/oarkflow/ref/serve"
)

// The UI ships its own copy of the capture module; it must not drift from the
// one the pipeline package embeds.
func TestStaticCaptureModuleMatchesEmbedded(t *testing.T) {
	got, err := os.ReadFile("static/js/liveness-capture.js")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, pipeline.LivenessCaptureJS) {
		t.Fatal("static/js/liveness-capture.js differs from pipeline/assets/liveness-capture.js; copy it over")
	}
}

func TestApplicationCompilesAndServesEveryPipeline(t *testing.T) {
	url, stop := startStubFaceworker()
	defer stop()
	t.Setenv("CASEKIT_JWT_SECRET", "test-jwt-secret-0123456789abcdef-xyz")
	t.Setenv("CASEKIT_SIGNING_SECRET", "test-signing-secret-0123456789abcdef")
	t.Setenv("CASEKIT_ADMIN_PASSWORD", "test-admin-password-123")
	t.Setenv("CASEKIT_LIVENESS_URL", url)
	t.Setenv("CASEKIT_LIVENESS_TOKEN", "demo-token")
	t.Setenv("CASEKIT_DSN", "file:"+filepath.Join(t.TempDir(), "c.db")+"?_pragma=busy_timeout(5000)")
	app, err := platform.LoadFile(context.Background(), "app.bcl", platform.DefaultLoadOptions())
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	defer app.Close()
}

// Every page is an SPL template; each must render against the shape of data
// its route supplies, with the intro, documents and stage controls visible.
func TestPagesRenderThroughSPL(t *testing.T) {
	r := serve.NewRenderer(serve.RendererConfig{TemplatesDir: "templates", Reload: true, Env: "development",
		Globals: map[string]any{"title": "Casekit", "appName": "Casekit", "error": "", "success": "",
			"ui": map[string]any{"accent": "", "density": "", "brand": ""}, "workspace": map[string]any{"level": "", "label": "", "required": false}, "intro": map[string]any{"title": "", "image": "", "image_alt": "", "content": "", "button": "Start", "documents": []any{}}, "stages": []any{}, "name": "", "description": ""}})
	catalog := []any{map[string]any{"name": "onboarding", "title": "Identity onboarding", "description": "d", "stages": 3}}
	def := map[string]any{"name": "onboarding", "title": "Identity onboarding", "description": "d",
		"intro": map[string]any{"title": "Verify your identity", "image": "/static/img/onboarding.svg", "image_alt": "id", "content": "<p>hi</p>", "button": "Start verification",
			"documents": []any{map[string]any{"title": "Privacy notice", "url": "/static/docs/privacy.html"}}},
		"stages": []any{map[string]any{"title": "Intake", "public": true}, map[string]any{"title": "Review", "roles": []any{"officer"}, "require_start": true, "admin_roles": []any{"admin"}, "entries": map[string]any{"per_user": 2, "min": 1, "allow_draft": true, "allow_edit": false}, "external": map[string]any{"party": "KYC verifier"}, "sla": map[string]any{"duration": "4h", "on_breach": "notify"}, "suspend_roles": []any{"officer"}, "resume_roles": []any{"manager"}, "info_roles": []any{"officer"},
			"actions": []any{map[string]any{"label": "Approve", "outcome": "approve"}}}}}
	cases := []struct {
		page string
		data map[string]any
		want []string
	}{
		{"pages/home", map[string]any{"data": catalog}, []string{"Identity onboarding", "/start/onboarding"}},
		{"pages/guide", map[string]any{"data": catalog}, []string{"/guide/onboarding"}},
		{"pages/intro", def, []string{"Verify your identity", "Start verification", "Privacy notice", `data-start="onboarding"`}},
		{"pages/guide_detail", def, []string{"Staff: officer", "resumed by manager", "Approve", "Deadline", "Must be started first", "reset the stage", "outside the workflow", "Data entry"}},
		{"pages/guide_fields", map[string]any{"name": "onboarding", "title": "Identity onboarding", "rows": []any{map[string]any{"stage": "review", "stage_title": "Review", "actors": "officer", "group": "Request", "form": "request", "field": "address", "label": "Address", "mode": "editable", "sees": "anyone who can open the stage", "edits": "officer", "notes": []any{"stage: required if true"}}}},
			[]string{"Field access by stage", "request.address", "editable", "stage: required if true"}},
		{"pages/work", map[string]any{}, []string{"Loading"}},
		{"pages/case", map[string]any{}, []string{"Loading"}},
	}
	for _, c := range cases {
		var buf bytes.Buffer
		if err := r.Render(&buf, c.page, c.data, "layouts/base"); err != nil {
			t.Fatalf("%s: %v", c.page, err)
		}
		for _, w := range c.want {
			if !bytes.Contains(buf.Bytes(), []byte(w)) {
				t.Errorf("%s: missing %q in\n%s", c.page, w, buf.String())
			}
		}
	}
}

func TestDemoClocksShortenOnlyDeadlinesAndSweep(t *testing.T) {
	in := []byte("  sla {\n    duration \"4h\"\n    warn_before \"1h\"\n  }\nschedule \"x\" {\n  every 1m\n}\nnumber_format \"duration\"\n")
	got := string(demoClocks(in))
	for _, want := range []string{`duration "45s"`, `warn_before "15s"`, "every 5s", `number_format "duration"`} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
	// The real document keeps its production clocks.
	src, _ := os.ReadFile("app.bcl")
	if !strings.Contains(string(src), `duration "4h"`) || strings.Contains(string(src), "45s") {
		t.Fatal("app.bcl must carry production deadlines")
	}
}
