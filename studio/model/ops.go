package model

import (
	"errors"
	"fmt"
	"strings"

	"github.com/oarkflow/bcl"
)

// All ops return a new *File. On any error the receiver is unchanged.

// SetField sets the value of the field at p. value is raw BCL (a quoted string,
// env("X","d"), 30m, [..], an expression …) and is spliced in untouched. When
// the field does not exist it is added at the end of its parent block (the
// parent must exist; a one-element path adds a top-level field).
func (f *File) SetField(p Path, value string) (*File, error) {
	if len(p) == 0 {
		return nil, errors.New("model: empty path")
	}
	value = strings.TrimSpace(value)
	if err := checkValue(value); err != nil {
		return nil, err
	}
	s, err := resolve(f.root, p)
	switch {
	case err == nil:
		if s.hasBody {
			return nil, fmt.Errorf("model: %s is a block, not a field", s.path)
		}
		repl := edit{start: s.valStart, end: s.valEnd, text: value}
		if s.valStart == s.valEnd { // bare `name` with no value yet
			repl.text = " " + value
		}
		parent := parentOf(f.root, s.path)
		return f.commit([]edit{repl}, func(nf *File) error {
			got, err := resolve(nf.root, s.path)
			if err != nil {
				return err
			}
			if got.hasBody {
				return errors.New("value turned the field into a block")
			}
			return sameCount(f, nf, parent, 0)
		})
	case errors.Is(err, ErrAmbiguous):
		return nil, err
	}
	// Create: the parent must exist.
	name := p[len(p)-1]
	if !isName(name) {
		return nil, fmt.Errorf("model: %q is not a valid field name", name)
	}
	parentPath, err := f.parentPath(p)
	if err != nil {
		return nil, err
	}
	edits, err := f.insertInto(parentPath, name+" "+value, false)
	if err != nil {
		return nil, err
	}
	return f.commit(edits, func(nf *File) error {
		kids, err := childrenAt(nf, parentPath)
		if err != nil {
			return err
		}
		if len(kids) == 0 || kids[len(kids)-1].head != name || kids[len(kids)-1].hasBody {
			return errors.New("new field was not parsed as a field")
		}
		return sameCount(f, nf, parentPath, 1)
	})
}

// RemoveField deletes the field at p together with the comment lines directly
// above it and a comment trailing it on the same line.
func (f *File) RemoveField(p Path) (*File, error) { return f.remove(p, false) }

// RemoveBlock deletes the block at p together with the comment lines directly
// above it.
func (f *File) RemoveBlock(p Path) (*File, error) { return f.remove(p, true) }

func (f *File) remove(p Path, wantBlock bool) (*File, error) {
	s, err := resolve(f.root, p)
	if err != nil {
		return nil, err
	}
	if s == nil {
		return nil, errors.New("model: empty path")
	}
	if s.hasBody != wantBlock {
		return nil, fmt.Errorf("model: %s is a %s", s.path, kindName(s))
	}
	e := f.removalEdit(s)
	parent := parentOf(f.root, s.path)
	return f.commit([]edit{e}, func(nf *File) error {
		return sameCount(f, nf, parent, -1)
	})
}

func kindName(s *stmt) string {
	if s.hasBody {
		return "block"
	}
	return "field"
}

// removalEdit returns the splice that deletes s.
func (f *File) removalEdit(s *stmt) edit {
	if ownLines(f.src, s) {
		return edit{start: attachedStart(f.src, s.start), end: lineAfter(f.src, s.end)}
	}
	return edit{start: s.start, end: s.end}
}

// AddBlock appends `typ "id" { body }` as the last child of the block at parent
// (a nil parent appends to the top level). body is raw BCL statements; it may be
// empty. An empty id adds an id-less object.
func (f *File) AddBlock(parent Path, typ, id, body string) (*File, error) {
	if !isName(typ) {
		return nil, fmt.Errorf("model: %q is not a valid block type", typ)
	}
	if strings.ContainsAny(id, "\r\n") {
		return nil, errors.New("model: block id has a line break")
	}
	if err := checkBody(body); err != nil {
		return nil, err
	}
	header := typ
	if id != "" {
		q, err := Quote(id)
		if err != nil {
			return nil, err
		}
		header += " " + q
	}
	var b strings.Builder
	b.WriteString(header + " {" + f.eol)
	if bt := strings.Trim(body, "\r\n"); strings.TrimSpace(bt) != "" {
		b.WriteString(strings.ReplaceAll(strings.ReplaceAll(bt, "\r\n", "\n"), "\n", f.eol) + f.eol)
	}
	b.WriteString("}")
	edits, err := f.insertInto(parent, b.String(), true)
	if err != nil {
		return nil, err
	}
	return f.commit(edits, func(nf *File) error {
		kids, err := childrenAt(nf, parent)
		if err != nil {
			return err
		}
		if len(kids) == 0 {
			return errors.New("block vanished")
		}
		k := kids[len(kids)-1]
		if !k.hasBody || k.head != typ || k.id != id {
			return errors.New("new block was not parsed as requested")
		}
		return sameCount(f, nf, parent, 1)
	})
}

