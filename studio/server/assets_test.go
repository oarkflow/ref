package server

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/oarkflow/fh"

	"github.com/oarkflow/ref/platform"
	"github.com/oarkflow/ref/studio/preview"
)

const (
	pageA   = "templates/pages/hello.html"
	layoutA = "templates/layouts/base.html"
)

// diskResources is a tiny host resources directory.
func diskResources() fstest.MapFS {
	return fstest.MapFS{
		"templates/pages/list.html":      {Data: []byte("@extends(\"layouts/base\")\n<h1>${title}</h1>")},
		"templates/layouts/base.html":    {Data: []byte("<html>@block(\"content\") {}</html>")},
		"templates/components/card.html": {Data: []byte("<div>card</div>")},
		"static/css/app.css":             {Data: []byte("body{}")},
		"config/secrets.txt":             {Data: []byte("do-not-serve")},
		"templates/private.key":          {Data: []byte("do-not-serve")},
	}
}

func withResources(fsys fstest.MapFS) func(*Config) {
	return func(c *Config) { c.Resources = fsys }
}

func assetURL(id, p string) string { return "/api/v1/drafts/" + id + "/assets/" + p }

func TestAssetEndpoints(t *testing.T) {
	e := newEnv(t, withResources(diskResources()))
	id, v := e.newDraft(tokEditor, "dir")

	// A fresh draft carries no assets.
	list := e.ok(200, "GET", "/api/v1/drafts/"+id+"/assets", tokEditor, nil)
	if got := list["assets"].([]any); len(got) != 0 || ver(list) != v {
		t.Fatalf("assets = %v", list)
	}

	// Add: the version bumps and the change names the asset.
	put := e.ok(200, "PUT", assetURL(id, pageA), tokEditor, obj{"content": "<h1>hi ${title}</h1>", "ifVersion": v})
	if ver(put) != v+1 || strs(put["changed"])[0] != pageA {
		t.Fatalf("put = %v", put)
	}
	list = e.ok(200, "GET", "/api/v1/drafts/"+id+"/assets", tokEditor, nil)
	item := list["assets"].([]any)[0].(obj)
	if item["path"] != pageA || item["kind"] != "template" || item["status"] != "added" || item["overridesDisk"] != false {
		t.Fatalf("item = %v", item)
	}
	got := e.ok(200, "GET", assetURL(id, pageA), tokEditor, nil)
	if got["content"] != "<h1>hi ${title}</h1>" || got["source"] != "draft" {
		t.Fatalf("get = %v", got)
	}
	// The draft summary lists it, and files stays BCL-only.
	d := e.ok(200, "GET", "/api/v1/drafts/"+id, tokEditor, nil)
	if a := strs(d["assets"]); len(a) != 1 || a[0] != pageA || len(strs(d["files"])) != 2 || d["dirty"] != true {
		t.Fatalf("draft = %v", d)
	}

	// A stale ifVersion is a 409, and changes nothing.
	if c := e.fail(409, "PUT", assetURL(id, pageA), tokEditor, obj{"content": "x", "ifVersion": v}); c != "stale" {
		t.Fatal(c)
	}

	// Replace.
	put = e.ok(200, "PUT", assetURL(id, pageA), tokEditor, obj{"content": "<h1>v2</h1>", "ifVersion": ver(put)})
	if ver(put) != v+2 {
		t.Fatalf("replace version = %d", ver(put))
	}
	// Replacing with the same content is not a change.
	same := e.ok(200, "PUT", assetURL(id, pageA), tokEditor, obj{"content": "<h1>v2</h1>"})
	if ver(same) != v+2 || len(same["changed"].([]any)) != 0 {
		t.Fatalf("same = %v", same)
	}

	// A syntax error is refused unless forced.
	bad := "<p>${ok}</p>\n@if(x) {\n  never closed"
	before := e.ok(200, "GET", "/api/v1/drafts/"+id, tokEditor, nil)
	if c := e.fail(422, "PUT", assetURL(id, pageA), tokEditor, obj{"content": bad}); c != "invalid_template" {
		t.Fatal(c)
	}
	if ver(e.ok(200, "GET", "/api/v1/drafts/"+id, tokEditor, nil)) != ver(before) {
		t.Fatal("a refused save changed the draft")
	}
	forced := e.ok(200, "PUT", assetURL(id, pageA)+"?force=1", tokEditor, obj{"content": bad})
	if ver(forced) != v+3 || len(forced["template"].([]any)) == 0 {
		t.Fatalf("forced = %v", forced)
	}
	var syntax bool
	for _, d := range forced["diagnostics"].([]any) {
		dm := d.(obj)
		if dm["code"] == "studio.pages.syntax" && dm["file"] == pageA && dm["severity"] == "error" {
			syntax = true
		}
	}
	if !syntax {
		t.Fatalf("no syntax diagnostic in %v", forced["diagnostics"])
	}

	// Undo and redo walk the asset edits: one step each.
	undone := e.ok(200, "POST", "/api/v1/drafts/"+id+"/undo", tokEditor, nil)
	if ver(undone) != v+4 {
		t.Fatalf("undo = %v", undone)
	}
	if got := e.ok(200, "GET", assetURL(id, pageA), tokEditor, nil)["content"]; got != "<h1>v2</h1>" {
		t.Fatalf("after undo: %v", got)
	}
	e.ok(200, "POST", "/api/v1/drafts/"+id+"/redo", tokEditor, nil)
	e.ok(200, "POST", "/api/v1/drafts/"+id+"/undo", tokEditor, nil)

	// Rename.
	cur := ver(e.ok(200, "GET", "/api/v1/drafts/"+id, tokEditor, nil))
	ren := e.ok(200, "POST", "/api/v1/drafts/"+id+"/assets/rename", tokEditor,
		obj{"from": pageA, "to": "templates/pages/greeting.html", "ifVersion": cur})
	if ver(ren) != cur+1 {
		t.Fatalf("rename = %v", ren)
	}
	e.fail(404, "GET", assetURL(id, pageA), tokEditor, nil)
	e.ok(200, "GET", assetURL(id, "templates/pages/greeting.html"), tokEditor, nil)
	if c := e.fail(422, "POST", "/api/v1/drafts/"+id+"/assets/rename", tokEditor,
		obj{"from": "templates/pages/none.html", "to": "templates/pages/x.html"}); c != "op_failed" {
		t.Fatal(c)
	}

	// Delete.
	cur = ver(e.ok(200, "GET", "/api/v1/drafts/"+id, tokEditor, nil))
	del := e.ok(200, "DELETE", assetURL(id, "templates/pages/greeting.html")+"?ifVersion="+strconv.FormatInt(cur, 10), tokEditor, nil)
	if ver(del) != cur+1 {
		t.Fatalf("delete = %v", del)
	}
	if c := e.fail(422, "DELETE", assetURL(id, "templates/pages/greeting.html"), tokEditor, nil); c != "op_failed" {
		t.Fatal(c)
	}

	// Paths and roles.
	if c := e.fail(422, "PUT", assetURL(id, "templates/pages/x.exe"), tokEditor, obj{"content": "x"}); c != "invalid_assets" {
		t.Fatal(c)
	}
	if c := e.fail(422, "PUT", assetURL(id, "elsewhere/x.html"), tokEditor, obj{"content": "x"}); c != "invalid_assets" {
		t.Fatal(c)
	}
	e.fail(400, "PUT", assetURL(id, "noslash"), tokEditor, obj{"content": "x"})
	e.fail(403, "PUT", assetURL(id, pageA), tokViewer, obj{"content": "x"})
	e.fail(403, "PUT", assetURL(id, pageA), tokEditor2, obj{"content": "x"}) // not their draft
}

