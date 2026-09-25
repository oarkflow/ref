package web

import (
	"bytes"
	"net/http"
	"net/http/httptest"

	"github.com/oarkflow/fh"
)

// WrapHTTPHandler adapts a standard library net/http.Handler into an
// fh.HandlerFunc, so stdlib-shaped handlers — promhttp.Handler(),
// health.LivenessHandler(reg), health.ReadinessHandler(reg), or any other
// third-party http.Handler — can be mounted directly on an *fh.App without
// fh needing to know about them.
//
// fh's Ctx has no native net/http.Handler adapter, so this builds a
// synthetic *http.Request from the fh.Ctx, runs the handler against an
// httptest.ResponseRecorder, and copies the recorded status/headers/body
// back onto the fh.Ctx. This is intentionally simple: it is meant for
// low-volume operational endpoints (metrics scrapes, health probes), not
// as a general-purpose high-throughput proxy layer.
func WrapHTTPHandler(h http.Handler) fh.HandlerFunc {
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
