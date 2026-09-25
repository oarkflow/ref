package platform

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

// The Passport Request example end to end over HTTP: an anonymous applicant
// fills the public wizard, an officer verifies and returns a field, the
// applicant corrects only that field, biometrics, four-eyes approval, and a
// certificate anyone can verify.

func newPassportApp(t *testing.T) *appHarness {
	return newAppHarness(t, "../examples/passport/app.bcl", map[string]string{
		"PASSPORT_DSN":            "file:" + t.TempDir() + "/passport.db?_pragma=busy_timeout(5000)",
		"PASSPORT_JWT_SECRET":     "passport-test-jwt-secret-0123456789abcdef",
		"PASSPORT_SIGNING_SECRET": "passport-test-signing-secret-0123456789",
	})
}

// callKey is call with the anonymous applicant's access key.
func (h *appHarness) callKey(method, path, key string, body any) (int, any) {
	h.t.Helper()
	var reader io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		reader = strings.NewReader(string(raw))
	}
	req, _ := http.NewRequest(method, h.base+path, reader)
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("X-Access-Key", key)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		h.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var decoded any
	_ = json.Unmarshal(raw, &decoded)
	return resp.StatusCode, decoded
}

func passportApplication() map[string]any {
	return map[string]any{
		"applicant":   map[string]any{"full_name": "Asha Rai", "dob": "1990-05-01", "gender": "female", "citizenship_no": "27-01-74-01234"},
		"contact":     map[string]any{"email": "asha@example.com", "phone": "+977 9800000000"},
		"addresses":   []any{map[string]any{"kind": "permanent", "line": "Ward 4, Baneshwor", "municipality": "Kathmandu"}},
		"request":     map[string]any{"kind": "new", "pages": "34", "service": "regular", "office": "ktm-dao"},
		"documents":   map[string]any{"photo": "upload://photo.jpg", "citizenship_scan": "upload://citizenship.pdf"},
		"declaration": map[string]any{"agree": true},
	}
}

// inputsOf flattens a view's page into path -> input view.
func inputsOf(view any) map[string]map[string]any {
	out := map[string]map[string]any{}
	groups, _ := dig(view, "page", "groups").([]any)
	for _, g := range groups {
		forms, _ := dig(g, "forms").([]any)
		for _, f := range forms {
			inputs, _ := dig(f, "inputs").([]any)
			for _, in := range inputs {
				m := in.(map[string]any)
				// A form shown in several groups (e.g. a summary) keeps the
				// editable rendering.
				if prev, ok := out[fmt.Sprint(m["path"])]; ok && prev["editable"] == true {
					continue
				}
				out[fmt.Sprint(m["path"])] = m
			}
		}
	}
	return out
}

