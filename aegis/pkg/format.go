package pkg

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sync"

	"github.com/oarkflow/ref/signing"
)

// bufferPool recycles bytes.Buffer instances for binary serialization hot paths.
var bufferPool = sync.Pool{
	New: func() any {
		return new(bytes.Buffer)
	},
}

func getPooledBuffer() *bytes.Buffer {
	b := bufferPool.Get().(*bytes.Buffer)
	b.Reset()
	return b
}

func putPooledBuffer(b *bytes.Buffer) {
	if b.Cap() > 128*1024 {
		return
	}
	b.Reset()
	bufferPool.Put(b)
}

// Marshal encodes a Package into its binary container format:
//
//	Magic:        4 bytes "APKG"
//	Version:      4 bytes uint32 little-endian
//	ManifestLen:  4 bytes uint32 little-endian
//	ManifestJSON: ManifestLen bytes (canonical JSON)
//	SignatureLen: 4 bytes uint32 little-endian
//	SignatureJSON: SignatureLen bytes
//	PayloadLen:   4 bytes uint32 little-endian
//	Payload:      PayloadLen bytes
func Marshal(pkg *Package) ([]byte, error) {
	if pkg == nil {
		return nil, errors.New("pkg: cannot marshal nil package")
	}

	manifestJSON, err := signing.Canonical(pkg.Manifest)
	if err != nil {
		return nil, fmt.Errorf("pkg: failed to marshal manifest: %w", err)
	}

	sigJSON, err := json.Marshal(pkg.Signature)
	if err != nil {
		return nil, fmt.Errorf("pkg: failed to marshal signature: %w", err)
	}

	if len(manifestJSON) > math.MaxUint32 {
		return nil, errors.New("pkg: manifest JSON exceeds max uint32 size")
	}
	if len(sigJSON) > math.MaxUint32 {
		return nil, errors.New("pkg: signature JSON exceeds max uint32 size")
	}
	if len(pkg.Payload) > math.MaxUint32 {
		return nil, errors.New("pkg: payload exceeds max uint32 size")
	}

	buf := getPooledBuffer()
	defer putPooledBuffer(buf)

	// Magic: 4 bytes "APKG"
	buf.WriteString(Magic)

	var lenBuf [4]byte

	// Version: 4 bytes uint32 LE
	binary.LittleEndian.PutUint32(lenBuf[:], uint32(FormatVersion))
	buf.Write(lenBuf[:])

	// ManifestLen: 4 bytes uint32 LE
	binary.LittleEndian.PutUint32(lenBuf[:], uint32(len(manifestJSON)))
	buf.Write(lenBuf[:])
	buf.Write(manifestJSON)

	// SignatureLen: 4 bytes uint32 LE
	binary.LittleEndian.PutUint32(lenBuf[:], uint32(len(sigJSON)))
	buf.Write(lenBuf[:])
	buf.Write(sigJSON)

	// PayloadLen: 4 bytes uint32 LE
	binary.LittleEndian.PutUint32(lenBuf[:], uint32(len(pkg.Payload)))
	buf.Write(lenBuf[:])
	buf.Write(pkg.Payload)

	out := make([]byte, buf.Len())
	copy(out, buf.Bytes())
	return out, nil
}

// Unmarshal decodes a Package from the binary container format, validating the magic header and version.
func Unmarshal(data []byte) (*Package, error) {
	if len(data) < 20 {
		return nil, errors.New("pkg: binary data too short (minimum 20 bytes required)")
	}

	// Magic: 4 bytes "APKG"
	if string(data[:4]) != Magic {
		return nil, fmt.Errorf("pkg: invalid magic header %q, expected %q", string(data[:4]), Magic)
	}

	// Version: 4 bytes uint32 LE
	version := binary.LittleEndian.Uint32(data[4:8])
	if version != FormatVersion {
		return nil, fmt.Errorf("pkg: unsupported format version %d, expected %d", version, FormatVersion)
	}

	offset := 8

	// ManifestLen: 4 bytes uint32 LE
	if offset+4 > len(data) {
		return nil, errors.New("pkg: truncated manifest length")
	}
	manifestLen := int(binary.LittleEndian.Uint32(data[offset : offset+4]))
	offset += 4

	if offset+manifestLen > len(data) {
		return nil, errors.New("pkg: truncated manifest JSON")
	}
	manifestJSON := data[offset : offset+manifestLen]
	offset += manifestLen

	var manifest Manifest
	if err := json.Unmarshal(manifestJSON, &manifest); err != nil {
		return nil, fmt.Errorf("pkg: failed to decode manifest JSON: %w", err)
	}

	// SignatureLen: 4 bytes uint32 LE
	if offset+4 > len(data) {
		return nil, errors.New("pkg: truncated signature length")
	}
	sigLen := int(binary.LittleEndian.Uint32(data[offset : offset+4]))
	offset += 4

	if offset+sigLen > len(data) {
		return nil, errors.New("pkg: truncated signature JSON")
	}
	sigJSON := data[offset : offset+sigLen]
	offset += sigLen

	var sig signing.Signature
	if err := json.Unmarshal(sigJSON, &sig); err != nil {
		return nil, fmt.Errorf("pkg: failed to decode signature JSON: %w", err)
	}

	// PayloadLen: 4 bytes uint32 LE
	if offset+4 > len(data) {
		return nil, errors.New("pkg: truncated payload length")
	}
	payloadLen := int(binary.LittleEndian.Uint32(data[offset : offset+4]))
	offset += 4

	if offset+payloadLen > len(data) {
		return nil, errors.New("pkg: truncated payload data")
	}
	payload := make([]byte, payloadLen)
	copy(payload, data[offset:offset+payloadLen])
	offset += payloadLen

	if offset != len(data) {
		return nil, fmt.Errorf("pkg: unexpected trailing data (%d bytes)", len(data)-offset)
	}

	return &Package{
		Manifest:  manifest,
		Payload:   payload,
		Signature: sig,
	}, nil
}
