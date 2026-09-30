package server

import (
	"strings"
	"testing"
)

// flowsEnv serves the real starter: its config directory and resources.
func flowsEnv(t *testing.T) (*env, string) {
	t.Helper()
	e := newEnvDir(t, "../../examples/starter/resources/config", func(c *Config) {
		c.ResourcesDir = "../../examples/starter/resources"
		c.TemplateGlobals = []string{"appName", "csrfToken"}
	})
	id, _ := e.newDraft(tokEditor, "dir")
	return e, id
}

type flowView struct {
	g     obj
	nodes map[string]obj
	edges map[string]obj
}

func loadFlows(e *env, id, query string) flowView {
	e.t.Helper()
	g := e.ok(200, "GET", "/api/v1/drafts/"+id+"/flows"+query, tokEditor, nil)
	v := flowView{g: g, nodes: map[string]obj{}, edges: map[string]obj{}}
	for _, n := range g["nodes"].([]any) {
		v.nodes[n.(obj)["id"].(string)] = n.(obj)
	}
	for _, x := range g["edges"].([]any) {
		v.edges[x.(obj)["id"].(string)] = x.(obj)
	}
	return v
}

// out lists the targets of edges of kind leaving from.
func (v flowView) out(kind, from string) []string {
	var r []string
	for _, x := range v.edges {
		if x["kind"] == kind && x["from"] == from {
			r = append(r, x["to"].(string))
		}
	}
	return r
}

func (v flowView) find(kind, subkind, label string) obj {
	for _, n := range v.nodes {
		if n["kind"] == kind && (subkind == "" || n["subkind"] == subkind) && n["label"] == label {
			return n
		}
	}
	return nil
}

func TestFlowsJourneyOverStarter(t *testing.T) {
	e, id := flowsEnv(t)
	v := loadFlows(e, id, "")

	// The todo list page is served by web.todos_list, which runs todo.list.
	list := v.nodes["page:pages/todos/list"]
	if list == nil || list["label"] != "Todos" {
		t.Fatalf("list page = %v", list)
	}
	if d := list["data"].(obj); d["protected"] != true || d["unused"] != false {
		t.Fatalf("list page data = %v", d)
	}
	if !contains(v.out("renders", "route:web.todos_list"), "page:pages/todos/list") {
		t.Fatalf("todos_list does not render the list page: %v", v.out("renders", "route:web.todos_list"))
	}
	if !contains(v.out("runs", "route:web.todos_list"), "intent:todo.list") {
		t.Fatalf("todos_list does not run todo.list: %v", v.out("runs", "route:web.todos_list"))
	}

	// A link on the list page to /todos/new navigates to the new-page route.
	newLink := v.find("element", "link", "New Todo")
	if newLink == nil {
		t.Fatal("no 'New Todo' link")
	}
	if newLink["shared"] == true || newLink["group"] != "page:pages/todos/list" || newLink["file"] != "templates/pages/todos/list.html" || newLink["line"].(float64) == 0 {
		t.Fatalf("link = %v", newLink)
	}
	if !contains(v.out("navigates", newLink["id"].(string)), "route:web.todos_new") {
		t.Fatalf("link goes to %v", v.out("navigates", newLink["id"].(string)))
	}
	if !contains(v.out("renders", "route:web.todos_new"), "page:pages/todos/new") {
		t.Fatal("web.todos_new does not render the new page")
	}
	if !contains(v.out("contains", "page:pages/todos/list"), newLink["id"].(string)) {
		t.Fatal("list page does not contain its link")
	}

	// The new-todo form posts to the create route, which runs todo.create,
	// which uses the database, and sends the visitor back to the list.
	form := v.find("element", "form", "Save draft")
	if form == nil {
		t.Fatal("no 'Save draft' form")
	}
	fd := form["data"].(obj)
	if fd["method"] != "POST" || fd["url"] != "/todos" || fd["redirect"] != "/todos" || !contains(strs(fd["fields"]), "title") {
		t.Fatalf("form data = %v", fd)
	}
	fid := form["id"].(string)
	if !contains(v.out("calls", fid), "route:web.todos_create") {
		t.Fatalf("form calls %v", v.out("calls", fid))
	}
	if !contains(v.out("runs", "route:web.todos_create"), "intent:todo.create") {
		t.Fatalf("todos_create runs %v", v.out("runs", "route:web.todos_create"))
	}
	uses := v.out("uses", "intent:todo.create")
	if len(uses) == 0 {
		t.Fatal("todo.create uses no resource")
	}
	sawDB := false
	for _, r := range uses {
		if n := v.nodes[r]; n != nil && n["subkind"] == "database" {
			sawDB = true
		}
	}
	if !sawDB {
		t.Fatalf("todo.create resources = %v", uses)
	}
	if !contains(v.out("redirects", "route:web.todos_create"), "page:pages/todos/list") {
		t.Fatalf("todos_create redirects to %v", v.out("redirects", "route:web.todos_create"))
	}
	for _, x := range v.edges {
		if x["kind"] == "redirects" && x["from"] == "route:web.todos_create" && x["label"] != "on success" {
			t.Fatalf("redirect edge = %v", x)
		}
	}
}

