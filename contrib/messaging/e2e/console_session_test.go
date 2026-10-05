package e2e

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"testing"
	"time"
)

// browser is a client with a cookie jar: what the console's script is.
type browser struct {
	t *testing.T
	s *stack
	c *http.Client
}

func (s *stack) browser() *browser {
	jar, _ := cookiejar.New(nil)
	return &browser{t: s.t, s: s, c: &http.Client{Jar: jar, Timeout: 20 * time.Second}}
}

func (b *browser) do(method, path string, body any) (int, any) {
	b.t.Helper()
	var rdr io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rdr = bytes.NewReader(raw)
	}
	req, _ := http.NewRequest(method, b.s.app.URL()+path, rdr)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := b.c.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out any
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, out
}

func (b *browser) login(email, password string) int {
	b.t.Helper()
	st, _ := b.do("POST", "/login", map[string]any{"email": email, "password": password})
	return st
}

func asMap(v any) map[string]any { m, _ := v.(map[string]any); return m }

func TestSessionLoginAndRoles(t *testing.T) {
	s := start(t)
	b := s.browser()
	if st, _ := b.do("GET", "/ui/me", nil); st != 401 {
		t.Fatalf("no session = %d", st)
	}
	if st := b.login("demo@example.com", "wrong-password"); st != 401 {
		t.Fatalf("wrong password = %d", st)
	}
	if st := b.login("demo@example.com", "demo-pass-123"); st != 200 {
		t.Fatalf("login = %d", st)
	}
	st, me := b.do("GET", "/ui/me", nil)
	if st != 200 || asMap(me)["id"] != "demo" {
		t.Fatalf("me = %d %v", st, me)
	}
	if st, _ := b.do("GET", "/ui/admin/stats", nil); st != 403 {
		t.Fatalf("an account reading operator data = %d, want 403", st)
	}
	// An account sends through the session, with no API key.
	st, out := b.do("POST", "/ui/messages", map[string]any{"to": "+9779841234567", "text": "from the console"})
	if st != 202 {
		t.Fatalf("send = %d %v", st, out)
	}
	id, _ := asMap(out)["id"].(string)
	eventually(t, "delivered", func() bool {
		_, m := b.do("GET", "/ui/messages/"+id, nil)
		return str(asMap(m), "status") == "delivered"
	})
	// The operator sees operator data; the session ends at logout.
	op := s.browser()
	if st := op.login("operator@example.com", "operator-pass-123"); st != 200 {
		t.Fatalf("operator login = %d", st)
	}
	if st, _ := op.do("GET", "/ui/admin/stats", nil); st != 200 {
		t.Fatalf("operator stats = %d", st)
	}
	if st, _ := b.do("POST", "/logout", nil); st != 200 {
		t.Fatalf("logout = %d", st)
	}
	if st, _ := b.do("GET", "/ui/me", nil); st != 401 {
		t.Fatalf("after logout = %d", st)
	}
}

