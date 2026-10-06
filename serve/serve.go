// Package serve runs a REF application that is nothing but configuration.
//
// An application directory holds everything the application is:
//
//	config/      the BCL (resources, intents, routes, processes, workers, …);
//	             bcl/ or a single app.bcl are accepted too
//	rules/       decision tables and rankings (a rules.engine "dir")
//	templates/   SPL page templates, layouts and components
//	static/      files served from a `static` block
//	migrations/  schema, if the BCL runs it from files
//
// serve changes into that directory (so every relative path in the BCL means
// "inside the application"), builds the SPL renderer, compiles config/, mounts
// its routes and static files, and runs until it is told to stop. There is no
// application code to write: a deployment is a directory.
//
// Anything the BCL cannot name, a driver for a resource kind or an action,
// is registered before Run with platform.RegisterResourceDriver and
// platform.RegisterActionDriver; a binary that wants some imports them and
// calls Run.
package serve

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/oarkflow/fh"
	"github.com/oarkflow/template"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"

	"github.com/oarkflow/ref/platform"
)

// Options configures Run. Everything has a default taken from the environment,
// so `ref ./app` is enough.
type Options struct {
	// Dir is the application directory.
	Dir string
	// Addr is the listen address (env PORT or REF_ADDR; default :8080).
	Addr string
	// Env is the environment name (env APP_ENV; default development). Anything
	// but "development" turns off template reloading.
	Env string
	// ConfigDir, TemplatesDir override the defaults. The configuration is the
	// first of config/, bcl/ (every .bcl in it, by name) or a single app.bcl
	// that the directory holds; templates default to templates/.
	ConfigDir    string
	TemplatesDir string
	// Globals are template variables available on every page, merged over the
	// defaults and over any APP_GLOBAL_<NAME> environment variable.
	Globals map[string]any
	// Load, when set, adjusts the platform's load options (a host that needs to
	// mutate the document or attach observers).
	Load func(*platform.LoadOptions)
	// Ready is closed with the listen address once the server accepts
	// connections. Optional; tests use it.
	Ready func(addr string)
	// Log receives lifecycle logging.
	Log *slog.Logger
}

