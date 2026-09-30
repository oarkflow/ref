package server

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"github.com/oarkflow/ref/studio"
)

const previewCookie = "studio_preview"

// previewSession authorises a browser's iframe to load one draft's preview.
// An iframe cannot send an Authorization header, so POST /preview sets a
// cookie scoped to the preview path.
type previewSession struct {
	draft   string
	who     string
	expires time.Time
}

func (s *Server) previewOff() error {
	return errf(http.StatusNotImplemented, "not_implemented", "preview is not enabled")
}

func (s *Server) previewURL(id string) string { return s.cfg.BasePath + "/preview/" + id + "/" }

func (s *Server) previewEnsure(c *call) error {
	if s.cfg.Preview == nil {
		return s.previewOff()
	}
	d, err := s.draft(c)
	if err != nil {
		return err
	}
	st, err := s.cfg.Preview.Ensure(c.r.Context(), d)
	if err != nil {
		return err
	}
	if st.URL == "" || strings.HasPrefix(st.URL, "/") && !strings.HasPrefix(st.URL, s.cfg.BasePath+"/") {
		st.URL = s.previewURL(d.id)
	}
	var tok [16]byte
	_, _ = rand.Read(tok[:])
	value := hex.EncodeToString(tok[:])
	expires := s.cfg.Now().Add(12 * time.Hour)
	s.pmu.Lock()
	for k, ps := range s.psessions {
		if s.cfg.Now().After(ps.expires) {
			delete(s.psessions, k)
		}
	}
	s.psessions[value] = previewSession{draft: d.id, who: c.id.Name, expires: expires}
	s.pmu.Unlock()
	http.SetCookie(c.w, &http.Cookie{Name: previewCookie, Value: value, Path: s.previewURL(d.id), Expires: expires,
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: c.r.TLS != nil})
	s.record(c, "preview.start", d.id, st.Status)
	if !s.forwarding {
		d.publish("preview", st)
	}
	return c.json(http.StatusOK, st)
}

func (s *Server) previewStop(c *call) error {
	if s.cfg.Preview == nil {
		return s.previewOff()
	}
	d, err := s.draft(c)
	if err != nil {
		return err
	}
	s.cfg.Preview.Stop(d.id)
	s.record(c, "preview.stop", d.id, "")
	if !s.forwarding {
		d.publish("preview", studio.PreviewStatus{Status: "stopped"})
	}
	c.w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Server) previewRequests(c *call) error {
	if s.cfg.Preview == nil {
		return s.previewOff()
	}
	d, err := s.draft(c)
	if err != nil {
		return err
	}
	reqs := s.cfg.Preview.Requests(d.id)
	if reqs == nil {
		reqs = []studio.RecordedRequest{}
	}
	return c.json(http.StatusOK, reqs)
}

// previewProxy serves /preview/{id}/... through the preview manager. It
// accepts the preview cookie or a bearer token of an editor.
func (s *Server) previewProxy(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Preview == nil {
		s.writeErr(w, s.previewOff())
		return
	}
	id := r.PathValue("id")
	if !s.previewAllowed(r, id) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="studio"`)
		s.writeErr(w, errf(http.StatusUnauthorized, "unauthorized", "start the preview from Studio first"))
		return
	}
	if s.restoreBase {
		// The mount stripped BasePath; the preview service was told its
		// prefix includes it (so its redirects and links are right).
		r2 := r.Clone(r.Context())
		u := *r.URL
		u.Path = s.cfg.BasePath + u.Path
		if u.RawPath != "" {
			u.RawPath = s.cfg.BasePath + u.RawPath
		}
		r2.URL = &u
		r = r2
	}
	s.cfg.Preview.Handler().ServeHTTP(w, r)
}

func (s *Server) previewAllowed(r *http.Request, draftID string) bool {
	if id, ok := s.authenticate(r, false); ok {
		d, found := s.store.Get(draftID)
		return found && id.Has(studio.RoleEditor) && (d.owner == id.Name || id.Has(studio.RoleAdmin))
	}
	ck, err := r.Cookie(previewCookie)
	if err != nil {
		return false
	}
	s.pmu.Lock()
	defer s.pmu.Unlock()
	ps, ok := s.psessions[ck.Value]
	return ok && ps.draft == draftID && s.cfg.Now().Before(ps.expires)
}
