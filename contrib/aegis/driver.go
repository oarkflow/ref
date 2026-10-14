// Package aegis provides an ActionDriver for REF that executes protected
// business logic inside the Aegis Runtime VM with cryptographic attestation,
// fuel budgeting, memory safety, and anti-tamper protections.
package aegis

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/oarkflow/fh"
	"github.com/oarkflow/ref/aegis/ir"
	"github.com/oarkflow/ref/aegis/pkg"
	"github.com/oarkflow/ref/aegis/vm"
	"github.com/oarkflow/ref/platform"
)

var (
	registerOnce sync.Once
)

// Register installs the "aegis.exec" action driver into platform.Registry.
func Register() {
	registerOnce.Do(func() {
		platform.RegisterActionDriver("aegis.exec", platform.ActionFactoryFunc(BuildAegisExec), platform.ActionInfo{
			Family:   "security",
			Summary:  "Execute protected business logic inside the Aegis Runtime VM",
			Provides: "{ result, fuel_used, status }",
			Kind:     "pure",
			Config: []platform.ConfigField{
				{Name: "package", Type: "string", Required: true, Summary: "Path to .apkg protected container"},
				{Name: "function", Type: "string", Required: true, Summary: "Exported function entrypoint to invoke"},
				{Name: "key", Type: "string", Required: false, Summary: "AES-256 package key in hex (defaults to AEGIS_KEY env)"},
				{Name: "args", Type: "slice", Required: false, Summary: "List of input fields/values passed to function"},
				{Name: "fuel", Type: "int", Required: false, Summary: "Instruction fuel budget (default 1,000,000)"},
			},
		})

		// Register fallback for queue.broker using fh.OpenDurableQueue
		platform.RegisterResourceDriver("queue.broker", platform.ResourceFactoryFunc(func(_ context.Context, spec platform.ResourceSpec) (platform.Resource, io.Closer, error) {
			dir, _ := spec.Config["dir"].(string)
			if dir == "" {
				dir = ".data/broker"
			}
			q, err := fh.OpenDurableQueue(fh.DurableQueueConfig{
				Dir: dir,
			})
			return q, q, err
		}), platform.ResourceKindInfo{
			Family:   "queue",
			Summary:  "Durable queue provider fallback",
			Provides: []string{"JobQueue", "QueueDelay", "QueueConsume"},
		})
	})
}

type cachedModule struct {
	module *ir.Module
	fuel   int64
}

var (
	moduleCacheMu sync.RWMutex
	moduleCache   = map[string]*cachedModule{}
)

