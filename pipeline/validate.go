package pipeline

import (
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

var (
	emailRe = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)
	phoneRe = regexp.MustCompile(`^\+?[0-9 ()\-.]{6,20}$`)
)

// coerce converts a submitted value to the input's kind and checks its
// constraints. An empty value is returned as nil with no error; requiredness
// is checked separately because it depends on the stage and on conditions.
func (c *Compiled) coerce(form string, in Input, raw any) (any, string, string) {
	if isEmpty(raw) {
		return nil, "", ""
	}
	label := in.Label
	if label == "" {
		label = in.Name
	}
	switch in.Kind {
	case KindNumber, KindInteger:
		f, ok := toNumber(raw)
		if !ok {
			return nil, "type", label + " must be a number"
		}
		if in.Kind == KindInteger && f != math.Trunc(f) {
			return nil, "type", label + " must be a whole number"
		}
		if in.Min != "" {
			if min, err := strconv.ParseFloat(in.Min, 64); err == nil && f < min {
				return nil, "min", fmt.Sprintf("%s must be at least %s", label, in.Min)
			}
		}
		if in.Max != "" {
			if max, err := strconv.ParseFloat(in.Max, 64); err == nil && f > max {
				return nil, "max", fmt.Sprintf("%s must be at most %s", label, in.Max)
			}
		}
		if in.Kind == KindInteger {
			return int64(f), "", ""
		}
		return f, "", ""
	case KindBoolean:
		switch v := raw.(type) {
		case bool:
			return v, "", ""
		case string:
			if b, err := strconv.ParseBool(strings.TrimSpace(v)); err == nil {
				return b, "", ""
			}
		}
		return nil, "type", label + " must be true or false"
	case KindMultiSelect:
		var values []string
		switch v := raw.(type) {
		case []string:
			values = v
		case []any:
			for _, item := range v {
				values = append(values, fmt.Sprint(item))
			}
		case string:
			for _, part := range strings.Split(v, ",") {
				if part = strings.TrimSpace(part); part != "" {
					values = append(values, part)
				}
			}
		default:
			return nil, "type", label + " must be a list"
		}
		out := make([]any, 0, len(values))
		for _, v := range values {
			if len(in.Options) > 0 && !slices.Contains(in.Options, v) {
				return nil, "option", fmt.Sprintf("%q is not a valid choice for %s", v, label)
			}
			out = append(out, v)
		}
		return out, "", ""
	case KindFile:
		switch v := raw.(type) {
		case string:
			return strings.TrimSpace(v), "", ""
		case map[string]any:
			if s, ok := v["id"].(string); ok && s != "" {
				return v, "", ""
			}
		}
		return nil, "type", label + " must be an uploaded file reference"
	}

	s, ok := raw.(string)
	if !ok {
		if _, isMap := raw.(map[string]any); isMap {
			return nil, "type", label + " must be text"
		}
		if _, isList := raw.([]any); isList {
			return nil, "type", label + " must be text"
		}
		s = fmt.Sprint(raw)
	}
	s = strings.TrimSpace(s)
	n := utf8.RuneCountInString(s)
	if in.MinLength > 0 && n < in.MinLength {
		return nil, "min_length", fmt.Sprintf("%s must be at least %d characters", label, in.MinLength)
	}
	if in.MaxLength > 0 && n > in.MaxLength {
		return nil, "max_length", fmt.Sprintf("%s must be at most %d characters", label, in.MaxLength)
	}
	if re := c.patterns[form+"."+in.Name]; re != nil && !re.MatchString(s) {
		return nil, "pattern", label + " has an invalid format"
	}
	switch in.Kind {
	case KindEmail:
		if !emailRe.MatchString(s) {
			return nil, "email", label + " must be an email address"
		}
	case KindPhone:
		if !phoneRe.MatchString(s) {
			return nil, "phone", label + " must be a phone number"
		}
	case KindSelect, KindRadio:
		if len(in.Options) > 0 && !slices.Contains(in.Options, s) {
			return nil, "option", fmt.Sprintf("%q is not a valid choice for %s", s, label)
		}
	case KindDate, KindDateTime:
		layout := "2006-01-02"
		if in.Kind == KindDateTime {
			layout = time.RFC3339
		}
		t, err := time.Parse(layout, s)
		if err != nil {
			return nil, "date", fmt.Sprintf("%s must be a %s", label, map[bool]string{true: "date (YYYY-MM-DD)", false: "timestamp (RFC 3339)"}[in.Kind == KindDate])
		}
		if in.Min != "" {
			if min, err := time.Parse(layout, in.Min); err == nil && t.Before(min) {
				return nil, "min", fmt.Sprintf("%s must be on or after %s", label, in.Min)
			}
		}
		if in.Max != "" {
			if max, err := time.Parse(layout, in.Max); err == nil && t.After(max) {
				return nil, "max", fmt.Sprintf("%s must be on or before %s", label, in.Max)
			}
		}
	}
	return s, "", ""
}

// missingValue reports whether a required input is unanswered. A required
// boolean means "must be accepted" (a declaration), so false counts as missing.
func missingValue(in Input, v any) bool {
	if in.Kind == KindBoolean {
		b, ok := v.(bool)
		return !ok || !b
	}
	return isEmpty(v)
}

func isEmpty(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(t) == ""
	case []any:
		return len(t) == 0
	case []string:
		return len(t) == 0
	case map[string]any:
		return len(t) == 0
	}
	return false
}

func toNumber(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case float32:
		return float64(t), true
	case int:
		return float64(t), true
	case int64:
		return float64(t), true
	case int32:
		return float64(t), true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(t), 64)
		return f, err == nil
	}
	return 0, false
}

func truthy(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case nil:
		return false
	case string:
		return t != "" && t != "false" && t != "0"
	default:
		f, ok := toNumber(v)
		return !ok || f != 0
	}
}