func TestAssetsInABatchShareOneUndoStep(t *testing.T) {
	e := newEnv(t, withResources(diskResources()))
	id, v := e.newDraft(tokEditor, "dir")
	out := e.ops(id, tokEditor, v,
		obj{"op": "setField", "file": "01_routes.bcl", "path": "route/health.ping/path", "value": `"/moved"`},
		obj{"op": "putAsset", "file": pageA, "content": "<p>moved</p>"},
		obj{"op": "putAsset", "file": "static/css/extra.css", "content": "a{}"})
	if ver(out) != v+1 || len(strs(out["changed"])) != 3 {
		t.Fatalf("batch = %v", out)
	}
	// Atomic: a bad asset path fails the whole batch.
	if c := e.fail(422, "POST", "/api/v1/drafts/"+id+"/ops", tokEditor, obj{"ops": []obj{
		{"op": "setField", "file": "01_routes.bcl", "path": "route/health.ping/path", "value": `"/other"`},
		{"op": "putAsset", "file": "templates/x.exe", "content": "x"}}}); c != "invalid_assets" {
		t.Fatal(c)
	}
	if !strings.Contains(e.file(id, "01_routes.bcl", tokEditor), `"/moved"`) {
		t.Fatal("part of a failed batch was applied")
	}
	// One undo reverts the route and both assets.
	e.ok(200, "POST", "/api/v1/drafts/"+id+"/undo", tokEditor, nil)
	if strings.Contains(e.file(id, "01_routes.bcl", tokEditor), `"/moved"`) {
		t.Fatal("undo left the route edit")
	}
	if list := e.ok(200, "GET", "/api/v1/drafts/"+id+"/assets", tokEditor, nil)["assets"].([]any); len(list) != 0 {
		t.Fatalf("undo left assets: %v", list)
	}
	e.ok(200, "POST", "/api/v1/drafts/"+id+"/redo", tokEditor, nil)
	if list := e.ok(200, "GET", "/api/v1/drafts/"+id+"/assets", tokEditor, nil)["assets"].([]any); len(list) != 2 {
		t.Fatalf("redo lost assets: %v", list)
	}
	// The diff lists the assets next to the BCL file.
	d := e.ok(200, "GET", "/api/v1/drafts/"+id+"/diff", tokEditor, nil)
	var paths []string
	for _, f := range d["files"].([]any) {
		paths = append(paths, f.(obj)["path"].(string)+":"+f.(obj)["status"].(string))
	}
	want := "01_routes.bcl:modified static/css/extra.css:added " + pageA + ":added"
	if strings.Join(paths, " ") != want {
		t.Fatalf("diff = %v, want %s", paths, want)
	}
}

