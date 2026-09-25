package dos

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestParseDateFormats(t *testing.T) {
	for _, in := range []string{"2026-03-04", "03/04/2026", "3/4/2026", "20260304", "2026/03/04", "2026-03-04T23:30:00-08:00"} {
		d, err := ParseDate(in)
		if err != nil || d.String() != "2026-03-04" {
			t.Fatalf("%q -> %v, %v", in, d, err)
		}
	}
	for _, bad := range []string{"", "2026-02-30", "yesterday", "13/01/2026"} {
		if _, err := ParseDate(bad); !errors.Is(err, ErrInvalidDate) {
			t.Fatalf("%q should fail, got %v", bad, err)
		}
	}
}

func TestPeriodBasics(t *testing.T) {
	p, err := ParsePeriod("2026-01-30", "2026-02-02")
	if err != nil {
		t.Fatal(err)
	}
	if p.Kind() != KindMulti || p.Days() != 4 || len(p.Dates()) != 4 || p.Dates()[3].String() != "2026-02-02" {
		t.Fatalf("period = %v days=%d", p, p.Days())
	}
	single, _ := ParsePeriod("2026-01-30", "")
	if single.Kind() != KindSingle || single.Days() != 1 {
		t.Fatalf("single = %v", single)
	}
	if _, err := ParsePeriod("2026-02-02", "2026-01-30"); !errors.Is(err, ErrInvalidPeriod) {
		t.Fatalf("reversed period: %v", err)
	}
	split := p.SplitByMonth()
	if len(split) != 2 || split[0].String() != "2026-01-30..2026-01-31" || split[1].String() != "2026-02-01..2026-02-02" {
		t.Fatalf("split = %v", split)
	}
	// Leap-year month end.
	leap, _ := ParsePeriod("2028-02-28", "2028-03-01")
	if s := leap.SplitByMonth(); len(s) != 2 || s[0].To.String() != "2028-02-29" {
		t.Fatalf("leap split = %v", s)
	}
}

func TestOverlapAndMerge(t *testing.T) {
	a := Period{MustParseDate("2026-01-01"), MustParseDate("2026-01-05")}
	b := Period{MustParseDate("2026-01-05"), MustParseDate("2026-01-07")}
	c := Period{MustParseDate("2026-01-08"), MustParseDate("2026-01-08")}
	d := Period{MustParseDate("2026-01-20"), MustParseDate("2026-01-21")}
	if !a.Overlaps(b) || b.Overlaps(c) {
		t.Fatal("overlap wrong")
	}
	if in, ok := a.Intersect(b); !ok || in.String() != "2026-01-05" {
		t.Fatalf("intersect = %v", in)
	}
	merged := Merge([]Period{d, c, a, b})
	if len(merged) != 2 || merged[0].String() != "2026-01-01..2026-01-08" || merged[1] != d {
		t.Fatalf("merged = %v", merged)
	}
	conflicts := Conflicts(Period{MustParseDate("2026-01-04"), MustParseDate("2026-01-06")}, []Period{a, c, b})
	if len(conflicts) != 2 || conflicts[0].Index != 0 || conflicts[1].Index != 2 || conflicts[1].Overlap.String() != "2026-01-05..2026-01-06" {
		t.Fatalf("conflicts = %+v", conflicts)
	}
}

func TestRules(t *testing.T) {
	today := MustParseDate("2026-06-15")
	rules := Rules{AllowMulti: true, MaxSpanDays: 30, MaxAgeDays: 90, SameMonth: true,
		Weekdays: []time.Weekday{time.Monday, time.Tuesday, time.Wednesday, time.Thursday, time.Friday}}
	ok, _ := ParsePeriod("2026-06-01", "2026-06-05") // Mon..Fri
	if v := rules.Validate(ok, today); len(v) != 0 {
		t.Fatalf("unexpected violations %+v", v)
	}
	bad, _ := ParsePeriod("2026-02-27", "2026-03-02")
	rulesSeen := map[string]bool{}
	for _, v := range rules.Validate(bad, today) {
		rulesSeen[v.Rule] = true
	}
	for _, want := range []string{"max_age", "same_month", "weekday"} {
		if !rulesSeen[want] {
			t.Fatalf("missing %s in %v", want, rulesSeen)
		}
	}
	future, _ := ParsePeriod("2026-06-16", "")
	if v := (Rules{}).Validate(future, today); len(v) != 1 || v[0].Rule != "future" {
		t.Fatalf("future: %+v", v)
	}
	if v := (Rules{AllowFuture: true}).Validate(ok, today); len(v) != 1 || v[0].Rule != "single_only" {
		t.Fatalf("single only: %+v", v)
	}
	lines := []Line{
		{Period: Single(MustParseDate("2026-06-02")), Units: 1},
		{Period: Single(MustParseDate("2026-06-09")), Units: -1},
	}
	lv := rules.ValidateLines(ok, lines, today)
	if len(lv) != 2 || lv[0].Line != 1 || lv[1].Line != 1 {
		t.Fatalf("line violations %+v", lv)
	}
}

func TestExpand(t *testing.T) {
	three, _ := ParsePeriod("2026-06-01", "2026-06-03")
	lines := []Line{
		{Period: three, Units: 7, Data: map[string]any{"cpt": "99232"}},
		{Period: three, Units: 1.5},
		{Period: Single(MustParseDate("2026-06-02")), Units: 2},
	}
	got, err := Expand(lines, ExpandDistribute)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 7 {
		t.Fatalf("rows = %d", len(got))
	}
	if got[0].Units != 3 || got[1].Units != 2 || got[2].Units != 2 || got[0].Data["cpt"] != "99232" {
		t.Fatalf("integer distribution = %+v", got[:3])
	}
	if got[3].Units != 0.5 || got[6].Date.String() != "2026-06-02" {
		t.Fatalf("fractional/single = %+v", got[3:])
	}
	per, _ := Expand(lines[:1], ExpandPerDay)
	if len(per) != 3 || per[2].Units != 7 || per[2].Date.String() != "2026-06-03" {
		t.Fatalf("per day = %+v", per)
	}
	if _, err := Expand(lines, "weekly"); err == nil {
		t.Fatal("unknown mode should fail")
	}
}

func TestDateJSON(t *testing.T) {
	p := Period{MustParseDate("2026-01-02"), MustParseDate("2026-01-03")}
	b, _ := json.Marshal(p)
	if string(b) != `{"from":"2026-01-02","to":"2026-01-03"}` {
		t.Fatalf("json = %s", b)
	}
	var back Period
	if err := json.Unmarshal([]byte(`{"from":"01/02/2026","to":"2026-01-03"}`), &back); err != nil || back != p {
		t.Fatalf("unmarshal = %v %v", back, err)
	}
}
