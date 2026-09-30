package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/oarkflow/ref/deploy"
	"github.com/oarkflow/ref/platform"
	"github.com/oarkflow/ref/studio"
)

const (
	tokAdmin    = "admin-token-0123456789"
	tokReviewer = "reviewer-token-0123456"
	tokEditor   = "editor-token-01234567"
	tokEditor2  = "editor2-token-0123456"
	tokViewer   = "viewer-token-01234567"
)

const appBCL = "name \"demo\"\nversion \"1\"\n"

const routesBCL = `# The health check.
intent "health.ping" {
  response "pong"
  node "pong" { uses "collect" requires [input] provides [pong] config { unwrap true } }
}

# Public route.
route "health.ping" {
  method GET
  path "/health"
  intent "health.ping"
}
`

type env struct {
	t         *testing.T
	srv       *Server
	ts        *httptest.Server
	mgr       *deploy.Manager
	dir       string
	activated atomic.Int32
}

func newEnv(t *testing.T, mut ...func(*Config)) *env {
	t.Helper()
	dir := t.TempDir()
	for name, content := range map[string]string{"00_app.bcl": appBCL, "01_routes.bcl": routesBCL} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return newEnvDir(t, dir, mut...)
}

func newEnvDir(t *testing.T, dir string, mut ...func(*Config)) *env {
	t.Helper()
	opts := platform.DefaultLoadOptions()
	e := &env{t: t, dir: dir}
	e.mgr = &deploy.Manager{
		Store: deploy.NewMemoryStore(), App: "demo", Approvals: 1,
		Validate: func(ctx context.Context, src []byte) platform.ValidationReport {
			return platform.Validate(ctx, src, dir, opts)
		},
		ValidateBundle: func(ctx context.Context, b platform.Bundle) platform.ValidationReport {
			return platform.ValidateBundle(ctx, b, dir, opts)
		},
	}
	files, err := platform.ReadBundleDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	rev, err := e.mgr.ProposeBundle(ctx, files, "bootstrap", "initial")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.mgr.Approve(ctx, rev.ID, "system", ""); err != nil {
		t.Fatal(err)
	}
	if _, err = e.mgr.Activate(ctx, rev.ID, "system"); err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		App: "demo", Manager: e.mgr, ConfigDir: dir, LoadOptions: opts,
		Tokens: map[string]studio.Identity{
			tokAdmin:    {Name: "admin", Roles: []string{"admin"}},
			tokReviewer: {Name: "reviewer", Roles: []string{"reviewer"}},
			tokEditor:   {Name: "editor", Roles: []string{"editor"}},
			tokEditor2:  {Name: "editor2", Roles: []string{"editor"}},
			tokViewer:   {Name: "viewer", Roles: []string{"viewer"}},
		},
		OnActivate: func(*deploy.Revision) { e.activated.Add(1) },
		Web:        fstest.MapFS{"index.html": {Data: []byte("<html>studio</html>")}},
	}
	for _, m := range mut {
		m(&cfg)
	}
	e.srv, err = New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	e.ts = httptest.NewServer(e.srv)
	t.Cleanup(e.ts.Close)
	return e
}

// call sends a request and returns the status and body.
func (e *env) call(method, path, token string, body any) (int, []byte) {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			e.t.Fatal(err)
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, e.ts.URL+path, rd)
	if err != nil {
		e.t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

type obj = map[string]any

// ok calls and requires status want, decoding a JSON object response.
func (e *env) ok(want int, method, path, token string, body any) obj {
	e.t.Helper()
	status, raw := e.call(method, path, token, body)
	if status != want {
		e.t.Fatalf("%s %s: status %d, want %d: %s", method, path, status, want, raw)
	}
	var out obj
	if len(raw) > 0 && raw[0] == '{' {
		if err := json.Unmarshal(raw, &out); err != nil {
			e.t.Fatalf("%s %s: bad JSON: %v: %s", method, path, err, raw)
		}
	}
	return out
}

// fail calls, requires status want and returns the error code.
func (e *env) fail(want int, method, path, token string, body any) string {
	e.t.Helper()
	out := e.ok(want, method, path, token, body)
	er, _ := out["error"].(obj)
	code, _ := er["code"].(string)
	if code == "" {
		e.t.Fatalf("%s %s: no error code in %v", method, path, out)
	}
	return code
}

func (e *env) newDraft(token, from string) (id string, version int64) {
	e.t.Helper()
	out := e.ok(201, "POST", "/api/v1/drafts", token, obj{"from": from})
	return out["id"].(string), int64(out["version"].(float64))
}

func (e *env) file(id, name, token string) string {
	e.t.Helper()
	out := e.ok(200, "GET", "/api/v1/drafts/"+id+"/files/"+name, token, nil)
	return out["content"].(string)
}

func (e *env) ops(id, token string, version int64, ops ...obj) obj {
	e.t.Helper()
	return e.ok(200, "POST", "/api/v1/drafts/"+id+"/ops", token, obj{"ops": ops, "ifVersion": version})
}

func ver(o obj) int64 { return int64(o["version"].(float64)) }

func strs(v any) []string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}

func TestMetaSchemaCatalog(t *testing.T) {
	e := newEnv(t)
	meta := e.ok(200, "GET", "/api/v1/meta", tokViewer, nil)
	if meta["app"] != "demo" || meta["version"] != "v1" {
		t.Fatalf("meta = %v", meta)
	}
	if meta["identity"].(obj)["name"] != "viewer" {
		t.Fatalf("identity = %v", meta["identity"])
	}
	if meta["features"].(obj)["preview"] != false {
		t.Fatalf("features = %v", meta["features"])
	}
	if meta["activeRevision"] == nil {
		t.Fatal("no active revision in meta")
	}
	blocks := e.ok(200, "GET", "/api/v1/schema/blocks", tokViewer, nil)
	route, ok := blocks["route"].(obj)
	if !ok || route["fields"] == nil {
		t.Fatalf("route schema missing: %v", blocks["route"])
	}
	cat := e.ok(200, "GET", "/api/v1/schema/catalog", tokViewer, nil)
	if len(cat["resource_kinds"].([]any)) == 0 || len(cat["node_types"].([]any)) == 0 {
		t.Fatalf("catalog is empty: %v", cat)
	}
}