func TestImportFromDisk(t *testing.T) {
	e := newEnv(t, withResources(diskResources()))
	id, v := e.newDraft(tokEditor, "dir")

	// Only templates/ and static/ files with an asset extension are readable
	// from disk; the rest of the resources directory is not reachable.
	for _, p := range []string{"config/secrets.txt", "templates/private.key", "templates/pages/none.html"} {
		e.fail(404, "GET", assetURL(id, p), tokEditor, nil)
	}
	// A bare path is a template name under templates/, so it cannot reach config/.
	e.fail(404, "POST", "/api/v1/drafts/"+id+"/assets/import-from-disk", tokEditor, obj{"paths": []string{"config/secrets.txt"}})
	e.fail(400, "POST", "/api/v1/drafts/"+id+"/assets/import-from-disk", tokEditor, obj{"paths": []string{"templates/private.key"}})

	// A file not overridden reads from disk.
	disk := e.ok(200, "GET", assetURL(id, "templates/pages/list.html"), tokEditor, nil)
	if disk["source"] != "disk" || !strings.Contains(disk["content"].(string), "extends") {
		t.Fatalf("disk read = %v", disk)
	}
	out := e.ok(200, "POST", "/api/v1/drafts/"+id+"/assets/import-from-disk", tokEditor,
		obj{"paths": []string{"pages/list", "templates/components/card.html", "static/css/app.css"}, "ifVersion": v})
	if ver(out) != v+1 || len(strs(out["imported"])) != 3 {
		t.Fatalf("import = %v", out)
	}
	list := e.ok(200, "GET", "/api/v1/drafts/"+id+"/assets", tokEditor, nil)["assets"].([]any)
	kinds := map[string]string{}
	for _, it := range list {
		m := it.(obj)
		kinds[m["path"].(string)] = m["kind"].(string)
		if m["overridesDisk"] != true || m["status"] != "added" {
			t.Fatalf("item = %v", m)
		}
	}
	if kinds["templates/pages/list.html"] != "template" || kinds["templates/components/card.html"] != "component" || kinds["static/css/app.css"] != "static" {
		t.Fatalf("kinds = %v", kinds)
	}
	// Importing again skips; a path that is not on disk is a 404 and imports nothing.
	again := e.ok(200, "POST", "/api/v1/drafts/"+id+"/assets/import-from-disk", tokEditor, obj{"paths": []string{"pages/list"}})
	if len(strs(again["imported"])) != 0 || len(strs(again["skipped"])) != 1 || ver(again) != v+1 {
		t.Fatalf("again = %v", again)
	}
	before := ver(e.ok(200, "GET", "/api/v1/drafts/"+id, tokEditor, nil))
	e.fail(404, "POST", "/api/v1/drafts/"+id+"/assets/import-from-disk", tokEditor, obj{"paths": []string{"pages/none", "layouts/base"}})
	if ver(e.ok(200, "GET", "/api/v1/drafts/"+id, tokEditor, nil)) != before {
		t.Fatal("a failed import changed the draft")
	}
	e.fail(400, "POST", "/api/v1/drafts/"+id+"/assets/import-from-disk", tokEditor, obj{"paths": []string{"../secret"}})
	// Without a resources directory there is nothing to import from.
	e2 := newEnv(t)
	id2, _ := e2.newDraft(tokEditor, "dir")
	if c := e2.fail(422, "POST", "/api/v1/drafts/"+id2+"/assets/import-from-disk", tokEditor, obj{"paths": []string{"pages/list"}}); c != "no_resources" {
		t.Fatal(c)
	}
}

