package e2e

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// numbers returns n valid Nepali mobile numbers followed by the given junk.
func numbers(n int, junk ...string) []string {
	var out []string
	for i := 0; i < n; i++ {
		out = append(out, fmt.Sprintf("98412345%02d", i))
	}
	return append(out, junk...)
}

func notifications(b *browser) (list []any, unread float64) {
	_, out := b.do("GET", "/ui/notifications", nil)
	m := asMap(out)
	unread, _ = m["unread"].(float64)
	return asList(m["notifications"]), unread
}

func campaignCount(b *browser) int { _, l := b.do("GET", "/ui/campaigns", nil); return len(asList(l)) }

// The rules (rules/campaigns.bcl) say what an invalid number means for a list.
func TestInvalidNumberPolicy(t *testing.T) {
	s := start(t)
	op, acct := operator(t, s), s.browser()
	acct.login("demo@example.com", "demo-pass-123")
	create := func(extra map[string]any, recipients []string) (int, map[string]any) {
		body := map[string]any{"name": "Policy", "text": "Hello", "recipients": recipients}
		for k, v := range extra {
			body[k] = v
		}
		st, out := acct.do("POST", "/ui/campaigns", body)
		return st, asMap(out)
	}
	refused := func(what string, st int, out map[string]any) {
		t.Helper()
		if st != 422 || str(out, "error", "code") != "INVALID_RECIPIENTS" {
			t.Fatalf("%s: %d %v, want 422 INVALID_RECIPIENTS", what, st, out)
		}
	}

	// Everything valid: accepted, no notice.
	st, out := create(nil, numbers(5))
	if st != 202 || out["skipped"] != 0.0 || out["notice"] != "" {
		t.Fatalf("all valid: %d %v", st, out)
	}
	if list, unread := notifications(acct); len(list) != 0 || unread != 0 {
		t.Fatalf("a clean list notified the account: %v", list)
	}

	// A few invalid numbers (under a fifth): the valid ones go ahead, the invalid are
	// counted, and the account is told.
	st, out = create(nil, numbers(9, "12345"))
	if st != 202 || out["valid"] != 9.0 || out["skipped"] != 1.0 || !strings.Contains(str(out, "notice"), "1 of 10 numbers are invalid") {
		t.Fatalf("a few invalid: %d %v", st, out)
	}
	list, unread := notifications(acct)
	if unread != 1 || len(list) != 1 || !strings.Contains(str(asMap(list[0]), "title"), "1 invalid number in campaign Policy") || !strings.Contains(str(asMap(list[0]), "body"), "12345") {
		t.Fatalf("the notification: unread %v %v", unread, list)
	}

	// More than a fifth invalid: the whole list is discarded, nothing stored.
	before := campaignCount(acct)
	st, out = create(nil, numbers(6, "12345", "abc", "0000"))
	refused("mostly invalid", st, out)
	if !strings.Contains(str(out, "error", "message"), "more than 20 percent") || campaignCount(acct) != before {
		t.Fatalf("a refused list was stored or the reason is missing: %v (campaigns %d -> %d)", out, before, campaignCount(acct))
	}
	// The submitter may ask to skip the invalid numbers (up to half the list), or to discard the list.
	if st, out = create(map[string]any{"on_invalid": "skip"}, numbers(6, "12345", "abc", "0000")); st != 202 || out["skipped"] != 3.0 {
		t.Fatalf("asked to skip: %d %v", st, out)
	}
	st, out = create(map[string]any{"on_invalid": "skip"}, numbers(2, "1", "2", "3", "4"))
	refused("asked to skip, but most are invalid", st, out)
	st, out = create(map[string]any{"on_invalid": "discard"}, numbers(30, "12345"))
	refused("asked to discard", st, out)
	if st, out = create(map[string]any{"on_invalid": "discard"}, numbers(30)); st != 202 {
		t.Fatalf("asked to discard, nothing invalid: %d %v", st, out)
	}
	// A one-time-code campaign is all or nothing, whatever the share.
	st, out = create(map[string]any{"type": "otp"}, numbers(30, "12345"))
	refused("otp", st, out)
	// A list with no valid number at all.
	st, out = create(map[string]any{"on_invalid": "skip"}, []string{"1", "abc"})
	if st != 422 || str(out, "error", "code") != "NO_VALID_RECIPIENTS" {
		t.Fatalf("nothing valid: %d %v", st, out)
	}

	// The preview shows what the rules would do, without storing anything.
	n := campaignCount(acct)
	_, prev := acct.do("POST", "/ui/campaigns/preview", map[string]any{"text": "x", "recipients": numbers(6, "12345", "abc", "0000")})
	p := asMap(asMap(prev)["policy"])
	if p["effect"] != "deny" || p["rule"] != "mostly-invalid" || p["code"] != "INVALID_RECIPIENTS" {
		t.Fatalf("preview policy = %v", p)
	}
	_, prev = acct.do("POST", "/ui/campaigns/preview", map[string]any{"text": "x", "recipients": numbers(9, "12345")})
	if p := asMap(asMap(prev)["policy"]); p["effect"] != "allow" || p["notify"] != true {
		t.Fatalf("preview policy for a few invalid = %v", p)
	}
	if campaignCount(acct) != n {
		t.Fatalf("a preview stored a campaign")
	}

	// A strict plan never sends a partly valid list.
	op.do("PUT", "/ui/admin/users/demo", map[string]any{"name": "Demo account", "tenant": "demo", "default_sender": "DEMO", "plan": "strict"})
	st, out = create(nil, numbers(30, "12345"))
	refused("strict plan", st, out)
	op.do("PUT", "/ui/admin/users/demo", map[string]any{"name": "Demo account", "tenant": "demo", "default_sender": "DEMO"})

	// The operator changes the rule; the next campaign obeys it. Here the limit goes from 20 to 60 percent.
	_, rule := op.do("GET", "/ui/admin/rules/campaigns", nil)
	src := str(asMap(rule), "source")
	if !strings.Contains(src, "campaign.invalid_percent > 20") {
		t.Fatalf("the rule source: %.300s", src)
	}
	if st, out := op.do("PUT", "/ui/admin/rules/campaigns", map[string]any{"source": strings.Replace(src, "campaign.invalid_percent > 20", "campaign.invalid_percent > 60", 1)}); st != 200 {
		t.Fatalf("edit the rule = %d %v", st, out)
	}
	if st, out = create(nil, numbers(6, "12345", "abc", "0000")); st != 202 || out["skipped"] != 3.0 {
		t.Fatalf("after relaxing the rule: %d %v", st, out)
	}
	// Facts for the tester are listed, so the rule can be tried on the Rules page.
	if kinds := factKinds(op, "campaigns"); kinds["campaign.invalid_percent"] != "number" || kinds["user.plan"] != "text" {
		t.Fatalf("fact kinds = %v", kinds)
	}
}

