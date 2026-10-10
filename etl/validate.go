package etl

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Evaluator evaluates a boolean expression against a row. The platform
// supplies one backed by its expression language.
type Evaluator func(expr string, row Row) (bool, error)

// Validator checks rows one at a time against a source's rules, so a file too
// large to hold can be validated as it is read. It remembers the values of
// unique columns, which is the only thing that grows with the data.
type Validator struct {
	src      *Source
	eval     Evaluator
	compiled []*regexp.Regexp
	seen     []map[string]bool
}

// NewValidator prepares a source's rules and refuses rules that cannot work.
func NewValidator(src *Source, eval Evaluator) (*Validator, error) {
	v := &Validator{src: src, eval: eval, compiled: make([]*regexp.Regexp, len(src.Rules)), seen: make([]map[string]bool, len(src.Rules))}
	for i, r := range src.Rules {
		switch r.Kind {
		case "required", "type", "range", "enum":
			if r.Field == "" {
				return nil, fmt.Errorf("%w: rule %q needs a field", ErrInvalid, r.Name)
			}
		case "unique":
			if r.Field == "" {
				return nil, fmt.Errorf("%w: rule %q needs a field", ErrInvalid, r.Name)
			}
			v.seen[i] = map[string]bool{}
		case "regex":
			re, e := regexp.Compile(r.Pattern)
			if e != nil || r.Field == "" {
				return nil, fmt.Errorf("%w: rule %q: bad regex or missing field", ErrInvalid, r.Name)
			}
			v.compiled[i] = re
		case "expr":
			if eval == nil {
				return nil, fmt.Errorf("%w: rule %q uses an expression but no evaluator is configured", ErrInvalid, r.Name)
			}
		default:
			return nil, fmt.Errorf("%w: rule %q has unknown kind %q", ErrInvalid, r.Name, r.Kind)
		}
	}
	return v, nil
}

// Check applies every rule to one row (rowNo counts from 1) and returns one
// entry for each rule it breaks; none means the row is valid.
func (v *Validator) Check(rowNo int, row Row) []Quarantine {
	var broke []Quarantine
	for i, r := range v.src.Rules {
		msg := ""
		val, present := row[r.Field]
		empty := !present || val == nil || strings.TrimSpace(fmt.Sprint(val)) == ""
		switch r.Kind {
		case "required":
			if empty {
				msg = "is required"
			}
		case "type":
			if !empty && !typeOK(r.Type, val) {
				msg = "is not a valid " + r.Type
			}
		case "regex":
			if !empty && !v.compiled[i].MatchString(fmt.Sprint(val)) {
				msg = "does not match " + r.Pattern
			}
		case "range":
			if !empty {
				f, ok := toFloat(val)
				switch {
				case !ok:
					msg = "is not a number"
				case r.Min != nil && f < *r.Min:
					msg = fmt.Sprintf("is below %v", *r.Min)
				case r.Max != nil && f > *r.Max:
					msg = fmt.Sprintf("is above %v", *r.Max)
				}
			}
		case "enum":
			if !empty && !contains(r.Values, fmt.Sprint(val)) {
				msg = "is not one of " + strings.Join(r.Values, ", ")
			}
		case "unique":
			if !empty {
				k := fmt.Sprint(val)
				if v.seen[i][k] {
					msg = "is a duplicate"
				}
				v.seen[i][k] = true
			}
		case "expr":
			ok, e := v.eval(r.Expr, row)
			if e != nil {
				msg = "could not be checked: " + e.Error()
			} else if !ok {
				msg = "failed " + r.Expr
			}
		}
		if msg == "" {
			continue
		}
		reason := r.Message
		if reason == "" {
			reason = strings.TrimSpace(r.Field + " " + msg)
		}
		broke = append(broke, Quarantine{RowNo: rowNo, Rule: r.Name, Field: r.Field, Reason: reason, Row: row})
	}
	return broke
}

// Validate applies a source's rules to rows. It returns the rows that passed
// and one Quarantine entry per rule a refused row broke (a row is refused once,
// but every broken rule is recorded). Row numbers start at 1.
func Validate(src *Source, rows []Row, eval Evaluator) (valid []Row, rejects []Quarantine, err error) {
	v, err := NewValidator(src, eval)
	if err != nil {
		return nil, nil, err
	}
	for n, row := range rows {
		if broke := v.Check(n+1, row); len(broke) == 0 {
			valid = append(valid, row)
		} else {
			rejects = append(rejects, broke...)
		}
	}
	return valid, rejects, nil
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func toFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case float32:
		return float64(x), true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		return f, err == nil
	}
	return 0, false
}

func typeOK(t string, v any) bool {
	switch t {
	case "string":
		return true
	case "int":
		f, ok := toFloat(v)
		return ok && f == float64(int64(f))
	case "float":
		_, ok := toFloat(v)
		return ok
	case "bool":
		switch x := v.(type) {
		case bool:
			return true
		case string:
			_, err := strconv.ParseBool(x)
			return err == nil
		}
		return false
	case "date":
		s, ok := v.(string)
		if !ok {
			return false
		}
		if _, err := time.Parse("2006-01-02", s); err == nil {
			return true
		}
		_, err := time.Parse(time.RFC3339, s)
		return err == nil
	}
	return false
}
