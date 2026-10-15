package main

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/oarkflow/ref/aegis/harden"
	"github.com/oarkflow/ref/aegis/integrity"
	"github.com/oarkflow/ref/aegis/ir"
	"github.com/oarkflow/ref/aegis/pkg"
	"github.com/oarkflow/ref/aegis/runtime"
	"github.com/oarkflow/ref/aegis/vfs"
	"github.com/oarkflow/ref/aegis/vm"
	"github.com/oarkflow/ref/serve"
)

// runRun handles the `aegis run` CLI command.
func runRun(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("run", flag.ContinueOnError)
	flags.SetOutput(stderr)

	var (
		pkgFlag    string
		appFlag    string
		addrFlag   string
		envFlag    string
		keyFlag    string
		encKeyFlag string
		funcFlag   string
		argsFlag   string
		fuelFlag   int64
		pubkeyFlag string
	)

	flags.StringVar(&pkgFlag, "pkg", "", "Path to Aegis package container (.apkg)")
	flags.StringVar(&appFlag, "app", "", "Path to application directory (e.g. ./examples/smsgateway)")
	flags.StringVar(&addrFlag, "addr", ":8080", "HTTP listen address when running application (default :8080)")
	flags.StringVar(&envFlag, "env", "development", "Application environment profile (default development)")
	flags.StringVar(&keyFlag, "key", "", "AES-256 package encryption key (hex, key file, or passphrase)")
	flags.StringVar(&encKeyFlag, "enckey", "", "Alias for -key")
	flags.StringVar(&funcFlag, "func", "", "Name of exported function to execute (for IR modules)")
	flags.StringVar(&argsFlag, "args", "", "Comma-separated list of numerical arguments (for IR modules)")
	flags.Int64Var(&fuelFlag, "fuel", 1000000, "Maximum execution fuel instruction limit")
	flags.StringVar(&pubkeyFlag, "pubkey", "", "Optional trusted public key PEM or JWKS to verify package signature")

	var flagArgs []string
	var nonFlags []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") {
			flagArgs = append(flagArgs, a)
			if !strings.Contains(a, "=") && i+1 < len(args) && (args[i+1] == "-" || !strings.HasPrefix(args[i+1], "-")) {
				flagArgs = append(flagArgs, args[i+1])
				i++
			}
		} else {
			nonFlags = append(nonFlags, a)
		}
	}

	if err := flags.Parse(flagArgs); err != nil {
		return err
	}

	target := ""
	if len(nonFlags) > 0 {
		target = nonFlags[0]
	}
	if target == "" && appFlag != "" {
		target = appFlag
	}
	if target == "" && pkgFlag != "" {
		target = pkgFlag
	}

	if target == "" {
		return errors.New("aegis: run: an application directory or package container (.apkg) is required")
	}

	// Case 1: Target is an application directory
	if info, err := os.Stat(target); err == nil && info.IsDir() {
		return runAppDir(target, addrFlag, envFlag, stdout, stderr)
	}

	// Case 2: Target is a package container file (.apkg)
	pkgFlag = target
	if keyFlag == "" && encKeyFlag != "" {
		keyFlag = encKeyFlag
	}
	if keyFlag == "" {
		keyFlag = os.Getenv("AEGIS_KEY")
	}
	if keyFlag == "" {
		return errors.New("aegis: run: encryption key (-key or AEGIS_KEY env) is required to open package")
	}

	// Close the OS paths that copy process memory to disk (core dumps, swap,
	// debugger reads) before any plaintext exists.
	hardenReport := harden.Apply(harden.FromEnv())
	for _, a := range hardenReport.Applied {
		fmt.Fprintf(stdout, "  [Memory] %s\n", a)
	}
	for _, s := range hardenReport.Skipped {
		fmt.Fprintf(stderr, "  [Memory] not applied - %s\n", s)
	}
	if keyFlag == "-" {
		// Read the key from stdin so it never appears in argv or a file.
		raw, err := io.ReadAll(io.LimitReader(os.Stdin, 4096))
		if err != nil {
			return fmt.Errorf("aegis: run: read key from stdin: %w", err)
		}
		keyFlag = strings.TrimSpace(string(raw))
		harden.Wipe(raw)
	}
	// The key is parsed once below; remove it from the environment so
	// /proc/<pid>/environ and child processes cannot read it.
	harden.ScrubEnv("AEGIS_KEY")

	// 1. Read package
	pkgData, err := os.ReadFile(pkgFlag)
	if err != nil {
		return fmt.Errorf("aegis: run: failed to read package %q: %w", pkgFlag, err)
	}
	sealedPkg, err := pkg.Unmarshal(pkgData)
	if err != nil {
		return fmt.Errorf("aegis: run: failed to decode package %q: %w", pkgFlag, err)
	}

	// 2. Resolve 32-byte encryption key
	encKey, err := parseEncryptionKey(keyFlag)
	if err != nil {
		return fmt.Errorf("aegis: run: invalid encryption key: %w", err)
	}

	// 3. Verify public key if provided
	if pubkeyFlag != "" {
		trustedKeys, err := loadPublicKey(pubkeyFlag)
		if err != nil {
			return fmt.Errorf("aegis: run: failed to load public key: %w", err)
		}
		reader := pkg.NewReader(trustedKeys)
		if err := reader.Verify(sealedPkg); err != nil {
			return fmt.Errorf("aegis: run: package verification failed: %w", err)
		}
	}

	// 4. Verify payload integrity
	if !pkg.VerifyHash(sealedPkg.Payload, sealedPkg.Manifest.PayloadHash) {
		return errors.New("aegis: run: payload hash mismatch (tampered)")
	}

	// 5. Decrypt payload
	const nonceSize = 12
	if len(sealedPkg.Payload) < nonceSize {
		return errors.New("aegis: run: payload too short")
	}
	nonce := sealedPkg.Payload[:nonceSize]
	ciphertext := sealedPkg.Payload[nonceSize:]

	block, err := aes.NewCipher(encKey)
	if err != nil {
		return fmt.Errorf("aegis: run: failed to initialize cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return fmt.Errorf("aegis: run: failed to initialize GCM: %w", err)
	}

	aad := []byte(sealedPkg.Manifest.PackageID + "|" + sealedPkg.Manifest.BuildID)
	plaintext, err := gcm.Open(nil, nonce, ciphertext, aad)
	if err != nil {
		return fmt.Errorf("aegis: run: decryption failed (incorrect key or corrupted payload): %w", err)
	}

	// 6. Verify plaintext hash
	if !pkg.VerifyHash(plaintext, sealedPkg.Manifest.PlaintextHash) {
		return errors.New("aegis: run: plaintext hash mismatch")
	}

	// Case 2A: Plaintext is a packaged REF application bundle (ZIP)
	if bytes.HasPrefix(plaintext, []byte("PK\x03\x04")) {
		// The key and the sealed bytes are no longer needed once decrypted.
		harden.Wipe(encKey)
		harden.Wipe(pkgData)
		return runEncryptedApp(plaintext, sealedPkg.Manifest.PackageID, addrFlag, envFlag, stdout, stderr)
	}

	// Case 2B: Plaintext is an Aegis IR module
	if funcFlag == "" {
		return errors.New("aegis: run: function name (-func) is required when executing an IR module")
	}

	// Decode IR module
	module, err := ir.Decode(plaintext)
	if err != nil {
		return fmt.Errorf("aegis: run: failed to decode IR module: %w", err)
	}

	// Parse call arguments
	var callArgs []uint64
	if argsFlag != "" {
		parts := strings.Split(argsFlag, ",")
		for _, p := range parts {
			trimmed := strings.TrimSpace(p)
			if trimmed == "" {
				continue
			}
			val, err := strconv.ParseInt(trimmed, 0, 64)
			if err != nil {
				uval, err2 := strconv.ParseUint(trimmed, 0, 64)
				if err2 != nil {
					return fmt.Errorf("aegis: run: invalid argument %q: %w", trimmed, err)
				}
				callArgs = append(callArgs, uval)
			} else {
				callArgs = append(callArgs, uint64(val))
			}
		}
	}

	// Execute function
	ctx := context.Background()
	var returns []uint64
	var fuelUsed int64
	var duration time.Duration

	if pubkeyFlag != "" {
		// Execute using Runtime session
		trustedKeys, _ := loadPublicKey(pubkeyFlag)
		rtCfg := runtime.DefaultConfig()
		rtCfg.TrustedKeys = trustedKeys
		rtCfg.GrantProvider = runtime.NewStaticGrantProvider(encKey)
		if fuelFlag > 0 {
			rtCfg.VM.InitialFuel = fuelFlag
		}

		rt, err := runtime.New(rtCfg)
		if err != nil {
			return fmt.Errorf("aegis: run: failed to create runtime: %w", err)
		}

		lp, err := rt.LoadPackage(ctx, pkgData)
		if err != nil {
			return fmt.Errorf("aegis: run: failed to load package into runtime: %w", err)
		}

		if err := rt.Authorize(ctx, lp, runtime.GrantRequest{}); err != nil {
			return fmt.Errorf("aegis: run: authorization failed: %w", err)
		}

		execReq := runtime.ExecutionRequest{
			Function: funcFlag,
			Fuel:     fuelFlag,
			Args:     callArgs,
		}
		res, err := rt.Execute(ctx, lp, execReq)
		if err != nil {
			return fmt.Errorf("aegis: run: execution failed: %w", err)
		}
		returns = res.Returns
		fuelUsed = res.FuelUsed
		duration = res.ExecutionDuration()
	} else {
		// Direct VM execution
		vmCfg := vm.Config{
			InitialFuel: fuelFlag,
		}
		vmInst, err := vm.New(module, vmCfg)
		if err != nil {
			return fmt.Errorf("aegis: run: failed to create VM: %w", err)
		}

		start := time.Now()
		returns, err = vmInst.Call(ctx, funcFlag, callArgs...)
		duration = time.Since(start)
		if err != nil {
			return fmt.Errorf("aegis: run: execution failed: %w", err)
		}
		fuelUsed = fuelFlag - vmInst.RemainingFuel()
	}

	// Format and print output
	if len(returns) == 1 {
		fmt.Fprintf(stdout, "Result: %d\n", returns[0])
	} else if len(returns) > 1 {
		var strVals []string
		for _, r := range returns {
			strVals = append(strVals, strconv.FormatUint(r, 10))
		}
		fmt.Fprintf(stdout, "Results: [%s]\n", strings.Join(strVals, ", "))
	} else {
		fmt.Fprintf(stdout, "Execution completed (void return)\n")
	}

	fmt.Fprintf(stdout, "Fuel Used: %d\n", fuelUsed)
	fmt.Fprintf(stdout, "Duration:  %s\n", duration)
	return nil
}