// The campaign workflow: an account submits, the operator approves on the
// task list, the run sends every recipient it can and records the one it cannot.
func TestCampaignApprovalWorkflow(t *testing.T) {
	s := start(t)
	acct, op := s.browser(), s.browser()
	acct.login("demo@example.com", "demo-pass-123")
	op.login("operator@example.com", "operator-pass-123")
	before, _ := s.balance("demo")

	st, out := acct.do("POST", "/ui/campaigns", map[string]any{"name": "Spring", "on_invalid": "skip", "text": "Sale today", "recipients": []string{"+9779841234567", "+9779841234568", "12345"}})
	if st != 202 {
		t.Fatalf("create = %d %v", st, out)
	}
	cid, _ := asMap(out)["id"].(string)

	// Nothing is sent, and nothing is charged, before approval.
	time.Sleep(500 * time.Millisecond)
	if n := len(s.smsc.Submitted()); n != 0 {
		t.Fatalf("%d messages sent before approval", n)
	}
	// The account cannot approve its own campaign.
	var task string
	eventually(t, "task on the operator's list", func() bool {
		_, tasks := op.do("GET", "/ui/admin/approvals", nil)
		list, _ := tasks.([]any)
		if len(list) == 0 {
			return false
		}
		task, _ = asMap(list[0])["task_id"].(string)
		return task != ""
	})
	if st, _ := acct.do("POST", "/ui/admin/approvals/"+task, map[string]any{"action": "approve"}); st != 403 {
		t.Fatalf("the requester approving = %d, want 403", st)
	}
	if st, out := op.do("POST", "/ui/admin/approvals/"+task, map[string]any{"action": "approve", "note": "ok"}); st != 200 {
		t.Fatalf("approve = %d %v", st, out)
	}
	eventually(t, "campaign sent", func() bool {
		_, c := acct.do("GET", "/ui/campaigns/"+cid, nil)
		return str(asMap(c), "campaign", "state") == "sent"
	})
	_, c := acct.do("GET", "/ui/campaigns/"+cid, nil)
	camp := asMap(asMap(c)["campaign"])
	if camp["sent"] != 2.0 || camp["failed"] != 0.0 || camp["skipped"] != 1.0 || camp["decided_by"] != "operator" {
		t.Fatalf("campaign = %v", camp)
	}
	eventually(t, "both delivered", func() bool { return len(s.smsc.Submitted()) == 2 })
	// Wait for the money to settle before reading it: a message that has been
	// submitted is charged, but the hold is only captured when the carrier
	// accepts, so the balance can still be in flight here.
	var after, held float64
	eventually(t, "the funds are settled", func() bool {
		after, held = s.balance("demo")
		return held == 0 && before-after > 0.0299
	})
	if before-after < 0.0299 || before-after > 0.0301 {
		t.Fatalf("balance %v -> %v held %v: two messages must be charged, the refused number must not", before, after, held)
	}

	// A rejected campaign sends nothing.
	_, out = acct.do("POST", "/ui/campaigns", map[string]any{"name": "Nope", "text": "x", "recipients": []string{"+9779841234567"}})
	cid2, _ := asMap(out)["id"].(string)
	eventually(t, "second task", func() bool {
		_, tasks := op.do("GET", "/ui/admin/approvals", nil)
		list, _ := tasks.([]any)
		if len(list) == 0 {
			return false
		}
		task, _ = asMap(list[0])["task_id"].(string)
		return true
	})
	op.do("POST", "/ui/admin/approvals/"+task, map[string]any{"action": "reject"})
	eventually(t, "rejected", func() bool {
		_, c := acct.do("GET", "/ui/campaigns/"+cid2, nil)
		return str(asMap(c), "campaign", "state") == "rejected"
	})
	if n := len(s.smsc.Submitted()); n != 2 {
		t.Fatalf("a rejected campaign sent %d more", n-2)
	}
}

// The template picker: the catalog lists each template's placeholders, and the
// preview renders them with the values the user typed.
func TestTemplateCatalogAndPreview(t *testing.T) {
	s := start(t)
	b := s.browser()
	b.login("demo@example.com", "demo-pass-123")
	st, list := b.do("GET", "/ui/templates", nil)
	rows, _ := list.([]any)
	if st != 200 || len(rows) != 4 {
		t.Fatalf("catalog = %d %v", st, list)
	}
	var fields string
	for _, r := range rows {
		if m := asMap(r); m["name"] == "otp_login" && m["lang"] == "" {
			fields, _ = m["fields"].(string)
		}
	}
	var decoded []map[string]any
	if err := json.Unmarshal([]byte(fields), &decoded); err != nil || len(decoded) != 3 || decoded[1]["name"] != "code" {
		t.Fatalf("otp placeholders = %q (%v)", fields, err)
	}
	st, out := b.do("POST", "/ui/templates/preview", map[string]any{"template": "otp_login", "vars": map[string]any{"brand": "Acme", "code": "482913", "minutes": "5"}})
	m := asMap(out)
	if st != 200 || m["text"] != "Your Acme login code is 482913. It expires in 5 minutes. Do not share it with anyone." || m["type"] != "otp" || m["segments"] != 1.0 {
		t.Fatalf("preview = %d %v", st, out)
	}
	_, out = b.do("POST", "/ui/templates/preview", map[string]any{"template": "otp_login", "lang": "ne", "vars": map[string]any{"brand": "Acme", "code": "1", "minutes": "5"}})
	if asMap(out)["encoding"] != "ucs2" {
		t.Fatalf("nepali preview = %v", out)
	}
	// Every catalog template renders with the catalog's own defaults plus a value for each empty field.
	for _, r := range rows {
		row := asMap(r)
		var fs []map[string]any
		_ = json.Unmarshal([]byte(row["fields"].(string)), &fs)
		vars := map[string]any{}
		for _, f := range fs {
			v, _ := f["default"].(string)
			if v == "" {
				v = "x"
			}
			vars[f["name"].(string)] = v
		}
		_, out := b.do("POST", "/ui/templates/preview", map[string]any{"template": row["name"], "lang": row["lang"], "vars": vars})
		if m := asMap(out); m["error"] != "" || m["text"] == "" {
			t.Fatalf("template %v/%v does not render with its own defaults: %v", row["name"], row["lang"], out)
		}
	}
	if st, _ := b.do("POST", "/ui/templates/preview", map[string]any{"template": "nope"}); st != 200 {
		t.Fatalf("unknown template preview = %d", st)
	}
}
