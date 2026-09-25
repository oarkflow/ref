// Package dos models dates of service: the single day or inclusive date range an
// activity (a patient encounter, a field inspection, a shift) was performed on.
//
// Dates of service are civil dates, not instants. "2026-03-04" is the same
// service date in every time zone, so this package uses its own Date type
// instead of time.Time, which would silently shift a date across midnight when
// converted between zones. Only "today" — needed for rules such as "no future
// dates" — depends on a zone, and callers pass it in explicitly.
package dos

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Date is a civil calendar date.
type Date struct {
	Year  int
	Month time.Month
	Day   int
}

// ErrInvalidDate reports an unparseable or impossible date.
var ErrInvalidDate = errors.New("dos: invalid date")

// layouts accepted by ParseDate, most common first.
var layouts = []string{"2006-01-02", "01/02/2006", "1/2/2006", "20060102", "2006/01/02"}

// ParseDate parses ISO (2006-01-02), US (01/02/2006), compact (20060102) and
// slash-ISO (2006/01/02) dates. An RFC 3339 timestamp is accepted and its
// date part used as written, without zone conversion.
func ParseDate(s string) (Date, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Date{}, fmt.Errorf("%w: empty", ErrInvalidDate)
	}
	if len(s) > 10 && (s[10] == 'T' || s[10] == ' ') {
		s = s[:10]
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, s); err == nil {
			return DateOf(t), nil
		}
	}
	return Date{}, fmt.Errorf("%w: %q (use YYYY-MM-DD)", ErrInvalidDate, s)
}

// MustParseDate is ParseDate for tests and constants.
func MustParseDate(s string) Date {
	d, err := ParseDate(s)
	if err != nil {
		panic(err)
	}
	return d
}

// DateOf returns t's date in t's own location.
func DateOf(t time.Time) Date {
	y, m, d := t.Date()
	return Date{y, m, d}
}

// Today returns the current date in loc (UTC when nil).
func Today(now time.Time, loc *time.Location) Date {
	if loc == nil {
		loc = time.UTC
	}
	return DateOf(now.In(loc))
}

// IsZero reports whether d is the zero Date.
func (d Date) IsZero() bool { return d == Date{} }

// Time returns midnight of d in UTC.
func (d Date) Time() time.Time { return time.Date(d.Year, d.Month, d.Day, 0, 0, 0, 0, time.UTC) }

// String renders d as YYYY-MM-DD.
func (d Date) String() string {
	if d.IsZero() {
		return ""
	}
	return fmt.Sprintf("%04d-%02d-%02d", d.Year, int(d.Month), d.Day)
}

// AddDays returns d shifted by n days.
func (d Date) AddDays(n int) Date { return DateOf(d.Time().AddDate(0, 0, n)) }

// Compare returns -1, 0 or +1.
func (d Date) Compare(o Date) int { return d.Time().Compare(o.Time()) }

// Before reports d < o.
func (d Date) Before(o Date) bool { return d.Compare(o) < 0 }

// After reports d > o.
func (d Date) After(o Date) bool { return d.Compare(o) > 0 }

// Weekday returns the day of the week.
func (d Date) Weekday() time.Weekday { return d.Time().Weekday() }

// DaysUntil returns the number of days from d to o (negative if o is earlier).
func (d Date) DaysUntil(o Date) int {
	return int(o.Time().Sub(d.Time()).Hours() / 24)
}

// MarshalJSON renders the date as a "YYYY-MM-DD" string.
func (d Date) MarshalJSON() ([]byte, error) { return json.Marshal(d.String()) }

// UnmarshalJSON parses any format ParseDate accepts.
func (d *Date) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	if s == "" {
		*d = Date{}
		return nil
	}
	parsed, err := ParseDate(s)
	if err != nil {
		return err
	}
	*d = parsed
	return nil
}

// Period is an inclusive range of service dates. From == To is a single date
// of service; anything longer is a multi-DOS period.
type Period struct {
	From Date `json:"from"`
	To   Date `json:"to"`
}

// Kind values reported by Period.Kind.
const (
	KindSingle = "single"
	KindMulti  = "multi"
)

// ErrInvalidPeriod reports a period whose end precedes its start.
var ErrInvalidPeriod = errors.New("dos: period ends before it starts")

