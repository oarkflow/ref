package platform

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/oarkflow/bcl"
	"github.com/oarkflow/ref/calendar/bs"
	"github.com/oarkflow/ref/calendar/fiscal"
	"github.com/oarkflow/ref/money"
)

// Expression functions for money, the Bikram Sambat calendar and fiscal
// years. They are pure — no clock, no I/O — so they are safe in every guard,
// condition, transform and template.
//
// Dates. A date argument is a Gregorian (AD) date or instant: a string
// ("2024-04-13", RFC 3339 such as the `now` variable) or a time. A bare date
// is taken as that calendar day; an instant is taken in Nepal time (UTC+05:45)
// for the BS functions and in its own offset for the Gregorian ones. A BS date
// is always written through bs_to_ad first, because BS 2000–2044 and AD
// 2000–2044 look alike.
//
//	bs_date(x)                  "2081-01-01"
//	bs_date(x, "D MMMM YYYY")   "1 Baisakh 2081" (layout tokens: see calendar/bs)
//	bs_format_np(x, layout)     Devanagari digits: "१ वैशाख २०८१"
//	bs_year(x) bs_month(x) bs_day(x)
//	bs_month_name(m) bs_month_name_np(m)
//	bs_to_ad("2081-01-01")      "2024-04-13"
//	bs_days_in_month(2081, 1)   31
//	devanagari_digits(s) ascii_digits(s)
//	fiscal_year(x)              Nepal: "2081/82"
//	fiscal_quarter(x)           1–4 (Q1 = Shrawan–Ashwin)
//	fiscal_year_start(x)        AD date of 1 Shrawan; x may also be "2081/82"
//	fiscal_year_end(x)          AD date of the last day of Asar
//	gregorian_fiscal_year(x, 4)          "2024/25"; (x, 10, "end") → "FY2025"
//	gregorian_fiscal_quarter(x, 4)
//	gregorian_fiscal_year_start(x, 4)    "2024-04-01"
//	gregorian_fiscal_year_end(x, 4)      "2025-03-31"
//
// Money. Amounts are decimal strings (or JSON numbers, read by their shortest
// decimal form); results are decimal strings with the currency's exact
// number of places. The currency comes first.
//
//	money_add("NPR", a, b, ...)          money_sub("NPR", a, b, ...)
//	money_sum("NPR", list)               money_cmp("NPR", a, b)  → -1, 0, 1
//	money_parse("NPR", "Rs 1,23,456.5")  "123456.50"
//	money_round("NPR", "1.005", "half_up")
//	money_format("NPR", a)               "Rs 12,34,567.50"; style "code" or "plain"
//	money_convert("USD", a, "NPR", "133.25", "half_even")
//	money_allocate("NPR", a, [50, 30, 20])        largest remainder, sums exactly
//	money_allocate("NPR", a, [1, 1, 1], "1")      in whole rupees
//	money_split("NPR", a, 3)                      equal parts
//	money_minor("NPR", a)  money_from_minor("NPR", 12345)
//	currency_minor_units("JPY")  currency_symbol("NPR")
var exprFunctions = map[string]bcl.EvalFunction{
	"bs_date":                     fnBSDate,
	"bs_format":                   fnBSFormat(false),
	"bs_format_np":                fnBSFormat(true),
	"bs_year":                     fnBSPart(func(d bs.Date) int { return d.Year }),
	"bs_month":                    fnBSPart(func(d bs.Date) int { return d.Month }),
	"bs_day":                      fnBSPart(func(d bs.Date) int { return d.Day }),
	"bs_month_name":               fnBSMonthName(bs.MonthName),
	"bs_month_name_np":            fnBSMonthName(bs.MonthNameNepali),
	"bs_to_ad":                    fnBSToAD,
	"bs_days_in_month":            fnBSDaysInMonth,
	"devanagari_digits":           fnDigits(bs.ToDevanagariDigits),
	"ascii_digits":                fnDigits(bs.FromDevanagariDigits),
	"fiscal_year":                 fnFiscalYear,
	"fiscal_quarter":              fnFiscalQuarter,
	"fiscal_year_start":           fnFiscalBound(true),
	"fiscal_year_end":             fnFiscalBound(false),
	"gregorian_fiscal_year":       fnGregorianFiscal("year"),
	"gregorian_fiscal_quarter":    fnGregorianFiscal("quarter"),
	"gregorian_fiscal_year_start": fnGregorianFiscal("start"),
	"gregorian_fiscal_year_end":   fnGregorianFiscal("end"),

	"money_add":            fnMoneyFold(false),
	"money_sub":            fnMoneyFold(true),
	"money_sum":            fnMoneySum,
	"money_cmp":            fnMoneyCmp,
	"money_parse":          fnMoneyParse,
	"money_round":          fnMoneyRound,
	"money_format":         fnMoneyFormat,
	"money_convert":        fnMoneyConvert,
	"money_allocate":       fnMoneyAllocate,
	"money_split":          fnMoneySplit,
	"money_minor":          fnMoneyMinor,
	"money_from_minor":     fnMoneyFromMinor,
	"currency_minor_units": fnCurrencyMinor,
	"currency_symbol":      fnCurrencySymbol,
}

