package platform

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/oarkflow/fh"
)

func TestSwaggerHTML(t *testing.T) {
	html := SwaggerHTML(SwaggerConfig{
		Title:        "Test API",
		Version:      "1.2.3",
		SpecURL:      "/docs/openapi.json",
		DocExpansion: "none",
		Filter:       true,
	})

	if !strings.Contains(html, "Test API") {
		t.Fatalf("expected title in html")
	}
	if !strings.Contains(html, "v1.2.3") {
		t.Fatalf("expected version badge in html")
	}
	if !strings.Contains(html, "/docs/openapi.json") {
		t.Fatalf("expected spec URL in html")
	}
	if !strings.Contains(html, "swagger-ui") {
		t.Fatalf("expected swagger-ui container in html")
	}
	if !strings.Contains(html, `docExpansion: "none"`) {
		t.Fatalf("expected docExpansion config in html")
	}
}

func TestSwaggerMountAndOpenAPI(t *testing.T) {
	bcl := `
name "test-service"
version "1.0.0"

shape "User" {
  field "id" string { required true }
  field "email" string { required true }
}

intent "users.list" {
  response "users"
  node "users" {
    uses "expression"
    provides [users]
    config {
      expression "[]"
    }
  }
}

route "users.list" {
  method GET
  path "/users"
  intent "users.list"
  allow_anonymous true
}

route "users.show" {
  method GET
  path "/users/:id"
  intent "users.list"
  allow_anonymous true
}
`
	opts := DefaultLoadOptions()
	p, err := Compile(context.Background(), []byte(bcl), "", opts)
	if err != nil {
		t.Fatalf("compile error: %v", err)
	}
	defer p.Close()

	spec := p.OpenAPI()
	if spec["openapi"] != "3.1.0" {
		t.Fatalf("expected openapi 3.1.0, got %v", spec["openapi"])
	}
	info, ok := spec["info"].(map[string]any)
	if !ok || info["title"] != "test-service" {
		t.Fatalf("unexpected info: %v", info)
	}
	paths, ok := spec["paths"].(map[string]any)
	if !ok || len(paths) < 2 {
		t.Fatalf("expected at least 2 paths, got %d", len(paths))
	}
	comp, ok := spec["components"].(map[string]any)
	if !ok {
		t.Fatalf("expected components")
	}
	sec, ok := comp["securitySchemes"].(map[string]any)
	if !ok || sec["cookieAuth"] == nil || sec["bearerAuth"] == nil {
		t.Fatalf("expected securitySchemes with cookieAuth and bearerAuth")
	}

	app := fh.NewFast()
	if err := p.Mount(app); err != nil {
		t.Fatalf("mount error: %v", err)
	}

	// Verify routes are registered
	routes := app.Routes()
	hasDocs := false
	hasOpenAPI := false
	for _, r := range routes {
		if r.Path == "/docs" {
			hasDocs = true
		}
		if r.Path == "/docs/openapi.json" || r.Path == "/openapi.json" {
			hasOpenAPI = true
		}
	}
	if !hasDocs {
		t.Fatalf("expected /docs route registered on app")
	}
	if !hasOpenAPI {
		t.Fatalf("expected openapi.json route registered on app")
	}

	t.Run("swagger ui html", func(t *testing.T) {
		appUI := fh.NewFast()
		if err := p.Mount(appUI); err != nil {
			t.Fatalf("mount error: %v", err)
		}
		req := httptest.NewRequest("GET", "/docs", nil)
		resp, err := appUI.Test(req)
		if err != nil {
			t.Fatalf("GET /docs failed: %v", err)
		}
		if resp.StatusCode != 200 {
			t.Fatalf("GET /docs returned status %d", resp.StatusCode)
		}
	})

	t.Run("openapi json spec", func(t *testing.T) {
		appSpec := fh.NewFast()
		if err := p.Mount(appSpec); err != nil {
			t.Fatalf("mount error: %v", err)
		}
		req := httptest.NewRequest("GET", "/docs/openapi.json", nil)
		resp, err := appSpec.Test(req)
		if err != nil {
			t.Fatalf("GET /docs/openapi.json failed: %v", err)
		}
		if resp.StatusCode != 200 {
			t.Fatalf("GET /docs/openapi.json returned status %d", resp.StatusCode)
		}
	})
}
