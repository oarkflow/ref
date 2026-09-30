package model

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Path addresses a statement. Each nesting level contributes the statement name
// (its type, for blocks) followed by its id when it has one:
//
//	route / web.todos_list / authz / roles
//
// Repeated statements are told apart with an index suffix on the last segment
// of the level: node/a[1] is the second `node "a"` block, parameter[2] the third
// id-less `parameter` block.
type Path []string

// ParsePath splits "a/b/c" into segments. A backslash escapes a slash inside a
// segment ("pages\/todos").
func ParsePath(s string) Path {
	if s == "" {
		return nil
	}
	var out Path
	var cur strings.Builder
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == '\\' && i+1 < len(s) && s[i+1] == '/':
			cur.WriteByte('/')
			i++
		case s[i] == '/':
			out = append(out, cur.String())
			cur.Reset()
		default:
			cur.WriteByte(s[i])
		}
	}
	return append(out, cur.String())
}

// String renders the path with escaped slashes; ParsePath reverses it.
func (p Path) String() string {
	parts := make([]string, len(p))
	for i, s := range p {
		parts[i] = strings.ReplaceAll(s, "/", `\/`)
	}
	return strings.Join(parts, "/")
}

// Child returns p extended by segs without aliasing p.
func (p Path) Child(segs ...string) Path {
	out := make(Path, 0, len(p)+len(segs))
	out = append(out, p...)
	return append(out, segs...)
}

var (
	// ErrNotFound reports a path that names no statement.
	ErrNotFound = errors.New("model: path not found")
	// ErrAmbiguous reports a path that names more than one statement.
	ErrAmbiguous = errors.New("model: path is ambiguous")
)

// splitIndex separates "name[2]" into ("name", 2, true).
func splitIndex(seg string) (string, int, bool) {
	if !strings.HasSuffix(seg, "]") {
		return seg, 0, false
	}
	open := strings.LastIndexByte(seg, '[')
	if open < 0 {
		return seg, 0, false
	}
	n, err := strconv.Atoi(seg[open+1 : len(seg)-1])
	if err != nil || n < 0 {
		return seg, 0, false
	}
	return seg[:open], n, true
}

// assignPaths fills stmt.path for every statement under stmts.
func assignPaths(stmts []*stmt, prefix Path) {
	// Count repeats to know where an index suffix is needed.
	type key struct{ head, id string }
	counts := map[key]int{}
	for _, s := range stmts {
		counts[key{s.head, s.id}]++
	}
	seen := map[key]int{}
	for _, s := range stmts {
		k := key{s.head, s.id}
		ord := seen[k]
		seen[k]++
		segs := []string{s.head}
		if s.hasID {
			segs = append(segs, s.id)
		}
		if counts[k] > 1 {
			segs[len(segs)-1] += "[" + strconv.Itoa(ord) + "]"
		}
		s.path = prefix.Child(segs...)
		assignPaths(s.children, s.path)
	}
}

// resolve finds the statement addressed by p under root. A nil path returns nil
// without error (the document root).
func resolve(root []*stmt, p Path) (*stmt, error) {
	var cur *stmt
	level := root
	i := 0
	for i < len(p) {
		name, idx, hasIdx := splitIndex(p[i])
		var cands []*stmt
		for _, s := range level {
			if s.head == name {
				cands = append(cands, s)
			}
		}
		if len(cands) == 0 {
			return nil, fmt.Errorf("%w: %q in %s", ErrNotFound, name, Path(p[:i+1]))
		}
		var pick *stmt
		anyID := false
		for _, c := range cands {
			anyID = anyID || c.hasID
		}
		switch {
		case i+1 < len(p) && anyID && !isFieldOnlyMatch(cands, p[i+1]):
			idName, idIdx, idHas := splitIndex(p[i+1])
			var m []*stmt
			for _, c := range cands {
				if c.hasID && c.id == idName {
					m = append(m, c)
				}
			}
			if len(m) == 0 {
				return nil, fmt.Errorf("%w: %s %q in %s", ErrNotFound, name, idName, Path(p[:i+2]))
			}
			switch {
			case idHas && idIdx < len(m):
				pick = m[idIdx]
			case idHas:
				return nil, fmt.Errorf("%w: %s %q has %d matches, index %d requested", ErrNotFound, name, idName, len(m), idIdx)
			case len(m) == 1:
				pick = m[0]
			default:
				return nil, fmt.Errorf("%w: %d statements match %s (add an index, e.g. %s[0])", ErrAmbiguous, len(m), Path(p[:i+2]), p[i+1])
			}
			i += 2
		default:
			// id-less statement, or an id'd one addressed by position.
			pool := cands
			if !hasIdx {
				var plain []*stmt
				for _, c := range cands {
					if !c.hasID {
						plain = append(plain, c)
					}
				}
				if len(plain) > 0 {
					pool = plain
				}
			}
			switch {
			case hasIdx && idx < len(pool):
				pick = pool[idx]
			case hasIdx:
				return nil, fmt.Errorf("%w: %s has %d matches, index %d requested", ErrNotFound, name, len(pool), idx)
			case len(pool) == 1 && !pool[0].hasID:
				pick = pool[0]
			case len(pool) == 1:
				return nil, fmt.Errorf("%w: %s needs an id (%s/<id>)", ErrNotFound, name, Path(p[:i+1]))
			default:
				return nil, fmt.Errorf("%w: %d statements match %s (add an index, e.g. %s[0])", ErrAmbiguous, len(pool), Path(p[:i+1]), p[i])
			}
			i++
		}
		cur = pick
		level = pick.children
		if i < len(p) && !pick.hasBody {
			return nil, fmt.Errorf("%w: %s is a field and has no children", ErrNotFound, pick.path)
		}
	}
	return cur, nil
}

// isFieldOnlyMatch reports whether the next segment should be read as a
// sibling level rather than an id: true when no id'd candidate carries that id
// but an id-less candidate exists (e.g. authz/roles: `authz {}` then field roles).
func isFieldOnlyMatch(cands []*stmt, next string) bool {
	name, _, _ := splitIndex(next)
	hasPlain := false
	for _, c := range cands {
		if c.hasID && c.id == name {
			return false
		}
		if !c.hasID {
			hasPlain = true
		}
	}
	return hasPlain
}