func TestSchemaBlocksGetDocs(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.DocSourceDirs = []string{"../../platform", "../../pipeline"} })
	blocks := e.ok(200, "GET", "/api/v1/schema/blocks", tokViewer, nil)
	documented := 0
	for _, f := range blocks["route"].(obj)["fields"].([]any) {
		if f.(obj)["doc"] != nil {
			documented++
		}
	}
	if documented == 0 {
		t.Fatal("no route field has a doc string")
	}
}

func TestAuthAndRoles(t *testing.T) {
	e := newEnv(t)
	if c := e.fail(401, "GET", "/api/v1/meta", "", nil); c != "unauthorized" {
		t.Fatal(c)
	}
	e.fail(401, "GET", "/api/v1/meta", "wrong-token-0123456789", nil)
	// A viewer reads, but cannot edit.
	e.ok(200, "GET", "/api/v1/revisions", tokViewer, nil)
	e.fail(403, "POST", "/api/v1/drafts", tokViewer, obj{"from": "dir"})
	// An editor cannot review or administer.
	id, _ := e.newDraft(tokEditor, "dir")
	e.fail(403, "POST", "/api/v1/revisions/rev_x/approve", tokEditor, obj{})
	e.fail(403, "POST", "/api/v1/revisions/rev_x/activate", tokEditor, nil)
	e.fail(403, "POST", "/api/v1/rollback", tokReviewer, obj{})
	e.fail(403, "GET", "/api/v1/audit", tokEditor, nil)
	e.fail(403, "GET", "/api/v1/audit", tokReviewer, nil)
	// Drafts belong to their owner; an admin can see any.
	e.fail(403, "GET", "/api/v1/drafts/"+id, tokEditor2, nil)
	e.ok(200, "GET", "/api/v1/drafts/"+id, tokAdmin, nil)
	e.fail(404, "GET", "/api/v1/drafts/drf_nope", tokEditor, nil)
	list, _ := e.call("GET", "/api/v1/drafts", tokEditor2, nil)
	if list != 200 {
		t.Fatal(list)
	}
	// A bad token config is refused at startup.
	if _, err := New(Config{Tokens: map[string]studio.Identity{"short": {Name: "x", Roles: []string{"admin"}}}}); err == nil {
		t.Fatal("short token accepted")
	}
	if _, err := New(Config{Tokens: map[string]studio.Identity{tokAdmin: {Name: "x", Roles: []string{"root"}}}}); err == nil {
		t.Fatal("unknown role accepted")
	}
}

func TestCreateDraftFromDirTreeAndFiles(t *testing.T) {
	e := newEnv(t)
	draft := e.ok(201, "POST", "/api/v1/drafts", tokEditor, obj{"name": "mine", "from": "dir"})
	id := draft["id"].(string)
	if draft["name"] != "mine" || draft["owner"] != "editor" || draft["dirty"] != false {
		t.Fatalf("draft = %v", draft)
	}
	if got := strs(draft["files"]); len(got) != 2 || got[0] != "00_app.bcl" || got[1] != "01_routes.bcl" {
		t.Fatalf("files = %v", got)
	}
	full := e.ok(200, "GET", "/api/v1/drafts/"+id, tokEditor, nil)
	if full["diagnostics"] == nil {
		t.Fatalf("no diagnostics array: %v", full)
	}
	files, _ := e.call("GET", "/api/v1/drafts/"+id+"/files", tokEditor, nil)
	if files != 200 {
		t.Fatal(files)
	}
	if got := e.file(id, "01_routes.bcl", tokEditor); got != routesBCL {
		t.Fatalf("content differs:\n%s", got)
	}

	status, raw := e.call("GET", "/api/v1/drafts/"+id+"/files/01_routes.bcl/tree", tokEditor, nil)
	if status != 200 {
		t.Fatal(status, string(raw))
	}
	var tree []obj
	if err := json.Unmarshal(raw, &tree); err != nil {
		t.Fatal(err)
	}
	if len(tree) != 2 || tree[1]["kind"] != "block" || tree[1]["type"] != "route" || tree[1]["id"] != "health.ping" {
		t.Fatalf("tree = %v", tree)
	}
	kids := tree[1]["children"].([]any)
	first := kids[0].(obj)
	if first["kind"] != "field" || first["name"] != "method" || first["raw"] != "GET" || first["path"] != "route/health.ping/method" {
		t.Fatalf("first child = %v", first)
	}
	// The navigator tree lists top-level statements without children.
	nav := e.ok(200, "GET", "/api/v1/drafts/"+id+"/tree", tokEditor, nil)
	top := nav["01_routes.bcl"].([]any)
	if len(top) != 2 || top[1].(obj)["children"] != nil {
		t.Fatalf("nav = %v", nav)
	}
	if len(nav["00_app.bcl"].([]any)) != 2 {
		t.Fatalf("top-level fields missing: %v", nav["00_app.bcl"])
	}
	// The draft starts from the config dir, and so equals the active revision.
	e.fail(404, "GET", "/api/v1/drafts/"+id+"/files/nope.bcl", tokEditor, nil)
	e.ok(204, "DELETE", "/api/v1/drafts/"+id, tokEditor, nil)
	e.fail(404, "GET", "/api/v1/drafts/"+id, tokEditor, nil)
}

