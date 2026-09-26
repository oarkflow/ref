// Package bs converts between the Gregorian (AD) calendar and Bikram Sambat
// (BS), Nepal's official calendar, and computes Nepal's fiscal year.
//
// Bikram Sambat months are solar: their lengths (29 to 32 days) come from the
// sun's transit through the zodiac and are published each year, so conversion
// is a table lookup rather than arithmetic. The embedded table covers BS
// 2000–2100 (AD 1943-04-14 to 2044-04-12); a date outside it is
// ErrOutOfRange rather than a guess. See data.go for where the table comes
// from and how it was verified.
//
// The fiscal year of the Government of Nepal runs from 1 Shrawan (month 4)
// to the last day of Asar (month 3) of the following BS year, and is written
// "2081/82".
package bs

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Supported range.
const (
	MinYear = 2000
	MaxYear = MinYear + len(monthDays) - 1
)

// ErrOutOfRange is returned for a date outside the embedded table.
var ErrOutOfRange = fmt.Errorf("bs: date outside the supported range BS %d–%d", MinYear, MaxYear)

// ErrInvalid is returned for a month or day that does not exist.
var ErrInvalid = errors.New("bs: invalid date")

// epoch is 1 Baisakh 2000 BS.
var epoch = time.Date(1943, time.April, 14, 0, 0, 0, 0, time.UTC)

// yearStart[i] is the day offset from epoch of 1 Baisakh of MinYear+i; the
// final element is one past the last supported day.
var yearStart = func() []int {
	out := make([]int, len(monthDays)+1)
	for i, months := range monthDays {
		total := 0
		for _, d := range months {
			total += int(d)
		}
		out[i+1] = out[i] + total
	}
	return out
}()

// Nepal is Nepal Standard Time (UTC+05:45). BS dates of an instant are taken
// in this zone; it has no daylight saving, so a fixed zone is exact and needs
// no tzdata.
var Nepal = time.FixedZone("NPT", 5*3600+45*60)

// Date is a Bikram Sambat calendar date. Month is 1 (Baisakh) to 12
// (Chaitra).
type Date struct {
	Year  int `json:"year"`
	Month int `json:"month"`
	Day   int `json:"day"`
}

// New validates and returns a date.
func New(year, month, day int) (Date, error) {
	d := Date{year, month, day}
	return d, d.Validate()
}

// Validate reports whether the date exists in the table.
func (d Date) Validate() error {
	if d.Year < MinYear || d.Year > MaxYear {
		return ErrOutOfRange
	}
	if d.Month < 1 || d.Month > 12 {
		return fmt.Errorf("%w: month %d", ErrInvalid, d.Month)
	}
	if n := int(monthDays[d.Year-MinYear][d.Month-1]); d.Day < 1 || d.Day > n {
		return fmt.Errorf("%w: %d/%02d has %d days, not %d", ErrInvalid, d.Year, d.Month, n, d.Day)
	}
	return nil
}

// DaysInMonth returns the length of a BS month.
func DaysInMonth(year, month int) (int, error) {
	if year < MinYear || year > MaxYear {
		return 0, ErrOutOfRange
	}
	if month < 1 || month > 12 {
		return 0, fmt.Errorf("%w: month %d", ErrInvalid, month)
	}
	return int(monthDays[year-MinYear][month-1]), nil
}

// DaysInYear returns the length of a BS year.
func DaysInYear(year int) (int, error) {
	if year < MinYear || year > MaxYear {
		return 0, ErrOutOfRange
	}
	return yearStart[year-MinYear+1] - yearStart[year-MinYear], nil
}

// ordinal is the date's day offset from epoch.
func (d Date) ordinal() (int, error) {
	if err := d.Validate(); err != nil {
		return 0, err
	}
	n := yearStart[d.Year-MinYear]
	for m := 0; m < d.Month-1; m++ {
		n += int(monthDays[d.Year-MinYear][m])
	}
	return n + d.Day - 1, nil
}

