package bs

import (
	"strconv"
	"strings"
)

// Month names, Baisakh first.
var (
	// MonthNames are the romanised names in common official use.
	MonthNames = [12]string{"Baisakh", "Jestha", "Asar", "Shrawan", "Bhadra", "Ashwin",
		"Kartik", "Mangsir", "Poush", "Magh", "Falgun", "Chaitra"}
	// MonthNamesNepali are the Devanagari names as printed in the official
	// calendar.
	MonthNamesNepali = [12]string{"वैशाख", "जेठ", "असार", "साउन", "भदौ", "असोज",
		"कात्तिक", "मंसिर", "पुस", "माघ", "फागुन", "चैत"}
	// WeekdayNamesNepali are Sunday first, like time.Weekday.
	WeekdayNamesNepali = [7]string{"आइतबार", "सोमबार", "मंगलबार", "बुधबार", "बिहीबार", "शुक्रबार", "शनिबार"}
)

// MonthName returns the romanised name of month 1–12, or "".
func MonthName(month int) string {
	if month < 1 || month > 12 {
		return ""
	}
	return MonthNames[month-1]
}

// MonthNameNepali returns the Devanagari name of month 1–12, or "".
func MonthNameNepali(month int) string {
	if month < 1 || month > 12 {
		return ""
	}
	return MonthNamesNepali[month-1]
}

const devanagariZero = '०'

// ToDevanagariDigits replaces ASCII digits with Devanagari ones: "2081" →
// "२०८१". Everything else is left alone.
func ToDevanagariDigits(s string) string {
	var b strings.Builder
	b.Grow(len(s) * 3)
	for _, r := range s {
		if r >= '0' && r <= '9' {
			r = devanagariZero + (r - '0')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// FromDevanagariDigits replaces Devanagari digits with ASCII ones.
func FromDevanagariDigits(s string) string {
	if !strings.ContainsFunc(s, isDevanagariDigit) {
		return s
	}
	var b strings.Builder
	for _, r := range s {
		if isDevanagariDigit(r) {
			r = '0' + (r - devanagariZero)
		}
		b.WriteRune(r)
	}
	return b.String()
}

func isDevanagariDigit(r rune) bool { return r >= devanagariZero && r <= devanagariZero+9 }

// Format renders the date with a layout of tokens:
//
//	YYYY  year            2081
//	YY    two-digit year  81
//	MMMM  month name      Baisakh
//	NNNN  Nepali month    वैशाख
//	MM    month, padded   01
//	M     month           1
//	DD    day, padded     05
//	D     day             5
//	dddd  weekday         Saturday
//	WWWW  Nepali weekday  शनिबार
//
// Any other character is copied. Digits are ASCII; wrap the result in
// ToDevanagariDigits (or use FormatNepali) for Devanagari numerals.
func (d Date) Format(layout string) string {
	var b strings.Builder
	for i := 0; i < len(layout); {
		rest := layout[i:]
		switch {
		case strings.HasPrefix(rest, "YYYY"):
			b.WriteString(strconv.Itoa(d.Year))
			i += 4
		case strings.HasPrefix(rest, "YY"):
			b.WriteString(pad2(d.Year % 100))
			i += 2
		case strings.HasPrefix(rest, "MMMM"):
			b.WriteString(MonthName(d.Month))
			i += 4
		case strings.HasPrefix(rest, "NNNN"):
			b.WriteString(MonthNameNepali(d.Month))
			i += 4
		case strings.HasPrefix(rest, "MM"):
			b.WriteString(pad2(d.Month))
			i += 2
		case strings.HasPrefix(rest, "M"):
			b.WriteString(strconv.Itoa(d.Month))
			i++
		case strings.HasPrefix(rest, "DD"):
			b.WriteString(pad2(d.Day))
			i += 2
		case strings.HasPrefix(rest, "D"):
			b.WriteString(strconv.Itoa(d.Day))
			i++
		case strings.HasPrefix(rest, "dddd"):
			if wd, err := d.Weekday(); err == nil {
				b.WriteString(wd.String())
			}
			i += 4
		case strings.HasPrefix(rest, "WWWW"):
			if wd, err := d.Weekday(); err == nil {
				b.WriteString(WeekdayNamesNepali[wd])
			}
			i += 4
		default:
			b.WriteByte(layout[i])
			i++
		}
	}
	return b.String()
}

// FormatNepali formats with Devanagari digits.
func (d Date) FormatNepali(layout string) string { return ToDevanagariDigits(d.Format(layout)) }

func pad2(n int) string {
	if n < 10 {
		return "0" + strconv.Itoa(n)
	}
	return strconv.Itoa(n)
}
