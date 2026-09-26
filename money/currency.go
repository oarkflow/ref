// Package money is exact currency arithmetic: an ISO 4217 currency registry,
// amounts held as integer minor units, and the operations a ledger needs —
// add, subtract, parse, format, convert at an explicit rate with an explicit
// rounding mode, and allocate an amount across parts so that the parts always
// sum to the whole.
//
// Nothing here uses floating point for arithmetic. An amount is an int64 count
// of minor units (paisa, cents, fils); conversion and allocation compute
// through math/big and round exactly once, at the end, by a named rule.
package money

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Grouping selects how the integer part of a formatted amount is grouped.
type Grouping string

const (
	// GroupThousands groups by three digits: 1,234,567.00.
	GroupThousands Grouping = "thousands"
	// GroupIndian groups the last three digits, then by two: 12,34,567.00 —
	// the lakh/crore convention used for NPR and INR.
	GroupIndian Grouping = "indian"
	// GroupNone writes the digits ungrouped.
	GroupNone Grouping = "none"
)

// Currency describes one currency: its ISO 4217 code, how many decimal places
// its minor unit has, and how it is written.
type Currency struct {
	Code string `json:"code"`
	// Minor is the number of decimal places of the minor unit: 2 for USD
	// (cents), 0 for JPY, 3 for KWD (fils).
	Minor  int    `json:"minor_units"`
	Symbol string `json:"symbol,omitempty"`
	Name   string `json:"name,omitempty"`
	// Grouping is the default digit grouping when formatting; empty means
	// thousands.
	Grouping Grouping `json:"grouping,omitempty"`
}

// Validate reports whether a currency definition is usable.
func (c Currency) Validate() error {
	if !validCode(c.Code) {
		return fmt.Errorf("money: currency code %q must be three to eight upper-case letters or digits", c.Code)
	}
	if c.Minor < 0 || c.Minor > 8 {
		return fmt.Errorf("money: currency %s: minor units must be 0-8, got %d", c.Code, c.Minor)
	}
	switch c.Grouping {
	case "", GroupThousands, GroupIndian, GroupNone:
	default:
		return fmt.Errorf("money: currency %s: grouping must be thousands, indian or none", c.Code)
	}
	return nil
}

