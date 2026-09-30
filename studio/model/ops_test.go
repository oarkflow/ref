package model

import (
	"errors"
	"strings"
	"testing"

	"github.com/oarkflow/bcl"
)

func mustOpen(t *testing.T, src string) *File {
	t.Helper()
	f, err := Open("t.bcl", []byte(src))
	if err != nil {
		t.Fatalf("Open: %v\n%s", err, src)
	}
	return f
}

func mustOp(t *testing.T, f *File, err error) *File {
	t.Helper()
	if err != nil {
		t.Fatalf("op failed: %v", err)
	}
	if _, perr := bcl.ParseFile("out", f.Source()); perr != nil {
		t.Fatalf("result does not parse: %v\n%s", perr, f.Text())
	}
	return f
}

func value(t *testing.T, f *File, path string) string {
	t.Helper()
	n, err := f.Lookup(ParsePath(path))
	if err != nil {
		t.Fatalf("Lookup(%s): %v\n%s", path, err, f.Text())
	}
	return n.Value
}

func commentCount(src []byte) int {
	td, err := bcl.ParseFileWithTrivia("x", src)
	if err != nil {
		return -1
	}
	return len(td.Comments)
}

const sample = `# top
route "a" { method GET } # trailing

# about b
route "b" {
  # c1
  path "/b"
  authz { roles ["x"] }
  h <<EOT
text
EOT
}
`

func TestSetField(t *testing.T) {
	tests := []struct {
		name, src, path, val, want string
	}{
		{
			name: "replace scalar keeps neighbours and comments",
			src:  sample, path: "route/b/path", val: `"/bb"`,
			want: strings.Replace(sample, `"/b"`, `"/bb"`, 1),
		},
		{
			name: "replace inside nested object",
			src:  sample, path: "route/b/authz/roles", val: `["y", "z"]`,
			want: strings.Replace(sample, `["x"]`, `["y", "z"]`, 1),
		},
		{
			name: "add to one-line block expands it",
			src:  sample, path: "route/a/path", val: `"/a"`,
			want: strings.Replace(sample, `route "a" { method GET } # trailing`, "route \"a\" {\n  method GET\n  path \"/a\"\n} # trailing", 1),
		},
		{
			name: "add to nested one-line object",
			src:  sample, path: "route/b/authz/mode", val: `"any"`,
			want: strings.Replace(sample, `authz { roles ["x"] }`, "authz {\n    roles [\"x\"]\n    mode \"any\"\n  }", 1),
		},
		{
			name: "add to empty block",
			src:  "a \"x\" {}\n", path: "a/x/k", val: "1",
			want: "a \"x\" {\n  k 1\n}\n",
		},
		{
			name: "add top-level field",
			src:  "name \"n\"\nblock \"b\" {\n}\n", path: "version", val: `"1"`,
			want: "name \"n\"\nblock \"b\" {\n}\nversion \"1\"\n",
		},
		{
			name: "add to file without trailing newline",
			src:  "a \"x\" {\n  k 1\n}", path: "z", val: "2",
			want: "a \"x\" {\n  k 1\n}\nz 2\n",
		},
		{
			name: "env call passes through untouched",
			src:  "r \"x\" {\n  dsn \"old\"\n}\n", path: "r/x/dsn", val: `env("DB_DSN", "file:x.db?a=b")`,
			want: "r \"x\" {\n  dsn env(\"DB_DSN\", \"file:x.db?a=b\")\n}\n",
		},
		{
			name: "duration and list",
			src:  "r \"x\" {\n  t 30m\n  l [1]\n}\n", path: "r/x/t", val: "1h",
			want: "r \"x\" {\n  t 1h\n  l [1]\n}\n",
		},
		{
			name: "replace multi-line list",
			src:  "r \"x\" {\n  l [\n    1,\n    2,\n  ]\n  after 1\n}\n", path: "r/x/l", val: "[9]",
			want: "r \"x\" {\n  l [9]\n  after 1\n}\n",
		},
		{
			name: "eq style is kept",
			src:  "r \"x\" {\n  k = 1\n}\n", path: "r/x/k", val: "2",
			want: "r \"x\" {\n  k = 2\n}\n",
		},
		{
			name: "heredoc value",
			src:  "r \"x\" {\n  k 1\n}\n", path: "r/x/k", val: "<<EOT\nline1\nline2\nEOT",
			want: "r \"x\" {\n  k <<EOT\nline1\nline2\nEOT\n}\n",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := mustOpen(t, tc.src)
			before := f.Text()
			nf, err := f.SetField(ParsePath(tc.path), tc.val)
			nf = mustOp(t, nf, err)
			if nf.Text() != tc.want {
				t.Errorf("got:\n%s\nwant:\n%s", nf.Text(), tc.want)
			}
			if f.Text() != before {
				t.Error("receiver was mutated")
			}
		})
	}
}

