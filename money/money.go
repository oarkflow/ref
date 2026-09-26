package money

import (
	"errors"
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"
)

// Errors a caller may want to tell apart.
var (
	ErrCurrencyMismatch = errors.New("money: currencies differ")
	ErrOverflow         = errors.New("money: amount out of range")
	ErrSyntax           = errors.New("money: not a decimal amount")
	ErrPrecision        = errors.New("money: more decimal places than the currency allows")
)

// Money is an exact amount: a count of minor units in one currency.
type Money struct {
	Minor    int64
	Currency Currency
}

// New returns an amount of minor units.
func New(minor int64, c Currency) Money { return Money{Minor: minor, Currency: c} }

// Zero returns a zero amount in a currency.
func Zero(c Currency) Money { return Money{Currency: c} }

// Parse reads a decimal amount ("1234.5", "-3", "1,234.50", "Rs 1,23,456.00")
// in a currency. Group separators, surrounding spaces, the currency's symbol
// and its code are ignored. More decimal places than the currency allows is
// ErrPrecision rather than a silent rounding; use ParseRound to round.
func Parse(s string, c Currency) (Money, error) {
	r, err := parseRat(clean(s, c))
	if err != nil {
		return Money{}, err
	}
	minor, exact, err := toMinor(r, c.Minor, HalfEven)
	if err != nil {
		return Money{}, err
	}
	if !exact {
		return Money{}, fmt.Errorf("%w: %s allows %d", ErrPrecision, c.Code, c.Minor)
	}
	return Money{Minor: minor, Currency: c}, nil
}

// ParseRound reads a decimal amount and rounds it to the currency's minor
// unit with the given mode.
func ParseRound(s string, c Currency, mode RoundingMode) (Money, error) {
	r, err := parseRat(clean(s, c))
	if err != nil {
		return Money{}, err
	}
	minor, _, err := toMinor(r, c.Minor, mode)
	if err != nil {
		return Money{}, err
	}
	return Money{Minor: minor, Currency: c}, nil
}

// FromFloat converts a float by its shortest decimal representation, so 0.1
// becomes exactly one tenth rather than 0.1000000000000000055. It still
// refuses excess precision.
func FromFloat(f float64, c Currency) (Money, error) {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return Money{}, ErrSyntax
	}
	return Parse(strconv.FormatFloat(f, 'f', -1, 64), c)
}

func clean(s string, c Currency) string {
	s = strings.TrimSpace(s)
	if c.Code != "" {
		s = strings.TrimSpace(strings.TrimPrefix(strings.TrimSuffix(s, c.Code), c.Code))
	}
	neg := false
	if strings.HasPrefix(s, "-") {
		neg, s = true, strings.TrimSpace(s[1:])
	}
	if c.Symbol != "" {
		s = strings.TrimSpace(strings.TrimPrefix(s, c.Symbol))
	}
	s = strings.NewReplacer(",", "", "_", "", " ", "", " ", "").Replace(s)
	if neg {
		s = "-" + s
	}
	return s
}

// parseRat reads a plain decimal exactly. Exponents, fractions and
// hexadecimal are refused: an amount is written as digits.
func parseRat(s string) (*big.Rat, error) {
	body := strings.TrimPrefix(strings.TrimPrefix(s, "-"), "+")
	if body == "" || body == "." {
		return nil, ErrSyntax
	}
	dots := 0
	for _, r := range body {
		switch {
		case r == '.':
			dots++
		case r < '0' || r > '9':
			return nil, fmt.Errorf("%w: %q", ErrSyntax, s)
		}
	}
	if dots > 1 {
		return nil, fmt.Errorf("%w: %q", ErrSyntax, s)
	}
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrSyntax, s)
	}
	return r, nil
}