const isoDate = "2006-01-02"

func arity(name string, args []any, min, max int) error {
	if len(args) < min || len(args) > max {
		if min == max {
			return fmt.Errorf("%s takes %d argument(s), got %d", name, min, len(args))
		}
		return fmt.Errorf("%s takes %d to %d arguments, got %d", name, min, max, len(args))
	}
	return nil
}

var dateOnly = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// dateArg reads a Gregorian date or instant, reporting whether it carried a
// time of day (and so an offset that matters).
func dateArg(value any) (time.Time, bool, error) {
	switch v := value.(type) {
	case time.Time:
		return v, true, nil
	case string:
		s := strings.TrimSpace(v)
		if dateOnly.MatchString(s) {
			t, err := time.Parse(isoDate, s)
			return t, false, err
		}
		for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05", "2006-01-02 15:04:05"} {
			if t, err := time.Parse(layout, s); err == nil {
				return t, true, nil
			}
		}
		return time.Time{}, false, fmt.Errorf("%q is not a date (YYYY-MM-DD) or an RFC 3339 time", v)
	case float64:
		sec, frac := math.Modf(v)
		return time.Unix(int64(sec), int64(frac*1e9)).UTC(), true, nil
	case int64:
		return time.Unix(v, 0).UTC(), true, nil
	case int:
		return time.Unix(int64(v), 0).UTC(), true, nil
	}
	return time.Time{}, false, fmt.Errorf("%v is not a date", value)
}

// bsArg converts a Gregorian date argument to BS.
func bsArg(value any) (bs.Date, error) {
	t, instant, err := dateArg(value)
	if err != nil {
		return bs.Date{}, err
	}
	if instant {
		return bs.FromInstant(t)
	}
	return bs.FromAD(t)
}

func intArg(name string, value any) (int, error) {
	f, ok := ToFloat(value)
	if !ok || f != math.Trunc(f) {
		return 0, fmt.Errorf("%s: %v is not a whole number", name, value)
	}
	return int(f), nil
}

func fnBSDate(args []any, _ *bcl.EvalOptions) (any, error) {
	if err := arity("bs_date", args, 1, 2); err != nil {
		return nil, err
	}
	d, err := bsArg(args[0])
	if err != nil {
		return nil, fmt.Errorf("bs_date: %w", err)
	}
	if len(args) == 2 {
		return d.Format(Stringify(args[1])), nil
	}
	return d.String(), nil
}

func fnBSFormat(nepali bool) bcl.EvalFunction {
	name := "bs_format"
	if nepali {
		name = "bs_format_np"
	}
	return func(args []any, _ *bcl.EvalOptions) (any, error) {
		if err := arity(name, args, 1, 2); err != nil {
			return nil, err
		}
		d, err := bsArg(args[0])
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		layout := "YYYY-MM-DD"
		if len(args) == 2 {
			layout = Stringify(args[1])
		}
		if nepali {
			return d.FormatNepali(layout), nil
		}
		return d.Format(layout), nil
	}
}

func fnBSPart(part func(bs.Date) int) bcl.EvalFunction {
	return func(args []any, _ *bcl.EvalOptions) (any, error) {
		if err := arity("bs_year/bs_month/bs_day", args, 1, 1); err != nil {
			return nil, err
		}
		d, err := bsArg(args[0])
		if err != nil {
			return nil, err
		}
		return part(d), nil
	}
}

func fnBSMonthName(name func(int) string) bcl.EvalFunction {
	return func(args []any, _ *bcl.EvalOptions) (any, error) {
		if err := arity("bs_month_name", args, 1, 1); err != nil {
			return nil, err
		}
		m, err := intArg("bs_month_name", args[0])
		if err != nil || m < 1 || m > 12 {
			return nil, fmt.Errorf("bs_month_name: month must be 1-12, got %v", args[0])
		}
		return name(m), nil
	}
}

