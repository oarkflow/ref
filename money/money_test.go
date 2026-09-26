package money

import (
	"errors"
	"math"
	"math/big"
	"math/rand"
	"testing"
)

func TestRegistryBuiltins(t *testing.T) {
	r := NewRegistry()
	for code, minor := range map[string]int{"NPR": 2, "INR": 2, "USD": 2, "EUR": 2, "GBP": 2, "JPY": 0, "KWD": 3, "BHD": 3, "CNY": 2} {
		c, ok := r.Lookup(code)
		if !ok || c.Minor != minor || c.Symbol == "" {
			t.Fatalf("%s: %+v %v", code, c, ok)
		}
	}
	if c, ok := r.Lookup(" npr "); !ok || c.Code != "NPR" {
		t.Fatalf("case-insensitive lookup: %+v", c)
	}
	if _, ok := r.Lookup("XXX"); ok {
		t.Fatal("unknown code found")
	}
}

func TestRegistryExtend(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(Currency{Code: "PTS", Minor: 0, Symbol: "pts"}); err != nil {
		t.Fatal(err)
	}
	if c, ok := r.Lookup("PTS"); !ok || c.Minor != 0 {
		t.Fatalf("custom: %+v", c)
	}
	if err := r.Register(Currency{Code: "JPY", Minor: 2}); err == nil {
		t.Fatal("redefining JPY's minor units must fail")
	}
	if err := r.Register(Currency{Code: "USD", Minor: 2, Symbol: "US$"}); err != nil {
		t.Fatalf("re-symbolling USD: %v", err)
	}
	for _, bad := range []Currency{{Code: "x"}, {Code: "ABC", Minor: 9}, {Code: "ABC", Grouping: "odd"}} {
		if err := r.Register(bad); err == nil {
			t.Fatalf("%+v accepted", bad)
		}
	}
	clone := r.Clone()
	_ = clone.Register(Currency{Code: "ZZZ", Minor: 1})
	if _, ok := r.Lookup("ZZZ"); ok {
		t.Fatal("clone shares state")
	}
	if len(r.Codes()) < 9 {
		t.Fatal("codes")
	}
}