// ParseDecimal reads a plain decimal string exactly, for rates and ratios.
// A fraction ("1/3") is also accepted, since a rate is often quoted that way.
func ParseDecimal(s string) (*big.Rat, error) {
	s = strings.TrimSpace(s)
	if strings.Count(s, "/") == 1 {
		num, den, _ := strings.Cut(s, "/")
		n, err := parseRat(strings.TrimSpace(num))
		if err != nil {
			return nil, err
		}
		d, err := parseRat(strings.TrimSpace(den))
		if err != nil {
			return nil, err
		}
		if d.Sign() == 0 {
			return nil, fmt.Errorf("%w: division by zero in %q", ErrSyntax, s)
		}
		return n.Quo(n, d), nil
	}
	return parseRat(strings.ReplaceAll(s, "_", ""))
}

// toMinor scales a decimal to minor units and rounds it, reporting whether
// the value was already exact.
func toMinor(r *big.Rat, scale int, mode RoundingMode) (int64, bool, error) {
	scaled := new(big.Rat).Mul(r, new(big.Rat).SetInt(pow10(scale)))
	n, exact, err := Round(scaled, mode)
	if err != nil {
		return 0, false, err
	}
	if !n.IsInt64() {
		return 0, false, ErrOverflow
	}
	return n.Int64(), exact, nil
}

func pow10(n int) *big.Int {
	return new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(n)), nil)
}

// Rat returns the amount as an exact decimal in major units.
func (m Money) Rat() *big.Rat {
	return new(big.Rat).SetFrac(big.NewInt(m.Minor), pow10(m.Currency.Minor))
}

// String renders the plain decimal: "1234.50", "-3.000", "1500".
func (m Money) String() string { return FormatMinor(m.Minor, m.Currency.Minor) }

// FormatMinor renders minor units as a plain decimal with scale places.
func FormatMinor(minor int64, scale int) string {
	neg := minor < 0
	digits := strconv.FormatUint(absU(minor), 10)
	if scale > 0 {
		if len(digits) <= scale {
			digits = strings.Repeat("0", scale-len(digits)+1) + digits
		}
		digits = digits[:len(digits)-scale] + "." + digits[len(digits)-scale:]
	}
	if neg {
		return "-" + digits
	}
	return digits
}

func absU(v int64) uint64 {
	if v < 0 {
		return uint64(-(v + 1)) + 1
	}
	return uint64(v)
}

// FormatOptions controls Format.
type FormatOptions struct {
	// Symbol prefixes the currency symbol (or the code when it has none).
	Symbol bool
	// Code suffixes the ISO code: "1,234.50 NPR".
	Code bool
	// Grouping overrides the currency's default grouping.
	Grouping Grouping
}

// Format renders the amount for display: grouped digits, and optionally the
// symbol or code. NPR and INR group the Indian way by default (12,34,567.00).
func (m Money) Format(opts FormatOptions) string {
	plain := m.String()
	neg := strings.HasPrefix(plain, "-")
	plain = strings.TrimPrefix(plain, "-")
	whole, frac, hasFrac := strings.Cut(plain, ".")
	grouping := opts.Grouping
	if grouping == "" {
		grouping = m.Currency.Grouping
	}
	whole = group(whole, grouping)
	out := whole
	if hasFrac {
		out += "." + frac
	}
	if neg {
		out = "-" + out
	}
	if opts.Symbol {
		symbol := m.Currency.Symbol
		if symbol == "" {
			symbol = m.Currency.Code
		}
		sep := ""
		if len([]rune(symbol)) > 1 {
			sep = " "
		}
		if neg {
			out = "-" + symbol + sep + strings.TrimPrefix(out, "-")
		} else {
			out = symbol + sep + out
		}
	}
	if opts.Code {
		out += " " + m.Currency.Code
	}
	return out
}

func group(digits string, g Grouping) string {
	if g == GroupNone || len(digits) <= 3 {
		return digits
	}
	if g == GroupIndian {
		head, tail := digits[:len(digits)-3], digits[len(digits)-3:]
		var parts []string
		for len(head) > 2 {
			parts = append([]string{head[len(head)-2:]}, parts...)
			head = head[:len(head)-2]
		}
		if head != "" {
			parts = append([]string{head}, parts...)
		}
		return strings.Join(append(parts, tail), ",")
	}
	var parts []string
	for len(digits) > 3 {
		parts = append([]string{digits[len(digits)-3:]}, parts...)
		digits = digits[:len(digits)-3]
	}
	return strings.Join(append([]string{digits}, parts...), ",")
}

