package model

import (
	"errors"
	"strings"
)

// Quote renders s as a BCL string literal that decodes back to s. Newlines and
// tabs are escaped; \r and other control characters cannot be escaped in BCL,
// so such strings use a raw backtick string, which itself cannot hold a backtick.
func Quote(s string) (string, error) {
	needsRaw := false
	for _, r := range s {
		if r < 0x20 && r != '\n' && r != '\t' || r == 0x7f {
			needsRaw = true
			break
		}
	}
	if needsRaw {
		if strings.ContainsRune(s, '`') {
			return "", errors.New("model: string has control characters and a backtick; not representable")
		}
		return "`" + s + "`", nil
	}
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String(), nil
}

// QuoteList renders a BCL list of strings: ["a", "b"].
func QuoteList(items ...string) (string, error) {
	parts := make([]string, len(items))
	for i, it := range items {
		q, err := Quote(it)
		if err != nil {
			return "", err
		}
		parts[i] = q
	}
	return "[" + strings.Join(parts, ", ") + "]", nil
}

// isBareID reports whether id can be written without quotes and still parse as\n// a block id. It is stricter than the bcl formatter, which prints dotted ids bare\n// even though `route a.b {` does not parse back as a block.
func isBareID(id string) bool {
	if id == "" || strings.Contains(id, "-") {
		return false
	}
	for _, r := range id {
		if !(r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return !(id[0] >= '0' && id[0] <= '9')
}

// isName reports whether s can head a statement as a bare word.
func isName(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		ok := r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || (i > 0 && (r >= '0' && r <= '9' || r == '.' || r == '-' || r == ':' || r == '/'))
		if !ok {
			return false
		}
	}
	return true
}
