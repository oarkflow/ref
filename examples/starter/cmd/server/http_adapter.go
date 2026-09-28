package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"

	"github.com/oarkflow/fh"
)

// wrapHTTPHandler bridges a stdlib net/http.Handler (health.LivenessHandler,
// health.ReadinessHandler) onto *fh.App, which has no native adapter for
// one. These two endpoints expose process internals, not application
// intents, which is why they are mounted here in Go rather than declared as
// BCL routes.
func wrapHTTPHandler(h http.Handler) fh.HandlerFunc {
	return func(c fh.Ctx) error {
		req, err := http.NewRequestWithContext(c.Context(), c.Method(), c.OriginalURL(), bytes.NewReader(c.Body()))
		if err != nil {
			return c.Status(http.StatusInternalServerError).SendString("failed to build request")
		}
		for name, values := range c.GetReqHeaders() {
			for _, value := range values {
				req.Header.Add(name, value)
			}
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		for name, values := range rec.Header() {
			for _, value := range values {
				c.Append(name, value)
			}
		}
		c.Status(rec.Code)
		return c.Send(rec.Body.Bytes())
	}
}
