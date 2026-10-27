package platform

import "testing"

// Conditions on inputs (visible_if, required_if, disabled_if) are read by
// people and by Studio's rule builder, so the meaning of "empty" is pinned: a
// value never entered is empty, and is NOT equal to a blank string.
func TestPipelineEmptinessSemantics(t *testing.T) {
	ev := &pipelineEvaluator{}
	env := map[string]map[string]any{
		"unset":  {"p": map[string]any{}},
		"blank":  {"p": map[string]any{"v": ""}},
		"filled": {"p": map[string]any{"v": "X1"}},
	}
	for _, tc := range []struct {
		expr                 string
		unset, blank, filled bool
	}{
		{"p.v empty", true, true, false},
		{"p.v not empty", false, false, true},
		{"p.v == ''", false, true, false},
		{"p.v != ''", true, false, true}, // true for a value never entered: not "filled in"
	} {
		for name, want := range map[string]bool{"unset": tc.unset, "blank": tc.blank, "filled": tc.filled} {
			got, err := ev.Eval(tc.expr, env[name])
			if err != nil || truthyExpr(got) != want {
				t.Errorf("%q with %s value: got %v (%v), want %v", tc.expr, name, got, err, want)
			}
		}
	}
}

func truthyExpr(v any) bool { b, _ := v.(bool); return b }
