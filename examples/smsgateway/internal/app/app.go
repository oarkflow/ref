// Package app assembles the SMS application: it registers the plugins with
// REF, loads the BCL, and connects the hub's consumers to the BCL pipelines.
package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/oarkflow/fh"
	_ "modernc.org/sqlite"

	"github.com/oarkflow/ref/invocation"
	"github.com/oarkflow/ref/platform"
	"github.com/oarkflow/ref/runtime"

	"github.com/oarkflow/ref/examples/smsgateway/internal/brokerq"
	"github.com/oarkflow/ref/examples/smsgateway/internal/sms"
	"github.com/oarkflow/ref/examples/smsgateway/internal/stages"
)

// Options configures Load.
type Options struct {
	// Dir holds the .bcl files.
	Dir string
	// HubName is the sms.hub resource (default "sms").
	HubName string
	// Env resolves env() in the BCL; default os.LookupEnv.
	Env func(string) (string, bool)
}

// App is a running application.
type App struct {
	Platform *platform.Platform
	Hub      *sms.Hub
	closed   bool
}

// Register installs every plugin into REF. Call it once, before Load. Gateway
// plugins register themselves from their own init functions, so importing a
// plugin package is all it takes to make its sms.gateway.<kind> resource kind
// available.
func Register() {
	brokerq.Register()
	sms.Register()
	stages.Register()
}

// Load compiles the BCL, starts the hub's consumers and returns the app. The
// BCL decides everything else.
func Load(ctx context.Context, opts Options) (*App, error) {
	Register()
	if opts.HubName == "" {
		opts.HubName = "sms"
	}
	lo := platform.DefaultLoadOptions()
	if opts.Env != nil {
		lo.Env = opts.Env
	}
	p, err := platform.LoadDir(ctx, opts.Dir, lo)
	if err != nil {
		return nil, err
	}
	res, ok := p.Resource(opts.HubName)
	if !ok {
		_ = p.Close()
		return nil, fmt.Errorf("app: the BCL declares no resource %q", opts.HubName)
	}
	hub, ok := res.(*sms.Hub)
	if !ok {
		_ = p.Close()
		return nil, fmt.Errorf("app: resource %q is not an sms.hub", opts.HubName)
	}
	if err := hub.Start(ctx, Runner(p)); err != nil {
		_ = p.Close()
		return nil, err
	}
	return &App{Platform: p, Hub: hub}, nil
}

// Runner returns the IntentRunner that executes a hub job as a BCL intent.
func Runner(p *platform.Platform) sms.IntentRunner {
	return func(ctx context.Context, intent string, payload []byte, headers map[string]string) error {
		id := fmt.Sprintf("job-%d", time.Now().UnixNano())
		inv := &invocation.Invocation{
			ID:        invocation.ID(id),
			Intent:    invocation.IntentID(intent),
			Input:     invocation.NewInput(payload, "application/json"),
			Metadata:  invocation.NewQueueMeta(intent, 0, id, headers),
			Transport: invocation.Transport{Protocol: "queue"},
			Received:  time.Now(),
		}
		result, err := p.Engine.Dispatch(ctx, inv)
		if result != nil {
			defer runtime.ReleaseDispatchResult(result)
		}
		return err
	}
}

// Mount adds the application's HTTP routes to app, with a health endpoint.
func (a *App) Mount(app *fh.App) error {
	if err := a.Platform.Mount(app); err != nil {
		return err
	}
	app.Get("/healthz", func(c fh.Ctx) error {
		status := 200
		body := map[string]any{"status": "ok"}
		if err := a.Ping(c.Context()); err != nil {
			status, body = 503, map[string]any{"status": "unavailable", "error": err.Error()}
		}
		c.Status(status)
		return c.JSON(body)
	})
	return nil
}

// Ping checks the database the hub depends on.
func (a *App) Ping(ctx context.Context) error {
	if a.closed {
		return errors.New("shutting down")
	}
	_, err := a.Hub.Store.Balance(ctx, "__ping__")
	if err != nil && !errors.Is(err, sms.ErrNotFound) && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	return nil
}

// Close stops the application: the hub first, then every resource.
func (a *App) Close() error {
	a.closed = true
	return a.Platform.Close()
}
