package platform

import "strings"

// Expression-language compatibility layer.
//
// bcl (v0.0.32) compares numbers by value and parses string literals
// correctly, but still does not treat `&&` and `||` as operators: the parser
// stops at the unknown token and returns the left operand, so `a && b` would
// silently mean `a` — an authorization condition checking only its first
// clause. They are rewritten to `and` / `or` outside string literals, so
// every guard, condition, filter and authz rule evaluates as written.

// rewriteExpression applies the source-level fixes.
func rewriteExpression(src string) string {
	if !strings.Contains(src, "&&") && !strings.Contains(src, "||") {
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
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}
