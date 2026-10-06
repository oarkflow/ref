package bootstrap

import (
	"os"
	"path/filepath"
)

// ResolveDir returns the first candidate that exists and is a directory, as
// an absolute path.
func ResolveDir(candidates ...string) string {
	for _, c := range candidates {
		if fi, err := os.Stat(c); err == nil && fi.IsDir() {
			abs, _ := filepath.Abs(c)
			return abs
		}
	}
	return candidates[0]
}
