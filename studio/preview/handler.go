package preview

import (
	"context"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"
)

func (g *generation) initProxy(prefix string) {
	g.transport = &http.Transport{
		DialContext:           func(ctx context.Context, _, _ string) (net.Conn, error) { return g.ln.dial(ctx) },
		DisableCompression:    true,
		MaxIdleConnsPerHost:   8,
		IdleConnTimeout:       30 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second,
	}
	g.proxy = &httputil.ReverseProxy{
		Director: func(r *http.Request) {
			r.URL.Scheme = "http"
			r.URL.Host = "preview"
		},
		Transport:      g.transport,
		ModifyResponse: func(resp *http.Response) error { return modifyResponse(resp, prefix) },
		FlushInterval:  -1, // stream (SSE) responses through
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			writeJSONError(w, http.StatusBadGateway, "preview_upstream", err.Error())
		},
	}
}

// statusWriter captures the status code and keeps Flush working for streams.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// Handler implements Manager.
func (s *Service) Handler() http.Handler { return http.HandlerFunc(s.serve) }

func (s *Service) serve(w http.ResponseWriter, r *http.Request) {
	prefix := s.opts.Prefix
	if !strings.HasPrefix(r.URL.Path, prefix) {
		writeJSONError(w, http.StatusNotFound, "preview_not_found", "not a preview path")
		return
	}
	id, tail, hasSlash := strings.Cut(r.URL.Path[len(prefix):], "/")
	e := s.entry(id, false)
	var g *generation
	if e != nil {
		g = e.current()
	}
	if id == "" || g == nil {
		writeJSONError(w, http.StatusNotFound, "preview_not_found", "no running preview for draft "+id)
		return
	}
	if !hasSlash { // /preview/{id} -> /preview/{id}/
		target := prefix + id + "/"
		if r.URL.RawQuery != "" {
			target += "?" + r.URL.RawQuery
		}
		http.Redirect(w, r, target, http.StatusTemporaryRedirect)
		return
	}
	g.touch()

	out := r.Clone(r.Context())
	out.URL = &url.URL{Path: "/" + tail, RawQuery: r.URL.RawQuery}
	out.RequestURI = ""
	out.Header = r.Header.Clone()
	out.Header.Del("Accept-Encoding") // keep HTML bodies rewritable
	out.Header.Set("X-Forwarded-Prefix", strings.TrimSuffix(prefix, "/")+"/"+id)
	out.Header.Set("X-Studio-Preview", "1")

	sw := &statusWriter{ResponseWriter: w}
	start := time.Now()
	g.proxy.ServeHTTP(sw, out)
	if sw.status == 0 {
		sw.status = http.StatusOK
	}
	route, intent := matchRoute(g.routes, r.Method, out.URL.Path)
	g.rec.requests.add(RequestEntry{
		Time: start, Method: r.Method, Path: out.URL.Path, Query: r.URL.RawQuery, Status: sw.status,
		DurationMs: float64(time.Since(start).Microseconds()) / 1000, Route: route, Intent: intent, Version: g.version,
	})
}
