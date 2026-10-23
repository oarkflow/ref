package main

import (
	"archive/zip"
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
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
		binaryFlag string
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
	flags.StringVar(&binaryFlag, "binary", "", "Path to an executable to seal as a runtime image for aegis-launch (see docs/aegis-protection-plan.md, phase 6)")
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

	modeCount := 0
	for _, f := range []string{moduleFlag, dirFlag, binaryFlag} {
		if f != "" {
			modeCount++
		}
	}
	if modeCount != 1 {
		return errors.New("aegis: package: exactly one of -module (IR file), -dir (application directory) or -binary (executable for aegis-launch) is required")
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

	// 2. Resolve 32-byte encryption key. A bare passphrase is derived with
	// Argon2id and a fresh salt (see resolvePackagingKey); the salt is not
	// secret and travels with the package so `aegis run` can reproduce the
	// same key from the same passphrase.
	encKey, kdf, err := resolvePackagingKey(encKeyFlag)
	if err != nil {
		return fmt.Errorf("aegis: package: invalid encryption key: %w", err)
	}
	if kdf != nil {
		fmt.Fprintf(stdout, "  Key Derivation: %s (salt stored in the manifest, not secret)\n", kdf.Algorithm)
	}

	// 3. Create package builder
	builder, err := pkg.NewBuilder(appIDFlag, verFlag, keySet, encKey)
	if err != nil {
		return fmt.Errorf("aegis: package: failed to create builder: %w", err)
	}
	if kdf != nil {
		builder.SetKDF(*kdf)
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
	var manifest []string
	switch {
	case dirFlag != "":
		// Package an entire REF application directory into an encrypted container
		absDir, err := filepath.Abs(dirFlag)
		if err != nil {
			return fmt.Errorf("aegis: package: failed to resolve directory: %w", err)
		}
		zipBytes, files, err := zipAppDir(absDir)
		if err != nil {
			return fmt.Errorf("aegis: package: failed to archive directory %q: %w", dirFlag, err)
		}
		manifest = files
		sp, err := builder.BuildPayload(zipBytes, "", ir.Version)
		if err != nil {
			return fmt.Errorf("aegis: package: failed to build application package: %w", err)
		}
		sealedPkg = sp
	case binaryFlag != "":
		// Package an executable as a runtime image: aegis-launch decrypts
		// this straight into memory and execs it, so nothing but the
		// launcher itself is ever a file on disk (see aegis/cmd/aegis-launch).
		binBytes, err := os.ReadFile(binaryFlag)
		if err != nil {
			return fmt.Errorf("aegis: package: failed to read binary %q: %w", binaryFlag, err)
		}
		if len(binBytes) < 4 || string(binBytes[:4]) != "\x7fELF" {
			return fmt.Errorf("aegis: package: %q is not an ELF executable (aegis-launch only runs on Linux); got the first bytes %x", binaryFlag, binBytes[:min(4, len(binBytes))])
		}
		sp, err := builder.BuildPayload(binBytes, "", ir.Version)
		if err != nil {
			return fmt.Errorf("aegis: package: failed to build runtime image package: %w", err)
		}
		sealedPkg = sp
	default:
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
	if len(manifest) > 0 {
		fmt.Fprintf(stdout, "  Sealed %d file(s):\n", len(manifest))
		for _, rel := range manifest {
			fmt.Fprintf(stdout, "    %s\n", rel)
		}
	}
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

// resolvePackagingKey and resolveRunKey moved to aegis/pkg/keyflag.go (as
// ResolvePackagingKey, ResolveRunKey) so aegis-launch can share them
// without importing this main package, which Go does not allow anyway.
// These thin wrappers keep the rest of this file's call sites unchanged.
func resolvePackagingKey(input string) ([]byte, *pkg.KDFInfo, error) {
	return pkg.ResolvePackagingKey(input)
}

func resolveRunKey(input string, kdf *pkg.KDFInfo) ([]byte, error) {
	return pkg.ResolveRunKey(input, kdf)
}

// denyPatterns are files that must never be sealed into a package: secrets
// that belong beside the source tree, not shipped (encrypted or not) inside
// an artifact that leaves this machine. Hitting one fails the build outright
// rather than silently skipping it, so a secret dropped into the application
// directory by mistake is caught at packaging time, not discovered later in
// a decrypted bundle that already went out the door.
//
// This is deliberately a short, specific list. A pattern broad enough to
// also catch ordinary application files (an entity named "password", a
// template with "secret" in its name) would train whoever packages this to
// routinely override it, which defeats the point.
var denyPatterns = []string{
	".env", ".env.*",
	"*.pem", "*.key", "*.p12", "*.pfx",
	"id_rsa", "id_rsa.*", "id_ed25519", "id_ed25519.*",
}

// infraExclude is skipped silently: build and version-control artifacts that
// do not belong in a deployed bundle but are not secrets either.
func infraExcluded(rel string) bool {
	return strings.HasPrefix(rel, ".git") || strings.HasPrefix(rel, ".data") || strings.HasSuffix(rel, ".apkg")
}

// matchesDenyPattern reports whether name (the file's base name) matches one
// of denyPatterns.
func matchesDenyPattern(name string) (string, bool) {
	for _, pat := range denyPatterns {
		if ok, _ := filepath.Match(pat, name); ok {
			return pat, true
		}
	}
	return "", false
}

// zipAppDir archives dir into a ZIP, refusing outright if it contains a
// denied file, and returns the sorted list of every path it actually sealed
// — the packaging manifest printed to the operator, so what went into the
// container is never a surprise.
func zipAppDir(dir string) ([]byte, []string, error) {
	buf := new(bytes.Buffer)
	zw := zip.NewWriter(buf)
	var manifest []string

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
		rel = filepath.ToSlash(rel)
		if infraExcluded(rel) {
			return nil
		}
		if pat, denied := matchesDenyPattern(filepath.Base(rel)); denied {
			return fmt.Errorf("refusing to package %q: it matches the denied pattern %q (a secret file). Move it outside the application directory, or rename it if it genuinely is not one", rel, pat)
		}

		header, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		header.Name = rel
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

		if _, err := io.Copy(w, f); err != nil {
			return err
		}
		manifest = append(manifest, rel)
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, nil, err
	}
	sort.Strings(manifest)
	return buf.Bytes(), manifest, nil
}
