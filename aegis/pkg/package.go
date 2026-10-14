package pkg

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/oarkflow/ref/aegis/ir"
	"github.com/oarkflow/ref/signing"
)

// Supported cryptography and algorithm constants.
const (
	// AlgorithmSHA256 identifies the SHA-256 digest algorithm.
	AlgorithmSHA256 = "SHA-256"

	// AlgorithmAES256GCM identifies the AES-256-GCM authenticated encryption algorithm.
	AlgorithmAES256GCM = "AES-256-GCM"

	// KeyDerivationHKDF identifies the HKDF-SHA256 key derivation function.
	KeyDerivationHKDF = "HKDF-SHA256"
)

// ProtectionLevel specifies the execution environment hardening and isolation level.
type ProtectionLevel string

const (
	// LevelStandard represents standard runtime execution.
	LevelStandard ProtectionLevel = "standard"

	// LevelHardened represents memory-isolated and diversified execution.
	LevelHardened ProtectionLevel = "hardened"

	// LevelIsolated represents hardware-isolated or enclave execution.
	LevelIsolated ProtectionLevel = "isolated"
)

// ExecutionPolicy declares per-function protection requirements.
type ExecutionPolicy struct {
	// Default specifies the fallback protection level for unlisted functions.
	Default ProtectionLevel `json:"default"`

	// Functions maps specific function names or signatures to their required protection level.
	Functions map[string]ProtectionLevel `json:"functions,omitempty"`
}

// Manifest describes the package contents, cryptographic hashes, and authorization requirements.
type Manifest struct {
	// Identity
	PackageID     string    `json:"package_id"`
	ApplicationID string    `json:"application_id"`
	BuildID       string    `json:"build_id"`
	Version       string    `json:"version"`
	CreatedAt     time.Time `json:"created_at"`

	// Integrity
	PayloadHash   string `json:"payload_hash"`   // SHA-256 hex of encrypted payload
	PlaintextHash string `json:"plaintext_hash"` // SHA-256 hex of IR before encryption
	HashAlgorithm string `json:"hash_algorithm"` // "SHA-256"

	// Compatibility
	MinRuntimeVersion    string   `json:"min_runtime_version"`
	IRVersion            uint32   `json:"ir_version"`
	RequiredCapabilities []string `json:"required_capabilities,omitempty"`

	// Execution policy
	ExecutionPolicy ExecutionPolicy `json:"execution_policy"`

	// Encryption metadata (NOT the key itself)
	EncryptionAlgorithm string `json:"encryption_algorithm"` // "AES-256-GCM"
	KeyDerivation       string `json:"key_derivation"`       // "HKDF-SHA256"
}

// Package is a sealed, signed unit of protected code containing encrypted IR and metadata.
type Package struct {
	// Manifest holds package metadata, authorization rules, and cryptographic digests.
	Manifest Manifest `json:"manifest"`

	// Payload contains the encrypted IR module bytes (12-byte nonce followed by AES-GCM ciphertext).
	Payload []byte `json:"payload"`

	// Signature is the detached signature over the canonical JSON representation of Manifest.
	Signature signing.Signature `json:"signature"`
}

// Builder constructs signed and encrypted packages from IR modules.
type Builder struct {
	applicationID        string
	version              string
	keySet               *signing.KeySet
	encryptionKey        []byte // 32 bytes for AES-256
	policy               ExecutionPolicy
	runtimeVersion       string
	requiredCapabilities []string
	packageID            string
}

// NewBuilder initializes a new Package Builder with the specified application ID, version,
// signing key set, and 32-byte encryption key.
func NewBuilder(appID, version string, ks *signing.KeySet, encKey []byte) (*Builder, error) {
	if appID == "" {
		return nil, errors.New("pkg: application ID cannot be empty")
	}
	if version == "" {
		return nil, errors.New("pkg: version cannot be empty")
	}
	if ks == nil {
		return nil, errors.New("pkg: key set cannot be nil")
	}
	if len(encKey) != 32 {
		return nil, fmt.Errorf("pkg: encryption key must be 32 bytes, got %d", len(encKey))
	}

	keyCopy := make([]byte, 32)
	copy(keyCopy, encKey)

	return &Builder{
		applicationID: appID,
		version:       version,
		keySet:        ks,
		encryptionKey: keyCopy,
		policy: ExecutionPolicy{
			Default: LevelStandard,
		},
	}, nil
}

