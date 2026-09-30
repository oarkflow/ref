package platform

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/oarkflow/bcl"
)

// Bundle limits. A bundle is configuration, not an asset store.
const (
	MaxBundleFiles     = 512
	MaxBundleFileBytes = 4 << 20
	MaxBundleBytes     = 16 << 20
)

// BundleFile is one BCL file of an application: its file name (bundles are
// flat, so there are no directories) and its content.
type BundleFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// Bundle is an application's BCL files, sorted by path. NewBundle builds a
// valid one; Source is what the compiler sees.
type Bundle []BundleFile

// NewBundle validates files and returns them as a sorted Bundle (the input is
// not modified). A path must be a plain file name — non-empty, no "/" or "\",
// valid UTF-8 without control characters, ending in ".bcl" with something
// before the extension — and appear once. Bundles are flat because LoadDir and
// ReadBundleDir only read the top level of a directory.
func NewBundle(files []BundleFile) (Bundle, error) {
	if len(files) == 0 {
		return nil, errors.New("ref/platform: a bundle needs at least one file")
	}
	if len(files) > MaxBundleFiles {
		return nil, fmt.Errorf("ref/platform: a bundle holds at most %d files (got %d)", MaxBundleFiles, len(files))
	}
	b := make(Bundle, len(files))
	copy(b, files)
	sort.Slice(b, func(i, j int) bool { return b[i].Path < b[j].Path })
	total := 0
	for i, f := range b {
		if err := validBundlePath(f.Path); err != nil {
			return nil, err
		}
		if i > 0 && b[i-1].Path == f.Path {
			return nil, fmt.Errorf("ref/platform: bundle path %q appears twice", f.Path)
		}
		if len(f.Content) > MaxBundleFileBytes {
			return nil, fmt.Errorf("ref/platform: bundle file %q is %d bytes; the limit is %d", f.Path, len(f.Content), MaxBundleFileBytes)
		}
		total += len(f.Content)
		if total > MaxBundleBytes {
			return nil, fmt.Errorf("ref/platform: a bundle holds at most %d bytes", MaxBundleBytes)
		}
	}
	return b, nil
}

