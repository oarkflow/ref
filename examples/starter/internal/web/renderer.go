// Package web is bootstrapping glue, not business logic: an adapter wiring
// github.com/oarkflow/spl + github.com/oarkflow/template into fh's template
// engine interface. cmd/server/main.go wires this once, in Go, because a
// BCL document has no way to name a Go rendering engine — every page it
// renders (which template, which layout, which intent feeds it) is still
// declared entirely in bcl/.
package web

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/oarkflow/template"
)

// RendererConfig configures the SPL template rendering engine.
type RendererConfig struct {
	TemplatesDir string
	IsDev        bool
	AppName      string
	AppVersion   string
}

// NewSPLRenderer sets up github.com/oarkflow/template with github.com/oarkflow/spl.
//
// Globals are fallback values SPL substitutes when a route's intent didn't
// publish that name — so an admin-only field like `users` renders empty
// instead of failing on an unprotected page, and every page has a `title`/
// `appName` even before a real request supplies one.
func NewSPLRenderer(cfg RendererConfig) (*template.SPLEngine, error) {
	if cfg.TemplatesDir == "" {
		cfg.TemplatesDir = "./templates"
	}
	if cfg.AppName == "" {
		cfg.AppName = "starter"
	}
	if cfg.AppVersion == "" {
		cfg.AppVersion = "0.1.0"
	}

	cleanDir := filepath.Clean(cfg.TemplatesDir)

	engine := template.NewSPL(cleanDir, ".html").Config(template.SPLConfig{
		Directory:  cleanDir,
		Extension:  ".html",
		SSR:        true,
		SecureMode: false,
		Reload:     cfg.IsDev,
		Globals: map[string]any{
			"title":       "starter",
			"appName":     cfg.AppName,
			"appVersion":  cfg.AppVersion,
			"currentYear": fmt.Sprintf("%d", time.Now().Year()),
			"environment": ternary(cfg.IsDev, "development", "production"),
			"error":       "",
			"success":     "",
			"user":        map[string]any{},
			"users":       []map[string]any{},
			"redirect":    "/dashboard",
			"email":       "",
			"name":        "",
			"token":       "",
		},
	})

	return engine, nil
}

func ternary(cond bool, a, b string) string {
	if cond {
		return a
	}
	return b
}
