package platform

import (
	"fmt"
	"math/big"
	"slices"
	"strconv"
	"strings"

	"github.com/oarkflow/ref/money"
)

// Currencies and money.
//
// Every generation has a currency registry: the built-in ISO 4217 set (see
// ref/money) plus the document's own `currency` blocks. A block can add a
// currency the built-ins lack — loyalty points, a token, an old currency a
// ledger still holds — or re-symbol a built-in one, but it can never change
// a built-in currency's minor units, since every amount stored in it would
// silently change value.
//
//	currency "PTS" {
//	  minor_units 0
//	  symbol "pts"
//	  name "Loyalty points"
//	}
//
// An entity decimal column takes its scale from its currency:
//
//	column "price" { currency "NPR" }   // decimal, 2 places
//	column "fare"  { currency "JPY" }   // decimal, 0 places
//
// The money.* actions and the money_* expression functions do exact
// arithmetic on decimal strings through integer minor units.

// CurrencySpec is one `currency` block.
type CurrencySpec struct {
	Code string `bcl:",id"`
	// MinorUnits is the number of decimal places. Required for a new
	// currency; a built-in one keeps its own.
	MinorUnits *int   `bcl:"minor_units"`
	Symbol     string `bcl:"symbol"`
	Name       string `bcl:"name"`
	// Grouping is thousands (default), indian (12,34,567) or none.
	Grouping string `bcl:"grouping"`
}

// compileCurrencies builds the generation's currency registry.
func compileCurrencies(doc Document) (*money.Registry, error) {
	reg := money.Default().Clone()
	seen := map[string]bool{}
	for _, spec := range doc.Currencies {
		code := strings.ToUpper(strings.TrimSpace(spec.Code))
		if seen[code] {
			return nil, fmt.Errorf("ref/platform: currency %q declared twice", spec.Code)
		}
		seen[code] = true
		c, known := money.NewRegistry().Lookup(code)
		if !known {
			if spec.MinorUnits == nil {
				return nil, fmt.Errorf("ref/platform: currency %q is not built in, so it needs minor_units", spec.Code)
			}
			c = money.Currency{Code: code}
		}
		if spec.MinorUnits != nil {
			c.Minor = *spec.MinorUnits
		}
		if spec.Symbol != "" {
			c.Symbol = spec.Symbol
		}
		if spec.Name != "" {
			c.Name = spec.Name
		}
		if spec.Grouping != "" {
			c.Grouping = money.Grouping(strings.ToLower(spec.Grouping))
		}
		if err := reg.Register(c); err != nil {
			return nil, fmt.Errorf("ref/platform: %w", err)
		}
	}
	return reg, nil
}

// publishCurrencies makes the document's own currencies visible to the
// money_* expression functions, which evaluate outside any one generation and
// so resolve codes through the process-wide registry. Only codes that are not
// built in are published: re-symbolling NPR in one document must not change
// how another generation in the same process formats it, so a built-in
// currency's document overrides apply to that generation's money.* actions
// only. A later document that redefines a custom code replaces it.
func publishCurrencies(doc Document, reg *money.Registry) {
	builtin := money.NewRegistry()
	for _, spec := range doc.Currencies {
		if _, isBuiltin := builtin.Lookup(spec.Code); isBuiltin {
			continue
		}
		if c, ok := reg.Lookup(spec.Code); ok {
			_ = money.Register(c)
		}
	}
}

// resolveEntityCurrencies sets the scale of every currency column from the
// generation's registry, before the entities compile.
func resolveEntityCurrencies(doc Document, reg *money.Registry) (Document, error) {
	if len(doc.Entities) == 0 {
		return doc, nil
	}
	entities := slices.Clone(doc.Entities)
	for i := range entities {
		if !slices.ContainsFunc(entities[i].Columns, func(c EntityColumn) bool { return c.Currency != "" }) {
			continue
		}
		cols := slices.Clone(entities[i].Columns)
		for j := range cols {
			col := &cols[j]
			if col.Currency == "" {
				continue
			}
			c, ok := reg.Lookup(col.Currency)
			if !ok {
				return doc, fmt.Errorf("ref/platform: entity %q: column %q: unknown currency %q", entities[i].Name, col.Name, col.Currency)
			}
			if col.Scale != 0 && col.Scale != c.Minor {
				return doc, fmt.Errorf("ref/platform: entity %q: column %q: scale %d conflicts with %s, which has %d decimal places",
					entities[i].Name, col.Name, col.Scale, c.Code, c.Minor)
			}
			col.Currency, col.Scale, col.currencyResolved = c.Code, c.Minor, true
		}
		entities[i].Columns = cols
	}
	doc.Entities = entities
	return doc, nil
}

