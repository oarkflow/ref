package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/oarkflow/fh"
	"github.com/oarkflow/tcpguard"

	"github.com/oarkflow/ref/intent"
)

// ---------------------------------------------------------------------------
// security.tcpguard — Network/API abuse protection via oarkflow/tcpguard
// ---------------------------------------------------------------------------
//
// Where ratelimit.* throttles a single route by a fixed window, this is a
// deployment-wide perimeter guard: credential-stuffing and password-spray
// detection, endpoint-scanning and user-agent-rotation detection, injection/
// path-traversal/SSRF request-shape probes, and graduated risk-scored
// decisions (allow, monitor, challenge, throttle, block) — all declared in
// tcpguard's own BCL policy files, not Go code.
//
// Declaring this resource is enough: Compile finds the first one in the
// document and Mount's request pipeline (routes.go's serve) evaluates every
// request through it before authentication runs, the same way database.sql's
// ping check needs no separate "enable" flag elsewhere in the document.
//
//	resource "guard" {
//	  kind "security.tcpguard"
//	  config {
//	    path "./security"   # a directory tcpguard.LoadTCPGuardBundleDir reads
//	    mode "enforce"       # or "monitor" to log findings without blocking
//	  }
//	}
//
// A guard decision that blocks the request is projected through the same
// intent.Failure -> HTTP status mapping every other failure in this platform
// uses, so a blocked request looks like any other 403/429 to a client, not a
// special case.
func registerSecurityResources(r *Registry) {
	mustResource(r, "security.tcpguard", ResourceFactoryFunc(openTCPGuard), ResourceKindInfo{
		Family:   "security",
		Summary:  "Network/API abuse-detection perimeter guard (github.com/oarkflow/tcpguard): abuse velocity, attack-probe and rate-abuse detection with graduated allow/monitor/challenge/throttle/block decisions, declared as a BCL policy pack.",
		Provides: []string{"TCPGuard"},
		Config: []ConfigField{
			{Name: "path", Type: "string", Summary: "Directory tcpguard.LoadTCPGuardBundleDir reads (a pack + rule files, optionally an .authz file). Mutually exclusive with source."},
			{Name: "source", Type: "string", Summary: "Inline tcpguard BCL policy source, as an alternative to path for a single-file policy."},
			{Name: "mode", Type: "string", Default: "enforce", Summary: "\"enforce\" to actually block/throttle, \"monitor\" to only record findings and let every request through."},
			{Name: "identity_fields", Type: "[]string", Summary: "Top-level JSON body field names tried in order (e.g. [\"email\", \"username\"]) to populate the per-request identity a rule's abuse.auth.user_failures/distinct_users signals key on. Without this every request's identity is empty, and every account's failures are counted together under that one empty key — the per-account half of a rate check degrades to a no-op; the per-IP half is unaffected."},
		},
	})
}

func registerSecurityActions(r *Registry) {
	mustAction(r, "security.report_event", securityReportEventAction, ActionInfo{
		Family:       "security",
		Summary:      "Report a business outcome (a login failure, a password-spray-shaped signup burst, ...) to the guard's abuse detectors. The guard's own per-request evaluation (serve()'s automatic check) only ever sees \"request.received\" — it has no way to know a login attempt failed, since that outcome is decided after the guard already ran. Rules keyed on an event this reports (auth-abuse-velocity's abuse.auth.ip_failures, for one) cannot fire without it.",
		ResourceKind: "security.tcpguard",
		Kind:         "effect",
		Config: []ConfigField{
			{Name: "event_type", Type: "string", Required: true, Summary: "e.g. \"auth.login_failed\" or \"auth.login_success\" — matched by a rule's trigger { on ... }"},
		},
	})
}

