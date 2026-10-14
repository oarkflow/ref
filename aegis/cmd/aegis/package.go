package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/oarkflow/ref/aegis/ir"
	"github.com/oarkflow/ref/aegis/pkg"
	"github.com/oarkflow/ref/aegis/policy"
	"github.com/oarkflow/ref/signing"
)

// runPackage handles the `aegis package` CLI command.
func runPackage(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("package", flag.ContinueOnError)
	flags.SetOutput(stderr)

	var (
		moduleFlag string
		dirFlag    string
		keyFlag    string
		encKeyFlag string
		outFlag    string
		appIDFlag  string
		verFlag    string
		policyFlag string
		capFlag    string
	)

	flags.StringVar(&moduleFlag, "module", "", "Path to input Aegis IR binary module (.air)")
	flags.StringVar(&dirFlag, "dir", "", "Path to application directory (e.g. ./examples/smsgateway)")
	flags.StringVar(&keyFlag, "key", "", "Path to Ed25519 signing key PEM file")
	flags.StringVar(&encKeyFlag, "enckey", "", "AES-256 package encryption key (hex, key file, or passphrase)")
	flags.StringVar(&outFlag, "out", "", "Path to output Aegis package container (.apkg)")
	flags.StringVar(&appIDFlag, "appid", "aegis.app", "Logical application identifier")
	flags.StringVar(&verFlag, "ver", "1.0.0", "Package semantic version")
	flags.StringVar(&policyFlag, "policy", "", "Optional path to policy JSON document")
	flags.StringVar(&capFlag, "cap", "", "Optional comma-separated list of required capabilities")

	if err := flags.Parse(args); err != nil {
		return err
	}

	if moduleFlag == "" && dirFlag == "" {
		return errors.New("aegis: package: either -module (IR file) or -dir (application directory) is required")
	}
	if keyFlag == "" {
		return errors.New("aegis: package: -key is required")
	}
	if encKeyFlag == "" {
		return errors.New("aegis: package: -enckey is required")
	}
	if outFlag == "" {
		return errors.New("aegis: package: -out is required")
	}

	// 1. Parse signing key and build key set
	signingKey, err := parsePrivateKey(keyFlag)
	if err != nil {
		return fmt.Errorf("aegis: package: failed to load signing key: %w", err)
	}
	keySet, err := signing.NewKeySet(signingKey)
	if err != nil {
		return fmt.Errorf("aegis: package: failed to create key set: %w", err)
	}

	// 2. Resolve 32-byte encryption key
	encKey, err := parseEncryptionKey(encKeyFlag)
	if err != nil {
		return fmt.Errorf("aegis: package: invalid encryption key: %w", err)
	}

	// 3. Create package builder
	builder, err := pkg.NewBuilder(appIDFlag, verFlag, keySet, encKey)
	if err != nil {
		return fmt.Errorf("aegis: package: failed to create builder: %w", err)
	}

	// 4. Apply optional policy
	if policyFlag != "" {
		pol, err := policy.ParseFile(policyFlag)
		if err != nil {
			return fmt.Errorf("aegis: package: failed to load policy file %q: %w", policyFlag, err)
		}
		execPolicy := pkg.ExecutionPolicy{
			Default:   pkg.ProtectionLevel(pol.Protection.Default),
			Functions: make(map[string]pkg.ProtectionLevel),
		}
		for fnName, fp := range pol.Functions {
			execPolicy.Functions[fnName] = pkg.ProtectionLevel(fp.Execution)
		}
		builder.SetPolicy(execPolicy)
	}

	// 5. Apply optional required capabilities
	if capFlag != "" {
		var caps []string
		for _, c := range strings.Split(capFlag, ",") {
			trimmed := strings.TrimSpace(c)
			if trimmed != "" {
				caps = append(caps, trimmed)
			}
		}
		builder.SetRequiredCapabilities(caps)
	}

	var sealedPkg *pkg.Package
	if dirFlag != "" {
		// Package an entire REF application directory into an encrypted container
		absDir, err := filepath.Abs(dirFlag)
		if err != nil {
			return fmt.Errorf("aegis: package: failed to resolve directory: %w", err)
		}
		zipBytes, err := zipAppDir(absDir)
		if err != nil {
			return fmt.Errorf("aegis: package: failed to archive directory %q: %w", dirFlag, err)
		}
		sp, err := builder.BuildPayload(zipBytes, "", ir.Version)
		if err != nil {
			return fmt.Errorf("aegis: package: failed to build application package: %w", err)
		}
		sealedPkg = sp
	} else {
		// Package an Aegis IR module
		modBytes, err := os.ReadFile(moduleFlag)
		if err != nil {
			return fmt.Errorf("aegis: package: failed to read module file %q: %w", moduleFlag, err)
		}
		module, err := ir.Decode(modBytes)
		if err != nil {
			return fmt.Errorf("aegis: package: failed to decode module %q: %w", moduleFlag, err)
		}
		sp, err := builder.Build(module)
		if err != nil {
			return fmt.Errorf("aegis: package: failed to build module package: %w", err)
		}
		sealedPkg = sp
	}

	// 8. Marshal package into binary container format
	pkgBytes, err := pkg.Marshal(sealedPkg)
	if err != nil {
		return fmt.Errorf("aegis: package: failed to serialize package: %w", err)
	}

	// 9. Write output file
	outDir := filepath.Dir(outFlag)
	if outDir != "" && outDir != "." {
		if err := os.MkdirAll(outDir, 0755); err != nil {
			return fmt.Errorf("aegis: package: failed to create output directory %q: %w", outDir, err)
		}
	}

	if err := os.WriteFile(outFlag, pkgBytes, 0644); err != nil {
		return fmt.Errorf("aegis: package: failed to write package file %q: %w", outFlag, err)
	}

	fmt.Fprintf(stdout, "Package created successfully:\n")
	fmt.Fprintf(stdout, "  Output:        %s\n", outFlag)
	fmt.Fprintf(stdout, "  Package ID:    %s\n", sealedPkg.Manifest.PackageID)
	fmt.Fprintf(stdout, "  Application:   %s\n", sealedPkg.Manifest.ApplicationID)
	fmt.Fprintf(stdout, "  Version:       %s\n", sealedPkg.Manifest.Version)
	fmt.Fprintf(stdout, "  Build ID:      %s\n", sealedPkg.Manifest.BuildID)
	fmt.Fprintf(stdout, "  Payload Hash:  %s\n", sealedPkg.Manifest.PayloadHash)
	fmt.Fprintf(stdout, "  Signature Alg: %s\n", sealedPkg.Signature.Algorithm)
	fmt.Fprintf(stdout, "  Key ID:        %s\n", sealedPkg.Signature.KeyID)
	return nil
}