func TestCreateDraftFromActiveAndRevision(t *testing.T) {
	e := newEnv(t)
	d := e.ok(201, "POST", "/api/v1/drafts", tokEditor, obj{"from": "active"})
	if d["baseRevision"] == nil || d["baseRevision"] == "" {
		t.Fatalf("no base revision: %v", d)
	}
	e.ok(201, "POST", "/api/v1/drafts", tokEditor, obj{})
	e.ok(201, "POST", "/api/v1/drafts", tokEditor, obj{"from": "revision:" + d["baseRevision"].(string)})
	e.fail(404, "POST", "/api/v1/drafts", tokEditor, obj{"from": "revision:rev_nope"})
	e.fail(400, "POST", "/api/v1/drafts", tokEditor, obj{"from": "elsewhere"})
}

func TestOpsEachType(t *testing.T) {
	e := newEnv(t)
	id, v := e.newDraft(tokEditor, "dir")
	step := func(op obj) string {
		t.Helper()
		out := e.ops(id, tokEditor, v, op)
		if ver(out) <= v {
			t.Fatalf("version did not advance for %v: %v", op, out)
		}
		v = ver(out)
		if op["op"] == "renameFile" {
			return ""
		}
		return e.file(id, op["file"].(string), tokEditor)
	}
	routes := "01_routes.bcl"

	got := step(obj{"op": "setField", "file": routes, "path": "route/health.ping/path", "value": `"/ping"`})
	if !strings.Contains(got, `path "/ping"`) || !strings.Contains(got, "# The health check.") || !strings.Contains(got, "# Public route.") {
		t.Fatalf("setField:\n%s", got)
	}
	got = step(obj{"op": "addBlock", "file": routes, "type": "route", "id": "second",
		"body": "method GET\npath \"/two\"\nintent \"health.ping\""})
	if !strings.Contains(got, `route "second"`) {
		t.Fatalf("addBlock:\n%s", got)
	}
	got = step(obj{"op": "renameBlock", "file": routes, "path": "route/second", "newId": "third"})
	if !strings.Contains(got, `route "third"`) || strings.Contains(got, `"second"`) {
		t.Fatalf("renameBlock:\n%s", got)
	}
	got = step(obj{"op": "moveBlock", "file": routes, "path": "route/third", "index": 0})
	if strings.Index(got, `route "third"`) > strings.Index(got, `intent "health.ping"`) {
		t.Fatalf("moveBlock:\n%s", got)
	}
	got = step(obj{"op": "removeField", "file": routes, "path": "route/third/path"})
	if strings.Contains(got, `"/two"`) {
		t.Fatalf("removeField:\n%s", got)
	}
	got = step(obj{"op": "removeBlock", "file": routes, "path": "route/third"})
	if strings.Contains(got, `"third"`) {
		t.Fatalf("removeBlock:\n%s", got)
	}
	got = step(obj{"op": "addFile", "file": "02_extra.bcl", "content": "# extra\n"})
	if got != "# extra\n" {
		t.Fatalf("addFile: %q", got)
	}
	step(obj{"op": "renameFile", "file": "02_extra.bcl", "newFile": "03_extra.bcl"})
	e.fail(404, "GET", "/api/v1/drafts/"+id+"/files/02_extra.bcl", tokEditor, nil)
	if got := e.file(id, "03_extra.bcl", tokEditor); got != "# extra\n" {
		t.Fatalf("renameFile: %q", got)
	}
	out := e.ops(id, tokEditor, v, obj{"op": "removeFile", "file": "03_extra.bcl"})
	v = ver(out)
	e.fail(404, "GET", "/api/v1/drafts/"+id+"/files/03_extra.bcl", tokEditor, nil)
	if changed := strs(out["changed"]); len(changed) != 1 || changed[0] != "03_extra.bcl" {
		t.Fatalf("changed = %v", changed)
	}

	// Bundles are flat, and a draft keeps at least one file.
	if c := e.fail(422, "POST", "/api/v1/drafts/"+id+"/ops", tokEditor,
		obj{"ops": []obj{{"op": "addFile", "file": "sub/x.bcl"}}}); c != "op_failed" {
		t.Fatal(c)
	}
	e.fail(422, "POST", "/api/v1/drafts/"+id+"/ops", tokEditor, obj{"ops": []obj{{"op": "addFile", "file": "notbcl.txt"}}})
	e.fail(422, "POST", "/api/v1/drafts/"+id+"/ops", tokEditor, obj{"ops": []obj{{"op": "addFile", "file": "bad.bcl", "content": "route {{{"}}})
	e.ops(id, tokEditor, v, obj{"op": "removeFile", "file": "00_app.bcl"})
	e.fail(422, "POST", "/api/v1/drafts/"+id+"/ops", tokEditor, obj{"ops": []obj{{"op": "removeFile", "file": routes}}})
	e.fail(422, "POST", "/api/v1/drafts/"+id+"/ops", tokEditor, obj{"ops": []obj{{"op": "mystery", "file": routes}}})
	e.fail(400, "POST", "/api/v1/drafts/"+id+"/ops", tokEditor, obj{"ops": []obj{}})
}