// securityReportEventAction re-derives the same *tcpguard.Context the
// automatic per-request check would have built (same request, same IP,
// same headers) and evaluates a caller-named event against it — the same
// low-level Guard.Evaluate the automatic check uses, just with an event
// type and timing the request pipeline itself cannot know in advance.
//
// A node using this needs the same http context the automatic check reads
// (httpContextKey, set once per request in routes.go's serve) — it is
// only ever meaningful on an HTTP-triggered intent, not a
// worker/schedule/webhook one, which is why it silently does nothing
// (acknowledgement(spec, false), no error) rather than failing when that
// context is absent: a queue worker retrying this same intent must not
// fail on account of a guard integration it was never reachable through.
var securityReportEventAction = ActionFactoryFunc(func(build BuildContext, spec NodeSpec) (Action, error) {
	guard, err := requireResource[*tcpguardWrapper](build, spec, "a security.tcpguard resource")
	if err != nil {
		return nil, err
	}
	eventType, err := requiredString(spec.Config, "event_type")
	if err != nil {
		return nil, fmt.Errorf("node %q: %w", spec.Name, err)
	}

	return ActionFunc(func(ctx *ActionContext) (ActionResult, error) {
		httpCtx, ok := ctx.Context.Value(httpContextKey{}).(fh.Ctx)
		if !ok {
			return acknowledgement(spec, false), nil
		}
		return acknowledgement(spec, guard.report(httpCtx, eventType)), nil
	}), nil
})

// tcpguardWrapper wraps oarkflow/tcpguard.Guard as a REF platform resource.
type tcpguardWrapper struct {
	guard   *tcpguard.Guard
	builder tcpguard.HTTPContextBuilder
}

// jsonIdentityExtractor tries each of fields, in order, as a top-level key
// in the request body decoded as JSON, and sets the first non-empty string
// value found as the request's identity — the "who" abuse.auth.
// user_failures/distinct_users (and any other per-account signal) keys on.
// The body was already fully buffered by fh before this runs
// (httpRequestFromCtx), so decoding it here is a second parse of an
// in-memory buffer, not a second network read.
func jsonIdentityExtractor(fields []string) func(*http.Request, *tcpguard.Context) {
	return func(r *http.Request, sec *tcpguard.Context) {
		if len(fields) == 0 || r.Body == nil {
			return
		}
		raw, err := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(raw))
		if err != nil || len(raw) == 0 {
			return
		}
		var body map[string]any
		if json.Unmarshal(raw, &body) != nil {
			return
		}
		for _, field := range fields {
			if value, ok := body[field].(string); ok && value != "" {
				sec.Identity.ID = value
				return
			}
		}
	}
}

func openTCPGuard(_ context.Context, spec ResourceSpec) (Resource, io.Closer, error) {
	if err := rejectUnknownConfig("security.tcpguard", spec.Config, "path", "source", "mode", "identity_fields"); err != nil {
		return nil, nil, err
	}
	path := configString(spec.Config, "path", "")
	source := configString(spec.Config, "source", "")
	if (path == "") == (source == "") {
		return nil, nil, fmt.Errorf("resource %q: security.tcpguard needs exactly one of \"path\" or \"source\"", spec.Name)
	}
	modeName := configString(spec.Config, "mode", "enforce")
	var mode tcpguard.Mode
	switch modeName {
	case "enforce":
		mode = tcpguard.Enforce
	case "monitor":
		mode = tcpguard.Monitor
	default:
		return nil, nil, fmt.Errorf("resource %q: mode must be \"enforce\" or \"monitor\", got %q", spec.Name, modeName)
	}
	builder := tcpguard.HTTPContextBuilder{
		IdentityExtractor: jsonIdentityExtractor(configStrings(spec.Config, "identity_fields")),
	}

	ctx := context.Background()
	var bundle tcpguard.Bundle
	var err error
	if path != "" {
		bundle, err = tcpguard.LoadTCPGuardBundleDir(ctx, path)
	} else {
		bundle, err = tcpguard.ParseTCPGuardBundle([]byte(source))
	}
	if err != nil {
		return nil, nil, fmt.Errorf("resource %q: loading tcpguard policy: %w", spec.Name, err)
	}

	guard, err := tcpguard.New(
		tcpguard.WithBundle(bundle),
		tcpguard.WithStore(tcpguard.NewMemoryStore()),
		tcpguard.WithMetrics(tcpguard.NewMemoryMetrics()),
		tcpguard.WithMode(mode),
		tcpguard.WithContextBuilder(builder),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("resource %q: %w", spec.Name, err)
	}

	return &tcpguardWrapper{guard: guard, builder: builder}, noopCloser{}, nil
}