// resolveColumnCurrency is compileEntity's view of a currency column. A
// column the document pre-pass already resolved keeps its scale; otherwise
// (an entity compiled on its own) the built-in registry decides.
func resolveColumnCurrency(col *EntityColumn) error {
	if col.Kind == "" {
		col.Kind = "decimal"
	}
	if col.Kind != "decimal" {
		return fmt.Errorf("currency %q needs kind decimal, not %s", col.Currency, col.Kind)
	}
	if col.currencyResolved {
		return nil
	}
	c, ok := money.Lookup(col.Currency)
	if !ok {
		return fmt.Errorf("unknown currency %q", col.Currency)
	}
	if col.Scale != 0 && col.Scale != c.Minor {
		return fmt.Errorf("scale %d conflicts with %s, which has %d decimal places", col.Scale, c.Code, c.Minor)
	}
	col.Currency, col.Scale, col.currencyResolved = c.Code, c.Minor, true
	return nil
}

// ---------------------------------------------------------------------------
// Value coercion shared by the money actions and expression functions
// ---------------------------------------------------------------------------

// moneyOf reads an amount — a decimal string, a JSON number or an integer —
// exactly. A float is taken by its shortest decimal form, so 0.1 is one
// tenth. More decimals than the currency allows is an error.
func moneyOf(value any, c money.Currency) (money.Money, error) {
	switch v := value.(type) {
	case nil:
		return money.Zero(c), nil
	case string:
		return money.Parse(v, c)
	case float64:
		return money.FromFloat(v, c)
	case float32:
		return money.FromFloat(float64(v), c)
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32:
		return money.Parse(fmt.Sprint(v), c)
	}
	return money.Money{}, fmt.Errorf("%v is not an amount", value)
}

// decimalOf reads a rate or a ratio exactly.
func decimalOf(value any) (*big.Rat, error) {
	switch v := value.(type) {
	case string:
		return money.ParseDecimal(v)
	case float64:
		return money.ParseDecimal(strconv.FormatFloat(v, 'f', -1, 64))
	case float32:
		return money.ParseDecimal(strconv.FormatFloat(float64(v), 'f', -1, 32))
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32:
		return money.ParseDecimal(fmt.Sprint(v))
	}
	return nil, fmt.Errorf("%v is not a decimal number", value)
}

func ratiosOf(value any) ([]*big.Rat, error) {
	list, ok := value.([]any)
	if !ok {
		if strs, isStrs := value.([]string); isStrs {
			for _, s := range strs {
				list = append(list, s)
			}
		} else {
			return nil, fmt.Errorf("ratios must be a list of numbers")
		}
	}
	out := make([]*big.Rat, len(list))
	for i, item := range list {
		r, err := decimalOf(item)
		if err != nil {
			return nil, fmt.Errorf("ratio %d: %w", i+1, err)
		}
		out[i] = r
	}
	return out, nil
}

// unitOf reads an allocation step given in major units ("0.05", 1) as minor
// units. It must be a whole number of minor units.
func unitOf(value any, c money.Currency) (int64, error) {
	if value == nil {
		return 1, nil
	}
	m, err := moneyOf(value, c)
	if err != nil {
		return 0, fmt.Errorf("unit: %w", err)
	}
	if m.Minor < 1 {
		return 0, fmt.Errorf("unit must be positive")
	}
	return m.Minor, nil
}

func amountStrings(parts []money.Money) []any {
	out := make([]any, len(parts))
	for i, p := range parts {
		out[i] = p.String()
	}
	return out
}

// ---------------------------------------------------------------------------
// Actions
// ---------------------------------------------------------------------------

func registerMoneyActions(r *Registry) {
	mustAction(r, "money.allocate", ActionFactoryFunc(buildMoneyAllocate), ActionInfo{
		Family:   "compute",
		Summary:  "Split an amount by ratios or into N parts; the parts always sum to the total (largest remainder)",
		Provides: "A list of decimal amount strings",
		Kind:     "pure",
		Config: []ConfigField{
			{Name: "currency", Type: "string", Required: true, Summary: "ISO 4217 code or a document currency"},
			{Name: "amount", Type: "expression", Required: true, Summary: "The total, e.g. input.total"},
			{Name: "ratios", Type: "expression", Summary: "A list of weights, e.g. [50, 30, 20] or input.shares"},
			{Name: "parts", Type: "int", Summary: "Split into this many equal parts instead of ratios"},
			{Name: "unit", Type: "string", Summary: "Allocate in multiples of this amount (0.05, 1); the remainder goes to the largest share"},
		},
	})
	mustAction(r, "money.convert", ActionFactoryFunc(buildMoneyConvert), ActionInfo{
		Family:   "compute",
		Summary:  "Convert an amount at an explicit rate with an explicit rounding mode",
		Provides: "The converted decimal amount string",
		Kind:     "pure",
		Config: []ConfigField{
			{Name: "from", Type: "string", Required: true},
			{Name: "to", Type: "string", Required: true},
			{Name: "amount", Type: "expression", Required: true},
			{Name: "rate", Type: "expression", Required: true, Summary: "Units of `to` per one unit of `from`, e.g. \"133.25\" or input.rate"},
			{Name: "rounding", Type: "string", Default: "half_even", Summary: "half_even, half_up, half_down, down, up, floor or ceiling"},
		},
	})
}

