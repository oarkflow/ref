// Package ops is process-level operational wiring: maintenance mode today,
// the kind of thing a future product will want without redesigning
// anything — a shared toggle, an HTTP middleware, a health check, and one
// BCL-reachable action to flip it. None of it is business logic, which is
// why it lives beside cmd/server rather than in resources/config/.
package ops

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/oarkflow/fh"
	"github.com/oarkflow/ref/platform"
)

const defaultMessage = "The service is temporarily down for maintenance."

// MaintenanceGate is a process-wide, concurrency-safe on/off switch. One
// instance is created in cmd/server/main.go and shared by the HTTP
// middleware, the readiness check and the "ops.maintenance_set" action
// below — the same value observed and changed from three different places.
type MaintenanceGate struct {
	on      atomic.Bool
	message atomic.Pointer[string]
	// since records when the gate last turned on, for the maintenance
	// page's "down since" line. It is only meaningful while On() is true.
	since atomic.Pointer[time.Time]
	// RetryAfterSeconds is sent as both the Retry-After header and a
	// template variable on the HTML/JSON response. 0 omits the header —
	// use it when you don't have an estimate.
	RetryAfterSeconds int
}

// NewMaintenanceGate builds a gate, defaulting message when empty.
func NewMaintenanceGate(on bool, message string) *MaintenanceGate {
	g := &MaintenanceGate{}
	g.Set(on, message)
	return g
}

// Set flips the gate. An empty message leaves the previous one in place (or
// falls back to defaultMessage on the very first call), so turning
// maintenance off doesn't need to repeat the reason turning it on gave.
func (g *MaintenanceGate) Set(on bool, message string) {
	switch {
	case message != "":
		g.message.Store(&message)
	case g.message.Load() == nil:
		fallback := defaultMessage
		g.message.Store(&fallback)
	}
	if on && !g.on.Load() {
		now := time.Now()
		g.since.Store(&now)
	}
	g.on.Store(on)
}

// Since reports when the gate last turned on. The zero time means it has
// never been on, or is off now and its last "on" time isn't tracked past
// the next Set(false, ...) — callers only need this while On() is true.
func (g *MaintenanceGate) Since() time.Time {
	if t := g.since.Load(); t != nil {
		return *t
	}
	return time.Time{}
}

// On reports whether maintenance mode is currently active.
func (g *MaintenanceGate) On() bool { return g.on.Load() }

// Message is the reason shown to a caller refused by the middleware, or
// reported by the readiness check.
func (g *MaintenanceGate) Message() string {
	if m := g.message.Load(); m != nil {
		return *m
	}
	return defaultMessage
}

// exemptPrefixes never get the maintenance response: health checks and
// static assets need to keep working, and the toggle route itself must stay
// reachable so an admin can turn maintenance back off without a redeploy.
var exemptPrefixes = []string{"/health", "/livez", "/readyz", "/metrics", "/static/", "/api/v1/admin/maintenance"}

// Middleware answers every request with 503 while the gate is on, except
// the exempt prefixes above. Register it with app.Use before p.Mount so it
// runs ahead of every BCL-declared route.
//
// A browser (Accept: text/html) gets the SPL page at MaintenancePage,
// rendered exactly the way a BCL route with `template`/`layout` would be
// (see cmd/server/main.go's fh.WithTemplateEngine) — maintenance mode gets
// its own look, variables and everything else a normal page has, it just
// isn't declared as a `route` because it must run before routing decides
// which one matched. Any other caller (a JSON API client) gets the same
// information as a JSON error body instead.
func (g *MaintenanceGate) Middleware() fh.Handler {
	return func(c fh.Ctx) error {
		if !g.On() {
			return c.Next()
		}
		path := c.Path()
		for _, prefix := range exemptPrefixes {
			if strings.HasPrefix(path, prefix) {
				return c.Next()
			}
		}
		if g.RetryAfterSeconds > 0 {
			c.Set("Retry-After", fmt.Sprintf("%d", g.RetryAfterSeconds))
		}
		c.Status(503)
		if c.Accepts("text/html", "application/json") == "text/html" {
			return c.Render(MaintenancePage, map[string]any{
				"title":             "Maintenance",
				"message":           g.Message(),
				"since":             g.Since().Format(time.RFC1123),
				"retryAfterSeconds": g.RetryAfterSeconds,
			}, MaintenanceLayout)
		}
		return c.JSON(map[string]any{
			"error": map[string]any{
				"code":                "MAINTENANCE",
				"message":             g.Message(),
				"since":               g.Since().Format(time.RFC3339),
				"retry_after_seconds": g.RetryAfterSeconds,
			},
		})
	}
}

