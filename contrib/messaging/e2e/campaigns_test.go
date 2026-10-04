package e2e

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// approveCampaign has the operator approve the open task of a campaign.
func approveCampaign(t *testing.T, op *browser, action string) {
	t.Helper()
	var task string
	eventually(t, "a task on the operator's list", func() bool {
		_, tasks := op.do("GET", "/ui/admin/approvals", nil)
		list := asList(tasks)
		if len(list) == 0 {
			return false
		}
		task, _ = asMap(list[0])["task_id"].(string)
		return task != ""
	})
	if st, out := op.do("POST", "/ui/admin/approvals/"+task, map[string]any{"action": action}); st != 200 {
		t.Fatalf("%s = %d %v", action, st, out)
	}
}

func campaignState(b *browser, id string) (map[string]any, []any) {
	_, c := b.do("GET", "/ui/campaigns/"+id, nil)
	m := asMap(c)
	return asMap(m["campaign"]), asList(m["recipients"])
}

func submittedTexts(s *stack) []string {
	var out []string
	for _, m := range s.smsc.Submitted() {
		out = append(out, m.Text)
	}
	return out
}

// A campaign from a CSV whose columns fill the template: every recipient gets
// their own text, numbers that cannot be dialled are skipped (and not charged),
// and each row's outcome is reported.
func TestCampaignFromCsvWithRowFields(t *testing.T) {
	s := start(t)
	op, acct := operator(t, s), s.browser()
	acct.login("demo@example.com", "demo-pass-123")
	before, _ := s.balance("demo")
	csv := "phone,name,amount,due\n" +
		"9841234567,Asha,1250,5 Oct\n" +
		"+977 984-123-4568,Bimal,300,6 Oct\n" +
		"12345,Short,1,7 Oct\n" +
		"not a number,Bad,2,8 Oct\n" +
		"009779841234569,Chitra,75,9 Oct\n"
	body := map[string]any{"name": "Dues", "csv": csv, "on_invalid": "skip", "text": "Dear {{ name }}, NPR {{ amount }} is due on {{ due }}. {{ signature }}", "vars": map[string]any{"signature": "Thanks, Acme"}}

	// Preview: counts, the skipped rows with reasons, the first message, the columns.
	st, prev := acct.do("POST", "/ui/campaigns/preview", body)
	p := asMap(prev)
	if st != 200 || p["total"] != 5.0 || p["valid"] != 3.0 || p["skipped"] != 2.0 {
		t.Fatalf("preview = %d %v", st, prev)
	}
	if got := fmt.Sprint(p["columns"]); got != "[amount due name phone]" {
		t.Fatalf("columns = %s", got)
	}
	if text := str(asMap(p["sample"]), "text"); text != "Dear Asha, NPR 1250 is due on 5 Oct. Thanks, Acme" {
		t.Fatalf("sample text = %q", text)
	}
	reasons := map[string]string{}
	for _, r := range asList(p["skipped_rows"]) {
		reasons[str(asMap(r), "phone")] = str(asMap(r), "reason")
	}
	if reasons["12345"] != "too_short" || reasons["not a number"] != "unparsable" {
		t.Fatalf("skipped rows = %v", reasons)
	}
	// Nothing is stored by a preview.
	if _, l := acct.do("GET", "/ui/campaigns", nil); len(asList(l)) != 0 {
		t.Fatalf("a preview stored a campaign")
	}

	st, out := acct.do("POST", "/ui/campaigns", body)
	if st != 202 || asMap(out)["valid"] != 3.0 || asMap(out)["skipped"] != 2.0 {
		t.Fatalf("create = %d %v", st, out)
	}
	cid := asMap(out)["id"].(string)
	time.Sleep(300 * time.Millisecond)
	if len(s.smsc.Submitted()) != 0 {
		t.Fatalf("a message went out before approval")
	}
	approveCampaign(t, op, "approve")
	eventually(t, "campaign sent", func() bool { c, _ := campaignState(acct, cid); return c["state"] == "sent" })
	c, rows := campaignState(acct, cid)
	if c["sent"] != 3.0 || c["failed"] != 0.0 || c["skipped"] != 2.0 || c["valid"] != 3.0 || len(rows) != 5 {
		t.Fatalf("campaign = %v (%d rows)", c, len(rows))
	}
	eventually(t, "three messages at the carrier", func() bool { return len(s.smsc.Submitted()) == 3 })
	texts := submittedTexts(s)
	want := map[string]bool{
		"Dear Asha, NPR 1250 is due on 5 Oct. Thanks, Acme": true,
		"Dear Bimal, NPR 300 is due on 6 Oct. Thanks, Acme": true,
		"Dear Chitra, NPR 75 is due on 9 Oct. Thanks, Acme": true,
	}
	for _, text := range texts {
		if !want[text] {
			t.Fatalf("an unexpected text %q (all: %v)", text, texts)
		}
		delete(want, text)
	}
	if len(want) != 0 {
		t.Fatalf("missing texts %v", want)
	}
	states := map[string]string{}
	for _, r := range rows {
		m := asMap(r)
		states[str(m, "to_number")] = str(m, "state") + "/" + str(m, "reason")
		if m["state"] == "sent" && m["message_id"] == "" {
			t.Fatalf("a sent row without a message: %v", m)
		}
	}
	if states["12345"] != "skipped/too_short" || states["not a number"] != "skipped/unparsable" || states["9841234567"] != "sent/" {
		t.Fatalf("row states = %v", states)
	}
	eventually(t, "every hold settled", func() bool { _, h := s.balance("demo"); return h == 0 })
	after, held := s.balance("demo")
	if held != 0 || before-after < 0.0449 || before-after > 0.0451 {
		t.Fatalf("balance %v -> %v held %v: three messages are charged, skipped rows are not", before, after, held)
	}
}

