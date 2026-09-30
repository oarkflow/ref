package platform

import (
	"errors"
	"regexp"
	"strings"

	"github.com/oarkflow/bcl"
)

// SourceSpan locates a diagnostic in the validated source. Line and Column
// are 1-based; Offset is a byte offset into the source.
type SourceSpan struct {
	File   string `json:"file,omitempty"`
	Line   int    `json:"line"`
	Column int    `json:"column"`
	Offset int    `json:"offset"`
}

// Diagnostic is one structured validation finding. Message is the same text
// that appears in ValidationReport.Errors or .Warnings; Path and Span say where
// it applies so an editor can attach it to a block or field.
//
// Path is "<block type>/<block id>" or "<block type>/<block id>/<field>",
// e.g. "route/web.todos_list/path". It is empty when the finding could not be
// tied to a block.
type Diagnostic struct {
	Severity string      `json:"severity"` // "error" or "warning"
	Code     string      `json:"code,omitempty"`
	Message  string      `json:"message"`
	Path     string      `json:"path,omitempty"`
	Span     *SourceSpan `json:"span,omitempty"`
}

const (
	SeverityError   = "error"
	SeverityWarning = "warning"
	// SeverityInfo marks a finding that needs no action here, such as an
	// environment variable this process does not have but the deployment may.
	SeverityInfo = "info"

	DiagEnvUnset = "env.unset"
	DiagParse    = "parse"
	DiagValidate = "validate"
	DiagWarning  = "warning"
)

func spanOf(s bcl.Span) *SourceSpan {
	return &SourceSpan{File: s.File, Line: s.Start.Line, Column: s.Start.Column, Offset: s.Start.Offset}
}

// sourceIndex maps blocks and their assignments in a parsed source to spans.
type sourceIndex struct {
	blocks map[string]*bcl.Block // "type/id"
	types  map[string]bool
}

func indexSource(src []byte) *sourceIndex {
	idx := &sourceIndex{blocks: map[string]*bcl.Block{}, types: map[string]bool{}}
	doc, err := bcl.ParseFile("", src)
	if err != nil || doc == nil {
		return idx
	}
	for _, n := range doc.Items {
		if b, ok := n.(*bcl.Block); ok {
			idx.types[b.Type] = true
			if b.ID != "" {
				idx.blocks[b.Type+"/"+b.ID] = b
			}
		}
	}
	return idx
}

// blockRef finds the first `<type> "<id>"` (or `<type> <id>:`) mention in msg
// that names a block in the source.
var blockRefRE = regexp.MustCompile(`([a-z_]+)[ :]+"([^"]+)"|([a-z_]+) ([A-Za-z0-9_.\-]+):`)

func (idx *sourceIndex) locate(msg string) (path string, span *SourceSpan) {
	for _, m := range blockRefRE.FindAllStringSubmatch(msg, -1) {
		typ, id := m[1], m[2]
		if typ == "" {
			typ, id = m[3], m[4]
		}
		b, ok := idx.blocks[typ+"/"+id]
		if !ok {
			continue
		}
		path, span = typ+"/"+id, spanOf(b.Span)
		// Refine to a field when the message names one of the block's keys.
		for _, n := range b.Body {
			a, ok := n.(*bcl.Assignment)
			if !ok || len(a.Name) < 3 {
				continue
			}
			if containsWord(msg, a.Name) {
				return path + "/" + a.Name, spanOf(a.Span)
			}
		}
		return path, span
	}
	return "", nil
}

func containsWord(s, w string) bool {
	for i := 0; ; {
		j := strings.Index(s[i:], w)
		if j < 0 {
			return false
		}
		start, end := i+j, i+j+len(w)
		okL := start == 0 || !isWordByte(s[start-1])
		okR := end == len(s) || !isWordByte(s[end])
		if okL && okR {
			return true
		}
		i = start + 1
	}
}

func isWordByte(c byte) bool {
	return c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// buildDiagnostics turns the report's string findings into structured ones.
// A parse failure that carries bcl diagnostics keeps their exact spans.
func buildDiagnostics(src []byte, parseErr error, errs, warns []string) []Diagnostic {
	var out []Diagnostic
	if parseErr != nil {
		var list bcl.ErrorList
		if errors.As(parseErr, &list) && len(list) > 0 {
			for _, d := range list {
				sev := d.Severity
				if sev == "" {
					sev = SeverityError
				}
				dd := Diagnostic{Severity: sev, Code: d.Code, Message: d.Message}
				if dd.Code == "" {
					dd.Code = DiagParse
				}
				if d.Span.Start.Line > 0 {
					dd.Span = spanOf(d.Span)
				}
				out = append(out, dd)
			}
			return out
		}
	}
	idx := indexSource(src)
	add := func(sev, code, msg string) {
		d := Diagnostic{Severity: sev, Code: code, Message: msg}
		d.Path, d.Span = idx.locate(msg)
		out = append(out, d)
	}
	for _, e := range errs {
		code := DiagValidate
		if parseErr != nil && strings.HasPrefix(e, "parse:") {
			code = DiagParse
		}
		add(SeverityError, code, e)
	}
	for _, w := range warns {
		if strings.HasSuffix(w, "is not set here") {
			// Validation substitutes a placeholder; the deployment that
			// activates the revision supplies the real value. Not a problem.
			add(SeverityInfo, DiagEnvUnset, w)
			continue
		}
		add(SeverityWarning, DiagWarning, w)
	}
	return out
}
