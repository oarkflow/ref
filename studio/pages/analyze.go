// Package pages introspects SPL page templates for the Studio page tools: which
// variables a template reads, which layout it extends, which components and
// files it pulls in, and which named slots ("blocks") a layout offers to the
// pages built on it.
//
// The syntax checks, the component/filter/include lists and the diagnostics
// come from the SPL engine's own parser (spl.Engine.AnalyzeTemplate), so they
// agree with what the renderer accepts. The variable lists come from a
// scanner in this package: the engine does not expose the expressions it
// parses, and the pages UI needs to tell an author which data a route's
// intent has to publish.
//
// Variable analysis is best effort. Scopes are flat: a name bound anywhere in
// a file (a loop variable, an @let, a component prop) is treated as local
// everywhere in it. A variable the template reads is therefore never missed,
// but one that is also bound somewhere in the same file may be left out.
package pages

import (
	"io/fs"
	"path"
	"sort"
	"strings"

	"github.com/oarkflow/spl"
)

// Diagnostic is a template syntax error or warning.
type Diagnostic struct {
	Severity string `json:"severity"` // "error" or "warning"
	Message  string `json:"message"`
	Line     int    `json:"line,omitempty"`
	Column   int    `json:"column,omitempty"`
}

// Analysis describes one template.
type Analysis struct {
	// Vars are the free variables the template reads: the root names of
	// Paths, minus anything the template binds itself. These are what the
	// route's intent (or the engine's globals) must supply.
	Vars []string `json:"vars"`
	// Paths are the dotted paths read ("todo.status", "user.roles"), minus
	// paths rooted in a local. Index expressions end a path ("items[0].x"
	// records "items").
	Paths []string `json:"paths"`
	// Locals are the names the template binds: loop variables, @let, @set,
	// @signal, @local and @computed names, and component props.
	Locals []string `json:"locals"`
	// Extends is the layout named by @extends ("" when none).
	Extends string `json:"extends,omitempty"`
	// Includes are the files pulled in by @include and @import, in order.
	Includes []string `json:"includes"`
	// Blocks are the @block names a layout offers; Defines are the @define
	// names a page fills in. A page should define only blocks its layout has.
	Blocks  []string `json:"blocks"`
	Defines []string `json:"defines"`
	// Slots are the @slot names of a component; Fills the @fill names used
	// with @render.
	Slots []string `json:"slots"`
	Fills []string `json:"fills"`
	// Components are the components the template defines or renders.
	Components []string `json:"components"`
	// Filters are the filters applied with `|`.
	Filters     []string     `json:"filters"`
	Diagnostics []Diagnostic `json:"diagnostics"`
}

// TemplateVars lists the free variables src reads (Analysis.Vars).
func TemplateVars(src string) []string { return Analyze(src).Vars }

