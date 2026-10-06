package platform

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/oarkflow/fh"
)

// SwaggerConfig configures Swagger UI viewer rendering.
type SwaggerConfig struct {
	Title        string
	Version      string
	SpecURL      string
	DocExpansion string // "none", "list", "full" (default: "none")
	Filter       bool   // enables live search bar (default: true)
}

// SwaggerHTML returns a standalone, responsive, high-performance HTML5 page hosting Swagger UI 5.x.
func SwaggerHTML(cfg SwaggerConfig) string {
	if cfg.Title == "" {
		cfg.Title = "API Documentation"
	}
	if cfg.DocExpansion == "" {
		cfg.DocExpansion = "none"
	}
	if cfg.SpecURL == "" {
		cfg.SpecURL = "/openapi.json"
	}

	filterVal := "true"
	if !cfg.Filter {
		filterVal = "false"
	}

	versionBadge := ""
	if cfg.Version != "" {
		versionBadge = fmt.Sprintf(`<span class="ref-badge">v%s</span>`, cfg.Version)
	}

	return fmt.Sprintf(`<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>%s - Interactive API Documentation</title>
  <link rel="stylesheet" type="text/css" href="https://unpkg.com/swagger-ui-dist@5/swagger-ui.css">
  <style>
    html {
      box-sizing: border-box;
      overflow-y: scroll;
      font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif;
    }
    *, *:before, *:after {
      box-sizing: inherit;
    }
    body {
      margin: 0;
      background: #fafafa;
      color: #3b4151;
    }
    .ref-header {
      background: linear-gradient(135deg, #1e293b 0%%, #0f172a 100%%);
      color: #ffffff;
      padding: 14px 28px;
      display: flex;
      align-items: center;
      justify-content: space-between;
      box-shadow: 0 2px 8px rgba(0,0,0,0.15);
    }
    .ref-header-left {
      display: flex;
      align-items: center;
      gap: 12px;
    }
    .ref-header h1 {
      margin: 0;
      font-size: 1.25rem;
      font-weight: 600;
      letter-spacing: -0.01em;
    }
    .ref-badge {
      background: #3b82f6;
      color: #ffffff;
      font-size: 0.75rem;
      font-weight: 600;
      padding: 2px 8px;
      border-radius: 9999px;
      text-transform: uppercase;
      letter-spacing: 0.05em;
    }
    .ref-header-right a {
      color: #94a3b8;
      text-decoration: none;
      font-size: 0.85rem;
      font-weight: 500;
      padding: 6px 14px;
      border: 1px solid #334155;
      border-radius: 6px;
      transition: all 0.2s ease;
    }
    .ref-header-right a:hover {
      color: #ffffff;
      border-color: #64748b;
      background: rgba(255,255,255,0.05);
    }
    .swagger-ui .topbar {
      display: none;
    }
    .swagger-ui .info {
      margin: 25px 0 20px 0;
    }
    .swagger-ui .info .title {
      font-size: 2rem;
      color: #1e293b;
    }
    .swagger-ui .scheme-container {
      padding: 15px 0;
      background: #ffffff;
      box-shadow: 0 1px 3px rgba(0,0,0,0.05);
      border-radius: 6px;
      margin-bottom: 20px;
    }
    .swagger-ui .opblock {
      border-radius: 6px;
      box-shadow: 0 1px 2px rgba(0,0,0,0.04);
      margin: 0 0 12px 0;
    }
  </style>
</head>
<body>
  <header class="ref-header">
    <div class="ref-header-left">
      <h1>%s</h1>
      %s
    </div>
    <div class="ref-header-right">
      <a href="%s" target="_blank" rel="noopener noreferrer">Download OpenAPI JSON</a>
    </div>
  </header>
  <div id="swagger-ui"></div>
  <script src="https://unpkg.com/swagger-ui-dist@5/swagger-ui-bundle.js" charset="UTF-8"></script>
  <script src="https://unpkg.com/swagger-ui-dist@5/swagger-ui-standalone-preset.js" charset="UTF-8"></script>
  <script>
    window.onload = function() {
      window.ui = SwaggerUIBundle({
        url: "%s",
        dom_id: '#swagger-ui',
        deepLinking: true,
        docExpansion: "%s",
        filter: %s,
        displayRequestDuration: true,
        persistAuthorization: true,
        tryItOutEnabled: true,
        presets: [
          SwaggerUIBundle.presets.apis,
          SwaggerUIStandalonePreset
        ],
        plugins: [
          SwaggerUIBundle.plugins.DownloadUrl
        ],
        layout: "StandaloneLayout"
      });
    };
  </script>
</body>
</html>`, cfg.Title, cfg.Title, versionBadge, cfg.SpecURL, cfg.SpecURL, cfg.DocExpansion, filterVal)
}

// MountSwagger registers Swagger UI and OpenAPI JSON endpoints onto app.
func (p *Platform) MountSwagger(app *fh.App, prefixes ...string) error {
	if app == nil {
		return fmt.Errorf("ref/platform: nil fh app")
	}
	if len(prefixes) == 0 {
		prefixes = []string{"/docs"}
	}

	title := p.Document.Name
	if title == "" {
		title = "API Documentation"
	}
	version := p.Document.Version

	spec := p.OpenAPI()
	formattedSpec, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		formattedSpec, _ = json.Marshal(spec)
	}

	handlerJSON := func(c fh.Ctx) error {
		c.Set("Access-Control-Allow-Origin", "*")
		c.Set("Access-Control-Allow-Methods", "GET,OPTIONS")
		c.Set("Access-Control-Allow-Headers", "Content-Type,Authorization")
		c.Set("Cache-Control", "no-cache, no-store, must-revalidate")
		c.Type("application/json; charset=utf-8")
		return c.Send(formattedSpec)
	}

	// Register global root-level specs
	app.Get("/openapi.json", handlerJSON)
	app.Get("/swagger.json", handlerJSON)

	for _, prefix := range prefixes {
		cleanPrefix := "/" + strings.Trim(prefix, "/")
		specURL := cleanPrefix + "/openapi.json"

		html := SwaggerHTML(SwaggerConfig{
			Title:        title,
			Version:      version,
			SpecURL:      specURL,
			DocExpansion: "none",
			Filter:       true,
		})

		handlerHTML := func(c fh.Ctx) error {
			c.Type("text/html; charset=utf-8")
			c.Set("Cache-Control", "no-cache, no-store, must-revalidate")
			return c.SendString(html)
		}

		app.Get(specURL, handlerJSON)
		app.Get(cleanPrefix, handlerHTML)
	}

	return nil
}
