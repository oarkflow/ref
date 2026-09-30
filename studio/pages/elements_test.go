package pages

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestNormalizeURL(t *testing.T) {
	cases := []struct {
		in, path, query, redirect string
		external, dynamic, skip   bool
	}{
		{in: "/todos", path: "/todos"},
		{in: "/todos/42/edit", path: "/todos/:param/edit"},
		{in: "/todos/${todo.id}/edit?redirect=/todos/${todo.id}", path: "/todos/:param/edit", redirect: "/todos/:param"},
		{in: "/todos/{{ id }}", path: "/todos/:param"},
		{in: "/todos?redirect=%2Ftodos&page=2", path: "/todos", query: "page=2", redirect: "/todos"},
		{in: "/search?q=a&next=/home", path: "/search", query: "q=a", redirect: "/home"},
		{in: "/a/b#frag", path: "/a/b"},
		{in: "https://example.com/x/1", path: "/example.com/x/:param", external: true},
		{in: "${url}", dynamic: true},
		{in: "#top", skip: true},
		{in: "mailto:a@b.c", skip: true},
		{in: "javascript:void(0)", skip: true},
		{in: "", skip: true},
		{in: "relative/page", path: "relative/page"},
	}
	for _, c := range cases {
		got := NormalizeURL(c.in)
		want := URLInfo{Path: c.path, Query: c.query, Redirect: c.redirect, External: c.external, Dynamic: c.dynamic, Skip: c.skip}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("NormalizeURL(%q) = %+v, want %+v", c.in, got, want)
		}
	}
}

func find(els []Element, kind, label string) *Element {
	for i := range els {
		if els[i].Kind == kind && els[i].Label == label {
			return &els[i]
		}
	}
	return nil
}

func TestElementsFormsLinksButtons(t *testing.T) {
	src := `@extends("layouts/base")
<h1>Things</h1>
<a href="/things/new" class="btn">
  New thing
</a>
<a href="#top">skip</a>
<a href="mailto:x@y.z">mail</a>
@for(t in things)
<a href="/things/${t.id}" title="Open">${t.name}</a>
@end
<form method="post" action="/things?redirect=/things">
  <input type="hidden" name="csrf_token" value="${csrfToken}">
  <input name="title"><textarea name="notes"></textarea><select name="kind"></select>
  <button type="submit">Create thing</button>
</form>
<form action="/search"><input name="q"><input type="submit" value="Find"></form>
<form method="post"><input name="x"><button>Save here</button></form>
<button type="button" id="more" data-toggle="panel">More</button>
<button hx-delete="/things/${t.id}">Remove</button>
<button formaction="/things/export" formmethod="post">Export</button>
`
	els := Elements(src)
	byLabel := map[string]Element{}
	for _, e := range els {
		byLabel[e.Kind+":"+e.Label] = e
	}
	if e := byLabel["link:New thing"]; e.URL != "/things/new" || e.Method != "GET" || e.Line != 3 {
		t.Errorf("new link = %+v", e)
	}
	if e := byLabel["link:Open"]; e.URL != "/things/:param" {
		t.Errorf("row link = %+v", e)
	}
	if len(els) > 0 && find(els, "link", "skip") != nil || find(els, "link", "mail") != nil {
		t.Error("anchors and mailto links must be ignored")
	}
	f := byLabel["form:Create thing"]
	if f.Method != "POST" || f.URL != "/things" || f.Redirect != "/things" || !reflect.DeepEqual(f.Fields, []string{"title", "notes", "kind"}) {
		t.Errorf("create form = %+v", f)
	}
	if s := byLabel["form:Find"]; s.Method != "GET" || s.URL != "/search" || !reflect.DeepEqual(s.Fields, []string{"q"}) {
		t.Errorf("search form = %+v", s)
	}
	if s := byLabel["form:Save here"]; !s.Self || s.URL != "" || s.Method != "POST" {
		t.Errorf("self form = %+v", s)
	}
	if b := byLabel["button:More"]; b.URL != "" || !contains(b.Hints, "data-toggle=panel") || !contains(b.Hints, "#more") {
		t.Errorf("ui button = %+v", b)
	}
	if b := byLabel["button:Remove"]; b.Method != "DELETE" || b.URL != "/things/:param" {
		t.Errorf("hx button = %+v", b)
	}
	if b := byLabel["button:Export"]; b.Method != "POST" || b.URL != "/things/export" {
		t.Errorf("formaction button = %+v", b)
	}
	// The submit buttons of forms are not separate elements.
	if find(els, "button", "Create thing") != nil {
		t.Error("a form's submit button became its own element")
	}
}

