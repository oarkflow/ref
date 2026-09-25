package config

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"
)

var durationType = reflect.TypeOf(time.Duration(0))

// applyDefaults walks a struct (recursing into nested structs) and, for
// every zero-valued field with a `default:"..."` tag, parses the tag value
// into the field. It runs once, before any Source, so it is always the
// lowest-precedence layer.
func applyDefaults(rv reflect.Value) error {
	rt := rv.Type()
	for i := 0; i < rt.NumField(); i++ {
		field := rt.Field(i)
		if !field.IsExported() {
			continue
		}
		fv := rv.Field(i)

		if fv.Kind() == reflect.Struct && fv.Type() != durationType {
			if err := applyDefaults(fv); err != nil {
				return fmt.Errorf("%s: %w", field.Name, err)
			}
			continue
		}

		def, ok := field.Tag.Lookup("default")
		if !ok || !fv.IsZero() {
			continue
		}
		if err := setFieldFromString(fv, def); err != nil {
			return fmt.Errorf("field %s: default %q: %w", field.Name, def, err)
		}
	}
	return nil
}

// checkRequired walks a struct (recursing into nested structs) collecting
// the dotted names of every `required:"true"` field that is still zero.
func checkRequired(rv reflect.Value, path string) []string {
	rt := rv.Type()
	var missing []string
	for i := 0; i < rt.NumField(); i++ {
		field := rt.Field(i)
		if !field.IsExported() {
			continue
		}
		fv := rv.Field(i)
		name := field.Name
		if path != "" {
			name = path + "." + name
		}

		if fv.Kind() == reflect.Struct && fv.Type() != durationType {
			missing = append(missing, checkRequired(fv, name)...)
			continue
		}

		if required, _ := strconv.ParseBool(field.Tag.Get("required")); required && fv.IsZero() {
			label := field.Name
			if env := field.Tag.Get("env"); env != "" {
				label = env
			} else if path != "" {
				label = name
			}
			missing = append(missing, label)
		}
	}
	return missing
}

// applyEnv walks a struct (recursing into nested structs), and for every
// field carrying an `env:"NAME"` tag, looks NAME up with the given lookup
// function. A found, non-empty value is parsed into the field; a field with
// no env tag, or whose variable is unset, is left untouched.
func applyEnv(rv reflect.Value, prefix string, lookup func(string) (string, bool)) error {
	rt := rv.Type()
	for i := 0; i < rt.NumField(); i++ {
		field := rt.Field(i)
		if !field.IsExported() {
			continue
		}
		fv := rv.Field(i)

		if fv.Kind() == reflect.Struct && fv.Type() != durationType {
			if err := applyEnv(fv, prefix, lookup); err != nil {
				return err
			}
			continue
		}

		name, ok := field.Tag.Lookup("env")
		if !ok || name == "" {
			continue
		}
		key := prefix + name
		value, present := lookup(key)
		if !present || value == "" {
			continue
		}
		if err := setFieldFromString(fv, value); err != nil {
			return fmt.Errorf("env %s: %w", key, err)
		}
	}
	return nil
}

// setFieldFromString parses s into fv according to fv's kind. It supports the
// scalar types a config struct realistically needs: strings, bools, every
// integer/float width, time.Duration, []byte and []string (comma-separated).
func setFieldFromString(fv reflect.Value, s string) error {
	if fv.Type() == durationType {
		d, err := time.ParseDuration(s)
		if err != nil {
			return err
		}
		fv.SetInt(int64(d))
		return nil
	}

	switch fv.Kind() {
	case reflect.String:
		fv.SetString(s)
	case reflect.Bool:
		b, err := strconv.ParseBool(s)
		if err != nil {
			return err
		}
		fv.SetBool(b)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return err
		}
		fv.SetInt(n)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			return err
		}
		fv.SetUint(n)
	case reflect.Float32, reflect.Float64:
		n, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return err
		}
		fv.SetFloat(n)
	case reflect.Slice:
		switch fv.Type().Elem().Kind() {
		case reflect.Uint8: // []byte
			fv.SetBytes([]byte(s))
		case reflect.String: // []string, comma-separated
			var out []string
			for _, part := range strings.Split(s, ",") {
				if trimmed := strings.TrimSpace(part); trimmed != "" {
					out = append(out, trimmed)
				}
			}
			fv.Set(reflect.ValueOf(out))
		default:
			return fmt.Errorf("unsupported slice element type %s", fv.Type().Elem())
		}
	default:
		return fmt.Errorf("unsupported field type %s", fv.Type())
	}
	return nil
}
