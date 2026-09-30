// Command dumpschema writes the studio's schema fixtures: the block schemas
// (platform.BlockSchemas, with doc comments attached) and the registry catalog.
// Run it from anywhere inside the repository:
//
//	go run ./studio/web/tools/dumpschema -out studio/web/src/fixtures
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/oarkflow/ref/platform"
)

func main() {
	out := flag.String("out", "src/fixtures", "output directory")
	root := flag.String("root", "", "repository root (to read doc comments); default: search upward")
	flag.Parse()
	if *root == "" {
		wd, _ := os.Getwd()
		for d := wd; d != filepath.Dir(d); d = filepath.Dir(d) {
			if _, err := os.Stat(filepath.Join(d, "go.mod")); err == nil {
				if b, _ := os.ReadFile(filepath.Join(d, "go.mod")); len(b) > 0 && string(b[:min(len(b), 40)]) != "" {
					if _, err := os.Stat(filepath.Join(d, "platform")); err == nil {
						*root = d
						break
					}
				}
			}
		}
	}
	if *root != "" {
		platform.AttachBlockDocs(filepath.Join(*root, "platform"), filepath.Join(*root, "pipeline"))
	}
	must(os.MkdirAll(*out, 0o755))
	write(filepath.Join(*out, "schema-blocks.json"), platform.BlockSchemas())
	write(filepath.Join(*out, "catalog.json"), platform.NewRegistry().Catalog())
}

func write(path string, v any) {
	b, err := json.MarshalIndent(v, "", "  ")
	must(err)
	must(os.WriteFile(path, append(b, '\n'), 0o644))
	fmt.Println("wrote", path, len(b), "bytes")
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
