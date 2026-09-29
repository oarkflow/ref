package main

import (
	"errors"
	"strings"

	"github.com/oarkflow/fh"
	"github.com/oarkflow/zlog"

	"github.com/oarkflow/ref/platform"
)

// registerHTMLErrorPages wires HTML rendering into the two places a request
// can fail before reaching an intent's own response:
//
//   - platform.RegisterFailureRenderer catches everything platform/routes.go's
//     projectFailure produces — an RBAC denial, a validation failure, an
//     idempotency conflict, ... — for a route that exists and matched.
//   - fh.WithErrorHandler catches everything platform never sees at all: a
//     path that matches no route (404), a method no route on that path
//     allows (405), a panic recovered mid-request. defaultNotFoundHandler
//     and friends (github.com/oarkflow/fh) would otherwise answer these
//     with the same generic JSON body regardless of what the caller asked
//     for.
//
// Both share renderHTMLError below, so a 403 from an RBAC gate and a 404
// from an unmatched path land on the exact same page shape. A JSON API
// caller (curl, fetch, a mobile client — anything not sending
// Accept: text/html, and anything under /api/ even if it did) still gets
// exactly the JSON it always got; nothing here changes that contract.
func registerHTMLErrorPages() {
	platform.RegisterFailureRenderer(func(c fh.Ctx, status int, body map[string]any) bool {
		code, _ := body["code"].(string)
		message, _ := body["message"].(string)
		return renderHTMLError(c, status, code, message)
	})
}

// htmlAwareErrorHandler replaces fh's default ErrorHandler (which just calls
// c.SafeErrorResponse — always JSON) with one that renders HTML first, for
// the framework-level failures platform's own routing never gets a chance
// to see. A status >= 500 is still logged, the same as the default handler
// did.
func htmlAwareErrorHandler(logger *zlog.Logger) fh.ErrorHandler {
	return func(c fh.Ctx, err error) {
		status, code, message := 500, "INTERNAL_ERROR", "internal error"
		var httpErr *fh.HTTPError
		if errors.As(err, &httpErr) {
			status, code, message = httpErr.Status, httpErr.Code, httpErr.Message
		}
		if status >= 500 {
			logger.Error("request error", zlog.String("method", c.Method()), zlog.String("path", c.Path()), zlog.Err(err))
		}
		if renderHTMLError(c, status, code, message) {
			return
		}
		_ = c.SafeErrorResponse(err)
	}
}

// renderHTMLError renders resources/templates/pages/errors/error.html when
// the request both prefers HTML and isn't a JSON API call, returning true
// when it did. A render failure (a genuinely broken template, say) falls
// through to the caller's own JSON fallback rather than risk a blank page.
func renderHTMLError(c fh.Ctx, status int, code, message string) bool {
	if strings.HasPrefix(c.Path(), "/api/") {
		return false
	}
	if c.Accepts("text/html", "application/json") != "text/html" {
		return false
	}

	primaryHref, primaryLabel := "/dashboard", "Return to dashboard"
	secondaryHref, secondaryLabel := "", ""
	switch status {
	case 401:
		primaryHref, primaryLabel = "/login", "Sign in"
	case 403:
		secondaryHref, secondaryLabel = "/login", "Switch account"
	}

	err := c.Status(status).Render("pages/errors/error", map[string]any{
		"status":         status,
		"code":           code,
		"message":        message,
		"title":          titleForStatus(status),
		"primaryHref":    primaryHref,
		"primaryLabel":   primaryLabel,
		"secondaryHref":  secondaryHref,
		"secondaryLabel": secondaryLabel,
	}, "layouts/error")
	return err == nil
}

func titleForStatus(status int) string {
	switch status {
	case 401:
		return "Sign-in required"
	case 403:
		return "Access denied"
	case 404:
		return "Page not found"
	case 405:
		return "Method not allowed"
	case 409:
		return "Conflict"
	case 422:
		return "Invalid request"
	case 429:
		return "Too many requests"
	case 503:
		return "Service unavailable"
	case 504:
		return "Timed out"
	default:
		if status >= 500 {
			return "Something went wrong"
		}
		return "Request failed"
	}
}
