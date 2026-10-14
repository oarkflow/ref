package pkg

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"strings"
)

// SHA256Hex computes the SHA-256 digest of data and returns it as a lowercase hex string.
func SHA256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// VerifyHash checks whether the SHA-256 digest of data matches expectedHex in constant time.
// It returns false if expectedHex is malformed or does not match the digest.
func VerifyHash(data []byte, expectedHex string) bool {
	expectedBytes, err := hex.DecodeString(strings.TrimSpace(expectedHex))
	if err != nil || len(expectedBytes) != sha256.Size {
		return false
	}
	actual := sha256.Sum256(data)
	return subtle.ConstantTimeCompare(actual[:], expectedBytes) == 1
}
