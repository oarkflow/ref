package pages

import (
	"regexp"
	"strings"
)

// cssAtRules are "@name" sequences in <style> blocks that are not template
// directives.
var cssAtRules = map[string]bool{
	"media": true, "supports": true, "container": true, "keyframes": true, "font-face": true,
	"page": true, "layer": true, "charset": true, "namespace": true, "property": true,
	"counter-style": true, "font-feature-values": true, "scope": true, "starting-style": true,
}

// exprKeywords are words in an expression that are not variables.
var exprKeywords = map[string]bool{
	"true": true, "false": true, "null": true, "nil": true, "undefined": true,
	"in": true, "and": true, "or": true, "not": true, "typeof": true, "new": true, "of": true, "is": true,
}

// builtinNames are names the engine provides in every template.
var builtinNames = map[string]bool{"loop": true}

var forHeader = regexp.MustCompile(`(?s)^\s*([A-Za-z_$][\w$]*)\s*(?:,\s*([A-Za-z_$][\w$]*))?\s+in\s+(.*)$`)

// scanner walks a template's text for directives and ${...} expressions.
type scanner struct {
	src       string
	extends   string
	seen      map[string]*[]string
	dedupe    map[string]bool
	locals    map[string]bool
	pathOrder []string
}

func (s *scanner) add(kind, v string) {
	if v == "" {
		return
	}
	if s.seen == nil {
		s.seen = map[string]*[]string{}
	}
	if s.dedupe == nil {
		s.dedupe = map[string]bool{}
	}
	if s.dedupe[kind+"\x00"+v] {
		return
	}
	s.dedupe[kind+"\x00"+v] = true
	l := s.seen[kind]
	if l == nil {
		l = new([]string)
		s.seen[kind] = l
	}
	*l = append(*l, v)
}

func (s *scanner) list(kind string) []string {
	if l := s.seen[kind]; l != nil {
		return append([]string(nil), *l...)
	}
	return nil
}

func (s *scanner) bind(name string) {
	if s.locals == nil {
		s.locals = map[string]bool{}
	}
	if name != "" {
		s.locals[name] = true
	}
}

func (s *scanner) run() {
	src := s.src
	for i := 0; i < len(src); {
		c := src[i]
		switch {
		case c == '$' && i+1 < len(src) && src[i+1] == '{':
			end := matchClose(src, i+1)
			if end < 0 {
				i += 2
				continue
			}
			s.expr(src[i+2 : end])
			i = end + 1
		case c == '@' && directiveStart(src, i):
			i = s.directive(i)
		default:
			i++
		}
	}
}

// directiveStart reports whether the "@" at i begins a directive rather than
// sitting inside an email address or a CSS at-rule.
func directiveStart(src string, i int) bool {
	if i+1 >= len(src) || !isLetter(src[i+1]) {
		return false
	}
	if i > 0 {
		p := src[i-1]
		if isLetter(p) || isDigit(p) || p == '.' || p == '_' || p == '-' || p == '@' || p == '/' {
			return false
		}
	}
	j := i + 1
	for j < len(src) && (isLetter(src[j]) || isDigit(src[j]) || src[j] == '-') {
		j++
	}
	return !cssAtRules[src[i+1:j]]
}

// directive handles the "@name(args)" at i and returns where scanning resumes.
func (s *scanner) directive(i int) int {
	src := s.src
	j := i + 1
	for j < len(src) && (isLetter(src[j]) || isDigit(src[j]) || src[j] == '_') {
		j++
	}
	name := src[i+1 : j]
	k := j
	for k < len(src) && (src[k] == ' ' || src[k] == '\t') {
		k++
	}
	if name == "raw" {
		// Literal block: nothing inside it is parsed.
		for k < len(src) && isSpace(src[k]) {
			k++
		}
		if k < len(src) && src[k] == '{' {
			if end := matchClose(src, k); end >= 0 {
				return end + 1
			}
		}
		return j
	}
	if k >= len(src) || src[k] != '(' {
		return j
	}
	end := matchClose(src, k)
	if end < 0 {
		return j
	}
	s.args(name, src[k+1:end])
	return end + 1
}

func (s *scanner) args(name, args string) {
	switch name {
	case "extends":
		if v, ok := firstString(args); ok && s.extends == "" {
			s.extends = v
		}
	case "include", "import":
		if v, ok := firstString(args); ok {
			s.add("include", v)
		}
	case "block", "define", "slot", "fill":
		if v, ok := firstString(args); ok {
			s.add(name, v)
		}
	case "component":
		parts := splitTop(args, ',')
		if len(parts) > 0 {
			if v, ok := firstString(parts[0]); ok {
				s.add("component", v)
			}
		}
		for _, p := range parts[min(1, len(parts)):] {
			decl, def, hasDef := strings.Cut(p, "=")
			f := strings.Fields(strings.TrimPrefix(strings.TrimSpace(decl), "?"))
			if len(f) > 0 {
				s.bind(f[0])
			}
			if hasDef {
				s.expr(def)
			}
		}
	case "render":
		parts := splitTop(args, ',')
		if len(parts) > 0 {
			if v, ok := firstString(parts[0]); ok {
				s.add("render", v)
			}
		}
		for _, p := range parts[min(1, len(parts)):] {
			s.expr(p)
		}
	case "for":
		s.forHeader(args)
	case "let", "set", "signal", "local", "computed", "computedClient", "state", "const":
		for _, p := range splitTop(args, ',') {
			if lhs, rhs, ok := splitAssign(p); ok {
				s.bind(lhs)
				s.expr(rhs)
			} else {
				s.expr(p)
			}
		}
	default:
		// @if, @elseif, @switch, @match, @case, @bind, @click, @reactive …
		s.expr(args)
	}
}