func TestFlowsSharedNavbar(t *testing.T) {
	e, id := flowsEnv(t)
	v := loadFlows(e, id, "")
	logout := v.find("element", "form", "Sign out")
	if logout == nil {
		t.Fatal("no logout form")
	}
	if logout["shared"] != true || !strings.HasPrefix(logout["group"].(string), "shared:components/navbar") {
		t.Fatalf("logout = %v", logout)
	}
	lid := logout["id"].(string)
	// One node, contained by every page that includes the navbar.
	holders := 0
	for _, x := range v.edges {
		if x["kind"] == "contains" && x["to"] == lid {
			holders++
			if x["shared"] != true {
				t.Fatalf("contains edge not marked shared: %v", x)
			}
		}
	}
	if holders < 3 {
		t.Fatalf("only %d pages hold the shared logout form", holders)
	}
	if !contains(v.out("calls", lid), "route:web.logout_action") {
		t.Fatalf("logout calls %v", v.out("calls", lid))
	}
}

func TestFlowsScriptFetchResolves(t *testing.T) {
	e, id := flowsEnv(t)
	v := loadFlows(e, id, "")
	var fetch obj
	for _, n := range v.nodes {
		if n["kind"] == "element" && n["subkind"] == "fetch" {
			if d := n["data"].(obj); d["url"] == "/api/v1/todos/:param/subtasks" {
				fetch = n
			}
		}
	}
	if fetch == nil {
		t.Fatal("no fetch element for the subtasks request")
	}
	if fetch["file"] != "static/js/todo-subtasks.js" || fetch["line"].(float64) < 90 || fetch["data"].(obj)["method"] != "POST" {
		t.Fatalf("fetch = %v", fetch)
	}
	fid := fetch["id"].(string)
	calls := v.out("calls", fid)
	if len(calls) != 1 || !strings.HasPrefix(calls[0], "route:") {
		t.Fatalf("subtasks fetch calls %v", calls)
	}
	if rd := v.nodes[calls[0]]["data"].(obj); rd["path"] != "/api/v1/todos/:id/subtasks" || rd["method"] != "POST" {
		t.Fatalf("subtasks route = %v", rd)
	}
	// The request comes from the show page.
	if !contains(v.out("contains", "page:pages/todos/show"), fid) {
		t.Fatal("show page does not contain the fetch")
	}
}

