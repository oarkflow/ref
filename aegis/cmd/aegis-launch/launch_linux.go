//go:build linux

package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"syscall"
	"unsafe"

	"github.com/oarkflow/ref/aegis/harden"
	"github.com/oarkflow/ref/aegis/integrity"
	"github.com/oarkflow/ref/aegis/pkg"
	"github.com/oarkflow/ref/signing"
	"golang.org/x/sys/unix"
)

// run parses the launcher's own flags (everything before a literal "--"),
// resolves and verifies the sealed runtime image, decrypts it straight into
// a sealed memfd, and execs it there — replacing this process so nothing
// about it (not even this launcher) is left running underneath. On success
// this function never returns.
func run(args []string) error {
	launcherArgs, runtimeArgs, err := splitArgs(args)
	if err != nil {
		return err
	}

	fs := flag.NewFlagSet("aegis-launch", flag.ContinueOnError)
	var (
		imageFlag       string
		keyFlag         string
		pubkeyFlag      string
		insecureSkip    bool
		integrityPolicy string
	)
	fs.StringVar(&imageFlag, "image", "", "Path to the sealed runtime image (produced by: aegis package -binary <elf> ...)")
	fs.StringVar(&keyFlag, "key", "", "AES-256 image encryption key (hex, key file, or passphrase); or set AEGIS_LAUNCH_KEY")
	fs.StringVar(&pubkeyFlag, "pubkey", "", "Trusted public key PEM or JWKS to verify the image signature. Required unless -insecure-skip-verify is given")
	fs.BoolVar(&insecureSkip, "insecure-skip-verify", false, "Run an unverified image (development only: anyone who can replace the image file can run arbitrary code as you)")
	fs.StringVar(&integrityPolicy, "integrity-policy", "enforce", `"enforce" (default) refuses to start when a debugger, injected library or timing anomaly is detected; "warn" logs it and continues`)
	if err := fs.Parse(launcherArgs); err != nil {
		return err
	}
	if integrityPolicy != "enforce" && integrityPolicy != "warn" {
		return fmt.Errorf("-integrity-policy must be %q or %q, got %q", "enforce", "warn", integrityPolicy)
	}
	if imageFlag == "" {
		return errors.New("-image is required")
	}
	if keyFlag == "" {
		keyFlag = os.Getenv("AEGIS_LAUNCH_KEY")
	}
	if keyFlag == "" {
		return errors.New("-key (or the AEGIS_LAUNCH_KEY environment variable) is required")
	}
	if len(runtimeArgs) == 0 {
		return errors.New(`no arguments to run; pass them after "--", e.g. -- run app.apkg -pubkey app.pub`)
	}

	// Close the OS paths that copy process memory to disk before any
	// plaintext exists, exactly as aegis run does.
	hardenReport := harden.Apply(harden.FromEnv())
	for _, a := range hardenReport.Applied {
		fmt.Fprintf(os.Stderr, "aegis-launch: [memory] %s\n", a)
	}
	for _, s := range hardenReport.Skipped {
		fmt.Fprintf(os.Stderr, "aegis-launch: [memory] not applied - %s\n", s)
	}
	harden.ScrubEnv("AEGIS_LAUNCH_KEY")

	imageData, err := os.ReadFile(imageFlag)
	if err != nil {
		return fmt.Errorf("read image %q: %w", imageFlag, err)
	}
	sealed, err := pkg.Unmarshal(imageData)
	if err != nil {
		return fmt.Errorf("decode image %q: %w", imageFlag, err)
	}

	switch {
	case pubkeyFlag != "":
		trustedKeys, err := loadTrustedKeys(pubkeyFlag)
		if err != nil {
			return fmt.Errorf("load public key: %w", err)
		}
		if err := pkg.NewReader(trustedKeys).Verify(sealed); err != nil {
			return fmt.Errorf("image verification failed: %w", err)
		}
		fmt.Fprintf(os.Stderr, "aegis-launch: [signature] verified against %s\n", pubkeyFlag)
	case insecureSkip:
		fmt.Fprintln(os.Stderr, "aegis-launch: [signature] NOT CHECKED (-insecure-skip-verify): this image's signer is not verified. Never use this flag in production.")
	default:
		return errors.New("a sealed image requires -pubkey to verify who signed it; pass -insecure-skip-verify only for local development")
	}

	// Check the host environment before any plaintext exists, not after
	// (see aegis run's run.go for why the order matters).
	results := integrity.DefaultChecker().Run(context.Background())
	passed, report := integrity.Summarize(results)
	switch {
	case passed:
		fmt.Fprintf(os.Stderr, "aegis-launch: [integrity] clean: %d/%d checks\n", len(results), len(results))
	case integrityPolicy == "warn":
		fmt.Fprintf(os.Stderr, "aegis-launch: [integrity] FAILED but continuing (-integrity-policy=warn): %s\n", report)
	default:
		return fmt.Errorf("security integrity violation: %s (pass -integrity-policy=warn to continue anyway, logging instead of refusing)", report)
	}

	encKey, err := pkg.ResolveRunKey(keyFlag, sealed.Manifest.KDF)
	if err != nil {
		return fmt.Errorf("invalid encryption key: %w", err)
	}
	if harden.LockBuffer(encKey) {
		defer harden.UnlockBuffer(encKey)
	}
	defer harden.Wipe(encKey)

	plaintext, err := pkg.Open(sealed, encKey)
	if err != nil {
		return fmt.Errorf("open image: %w", err)
	}
	harden.Wipe(encKey)
	harden.Wipe(imageData)
	if !bytes.HasPrefix(plaintext, []byte("\x7fELF")) {
		harden.Wipe(plaintext)
		return errors.New("the decrypted image is not an ELF executable (package it with: aegis package -binary <elf-file> ...)")
	}

	fd, err := sealedExecutableMemfd(plaintext)
	harden.Wipe(plaintext)
	if err != nil {
		return err
	}
	// From here on there is no going back to Go code on error paths that
	// matter: fexecve either replaces this process or it does not return at
	// all in the success case. A failure just returns an error like
	// anything else above.
	return fexecve(fd, append([]string{"aegis"}, runtimeArgs...), os.Environ())
}