// parsePrivateKey parses a signing Key from a PEM private key file or string.
func parsePrivateKey(pathOrContent string) (*signing.Key, error) {
	trimmed := strings.TrimSpace(pathOrContent)
	if trimmed == "" {
		return nil, errors.New("aegis: signing key cannot be empty")
	}

	var data []byte
	if fileInfo, err := os.Stat(trimmed); err == nil && !fileInfo.IsDir() {
		fileBytes, err := os.ReadFile(trimmed)
		if err != nil {
			return nil, fmt.Errorf("aegis: failed to read signing key file %q: %w", trimmed, err)
		}
		data = fileBytes
	} else {
		data = []byte(trimmed)
	}

	key, err := signing.ParsePrivateKeyPEM("", signing.EdDSA, data)
	if err != nil {
		key, err = signing.ParseKeyPEM("", signing.EdDSA, data)
		if err != nil {
			return nil, fmt.Errorf("aegis: failed to parse private key PEM: %w", err)
		}
	}

	if !key.CanSign() {
		return nil, errors.New("aegis: key holds no private signing material")
	}

	return key, nil
}

// parseEncryptionKey extracts a 32-byte symmetric key from a hex string, file path, or passphrase.
func parseEncryptionKey(input string) ([]byte, error) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return nil, errors.New("aegis: encryption key cannot be empty")
	}

	// 1. Check if input is a path to an existing file
	if fileInfo, err := os.Stat(trimmed); err == nil && !fileInfo.IsDir() {
		data, err := os.ReadFile(trimmed)
		if err != nil {
			return nil, fmt.Errorf("aegis: failed to read key file %q: %w", trimmed, err)
		}
		dataStr := strings.TrimSpace(string(data))
		if len(dataStr) == 64 {
			if decoded, err := hex.DecodeString(dataStr); err == nil && len(decoded) == 32 {
				return decoded, nil
			}
		}
		if len(data) == 32 {
			key := make([]byte, 32)
			copy(key, data)
			return key, nil
		}
		hash := sha256.Sum256(data)
		return hash[:], nil
	}

	// 2. Check if input is a 64-character hex string
	if len(trimmed) == 64 {
		if decoded, err := hex.DecodeString(trimmed); err == nil && len(decoded) == 32 {
			return decoded, nil
		}
	}

	// 3. Otherwise treat input as a passphrase and derive a 32-byte key via SHA-256
	hash := sha256.Sum256([]byte(trimmed))
	return hash[:], nil
}

// zipAppDir recursively packages an application directory into a ZIP archive byte slice.
func zipAppDir(dir string) ([]byte, error) {
	buf := new(bytes.Buffer)
	zw := zip.NewWriter(buf)

	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		// Skip temporary files, git repos, and local runtime databases
		if strings.HasPrefix(rel, ".git") || strings.HasPrefix(rel, ".data") {
			return nil
		}

		header, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(rel)
		header.Method = zip.Deflate

		w, err := zw.CreateHeader(header)
		if err != nil {
			return err
		}

		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()

		_, err = io.Copy(w, f)
		return err
	})
	if err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
