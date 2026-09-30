package server

import (
	"crypto/sha1"
	"encoding/hex"
	"net/http"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/oarkflow/ref/studio/model"
	"github.com/oarkflow/ref/studio/pages"
)

// Journeys: a graph of how the app behaves for a visitor. Pages hold the
// links, forms, buttons and script requests found in their templates; those
// reach routes; routes render pages and run intents; intents use resources;
// and a request that carries "?redirect=/x" sends the visitor to another
// page. The graph is derived from the draft's BCL and templates on demand
// (and cached per draft version), so it is always what the draft says.

// flowNode is one box in the journey graph.
type flowNode struct {
	ID string `json:"id"`
	// Kind is page, element, route, intent, resource, external or unresolved.
	Kind string `json:"kind"`
	// Subkind refines it: link|form|button|fetch for elements, "process" for
	// a process, the resource category (database, email, ...) for resources.
	Subkind string `json:"subkind,omitempty"`
	Label   string `json:"label"`
	// Group names the container a node visually belongs to: the page an
	// element sits on, or the shared component ("components/navbar") whose
	// elements appear on many pages.
	Group  string `json:"group,omitempty"`
	Shared bool   `json:"shared,omitempty"`
	// File and Line locate the node's source: a BCL file (with Path, the
	// model path of the block) or a template/script asset path.
	File string         `json:"file,omitempty"`
	Line int            `json:"line,omitempty"`
	Path string         `json:"path,omitempty"`
	Data map[string]any `json:"data,omitempty"`
}

// flowEdge connects two nodes.
type flowEdge struct {
	ID string `json:"id"`
	// Kind is contains, navigates, calls, runs, renders, redirects or uses.
	Kind  string `json:"kind"`
	From  string `json:"from"`
	To    string `json:"to"`
	Label string `json:"label,omitempty"`
	// Shared marks a contains edge from a page to an element it inherits
	// from a layout or component.
	Shared bool           `json:"shared,omitempty"`
	Data   map[string]any `json:"data,omitempty"`
}

type flowWarning struct {
	Code     string `json:"code"`
	Severity string `json:"severity"` // warning | info
	Message  string `json:"message"`
	Node     string `json:"node,omitempty"`
	File     string `json:"file,omitempty"`
	Line     int    `json:"line,omitempty"`
}

type flowStats struct {
	Nodes    map[string]int `json:"nodes"`
	Edges    map[string]int `json:"edges"`
	Pages    int            `json:"pages"`
	Routes   int            `json:"routes"`
	Elements int            `json:"elements"`
	// SharedElements are elements that come from layouts and components.
	SharedElements int `json:"sharedElements"`
	Unresolved     int `json:"unresolved"`
	UnusedPages    int `json:"unusedPages"`
}

type flowGraph struct {
	Version  int64         `json:"version"`
	Focus    string        `json:"focus,omitempty"`
	Depth    int           `json:"depth,omitempty"`
	Nodes    []flowNode    `json:"nodes"`
	Edges    []flowEdge    `json:"edges"`
	Stats    flowStats     `json:"stats"`
	Warnings []flowWarning `json:"warnings"`
}

// flowCache keeps the last full graph per draft, valid for one draft version.
type flowCache struct {
	mu sync.Mutex
	m  map[string]*flowGraph
}

func (fc *flowCache) get(id string, version int64) *flowGraph {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	if g := fc.m[id]; g != nil && g.Version == version {
		return g
	}
	return nil
}

func (fc *flowCache) put(id string, g *flowGraph) {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	if fc.m == nil || len(fc.m) > 256 {
		fc.m = map[string]*flowGraph{}
	}
	fc.m[id] = g
}

func (fc *flowCache) drop(id string) {
	fc.mu.Lock()
	delete(fc.m, id)
	fc.mu.Unlock()
}

// ---------------------------------------------------------------------------
// BCL scan
// ---------------------------------------------------------------------------

// flowRoute is a route with the settings it inherits from its route_group.
type flowRoute struct {
	ID, Method, Path      string
	Intent, Process       string
	Template, Layout      string
	Auth, Session, Group  string
	Redirect              string
	AllowAnon, RateLimit  bool
	Roles                 []string
	File, MPath           string
	Line                  int
	segs                  []string
	order                 int
	TemplateSet, HasPath  bool
	dynamicPath, dynamicT bool
}