func fromOrdinal(n int) (Date, error) {
	if n < 0 || n >= yearStart[len(yearStart)-1] {
		return Date{}, ErrOutOfRange
	}
	lo, hi := 0, len(monthDays)-1
	for lo < hi { // last year whose start <= n
		mid := (lo + hi + 1) / 2
		if yearStart[mid] <= n {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	n -= yearStart[lo]
	month := 0
	for n >= int(monthDays[lo][month]) {
		n -= int(monthDays[lo][month])
		month++
	}
	return Date{Year: MinYear + lo, Month: month + 1, Day: n + 1}, nil
}

// FromAD converts the Gregorian calendar date of t — its year, month and day
// in t's own location — to BS. To take an instant's date in Nepal, convert it
// first: FromAD(t.In(bs.Nepal)), or use FromInstant.
func FromAD(t time.Time) (Date, error) {
	day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	return fromOrdinal(int(day.Sub(epoch).Hours() / 24))
}

// FromInstant converts an instant to the BS date it falls on in Nepal.
func FromInstant(t time.Time) (Date, error) { return FromAD(t.In(Nepal)) }

// ToAD returns the Gregorian date as midnight UTC.
func (d Date) ToAD() (time.Time, error) {
	n, err := d.ordinal()
	if err != nil {
		return time.Time{}, err
	}
	return epoch.AddDate(0, 0, n), nil
}

// ToAD converts a BS date to its Gregorian date (midnight UTC).
func ToAD(year, month, day int) (time.Time, error) { return Date{year, month, day}.ToAD() }

// AddDays moves the date by n days.
func (d Date) AddDays(n int) (Date, error) {
	o, err := d.ordinal()
	if err != nil {
		return Date{}, err
	}
	return fromOrdinal(o + n)
}

// Weekday returns the day of the week.
func (d Date) Weekday() (time.Weekday, error) {
	t, err := d.ToAD()
	if err != nil {
		return 0, err
	}
	return t.Weekday(), nil
}

// Before reports whether d is earlier than o.
func (d Date) Before(o Date) bool {
	if d.Year != o.Year {
		return d.Year < o.Year
	}
	if d.Month != o.Month {
		return d.Month < o.Month
	}
	return d.Day < o.Day
}

// String renders the ISO-like form "2081-01-15".
func (d Date) String() string { return fmt.Sprintf("%04d-%02d-%02d", d.Year, d.Month, d.Day) }

// Parse reads "2081-01-15", "2081/1/15" or "2081.01.15", in ASCII or
// Devanagari digits, and validates it.
func Parse(s string) (Date, error) {
	s = FromDevanagariDigits(strings.TrimSpace(s))
	parts := strings.FieldsFunc(s, func(r rune) bool { return r == '-' || r == '/' || r == '.' })
	if len(parts) != 3 {
		return Date{}, fmt.Errorf("%w: %q is not YYYY-MM-DD", ErrInvalid, s)
	}
	var nums [3]int
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return Date{}, fmt.Errorf("%w: %q is not YYYY-MM-DD", ErrInvalid, s)
		}
		nums[i] = n
	}
	return New(nums[0], nums[1], nums[2])
}

// FiscalYear is a Nepali fiscal year, identified by the BS year it starts
// in: FiscalYear(2081) is FY 2081/82, 1 Shrawan 2081 to the end of Asar 2082.
type FiscalYear int

// FiscalStartMonth is Shrawan.
const FiscalStartMonth = 4

// FiscalYearOf returns the fiscal year a date falls in.
func FiscalYearOf(d Date) FiscalYear {
	if d.Month >= FiscalStartMonth {
		return FiscalYear(d.Year)
	}
	return FiscalYear(d.Year - 1)
}

// String renders "2081/82".
func (f FiscalYear) String() string {
	return fmt.Sprintf("%d/%02d", int(f), (int(f)+1)%100)
}

// Start returns 1 Shrawan of the fiscal year.
func (f FiscalYear) Start() (Date, error) { return New(int(f), FiscalStartMonth, 1) }

// End returns the last day of Asar of the following BS year.
func (f FiscalYear) End() (Date, error) {
	n, err := DaysInMonth(int(f)+1, FiscalStartMonth-1)
	if err != nil {
		return Date{}, err
	}
	return New(int(f)+1, FiscalStartMonth-1, n)
}

// FiscalQuarter returns the fiscal quarter, 1 to 4: Q1 is Shrawan–Ashwin, Q2
// Kartik–Poush, Q3 Magh–Chaitra, Q4 Baisakh–Asar.
func FiscalQuarter(d Date) int {
	return (d.Month-FiscalStartMonth+12)%12/3 + 1
}

// ParseFiscalYear reads "2081/82", "2081-82", "2081/2082" or "2081".
func ParseFiscalYear(s string) (FiscalYear, error) {
	s = FromDevanagariDigits(strings.TrimSpace(s))
	head, tail, found := strings.Cut(strings.ReplaceAll(s, "-", "/"), "/")
	y, err := strconv.Atoi(strings.TrimSpace(head))
	if err != nil {
		return 0, fmt.Errorf("bs: %q is not a fiscal year like 2081/82", s)
	}
	if found {
		next, err := strconv.Atoi(strings.TrimSpace(tail))
		if err != nil || (next != y+1 && next != (y+1)%100) {
			return 0, fmt.Errorf("bs: %q is not a fiscal year like 2081/82", s)
		}
	}
	if y < MinYear || y >= MaxYear {
		return 0, ErrOutOfRange
	}
	return FiscalYear(y), nil
}