func TestSetFieldValueKinds(t *testing.T) {
	src := "a \"x\" {\n  expr_line count > 1 && ok\n  call env(\"A\", \"b\")\n  ref other.value\n  list [1, 2]\n  obj { k 1 j \"v\" }\n  plain \"s\"\n}\n"
	f := mustOpen(t, src)
	for path, want := range map[string]string{
		"a/x/expr_line": "count > 1 && ok",
		"a/x/call":      `env("A", "b")`,
		"a/x/ref":       "other.value",
		"a/x/list":      "[1, 2]",
		"a/x/plain":     `"s"`,
	} {
		if got := value(t, f, path); got != want {
			t.Errorf("%s value = %q, want %q", path, got, want)
		}
	}
	if n, err := f.Lookup(ParsePath("a/x/obj")); err != nil || n.Kind != KindBlock {
		t.Fatalf("obj = %+v, %v", n, err)
	}
	// Replace each kind with each other kind; the untouched fields stay byte-identical.
	values := []string{`count > 2 || flag`, `env("Z")`, `some.ref`, `["a", "b"]`, `"str"`, `45`, `5m`, `true`}
	for _, path := range []string{"a/x/expr_line", "a/x/call", "a/x/ref", "a/x/list", "a/x/plain"} {
		for _, v := range values {
			nf, err := f.SetField(ParsePath(path), v)
			if err != nil {
				// A value that turns the line into an expression is fine to refuse only
				// if the receiver is untouched; verify below.
				t.Fatalf("SetField(%s, %s): %v", path, v, err)
			}
			for _, other := range []string{"a/x/expr_line", "a/x/call", "a/x/ref", "a/x/list", "a/x/plain"} {
				if other == path {
					continue
				}
				if got, want := value(t, nf, other), value(t, f, other); got != want {
					t.Errorf("SetField(%s,%s) disturbed %s: %q != %q", path, v, other, got, want)
				}
			}
			if got := value(t, nf, path); got != v {
				t.Errorf("SetField(%s,%s) stored %q", path, v, got)
			}
			if n, err := nf.Lookup(ParsePath("a/x/obj")); err != nil || n.Kind != KindBlock {
				t.Errorf("obj disturbed by %s=%s: %v", path, v, err)
			}
		}
	}
}

func TestSetFieldRejects(t *testing.T) {
	f := mustOpen(t, sample)
	before := f.Text()
	for name, tc := range map[string]struct{ path, val string }{
		"two statements":     {"route/b/path", "\"x\"\nextra 1"},
		"unbalanced":         {"route/b/path", `}`},
		"open brace":         {"route/b/path", `foo {`},
		"empty":              {"route/b/path", `  `},
		"missing parent":     {"route/zzz/k", `1`},
		"parent is field":    {"route/b/path/k", `1`},
		"block as field":     {"route/b/authz", `1`},
		"bad field name":     {"route/b/has space", `1`},
		"empty path":         {"", `1`},
		"unterminated str":   {"route/b/path", `"abc`},
		"comment eats brace": {"route/b/authz/roles", `1 # c`}, // would swallow the closing brace of the one-liner
	} {
		t.Run(name, func(t *testing.T) {
			nf, err := f.SetField(ParsePath(tc.path), tc.val)
			if err == nil {
				t.Fatalf("expected error, got:\n%s", nf.Text())
			}
			if f.Text() != before {
				t.Error("failed op changed the receiver")
			}
		})
	}
}

