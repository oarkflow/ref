package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/bcrypt"
)

var (
	ErrInvalidHash         = errors.New("argon2id: hash format is invalid")
	ErrIncompatibleVersion = errors.New("argon2id: incompatible version")
)

// Argon2idConfig defines parameters for Argon2id hashing.
// Defaults conform to OWASP Password Storage Cheat Sheet recommendations.
type Argon2idConfig struct {
	Memory      uint32 `json:"memory"`      // Memory in KiB (default: 65536 = 64 MiB)
	Iterations  uint32 `json:"iterations"`  // Number of passes (default: 3)
	Parallelism uint8  `json:"parallelism"` // Threads (default: 4)
	SaltLength  uint32 `json:"salt_length"` // Salt length in bytes (default: 16)
	KeyLength   uint32 `json:"key_length"`  // Output key length in bytes (default: 32)
}

// DefaultArgon2idConfig returns production OWASP recommendations.
func DefaultArgon2idConfig() Argon2idConfig {
	return Argon2idConfig{
		Memory:      64 * 1024, // 64 MiB
		Iterations:  3,
		Parallelism: 4,
		SaltLength:  16,
		KeyLength:   32,
	}
}

// FastArgon2idConfig returns low-cost parameters for fast unit testing.
func FastArgon2idConfig() Argon2idConfig {
	return Argon2idConfig{
		Memory:      8 * 1024, // 8 MiB
		Iterations:  1,
		Parallelism: 1,
		SaltLength:  16,
		KeyLength:   32,
	}
}

// PasswordHasher provides password hashing and timing-attack-safe verification.
type PasswordHasher struct {
	cfg       Argon2idConfig
	dummyHash string
}

// NewPasswordHasher creates a new hasher with the given configuration.
func NewPasswordHasher(cfg ...Argon2idConfig) *PasswordHasher {
	c := DefaultArgon2idConfig()
	if len(cfg) > 0 {
		c = cfg[0]
	}
	h := &PasswordHasher{cfg: c}
	// Generate a dummy hash with the current config for constant-time lookups
	dummy, err := h.Hash("dummy_password_for_timing_safety_321")
	if err != nil {
		// Fallback static dummy if generation failed (should not happen)
		dummy = "$argon2id$v=19$m=65536,t=3,p=4$c29tZXNhbHQxMjM0NTY3OA$9vX6N8yV1K4sR7vF3nP2xW0qT8bY5aL7"
	}
	h.dummyHash = dummy
	return h
}

// Hash creates a standard PHC-formatted Argon2id hash:
// $argon2id$v=19$m=65536,t=3,p=4$<salt>$<hash>
func (h *PasswordHasher) Hash(password string) (string, error) {
	salt := make([]byte, h.cfg.SaltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("failed to generate random salt: %w", err)
	}

	hash := argon2.IDKey([]byte(password), salt, h.cfg.Iterations, h.cfg.Memory, h.cfg.Parallelism, h.cfg.KeyLength)

	b64Salt := base64.RawStdEncoding.EncodeToString(salt)
	b64Hash := base64.RawStdEncoding.EncodeToString(hash)

	encoded := fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, h.cfg.Memory, h.cfg.Iterations, h.cfg.Parallelism, b64Salt, b64Hash)

	return encoded, nil
}

// Verify compares a plaintext password with a hash.
// Supports both Argon2id ($argon2id$) and legacy bcrypt ($2a$, $2b$).
// When the password or hash is empty, it runs against dummyHash to prevent timing enumeration.
func (h *PasswordHasher) Verify(password, encodedHash string) (bool, error) {
	if password == "" || encodedHash == "" {
		_, _ = h.verifyArgon2id("invalid_probe", h.dummyHash)
		return false, nil
	}

	if strings.HasPrefix(encodedHash, "$argon2id$") {
		return h.verifyArgon2id(password, encodedHash)
	}

	// Legacy bcrypt compatibility
	if strings.HasPrefix(encodedHash, "$2a$") || strings.HasPrefix(encodedHash, "$2b$") || strings.HasPrefix(encodedHash, "$2y$") {
		err := bcrypt.CompareHashAndPassword([]byte(encodedHash), []byte(password))
		return err == nil, nil
	}

	// Unknown hash format: perform dummy check to mask timing difference
	_, _ = h.verifyArgon2id(password, h.dummyHash)
	return false, ErrInvalidHash
}

// DummyVerify runs a verification against the dummy hash to equalize response times
// when a user is not found in the database.
func (h *PasswordHasher) DummyVerify(password string) {
	_, _ = h.verifyArgon2id(password, h.dummyHash)
}

func (h *PasswordHasher) verifyArgon2id(password, encodedHash string) (bool, error) {
	parts := strings.Split(encodedHash, "$")
	if len(parts) != 6 {
		return false, ErrInvalidHash
	}
	if parts[1] != "argon2id" {
		return false, ErrInvalidHash
	}

	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return false, ErrInvalidHash
	}
	if version != argon2.Version {
		return false, ErrIncompatibleVersion
	}

	params := strings.Split(parts[3], ",")
	if len(params) != 3 {
		return false, ErrInvalidHash
	}

	var memory, iterations uint32
	var parallelism uint64
	for _, p := range params {
		kv := strings.Split(p, "=")
		if len(kv) != 2 {
			return false, ErrInvalidHash
		}
		switch kv[0] {
		case "m":
			m, err := strconv.ParseUint(kv[1], 10, 32)
			if err != nil {
				return false, ErrInvalidHash
			}
			memory = uint32(m)
		case "t":
			t, err := strconv.ParseUint(kv[1], 10, 32)
			if err != nil {
				return false, ErrInvalidHash
			}
			iterations = uint32(t)
		case "p":
			var err error
			parallelism, err = strconv.ParseUint(kv[1], 10, 8)
			if err != nil {
				return false, ErrInvalidHash
			}
		default:
			return false, ErrInvalidHash
		}
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, ErrInvalidHash
	}

	expectedHash, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, ErrInvalidHash
	}

	actualHash := argon2.IDKey([]byte(password), salt, iterations, memory, uint8(parallelism), uint32(len(expectedHash)))

	if subtle.ConstantTimeCompare(actualHash, expectedHash) == 1 {
		return true, nil
	}
	return false, nil
}

// GenerateRandomToken generates a cryptographically secure random hex token.
func GenerateRandomToken(byteLength int) (string, error) {
	b := make([]byte, byteLength)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("failed to generate random token: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// HashToken hashes a plaintext token using SHA-256 for safe database storage.
func HashToken(plainToken string) string {
	sum := sha256.Sum256([]byte(plainToken))
	return hex.EncodeToString(sum[:])
}
