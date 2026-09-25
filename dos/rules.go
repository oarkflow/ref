package dos

import (
	"fmt"
	"math"
	"slices"
	"time"
)

// Rules are the date-of-service checks an application enforces. The zero
// value allows any valid period, including future dates.
type Rules struct {
	// AllowMulti permits periods longer than one day. When false every
	// activity must be a single date of service.
	AllowMulti bool `json:"allow_multi"`
	// MaxSpanDays caps a multi-DOS period's length in days (0 = no cap).
	MaxSpanDays int `json:"max_span_days,omitempty"`
	// AllowFuture permits dates after today.
	AllowFuture bool `json:"allow_future"`
	// MaxAgeDays rejects periods that started more than this many days before
	// today — e.g. a payer's timely-filing limit (0 = no limit).
	MaxAgeDays int `json:"max_age_days,omitempty"`
	// SameMonth requires a multi-DOS period to stay within one calendar month.
	SameMonth bool `json:"same_month,omitempty"`
	// Weekdays, when set, restricts which days of the week may be billed.
	Weekdays []time.Weekday `json:"weekdays,omitempty"`
	// NotBefore rejects dates before a fixed date (e.g. a contract start).
	NotBefore Date `json:"not_before,omitempty"`
}

// Violation is one failed rule.
type Violation struct {
	Rule    string `json:"rule"`
	Message string `json:"message"`
	// Line is the line-item index a violation belongs to, or -1 for the
	// period itself.
	Line int `json:"line"`
}

func (v Violation) Error() string { return v.Message }

// Validate checks p against the rules relative to today.
func (r Rules) Validate(p Period, today Date) []Violation {
	var out []Violation
	add := func(rule, format string, args ...any) {
		out = append(out, Violation{Rule: rule, Message: fmt.Sprintf(format, args...), Line: -1})
	}
	if !p.IsSingle() && !r.AllowMulti {
		add("single_only", "only a single date of service is allowed, got %s", p)
	}
	if r.MaxSpanDays > 0 && p.Days() > r.MaxSpanDays {
		add("max_span", "period %s spans %d days, more than the %d allowed", p, p.Days(), r.MaxSpanDays)
	}
	if !r.AllowFuture && !today.IsZero() && p.To.After(today) {
		add("future", "date of service %s is in the future", p.To)
	}
	if r.MaxAgeDays > 0 && !today.IsZero() && p.From.DaysUntil(today) > r.MaxAgeDays {
		add("max_age", "date of service %s is more than %d days old", p.From, r.MaxAgeDays)
	}
	if r.SameMonth && (p.From.Year != p.To.Year || p.From.Month != p.To.Month) {
		add("same_month", "period %s crosses a month boundary", p)
	}
	if !r.NotBefore.IsZero() && p.From.Before(r.NotBefore) {
		add("not_before", "date of service %s is before %s", p.From, r.NotBefore)
	}
	if len(r.Weekdays) > 0 {
		for _, d := range p.Dates() {
			if !slices.Contains(r.Weekdays, d.Weekday()) {
				add("weekday", "date of service %s falls on a %s, which is not allowed", d, d.Weekday())
				break
			}
		}
	}
	return out
}

// Line is one billable line item. A line either carries its own period (for a
// multi-DOS encounter whose procedures happened on different days) or inherits
// the encounter's.
type Line struct {
	Period Period         `json:"period"`
	Units  float64        `json:"units"`
	Data   map[string]any `json:"data,omitempty"`
}

// ValidateLines checks every line falls within the encounter period and has
// positive units. Line periods also go through the rules.
func (r Rules) ValidateLines(encounter Period, lines []Line, today Date) []Violation {
	var out []Violation
	for i, l := range lines {
		if !encounter.ContainsPeriod(l.Period) {
			out = append(out, Violation{Rule: "line_outside_period", Line: i,
				Message: fmt.Sprintf("line %d date %s is outside the encounter period %s", i+1, l.Period, encounter)})
		}
		if l.Units < 0 {
			out = append(out, Violation{Rule: "units", Line: i, Message: fmt.Sprintf("line %d has negative units", i+1)})
		}
		for _, v := range r.Validate(l.Period, today) {
			v.Line = i
			v.Message = fmt.Sprintf("line %d: %s", i+1, v.Message)
			out = append(out, v)
		}
	}
	return out
}

// Expansion modes for Expand.
const (
	// ExpandPerDay repeats the line's units on every date ("1 unit per day").
	ExpandPerDay = "per_day"
	// ExpandDistribute spreads the line's total units across its dates.
	// Whole-number totals are split into whole units, with any remainder going
	// to the earliest dates, so the per-day units always add back up exactly.
	ExpandDistribute = "distribute"
)

// DayLine is one per-date line produced by Expand.
type DayLine struct {
	Line  int            `json:"line"`
	Date  Date           `json:"dos"`
	Units float64        `json:"units"`
	Data  map[string]any `json:"data,omitempty"`
}

// Expand explodes each line into one row per date of service.
func Expand(lines []Line, mode string) ([]DayLine, error) {
	if mode == "" {
		mode = ExpandDistribute
	}
	if mode != ExpandPerDay && mode != ExpandDistribute {
		return nil, fmt.Errorf("dos: unknown expand mode %q (use %s or %s)", mode, ExpandPerDay, ExpandDistribute)
	}
	var out []DayLine
	for i, l := range lines {
		dates := l.Period.Dates()
		shares := make([]float64, len(dates))
		switch {
		case mode == ExpandPerDay:
			for j := range shares {
				shares[j] = l.Units
			}
		case l.Units == math.Trunc(l.Units):
			total := int64(l.Units)
			base, rem := total/int64(len(dates)), total%int64(len(dates))
			for j := range shares {
				shares[j] = float64(base)
				if int64(j) < rem {
					shares[j]++
				}
			}
		default:
			each := l.Units / float64(len(dates))
			for j := range shares {
				shares[j] = each
			}
		}
		for j, d := range dates {
			out = append(out, DayLine{Line: i, Date: d, Units: shares[j], Data: l.Data})
		}
	}
	return out, nil
}
