package config

import (
	"os"
	"strconv"
	"time"
)

// Config aggregates all runtime configuration options for the boilerplate.
type Config struct {
	Server    ServerConfig
	Auth      AuthConfig
	Argon2id  Argon2idConfig
	Templates TemplatesConfig
}

type ServerConfig struct {
	Address     string
	Environment string
	Port        string
}

type AuthConfig struct {
	SessionCookie string
	SessionTTL    time.Duration
	ResetTokenTTL time.Duration
}

type Argon2idConfig struct {
	Memory      uint32
	Iterations  uint32
	Parallelism uint8
	SaltLength  uint32
	KeyLength   uint32
}

type TemplatesConfig struct {
	Directory string
	Extension string
	Reload    bool
	SSR       bool
}

// Load reads configuration from environment variables with sensible defaults.
func Load() Config {
	env := getEnv("APP_ENV", "development")
	isDev := env == "development"

	port := getEnv("PORT", "8080")
	addr := getEnv("HTTP_ADDR", ":"+port)

	mem := uint32(getEnvInt("ARGON2ID_MEMORY", 64*1024))
	iter := uint32(getEnvInt("ARGON2ID_ITERATIONS", 3))
	threads := uint8(getEnvInt("ARGON2ID_PARALLELISM", 4))

	return Config{
		Server: ServerConfig{
			Address:     addr,
			Environment: env,
			Port:        port,
		},
		Auth: AuthConfig{
			SessionCookie: "ref_boilerplate_sid",
			SessionTTL:    24 * time.Hour,
			ResetTokenTTL: 15 * time.Minute,
		},
		Argon2id: Argon2idConfig{
			Memory:      mem,
			Iterations:  iter,
			Parallelism: threads,
			SaltLength:  16,
			KeyLength:   32,
		},
		Templates: TemplatesConfig{
			Directory: getEnv("TEMPLATES_DIR", "boilerplate/templates"),
			Extension: ".html",
			Reload:    isDev,
			SSR:       true,
		},
	}
}

func getEnv(key, defaultVal string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultVal
}

func getEnvInt(key string, defaultVal int) int {
	if val := os.Getenv(key); val != "" {
		if n, err := strconv.Atoi(val); err == nil {
			return n
		}
	}
	return defaultVal
}