func TestRemove(t *testing.T) {
	f := mustOpen(t, sample)
	// field with attached comment
	nf, err := f.RemoveField(ParsePath("route/b/path"))
	nf = mustOp(t, nf, err)
	if strings.Contains(nf.Text(), "c1") || strings.Contains(nf.Text(), `"/b"`) {
		t.Errorf("attached comment or field survived:\n%s", nf.Text())
	}
	if !strings.Contains(nf.Text(), "# about b") || !strings.Contains(nf.Text(), "# trailing") {
		t.Errorf("unrelated comments lost:\n%s", nf.Text())
	}
	// field inside a one-liner
	nf, err = f.RemoveField(ParsePath("route/b/authz/roles"))
	nf = mustOp(t, nf, err)
	if strings.Contains(nf.Text(), "roles") || !strings.Contains(nf.Text(), "authz") {
		t.Errorf("one-line removal wrong:\n%s", nf.Text())
	}
	// block with attached comment
	nf, err = f.RemoveBlock(ParsePath("route/b"))
	nf = mustOp(t, nf, err)
	want := "# top\nroute \"a\" { method GET } # trailing\n"
	if nf.Text() != want {
		t.Errorf("RemoveBlock:\n%q\nwant\n%q", nf.Text(), want)
	}
	// last block, source without trailing newline
	f2 := mustOpen(t, "a \"1\" {\n}\n# bye\nb \"2\" {\n  x 1\n}")
	nf, err = f2.RemoveBlock(ParsePath("b/2"))
	nf = mustOp(t, nf, err)
	if nf.Text() != "a \"1\" {\n}\n" {
		t.Errorf("got %q", nf.Text())
	}
	// wrong kinds and missing
	if _, err := f.RemoveField(ParsePath("route/b")); err == nil {
		t.Error("RemoveField on a block should fail")
	}
	if _, err := f.RemoveBlock(ParsePath("route/b/path")); err == nil {
		t.Error("RemoveBlock on a field should fail")
	}
	if _, err := f.RemoveBlock(ParsePath("route/nope")); !errors.Is(err, ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}

func TestRenameBlock(t *testing.T) {
	f := mustOpen(t, "route \"a\" {\n}\nroute bare {\n}\nwhen x > 1 {\n}\n")
	nf, err := f.RenameBlock(ParsePath("route/a"), "b")
	nf = mustOp(t, nf, err)
	if !strings.HasPrefix(nf.Text(), "route \"b\" {") {
		t.Errorf("quoted id lost its quotes:\n%s", nf.Text())
	}
	nf, err = f.RenameBlock(ParsePath("route/bare"), "plain_id")
	nf = mustOp(t, nf, err)
	if !strings.Contains(nf.Text(), "route plain_id {") {
		t.Errorf("bare id was not kept bare:\n%s", nf.Text())
	}
	nf, err = f.RenameBlock(ParsePath("route/bare"), "dotted.id")
	nf = mustOp(t, nf, err)
	if !strings.Contains(nf.Text(), `route "dotted.id" {`) {
		t.Errorf("dotted id must be quoted (bare form does not parse back):\n%s", nf.Text())
	}
	nf, err = f.RenameBlock(ParsePath("route/bare"), "with-dash")
	nf = mustOp(t, nf, err)
	if !strings.Contains(nf.Text(), `route "with-dash" {`) {
		t.Errorf("id needing quotes was not quoted:\n%s", nf.Text())
	}
	nf, err = f.RenameBlock(ParsePath("route/a"), `we"ird`)
	nf = mustOp(t, nf, err)
	if _, err := nf.Lookup(Path{"route", `we"ird`}); err != nil {
		t.Errorf("escaped id not addressable: %v", err)
	}
	if _, err := f.RenameBlock(ParsePath("route/a"), ""); err == nil {
		t.Error("empty id accepted")
	}
	if _, err := f.RenameBlock(ParsePath("route/a"), "x\ny"); err == nil {
		t.Error("multi-line id accepted")
	}
	if b := f.Blocks(); len(b) != 3 || !b[2].Opaque {
		t.Fatalf("blocks: %+v", b)
	}
}

func TestAddBlock(t *testing.T) {
	f := mustOpen(t, sample)
	nf, err := f.AddBlock(nil, "worker", "w1", "queue \"jobs\"\nintent \"x\"")
	nf = mustOp(t, nf, err)
	if !strings.HasSuffix(nf.Text(), "EOT\n}\n\nworker \"w1\" {\n  queue \"jobs\"\n  intent \"x\"\n}\n") {
		t.Errorf("top-level add:\n%s", nf.Text())
	}
	nf, err = f.AddBlock(ParsePath("route/b"), "cors", "", "origins [\"*\"]")
	nf = mustOp(t, nf, err)
	if !strings.Contains(nf.Text(), "  cors {\n    origins [\"*\"]\n  }\n}") {
		t.Errorf("nested add:\n%s", nf.Text())
	}
	nf, err = f.AddBlock(ParsePath("route/a"), "node", "n1", "")
	nf = mustOp(t, nf, err)
	n, err := nf.Lookup(ParsePath("route/a/node/n1"))
	if err != nil || n.Kind != KindBlock {
		t.Errorf("empty-body add: %+v %v", n, err)
	}
	nf, err = f.AddBlock(nil, "note", "with <<EOT", "text <<EOT\nhello\nEOT")
	nf = mustOp(t, nf, err)
	if got := value(t, nf, `note/with <<EOT/text`); !strings.HasPrefix(got, "<<EOT") {
		t.Errorf("heredoc body: %q", got)
	}
	// into a file without a final newline and into an empty file
	nf, err = mustOpen(t, "a \"x\" {\n}").AddBlock(nil, "b", "y", "")
	nf = mustOp(t, nf, err)
	if nf.Text() != "a \"x\" {\n}\n\nb \"y\" {\n}\n" {
		t.Errorf("no-newline source: %q", nf.Text())
	}
	nf, err = mustOpen(t, "").AddBlock(nil, "b", "y", "k 1")
	nf = mustOp(t, nf, err)
	if nf.Text() != "b \"y\" {\n  k 1\n}\n" {
		t.Errorf("empty source: %q", nf.Text())
	}
	for name, tc := range map[string]struct{ parent, typ, id, body string }{
		"closing brace in body": {"", "x", "y", "}\nz {"},
		"bad type":              {"", "has space", "y", ""},
		"multiline id":          {"", "x", "a\nb", ""},
		"parent is a field":     {"route/b/path", "x", "y", ""},
		"missing parent":        {"route/nope", "x", "y", ""},
	} {
		if _, err := f.AddBlock(ParsePath(tc.parent), tc.typ, tc.id, tc.body); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestMoveBlock(t *testing.T) {
	f := mustOpen(t, sample+"node \"n\" {\n}\n")
	order := func(f *File) string {
		var ids []string
		for _, b := range f.Blocks() {
			ids = append(ids, b.Head+"/"+b.ID)
		}
		return strings.Join(ids, ",")
	}
	if got := order(f); got != "route/a,route/b,node/n" {
		t.Fatal(got)
	}
	nf, err := f.MoveBlock(ParsePath("route/b"), 0)
	nf = mustOp(t, nf, err)
	if got := order(nf); got != "route/b,route/a,node/n" {
		t.Errorf("order %s\n%s", got, nf.Text())
	}
	if !strings.HasPrefix(nf.Text(), "# about b\nroute \"b\" {") || !strings.Contains(nf.Text(), "}\n\n# top\nroute \"a\"") {
		t.Errorf("comments or blank lines did not travel with the block:\n%s", nf.Text())
	}
	nf, err = f.MoveBlock(ParsePath("route/a"), 3)
	nf = mustOp(t, nf, err)
	if got := order(nf); got != "route/b,node/n,route/a" {
		t.Errorf("order %s\n%s", got, nf.Text())
	}
	if !strings.HasSuffix(nf.Text(), "# top\nroute \"a\" { method GET } # trailing\n") {
		t.Errorf("moved block glued to previous line:\n%s", nf.Text())
	}
	// no-ops
	for _, idx := range []int{0, 1} {
		nf, err = f.MoveBlock(ParsePath("route/a"), idx)
		if err != nil || !nf.Equal(f) {
			t.Errorf("index %d should be a no-op (err %v)", idx, err)
		}
	}
	// moving inside a block, and into a file without trailing newline
	g := mustOpen(t, "r \"x\" {\n  a 1\n  b 2\n  c 3\n}")
	ng, err := g.MoveBlock(ParsePath("r/x/c"), 0)
	ng = mustOp(t, ng, err)
	if ng.Text() != "r \"x\" {\n  c 3\n  a 1\n  b 2\n}\n" && ng.Text() != "r \"x\" {\n  c 3\n  a 1\n  b 2\n}" {
		t.Errorf("field move:\n%q", ng.Text())
	}
	if _, err := f.MoveBlock(ParsePath("route/a"), 9); err == nil {
		t.Error("out of range accepted")
	}
	if _, err := f.MoveBlock(ParsePath("route/a"), -1); err == nil {
		t.Error("negative index accepted")
	}
	h := mustOpen(t, "a \"1\" { x 1 } b \"2\" { y 2 }\n")
	if _, err := h.MoveBlock(ParsePath("b/2"), 0); err == nil {
		t.Error("moving a block that shares a line should fail")
	}
}

func TestDuplicateIDs(t *testing.T) {
	f := mustOpen(t, "node \"n\" {\n  v 1\n}\nnode \"n\" {\n  v 2\n}\nparam {\n  q 1\n}\nparam {\n  q 2\n}\n")
	if _, err := f.Lookup(ParsePath("node/n")); !errors.Is(err, ErrAmbiguous) {
		t.Errorf("want ErrAmbiguous, got %v", err)
	}
	if _, err := f.SetField(ParsePath("node/n/v"), "9"); !errors.Is(err, ErrAmbiguous) {
		t.Errorf("want ErrAmbiguous, got %v", err)
	}
	if got := value(t, f, "node/n[1]/v"); got != "2" {
		t.Errorf("node/n[1]/v = %q", got)
	}
	if got := value(t, f, "param[1]/q"); got != "2" {
		t.Errorf("param[1]/q = %q", got)
	}
	nf, err := f.SetField(ParsePath("node/n[0]/v"), "10")
	nf = mustOp(t, nf, err)
	if value(t, nf, "node/n[0]/v") != "10" || value(t, nf, "node/n[1]/v") != "2" {
		t.Errorf("wrong duplicate edited:\n%s", nf.Text())
	}
	for _, b := range f.Blocks() {
		if _, err := f.Lookup(b.Path); err != nil {
			t.Errorf("canonical path %s does not resolve: %v", b.Path, err)
		}
	}
	nf, err = f.RemoveBlock(ParsePath("node/n[0]"))
	nf = mustOp(t, nf, err)
	if value(t, nf, "node/n/v") != "2" {
		t.Errorf("wrong duplicate removed:\n%s", nf.Text())
	}
	nf, err = f.RenameBlock(ParsePath("node/n[1]"), "m")
	nf = mustOp(t, nf, err)
	if value(t, nf, "node/m/v") != "2" || value(t, nf, "node/n/v") != "1" {
		t.Errorf("rename hit the wrong duplicate:\n%s", nf.Text())
	}
}

func TestLayouts(t *testing.T) {
	t.Run("CRLF stays CRLF", func(t *testing.T) {
		src := strings.ReplaceAll(sample, "\n", "\r\n")
		f := mustOpen(t, src)
		steps := []func(*File) (*File, error){
			func(f *File) (*File, error) { return f.SetField(ParsePath("route/a/path"), `"/a"`) },
			func(f *File) (*File, error) { return f.SetField(ParsePath("route/b/path"), `"/z"`) },
			func(f *File) (*File, error) { return f.AddBlock(nil, "worker", "w", "q 1") },
			func(f *File) (*File, error) { return f.AddBlock(ParsePath("route/b"), "cors", "", "o 1") },
			func(f *File) (*File, error) { return f.RemoveField(ParsePath("route/b/authz/roles")) },
			func(f *File) (*File, error) { return f.MoveBlock(ParsePath("worker/w"), 0) },
			func(f *File) (*File, error) { return f.RenameBlock(ParsePath("route/b"), "bb") },
		}
		for i, step := range steps {
			var err error
			f, err = step(f)
			f = mustOp(t, f, err)
			if bare := strings.Count(f.Text(), "\n") - strings.Count(f.Text(), "\r\n"); bare != 0 {
				t.Fatalf("step %d left %d bare LF:\n%q", i, bare, f.Text())
			}
		}
		if got := value(t, f, "route/bb/h"); !strings.HasPrefix(got, "<<EOT\r\ntext\r\nEOT") {
			t.Errorf("heredoc mangled: %q", got)
		}
	})
	t.Run("tabs", func(t *testing.T) {
		f := mustOpen(t, "route \"a\" {\n\tmethod GET\n}\n")
		if !f.Format().UseTabs {
			t.Fatal("tabs not detected")
		}
		nf, err := f.SetField(ParsePath("route/a/path"), `"/a"`)
		nf = mustOp(t, nf, err)
		if nf.Text() != "route \"a\" {\n\tmethod GET\n\tpath \"/a\"\n}\n" {
			t.Errorf("got %q", nf.Text())
		}
	})
	t.Run("four spaces", func(t *testing.T) {
		f := mustOpen(t, "route \"a\" {\n    method GET\n}\n")
		nf, err := f.SetField(ParsePath("route/a/path"), `"/a"`)
		nf = mustOp(t, nf, err)
		if nf.Text() != "route \"a\" {\n    method GET\n    path \"/a\"\n}\n" {
			t.Errorf("got %q", nf.Text())
		}
	})
	t.Run("comments before inside after the target", func(t *testing.T) {
		src := "# before\nr \"x\" {\n  # inside 1\n  a 1 # eol\n  # inside 2\n  b 2\n  # last\n} # after\n# tail\n"
		f := mustOpen(t, src)
		nf, err := f.SetField(ParsePath("r/x/a"), "5")
		nf = mustOp(t, nf, err)
		nf, err = nf.SetField(ParsePath("r/x/c"), "6")
		nf = mustOp(t, nf, err)
		nf, err = nf.RenameBlock(ParsePath("r/x"), "y")
		nf = mustOp(t, nf, err)
		want := "# before\nr \"y\" {\n  # inside 1\n  a 5 # eol\n  # inside 2\n  b 2\n  # last\n  c 6\n} # after\n# tail\n"
		if nf.Text() != want {
			t.Errorf("got:\n%s\nwant:\n%s", nf.Text(), want)
		}
	})
	t.Run("trailing block without newline", func(t *testing.T) {
		f := mustOpen(t, "a \"x\" {\n  k 1\n}")
		nf, err := f.SetField(ParsePath("a/x/k"), "2")
		nf = mustOp(t, nf, err)
		if !strings.Contains(nf.Text(), "k 2") {
			t.Errorf("got %q", nf.Text())
		}
	})
	t.Run("empty file", func(t *testing.T) {
		f := mustOpen(t, "")
		if len(f.Blocks()) != 0 {
			t.Error("blocks in empty file")
		}
		nf, err := f.SetField(ParsePath("name"), `"x"`)
		nf = mustOp(t, nf, err)
		if nf.Text() != "name \"x\"\n" {
			t.Errorf("got %q", nf.Text())
		}
	})
	t.Run("dotted, spread, schema and const statements", func(t *testing.T) {
		src := "const K = 5\nschema S {\n  required name string\n}\n&base {\n  x 1\n}\nrouter \"r\" {\n  cfg.timeout 5s\n  cfg.name \"x\"\n}\n"
		f := mustOpen(t, src)
		nf, err := f.SetField(ParsePath("router/r/cfg.timeout"), "9s")
		nf = mustOp(t, nf, err)
		if !strings.Contains(nf.Text(), "cfg.timeout 9s") || !strings.Contains(nf.Text(), "required name string") {
			t.Errorf("got:\n%s", nf.Text())
		}
		if _, err := f.SetField(ParsePath("schema/S/x"), "1"); err == nil {
			t.Error("schema body should not be editable")
		}
		nf, err = f.SetField(ParsePath("&base/x"), "2")
		nf = mustOp(t, nf, err)
		if value(t, nf, "&base/x") != "2" {
			t.Errorf("spread body edit failed:\n%s", nf.Text())
		}
	})
	t.Run("statements sharing a line", func(t *testing.T) {
		f := mustOpen(t, "payload { email env(\"E\", \"a@b\") name \"Ops\" }\n")
		if got := value(t, f, "payload/name"); got != `"Ops"` {
			t.Fatalf("name = %q", got)
		}
		nf, err := f.SetField(ParsePath("payload/name"), `"X"`)
		nf = mustOp(t, nf, err)
		if value(t, nf, "payload/name") != `"X"` || value(t, nf, "payload/email") != `env("E", "a@b")` {
			t.Errorf("got:\n%s", nf.Text())
		}
		nf, err = f.RemoveField(ParsePath("payload/email"))
		nf = mustOp(t, nf, err)
		if strings.Contains(nf.Text(), "email") || value(t, nf, "payload/name") != `"Ops"` {
			t.Errorf("got:\n%s", nf.Text())
		}
	})
}

func TestUnsupportedSourceIsRefused(t *testing.T) {
	for _, src := range []string{"a {", "a \"x\" { k 1 ", "x = ", "a \"x\" }\n"} {
		if _, err := Open("bad", []byte(src)); err == nil {
			t.Errorf("Open(%q) succeeded", src)
		}
	}
}

func TestNodeInfo(t *testing.T) {
	f := mustOpen(t, sample)
	n, err := f.Lookup(ParsePath("route/b"))
	if err != nil {
		t.Fatal(err)
	}
	if n.Kind != KindBlock || n.Head != "route" || n.ID != "b" || n.Line != 5 || n.Doc != "about b" || n.Index != 1 {
		t.Errorf("%+v", n)
	}
	kids, err := f.Statements(ParsePath("route/b"))
	if err != nil || len(kids) != 3 {
		t.Fatalf("%v %v", kids, err)
	}
	if kids[0].Doc != "c1" || kids[0].Value != `"/b"` || kids[0].Path.String() != "route/b/path" {
		t.Errorf("%+v", kids[0])
	}
	top, err := f.Statements(nil)
	if err != nil || len(top) != 2 {
		t.Errorf("%v %v", top, err)
	}
	if _, err := f.Statements(ParsePath("route/b/path")); err == nil {
		t.Error("Statements on a field should fail")
	}
	if _, err := f.FindBlock("route", "a"); err != nil {
		t.Error(err)
	}
}

func TestPathParseString(t *testing.T) {
	p := Path{"route", "pages/todos", "x"}
	if s := p.String(); s != `route/pages\/todos/x` {
		t.Errorf("String = %s", s)
	}
	if got := ParsePath(p.String()); len(got) != 3 || got[1] != "pages/todos" {
		t.Errorf("ParsePath = %v", got)
	}
	if ParsePath("") != nil {
		t.Error("empty path should be nil")
	}
	f := mustOpen(t, "route \"pages/todos\" {\n  k 1\n}\n")
	if value(t, f, `route/pages\/todos/k`) != "1" {
		t.Error("escaped slash id not resolvable")
	}
}

func TestQuote(t *testing.T) {
	for _, s := range []string{"plain", `say "hi"`, `back\slash`, "line1\nline2", "tab\there", "unicode ✓ é", "", "single 'q'"} {
		q, err := Quote(s)
		if err != nil {
			t.Fatalf("Quote(%q): %v", s, err)
		}
		doc, err := bcl.Parse([]byte("k " + q))
		if err != nil {
			t.Fatalf("Quote(%q) = %s does not parse: %v", s, q, err)
		}
		lit := doc.Items[0].(*bcl.Assignment).Value.(*bcl.Literal)
		if lit.Data != s {
			t.Errorf("Quote(%q) = %s decodes to %q", s, q, lit.Data)
		}
	}
	q, err := Quote("a\rb")
	if err != nil || q != "`a\rb`" {
		t.Errorf("CR string: %q %v", q, err)
	}
	if _, err := Quote("a\r`b"); err == nil {
		t.Error("unrepresentable string accepted")
	}
	if l, err := QuoteList("a", `b"c`); err != nil || l != `["a", "b\"c"]` {
		t.Errorf("QuoteList = %s %v", l, err)
	}
}

func TestHistory(t *testing.T) {
	f := mustOpen(t, sample)
	h := NewHistory(f)
	if h.CanUndo() || h.CanRedo() {
		t.Fatal("fresh history has steps")
	}
	set := func(path, v string) func(*File) (*File, error) {
		return func(f *File) (*File, error) { return f.SetField(ParsePath(path), v) }
	}
	states := []string{f.Text()}
	for _, v := range []string{`"/1"`, `"/2"`, `"/3"`} {
		if err := h.Apply(set("route/b/path", v)); err != nil {
			t.Fatal(err)
		}
		states = append(states, h.Current().Text())
	}
	if err := h.Apply(set("route/b/nope/deeper", "1")); err == nil {
		t.Fatal("bad op accepted")
	}
	if h.Len() != 3 || h.Current().Text() != states[3] {
		t.Fatal("failed op changed history")
	}
	if err := h.Apply(set("route/b/path", `"/3"`)); err != nil || h.Len() != 3 {
		t.Errorf("no-op made a step (len %d, err %v)", h.Len(), err)
	}
	for i := 2; i >= 0; i-- {
		if err := h.Undo(); err != nil {
			t.Fatal(err)
		}
		if h.Current().Text() != states[i] {
			t.Errorf("undo to state %d not byte-exact", i)
		}
	}
	if h.Undo() == nil {
		t.Error("undo past the start succeeded")
	}
	for i := 1; i <= 3; i++ {
		if err := h.Redo(); err != nil || h.Current().Text() != states[i] {
			t.Fatalf("redo %d: %v", i, err)
		}
	}
	if h.Redo() == nil {
		t.Error("redo past the end succeeded")
	}
	_ = h.Undo()
	if err := h.Apply(set("route/b/path", `"/new"`)); err != nil {
		t.Fatal(err)
	}
	if h.CanRedo() {
		t.Error("redo stack survived a new edit")
	}
}
