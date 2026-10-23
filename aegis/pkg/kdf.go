package pkg

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"

	"golang.org/x/crypto/argon2"
)

// KDFInfo describes how a package's AES key was derived from an operator's
// passphrase. It belongs in the Manifest: the salt and cost parameters are
// not secret, and the run side must use exactly these to reproduce the key
// the package side derived from the same passphrase. A package keyed
// directly (a raw 32-byte key, or a 64-character hex string) carries no
// KDFInfo at all; the key is used as given.
type KDFInfo struct {
	// Algorithm is "argon2id"; nothing else is supported.
	Algorithm string `json:"algorithm"`
	// Salt is random per package, hex-encoded. Reusing a salt across
	// packages would let an attacker who cracks one passphrase check it
	// against every other package sharing that salt for free.
	Salt string `json:"salt"`
	// TimeCost, MemoryKiB and Parallelism are Argon2id's cost parameters.
	// They travel with the package so a package made with heavier settings
	// does not silently fail to open years later against a weaker default.
	TimeCost    uint32 `json:"time_cost"`
	MemoryKiB   uint32 `json:"memory_kib"`
	Parallelism uint8  `json:"parallelism"`
}

// DefaultKDFParams are the Argon2id cost parameters used when packaging does
// not ask for different ones: heavier than OWASP's baseline recommendation,
// because the only cost that matters here is an attacker's, who is free to
// try candidate passphrases in parallel on their own hardware with no rate
// limit to stop them.
func DefaultKDFParams() KDFInfo {
	return KDFInfo{Algorithm: "argon2id", TimeCost: 3, MemoryKiB: 64 * 1024, Parallelism: 4}
}

// DeriveKey turns passphrase into a 32-byte AES key under info. When
// info.Salt is empty a random one is generated — the packaging side's case —
// and returned in the result for the manifest; the run side's case passes in
// the manifest's existing salt and parameters so the same passphrase
// reproduces the same key.
func DeriveKey(passphrase string, info KDFInfo) ([]byte, KDFInfo, error) {
	if info.Algorithm == "" {
		info.Algorithm = "argon2id"
	}
	if info.Algorithm != "argon2id" {
		return nil, info, fmt.Errorf("pkg: unsupported key derivation algorithm %q", info.Algorithm)
	}
	def := DefaultKDFParams()
	if info.TimeCost == 0 {
		info.TimeCost = def.TimeCost
	}
	if info.MemoryKiB == 0 {
		info.MemoryKiB = def.MemoryKiB
	}
	if info.Parallelism == 0 {
		info.Parallelism = def.Parallelism
	}

	var salt []byte
	if info.Salt == "" {
		salt = make([]byte, 16)
		if _, err := rand.Read(salt); err != nil {
			return nil, info, fmt.Errorf("pkg: generate kdf salt: %w", err)
		}
		info.Salt = hex.EncodeToString(salt)
	} else {
		var err error
		salt, err = hex.DecodeString(info.Salt)
		if err != nil {
			return nil, info, fmt.Errorf("pkg: kdf salt %q is not hex: %w", info.Salt, err)
		}
		if len(salt) < 8 {
			return nil, info, fmt.Errorf("pkg: kdf salt is %d bytes, too short to trust", len(salt))
		}
	}

	key := argon2.IDKey([]byte(passphrase), salt, info.TimeCost, info.MemoryKiB, info.Parallelism, 32)
	return key, info, nil
}