func npr(t *testing.T, s string) Money {
	t.Helper()
	m, err := Parse(s, MustLookupDefault("NPR"))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func MustLookupDefault(code string) Currency { return Default().MustLookup(code) }

func TestParseAndString(t *testing.T) {
	cases := []struct {
		in, code, want string
	}{
		{"1234.5", "NPR", "1234.50"},
		{"-3", "NPR", "-3.00"},
		{"Rs 1,23,456.07", "NPR", "123456.07"},
		{"-Rs 5", "NPR", "-5.00"},
		{"1,500", "JPY", "1500"},
		{"0.125", "KWD", "0.125"},
		{"12.3 USD", "USD", "12.30"},
		{".5", "EUR", "0.50"},
	}
	for _, c := range cases {
		m, err := Parse(c.in, MustLookupDefault(c.code))
		if err != nil || m.String() != c.want {
			t.Fatalf("Parse(%q, %s) = %v, %v; want %s", c.in, c.code, m, err, c.want)
		}
	}
	for _, bad := range []string{"", "abc", "1.2.3", "1e5", "0x10", "--1"} {
		if _, err := Parse(bad, MustLookupDefault("USD")); !errors.Is(err, ErrSyntax) {
			t.Fatalf("Parse(%q) err = %v", bad, err)
		}
	}
	if _, err := Parse("1.005", MustLookupDefault("USD")); !errors.Is(err, ErrPrecision) {
		t.Fatalf("precision: %v", err)
	}
	if _, err := Parse("1.5", MustLookupDefault("JPY")); !errors.Is(err, ErrPrecision) {
		t.Fatalf("JPY precision: %v", err)
	}
	if _, err := Parse("99999999999999999999", MustLookupDefault("USD")); !errors.Is(err, ErrOverflow) {
		t.Fatalf("overflow: %v", err)
	}
	m, err := ParseRound("1.005", MustLookupDefault("USD"), HalfUp)
	if err != nil || m.String() != "1.01" {
		t.Fatalf("ParseRound: %v %v", m, err)
	}
	f, err := FromFloat(0.1, MustLookupDefault("USD"))
	if err != nil || f.Minor != 10 {
		t.Fatalf("FromFloat: %v %v", f, err)
	}
	if FormatMinor(math.MinInt64, 2) != "-92233720368547758.08" {
		t.Fatal(FormatMinor(math.MinInt64, 2))
	}
	if FormatMinor(5, 3) != "0.005" || FormatMinor(-5, 2) != "-0.05" || FormatMinor(7, 0) != "7" {
		t.Fatal("FormatMinor small values")
	}
}

func TestFormat(t *testing.T) {
	cases := []struct {
		amount, code string
		opts         FormatOptions
		want         string
	}{
		{"1234567.5", "NPR", FormatOptions{}, "12,34,567.50"},
		{"1234567.5", "NPR", FormatOptions{Symbol: true}, "Rs 12,34,567.50"},
		{"-1234567.5", "INR", FormatOptions{Symbol: true}, "-₹12,34,567.50"},
		{"1234567.5", "USD", FormatOptions{Symbol: true}, "$1,234,567.50"},
		{"1234567", "JPY", FormatOptions{Code: true}, "1,234,567 JPY"},
		{"1234.567", "KWD", FormatOptions{Symbol: true}, "KD 1,234.567"},
		{"100000", "NPR", FormatOptions{Grouping: GroupThousands}, "100,000.00"},
		{"100000", "NPR", FormatOptions{Grouping: GroupNone}, "100000.00"},
		{"999", "NPR", FormatOptions{}, "999.00"},
		{"12345", "INR", FormatOptions{}, "12,345.00"},
		{"123456789", "INR", FormatOptions{}, "12,34,56,789.00"},
	}
	for _, c := range cases {
		m, err := Parse(c.amount, MustLookupDefault(c.code))
		if err != nil {
			t.Fatal(err)
		}
		if got := m.Format(c.opts); got != c.want {
			t.Fatalf("Format(%s %s %+v) = %q, want %q", c.amount, c.code, c.opts, got, c.want)
		}
	}
}

func TestArithmetic(t *testing.T) {
	a, b := npr(t, "0.10"), npr(t, "0.20")
	sum, err := a.Add(b)
	if err != nil || sum.String() != "0.30" {
		t.Fatalf("0.10+0.20 = %v %v", sum, err)
	}
	diff, _ := a.Sub(b)
	if diff.String() != "-0.10" {
		t.Fatal(diff)
	}
	usd, _ := Parse("1", MustLookupDefault("USD"))
	if _, err := a.Add(usd); !errors.Is(err, ErrCurrencyMismatch) {
		t.Fatalf("mismatch: %v", err)
	}
	big := New(math.MaxInt64, a.Currency)
	if _, err := big.Add(New(1, a.Currency)); !errors.Is(err, ErrOverflow) {
		t.Fatalf("overflow: %v", err)
	}
	if _, err := New(math.MinInt64, a.Currency).Sub(New(1, a.Currency)); !errors.Is(err, ErrOverflow) {
		t.Fatalf("underflow: %v", err)
	}
	if _, err := New(math.MinInt64, a.Currency).Neg(); !errors.Is(err, ErrOverflow) {
		t.Fatal("neg overflow")
	}
	if c, _ := a.Cmp(b); c != -1 {
		t.Fatal("cmp")
	}
	total, err := Sum(a.Currency, a, b, b)
	if err != nil || total.String() != "0.50" {
		t.Fatal(total, err)
	}
	vat, err := npr(t, "99.99").Mul(big13(), HalfUp)
	if err != nil || vat.String() != "13.00" {
		t.Fatalf("13%% of 99.99 = %v %v", vat, err)
	}
	cash, _ := npr(t, "10.37").Round(5, HalfUp)
	if cash.String() != "10.35" {
		t.Fatal(cash)
	}
	whole, _ := npr(t, "10.50").Round(100, HalfEven)
	if whole.String() != "10.00" {
		t.Fatal(whole)
	}
}

func big13() *big.Rat { r, _ := ParseDecimal("0.13"); return r }

func TestRoundingModes(t *testing.T) {
	type row struct{ in, halfEven, halfUp, halfDown, down, up, floor, ceiling string }
	rows := []row{
		{"2.5", "2", "3", "2", "2", "3", "2", "3"},
		{"3.5", "4", "4", "3", "3", "4", "3", "4"},
		{"-2.5", "-2", "-3", "-2", "-2", "-3", "-3", "-2"},
		{"2.4", "2", "2", "2", "2", "3", "2", "3"},
		{"2.6", "3", "3", "3", "2", "3", "2", "3"},
		{"-2.6", "-3", "-3", "-3", "-2", "-3", "-3", "-2"},
		{"7", "7", "7", "7", "7", "7", "7", "7"},
	}
	modes := []RoundingMode{HalfEven, HalfUp, HalfDown, Down, Up, Floor, Ceiling}
	for _, r := range rows {
		want := []string{r.halfEven, r.halfUp, r.halfDown, r.down, r.up, r.floor, r.ceiling}
		in, _ := ParseDecimal(r.in)
		for i, mode := range modes {
			got, _, err := Round(in, mode)
			if err != nil || got.String() != want[i] {
				t.Fatalf("Round(%s, %s) = %v, want %s", r.in, mode, got, want[i])
			}
		}
	}
	for name, want := range map[string]RoundingMode{"": HalfEven, "bankers": HalfEven, "HALF-UP": HalfUp, "truncate": Down, "ceil": Ceiling} {
		if got, err := ParseRoundingMode(name); err != nil || got != want {
			t.Fatalf("ParseRoundingMode(%q) = %v %v", name, got, err)
		}
	}
	if _, err := ParseRoundingMode("sideways"); err == nil {
		t.Fatal("unknown mode accepted")
	}
}

func TestConvert(t *testing.T) {
	usd, _ := Parse("10.00", MustLookupDefault("USD"))
	rate, _ := ParseDecimal("133.255")
	got, err := usd.Convert(MustLookupDefault("NPR"), rate, HalfEven)
	if err != nil || got.String() != "1332.55" {
		t.Fatalf("USD→NPR: %v %v", got, err)
	}
	one, _ := Parse("1.00", MustLookupDefault("USD"))
	// 1 × 133.255 = 133.255 → a tie at the paisa: half-even goes to .26 (6 is even), half-up .26, down .25.
	for mode, want := range map[RoundingMode]string{HalfEven: "133.26", HalfUp: "133.26", Down: "133.25", Up: "133.26"} {
		got, _ := one.Convert(MustLookupDefault("NPR"), rate, mode)
		if got.String() != want {
			t.Fatalf("%s: %v want %s", mode, got, want)
		}
	}
	r2, _ := ParseDecimal("0.125")
	half, _ := Parse("1.00", MustLookupDefault("USD"))
	for mode, want := range map[RoundingMode]string{HalfEven: "0.12", HalfUp: "0.13", Down: "0.12", Up: "0.13"} {
		got, _ := half.Convert(MustLookupDefault("EUR"), r2, mode)
		if got.String() != want {
			t.Fatalf("0.125 %s: %v want %s", mode, got, want)
		}
	}
	jpy, _ := Parse("1000", MustLookupDefault("JPY"))
	r3, _ := ParseDecimal("1/150")
	got, _ = jpy.Convert(MustLookupDefault("USD"), r3, HalfEven)
	if got.String() != "6.67" {
		t.Fatalf("JPY→USD: %v", got)
	}
	kwd, _ := Parse("1.000", MustLookupDefault("KWD"))
	r4, _ := ParseDecimal("3.2567")
	got, _ = kwd.Convert(MustLookupDefault("USD"), r4, Down)
	if got.String() != "3.25" {
		t.Fatalf("KWD→USD: %v", got)
	}
	if _, err := usd.Convert(MustLookupDefault("NPR"), new(big.Rat), HalfEven); err == nil {
		t.Fatal("zero rate accepted")
	}
	if _, err := ParseDecimal("1/0"); err == nil {
		t.Fatal("1/0 accepted")
	}
}

func minors(ms []Money) []int64 {
	out := make([]int64, len(ms))
	for i, m := range ms {
		out[i] = m.Minor
	}
	return out
}

func equal(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestAllocate(t *testing.T) {
	c := MustLookupDefault("NPR")
	cases := []struct {
		total  int64
		ratios []int64
		want   []int64
	}{
		{100, []int64{1, 1, 1}, []int64{34, 33, 33}},
		{5, []int64{3, 7}, []int64{2, 3}}, // 1.5 / 3.5: tie → earlier
		{100, []int64{70, 20, 10}, []int64{70, 20, 10}},
		{1000, []int64{1, 2, 3}, []int64{167, 333, 500}},
		{-100, []int64{1, 1, 1}, []int64{-34, -33, -33}},
		{0, []int64{1, 2}, []int64{0, 0}},
		{7, []int64{0, 1, 0}, []int64{0, 7, 0}},
		{2, []int64{1, 1, 1}, []int64{1, 1, 0}},
	}
	for _, tc := range cases {
		parts, err := New(tc.total, c).AllocateInts(tc.ratios...)
		if err != nil || !equal(minors(parts), tc.want) {
			t.Fatalf("Allocate(%d, %v) = %v %v, want %v", tc.total, tc.ratios, minors(parts), err, tc.want)
		}
	}
	// Largest remainder, not "first gets the rest": 0.1 × 10 = 1 exactly is fine,
	// but 3 over (0.5, 0.3, 0.2) → 1.5, 0.9, 0.6 → floors 1,0,0, remainders .5,.9,.6.
	r := func(s string) *big.Rat { v, _ := ParseDecimal(s); return v }
	parts, _ := New(3, c).Allocate(r("0.5"), r("0.3"), r("0.2"))
	if !equal(minors(parts), []int64{1, 1, 1}) {
		t.Fatalf("largest remainder: %v", minors(parts))
	}
	parts, _ = New(10, c).Allocate(r("0.5"), r("0.3"), r("0.2"))
	if !equal(minors(parts), []int64{5, 3, 2}) {
		t.Fatal(minors(parts))
	}
	split, _ := New(1000, c).Split(3)
	if !equal(minors(split), []int64{334, 333, 333}) {
		t.Fatal(minors(split))
	}
	for _, bad := range [][]*big.Rat{nil, {big.NewRat(0, 1)}, {big.NewRat(-1, 1), big.NewRat(2, 1)}} {
		if _, err := New(10, c).Allocate(bad...); err == nil {
			t.Fatalf("ratios %v accepted", bad)
		}
	}
	if _, err := New(10, c).Split(0); err == nil {
		t.Fatal("split 0")
	}
}

func TestAllocateUnit(t *testing.T) {
	c := MustLookupDefault("NPR")
	// 100.03 in whole rupees across 1:1:1 → 3334? No: 100 rupees → 34,33,33 rupees; 0.03 to the largest ratio (first).
	parts, err := New(10003, c).AllocateUnit(100, big.NewRat(1, 1), big.NewRat(1, 1), big.NewRat(1, 1))
	if err != nil || !equal(minors(parts), []int64{3403, 3300, 3300}) {
		t.Fatalf("whole rupees: %v %v", minors(parts), err)
	}
	// 0.05 cash steps: 10.00 over 1:2 → 200 steps → 66.67/133.33 steps → 67, 133 → 3.35, 6.65.
	parts, _ = New(1000, c).AllocateUnit(5, big.NewRat(1, 1), big.NewRat(2, 1))
	if !equal(minors(parts), []int64{335, 665}) {
		t.Fatal(minors(parts))
	}
	// Leftover goes to the largest ratio.
	parts, _ = New(1002, c).AllocateUnit(5, big.NewRat(1, 1), big.NewRat(3, 1))
	if !equal(minors(parts), []int64{250, 752}) {
		t.Fatal(minors(parts))
	}
	parts, _ = New(-1002, c).AllocateUnit(5, big.NewRat(1, 1), big.NewRat(3, 1))
	if !equal(minors(parts), []int64{-250, -752}) {
		t.Fatal(minors(parts))
	}
	if _, err := New(10, c).AllocateUnit(0, big.NewRat(1, 1)); err == nil {
		t.Fatal("unit 0")
	}
}

// TestAllocateProperties checks, on random inputs, that the parts always sum
// to the total, never differ from their exact share by a unit or more, and
// are multiples of the unit (except the one part that carries the leftover).
func TestAllocateProperties(t *testing.T) {
	c := MustLookupDefault("NPR")
	rng := rand.New(rand.NewSource(7))
	for i := 0; i < 5000; i++ {
		total := rng.Int63n(2_000_000_000) - 1_000_000_000
		n := 1 + rng.Intn(8)
		ratios := make([]*big.Rat, n)
		sum := new(big.Rat)
		for j := range ratios {
			ratios[j] = big.NewRat(rng.Int63n(1000), 1+rng.Int63n(9))
			sum.Add(sum, ratios[j])
		}
		if sum.Sign() == 0 {
			ratios[0] = big.NewRat(1, 1)
			sum.SetInt64(1)
		}
		unit := []int64{1, 1, 5, 100}[rng.Intn(4)]
		parts, err := New(total, c).AllocateUnit(unit, ratios...)
		if err != nil {
			t.Fatal(err)
		}
		var got int64
		off := 0
		for j, p := range parts {
			got += p.Minor
			if p.Minor%unit != 0 {
				off++
			}
			exact := new(big.Rat).Mul(big.NewRat(total, 1), ratios[j])
			exact.Quo(exact, sum)
			delta := new(big.Rat).Sub(new(big.Rat).SetInt64(p.Minor), exact)
			delta.Abs(delta)
			if delta.Cmp(big.NewRat(2*unit, 1)) >= 0 {
				t.Fatalf("part %d of %d by %v (unit %d) = %d, exact %s", j, total, ratios, unit, p.Minor, exact.FloatString(3))
			}
		}
		if got != total {
			t.Fatalf("parts of %d sum to %d", total, got)
		}
		if off > 1 {
			t.Fatalf("%d parts are not multiples of %d", off, unit)
		}
	}
}
