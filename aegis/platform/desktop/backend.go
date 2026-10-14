// Package desktop implements the Aegis execution backend for standard desktop
// and server environments (Linux, macOS, Windows) using process-level integrity
// verification and sandboxed virtual machine execution.
package desktop

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/oarkflow/ref/aegis/integrity"
	"github.com/oarkflow/ref/aegis/ir"
	"github.com/oarkflow/ref/aegis/platform"
)

func init() {
	platform.RegisterDefault(New())
}

// Backend executes Aegis IR modules in an operating system process protected
// by runtime anti-debugging, timing anomaly checks, and environment verification.
type Backend struct {
	mu          sync.Mutex
	config      platform.BackendConfig
	module      *ir.Module
	checker     *integrity.Checker
	initialized bool
}

// New creates a new desktop ExecutionBackend with default integrity checks.
func New() *Backend {
	return &Backend{
		checker: integrity.DefaultChecker(),
	}
}

// NewWithChecker creates a desktop ExecutionBackend with a customized integrity checker.
func NewWithChecker(checker *integrity.Checker) *Backend {
	if checker == nil {
		checker = integrity.DefaultChecker()
	}
	return &Backend{
		checker: checker,
	}
}

// Name returns the canonical identifier for the desktop platform adapter.
func (b *Backend) Name() string {
	return "desktop"
}

// Capabilities returns the security properties and unmitigated threat profile for desktop execution.
func (b *Backend) Capabilities() platform.Capabilities {
	return platform.Capabilities{
		ResistsStaticAnalysis:         true,
		ResistsRuntimeInstrumentation: true,
		ResistsPrivilegedAttacker:     false,
		HasHardwareIsolation:         false,
		HasRemoteAttestation:          false,
		HasSecureKeyStorage:           false,
		HasMemoryEncryption:           false,
		Limitations: []string{
			"Root/Administrator can inspect process memory and registers",
			"Ordinary process memory is not hardware-encrypted",
		},
		ResidualRisks: []string{
			"Host operating system compromise bypasses process boundaries",
			"Kernel-level debuggers and ptrace can inspect or alter execution state",
		},
	}
}

// Initialize configures runtime resource limits and attestation parameters.
func (b *Backend) Initialize(ctx context.Context, config platform.BackendConfig) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.config = config
	b.initialized = true
	return nil
}

// LoadModule decodes and validates an Aegis IR module for subsequent execution.
func (b *Backend) LoadModule(ctx context.Context, moduleData []byte) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if !b.initialized {
		return platform.ErrBackendNotInitialized
	}

	mod, err := ir.Decode(moduleData)
	if err != nil {
		return fmt.Errorf("desktop: failed to decode module: %w", err)
	}
	if err := ir.Validate(mod); err != nil {
		return fmt.Errorf("desktop: module validation failed: %w", err)
	}

	b.module = mod
	return nil
}

// Execute verifies host process integrity before invoking the target function in an isolated VM.
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
	checker := b.checker
	b.mu.Unlock()

	// 1. Defense-in-depth: run pre-flight integrity probes
	if checker != nil {
		results := checker.Run(ctx)
		for _, probeResult := range results {
			if !probeResult.Passed && probeResult.Severity == integrity.SeverityCritical {
				return nil, fmt.Errorf("desktop: %w: %s (%s)", platform.ErrIntegrityViolation, probeResult.Check, probeResult.Evidence)
			}
		}
	}

	// 2. Execute module function in VM
	res, err := platform.ExecuteModule(ctx, mod, cfg, req)
	if err != nil {
		return nil, fmt.Errorf("desktop: %w", err)
	}

	return res, nil
}

// Attest produces an informational attestation statement for desktop/development environments.
func (b *Backend) Attest(ctx context.Context) ([]byte, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if !b.initialized {
		return nil, platform.ErrBackendNotInitialized
	}

	var nonceStr string
	if len(b.config.AttestationNonce) > 0 {
		nonceStr = base64.StdEncoding.EncodeToString(b.config.AttestationNonce)
	}

	payload := map[string]any{
		"platform":         "linux-desktop",
		"app_identity":     "aegis-desktop",
		"package_name":     "com.oarkflow.aegis.desktop",
		"device_integrity": "none",
		"timestamp":        time.Now().Unix(),
		"details": map[string]string{
			"platform":    "desktop",
			"environment": "hardened-local-process",
			"nonce":       nonceStr,
		},
		"warnings": []string{"no hardware attestation available"},
	}

	evidenceBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("desktop: failed to serialize attestation payload: %w", err)
	}

	return evidenceBytes, nil
}

// Close releases backend resources and resets loaded state.
func (b *Backend) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.module = nil
	b.initialized = false
	return nil
}