func TestStaleVersionAndAtomicBatch(t *testing.T) {
	e := newEnv(t)
	id, v := e.newDraft(tokEditor, "dir")
	set := func(val string) obj {
		return obj{"op": "setField", "file": "01_routes.bcl", "path": "route/health.ping/path", "value": val}
	}
	out := e.ops(id, tokEditor, v, set(`"/one"`))
	// The old version is now stale.
	if c := e.fail(409, "POST", "/api/v1/drafts/"+id+"/ops", tokEditor, obj{"ops": []obj{set(`"/two"`)}, "ifVersion": v}); c != "stale" {
		t.Fatal(c)
	}
	v = ver(out)

	before := e.file(id, "01_routes.bcl", tokEditor)
	// The first op is fine, the second names a path that does not exist:
	// nothing is applied.
	status, raw := e.call("POST", "/api/v1/drafts/"+id+"/ops", tokEditor, obj{"ifVersion": v, "ops": []obj{
		set(`"/changed"`),
		{"op": "setField", "file": "01_routes.bcl", "path": "route/nope/path", "value": `"/x"`},
	}})
	if status != 422 {
		t.Fatalf("status %d: %s", status, raw)
	}
	var body obj
	_ = json.Unmarshal(raw, &body)
	er := body["error"].(obj)
	if er["code"] != "op_failed" || er["details"].(obj)["index"] != float64(1) {
		t.Fatalf("error = %v", er)
	}
	if after := e.file(id, "01_routes.bcl", tokEditor); after != before {
		t.Fatalf("a failed batch changed the draft:\n%s", after)
	}
	if now := e.ok(200, "GET", "/api/v1/drafts/"+id, tokEditor, nil); int64(now["version"].(float64)) != v {
		t.Fatalf("version moved to %v", now["version"])
	}
	// A dry run reports the outcome without committing.
	dry := e.ok(200, "POST", "/api/v1/drafts/"+id+"/ops", tokEditor, obj{"dryRun": true, "ifVersion": v, "ops": []obj{set(`"/dry"`)}})
	if ver(dry) != v || len(strs(dry["changed"])) != 1 {
		t.Fatalf("dry = %v", dry)
	}
	if e.file(id, "01_routes.bcl", tokEditor) != before {
		t.Fatal("a dry run changed the draft")
	}
	// A batch that changes nothing does not bump the version.
	same := e.ops(id, tokEditor, v, obj{"op": "setField", "file": "01_routes.bcl", "path": "route/health.ping/path", "value": `"/one"`})
	if ver(same) != v || len(strs(same["changed"])) != 0 {
		t.Fatalf("no-op = %v", same)
	}
}

func TestUndoRedoFormatAndPutFile(t *testing.T) {
	e := newEnv(t)
	id, v := e.newDraft(tokEditor, "dir")
	orig := e.file(id, "01_routes.bcl", tokEditor)
	e.fail(409, "POST", "/api/v1/drafts/"+id+"/undo", tokEditor, nil)
	e.fail(409, "POST", "/api/v1/drafts/"+id+"/redo", tokEditor, nil)

	out := e.ops(id, tokEditor, v,
		obj{"op": "setField", "file": "01_routes.bcl", "path": "route/health.ping/path", "value": `"/u"`},
		obj{"op": "addFile", "file": "02_new.bcl", "content": "# hi\n"})
	edited := e.file(id, "01_routes.bcl", tokEditor)
	if edited == orig {
		t.Fatal("edit did nothing")
	}
	undone := e.ok(200, "POST", "/api/v1/drafts/"+id+"/undo", tokEditor, nil)
	if ver(undone) <= ver(out) {
		t.Fatal("undo must move the version forward")
	}
	if got := e.file(id, "01_routes.bcl", tokEditor); got != orig {
		t.Fatalf("undo did not restore the bytes:\n%s", got)
	}
	e.fail(404, "GET", "/api/v1/drafts/"+id+"/files/02_new.bcl", tokEditor, nil) // the batch undoes as one step
	e.ok(200, "POST", "/api/v1/drafts/"+id+"/redo", tokEditor, nil)
	if got := e.file(id, "01_routes.bcl", tokEditor); got != edited {
		t.Fatalf("redo:\n%s", got)
	}
	if e.file(id, "02_new.bcl", tokEditor) != "# hi\n" {
		t.Fatal("redo lost the new file")
	}

	// Format only touches whitespace.
	cur := ver(e.ok(200, "GET", "/api/v1/drafts/"+id, tokEditor, nil))
	e.ok(200, "PUT", "/api/v1/drafts/"+id+"/files/02_new.bcl", tokEditor,
		obj{"content": "route   \"x\" {\n      method GET\n}\n# tail\n", "ifVersion": cur})
	f := e.ok(200, "POST", "/api/v1/drafts/"+id+"/format", tokEditor, obj{})
	if got := strs(f["changed"]); len(got) != 1 || got[0] != "02_new.bcl" {
		t.Fatalf("format changed %v", got)
	}
	if got := e.file(id, "02_new.bcl", tokEditor); !strings.Contains(got, "# tail") || strings.Contains(got, "route   ") {
		t.Fatalf("format:\n%s", got)
	}

	// Replacing a file needs valid BCL, and honours ifVersion.
	code := e.fail(422, "PUT", "/api/v1/drafts/"+id+"/files/02_new.bcl", tokEditor, obj{"content": "route \"x\" {\n"})
	if code != "invalid_bcl" {
		t.Fatal(code)
	}
	if c := e.fail(409, "PUT", "/api/v1/drafts/"+id+"/files/02_new.bcl", tokEditor, obj{"content": "# x\n", "ifVersion": 1}); c != "stale" {
		t.Fatal(c)
	}
	e.fail(404, "PUT", "/api/v1/drafts/"+id+"/files/missing.bcl", tokEditor, obj{"content": "# x\n"})
}

