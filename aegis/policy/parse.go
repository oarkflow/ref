package policy

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

var parseBufferPool = sync.Pool{
	New: func() any {
		return new(bytes.Buffer)
	},
}

// ParseJSON parses a developer-authored Policy from JSON-encoded bytes.
//
// Note: YAML support will be added in a future update without adding external dependencies.
func ParseJSON(data []byte) (*Policy, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return nil, errors.New("policy: empty policy data")
	}

	var pol Policy
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	if err := decoder.Decode(&pol); err != nil {
		return nil, fmt.Errorf("policy: failed to parse JSON policy: %w", err)
	}

	if pol.Functions == nil {
		pol.Functions = make(map[string]FuncPolicy)
	}

	return &pol, nil
}

// ParseFile reads and parses a Policy document from the filesystem at path.
//
// File types are detected by extension:
//   - .json: Parsed as JSON
//   - .yaml, .yml: Returns an informative error that YAML support will arrive in a future update
//   - Any other or missing extension: Assumed to be JSON
func ParseFile(path string) (*Policy, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("policy: failed to read policy file %q: %w", path, err)
	}

	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".yaml", ".yml":
		return nil, fmt.Errorf("policy: YAML format (%s) is not yet supported for file %q; please use JSON", ext, path)
	case ".json":
		return ParseJSON(data)
	default:
		// Assume JSON for files without a known JSON/YAML extension.
		return ParseJSON(data)
	}
}