// A template campaign: each row's fields fill the SPL template; the campaign's
// own values are defaults a row overrides.
func TestCampaignTemplateTakesRowFields(t *testing.T) {
	s := start(t)
	op, acct := operator(t, s), s.browser()
	acct.login("demo@example.com", "demo-pass-123")
	rows := []any{
		map[string]any{"mobile": "9841234567", "amount": "100", "date": "1 Oct", "receipt": "R-1"},
		map[string]any{"mobile": "9841234568", "amount": "200", "date": "2 Oct", "receipt": "R-2", "currency": "USD"},
	}
	st, out := acct.do("POST", "/ui/campaigns", map[string]any{"name": "Receipts", "rows": rows, "phone_field": "mobile", "template": "payment_received", "vars": map[string]any{"currency": "NPR"}})
	if st != 202 || asMap(out)["valid"] != 2.0 {
		t.Fatalf("create = %d %v", st, out)
	}
	approveCampaign(t, op, "approve")
	cid := asMap(out)["id"].(string)
	eventually(t, "sent", func() bool { c, _ := campaignState(acct, cid); return c["state"] == "sent" })
	eventually(t, "two messages", func() bool { return len(s.smsc.Submitted()) == 2 })
	got := strings.Join(submittedTexts(s), "|")
	for _, want := range []string{"We received your payment of NPR 100 on 1 Oct. Receipt R-1.", "We received your payment of USD 200 on 2 Oct. Receipt R-2."} {
		if !strings.Contains(got, want) {
			t.Fatalf("texts = %q, missing %q", got, want)
		}
	}
}

// A campaign from a plain list of numbers; one entry is invalid and is skipped.
func TestCampaignFromAListSkipsInvalidNumbers(t *testing.T) {
	s := start(t)
	op, acct := operator(t, s), s.browser()
	acct.login("demo@example.com", "demo-pass-123")
	st, out := acct.do("POST", "/ui/campaigns", map[string]any{"name": "List", "on_invalid": "skip", "text": "Hello everyone", "recipients": []string{"9841234567", "98412", "+9779841234568", ""}})
	if st != 202 || asMap(out)["total"] != 4.0 || asMap(out)["valid"] != 2.0 || asMap(out)["skipped"] != 2.0 {
		t.Fatalf("create = %d %v", st, out)
	}
	approveCampaign(t, op, "approve")
	cid := asMap(out)["id"].(string)
	eventually(t, "sent", func() bool { c, _ := campaignState(acct, cid); return c["state"] == "sent" })
	if c, _ := campaignState(acct, cid); c["sent"] != 2.0 || c["skipped"] != 2.0 {
		t.Fatalf("campaign = %v", c)
	}
	// Nothing valid at all, and too much, are refused when created.
	if st, _ := acct.do("POST", "/ui/campaigns", map[string]any{"name": "None", "on_invalid": "skip", "text": "x", "recipients": []string{"1", "abc"}}); st != 422 {
		t.Fatalf("no valid recipient = %d", st)
	}
	if st, _ := acct.do("POST", "/ui/campaigns", map[string]any{"name": "Empty", "text": "x", "recipients": []string{}}); st != 422 {
		t.Fatalf("no recipients = %d", st)
	}
}