func TestValidateDiagnosticsNameFileAndLine(t *testing.T) {
	e := newEnv(t)
	id, v := e.newDraft(tokEditor, "dir")
	clean := e.ok(200, "POST", "/api/v1/drafts/"+id+"/validate", tokEditor, obj{})
	if clean["valid"] != true {
		t.Fatalf("the untouched config should validate: %v", clean)
	}
	out := e.ops(id, tokEditor, v, obj{"op": "setField", "file": "01_routes.bcl", "path": "route/health.ping/intent", "value": `"nope"`})
	diags, _ := out["diagnostics"].([]any)
	found := false
	for _, x := range diags {
		d := x.(obj)
		if d["severity"] == "error" && d["file"] == "01_routes.bcl" && d["line"] != nil && d["line"].(float64) > 0 {
			found = true
		}
	}
	if !found {
		t.Fatalf("no error diagnostic with file and line: %v", diags)
	}
	res := e.ok(200, "POST", "/api/v1/drafts/"+id+"/validate", tokEditor, obj{"ifVersion": ver(out)})
	if res["valid"] != false {
		t.Fatalf("validate = %v", res)
	}
	e.fail(409, "POST", "/api/v1/drafts/"+id+"/validate", tokEditor, obj{"ifVersion": 1})
	// The per-file counts reflect it.
	status, raw := e.call("GET", "/api/v1/drafts/"+id+"/files", tokEditor, nil)
	if status != 200 || !strings.Contains(string(raw), `"errors":`) {
		t.Fatal(status, string(raw))
	}
	sum := e.ok(200, "GET", "/api/v1/drafts/"+id, tokEditor, nil)
	if sum["dirty"] != true {
		t.Fatalf("summary = %v", sum)
	}
	// The draft cannot be proposed while invalid.
	er := e.ok(422, "POST", "/api/v1/drafts/"+id+"/propose", tokEditor, obj{"message": "bad"})
	if er["error"].(obj)["code"] != "invalid" || er["error"].(obj)["details"].(obj)["diagnostics"] == nil {
		t.Fatalf("propose error = %v", er)
	}
}

func TestDiff(t *testing.T) {
	e := newEnv(t)
	id, v := e.newDraft(tokEditor, "dir")
	if d := e.ok(200, "GET", "/api/v1/drafts/"+id+"/diff", tokEditor, nil); len(d["files"].([]any)) != 0 {
		t.Fatalf("clean draft has a diff: %v", d)
	}
	e.ops(id, tokEditor, v,
		obj{"op": "setField", "file": "01_routes.bcl", "path": "route/health.ping/path", "value": `"/ping"`},
		obj{"op": "addFile", "file": "02_new.bcl", "content": "# new file\n"},
		obj{"op": "removeFile", "file": "00_app.bcl"})
	d := e.ok(200, "GET", "/api/v1/drafts/"+id+"/diff", tokEditor, nil)
	status := map[string]string{}
	unified := map[string]string{}
	for _, x := range d["files"].([]any) {
		f := x.(obj)
		status[f["path"].(string)] = f["status"].(string)
		unified[f["path"].(string)], _ = f["unified"].(string)
	}
	if status["01_routes.bcl"] != "modified" || status["02_new.bcl"] != "added" || status["00_app.bcl"] != "removed" {
		t.Fatalf("status = %v", status)
	}
	if u := unified["01_routes.bcl"]; !strings.Contains(u, "--- a/01_routes.bcl") || !strings.Contains(u, `-  path "/health"`) || !strings.Contains(u, `+  path "/ping"`) {
		t.Fatalf("unified:\n%s", u)
	}
	if !strings.Contains(unified["02_new.bcl"], "+# new file") || !strings.Contains(unified["00_app.bcl"], "-name \"demo\"") {
		t.Fatalf("unified add/remove: %v", unified)
	}
	if d["changes"] == nil {
		t.Fatal("no changes array")
	}
}

