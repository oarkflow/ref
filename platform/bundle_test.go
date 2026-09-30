package platform

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewBundleValidatesAndSorts(t *testing.T) {
	ok := func(paths ...string) []BundleFile {
		var fs []BundleFile
		for _, p := range paths {
			fs = append(fs, BundleFile{Path: p, Content: "name \"x\"\n"})
		}
		return fs
	}
	b, err := NewBundle(ok("10_b.bcl", "00_a.bcl", "05_sub.bcl"))
	if err != nil {
		t.Fatal(err)
	}
	if got := []string{b[0].Path, b[1].Path, b[2].Path}; got[0] != "00_a.bcl" || got[1] != "05_sub.bcl" || got[2] != "10_b.bcl" {
		t.Fatalf("not sorted: %v", got)
	}
	for name, files := range map[string][]BundleFile{
		"empty":         nil,
		"empty path":    ok(""),
		"absolute":      ok("/etc/a.bcl"),
		"dotdot":        ok("../a.bcl"),
		"dotdot middle": ok("a/../b.bcl"),
		"dot":           ok("./a.bcl"),
		"double slash":  ok("a//b.bcl"),
		"backslash":     ok(`a\b.bcl`),
		"not bcl":       ok("a.txt"),
		"no name":       ok("dir/.bcl"),
		"control char":  ok("a\x00.bcl"),
		"trailing":      ok("a/"),
		"subdirectory":  ok("sub/05.bcl"),
		"duplicate":     ok("a.bcl", "a.bcl"),
		"invalid utf8":  ok("\xff.bcl"),
	} {
		if _, err := NewBundle(files); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	if _, err := NewBundle([]BundleFile{{Path: "a.bcl", Content: strings.Repeat("x", MaxBundleFileBytes+1)}}); err == nil {
		t.Error("oversized file accepted")
	}
	// The input slice is left alone.
	in := ok("z.bcl", "a.bcl")
	if _, err := NewBundle(in); err != nil || in[0].Path != "z.bcl" {
		t.Fatalf("input modified: %v %v", in, err)
	}
}

func TestBundleSourceMatchesLoadDir(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"00_app.bcl":    "name \"bundled\"\nversion \"1\"\n",
		"10_intent.bcl": "intent \"hello\" {\n  response \"msg\"\n  node \"msg\" {\n    uses \"constant\"\n    provides [msg]\n    config { value \"hi\" }\n  }\n}", // no trailing newline
		"20_route.bcl":  "route \"hello\" {\n  method GET\n  path \"/hello\"\n  intent \"hello\"\n}\n\n",
	}
	for n, c := range files {
		if err := os.WriteFile(filepath.Join(dir, n), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	b, err := ReadBundleDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := files["00_app.bcl"] + "\n" + files["10_intent.bcl"] + "\n" + files["20_route.bcl"] + "\n"
	if string(b.Source()) != want {
		t.Fatalf("Source:\n%q\nwant\n%q", b.Source(), want)
	}
	// LoadDir and CompileBundle read the same document.
	ctx := context.Background()
	p1, err := LoadDir(ctx, dir, DefaultLoadOptions())
	if err != nil {
		t.Fatal(err)
	}
	defer p1.Close()
	p2, err := CompileBundle(ctx, b, dir, DefaultLoadOptions())
	if err != nil {
		t.Fatal(err)
	}
	defer p2.Close()
	r1 := Validate(ctx, b.Source(), dir, DefaultLoadOptions())
	r2 := ValidateBundle(ctx, b, dir, DefaultLoadOptions())
	if !r1.Valid || !r2.Valid || strings.Join(r1.Summary.Routes, ",") != strings.Join(r2.Summary.Routes, ",") {
		t.Fatalf("reports differ: %+v / %+v", r1, r2)
	}
	if _, err := ReadBundleDir(t.TempDir()); err == nil {
		t.Fatal("an empty directory is not a bundle")
	}
}

func TestBundleLocate(t *testing.T) {
	b, err := NewBundle([]BundleFile{
		{Path: "a.bcl", Content: "l1\nl2"}, // joined: l1, l2, then the separator ends line 2
		{Path: "b.bcl", Content: ""},       // one (empty) line
		{Path: "c.bcl", Content: "x\ny\n"}, // x, y, "" (the file's own trailing newline yields an empty line)
		{Path: "d.bcl", Content: "last"},
	})
	if err != nil {
		t.Fatal(err)
	}
	src := string(b.Source())
	lines := strings.Split(src, "\n") // the final element is the empty string after the last "\n"
	type want struct {
		file string
		line int
	}
	for joined, w := range map[int]want{
		1: {"a.bcl", 1}, 2: {"a.bcl", 2}, 3: {"b.bcl", 1},
		4: {"c.bcl", 1}, 5: {"c.bcl", 2}, 6: {"c.bcl", 3},
		7: {"d.bcl", 1},
	} {
		f, l, ok := b.Locate(joined)
		if !ok || f != w.file || l != w.line {
			t.Errorf("Locate(%d) = %s:%d %v; want %s:%d", joined, f, l, ok, w.file, w.line)
		}
	}
	if _, _, ok := b.Locate(8); ok {
		t.Error("Locate past the end")
	}
	if _, _, ok := b.Locate(0); ok {
		t.Error("Locate(0)")
	}
	// The mapping agrees with the text: the located file line has the same content.
	byPath := map[string][]string{}
	for _, f := range b {
		byPath[f.Path] = strings.Split(f.Content, "\n")
	}
	for i := 1; i <= 7; i++ {
		f, l, _ := b.Locate(i)
		if byPath[f][l-1] != lines[i-1] {
			t.Errorf("line %d: %q vs %s:%d %q", i, lines[i-1], f, l, byPath[f][l-1])
		}
	}
	// Offsets.
	off := strings.Index(src, "last")
	if f, o, ok := b.LocateOffset(off); !ok || f != "d.bcl" || o != 0 {
		t.Errorf("LocateOffset(last) = %s %d %v", f, o, ok)
	}
	if f, o, ok := b.LocateOffset(strings.Index(src, "l2")); !ok || f != "a.bcl" || o != 3 {
		t.Errorf("LocateOffset(l2) = %s %d %v", f, o, ok)
	}
	if _, _, ok := b.LocateOffset(len(src)); ok {
		t.Error("LocateOffset past the end")
	}
}

func TestValidateBundleNamesTheFile(t *testing.T) {
	ctx := context.Background()
	app := "name \"x\"\nversion \"1\"\n\nintent \"hello\" {\n  response \"msg\"\n  node \"msg\" {\n    uses \"constant\"\n    provides [msg]\n    config { value \"hi\" }\n  }\n}\n"
	// The route points at an intent that does not exist; its intent field is on line 7 of the joined routes file.
	routes := "# routes\n\n\nroute \"broken\" {\n  method GET\n  path \"/b\"\n  intent \"missing\"\n}\n"
	b, err := NewBundle([]BundleFile{{Path: "00_app.bcl", Content: app}, {Path: "10_routes.bcl", Content: routes}})
	if err != nil {
		t.Fatal(err)
	}
	r := ValidateBundle(ctx, b, ".", DefaultLoadOptions())
	if r.Valid {
		t.Fatal("expected a validation error")
	}
	var found bool
	for _, d := range r.Diagnostics {
		if d.Span == nil || !strings.Contains(d.Message, "broken") {
			continue
		}
		found = true
		// The path is refined to the offending field: `intent` on line 7.
		if d.Span.File != "10_routes.bcl" || d.Span.Line != 7 {
			t.Errorf("diagnostic span = %+v; want 10_routes.bcl:7", *d.Span)
		}
		if got := b[1].Content[d.Span.Offset:]; !strings.HasPrefix(got, "intent \"missing\"") {
			t.Errorf("offset %d does not point at the field: %q", d.Span.Offset, got)
		}
	}
	if !found {
		t.Fatalf("no located diagnostic for the route: %+v", r.Diagnostics)
	}
	// Plain Validate is unchanged: no file name, line in the joined document.
	plain := Validate(ctx, b.Source(), ".", DefaultLoadOptions())
	for _, d := range plain.Diagnostics {
		if d.Span != nil && d.Span.File != "" {
			t.Errorf("Validate should not name files: %+v", *d.Span)
		}
	}
}

func TestBundleParseErrorsNameTheFile(t *testing.T) {
	ctx := context.Background()
	good := "name \"x\"\nversion \"1\"\n"
	bad := "# comment\nroute \"r\" {\n  method GET\n" // unterminated block
	b, err := NewBundle([]BundleFile{{Path: "a.bcl", Content: good}, {Path: "b.bcl", Content: bad}})
	if err != nil {
		t.Fatal(err)
	}
	r := ValidateBundle(ctx, b, ".", DefaultLoadOptions())
	if r.Valid || len(r.Diagnostics) == 0 {
		t.Fatalf("expected parse diagnostics: %+v", r)
	}
	for _, d := range r.Diagnostics {
		if d.Span != nil && d.Span.File != "b.bcl" {
			t.Errorf("parse diagnostic in %q: %+v", d.Span.File, *d.Span)
		}
	}
	_, err = CompileBundle(ctx, b, ".", DefaultLoadOptions())
	if err == nil || !strings.Contains(err.Error(), "b.bcl") {
		t.Fatalf("compile error should name b.bcl: %v", err)
	}
}
