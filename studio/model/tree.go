package model

import (
	"fmt"
	"strings"

	"github.com/oarkflow/bcl"
)

// stmt is one statement of a body: a field (`name value`), a block
// (`type "id" { ... }`) or an id-less object (`authz { ... }`). Offsets are byte
// offsets into the source the tree was built from.
type stmt struct {
	start, end int // first token start .. last token end (closing brace for blocks)
	head       string
	headStart  int
	headEnd    int

	hasBody     bool
	opaqueBody  bool // body not parsed into children (schema declarations)
	open, close int  // offsets of '{' and '}' (block statements)
	children    []*stmt

	hasID   bool // an id token follows the head
	idOpaq  bool // the header has more than head+id (override x "y" {, when cond {)
	id      string
	idStart int
	idEnd   int
	idQuote bool // the id token was a string literal

	hasEq            bool
	valStart, valEnd int // field value range; valStart==valEnd when there is none

	// path is the canonical Path of the statement, filled in after parsing.
	path  Path
	index int // position among the parent's statements
}

type treeParser struct {
	src  string
	toks []tok
	pos  int
}

// parseTree builds the statement tree of src.
func parseTree(src string) ([]*stmt, error) {
	toks, err := scan(src)
	if err != nil {
		return nil, err
	}
	p := &treeParser{src: src, toks: toks}
	stmts, err := p.body(false)
	if err != nil {
		return nil, err
	}
	assignPaths(stmts, nil)
	return stmts, nil
}

func (p *treeParser) skipTrivia() {
	for {
		switch p.toks[p.pos].kind {
		case tNL, tComment, tComma:
			p.pos++
		default:
			return
		}
	}
}

func (p *treeParser) body(nested bool) ([]*stmt, error) {
	var out []*stmt
	for {
		p.skipTrivia()
		t := p.toks[p.pos]
		switch t.kind {
		case tEOF:
			if nested {
				return nil, fmt.Errorf("unexpected end of file, expected '}'")
			}
			return out, nil
		case tRBrace:
			if !nested {
				return nil, fmt.Errorf("unbalanced '}' at offset %d", t.start)
			}
			return out, nil // caller consumes the brace
		}
		s, err := p.statement()
		if err != nil {
			return nil, err
		}
		s.index = len(out)
		out = append(out, s)
	}
}

func (p *treeParser) text(t tok) string { return p.src[t.start:t.end] }

var exprWords = map[string]bool{
	"in": true, "not_in": true, "contains": true, "starts_with": true, "ends_with": true,
	"matches": true, "has": true, "has_any": true, "has_all": true, "between": true,
	"exists": true, "empty": true, "equals": true, "greater_than": true, "less_than": true,
	"greater_or_equal": true, "less_or_equal": true, "and": true, "or": true,
}

func isOpener(k tokKind) bool { return k == tLBrace || k == tLBracket || k == tLParen }
func isCloser(k tokKind) bool { return k == tRBrace || k == tRBracket || k == tRParen }

// sig returns the index of the first non-comment token at or after i.
func (p *treeParser) sig(i int) int {
	for p.toks[i].kind == tComment {
		i++
	}
	return i
}

func (p *treeParser) kindAt(i int) tokKind { return p.toks[p.sig(i)].kind }

func (p *treeParser) isExprWord(i int) bool {
	t := p.toks[i]
	return t.kind == tIdent && exprWords[p.text(t)]
}

func (p *treeParser) atEnd(k tokKind) bool { return k == tNL || k == tRBrace || k == tEOF }

// closerOf returns the index of the closer matching the opener at i.
func (p *treeParser) closerOf(i int) (int, error) {
	var stack []tokKind
	for j := i; j < len(p.toks); j++ {
		k := p.toks[j].kind
		switch {
		case isOpener(k):
			stack = append(stack, k)
		case isCloser(k):
			want := map[tokKind]tokKind{tRBrace: tLBrace, tRBracket: tLBracket, tRParen: tLParen}[k]
			if len(stack) == 0 || stack[len(stack)-1] != want {
				return 0, fmt.Errorf("mismatched %q at offset %d", p.text(p.toks[j]), p.toks[j].start)
			}
			stack = stack[:len(stack)-1]
			if len(stack) == 0 {
				return j, nil
			}
		case k == tEOF:
			return 0, fmt.Errorf("unterminated group opened at offset %d", p.toks[i].start)
		}
	}
	return 0, fmt.Errorf("unterminated group opened at offset %d", p.toks[i].start)
}