func validBundlePath(p string) error {
	bad := func(why string) error { return fmt.Errorf("ref/platform: bundle path %q %s", p, why) }
	switch {
	case p == "":
		return errors.New("ref/platform: bundle path is empty")
	case !utf8.ValidString(p):
		return bad("is not valid UTF-8")
	case strings.Contains(p, "/"):
		return bad("must be a plain file name; bundles are flat, so no directories")
	case strings.Contains(p, `\`):
		return bad("must not contain a backslash")
	case !strings.HasSuffix(p, ".bcl"):
		return bad(`must end in ".bcl"`)
	case p == ".bcl":
		return bad("has no file name")
	}
	for _, r := range p {
		if r < 0x20 || r == 0x7f {
			return bad("has a control character")
		}
	}
	return nil
}

// JoinBundle concatenates files in the order given, each followed by "\n".
// It is the one place that defines how several files become the single
// document the compiler reads: Bundle.Source, LoadFiles and LoadDir all use it.
func JoinBundle(files []BundleFile) []byte {
	n := 0
	for _, f := range files {
		n += len(f.Content) + 1
	}
	buf := bytes.NewBuffer(make([]byte, 0, n))
	for _, f := range files {
		buf.WriteString(f.Content)
		buf.WriteByte('\n')
	}
	return buf.Bytes()
}

// Source is the bundle's document: the files in path order, each followed by
// "\n" — byte for byte what LoadDir compiles for the same directory.
func (b Bundle) Source() []byte { return JoinBundle(b) }

// Locate maps a 1-based line of Source back to the file holding it and the
// 1-based line within that file. ok is false past the end of the document.
func (b Bundle) Locate(line int) (file string, fileLine int, ok bool) {
	return locateLine(b, line)
}

// LocateOffset maps a byte offset into Source to the file holding it and the
// offset within that file (a file's trailing separator "\n" belongs to it).
func (b Bundle) LocateOffset(offset int) (file string, fileOffset int, ok bool) {
	if offset < 0 {
		return "", 0, false
	}
	start := 0
	for _, f := range b {
		size := len(f.Content) + 1
		if offset < start+size {
			return f.Path, offset - start, true
		}
		start += size
	}
	return "", 0, false
}

func locateLine[F ~[]BundleFile](files F, line int) (string, int, bool) {
	if line < 1 {
		return "", 0, false
	}
	start := 1
	for _, f := range files {
		// Content plus its separator newline occupies strings.Count+1 lines.
		lines := strings.Count(f.Content, "\n") + 1
		if line < start+lines {
			return f.Path, line - start + 1, true
		}
		start += lines
	}
	return "", 0, false
}

// locateClamped is locateLine for diagnostics: a position at or past the end
// of the joined document (an "unexpected end of file" error points one line
// beyond the last file) is attributed to the last line of the last file.
func locateClamped(files []BundleFile, line int) (string, int, bool) {
	if f, l, ok := locateLine(files, line); ok {
		return f, l, true
	}
	if line < 1 || len(files) == 0 {
		return "", 0, false
	}
	last := files[len(files)-1]
	return last.Path, strings.Count(last.Content, "\n") + 1, true
}

// remapSpan rewrites a span over the joined document into one over the file
// that holds it.
func remapSpan(files []BundleFile, s bcl.Span) bcl.Span {
	if s.Start.Line < 1 {
		return s
	}
	file, line, ok := locateClamped(files, s.Start.Line)
	if !ok {
		return s
	}
	shift := s.Start.Line - line
	start := 0
	for _, f := range files {
		if f.Path == file {
			break
		}
		start += len(f.Content) + 1
	}
	s.File = file
	s.Start.Line = line
	s.Start.Offset = max(s.Start.Offset-start, 0)
	if s.End.Line >= 1 {
		s.End.Line = max(s.End.Line-shift, 1)
		s.End.Offset = max(s.End.Offset-start, 0)
	}
	return s
}

func remapSourceSpan(files []BundleFile, s *SourceSpan) {
	if s == nil || s.Line < 1 {
		return
	}
	file, line, ok := locateClamped(files, s.Line)
	if !ok {
		return
	}
	start := 0
	for _, f := range files {
		if f.Path == file {
			break
		}
		start += len(f.Content) + 1
	}
	s.File, s.Line, s.Offset = file, line, max(s.Offset-start, 0)
}

// remapCompileError rewrites the spans of a bcl diagnostic list wrapped in err
// so they name a file of the bundle instead of a line of the joined document.
// Errors without spans pass through unchanged.
func remapCompileError(files []BundleFile, err error) error {
	var list bcl.ErrorList
	if !errors.As(err, &list) || len(list) == 0 {
		return err
	}
	out := make(bcl.ErrorList, len(list))
	for i, d := range list {
		d.Span = remapSpan(files, d.Span)
		out[i] = d
	}
	return fmt.Errorf("ref/platform: compile BCL: %w", out)
}

// ValidateBundle is Validate over a bundle: the same checks on Bundle.Source,
// with every diagnostic span pointing at the file and line it came from.
// Validate itself is unchanged and still takes a single document.
func ValidateBundle(ctx context.Context, b Bundle, baseDir string, opts LoadOptions) ValidationReport {
	r := Validate(ctx, b.Source(), baseDir, opts)
	for i := range r.Diagnostics {
		remapSourceSpan(b, r.Diagnostics[i].Span)
	}
	return r
}

// CompileBundle is Compile over a bundle. A parse or decode error names the
// file and line it came from.
func CompileBundle(ctx context.Context, b Bundle, baseDir string, opts LoadOptions) (*Platform, error) {
	p, err := Compile(ctx, b.Source(), baseDir, opts)
	if err != nil {
		return nil, remapCompileError(b, err)
	}
	return p, nil
}

// ReadBundleDir reads every *.bcl file directly inside dir into a Bundle.
func ReadBundleDir(dir string) (Bundle, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("ref/platform: read BCL dir %q: %w", dir, err)
	}
	var files []BundleFile
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".bcl") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("ref/platform: read BCL file %q: %w", e.Name(), err)
		}
		files = append(files, BundleFile{Path: e.Name(), Content: string(data)})
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("ref/platform: no .bcl files found in %q", dir)
	}
	return NewBundle(files)
}
