package fiscal

import (
	"testing"
	"time"
)

func day(s string) time.Time {
	t, _ := time.Parse("2006-01-02", s)
	return t
}

func TestCalendars(t *testing.T) {
	cases := []struct {
		start       int
		byEnd       bool
		date        string
		year        int
		label       string
		quarter     int
		first, last string
	}{
		{4, false, "2024-04-01", 2024, "2024/25", 1, "2024-04-01", "2025-03-31"},
		{4, false, "2025-03-31", 2024, "2024/25", 4, "2024-04-01", "2025-03-31"},
		{4, false, "2024-12-15", 2024, "2024/25", 3, "2024-04-01", "2025-03-31"},
		{7, false, "2024-06-30", 2023, "2023/24", 4, "2023-07-01", "2024-06-30"},
		{7, false, "2024-07-01", 2024, "2024/25", 1, "2024-07-01", "2025-06-30"},
		{10, true, "2024-10-01", 2025, "FY2025", 1, "2024-10-01", "2025-09-30"},
		{10, true, "2025-09-30", 2025, "FY2025", 4, "2024-10-01", "2025-09-30"},
		{1, false, "2024-02-29", 2024, "2024", 1, "2024-01-01", "2024-12-31"},
		{1, true, "2024-11-01", 2024, "2024", 4, "2024-01-01", "2024-12-31"},
		{3, false, "2024-02-29", 2023, "2023/24", 4, "2023-03-01", "2024-02-29"},
		{12, false, "1999-12-01", 1999, "1999/00", 1, "1999-12-01", "2000-11-30"},
	}
	for _, c := range cases {
		cal, err := New(c.start)
		if err != nil {
			t.Fatal(err)
		}
		cal.NameByEndYear = c.byEnd
		d := day(c.date)
		if cal.Year(d) != c.year || cal.Label(d) != c.label || cal.Quarter(d) != c.quarter ||
			cal.Start(d).Format("2006-01-02") != c.first || cal.End(d).Format("2006-01-02") != c.last {
			t.Fatalf("start %d %s: year %d label %s Q%d %s–%s", c.start, c.date,
				cal.Year(d), cal.Label(d), cal.Quarter(d), cal.Start(d).Format("2006-01-02"), cal.End(d).Format("2006-01-02"))
		}
	}
	for _, bad := range []int{0, 13} {
		if _, err := New(bad); err == nil {
			t.Fatalf("start month %d accepted", bad)
		}
	}
	if (Calendar{}).Quarter(day("2024-05-01")) != 2 || (Calendar{}).Start(day("2024-05-01")).Month() != time.January {
		t.Fatal("zero calendar behaves as January")
	}
}
