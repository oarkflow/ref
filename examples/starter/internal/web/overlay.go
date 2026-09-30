package web

import (
	"fmt"
	"io"
	"io/fs"
	"mime"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/oarkflow/fh"

	"github.com/oarkflow/ref/platform"
)

// A deploy revision may carry assets (platform.Assets): page templates under
// "templates/" and static files under "static/". They override the files this
// application ships on disk, path for path; every file a revision does not
// name keeps coming from disk. So a revision that changes one page carries one
// file.

const (
	templatesPrefix = "templates/"
	staticPrefix    = "static/"
)

// HasTemplateAssets reports whether assets override any template.
func HasTemplateAssets(assets platform.Assets) bool { return hasPrefix(assets, templatesPrefix) }

// HasStaticAssets reports whether assets override any static file.
func HasStaticAssets(assets platform.Assets) bool { return hasPrefix(assets, staticPrefix) }

func hasPrefix(assets platform.Assets, prefix string) bool {
	for _, a := range assets {
		if strings.HasPrefix(a.Path, prefix) {
			return true
		}
	}
	return false
}

// MaterializeTemplates fills dst with the templates the renderer should read
// for a revision: everything under baseDir (the templates on disk), then the
// revision's "templates/…" assets on top. The SPL engine reads templates from
// a directory, so a revision's overrides have to exist as files; dst is a
// per-generation scratch directory the caller removes when the generation is
// done.
func MaterializeTemplates(baseDir string, assets platform.Assets, dst string) error {
	if err := copyTree(baseDir, dst); err != nil {
		return fmt.Errorf("copying templates from %s: %w", baseDir, err)
	}
	for _, a := range assets {
		rel, ok := strings.CutPrefix(a.Path, templatesPrefix)
		if !ok {
			continue
		}
		// NewAssets guarantees a clean, relative, "templates/…" path; check
		// again here because this writes to disk.
		if rel == "" || path.Clean(rel) != rel || strings.HasPrefix(rel, "../") {
			return fmt.Errorf("asset %q escapes the templates directory", a.Path)
		}
		target := filepath.Join(dst, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(target, []byte(a.Content), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// copyTree copies the regular files under src into dst.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			_ = out.Close()
			return err
		}
		return out.Close()
	})
}

// StaticOverlay answers GET and HEAD requests under urlPrefix (for example
// "/static") from the revision's "static/…" assets, and passes every other
// request, and every path the assets do not define, to the next handler, so
// the files served from disk by the document's `static` block keep working.
// Register it before the platform's routes.
func StaticOverlay(urlPrefix string, assets platform.Assets) fh.Handler {
	urlPrefix = "/" + strings.Trim(urlPrefix, "/")
	return func(c fh.Ctx) error {
		if m := c.Method(); m != "GET" && m != "HEAD" {
			return c.Next()
		}
		rest, ok := strings.CutPrefix(c.Path(), urlPrefix+"/")
		if !ok || rest == "" {
			return c.Next()
		}
		content, ok := assets.Get(staticPrefix + rest)
		if !ok {
			return c.Next()
		}
		ctype := mime.TypeByExtension(strings.ToLower(path.Ext(rest)))
		if ctype == "" {
			ctype = "text/plain; charset=utf-8"
		}
		c.Set("Content-Type", ctype)
		// An override is a change someone is looking at right now.
		c.Set("Cache-Control", "no-cache")
		c.Set("X-Content-Type-Options", "nosniff")
		return c.SendString(content)
	}
}
