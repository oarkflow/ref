package server

import (
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/oarkflow/ref/platform"
	"github.com/oarkflow/ref/studio"
	"github.com/oarkflow/ref/studio/model"
	"github.com/oarkflow/ref/studio/pages"
)

// Assets are the page templates and static files a draft overrides. They sit
// in the draft's snapshot next to its BCL files (a key with a slash is an
// asset), so they share the draft's version, undo history and persistence,
// and one batch of ops can change a route and its template together.

// assetKind classifies an asset path for the pages UI.
func assetKind(p string) string {
	root, rest, _ := strings.Cut(p, "/")
	switch root {
	case "static":
		return "static"
	case "templates":
		switch {
		case strings.HasPrefix(rest, "layouts/"):
			return "layout"
		case strings.HasPrefix(rest, "components/"), strings.HasPrefix(rest, "partials/"):
			return "component"
		case strings.HasSuffix(p, ".html"):
			return "template"
		}
	}
	return "other"
}

// templateName is the name routes and @extends/@include use for a template
// asset path: "templates/pages/todos/list.html" -> "pages/todos/list".
func templateName(assetPath string) (string, bool) {
	rest, ok := strings.CutPrefix(assetPath, "templates/")
	if !ok || !strings.HasSuffix(rest, ".html") {
		return "", false
	}
	return strings.TrimSuffix(rest, ".html"), true
}

// validAssetPath applies the platform's asset rules (root, extension, clean
// path) to a path before it is read from the host's disk, so the resources
// directory's other files (config, keys) are never reachable.
func validAssetPath(p string) bool {
	_, err := platform.NewAssets([]platform.BundleFile{{Path: p}})
	return err == nil
}

// baseFS is the host's resources directory (templates/, static/), or nil.
func (s *Server) baseFS() fs.FS { return s.cfg.Resources }

// templateFS is the templates directory as a draft's pages would see it: the
// draft's overrides over the host's files.
func (s *Server) templateFS(assets platform.Assets) fs.FS {
	sub, err := fs.Sub(assets.Overlay(s.baseFS()), "templates")
	if err != nil {
		return assets.FS()
	}
	return sub
}

// ---------------------------------------------------------------------------
// Asset endpoints
// ---------------------------------------------------------------------------

type assetItem struct {
	Path string `json:"path"`
	Size int    `json:"size"`
	// Kind is template, layout, component, static or other.
	Kind string `json:"kind"`
	// Status is against the draft's base: added, modified, removed or
	// unchanged. A removed asset is listed with size 0.
	Status string `json:"status"`
	// OverridesDisk is true when the host has a file at the same path, so the
	// asset replaces it rather than adding a new file.
	OverridesDisk bool `json:"overridesDisk"`
}

func (s *Server) existsOnDisk(p string) bool {
	if s.baseFS() == nil || !validAssetPath(p) {
		return false
	}
	st, err := fs.Stat(s.baseFS(), p)
	return err == nil && !st.IsDir()
}

