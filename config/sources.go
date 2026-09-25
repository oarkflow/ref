package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/oarkflow/bcl"
)

// FromEnv reads environment variables via os.LookupEnv, prefixing every
// `env:"NAME"` tag with prefix (pass "" for no prefix). It is a thin
// convenience wrapper around FromEnvFunc.
func FromEnv(prefix string) Source {
	return FromEnvFunc(prefix, os.LookupEnv)
}

// FromEnvFunc reads environment variables via an injectable lookup function,
// generalising the pattern already used by examples/ref-app's
// LoadConfig(look func(string) (string, bool)) so tests never touch the real
// environment.
func FromEnvFunc(prefix string, lookup func(string) (string, bool)) Source {
	if lookup == nil {
		lookup = os.LookupEnv
	}
	return SourceFunc(func(target any) error {
		rv := reflect.ValueOf(target)
		if rv.Kind() != reflect.Pointer || rv.Elem().Kind() != reflect.Struct {
			return fmt.Errorf("config: FromEnv target must be a pointer to struct")
		}
		return applyEnv(rv.Elem(), prefix, lookup)
	})
}

// FromFile decodes a config file into the target, dispatching on extension:
// ".bcl" is decoded with github.com/oarkflow/bcl (the module's existing
// config/DSL dependency), ".json" with encoding/json. Any other extension is
// an error, since guessing a format silently is worse than refusing.
//
// Only the fields actually present in the file are set, so FromFile composes
// correctly with lower- or higher-precedence sources.
func FromFile(path string) Source {
	return SourceFunc(func(target any) error {
		switch ext := strings.ToLower(filepath.Ext(path)); ext {
		case ".bcl":
			return FromBCLFile(path).Apply(target)
		case ".json":
			return FromJSONFile(path).Apply(target)
		default:
			return fmt.Errorf("config: FromFile %s: unsupported extension %q (want .bcl or .json)", path, ext)
		}
	})
}

// FromBCLFile decodes a BCL file directly into the target struct using
// github.com/oarkflow/bcl.DecodeFile.
func FromBCLFile(path string) Source {
	return SourceFunc(func(target any) error {
		if err := bcl.DecodeFile(path, target); err != nil {
			return fmt.Errorf("config: decoding BCL file %s: %w", path, err)
		}
		return nil
	})
}

// FromJSONFile decodes a JSON file directly into the target struct.
func FromJSONFile(path string) Source {
	return SourceFunc(func(target any) error {
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("config: reading JSON file %s: %w", path, err)
		}
		if err := json.Unmarshal(data, target); err != nil {
			return fmt.Errorf("config: decoding JSON file %s: %w", path, err)
		}
		return nil
	})
}

// FromMap applies explicit overrides keyed by a field's `env:"NAME"` tag
// value (matching the same name FromEnv would use, without the prefix). It
// is meant to be the highest-precedence Source — command-line flags or
// programmatic overrides layered on top of file and env sources.
//
// Values may be the field's native type (e.g. int, time.Duration) or a
// string, which is parsed the same way an environment variable would be.
func FromMap(overrides map[string]any) Source {
	return SourceFunc(func(target any) error {
		if len(overrides) == 0 {
			return nil
		}
		rv := reflect.ValueOf(target)
		if rv.Kind() != reflect.Pointer || rv.Elem().Kind() != reflect.Struct {
			return fmt.Errorf("config: FromMap target must be a pointer to struct")
		}
		return applyOverrides(rv.Elem(), overrides)
	})
}

func applyOverrides(rv reflect.Value, overrides map[string]any) error {
	rt := rv.Type()
	for i := 0; i < rt.NumField(); i++ {
		field := rt.Field(i)
		if !field.IsExported() {
			continue
		}
		fv := rv.Field(i)

		if fv.Kind() == reflect.Struct && fv.Type() != durationType {
			if err := applyOverrides(fv, overrides); err != nil {
				return err
			}
			continue
		}

		name := field.Tag.Get("env")
		if name == "" {
			continue
		}
		raw, ok := overrides[name]
		if !ok {
			continue
		}
		if s, isString := raw.(string); isString {
			if err := setFieldFromString(fv, s); err != nil {
				return fmt.Errorf("override %s: %w", name, err)
			}
			continue
		}
		val := reflect.ValueOf(raw)
		if !val.Type().AssignableTo(fv.Type()) {
			if val.Type().ConvertibleTo(fv.Type()) {
				val = val.Convert(fv.Type())
			} else {
				return fmt.Errorf("override %s: cannot assign %T to %s", name, raw, fv.Type())
			}
		}
		fv.Set(val)
	}
	return nil
}
