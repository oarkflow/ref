package platform

import (
	"math"
	"strconv"
	"strings"

	"github.com/oarkflow/bcl"
)

// Expression-language compatibility layer.
//
// The bcl evaluator (v0.0.31) has three behaviours that silently produce wrong
// answers for the expressions people naturally write, and it exposes no hook
// on its operators. This file corrects them at the boundary, so every guard,
// condition, filter and authz rule in the platform evaluates as written:
//
//  1. `&&` and `||` are not operators to it. The parser stops at the unknown
//     token and returns the left operand, so `a && b` meant just `a` — an
//     authorization condition quietly checked only its first clause. They are
//     rewritten to `and` / `or` (outside string literals).
//  2. `==` / `!=` compare with reflect.DeepEqual, so int64(2) (an integer
//     literal, or a SQL column) differs from float64(2) (every JSON number)
//     and from int(2) (len()). Integral numbers are normalised to int64 before
//     evaluation, integral float literals such as `2.0` compile as `2`, and
//     len/length return int64.
//
// Non-integral floats are untouched, and ordering comparisons (<, >, …) were
// already numeric, so the normalisation cannot change a result that was right.

// rewriteExpression applies the source-level fixes.
func rewriteExpression(src string) string {
	if !strings.ContainsAny(src, "&|.") {
		return src
	}
	var b strings.Builder
	b.Grow(len(src) + 8)
	quote := byte(0)
	for i := 0; i < len(src); i++ {
		c := src[i]
		if quote != 0 {
			b.WriteByte(c)
			if c == '\\' && i+1 < len(src) {
				i++
				b.WriteByte(src[i])
				continue
			}
			if c == quote {
				quote = 0
			}
			continue
		}
		switch {
		case c == '"' || c == '\'' || c == '`':
			quote = c
			b.WriteByte(c)
		case c == '&' && i+1 < len(src) && src[i+1] == '&':
			b.WriteString(" and ")
			i++
		case c == '|' && i+1 < len(src) && src[i+1] == '|':
			b.WriteString(" or ")
			i++
		case c >= '0' && c <= '9' && (i == 0 || !isIdentByte(src[i-1])):
			// A numeric literal: drop a zero fraction ("2.0", "2.00") so it
			// parses as the same integer a JSON 2 or a SQL 2 normalises to.
			j := i
			for j < len(src) && src[j] >= '0' && src[j] <= '9' {
				j++
			}
			k := j
			if k < len(src) && src[k] == '.' {
				k++
				zeros := k
				for k < len(src) && src[k] == '0' {
					k++
				}
				fractionIsZero := k > zeros && (k == len(src) || !(src[k] >= '0' && src[k] <= '9') && !isIdentByte(src[k]))
				if fractionIsZero {
					b.WriteString(src[i:j])
					i = k - 1
					continue
				}
			}
			b.WriteString(src[i:j])
			i = j - 1
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

func isIdentByte(c byte) bool {
	return c == '_' || c == '.' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

// normalizeNumbers returns v with every integral number converted to int64.
// Containers are copied only when something inside them changes, so the
// common case (nothing to convert) allocates nothing.
func normalizeNumbers(v any) (any, bool) {
	switch t := v.(type) {
	case float64:
		if t == math.Trunc(t) && !math.IsInf(t, 0) && math.Abs(t) < 1<<53 {
			return int64(t), true
		}
	case float32:
		f := float64(t)
		if f == math.Trunc(f) && !math.IsInf(f, 0) && math.Abs(f) < 1<<53 {
			return int64(f), true
		}
		return f, true
	case int:
		return int64(t), true
	case int32:
		return int64(t), true
	case int16:
		return int64(t), true
	case int8:
		return int64(t), true
	case uint:
		return int64(t), true
	case uint32:
		return int64(t), true
	case uint16:
		return int64(t), true
	case uint8:
		return int64(t), true
	case uint64:
		if t <= math.MaxInt64 {
			return int64(t), true
		}
	case map[string]any:
		var out map[string]any
		for k, item := range t {
			if n, changed := normalizeNumbers(item); changed {
				if out == nil {
					out = make(map[string]any, len(t))
					for k2, v2 := range t {
						out[k2] = v2
					}
				}
				out[k] = n
			}
		}
		if out != nil {
			return out, true
		}
	case Env:
		n, changed := normalizeNumbers(map[string]any(t))
		if changed {
			return Env(n.(map[string]any)), true
		}
	case []any:
		var out []any
		for i, item := range t {
			if n, changed := normalizeNumbers(item); changed {
				if out == nil {
					out = append([]any(nil), t...)
				}
				out[i] = n
			}
		}
		if out != nil {
			return out, true
		}
	case []map[string]any:
		var out []map[string]any
		for i, item := range t {
			if n, changed := normalizeNumbers(item); changed {
				if out == nil {
					out = append([]map[string]any(nil), t...)
				}
				out[i] = n.(map[string]any)
			}
		}
		if out != nil {
			return out, true
		}
	case []int:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = int64(x)
		}
		return out, true
	case []float64:
		out := make([]any, len(t))
		for i, x := range t {
			out[i], _ = normalizeNumbers(x)
		}
		return out, true
	}
	return v, false
}

// lengthFunction replaces bcl's len/length, which return int.
func lengthFunction(args []any, _ *bcl.EvalOptions) (any, error) {
	if len(args) != 1 {
		return nil, errArgCount("len", 1)
	}
	switch t := args[0].(type) {
	case nil:
		return int64(0), nil
	case string:
		return int64(len(t)), nil // bytes, as bcl's own len does
	case []any:
		return int64(len(t)), nil
	case []map[string]any:
		return int64(len(t)), nil
	case map[string]any:
		return int64(len(t)), nil
	case []string:
		return int64(len(t)), nil
	default:
		return int64(len(Stringify(t))), nil
	}
}

type argCountError struct {
	name string
	want int
}

func (e argCountError) Error() string {
	return e.name + " requires " + strconv.Itoa(e.want) + " argument(s)"
}

func errArgCount(name string, want int) error { return argCountError{name, want} }

var exprFunctions = map[string]bcl.EvalFunction{
	"len":    lengthFunction,
	"length": lengthFunction,
}