// BuildAegisExec compiles an Aegis execution node into a platform.Action.
func BuildAegisExec(_ platform.BuildContext, spec platform.NodeSpec) (platform.Action, error) {
	pkgPath, _ := spec.Config["package"].(string)
	if pkgPath == "" {
		return nil, fmt.Errorf("node %q: aegis.exec requires config.package", spec.Name)
	}
	funcName, _ := spec.Config["function"].(string)
	if funcName == "" {
		return nil, fmt.Errorf("node %q: aegis.exec requires config.function", spec.Name)
	}

	keyHex, _ := spec.Config["key"].(string)
	if keyHex == "" {
		keyHex = os.Getenv("AEGIS_KEY")
	}

	fuelLimit := int64(1000000)
	if f, ok := spec.Config["fuel"].(int); ok && f > 0 {
		fuelLimit = int64(f)
	} else if f, ok := spec.Config["fuel"].(float64); ok && f > 0 {
		fuelLimit = int64(f)
	}

	var argKeys []string
	if rawArgs, ok := spec.Config["args"].([]any); ok {
		for _, a := range rawArgs {
			if s, ok := a.(string); ok {
				argKeys = append(argKeys, s)
			}
		}
	} else if rawArgs, ok := spec.Config["args"].([]string); ok {
		argKeys = rawArgs
	}

	provides := spec.Name
	if len(spec.Provides) > 0 {
		provides = spec.Provides[0]
	}

	// Helper to load and decrypt module
	loadMod := func() (*ir.Module, error) {
		moduleCacheMu.RLock()
		if c, found := moduleCache[pkgPath]; found {
			moduleCacheMu.RUnlock()
			return c.module, nil
		}
		moduleCacheMu.RUnlock()

		moduleCacheMu.Lock()
		defer moduleCacheMu.Unlock()

		if c, found := moduleCache[pkgPath]; found {
			return c.module, nil
		}

		if keyHex == "" {
			keyHex = os.Getenv("AEGIS_KEY")
		}
		if keyHex == "" {
			return nil, fmt.Errorf("node %q: aegis.exec requires encryption key (config.key or AEGIS_KEY env)", spec.Name)
		}

		keyBytes, err := hex.DecodeString(strings.TrimSpace(keyHex))
		if err != nil || len(keyBytes) != 32 {
			return nil, fmt.Errorf("node %q: invalid 32-byte hex key: %w", spec.Name, err)
		}

		pkgBytes, err := os.ReadFile(pkgPath)
		if err != nil {
			return nil, fmt.Errorf("node %q: read package %q: %w", spec.Name, pkgPath, err)
		}

		sealedPkg, err := pkg.Unmarshal(pkgBytes)
		if err != nil {
			return nil, fmt.Errorf("node %q: unmarshal package: %w", spec.Name, err)
		}

		if !pkg.VerifyHash(sealedPkg.Payload, sealedPkg.Manifest.PayloadHash) {
			return nil, fmt.Errorf("node %q: payload hash mismatch (tampered)", spec.Name)
		}

		const nonceSize = 12
		if len(sealedPkg.Payload) < nonceSize {
			return nil, fmt.Errorf("node %q: payload too short", spec.Name)
		}
		nonce := sealedPkg.Payload[:nonceSize]
		ciphertext := sealedPkg.Payload[nonceSize:]

		block, err := aes.NewCipher(keyBytes)
		if err != nil {
			return nil, err
		}
		gcm, err := cipher.NewGCM(block)
		if err != nil {
			return nil, err
		}

		aad := []byte(sealedPkg.Manifest.PackageID + "|" + sealedPkg.Manifest.BuildID)
		plaintext, err := gcm.Open(nil, nonce, ciphertext, aad)
		if err != nil {
			return nil, fmt.Errorf("node %q: decryption failed: %w", spec.Name, err)
		}

		if !pkg.VerifyHash(plaintext, sealedPkg.Manifest.PlaintextHash) {
			return nil, fmt.Errorf("node %q: plaintext integrity failed", spec.Name)
		}

		mod, err := ir.Decode(plaintext)
		if err != nil {
			return nil, fmt.Errorf("node %q: decode IR module: %w", spec.Name, err)
		}

		moduleCache[pkgPath] = &cachedModule{module: mod, fuel: fuelLimit}
		return mod, nil
	}

	return platform.ActionFunc(func(ctx *platform.ActionContext) (platform.ActionResult, error) {
		mod, err := loadMod()
		if err != nil {
			return platform.ActionResult{}, err
		}

		// Parse arguments from inputs or literals
		var callArgs []uint64
		for _, argKey := range argKeys {
			val := resolveArg(ctx.Inputs, argKey)
			callArgs = append(callArgs, val)
		}

		v, err := vm.New(mod, vm.Config{
			InitialFuel: fuelLimit,
		})
		if err != nil {
			return platform.ActionResult{}, fmt.Errorf("node %q: instantiate VM: %w", spec.Name, err)
		}

		callCtx := ctx.Context
		if callCtx == nil {
			callCtx = context.Background()
		}

		results, err := v.Call(callCtx, funcName, callArgs...)
		if err != nil {
			return platform.ActionResult{}, fmt.Errorf("node %q: VM execution failed: %w", spec.Name, err)
		}

		resVal := uint64(0)
		if len(results) > 0 {
			resVal = results[0]
		}

		return platform.ActionResult{
			Outputs: map[string]any{
				provides: map[string]any{
					"result":    resVal,
					"results":   results,
					"fuel_used": fuelLimit - v.RemainingFuel(),
					"status":    "ok",
				},
			},
		}, nil
	}), nil
}

func resolveArg(inputs map[string]any, key string) uint64 {
	// First check direct map lookup
	if val, ok := inputs[key]; ok {
		return toUint64(val)
	}

	// Check dotted path (e.g. "order.total" or "item.quantity")
	if strings.Contains(key, ".") {
		parts := strings.Split(key, ".")
		var cur any = inputs
		for _, p := range parts {
			if m, ok := cur.(map[string]any); ok {
				cur = m[p]
			} else {
				cur = nil
				break
			}
		}
		if cur != nil {
			return toUint64(cur)
		}
	}

	// Try numeric literal directly
	if n, err := strconv.ParseUint(key, 0, 64); err == nil {
		return n
	}
	if n, err := strconv.ParseInt(key, 0, 64); err == nil {
		return uint64(n)
	}

	return 0
}

func toUint64(v any) uint64 {
	switch val := v.(type) {
	case int:
		return uint64(val)
	case int32:
		return uint64(val)
	case int64:
		return uint64(val)
	case uint:
		return uint64(val)
	case uint32:
		return uint64(val)
	case uint64:
		return val
	case float64:
		return uint64(val)
	case float32:
		return uint64(val)
	case bool:
		if val {
			return 1
		}
		return 0
	case string:
		if n, err := strconv.ParseUint(val, 0, 64); err == nil {
			return n
		}
		if n, err := strconv.ParseInt(val, 0, 64); err == nil {
			return uint64(n)
		}
	}
	return 0
}