func (m Money) same(o Money) error {
	if m.Currency.Code != o.Currency.Code {
		return fmt.Errorf("%w: %s and %s", ErrCurrencyMismatch, m.Currency.Code, o.Currency.Code)
	}
	return nil
}

// Add returns m + o, refusing mixed currencies and overflow.
func (m Money) Add(o Money) (Money, error) {
	if err := m.same(o); err != nil {
		return Money{}, err
	}
	sum := m.Minor + o.Minor
	if (o.Minor > 0 && sum < m.Minor) || (o.Minor < 0 && sum > m.Minor) {
		return Money{}, ErrOverflow
	}
	return Money{Minor: sum, Currency: m.Currency}, nil
}

// Sub returns m - o, refusing mixed currencies and overflow.
func (m Money) Sub(o Money) (Money, error) {
	if o.Minor == math.MinInt64 {
		return Money{}, ErrOverflow
	}
	return m.Add(Money{Minor: -o.Minor, Currency: o.Currency})
}

// Neg returns -m.
func (m Money) Neg() (Money, error) {
	if m.Minor == math.MinInt64 {
		return Money{}, ErrOverflow
	}
	return Money{Minor: -m.Minor, Currency: m.Currency}, nil
}

// Cmp compares two amounts of one currency: -1, 0 or 1.
func (m Money) Cmp(o Money) (int, error) {
	if err := m.same(o); err != nil {
		return 0, err
	}
	switch {
	case m.Minor < o.Minor:
		return -1, nil
	case m.Minor > o.Minor:
		return 1, nil
	}
	return 0, nil
}

// IsZero reports whether the amount is zero.
func (m Money) IsZero() bool { return m.Minor == 0 }

// Sum adds amounts of one currency.
func Sum(c Currency, amounts ...Money) (Money, error) {
	total := Zero(c)
	for _, a := range amounts {
		var err error
		if total, err = total.Add(a); err != nil {
			return Money{}, err
		}
	}
	return total, nil
}

// Mul multiplies by an exact decimal factor (a quantity, a tax rate) and
// rounds once with the given mode.
func (m Money) Mul(factor *big.Rat, mode RoundingMode) (Money, error) {
	product := new(big.Rat).Mul(new(big.Rat).SetInt64(m.Minor), factor)
	n, _, err := Round(product, mode)
	if err != nil {
		return Money{}, err
	}
	if !n.IsInt64() {
		return Money{}, ErrOverflow
	}
	return Money{Minor: n.Int64(), Currency: m.Currency}, nil
}

// Convert changes currency at an explicit rate — units of the target per one
// unit of the source ("133.25" NPR per USD) — rounding once with mode.
func (m Money) Convert(to Currency, rate *big.Rat, mode RoundingMode) (Money, error) {
	if rate == nil || rate.Sign() <= 0 {
		return Money{}, fmt.Errorf("money: a conversion rate must be positive")
	}
	major := new(big.Rat).Mul(m.Rat(), rate)
	minor, _, err := toMinor(major, to.Minor, mode)
	if err != nil {
		return Money{}, err
	}
	return Money{Minor: minor, Currency: to}, nil
}

// Round rescales an amount to a coarser step — cash rounding to 0.05, or to
// whole rupees — expressed in minor units (5 for 0.05 in a 2-decimal
// currency). The result is a multiple of step.
func (m Money) Round(step int64, mode RoundingMode) (Money, error) {
	if step <= 0 {
		return Money{}, fmt.Errorf("money: rounding step must be positive")
	}
	q, _, err := Round(new(big.Rat).SetFrac(big.NewInt(m.Minor), big.NewInt(step)), mode)
	if err != nil {
		return Money{}, err
	}
	q.Mul(q, big.NewInt(step))
	if !q.IsInt64() {
		return Money{}, ErrOverflow
	}
	return Money{Minor: q.Int64(), Currency: m.Currency}, nil
}
