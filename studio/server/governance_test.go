package server

import (
	"encoding/json"
	"strings"
	"testing"
)

// proposeChange makes a draft that moves the health route, proposes it and
// returns the revision id.
func proposeChange(t *testing.T, e *env, token, path string) string {
	t.Helper()
	id, v := e.newDraft(token, "active")
	e.ops(id, token, v, obj{"op": "setField", "file": "01_routes.bcl", "path": "route/health.ping/path", "value": `"` + path + `"`})
	return e.ok(201, "POST", "/api/v1/drafts/"+id+"/propose", token, obj{"message": "move to " + path})["id"].(string)
}

func commentStores(t *testing.T) map[string]func(*Config) {
	mut, _ := withSQL(t)
	return map[string]func(*Config){"memory": func(*Config) {}, "sql": mut}
}

func TestRevisionComments(t *testing.T) {
	for name, mut := range commentStores(t) {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t, mut)
			rev := proposeChange(t, e, tokEditor, "/moved")
			base := "/api/v1/revisions/" + rev + "/comments"

			if status, raw := e.call("GET", base, tokViewer, nil); status != 200 || strings.TrimSpace(string(raw)) != "[]" {
				t.Fatalf("empty thread = %d %s", status, raw)
			}
			// Viewers read; editors and above write.
			e.fail(403, "POST", base, tokViewer, obj{"body": "hi"})
			e.fail(401, "POST", base, "", obj{"body": "hi"})

			first := e.ok(201, "POST", base, tokReviewer, obj{"body": "  Why move it?  "})
			if first["author"] != "reviewer" || first["body"] != "Why move it?" || first["revisionId"] != rev || first["id"] == "" {
				t.Fatalf("comment = %v", first)
			}
			reply := e.ok(201, "POST", base, tokEditor, obj{"body": "The load balancer expects /moved", "replyTo": first["id"]})
			if reply["replyTo"] != first["id"] {
				t.Fatalf("reply = %v", reply)
			}

			status, raw := e.call("GET", base, tokViewer, nil)
			var thread []Comment
			if err := json.Unmarshal([]byte(raw), &thread); err != nil || status != 200 || len(thread) != 2 {
				t.Fatalf("thread = %d %s (%v)", status, raw, err)
			}
			if thread[0].ID != first["id"] || thread[1].ID != reply["id"] || !thread[0].At.Before(thread[1].At) && !thread[0].At.Equal(thread[1].At) {
				t.Fatalf("the thread is not oldest first: %+v", thread)
			}

			for label, body := range map[string]obj{
				"empty":         {"body": "   "},
				"missing":       {},
				"too long":      {"body": strings.Repeat("x", maxCommentRunes+1)},
				"unknown reply": {"body": "hi", "replyTo": "cmt_nope"},
			} {
				if c := e.fail(422, "POST", base, tokEditor, body); c != "invalid_comment" {
					t.Errorf("%s: code %q", label, c)
				}
			}
			// The limit is in characters, not bytes.
			e.ok(201, "POST", base, tokEditor, obj{"body": strings.Repeat("é", maxCommentRunes)})

			// A comment belongs to its revision.
			other := proposeChange(t, e, tokEditor2, "/other")
			if status, raw := e.call("GET", "/api/v1/revisions/"+other+"/comments", tokViewer, nil); status != 200 || strings.TrimSpace(string(raw)) != "[]" {
				t.Fatalf("another revision's thread = %d %s", status, raw)
			}
			e.fail(422, "POST", "/api/v1/revisions/"+other+"/comments", tokEditor, obj{"body": "x", "replyTo": first["id"]})
			e.fail(404, "GET", "/api/v1/revisions/rev_nope/comments", tokViewer, nil)
			e.fail(404, "POST", "/api/v1/revisions/rev_nope/comments", tokEditor, obj{"body": "x"})

			status, raw = e.call("GET", "/api/v1/audit?limit=100", tokAdmin, nil)
			if !strings.Contains(string(raw), `"revision.comment"`) || status != 200 {
				t.Fatalf("comments are not in the audit trail: %s", raw)
			}
		})
	}
}

func TestCommentLimitPerRevision(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.MaxCommentsPerRevision = 2 })
	rev := proposeChange(t, e, tokEditor, "/moved")
	base := "/api/v1/revisions/" + rev + "/comments"
	e.ok(201, "POST", base, tokEditor, obj{"body": "one"})
	e.ok(201, "POST", base, tokEditor, obj{"body": "two"})
	if c := e.fail(409, "POST", base, tokEditor, obj{"body": "three"}); c != "too_many_comments" {
		t.Fatal(c)
	}
}

