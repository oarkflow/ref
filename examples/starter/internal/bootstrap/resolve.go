package bootstrap

import (
	"os"
	"path/filepath"
)

// ResolveDir returns the first candidate that exists and is a directory, as
// an absolute path — so every binary in this starter finds bcl/, templates/,
// migrations/, ... the same way, whether it's run from the repository root
// (`go run ./examples/starter/cmd/server`) or from within the module
// (`go run ./cmd/server`). Falls back to the first candidate, unresolved,
// if none exist — the caller's own "no such directory" error is clearer
// than one manufactured here.
func ResolveDir(candidates ...string) string {
	for _, c := range candidates {
		if fi, err := os.Stat(c); err == nil && fi.IsDir() {
			abs, _ := filepath.Abs(c)
			return abs
		}
	}
	return candidates[0]
}