// A saved audience: built once from a CSV, checked, and used for a campaign.
func TestAudienceCampaign(t *testing.T) {
	s := start(t)
	op, acct, other := operator(t, s), s.browser(), s.browser()
	acct.login("demo@example.com", "demo-pass-123")
	op.do("PUT", "/ui/admin/users/newco", map[string]any{"name": "NewCo"})
	op.do("PUT", "/ui/admin/users/newco/password", map[string]any{"email": "ops@newco.example", "password": "a-long-password-1"})
	other.login("ops@newco.example", "a-long-password-1")

	st, out := acct.do("POST", "/ui/audiences", map[string]any{"name": "Customers", "csv": "phone;name\n9841234567;Asha\n98x;Bad\n9841234568;Bimal\n"})
	a := asMap(out)
	if st != 201 || a["total"] != 3.0 || a["valid"] != 2.0 || a["skipped"] != 1.0 {
		t.Fatalf("audience = %d %v", st, out)
	}
	aid := a["id"].(string)
	_, got := acct.do("GET", "/ui/audiences/"+aid, nil)
	if members := asList(asMap(got)["members"]); len(members) != 3 {
		t.Fatalf("members = %v", got)
	}
	if st, _ := other.do("GET", "/ui/audiences/"+aid, nil); st != 404 {
		t.Fatalf("another account read the audience: %d", st)
	}
	if st, _ := other.do("POST", "/ui/campaigns", map[string]any{"name": "Steal", "audience": aid, "text": "x"}); st != 404 {
		t.Fatalf("another account used the audience: %d", st)
	}
	st, c := acct.do("POST", "/ui/campaigns", map[string]any{"name": "To customers", "audience": aid, "on_invalid": "skip", "text": "Hi {{ name }}!"})
	if st != 202 || asMap(c)["valid"] != 2.0 || asMap(c)["skipped"] != 1.0 {
		t.Fatalf("campaign from the audience = %d %v", st, c)
	}
	approveCampaign(t, op, "approve")
	cid := asMap(c)["id"].(string)
	eventually(t, "sent", func() bool { x, _ := campaignState(acct, cid); return x["state"] == "sent" })
	eventually(t, "two messages", func() bool { return len(s.smsc.Submitted()) == 2 })
	got2 := strings.Join(submittedTexts(s), "|")
	if !strings.Contains(got2, "Hi Asha!") || !strings.Contains(got2, "Hi Bimal!") {
		t.Fatalf("texts = %q", got2)
	}
	if _, l := acct.do("GET", "/ui/audiences", nil); len(asList(l)) != 1 {
		t.Fatalf("audiences = %v", l)
	}
	if st, _ := acct.do("DELETE", "/ui/audiences/"+aid, nil); st != 200 {
		t.Fatalf("delete = %d", st)
	}
	if st, _ := acct.do("GET", "/ui/audiences/"+aid, nil); st != 404 {
		t.Fatalf("deleted audience still there: %d", st)
	}
}

// Every send, not only a campaign's, is checked with the phone library.
func TestSingleSendRefusesAnInvalidPhone(t *testing.T) {
	s := start(t)
	// 9100000000 has the right length for Nepal but is no assigned range.
	st, out := s.send("demo", map[string]any{"to": "+977 9100000000", "text": "hi"})
	if st != 422 {
		t.Fatalf("an undialable number = %d %v", st, out)
	}
	if code := str(out, "error", "code"); code != "INVALID_PHONE" && code != "INVALID_NUMBER" {
		t.Fatalf("code = %s", code)
	}
	if st, out := s.send("demo", map[string]any{"to": "+977 9841234567", "text": "hi"}); st != 202 {
		t.Fatalf("a valid number = %d %v", st, out)
	}
}
