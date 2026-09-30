package server

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

func marshalEvent(v any) ([]byte, error) { return json.Marshal(v) }

// serveWeb serves the web app: files as they are, index.html for any other
// path without an extension (client-side routes).
func (s *Server) serveWeb(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		s.writeErr(w, errf(http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed"))
		return
	}
	name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if name == "" {
		name = "index.html"
	}
	f, err := s.web.Open(name)
	if err == nil {
		if st, serr := f.Stat(); serr != nil || st.IsDir() {
			f.Close()
			f, err = nil, fs.ErrNotExist
		}
	}
	if err != nil {
		if path.Ext(name) != "" {
			http.NotFound(w, r)
			return
		}
		name = "index.html"
		if f, err = s.web.Open(name); err != nil {
			http.NotFound(w, r)
			return
		}
	}
	defer f.Close()
	if name == "index.html" {
		w.Header().Set("Cache-Control", "no-store")
	} else if strings.HasPrefix(name, "assets/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if rs, ok := f.(interface {
		Read([]byte) (int, error)
		Seek(int64, int) (int64, error)
	}); ok {
		http.ServeContent(w, r, name, time.Time{}, rs)
		return
	}
	// fs.File without Seek (embed.FS files do support it; MapFS may not).
	data, err := fs.ReadFile(s.web, name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	http.ServeContent(w, r, name, time.Time{}, strings.NewReader(string(data)))
}