// RenameBlock changes the id of the block at p.
func (f *File) RenameBlock(p Path, newID string) (*File, error) {
	s, err := resolve(f.root, p)
	if err != nil {
		return nil, err
	}
	if s == nil || !s.hasBody {
		return nil, fmt.Errorf("model: %s is not a block", p)
	}
	if !s.hasID || s.idOpaq {
		return nil, fmt.Errorf("model: %s has no renameable id", s.path)
	}
	if newID == "" || strings.ContainsAny(newID, "\r\n") {
		return nil, errors.New("model: new id must be non-empty and single-line")
	}
	text := newID
	if s.idQuote || !isBareID(newID) {
		q, err := Quote(newID)
		if err != nil {
			return nil, err
		}
		text = q
	}
	parent := parentOf(f.root, s.path)
	idx := s.index
	return f.commit([]edit{{start: s.idStart, end: s.idEnd, text: text}}, func(nf *File) error {
		kids, err := childrenAt(nf, parent)
		if err != nil {
			return err
		}
		if idx >= len(kids) || kids[idx].id != newID || !kids[idx].hasBody {
			return errors.New("rename did not land on the same block")
		}
		return sameCount(f, nf, parent, 0)
	})
}

// MoveBlock moves the statement at p so it becomes child number index (0-based,
// counted before the move) of its parent. The statement, its attached comments
// and its blank-line separator travel together. Both the statement and its
// neighbours must occupy whole lines.
func (f *File) MoveBlock(p Path, index int) (*File, error) {
	s, err := resolve(f.root, p)
	if err != nil {
		return nil, err
	}
	if s == nil {
		return nil, errors.New("model: empty path")
	}
	parent := parentOf(f.root, s.path)
	sibs := siblingsOf(f.root, s.path)
	if index < 0 || index > len(sibs) {
		return nil, fmt.Errorf("model: index %d out of range 0..%d", index, len(sibs))
	}
	if index == s.index || index == s.index+1 {
		return f, nil
	}
	if !ownLines(f.src, s) {
		return nil, fmt.Errorf("model: %s shares a line with other code; cannot move", s.path)
	}
	// The unit is the attached comments plus the statement's lines. Files that
	// separate siblings with blank lines keep doing so: the blank line next to
	// the unit is cut with it and re-added at the destination.
	blankSep := false
	for i := 1; i < len(sibs); i++ {
		prevEnd := lineAfter(f.src, sibs[i-1].end)
		if prevEnd < attachedStart(f.src, sibs[i].start) {
			blankSep = true
			break
		}
	}
	us, ue := attachedStart(f.src, s.start), lineAfter(f.src, s.end)
	unit := f.src[us:ue]
	if !strings.HasSuffix(unit, "\n") {
		unit += f.eol
	}
	if blankSep {
		if ue < len(f.src) && isBlank(f.src[ue:lineAfter(f.src, ue)]) {
			for ue < len(f.src) && isBlank(f.src[ue:lineAfter(f.src, ue)]) {
				ue = lineAfter(f.src, ue)
			}
		} else {
			for us > 0 && isBlank(f.src[lineStart(f.src, us-1):us]) {
				us = lineStart(f.src, us-1)
			}
		}
	}
	// Insert position: before sibling `index`, or after the last sibling.
	var at int
	ins := unit
	switch {
	case index < len(sibs):
		t := sibs[index]
		if !ownLines(f.src, t) {
			return nil, fmt.Errorf("model: %s shares a line with other code; cannot move next to it", t.path)
		}
		at = attachedStart(f.src, t.start)
		if blankSep {
			ins += f.eol
		}
	default:
		t := sibs[len(sibs)-1]
		if !ownLines(f.src, t) {
			return nil, fmt.Errorf("model: %s shares a line with other code; cannot move next to it", t.path)
		}
		at = lineAfter(f.src, t.end)
		if !strings.HasSuffix(f.src[:at], "\n") {
			ins = f.eol + ins
		}
		if blankSep {
			ins = f.eol + ins
		}
	}
	if at >= us && at <= ue {
		return f, nil
	}
	name, id := s.head, s.id
	return f.commit([]edit{{start: us, end: ue}, {start: at, end: at, text: ins}}, func(nf *File) error {
		kids, err := childrenAt(nf, parent)
		if err != nil {
			return err
		}
		if len(kids) != len(sibs) {
			return errors.New("move changed the statement count")
		}
		want := index
		if index > s.index {
			want = index - 1
		}
		if kids[want].head != name || kids[want].id != id {
			return errors.New("statement did not land at the requested index")
		}
		return nil
	})
}

