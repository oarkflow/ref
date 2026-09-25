package pipeline

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// WorkCalendar is a compiled Calendar: weekly working windows, holidays and a
// location. The zero value (nil) means "always open" (24x7, UTC).
type WorkCalendar struct {
	Name     string
	loc      *time.Location
	windows  [7][]window // by time.Weekday
	holidays map[string]bool
	yearly   map[string]bool // "MM-DD"
}

type window struct{ start, end int } // minutes since midnight, end exclusive

var weekdays = map[string]time.Weekday{
	"sun": time.Sunday, "mon": time.Monday, "tue": time.Tuesday, "wed": time.Wednesday,
	"thu": time.Thursday, "fri": time.Friday, "sat": time.Saturday,
}

// CompileCalendar parses a Calendar.
func CompileCalendar(cal Calendar) (*WorkCalendar, error) {
	wc := &WorkCalendar{Name: cal.Name, loc: time.UTC, holidays: map[string]bool{}, yearly: map[string]bool{}}
	if cal.Timezone != "" {
		loc, err := time.LoadLocation(cal.Timezone)
		if err != nil {
			return nil, fmt.Errorf("calendar %q: timezone: %w", cal.Name, err)
		}
		wc.loc = loc
	}
	hours := strings.ToLower(strings.TrimSpace(cal.Hours))
	if hours == "" || hours == "24x7" {
		for d := range wc.windows {
			wc.windows[d] = []window{{0, 24 * 60}}
		}
	} else {
		for _, seg := range strings.Split(hours, ";") {
			seg = strings.TrimSpace(seg)
			if seg == "" {
				continue
			}
			fields := strings.Fields(seg)
			if len(fields) != 2 {
				return nil, fmt.Errorf("calendar %q: %q must look like \"mon-fri 09:00-17:00\"", cal.Name, seg)
			}
			days, err := parseDays(fields[0])
			if err != nil {
				return nil, fmt.Errorf("calendar %q: %w", cal.Name, err)
			}
			for _, span := range strings.Split(fields[1], ",") {
				w, err := parseWindow(span)
				if err != nil {
					return nil, fmt.Errorf("calendar %q: %w", cal.Name, err)
				}
				for _, d := range days {
					wc.windows[d] = append(wc.windows[d], w)
				}
			}
		}
	}
	for _, h := range cal.Holidays {
		h = strings.TrimSpace(h)
		switch {
		case len(h) == 10:
			if _, err := time.Parse("2006-01-02", h); err != nil {
				return nil, fmt.Errorf("calendar %q: holiday %q: %w", cal.Name, h, err)
			}
			wc.holidays[h] = true
		case len(h) == 5:
			if _, err := time.Parse("01-02", h); err != nil {
				return nil, fmt.Errorf("calendar %q: holiday %q: %w", cal.Name, h, err)
			}
			wc.yearly[h] = true
		default:
			return nil, fmt.Errorf("calendar %q: holiday %q must be YYYY-MM-DD or MM-DD", cal.Name, h)
		}
	}
	open := false
	for _, ws := range wc.windows {
		open = open || len(ws) > 0
	}
	if !open {
		return nil, fmt.Errorf("calendar %q has no working hours", cal.Name)
	}
	return wc, nil
}

func parseDays(spec string) ([]time.Weekday, error) {
	var out []time.Weekday
	for _, part := range strings.Split(spec, ",") {
		if from, to, ok := strings.Cut(part, "-"); ok {
			a, okA := weekdays[from]
			b, okB := weekdays[to]
			if !okA || !okB {
				return nil, fmt.Errorf("unknown day range %q", part)
			}
			for d := a; ; d = (d + 1) % 7 {
				out = append(out, d)
				if d == b {
					break
				}
			}
			continue
		}
		d, ok := weekdays[part]
		if !ok {
			return nil, fmt.Errorf("unknown day %q", part)
		}
		out = append(out, d)
	}
	return out, nil
}

func parseWindow(span string) (window, error) {
	from, to, ok := strings.Cut(strings.TrimSpace(span), "-")
	if !ok {
		return window{}, fmt.Errorf("hours %q must look like 09:00-17:00", span)
	}
	a, err := parseClock(from)
	if err != nil {
		return window{}, err
	}
	b, err := parseClock(to)
	if err != nil {
		return window{}, err
	}
	if b <= a {
		return window{}, fmt.Errorf("hours %q end before they start", span)
	}
	return window{a, b}, nil
}

func parseClock(s string) (int, error) {
	h, m, ok := strings.Cut(strings.TrimSpace(s), ":")
	hh, err1 := strconv.Atoi(h)
	mm, err2 := strconv.Atoi(m)
	if !ok || err1 != nil || err2 != nil || hh < 0 || hh > 24 || mm < 0 || mm > 59 || (hh == 24 && mm != 0) {
		return 0, fmt.Errorf("invalid time %q", s)
	}
	return hh*60 + mm, nil
}

func (wc *WorkCalendar) holiday(day time.Time) bool {
	return wc.holidays[day.Format("2006-01-02")] || wc.yearly[day.Format("01-02")]
}

// windowsOn returns the working windows of a local day as absolute times.
func (wc *WorkCalendar) windowsOn(day time.Time) [][2]time.Time {
	if wc.holiday(day) {
		return nil
	}
	y, m, d := day.Date()
	var out [][2]time.Time
	for _, w := range wc.windows[day.Weekday()] {
		out = append(out, [2]time.Time{
			time.Date(y, m, d, 0, 0, 0, 0, wc.loc).Add(time.Duration(w.start) * time.Minute),
			time.Date(y, m, d, 0, 0, 0, 0, wc.loc).Add(time.Duration(w.end) * time.Minute),
		})
	}
	return out
}