// currencyFor resolves a code against the generation being built.
func currencyFor(build BuildContext, spec NodeSpec, key string) (money.Currency, error) {
	code, err := requiredString(spec.Config, key)
	if err != nil {
		return money.Currency{}, fmt.Errorf("node %q: %w", spec.Name, err)
	}
	reg := money.Default()
	if build.Platform != nil && build.Platform.currencies != nil {
		reg = build.Platform.currencies
	}
	c, ok := reg.Lookup(code)
	if !ok {
		return money.Currency{}, fmt.Errorf("node %q: unknown currency %q", spec.Name, code)
	}
	return c, nil
}

// configValueExpr compiles a config value that may be an expression string or
// a literal (a number, a list).
func configValueExpr(config map[string]any, key string) (func(Env) (any, error), error) {
	raw, ok := config[key]
	if !ok {
		return nil, nil
	}
	if text, isText := raw.(string); isText {
		expr, err := CompileExpr(text)
		if err != nil {
			return nil, fmt.Errorf("config.%s: %w", key, err)
		}
		return expr.Eval, nil
	}
	return func(Env) (any, error) { return raw, nil }, nil
}

func buildMoneyAllocate(build BuildContext, spec NodeSpec) (Action, error) {
	if err := exactlyOneOutput(spec); err != nil {
		return nil, err
	}
	c, err := currencyFor(build, spec, "currency")
	if err != nil {
		return nil, err
	}
	amount, err := configValueExpr(spec.Config, "amount")
	if err != nil || amount == nil {
		return nil, fmt.Errorf("node %q: money.allocate needs config.amount %v", spec.Name, err)
	}
	ratios, err := configValueExpr(spec.Config, "ratios")
	if err != nil {
		return nil, fmt.Errorf("node %q: %w", spec.Name, err)
	}
	parts, err := configInt(spec.Config, "parts", 0)
	if err != nil {
		return nil, fmt.Errorf("node %q: %w", spec.Name, err)
	}
	if (ratios == nil) == (parts <= 0) {
		return nil, fmt.Errorf("node %q: money.allocate needs exactly one of config.ratios and config.parts", spec.Name)
	}
	unit, err := unitOf(spec.Config["unit"], c)
	if err != nil {
		return nil, fmt.Errorf("node %q: %w", spec.Name, err)
	}
	provides := spec.Provides[0]
	return ActionFunc(func(ctx *ActionContext) (ActionResult, error) {
		env := actionEnv(ctx)
		raw, err := amount(env)
		if err != nil {
			return ActionResult{}, err
		}
		total, err := moneyOf(raw, c)
		if err != nil {
			return ActionResult{}, invalidInput("amount: %v", err)
		}
		var weights []*big.Rat
		if ratios != nil {
			value, err := ratios(env)
			if err != nil {
				return ActionResult{}, err
			}
			if weights, err = ratiosOf(value); err != nil {
				return ActionResult{}, invalidInput("%v", err)
			}
		} else {
			for range parts {
				weights = append(weights, big.NewRat(1, 1))
			}
		}
		split, err := total.AllocateUnit(unit, weights...)
		if err != nil {
			return ActionResult{}, invalidInput("%v", err)
		}
		return ActionResult{Outputs: map[string]any{provides: amountStrings(split)}}, nil
	}), nil
}

func buildMoneyConvert(build BuildContext, spec NodeSpec) (Action, error) {
	if err := exactlyOneOutput(spec); err != nil {
		return nil, err
	}
	from, err := currencyFor(build, spec, "from")
	if err != nil {
		return nil, err
	}
	to, err := currencyFor(build, spec, "to")
	if err != nil {
		return nil, err
	}
	amount, err := configValueExpr(spec.Config, "amount")
	if err != nil || amount == nil {
		return nil, fmt.Errorf("node %q: money.convert needs config.amount %v", spec.Name, err)
	}
	rate, err := configValueExpr(spec.Config, "rate")
	if err != nil || rate == nil {
		return nil, fmt.Errorf("node %q: money.convert needs config.rate %v", spec.Name, err)
	}
	mode, err := money.ParseRoundingMode(configString(spec.Config, "rounding", ""))
	if err != nil {
		return nil, fmt.Errorf("node %q: %w", spec.Name, err)
	}
	provides := spec.Provides[0]
	return ActionFunc(func(ctx *ActionContext) (ActionResult, error) {
		env := actionEnv(ctx)
		rawAmount, err := amount(env)
		if err != nil {
			return ActionResult{}, err
		}
		value, err := moneyOf(rawAmount, from)
		if err != nil {
			return ActionResult{}, invalidInput("amount: %v", err)
		}
		rawRate, err := rate(env)
		if err != nil {
			return ActionResult{}, err
		}
		r, err := decimalOf(rawRate)
		if err != nil {
			return ActionResult{}, invalidInput("rate: %v", err)
		}
		converted, err := value.Convert(to, r, mode)
		if err != nil {
			return ActionResult{}, invalidInput("%v", err)
		}
		return ActionResult{Outputs: map[string]any{provides: converted.String()}}, nil
	}), nil
}
