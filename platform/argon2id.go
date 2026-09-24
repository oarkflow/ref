package platform

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/bcrypt"
)

// Argon2idParams holds the parameters used for Argon2id hashing.
// Defaults follow OWASP recommendations (RFC 9106 Section 4).
type Argon2idParams struct {
	Memory      uint32
	Iterations  uint32
	Parallelism uint8
	SaltLength  uint32
	KeyLength   uint32
}

// DefaultArgon2idParams returns OWASP recommended default parameters.
func DefaultArgon2idParams() Argon2idParams {
	return Argon2idParams{
		Memory:      64 * 1024, // 64 MiB
		Iterations:  3,
		Parallelism: 4,
		SaltLength:  16,
		KeyLength:   32,
	}
}

// FastArgon2idParams returns parameters suitable for testing / fast hashing.
func FastArgon2idParams() Argon2idParams {
	return Argon2idParams{
		Memory:      8 * 1024, // 8 MiB
		Iterations:  1,
		Parallelism: 1,
		SaltLength:  16,
		KeyLength:   32,
	}
}

var (
	errInvalidHash         = errors.New("argon2id: hash format is invalid")
	errIncompatibleVersion = errors.New("argon2id: incompatible version")

	// Precomputed dummy hash for timing attack mitigation during failed lookups
	dummyArgon2idHash = "$argon2id$v=19$m=8192,t=1,p=1$c29tZXNhbHQxMjM0NTY3OA$9vX6N8yV1K4sR7vF3nP2xW0qT8bY5aL7"
)

// HashPasswordArgon2id generates an Argon2id PHC-formatted string from a plaintext password.
func HashPasswordArgon2id(password string, params ...Argon2idParams) (string, error) {
	p := DefaultArgon2idParams()
	if len(params) > 0 {
		p = params[0]
	}

	salt := make([]byte, p.SaltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("argon2id: salt generation failed: %w", err)
	}

	hash := argon2.IDKey([]byte(password), salt, p.Iterations, p.Memory, p.Parallelism, p.KeyLength)

	b64Salt := base64.RawStdEncoding.EncodeToString(salt)
	b64Hash := base64.RawStdEncoding.EncodeToString(hash)

	encoded := fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, p.Memory, p.Iterations, p.Parallelism, b64Salt, b64Hash)

	return encoded, nil
}

// VerifyPassword verifies a password against either an Argon2id PHC hash or a legacy bcrypt hash.
// If the hash is unrecognized, it falls back to a dummy constant-time verification to prevent timing attacks.
func VerifyPassword(password, encodedHash string) (bool, error) {
	if strings.HasPrefix(encodedHash, "$argon2id$") {
		return verifyArgon2id(password, encodedHash)
	}
	if strings.HasPrefix(encodedHash, "$2a$") || strings.HasPrefix(encodedHash, "$2b$") || strings.HasPrefix(encodedHash, "$2y$") {
		err := bcrypt.CompareHashAndPassword([]byte(encodedHash), []byte(password))
		return err == nil, nil
	}

	// Constant-time dummy verification on invalid hash to mitigate user enumeration
	_, _ = verifyArgon2id(password, dummyArgon2idHash)
	return false, errInvalidHash
}

func verifyArgon2id(password, encodedHash string) (bool, error) {
	parts := strings.Split(encodedHash, "$")
	if len(parts) != 6 {
		return false, errInvalidHash
	}
	if parts[1] != "argon2id" {
		return false, errInvalidHash
	}

	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return false, errInvalidHash
	}
	if version != argon2.Version {
		return false, errIncompatibleVersion
	}

	params := strings.Split(parts[3], ",")
	if len(params) != 3 {
		return false, errInvalidHash
	}

	var memory, iterations uint32
	var parallelism uint64
	for _, p := range params {
		kv := strings.Split(p, "=")
		if len(kv) != 2 {
			return false, errInvalidHash
		}
		switch kv[0] {
		case "m":
			m, err := strconv.ParseUint(kv[1], 10, 32)
			if err != nil {
				return false, errInvalidHash
			}
			memory = uint32(m)
		case "t":
			t, err := strconv.ParseUint(kv[1], 10, 32)
			if err != nil {
				return false, errInvalidHash
			}
			iterations = uint32(t)
		case "p":
			var err error
			parallelism, err = strconv.ParseUint(kv[1], 10, 8)
			if err != nil {
				return false, errInvalidHash
			}
		default:
			return false, errInvalidHash
		}
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, errInvalidHash
	}

	expectedHash, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, errInvalidHash
	}

	actualHash := argon2.IDKey([]byte(password), salt, iterations, memory, uint8(parallelism), uint32(len(expectedHash)))

	if subtle.ConstantTimeCompare(actualHash, expectedHash) == 1 {
		return true, nil
	}
	return false, nil
}
