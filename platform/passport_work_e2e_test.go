package platform

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// Work management over HTTP on the Passport example: routing, personal
// queues, claims, assignment, delegation, holds, notes, external links,
// computed fields and rules, bulk operations, analytics, legal hold,
// erasure and post-commit event hooks.
func TestPassportWorkManagement(t *testing.T) {
	h := newPassportApp(t)
	citizen := h.token("jwt", "citizen-1", nil, nil)
	o1 := h.token("jwt", "officer-1", []string{"officer"}, map[string]any{"org_units": []any{"ktm"}})
	o2 := h.token("jwt", "officer-2", []string{"officer"}, map[string]any{"org_units": []any{"ktm"}})
	super := h.token("jwt", "super-1", []string{"supervisor"}, map[string]any{"org_units": []any{"bagmati"}})
	dpo := h.token("jwt", "dpo-1", []string{"dpo"}, nil)

	// Rules and computed inputs on the public page.
	app := passportApplication()
	app["request"] = map[string]any{"kind": "new", "pages": "66", "service": "fast_track", "office": "ktm-dao"}
	status, body := h.call("POST", "/api/passport/cases", citizen, map[string]any{"org_unit": "ktm", "data": app})
	if status != 201 {
		t.Fatalf("start: %d %v", status, body)
	}
	base := "/api/passport/cases/" + fmt.Sprint(dig(body, "case", "id"))
	if fee := inputsOf(body)["request.fee"]; fee["value"] != float64(20000) || fee["editable"] == true || fee["computed"] != true {
		t.Fatalf("computed fee: %v", fee)
	}
	status, body = h.call("POST", base+"/stages/application/actions/submit", citizen, map[string]any{})
	if status != 422 || !strings.Contains(fmt.Sprint(body), "request.office") || !strings.Contains(fmt.Sprint(body), "Fast track") {
		t.Fatalf("rule: %d %v", status, body)
	}
	app["request"] = map[string]any{"kind": "new", "pages": "34", "service": "regular", "office": "ktm-dao"}
	status, body = h.call("POST", base+"/stages/application/actions/submit", citizen, map[string]any{"data": app})
	if status != 200 || dig(body, "case", "stage") != "verification" {
		t.Fatalf("submit: %d %v", status, body)
	}

	// Routed to the least-loaded ktm officer, with the reason recorded.
	status, view := h.call("GET", base, o1, nil)
	if status != 200 || dig(view, "work", "assignee") != "officer-1" || !strings.Contains(fmt.Sprint(dig(view, "work", "routing", "reason")), "least loaded") {
		t.Fatalf("routing: %d %v", status, dig(view, "work"))
	}
	if dig(view, "work", "sla", "status") != "on_track" || dig(view, "work", "sla", "due_at") == nil {
		t.Fatalf("sla: %v", dig(view, "work", "sla"))
	}
	if status, body := h.call("GET", "/api/passport/cases?scope=assigned", o1, nil); status != 200 || len(body.([]any)) != 1 {
		t.Fatalf("my work: %d %v", status, body)
	}
	if status, body := h.call("GET", "/api/passport/cases?scope=assigned", o2, nil); status != 200 || len(body.([]any)) != 0 {
		t.Fatalf("o2 work: %d %v", status, body)
	}

	// Claims: officer-2 cannot act on officer-1's case.
	if status, _ := h.call("POST", base+"/stages/verification/actions/accept", o2, map[string]any{}); status != 403 {
		t.Fatalf("non-assignee act: %d", status)
	}
	// A supervisor reassigns; officer-2 delegates back; officer-1 releases.
	if status, body := h.call("POST", base+"/stages/verification/work/assign", o1, map[string]any{"to": "officer-2"}); status != 403 {
		t.Fatalf("officer assign: %d %v", status, body)
	}
	status, body = h.call("POST", base+"/stages/verification/work/assign", super, map[string]any{"to": "officer-2", "comment": "balancing"})
	if status != 200 {
		t.Fatalf("assign: %d %v", status, body)
	}
	if status, _ := h.call("POST", base+"/stages/verification/work/assign", super, map[string]any{"to": "officer-9"}); status != 422 {
		t.Fatalf("assign outside the district: %d", status)
	}
	status, body = h.call("POST", base+"/stages/verification/work/delegate", o2, map[string]any{"to": "officer-1", "comment": "I know the applicant"})
	if status != 200 || dig(body, "work", "assignee") != "officer-1" {
		t.Fatalf("delegate: %d %v", status, dig(body, "work"))
	}

	// Holds stop the work (and the SLA clock).
	status, body = h.call("POST", base+"/stages/verification/work/suspend", o1, map[string]any{"reason": "awaiting ward letter"})
	if status != 200 || dig(body, "work", "suspended", "reason") != "awaiting ward letter" || dig(body, "work", "sla", "status") != "paused" {
		t.Fatalf("suspend: %d %v", status, dig(body, "work"))
	}
	if status, _ := h.call("POST", base+"/stages/verification/actions/accept", o1, map[string]any{}); status != 409 {
		t.Fatalf("act on hold: %d", status)
	}
	if status, body := h.call("POST", base+"/stages/verification/work/resume", o1, map[string]any{"comment": "letter arrived"}); status != 200 {
		t.Fatalf("resume: %d %v", status, body)
	}

	// Notes: internal for staff, public for the applicant too.
	if status, body := h.call("POST", base+"/notes", o1, map[string]any{"body": "Photo looks older than 6 months", "internal": true}); status != 200 {
		t.Fatalf("internal note: %d %v", status, body)
	}
	if status, body := h.call("POST", base+"/notes", citizen, map[string]any{"body": "I can bring a new photo", "internal": false}); status != 200 {
		t.Fatalf("applicant note: %d %v", status, body)
	}
	if status, _ := h.call("POST", base+"/notes", citizen, map[string]any{"body": "sneaky", "internal": true}); status != 403 {
		t.Fatalf("applicant internal note: %d", status)
	}
	_, cv := h.call("GET", base, citizen, nil)
	_, ov := h.call("GET", base, o1, nil)
	if n := len(dig(cv, "notes").([]any)); n != 1 {
		t.Fatalf("applicant sees %d notes", n)
	}
	if n := len(dig(ov, "notes").([]any)); n != 2 {
		t.Fatalf("officer sees %d notes", n)
	}

	// External link: the ward office fills its recommendation, once.
	status, body = h.call("POST", base+"/stages/verification/links", o1, map[string]any{"party": "Ward 4 office", "scope": []any{"ward_recommendation"}, "ttl": "72h"})
	token, _ := dig(body, "link_token").(string)
	if status != 200 || token == "" {
		t.Fatalf("link: %d %v", status, body)
	}
	status, lv := h.call("GET", "/api/passport/links/"+token, "", nil)
	if status != 200 || dig(lv, "external", "party") != "Ward 4 office" || len(dig(lv, "page", "groups").([]any)) != 1 {
		t.Fatalf("link view: %d %v", status, lv)
	}
	if strings.Contains(fmt.Sprint(lv), "27-01-74") || strings.Contains(fmt.Sprint(lv), "asha@example.com") {
		t.Fatal("the link view leaks applicant data")
	}
	if status, _ := h.call("POST", "/api/passport/links/"+token, "", map[string]any{"data": map[string]any{"ward_recommendation": map[string]any{"recommended": true}}}); status != 422 {
		t.Fatalf("incomplete link submit: %d", status)
	}
	status, body = h.call("POST", "/api/passport/links/"+token, "", map[string]any{"data": map[string]any{
		"ward_recommendation": map[string]any{"recommended": true, "ward_officer": "R. Shrestha"},
		"applicant":           map[string]any{"full_name": "Mallory"},
	}})
	if status != 200 || dig(body, "submitted") != true {
		t.Fatalf("link submit: %d %v", status, body)
	}
	if status, _ := h.call("POST", "/api/passport/links/"+token, "", map[string]any{"data": map[string]any{}}); status != 403 {
		t.Fatalf("link reuse: %d", status)
	}
	_, ov = h.call("GET", base, o1, nil)
	if inputsOf(ov)["ward_recommendation.ward_officer"]["value"] != "R. Shrestha" || inputsOf(ov)["applicant.full_name"]["value"] != "Asha Rai" {
		t.Fatalf("link data: %v %v", inputsOf(ov)["ward_recommendation.ward_officer"], inputsOf(ov)["applicant.full_name"])
	}

	// Bulk: a note on several cases; an unknown id fails on its own.
	status, second := h.call("POST", "/api/passport/cases", citizen, map[string]any{"org_unit": "ktm", "data": passportApplication()})
	id2 := fmt.Sprint(dig(second, "case", "id"))
	h.call("POST", "/api/passport/cases/"+id2+"/stages/application/actions/submit", citizen, map[string]any{})
	id1 := strings.TrimPrefix(base, "/api/passport/cases/")
	status, body = h.call("POST", "/api/passport/bulk", super, map[string]any{
		"ids": []any{id1, id2, "case_nope"}, "op": "note", "input": map[string]any{"body": "Office closed on Friday", "internal": true},
	})
	if status != 200 || dig(body, "applied") != float64(2) || dig(body, "failed") != float64(1) {
		t.Fatalf("bulk: %d %v", status, body)
	}

	// Analytics and the sweep.
	status, rep := h.call("GET", "/api/passport/analytics", super, nil)
	if status != 200 || dig(rep, "open") != float64(2) {
		t.Fatalf("analytics: %d %v", status, rep)
	}
	if status, _ := h.call("GET", "/api/passport/analytics", o1, nil); status != 403 {
		t.Fatalf("analytics without the role: %d", status)
	}
	if status, body := h.call("POST", "/api/passport/sweep", o1, map[string]any{}); status != 200 || dig(body, "scanned") != float64(2) {
		t.Fatalf("sweep: %d %v", status, body)
	}

	// Right to erasure, blocked by a legal hold until it is released.
	erase := map[string]any{"identifiers": map[string]any{"contact.email": "ASHA@example.com"}, "mode": "anonymize", "reason": "request DSR-7"}
	if status, _ := h.call("POST", "/api/passport/erasure", o1, erase); status != 403 {
		t.Fatalf("erasure without the dpo role: %d", status)
	}
	if status, body := h.call("POST", base+"/hold/place", super, map[string]any{"reason": "court order 17"}); status != 200 {
		t.Fatalf("hold: %d %v", status, body)
	}
	status, body = h.call("POST", "/api/passport/erasure", dpo, erase)
	outcomes := fmt.Sprint(body)
	if status != 200 || dig(body, "matched") != float64(2) || !strings.Contains(outcomes, "blocked") || !strings.Contains(outcomes, "anonymized") {
		t.Fatalf("erasure with a hold: %d %v", status, body)
	}
	if ref, _ := dig(body, "subject_ref").(string); len(ref) != 64 || strings.Contains(outcomes, "asha@") {
		t.Fatalf("receipt leaks the subject: %v", body)
	}

	// Event hooks recorded notifications after each committed change. They
	// are delivered from the durable outbox, asynchronously.
	deadline := time.Now().Add(5 * time.Second)
	for {
		status, body = h.call("GET", "/api/passport/notifications", o1, nil)
		if status == 200 && strings.Contains(fmt.Sprint(body), "link.submitted") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("notifications: %d %v", status, body)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
