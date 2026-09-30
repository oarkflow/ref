package main

// A fake live preview for the mock server: enough of the real contract
// (POST/DELETE /drafts/{id}/preview, /drafts/{id}/preview/requests, SSE
// `preview` events, and /preview/{id}/... pages) to build and test the preview
// UI offline. The "app" it serves is generated from the built snapshot's route
// blocks, so editing a route's path visibly changes what the preview serves.

import (
	"fmt"
	"html"
	"math/rand"
	"net/http"
	"strings"
	"time"

	"github.com/oarkflow/ref/studio/model"
)

// mockBuildDelay is how long a fake build takes, long enough to see the
// "Updating…" state and to exercise debouncing.
const mockBuildDelay = 400 * time.Millisecond

type recorded struct {
	At         time.Time      `json:"at"`
	Kind       string         `json:"kind"`
	Method     string         `json:"method,omitempty"`
	URL        string         `json:"url,omitempty"`
	Status     int            `json:"status,omitempty"`
	DurationMS float64        `json:"durationMs,omitempty"`
	Detail     map[string]any `json:"detail,omitempty"`
}

type mockPreview struct {
	status   string // starting | ready | failed | stopped
	built    int64
	snapshot map[string]string
	log      []recorded
}

type mockRoute struct{ id, method, path, intent string }

func (d *draft) hasErrors() []diag {
	var out []diag
	for _, x := range d.diags {
		if x.Severity == "error" {
			out = append(out, x)
		}
	}
	return out
}

func routesOfSnapshot(snap map[string]string) []mockRoute {
	var out []mockRoute
	for name, src := range snap {
		f, err := model.Open(name, []byte(src))
		if err != nil {
			continue
		}
		for _, n := range tree(f, true) {
			if n.Type != "route" {
				continue
			}
			r := mockRoute{id: n.ID}
			for _, c := range n.Children {
				v := strings.Trim(c.Raw, `"`)
				switch c.Name {
				case "method":
					r.method = strings.ToUpper(v)
				case "path":
					r.path = v
				case "intent":
					r.intent = v
				}
			}
			if r.path != "" {
				out = append(out, r)
			}
		}
	}
	return out
}

func matchPath(pattern, path string) bool {
	pat := strings.Split(strings.Trim(pattern, "/"), "/")
	got := strings.Split(strings.Trim(path, "/"), "/")
	if len(pat) != len(got) {
		return false
	}
	for i := range pat {
		if strings.HasPrefix(pat[i], "{") || strings.HasPrefix(pat[i], ":") {
			continue
		}
		if pat[i] != got[i] {
			return false
		}
	}
	return true
}

func (s *server) previewRoutes(mux *http.ServeMux) {
	const p = "/api/v1"
	mux.HandleFunc("POST "+p+"/drafts/{id}/preview", s.auth("editor", s.startPreview))
	mux.HandleFunc("DELETE "+p+"/drafts/{id}/preview", s.auth("editor", func(w http.ResponseWriter, r *http.Request, who identity) {
		s.mu.Lock()
		defer s.mu.Unlock()
		d := s.getDraft(w, r, who)
		if d == nil {
			return
		}
		if d.pv != nil {
			d.pv.status = "stopped"
		}
		d.broadcast("preview", map[string]any{"status": "stopped"})
		s.log(who.Name, "preview.stop", d.id, "")
		w.WriteHeader(http.StatusNoContent)
	}))
	mux.HandleFunc("GET "+p+"/drafts/{id}/preview/requests", s.auth("editor", func(w http.ResponseWriter, r *http.Request, who identity) {
		s.mu.Lock()
		defer s.mu.Unlock()
		d := s.getDraft(w, r, who)
		if d == nil {
			return
		}
		out := []recorded{}
		if d.pv != nil {
			out = append(out, d.pv.log...)
		}
		writeJSON(w, 200, out)
	}))
	mux.HandleFunc("/preview/{id}/{rest...}", s.servePreview)
}

func (s *server) startPreview(w http.ResponseWriter, r *http.Request, who identity) {
	s.mu.Lock()
	d := s.getDraft(w, r, who)
	if d == nil {
		s.mu.Unlock()
		return
	}
	ver := d.version
	errs := d.hasErrors()
	snap := d.snapshot()
	if d.pv == nil {
		d.pv = &mockPreview{}
	}
	d.pv.status = "starting"
	d.broadcast("preview", map[string]any{"status": "starting", "version": ver})
	s.log(who.Name, "preview.start", d.id, fmt.Sprint("v", ver))
	id := d.id
	s.mu.Unlock()

	time.Sleep(mockBuildDelay)

	s.mu.Lock()
	defer s.mu.Unlock()
	d = s.drafts[id]
	if d == nil {
		fail(w, 404, "not_found", "draft deleted", nil)
		return
	}
	if len(errs) > 0 {
		// A failed rebuild keeps the previous build serving, as the real manager does.
		if d.pv.built == 0 {
			d.pv.status = "failed"
		} else {
			d.pv.status = "ready"
		}
		body := map[string]any{"status": "failed", "version": ver, "error": errs}
		d.broadcast("preview", body)
		writeJSON(w, 200, body)
		return
	}
	d.pv.status, d.pv.built, d.pv.snapshot = "ready", ver, snap
	body := map[string]any{"status": "ready", "url": "/preview/" + id + "/", "version": ver}
	http.SetCookie(w, &http.Cookie{Name: "studio_preview", Value: id, Path: "/preview/" + id + "/", HttpOnly: true, SameSite: http.SameSiteLaxMode})
	d.broadcast("preview", body)
	writeJSON(w, 200, body)
}

