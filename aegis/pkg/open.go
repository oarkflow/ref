package pkg

import (
	"crypto/aes"
	"crypto/cipher"
	"errors"
	"fmt"
)

// Open verifies and decrypts a sealed Package's payload with key: the
// payload hash (catching tampering before any decryption is attempted),
// AES-256-GCM decryption (catching a wrong key), and the plaintext hash
// (catching a key that happened to pass GCM's own authentication but is
// still not the one this package was sealed with — GCM's tag alone already
// makes this exceedingly unlikely, but the plaintext hash was already part
// of the manifest and costs nothing extra to check).
//
// Open does not check the package's signature; call Reader.Verify first
// for that, as every caller in this repository does.
func Open(p *Package, key []byte) ([]byte, error) {
	if p == nil {
		return nil, errors.New("pkg: open: package is nil")
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("pkg: open: key must be 32 bytes, got %d", len(key))
	}
	if !VerifyHash(p.Payload, p.Manifest.PayloadHash) {
		return nil, errors.New("pkg: open: payload hash mismatch (tampered)")
	}

	const nonceSize = 12
	if len(p.Payload) < nonceSize {
		return nil, errors.New("pkg: open: payload too short")
	}
	nonce := p.Payload[:nonceSize]
	ciphertext := p.Payload[nonceSize:]

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("pkg: open: initialize cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("pkg: open: initialize GCM: %w", err)
	}

	aad := []byte(p.Manifest.PackageID + "|" + p.Manifest.BuildID)
	plaintext, err := gcm.Open(nil, nonce, ciphertext, aad)
	if err != nil {
		return nil, fmt.Errorf("pkg: open: decryption failed (incorrect key or corrupted payload): %w", err)
	}

	if !VerifyHash(plaintext, p.Manifest.PlaintextHash) {
		return nil, errors.New("pkg: open: plaintext hash mismatch")
	}
	return plaintext, nil
}
