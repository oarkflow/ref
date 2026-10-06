package config

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// LoadDotenv loads a dotenv file (e.g. .env) line-by-line into os environment variables.
// Lines starting with # or empty lines are ignored.
// "export KEY=VAL" syntax is supported.
// Quoted values (single or double quotes) are unquoted.
// Variables that already exist in os.LookupEnv are preserved.
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

// DotenvPath returns ENV_FILE if set in the environment, or defaults to ".env".
func DotenvPath() string {
	if p := os.Getenv("ENV_FILE"); p != "" {
		return p
	}
	return ".env"
}

// LoadSecretFile reads a file (such as a mounted Kubernetes secret or Docker secret)
// and sets its contents into the specified environment variable envVar, unless that
// environment variable is already set.
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

// ResolveDir checks candidate paths in order and returns the first candidate that exists
// and is a directory as an absolute path. If none match, it returns the first candidate.
func ResolveDir(candidates ...string) string {
	for _, c := range candidates {
		if fi, err := os.Stat(c); err == nil && fi.IsDir() {
			abs, _ := filepath.Abs(c)
			return abs
		}
	}
	if len(candidates) > 0 {
		return candidates[0]
	}
	return "."
}

// Env reads an environment variable, returning fallback if key is not set or empty.
func Env(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}