func TestProposeApproveActivateRollback(t *testing.T) {
	e := newEnv(t)
	id, v := e.newDraft(tokEditor, "active")
	e.ops(id, tokEditor, v, obj{"op": "setField", "file": "01_routes.bcl", "path": "route/health.ping/path", "value": `"/ping"`})
	rev := e.ok(201, "POST", "/api/v1/drafts/"+id+"/propose", tokEditor, obj{"message": "move the health route"})
	revID := rev["id"].(string)
	if rev["status"] != "pending" || rev["author"] != "editor" {
		t.Fatalf("revision = %v", rev)
	}
	if cf := rev["changed_files"].([]any); len(cf) != 1 || cf[0].(obj)["path"] != "01_routes.bcl" {
		t.Fatalf("changed_files = %v", rev["changed_files"])
	}
	if len(rev["files"].([]any)) != 2 {
		t.Fatalf("the revision should carry its files: %v", rev["files"])
	}

	// Not approved yet.
	e.fail(409, "POST", "/api/v1/revisions/"+revID+"/activate", tokReviewer, nil)
	// The author cannot approve (role) — and neither can a reviewer who authored it.
	e.fail(403, "POST", "/api/v1/revisions/"+revID+"/approve", tokEditor, obj{})
	rid, rv := e.newDraft(tokReviewer, "active")
	e.ops(rid, tokReviewer, rv, obj{"op": "setField", "file": "01_routes.bcl", "path": "route/health.ping/path", "value": `"/mine"`})
	own := e.ok(201, "POST", "/api/v1/drafts/"+rid+"/propose", tokReviewer, obj{"message": "mine"})
	if c := e.fail(403, "POST", "/api/v1/revisions/"+own["id"].(string)+"/approve", tokReviewer, obj{}); c != "forbidden" {
		t.Fatal(c)
	}

	// A second person approves and activates.
	appr := e.ok(200, "POST", "/api/v1/revisions/"+revID+"/approve", tokReviewer, obj{"comment": "looks right"})
	if appr["status"] != "approved" {
		t.Fatalf("approve = %v", appr)
	}
	act := e.ok(200, "POST", "/api/v1/revisions/"+revID+"/activate", tokReviewer, nil)
	if act["status"] != "active" || e.activated.Load() != 1 {
		t.Fatalf("activate = %v (hook calls %d)", act, e.activated.Load())
	}
	meta := e.ok(200, "GET", "/api/v1/meta", tokViewer, nil)
	if meta["activeRevision"].(obj)["id"] != revID {
		t.Fatalf("active = %v", meta["activeRevision"])
	}
	full := e.ok(200, "GET", "/api/v1/revisions/"+revID, tokViewer, nil)
	if full["source"] == nil || len(full["files"].([]any)) != 2 {
		t.Fatalf("revision = %v", full)
	}
	e.fail(404, "GET", "/api/v1/revisions/rev_nope", tokViewer, nil)
	status, raw := e.call("GET", "/api/v1/revisions?limit=5", tokViewer, nil)
	var list []obj
	if status != 200 || json.Unmarshal(raw, &list) != nil || len(list) != 3 || list[0]["source"] != nil {
		t.Fatalf("list = %d %s", status, raw)
	}

	// A rejected revision cannot be activated.
	e.ok(200, "POST", "/api/v1/revisions/"+own["id"].(string)+"/reject", tokAdmin, obj{"comment": "no"})
	e.fail(409, "POST", "/api/v1/revisions/"+own["id"].(string)+"/activate", tokAdmin, nil)

	// Rollback is admin-only and re-activates the earlier revision.
	rb := e.ok(200, "POST", "/api/v1/rollback", tokAdmin, obj{"reason": "test"})
	if rb["status"] != "active" || rb["id"] == revID || e.activated.Load() != 2 {
		t.Fatalf("rollback = %v (hook calls %d)", rb, e.activated.Load())
	}

	// Everything above is in the audit trail.
	status, raw = e.call("GET", "/api/v1/audit?limit=100", tokAdmin, nil)
	if status != 200 {
		t.Fatal(status)
	}
	var trail []AuditEntry
	if err := json.Unmarshal(raw, &trail); err != nil {
		t.Fatal(err)
	}
	seen := map[[2]string]bool{}
	for _, a := range trail {
		seen[[2]string{a.Action, a.Who}] = true
	}
	for _, want := range [][2]string{{"draft.create", "editor"}, {"draft.ops", "editor"}, {"revision.propose", "editor"},
		{"draft.create", "reviewer"}, {"revision.propose", "reviewer"}, {"revision.approve", "reviewer"},
		{"revision.activate", "reviewer"}, {"revision.reject", "admin"}, {"revision.rollback", "admin"}} {
		if !seen[want] {
			t.Errorf("audit trail has no %s by %s (%v)", want[0], want[1], seen)
		}
	}
	if len(trail) > 1 && trail[0].At.Before(trail[len(trail)-1].At) {
		t.Error("the audit trail should be newest first")
	}
}

func TestAuditHookAndRing(t *testing.T) {
	var hooked atomic.Int32
	e := newEnv(t, func(c *Config) {
		c.AuditSize = 3
		c.Audit = func(AuditEntry) { hooked.Add(1) }
	})
	for range 5 {
		e.newDraft(tokEditor, "dir")
	}
	status, raw := e.call("GET", "/api/v1/audit", tokAdmin, nil)
	var trail []AuditEntry
	_ = json.Unmarshal(raw, &trail)
	if status != 200 || len(trail) != 3 || hooked.Load() != 5 {
		t.Fatalf("ring = %d entries, hook calls %d", len(trail), hooked.Load())
	}
}

func TestDraftLimit(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.MaxDraftsPerOwner = 2 })
	e.newDraft(tokEditor, "dir")
	e.newDraft(tokEditor, "dir")
	e.fail(409, "POST", "/api/v1/drafts", tokEditor, obj{"from": "dir"})
	e.newDraft(tokEditor2, "dir")
}

func TestEventsStreamReceivesChanges(t *testing.T) {
	e := newEnv(t)
	id, v := e.newDraft(tokEditor, "dir")

	req, _ := http.NewRequest("GET", e.ts.URL+"/api/v1/drafts/"+id+"/events?access_token="+tokEditor, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resp, err := http.DefaultClient.Do(req.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("status %d, type %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	type ev struct{ name, data string }
	events := make(chan ev, 16)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		var name string
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, "event: "):
				name = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				events <- ev{name, strings.TrimPrefix(line, "data: ")}
			}
		}
		close(events)
	}()
	next := func() ev {
		t.Helper()
		select {
		case x, ok := <-events:
			if !ok {
				t.Fatal("stream closed")
			}
			return x
		case <-time.After(5 * time.Second):
			t.Fatal("no event")
		}
		return ev{}
	}
	if first := next(); first.name != "changed" || !strings.Contains(first.data, `"version":1`) {
		t.Fatalf("first event = %v", first)
	}

	sub, unsub := mustDraft(t, e, id).Subscribe()
	defer unsub()

	out := e.ops(id, tokEditor, v, obj{"op": "setField", "file": "01_routes.bcl", "path": "route/health.ping/path", "value": `"/live"`})
	got := next()
	if got.name != "changed" || !strings.Contains(got.data, "01_routes.bcl") {
		t.Fatalf("changed event = %v", got)
	}
	if d := next(); d.name != "diagnostics" {
		t.Fatalf("diagnostics event = %v", d)
	}
	select {
	case n := <-sub:
		if n != ver(out) {
			t.Fatalf("studio.Draft subscription got %d, want %d", n, ver(out))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("studio.Draft.Subscribe got nothing")
	}

	// Deleting the draft ends the stream.
	e.ok(204, "DELETE", "/api/v1/drafts/"+id, tokEditor, nil)
	deadline := time.After(5 * time.Second)
	for {
		select {
		case _, ok := <-events:
			if !ok {
				return
			}
		case <-deadline:
			t.Fatal("stream did not end")
		}
	}
}

