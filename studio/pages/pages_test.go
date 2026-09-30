package pages

import (
	"io/fs"
	"os"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
)

const starterTemplates = "../../examples/starter/resources/templates"

func TestTemplateVarsExpressions(t *testing.T) {
	cases := []struct {
		name string
		src  string
		vars []string
		path []string
	}{
		{"plain", `<p>${title} — ${appName}</p>`, []string{"appName", "title"}, []string{"appName", "title"}},
		{"dotted", `${todo.status} ${todo.owner.name}`, []string{"todo"}, []string{"todo.owner.name", "todo.status"}},
		{"filters", `${name | upper} ${price | money("USD")} ${a || b}`, []string{"a", "b", "name", "price"}, []string{"a", "b", "name", "price"}},
		{"strings are not vars", `${"hello world"} ${'x.y'} ${greeting + " " + name}`, []string{"greeting", "name"}, []string{"greeting", "name"}},
		{"numbers and keywords", `${count > 10 && enabled == true ? "a" : "b"} ${x == null}`, []string{"count", "enabled", "x"}, []string{"count", "enabled", "x"}},
		{"index ends a path", `${items[0].name} ${items[i]}`, []string{"i", "items"}, []string{"i", "items"}},
		{"function call", `${len(items)} ${user.name.upper()}`, []string{"items", "user"}, []string{"items", "user.name"}},
		{"optional chaining", `${user?.address?.city}`, []string{"user"}, []string{"user.address.city"}},
		{"object literal", `${ {"a": x, "b": y.z} }`, []string{"x", "y"}, []string{"x", "y.z"}},
		{"nested braces", `${ {"k": v} } and ${w}`, []string{"v", "w"}, []string{"v", "w"}},
		{"builtin loop", `@for(i in list) { ${loop.index1} ${i} }`, []string{"list"}, []string{"list"}},
		{"for locals", `@for(todo in todos) { ${todo.title} ${owner} }`, []string{"owner", "todos"}, []string{"owner", "todos"}},
		{"for with index and key", `@for(idx, row in rows; key row.id) { ${idx} ${row.name} }`, []string{"rows"}, []string{"rows"}},
		{"if conditions", `@if(user.roles == "admin" && !locked) { x } @elseif(mode) { y }`, []string{"locked", "mode", "user"}, []string{"locked", "mode", "user.roles"}},
		{"let binds", `@let(total = price * qty) ${total} ${tax}`, []string{"price", "qty", "tax"}, []string{"price", "qty", "tax"}},
		{"signal binds", `@signal(count = 0) @bind(count) ${count} ${other}`, []string{"other"}, []string{"other"}},
		{"component props", `@component("Badge", label string, color string = fallback) { ${label} ${color} ${extra} }`, []string{"extra", "fallback"}, []string{"extra", "fallback"}},
		{"render props", `@render("Card", item) @render("Badge", {"label": tag})`, []string{"item", "tag"}, []string{"item", "tag"}},
		{"switch", `@switch(status) { @case("a") { x } @default { y } }`, []string{"status"}, []string{"status"}},
		{"email is not a directive", `<a href="mailto:ops@example.com">ops@example.com</a> ${who}`, []string{"who"}, []string{"who"}},
		{"css at-rules ignored", `<style>@media (max-width: 600px) { a { color: red } }</style> ${x}`, []string{"x"}, []string{"x"}},
		{"raw block skipped", `@raw { ${notAVar} @if(nope) { } } ${real}`, []string{"real"}, []string{"real"}},
		{"unterminated", `${oops`, []string{}, []string{}},
		{"empty", ``, []string{}, []string{}},
		{"dollar builtins", `${$loop.index} ${$ctx}`, []string{}, []string{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := Analyze(c.src)
			if !reflect.DeepEqual(a.Vars, c.vars) {
				t.Errorf("vars = %v; want %v", a.Vars, c.vars)
			}
			if !reflect.DeepEqual(a.Paths, c.path) {
				t.Errorf("paths = %v; want %v", a.Paths, c.path)
			}
			if !reflect.DeepEqual(TemplateVars(c.src), c.vars) {
				t.Errorf("TemplateVars = %v", TemplateVars(c.src))
			}
		})
	}
}