func endsWithBlank(s string) bool {
	t := strings.TrimRight(s, "\r")
	return strings.HasSuffix(t, "\n\n") || strings.HasSuffix(t, "\n\r\n")
}

// insertInto builds the edits that append text (no trailing newline) as the last
// statement of the block at parent (nil = top level).
func (f *File) insertInto(parent Path, text string, isBlock bool) ([]edit, error) {
	src := f.src
	if len(parent) == 0 {
		var pre string
		if src != "" && !strings.HasSuffix(src, "\n") {
			pre = f.eol
		}
		if isBlock && strings.TrimSpace(src) != "" && !endsWithBlank(src+pre) {
			pre += f.eol
		}
		return []edit{{start: len(src), end: len(src), text: pre + text + f.eol}}, nil
	}
	s, err := resolve(f.root, parent)
	if err != nil {
		return nil, err
	}
	if !s.hasBody {
		return nil, fmt.Errorf("model: %s is a field, not a block", s.path)
	}
	if s.opaqueBody {
		return nil, fmt.Errorf("model: %s has a schema body the editor does not edit", s.path)
	}
	var eds []edit
	// break `{ x 1 }` open: anything between '{' and end of line other than a
	// comment goes onto its own line first.
	i := s.open + 1
	for i < s.close && (src[i] == ' ' || src[i] == '\t') {
		i++
	}
	if i < s.close && src[i] != '\n' && src[i] != '\r' && src[i] != '#' && !strings.HasPrefix(src[i:], "//") {
		eds = append(eds, edit{start: s.open + 1, end: s.open + 1, text: f.eol})
	}
	ls := lineStart(src, s.close)
	if isBlank(src[ls:s.close]) {
		eds = append(eds, edit{start: ls, end: ls, text: text + f.eol})
	} else {
		eds = append(eds, edit{start: s.close, end: s.close, text: f.eol + text + f.eol})
	}
	return eds, nil
}

// parentPath returns the path of the block that would contain a new field at p:
// p without its last segment (a field name never carries an id).
func (f *File) parentPath(p Path) (Path, error) {
	if len(p) <= 1 {
		return nil, nil
	}
	return p[:len(p)-1], nil
}

// parentOf returns the path of the parent of the statement whose canonical path
// is p; nil for top-level statements.
func parentOf(root []*stmt, p Path) Path {
	var walk func(level []*stmt, prefix Path) (Path, bool)
	walk = func(level []*stmt, prefix Path) (Path, bool) {
		for _, s := range level {
			if pathEq(s.path, p) {
				return prefix, true
			}
			if s.hasBody && len(s.path) < len(p) {
				if pp, ok := walk(s.children, s.path); ok {
					return pp, true
				}
			}
		}
		return nil, false
	}
	pp, _ := walk(root, nil)
	return pp
}

func siblingsOf(root []*stmt, p Path) []*stmt {
	pp := parentOf(root, p)
	if len(pp) == 0 {
		return root
	}
	s, _ := resolve(root, pp)
	if s == nil {
		return nil
	}
	return s.children
}

func pathEq(a, b Path) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func childrenAt(f *File, p Path) ([]*stmt, error) {
	if len(p) == 0 {
		return f.root, nil
	}
	s, err := resolve(f.root, p)
	if err != nil {
		return nil, err
	}
	return s.children, nil
}

// sameCount checks that the statement count under p changed by delta.
func sameCount(before, after *File, p Path, delta int) error {
	a, err := childrenAt(before, p)
	if err != nil {
		return err
	}
	b, err := childrenAt(after, p)
	if err != nil {
		return err
	}
	if len(b) != len(a)+delta {
		return fmt.Errorf("expected %d statements under %s, got %d", len(a)+delta, p, len(b))
	}
	return nil
}

// checkValue rejects a value that is not exactly one BCL value.
func checkValue(v string) error {
	if v == "" {
		return errors.New("model: empty value")
	}
	doc, err := bcl.ParseFile("value", []byte("x "+v))
	if err != nil {
		return fmt.Errorf("model: invalid value %q: %w", v, err)
	}
	if len(doc.Items) != 1 {
		return fmt.Errorf("model: %q is not a single value", v)
	}
	if _, ok := doc.Items[0].(*bcl.Assignment); !ok {
		return fmt.Errorf("model: %q is not a plain value", v)
	}
	return nil
}

// checkBody rejects a block body that does not parse on its own.
func checkBody(body string) error {
	if strings.TrimSpace(body) == "" {
		return nil
	}
	if _, err := bcl.ParseFile("body", []byte(body)); err != nil {
		return fmt.Errorf("model: invalid block body: %w", err)
	}
	return nil
}