const pageTmpl = `<!doctype html><meta charset="utf-8"><title>%s</title>
<style>body{font:15px system-ui;margin:0;padding:24px;min-height:%dpx;background:linear-gradient(#fff,#eef)}
code{background:#eee;padding:1px 5px;border-radius:4px}nav a{margin-right:12px}footer{margin-top:1400px;color:#666}</style>
<nav><a href=".">Home</a><a href="send-welcome">Send welcome email</a><a href="boom">Boom</a></nav>%s
<footer>Mock preview — built from draft version %d</footer>`

func (s *server) servePreview(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	id, rest := r.PathValue("id"), r.PathValue("rest")
	s.mu.Lock()
	d := s.drafts[id]
	if d == nil || d.pv == nil || d.pv.snapshot == nil || d.pv.status == "stopped" {
		s.mu.Unlock()
		fail(w, 404, "not_found", "no preview for this draft", nil)
		return
	}
	snap, built := d.pv.snapshot, d.pv.built
	s.mu.Unlock()

	path := "/" + rest
	status, route, intent := 200, "", ""
	var body string
	var outbound *recorded
	switch {
	case rest == "":
		var b strings.Builder
		b.WriteString("<h1>Preview</h1><p>Routes in this build:</p><ul>")
		for _, rt := range routesOfSnapshot(snap) {
			fmt.Fprintf(&b, `<li><code>%s</code> <a href="%s">%s</a> <small>%s</small></li>`, html.EscapeString(rt.method), html.EscapeString(strings.TrimPrefix(rt.path, "/")), html.EscapeString(rt.path), html.EscapeString(rt.id))
		}
		b.WriteString("</ul>")
		body = b.String()
	case rest == "send-welcome":
		payload := `{"to":"new.user@example.com","subject":"Welcome to the app","body":"Hello and welcome! This message was never sent: the preview stubs outbound mail and only records it here."}`
		outbound = &recorded{At: time.Now().UTC(), Kind: "outbound", Method: "POST", URL: "smtp://mail.internal/send",
			Detail: map[string]any{"channel": "smtp", "preview": payload, "bytes": len(payload)}}
		status, route = 202, "web.send_welcome"
		body = "<h1>Welcome email queued</h1><p>The preview recorded it instead of sending it.</p>"
	case rest == "boom":
		status, route = 500, "web.boom"
		body = "<h1>Simulated failure</h1>"
	default:
		var hit *mockRoute
		for _, rt := range routesOfSnapshot(snap) {
			rt := rt
			if (rt.method == "" || rt.method == r.Method) && matchPath(rt.path, path) {
				hit = &rt
				break
			}
		}
		if hit == nil {
			status = 404
			body = "<h1>404</h1><p>No route matches <code>" + html.EscapeString(path) + "</code> in this build.</p>"
		} else {
			route, intent = hit.id, hit.intent
			body = fmt.Sprintf("<h1>%s</h1><p><code>%s %s</code> runs intent <code>%s</code>.</p>", html.EscapeString(hit.id), html.EscapeString(hit.method), html.EscapeString(hit.path), html.EscapeString(hit.intent))
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	fmt.Fprintf(w, pageTmpl, "Preview", 600, body, built)

	u := path
	if r.URL.RawQuery != "" {
		u += "?" + r.URL.RawQuery
	}
	rec := recorded{At: time.Now().UTC(), Kind: "request", Method: r.Method, URL: u, Status: status,
		DurationMS: float64(time.Since(start).Microseconds())/1000 + rand.Float64()*8, Detail: map[string]any{"version": built}}
	if route != "" {
		rec.Detail["route"] = route
	}
	if intent != "" {
		rec.Detail["intent"] = intent
	}
	s.mu.Lock()
	if d := s.drafts[id]; d != nil && d.pv != nil {
		d.pv.log = append(d.pv.log, rec)
		if outbound != nil {
			d.pv.log = append(d.pv.log, *outbound)
		}
		if len(d.pv.log) > 200 {
			d.pv.log = d.pv.log[len(d.pv.log)-200:]
		}
	}
	s.mu.Unlock()
}