func TestAnalyzeStructure(t *testing.T) {
	page := `@extends("layouts/base.html")
@define("content") {
  @include("components/alert.html")
  @include("components/todos/row.html")
  @include("components/alert.html")
  <h1>${title | upper}</h1>
}
@define("scripts") { }`
	a := Analyze(page)
	if a.Extends != "layouts/base.html" {
		t.Errorf("extends = %q", a.Extends)
	}
	if !reflect.DeepEqual(a.Includes, []string{"components/alert.html", "components/todos/row.html"}) {
		t.Errorf("includes = %v", a.Includes)
	}
	if !reflect.DeepEqual(a.Defines, []string{"content", "scripts"}) {
		t.Errorf("defines = %v", a.Defines)
	}
	if !reflect.DeepEqual(a.Filters, []string{"upper"}) {
		t.Errorf("filters = %v", a.Filters)
	}
	if got := Includes(page); !reflect.DeepEqual(got, []string{"layouts/base.html", "components/alert.html", "components/todos/row.html"}) {
		t.Errorf("Includes = %v", got)
	}
	layout := Analyze(`<body>@block("head") {} @block("content") {} @block("head") {}</body>`)
	if !reflect.DeepEqual(layout.Blocks, []string{"head", "content"}) {
		t.Errorf("blocks = %v", layout.Blocks)
	}
	comp := Analyze(`@component("Panel") { @slot("header") @slot } @render("Panel") { @fill("header") { x } }`)
	if !reflect.DeepEqual(comp.Components, []string{"Panel"}) || !reflect.DeepEqual(comp.Slots, []string{"header"}) || !reflect.DeepEqual(comp.Fills, []string{"header"}) {
		t.Errorf("components/slots/fills = %v %v %v", comp.Components, comp.Slots, comp.Fills)
	}
}

func TestAnalyzeReportsSyntaxErrors(t *testing.T) {
	a := Analyze("<p>${ok}</p>\n@if(x) {\n  never closed")
	var errs int
	for _, d := range a.Diagnostics {
		if d.Severity == "error" {
			errs++
		}
	}
	if errs == 0 {
		t.Fatalf("expected a syntax error, got %+v", a.Diagnostics)
	}
	// The scanner still reports what it could read.
	if !reflect.DeepEqual(a.Vars, []string{"ok", "x"}) {
		t.Errorf("vars = %v", a.Vars)
	}
	if good := Analyze("<p>${ok}</p>"); len(good.Diagnostics) != 0 {
		t.Errorf("unexpected diagnostics: %+v", good.Diagnostics)
	}
	if w := Analyze(`${x | nosuchfilter}`); len(w.Diagnostics) == 0 || w.Diagnostics[0].Severity != "warning" {
		t.Errorf("expected an unknown-filter warning: %+v", w.Diagnostics)
	}
}

func TestResolveSynthetic(t *testing.T) {
	fsys := fstest.MapFS{
		"layouts/base.html":     {Data: []byte(`<title>${title}</title>@include("components/nav.html")@block("head") {}@block("content") {}@block("scripts") {}`)},
		"components/nav.html":   {Data: []byte(`<nav>${user.name} @include("components/nav.html")</nav>`)},
		"components/row.html":   {Data: []byte(`<tr>${item.name} ${currency}</tr>`)},
		"pages/list.html":       {Data: []byte(`@extends("layouts/base.html") @define("content") { @for(item in items) { @include("components/row.html") } @include("components/gone.html") } @define("footer") { }`)},
		"pages/bad-layout.html": {Data: []byte(`@extends("layouts/nope.html") @define("content") { }`)},
	}
	r, err := Resolve(fsys, "pages/list")
	if err != nil {
		t.Fatal(err)
	}
	if r.Name != "pages/list.html" {
		t.Errorf("name = %q", r.Name)
	}
	if want := []string{"pages/list.html", "layouts/base.html", "components/nav.html", "components/row.html"}; !reflect.DeepEqual(r.Files, want) {
		t.Errorf("files = %v; want %v", r.Files, want)
	}
	if !reflect.DeepEqual(r.Layouts, []string{"layouts/base.html"}) {
		t.Errorf("layouts = %v", r.Layouts)
	}
	// item is bound by the page's @for, so the included row does not need it.
	if want := []string{"currency", "items", "title", "user"}; !reflect.DeepEqual(r.Vars, want) {
		t.Errorf("vars = %v; want %v", r.Vars, want)
	}
	if !reflect.DeepEqual(r.Missing, []string{"components/gone.html"}) {
		t.Errorf("missing = %v", r.Missing)
	}
	if want := [][]string{{"components/nav.html", "components/nav.html"}}; !reflect.DeepEqual(r.Cycles, want) {
		t.Errorf("cycles = %v", r.Cycles)
	}
	if !reflect.DeepEqual(r.Blocks, []string{"content", "head", "scripts"}) ||
		!reflect.DeepEqual(r.Unfilled, []string{"head", "scripts"}) ||
		!reflect.DeepEqual(r.Unknown, []string{"footer"}) {
		t.Errorf("blocks %v unfilled %v unknown %v", r.Blocks, r.Unfilled, r.Unknown)
	}
	bl, err := Resolve(fsys, "pages/bad-layout.html")
	if err != nil || !reflect.DeepEqual(bl.Missing, []string{"layouts/nope.html"}) {
		t.Errorf("bad layout: %v %+v", err, bl)
	}
	if _, err := Resolve(fsys, "pages/none"); err == nil {
		t.Error("a missing template should be an error")
	}
	// Paths outside the tree never resolve.
	if _, err := Resolve(fsys, "../secret"); err == nil {
		t.Error("escaping path resolved")
	}
}