// SetPolicy configures the execution policy requirements for the package.
func (b *Builder) SetPolicy(p ExecutionPolicy) {
	b.policy = p
}

// SetMinRuntimeVersion configures the minimum required Aegis runtime version.
func (b *Builder) SetMinRuntimeVersion(v string) {
	b.runtimeVersion = v
}

// SetRequiredCapabilities configures optional capability identifiers required by the package.
func (b *Builder) SetRequiredCapabilities(caps []string) {
	b.requiredCapabilities = caps
}

// SetPackageID configures an explicit package identifier (if unset, a random UUID is generated).
func (b *Builder) SetPackageID(id string) {
	b.packageID = id
}

// Build validates, serializes, encrypts, and signs an IR module into a sealed Package.
func (b *Builder) Build(module *ir.Module) (*Package, error) {
	if module == nil {
		return nil, errors.New("pkg: module cannot be nil")
	}

	// 1. Validate the module using ir.Validate
	if err := ir.Validate(module); err != nil {
		return nil, fmt.Errorf("pkg: invalid module: %w", err)
	}

	// 2. Encode the module using ir.Encode
	plaintext, err := ir.Encode(module)
	if err != nil {
		return nil, fmt.Errorf("pkg: failed to encode module: %w", err)
	}

	// 3. SHA-256 hash the plaintext
	plaintextHash := SHA256Hex(plaintext)

	// 4. Generate random 12-byte nonce
	nonce := make([]byte, 12)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("pkg: failed to generate nonce: %w", err)
	}

	// Package ID & Build ID
	pkgID := b.packageID
	if pkgID == "" {
		id, err := newUUID()
		if err != nil {
			return nil, fmt.Errorf("pkg: failed to generate package ID: %w", err)
		}
		pkgID = id
	}
	buildID := hex.EncodeToString(module.BuildID[:])

	// 5. AES-256-GCM encrypt with AAD = PackageID + "|" + BuildID (use hex of module.BuildID)
	block, err := aes.NewCipher(b.encryptionKey)
	if err != nil {
		return nil, fmt.Errorf("pkg: failed to initialize cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("pkg: failed to initialize GCM: %w", err)
	}
	aad := []byte(pkgID + "|" + buildID)
	ciphertext := gcm.Seal(nil, nonce, plaintext, aad)

	// 6. Prepend nonce to ciphertext (nonce || ciphertext)
	payload := make([]byte, len(nonce)+len(ciphertext))
	copy(payload, nonce)
	copy(payload[len(nonce):], ciphertext)

	// 7. SHA-256 hash the encrypted payload
	payloadHash := SHA256Hex(payload)

	// 8. Build Manifest with all fields
	policy := b.policy
	if policy.Default == "" {
		policy.Default = LevelStandard
	}

	manifest := Manifest{
		PackageID:            pkgID,
		ApplicationID:        b.applicationID,
		BuildID:              buildID,
		Version:              b.version,
		CreatedAt:            time.Now().UTC().Truncate(time.Second),
		PayloadHash:          payloadHash,
		PlaintextHash:        plaintextHash,
		HashAlgorithm:        AlgorithmSHA256,
		MinRuntimeVersion:    b.runtimeVersion,
		IRVersion:            module.Version,
		RequiredCapabilities: b.requiredCapabilities,
		ExecutionPolicy:      policy,
		EncryptionAlgorithm:  AlgorithmAES256GCM,
		KeyDerivation:        KeyDerivationHKDF,
	}

	// 9. Canonicalize the manifest JSON (use signing.Canonical or json.Marshal with sorted keys)
	manifestBytes, err := signing.Canonical(manifest)
	if err != nil {
		return nil, fmt.Errorf("pkg: failed to canonicalize manifest: %w", err)
	}

	// 10. Sign the canonical manifest using the KeySet
	sig, err := b.keySet.Sign(manifestBytes)
	if err != nil {
		return nil, fmt.Errorf("pkg: failed to sign manifest: %w", err)
	}

	// 11. Return the Package
	return &Package{
		Manifest:  manifest,
		Payload:   payload,
		Signature: sig,
	}, nil
}

