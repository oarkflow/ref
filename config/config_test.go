package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type testConfig struct {
	Port    int           `env:"PORT" default:"8080"`
	Host    string        `env:"HOST" default:"localhost"`
	Debug   bool          `env:"DEBUG" default:"false"`
	Timeout time.Duration `env:"TIMEOUT" default:"5s"`
	DSN     string        `env:"DSN" required:"true"`
}

func lookupFrom(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		v, ok := values[key]
		return v, ok
	}
}

func TestLoadEnvOnlyWithDefaults(t *testing.T) {
	look := lookupFrom(map[string]string{
		"DSN": "postgres://example",
	})
	cfg, err := Load[testConfig](FromEnvFunc("", look))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Port != 8080 {
		t.Errorf("Port = %d, want default 8080", cfg.Port)
	}
	if cfg.Host != "localhost" {
		t.Errorf("Host = %q, want default localhost", cfg.Host)
	}
	if cfg.Debug != false {
		t.Errorf("Debug = %v, want default false", cfg.Debug)
	}
	if cfg.Timeout != 5*time.Second {
		t.Errorf("Timeout = %v, want default 5s", cfg.Timeout)
	}
	if cfg.DSN != "postgres://example" {
		t.Errorf("DSN = %q, want postgres://example", cfg.DSN)
	}
}

func TestLoadRequiredFieldMissing(t *testing.T) {
	look := lookupFrom(map[string]string{})
	_, err := Load[testConfig](FromEnvFunc("", look))
	if err == nil {
		t.Fatal("Load: expected error for missing required DSN, got nil")
	}
	if !strings.Contains(err.Error(), "DSN") {
		t.Errorf("error %q does not mention DSN", err.Error())
	}
}

func TestLoadPrecedenceFileEnvOverride(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "app.json")
	// File sets Port and Host.
	if err := os.WriteFile(filePath, []byte(`{"port": 1111, "host": "from-file", "dsn": "from-file-dsn"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	// Env overrides Host only (Port left as the file's value).
	look := lookupFrom(map[string]string{
		"HOST": "from-env",
	})

	// Explicit override wins for Port.
	overrides := map[string]any{
		"PORT": 9999,
	}

	cfg, err := Load[testConfig](
		FromFile(filePath),
		FromEnvFunc("", look),
		FromMap(overrides),
	)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Port != 9999 {
		t.Errorf("Port = %d, want override 9999", cfg.Port)
	}
	if cfg.Host != "from-env" {
		t.Errorf("Host = %q, want env from-env", cfg.Host)
	}
	if cfg.DSN != "from-file-dsn" {
		t.Errorf("DSN = %q, want file value from-file-dsn (untouched by env/override)", cfg.DSN)
	}
}

type validatingConfig struct {
	Port int `env:"PORT" default:"8080"`
}

func (c validatingConfig) Validate() error {
	if c.Port < 0 {
		return errors.New("port must not be negative")
	}
	return nil
}

func TestLoadValidateHookInvoked(t *testing.T) {
	look := lookupFrom(map[string]string{"PORT": "-5"})
	_, err := Load[validatingConfig](FromEnvFunc("", look))
	if err == nil {
		t.Fatal("Load: expected validation error, got nil")
	}
	if !strings.Contains(err.Error(), "validation failed") || !strings.Contains(err.Error(), "port must not be negative") {
		t.Errorf("error %q does not surface Validate()'s message", err.Error())
	}
}

func TestLoadValidateHookPasses(t *testing.T) {
	look := lookupFrom(map[string]string{"PORT": "42"})
	cfg, err := Load[validatingConfig](FromEnvFunc("", look))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Port != 42 {
		t.Errorf("Port = %d, want 42", cfg.Port)
	}
}

type watchConfig struct {
	Value string
}

func TestWatchDetectsChangeAndSkipsNoop(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "watch.json")
	if err := os.WriteFile(filePath, []byte(`{"value": "v1"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	changes := make(chan watchConfig, 8)
	stop, err := Watch[watchConfig](filePath, 10*time.Millisecond, func(c watchConfig) {
		changes <- c
	})
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	defer stop()

	// No change: several ticks should pass with nothing on the channel.
	select {
	case c := <-changes:
		t.Fatalf("unexpected onChange before any file modification: %+v", c)
	case <-time.After(60 * time.Millisecond):
	}

	// Now change the file's content and expect exactly one notification.
	if err := os.WriteFile(filePath, []byte(`{"value": "v2"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	select {
	case c := <-changes:
		if c.Value != "v2" {
			t.Errorf("onChange value = %q, want v2", c.Value)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for onChange after file modification")
	}

	// Rewriting the same content should not fire onChange again.
	if err := os.WriteFile(filePath, []byte(`{"value": "v2"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	select {
	case c := <-changes:
		t.Fatalf("unexpected second onChange for unchanged content: %+v", c)
	case <-time.After(80 * time.Millisecond):
	}
}
