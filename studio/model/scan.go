package model

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// tokKind is the coarse lexical class the editor needs. It deliberately knows
// less than the bcl lexer: the AST stays the authority on meaning, this scanner
// only locates statement boundaries.
type tokKind uint8

const (
	tNL tokKind = iota
	tComment
	tString
	tHeredoc
	tIdent
	tNumber
	tOp
	tDot
	tLBrace
	tRBrace
	tLBracket
	tRBracket
	tLParen
	tRParen
	tEqual
	tComma
	tEOF
)

type tok struct {
	kind       tokKind
	start, end int // byte offsets into the source
}

// scan tokenizes src, mirroring the token boundaries of bcl's lexer for
// strings, heredocs and comments so that nothing inside them is mistaken for
// structure.
func scan(src string) ([]tok, error) {
	var toks []tok
	i := 0
	n := len(src)
	add := func(k tokKind, s, e int) { toks = append(toks, tok{k, s, e}) }
	for i < n {
		c := src[i]
		switch {
		case c == ' ' || c == '\t' || c == '\r':
			i++
		case c == '\n':
			add(tNL, i, i+1)
			i++
		case c == '#' || (c == '/' && i+1 < n && src[i+1] == '/'):
			s := i
			for i < n && src[i] != '\n' {
				i++
			}
			add(tComment, s, i)
		case c == '/' && i+1 < n && src[i+1] == '*':
			s := i
			j := strings.Index(src[i+2:], "*/")
			if j < 0 {
				return nil, fmt.Errorf("unterminated block comment at offset %d", s)
			}
			i += 2 + j + 2
			add(tComment, s, i)
		case c == '{':
			add(tLBrace, i, i+1)
			i++
		case c == '}':
			add(tRBrace, i, i+1)
			i++
		case c == '[':
			add(tLBracket, i, i+1)
			i++
		case c == ']':
			add(tRBracket, i, i+1)
			i++
		case c == '(':
			add(tLParen, i, i+1)
			i++
		case c == ')':
			add(tRParen, i, i+1)
			i++
		case c == ',':
			add(tComma, i, i+1)
			i++
		case c == '=':
			if i+1 < n && src[i+1] == '=' {
				add(tOp, i, i+2)
				i += 2
			} else {
				add(tEqual, i, i+1)
				i++
			}
		case c == '.':
			add(tDot, i, i+1)
			i++
		case (c == '<' || c == '>' || c == '!') && !(c == '<' && i+1 < n && src[i+1] == '<'):
			if i+1 < n && src[i+1] == '=' {
				add(tOp, i, i+2)
				i += 2
			} else {
				add(tOp, i, i+1)
				i++
			}
		case (c == '&' && i+1 < n && src[i+1] == '&') || (c == '|' && i+1 < n && src[i+1] == '|'):
			add(tOp, i, i+2)
			i += 2
		case c == '<' && i+1 < n && src[i+1] == '<':
			s := i
			i += 2
			for i < n && (src[i] == ' ' || src[i] == '\t') {
				i++
			}
			ms := i
			for i < n && src[i] != '\n' {
				i++
			}
			marker := src[ms:i]
			if marker == "" {
				return nil, fmt.Errorf("missing heredoc marker at offset %d", s)
			}
			if i < n {
				i++ // newline after the marker
			}
			j := strings.Index(src[i:], "\n"+marker)
			if j < 0 {
				return nil, fmt.Errorf("unterminated heredoc %q at offset %d", marker, s)
			}
			i += j + 1 + len(marker)
			add(tHeredoc, s, i)
		case c == '"' || c == '\'':
			s := i
			if c == '"' && strings.HasPrefix(src[i:], `"""`) {
				j := strings.Index(src[i+3:], `"""`)
				if j < 0 {
					return nil, fmt.Errorf("unterminated multiline string at offset %d", s)
				}
				i += 3 + j + 3
				add(tString, s, i)
				break
			}
			i++
			for {
				if i >= n || src[i] == '\n' {
					return nil, fmt.Errorf("unterminated string at offset %d", s)
				}
				if src[i] == '\\' {
					i += 2
					continue
				}
				if src[i] == c {
					i++
					break
				}
				i++
			}
			add(tString, s, i)
		case c == '`':
			s := i
			j := strings.IndexByte(src[i+1:], '`')
			if j < 0 {
				return nil, fmt.Errorf("unterminated raw string at offset %d", s)
			}
			i += 1 + j + 1
			add(tString, s, i)
		default:
			r, sz := utf8.DecodeRuneInString(src[i:])
			s := i
			kind := tOp
			switch {
			case r == '_' || unicode.IsLetter(r) || r == '*':
				kind = tIdent
				i += sz
				for i < n {
					r, sz = utf8.DecodeRuneInString(src[i:])
					if !(r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) || r == '*' || r == ':' || r == '-' || r == '/') {
						break
					}
					i += sz
				}
			case unicode.IsDigit(r) || (r == '-' && i+1 < n && src[i+1] >= '0' && src[i+1] <= '9'):
				kind = tNumber
				i += sz
				for i < n {
					r, sz = utf8.DecodeRuneInString(src[i:])
					if !(unicode.IsDigit(r) || r == '.' || unicode.IsLetter(r) || r == '-' || r == ':' || r == 'T' || r == 'Z') {
						break
					}
					i += sz
				}
			default:
				i += sz
			}
			add(kind, s, i)
		}
	}
	add(tEOF, n, n)
	return toks, nil
}

// unquote decodes the text of a string token the way bcl's lexer does.
func unquote(raw string) string {
	if strings.HasPrefix(raw, `"""`) && strings.HasSuffix(raw, `"""`) && len(raw) >= 6 {
		return raw[3 : len(raw)-3]
	}
	if len(raw) < 2 {
		return raw
	}
	q := raw[0]
	body := raw[1 : len(raw)-1]
	if q == '`' || !strings.Contains(body, `\`) {
		return body
	}
	var b strings.Builder
	for i := 0; i < len(body); i++ {
		if body[i] != '\\' || i+1 >= len(body) {
			b.WriteByte(body[i])
			continue
		}
		i++
		switch body[i] {
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		default:
			b.WriteByte(body[i])
		}
	}
	return b.String()
}