// splitArgs separates aegis-launch's own flags from the arguments meant for
// the exec'd runtime, at a literal "--". Requiring it (rather than using
// flag.Args() after the registered flags) means a runtime flag can never be
// misread as one of this launcher's own, in either direction.
func splitArgs(args []string) (launcherArgs, runtimeArgs []string, err error) {
	for i, a := range args {
		if a == "--" {
			return args[:i], args[i+1:], nil
		}
	}
	return args, nil, nil
}

// loadTrustedKeys parses pathOrContent (a file path or inline text) as a
// single PEM public key or a JWKS document. Unlike aegis run's -pubkey
// (aegis/cmd/aegis/verify.go), this does not accept a bundle of several PEM
// blocks — that support lives in aegis/runtime, which this launcher does
// not import, because aegis/runtime pulls in the VM and compiler and this
// binary's whole reason to exist is staying small. A JWKS document (one
// file, several keys, with ids) covers the same multi-key case.
func loadTrustedKeys(pathOrContent string) (*signing.KeySet, error) {
	trimmed := strings.TrimSpace(pathOrContent)
	if trimmed == "" {
		return nil, errors.New("public key cannot be empty")
	}
	data := []byte(trimmed)
	if fileInfo, err := os.Stat(trimmed); err == nil && !fileInfo.IsDir() {
		fileBytes, err := os.ReadFile(trimmed)
		if err != nil {
			return nil, fmt.Errorf("read public key file %q: %w", trimmed, err)
		}
		data = fileBytes
	}
	data = bytes.TrimSpace(data)
	if key, err := signing.ParsePublicKeyPEM("", "", data); err == nil {
		return signing.NewKeySet(nil, key)
	}
	if len(data) > 0 && data[0] == '{' {
		if ks, err := signing.ParseJWKS(data); err == nil {
			return ks, nil
		}
	}
	return nil, errors.New("failed to parse public key as a single PEM key or a JWKS document")
}

// sealedExecutableMemfd creates an anonymous, memory-backed file, writes elf
// into it, and seals it against every further write, shrink or grow before
// returning its file descriptor — so between this call returning and the
// fexecve that follows, nothing (including this very process) can alter
// the bytes the kernel is about to map and execute.
func sealedExecutableMemfd(elf []byte) (int, error) {
	fd, err := unix.MemfdCreate("aegis-runtime", unix.MFD_CLOEXEC|unix.MFD_ALLOW_SEALING)
	if err != nil {
		return -1, fmt.Errorf("memfd_create: %w", err)
	}
	if _, err := unix.Write(fd, elf); err != nil {
		_ = unix.Close(fd)
		return -1, fmt.Errorf("write to memfd: %w", err)
	}
	const seals = unix.F_SEAL_SEAL | unix.F_SEAL_SHRINK | unix.F_SEAL_GROW | unix.F_SEAL_WRITE
	if _, err := unix.FcntlInt(uintptr(fd), unix.F_ADD_SEALS, seals); err != nil {
		_ = unix.Close(fd)
		return -1, fmt.Errorf("seal memfd (a hardened kernel can refuse memfd sealing or execution; this is the integrity this launcher relies on, so it refuses rather than running unsealed): %w", err)
	}
	return fd, nil
}

// fexecve replaces this process with the program held by fd, by calling
// execveat(fd, "", argv, envp, AT_EMPTY_PATH) — glibc's own implementation
// of fexecve(3) on Linux, since the kernel has no separate fexecve syscall.
// It calls the raw syscall directly (as syscall.Exec does for plain execve)
// rather than through a library wrapper, because there is no path-based
// name to exec: the "file" is the memfd itself.
func fexecve(fd int, argv, envp []string) error {
	emptyPath, err := syscall.BytePtrFromString("")
	if err != nil {
		return err
	}
	argvp, err := syscall.SlicePtrFromStrings(argv)
	if err != nil {
		return err
	}
	envpp, err := syscall.SlicePtrFromStrings(envp)
	if err != nil {
		return err
	}
	_, _, errno := syscall.Syscall6(
		unix.SYS_EXECVEAT,
		uintptr(fd),
		uintptr(unsafe.Pointer(emptyPath)),
		uintptr(unsafe.Pointer(&argvp[0])),
		uintptr(unsafe.Pointer(&envpp[0])),
		uintptr(unix.AT_EMPTY_PATH),
		0,
	)
	return fmt.Errorf("execveat: %w", errno)
}