// lineEnd consumes tokens from i until a newline, '}' or EOF at depth 0 and
// returns the index of the last significant token consumed (or i-1 when none).
func (p *treeParser) lineEnd(i int) (last int, err error) {
	last = i - 1
	depth := 0
	for ; ; i++ {
		t := p.toks[i]
		switch {
		case t.kind == tEOF, depth == 0 && (t.kind == tNL || t.kind == tRBrace):
			return last, nil
		case t.kind == tComment:
			continue
		case t.kind == tNL:
			continue
		case isOpener(t.kind):
			depth++
		case isCloser(t.kind):
			depth--
			if depth < 0 {
				return 0, fmt.Errorf("mismatched %q at offset %d", p.text(t), t.start)
			}
		}
		last = i
	}
}

// exprLine mirrors bcl's isExpressionLine: an operator at depth 0 before the
// end of the line makes the whole rest of the line one expression.
func (p *treeParser) exprLine(i int) bool {
	if t := p.toks[i]; t.kind == tIdent && p.text(t) == "match" {
		return true
	}
	depth := 0
	for ; i < len(p.toks); i++ {
		t := p.toks[i]
		if t.kind == tComment {
			continue
		}
		if depth == 0 && (t.kind == tNL || t.kind == tRBrace || t.kind == tEOF) {
			return false
		}
		if depth == 0 && (t.kind == tOp || p.isExprWord(i)) {
			return true
		}
		if isOpener(t.kind) {
			depth++
		}
		if isCloser(t.kind) {
			depth--
		}
	}
	return false
}

// valueEnd returns the index of the last token of the single value starting at i.
func (p *treeParser) valueEnd(i int) (int, error) {
	switch p.toks[i].kind {
	case tString, tHeredoc, tNumber:
		return i, nil
	case tIdent:
		j := i
		for p.toks[j+1].kind == tDot && p.toks[j+2].kind == tIdent {
			j += 2
		}
		if p.toks[j+1].kind == tLParen {
			return p.closerOf(j + 1)
		}
		return j, nil
	case tLBracket:
		return p.closerOf(i)
	}
	return 0, fmt.Errorf("unsupported value at offset %d", p.toks[i].start)
}

// conditionalBlockAhead mirrors bcl: a '{' at depth 0 before the end of line.
func (p *treeParser) conditionalBlockAhead(i int) (int, bool) {
	depth := 0
	for ; i < len(p.toks); i++ {
		t := p.toks[i]
		if t.kind == tComment {
			continue
		}
		if depth == 0 && t.kind == tLBrace {
			return i, true
		}
		if depth == 0 && (t.kind == tNL || t.kind == tRBrace || t.kind == tEOF) {
			return 0, false
		}
		if t.kind == tLBracket || t.kind == tLParen {
			depth++
		}
		if t.kind == tRBracket || t.kind == tRParen {
			depth--
		}
	}
	return 0, false
}

func (p *treeParser) headText(t tok) string {
	if t.kind == tString {
		return unquote(p.text(t))
	}
	return strings.TrimSuffix(p.text(t), ":")
}

func (p *treeParser) setHead(s *stmt, hi int) {
	h := p.toks[hi]
	s.head, s.headStart, s.headEnd = p.headText(h), h.start, h.end
}

// bodyStmt finishes a statement whose body opens at token open. hdr holds the
// header tokens after the head (before '{').
func (p *treeParser) bodyStmt(s *stmt, hdr []int, open int) (*stmt, error) {
	s.hasBody = true
	s.open = p.toks[open].start
	p.pos = open + 1
	kids, err := p.body(true)
	if err != nil {
		return nil, err
	}
	cl := p.toks[p.pos]
	if cl.kind != tRBrace {
		return nil, fmt.Errorf("expected '}' at offset %d", cl.start)
	}
	s.close, s.end, s.children = cl.start, cl.end, kids
	p.pos++
	if len(hdr) > 0 && p.toks[hdr[len(hdr)-1]].kind == tEqual {
		s.hasEq = true
		hdr = hdr[:len(hdr)-1]
	}
	switch {
	case len(hdr) == 1 && !s.hasEq && (p.toks[hdr[0]].kind == tIdent || p.toks[hdr[0]].kind == tString || p.toks[hdr[0]].kind == tNumber):
		t := p.toks[hdr[0]]
		s.hasID, s.id, s.idStart, s.idEnd, s.idQuote = true, p.headText(t), t.start, t.end, t.kind == tString
		if t.kind == tIdent {
			s.id = p.text(t)
		}
	case len(hdr) >= 1:
		s.hasID, s.idOpaq = true, true
		s.idStart, s.idEnd = p.toks[hdr[0]].start, p.toks[hdr[len(hdr)-1]].end
		s.id = strings.TrimSpace(p.src[s.idStart:s.idEnd])
	}
	return s, nil
}