// pagesBCL routes to the starter's real templates: one with an intent that
// says nothing about the page's data, one to a template that is not there.
const pagesBCL = `intent "todo.list" {
  response "out"
  node "out" { uses "collect" requires [input] provides [out] config { unwrap true } }
}

route "web.todos_list" {
  method GET
  path "/todos"
  intent "todo.list"
  template "pages/todos/list"
  layout "layouts/base"
}

route "web.nowhere" {
  method GET
  path "/nowhere"
  intent "todo.list"
  template "pages/nope"
  layout "layouts/nope"
}

route_group "grouped" {
  prefix "/g"
  route "web.new" {
    method GET
    path "/new"
    template "pages/todos/new"
    layout "layouts/base"
  }
}
`

func starterEnv(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	for name, content := range map[string]string{"00_app.bcl": appBCL, "01_pages.bcl": pagesBCL} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return newEnvDir(t, dir, func(c *Config) { c.ResourcesDir = "../../examples/starter/resources" })
}

func TestTemplateCatalogOverStarterTemplates(t *testing.T) {
	e := starterEnv(t)
	id, _ := e.newDraft(tokEditor, "dir")
	cat := e.ok(200, "GET", "/api/v1/drafts/"+id+"/templates", tokEditor, nil)
	byName := map[string]obj{}
	for _, tm := range cat["templates"].([]any) {
		byName[tm.(obj)["name"].(string)] = tm.(obj)
	}
	if len(byName) < 15 {
		t.Fatalf("only %d templates", len(byName))
	}

	list := byName["pages/todos/list"]
	if list == nil {
		t.Fatalf("no todo list page in %v", keys(byName))
	}
	if list["kind"] != "page" || list["source"] != "disk" || list["extends"] != "layouts/base" || list["unused"] != false {
		t.Fatalf("list = %v", list)
	}
	if !contains(strs(list["vars"]), "todos") || !contains(strs(list["layouts"]), "layouts/base") || !contains(strs(list["includes"]), "components/todos/row") {
		t.Fatalf("list details = %v", list)
	}
	routes := list["routes"].([]any)
	if len(routes) != 1 {
		t.Fatalf("routes = %v", routes)
	}
	r := routes[0].(obj)
	if r["route"] != "web.todos_list" || r["file"] != "01_pages.bcl" || r["path"] != "route/web.todos_list" || r["method"] != "GET" ||
		r["url"] != "/todos" || r["as"] != "template" || r["line"].(float64) == 0 {
		t.Fatalf("route ref = %v", r)
	}
	// A route in a route_group counts, with the group's prefix.
	nw := byName["pages/todos/new"]["routes"].([]any)
	if len(nw) != 1 || nw[0].(obj)["url"] != "/g/new" {
		t.Fatalf("grouped route = %v", nw)
	}
	// The layout is used by the routes as a layout.
	base := byName["layouts/base"]
	if base["kind"] != "layout" || len(base["routes"].([]any)) < 2 || base["routes"].([]any)[0].(obj)["as"] != "layout" {
		t.Fatalf("base = %v", base)
	}
	if byName["components/alert"]["kind"] != "component" || byName["components/alert"]["unused"] != false {
		t.Fatalf("alert = %v", byName["components/alert"])
	}
	// A page nothing routes to is unused.
	if byName["pages/auth/login"]["unused"] != true {
		t.Fatalf("login = %v", byName["pages/auth/login"])
	}
	// References to templates that are not there.
	var missing []string
	for _, m := range cat["missing"].([]any) {
		missing = append(missing, m.(obj)["route"].(string)+":"+m.(obj)["as"].(string)+":"+m.(obj)["template"].(string))
	}
	if strings.Join(missing, " ") != "web.nowhere:template:pages/nope web.nowhere:layout:layouts/nope" {
		t.Fatalf("missing = %v", missing)
	}
	if !contains(strs(cat["globals"]), "appName") {
		t.Fatalf("globals = %v", cat["globals"])
	}

	// One template by name, and sample data for it.
	one := e.ok(200, "GET", "/api/v1/drafts/"+id+"/templates/pages/todos/list", tokEditor, nil)
	if one["name"] != "pages/todos/list" {
		t.Fatalf("one = %v", one)
	}
	pd := e.ok(200, "GET", "/api/v1/drafts/"+id+"/templates/pages/todos/list/preview-data", tokEditor, nil)
	data := pd["data"].(obj)
	if pd["guessed"] != true || data["todos"] == nil {
		t.Fatalf("preview-data = %v", pd)
	}
	if items, ok := data["todos"].([]any); !ok || len(items) == 0 {
		t.Fatalf("todos sample = %v", data["todos"])
	}
	e.fail(404, "GET", "/api/v1/drafts/"+id+"/templates/pages/none/preview-data", tokEditor, nil)

	// The draft's own override replaces the disk file in the catalog.
	e.ok(200, "PUT", assetURL(id, "templates/pages/todos/list.html"), tokEditor, obj{"content": "<h1>${headline}</h1>"})
	over := e.ok(200, "GET", "/api/v1/drafts/"+id+"/templates/pages/todos/list", tokEditor, nil)
	if over["source"] != "override" || !contains(strs(over["vars"]), "headline") || contains(strs(over["vars"]), "todos") {
		t.Fatalf("override = %v", over)
	}
}