const maxCalendarDays = 3660

// IsOpen reports whether t is working time.
func (wc *WorkCalendar) IsOpen(t time.Time) bool {
	if wc == nil {
		return true
	}
	lt := t.In(wc.loc)
	for _, w := range wc.windowsOn(lt) {
		if !lt.Before(w[0]) && lt.Before(w[1]) {
			return true
		}
	}
	return false
}

// Add advances d of working time from start.
func (wc *WorkCalendar) Add(start time.Time, d time.Duration) time.Time {
	if wc == nil {
		return start.Add(d)
	}
	cur := start.In(wc.loc)
	remaining := d
	for i := 0; i < maxCalendarDays; i++ {
		for _, w := range wc.windowsOn(cur) {
			if !cur.Before(w[1]) {
				continue
			}
			from := cur
			if from.Before(w[0]) {
				from = w[0]
			}
			avail := w[1].Sub(from)
			if remaining <= avail {
				return from.Add(remaining)
			}
			remaining -= avail
			cur = w[1]
		}
		y, m, dd := cur.Date()
		cur = time.Date(y, m, dd+1, 0, 0, 0, 0, wc.loc)
	}
	return start.Add(d) // a calendar with (almost) no working time: fall back
}

// AddDays advances n working days: the deadline is the same working moment n
// working days later (the end of that day's hours when start is after hours).
func (wc *WorkCalendar) AddDays(start time.Time, n int) time.Time {
	if wc == nil {
		return start.AddDate(0, 0, n)
	}
	// Measure how far into its working day start is, then advance whole
	// working days and re-apply that offset.
	cur := wc.next(start).In(wc.loc)
	for i := 0; i < maxCalendarDays && n > 0; i++ {
		y, m, d := cur.Date()
		cur = time.Date(y, m, d+1, cur.Hour(), cur.Minute(), cur.Second(), 0, wc.loc)
		if len(wc.windowsOn(cur)) > 0 {
			n--
		}
	}
	if !wc.IsOpen(cur) {
		// Clamp to the end of that day's last window.
		ws := wc.windowsOn(cur)
		if len(ws) > 0 && cur.After(ws[len(ws)-1][1]) {
			return ws[len(ws)-1][1]
		}
		return wc.next(cur)
	}
	return cur
}

// next returns t if it is working time, else the start of the next window.
func (wc *WorkCalendar) next(t time.Time) time.Time {
	if wc == nil || wc.IsOpen(t) {
		return t
	}
	cur := t.In(wc.loc)
	for i := 0; i < maxCalendarDays; i++ {
		for _, w := range wc.windowsOn(cur) {
			if cur.Before(w[0]) {
				return w[0]
			}
		}
		y, m, d := cur.Date()
		cur = time.Date(y, m, d+1, 0, 0, 0, 0, wc.loc)
	}
	return t
}

// Between is the working time from a to b.
func (wc *WorkCalendar) Between(a, b time.Time) time.Duration {
	if !b.After(a) {
		return 0
	}
	if wc == nil {
		return b.Sub(a)
	}
	var total time.Duration
	cur := a.In(wc.loc)
	for i := 0; i < maxCalendarDays && cur.Before(b); i++ {
		for _, w := range wc.windowsOn(cur) {
			from, to := w[0], w[1]
			if from.Before(a) {
				from = a
			}
			if to.After(b) {
				to = b
			}
			if to.After(from) {
				total += to.Sub(from)
			}
		}
		y, m, d := cur.Date()
		cur = time.Date(y, m, d+1, 0, 0, 0, 0, wc.loc)
	}
	return total
}

// Span is a duration that is either working time or a count of working days
// ("3d"). Durations accept Go syntax plus d (days) and w (weeks).
type Span struct {
	Days int
	Dur  time.Duration
}

// ParseSpan parses "72h", "90m", "3d", "2w" or "1d12h".
func ParseSpan(s string) (Span, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Span{}, nil
	}
	var sp Span
	rest := s
	for _, unit := range []struct {
		suffix string
		days   int
	}{{"w", 7}, {"d", 1}} {
		if i := strings.Index(rest, unit.suffix); i > 0 {
			n, err := strconv.Atoi(rest[:i])
			if err != nil {
				return Span{}, fmt.Errorf("invalid duration %q", s)
			}
			sp.Days += n * unit.days
			rest = rest[i+1:]
		}
	}
	if rest != "" {
		d, err := time.ParseDuration(rest)
		if err != nil || d < 0 {
			return Span{}, fmt.Errorf("invalid duration %q", s)
		}
		sp.Dur = d
	}
	if sp.Days < 0 {
		return Span{}, fmt.Errorf("invalid duration %q", s)
	}
	return sp, nil
}

// Zero reports whether the span is empty.
func (s Span) Zero() bool { return s.Days == 0 && s.Dur == 0 }

// From applies the span to start under the calendar (nil = wall clock).
func (s Span) From(wc *WorkCalendar, start time.Time) time.Time {
	t := start
	if s.Days > 0 {
		t = wc.AddDays(t, s.Days)
	}
	if s.Dur > 0 {
		t = wc.Add(t, s.Dur)
	}
	return t
}

// Before subtracts the span's wall duration from t (warnings are measured
// back from the deadline; days count as 24h here).
func (s Span) Before(t time.Time) time.Time {
	return t.Add(-(time.Duration(s.Days)*24*time.Hour + s.Dur))
}