func factKinds(op *browser, rule string) map[string]string {
	_, r := op.do("GET", "/ui/admin/rules/"+rule, nil)
	out := map[string]string{}
	for _, f := range asList(asMap(r)["facts"]) {
		out[str(asMap(f), "path")] = str(asMap(f), "kind")
	}
	return out
}

// An audience is counted when the campaign is created from it, and the same rules apply.
func TestInvalidPolicyForAnAudience(t *testing.T) {
	s := start(t)
	acct := s.browser()
	acct.login("demo@example.com", "demo-pass-123")
	_, out := acct.do("POST", "/ui/audiences", map[string]any{"name": "Mostly bad", "recipients": numbers(2, "1", "2", "3", "4")})
	aid := str(asMap(out), "id")
	st, out := acct.do("POST", "/ui/campaigns", map[string]any{"name": "A", "audience": aid, "text": "x"})
	if st != 422 || str(asMap(out), "error", "code") != "INVALID_RECIPIENTS" {
		t.Fatalf("audience, mostly invalid = %d %v", st, out)
	}
	if campaignCount(acct) != 0 {
		t.Fatalf("a refused campaign was stored")
	}
	_, out = acct.do("POST", "/ui/audiences", map[string]any{"name": "A few bad", "recipients": numbers(12, "1")})
	st, out = acct.do("POST", "/ui/campaigns", map[string]any{"name": "B", "audience": str(asMap(out), "id"), "text": "x"})
	if st != 202 || asMap(out)["skipped"] != 1.0 || !strings.Contains(str(asMap(out), "notice"), "1 of 13") {
		t.Fatalf("audience, a few invalid = %d %v", st, out)
	}
	if _, unread := notifications(acct); unread != 1 {
		t.Fatalf("unread = %v", unread)
	}
}

