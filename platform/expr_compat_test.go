package platform

import (
	"strings"
	"testing"

	"github.com/oarkflow/bcl"
)

func TestExpressionCompatibility(t *testing.T) {
	env := Env{
		"input": map[string]any{"qty": float64(2), "price": 2.5, "note": "a && b || c", "tags": []any{"x", "y"}},
		"row":   map[string]any{"count": int64(2)},
		"n":     2,
	}
	cases := map[string]bool{
		// && and || are real operators, not "the left operand" (native in
		// bcl since v0.0.35; before, ref rewrote them to and / or).
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
		// Operators inside string literals are just text.
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
			t.Errorf("%s = %v, want %v", src, got, want)
		}
	}
}

// Malformed expressions fail at compile time, so validation catches them
// (bcl rejects them in CompileExpression since v0.0.36); expressions that
// are only wrong for some data still compile.
func TestMalformedExpressionsFailToCompile(t *testing.T) {
	for _, src := range []string{"(a", "(x + 1", "a ==", "a +", "x >", "x y", `"USD" "NPR"`, `"USD" to "NPR"`,
		"(x > 0) )", "x in [1, 2", "false && (a", "true || (a", "match x { 1 => ("} {
		if _, err := CompileExpr(src); err == nil {
			t.Errorf("%s compiled", src)
		}
	}
	for _, src := range []string{"input.amount / 0", "a.b.c == 1", "len(missing) > 0", "x > 1 && y < 2",
		"money_add('NPR', input.a, input.b)", "fiscal_year(input.date) == '2081/82'"} {
		if _, err := CompileExpr(src); err != nil {
			t.Errorf("%s: %v", src, err)
		}
	}
}

// A misspelled function fails at compile time, and at evaluation too (strict
// functions); names inside strings and operators before "(" are not calls.
func TestUnknownFunctionsFailToCompile(t *testing.T) {
	for _, src := range []string{"uper(input.name) == 'X'", "lenght(input.tags) > 0", "input.ok && fiscal_yr(input.date) == '2081/82'",
		"false && nope(1)"} {
		if _, err := CompileExpr(src); err == nil || !strings.Contains(err.Error(), "unknown function") {
			t.Errorf("%s: %v", src, err)
		}
	}
	for _, src := range []string{"upper(input.name) == 'X'", "len(input.tags) > 0", "bs_year(input.date) > 2080",
		"input.note == 'call(me)'", "a and (b or c)", "x in [1, 2] or not (a)", "lower( input.name ) == 'x'"} {
		if _, err := CompileExpr(src); err != nil {
			t.Errorf("%s: %v", src, err)
		}
	}
	if got := calledNames(`upper(a) == "f(x)" && money_add('NPR', b, c) or (d)`); strings.Join(got, ",") != "upper,money_add" {
		t.Errorf("calledNames = %v", got)
	}
	// Evaluation is strict as well, for expressions compiled some other way.
	prog, err := bcl.CompileExpression("uper('a')")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (&Expression{raw: "uper('a')", prog: prog}).Eval(Env{}); err == nil || !strings.Contains(err.Error(), "unknown function") {
		t.Errorf("strict evaluation: %v", err)
	}
}

// bcl (v0.0.36) cannot check function names when compiling (it takes no
// options there), which is why checkFunctions probes them. If it starts to,
// this fails and checkFunctions can go.
func TestBCLCompileStillAcceptsUnknownFunctions(t *testing.T) {
	if _, err := bcl.CompileExpression("uper('a')"); err != nil {
		t.Fatalf("bcl.CompileExpression now rejects unknown functions (%v): checkFunctions is redundant", err)
	}
}

// TestOrderingWithMissingValues pins bcl v0.0.33: an ordering comparison
// with a missing or non-comparable operand is false, so a guard such as
// `amount > limit` never passes because amount is absent.
func TestOrderingWithMissingValues(t *testing.T) {
	for src, want := range map[string]bool{
		"x > 1000": false, "x >= 1000": false, "x < 1000": false, "x <= 1000": false,
		"nil > 1000": false, "'abc' > 1000": false, "2 > 1": true, "'b' > 'a'": true,
	} {
		expr, err := CompileExpr(src)
		if err != nil {
			t.Fatal(err)
		}
		got, err := expr.Bool(Env{})
		if err != nil || got != want {
			t.Errorf("%s = %v (%v), want %v", src, got, err, want)
		}
	}
}
