// Package platform provides execution backend abstractions, platform adapters,
// capability models, and registry dispatch for the Aegis Runtime.
//
// Aegis executes guest IR modules within platform-specific isolation boundaries,
// ranging from local desktop processes hardened against debuggers, to mobile
// operating system keystore integrations, web browser runtimes, and hardware-isolated
// confidential computing enclaves (e.g. Intel SGX, AMD SEV-SNP, Arm CCA).
package platform

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
)

// Standard package error definitions prefixed with "platform: ".
var (
	// ErrBackendNotInitialized is returned when invoking operations on an uninitialized backend.
	ErrBackendNotInitialized = errors.New("platform: backend not initialized")

	// ErrModuleNotLoaded is returned when attempting execution before an IR module is loaded.
	ErrModuleNotLoaded = errors.New("platform: module not loaded")

	// ErrFunctionNotFound is returned when the target function export does not exist in the loaded module.
	ErrFunctionNotFound = errors.New("platform: function not found")

	// ErrFuelExhausted is returned when guest function execution exceeds its instruction fuel limit.
	ErrFuelExhausted = errors.New("platform: fuel exhausted")

	// ErrIntegrityViolation is returned when host integrity probes detect tampering or unauthorized instrumentation.
	ErrIntegrityViolation = errors.New("platform: host integrity check failed")

	// ErrNilBackend is returned when attempting to register a nil ExecutionBackend.
	ErrNilBackend = errors.New("platform: backend cannot be nil")
)

// ExecutionBackend defines the interface for an Aegis platform execution adapter.
//
// Implementations manage the lifecycle, security isolation, module loading,
// bytecode execution, and hardware attestation for specific operating environments.
type ExecutionBackend interface {
	// Name returns the canonical identifier of this platform backend (e.g., "desktop", "android", "confidential").
	Name() string

	// Capabilities returns the security, isolation, and attestation profile for this backend.
	Capabilities() Capabilities

	// Initialize configures the backend with runtime resource bounds and cryptographic parameters.
	Initialize(ctx context.Context, config BackendConfig) error

	// LoadModule decodes, validates, and prepares an Aegis IR binary module for execution.
	LoadModule(ctx context.Context, module []byte) error

	// Execute invokes a target function within the loaded module under the platform's isolation boundary.
	Execute(ctx context.Context, req ExecutionRequest) (*ExecutionResult, error)

	// Attest generates cryptographic hardware or platform attestation evidence.
	Attest(ctx context.Context) ([]byte, error)

	// Close terminates the execution environment and releases associated system resources.
	Close() error
}

// BackendConfig specifies the initialization configuration for an ExecutionBackend.
type BackendConfig struct {
	// MaxMemoryPages defines the maximum linear memory size in 64 KiB pages (default 256 = 16 MiB).
	MaxMemoryPages uint32 `json:"max_memory_pages"`

	// InitialFuel defines the initial instruction execution fuel allocation.
	InitialFuel int64 `json:"initial_fuel"`

	// StoragePath specifies an optional directory for persistent state or key management.
	StoragePath string `json:"storage_path"`

	// AttestationNonce provides a cryptographic challenge nonce to bind into attestation evidence.
	AttestationNonce []byte `json:"attestation_nonce"`
}

// ExecutionRequest encapsulates a single function invocation within a backend.
type ExecutionRequest struct {
	// Function is the name of the exported function to invoke.
	Function string `json:"function"`

	// Args contains numeric arguments for the function invocation.
	Args []uint64 `json:"args,omitempty"`

	// Inputs contains named byte buffers accessible to the guest function via host I/O.
	Inputs map[string][]byte `json:"inputs,omitempty"`

	// Fuel overrides the default instruction fuel limit if positive.
	Fuel int64 `json:"fuel,omitempty"`
}