// MaintenancePage and MaintenanceLayout name the SPL template rendered
// above. They're exported vars, not constants, so a product that reorganises
// templates/ can repoint them in main() without editing this package.
var (
	MaintenancePage   = "pages/errors/maintenance"
	MaintenanceLayout = "layouts/auth"
)

// HealthCheck fails while maintenance is on. Wire it with
// healthRegistry.Register (readiness only, not liveness): a load balancer
// should stop routing here, but the process itself is fine and should not
// be restarted.
func (g *MaintenanceGate) HealthCheck(context.Context) error {
	if g.On() {
		return fmt.Errorf("maintenance mode is on: %s", g.Message())
	}
	return nil
}

// RegisterAction installs "ops.maintenance_set" into every Registry created
// from this point on, so a BCL route can flip this gate. Call it once,
// before platform.LoadDir/LoadFiles/Compile.
//
// This is deliberately the concrete example this starter ships for "an
// easy plugin point for a third-party tool": platform.RegisterActionDriver
// is the same extension point a real integration (paging a status page,
// posting to Slack, whatever a future product needs) would use — a small
// Go function, registered once, then reachable from BCL like any built-in
// action.
func (g *MaintenanceGate) RegisterAction() {
	platform.RegisterActionDriver("ops.maintenance_set",
		platform.ActionFactoryFunc(func(_ platform.BuildContext, spec platform.NodeSpec) (platform.Action, error) {
			return platform.ActionFunc(func(ctx *platform.ActionContext) (platform.ActionResult, error) {
				input, _ := ctx.Inputs["input"].(map[string]any)
				on, _ := input["on"].(bool)
				message, _ := input["message"].(string)
				if seconds, ok := input["retry_after_seconds"].(float64); ok {
					g.RetryAfterSeconds = int(seconds)
				}
				g.Set(on, message)
				if len(spec.Provides) == 0 {
					return platform.ActionResult{}, nil
				}
				return platform.ActionResult{Outputs: map[string]any{
					spec.Provides[0]: map[string]any{
						"maintenance":         g.On(),
						"message":             g.Message(),
						"retry_after_seconds": g.RetryAfterSeconds,
					},
				}}, nil
			}), nil
		}),
		platform.ActionInfo{
			Family:   "ops",
			Summary:  "Toggle maintenance mode for the whole process",
			Kind:     "effect",
			Provides: "{ maintenance, message }",
		},
	)

	// "ops.maintenance_status" is the other half of the same story: the HTTP
	// middleware already refuses every route while maintenance is on, but a
	// worker, a schedule or a webhook trigger runs an intent directly,
	// bypassing HTTP entirely — nothing stops a background job from writing
	// to a database mid-migration just because maintenance mode is "on".
	// A node using this action, required by whatever should pause, closes
	// that gap explicitly per intent (see resources/config/03_intents.bcl's
	// notify.welcome and notification.password_reset for the pattern:
	// requires it, then a validate.expression node gates on it).
	platform.RegisterActionDriver("ops.maintenance_status",
		platform.ActionFactoryFunc(func(_ platform.BuildContext, spec platform.NodeSpec) (platform.Action, error) {
			return platform.ActionFunc(func(*platform.ActionContext) (platform.ActionResult, error) {
				if len(spec.Provides) == 0 {
					return platform.ActionResult{}, nil
				}
				return platform.ActionResult{Outputs: map[string]any{spec.Provides[0]: g.On()}}, nil
			}), nil
		}),
		platform.ActionInfo{
			Family:   "ops",
			Summary:  "Whether maintenance mode is currently on",
			Kind:     "pure",
			Provides: "bool",
		},
	)
}
