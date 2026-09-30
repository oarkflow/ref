// Package studio holds the types shared by the Studio packages. types.go has
// Identity, Draft and DraftSource; this file adds the role model, the
// diagnostic and preview wire types and the PreviewManager seam, and
// assets.go embeds the web app. The contract is docs/studio-api.md.
package studio

import (
	"context"
	"net/http"
	"time"

	"github.com/oarkflow/ref/platform"
)

// Role is a Studio permission level. Roles are ordered: each includes the
// rights of the ones before it.
type Role string

const (
	RoleViewer   Role = "viewer"   // read schemas, revisions
	RoleEditor   Role = "editor"   // create and edit drafts, propose, preview
	RoleReviewer Role = "reviewer" // approve, reject, activate
	RoleAdmin    Role = "admin"    // everything, incl. rollback, audit, others' drafts
)

var roleRank = map[Role]int{RoleViewer: 1, RoleEditor: 2, RoleReviewer: 3, RoleAdmin: 4}

// ValidRole reports whether r is one of the four roles.
func ValidRole(r Role) bool { _, ok := roleRank[r]; return ok }

// Has reports whether the identity holds min or a higher role. Unknown role
// names grant nothing.
func (i Identity) Has(min Role) bool {
	need := roleRank[min]
	for _, r := range i.Roles {
		if rank := roleRank[Role(r)]; rank >= need && rank > 0 {
			return true
		}
	}
	return false
}

// Diagnostic is one finding, as sent to the web app.
type Diagnostic struct {
	Severity string `json:"severity"` // "error", "warning" or "info"
	Code     string `json:"code,omitempty"`
	Message  string `json:"message"`
	File     string `json:"file,omitempty"`
	Line     int    `json:"line,omitempty"`
	Column   int    `json:"column,omitempty"`
	Offset   int    `json:"offset,omitempty"`
	// Path names the block or field, e.g. "route/web.todos_list/path".
	Path string `json:"path,omitempty"`
}

// FromPlatform converts validation diagnostics.
func FromPlatform(in []platform.Diagnostic) []Diagnostic {
	out := make([]Diagnostic, 0, len(in))
	for _, d := range in {
		x := Diagnostic{Severity: d.Severity, Code: d.Code, Message: d.Message, Path: d.Path}
		if d.Span != nil {
			x.File, x.Line, x.Column, x.Offset = d.Span.File, d.Span.Line, d.Span.Column, d.Span.Offset
		}
		out = append(out, x)
	}
	return out
}

// PreviewStatus is the state of a draft's preview generation.
type PreviewStatus struct {
	Status  string       `json:"status"` // "starting", "ready" or "failed"
	URL     string       `json:"url,omitempty"`
	Version int64        `json:"version,omitempty"` // draft version the generation was built from
	Error   []Diagnostic `json:"error,omitempty"`
	Message string       `json:"message,omitempty"`
}

// RecordedRequest is one request the preview generation served, or one
// outbound call it stubbed.
type RecordedRequest struct {
	At         time.Time      `json:"at"`
	Kind       string         `json:"kind"` // "request" or "outbound"
	Method     string         `json:"method,omitempty"`
	URL        string         `json:"url,omitempty"`
	Status     int            `json:"status,omitempty"`
	DurationMS float64        `json:"durationMs,omitempty"`
	Detail     map[string]any `json:"detail,omitempty"`
}

// PreviewManager builds and serves preview generations for drafts. The
// studio/preview package implements it; the server depends only on this
// interface.
type PreviewManager interface {
	// Ensure builds (or rebuilds, if the draft changed) the draft's preview
	// generation. It is idempotent.
	Ensure(ctx context.Context, d Draft) (PreviewStatus, error)
	Stop(id string)
	// Handler serves /preview/{id}/... for every draft.
	Handler() http.Handler
	Requests(id string) []RecordedRequest
}