// NewPeriod builds a period, rejecting to < from. A zero `to` means a single
// date of service.
func NewPeriod(from, to Date) (Period, error) {
	if from.IsZero() {
		return Period{}, fmt.Errorf("%w: missing start date", ErrInvalidDate)
	}
	if to.IsZero() {
		to = from
	}
	if to.Before(from) {
		return Period{}, fmt.Errorf("%w: %s .. %s", ErrInvalidPeriod, from, to)
	}
	return Period{From: from, To: to}, nil
}

// ParsePeriod parses a start and optional end date.
func ParsePeriod(from, to string) (Period, error) {
	f, err := ParseDate(from)
	if err != nil {
		return Period{}, err
	}
	var t Date
	if strings.TrimSpace(to) != "" {
		if t, err = ParseDate(to); err != nil {
			return Period{}, err
		}
	}
	return NewPeriod(f, t)
}

// Single returns the one-day period for d.
func Single(d Date) Period { return Period{From: d, To: d} }

// IsSingle reports whether the period covers exactly one date.
func (p Period) IsSingle() bool { return p.From == p.To }

// Kind returns KindSingle or KindMulti.
func (p Period) Kind() string {
	if p.IsSingle() {
		return KindSingle
	}
	return KindMulti
}

// Days returns the number of dates covered, inclusive.
func (p Period) Days() int { return p.From.DaysUntil(p.To) + 1 }

// Dates lists every date in the period.
func (p Period) Dates() []Date {
	out := make([]Date, 0, p.Days())
	for d := p.From; !d.After(p.To); d = d.AddDays(1) {
		out = append(out, d)
	}
	return out
}

// Contains reports whether d is inside the period.
func (p Period) Contains(d Date) bool { return !d.Before(p.From) && !d.After(p.To) }

// ContainsPeriod reports whether q lies wholly inside p.
func (p Period) ContainsPeriod(q Period) bool { return p.Contains(q.From) && p.Contains(q.To) }

// Overlaps reports whether the two periods share at least one date.
func (p Period) Overlaps(q Period) bool { return !p.To.Before(q.From) && !q.To.Before(p.From) }

// Intersect returns the shared dates, if any.
func (p Period) Intersect(q Period) (Period, bool) {
	if !p.Overlaps(q) {
		return Period{}, false
	}
	from, to := p.From, p.To
	if q.From.After(from) {
		from = q.From
	}
	if q.To.Before(to) {
		to = q.To
	}
	return Period{From: from, To: to}, true
}

// String renders "2026-01-02" or "2026-01-02..2026-01-05".
func (p Period) String() string {
	if p.IsSingle() {
		return p.From.String()
	}
	return p.From.String() + ".." + p.To.String()
}

// SplitByMonth cuts the period at calendar-month boundaries, which is how
// multi-DOS claims spanning a month end are usually billed.
func (p Period) SplitByMonth() []Period {
	var out []Period
	start := p.From
	for !start.After(p.To) {
		monthEnd := DateOf(time.Date(start.Year, start.Month+1, 0, 0, 0, 0, 0, time.UTC))
		end := p.To
		if monthEnd.Before(end) {
			end = monthEnd
		}
		out = append(out, Period{From: start, To: end})
		start = end.AddDays(1)
	}
	return out
}

// Merge coalesces overlapping and adjacent periods, returning them sorted.
func Merge(periods []Period) []Period {
	if len(periods) == 0 {
		return nil
	}
	sorted := append([]Period(nil), periods...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].From.Before(sorted[j].From) })
	out := []Period{sorted[0]}
	for _, p := range sorted[1:] {
		last := &out[len(out)-1]
		if !p.From.After(last.To.AddDays(1)) {
			if p.To.After(last.To) {
				last.To = p.To
			}
			continue
		}
		out = append(out, p)
	}
	return out
}

// Conflict is one overlap between a candidate period and an existing one.
type Conflict struct {
	// Index is the position of the existing period in the list checked.
	Index   int    `json:"index"`
	Period  Period `json:"period"`
	Overlap Period `json:"overlap"`
}

// Conflicts returns every existing period that shares a date with p.
func Conflicts(p Period, existing []Period) []Conflict {
	var out []Conflict
	for i, e := range existing {
		if overlap, ok := p.Intersect(e); ok {
			out = append(out, Conflict{Index: i, Period: e, Overlap: overlap})
		}
	}
	return out
}