func TestRouteTemplateLinkageWarnings(t *testing.T) {
	e := starterEnv(t)
	id, _ := e.newDraft(tokEditor, "dir")
	val := e.ok(200, "POST", "/api/v1/drafts/"+id+"/validate", tokEditor, nil)
	byCode := map[string][]obj{}
	for _, d := range val["diagnostics"].([]any) {
		m := d.(obj)
		if c, _ := m["code"].(string); strings.HasPrefix(c, "studio.pages.") {
			if m["severity"] != "warning" {
				t.Errorf("%s is %v, want a warning", c, m["severity"])
			}
			byCode[c] = append(byCode[c], m)
		}
	}
	tm := byCode["studio.pages.template_missing"]
	if len(tm) != 1 || tm[0]["file"] != "01_pages.bcl" || tm[0]["path"] != "route/web.nowhere/template" || !strings.Contains(tm[0]["message"].(string), "pages/nope") || tm[0]["line"].(float64) == 0 {
		t.Fatalf("template_missing = %v", tm)
	}
	lm := byCode["studio.pages.layout_missing"]
	if len(lm) != 1 || lm[0]["path"] != "route/web.nowhere/layout" {
		t.Fatalf("layout_missing = %v", lm)
	}
	// The todo list reads `todos`, which the intent never mentions; appName
	// and title come from the host and are not asked of the intent.
	vu := byCode["studio.pages.vars_unprovided"]
	if len(vu) != 1 || vu[0]["path"] != "route/web.todos_list/intent" {
		t.Fatalf("vars_unprovided = %v", vu)
	}
	msg := vu[0]["message"].(string)
	if !strings.Contains(msg, "todos") || strings.Contains(msg, "appName") || strings.Contains(msg, "title") {
		t.Fatalf("message = %s", msg)
	}
	// The draft's diagnostics carry the same findings; an intent that does
	// mention the variable clears the warning.
	v := int64(val["version"].(float64))
	fixed := e.ops(id, tokEditor, v, obj{"op": "setField", "file": "01_pages.bcl", "path": "intent/todo.list/response", "value": `"todos"`})
	for _, d := range fixed["diagnostics"].([]any) {
		if d.(obj)["code"] == "studio.pages.vars_unprovided" && d.(obj)["path"] == "route/web.todos_list/intent" {
			t.Fatalf("the warning stayed after the intent named todos: %v", d)
		}
	}
	// Creating the missing template clears template_missing.
	out := e.ok(200, "PUT", assetURL(id, "templates/pages/nope.html"), tokEditor, obj{"content": "<p>${x}</p>"})
	for _, d := range out["diagnostics"].([]any) {
		if d.(obj)["code"] == "studio.pages.template_missing" {
			t.Fatalf("still missing: %v", d)
		}
	}
}