// check evaluates the request through the guard. It returns handled=true
// when the caller must stop and not call ctx.Next() — either the guard
// blocked the request (in which case the failure has already been written)
// or a genuine evaluation error occurred, which fails open (logs and lets
// the request through) rather than turning a guard malfunction into a
// self-inflicted outage.
func (w *tcpguardWrapper) check(c fh.Ctx) (handled bool, err error) {
	req, buildErr := httpRequestFromCtx(c)
	if buildErr != nil {
		return false, nil
	}
	result, evalErr := w.guard.EvaluateHTTPRequest(req)
	if evalErr != nil {
		return false, nil
	}
	if !result.Enforced {
		return false, nil
	}
	return true, projectFailure(c, guardFailure(result))
}

// report evaluates a caller-named event (not "request.received") against a
// freshly built *tcpguard.Context for c's request — the same one the
// automatic check above would have built, just at a different point in
// time with a different event type. It never blocks or writes a response:
// unlike check, this always runs after the route has already decided its
// own outcome (see SecurityEventSpec), so all it does is feed the guard's
// stateful detectors for a *future* request to act on. Returns false
// (never an error) when the request or context could not be rebuilt,
// matching check's own fail-open posture.
func (w *tcpguardWrapper) report(c fh.Ctx, eventType string) bool {
	req, err := httpRequestFromCtx(c)
	if err != nil {
		return false
	}
	sec, err := w.builder.BuildHTTP(c.Context(), req)
	if err != nil {
		return false
	}
	w.guard.Evaluate(c.Context(), tcpguard.Event{Type: eventType}, sec)
	return true
}

// guardFailure maps a tcpguard decision onto this platform's own failure
// algebra, so a blocked request looks like any other 403/429 to a client
// instead of a bespoke response shape.
func guardFailure(result tcpguard.HTTPRequestResult) intent.Failure {
	category := intent.CategoryPermission
	code := "REQUEST_BLOCKED"
	if result.Decision.Effect == "throttle" {
		category = intent.CategoryRateLimit
		code = "REQUEST_THROTTLED"
	}
	message := result.Decision.Explanation
	if message == "" {
		message = "this request was refused by the security guard"
	}
	return intent.Failure{Code: code, Category: category, Message: message}
}

// httpRequestFromCtx adapts fh's own Ctx into a stdlib *http.Request, which
// is the shape tcpguard's detectors and context builder are written
// against. Unlike a minimal adapter, this preserves the body (detectors
// inspect it for injection/SSRF probes and payload size) and every request
// header (case, multi-value) rather than a hand-picked subset.
func httpRequestFromCtx(c fh.Ctx) (*http.Request, error) {
	host := c.Hostname()
	if host == "" {
		host = "localhost"
	}
	target := c.OriginalURL()
	if target == "" {
		target = c.Path()
	}
	req, err := http.NewRequestWithContext(c.Context(), c.Method(), "http://"+host+target, bytes.NewReader(c.Body()))
	if err != nil {
		return nil, err
	}
	header := make(http.Header, len(c.GetReqHeaders()))
	for k, vs := range c.GetReqHeaders() {
		header[k] = append([]string(nil), vs...)
	}
	req.Header = header
	remote := c.IP()
	if remote != "" && !strings.Contains(remote, ":") {
		remote += ":0"
	}
	req.RemoteAddr = remote
	return req, nil
}
