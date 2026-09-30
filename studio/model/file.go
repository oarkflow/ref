package model

import (
	"bytes"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/oarkflow/bcl"
)

// Kind tells fields from blocks.
type Kind uint8

const (
	// KindField is `name value`.
	KindField Kind = iota
	// KindBlock is `type "id" { ... }` or an id-less `name { ... }` object.
	KindBlock
)

func (k Kind) String() string {
	if k == KindBlock {
		return "block"
	}
	return "field"
}

// Node is a read-only view of one statement.
type Node struct {
	Path   Path
	Kind   Kind
	Head   string // block type / field name
	ID     string
	HasID  bool
	Index  int // position among its siblings
	Start  int // byte offsets in File.Source()
	End    int
	Line   int    // 1-based line of Start
	Value  string // raw BCL text of a field's value
	Doc    string // comment lines attached directly above (markers stripped)
	Opaque bool   // header the editor cannot rename (override x "y" {, when cond {)
}

// File is an immutable, parsed BCL source. Every edit returns a new File and
// leaves the receiver untouched, so failed edits and undo need no bookkeeping.
type File struct {
	name string
	src  string
	root []*stmt
	fmt  bcl.FormatOptions
	eol  string
}

// Open parses src and prepares it for editing. The source is kept byte for
// byte until the first edit; the formatting style (tabs or spaces, indent width)
// is detected from it.
func Open(name string, src []byte) (*File, error) {
	return openWith(name, string(src), nil)
}

// OpenWith is Open with an explicit formatting style.
func OpenWith(name string, src []byte, format bcl.FormatOptions) (*File, error) {
	return openWith(name, string(src), &format)
}

func openWith(name, src string, format *bcl.FormatOptions) (*File, error) {
	doc, err := bcl.ParseFile(name, []byte(src))
	if err != nil {
		return nil, fmt.Errorf("model: parse %s: %w", name, err)
	}
	root, err := parseTree(src)
	if err != nil {
		return nil, fmt.Errorf("model: %s: %w", name, err)
	}
	if err := verify(root, doc.Items, "root"); err != nil {
		return nil, fmt.Errorf("model: %s: unsupported layout: %w", name, err)
	}
	f := &File{name: name, src: src, root: root, eol: bcl.LineEnding([]byte(src))}
	if f.eol == "" {
		f.eol = "\n"
	}
	if format != nil {
		f.fmt = *format
	} else {
		f.fmt = detectFormat(src, root)
	}
	return f, nil
}

// detectFormat infers the indentation style from the first nested statement.
func detectFormat(src string, root []*stmt) bcl.FormatOptions {
	opts := bcl.FormatOptions{KeepBlankLines: true}
	var find func([]*stmt) *stmt
	find = func(ss []*stmt) *stmt {
		for _, s := range ss {
			if len(s.children) > 0 {
				return s.children[0]
			}
		}
		return nil
	}
	if c := find(root); c != nil {
		ls := lineStart(src, c.start)
		ind := src[ls:c.start]
		if strings.TrimLeft(ind, " \t") == "" && len(ind) > 0 {
			if ind[0] == '\t' {
				opts.UseTabs = true
			} else if len(ind) <= 8 {
				opts.IndentWidth = len(ind)
			}
		}
	}
	return opts
}

// Name is the file name given to Open.
func (f *File) Name() string { return f.name }

// Source returns a copy of the current source text.
func (f *File) Source() []byte { return []byte(f.src) }

// Text is Source as a string.
func (f *File) Text() string { return f.src }

// Format is the style edits are formatted with.
func (f *File) Format() bcl.FormatOptions { return f.fmt }

// WithFormat returns a File that formats future edits with opts.
func (f *File) WithFormat(opts bcl.FormatOptions) *File {
	nf := *f
	nf.fmt = opts
	return &nf
}

// Blocks lists the top-level blocks in file order.
func (f *File) Blocks() []Node {
	var out []Node
	for _, s := range f.root {
		if s.hasBody {
			out = append(out, f.node(s))
		}
	}
	return out
}

// Statements lists the children of the statement at p; a nil path lists the
// top level.
func (f *File) Statements(p Path) ([]Node, error) {
	level := f.root
	if len(p) > 0 {
		s, err := resolve(f.root, p)
		if err != nil {
			return nil, err
		}
		if !s.hasBody {
			return nil, fmt.Errorf("model: %s is a field", s.path)
		}
		level = s.children
	}
	out := make([]Node, len(level))
	for i, s := range level {
		out[i] = f.node(s)
	}
	return out, nil
}