func TestProposeWithAssetsThenApproveAndActivate(t *testing.T) {
	e := newEnv(t, withResources(diskResources()))
	id, v := e.newDraft(tokEditor, "active")
	e.ops(id, tokEditor, v,
		obj{"op": "putAsset", "file": "templates/pages/list.html", "content": "<h1>override</h1>"},
		obj{"op": "putAsset", "file": "static/css/extra.css", "content": "a{}"})
	rev := e.ok(201, "POST", "/api/v1/drafts/"+id+"/propose", tokEditor, obj{"message": "with assets"})
	rid := rev["id"].(string)
	if len(rev["assets"].([]any)) != 2 || rev["status"] != "pending" {
		t.Fatalf("revision = %v", rev)
	}

	// The summary and detail carry the assets and what changed.
	sum := e.ok(200, "GET", "/api/v1/revisions/"+rid, tokViewer, nil)
	if len(sum["assets"].([]any)) != 2 || len(sum["changed_assets"].([]any)) != 2 {
		t.Fatalf("detail = %v", sum)
	}
	status, raw := e.call("GET", "/api/v1/revisions", tokViewer, nil)
	if status != 200 || !strings.Contains(string(raw), `"changed_assets"`) || !strings.Contains(string(raw), `"assets":["static/css/extra.css","templates/pages/list.html"]`) {
		t.Fatalf("list = %d %s", status, raw)
	}
	// Its diff against the active revision lists the assets with unified diffs.
	d := e.ok(200, "GET", "/api/v1/revisions/"+rid+"/diff?against=active", tokViewer, nil)
	var seen []string
	for _, f := range d["files"].([]any) {
		m := f.(obj)
		seen = append(seen, m["path"].(string)+":"+m["status"].(string))
		if !strings.Contains(m["unified"].(string), "+") {
			t.Fatalf("no unified diff for %v", m)
		}
	}
	if strings.Join(seen, " ") != "static/css/extra.css:added templates/pages/list.html:added" {
		t.Fatalf("revision diff = %v", seen)
	}

	// The author cannot approve; a reviewer can; then it activates.
	e.fail(403, "POST", "/api/v1/revisions/"+rid+"/approve", tokEditor, obj{})
	e.ok(200, "POST", "/api/v1/revisions/"+rid+"/approve", tokReviewer, obj{"comment": "ok"})
	act := e.ok(200, "POST", "/api/v1/revisions/"+rid+"/activate", tokReviewer, nil)
	if act["status"] != "active" || e.activated.Load() != 1 {
		t.Fatalf("activate = %v (%d activations)", act, e.activated.Load())
	}

	// What a supervisor would build from: the active revision verifies, and
	// its assets overlay the host's files.
	m := e.mgr
	active, err := m.Active(context.Background())
	if err != nil || active.ID != rid {
		t.Fatalf("active = %v %v", active, err)
	}
	if err := m.Verify(active); err != nil {
		t.Fatalf("verify: %v", err)
	}
	over := active.AssetsFS(diskResources())
	b, err := fs.ReadFile(over, "templates/pages/list.html")
	if err != nil || string(b) != "<h1>override</h1>" {
		t.Fatalf("overlay = %q %v", b, err)
	}
	if b, _ := fs.ReadFile(over, "templates/layouts/base.html"); !strings.Contains(string(b), "block") {
		t.Fatalf("disk file lost from the overlay: %q", b)
	}

	// A draft started from the active revision starts with its assets, clean.
	id2, _ := e.newDraft(tokEditor, "active")
	d2 := e.ok(200, "GET", "/api/v1/drafts/"+id2, tokEditor, nil)
	if len(strs(d2["assets"])) != 2 || d2["dirty"] != false {
		t.Fatalf("draft from the active revision = %v", d2)
	}
	// Proposing without changing anything is rejected by the manager, and a
	// draft with no assets still uses the plain path.
	id3, v3 := e.newDraft(tokEditor, "dir")
	e.ops(id3, tokEditor, v3, obj{"op": "setField", "file": "01_routes.bcl", "path": "route/health.ping/path", "value": `"/p"`})
	plain := e.ok(201, "POST", "/api/v1/drafts/"+id3+"/propose", tokEditor, obj{"message": "plain"})
	if plain["assets"] != nil {
		t.Fatalf("plain revision has assets: %v", plain["assets"])
	}
}

