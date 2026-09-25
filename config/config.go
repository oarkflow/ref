// Package config is the first-class, reusable config-loading primitive for
// this repository. It generalises the ad hoc pattern already used by
// examples/ref-app (LoadConfig(look func(string) (string, bool))) and
// examples/boilerplate/config (Load() with getEnv/getEnvInt helpers) into a
// single generic loader that any package or example can use, without forcing
// either example to change.
//
// A config struct declares its shape with three struct tags:
//
//	env:"NAME"       - the environment variable that binds to this field
//	default:"value"  - the value used when nothing else sets the field
//	required:"true"  - Load fails if the field is still zero after all sources
//
// Sources are applied in the order passed to Load, each one only overwriting
// the fields it actually has a value for. The recommended precedence, low to
// high, is:
//
//	struct `default` tags  <  file (BCL/JSON)  <  env vars  <  explicit overrides
//
// i.e. call Load with sources ordered from least to most authoritative:
//
//	type AppConfig struct {
//		Port    int           `env:"REF_PORT" default:"8080"`
//		DSN     string         `env:"REF_DSN" required:"true"`
//		Timeout time.Duration `env:"REF_TIMEOUT" default:"5s"`
//	}
//
//	func (c AppConfig) Validate() error {
//		if c.Port <= 0 {
//			return fmt.Errorf("port must be positive")
//		}
//		return nil
//	}
//
//	cfg, err := config.Load[AppConfig](
//		config.FromFile("app.bcl"),           // lower precedence
//		config.FromEnv("REF_"),               // higher precedence
//		config.FromMap(map[string]any{         // explicit overrides win
//			"REF_PORT": 9090,
//		}),
//	)
//
// `default` tags are applied first, to every zero-valued field, before any
// Source runs. Each Source then only touches the fields it has a value for,
// so layering is simply "pass sources in increasing precedence order".
//
// After all sources are applied, Load enforces `required:"true"` fields and,
// if the config type implements
//
//	interface{ Validate() error }
//
// calls Validate and wraps any error it returns.
//
// Package-level notes:
//
//   - FromEnv takes an injectable lookup function (FromEnvFunc), matching the
//     testable style of examples/ref-app's LoadConfig(look func(string) (string, bool)).
//   - FromFile dispatches on extension: ".bcl" decodes with github.com/oarkflow/bcl
//     (already a module dependency, used the same way examples/boilerplate's BCL
//     demo uses it), ".json" decodes with encoding/json. Both only set the fields
//     present in the file, which is what makes file+env+override layering work.
//   - Watch polls a file-based config on an interval (stdlib time.Ticker; there is
//     no fsnotify dependency here, by design) and invokes a callback when the
//     parsed result changes, compared with reflect.DeepEqual.
package config

import (
	"fmt"
	"reflect"
)

// Source is one origin of configuration values. Apply receives a pointer to
// the config struct and should only set the fields it has a value for,
// leaving every other field untouched so that layering multiple sources
// produces the expected precedence.
type Source interface {
	Apply(target any) error
}

// SourceFunc adapts a function to the Source interface.
type SourceFunc func(target any) error

// Apply implements Source.
func (f SourceFunc) Apply(target any) error { return f(target) }

// Validator is the optional hook a config struct may implement. Load calls it
// once, after every Source has been applied and required fields have been
// checked, and wraps a non-nil error with a clear "config: validation failed"
// prefix.
type Validator interface {
	Validate() error
}

// Load builds a T by applying `default` struct tags, then each Source in the
// order given, then checking `required:"true"` fields, then calling
// Validate() if T (or *T) implements Validator.
//
// Sources are layered least-authoritative first: a later source's value for a
// given field overrides an earlier source's, but a source that has no
// opinion about a field never clobbers what an earlier source set.
func Load[T any](sources ...Source) (T, error) {
	var cfg T
	rv := reflect.ValueOf(&cfg).Elem()
	if rv.Kind() != reflect.Struct {
		return cfg, fmt.Errorf("config: Load requires a struct type, got %s", rv.Kind())
	}

	if err := applyDefaults(rv); err != nil {
		return cfg, fmt.Errorf("config: applying defaults: %w", err)
	}

	for i, src := range sources {
		if src == nil {
			continue
		}
		if err := src.Apply(&cfg); err != nil {
			return cfg, fmt.Errorf("config: source %d: %w", i, err)
		}
	}

	if missing := checkRequired(rv, ""); len(missing) > 0 {
		return cfg, fmt.Errorf("config: missing required field(s): %v", missing)
	}

	if v, ok := any(&cfg).(Validator); ok {
		if err := v.Validate(); err != nil {
			return cfg, fmt.Errorf("config: validation failed: %w", err)
		}
	} else if v, ok := any(cfg).(Validator); ok {
		if err := v.Validate(); err != nil {
			return cfg, fmt.Errorf("config: validation failed: %w", err)
		}
	}

	return cfg, nil
}
