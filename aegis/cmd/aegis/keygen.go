// Package main implements the Aegis CLI commands for key generation, compilation,
// packaging, inspection, verification, and execution.
package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/oarkflow/ref/signing"
)

// runKeygen executes the keygen command to produce an Ed25519 signing key pair
// and a 32-byte AES-256 package encryption key.
func runKeygen(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("keygen", flag.ContinueOnError)
	flags.SetOutput(stderr)

	var outPrefix string
	flags.StringVar(&outPrefix, "out", "aegis", "Output file prefix for generated keys")

	if err := flags.Parse(args); err != nil {
		return err
	}

	if outPrefix == "" {
		return errors.New("aegis: keygen: output prefix (-out) cannot be empty")
	}

	// Ensure parent directory exists
	outDir := filepath.Dir(outPrefix)
	if outDir != "" && outDir != "." {
		if err := os.MkdirAll(outDir, 0755); err != nil {
			return fmt.Errorf("aegis: keygen: failed to create output directory %q: %w", outDir, err)
		}
	}

	// 1. Generate Ed25519 signing key
	signingKey, err := signing.GenerateEd25519("")
	if err != nil {
		return fmt.Errorf("aegis: keygen: failed to generate Ed25519 signing key: %w", err)
	}

	privPEM, err := signingKey.MarshalPrivateKeyPEM()
	if err != nil {
		return fmt.Errorf("aegis: keygen: failed to marshal private key PEM: %w", err)
	}

	pubPEM, err := signingKey.MarshalPublicKeyPEM()
	if err != nil {
		return fmt.Errorf("aegis: keygen: failed to marshal public key PEM: %w", err)
	}

	// 2. Generate 32-byte AES package encryption key
	aesKey := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, aesKey); err != nil {
		return fmt.Errorf("aegis: keygen: failed to generate AES encryption key: %w", err)
	}
	aesHex := hex.EncodeToString(aesKey)

	// 3. Write key files
	privFile := outPrefix + ".key"
	pubFile := outPrefix + ".pub"
	encFile := outPrefix + ".enc"

	if err := os.WriteFile(privFile, privPEM, 0600); err != nil {
		return fmt.Errorf("aegis: keygen: failed to write private key file %q: %w", privFile, err)
	}

	if err := os.WriteFile(pubFile, pubPEM, 0644); err != nil {
		return fmt.Errorf("aegis: keygen: failed to write public key file %q: %w", pubFile, err)
	}

	if err := os.WriteFile(encFile, []byte(aesHex+"\n"), 0600); err != nil {
		return fmt.Errorf("aegis: keygen: failed to write encryption key file %q: %w", encFile, err)
	}

	fmt.Fprintf(stdout, "Generated Ed25519 signing key:\n")
	fmt.Fprintf(stdout, "  Private Key: %s\n", privFile)
	fmt.Fprintf(stdout, "  Public Key:  %s\n", pubFile)
	fmt.Fprintf(stdout, "  Key ID:      %s\n", signingKey.ID)
	fmt.Fprintf(stdout, "\n")
	fmt.Fprintf(stdout, "Generated AES-256 package encryption key:\n")
	fmt.Fprintf(stdout, "  Key File:    %s\n", encFile)
	fmt.Fprintf(stdout, "  Key (Hex):   %s\n", aesHex)

	return nil
}