// fieldStmt finishes a body-less statement: value tokens run from vi to last.
func (p *treeParser) fieldStmt(s *stmt, vi, last int) *stmt {
	s.end = p.toks[last].end
	if vi > last {
		s.valStart, s.valEnd = s.end, s.end
	} else {
		s.valStart, s.valEnd = p.toks[vi].start, s.end
	}
	p.pos = last + 1
	return s
}

// lineStmt finishes a statement that owns the rest of its line.
func (p *treeParser) lineStmt(s *stmt, hi, from int) (*stmt, error) {
	last, err := p.lineEnd(from)
	if err != nil {
		return nil, err
	}
	if last < hi {
		last = hi
	}
	vi := p.sig(from)
	if vi < len(p.toks) && p.toks[vi].kind == tEqual {
		s.hasEq = true
		vi = p.sig(vi + 1)
	}
	return p.fieldStmt(s, vi, last), nil
}

// statement parses one statement, following the same decisions as bcl's
// parseNode so that statement boundaries agree with the parser.
func (p *treeParser) statement() (*stmt, error) {
	hi := p.pos
	h := p.toks[hi]
	s := &stmt{start: h.start}
	if h.kind == tLBrace {
		return p.bodyStmt(s, nil, hi)
	}
	if h.kind == tOp && p.text(h) == "&" {
		return p.spread(s, hi)
	}
	if h.kind != tIdent && h.kind != tString && h.kind != tNumber {
		return nil, fmt.Errorf("expected declaration, assignment or block at offset %d", h.start)
	}
	p.setHead(s, hi)
	n1 := p.sig(hi + 1)
	nk := p.toks[n1].kind
	end := p.atEnd(nk)

	// value(vi) handles everything after `name [=]`.
	value := func(vi int) (*stmt, error) {
		vk := p.toks[vi].kind
		switch {
		case vk == tLBrace:
			return p.bodyStmt(s, p.eqHdr(hi, vi), vi)
		case p.exprLine(vi):
			return p.lineStmt(s, hi, vi)
		}
		last, err := p.valueEnd(vi)
		if err != nil {
			return nil, err
		}
		return p.fieldStmt(s, vi, last), nil
	}

	switch h.kind {
	case tNumber:
		if end {
			return p.fieldStmt(s, hi+1, hi), nil
		}
		return value(n1)
	case tString:
		if nk == tLBrace {
			return p.bodyStmt(s, nil, n1)
		}
		if end {
			return p.fieldStmt(s, hi+1, hi), nil
		}
		return value(n1)
	}

	text := p.text(h)
	if text == "schema" && nk == tIdent && p.toks[p.sig(n1+1)].kind == tLBrace {
		// `schema` bodies use their own clause syntax; keep them opaque.
		open := p.sig(n1 + 1)
		cl, err := p.closerOf(open)
		if err != nil {
			return nil, err
		}
		t := p.toks[n1]
		s.hasBody, s.opaqueBody = true, true
		s.hasID, s.id, s.idStart, s.idEnd = true, p.text(t), t.start, t.end
		s.open, s.close, s.end = p.toks[open].start, p.toks[cl].start, p.toks[cl].end
		p.pos = cl + 1
		return s, nil
	}
	switch text {
	case "import":
		return p.lineStmt(s, hi, n1)
	case "param", "const", "type":
		if nk == tIdent {
			return p.lineStmt(s, hi, n1)
		}
	}
	if (text == "override" || text == "use") && nk == tIdent && p.kindAt(n1+1) == tString && p.kindAt(p.sig(n1+1)+1) == tLBrace {
		return p.bodyStmt(s, []int{n1, p.sig(n1 + 1)}, p.sig(p.sig(n1+1)+1))
	}
	if text == "when" && nk != tLBrace {
		if open, ok := p.conditionalBlockAhead(n1); ok {
			var hdr []int
			for j := n1; j < open; j++ {
				if p.toks[j].kind != tComment {
					hdr = append(hdr, j)
				}
			}
			return p.bodyStmt(s, hdr, open)
		}
	}
	if nk == tLParen || text == "map" || text == "field" || text == "include" {
		return p.lineStmt(s, hi, n1)
	}
	if nk == tDot {
		i := n1
		for p.toks[i].kind == tDot && p.toks[i+1].kind == tIdent {
			i += 2
		}
		nx := p.toks[i].kind
		if p.atEnd(nx) || nx == tLParen || nx == tOp || p.isExprWord(i) {
			return p.lineStmt(s, hi, n1)
		}
		// dotted assignment: the head extends over the path
		s.headEnd = p.toks[i-1].end
		s.head = p.src[s.headStart:s.headEnd]
		hi, n1 = i-1, i
		nk = p.toks[n1].kind
		end = p.atEnd(nk)
		if nk == tEqual {
			s.hasEq = true
			vi := p.sig(n1 + 1)
			return value(vi)
		}
		if end {
			return p.fieldStmt(s, hi+1, hi), nil
		}
		return value(n1)
	}
	next2 := p.sig(n1 + 1)
	switch {
	case (nk == tString || nk == tNumber || nk == tIdent) && p.toks[next2].kind == tLBrace:
		return p.bodyStmt(s, []int{n1}, next2)
	case nk == tIdent && (p.toks[next2].kind == tString || p.toks[next2].kind == tHeredoc):
		return p.fieldStmt(s, n1, next2), nil
	case nk == tLBrace:
		return p.bodyStmt(s, nil, n1)
	case nk == tOp || p.isExprWord(n1):
		return p.lineStmt(s, hi, n1)
	}
	if nk == tEqual {
		s.hasEq = true
		vi := p.sig(n1 + 1)
		if p.atEnd(p.toks[vi].kind) {
			return nil, fmt.Errorf("expected value after '=' at offset %d", p.toks[n1].start)
		}
		return value(vi)
	}
	if end {
		return p.fieldStmt(s, hi+1, hi), nil
	}
	return value(n1)
}