func (o *Options) defaults() error {
	if o.Dir == "" {
		return errors.New("serve: an application directory is required")
	}
	abs, err := filepath.Abs(o.Dir)
	if err != nil {
		return err
	}
	o.Dir = abs
	if o.ConfigDir == "" {
		for _, name := range []string{"config", "bcl", "app.bcl"} {
			path := filepath.Join(abs, name)
			if strings.HasSuffix(name, ".bcl") {
				if _, err := os.Stat(path); err == nil {
					o.ConfigDir = name
					break
				}
			} else if found, _ := filepath.Glob(filepath.Join(path, "*.bcl")); len(found) > 0 {
				o.ConfigDir = name
				break
			}
		}
		if o.ConfigDir == "" {
			return fmt.Errorf("serve: %s holds no config/, bcl/ or app.bcl", abs)
		}
	}
	if o.TemplatesDir == "" {
		o.TemplatesDir = "templates"
	}
	if o.Env == "" {
		o.Env = envOr("APP_ENV", "development")
	}
	if o.Addr == "" {
		switch {
		case os.Getenv("REF_ADDR") != "":
			o.Addr = os.Getenv("REF_ADDR")
		case os.Getenv("PORT") != "":
			o.Addr = ":" + os.Getenv("PORT")
		default:
			o.Addr = ":8080"
		}
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	return nil
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// App is a running application.
type App struct {
	Platform *platform.Platform
	Server   *fh.App
	Addr     string
	listener net.Listener
	served   chan error
}

// Start compiles the application and starts serving, returning once it listens.
func Start(ctx context.Context, opts Options) (*App, error) {
	if err := opts.defaults(); err != nil {
		return nil, err
	}
	if err := os.Chdir(opts.Dir); err != nil {
		return nil, fmt.Errorf("serve: %w", err)
	}
	renderer := NewRenderer(RendererConfig{
		TemplatesDir: opts.TemplatesDir, Reload: opts.Env == "development", Env: opts.Env, Globals: opts.Globals,
	})

	lo := platform.DefaultLoadOptions()
	lo.Templates = renderer
	if opts.Load != nil {
		opts.Load(&lo)
	}
	var p *platform.Platform
	var err error
	if strings.HasSuffix(opts.ConfigDir, ".bcl") {
		p, err = platform.LoadFile(ctx, opts.ConfigDir, lo)
	} else {
		p, err = platform.LoadDirRecursive(ctx, opts.ConfigDir, lo)
	}
	if err != nil {
		return nil, err
	}

	srv := fh.NewFast(fh.WithTemplateEngine(renderer))
	if err := p.Mount(srv); err != nil {
		_ = p.Close()
		return nil, fmt.Errorf("serve: mount: %w", err)
	}
	srv.Get("/livez", func(c fh.Ctx) error { return c.JSON(map[string]any{"status": "ok"}) })

	ln, err := net.Listen("tcp", opts.Addr)
	if err != nil {
		_ = p.Close()
		return nil, err
	}
	a := &App{Platform: p, Server: srv, Addr: ln.Addr().String(), listener: ln, served: make(chan error, 1)}
	go func() { a.served <- srv.Serve(ln) }()
	opts.Log.Info("serving", "addr", a.Addr, "dir", opts.Dir, "env", opts.Env)
	if opts.Ready != nil {
		opts.Ready(a.Addr)
	}
	return a, nil
}

// URL is the base URL of the running server, usable from the same host.
func (a *App) URL() string {
	host, port, err := net.SplitHostPort(a.Addr)
	if err != nil {
		return "http://" + a.Addr
	}
	if host == "" || host == "::" || host == "0.0.0.0" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port)
}

// Wait blocks until the server stops.
func (a *App) Wait() error { return <-a.served }

// Stop shuts the server and then the platform down.
func (a *App) Stop(timeout time.Duration) error {
	err := a.Server.ShutdownWithTimeout(timeout)
	_ = a.listener.Close()
	if perr := a.Platform.Close(); err == nil {
		err = perr
	}
	return err
}

// Run starts the application and blocks until the context ends or the process
// receives an interrupt, then stops it.
func Run(ctx context.Context, opts Options) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	a, err := Start(ctx, opts)
	if err != nil {
		return err
	}
	select {
	case <-ctx.Done():
	case err := <-a.served:
		if err != nil {
			_ = a.Platform.Close()
			return err
		}
	}
	return a.Stop(10 * time.Second)
}

// RendererConfig configures the SPL renderer.
type RendererConfig struct {
	TemplatesDir string
	Reload       bool
	Env          string
	Globals      map[string]any
}

// NewRenderer builds the SPL engine (github.com/oarkflow/template over
// github.com/oarkflow/spl). Globals are what a template sees when a route's
// intent did not publish that name, so a page renders empty instead of failing.
func NewRenderer(cfg RendererConfig) *template.SPLEngine {
	dir := filepath.Clean(cfg.TemplatesDir)
	globals := map[string]any{
		"title":       "",
		"appName":     envOr("APP_NAME", "app"),
		"appVersion":  envOr("APP_VERSION", "0.1.0"),
		"currentYear": fmt.Sprintf("%d", time.Now().Year()),
		"environment": cfg.Env,
		"error":       "",
		"success":     "",
		"user":        map[string]any{},
	}
	for _, kv := range os.Environ() {
		if name, value, ok := strings.Cut(kv, "="); ok && strings.HasPrefix(name, "APP_GLOBAL_") {
			globals[strings.ToLower(strings.TrimPrefix(name, "APP_GLOBAL_"))] = value
		}
	}
	for k, v := range cfg.Globals {
		globals[k] = v
	}
	return template.NewSPL(dir, ".html").Config(template.SPLConfig{
		Directory: dir, Extension: ".html", SSR: true, Reload: cfg.Reload, Globals: globals,
	})
}
