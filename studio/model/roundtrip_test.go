package model

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/oarkflow/bcl"
)

const examplesDir = "../../examples/starter/resources/config"

type example struct {
	name string
	src  []byte
}

func loadExamples(t *testing.T) []example {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(examplesDir, "*.bcl"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no example configs under %s (%v)", examplesDir, err)
	}
	var out []example
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, example{filepath.Base(p), b})
	}
	return out
}

// walk visits every statement under level p (nil = top level), depth first.
func walk(t *testing.T, f *File, p Path, fn func(Node)) {
	t.Helper()
	nodes, err := f.Statements(p)
	if err != nil {
		t.Fatalf("Statements(%s): %v", p, err)
	}
	for _, n := range nodes {
		fn(n)
		if n.Kind == KindBlock {
			walk2(t, f, n.Path, fn)
		}
	}
}

func walk2(t *testing.T, f *File, p Path, fn func(Node)) {
	t.Helper()
	// schema bodies are opaque: nothing to visit
	if s, err := resolve(f.root, p); err == nil && s.opaqueBody {
		return
	}
	walk(t, f, p, fn)
}

func commentTexts(src []byte) map[string]int {
	td, err := bcl.ParseFileWithTrivia("x", src)
	if err != nil {
		return nil
	}
	m := map[string]int{}
	for _, c := range td.Comments {
		m[strings.TrimSpace(c.Text)]++
	}
	return m
}

func lostComments(orig, cur map[string]int) []string {
	var lost []string
	for text, n := range orig {
		if cur[text] < n {
			lost = append(lost, text)
		}
	}
	return lost
}

// (a) Opening and re-formatting never loses a comment and reaches a fixed point.
func TestExamplesReformat(t *testing.T) {
	for _, ex := range loadExamples(t) {
		t.Run(ex.name, func(t *testing.T) {
			f, err := Open(ex.name, ex.src)
			if err != nil {
				t.Fatal(err)
			}
			if !f.Equal(mustOpen(t, string(ex.src))) || string(f.Source()) != string(ex.src) {
				t.Fatal("Open must keep the source byte for byte")
			}
			g, err := f.Reformat()
			if err != nil {
				t.Fatal(err)
			}
			if a, b := commentCount(ex.src), commentCount(g.Source()); a != b {
				t.Errorf("comments %d -> %d after Reformat", a, b)
			}
			if lost := lostComments(commentTexts(ex.src), commentTexts(g.Source())); len(lost) > 0 {
				t.Errorf("comments changed by Reformat: %q", lost)
			}
			g2, err := g.Reformat()
			if err != nil {
				t.Fatal(err)
			}
			if !g2.Equal(g) {
				t.Error("Reformat is not idempotent")
			}
			if len(f.Blocks()) != len(g.Blocks()) {
				t.Error("Reformat changed the block count")
			}
		})
	}
}

// (b) On every field of every example: an edit lands, its undo restores the
// exact bytes, and setting the original value back reproduces the formatted
// original (the splice is a true inverse, not just a syntactically valid edit).
func TestExamplesSetFieldUndoAndInverse(t *testing.T) {
	total := 0
	for _, ex := range loadExamples(t) {
		t.Run(ex.name, func(t *testing.T) {
			f, err := Open(ex.name, ex.src)
			if err != nil {
				t.Fatal(err)
			}
			f0, err := f.Reformat()
			if err != nil {
				t.Fatal(err)
			}
			h := NewHistory(f)
			seen := 0
			walk(t, f, nil, func(n Node) {
				if n.Kind != KindField {
					return
				}
				// Every field is checked in a full run; the default run samples
				// about a hundred per file to keep the suite fast.
				seen++
				if os.Getenv("MODEL_FULL") == "" && seen%fieldStride(f) != 0 {
					return
				}
				total++
				if err := h.Apply(func(f *File) (*File, error) { return f.SetField(n.Path, `"__studio__"`) }); err != nil {
					t.Errorf("SetField(%s): %v", n.Path, err)
					return
				}
				got, err := h.Current().Lookup(n.Path)
				if err != nil || got.Value != `"__studio__"` {
					t.Errorf("edit of %s did not land: %+v %v", n.Path, got, err)
				}
				// inverse on the formatted original
				e1, err := f0.SetField(n.Path, `"__studio__"`)
				if err != nil {
					t.Errorf("SetField on formatted(%s): %v", n.Path, err)
				} else {
					orig, _ := f0.Lookup(n.Path)
					e2, err := e1.SetField(n.Path, orig.Value)
					if err != nil || !e2.Equal(f0) {
						t.Errorf("setting %s back did not reproduce the original (err %v)", n.Path, err)
					}
				}
				if err := h.Undo(); err != nil {
					t.Fatal(err)
				}
				if string(h.Current().Source()) != string(ex.src) {
					t.Errorf("undo of %s did not restore the original bytes", n.Path)
				}
			})
		})
	}
	t.Logf("checked %d fields", total)
}