// spread parses `&target` or `&target { ... }`.
func (p *treeParser) spread(s *stmt, hi int) (*stmt, error) {
	t := p.sig(hi + 1)
	if k := p.toks[t].kind; k != tIdent && k != tString && k != tNumber {
		return nil, fmt.Errorf("expected spread target at offset %d", p.toks[t].start)
	}
	last := t
	for p.toks[last+1].kind == tDot && (p.toks[last+2].kind == tIdent || p.toks[last+2].kind == tString || p.toks[last+2].kind == tNumber) {
		last += 2
	}
	s.head, s.headStart, s.headEnd = "&"+p.src[p.toks[t].start:p.toks[last].end], s.start, p.toks[last].end
	if nx := p.sig(last + 1); p.toks[nx].kind == tLBrace {
		return p.bodyStmt(s, nil, nx)
	}
	return p.fieldStmt(s, last+1, last), nil
}

// eqHdr builds the header token list for a body reached through `name = {`.
func (p *treeParser) eqHdr(hi, open int) []int {
	if open > 0 && p.toks[p.sig(hi+1)].kind == tEqual {
		return []int{p.sig(hi + 1)}
	}
	return nil
}

// verify checks the statement tree against the bcl AST: same statement count
// and the same start offset per statement, recursively. Anything the editor
// would slice differently from the parser is refused up front instead of being
// discovered after a corrupting edit.
func verify(stmts []*stmt, nodes []bcl.Node, where string) error {
	if len(stmts) != len(nodes) {
		return fmt.Errorf("%s: editor sees %d statements, parser sees %d", where, len(stmts), len(nodes))
	}
	for i, s := range stmts {
		n := nodes[i]
		if got := n.GetSpan().Start.Offset; got != s.start {
			return fmt.Errorf("%s: statement %d starts at offset %d in the editor, %d in the parser", where, i, s.start, got)
		}
		var kids []bcl.Node
		var has bool
		switch x := n.(type) {
		case *bcl.Block:
			kids, has = x.Body, true
		case *bcl.Assignment:
			if o, ok := x.Value.(*bcl.Object); ok {
				kids, has = o.Fields, true
			}
		case *bcl.Spread:
			kids, has = x.Body, len(x.Body) > 0
		case *bcl.Object:
			kids, has = x.Fields, true
		}
		if has && s.hasBody && !s.opaqueBody {
			if err := verify(s.children, kids, fmt.Sprintf("%s/%d", where, i)); err != nil {
				return err
			}
		}
	}
	return nil
}
