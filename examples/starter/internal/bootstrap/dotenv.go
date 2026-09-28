// Package bootstrap is process-startup wiring shared by every binary this
// starter ships (cmd/server, cmd/migrator, ...) — currently just the .env
// loader, so both read the same file the same way instead of each growing
// its own copy.
package bootstrap

import (
	"bufio"
	"os"
	"strings"
)

// LoadDotenv reads a simple KEY=VALUE .env file — comments with '#', an
// optional "export " prefix, optional single/double quotes around the
// value — and calls os.Setenv for each key the process doesn't already
// have. A real environment variable (from the shell, Docker, systemd, CI,
// a secrets manager, ...) always wins over the file; it is never the other
// way around, so committing a .env to a repo and deploying with real env
// vars set can never have the file silently override production.
//
// A missing file is not an error: .env is a local-development convenience,
// and its absence everywhere else is the normal case, not a
// misconfiguration.
func LoadDotenv(path string) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		value = unquote(strings.TrimSpace(value))
		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		if err := os.Setenv(key, value); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func unquote(value string) string {
	if len(value) < 2 {
		return value
	}
	first, last := value[0], value[len(value)-1]
	if (first == '"' && last == '"') || (first == '\'' && last == '\'') {
		return value[1 : len(value)-1]
	}
	return value
}

// DotenvPath is ".env" in the current working directory, or ENV_FILE when
// set — checked against the real process environment, never the file
// itself, so there's no chicken-and-egg problem picking which file to load.
func DotenvPath() string {
	if p := os.Getenv("ENV_FILE"); p != "" {
		return p
	}
	return ".env"
}

// LoadSecretFile reads a single secret's raw value from path and calls
// os.Setenv(envVar, ...) — but only when envVar is not already set. A real
// environment variable always wins, exactly as LoadDotenv's does; this is
// the same rule applied to a file a secrets-manager sidecar (a Vault Agent
// template, the AWS/GCP Secrets Manager CSI driver, ...) writes instead of
// a human-edited .env.
//
// A missing file is not an error, for the same reason LoadDotenv's is not:
// this path is a local-development or single-secrets-manager convenience,
// and its absence — SESSION_SECRET set directly by the platform, say — is
// the normal case everywhere else.
//
// Trailing newlines are trimmed: `echo` (unlike `printf`) appends one, and a
// secret ending in "\n" would otherwise silently fail the 32-byte minimum a
// caller like the session resource enforces, or just not match what was
// actually written to the secrets manager.
func LoadSecretFile(envVar, path string) error {
	if _, exists := os.LookupEnv(envVar); exists {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	value := strings.TrimRight(string(data), "\r\n")
	if value == "" {
		return nil
	}
	return os.Setenv(envVar, value)
}