func (s *Server) listAssets(c *call) error {
	d, err := s.draft(c)
	if err != nil {
		return err
	}
	v := d.view()
	cur := v.state.assets()
	base := map[string]string{}
	for _, f := range d.baseAssets {
		base[f.Path] = f.Content
	}
	out := []assetItem{}
	seen := map[string]bool{}
	for _, f := range cur {
		seen[f.Path] = true
		status := "unchanged"
		if old, ok := base[f.Path]; !ok {
			status = "added"
		} else if old != f.Content {
			status = "modified"
		}
		out = append(out, assetItem{Path: f.Path, Size: len(f.Content), Kind: assetKind(f.Path), Status: status,
			OverridesDisk: s.existsOnDisk(f.Path)})
	}
	for p := range base {
		if !seen[p] {
			out = append(out, assetItem{Path: p, Kind: assetKind(p), Status: "removed", OverridesDisk: s.existsOnDisk(p)})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return c.json(http.StatusOK, map[string]any{"version": v.version, "assets": out})
}

// assetPathParam reads {path...} and rejects anything that is not a valid
// asset path.
func assetPathParam(c *call) (string, error) {
	p := c.r.PathValue("path")
	if !isAssetPath(p) {
		return "", errf(http.StatusBadRequest, "bad_request", "%q is not an asset path (assets live under templates/ or static/)", p)
	}
	return p, nil
}

func (s *Server) getAsset(c *call) error {
	d, err := s.draft(c)
	if err != nil {
		return err
	}
	p, err := assetPathParam(c)
	if err != nil {
		return err
	}
	v := d.view()
	if e, ok := v.state[p]; ok {
		return c.json(http.StatusOK, map[string]any{"path": p, "content": e.text(), "version": v.version,
			"kind": assetKind(p), "source": "draft"})
	}
	// Not overridden: the host's file, so the editor can show what a new
	// override would start from.
	if s.baseFS() != nil && validAssetPath(p) {
		if b, err := fs.ReadFile(s.baseFS(), p); err == nil {
			return c.json(http.StatusOK, map[string]any{"path": p, "content": string(b), "version": v.version,
				"kind": assetKind(p), "source": "disk"})
		}
	}
	return errf(http.StatusNotFound, "not_found", "no asset %q in the draft or on disk", p)
}

// templateSyntax checks a template's syntax. Only errors count: warnings are
// returned to the client but never block a save.
func templateSyntax(p, content string) (errs, all []pages.Diagnostic) {
	if !strings.HasSuffix(p, ".html") || assetKind(p) == "static" {
		return nil, nil
	}
	for _, d := range pages.Analyze(content).Diagnostics {
		all = append(all, d)
		if d.Severity == "error" {
			errs = append(errs, d)
		}
	}
	return errs, all
}

func (s *Server) putAsset(c *call) error {
	d, err := s.draft(c)
	if err != nil {
		return err
	}
	p, err := assetPathParam(c)
	if err != nil {
		return err
	}
	var in struct {
		Content   string `json:"content"`
		IfVersion *int64 `json:"ifVersion"`
	}
	c.limit = s.cfg.MaxBody
	if err := c.decode(s, &in); err != nil {
		return err
	}
	errs, all := templateSyntax(p, in.Content)
	if len(errs) > 0 && c.r.URL.Query().Get("force") != "1" {
		return errf(http.StatusUnprocessableEntity, "invalid_template", "the template has a syntax error").
			with(map[string]any{"diagnostics": templateDiagnostics(p, errs), "force": "repeat the request with ?force=1 to save it anyway"})
	}
	ch, err := d.applyOps(s.cfg.Now().UTC(), in.IfVersion, []Op{{Op: "putAsset", File: p, Content: in.Content}}, false)
	if err != nil {
		return err
	}
	s.record(c, "draft.put_asset", d.id, p)
	return s.finishAsset(c, d, ch, all)
}

func (s *Server) deleteAsset(c *call) error {
	d, err := s.draft(c)
	if err != nil {
		return err
	}
	p, err := assetPathParam(c)
	if err != nil {
		return err
	}
	ifv, err := ifVersionQuery(c)
	if err != nil {
		return err
	}
	ch, err := d.applyOps(s.cfg.Now().UTC(), ifv, []Op{{Op: "removeAsset", File: p}}, false)
	if err != nil {
		return opFailureOr(err)
	}
	s.record(c, "draft.delete_asset", d.id, p)
	return s.finishAsset(c, d, ch, nil)
}

func (s *Server) renameAsset(c *call) error {
	d, err := s.draft(c)
	if err != nil {
		return err
	}
	var in struct {
		From      string `json:"from"`
		To        string `json:"to"`
		IfVersion *int64 `json:"ifVersion"`
	}
	if err := c.decode(s, &in); err != nil {
		return err
	}
	ch, err := d.applyOps(s.cfg.Now().UTC(), in.IfVersion, []Op{{Op: "renameAsset", File: in.From, NewFile: in.To}}, false)
	if err != nil {
		return opFailureOr(err)
	}
	s.record(c, "draft.rename_asset", d.id, in.From+" -> "+in.To)
	return s.finishAsset(c, d, ch, nil)
}

// importFromDisk copies the host's templates into the draft as overrides, so
// an author can start editing one. A path may be an asset path
// ("templates/pages/todos/list.html") or a template name ("pages/todos/list").
func (s *Server) importFromDisk(c *call) error {
	d, err := s.draft(c)
	if err != nil {
		return err
	}
	if s.baseFS() == nil {
		return errf(http.StatusUnprocessableEntity, "no_resources", "no resources directory is configured, so there is nothing to import")
	}
	var in struct {
		Paths     []string `json:"paths"`
		IfVersion *int64   `json:"ifVersion"`
	}
	if err := c.decode(s, &in); err != nil {
		return err
	}
	if len(in.Paths) == 0 {
		return errf(http.StatusBadRequest, "bad_request", "paths is empty")
	}
	if len(in.Paths) > 200 {
		return errf(http.StatusBadRequest, "bad_request", "at most 200 paths per request")
	}
	have := d.view().state
	var ops []Op
	var imported, skipped []string
	var missing []string
	seen := map[string]bool{}
	for _, raw := range in.Paths {
		p := strings.TrimPrefix(path.Clean(strings.ReplaceAll(strings.TrimSpace(raw), `\`, "/")), "/")
		if !strings.HasPrefix(p, "templates/") && !strings.HasPrefix(p, "static/") {
			if path.Ext(p) == "" {
				p += ".html"
			}
			p = "templates/" + p
		}
		if seen[p] {
			continue
		}
		seen[p] = true
		if !validAssetPath(p) {
			return errf(http.StatusBadRequest, "bad_request", "%q is not an asset path (templates/ or static/, .html .css .js .json .txt .svg)", raw)
		}
		if _, ok := have[p]; ok {
			skipped = append(skipped, p)
			continue
		}
		b, err := fs.ReadFile(s.baseFS(), p)
		if err != nil {
			missing = append(missing, p)
			continue
		}
		ops = append(ops, Op{Op: "putAsset", File: p, Content: string(b)})
		imported = append(imported, p)
	}
	if len(missing) > 0 {
		return errf(http.StatusNotFound, "not_found", "not on disk: %s", strings.Join(missing, ", ")).with(map[string]any{"missing": missing})
	}
	if len(ops) == 0 {
		return c.json(http.StatusOK, map[string]any{"version": d.Version(), "imported": []string{}, "skipped": nonNilStrings(skipped)})
	}
	ch, err := d.applyOps(s.cfg.Now().UTC(), in.IfVersion, ops, false)
	if err != nil {
		return opFailureOr(err)
	}
	s.record(c, "draft.import_assets", d.id, strings.Join(imported, ","))
	return s.finishAsset(c, d, ch, nil, "imported", imported, "skipped", nonNilStrings(skipped))
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func ifVersionQuery(c *call) (*int64, error) {
	q := c.r.URL.Query().Get("ifVersion")
	if q == "" {
		return nil, nil
	}
	n, err := strconv.ParseInt(q, 10, 64)
	if err != nil {
		return nil, errf(http.StatusBadRequest, "bad_request", "ifVersion must be a number")
	}
	return &n, nil
}

// opFailureOr reports a failed op as a 422 that names it, and passes other
// errors (stale, invalid assets) through.
func opFailureOr(err error) error {
	var oe *opError
	if errors.As(err, &oe) {
		return opFailure(err)
	}
	return err
}

// finishAsset is finish for an asset edit: the usual edit result, plus the
// template's own syntax findings and any extra fields.
func (s *Server) finishAsset(c *call, d *Draft, ch change, tmpl []pages.Diagnostic, extra ...any) error {
	diags := s.diagnostics(c.r.Context(), d, ch.version, ch.state)
	changed := ch.changed
	if changed == nil {
		changed = []string{}
	}
	s.persist(d)
	if len(changed) > 0 {
		d.publish("changed", changedEvent{Version: ch.version, Changed: changed})
		d.publish("diagnostics", map[string]any{"version": ch.version, "diagnostics": diags})
	}
	out := map[string]any{"version": ch.version, "applied": ch.applied, "diagnostics": diags, "changed": changed}
	if len(tmpl) > 0 {
		out["template"] = tmpl
	}
	for i := 0; i+1 < len(extra); i += 2 {
		out[extra[i].(string)] = extra[i+1]
	}
	return c.json(http.StatusOK, out)
}

// templateDiagnostics converts a template's syntax findings.
func templateDiagnostics(file string, ds []pages.Diagnostic) []studio.Diagnostic {
	out := make([]studio.Diagnostic, 0, len(ds))
	for _, d := range ds {
		out = append(out, studio.Diagnostic{Severity: d.Severity, Code: "studio.pages.syntax", Message: d.Message,
			File: file, Line: d.Line, Column: d.Column})
	}
	return out
}

// ---------------------------------------------------------------------------
// Route <-> template scan
// ---------------------------------------------------------------------------

// routeRef is a route block and the templates it names.
type routeRef struct {
	File     string // BCL file
	Path     string // model path of the route block, "route/web.todos_list"
	Route    string // route id
	Method   string
	URLPath  string
	Template string // "" when absent or not a plain string
	Layout   string
	Intent   string
	Line     int
	// Lines of the template and layout fields, for diagnostics to point at.
	TemplateLine, LayoutLine int
}

// bclString returns the value of a plain quoted BCL string, or false for
// anything dynamic (env(), an expression, a reference).
func bclString(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if len(raw) >= 2 && raw[0] == '"' {
		if s, err := strconv.Unquote(raw); err == nil {
			return s, true
		}
	}
	return "", false
}

// scanRoutes finds every route block (top level and inside route_group) in
// the draft's editable BCL files.
func scanRoutes(state snapshot) []routeRef {
	var out []routeRef
	for _, name := range state.names() {
		e := state[name]
		if e.file == nil {
			continue
		}
		top, err := e.file.Statements(nil)
		if err != nil {
			continue
		}
		for _, n := range top {
			out = append(out, routesIn(e.file, name, n, "")...)
		}
	}
	return out
}

func routesIn(f *model.File, file string, n model.Node, prefix string) []routeRef {
	if n.Kind != model.KindBlock {
		return nil
	}
	kids, err := f.Statements(n.Path)
	if err != nil {
		return nil
	}
	switch n.Head {
	case "route":
		r := routeRef{File: file, Path: n.Path.String(), Route: n.ID, Line: n.Line}
		for _, k := range kids {
			if k.Kind != model.KindField {
				continue
			}
			v, ok := bclString(k.Value)
			switch k.Head {
			case "template":
				if ok {
					r.Template, r.TemplateLine = v, k.Line
				}
			case "layout":
				if ok {
					r.Layout, r.LayoutLine = v, k.Line
				}
			case "intent":
				if ok {
					r.Intent = v
				}
			case "method":
				r.Method = strings.ToUpper(strings.Trim(k.Value, `" `))
			case "path":
				if ok {
					r.URLPath = prefix + v
				}
			}
		}
		return []routeRef{r}
	case "route_group":
		p := prefix
		for _, k := range kids {
			if k.Kind == model.KindField && k.Head == "prefix" {
				if v, ok := bclString(k.Value); ok {
					p = prefix + v
				}
			}
		}
		var out []routeRef
		for _, k := range kids {
			out = append(out, routesIn(f, file, k, p)...)
		}
		return out
	}
	return nil
}

// intentText maps an intent's name to the source of its block.
func intentText(state snapshot) map[string]string {
	out := map[string]string{}
	for _, name := range state.names() {
		e := state[name]
		if e.file == nil {
			continue
		}
		src := string(e.file.Source())
		top, err := e.file.Statements(nil)
		if err != nil {
			continue
		}
		for _, n := range top {
			if n.Kind == model.KindBlock && n.Head == "intent" && n.End <= len(src) && n.Start <= n.End {
				out[n.ID] = src[n.Start:n.End]
			}
		}
	}
	return out
}

var identRE = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`)

// defaultGlobals are variables the host supplies to every template, so a
// template reading them needs nothing from the route's intent. Extend with
// Config.TemplateGlobals.
var defaultGlobals = []string{"appName", "title", "user", "csrf", "csrfToken", "csrf_token", "flash", "error", "errors",
	"session", "principal", "request", "req", "path", "params", "query", "now", "year", "env", "config", "version",
	"nonce", "locale", "lang", "theme", "description", "meta", "layout", "content", "page", "assets", "loop",
	"appVersion", "currentYear", "environment", "success", "redirect"}

func (s *Server) globals() map[string]bool {
	g := map[string]bool{}
	for _, n := range defaultGlobals {
		g[n] = true
	}
	for _, n := range s.cfg.TemplateGlobals {
		g[n] = true
	}
	return g
}

// pageDiagnostics checks how the draft's routes line up with its templates.
// Everything is a warning except a syntax error in a template the draft
// carries, and the vars check is deliberately shy: a variable counts as
// provided if its name appears anywhere in the route's intent.
func (s *Server) pageDiagnostics(state snapshot) []studio.Diagnostic {
	assets := state.assets()
	var out []studio.Diagnostic
	// Syntax of the templates the draft carries.
	for _, a := range assets {
		if _, ok := templateName(a.Path); !ok {
			continue
		}
		_, all := templateSyntax(a.Path, a.Content)
		out = append(out, templateDiagnostics(a.Path, all)...)
	}
	routes := scanRoutes(state)
	if len(routes) == 0 {
		return out
	}
	tfs := s.templateFS(assets)
	exists := func(name string) bool {
		st, err := fs.Stat(tfs, name+".html")
		return err == nil && !st.IsDir()
	}
	intents := intentText(state)
	globals := s.globals()
	for _, r := range routes {
		if r.Template == "" && r.Layout == "" {
			continue
		}
		at := func(field string, line int) studio.Diagnostic {
			return studio.Diagnostic{Severity: "warning", File: r.File, Line: line, Path: r.Path + "/" + field}
		}
		if r.Layout != "" && !exists(strings.TrimSuffix(r.Layout, ".html")) {
			d := at("layout", r.LayoutLine)
			d.Code, d.Message = "studio.pages.layout_missing", fmt.Sprintf("route %q uses layout %q, which does not exist", r.Route, r.Layout)
			out = append(out, d)
		}
		if r.Template == "" {
			continue
		}
		name := strings.TrimSuffix(r.Template, ".html")
		if !exists(name) {
			d := at("template", r.TemplateLine)
			d.Code, d.Message = "studio.pages.template_missing", fmt.Sprintf("route %q uses template %q, which does not exist", r.Route, r.Template)
			out = append(out, d)
			continue
		}
		res, err := pages.Resolve(tfs, name)
		if err != nil {
			continue
		}
		for _, m := range res.Missing {
			d := at("template", r.TemplateLine)
			d.Code, d.Message = "studio.pages.include_missing", fmt.Sprintf("template %q pulls in %q, which does not exist", r.Template, m)
			out = append(out, d)
		}
		for _, cyc := range res.Cycles {
			d := at("template", r.TemplateLine)
			d.Code, d.Message = "studio.pages.include_cycle", fmt.Sprintf("template %q has an include cycle: %s", r.Template, strings.Join(cyc, " -> "))
			out = append(out, d)
		}
		text, ok := intents[r.Intent]
		if r.Intent == "" || !ok {
			continue
		}
		mentioned := map[string]bool{}
		for _, w := range identRE.FindAllString(text, -1) {
			mentioned[w] = true
		}
		var missing []string
		for _, v := range res.Vars {
			if !globals[v] && !mentioned[v] {
				missing = append(missing, v)
			}
		}
		if len(missing) > 0 {
			d := at("intent", r.Line)
			shown := missing
			if len(shown) > 5 {
				shown = shown[:5]
			}
			d.Code = "studio.pages.vars_unprovided"
			d.Message = fmt.Sprintf("template %q reads %s, which intent %q never mentions (it may not provide them)",
				r.Template, strings.Join(shown, ", "), r.Intent)
			out = append(out, d)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Template catalog
// ---------------------------------------------------------------------------

type templateRoute struct {
	File   string `json:"file"`
	Path   string `json:"path"` // model path of the route block
	Route  string `json:"route"`
	Method string `json:"method,omitempty"`
	URL    string `json:"url,omitempty"`
	// As is "template" or "layout".
	As   string `json:"as"`
	Line int    `json:"line,omitempty"`
}

type templateInfo struct {
	Name string `json:"name"` // "pages/todos/list"
	Path string `json:"path"` // "templates/pages/todos/list.html"
	// Kind is page, layout or component.
	Kind string `json:"kind"`
	// Source is disk (the host's file), draft (added by the draft) or
	// override (the draft replaces a host file).
	Source      string             `json:"source"`
	Extends     string             `json:"extends,omitempty"`
	Layouts     []string           `json:"layouts"`
	Includes    []string           `json:"includes"`
	Vars        []string           `json:"vars"`
	Blocks      []string           `json:"blocks"`
	Unfilled    []string           `json:"unfilled"`
	Unknown     []string           `json:"unknown"`
	Missing     []string           `json:"missing"`
	Diagnostics []pages.Diagnostic `json:"diagnostics"`
	Routes      []templateRoute    `json:"routes"`
	// Unused is true when no route names the template and no other template
	// extends or includes it. The host may still render it from Go code
	// (error pages, emails), so treat it as a hint.
	Unused bool `json:"unused"`
}

type missingRef struct {
	File     string `json:"file"`
	Path     string `json:"path"`
	Route    string `json:"route"`
	Template string `json:"template"`
	As       string `json:"as"`
	Line     int    `json:"line,omitempty"`
}

// templateKind classifies a template name.
func templateKind(name string) string {
	switch {
	case strings.HasPrefix(name, "layouts/"):
		return "layout"
	case strings.HasPrefix(name, "components/"), strings.HasPrefix(name, "partials/"):
		return "component"
	}
	return "page"
}

// listTemplateNames returns every template the draft's pages could name.
func listTemplateNames(tfs fs.FS) []string {
	var names []string
	_ = fs.WalkDir(tfs, ".", func(p string, de fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !de.IsDir() && strings.HasSuffix(p, ".html") {
			names = append(names, strings.TrimSuffix(p, ".html"))
		}
		return nil
	})
	sort.Strings(names)
	return names
}

func (s *Server) templateCatalog(state snapshot) ([]templateInfo, []missingRef) {
	assets := state.assets()
	tfs := s.templateFS(assets)
	routes := scanRoutes(state)
	byTemplate := map[string][]templateRoute{}
	var missing []missingRef
	names := listTemplateNames(tfs)
	known := map[string]bool{}
	for _, n := range names {
		known[n] = true
	}
	for _, r := range routes {
		for _, ref := range []struct {
			name, as string
			line     int
		}{{r.Template, "template", r.TemplateLine}, {r.Layout, "layout", r.LayoutLine}} {
			if ref.name == "" {
				continue
			}
			n := strings.TrimSuffix(ref.name, ".html")
			byTemplate[n] = append(byTemplate[n], templateRoute{File: r.File, Path: r.Path, Route: r.Route, Method: r.Method,
				URL: r.URLPath, As: ref.as, Line: ref.line})
			if !known[n] {
				missing = append(missing, missingRef{File: r.File, Path: r.Path, Route: r.Route, Template: ref.name, As: ref.as, Line: ref.line})
			}
		}
	}
	drafted := map[string]bool{}
	for _, a := range assets {
		drafted[a.Path] = true
	}
	referenced := map[string]bool{}
	infos := make([]templateInfo, 0, len(names))
	for _, n := range names {
		info := templateInfo{Name: n, Path: "templates/" + n + ".html", Kind: templateKind(n),
			Layouts: []string{}, Includes: []string{}, Vars: []string{}, Blocks: []string{}, Unfilled: []string{},
			Unknown: []string{}, Missing: []string{}, Diagnostics: []pages.Diagnostic{}, Routes: []templateRoute{}}
		switch {
		case drafted[info.Path] && s.existsOnDisk(info.Path):
			info.Source = "override"
		case drafted[info.Path]:
			info.Source = "draft"
		default:
			info.Source = "disk"
		}
		if res, err := pages.Resolve(tfs, n); err == nil {
			an := res.Analyses[res.Name]
			info.Extends = strings.TrimSuffix(an.Extends, ".html")
			info.Includes = trimHTML(an.Includes)
			info.Layouts = trimHTML(res.Layouts)
			info.Vars = nonNilStrings(res.Vars)
			info.Blocks = nonNilStrings(res.Blocks)
			info.Unfilled = nonNilStrings(res.Unfilled)
			info.Unknown = nonNilStrings(res.Unknown)
			info.Missing = trimHTML(res.Missing)
			if ds := res.Diagnostics[res.Name]; ds != nil {
				info.Diagnostics = ds
			}
			for _, dep := range an.Includes {
				referenced[strings.TrimSuffix(path.Clean(dep), ".html")] = true
			}
			if an.Extends != "" {
				referenced[strings.TrimSuffix(path.Clean(an.Extends), ".html")] = true
			}
		}
		info.Routes = append(info.Routes, byTemplate[n]...)
		infos = append(infos, info)
	}
	for i := range infos {
		infos[i].Unused = len(infos[i].Routes) == 0 && !referenced[infos[i].Name]
	}
	return infos, missing
}

func trimHTML(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		out = append(out, strings.TrimSuffix(s, ".html"))
	}
	return out
}

func (s *Server) listTemplates(c *call) error {
	d, err := s.draft(c)
	if err != nil {
		return err
	}
	v := d.view()
	infos, missing := s.templateCatalog(v.state)
	if missing == nil {
		missing = []missingRef{}
	}
	g := make([]string, 0)
	for n := range s.globals() {
		g = append(g, n)
	}
	sort.Strings(g)
	return c.json(http.StatusOK, map[string]any{"version": v.version, "templates": infos, "missing": missing, "globals": g})
}

// getTemplate serves GET /drafts/{id}/templates/{name...}: one template's
// catalog entry, or, with a "/preview-data" suffix, sample data for it.
func (s *Server) getTemplate(c *call) error {
	d, err := s.draft(c)
	if err != nil {
		return err
	}
	name := c.r.PathValue("name")
	wantData := false
	if n, ok := strings.CutSuffix(name, "/preview-data"); ok {
		name, wantData = n, true
	}
	name = strings.TrimSuffix(name, ".html")
	v := d.view()
	tfs := s.templateFS(v.state.assets())
	if st, err := fs.Stat(tfs, name+".html"); err != nil || st.IsDir() {
		return errf(http.StatusNotFound, "not_found", "no template %q", name)
	}
	if wantData {
		res, err := pages.Resolve(tfs, name)
		if err != nil {
			return errf(http.StatusNotFound, "not_found", "no template %q", name)
		}
		return c.json(http.StatusOK, map[string]any{"name": name, "version": v.version, "guessed": true,
			"vars": nonNilStrings(res.Vars), "data": sampleData(res.Vars, res.Paths)})
	}
	infos, _ := s.templateCatalog(v.state)
	for _, in := range infos {
		if in.Name == name {
			return c.json(http.StatusOK, in)
		}
	}
	return errf(http.StatusNotFound, "not_found", "no template %q", name)
}

// ---------------------------------------------------------------------------
// Sample data
// ---------------------------------------------------------------------------

// sampleData builds an object a template could render with, from the
// variables and dotted paths it reads. Types are guessed from names, so it is
// a starting point for a sample preview, not a contract.
func sampleData(vars, paths []string) map[string]any {
	root := map[string]any{}
	set := func(p string) {
		parts := strings.Split(p, ".")
		cur := root
		for i, part := range parts {
			if i == len(parts)-1 {
				if _, ok := cur[part]; !ok {
					cur[part] = guessValue(part)
				}
				return
			}
			next, ok := cur[part].(map[string]any)
			if !ok {
				next = map[string]any{}
				cur[part] = next
			}
			cur = next
		}
	}
	for _, v := range vars {
		set(v)
	}
	for _, p := range paths {
		set(p)
	}
	return root
}

var (
	intNames  = map[string]bool{"id": true, "count": true, "total": true, "page": true, "pages": true, "limit": true, "offset": true, "index": true, "number": true, "age": true, "size": true, "length": true, "qty": true, "quantity": true, "position": true, "priority": true, "version": true, "status_code": true}
	numNames  = map[string]bool{"price": true, "amount": true, "balance": true, "rate": true, "score": true, "percent": true, "cost": true}
	strNames  = map[string]string{"name": "Sample name", "title": "Sample title", "label": "Sample label", "description": "A short sample description.", "message": "Sample message", "text": "Sample text", "status": "active", "role": "user", "email": "user@example.com", "url": "/example", "href": "/example", "link": "/example", "phone": "+1 555 0100", "error": "", "csrf": "csrf-token", "csrftoken": "csrf-token", "appname": "Sample App"}
	boolStart = []string{"is", "has", "can", "should", "show", "allow", "enable", "disable", "needs", "will"}
	boolNames = map[string]bool{"enabled": true, "disabled": true, "active": true, "checked": true, "selected": true, "visible": true, "hidden": true, "done": true, "completed": true, "public": true, "admin": true, "authenticated": true, "loggedin": true}
	notPlural = map[string]bool{"status": true, "address": true, "class": true, "access": true, "process": true, "progress": true, "alias": true, "news": true, "series": true, "analysis": true, "canvas": true, "bonus": true, "focus": true, "basis": true, "loss": true, "success": true, "business": true}
)

func guessValue(name string) any {
	lower := strings.ToLower(name)
	if v, ok := strNames[lower]; ok {
		return v
	}
	switch {
	case intNames[lower]:
		return 1
	case numNames[lower]:
		return 9.99
	case boolNames[lower]:
		return true
	}
	for _, p := range boolStart {
		if strings.HasPrefix(name, p) && len(name) > len(p) && name[len(p)] >= 'A' && name[len(p)] <= 'Z' {
			return true
		}
	}
	if lower == "date" || lower == "time" || strings.HasSuffix(lower, "date") || strings.HasSuffix(lower, "time") ||
		strings.HasSuffix(lower, "_at") || (len(name) > 2 && strings.HasSuffix(name, "At")) {
		return "2026-01-15T09:30:00Z"
	}
	if strings.HasSuffix(lower, "s") && !notPlural[lower] && len(lower) > 2 {
		// A collection: two generic items.
		return []any{sampleItem(1), sampleItem(2)}
	}
	return "Sample " + humanize(name)
}

func sampleItem(n int) map[string]any {
	return map[string]any{"id": n, "title": fmt.Sprintf("Sample item %d", n), "name": fmt.Sprintf("Sample item %d", n),
		"status": "active", "createdAt": "2026-01-15T09:30:00Z"}
}

// humanize turns fooBar / foo_bar into "foo bar".
func humanize(s string) string {
	var b strings.Builder
	for i, r := range s {
		switch {
		case r == '_' || r == '-':
			b.WriteByte(' ')
		case r >= 'A' && r <= 'Z':
			if i > 0 {
				b.WriteByte(' ')
			}
			b.WriteRune(r + 32)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
