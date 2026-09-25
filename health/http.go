package health

import (
	"encoding/json"
	"net/http"
)

// statusCode maps a Report's Status to the HTTP status code written by the
// handlers below: 200 for StatusUp, 503 for StatusDegraded or StatusDown.
func statusCode(s Status) int {
	if s == StatusUp {
		return http.StatusOK
	}
	return http.StatusServiceUnavailable
}

func writeReport(w http.ResponseWriter, report Report) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(statusCode(report.Status))
	// Report implements json.Marshaler, so encoding errors here are not
	// expected; if one still occurs there is nothing further useful to
	// write since the status line/headers are already sent.
	_ = json.NewEncoder(w).Encode(report)
}

// LivenessHandler returns an http.Handler that runs the Registry's liveness
// checks and writes the resulting Report as JSON, with a 200 status for
// StatusUp and 503 otherwise.
func LivenessHandler(reg *Registry) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeReport(w, reg.Liveness(r.Context()))
	})
}

// ReadinessHandler returns an http.Handler that runs the Registry's full
// set of checks (liveness + readiness) and writes the resulting Report as
// JSON, with a 200 status for StatusUp and 503 otherwise.
func ReadinessHandler(reg *Registry) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeReport(w, reg.Readiness(r.Context()))
	})
}

// NewHTTPHandler returns a single http.Handler that serves liveness at
// "/livez" and readiness at "/readyz" (and, for convenience, at "/healthz"
// as an alias for readiness). Mount it under any prefix with
// http.StripPrefix, or use LivenessHandler/ReadinessHandler directly to
// pick your own paths.
func NewHTTPHandler(reg *Registry) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/livez", LivenessHandler(reg))
	mux.Handle("/readyz", ReadinessHandler(reg))
	mux.Handle("/healthz", ReadinessHandler(reg))
	return mux
}