// The account is told when its campaign has been sent, and when it is rejected.
func TestCampaignNotifications(t *testing.T) {
	s := start(t)
	op, acct := operator(t, s), s.browser()
	acct.login("demo@example.com", "demo-pass-123")
	_, out := acct.do("POST", "/ui/campaigns", map[string]any{"name": "Told", "text": "Hi", "recipients": numbers(9, "12345")})
	cid := str(asMap(out), "id")
	approveCampaign(t, op, "approve")
	eventually(t, "sent", func() bool { c, _ := campaignState(acct, cid); return c["state"] == "sent" })
	eventually(t, "the completion notice", func() bool { l, _ := notifications(acct); return len(l) == 2 })
	list, unread := notifications(acct)
	titles := []string{str(asMap(list[0]), "title"), str(asMap(list[1]), "title")}
	if unread != 2 || !strings.Contains(strings.Join(titles, "|"), "Campaign Told was sent") || !strings.Contains(strings.Join(titles, "|"), "1 invalid number") {
		t.Fatalf("notifications: unread %v %v", unread, titles)
	}
	// Mark one read, then all.
	id := str(asMap(list[0]), "id")
	if st, _ := acct.do("POST", "/ui/notifications/read", map[string]any{"ids": []string{id}}); st != 200 {
		t.Fatalf("mark one = %d", st)
	}
	if _, unread := notifications(acct); unread != 1 {
		t.Fatalf("unread after one = %v", unread)
	}
	if st, out := acct.do("POST", "/ui/notifications/read", map[string]any{"everything": true}); st != 200 {
		t.Fatalf("mark all = %d %v", st, out)
	}
	if _, unread := notifications(acct); unread != 0 {
		t.Fatalf("unread after all = %v", unread)
	}
	// A rejection is reported too.
	acct.do("POST", "/ui/campaigns", map[string]any{"name": "Nope", "text": "Hi", "recipients": numbers(3)})
	time.Sleep(300 * time.Millisecond)
	approveCampaign(t, op, "reject")
	eventually(t, "the rejection notice", func() bool { _, u := notifications(acct); return u == 1 })
	if l, _ := notifications(acct); !strings.Contains(str(asMap(l[0]), "title"), "Nope was rejected") {
		t.Fatalf("notification = %v", l[0])
	}
	// Another account sees none of it.
	other := s.browser()
	op.do("PUT", "/ui/admin/users/newco", map[string]any{"name": "NewCo"})
	op.do("PUT", "/ui/admin/users/newco/password", map[string]any{"email": "ops@newco.example", "password": "a-long-password-1"})
	other.login("ops@newco.example", "a-long-password-1")
	if l, u := notifications(other); len(l) != 0 || u != 0 {
		t.Fatalf("another account saw notifications: %v", l)
	}
	if st, _ := s.browser().do("GET", "/ui/notifications", nil); st != 401 {
		t.Fatalf("anonymous = %d", st)
	}
}