func mustDraft(t *testing.T, e *env, id string) studio.Draft {
	t.Helper()
	d, ok := e.srv.Get(id)
	if !ok {
		t.Fatal("no draft")
	}
	return d
}

func TestServerImplementsDraftSource(t *testing.T) {
	e := newEnv(t)
	var _ studio.DraftSource = e.srv
	id, v := e.newDraft(tokEditor, "dir")
	d := mustDraft(t, e, id)
	if d.ID() != id || d.Version() != v || len(d.Bundle()) != 2 {
		t.Fatalf("draft = %v %d %d", d.ID(), d.Version(), len(d.Bundle()))
	}
	e.ops(id, tokEditor, v, obj{"op": "setField", "file": "01_routes.bcl", "path": "route/health.ping/path", "value": `"/snap"`})
	if !strings.Contains(d.Bundle()[1].Content, `"/snap"`) || d.Version() != v+1 {
		t.Fatal("Draft does not reflect the change")
	}
	if _, ok := e.srv.Get("drf_nope"); ok {
		t.Fatal("found a draft that does not exist")
	}
}

// ---------------------------------------------------------------------------
// Preview
// ---------------------------------------------------------------------------

type fakePreview struct {
	stopped atomic.Int32
	built   atomic.Int64
}

func (f *fakePreview) Ensure(_ context.Context, d studio.Draft) (studio.PreviewStatus, error) {
	f.built.Store(d.Version())
	return studio.PreviewStatus{Status: "ready", Version: d.Version()}, nil
}
func (f *fakePreview) Stop(string) { f.stopped.Add(1) }
func (f *fakePreview) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "preview:"+r.URL.Path) })
}
func (f *fakePreview) Requests(string) []studio.RecordedRequest { return nil }

func TestPreviewIsNotImplementedWithoutAManager(t *testing.T) {
	e := newEnv(t)
	id, _ := e.newDraft(tokEditor, "dir")
	if c := e.fail(501, "POST", "/api/v1/drafts/"+id+"/preview", tokEditor, nil); c != "not_implemented" {
		t.Fatal(c)
	}
	e.fail(501, "DELETE", "/api/v1/drafts/"+id+"/preview", tokEditor, nil)
	e.fail(501, "GET", "/api/v1/drafts/"+id+"/preview/requests", tokEditor, nil)
	e.fail(501, "GET", "/preview/"+id+"/", tokEditor, nil)
}

func TestPreviewWithAManager(t *testing.T) {
	fp := &fakePreview{}
	e := newEnv(t, func(c *Config) { c.Preview = fp; c.BasePath = "/studio/" })
	if e.ok(200, "GET", "/api/v1/meta", tokViewer, nil)["features"].(obj)["preview"] != true {
		t.Fatal("preview feature not advertised")
	}
	id, _ := e.newDraft(tokEditor, "dir")

	req, _ := http.NewRequest("POST", e.ts.URL+"/api/v1/drafts/"+id+"/preview", nil)
	req.Header.Set("Authorization", "Bearer "+tokEditor)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var st studio.PreviewStatus
	_ = json.NewDecoder(resp.Body).Decode(&st)
	resp.Body.Close()
	if resp.StatusCode != 200 || st.Status != "ready" || st.URL != "/studio/preview/"+id+"/" {
		t.Fatalf("status %d, %+v", resp.StatusCode, st)
	}
	cookies := resp.Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].Path != "/studio/preview/"+id+"/" {
		t.Fatalf("cookies = %v", cookies)
	}

	get := func(path string, setup func(*http.Request)) (int, string) {
		r, _ := http.NewRequest("GET", e.ts.URL+path, nil)
		if setup != nil {
			setup(r)
		}
		rs, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer rs.Body.Close()
		b, _ := io.ReadAll(rs.Body)
		return rs.StatusCode, string(b)
	}
	// The iframe has only the cookie.
	if code, body := get("/preview/"+id+"/todos", func(r *http.Request) { r.AddCookie(cookies[0]) }); code != 200 || body != "preview:/preview/"+id+"/todos" {
		t.Fatalf("cookie: %d %q", code, body)
	}
	if code, _ := get("/preview/"+id+"/todos", nil); code != 401 {
		t.Fatalf("no credentials: %d", code)
	}
	// The cookie is good for that draft only.
	other, _ := e.newDraft(tokEditor, "dir")
	if code, _ := get("/preview/"+other+"/", func(r *http.Request) { r.AddCookie(cookies[0]) }); code != 401 {
		t.Fatalf("cookie for another draft: %d", code)
	}
	// A bearer token works if the caller owns the draft (or is admin), not otherwise.
	bearer := func(tok string) func(*http.Request) {
		return func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+tok) }
	}
	if code, _ := get("/preview/"+id+"/", bearer(tokEditor)); code != 200 {
		t.Fatalf("owner bearer: %d", code)
	}
	if code, _ := get("/preview/"+id+"/", bearer(tokAdmin)); code != 200 {
		t.Fatalf("admin bearer: %d", code)
	}
	if code, _ := get("/preview/"+id+"/", bearer(tokEditor2)); code != 401 {
		t.Fatalf("other editor: %d", code)
	}
	if code, _ := get("/preview/"+id+"/", bearer(tokViewer)); code != 401 {
		t.Fatalf("viewer: %d", code)
	}

	e.ok(200, "GET", "/api/v1/drafts/"+id+"/preview/requests", tokEditor, nil)
	e.ok(204, "DELETE", "/api/v1/drafts/"+id+"/preview", tokEditor, nil)
	e.ok(204, "DELETE", "/api/v1/drafts/"+id, tokEditor, nil)
	if fp.stopped.Load() != 2 {
		t.Fatalf("Stop called %d times, want 2", fp.stopped.Load())
	}
}