// Reader verifies and decrypts packages using trusted verification keys.
type Reader struct {
	trustedKeys *signing.KeySet
}

// NewReader creates a package Reader configured with a set of trusted verification keys.
func NewReader(trustedKeys *signing.KeySet) *Reader {
	return &Reader{trustedKeys: trustedKeys}
}

// Verify validates the package signature, payload hash, and format version compatibility.
// It returns nil if the package is genuine and unmodified.
func (r *Reader) Verify(pkg *Package) error {
	if pkg == nil {
		return errors.New("pkg: package cannot be nil")
	}
	if r.trustedKeys == nil {
		return errors.New("pkg: no trusted keys configured")
	}

	// 1. Canonicalize the manifest
	manifestBytes, err := signing.Canonical(pkg.Manifest)
	if err != nil {
		return fmt.Errorf("pkg: failed to canonicalize manifest: %w", err)
	}

	// 2. Verify the signature against trusted keys
	if err := r.trustedKeys.Verify(manifestBytes, pkg.Signature); err != nil {
		return fmt.Errorf("pkg: signature verification failed: %w", err)
	}

	// 3. Verify PayloadHash matches SHA-256 of Payload
	if !VerifyHash(pkg.Payload, pkg.Manifest.PayloadHash) {
		return errors.New("pkg: payload hash mismatch")
	}

	// 4. Verify IRVersion is supported
	if pkg.Manifest.IRVersion != ir.Version {
		return fmt.Errorf("pkg: unsupported IR version %d, expected %d", pkg.Manifest.IRVersion, ir.Version)
	}

	return nil
}

// Decrypt verifies the package, decrypts the payload, checks plaintext integrity against
// the manifest PlaintextHash, and decodes the IR Module.
func (r *Reader) Decrypt(pkg *Package, key []byte) (*ir.Module, error) {
	// 1. Call Verify first
	if err := r.Verify(pkg); err != nil {
		return nil, err
	}

	if len(key) != 32 {
		return nil, fmt.Errorf("pkg: encryption key must be 32 bytes, got %d", len(key))
	}

	const nonceSize = 12
	if len(pkg.Payload) < nonceSize {
		return nil, errors.New("pkg: payload too short")
	}

	// 2. Split payload into nonce (first 12 bytes) and ciphertext
	nonce := pkg.Payload[:nonceSize]
	ciphertext := pkg.Payload[nonceSize:]

	// 3. AES-256-GCM decrypt with same AAD
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("pkg: failed to initialize cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("pkg: failed to initialize GCM: %w", err)
	}

	aad := []byte(pkg.Manifest.PackageID + "|" + pkg.Manifest.BuildID)
	plaintext, err := gcm.Open(nil, nonce, ciphertext, aad)
	if err != nil {
		return nil, fmt.Errorf("pkg: decryption failed: %w", err)
	}

	// 4. Verify PlaintextHash matches SHA-256 of decrypted data
	if !VerifyHash(plaintext, pkg.Manifest.PlaintextHash) {
		return nil, errors.New("pkg: plaintext hash mismatch")
	}

	// 5. Decode using ir.Decode
	module, err := ir.Decode(plaintext)
	if err != nil {
		return nil, fmt.Errorf("pkg: decode module failed: %w", err)
	}

	// 6. Return the Module
	return module, nil
}

// newUUID generates a random RFC 4122 version 4 UUID.
func newUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40 // Version 4
	b[8] = (b[8] & 0x3f) | 0x80 // RFC 4122 variant
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}
