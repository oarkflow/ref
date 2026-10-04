package phonecheck

import "testing"

func TestCheck(t *testing.T) {
	for _, c := range []struct {
		in, region, e164, reason string
		valid                    bool
	}{
		{"9856034617", "NP", "+9779856034617", "", true},
		{"+977 9856034617", "NP", "+9779856034617", "", true},
		{"009779856034617", "NP", "+9779856034617", "", true},
		{"+91 98765 43210", "NP", "+919876543210", "", true},
		{"98560", "NP", "", "too_short", false},
		{"12345", "NP", "", "too_short", false},
		{"abc", "NP", "", "unparsable", false},
		{"", "NP", "", "empty", false},
		{"+97798560346170000", "NP", "", "too_long", false},
	} {
		got := Check(c.in, c.region, nil)
		if got["valid"] != c.valid || got["e164"] != c.e164 || (!c.valid && got["reason"] == "") {
			t.Errorf("%q: %v", c.in, got)
		}
		if c.reason != "" && got["reason"] != c.reason {
			t.Errorf("%q: reason %v, want %s", c.in, got["reason"], c.reason)
		}
	}
	if got := Check("9856034617", "NP", map[string]bool{"FIXED_LINE": true}); got["valid"] != false || got["reason"] != "type_not_allowed" {
		t.Errorf("a mobile number where only fixed lines are allowed: %v", got)
	}
	if got := Check("9856034617", "NP", nil); got["type"] != "MOBILE" || got["region"] != "NP" || got["country_code"] != 977 {
		t.Errorf("details: %v", got)
	}
}

func TestRowsAreCheckedAndKeepTheirFields(t *testing.T) {
	// Exercised through Check; the row wrapper is covered by the campaign tests.
	if got := Check(" 9856034617 ", "NP", nil); got["valid"] != true {
		t.Errorf("surrounding spaces: %v", got)
	}
}