// Lookup finds the statement at p.
func (f *File) Lookup(p Path) (Node, error) {
	if len(p) == 0 {
		return Node{}, fmt.Errorf("%w: empty path", ErrNotFound)
	}
	s, err := resolve(f.root, p)
	if err != nil {
		return Node{}, err
	}
	return f.node(s), nil
}

// FindBlock is a convenience for looking up a top-level block by type and id.
func (f *File) FindBlock(typ, id string) (Node, error) {
	if id == "" {
		return f.Lookup(Path{typ})
	}
	return f.Lookup(Path{typ, id})
}

func (f *File) node(s *stmt) Node {
	n := Node{
		Path: s.path, Head: s.head, ID: s.id, HasID: s.hasID, Index: s.index,
		Start: s.start, End: s.end, Opaque: s.idOpaq,
		Line: 1 + strings.Count(f.src[:s.start], "\n"),
	}
	if s.hasBody {
		n.Kind = KindBlock
	} else {
		n.Kind = KindField
		n.Value = f.src[s.valStart:s.valEnd]
	}
	if as := attachedStart(f.src, s.start); as < lineStart(f.src, s.start) {
		var lines []string
		for _, l := range strings.Split(strings.TrimRight(f.src[as:lineStart(f.src, s.start)], "\r\n"), "\n") {
			l = strings.TrimSpace(l)
			l = strings.TrimPrefix(strings.TrimPrefix(l, "#"), "//")
			lines = append(lines, strings.TrimSpace(l))
		}
		n.Doc = strings.Join(lines, "\n")
	}
	return n
}

// edit is one splice of the source: [start,end) is replaced by text.
type edit struct {
	start, end int
	text       string
}

// commit applies edits, reformats, reparses and verifies the result, then runs
// check against it. Any failure returns the error and no File.
func (f *File) commit(edits []edit, check func(nf *File) error) (*File, error) {
	sort.SliceStable(edits, func(i, j int) bool { return edits[i].start > edits[j].start })
	src := f.src
	// apply back to front so earlier offsets stay valid
	out := src
	for i, e := range edits {
		if e.start < 0 || e.end < e.start || e.end > len(src) {
			return nil, fmt.Errorf("model: internal error: bad edit range [%d,%d)", e.start, e.end)
		}
		if i > 0 && e.end > edits[i-1].start {
			return nil, errors.New("model: internal error: overlapping edits")
		}
		out = out[:e.start] + e.text + out[e.end:]
	}
	formatted, err := bcl.FormatWithOptions([]byte(out), f.fmt)
	if err != nil {
		return nil, fmt.Errorf("model: edit produced invalid source: %w", err)
	}
	fmtOpts := f.fmt
	nf, err := openWith(f.name, string(formatted), &fmtOpts)
	if err != nil {
		return nil, fmt.Errorf("model: edit produced invalid source: %w", err)
	}
	nf.eol = f.eol
	if check != nil {
		if err := check(nf); err != nil {
			return nil, fmt.Errorf("model: edit rejected: %w", err)
		}
	}
	return nf, nil
}

// Reformat re-formats the whole file. It changes whitespace only; comments and
// declaration order are kept.
func (f *File) Reformat() (*File, error) {
	return f.commit(nil, nil)
}

// Equal reports whether two files have identical source.
func (f *File) Equal(o *File) bool { return f.src == o.src }

// helpers ---------------------------------------------------------------

func lineStart(src string, off int) int {
	return bytes.LastIndexByte([]byte(src[:off]), '\n') + 1
}

// lineAfter returns the offset just past the newline ending the line that
// contains off, or len(src).
func lineAfter(src string, off int) int {
	i := strings.IndexByte(src[off:], '\n')
	if i < 0 {
		return len(src)
	}
	return off + i + 1
}

func isBlank(s string) bool { return strings.TrimLeft(s, " \t\r") == "" }

// attachedStart returns the start of the comment block directly above the
// statement starting at off (no blank line between), or the line start of off.
func attachedStart(src string, off int) int {
	ls := lineStart(src, off)
	if !isBlank(src[ls:off]) {
		return off
	}
	for ls > 0 {
		prevEnd := ls - 1
		prevStart := lineStart(src, prevEnd)
		line := strings.TrimSpace(src[prevStart:prevEnd])
		if strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//") {
			ls = prevStart
			continue
		}
		break
	}
	return ls
}

// ownLines reports whether the statement occupies whole lines: only blanks
// before it on its first line and only blanks or a line comment after it.
func ownLines(src string, s *stmt) bool {
	if !isBlank(src[lineStart(src, s.start):s.start]) {
		return false
	}
	end := lineAfter(src, s.end)
	tail := strings.TrimSpace(src[s.end:end])
	return tail == "" || strings.HasPrefix(tail, "#") || strings.HasPrefix(tail, "//")
}
