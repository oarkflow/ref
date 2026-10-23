package pkg

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
)

// This file resolves a -key/-enckey command-line flag into a 32-byte AES
// key. It is shared by every command that takes one — `aegis package`,
// `aegis run` and aegis-launch — so the three forms a key can take (a key
// file, a 64-character hex string, or an operator passphrase run through
// DeriveKey) are defined in exactly one place.

// TryDirectKey resolves trimmed as a key file or a 64-character hex string:
// the two forms whose bytes are used as the AES key directly, with no
// passphrase derivation. It reports ok=false only when trimmed is bare text
// that is neither — the case the caller must treat as a passphrase instead.
//
// A key file that is not exactly 32 raw bytes or 64 hex characters is hashed
// with SHA-256 to fold it to 32 bytes. That is a key FILE, not a human
// passphrase: the attacker's task is to steal the file, not guess its
// contents, so this does not need Argon2id's cost.
func TryDirectKey(trimmed string) (key []byte, ok bool, err error) {
	if fileInfo, statErr := os.Stat(trimmed); statErr == nil && !fileInfo.IsDir() {
		data, readErr := os.ReadFile(trimmed)
		if readErr != nil {
			return nil, true, fmt.Errorf("pkg: failed to read key file %q: %w", trimmed, readErr)
		}
		dataStr := strings.TrimSpace(string(data))
		if len(dataStr) == 64 {
			if decoded, decErr := hex.DecodeString(dataStr); decErr == nil && len(decoded) == 32 {
				return decoded, true, nil
			}
		}
		if len(data) == 32 {
			k := make([]byte, 32)
			copy(k, data)
			return k, true, nil
		}
		hash := sha256.Sum256(data)
		return hash[:], true, nil
	}
	if len(trimmed) == 64 {
		if decoded, decErr := hex.DecodeString(trimmed); decErr == nil && len(decoded) == 32 {
			return decoded, true, nil
		}
	}
	return nil, false, nil
}

// ErrPassphraseNeedsKDF marks input that TryDirectKey could not resolve:
// bare text that must go through DeriveKey instead, via
// ResolvePackagingKey or ResolveRunKey. ParseEncryptionKey does not derive a
// key on its own so that nothing can reach this fallback without a stored,
// reusable salt.
var ErrPassphraseNeedsKDF = errors.New("pkg: this is not a key file or a 64-character hex key; it looks like a passphrase, which needs ResolvePackagingKey or ResolveRunKey so its salt is recorded and reused")

// ParseEncryptionKey extracts a 32-byte symmetric key from a hex string or a
// file path. A bare passphrase is refused with ErrPassphraseNeedsKDF; use
// ResolvePackagingKey (packaging) or ResolveRunKey (running) for that case.
func ParseEncryptionKey(input string) ([]byte, error) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return nil, errors.New("pkg: encryption key cannot be empty")
	}
	key, ok, err := TryDirectKey(trimmed)
	if err != nil {
		return nil, err
	}
	if ok {
		return key, nil
	}
	return nil, ErrPassphraseNeedsKDF
}

// ResolvePackagingKey resolves a packaging-time -enckey flag. A key file or
// a 64-character hex string is used as given. Anything else is an operator
// passphrase, derived into a key with Argon2id and a freshly generated
// salt; the caller must store the returned KDFInfo in the package
// (Builder.SetKDF) so the run side can reproduce the same key from the same
// passphrase.
func ResolvePackagingKey(input string) ([]byte, *KDFInfo, error) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return nil, nil, errors.New("pkg: encryption key cannot be empty")
	}
	if key, ok, err := TryDirectKey(trimmed); err != nil {
		return nil, nil, err
	} else if ok {
		return key, nil, nil
	}
	key, info, err := DeriveKey(trimmed, KDFInfo{})
	if err != nil {
		return nil, nil, fmt.Errorf("pkg: derive key from passphrase: %w", err)
	}
	return key, &info, nil
}

// ResolveRunKey resolves a run-time -key flag. When kdf is non-nil, the
// package's manifest says its key came from a passphrase, so input is
// treated as that passphrase and derived with the manifest's own salt and
// cost — reproducing the exact key regardless of what the string looks
// like. Otherwise input is a key file or hex string, resolved the same way
// ResolvePackagingKey resolves one.
func ResolveRunKey(input string, kdf *KDFInfo) ([]byte, error) {
	if kdf != nil {
		key, _, err := DeriveKey(input, *kdf)
		if err != nil {
			return nil, fmt.Errorf("pkg: derive key from passphrase: %w", err)
		}
		return key, nil
	}
	return ParseEncryptionKey(input)
}
