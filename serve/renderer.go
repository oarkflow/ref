package serve

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/oarkflow/spl"
	"github.com/oarkflow/template"
)

// RendererConfig configures the SPL renderer.
type RendererConfig struct {
	TemplatesDir string
	Reload       bool
	Env          string
	Globals      map[string]any
	FS           fs.FS
}

// Renderer renders SPL templates from either the local disk or an in-memory virtual filesystem.
// It implements both platform.TemplateRenderer and fh.TemplateEngine.
type Renderer struct {
	cfg       RendererConfig
	fsys      fs.FS
	splEngine *spl.Engine
	diskSPL   *template.SPLEngine
}

// NewRenderer builds the SPL engine adapter. Globals are what a template sees when a route's
// intent did not publish that name, so a page renders empty instead of failing.
func NewRenderer(cfg RendererConfig) *Renderer {
	globals := map[string]any{
		"title":       "",
		"appName":     envOr("APP_NAME", "app"),
		"appVersion":  envOr("APP_VERSION", "0.1.0"),
		"currentYear": fmt.Sprintf("%d", time.Now().Year()),
		"environment": cfg.Env,
		"error":                 "",
		"success":               "",
		"user":                  map[string]any{},
		"stats":                 map[string]any{},
		"patient":               map[string]any{},
		"em":                    map[string]any{},
		"summary_rows":          []any{},
		"encounters":            []any{},
		"inprogress_encounters": []any{},
		"qa_encounters":         []any{},
		"suspended_encounters":  []any{},
		"de_encounters":         []any{},
		"cpt_rows":              []any{},
		"dx_rows":               []any{},
		"q":                     "",
		"status":                "",
		"wid":                   "",
		"eid":                   "",
	}
	for _, kv := range os.Environ() {
		if name, value, ok := strings.Cut(kv, "="); ok && strings.HasPrefix(name, "APP_GLOBAL_") {
			globals[strings.ToLower(strings.TrimPrefix(name, "APP_GLOBAL_"))] = value
		}
	}
	for k, v := range cfg.Globals {
		globals[k] = v
	}

	r := &Renderer{cfg: cfg}

	if cfg.FS != nil {
		templatesDir := strings.TrimPrefix(filepath.ToSlash(filepath.Clean(cfg.TemplatesDir)), "/")
		subFS, err := fs.Sub(cfg.FS, templatesDir)
		if err != nil {
			subFS = cfg.FS
		}
		r.fsys = subFS

		eng := spl.New()
		eng.FS = subFS
		eng.AutoEscape = true
		eng.SecureMode = false
		for k, v := range globals {
			eng.Globals[k] = v
		}
		r.splEngine = eng
	} else {
		dir := filepath.Clean(cfg.TemplatesDir)
		r.diskSPL = template.NewSPL(dir, ".html").Config(template.SPLConfig{
			Directory: dir, Extension: ".html", SSR: true, Reload: cfg.Reload, Globals: globals,
		})
	}

	return r
}

// Render implements both fh.TemplateEngine and platform.TemplateRenderer.
func (r *Renderer) Render(w io.Writer, name string, data any, layout ...string) error {
	if r.diskSPL != nil {
		return r.diskSPL.Render(w, name, data, layout...)
	}

	// In-memory Virtual Filesystem rendering path
	if r.cfg.Reload && r.splEngine != nil {
		r.splEngine.InvalidateCaches()
	}

	input, ok := data.(map[string]any)
	if !ok {
		if data != nil {
			return fmt.Errorf("spl: data must be map[string]any, got %T", data)
		}
		input = nil
	}
	binding := make(map[string]any, len(input)+len(r.splEngine.Globals))
	for k, v := range r.splEngine.Globals {
		binding[k] = v
	}
	for k, v := range input {
		binding[k] = v
	}

	cleanName := normalizeTmplName(name)

	if len(layout) > 0 && layout[0] != "" {
		layoutName := normalizeTmplName(layout[0])
		content, err := fs.ReadFile(r.fsys, cleanName)
		if err != nil {
			return fmt.Errorf("spl: read %s from VFS: %w", cleanName, err)
		}
		var wrapped string
		if strings.Contains(string(content), "@extends") {
			wrapped = string(content)
		} else {
			wrapped = fmt.Sprintf("@extends(%q)\n%s", layoutName, string(content))
		}
		out, err := r.splEngine.RenderSSR(wrapped, binding)
		if err != nil {
			return fmt.Errorf("spl: render %s with layout %s: %w", cleanName, layoutName, err)
		}
		_, err = io.WriteString(w, moveScriptsInsideBody(out))
		return err
	}

	out, err := r.splEngine.RenderFSFileSSR(cleanName, binding)
	if err != nil {
		return fmt.Errorf("spl: render %s from VFS: %w", cleanName, err)
	}
	_, err = io.WriteString(w, moveScriptsInsideBody(out))
	return err
}

func normalizeTmplName(name string) string {
	name = strings.TrimPrefix(filepath.ToSlash(filepath.Clean(name)), "/")
	if !strings.HasSuffix(name, ".html") {
		name += ".html"
	}
	return name
}

func moveScriptsInsideBody(s string) string {
	bodyIdx := strings.LastIndex(s, `</body>`)
	if bodyIdx < 0 {
		return s
	}
	markers := []string{`<script data-spl-runtime`, `<script data-spl-hydration`, `<script type="application/json" data-spl-hydration>`}
	idx := -1
	for _, marker := range markers {
		if found := strings.Index(s[bodyIdx:], marker); found >= 0 {
			found += bodyIdx
			if idx < 0 || found < idx {
				idx = found
			}
		}
	}
	if idx < 0 {
		return s
	}
	return s[:bodyIdx] + s[idx:] + s[bodyIdx:idx]
}
