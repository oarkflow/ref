package config

import (
	"fmt"
	"reflect"
	"strings"

	ocfg "github.com/oarkflow/config"
)

// FromOarkflow adapts an already-loaded github.com/oarkflow/config Manager
// into a Source, so ref/config.Load[T] can layer its BCL/env/flag/dotenv
// providers, secret redaction and hot reload with FromEnv, FromMap and the
// rest.
//
// This is the intended way a host loads bootstrap settings — the settings a
// process needs before platform.LoadDir/LoadFile can even parse the BCL
// application document's own secrets block (listen address, replica id, log
// level, and the like). It is deliberately unrelated to that document's own
// secret and config-revision lifecycle, which platform/deploy already owns.
//
// Each exported field of T with an `env:"NAME"` tag is read from cfg at the
// dot path "<section>.<lowercased field name>" (e.g. a field Port under
// section "app" reads "app.port"). A field is only set when cfg.Has(path) is
// true, so a Manager with no opinion about a field never overwrites a
// lower-precedence default or an earlier Source — the same contract every
// other Source in this package follows. Nested struct fields (other than
// time.Duration) recurse using the same section, so a config struct can
// still be organised into logical groups without changing the dot paths.
func FromOarkflow(cfg *ocfg.Manager, section string) Source {
	return SourceFunc(func(target any) error {
		rv := reflect.ValueOf(target)
		if rv.Kind() != reflect.Pointer || rv.Elem().Kind() != reflect.Struct {
			return fmt.Errorf("config: FromOarkflow target must be a pointer to struct")
		}
		return applyOarkflow(cfg, rv.Elem(), section)
	})
}

func applyOarkflow(cfg *ocfg.Manager, rv reflect.Value, section string) error {
	rt := rv.Type()
	for i := 0; i < rt.NumField(); i++ {
		field := rt.Field(i)
		if !field.IsExported() {
			continue
		}
		fv := rv.Field(i)

		if fv.Kind() == reflect.Struct && fv.Type() != durationType {
			if err := applyOarkflow(cfg, fv, section); err != nil {
				return err
			}
			continue
		}

		if _, tagged := field.Tag.Lookup("env"); !tagged {
			continue
		}
		path := section + "." + strings.ToLower(field.Name)
		if !cfg.Has(path) {
			continue
		}
		if err := setFieldFromString(fv, cfg.String(path)); err != nil {
			return fmt.Errorf("path %s: %w", path, err)
		}
	}
	return nil
}
