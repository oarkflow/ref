package main

import (
	"context"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	"github.com/oarkflow/ref/platform"
)

// LoadRecursiveConfig finds all *.bcl files under rootDir (including all subdirectories),
// sorts them deterministically by relative path, and compiles them via platform.LoadFiles.
func LoadRecursiveConfig(ctx context.Context, rootDir string, opts platform.LoadOptions) (*platform.Platform, error) {
	var files []string
	err := filepath.WalkDir(rootDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(d.Name(), ".bcl") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walking config dir %q: %w", rootDir, err)
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no .bcl files found in %q (or its subdirectories)", rootDir)
	}

	// Sort deterministically so folders load in order: 00_core < 01_master < 02_encounters ...
	sort.Slice(files, func(i, j int) bool {
		relI, _ := filepath.Rel(rootDir, files[i])
		relJ, _ := filepath.Rel(rootDir, files[j])
		return relI < relJ
	})

	return platform.LoadFiles(ctx, files, opts)
}