// fieldStride picks the sampling stride so that ~100 fields per file are checked.
func fieldStride(f *File) int {
	n := 0
	for _, s := range f.root {
		n += countStmts(s)
	}
	if stride := n / 100; stride > 1 {
		return stride
	}
	return 1
}

func countStmts(s *stmt) int {
	n := 1
	for _, c := range s.children {
		n += countStmts(c)
	}
	return n
}

// Adding a field to every block is undoable, lands as the last child, and
// touches no other top-level statement (after formatting the original once).
func TestExamplesEditsAreLocal(t *testing.T) {
	for _, ex := range loadExamples(t) {
		t.Run(ex.name, func(t *testing.T) {
			f, err := Open(ex.name, ex.src)
			if err != nil {
				t.Fatal(err)
			}
			f0, err := f.Reformat()
			if err != nil {
				t.Fatal(err)
			}
			h := NewHistory(f)
			walk(t, f0, nil, func(n Node) {
				if n.Kind != KindBlock {
					return
				}
				if s, _ := resolve(f0.root, n.Path); s == nil || s.opaqueBody {
					return
				}
				probe := n.Path.Child("zz_probe")
				if err := h.Apply(func(*File) (*File, error) { return f0.SetField(probe, "1") }); err != nil {
					t.Errorf("add to %s: %v", n.Path, err)
					return
				}
				cur := h.Current()
				kids, _ := cur.Statements(n.Path)
				if len(kids) == 0 || kids[len(kids)-1].Head != "zz_probe" {
					t.Errorf("probe is not the last child of %s", n.Path)
				}
				// Top-level statements other than the one containing n are byte-identical.
				top := n.Path[:1]
				if len(n.Path) > 1 {
					if s, _ := resolve(f0.root, n.Path); s != nil {
						top = rootOf(f0, n.Path)
					}
				}
				assertOthersUnchanged(t, f0, cur, top)
				if err := h.Undo(); err != nil {
					t.Fatal(err)
				}
			})
			if string(h.Current().Source()) != string(ex.src) {
				t.Error("undo did not restore the original")
			}
		})
	}
}

// rootOf returns the path of the top-level statement containing p.
func rootOf(f *File, p Path) Path {
	for _, s := range f.root {
		if len(p) >= len(s.path) && pathEq(p[:len(s.path)], s.path) {
			return s.path
		}
	}
	return nil
}

func assertOthersUnchanged(t *testing.T, before, after *File, target Path) {
	t.Helper()
	a, _ := before.Statements(nil)
	b, _ := after.Statements(nil)
	if len(a) != len(b) {
		t.Fatalf("top-level count %d -> %d", len(a), len(b))
	}
	for i := range a {
		if pathEq(a[i].Path, target) {
			continue
		}
		ta, tb := before.Text()[a[i].Start:a[i].End], after.Text()[b[i].Start:b[i].End]
		if ta != tb {
			t.Fatalf("editing %s changed unrelated statement %s:\n%s\n---\n%s", target, a[i].Path, ta, tb)
		}
	}
}

