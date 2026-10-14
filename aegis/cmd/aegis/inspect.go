package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/oarkflow/ref/aegis/pkg"
)

// runInspect handles the `aegis inspect` CLI command.
func runInspect(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("inspect", flag.ContinueOnError)
	flags.SetOutput(stderr)

	var pkgFlag string
	flags.StringVar(&pkgFlag, "pkg", "", "Path to Aegis package container (.apkg)")

	if err := flags.Parse(args); err != nil {
		return err
	}

	if pkgFlag == "" {
		return errors.New("aegis: inspect: package file (-pkg) is required")
	}

	data, err := os.ReadFile(pkgFlag)
	if err != nil {
		return fmt.Errorf("aegis: inspect: failed to read package %q: %w", pkgFlag, err)
	}

	sealedPkg, err := pkg.Unmarshal(data)
	if err != nil {
		return fmt.Errorf("aegis: inspect: failed to decode package %q: %w", pkgFlag, err)
	}

	m := sealedPkg.Manifest

	fmt.Fprintf(stdout, "Package: %s\n", pkgFlag)
	fmt.Fprintf(stdout, "\nIdentity:\n")
	fmt.Fprintf(stdout, "  Package ID:          %s\n", m.PackageID)
	fmt.Fprintf(stdout, "  Application ID:      %s\n", m.ApplicationID)
	fmt.Fprintf(stdout, "  Build ID:            %s\n", m.BuildID)
	fmt.Fprintf(stdout, "  Version:             %s\n", m.Version)
	fmt.Fprintf(stdout, "  Created At:          %s\n", m.CreatedAt.Format(time.RFC3339))

	fmt.Fprintf(stdout, "\nIntegrity:\n")
	fmt.Fprintf(stdout, "  Hash Algorithm:      %s\n", m.HashAlgorithm)
	fmt.Fprintf(stdout, "  Plaintext Hash (IR): %s\n", m.PlaintextHash)
	fmt.Fprintf(stdout, "  Payload Hash (GCM):  %s\n", m.PayloadHash)

	fmt.Fprintf(stdout, "\nCompatibility:\n")
	fmt.Fprintf(stdout, "  IR Version:          %d\n", m.IRVersion)
	if m.MinRuntimeVersion != "" {
		fmt.Fprintf(stdout, "  Min Runtime Version: %s\n", m.MinRuntimeVersion)
	}
	if len(m.RequiredCapabilities) > 0 {
		fmt.Fprintf(stdout, "  Capabilities:        %s\n", strings.Join(m.RequiredCapabilities, ", "))
	} else {
		fmt.Fprintf(stdout, "  Capabilities:        (none)\n")
	}

	fmt.Fprintf(stdout, "\nExecution Policy:\n")
	fmt.Fprintf(stdout, "  Default Protection:  %s\n", m.ExecutionPolicy.Default)
	if len(m.ExecutionPolicy.Functions) > 0 {
		fmt.Fprintf(stdout, "  Function Overrides:\n")
		for fnName, lvl := range m.ExecutionPolicy.Functions {
			fmt.Fprintf(stdout, "    - %s: %s\n", fnName, lvl)
		}
	}

	fmt.Fprintf(stdout, "\nEncryption:\n")
	fmt.Fprintf(stdout, "  Algorithm:           %s\n", m.EncryptionAlgorithm)
	fmt.Fprintf(stdout, "  Key Derivation:      %s\n", m.KeyDerivation)
	fmt.Fprintf(stdout, "  Payload Size:        %d bytes\n", len(sealedPkg.Payload))

	fmt.Fprintf(stdout, "\nSignature:\n")
	fmt.Fprintf(stdout, "  Algorithm:           %s\n", sealedPkg.Signature.Algorithm)
	fmt.Fprintf(stdout, "  Key ID:              %s\n", sealedPkg.Signature.KeyID)
	if len(sealedPkg.Signature.Value) > 0 {
		sigVal := sealedPkg.Signature.Value
		if len(sigVal) > 32 {
			fmt.Fprintf(stdout, "  Signature:           %s... (%d chars)\n", sigVal[:32], len(sigVal))
		} else {
			fmt.Fprintf(stdout, "  Signature:           %s\n", sigVal)
		}
	}

	return nil
}
