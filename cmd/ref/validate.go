package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/oarkflow/ref/config"
	"github.com/oarkflow/ref/platform"
)

// runValidation checks configuration without opening a database or serving.
func runValidation(ctx context.Context, command string, args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: ref %s <application directory>", command)
	}
	root, err := filepath.Abs(args[0])
	if err != nil {
		return err
	}
	for _, path := range []string{filepath.Join(root, ".env"), filepath.Join(root, ".env.example")} {
		if _, err := os.Stat(path); err == nil {
			if err := config.LoadDotenv(path); err != nil {
				return err
			}
			break
		}
	}
	var source []platform.BundleFile
	var baseDir string
	for _, candidate := range []string{"config", "bcl", "app.bcl", "resources/config"} {
		path := filepath.Join(root, candidate)
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		if !info.IsDir() {
			if strings.HasSuffix(path, ".bcl") {
				data, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				source = []platform.BundleFile{{Path: path, Content: string(data)}}
				baseDir = filepath.Dir(path)
			}
			break
		}
		err = filepath.WalkDir(path, func(file string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".bcl") {
				return nil
			}
			data, err := os.ReadFile(file)
			if err != nil {
				return err
			}
			source = append(source, platform.BundleFile{Path: file, Content: string(data)})
			return nil
		})
		if err != nil {
			return err
		}
		if len(source) > 0 {
			baseDir = path
			break
		}
	}
	if len(source) == 0 {
		return fmt.Errorf("no BCL configuration found in %q", root)
	}
	sort.Slice(source, func(i, j int) bool { return source[i].Path < source[j].Path })
	opts := platform.DefaultLoadOptions()
	opts.Profile = config.Env("APP_ENV", "development")
	report := platform.Validate(ctx, platform.JoinBundle(source), baseDir, opts)
	var output any = report
	if command == "show" && report.Valid {
		output = report.Summary
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(output); err != nil {
		return err
	}
	if !report.Valid {
		return fmt.Errorf("configuration has %d error(s)", len(report.Errors))
	}
	return nil
}