func TestFlowsUnresolvedIsFlagged(t *testing.T) {
	e, id := flowsEnv(t)
	base := loadFlows(e, id, "")
	if n := int(base.g["stats"].(obj)["unresolved"].(float64)); n != 0 {
		var ws []any
		for _, w := range base.g["warnings"].([]any) {
			if w.(obj)["code"] == "flows.unresolved" {
				ws = append(ws, w)
			}
		}
		t.Logf("starter already has %d unresolved: %v", n, ws)
	}
	before := int(base.g["stats"].(obj)["unresolved"].(float64))

	// A page whose button posts to a URL no route serves.
	e.ok(200, "PUT", "/api/v1/drafts/"+id+"/assets/templates/pages/todos/new.html", tokEditor, obj{"content": `@extends("layouts/base")
<h1>New todo</h1>
<form method="POST" action="/todos/archive-all"><button type="submit">Archive everything</button></form>
<a href="/nowhere/${todo.id}">Lost</a>
`, "ifVersion": nil})
	v := loadFlows(e, id, "")
	u := v.find("unresolved", "", "POST /todos/archive-all")
	if u == nil {
		t.Fatalf("no unresolved node; nodes: %d", len(v.nodes))
	}
	if int(v.g["stats"].(obj)["unresolved"].(float64)) != before+2 {
		t.Fatalf("stats = %v", v.g["stats"])
	}
	found := false
	for _, w := range v.g["warnings"].([]any) {
		w := w.(obj)
		if w["code"] == "flows.unresolved" && strings.Contains(w["message"].(string), "/todos/archive-all") && w["severity"] == "warning" &&
			w["file"] == "templates/pages/todos/new.html" && w["line"].(float64) > 0 {
			found = true
		}
	}
	if !found {
		t.Fatalf("warnings = %v", v.g["warnings"])
	}
	form := v.find("element", "form", "Archive everything")
	if form == nil || !contains(v.out("calls", form["id"].(string)), u["id"].(string)) {
		t.Fatal("form does not lead to the unresolved node")
	}
}

func TestFlowsFocusAndDepth(t *testing.T) {
	e, id := flowsEnv(t)
	all := loadFlows(e, id, "")

	d1 := loadFlows(e, id, "?focus=route:web.todos_create&depth=1")
	if d1.g["focus"] != "route:web.todos_create" || int(d1.g["depth"].(float64)) != 1 {
		t.Fatalf("focus echo = %v %v", d1.g["focus"], d1.g["depth"])
	}
	for _, want := range []string{"route:web.todos_create", "intent:todo.create", "page:pages/todos/list"} {
		if d1.nodes[want] == nil {
			t.Fatalf("depth 1 misses %s (have %d nodes)", want, len(d1.nodes))
		}
	}
	// The form that calls it is one hop away too, but the database is two.
	if d1.find("element", "form", "Save draft") == nil {
		t.Fatal("depth 1 misses the calling form")
	}
	for _, n := range d1.nodes {
		if n["kind"] == "resource" {
			t.Fatalf("depth 1 reaches resource %v", n["id"])
		}
	}
	d2 := loadFlows(e, id, "?focus=route:web.todos_create&depth=2")
	sawRes := false
	for _, n := range d2.nodes {
		if n["kind"] == "resource" {
			sawRes = true
		}
	}
	if !sawRes || len(d2.nodes) <= len(d1.nodes) || len(d2.nodes) >= len(all.nodes) {
		t.Fatalf("depth sizes: d1=%d d2=%d all=%d resource=%v", len(d1.nodes), len(d2.nodes), len(all.nodes), sawRes)
	}
	// Every edge of a focused graph stays inside it.
	for _, x := range d2.edges {
		if d2.nodes[x["from"].(string)] == nil || d2.nodes[x["to"].(string)] == nil {
			t.Fatalf("dangling edge %v", x)
		}
	}
	// A shared element does not drag in every other page that holds it.
	pg := loadFlows(e, id, "?focus=page:pages/auth/login&depth=1")
	for id := range pg.nodes {
		if strings.HasPrefix(id, "page:") && id != "page:pages/auth/login" {
			t.Fatalf("page focus pulled in %s", id)
		}
	}
	// Intent focus.
	if in := loadFlows(e, id, "?focus=intent:todo.create&depth=1"); in.nodes["route:web.todos_create"] == nil {
		t.Fatal("intent focus misses its route")
	}

	e.status(404, "GET", "/api/v1/drafts/"+id+"/flows?focus=page:pages/none", tokEditor, nil)
	e.status(400, "GET", "/api/v1/drafts/"+id+"/flows?focus=nonsense", tokEditor, nil)
	e.status(400, "GET", "/api/v1/drafts/"+id+"/flows?focus=route:web.todos_list&depth=99", tokEditor, nil)
	e.status(401, "GET", "/api/v1/drafts/"+id+"/flows", "", nil)
}