type flowIntent struct {
	ID, Subkind string
	Steps       int
	Kinds       map[string]int
	Resources   []string
	Children    []string
	File, MPath string
	Line        int
	Description string
}

type flowResource struct {
	ID, Kind    string
	File, MPath string
	Line        int
}

type bclScan struct {
	routes    []*flowRoute
	intents   map[string]*flowIntent
	resources map[string]*flowResource
	statics   []string
	entities  int
	warnings  []flowWarning
}

var quotedRE = regexp.MustCompile(`"([^"]*)"`)

type routeCtx struct {
	prefix, auth, session, group string
	roles                        []string
	rateLimit, allowAnon         bool
}

func (s *Server) scanBCL(state snapshot) *bclScan {
	out := &bclScan{intents: map[string]*flowIntent{}, resources: map[string]*flowResource{}}
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
			if n.Kind != model.KindBlock {
				continue
			}
			switch n.Head {
			case "route", "route_group":
				out.scanRoute(e.file, name, n, routeCtx{})
			case "intent":
				out.scanIntent(e.file, name, n, "intent")
			case "process":
				out.scanIntent(e.file, name, n, "process")
			case "resource":
				r := &flowResource{ID: n.ID, File: name, MPath: n.Path.String(), Line: n.Line}
				if kids, err := e.file.Statements(n.Path); err == nil {
					for _, k := range kids {
						if k.Kind == model.KindField && k.Head == "kind" {
							r.Kind, _ = bclString(k.Value)
						}
					}
				}
				out.resources[n.ID] = r
			case "static":
				if kids, err := e.file.Statements(n.Path); err == nil {
					for _, k := range kids {
						if k.Kind == model.KindField && k.Head == "prefix" {
							if v, ok := bclString(k.Value); ok && v != "" {
								out.statics = append(out.statics, strings.TrimRight(v, "/")+"/")
							}
						}
					}
				}
			case "entity":
				out.entities++
			}
		}
	}
	for i, r := range out.routes {
		r.order = i
	}
	return out
}

func (o *bclScan) scanRoute(f *model.File, file string, n model.Node, ctx routeCtx) {
	kids, err := f.Statements(n.Path)
	if err != nil {
		return
	}
	if n.Head == "route_group" {
		c := ctx
		c.group = n.ID
		for _, k := range kids {
			switch {
			case k.Kind == model.KindField:
				v, ok := bclString(k.Value)
				switch k.Head {
				case "prefix":
					if ok {
						c.prefix = ctx.prefix + v
					}
				case "auth":
					if ok {
						c.auth = v
					}
				case "session":
					if ok {
						c.session = v
					}
				case "allow_anonymous":
					c.allowAnon = strings.TrimSpace(k.Value) == "true"
				}
			case k.Head == "authz":
				c.roles = rolesOf(f, k)
			case k.Head == "rate_limit":
				c.rateLimit = true
			}
		}
		for _, k := range kids {
			if k.Kind == model.KindBlock && (k.Head == "route" || k.Head == "route_group") {
				o.scanRoute(f, file, k, c)
			}
		}
		return
	}
	r := &flowRoute{ID: n.ID, Method: "GET", File: file, MPath: n.Path.String(), Line: n.Line,
		Auth: ctx.auth, Session: ctx.session, Group: ctx.group, Roles: ctx.roles, RateLimit: ctx.rateLimit, AllowAnon: ctx.allowAnon}
	pathSet := false
	rel := ""
	for _, k := range kids {
		switch {
		case k.Kind == model.KindField:
			v, ok := bclString(k.Value)
			switch k.Head {
			case "method":
				r.Method = strings.ToUpper(strings.Trim(strings.TrimSpace(k.Value), `"`))
			case "path":
				if ok {
					rel, pathSet = v, true
				} else {
					r.dynamicPath = true
				}
			case "intent":
				if ok {
					r.Intent = v
				}
			case "process":
				if ok {
					r.Process = v
				}
			case "template":
				if ok {
					r.Template, r.TemplateSet = strings.TrimSuffix(v, ".html"), true
				} else {
					r.dynamicT = true
				}
			case "layout":
				if ok {
					r.Layout = strings.TrimSuffix(v, ".html")
				}
			case "auth":
				if ok {
					r.Auth = v
				}
			case "session":
				if ok {
					r.Session = v
				}
			case "allow_anonymous":
				r.AllowAnon = strings.TrimSpace(k.Value) == "true"
			case "redirect":
				if ok {
					r.Redirect = v
				}
			}
		case k.Head == "authz":
			r.Roles = rolesOf(f, k)
		case k.Head == "rate_limit":
			r.RateLimit = true
		}
	}
	if r.dynamicPath {
		o.warnings = append(o.warnings, flowWarning{Code: "flows.dynamic_route", Severity: "info",
			Message: "route " + n.ID + " has a computed path and is not drawn", File: file, Line: n.Line})
		return
	}
	full := ctx.prefix + rel
	if !pathSet && ctx.prefix == "" {
		full = "/"
	}
	if full == "" {
		full = "/"
	}
	r.Path, r.HasPath = full, true
	r.segs = routeSegs(full)
	o.routes = append(o.routes, r)
}