func TestPassportPipelineEndToEnd(t *testing.T) {
	h := newPassportApp(t)
	officer := h.token("jwt", "officer-1", []string{"officer"}, map[string]any{"org_units": []any{"ktm"}})
	farOfficer := h.token("jwt", "officer-9", []string{"officer"}, map[string]any{"org_units": []any{"morang"}})
	super := h.token("jwt", "super-1", []string{"supervisor"}, map[string]any{"org_units": []any{"bagmati"}})
	superAsOfficer := h.token("jwt", "officer-1", []string{"supervisor"}, map[string]any{"org_units": []any{"ktm"}})

	// --- The public page: an anonymous applicant starts a case. ---
	status, started := h.callKey("POST", "/api/passport/cases", "", map[string]any{"org_unit": "ktm"})
	if status != 201 {
		t.Fatalf("start: %d %v", status, started)
	}
	key, _ := dig(started, "access_key").(string)
	id, _ := dig(started, "case", "id").(string)
	if key == "" || id == "" || !strings.HasPrefix(fmt.Sprint(dig(started, "case", "number")), "PP-") {
		t.Fatalf("start response: %v", started)
	}
	if dig(started, "page", "layout") != "wizard" {
		t.Fatalf("page layout: %v", dig(started, "page", "layout"))
	}
	groups := dig(started, "page", "groups").([]any)
	if len(groups) != 5 || dig(groups, 2, "layout") != "tabbed" || dig(groups, 3, "mode") != "summary" {
		t.Fatalf("groups: %v", groups)
	}
	// The office list is reference data resolved for the case's district.
	choices := fmt.Sprint(inputsOf(started)["request.office"]["choices"])
	if !strings.Contains(choices, "ktm-dao") || strings.Contains(choices, "mrg-dao") || !strings.Contains(choices, "dop") {
		t.Fatalf("office choices for ktm: %v", choices)
	}

	base := "/api/passport/cases/" + id
	// The case is private: no key, no access.
	if status, _ := h.callKey("GET", base, "", nil); status != 403 && status != 404 {
		t.Fatalf("view without key: %d", status)
	}
	if status, _ := h.callKey("GET", base, "wrong-key", nil); status != 403 && status != 404 {
		t.Fatalf("view with wrong key: %d", status)
	}

	// Field-level validation, all at once, nothing written.
	bad := passportApplication()
	bad["request"] = map[string]any{"kind": "new", "pages": "34", "service": "regular", "office": "mrg-dao"}
	bad["contact"] = map[string]any{"email": "not-an-email", "phone": "+977 9800000000"}
	status, body := h.callKey("PUT", base+"/stages/application", key, map[string]any{"data": bad})
	if status != 422 {
		t.Fatalf("invalid save: %d %v", status, body)
	}
	if details := fmt.Sprint(body); !strings.Contains(details, "request.office") || !strings.Contains(details, "contact.email") {
		t.Fatalf("validation details: %v", body)
	}

	// Save a draft, then submit.
	if status, body := h.callKey("PUT", base+"/stages/application", key, map[string]any{"data": passportApplication()}); status != 200 {
		t.Fatalf("save: %d %v", status, body)
	}
	status, body = h.callKey("POST", base+"/stages/application/actions/submit", key, map[string]any{})
	if status != 200 || dig(body, "case", "stage") != "verification" {
		t.Fatalf("submit: %d %v", status, body)
	}
	// The applicant now sees their application read-only, plus progress.
	status, body = h.callKey("GET", base, key, nil)
	if status != 200 || dig(body, "stage", "name") != "application" || dig(body, "can_edit") != false {
		t.Fatalf("applicant view after submit: %d %v", status, body)
	}

	// --- Officer work queue, scoped to jurisdiction. ---
	status, body = h.call("GET", "/api/passport/cases", officer, nil)
	if list, _ := body.([]any); status != 200 || len(list) != 1 || dig(list, 0, "stage") != "verification" {
		t.Fatalf("ktm officer queue: %d %v", status, body)
	}
	if status, body := h.call("GET", "/api/passport/cases", farOfficer, nil); status != 200 || len(body.([]any)) != 0 {
		t.Fatalf("morang officer queue: %d %v", status, body)
	}
	if status, _ := h.call("GET", base, farOfficer, nil); status != 404 {
		t.Fatalf("out-of-jurisdiction view: %d", status)
	}

	status, view := h.call("GET", base, officer, nil)
	if status != 200 || dig(view, "page", "layout") != "tabbed" {
		t.Fatalf("officer view: %d %v", status, view)
	}
	cit := inputsOf(view)["applicant.citizenship_no"]
	if cit["masked"] != true || cit["editable"] == true || strings.Contains(fmt.Sprint(cit["value"]), "27-01") {
		t.Fatalf("sensitive input for officer: %v", cit)
	}
	if inputsOf(view)["officer_check.notes"]["editable"] != true {
		t.Fatal("officer notes should be editable")
	}
	// The watchlist automation ran through its intent on entry.
	nodes := map[string]any{}
	for _, n := range dig(view, "nodes").([]any) {
		nodes[fmt.Sprint(dig(n, "name"))] = dig(n, "status")
	}
	if nodes["watchlist"] != "passed" || nodes["adult_or_guardian"] != "passed" || nodes["review_identity"] != "pending" {
		t.Fatalf("verification nodes: %v", nodes)
	}

	node := func(tok, stage, name, verb string, in map[string]any) (int, any) {
		return h.call("POST", fmt.Sprintf("%s/stages/%s/nodes/%s/%s", base, stage, name, verb), tok, in)
	}
	// Verify identity, flag the date of birth, return for correction.
	verdicts := map[string]any{
		"applicant.full_name": map[string]any{"status": "verified"}, "applicant.gender": map[string]any{"status": "verified"},
		"applicant.citizenship_no": map[string]any{"status": "verified"},
		"applicant.dob":            map[string]any{"status": "flagged", "comment": "does not match the citizenship certificate"},
	}
	if status, body := node(officer, "verification", "review_identity", "verify", map[string]any{"verdicts": verdicts}); status != 200 {
		t.Fatalf("verify: %d %v", status, body)
	}
	if status, body := h.call("POST", base+"/stages/verification/actions/return", officer, map[string]any{}); status != 422 {
		t.Fatalf("return without comment: %d %v", status, body)
	}
	status, body = h.call("POST", base+"/stages/verification/actions/return", officer, map[string]any{"comment": "please fix your date of birth"})
	if status != 200 {
		t.Fatalf("return: %d %v", status, body)
	}

	// Correction: only the flagged field is editable.
	status, body = h.callKey("GET", base, key, nil)
	editable := []string{}
	for path, in := range inputsOf(body) {
		if in["editable"] == true {
			editable = append(editable, path)
		}
	}
	if status != 200 || len(editable) != 1 || editable[0] != "applicant.dob" {
		t.Fatalf("correction mode: %d editable=%v", status, editable)
	}
	status, body = h.callKey("POST", base+"/stages/application/actions/submit", key, map[string]any{
		"data": map[string]any{"applicant": map[string]any{"dob": "1990-06-01", "full_name": "Someone Else"}},
	})
	if status != 200 || dig(body, "case", "stage") != "verification" {
		t.Fatalf("resubmit: %d %v", status, body)
	}

	// Re-verify the corrected field, verify the request, accept.
	if status, body := node(officer, "verification", "review_identity", "verify", map[string]any{"verdicts": map[string]any{"applicant.dob": map[string]any{"status": "verified"}}}); status != 200 {
		t.Fatalf("re-verify: %d %v", status, body)
	}
	if status, body := node(officer, "verification", "review_identity", "complete", map[string]any{}); status != 200 {
		t.Fatalf("complete identity review: %d %v", status, body)
	}
	reqVerdicts := map[string]any{}
	for _, p := range []string{"request.kind", "request.pages", "request.service", "request.office", "documents.photo", "documents.citizenship_scan"} {
		reqVerdicts[p] = map[string]any{"status": "verified"}
	}
	if status, body := node(officer, "verification", "review_request", "verify", map[string]any{"verdicts": reqVerdicts}); status != 200 {
		t.Fatalf("verify request: %d %v", status, body)
	}
	if status, body := node(officer, "verification", "review_request", "complete", map[string]any{}); status != 200 {
		t.Fatalf("complete request review: %d %v", status, body)
	}
	status, body = h.call("POST", base+"/stages/verification/actions/accept", officer, map[string]any{
		"data": map[string]any{"officer_check": map[string]any{"documents_seen": true, "notes": "originals seen"}},
	})
	if status != 200 || dig(body, "case", "stage") != "biometrics" {
		t.Fatalf("accept: %d %v", status, body)
	}

	// Biometrics: each capture is a node; the optional signature is not needed.
	for _, name := range []string{"photo", "fingerprints"} {
		status, body = node(officer, "biometrics", name, "complete", map[string]any{"result": map[string]any{"station": "K-2"}})
		if status != 200 {
			t.Fatalf("capture %s: %d %v", name, status, body)
		}
	}
	if dig(body, "case", "stage") != "approval" {
		t.Fatalf("biometrics should auto-advance: %v", dig(body, "case"))
	}

	// Four-eyes: the officer who verified may not sign off, even holding the
	// supervisor role.
	if status, body := node(superAsOfficer, "approval", "sign_off", "approve", map[string]any{}); status != 403 {
		t.Fatalf("four-eyes: %d %v", status, body)
	}
	status, view = h.call("GET", base, super, nil)
	if status != 200 || inputsOf(view)["applicant.citizenship_no"]["masked"] == true {
		t.Fatalf("supervisor reveal: %d %v", status, inputsOf(view)["applicant.citizenship_no"])
	}
	if status, body := node(super, "approval", "sign_off", "approve", map[string]any{"comment": "ok"}); status != 200 {
		t.Fatalf("sign off: %d %v", status, body)
	}
	if status, body := h.call("POST", base+"/stages/approval/actions/approve", super, map[string]any{}); status != 200 {
		t.Fatalf("approve: %d %v", status, body)
	}

	// Issuance ran by itself: the case is complete with a certificate.
	status, body = h.callKey("GET", base+"/history", key, nil)
	if status != 200 || dig(body, "status") != "completed" {
		t.Fatalf("final case: %d %v", status, body)
	}
	certs, _ := dig(body, "certificates").([]any)
	if len(certs) != 1 {
		t.Fatalf("certificates: %v", certs)
	}
	if dig(certs, 0, "subject", "applicant.dob") != "1990-06-01" || dig(certs, 0, "subject", "applicant.full_name") != "Asha Rai" {
		t.Fatalf("certificate subject: %v", dig(certs, 0, "subject"))
	}

	// Anyone can verify it, by number or by code.
	for _, k := range []string{fmt.Sprint(dig(certs, 0, "number")), fmt.Sprint(dig(certs, 0, "code"))} {
		status, body = h.call("GET", "/api/passport/certificates/"+k, "", nil)
		if status != 200 || dig(body, "valid") != true {
			t.Fatalf("verify %s: %d %v", k, status, body)
		}
	}
	if status, body := h.call("GET", "/api/passport/certificates/NOPE-0000", "", nil); status != 200 || dig(body, "valid") != false {
		t.Fatalf("verify unknown: %d %v", status, body)
	}

	// The case is closed to further changes.
	if status, _ := h.callKey("POST", base+"/stages/application/actions/submit", key, map[string]any{}); status != 409 {
		t.Fatalf("act on completed case: %d", status)
	}
}