func TestAssetsSurviveARestart(t *testing.T) {
	mut, path := withSQL(t)
	e1 := newEnv(t, mut, withResources(diskResources()))
	dir := e1.dir
	id, v := e1.newDraft(tokEditor, "active")
	one := e1.ok(200, "PUT", assetURL(id, pageA), tokEditor, obj{"content": "<p>one</p>", "ifVersion": v})
	two := e1.ok(200, "PUT", assetURL(id, pageA), tokEditor, obj{"content": "<p>two</p>", "ifVersion": ver(one)})
	e1.srv.Close()

	e2 := newEnvDir(t, dir, sqlMutator(t, path), withResources(diskResources()))
	got := e2.ok(200, "GET", assetURL(id, pageA), tokEditor, nil)
	if got["content"] != "<p>two</p>" || ver(got) != ver(two) {
		t.Fatalf("after the restart: %v", got)
	}
	d := e2.ok(200, "GET", "/api/v1/drafts/"+id, tokEditor, nil)
	if a := strs(d["assets"]); len(a) != 1 || d["dirty"] != true {
		t.Fatalf("draft = %v", d)
	}
	// Undo history includes the asset edits.
	e2.ok(200, "POST", "/api/v1/drafts/"+id+"/undo", tokEditor, nil)
	if got := e2.ok(200, "GET", assetURL(id, pageA), tokEditor, nil)["content"]; got != "<p>one</p>" {
		t.Fatalf("undo after the restart: %v", got)
	}
	e2.ok(200, "POST", "/api/v1/drafts/"+id+"/undo", tokEditor, nil)
	e2.fail(404, "GET", assetURL(id, pageA), tokEditor, nil)
}