func fnBSToAD(args []any, _ *bcl.EvalOptions) (any, error) {
	if err := arity("bs_to_ad", args, 1, 1); err != nil {
		return nil, err
	}
	d, err := bs.Parse(Stringify(args[0]))
	if err != nil {
		return nil, fmt.Errorf("bs_to_ad: %w", err)
	}
	t, err := d.ToAD()
	if err != nil {
		return nil, fmt.Errorf("bs_to_ad: %w", err)
	}
	return t.Format(isoDate), nil
}

func fnBSDaysInMonth(args []any, _ *bcl.EvalOptions) (any, error) {
	if err := arity("bs_days_in_month", args, 2, 2); err != nil {
		return nil, err
	}
	y, err := intArg("bs_days_in_month", args[0])
	if err != nil {
		return nil, err
	}
	m, err := intArg("bs_days_in_month", args[1])
	if err != nil {
		return nil, err
	}
	n, err := bs.DaysInMonth(y, m)
	if err != nil {
		return nil, fmt.Errorf("bs_days_in_month: %w", err)
	}
	return n, nil
}

func fnDigits(convert func(string) string) bcl.EvalFunction {
	return func(args []any, _ *bcl.EvalOptions) (any, error) {
		if err := arity("devanagari_digits/ascii_digits", args, 1, 1); err != nil {
			return nil, err
		}
		return convert(Stringify(args[0])), nil
	}
}

var fiscalLabel = regexp.MustCompile(`^\d{4}\s*[/-]\s*\d{2,4}$`)

// fiscalArg reads a date or an explicit fiscal year label ("2081/82").
func fiscalArg(value any) (bs.FiscalYear, error) {
	if s, ok := value.(string); ok && fiscalLabel.MatchString(strings.TrimSpace(s)) {
		return bs.ParseFiscalYear(s)
	}
	d, err := bsArg(value)
	if err != nil {
		return 0, err
	}
	return bs.FiscalYearOf(d), nil
}

func fnFiscalYear(args []any, _ *bcl.EvalOptions) (any, error) {
	if err := arity("fiscal_year", args, 1, 1); err != nil {
		return nil, err
	}
	fy, err := fiscalArg(args[0])
	if err != nil {
		return nil, fmt.Errorf("fiscal_year: %w", err)
	}
	return fy.String(), nil
}

func fnFiscalQuarter(args []any, _ *bcl.EvalOptions) (any, error) {
	if err := arity("fiscal_quarter", args, 1, 1); err != nil {
		return nil, err
	}
	d, err := bsArg(args[0])
	if err != nil {
		return nil, fmt.Errorf("fiscal_quarter: %w", err)
	}
	return bs.FiscalQuarter(d), nil
}