func TestHeading(t *testing.T) {
	for src, want := range map[string]string{
		`<title>T</title><h1> Hello   world </h1>`: "T",
		`<h1>Page ${name}</h1>`:                    "Page …",
		`<h1>${title}</h1><h1>Second</h1>`:         "Second",
		`<p>no heading</p>`:                        "",
	} {
		if got := Heading(src); got != want {
			t.Errorf("Heading(%q) = %q, want %q", src, got, want)
		}
	}
}

func TestScriptElements(t *testing.T) {
	js := "var id = 3;\n" +
		"// fetch('/commented')\n" +
		"fetch(\"/api/v1/todos/\" + todoId + \"/subtasks\", {\n  method: 'POST',\n  body: 1 });\n" +
		"fetch(`/api/items/${item.id}`, { method: \"DELETE\" });\n" +
		"fetch('/api/plain');\n" +
		"fetch(url);\n" +
		"var x = new XMLHttpRequest(); x.open('PUT', '/api/legacy/' + id);\n" +
		"axios.post('/api/axios', data);\n" +
		"if (a == location.href) {}\n" +
		"window.location = '/dashboard';\n" +
		"location.assign(`/todos/${id}`);\n"
	got := map[string]Element{}
	for _, e := range ScriptElements(js, 10) {
		got[e.Method+" "+e.URL] = e
	}
	for _, want := range []string{
		"POST /api/v1/todos/:param/subtasks", "DELETE /api/items/:param", "GET /api/plain", "PUT /api/legacy/:param",
		"POST /api/axios", "GET /dashboard", "GET /todos/:param",
	} {
		if _, ok := got[want]; !ok {
			t.Errorf("missing %q; got %v", want, keys(got))
		}
	}
	if _, ok := got["GET /commented"]; ok {
		t.Error("commented-out fetch was picked up")
	}
	if e := got["POST /api/v1/todos/:param/subtasks"]; e.Line != 12 || e.Kind != "fetch" || !contains(e.Hints, "script") {
		t.Errorf("subtasks = %+v", e)
	}
	if e := got["GET "]; !e.Dynamic || e.URL != "" || e.Kind != "fetch" {
		t.Errorf("fetch(url) should be dynamic: %+v (have %v)", e, keys(got))
	}
	if e := got["GET /dashboard"]; e.Kind != "link" {
		t.Errorf("location assignment = %+v", e)
	}
}

func TestElementsForStarter(t *testing.T) {
	res := os.DirFS("../../examples/starter/resources")
	tpl := os.DirFS("../../examples/starter/resources/templates")
	pe, err := ElementsFor(tpl, res, "pages/todos/show")
	if err != nil {
		t.Fatal(err)
	}
	if pe.Name != "pages/todos/show" || len(pe.Missing) != 0 {
		t.Fatalf("name %q missing %v", pe.Name, pe.Missing)
	}
	var fetch, logout, decide *Element
	for i := range pe.Elements {
		e := &pe.Elements[i]
		switch {
		case e.Kind == "fetch" && e.URL == "/api/v1/todos/:param/subtasks":
			fetch = e
		case e.Kind == "form" && e.URL == "/logout":
			logout = e
		case e.Kind == "form" && e.URL == "/tasks/:param/decide":
			decide = e
		}
	}
	if fetch == nil || fetch.Source != "static/js/todo-subtasks.js" || fetch.Via != "pages/todos/show.html" || fetch.Shared {
		t.Fatalf("fetch = %+v", fetch)
	}
	if logout == nil || !logout.Shared || !strings.HasPrefix(logout.Source, "components/navbar") || logout.Label != "Sign out" {
		t.Fatalf("logout = %+v", logout)
	}
	if decide == nil || decide.Redirect != "/todos/:param" || decide.Shared {
		t.Fatalf("decide = %+v", decide)
	}
	// A page without the resources tree still yields its own elements and
	// reports the scripts it could not read.
	pe2, err := ElementsFor(tpl, nil, "pages/todos/show")
	if err != nil || len(pe2.Missing) == 0 {
		t.Fatalf("no resources: %v %v", err, pe2.Missing)
	}
	if _, err := ElementsFor(tpl, res, "pages/none"); err == nil {
		t.Fatal("missing template should be an error")
	}
}

func TestIsAssetURL(t *testing.T) {
	for p, want := range map[string]bool{"/static/js/a.js": true, "/favicon.ico": true, "/sw.js": true, "/img/x.png": true, "/todos": false, "/api/v1/x": false} {
		if IsAssetURL(p) != want {
			t.Errorf("IsAssetURL(%q) = %v", p, !want)
		}
	}
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
