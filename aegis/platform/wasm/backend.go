// Package wasm implements the Aegis execution adapter for WebAssembly in web browser runtimes.
//
// This backend models client-side execution in a browser JavaScript/Wasm context,
// honestly documenting that client-side developer tools have total inspection rights
// and that no hardware-backed isolation exists.
package wasm

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/oarkflow/ref/aegis/ir"
	"github.com/oarkflow/ref/aegis/platform"
)

func init() {
	platform.RegisterDefault(New())
}

// Backend executes Aegis IR modules in a WebAssembly sandbox within web browsers.
type Backend struct {
	mu          sync.Mutex
	config      platform.BackendConfig
	module      *ir.Module
	initialized bool
}

// New creates a new WebAssembly/browser execution backend.
func New() *Backend {
	return &Backend{}
}

// Name returns the canonical identifier for the WebAssembly platform adapter.
func (b *Backend) Name() string {
	return "wasm"
}

// Capabilities returns the security properties and unmitigated threat profile for browser Wasm.
func (b *Backend) Capabilities() platform.Capabilities {
	return platform.Capabilities{
		ResistsStaticAnalysis:         false,
		ResistsRuntimeInstrumentation: false,
		ResistsPrivilegedAttacker:     false,
		HasHardwareIsolation:         false,
		HasRemoteAttestation:          false,
		HasSecureKeyStorage:           false,
		HasMemoryEncryption:           false,
		Limitations: []string{
			"Code executing in browser DOM/JS context is completely inspectable by the user",
			"Client devtools have full inspection rights",
		},
		ResidualRisks: []string{
			"Browser extensions and malicious scripts in the same origin can inspect heap memory",
			"No hardware isolation or local process hardening available in browser runtime",
		},
	}
}

// Initialize configures runtime parameters.
func (b *Backend) Initialize(ctx context.Context, config platform.BackendConfig) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.config = config
	b.initialized = true
	return nil
}

// LoadModule decodes and validates an Aegis IR module for Wasm execution.
func (b *Backend) LoadModule(ctx context.Context, moduleData []byte) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if !b.initialized {
		return platform.ErrBackendNotInitialized
	}

	mod, err := ir.Decode(moduleData)
	if err != nil {
		return fmt.Errorf("wasm: failed to decode module: %w", err)
	}
	if err := ir.Validate(mod); err != nil {
		return fmt.Errorf("wasm: module validation failed: %w", err)
	}

	b.module = mod
	return nil
}

// Execute invokes the target function within the WebAssembly VM sandbox.
func (b *Backend) Execute(ctx context.Context, req platform.ExecutionRequest) (*platform.ExecutionResult, error) {
	b.mu.Lock()
	if !b.initialized {
		b.mu.Unlock()
		return nil, platform.ErrBackendNotInitialized
	}
	if b.module == nil {
		b.mu.Unlock()
		return nil, platform.ErrModuleNotLoaded
	}

	mod := b.module
	cfg := b.config
	b.mu.Unlock()

	res, err := platform.ExecuteModule(ctx, mod, cfg, req)
	if err != nil {
		return nil, fmt.Errorf("wasm: %w", err)
	}

	return res, nil
}

// Attest produces an informational token confirming execution in an unattested browser environment.
func (b *Backend) Attest(ctx context.Context) ([]byte, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if !b.initialized {
		return nil, platform.ErrBackendNotInitialized
	}

	payload := map[string]any{
		"platform":         "web",
		"app_identity":     "browser-wasm",
		"package_name":     "com.oarkflow.aegis.web",
		"device_integrity": "none",
		"timestamp":        time.Now().Unix(),
		"details": map[string]string{
			"platform":   "web",
			"environment": "browser-dom-wasm",
		},
		"warnings": []string{"no hardware attestation available in browser environment"},
	}

	evidenceBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("wasm: failed to serialize web attestation payload: %w", err)
	}

	return evidenceBytes, nil
}

// Close releases resources held by the Wasm backend.
func (b *Backend) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.module = nil
	b.initialized = false
	return nil
}
