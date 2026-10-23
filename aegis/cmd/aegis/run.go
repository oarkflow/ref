package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	goruntime "runtime"
	"strconv"
	"strings"
	"time"

	"github.com/oarkflow/ref/aegis/control"
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
		pkgFlag         string
		appFlag         string
		addrFlag        string
		envFlag         string
		keyFlag         string
		encKeyFlag      string
		funcFlag        string
		argsFlag        string
		fuelFlag        int64
		pubkeyFlag      string
		insecureSkip    bool
		integrityPolicy string
		respondOpts     respondOptions
		grantServerFlag string
		deviceIDFlag    string
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
	flags.StringVar(&pubkeyFlag, "pubkey", "", "Trusted public key PEM or JWKS to verify the package signature. Required for a .apkg unless -insecure-skip-verify is given")
	flags.BoolVar(&insecureSkip, "insecure-skip-verify", false, "Run a .apkg without checking who signed it (development only: anyone who can write the file can run arbitrary code as you)")
	flags.StringVar(&integrityPolicy, "integrity-policy", "enforce", "\"enforce\" (default) refuses to start when a debugger, injected library or timing anomaly is detected; \"warn\" logs it and continues")
	flags.StringVar(&grantServerFlag, "grant-server", "", "Base URL of a control server (aegis serve) to fetch the decryption key from, instead of -key; the package must have been registered there first (aegis register)")
	flags.StringVar(&deviceIDFlag, "device-id", "", "Identifies this machine to the control server when using -grant-server (optional; an empty value is a grant not bound to any one device)")

	flags.StringVar(&respondOpts.Mode, "respond", "off", "\"on\" watches integrity continuously and escalates automatically (restrict writes, isolate, wipe, exit for restart); \"off\" (default) checks once at startup")
	flags.StringVar(&respondOpts.PolicyPath, "respond-policy", "", "Signed escalation policy (JSON) for -respond=on; needs -respond-pubkey")
	flags.StringVar(&respondOpts.PubKeyHex, "respond-pubkey", "", "Ed25519 public key (hex) that signed the -respond-policy; use a key other than the package signing key")
	flags.DurationVar(&respondOpts.Interval, "respond-interval", 30*time.Second, "How often -respond=on re-runs the integrity checks")

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
	if integrityPolicy != "enforce" && integrityPolicy != "warn" {
		return fmt.Errorf("aegis: run: -integrity-policy must be %q or %q, got %q", "enforce", "warn", integrityPolicy)
	}

	if err := respondOpts.validate(); err != nil {
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
		if respondOpts.enabled() {
			return errors.New("aegis: run: -respond=on needs a sealed .apkg; an application directory runs unprotected")
		}
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
	if keyFlag == "" && grantServerFlag == "" {
		return errors.New("aegis: run: encryption key (-key or AEGIS_KEY env) or -grant-server is required to open package")
	}
	if keyFlag != "" && grantServerFlag != "" {
		return errors.New("aegis: run: -key and -grant-server are two different ways to get the same key; give exactly one")
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

	// 2. Resolve 32-byte encryption key, either locally or from a control
	// server. Locally: when the manifest says this package was keyed from a
	// passphrase, keyFlag is that passphrase and is derived with the
	// manifest's own stored salt and cost (resolveRunKey), so it reproduces
	// the exact key the package side derived. Remotely: the control server
	// looks the key up by package id (registered ahead of time with `aegis
	// register`) and hands it out as a short-lived grant, after checking the
	// package and device are not revoked — see aegis/control/grant.go.
	var encKey []byte
	if grantServerFlag != "" {
		grant, err := control.NewHTTPGrantProvider(grantServerFlag).RequestGrant(context.Background(), runtime.GrantRequest{
			PackageID:     sealedPkg.Manifest.PackageID,
			ApplicationID: sealedPkg.Manifest.ApplicationID,
			BuildID:       sealedPkg.Manifest.BuildID,
			Version:       sealedPkg.Manifest.Version,
			Platform:      goruntime.GOOS + "/" + goruntime.GOARCH,
			DeviceID:      deviceIDFlag,
		})
		if err != nil {
			return fmt.Errorf("aegis: run: fetch grant from %s: %w", grantServerFlag, err)
		}
		encKey = grant.Key
		fmt.Fprintf(stdout, "  [Grant] obtained from %s (session %s, expires %s)\n", grantServerFlag, grant.SessionID, time.Unix(grant.ExpiresAt, 0).UTC().Format(time.RFC3339))
	} else {
		encKey, err = resolveRunKey(keyFlag, sealedPkg.Manifest.KDF)
		if err != nil {
			return fmt.Errorf("aegis: run: invalid encryption key: %w", err)
		}
	}
	if harden.LockBuffer(encKey) {
		defer harden.UnlockBuffer(encKey)
	}

	// 3. Verify the package signature. A sealed package carries no proof of
	// who built it without this: anyone who can write a .apkg to this path
	// (or intercept it in transit) can run arbitrary code as this process.
	// -insecure-skip-verify exists for local development and says so loudly.
	switch {
	case pubkeyFlag != "":
		trustedKeys, err := loadPublicKey(pubkeyFlag)
		if err != nil {
			return fmt.Errorf("aegis: run: failed to load public key: %w", err)
		}
		reader := pkg.NewReader(trustedKeys)
		if err := reader.Verify(sealedPkg); err != nil {
			return fmt.Errorf("aegis: run: package verification failed: %w", err)
		}
		fmt.Fprintf(stdout, "  [Signature] Verified against %s\n", pubkeyFlag)
	case insecureSkip:
		fmt.Fprintln(stderr, "  [Signature] NOT CHECKED (-insecure-skip-verify): this package's signer is not verified. Anyone who can replace this file can run arbitrary code as this process. Never use this flag in production.")
	default:
		return errors.New("aegis: run: a .apkg requires -pubkey to verify who signed it; pass -insecure-skip-verify only for local development")
	}

	// 4. Verify payload integrity
	if !pkg.VerifyHash(sealedPkg.Payload, sealedPkg.Manifest.PayloadHash) {
		return errors.New("aegis: run: payload hash mismatch (tampered)")
	}

	// 4b. Check the host environment (debugger, injection, timing anomalies)
	// before any plaintext exists, not after. A debugger already attached by
	// the time the key is in memory has already seen it; checking afterward
	// (as this used to) only proves the check itself ran under one.
	integrityResults := integrity.DefaultChecker().Run(context.Background())
	integrityPassed, integrityReport := integrity.Summarize(integrityResults)
	switch {
	case integrityPassed:
		fmt.Fprintf(stdout, "  [Integrity] Clean: %d/%d checks\n", len(integrityResults), len(integrityResults))
	case integrityPolicy == "warn":
		fmt.Fprintf(stderr, "  [Integrity] FAILED but continuing (-integrity-policy=warn): %s\n", integrityReport)
	default:
		return fmt.Errorf("aegis: run: security integrity violation: %s (pass -integrity-policy=warn to continue anyway, logging instead of refusing)", integrityReport)
	}

	// 5. Decrypt the payload and verify the plaintext hash (pkg.Open, shared
	// with aegis-launch, which needs exactly the same sequence).
	plaintext, err := pkg.Open(sealedPkg, encKey)
	if err != nil {
		return fmt.Errorf("aegis: run: %w", err)
	}

	// Case 2A: Plaintext is a packaged REF application bundle (ZIP)
	if bytes.HasPrefix(plaintext, []byte("PK\x03\x04")) {
		// The key and the sealed bytes are no longer needed once decrypted.
		harden.Wipe(encKey)
		harden.Wipe(pkgData)
		return runEncryptedApp(plaintext, sealedPkg.Manifest.PackageID, addrFlag, envFlag, hardenReport, respondOpts, stdout, stderr)
	}

	// Case 2B: Plaintext is an Aegis IR module
	if respondOpts.enabled() {
		fmt.Fprintln(stderr, "  [Respond] ignored: -respond=on applies to sealed applications, not IR modules")
	}
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

// runAppDir runs an application straight from its source directory: BCL,
// SPL and templates in plain text on disk, no encryption, no signature
// check, no process hardening (hardening some protections, such as
// PT_DENY_ATTACH or non-dumpable, would also block a developer's own
// debugger attaching to this same process). This is the development path;
// a deployment that wants any of Aegis's protections uses `aegis package`
// and `aegis run <app.apkg> -pubkey ...` instead.
func runAppDir(dir, addr, env string, stdout, _ io.Writer) error {
	fmt.Fprintf(stdout, "=================================================================\n")
	fmt.Fprintf(stdout, "   AEGIS: UNPROTECTED (development)\n")
	fmt.Fprintf(stdout, "=================================================================\n")
	fmt.Fprintf(stdout, "  No encryption, no signature check, no process hardening.\n")
	fmt.Fprintf(stdout, "  The application's BCL, SPL and templates are plain files on disk.\n")
	fmt.Fprintf(stdout, "  For production: `aegis package` then `aegis run <app.apkg> -pubkey ...`.\n")
	fmt.Fprintf(stdout, "==> Starting REF application from %s on %s...\n", dir, addr)

	return serve.Run(context.Background(), serve.Options{
		Dir:  dir,
		Addr: addr,
		Env:  env,
	})
}

// runEncryptedApp serves a sealed application whose signature has already
// been verified (or the caller passed -insecure-skip-verify) and whose host
// integrity has already been checked (see step 4b in runRun) before this
// plaintext ever existed. hardenReport is what runRun's harden.Apply
// actually achieved on this OS; the banner states exactly that instead of a
// blanket claim, so what it says matches what happened.
func runEncryptedApp(plaintext []byte, pkgID, addr, env string, hardenReport harden.Report, ro respondOptions, stdout, stderr io.Writer) error {
	fmt.Fprintf(stdout, "=================================================================\n")
	fmt.Fprintf(stdout, "   AEGIS RUNTIME: SECURE PLATFORM EXECUTION (CONTAINER)\n")
	fmt.Fprintf(stdout, "=================================================================\n")
	fmt.Fprintf(stdout, "==> Decrypted Protected Application Container [%s]\n", pkgID)

	if len(hardenReport.Skipped) == 0 {
		fmt.Fprintf(stdout, "  [Isolation] Process Hardening: ENFORCED (%d/%d protections applied) | Filesystem: VIRTUAL IN-MEMORY (ZERO DISK RESIDUE)\n",
			len(hardenReport.Applied), len(hardenReport.Applied))
	} else {
		fmt.Fprintf(stdout, "  [Isolation] Process Hardening: PARTIAL (%d/%d protections applied; see [Memory] lines above) | Filesystem: VIRTUAL IN-MEMORY (ZERO DISK RESIDUE)\n",
			len(hardenReport.Applied), len(hardenReport.Applied)+len(hardenReport.Skipped))
	}

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

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	opts := serve.Options{FS: memFS, Addr: addr, Env: env}
	var rt *respondRuntime
	if ro.enabled() {
		rt, err = startRespond(ctx, ro, memFS, cancel, stdout, stderr)
		if err != nil {
			return err
		}
		defer rt.stop()
		opts.Gate = rt.enforcer.Gate
	}
	runErr := serve.Run(ctx, opts)
	if rt != nil {
		if code := rt.exit(); code != 0 {
			return &exitError{Code: code, Msg: fmt.Sprintf("aegis: run: stopped by respond controller at level %s (exit %d)", rt.ctl.Level(), code)}
		}
	}
	return runErr
}