// ExecutionResult encapsulates the outcome of an executed function invocation.
type ExecutionResult struct {
	// Returns contains numeric values returned by the guest function.
	Returns []uint64 `json:"returns,omitempty"`

	// Outputs contains named byte buffers emitted by the function via host I/O.
	Outputs map[string][]byte `json:"outputs,omitempty"`

	// FuelUsed is the total count of instructions consumed during execution.
	FuelUsed int64 `json:"fuel_used"`

	// Duration is the execution elapsed time in nanoseconds.
	Duration int64 `json:"duration"`
}

// Registry is a thread-safe registry of ExecutionBackend adapters.
type Registry struct {
	mu       sync.RWMutex
	backends map[string]ExecutionBackend
}

// NewRegistry creates a new Registry pre-populated with the supplied backends.
func NewRegistry(backends ...ExecutionBackend) *Registry {
	reg := &Registry{
		backends: make(map[string]ExecutionBackend),
	}
	for _, b := range backends {
		if b != nil {
			reg.Register(b)
		}
	}
	return reg
}

// defaultRegistryMu protects the default registry singleton template.
var (
	defaultRegistryMu sync.RWMutex
	defaultRegistry   = NewRegistry()
)

// RegisterDefault registers a backend into the global default registry template.
func RegisterDefault(backend ExecutionBackend) {
	if backend == nil {
		return
	}
	defaultRegistryMu.Lock()
	defer defaultRegistryMu.Unlock()
	defaultRegistry.Register(backend)
}

// DefaultRegistry returns a fresh Registry instance populated with all registered default adapters.
func DefaultRegistry() *Registry {
	defaultRegistryMu.RLock()
	defer defaultRegistryMu.RUnlock()
	reg := NewRegistry()
	for _, b := range defaultRegistry.List() {
		reg.Register(b)
	}
	return reg
}

// Register registers an ExecutionBackend into this registry instance.
func (r *Registry) Register(backend ExecutionBackend) {
	if backend == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.backends[backend.Name()] = backend
}

// Get retrieves an ExecutionBackend by its canonical name or known platform alias.
func (r *Registry) Get(name string) (ExecutionBackend, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	// 1. Direct match
	if b, ok := r.backends[name]; ok {
		return b, true
	}

	// 2. Case-insensitive match
	norm := strings.ToLower(strings.TrimSpace(name))
	for k, v := range r.backends {
		if strings.ToLower(k) == norm {
			return v, true
		}
	}

	// 3. Known alias routing
	targetKey := resolvePlatformAlias(norm)
	if targetKey != "" {
		if b, ok := r.backends[targetKey]; ok {
			return b, true
		}
		for k, v := range r.backends {
			if strings.ToLower(k) == targetKey {
				return v, true
			}
		}
	}

	return nil, false
}

// resolvePlatformAlias maps well-known platform names and OS identifiers to canonical backend names.
func resolvePlatformAlias(alias string) string {
	switch alias {
	case "linux", "linux-desktop", "macos-desktop", "windows-desktop", "darwin", "windows", "desktop-local":
		return "desktop"
	case "play", "play-integrity", "android-keystore", "android-mobile":
		return "android"
	case "ios", "macos", "app-attest", "secure-enclave", "apple-ios":
		return "apple"
	case "web", "browser", "js", "wasm-browser":
		return "wasm"
	case "sgx", "sev-snp", "tdx", "cca", "linux-confidential", "hardware-enclave":
		return "confidential"
	default:
		return ""
	}
}

// List returns a slice of all registered backends sorted by name.
func (r *Registry) List() []ExecutionBackend {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, 0, len(r.backends))
	for name := range r.backends {
		names = append(names, name)
	}
	sort.Strings(names)

	list := make([]ExecutionBackend, 0, len(names))
	for _, name := range names {
		list = append(list, r.backends[name])
	}
	return list
}

// Names returns a sorted slice of all registered backend names.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, 0, len(r.backends))
	for name := range r.backends {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