func (s *scanner) forHeader(args string) {
	main, rest, hasRest := cutTop(args, ';')
	m := forHeader.FindStringSubmatch(main)
	if m == nil {
		s.expr(args)
		return
	}
	s.bind(m[1])
	s.bind(m[2])
	s.expr(m[3])
	if hasRest {
		s.expr(strings.TrimPrefix(strings.TrimSpace(rest), "key"))
	}
}

// splitAssign splits "name = expr" (a lone "=", not "==", "=>" or "!=").
func splitAssign(p string) (lhs, rhs string, ok bool) {
	i := strings.IndexByte(p, '=')
	if i <= 0 {
		return "", "", false
	}
	if i+1 < len(p) && (p[i+1] == '=' || p[i+1] == '>') {
		return "", "", false
	}
	lhs = strings.TrimSpace(p[:i])
	if lhs == "" || !isIdentWord(lhs) {
		return "", "", false
	}
	return lhs, p[i+1:], true
}

func isIdentWord(w string) bool {
	for i := 0; i < len(w); i++ {
		if !(isIdentPart(w[i])) || (i == 0 && !isIdentStart(w[i])) {
			return false
		}
	}
	return w != ""
}

// expr records the variable paths an expression reads and the filters it uses.
func (s *scanner) expr(e string) {
	n := len(e)
	afterPipe := false
	var prev byte
	for i := 0; i < n; {
		c := e[i]
		switch {
		case isSpace(c):
			i++
		case c == '"' || c == '\'' || c == '`':
			i = skipString(e, i)
			prev, afterPipe = c, false
		case isDigit(c):
			for i < n && (isDigit(e[i]) || e[i] == '.' || isLetter(e[i])) {
				i++
			}
			prev, afterPipe = '0', false
		case c == '|':
			if i+1 < n && e[i+1] == '|' {
				i += 2
				afterPipe = false
			} else {
				i++
				afterPipe = true
			}
			prev = '|'
		case isIdentStart(c):
			j := i
			for j < n && isIdentPart(e[j]) {
				j++
			}
			word := e[i:j]
			path := word
			for {
				k := j
				if k+1 < n && e[k] == '?' && e[k+1] == '.' {
					k++
				}
				if k+1 < n && e[k] == '.' && isIdentStart(e[k+1]) {
					m := k + 1
					for m < n && isIdentPart(e[m]) {
						m++
					}
					path += "." + e[k+1:m]
					j = m
					continue
				}
				break
			}
			k := j
			for k < n && isSpace(e[k]) {
				k++
			}
			isCall := k < n && e[k] == '('
			switch {
			case afterPipe:
				s.add("filter", word)
			case prev == '.':
				// Member access after "]" or ")": not a variable.
			case exprKeywords[word]:
			case word[0] == '$':
			case isCall:
				if head, _, dotted := cutLast(path, '.'); dotted {
					s.pathOrder = append(s.pathOrder, head)
				}
			default:
				s.pathOrder = append(s.pathOrder, path)
			}
			afterPipe, prev, i = false, 'a', j
		default:
			prev = c
			i++
		}
	}
}

func cutLast(s string, sep byte) (head, tail string, ok bool) {
	i := strings.LastIndexByte(s, sep)
	if i < 0 {
		return s, "", false
	}
	return s[:i], s[i+1:], true
}

// firstString returns the content of the leading quoted string of s.
func firstString(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" || (s[0] != '"' && s[0] != '\'' && s[0] != '`') {
		return "", false
	}
	end := skipString(s, 0)
	if end < 2 || s[end-1] != s[0] {
		return "", false
	}
	return strings.ReplaceAll(s[1:end-1], `\`+string(s[0]), string(s[0])), true
}

// skipString returns the index just past the string literal starting at i.
func skipString(s string, i int) int {
	q := s[i]
	i++
	for i < len(s) {
		switch s[i] {
		case '\\':
			i += 2
			continue
		case q:
			return i + 1
		}
		i++
	}
	return len(s)
}

// matchClose returns the index of the bracket closing the one at i, ignoring
// brackets inside string literals, or -1.
func matchClose(s string, i int) int {
	open := s[i]
	var closer byte
	switch open {
	case '(':
		closer = ')'
	case '{':
		closer = '}'
	case '[':
		closer = ']'
	default:
		return -1
	}
	depth := 0
	for j := i; j < len(s); j++ {
		switch s[j] {
		case '"', '\'', '`':
			j = skipString(s, j) - 1
		case open:
			depth++
		case closer:
			depth--
			if depth == 0 {
				return j
			}
		}
	}
	return -1
}

// splitTop splits s on sep outside strings and brackets.
func splitTop(s string, sep byte) []string {
	var out []string
	depth, start := 0, 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '"', '\'', '`':
			i = skipString(s, i) - 1
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			depth--
		case sep:
			if depth == 0 {
				out = append(out, s[start:i])
				start = i + 1
			}
		}
	}
	return append(out, s[start:])
}

// cutTop cuts s at the first sep outside strings and brackets.
func cutTop(s string, sep byte) (before, after string, found bool) {
	parts := splitTop(s, sep)
	if len(parts) == 1 {
		return s, "", false
	}
	return parts[0], s[len(parts[0])+1:], true
}

func isSpace(c byte) bool      { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }
func isDigit(c byte) bool      { return c >= '0' && c <= '9' }
func isLetter(c byte) bool     { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }
func isIdentStart(c byte) bool { return isLetter(c) || c == '_' || c == '$' }
func isIdentPart(c byte) bool  { return isLetter(c) || isDigit(c) || c == '_' || c == '$' }
