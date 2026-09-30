// Command dumptrees writes block-tree fixtures (the JSON shape of
// GET /drafts/{id}/files/{file}/tree) for real configuration files, using
// studio/model. The canvas tests derive graphs from them.
//
//	go run ./studio/web/tools/dumptrees -out studio/web/src/fixtures
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/oarkflow/ref/studio/model"
)

type node struct {
	Kind     string `json:"kind"`
	Path     string `json:"path"`
	Type     string `json:"type,omitempty"`
	ID       string `json:"id,omitempty"`
	Name     string `json:"name,omitempty"`
	Raw      string `json:"raw,omitempty"`
	Comment  string `json:"comment,omitempty"`
	Line     int    `json:"line"`
	Start    int    `json:"start"`
	End      int    `json:"end"`
	Children []node `json:"children,omitempty"`
}

func toNode(f *model.File, n model.Node, depth int) node {
	b := node{Path: n.Path.String(), Comment: n.Doc, Line: n.Line, Start: n.Start, End: n.End}
	if n.Kind == model.KindBlock {
		b.Kind, b.Type, b.ID = "block", n.Head, n.ID
		if depth < 64 {
			if kids, err := f.Statements(n.Path); err == nil {
				for _, k := range kids {
					b.Children = append(b.Children, toNode(f, k, depth+1))
				}
			}
		}
		return b
	}
	b.Kind, b.Name, b.Raw = "field", n.Head, n.Value
	return b
}

func main() {
	out := flag.String("out", "src/fixtures", "output directory")
	root := flag.String("root", "", "repository root; default: search upward for go.mod")
	flag.Parse()
	if *root == "" {
		wd, _ := os.Getwd()
		for d := wd; d != filepath.Dir(d); d = filepath.Dir(d) {
			if _, err := os.Stat(filepath.Join(d, "platform")); err == nil {
				*root = d
				break
			}
		}
	}
	// name in fixtures -> source path relative to the repository root
	sources := map[string]string{
		"03_intents.bcl":               "examples/starter/resources/config/03_intents.bcl",
		"12_todo_workflow_example.bcl": "examples/starter/resources/config/12_todo_workflow_example.bcl",
		"passport.bcl":                 "examples/passport/app.bcl",
		"09_workflow_example.bcl":      "examples/starter/resources/config/09_workflow_example.bcl",
	}
	all := map[string][]node{}
	for name, rel := range sources {
		src, err := os.ReadFile(filepath.Join(*root, rel))
		must(err)
		f, err := model.Open(name, src)
		must(err)
		top, err := f.Statements(nil)
		must(err)
		tree := []node{}
		for _, n := range top {
			tree = append(tree, toNode(f, n, 0))
		}
		all[name] = tree
	}
	must(os.MkdirAll(*out, 0o755))
	b, err := json.MarshalIndent(all, "", " ")
	must(err)
	must(os.WriteFile(filepath.Join(*out, "trees.json"), append(b, '\n'), 0o644))
	fmt.Println("wrote", filepath.Join(*out, "trees.json"), len(b), "bytes")
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
