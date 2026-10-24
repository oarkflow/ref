// Package aegis provides an ActionDriver for REF that executes protected
// business logic inside the Aegis Runtime VM with cryptographic attestation,
// fuel budgeting, memory safety, and anti-tamper protections.
package aegis

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
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
	"github.com/oarkflow/ref/signing"
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
				{Name: "key", Type: "string", Required: false, Summary: "AES-256 package key in hex (defaults to key_file, AEGIS_KEY, AEGIS_KEY_FILE)"},
				{Name: "key_file", Type: "string", Required: false, Summary: "File holding the hex package key"},
				{Name: "trusted_keys", Type: "string", Required: false, Summary: "JWKS file of trusted signers; enforces package signature (or AEGIS_TRUSTED_KEYS)"},
				{Name: "result_type", Type: "string", Required: false, Summary: "uint64 (default), int32, int64 or bool"},
				{Name: "args", Type: "slice", Required: false, Summary: "List of input fields/values passed to function"},
				{Name: "fuel", Type: "int", Required: false, Summary: "Instruction fuel budget (default 1,000,000)"},
			},
		})

		// Register fallback for queue.broker using fh.OpenDurableQueue
		platform.RegisterFallbackResourceDriver("queue.broker", platform.ResourceFactoryFunc(func(_ context.Context, spec platform.ResourceSpec) (platform.Resource, io.Closer, error) {
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
}

var (
	moduleCacheMu sync.RWMutex
	moduleCache   = map[string]*cachedModule{}
)

// resolveKey returns the 32-byte package key from config.key, config.key_file,
// AEGIS_KEY or AEGIS_KEY_FILE (in that order).
func resolveKey(cfg map[string]any) ([]byte, error) {
	keyHex, _ := cfg["key"].(string)
	if keyHex == "" {
		if f, _ := cfg["key_file"].(string); f != "" {
			b, err := os.ReadFile(f)
			if err != nil {
				return nil, fmt.Errorf("read key_file: %w", err)
			}
			keyHex = string(b)
		}
	}
	if keyHex == "" {
		keyHex = os.Getenv("AEGIS_KEY")
	}
	if keyHex == "" {
		if f := os.Getenv("AEGIS_KEY_FILE"); f != "" {
			b, err := os.ReadFile(f)
			if err != nil {
				return nil, fmt.Errorf("read AEGIS_KEY_FILE: %w", err)
			}
			keyHex = string(b)
		}
	}
	if keyHex == "" {
		return nil, fmt.Errorf("encryption key required (config.key, config.key_file, AEGIS_KEY or AEGIS_KEY_FILE)")
	}
	key, err := hex.DecodeString(strings.TrimSpace(keyHex))
	if err != nil || len(key) != 32 {
		return nil, fmt.Errorf("invalid 32-byte hex key")
	}
	return key, nil
}

// resolveTrusted loads the trusted verification keys (JWKS) from
// config.trusted_keys or AEGIS_TRUSTED_KEYS. It returns nil when none are set.
func resolveTrusted(cfg map[string]any) (*signing.KeySet, error) {
	path, _ := cfg["trusted_keys"].(string)
	if path == "" {
		path = os.Getenv("AEGIS_TRUSTED_KEYS")
	}
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read trusted_keys: %w", err)
	}
	return signing.ParseJWKS(data)
}

// loadModule reads, verifies and decrypts a package. With trusted keys the
// signature is enforced; without them only the hashes and AEAD tag are checked.
func loadModule(pkgPath string, key []byte, trusted *signing.KeySet) (*ir.Module, error) {
	raw, err := os.ReadFile(pkgPath)
	if err != nil {
		return nil, fmt.Errorf("read package %q: %w", pkgPath, err)
	}
	sealed, err := pkg.Unmarshal(raw)
	if err != nil {
		return nil, fmt.Errorf("unmarshal package: %w", err)
	}
	if trusted != nil {
		return pkg.NewReader(trusted).Decrypt(sealed, key)
	}
	if !pkg.VerifyHash(sealed.Payload, sealed.Manifest.PayloadHash) {
		return nil, fmt.Errorf("payload hash mismatch (tampered)")
	}
	const nonceSize = 12
	if len(sealed.Payload) < nonceSize {
		return nil, fmt.Errorf("payload too short")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	aad := []byte(sealed.Manifest.PackageID + "|" + sealed.Manifest.BuildID)
	plaintext, err := gcm.Open(nil, sealed.Payload[:nonceSize], sealed.Payload[nonceSize:], aad)
	if err != nil {
		return nil, fmt.Errorf("decryption failed: %w", err)
	}
	if !pkg.VerifyHash(plaintext, sealed.Manifest.PlaintextHash) {
		return nil, fmt.Errorf("plaintext integrity failed")
	}
	return ir.Decode(plaintext)
}

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

	fuelLimit := int64(1000000)
	if f, ok := spec.Config["fuel"].(int); ok && f > 0 {
		fuelLimit = int64(f)
	} else if f, ok := spec.Config["fuel"].(float64); ok && f > 0 {
		fuelLimit = int64(f)
	}

	resultType, _ := spec.Config["result_type"].(string)
	switch resultType {
	case "", "uint64", "int32", "int64", "bool":
	default:
		return nil, fmt.Errorf("node %q: unsupported result_type %q (uint64|int32|int64|bool)", spec.Name, resultType)
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

	loadMod := func() (*ir.Module, error) {
		key, err := resolveKey(spec.Config)
		if err != nil {
			return nil, fmt.Errorf("node %q: %w", spec.Name, err)
		}
		trusted, err := resolveTrusted(spec.Config)
		if err != nil {
			return nil, fmt.Errorf("node %q: %w", spec.Name, err)
		}
		// Cache by path and key so a different key never reuses a module.
		fp := sha256.Sum256(key)
		cacheKey := pkgPath + "|" + hex.EncodeToString(fp[:8])
		if trusted != nil {
			for _, k := range trusted.Keys() {
				cacheKey += "|" + k.Thumbprint()
			}
		}

		moduleCacheMu.RLock()
		c, found := moduleCache[cacheKey]
		moduleCacheMu.RUnlock()
		if found {
			return c.module, nil
		}

		moduleCacheMu.Lock()
		defer moduleCacheMu.Unlock()
		if c, found := moduleCache[cacheKey]; found {
			return c.module, nil
		}
		mod, err := loadModule(pkgPath, key, trusted)
		if err != nil {
			return nil, fmt.Errorf("node %q: %w", spec.Name, err)
		}
		moduleCache[cacheKey] = &cachedModule{module: mod}
		return mod, nil
	}

	return platform.ActionFunc(func(ctx *platform.ActionContext) (platform.ActionResult, error) {
		mod, err := loadMod()
		if err != nil {
			return platform.ActionResult{}, err
		}

		var callArgs []uint64
		for _, argKey := range argKeys {
			callArgs = append(callArgs, resolveArg(ctx.Inputs, argKey))
		}

		v, err := vm.New(mod, vm.Config{InitialFuel: fuelLimit})
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

		var first uint64
		if len(results) > 0 {
			first = results[0]
		}
		var result any = first
		switch resultType {
		case "int32":
			result = int64(int32(first))
		case "int64":
			result = int64(first)
		case "bool":
			result = first != 0
		}

		return platform.ActionResult{
			Outputs: map[string]any{
				provides: map[string]any{
					"result":    result,
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