func rolesOf(f *model.File, authz model.Node) []string {
	kids, err := f.Statements(authz.Path)
	if err != nil {
		return nil
	}
	for _, k := range kids {
		if k.Kind == model.KindField && k.Head == "roles" {
			var out []string
			for _, m := range quotedRE.FindAllStringSubmatch(k.Value, -1) {
				out = append(out, m[1])
			}
			return out
		}
	}
	return nil
}

func routeSegs(p string) []string {
	var out []string
	for _, s := range strings.Split(p, "/") {
		if s == "" {
			continue
		}
		if strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}") {
			s = ":" + strings.Trim(s, "{}")
		}
		out = append(out, s)
	}
	return out
}

func (o *bclScan) scanIntent(f *model.File, file string, n model.Node, sub string) {
	it := &flowIntent{ID: n.ID, Subkind: sub, Kinds: map[string]int{}, File: file, MPath: n.Path.String(), Line: n.Line}
	kids, err := f.Statements(n.Path)
	if err != nil {
		return
	}
	res := map[string]bool{}
	child := map[string]bool{}
	for _, k := range kids {
		if k.Kind == model.KindField && k.Head == "description" {
			it.Description, _ = bclString(k.Value)
		}
		if k.Kind != model.KindBlock {
			continue
		}
		if sub == "intent" && k.Head == "node" {
			it.Steps++
			uses := ""
			if sk, err := f.Statements(k.Path); err == nil {
				for _, x := range sk {
					if x.Kind == model.KindField && x.Head == "uses" {
						uses, _ = bclString(x.Value)
					}
				}
			}
			fam := "custom"
			if uses != "" {
				fam, _, _ = strings.Cut(uses, ".")
			}
			it.Kinds[fam]++
		} else if sub == "process" && (k.Head == "node" || k.Head == "edge") {
			it.Kinds[k.Head]++
			if k.Head == "node" {
				it.Steps++
			}
		}
		walkStatements(f, k, 0, func(x model.Node) {
			if x.Kind != model.KindField {
				return
			}
			if v, ok := bclString(x.Value); ok {
				switch x.Head {
				case "resource":
					res[v] = true
				case "intent":
					child[v] = true
				}
			}
		})
	}
	it.Resources = sortedSet(res)
	it.Children = sortedSet(child)
	key := n.ID
	if sub == "process" {
		key = "process:" + n.ID
	}
	o.intents[key] = it
}

func walkStatements(f *model.File, n model.Node, depth int, fn func(model.Node)) {
	fn(n)
	if n.Kind != model.KindBlock || depth > 5 {
		return
	}
	kids, err := f.Statements(n.Path)
	if err != nil {
		return
	}
	for _, k := range kids {
		walkStatements(f, k, depth+1, fn)
	}
}

