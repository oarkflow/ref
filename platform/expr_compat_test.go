package platform

import "testing"

func TestExpressionCompatibility(t *testing.T) {
	env := Env{
		"input": map[string]any{"qty": float64(2), "price": 2.5, "note": "a && b || c", "tags": []any{"x", "y"}},
		"row":   map[string]any{"count": int64(2)},
		"n":     2,
	}
	cases := map[string]bool{
		// && and || are real operators, not "the left operand".
		"true && false":                        false,
		"false || true":                        true,
		"input.qty == 2 && row.count == 3":     false,
		"input.qty == 3 || row.count == 2":     true,
		"(input.qty == 2) && (row.count == 2)": true,
		// Numbers compare by value whatever their Go type.
		"input.qty == 2":           true, // JSON float64 vs literal
		"row.count == input.qty":   true, // SQL int64 vs JSON float64
		"n == 2":                   true, // Go int
		"input.qty == 2.0":         true,
		"input.qty != 2":           false,
		"input.price == 2.5":       true,
		"len(input.tags) == 2":     true,
		"len(input.tags) != 0":     true,
		"length(input.note) == 11": true,
		// String literals are never rewritten.
		"input.note == 'a && b || c'":   true,
		"input.note == \"a && b || c\"": true,
		// Precedence: and binds tighter than or.
		"true || false && false": true,
	}
	for src, want := range cases {
		expr, err := CompileExpr(src)
		if err != nil {
			t.Fatalf("%s: %v", src, err)
		}
		got, err := expr.Bool(env)
		if err != nil {
			t.Fatalf("%s: %v", src, err)
		}
		if got != want {
			t.Errorf("%s = %v, want %v (rewritten: %q)", src, got, want, rewriteExpression(src))
		}
	}
}

func TestRewriteExpressionOnlyTouchesOperators(t *testing.T) {
	for src, want := range map[string]string{
		"a && b":               "a  and  b",
		"a||b":                 "a or b",
		"'x && y' == s":        "'x && y' == s",
		"\"p || q\" == s && t": "\"p || q\" == s  and  t",
		"x == 2.0":             "x == 2.0",
	} {
		if got := rewriteExpression(src); got != want {
			t.Errorf("rewrite(%q) = %q, want %q", src, got, want)
		}
	}
}