// Includes lists the files src pulls in: its @extends layout first, then its
// @include and @import paths in order, without duplicates.
func Includes(src string) []string {
	a := Analyze(src)
	var out []string
	seen := map[string]bool{}
	add := func(p string) {
		if p != "" && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	add(a.Extends)
	for _, p := range a.Includes {
		add(p)
	}
	return out
}

// Analyze inspects one template. It never fails: a template that does not
// parse still yields what the scanner found, plus the parser's diagnostics.
func Analyze(src string) Analysis {
	sc := &scanner{src: src, seen: map[string]*[]string{}}
	sc.run()

	a := Analysis{
		Extends:  sc.extends,
		Includes: nonNil(sc.list("include")),
		Blocks:   nonNil(sc.list("block")),
		Defines:  nonNil(sc.list("define")),
		Slots:    nonNil(sc.list("slot")),
		Fills:    nonNil(sc.list("fill")),
	}

	// The engine's parser: syntax diagnostics, and the component, filter and
	// include lists as the renderer sees them.
	engine := spl.New()
	an := engine.AnalyzeTemplate(src)
	for _, d := range an.Diagnostics {
		a.Diagnostics = append(a.Diagnostics, Diagnostic{Severity: d.Severity, Message: d.Message, Line: d.Line, Column: d.Column})
	}
	a.Components = mergeSorted(an.Components, sc.list("component"), sc.list("render"))
	a.Filters = mergeSorted(an.Filters, sc.list("filter"))
	a.Includes = mergeOrdered(a.Includes, an.Includes)
	a.Diagnostics = nonNilDiag(a.Diagnostics)

	locals := map[string]bool{}
	for name := range sc.locals {
		locals[name] = true
	}
	for name := range builtinNames {
		locals[name] = true
	}
	var paths, roots []string
	seenPath, seenRoot := map[string]bool{}, map[string]bool{}
	for _, p := range sc.pathOrder {
		root, _, _ := strings.Cut(p, ".")
		if locals[root] {
			continue
		}
		if !seenPath[p] {
			seenPath[p] = true
			paths = append(paths, p)
		}
		if !seenRoot[root] {
			seenRoot[root] = true
			roots = append(roots, root)
		}
	}
	sort.Strings(paths)
	sort.Strings(roots)
	a.Paths, a.Vars = nonNil(paths), nonNil(roots)
	for name := range sc.locals {
		a.Locals = append(a.Locals, name)
	}
	sort.Strings(a.Locals)
	a.Locals = nonNil(a.Locals)
	return a
}

// Resolved is a template together with everything it pulls in.
type Resolved struct {
	Name string `json:"name"`
	// Files are the templates involved, the page first, then its layout
	// chain, then everything it includes (depth first, each once).
	Files []string `json:"files"`
	// Layouts is the @extends chain, nearest first.
	Layouts []string `json:"layouts"`
	// Vars are the free variables of all the files together, minus every
	// name any of them binds. An included component that reads a loop
	// variable of the page therefore does not list it.
	Vars  []string `json:"vars"`
	Paths []string `json:"paths"`
	// Blocks are the @block slots offered by the layout chain; Unfilled are
	// the ones the page does not @define; Unknown are @defines no layout
	// offers.
	Blocks   []string `json:"blocks"`
	Unfilled []string `json:"unfilled"`
	Unknown  []string `json:"unknown"`
	// Missing are includes and layouts that could not be read.
	Missing []string `json:"missing"`
	// Cycles are include or layout cycles, each as the path around it.
	Cycles      [][]string              `json:"cycles"`
	Diagnostics map[string][]Diagnostic `json:"diagnostics,omitempty"`
	// Analyses holds each file's own analysis.
	Analyses map[string]Analysis `json:"analyses"`
}

// Resolve analyses the template name (relative to the root of fsys, with or
// without the ".html" extension) and everything it extends and includes.
// It fails only if the template itself cannot be read.
func Resolve(fsys fs.FS, name string) (*Resolved, error) {
	name = normalize(name)
	src, err := fs.ReadFile(fsys, name)
	if err != nil {
		return nil, err
	}
	r := &Resolved{Name: name, Analyses: map[string]Analysis{}, Diagnostics: map[string][]Diagnostic{}}
	visited := map[string]bool{}
	var stack []string

	var visit func(file, content string)
	visit = func(file, content string) {
		visited[file] = true
		r.Files = append(r.Files, file)
		an := Analyze(content)
		r.Analyses[file] = an
		if len(an.Diagnostics) > 0 {
			r.Diagnostics[file] = an.Diagnostics
		}
		stack = append(stack, file)
		defer func() { stack = stack[:len(stack)-1] }()
		for _, dep := range Includes(content) {
			dep = normalize(dep)
			if i := indexOf(stack, dep); i >= 0 {
				r.Cycles = append(r.Cycles, append(append([]string{}, stack[i:]...), dep))
				continue
			}
			if visited[dep] {
				continue
			}
			b, err := fs.ReadFile(fsys, dep)
			if err != nil {
				if !contains(r.Missing, dep) {
					r.Missing = append(r.Missing, dep)
				}
				continue
			}
			visit(dep, string(b))
		}
	}
	visit(name, string(src))

	// The layout chain.
	chainSeen := map[string]bool{name: true}
	for cur := r.Analyses[name].Extends; cur != ""; {
		cur = normalize(cur)
		if chainSeen[cur] {
			break
		}
		chainSeen[cur] = true
		r.Layouts = append(r.Layouts, cur)
		cur = r.Analyses[cur].Extends
	}
	if r.Layouts == nil {
		r.Layouts = []string{}
	}

	// Union of reads, minus every local of every file.
	allLocals := map[string]bool{}
	for _, an := range r.Analyses {
		for _, l := range an.Locals {
			allLocals[l] = true
		}
	}
	pathSet, varSet := map[string]bool{}, map[string]bool{}
	for _, an := range r.Analyses {
		for _, p := range an.Paths {
			root, _, _ := strings.Cut(p, ".")
			if allLocals[root] {
				continue
			}
			pathSet[p] = true
			varSet[root] = true
		}
		// A variable a file reads that another file binds was already
		// removed from Vars above only for that file's own locals; the
		// union step here removes the cross-file ones.
		for _, v := range an.Vars {
			if !allLocals[v] {
				varSet[v] = true
			}
		}
	}
	r.Vars, r.Paths = sortedKeys(varSet), sortedKeys(pathSet)

	// Blocks: what the layout chain offers versus what the page defines.
	offered := map[string]bool{}
	for _, l := range r.Layouts {
		for _, b := range r.Analyses[l].Blocks {
			offered[b] = true
		}
	}
	defined := map[string]bool{}
	for _, d := range r.Analyses[name].Defines {
		defined[d] = true
	}
	r.Blocks = sortedKeys(offered)
	r.Unfilled, r.Unknown = []string{}, []string{}
	for b := range offered {
		if !defined[b] {
			r.Unfilled = append(r.Unfilled, b)
		}
	}
	if len(r.Layouts) > 0 {
		for d := range defined {
			if !offered[d] {
				r.Unknown = append(r.Unknown, d)
			}
		}
	}
	sort.Strings(r.Unfilled)
	sort.Strings(r.Unknown)
	if r.Missing == nil {
		r.Missing = []string{}
	}
	if r.Cycles == nil {
		r.Cycles = [][]string{}
	}
	return r, nil
}

// normalize mirrors the renderer: clean, slash-separated, ".html" appended
// when the name has no extension of that kind.
func normalize(name string) string {
	name = path.Clean(strings.ReplaceAll(strings.TrimSpace(name), `\`, "/"))
	name = strings.TrimPrefix(name, "/")
	if !strings.HasSuffix(name, ".html") {
		name += ".html"
	}
	return name
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func nonNilDiag(d []Diagnostic) []Diagnostic {
	if d == nil {
		return []Diagnostic{}
	}
	return d
}

func indexOf(s []string, v string) int {
	for i, x := range s {
		if x == v {
			return i
		}
	}
	return -1
}

func contains(s []string, v string) bool { return indexOf(s, v) >= 0 }

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// mergeSorted returns the sorted union of lists.
func mergeSorted(lists ...[]string) []string {
	set := map[string]bool{}
	for _, l := range lists {
		for _, v := range l {
			set[v] = true
		}
	}
	return sortedKeys(set)
}

// mergeOrdered appends to a the entries of b it does not already have.
func mergeOrdered(a, b []string) []string {
	for _, v := range b {
		if !contains(a, v) {
			a = append(a, v)
		}
	}
	return a
}