// ---------------------------------------------------------------------------
// Web app
// ---------------------------------------------------------------------------

func TestServesTheWebApp(t *testing.T) {
	e := newEnv(t, func(c *Config) {
		c.Web = fstest.MapFS{
			"index.html":    {Data: []byte("<html>studio</html>")},
			"assets/app.js": {Data: []byte("console.log(1)")},
		}
	})
	get := func(path string) (int, string, string) {
		rs, err := http.Get(e.ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer rs.Body.Close()
		b, _ := io.ReadAll(rs.Body)
		return rs.StatusCode, string(b), rs.Header.Get("Cache-Control")
	}
	if code, body, cc := get("/"); code != 200 || body != "<html>studio</html>" || cc != "no-store" {
		t.Fatalf("/: %d %q %q", code, body, cc)
	}
	if code, body, _ := get("/drafts/drf_x/edit"); code != 200 || body != "<html>studio</html>" {
		t.Fatalf("client route: %d %q", code, body)
	}
	if code, body, cc := get("/assets/app.js"); code != 200 || body != "console.log(1)" || !strings.Contains(cc, "immutable") {
		t.Fatalf("asset: %d %q %q", code, body, cc)
	}
	if code, _, _ := get("/assets/missing.js"); code != 404 {
		t.Fatalf("missing asset: %d", code)
	}
	if code, _, _ := get("/api/v1/nothing"); code != 404 {
		t.Fatalf("unknown api: %d", code)
	}
}

func TestEmbeddedPlaceholderExists(t *testing.T) {
	data, err := studio.WebAssets().Open("index.html")
	if err != nil {
		t.Fatal(err)
	}
	data.Close()
}

// ---------------------------------------------------------------------------
// A realistic configuration: the starter's
// ---------------------------------------------------------------------------

func TestStarterConfigRoundTrip(t *testing.T) {
	dir := filepath.Join("..", "..", "examples", "starter", "resources", "config")
	if _, err := os.Stat(dir); err != nil {
		t.Skip("starter config not present")
	}
	e := newEnvDir(t, dir)
	id, v := e.newDraft(tokEditor, "dir")
	sum := e.ok(200, "GET", "/api/v1/drafts/"+id, tokEditor, nil)
	if n := len(sum["files"].([]any)); n < 10 {
		t.Fatalf("only %d files", n)
	}
	if c := sum["diagnostics"]; c == nil {
		t.Fatal("no diagnostics")
	}
	// Every file's tree loads.
	for _, f := range sum["files"].([]any) {
		if status, raw := e.call("GET", "/api/v1/drafts/"+id+"/files/"+f.(string)+"/tree", tokEditor, nil); status != 200 {
			t.Fatalf("%v tree: %d %s", f, status, raw)
		}
	}
	// Edit a real route, see a local diff, and propose it.
	out := e.ops(id, tokEditor, v, obj{"op": "setField", "file": "13_todo_pages.bcl", "path": "route/web.todos_list/path", "value": `"/my-todos"`})
	if got := strs(out["changed"]); len(got) != 1 || got[0] != "13_todo_pages.bcl" {
		t.Fatalf("changed = %v", got)
	}
	d := e.ok(200, "GET", "/api/v1/drafts/"+id+"/diff", tokEditor, nil)
	if fs := d["files"].([]any); len(fs) != 1 || !strings.Contains(fs[0].(obj)["unified"].(string), `+  path "/my-todos"`) {
		t.Fatalf("diff = %v", d["files"])
	}
	rev := e.ok(201, "POST", "/api/v1/drafts/"+id+"/propose", tokEditor, obj{"message": "rename todos route"})
	if rev["status"] != "pending" {
		t.Fatalf("revision = %v", rev)
	}
}

// ---------------------------------------------------------------------------
// Unified diff
// ---------------------------------------------------------------------------

func TestUnifiedDiff(t *testing.T) {
	s := func(v string) *string { return &v }
	cases := []struct{ name, old, new, want string }{
		{"same", "a\nb\n", "a\nb\n", ""},
		{"change", "a\nb\nc\n", "a\nB\nc\n", "--- a/f\n+++ b/f\n@@ -1,3 +1,3 @@\n a\n-b\n+B\n c\n"},
		{"insert", "a\nc\n", "a\nb\nc\n", "--- a/f\n+++ b/f\n@@ -1,2 +1,3 @@\n a\n+b\n c\n"},
		{"delete", "a\nb\nc\n", "a\nc\n", "--- a/f\n+++ b/f\n@@ -1,3 +1,2 @@\n a\n-b\n c\n"},
	}
	for _, c := range cases {
		if got := unifiedDiff("f", s(c.old), s(c.new)); got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
	}
	if got := unifiedDiff("f", nil, s("x\ny\n")); got != "--- /dev/null\n+++ b/f\n@@ -0,0 +1,2 @@\n+x\n+y\n" {
		t.Errorf("added: %q", got)
	}
	if got := unifiedDiff("f", s("x\n"), nil); got != "--- a/f\n+++ /dev/null\n@@ -1,1 +0,0 @@\n-x\n" {
		t.Errorf("removed: %q", got)
	}
	// Distant changes become separate hunks.
	var old, cur []string
	for i := range 40 {
		old = append(old, "line"+strings.Repeat("x", i%3))
		cur = append(cur, "line"+strings.Repeat("x", i%3))
	}
	cur[2], cur[35] = "CHANGED-A", "CHANGED-B"
	got := unifiedDiff("f", s(strings.Join(old, "\n")+"\n"), s(strings.Join(cur, "\n")+"\n"))
	if strings.Count(got, "@@ -") != 2 {
		t.Errorf("want two hunks:\n%s", got)
	}
}