func fnFiscalBound(start bool) bcl.EvalFunction {
	name := map[bool]string{true: "fiscal_year_start", false: "fiscal_year_end"}[start]
	return func(args []any, _ *bcl.EvalOptions) (any, error) {
		if err := arity(name, args, 1, 1); err != nil {
			return nil, err
		}
		fy, err := fiscalArg(args[0])
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		var d bs.Date
		if start {
			d, err = fy.Start()
		} else {
			d, err = fy.End()
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		t, err := d.ToAD()
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		return t.Format(isoDate), nil
	}
}

func fnGregorianFiscal(what string) bcl.EvalFunction {
	name := "gregorian_fiscal_" + map[string]string{"year": "year", "quarter": "quarter", "start": "year_start", "end": "year_end"}[what]
	return func(args []any, _ *bcl.EvalOptions) (any, error) {
		if err := arity(name, args, 2, 3); err != nil {
			return nil, err
		}
		t, _, err := dateArg(args[0])
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		month, err := intArg(name, args[1])
		if err != nil {
			return nil, err
		}
		cal, err := fiscal.New(month)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		if len(args) == 3 {
			switch strings.ToLower(Stringify(args[2])) {
			case "end":
				cal.NameByEndYear = true
			case "start", "":
			default:
				return nil, fmt.Errorf("%s: naming must be \"start\" or \"end\"", name)
			}
		}
		switch what {
		case "year":
			return cal.Label(t), nil
		case "quarter":
			return cal.Quarter(t), nil
		case "start":
			return cal.Start(t).Format(isoDate), nil
		}
		return cal.End(t).Format(isoDate), nil
	}
}

// ---------------------------------------------------------------------------
// Money
// ---------------------------------------------------------------------------

func currencyArg(name string, value any) (money.Currency, error) {
	code := Stringify(value)
	c, ok := money.Lookup(code)
	if !ok {
		return money.Currency{}, fmt.Errorf("%s: unknown currency %q", name, code)
	}
	return c, nil
}

func amountArg(name string, value any, c money.Currency) (money.Money, error) {
	m, err := moneyOf(value, c)
	if err != nil {
		return money.Money{}, fmt.Errorf("%s: %w", name, err)
	}
	return m, nil
}

func modeArg(name string, args []any, index int) (money.RoundingMode, error) {
	if len(args) <= index {
		return money.HalfEven, nil
	}
	mode, err := money.ParseRoundingMode(Stringify(args[index]))
	if err != nil {
		return "", fmt.Errorf("%s: %w", name, err)
	}
	return mode, nil
}

func fnMoneyFold(subtract bool) bcl.EvalFunction {
	name := map[bool]string{false: "money_add", true: "money_sub"}[subtract]
	return func(args []any, _ *bcl.EvalOptions) (any, error) {
		if len(args) < 2 {
			return nil, fmt.Errorf("%s takes a currency and at least one amount", name)
		}
		c, err := currencyArg(name, args[0])
		if err != nil {
			return nil, err
		}
		total, err := amountArg(name, args[1], c)
		if err != nil {
			return nil, err
		}
		for _, raw := range args[2:] {
			next, err := amountArg(name, raw, c)
			if err != nil {
				return nil, err
			}
			if subtract {
				total, err = total.Sub(next)
			} else {
				total, err = total.Add(next)
			}
			if err != nil {
				return nil, fmt.Errorf("%s: %w", name, err)
			}
		}
		return total.String(), nil
	}
}

func fnMoneySum(args []any, _ *bcl.EvalOptions) (any, error) {
	if err := arity("money_sum", args, 2, 2); err != nil {
		return nil, err
	}
	c, err := currencyArg("money_sum", args[0])
	if err != nil {
		return nil, err
	}
	list, ok := args[1].([]any)
	if !ok && args[1] != nil {
		return nil, fmt.Errorf("money_sum: the second argument must be a list")
	}
	total := money.Zero(c)
	for _, raw := range list {
		m, err := amountArg("money_sum", raw, c)
		if err != nil {
			return nil, err
		}
		if total, err = total.Add(m); err != nil {
			return nil, fmt.Errorf("money_sum: %w", err)
		}
	}
	return total.String(), nil
}

func fnMoneyCmp(args []any, _ *bcl.EvalOptions) (any, error) {
	if err := arity("money_cmp", args, 3, 3); err != nil {
		return nil, err
	}
	c, err := currencyArg("money_cmp", args[0])
	if err != nil {
		return nil, err
	}
	a, err := amountArg("money_cmp", args[1], c)
	if err != nil {
		return nil, err
	}
	b, err := amountArg("money_cmp", args[2], c)
	if err != nil {
		return nil, err
	}
	return a.Cmp(b)
}

func fnMoneyParse(args []any, _ *bcl.EvalOptions) (any, error) {
	if err := arity("money_parse", args, 2, 2); err != nil {
		return nil, err
	}
	c, err := currencyArg("money_parse", args[0])
	if err != nil {
		return nil, err
	}
	m, err := amountArg("money_parse", args[1], c)
	if err != nil {
		return nil, err
	}
	return m.String(), nil
}

func fnMoneyRound(args []any, _ *bcl.EvalOptions) (any, error) {
	if err := arity("money_round", args, 2, 3); err != nil {
		return nil, err
	}
	c, err := currencyArg("money_round", args[0])
	if err != nil {
		return nil, err
	}
	mode, err := modeArg("money_round", args, 2)
	if err != nil {
		return nil, err
	}
	text := Stringify(args[1])
	if f, ok := args[1].(float64); ok {
		text = strconv.FormatFloat(f, 'f', -1, 64)
	}
	m, err := money.ParseRound(text, c, mode)
	if err != nil {
		return nil, fmt.Errorf("money_round: %w", err)
	}
	return m.String(), nil
}

func fnMoneyFormat(args []any, _ *bcl.EvalOptions) (any, error) {
	if err := arity("money_format", args, 2, 3); err != nil {
		return nil, err
	}
	c, err := currencyArg("money_format", args[0])
	if err != nil {
		return nil, err
	}
	m, err := amountArg("money_format", args[1], c)
	if err != nil {
		return nil, err
	}
	opts := money.FormatOptions{Symbol: true}
	if len(args) == 3 {
		switch strings.ToLower(Stringify(args[2])) {
		case "symbol", "":
		case "code":
			opts = money.FormatOptions{Code: true}
		case "plain":
			opts = money.FormatOptions{}
		default:
			return nil, fmt.Errorf("money_format: style must be symbol, code or plain")
		}
	}
	return m.Format(opts), nil
}

func fnMoneyConvert(args []any, _ *bcl.EvalOptions) (any, error) {
	if err := arity("money_convert", args, 4, 5); err != nil {
		return nil, err
	}
	from, err := currencyArg("money_convert", args[0])
	if err != nil {
		return nil, err
	}
	m, err := amountArg("money_convert", args[1], from)
	if err != nil {
		return nil, err
	}
	to, err := currencyArg("money_convert", args[2])
	if err != nil {
		return nil, err
	}
	rate, err := decimalOf(args[3])
	if err != nil {
		return nil, fmt.Errorf("money_convert: rate: %w", err)
	}
	mode, err := modeArg("money_convert", args, 4)
	if err != nil {
		return nil, err
	}
	out, err := m.Convert(to, rate, mode)
	if err != nil {
		return nil, fmt.Errorf("money_convert: %w", err)
	}
	return out.String(), nil
}

func fnMoneyAllocate(args []any, _ *bcl.EvalOptions) (any, error) {
	if err := arity("money_allocate", args, 3, 4); err != nil {
		return nil, err
	}
	c, err := currencyArg("money_allocate", args[0])
	if err != nil {
		return nil, err
	}
	m, err := amountArg("money_allocate", args[1], c)
	if err != nil {
		return nil, err
	}
	ratios, err := ratiosOf(args[2])
	if err != nil {
		return nil, fmt.Errorf("money_allocate: %w", err)
	}
	var unit any
	if len(args) == 4 {
		unit = args[3]
	}
	step, err := unitOf(unit, c)
	if err != nil {
		return nil, fmt.Errorf("money_allocate: %w", err)
	}
	parts, err := m.AllocateUnit(step, ratios...)
	if err != nil {
		return nil, fmt.Errorf("money_allocate: %w", err)
	}
	return amountStrings(parts), nil
}

func fnMoneySplit(args []any, opts *bcl.EvalOptions) (any, error) {
	if err := arity("money_split", args, 3, 4); err != nil {
		return nil, err
	}
	n, err := intArg("money_split", args[2])
	if err != nil {
		return nil, err
	}
	if n < 1 || n > 10000 {
		return nil, fmt.Errorf("money_split: parts must be 1-10000, got %d", n)
	}
	ratios := make([]any, n)
	for i := range ratios {
		ratios[i] = 1
	}
	rest := []any{args[0], args[1], ratios}
	if len(args) == 4 {
		rest = append(rest, args[3])
	}
	return fnMoneyAllocate(rest, opts)
}

func fnMoneyMinor(args []any, _ *bcl.EvalOptions) (any, error) {
	if err := arity("money_minor", args, 2, 2); err != nil {
		return nil, err
	}
	c, err := currencyArg("money_minor", args[0])
	if err != nil {
		return nil, err
	}
	m, err := amountArg("money_minor", args[1], c)
	if err != nil {
		return nil, err
	}
	return m.Minor, nil
}

func fnMoneyFromMinor(args []any, _ *bcl.EvalOptions) (any, error) {
	if err := arity("money_from_minor", args, 2, 2); err != nil {
		return nil, err
	}
	c, err := currencyArg("money_from_minor", args[0])
	if err != nil {
		return nil, err
	}
	var minor int64
	switch v := args[1].(type) {
	case int64:
		minor = v
	case int:
		minor = int64(v)
	case string:
		if minor, err = strconv.ParseInt(strings.TrimSpace(v), 10, 64); err != nil {
			return nil, fmt.Errorf("money_from_minor: %q is not a whole number", v)
		}
	default:
		f, ok := ToFloat(v)
		if !ok || f != math.Trunc(f) || math.Abs(f) > 1<<53 {
			return nil, fmt.Errorf("money_from_minor: %v is not a whole number", v)
		}
		minor = int64(f)
	}
	return money.New(minor, c).String(), nil
}

func fnCurrencyMinor(args []any, _ *bcl.EvalOptions) (any, error) {
	if err := arity("currency_minor_units", args, 1, 1); err != nil {
		return nil, err
	}
	c, err := currencyArg("currency_minor_units", args[0])
	if err != nil {
		return nil, err
	}
	return c.Minor, nil
}

func fnCurrencySymbol(args []any, _ *bcl.EvalOptions) (any, error) {
	if err := arity("currency_symbol", args, 1, 1); err != nil {
		return nil, err
	}
	c, err := currencyArg("currency_symbol", args[0])
	if err != nil {
		return nil, err
	}
	return c.Symbol, nil
}