func validCode(code string) bool {
	if len(code) < 3 || len(code) > 8 {
		return false
	}
	for _, r := range code {
		if (r < 'A' || r > 'Z') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}

// builtin is the ISO 4217 subset every registry starts with: the currencies
// most applications meet, including every zero- and three-decimal one a
// ledger is likely to get wrong by assuming two.
var builtin = []Currency{
	{Code: "NPR", Minor: 2, Symbol: "Rs", Name: "Nepalese rupee", Grouping: GroupIndian},
	{Code: "INR", Minor: 2, Symbol: "₹", Name: "Indian rupee", Grouping: GroupIndian},
	{Code: "USD", Minor: 2, Symbol: "$", Name: "US dollar"},
	{Code: "EUR", Minor: 2, Symbol: "€", Name: "Euro"},
	{Code: "GBP", Minor: 2, Symbol: "£", Name: "Pound sterling"},
	{Code: "JPY", Minor: 0, Symbol: "¥", Name: "Japanese yen"},
	{Code: "CNY", Minor: 2, Symbol: "¥", Name: "Renminbi"},
	{Code: "KWD", Minor: 3, Symbol: "KD", Name: "Kuwaiti dinar"},
	{Code: "BHD", Minor: 3, Symbol: "BD", Name: "Bahraini dinar"},
	{Code: "OMR", Minor: 3, Symbol: "RO", Name: "Omani rial"},
	{Code: "JOD", Minor: 3, Symbol: "JD", Name: "Jordanian dinar"},
	{Code: "TND", Minor: 3, Symbol: "DT", Name: "Tunisian dinar"},
	{Code: "LYD", Minor: 3, Symbol: "LD", Name: "Libyan dinar"},
	{Code: "IQD", Minor: 3, Symbol: "ID", Name: "Iraqi dinar"},
	{Code: "KRW", Minor: 0, Symbol: "₩", Name: "South Korean won"},
	{Code: "VND", Minor: 0, Symbol: "₫", Name: "Vietnamese dong"},
	{Code: "CLP", Minor: 0, Symbol: "$", Name: "Chilean peso"},
	{Code: "ISK", Minor: 0, Symbol: "kr", Name: "Icelandic króna"},
	{Code: "UGX", Minor: 0, Symbol: "USh", Name: "Ugandan shilling"},
	{Code: "XOF", Minor: 0, Symbol: "CFA", Name: "West African CFA franc"},
	{Code: "XAF", Minor: 0, Symbol: "FCFA", Name: "Central African CFA franc"},
	{Code: "PYG", Minor: 0, Symbol: "₲", Name: "Paraguayan guaraní"},
	{Code: "AUD", Minor: 2, Symbol: "A$", Name: "Australian dollar"},
	{Code: "CAD", Minor: 2, Symbol: "C$", Name: "Canadian dollar"},
	{Code: "CHF", Minor: 2, Symbol: "CHF", Name: "Swiss franc"},
	{Code: "NZD", Minor: 2, Symbol: "NZ$", Name: "New Zealand dollar"},
	{Code: "SGD", Minor: 2, Symbol: "S$", Name: "Singapore dollar"},
	{Code: "HKD", Minor: 2, Symbol: "HK$", Name: "Hong Kong dollar"},
	{Code: "SEK", Minor: 2, Symbol: "kr", Name: "Swedish krona"},
	{Code: "NOK", Minor: 2, Symbol: "kr", Name: "Norwegian krone"},
	{Code: "DKK", Minor: 2, Symbol: "kr", Name: "Danish krone"},
	{Code: "PLN", Minor: 2, Symbol: "zł", Name: "Polish złoty"},
	{Code: "CZK", Minor: 2, Symbol: "Kč", Name: "Czech koruna"},
	{Code: "HUF", Minor: 2, Symbol: "Ft", Name: "Hungarian forint"},
	{Code: "RUB", Minor: 2, Symbol: "₽", Name: "Russian ruble"},
	{Code: "TRY", Minor: 2, Symbol: "₺", Name: "Turkish lira"},
	{Code: "ZAR", Minor: 2, Symbol: "R", Name: "South African rand"},
	{Code: "BRL", Minor: 2, Symbol: "R$", Name: "Brazilian real"},
	{Code: "MXN", Minor: 2, Symbol: "$", Name: "Mexican peso"},
	{Code: "ARS", Minor: 2, Symbol: "$", Name: "Argentine peso"},
	{Code: "AED", Minor: 2, Symbol: "AED", Name: "UAE dirham"},
	{Code: "SAR", Minor: 2, Symbol: "SAR", Name: "Saudi riyal"},
	{Code: "QAR", Minor: 2, Symbol: "QAR", Name: "Qatari riyal"},
	{Code: "ILS", Minor: 2, Symbol: "₪", Name: "Israeli new shekel"},
	{Code: "EGP", Minor: 2, Symbol: "E£", Name: "Egyptian pound"},
	{Code: "NGN", Minor: 2, Symbol: "₦", Name: "Nigerian naira"},
	{Code: "KES", Minor: 2, Symbol: "KSh", Name: "Kenyan shilling"},
	{Code: "BDT", Minor: 2, Symbol: "৳", Name: "Bangladeshi taka", Grouping: GroupIndian},
	{Code: "PKR", Minor: 2, Symbol: "Rs", Name: "Pakistani rupee", Grouping: GroupIndian},
	{Code: "LKR", Minor: 2, Symbol: "Rs", Name: "Sri Lankan rupee"},
	{Code: "BTN", Minor: 2, Symbol: "Nu.", Name: "Bhutanese ngultrum", Grouping: GroupIndian},
	{Code: "IDR", Minor: 2, Symbol: "Rp", Name: "Indonesian rupiah"},
	{Code: "MYR", Minor: 2, Symbol: "RM", Name: "Malaysian ringgit"},
	{Code: "THB", Minor: 2, Symbol: "฿", Name: "Thai baht"},
	{Code: "PHP", Minor: 2, Symbol: "₱", Name: "Philippine peso"},
	{Code: "TWD", Minor: 2, Symbol: "NT$", Name: "New Taiwan dollar"},
}

// Registry is a set of currencies, safe for concurrent use.
type Registry struct {
	mu    sync.RWMutex
	codes map[string]Currency
}

// NewRegistry returns a registry holding the built-in ISO 4217 currencies.
func NewRegistry() *Registry {
	r := &Registry{codes: make(map[string]Currency, len(builtin))}
	for _, c := range builtin {
		r.codes[c.Code] = c
	}
	return r
}

// Clone returns an independent copy, for extending a registry without
// changing the one it came from.
func (r *Registry) Clone() *Registry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := &Registry{codes: make(map[string]Currency, len(r.codes))}
	for k, v := range r.codes {
		out.codes[k] = v
	}
	return out
}

// Lookup returns the currency for a code, case-insensitively.
func (r *Registry) Lookup(code string) (Currency, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.codes[strings.ToUpper(strings.TrimSpace(code))]
	return c, ok
}

// MustLookup is Lookup for a code that is known to exist.
func (r *Registry) MustLookup(code string) Currency {
	c, ok := r.Lookup(code)
	if !ok {
		panic("money: unknown currency " + code)
	}
	return c
}

// Register adds a currency, or updates one. Redefining a built-in ISO
// currency with a different number of minor units is refused: every amount
// already stored in it would silently change value.
func (r *Registry) Register(c Currency) error {
	c.Code = strings.ToUpper(strings.TrimSpace(c.Code))
	if err := c.Validate(); err != nil {
		return err
	}
	for _, b := range builtin {
		if b.Code == c.Code && b.Minor != c.Minor {
			return fmt.Errorf("money: currency %s is ISO 4217 with %d minor units; it cannot be redefined with %d", c.Code, b.Minor, c.Minor)
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.codes[c.Code] = c
	return nil
}

// Codes returns every registered code, sorted.
func (r *Registry) Codes() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.codes))
	for k := range r.codes {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

var defaultRegistry = NewRegistry()

// Default is the process-wide registry: the built-ins plus whatever the host
// registered with Register.
func Default() *Registry { return defaultRegistry }

// Lookup finds a currency in the default registry.
func Lookup(code string) (Currency, bool) { return defaultRegistry.Lookup(code) }

// Register adds a currency to the default registry.
func Register(c Currency) error { return defaultRegistry.Register(c) }