func TestRevisionDiff(t *testing.T) {
	e := newEnv(t)
	active := e.ok(200, "GET", "/api/v1/meta", tokViewer, nil)["activeRevision"].(obj)["id"].(string)
	rev := proposeChange(t, e, tokEditor, "/moved")

	// Default: against the revision it was proposed against (the active one).
	d := e.ok(200, "GET", "/api/v1/revisions/"+rev+"/diff", tokViewer, nil)
	if d["from"] != active || d["to"] != rev {
		t.Fatalf("diff = %v", d)
	}
	files := d["files"].([]any)
	if len(files) != 1 {
		t.Fatalf("files = %v", files)
	}
	f := files[0].(obj)
	if f["path"] != "01_routes.bcl" || f["status"] != "modified" ||
		!strings.Contains(f["unified"].(string), `-  path "/health"`) || !strings.Contains(f["unified"].(string), `+  path "/moved"`) {
		t.Fatalf("file diff = %v", f)
	}
	if ch := d["changes"].([]any); len(ch) == 0 {
		t.Fatalf("no block-level changes: %v", d)
	}

	// ?against=active is the same here; an explicit id works; a revision against itself is empty.
	same := e.ok(200, "GET", "/api/v1/revisions/"+rev+"/diff?against=active", tokViewer, nil)
	if same["from"] != active || len(same["files"].([]any)) != 1 {
		t.Fatalf("against=active = %v", same)
	}
	self := e.ok(200, "GET", "/api/v1/revisions/"+rev+"/diff?against="+rev, tokViewer, nil)
	if len(self["files"].([]any)) != 0 || len(self["changes"].([]any)) != 0 {
		t.Fatalf("a revision against itself = %v", self)
	}

	// Between two arbitrary revisions, in the direction from -> to.
	rev2 := proposeChange(t, e, tokEditor2, "/again")
	between := e.ok(200, "GET", "/api/v1/revisions/"+rev2+"/diff?against="+rev, tokViewer, nil)
	u := between["files"].([]any)[0].(obj)["unified"].(string)
	if !strings.Contains(u, `-  path "/moved"`) || !strings.Contains(u, `+  path "/again"`) {
		t.Fatalf("diff between revisions:\n%s", u)
	}

	// The active revision was proposed with no base: everything is added.
	first := e.ok(200, "GET", "/api/v1/revisions/"+active+"/diff", tokViewer, nil)
	if first["from"] != "" || len(first["files"].([]any)) != 2 {
		t.Fatalf("first revision diff = %v", first)
	}
	for _, x := range first["files"].([]any) {
		if x.(obj)["status"] != "added" {
			t.Fatalf("file = %v", x)
		}
	}

	e.fail(404, "GET", "/api/v1/revisions/rev_nope/diff", tokViewer, nil)
	e.fail(404, "GET", "/api/v1/revisions/"+rev+"/diff?against=rev_nope", tokViewer, nil)
	e.fail(401, "GET", "/api/v1/revisions/"+rev+"/diff", "", nil)
}

func TestRequiredApprovalsAreExposed(t *testing.T) {
	e := newEnv(t, func(*Config) {})
	e.mgr.Approvals = 2
	rev := proposeChange(t, e, tokEditor, "/moved")

	full := e.ok(200, "GET", "/api/v1/revisions/"+rev, tokViewer, nil)
	if full["required_approvals"] != float64(2) || full["id"] != rev || full["source"] == nil {
		t.Fatalf("revision detail = %v", full)
	}
	status, raw := e.call("GET", "/api/v1/revisions", tokViewer, nil)
	var list []obj
	if status != 200 || json.Unmarshal(raw, &list) != nil || len(list) == 0 {
		t.Fatalf("list = %d %s", status, raw)
	}
	for _, r := range list {
		if r["required_approvals"] != float64(2) {
			t.Fatalf("summary = %v", r)
		}
	}
	meta := e.ok(200, "GET", "/api/v1/meta", tokViewer, nil)
	if meta["activeRevision"].(obj)["required_approvals"] != float64(2) {
		t.Fatalf("meta = %v", meta)
	}
	// Two approvals are needed before activation.
	e.ok(200, "POST", "/api/v1/revisions/"+rev+"/approve", tokReviewer, obj{})
	e.fail(409, "POST", "/api/v1/revisions/"+rev+"/activate", tokReviewer, nil)
	e.ok(200, "POST", "/api/v1/revisions/"+rev+"/approve", tokAdmin, obj{})
	e.ok(200, "POST", "/api/v1/revisions/"+rev+"/activate", tokReviewer, nil)
}