// (c) Random edit sequences never lose a comment, every state parses, and
// undoing everything returns the exact original bytes; redo returns the final.
func TestExamplesRandomSequences(t *testing.T) {
	for i, ex := range loadExamples(t) {
		for seed := int64(1); seed <= 3; seed++ {
			t.Run(fmt.Sprintf("%s/seed%d", ex.name, seed), func(t *testing.T) {
				rng := rand.New(rand.NewSource(seed*1000 + int64(i)))
				f, err := Open(ex.name, ex.src)
				if err != nil {
					t.Fatal(err)
				}
				origComments := commentTexts(ex.src)
				h := NewHistory(f)
				applied, refused := 0, 0
				for step := 0; step < 40; step++ {
					cur := h.Current()
					var all []Node
					walk(t, cur, nil, func(n Node) { all = append(all, n) })
					var blocks, fields, markers []Node
					for _, n := range all {
						switch {
						case strings.HasPrefix(n.Head, "zz"):
							markers = append(markers, n)
						case n.Kind == KindBlock:
							blocks = append(blocks, n)
						default:
							fields = append(fields, n)
						}
					}
					var op func(*File) (*File, error)
					name := ""
					switch rng.Intn(6) {
					case 0:
						if len(fields) == 0 {
							continue
						}
						n := fields[rng.Intn(len(fields))]
						v := []string{`"v"`, `42`, `[1, 2]`, `env("X", "y")`, `5m`, `true`}[rng.Intn(6)]
						name, op = "set "+n.Path.String(), func(f *File) (*File, error) { return f.SetField(n.Path, v) }
					case 1:
						if len(blocks) == 0 {
							continue
						}
						n := blocks[rng.Intn(len(blocks))]
						key := fmt.Sprintf("zz_f%d", step)
						name, op = "add field "+n.Path.String(), func(f *File) (*File, error) { return f.SetField(n.Path.Child(key), "1") }
					case 2:
						var parent Path
						if len(blocks) > 0 && rng.Intn(3) > 0 {
							parent = blocks[rng.Intn(len(blocks))].Path
						}
						id := fmt.Sprintf("b%d", step)
						name, op = "add block "+parent.String(), func(f *File) (*File, error) { return f.AddBlock(parent, "zzblk", id, "k 1\nl [1]") }
					case 3:
						var cand []Node
						for _, n := range blocks {
							if n.HasID && !n.Opaque {
								cand = append(cand, n)
							}
						}
						if len(cand) == 0 {
							continue
						}
						n := cand[rng.Intn(len(cand))]
						id := fmt.Sprintf("r%d", step)
						name, op = "rename "+n.Path.String(), func(f *File) (*File, error) { return f.RenameBlock(n.Path, id) }
					case 4:
						if len(all) == 0 {
							continue
						}
						n := all[rng.Intn(len(all))]
						sibs, _ := cur.Statements(parentOf(cur.root, n.Path))
						idx := rng.Intn(len(sibs) + 1)
						name, op = "move "+n.Path.String(), func(f *File) (*File, error) { return f.MoveBlock(n.Path, idx) }
					case 5:
						if len(markers) == 0 {
							continue
						}
						n := markers[rng.Intn(len(markers))]
						name, op = "remove "+n.Path.String(), func(f *File) (*File, error) {
							if n.Kind == KindBlock {
								return f.RemoveBlock(n.Path)
							}
							return f.RemoveField(n.Path)
						}
					}
					before := cur.Text()
					if err := h.Apply(op); err != nil {
						if cur.Text() != before || h.Current() != cur {
							t.Fatalf("failed %s changed state", name)
						}
						refused++
						continue // refusal is allowed; corruption is not
					}
					applied++
					out := h.Current()
					if _, err := bcl.ParseFile("x", out.Source()); err != nil {
						t.Fatalf("after %s: unparsable: %v", name, err)
					}
					if lost := lostComments(origComments, commentTexts(out.Source())); len(lost) > 0 {
						t.Fatalf("after %s: comments lost: %q", name, lost)
					}
				}
				t.Logf("applied %d edits, refused %d", applied, refused)
				if applied < 8 {
					t.Fatalf("only %d edits applied; the sequence is not exercising the engine", applied)
				}
				final := h.Current().Text()
				for h.CanUndo() {
					if err := h.Undo(); err != nil {
						t.Fatal(err)
					}
				}
				if string(h.Current().Source()) != string(ex.src) {
					t.Fatalf("undo-all did not restore the original bytes after %d edits", applied)
				}
				for h.CanRedo() {
					if err := h.Redo(); err != nil {
						t.Fatal(err)
					}
				}
				if h.Current().Text() != final {
					t.Fatal("redo-all did not reproduce the final state")
				}
			})
		}
	}
}