// TestPreviewRebuildsWhenAnAssetChanges: an asset edit bumps the version, so
// the preview builds a new generation that serves the new template.
func TestPreviewRebuildsWhenAnAssetChanges(t *testing.T) {
	mut := func(c *Config) {
		withPreview(t, "")(c)
		c.PreviewOptions.NewAppFor = func(preview.BuildEnv) *fh.App { return fh.New(fh.WithStartupBannerDisabled(true)) }
		c.PreviewOptions.MountFor = func(app *fh.App, p *platform.Platform, env preview.BuildEnv) error {
			app.Get("/page", func(cx fh.Ctx) error {
				body, ok := env.Assets.Get("templates/pages/hello.html")
				if !ok {
					return cx.Status(404).SendString("no template")
				}
				cx.Set("Content-Type", "text/html")
				return cx.SendString(body)
			})
			return p.Mount(app)
		}
	}
	e := newEnv(t, mut)
	t.Cleanup(func() { e.srv.Close() })
	id, v := e.newDraft(tokEditor, "active")
	br := newBrowser(t, e.ts.URL)

	if status, st := br.ensure(id); status != 200 || st.Status != "ready" || st.Version != v {
		t.Fatalf("ensure = %d %+v", status, st)
	}
	if code, _, _ := br.do("GET", "/preview/"+id+"/page", "", nil); code != 404 {
		t.Fatalf("a draft without the template: %d", code)
	}

	put := e.ok(200, "PUT", assetURL(id, pageA), tokEditor, obj{"content": "<h1>first</h1>", "ifVersion": v})
	if status, st := br.ensure(id); status != 200 || st.Status != "ready" || st.Version != ver(put) {
		t.Fatalf("rebuild = %d %+v (want version %d)", status, st, ver(put))
	}
	if code, _, body := br.do("GET", "/preview/"+id+"/page", "", nil); code != 200 || !strings.Contains(body, "<h1>first</h1>") {
		t.Fatalf("first: %d %q", code, body)
	}

	put = e.ok(200, "PUT", assetURL(id, pageA), tokEditor, obj{"content": "<h1>second</h1>"})
	if _, st := br.ensure(id); st.Version != ver(put) || st.Status != "ready" {
		t.Fatalf("second rebuild = %+v", st)
	}
	if code, _, body := br.do("GET", "/preview/"+id+"/page", "", nil); code != 200 || !strings.Contains(body, "<h1>second</h1>") {
		t.Fatalf("second: %d %q", code, body)
	}
	// Undo goes back to the first template, and a rebuild shows it.
	undone := e.ok(200, "POST", "/api/v1/drafts/"+id+"/undo", tokEditor, nil)
	if _, st := br.ensure(id); st.Version != ver(undone) {
		t.Fatalf("after undo = %+v", st)
	}
	if _, _, body := br.do("GET", "/preview/"+id+"/page", "", nil); !strings.Contains(body, "<h1>first</h1>") {
		t.Fatalf("after undo: %q", body)
	}
}

func keys(m map[string]obj) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
