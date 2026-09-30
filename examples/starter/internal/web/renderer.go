// Package web is bootstrapping glue, not business logic: an adapter wiring
// github.com/oarkflow/spl + github.com/oarkflow/template into fh's template
// engine interface. cmd/server/main.go wires this once, in Go, because a
// BCL document has no way to name a Go rendering engine — every page it
// renders (which template, which layout, which intent feeds it) is still
// declared entirely in resources/config/.
package web

import (
	"fmt"
	"path/filepath"
	"sort"
	"time"

	"github.com/oarkflow/template"
)

// RendererConfig configures the SPL template rendering engine.
type RendererConfig struct {
	TemplatesDir string
	IsDev        bool
	AppName      string
	AppVersion   string
	// DemoPassword is the password internal/ops/seed.go actually seeded the
	// demo role accounts with (AdminPassword — see cmd/server/main.go).
	// Passed through as a global so resources/templates/pages/auth/login.html's
	// one-click demo-login buttons always match what was really seeded,
	// even when ADMIN_PASSWORD is overridden away from its default.
	DemoPassword string
}

// NewSPLRenderer sets up github.com/oarkflow/template with github.com/oarkflow/spl.
//
// Globals are fallback values SPL substitutes when a route's intent didn't
// publish that name — so an admin-only field like `users` renders empty
// instead of failing on an unprotected page, and every page has a `title`/
// `appName` even before a real request supplies one.
func NewSPLRenderer(cfg RendererConfig) (*template.SPLEngine, error) {
	if cfg.TemplatesDir == "" {
		cfg.TemplatesDir = "./resources/templates"
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
		Globals:    Globals(cfg),
	})

	return engine, nil
}

// Globals are the values SPL substitutes when a route's intent didn't publish
// that name (see NewSPLRenderer). cfg's zero fields take the same defaults
// NewSPLRenderer applies.
func Globals(cfg RendererConfig) map[string]any {
	if cfg.AppName == "" {
		cfg.AppName = "starter"
	}
	if cfg.AppVersion == "" {
		cfg.AppVersion = "0.1.0"
	}
	return map[string]any{
		"title":        "starter",
		"appName":      cfg.AppName,
		"appVersion":   cfg.AppVersion,
		"currentYear":  fmt.Sprintf("%d", time.Now().Year()),
		"environment":  ternary(cfg.IsDev, "development", "production"),
		"demoPassword": cfg.DemoPassword,
		"error":        "",
		"success":      "",
		"user":         map[string]any{},
		"users":        []map[string]any{},
		"redirect":     "/dashboard",
		"email":        "",
		"name":         "",
		"token":        "",
	}
}

// GlobalNames lists the variables every template can read without a route's
// intent providing them. Studio's page-linkage check takes it as
// TemplateGlobals so it does not warn about them.
func GlobalNames() []string {
	g := Globals(RendererConfig{})
	names := make([]string, 0, len(g))
	for k := range g {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

func ternary(cond bool, a, b string) string {
	if cond {
		return a
	}
	return b
}