// TestStarterTemplates runs the analysis over every template the starter
// ships: none may report a syntax error, every include and layout resolves,
// and the todo list page reports what its route's intent must publish.
func TestStarterTemplates(t *testing.T) {
	fsys := os.DirFS(starterTemplates)
	entries := map[string]bool{}
	err := walkHTML(t, starterTemplates, func(rel string) { entries[rel] = true })
	if err != nil || len(entries) < 15 {
		t.Fatalf("templates: %d %v", len(entries), err)
	}
	for name := range entries {
		src, err := os.ReadFile(starterTemplates + "/" + name)
		if err != nil {
			t.Fatal(err)
		}
		a := Analyze(string(src))
		for _, d := range a.Diagnostics {
			if d.Severity == "error" {
				t.Errorf("%s: %+v", name, d)
			}
		}
		r, err := Resolve(fsys, name)
		if err != nil {
			t.Fatal(err)
		}
		if len(r.Missing) > 0 || len(r.Cycles) > 0 {
			t.Errorf("%s: missing %v cycles %v", name, r.Missing, r.Cycles)
		}
		if len(r.Unknown) > 0 {
			t.Errorf("%s defines blocks its layout does not offer: %v", name, r.Unknown)
		}
	}

	r, err := Resolve(fsys, "pages/todos/list")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(r.Layouts, []string{"layouts/base.html"}) {
		t.Errorf("layouts = %v", r.Layouts)
	}
	if want := []string{"pages/todos/list.html", "layouts/base.html", "components/navbar.html", "components/alert.html", "components/footer.html", "components/todos/row.html", "components/todos/status-badge.html"}; !sameSet(r.Files, want) {
		t.Errorf("files = %v", r.Files)
	}
	for _, v := range []string{"todos", "appName", "title"} {
		if !contains(r.Vars, v) {
			t.Errorf("list page should need %q: %v", v, r.Vars)
		}
	}
	if contains(r.Vars, "todo") {
		t.Errorf("todo is the page's loop variable, not an input: %v", r.Vars)
	}
	if !contains(r.Paths, "todos") {
		t.Errorf("paths: %v", r.Paths)
	}
	if !contains(r.Blocks, "content") || contains(r.Unfilled, "content") {
		t.Errorf("blocks %v unfilled %v", r.Blocks, r.Unfilled)
	}
	a := r.Analyses["pages/todos/list.html"]
	if a.Extends != "layouts/base.html" || !contains(a.Defines, "content") || !contains(a.Includes, "components/todos/row.html") {
		t.Errorf("list analysis: %+v", a)
	}
}

func walkHTML(t *testing.T, root string, fn func(rel string)) error {
	t.Helper()
	fsys := os.DirFS(root)
	return fsWalk(fsys, func(p string) {
		if strings.HasSuffix(p, ".html") {
			fn(p)
		}
	})
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for _, x := range a {
		if !contains(b, x) {
			return false
		}
	}
	return true
}

func fsWalk(fsys fs.FS, fn func(p string)) error {
	return fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			fn(p)
		}
		return nil
	})
}
