// Package fiscal computes fiscal years on the Gregorian calendar for any
// start month: April (India, UK, Japan), July (Australia, Pakistan), October
// (US federal government) or January (a calendar-year company).
//
// Nepal's fiscal year starts on 1 Shrawan in the Bikram Sambat calendar and
// does not fall on a fixed Gregorian date; see package calendar/bs.
package fiscal

import (
	"fmt"
	"time"
)

// Calendar is a fiscal calendar starting on the first day of StartMonth.
type Calendar struct {
	StartMonth time.Month
	// NameByEndYear names a fiscal year by the calendar year it ends in (the
	// US federal convention: FY2025 starts 1 October 2024). By default a
	// fiscal year is named by the year it starts in (2024/25).
	NameByEndYear bool
}

// New returns a calendar starting in month 1–12.
func New(startMonth int) (Calendar, error) {
	if startMonth < 1 || startMonth > 12 {
		return Calendar{}, fmt.Errorf("fiscal: start month must be 1-12, got %d", startMonth)
	}
	return Calendar{StartMonth: time.Month(startMonth)}, nil
}

// startYear is the calendar year in which the fiscal year containing t began.
func (c Calendar) startYear(t time.Time) int {
	if c.StartMonth <= 1 || t.Month() >= c.StartMonth {
		return t.Year()
	}
	return t.Year() - 1
}

func (c Calendar) spans() bool { return c.StartMonth > 1 }

// Year returns the fiscal year number containing t's calendar date.
func (c Calendar) Year(t time.Time) int {
	y := c.startYear(t)
	if c.NameByEndYear && c.spans() {
		return y + 1
	}
	return y
}

// Label names the fiscal year containing t: "2024/25" when it spans two
// calendar years, "2024" when it starts in January, "FY2025" when named by
// its end year.
func (c Calendar) Label(t time.Time) string {
	y := c.startYear(t)
	switch {
	case !c.spans():
		return fmt.Sprint(y)
	case c.NameByEndYear:
		return fmt.Sprintf("FY%d", y+1)
	}
	return fmt.Sprintf("%d/%02d", y, (y+1)%100)
}

// Start returns the first day of the fiscal year containing t, at midnight in
// t's location.
func (c Calendar) Start(t time.Time) time.Time {
	month := c.StartMonth
	if month < 1 {
		month = time.January
	}
	return time.Date(c.startYear(t), month, 1, 0, 0, 0, 0, t.Location())
}

// End returns the last day of the fiscal year containing t, at midnight.
func (c Calendar) End(t time.Time) time.Time {
	return c.Start(t).AddDate(1, 0, -1)
}

// Quarter returns the fiscal quarter, 1–4, containing t.
func (c Calendar) Quarter(t time.Time) int {
	start := int(c.StartMonth)
	if start < 1 {
		start = 1
	}
	return (int(t.Month())-start+12)%12/3 + 1
}
