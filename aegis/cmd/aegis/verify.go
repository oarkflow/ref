package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/oarkflow/ref/aegis/pkg"
	"github.com/oarkflow/ref/aegis/runtime"
	"github.com/oarkflow/ref/signing"
)

// runVerify handles the `aegis verify` CLI command.
func runVerify(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("verify", flag.ContinueOnError)
	flags.SetOutput(stderr)

	var (
		pkgFlag    string
		pubkeyFlag string
	)

	flags.StringVar(&pkgFlag, "pkg", "", "Path to Aegis package container (.apkg)")
	flags.StringVar(&pubkeyFlag, "pubkey", "", "Path to trusted public key PEM or JWKS document")

	if err := flags.Parse(args); err != nil {
		return err
	}

	if pkgFlag == "" {
		return errors.New("aegis: verify: package file (-pkg) is required")
	}
	if pubkeyFlag == "" {
		return errors.New("aegis: verify: public key file (-pubkey) is required")
	}

	// 1. Read package
	pkgData, err := os.ReadFile(pkgFlag)
	if err != nil {
		return fmt.Errorf("aegis: verify: failed to read package %q: %w", pkgFlag, err)
	}
	sealedPkg, err := pkg.Unmarshal(pkgData)
	if err != nil {
		return fmt.Errorf("aegis: verify: failed to decode package %q: %w", pkgFlag, err)
	}

	// 2. Load trusted public keys
	trustedKeys, err := loadPublicKey(pubkeyFlag)
	if err != nil {
		return fmt.Errorf("aegis: verify: failed to load public key: %w", err)
	}

	// 3. Verify package
	reader := pkg.NewReader(trustedKeys)
	if err := reader.Verify(sealedPkg); err != nil {
		return fmt.Errorf("aegis: verify: verification failed: %w", err)
	}

	fmt.Fprintf(stdout, "Package verification SUCCEEDED:\n")
	fmt.Fprintf(stdout, "  Status:       VALID\n")
	fmt.Fprintf(stdout, "  Package ID:   %s\n", sealedPkg.Manifest.PackageID)
	fmt.Fprintf(stdout, "  Key ID:       %s\n", sealedPkg.Signature.KeyID)
	fmt.Fprintf(stdout, "  Payload Hash: %s (verified)\n", sealedPkg.Manifest.PayloadHash)
	fmt.Fprintf(stdout, "  Plaintext:    %s\n", sealedPkg.Manifest.PlaintextHash)
	return nil
}

// loadPublicKey parses a KeySet from a PEM public key or JWKS file/string.
func loadPublicKey(pathOrContent string) (*signing.KeySet, error) {
	trimmed := strings.TrimSpace(pathOrContent)
	if trimmed == "" {
		return nil, errors.New("aegis: public key cannot be empty")
	}

	var data []byte
	if fileInfo, err := os.Stat(trimmed); err == nil && !fileInfo.IsDir() {
		fileBytes, err := os.ReadFile(trimmed)
		if err != nil {
			return nil, fmt.Errorf("aegis: failed to read public key file %q: %w", trimmed, err)
		}
		data = fileBytes
	} else {
		data = []byte(trimmed)
	}

	trimmedData := bytes.TrimSpace(data)

	// Try single PEM public key first (computes RFC 7638 thumbprint key ID)
	if key, err := signing.ParsePublicKeyPEM("", "", trimmedData); err == nil {
		return signing.NewKeySet(nil, key)
	}

	// Try JWKS if it looks like JSON
	if len(trimmedData) > 0 && trimmedData[0] == '{' {
		if ks, err := signing.ParseJWKS(trimmedData); err == nil {
			return ks, nil
		}
	}

	// Try multiple PEM blocks
	if ks, err := runtime.ParseTrustedKeysFromPEM(trimmedData); err == nil && ks != nil {
		return ks, nil
	}

	return nil, errors.New("aegis: failed to parse public key as PEM or JWKS")
}