func TestPassportRenewalRejectAndSignedInApplicant(t *testing.T) {
	h := newPassportApp(t)
	citizen := h.token("jwt", "citizen-7", nil, nil)
	officer := h.token("jwt", "officer-1", []string{"officer"}, map[string]any{"org_units": []any{"bagmati"}})

	app := passportApplication()
	app["request"] = map[string]any{"kind": "renewal", "old_passport": "PA1234567", "pages": "66", "service": "fast_track", "office": "dop"}
	status, body := h.call("POST", "/api/passport/cases", citizen, map[string]any{"org_unit": "lalitpur", "data": app})
	if status != 201 || dig(body, "access_key") != nil {
		t.Fatalf("signed-in start: %d %v", status, body)
	}
	base := "/api/passport/cases/" + fmt.Sprint(dig(body, "case", "id"))

	if status, body := h.call("GET", "/api/passport/cases?scope=mine", citizen, nil); status != 200 || len(body.([]any)) != 1 {
		t.Fatalf("mine: %d %v", status, body)
	}
	// A stale revision is a conflict, not a silent overwrite.
	if status, _ := h.call("POST", base+"/stages/application/actions/submit", citizen, map[string]any{"revision": 999}); status != 409 {
		t.Fatalf("stale revision: %d", status)
	}
	if status, body := h.call("POST", base+"/stages/application/actions/submit", citizen, map[string]any{}); status != 200 {
		t.Fatalf("submit: %d %v", status, body)
	}
	// A citizen cannot act on the officer's stage.
	if status, _ := h.call("POST", base+"/stages/verification/actions/accept", citizen, map[string]any{}); status != 403 {
		t.Fatalf("citizen accept: %d", status)
	}
	status, body = h.call("POST", base+"/stages/verification/actions/reject", officer, map[string]any{"comment": "citizenship number does not exist"})
	if status != 200 || dig(body, "case", "status") != "rejected" {
		t.Fatalf("reject: %d %v", status, body)
	}
	status, body = h.call("GET", base+"/history", citizen, nil)
	if status != 200 || dig(body, "status") != "rejected" {
		t.Fatalf("history: %d %v", status, body)
	}
}