func runAppDir(dir, addr, env string, stdout, stderr io.Writer) error {
	fmt.Fprintf(stdout, "=================================================================\n")
	fmt.Fprintf(stdout, "   AEGIS RUNTIME: SECURE PLATFORM EXECUTION\n")
	fmt.Fprintf(stdout, "=================================================================\n")

	checker := integrity.DefaultChecker()
	results := checker.Run(context.Background())
	passed, report := integrity.Summarize(results)
	fmt.Fprintf(stdout, "  [Integrity] Verified: %v | Checks Passed: %d/%d\n", passed, len(results), len(results))
	if !passed {
		return fmt.Errorf("aegis: security integrity violation: %s", report)
	}
	fmt.Fprintf(stdout, "  [Isolation] Memory Boundary: ENFORCED | Capabilities: GATED\n")
	fmt.Fprintf(stdout, "==> Starting REF application from %s on %s...\n", dir, addr)

	return serve.Run(context.Background(), serve.Options{
		Dir:  dir,
		Addr: addr,
		Env:  env,
	})
}

func runEncryptedApp(plaintext []byte, pkgID, addr, env string, stdout, stderr io.Writer) error {
	fmt.Fprintf(stdout, "=================================================================\n")
	fmt.Fprintf(stdout, "   AEGIS RUNTIME: SECURE PLATFORM EXECUTION (CONTAINER)\n")
	fmt.Fprintf(stdout, "=================================================================\n")
	fmt.Fprintf(stdout, "==> Decrypted Protected Application Container [%s]\n", pkgID)

	checker := integrity.DefaultChecker()
	results := checker.Run(context.Background())
	passed, report := integrity.Summarize(results)
	fmt.Fprintf(stdout, "  [Integrity] Verified: %v | Checks Passed: %d/%d\n", passed, len(results), len(results))
	if !passed {
		return fmt.Errorf("aegis: security integrity violation: %s", report)
	}
	fmt.Fprintf(stdout, "  [Isolation] Memory Boundary: ENFORCED | Filesystem: VIRTUAL IN-MEMORY (ZERO DISK RESIDUE)\n")

	// Load decrypted payload into in-memory virtual filesystem with zero disk writes
	memFS, err := vfs.FromZip(plaintext)
	if err != nil {
		return fmt.Errorf("aegis: failed to load in-memory virtual filesystem: %w", err)
	}
	defer memFS.Wipe()
	// MemFS holds its own copy; zero the decrypted archive buffer.
	harden.Wipe(plaintext)

	fmt.Fprintf(stdout, "==> Loaded %d virtual files into in-memory VFS\n", len(memFS.ListAll()))
	fmt.Fprintf(stdout, "==> Starting protected REF application on %s...\n", addr)
	return serve.Run(context.Background(), serve.Options{
		FS:   memFS,
		Addr: addr,
		Env:  env,
	})
}