func sortedSet(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ---------------------------------------------------------------------------
// Route matching
// ---------------------------------------------------------------------------

// matchRoute finds the route a request to urlPath with method would reach.
// Segments match literally; a ":name" segment on either side matches
// anything (a literal route segment matches a request parameter only weakly,
// so /todos/:param prefers /todos/:id over /todos/new); a trailing "*"
// matches the rest. The most specific route wins, then the first declared.
func matchRoute(routes []*flowRoute, method, urlPath string) *flowRoute {
	req := routeSegs(urlPath)
	var best *flowRoute
	bestScore := -1
	for _, r := range routes {
		if !strings.EqualFold(r.Method, method) {
			continue
		}
		score, ok := 0, true
		wild := len(r.segs) > 0 && (r.segs[len(r.segs)-1] == "*" || strings.HasSuffix(r.segs[len(r.segs)-1], "*"))
		if wild {
			if len(req) < len(r.segs)-1 {
				continue
			}
		} else if len(req) != len(r.segs) {
			continue
		}
		for i, rs := range r.segs {
			if wild && i == len(r.segs)-1 {
				break
			}
			qs := req[i]
			switch {
			case rs == qs && !strings.HasPrefix(rs, ":"):
				score += 4
			case strings.HasPrefix(rs, ":") && strings.HasPrefix(qs, ":"):
				score += 3
			case strings.HasPrefix(rs, ":"):
				score += 2
			case strings.HasPrefix(qs, ":"):
				score++
			default:
				ok = false
			}
			if !ok {
				break
			}
		}
		if ok && score > bestScore {
			best, bestScore = r, score
		}
	}
	return best
}

// ---------------------------------------------------------------------------
// Graph
// ---------------------------------------------------------------------------

func shortHash(parts ...string) string {
	h := sha1.Sum([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(h[:])[:8]
}

type graphBuilder struct {
	nodes    map[string]*flowNode
	edges    map[string]*flowEdge
	warnings []flowWarning
}

func (b *graphBuilder) node(n flowNode) *flowNode {
	if old := b.nodes[n.ID]; old != nil {
		return old
	}
	b.nodes[n.ID] = &n
	return &n
}

func (b *graphBuilder) edge(kind, from, to, label string, data map[string]any) *flowEdge {
	id := kind + ":" + from + "->" + to
	if e := b.edges[id]; e != nil {
		if label != "" && e.Label != label && !strings.Contains(e.Label, label) {
			if e.Label == "" {
				e.Label = label
			}
		}
		return e
	}
	e := &flowEdge{ID: id, Kind: kind, From: from, To: to, Label: label, Data: data}
	b.edges[id] = e
	return e
}

func (b *graphBuilder) warn(code, sev, msg, node, file string, line int) {
	b.warnings = append(b.warnings, flowWarning{Code: code, Severity: sev, Message: msg, Node: node, File: file, Line: line})
}

func resourceCategory(kind string) string {
	fam, rest, _ := strings.Cut(kind, ".")
	switch {
	case fam == "service" && strings.Contains(rest, "smtp"):
		return "email"
	case fam == "service" && strings.Contains(rest, "http"):
		return "http"
	case fam == "service":
		return "service"
	case fam == "database", fam == "store":
		return "database"
	case fam == "storage":
		return "files"
	case fam == "":
		return "resource"
	}
	return fam
}

func assetPathOf(source string) string {
	if strings.HasPrefix(source, "static/") {
		return source
	}
	return "templates/" + source
}

func (s *Server) buildFlows(state snapshot, version int64) *flowGraph {
	scan := s.scanBCL(state)
	b := &graphBuilder{nodes: map[string]*flowNode{}, edges: map[string]*flowEdge{}}
	b.warnings = append(b.warnings, scan.warnings...)

	assets := state.assets()
	tfs := s.templateFS(assets)
	rfs := assets.Overlay(s.baseFS())
	names := listTemplateNames(tfs)

	// --- pages ---------------------------------------------------------
	routesByTemplate := map[string][]*flowRoute{}
	for _, r := range scan.routes {
		if r.Template != "" {
			routesByTemplate[r.Template] = append(routesByTemplate[r.Template], r)
		}
	}
	pageSet := map[string]bool{}
	for _, n := range names {
		if templateKind(n) == "page" {
			pageSet[n] = true
		}
	}

	// --- routes --------------------------------------------------------
	for _, r := range scan.routes {
		public := r.Auth == "" || r.AllowAnon
		data := map[string]any{"name": r.ID, "method": r.Method, "path": r.Path, "public": public, "protected": !public,
			"rateLimited": r.RateLimit}
		if r.Auth != "" {
			data["auth"] = r.Auth
		}
		if r.Session != "" {
			data["session"] = r.Session
		}
		if len(r.Roles) > 0 {
			data["roles"] = r.Roles
		}
		if r.Intent != "" {
			data["intent"] = r.Intent
		}
		if r.Process != "" {
			data["process"] = r.Process
		}
		if r.Template != "" {
			data["template"] = r.Template
		}
		if r.Layout != "" {
			data["layout"] = r.Layout
		}
		if r.Group != "" {
			data["group"] = r.Group
		}
		id := "route:" + r.ID
		if b.nodes[id] != nil {
			id += "#" + strconv.Itoa(r.order)
		}
		r.ID = strings.TrimPrefix(id, "route:") // keep node id and route id in step
		b.node(flowNode{ID: id, Kind: "route", Subkind: strings.ToLower(r.Method), Label: r.Method + " " + r.Path, Group: r.Group,
			File: r.File, Line: r.Line, Path: r.MPath, Data: data})
	}

	// --- intents, processes, resources ------------------------------------
	usedRes := map[string]bool{}
	for key, it := range scan.intents {
		id := "intent:" + it.ID
		if it.Subkind == "process" {
			id = "process:" + it.ID
		}
		kinds := map[string]int{}
		for k, v := range it.Kinds {
			kinds[k] = v
		}
		data := map[string]any{"name": it.ID, "steps": it.Steps, "stepKinds": kinds, "resources": nonNilStrings(it.Resources)}
		if it.Description != "" {
			data["description"] = it.Description
		}
		b.node(flowNode{ID: id, Kind: "intent", Subkind: it.Subkind, Label: it.ID, File: it.File, Line: it.Line, Path: it.MPath, Data: data})
		_ = key
		for _, rn := range it.Resources {
			usedRes[rn] = true
		}
	}
	for rn := range usedRes {
		if res := scan.resources[rn]; res != nil {
			cat := resourceCategory(res.Kind)
			b.node(flowNode{ID: "resource:" + rn, Kind: "resource", Subkind: cat, Label: rn, File: res.File, Line: res.Line, Path: res.MPath,
				Data: map[string]any{"name": rn, "kind": res.Kind, "category": cat}})
		}
	}
	for key, it := range scan.intents {
		from := "intent:" + it.ID
		if it.Subkind == "process" {
			from = "process:" + it.ID
		}
		_ = key
		for _, rn := range it.Resources {
			if res := scan.resources[rn]; res != nil {
				b.edge("uses", from, "resource:"+rn, resourceCategory(res.Kind), nil)
			} else {
				b.warn("flows.resource_missing", "warning", it.ID+" uses resource "+rn+", which is not declared", from, it.File, it.Line)
			}
		}
		for _, ch := range it.Children {
			if ch == it.ID {
				continue
			}
			if scan.intents[ch] != nil {
				b.edge("runs", from, "intent:"+ch, "then runs", nil)
			}
		}
	}

	// --- route -> page / intent / redirect ----------------------------------
	for _, r := range scan.routes {
		rid := "route:" + r.ID
		if r.Intent != "" {
			if scan.intents[r.Intent] != nil {
				b.edge("runs", rid, "intent:"+r.Intent, "runs", nil)
			} else {
				b.warn("flows.intent_missing", "warning", "route "+r.ID+" runs intent "+r.Intent+", which is not declared", rid, r.File, r.Line)
			}
		}
		if r.Process != "" {
			if scan.intents["process:"+r.Process] != nil {
				b.edge("runs", rid, "process:"+r.Process, "starts", nil)
			} else {
				b.warn("flows.process_missing", "warning", "route "+r.ID+" starts process "+r.Process+", which is not declared", rid, r.File, r.Line)
			}
		}
		if r.Template != "" {
			if pageSet[r.Template] {
				b.edge("renders", rid, "page:"+r.Template, "shows", nil)
			} else {
				b.warn("flows.template_missing", "warning", "route "+r.ID+" renders template "+r.Template+", which does not exist", rid, r.File, r.Line)
			}
		}
	}

	// A route's landing target: the page it renders, else the route itself.
	landing := func(r *flowRoute) string {
		if r.Template != "" && pageSet[r.Template] {
			return "page:" + r.Template
		}
		return "route:" + r.ID
	}
	for _, r := range scan.routes {
		if r.Redirect == "" {
			continue
		}
		info := pages.NormalizeURL(r.Redirect)
		if t := matchRoute(scan.routes, "GET", info.Path); t != nil {
			b.edge("redirects", "route:"+r.ID, landing(t), "on success", map[string]any{"source": "route"})
		}
	}

	// --- pages and their elements -------------------------------------------
	isStatic := func(p string) bool {
		if pages.IsAssetURL(p) {
			return true
		}
		for _, pre := range scan.statics {
			if strings.HasPrefix(p, pre) {
				return true
			}
		}
		return false
	}
	pageNames := make([]string, 0, len(pageSet))
	for n := range pageSet {
		pageNames = append(pageNames, n)
	}
	sort.Strings(pageNames)

	seenElem := map[string]bool{}
	pageRoute := func(page string) *flowRoute {
		var first *flowRoute
		for _, r := range routesByTemplate[page] {
			if r.Method == "GET" {
				return r
			}
			if first == nil {
				first = r
			}
		}
		return first
	}

	for _, pn := range pageNames {
		pe, err := pages.ElementsFor(tfs, rfs, pn)
		pid := "page:" + pn
		rs := routesByTemplate[pn]
		routeIDs := make([]string, 0, len(rs))
		protected := false
		for _, r := range rs {
			routeIDs = append(routeIDs, r.ID)
			if r.Auth != "" && !r.AllowAnon {
				protected = true
			}
		}
		label := humanize(path.Base(pn))
		heading := ""
		if err == nil && pe.Heading != "" {
			heading = pe.Heading
			label = pe.Heading
		} else if label != "" {
			label = strings.ToUpper(label[:1]) + label[1:]
		}
		pdata := map[string]any{"template": pn, "routes": routeIDs, "protected": protected, "public": !protected && len(rs) > 0,
			"unused": len(rs) == 0}
		if heading != "" {
			pdata["heading"] = heading
		}
		b.node(flowNode{ID: pid, Kind: "page", Label: label, Group: pid, File: "templates/" + pn + ".html", Line: 1,
			Path: "templates/" + pn + ".html", Data: pdata})
		if err != nil {
			b.warn("flows.page_unreadable", "warning", "template "+pn+" could not be read", pid, "templates/"+pn+".html", 1)
			continue
		}
		for _, m := range pe.Missing {
			b.warn("flows.script_missing", "info", "script "+m+" loaded by "+pn+" was not found", pid, "templates/"+pn+".html", 1)
		}
		pageOrd := map[string]int{}
		for _, e := range pe.Elements {
			shared := e.Shared
			src := e.Source
			key := shortHash(e.Kind, e.Method, e.URL, e.Label, e.Redirect, e.RawURL, strings.Join(e.Fields, ","))
			// An element of the page itself is unique to it (a repeat gets
			// a numeric suffix); one from a layout or component is a single
			// node shared by every page that includes that file.
			var eid, group string
			if shared {
				eid = "element:shared:" + src + "#" + key
				group = "shared:" + strings.TrimSuffix(src, ".html")
			} else {
				eid = "element:" + pid + "#" + key
				if n := pageOrd[eid]; n > 0 {
					eid += "." + strconv.Itoa(n)
				}
				pageOrd["element:"+pid+"#"+key]++
				group = pid
			}
			first := !seenElem[eid]
			seenElem[eid] = true
			if first {
				edata := map[string]any{"method": e.Method, "url": e.URL, "source": src}
				if e.RawURL != "" {
					edata["rawUrl"] = e.RawURL
				}
				if e.Redirect != "" {
					edata["redirect"] = e.Redirect
				}
				if len(e.Fields) > 0 {
					edata["fields"] = e.Fields
				}
				if len(e.Hints) > 0 {
					edata["hints"] = e.Hints
				}
				if e.External {
					edata["external"] = true
				}
				if e.Dynamic {
					edata["dynamic"] = true
				}
				if e.Via != "" {
					edata["via"] = e.Via
				}
				b.node(flowNode{ID: eid, Kind: "element", Subkind: e.Kind, Label: e.Label, Group: group, Shared: shared,
					File: assetPathOf(src), Line: e.Line, Path: assetPathOf(src), Data: edata})
			}
			ce := b.edge("contains", pid, eid, "", nil)
			ce.Shared = shared
			if !first {
				continue
			}

			// --- where the element leads -------------------------------------
			url := e.URL
			if e.Self {
				if pr := pageRoute(pn); pr != nil {
					url = normalizeRoutePath(pr.Path)
				}
			}
			switch {
			case url == "" || e.Dynamic:
				if e.Dynamic {
					b.warn("flows.dynamic_target", "info", "\""+e.Label+"\" on "+src+" goes to a computed URL and is not followed", eid, assetPathOf(src), e.Line)
				}
				continue
			case e.External:
				xid := "external:" + shortHash(url)
				b.node(flowNode{ID: xid, Kind: "external", Label: url, Data: map[string]any{"url": url}})
				kind := "calls"
				if e.Kind == "link" {
					kind = "navigates"
				}
				b.edge(kind, eid, xid, "external", nil)
				continue
			case isStatic(url):
				continue
			case !strings.HasPrefix(url, "/"):
				b.warn("flows.relative_target", "info", "\""+e.Label+"\" on "+src+" uses a relative URL ("+e.RawURL+") and is not followed", eid, assetPathOf(src), e.Line)
				continue
			}
			method := e.Method
			if method == "" {
				method = "GET"
			}
			target := matchRoute(scan.routes, method, url)
			if target == nil {
				uid := "unresolved:" + shortHash(method, url)
				b.node(flowNode{ID: uid, Kind: "unresolved", Label: method + " " + url,
					Data: map[string]any{"method": method, "url": url}})
				kind := "calls"
				if e.Kind == "link" {
					kind = "navigates"
				}
				b.edge(kind, eid, uid, "no matching route", nil)
				b.warn("flows.unresolved", "warning", "\""+e.Label+"\" on "+src+" calls "+method+" "+url+", which no route serves", eid, assetPathOf(src), e.Line)
				continue
			}
			rid := "route:" + target.ID
			if e.Kind == "link" || (method == "GET" && target.Template != "") {
				b.edge("navigates", eid, rid, "opens", nil)
			} else {
				b.edge("calls", eid, rid, method, nil)
			}
			if e.Redirect != "" && !strings.HasPrefix(e.Redirect, "http") {
				if rt := matchRoute(scan.routes, "GET", e.Redirect); rt != nil {
					re := b.edge("redirects", rid, landing(rt), "on success", map[string]any{"source": "query"})
					via, _ := re.Data["via"].([]string)
					re.Data["via"] = append(via, eid)
				} else if !isStatic(e.Redirect) {
					b.warn("flows.redirect_unresolved", "warning", "\""+e.Label+"\" on "+src+" redirects to "+e.Redirect+", which no route serves", eid, assetPathOf(src), e.Line)
				}
			}
		}
	}

	if scan.entities > 0 {
		b.warn("flows.entities", "info", strconv.Itoa(scan.entities)+" entity block(s) generate REST routes that are not drawn here", "", "", 0)
	}

	return b.finish(version)
}

// normalizeRoutePath turns a route path into the form elements use
// ("/todos/:id" -> "/todos/:id" stays; "{id}" -> ":id").
func normalizeRoutePath(p string) string {
	return "/" + strings.Join(routeSegs(p), "/")
}

var kindOrder = map[string]int{"page": 0, "element": 1, "route": 2, "intent": 3, "resource": 4, "external": 5, "unresolved": 6}

func (b *graphBuilder) finish(version int64) *flowGraph {
	g := &flowGraph{Version: version, Nodes: make([]flowNode, 0, len(b.nodes)), Edges: make([]flowEdge, 0, len(b.edges)),
		Warnings: b.warnings}
	if g.Warnings == nil {
		g.Warnings = []flowWarning{}
	}
	for _, n := range b.nodes {
		g.Nodes = append(g.Nodes, *n)
	}
	sort.Slice(g.Nodes, func(i, j int) bool {
		a, c := g.Nodes[i], g.Nodes[j]
		if kindOrder[a.Kind] != kindOrder[c.Kind] {
			return kindOrder[a.Kind] < kindOrder[c.Kind]
		}
		return a.ID < c.ID
	})
	for _, e := range b.edges {
		if b.nodes[e.From] != nil && b.nodes[e.To] != nil {
			g.Edges = append(g.Edges, *e)
		}
	}
	sort.Slice(g.Edges, func(i, j int) bool { return g.Edges[i].ID < g.Edges[j].ID })
	sort.SliceStable(g.Warnings, func(i, j int) bool {
		if g.Warnings[i].Severity != g.Warnings[j].Severity {
			return g.Warnings[i].Severity == "warning"
		}
		return g.Warnings[i].Code < g.Warnings[j].Code
	})
	g.computeStats()
	return g
}

func (g *flowGraph) computeStats() {
	st := flowStats{Nodes: map[string]int{}, Edges: map[string]int{}}
	for _, n := range g.Nodes {
		st.Nodes[n.Kind]++
		switch n.Kind {
		case "page":
			st.Pages++
			if u, _ := n.Data["unused"].(bool); u {
				st.UnusedPages++
			}
		case "route":
			st.Routes++
		case "element":
			st.Elements++
			if n.Shared {
				st.SharedElements++
			}
		case "unresolved":
			st.Unresolved++
		}
	}
	for _, e := range g.Edges {
		st.Edges[e.Kind]++
	}
	g.Stats = st
}

// focused returns the part of g within depth edges of the node id. Edges are
// followed in both directions, except that a page's shared elements do not
// lead back out to the other pages that also contain them.
func (g *flowGraph) focused(id string, depth int) *flowGraph {
	adj := map[string][]flowEdge{}
	for _, e := range g.Edges {
		adj[e.From] = append(adj[e.From], e)
		adj[e.To] = append(adj[e.To], e)
	}
	dist := map[string]int{id: 0}
	queue := []string{id}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if dist[cur] >= depth {
			continue
		}
		for _, e := range adj[cur] {
			next := e.To
			if next == cur {
				next = e.From
			}
			if e.Kind == "contains" && e.Shared && e.To == cur {
				continue // shared element -> the other pages that hold it
			}
			if _, seen := dist[next]; !seen {
				dist[next] = dist[cur] + 1
				queue = append(queue, next)
			}
		}
	}
	out := &flowGraph{Version: g.Version, Focus: id, Depth: depth, Nodes: []flowNode{}, Edges: []flowEdge{}}
	for _, n := range g.Nodes {
		if _, ok := dist[n.ID]; ok {
			out.Nodes = append(out.Nodes, n)
		}
	}
	for _, e := range g.Edges {
		_, a := dist[e.From]
		_, b := dist[e.To]
		if a && b {
			out.Edges = append(out.Edges, e)
		}
	}
	for _, w := range g.Warnings {
		if w.Node == "" {
			out.Warnings = append(out.Warnings, w)
			continue
		}
		if _, ok := dist[w.Node]; ok {
			out.Warnings = append(out.Warnings, w)
		}
	}
	if out.Warnings == nil {
		out.Warnings = []flowWarning{}
	}
	out.computeStats()
	return out
}

// getFlows serves GET /drafts/{id}/flows[?focus=page:<template>|route:<name>|
// intent:<name>&depth=N].
func (s *Server) getFlows(c *call) error {
	d, err := s.draft(c)
	if err != nil {
		return err
	}
	v := d.view()
	g := s.flows.get(d.id, v.version)
	if g == nil {
		g = s.buildFlows(v.state, v.version)
		s.flows.put(d.id, g)
	}
	focus := c.r.URL.Query().Get("focus")
	if focus == "" {
		return c.json(http.StatusOK, g)
	}
	if !strings.Contains(focus, ":") {
		return errf(http.StatusBadRequest, "bad_focus", "focus must look like page:<template>, route:<name> or intent:<name>")
	}
	depth := 2
	if q := c.r.URL.Query().Get("depth"); q != "" {
		n, err := strconv.Atoi(q)
		if err != nil || n < 0 || n > 8 {
			return errf(http.StatusBadRequest, "bad_depth", "depth must be between 0 and 8")
		}
		depth = n
	}
	found := false
	for _, n := range g.Nodes {
		if n.ID == focus {
			found = true
			break
		}
	}
	if !found {
		return errf(http.StatusNotFound, "not_found", "no node %q in the journey graph", focus)
	}
	return c.json(http.StatusOK, g.focused(focus, depth))
}
