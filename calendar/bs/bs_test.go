package bs

import (
	"errors"
	"testing"
	"time"
)

func ad(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse("2006-01-02", s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// newYears is 1 Baisakh of every supported year. BS 2000–2090 were produced
// by the bikram-sambat npm package's own converter (an independent
// implementation with a different epoch, BS 1970), and BS 2088–2100 by the
// nepali_datetime PyPI package's converter; they agree where they overlap.
var newYears = []struct {
	year int
	ad   string
}{
	{2000, "1943-04-14"}, {2001, "1944-04-13"}, {2002, "1945-04-13"}, {2003, "1946-04-13"},
	{2004, "1947-04-14"}, {2005, "1948-04-13"}, {2006, "1949-04-13"}, {2007, "1950-04-13"},
	{2008, "1951-04-14"}, {2009, "1952-04-13"}, {2010, "1953-04-13"}, {2011, "1954-04-13"},
	{2012, "1955-04-14"}, {2013, "1956-04-13"}, {2014, "1957-04-13"}, {2015, "1958-04-13"},
	{2016, "1959-04-14"}, {2017, "1960-04-13"}, {2018, "1961-04-13"}, {2019, "1962-04-13"},
	{2020, "1963-04-14"}, {2021, "1964-04-13"}, {2022, "1965-04-13"}, {2023, "1966-04-13"},
	{2024, "1967-04-14"}, {2025, "1968-04-13"}, {2026, "1969-04-13"}, {2027, "1970-04-14"},
	{2028, "1971-04-14"}, {2029, "1972-04-13"}, {2030, "1973-04-13"}, {2031, "1974-04-14"},
	{2032, "1975-04-14"}, {2033, "1976-04-13"}, {2034, "1977-04-13"}, {2035, "1978-04-14"},
	{2036, "1979-04-14"}, {2037, "1980-04-13"}, {2038, "1981-04-13"}, {2039, "1982-04-14"},
	{2040, "1983-04-14"}, {2041, "1984-04-13"}, {2042, "1985-04-13"}, {2043, "1986-04-14"},
	{2044, "1987-04-14"}, {2045, "1988-04-13"}, {2046, "1989-04-13"}, {2047, "1990-04-14"},
	{2048, "1991-04-14"}, {2049, "1992-04-13"}, {2050, "1993-04-13"}, {2051, "1994-04-14"},
	{2052, "1995-04-14"}, {2053, "1996-04-13"}, {2054, "1997-04-13"}, {2055, "1998-04-14"},
	{2056, "1999-04-14"}, {2057, "2000-04-13"}, {2058, "2001-04-14"}, {2059, "2002-04-14"},
	{2060, "2003-04-14"}, {2061, "2004-04-13"}, {2062, "2005-04-14"}, {2063, "2006-04-14"},
	{2064, "2007-04-14"}, {2065, "2008-04-13"}, {2066, "2009-04-14"}, {2067, "2010-04-14"},
	{2068, "2011-04-14"}, {2069, "2012-04-13"}, {2070, "2013-04-14"}, {2071, "2014-04-14"},
	{2072, "2015-04-14"}, {2073, "2016-04-13"}, {2074, "2017-04-14"}, {2075, "2018-04-14"},
	{2076, "2019-04-14"}, {2077, "2020-04-13"}, {2078, "2021-04-14"}, {2079, "2022-04-14"},
	{2080, "2023-04-14"}, {2081, "2024-04-13"}, {2082, "2025-04-14"}, {2083, "2026-04-14"},
	{2084, "2027-04-14"}, {2085, "2028-04-13"}, {2086, "2029-04-14"}, {2087, "2030-04-14"},
	{2088, "2031-04-15"}, {2089, "2032-04-14"}, {2090, "2033-04-14"}, {2091, "2034-04-14"},
	{2092, "2035-04-15"}, {2093, "2036-04-14"}, {2094, "2037-04-14"}, {2095, "2038-04-14"},
	{2096, "2039-04-15"}, {2097, "2040-04-13"}, {2098, "2041-04-14"}, {2099, "2042-04-14"},
	{2100, "2043-04-14"},
}

// shrawanFirst is 1 Shrawan — the first day of Nepal's fiscal year — as
// computed by bikram-sambat's converter (e.g. FY 2081/82 began 2024-07-16 and
// FY 2082/83 on 2025-07-17).
var shrawanFirst = []struct {
	year int
	ad   string
}{
	{2070, "2013-07-16"}, {2071, "2014-07-17"}, {2072, "2015-07-17"}, {2073, "2016-07-16"},
	{2074, "2017-07-16"}, {2075, "2018-07-17"}, {2076, "2019-07-17"}, {2077, "2020-07-16"},
	{2078, "2021-07-16"}, {2079, "2022-07-17"}, {2080, "2023-07-17"}, {2081, "2024-07-16"},
	{2082, "2025-07-17"}, {2083, "2026-07-17"}, {2084, "2027-07-17"},
}

func TestAnchors(t *testing.T) {
	if len(newYears) != MaxYear-MinYear+1 || MaxYear != 2100 {
		t.Fatalf("range %d–%d, vectors %d", MinYear, MaxYear, len(newYears))
	}
	check := func(bs Date, adDate string) {
		t.Helper()
		got, err := bs.ToAD()
		if err != nil || got.Format("2006-01-02") != adDate {
			t.Fatalf("%v → %v %v, want %s", bs, got, err, adDate)
		}
		back, err := FromAD(ad(t, adDate))
		if err != nil || back != bs {
			t.Fatalf("%s → %v %v, want %v", adDate, back, err, bs)
		}
	}
	for _, v := range newYears {
		check(Date{v.year, 1, 1}, v.ad)
	}
	for _, v := range shrawanFirst {
		check(Date{v.year, 4, 1}, v.ad)
	}
	check(Date{2081, 12, 31}, "2025-04-13")
	check(Date{2082, 3, 32}, "2025-07-16")
	check(Date{2063, 9, 15}, "2006-12-30")
	check(Date{2100, 12, 30}, "2044-04-12")
	check(Date{2083, 6, 10}, "2026-09-26")
}

func TestRange(t *testing.T) {
	if _, err := FromAD(ad(t, "1943-04-13")); !errors.Is(err, ErrOutOfRange) {
		t.Fatalf("before range: %v", err)
	}
	if _, err := FromAD(ad(t, "2044-04-13")); !errors.Is(err, ErrOutOfRange) {
		t.Fatalf("after range: %v", err)
	}
	if _, err := FromAD(time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)); !errors.Is(err, ErrOutOfRange) {
		t.Fatal("far future")
	}
	if _, err := ToAD(1999, 12, 30); !errors.Is(err, ErrOutOfRange) {
		t.Fatal("BS 1999")
	}
	if _, err := New(2081, 13, 1); !errors.Is(err, ErrInvalid) {
		t.Fatal("month 13")
	}
	if _, err := New(2081, 1, 32); !errors.Is(err, ErrInvalid) {
		t.Fatal("Baisakh 2081 has 31 days")
	}
	if _, err := (Date{2081, 1, 1}).AddDays(-1_000_000); !errors.Is(err, ErrOutOfRange) {
		t.Fatal("AddDays out of range")
	}
}

// TestRoundTripWholeRange walks every day from 1943-04-14 to 2044-04-12 and
// checks that BS dates advance one day at a time, that month and year lengths
// match the table, and that every date converts back to the same AD day.
func TestRoundTripWholeRange(t *testing.T) {
	day := ad(t, "1943-04-14")
	prev := Date{}
	count := 0
	for {
		d, err := FromAD(day)
		if errors.Is(err, ErrOutOfRange) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if count > 0 {
			next, err := prev.AddDays(1)
			if err != nil || next != d {
				t.Fatalf("%s: %v does not follow %v", day.Format("2006-01-02"), d, prev)
			}
			if d.Day == 1 {
				n, _ := DaysInMonth(prev.Year, prev.Month)
				if prev.Day != n {
					t.Fatalf("month %d/%d ended on day %d, table says %d", prev.Year, prev.Month, prev.Day, n)
				}
			}
		}
		back, err := d.ToAD()
		if err != nil || !back.Equal(day) {
			t.Fatalf("round trip %s → %v → %v", day.Format("2006-01-02"), d, back)
		}
		if wd, _ := d.Weekday(); wd != day.Weekday() {
			t.Fatalf("weekday of %v", d)
		}
		prev, day = d, day.AddDate(0, 0, 1)
		count++
	}
	total := 0
	for y := MinYear; y <= MaxYear; y++ {
		n, _ := DaysInYear(y)
		if n < 364 || n > 366 {
			t.Fatalf("BS %d has %d days", y, n)
		}
		total += n
		for m := 1; m <= 12; m++ {
			if n, _ := DaysInMonth(y, m); n < 29 || n > 32 {
				t.Fatalf("BS %d/%d has %d days", y, m, n)
			}
		}
	}
	if count != total || prev != (Date{2100, 12, 30}) {
		t.Fatalf("walked %d days to %v, table has %d", count, prev, total)
	}
}

func TestFromInstantUsesNepalTime(t *testing.T) {
	// 18:30 UTC on 12 April 2024 is 00:15 on 13 April in Nepal: New Year 2081.
	instant := time.Date(2024, 4, 12, 18, 30, 0, 0, time.UTC)
	d, err := FromInstant(instant)
	if err != nil || d != (Date{2081, 1, 1}) {
		t.Fatalf("FromInstant = %v %v", d, err)
	}
	d, _ = FromAD(instant)
	if d != (Date{2080, 12, 30}) {
		t.Fatalf("FromAD uses the time's own calendar date: %v", d)
	}
}

func TestParse(t *testing.T) {
	for in, want := range map[string]Date{
		"2081-01-15": {2081, 1, 15}, "2081/4/1": {2081, 4, 1}, " 2081.12.30 ": {2081, 12, 30}, "२०८१-०४-०१": {2081, 4, 1},
	} {
		got, err := Parse(in)
		if err != nil || got != want {
			t.Fatalf("Parse(%q) = %v %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "2081-01", "2081-xx-01", "2081-02-33", "1990-01-01"} {
		if _, err := Parse(bad); err == nil {
			t.Fatalf("Parse(%q) accepted", bad)
		}
	}
}

func TestFiscalYear(t *testing.T) {
	cases := []struct {
		date    Date
		fy      string
		quarter int
	}{
		{Date{2081, 4, 1}, "2081/82", 1},
		{Date{2081, 3, 32}, "2080/81", 4},
		{Date{2081, 6, 30}, "2081/82", 1},
		{Date{2081, 7, 1}, "2081/82", 2},
		{Date{2081, 9, 29}, "2081/82", 2},
		{Date{2081, 10, 1}, "2081/82", 3},
		{Date{2081, 12, 31}, "2081/82", 3},
		{Date{2082, 1, 1}, "2081/82", 4},
		{Date{2082, 3, 32}, "2081/82", 4},
		{Date{2099, 5, 1}, "2099/00", 1},
	}
	for _, c := range cases {
		fy := FiscalYearOf(c.date)
		if fy.String() != c.fy || FiscalQuarter(c.date) != c.quarter {
			t.Fatalf("%v: FY %s Q%d, want %s Q%d", c.date, fy, FiscalQuarter(c.date), c.fy, c.quarter)
		}
	}
	fy := FiscalYear(2081)
	start, _ := fy.Start()
	end, _ := fy.End()
	startAD, _ := start.ToAD()
	endAD, _ := end.ToAD()
	if start != (Date{2081, 4, 1}) || end != (Date{2082, 3, 32}) ||
		startAD.Format("2006-01-02") != "2024-07-16" || endAD.Format("2006-01-02") != "2025-07-16" {
		t.Fatalf("FY 2081/82: %v (%v) – %v (%v)", start, startAD, end, endAD)
	}
	if _, err := FiscalYear(2100).End(); !errors.Is(err, ErrOutOfRange) {
		t.Fatal("FY 2100/01 ends outside the table")
	}
	for in, want := range map[string]FiscalYear{"2081/82": 2081, "2081-82": 2081, "2081/2082": 2081, "2081": 2081, "२०८१/८२": 2081, "2099/00": 2099} {
		if got, err := ParseFiscalYear(in); err != nil || got != want {
			t.Fatalf("ParseFiscalYear(%q) = %v %v", in, got, err)
		}
	}
	for _, bad := range []string{"2081/83", "abc", "1990/91", "2100/01"} {
		if _, err := ParseFiscalYear(bad); err == nil {
			t.Fatalf("ParseFiscalYear(%q) accepted", bad)
		}
	}
}

func TestFormat(t *testing.T) {
	d := Date{2081, 1, 5}
	cases := map[string]string{
		"YYYY-MM-DD":       "2081-01-05",
		"D MMMM YYYY":      "5 Baisakh 2081",
		"YY/M/D":           "81/1/5",
		"dddd, D NNNN":     "Wednesday, 5 वैशाख",
		"WWWW":             "बुधबार",
		"MMMM (NNNN) YYYY": "Baisakh (वैशाख) 2081",
	}
	for layout, want := range cases {
		if got := d.Format(layout); got != want {
			t.Fatalf("Format(%q) = %q, want %q", layout, got, want)
		}
	}
	if got := d.FormatNepali("YYYY NNNN D"); got != "२०८१ वैशाख ५" {
		t.Fatalf("FormatNepali = %q", got)
	}
	if ToDevanagariDigits("FY 2081/82") != "FY २०८१/८२" || FromDevanagariDigits("२०८१/८२") != "2081/82" {
		t.Fatal("digit conversion")
	}
	if MonthName(4) != "Shrawan" || MonthNameNepali(4) != "साउन" || MonthName(0) != "" || MonthNameNepali(13) != "" {
		t.Fatal("month names")
	}
	if d.String() != "2081-01-05" {
		t.Fatal(d.String())
	}
	if !d.Before(Date{2081, 1, 6}) || d.Before(Date{2080, 12, 30}) || !d.Before(Date{2081, 2, 1}) {
		t.Fatal("Before")
	}
}
