package platform

import (
	"bytes"
	"context"
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
		},
	})
}

// tcpguardWrapper wraps oarkflow/tcpguard.Guard as a REF platform resource.
type tcpguardWrapper struct {
	guard *tcpguard.Guard
}

func openTCPGuard(_ context.Context, spec ResourceSpec) (Resource, io.Closer, error) {
	if err := rejectUnknownConfig("security.tcpguard", spec.Config, "path", "source", "mode"); err != nil {
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
	)
	if err != nil {
		return nil, nil, fmt.Errorf("resource %q: %w", spec.Name, err)
	}

	return &tcpguardWrapper{guard: guard}, noopCloser{}, nil
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
