// Package cfg decodes the loosely typed config map REF hands a resource
// factory into a typed struct.
//
// REF decodes BCL into map[string]any with a few conventions this package
// undoes, so every plugin reads its configuration the same way:
//
//   - a duration literal (5s, 250ms) arrives as {"$duration": "5s"};
//   - a labelled block (rule "a" { … }) arrives as {id, type, body}, and a
//     repeated one as a list of those;
//   - integers are int64 and floats float64.
//
// Decode normalises all of that into plain JSON and unmarshals it, so a plugin
// declares its configuration as an ordinary struct with json tags and gets
// unknown-field detection for free: a typo in BCL fails at load time rather
// than silently using a default.
package cfg

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Duration is a time.Duration that decodes from "5s", "250ms" or a number of
// seconds.
type Duration time.Duration

// D returns the value as a time.Duration.
func (d Duration) D() time.Duration { return time.Duration(d) }

// UnmarshalJSON implements json.Unmarshaler.
func (d *Duration) UnmarshalJSON(b []byte) error {
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	switch x := v.(type) {
	case string:
		if strings.TrimSpace(x) == "" {
			*d = 0
			return nil
		}
		parsed, err := time.ParseDuration(x)
		if err != nil {
			return fmt.Errorf("invalid duration %q", x)
		}
		*d = Duration(parsed)
	case float64:
		*d = Duration(time.Duration(x * float64(time.Second)))
	case nil:
		*d = 0
	default:
		return fmt.Errorf("invalid duration %v", v)
	}
	return nil
}

// MarshalJSON implements json.Marshaler.
func (d Duration) MarshalJSON() ([]byte, error) { return json.Marshal(time.Duration(d).String()) }

// Decode normalises config and unmarshals it into out, rejecting unknown keys.
func Decode(config map[string]any, out any) error {
	raw, err := json.Marshal(Normalize(config))
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}
	return nil
}

// Normalize rewrites REF's BCL conventions into plain JSON-shaped values.
func Normalize(v any) any {
	switch x := v.(type) {
	case map[string]any:
		if d, ok := x["$duration"]; ok && len(x) == 1 {
			return d
		}
		if body, ok := x["body"].(map[string]any); ok && labelled(x) {
			out := map[string]any{}
			for k, val := range body {
				out[k] = Normalize(val)
			}
			if id, ok := x["id"]; ok {
				out["id"] = id
			}
			return out
		}
		out := make(map[string]any, len(x))
		for k, val := range x {
			out[k] = Normalize(val)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			out[i] = Normalize(val)
		}
		return out
	default:
		return v
	}
}

func labelled(m map[string]any) bool {
	if _, ok := m["body"]; !ok {
		return false
	}
	for k := range m {
		switch k {
		case "id", "type", "body":
		default:
			return false
		}
	}
	return true
}