func TestFlowsIDsAreStable(t *testing.T) {
	e, id := flowsEnv(t)
	a := loadFlows(e, id, "")
	b := loadFlows(e, id, "")
	if len(a.nodes) != len(b.nodes) || len(a.edges) != len(b.edges) {
		t.Fatalf("counts differ: %d/%d vs %d/%d", len(a.nodes), len(a.edges), len(b.nodes), len(b.edges))
	}
	// A second draft of the same source yields the same ids.
	id2, _ := e.newDraft(tokEditor, "dir")
	c := loadFlows(e, id2, "")
	for k := range a.nodes {
		if c.nodes[k] == nil {
			t.Fatalf("node %s missing in the second draft", k)
		}
	}
	for k := range a.edges {
		if c.edges[k] == nil {
			t.Fatalf("edge %s missing in the second draft", k)
		}
	}
	// Editing one page leaves the ids of the others alone.
	e.ok(200, "PUT", "/api/v1/drafts/"+id+"/assets/templates/pages/todos/new.html", tokEditor, obj{"content": "<h1>New</h1>\n<a href=\"/todos\">Back</a>\n"})
	d := loadFlows(e, id, "")
	for k := range a.nodes {
		if strings.HasPrefix(k, "element:page:pages/todos/new#") {
			continue
		}
		if d.nodes[k] == nil {
			t.Fatalf("node %s vanished after editing another page", k)
		}
	}
	// Cache: the version in the payload follows the draft.
	if a.g["version"] == d.g["version"] {
		t.Fatalf("version not bumped: %v", d.g["version"])
	}
}

func TestFlowsStatsAndCoverage(t *testing.T) {
	e, id := flowsEnv(t)
	v := loadFlows(e, id, "")
	st := v.g["stats"].(obj)
	t.Logf("starter journey: pages=%v routes=%v elements=%v shared=%v unresolved=%v unusedPages=%v nodes=%v edges=%v",
		st["pages"], st["routes"], st["elements"], st["sharedElements"], st["unresolved"], st["unusedPages"], st["nodes"], st["edges"])
	if st["pages"].(float64) < 8 || st["routes"].(float64) < 20 || st["elements"].(float64) < 20 {
		t.Fatalf("stats too small: %v", st)
	}
	if st["sharedElements"].(float64) < 5 {
		t.Fatalf("shared elements = %v", st["sharedElements"])
	}
	for _, w := range v.g["warnings"].([]any) {
		w := w.(obj)
		if w["severity"] != "warning" && w["severity"] != "info" {
			t.Fatalf("bad severity %v", w)
		}
		if w["code"] == "" || w["message"] == "" {
			t.Fatalf("warning = %v", w)
		}
	}
	// Viewers can read, but only the owner or an admin.
	e.status(403, "GET", "/api/v1/drafts/"+id+"/flows", tokViewer, nil)
}

// status asserts only the status code of a call.
func (e *env) status(want int, method, path, token string, body any) {
	e.t.Helper()
	if got, raw := e.call(method, path, token, body); got != want {
		e.t.Fatalf("%s %s = %d, want %d: %s", method, path, got, want, raw)
	}
}
