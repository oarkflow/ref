package main

import (
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

	"github.com/oarkflow/ref/aegis/ir"
	"github.com/oarkflow/ref/aegis/pkg"
	"github.com/oarkflow/ref/aegis/runtime"
	"github.com/oarkflow/ref/aegis/vm"
)

// runRun handles the `aegis run` CLI command.
func runRun(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("run", flag.ContinueOnError)
	flags.SetOutput(stderr)

	var (
		pkgFlag    string
		keyFlag    string
		encKeyFlag string
		funcFlag   string
		argsFlag   string
		fuelFlag   int64
		pubkeyFlag string
	)

	flags.StringVar(&pkgFlag, "pkg", "", "Path to Aegis package container (.apkg)")
	flags.StringVar(&keyFlag, "key", "", "AES-256 package encryption key (hex, key file, or passphrase)")
	flags.StringVar(&encKeyFlag, "enckey", "", "Alias for -key")
	flags.StringVar(&funcFlag, "func", "", "Name of exported function to execute")
	flags.StringVar(&argsFlag, "args", "", "Comma-separated list of numerical arguments")
	flags.Int64Var(&fuelFlag, "fuel", 1000000, "Maximum execution fuel instruction limit")
	flags.StringVar(&pubkeyFlag, "pubkey", "", "Optional trusted public key PEM or JWKS to verify package signature")

	if err := flags.Parse(args); err != nil {
		return err
	}

	if pkgFlag == "" {
		return errors.New("aegis: run: package file (-pkg) is required")
	}
	if keyFlag == "" && encKeyFlag != "" {
		keyFlag = encKeyFlag
	}
	if keyFlag == "" {
		return errors.New("aegis: run: encryption key (-key) is required")
	}
	if funcFlag == "" {
		return errors.New("aegis: run: function name (-func) is required")
	}

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
		return errors.New("aegis: run: payload hash mismatch")
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
		return fmt.Errorf("aegis: run: decryption failed: %w", err)
	}

	// 6. Verify plaintext hash
	if !pkg.VerifyHash(plaintext, sealedPkg.Manifest.PlaintextHash) {
		return errors.New("aegis: run: plaintext hash mismatch")
	}

	// 7. Decode IR module
	module, err := ir.Decode(plaintext)
	if err != nil {
		return fmt.Errorf("aegis: run: failed to decode IR module: %w", err)
	}

	// 8. Parse call arguments
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

	// 9. Execute function
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

		start := time.Now()
		execRes, err := rt.Execute(ctx, lp, runtime.ExecutionRequest{
			Function: funcFlag,
			Args:     callArgs,
			Fuel:     fuelFlag,
		})
		duration = time.Since(start)
		if err != nil {
			return fmt.Errorf("aegis: run: execution failed: %w", err)
		}
		returns = execRes.Returns
		fuelUsed = execRes.FuelUsed
	} else {
		// Execute directly using VM
		vmCfg := vm.DefaultConfig()
		if fuelFlag > 0 {
			vmCfg.InitialFuel = fuelFlag
		}

		v, err := vm.New(module, vmCfg)
		if err != nil {
			return fmt.Errorf("aegis: run: failed to instantiate vm: %w", err)
		}
		defer v.Close()

		start := time.Now()
		rawReturns, err := v.Call(ctx, funcFlag, callArgs...)
		duration = time.Since(start)
		if err != nil {
			return fmt.Errorf("aegis: run: execution failed: %w", err)
		}
		returns = rawReturns
		if fuelFlag > 0 {
			fuelUsed = fuelFlag - v.RemainingFuel()
		}
	}

	// 10. Output results
	if len(returns) == 1 {
		fmt.Fprintf(stdout, "Result: %d\n", returns[0])
	} else if len(returns) > 1 {
		var resStrs []string
		for _, r := range returns {
			resStrs = append(resStrs, strconv.FormatUint(r, 10))
		}
		fmt.Fprintf(stdout, "Result: [%s]\n", strings.Join(resStrs, ", "))
	} else {
		fmt.Fprintf(stdout, "Result: (none)\n")
	}

	fmt.Fprintf(stdout, "Fuel Used: %d\n", fuelUsed)
	fmt.Fprintf(stdout, "Duration:  %s\n", duration)

	return nil
}
