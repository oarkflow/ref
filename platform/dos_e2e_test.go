package platform

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/oarkflow/ref/dos"
)

func TestMedicalCodingExample(t *testing.T) {
	dir := t.TempDir()
	h := newAppHarness(t, "../examples/medical-coding/app.bcl", map[string]string{
		"CODING_DSN":        "file:" + filepath.Join(dir, "coding.db"),
		"CODING_JWT_SECRET": strings.Repeat("m", 40),
	})
	acme := h.token("jwt", "coder-1", []string{"coder"}, map[string]any{"tenant_id": "acme-billing"})
	globex := h.token("jwt", "coder-9", []string{"coder"}, map[string]any{"tenant_id": "globex-billing"})
	noTenant := h.token("jwt", "coder-x", []string{"coder"}, nil)

	today := dos.Today(time.Now(), time.UTC)
	day := func(offset int) string { return today.AddDays(offset).String() }

	t.Run("single DOS office visit", func(t *testing.T) {
		status, body := h.call("POST", "/api/encounters", acme, map[string]any{
			"patient_id": "P1", "provider_id": "DR1", "dos": day(-10),
			"lines": []any{
				map[string]any{"cpt": "99213", "icd": "J06.9", "units": 1},
				map[string]any{"cpt": "87880", "icd": "J02.0", "units": 1},
			},
		})
		if status != 201 {
			t.Fatalf("create = %d %v", status, body)
		}
		if dig(body, "saved", "parent", "dos_kind") != "single" || Stringify(dig(body, "saved", "inserted")) != "2" {
			t.Fatalf("single encounter = %v", body)
		}
	})

	var multiID string
	t.Run("multi DOS inpatient stay expands per day", func(t *testing.T) {
		status, body := h.call("POST", "/api/encounters", acme, map[string]any{
			"patient_id": "P2", "provider_id": "DR1", "dos_from": day(-20), "dos_to": day(-17),
			"lines": []any{
				// Subsequent hospital care every day of the stay.
				map[string]any{"cpt": "99232", "icd": "I50.9", "units": 1},
				// A procedure on one specific day inside the stay.
				map[string]any{"cpt": "93306", "icd": "I50.9", "units": 1, "dos": day(-18)},
			},
		})
		if status != 201 {
			t.Fatalf("create = %d %v", status, body)
		}
		if dig(body, "saved", "parent", "dos_kind") != "multi" || Stringify(dig(body, "saved", "parent", "days")) != "4" {
			t.Fatalf("encounter = %v", dig(body, "saved"))
		}
		// 4 daily visit lines + 1 procedure line.
		if Stringify(dig(body, "saved", "inserted")) != "5" {
			t.Fatalf("lines = %v", dig(body, "saved"))
		}
		multiID = Stringify(dig(body, "saved", "parent", "id"))

		status, lines := h.call("GET", "/api/encounters/"+multiID+"/lines", acme, nil)
		list, _ := lines.([]any)
		if status != 200 || len(list) != 5 || dig(list, 0, "dos") != day(-20) || dig(list, 4, "dos") != day(-17) {
			t.Fatalf("stored lines = %d %v", status, lines)
		}
		echo := 0
		for _, l := range list {
			if dig(l, "cpt") == "93306" {
				echo++
				if dig(l, "dos") != day(-18) {
					t.Fatalf("procedure on wrong date: %v", l)
				}
			}
		}
		if echo != 1 {
			t.Fatalf("procedure lines = %d", echo)
		}
	})

	t.Run("overlapping DOS is duplicate billing", func(t *testing.T) {
		status, body := h.call("POST", "/api/encounters", acme, map[string]any{
			"patient_id": "P2", "provider_id": "DR1", "dos": day(-18),
			"lines": []any{map[string]any{"cpt": "99232", "icd": "I50.9"}},
		})
		if status != 409 || dig(body, "error", "code") != "DOS_OVERLAP" {
			t.Fatalf("overlap = %d %v", status, body)
		}
		if Stringify(dig(body, "error", "details", 0, "id")) != multiID {
			t.Fatalf("conflict should name the existing encounter: %v", body)
		}
		// A different provider on the same day is a separate encounter.
		status, _ = h.call("POST", "/api/encounters", acme, map[string]any{
			"patient_id": "P2", "provider_id": "DR2", "dos": day(-18),
			"lines": []any{map[string]any{"cpt": "99254", "icd": "I50.9"}},
		})
		if status != 201 {
			t.Fatalf("different provider = %d", status)
		}
	})

	t.Run("rule violations are reported together", func(t *testing.T) {
		status, body := h.call("POST", "/api/encounters", acme, map[string]any{
			"patient_id": "P3", "provider_id": "DR1", "dos_from": day(-5), "dos_to": day(2),
			"lines": []any{map[string]any{"cpt": "99232", "icd": "R07.9", "dos": day(-9)}},
		})
		if status != 422 || dig(body, "error", "code") != "INVALID_DATE_OF_SERVICE" {
			t.Fatalf("invalid = %d %v", status, body)
		}
		rules := map[string]bool{}
		for _, d := range dig(body, "error", "details").([]any) {
			rules[Stringify(dig(d, "rule"))] = true
		}
		if !rules["future"] || !rules["line_outside_period"] {
			t.Fatalf("expected future + line_outside_period, got %v", rules)
		}
		if status, _ := h.call("POST", "/api/encounters", acme, map[string]any{
			"patient_id": "P3", "provider_id": "DR1", "dos": day(-400),
			"lines": []any{map[string]any{"cpt": "99213", "icd": "R07.9"}},
		}); status != 422 {
			t.Fatalf("timely filing = %d", status)
		}
		if status, _ := h.call("POST", "/api/encounters", acme, map[string]any{
			"patient_id": "P3", "provider_id": "DR1", "dos_from": day(-3), "dos_to": day(-5),
			"lines": []any{map[string]any{"cpt": "99213", "icd": "R07.9"}},
		}); status != 422 {
			t.Fatalf("reversed period = %d", status)
		}
		// Nothing half-saved: the failed requests left no encounter for P3.
		_, list := h.call("GET", "/api/encounters", acme, nil)
		for _, e := range list.([]any) {
			if dig(e, "patient_id") == "P3" {
				t.Fatalf("a rejected encounter was stored: %v", e)
			}
		}
	})

	t.Run("preview validates without saving", func(t *testing.T) {
		status, body := h.call("POST", "/api/encounters/preview", acme, map[string]any{
			"patient_id": "P4", "provider_id": "DR1", "dos_from": day(-40), "dos_to": day(-38),
			"lines": []any{map[string]any{"cpt": "97110", "icd": "M54.5", "units": 2}},
		})
		if status != 200 || dig(body, "validation", "valid") != true || dig(body, "period", "days") != float64(3) {
			t.Fatalf("preview = %d %v", status, body)
		}
		if lines := dig(body, "lines").([]any); len(lines) != 3 || dig(lines, 0, "units") != float64(2) {
			t.Fatalf("per-day units = %v", lines)
		}
		status, body = h.call("POST", "/api/encounters/preview", acme, map[string]any{
			"patient_id": "P4", "provider_id": "DR1", "dos": day(5),
			"lines": []any{map[string]any{"cpt": "97110", "icd": "M54.5"}},
		})
		if status != 200 || dig(body, "validation", "valid") != false {
			t.Fatalf("preview should report, not fail: %d %v", status, body)
		}
	})

	t.Run("tenant isolation", func(t *testing.T) {
		_, mine := h.call("GET", "/api/encounters", acme, nil)
		if n := len(mine.([]any)); n != 3 {
			t.Fatalf("acme sees %d encounters, want 3", n)
		}
		_, theirs := h.call("GET", "/api/encounters", globex, nil)
		if n := len(theirs.([]any)); n != 0 {
			t.Fatalf("globex sees %d encounters, want 0", n)
		}
		if _, lines := h.call("GET", "/api/encounters/"+multiID+"/lines", globex, nil); len(lines.([]any)) != 0 {
			t.Fatalf("globex read acme's lines: %v", lines)
		}
		// The same patient/provider/date under another tenant is not a duplicate.
		status, _ := h.call("POST", "/api/encounters", globex, map[string]any{
			"patient_id": "P2", "provider_id": "DR1", "dos": day(-18),
			"lines": []any{map[string]any{"cpt": "99232", "icd": "I50.9"}},
		})
		if status != 201 {
			t.Fatalf("globex create = %d", status)
		}
		if status, _ := h.call("GET", "/api/encounters", noTenant, nil); status != 403 {
			t.Fatalf("no tenant should be rejected, got %d", status)
		}
	})
}
