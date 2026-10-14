// Package hostif provides the Aegis Runtime host interface layer, managing
// registered host functions, capability-based security policies, resource
// limits, and guest-host communication buffers.
//
// Host functions expose external host capabilities (such as I/O, cryptography,
// time querying, and logging) to guest IR modules executing within the
// Aegis Virtual Machine. Every host function is registered alongside a Policy
// defining required capabilities, maximum invocation counts, data boundaries,
// and grant prerequisites.
package hostif

import (
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/oarkflow/ref/aegis/vm"
)

// Standard capability identifiers for Aegis host interface functions.
const (
	// CapIO grants access to host input reading and output writing operations.
	CapIO = "io"
	// CapCrypto grants access to cryptographic primitives (hashing, random generation).
	CapCrypto = "crypto"
	// CapTime grants access to host system clock and timestamp queries.
	CapTime = "time"
	// CapLog grants access to guest-to-host diagnostic and trace logging.
	CapLog = "log"
)

// StandardCapabilities contains the complete set of standard Aegis capabilities.
var StandardCapabilities = []string{CapIO, CapCrypto, CapTime, CapLog}

// Sentinel errors returned by the hostif package.
var (
	// ErrCapabilityDenied is returned when an execution lacks the required capability.
	ErrCapabilityDenied = errors.New("hostif: capability denied")
	// ErrGrantRequired is returned when a function requires an explicit grant that was not provided.
	ErrGrantRequired = errors.New("hostif: capability requires explicit grant")
	// ErrLimitExceeded is returned when a function exceeds its maximum permitted invocation count.
	ErrLimitExceeded = errors.New("hostif: invocation limit exceeded")
	// ErrDataLimitExceeded is returned when read/write payload size exceeds maximum allowed data bytes.
	ErrDataLimitExceeded = errors.New("hostif: data size limit exceeded")
	// ErrFunctionNotFound is returned when a requested host function is not registered.
	ErrFunctionNotFound = errors.New("hostif: host function not found")
	// ErrAlreadyRegistered is returned when attempting to register a duplicate function.
	ErrAlreadyRegistered = errors.New("hostif: host function already registered")
	// ErrInvalidHostFunc is returned when host function definition contains invalid fields.
	ErrInvalidHostFunc = errors.New("hostif: invalid host function")
	// ErrInvalidPolicy is returned when a policy contains invalid constraints.
	ErrInvalidPolicy = errors.New("hostif: invalid policy")
	// ErrNilVM is returned when a host function is executed with a nil VM pointer.
	ErrNilVM = errors.New("hostif: vm instance is nil")
	// ErrCarrierNotFound is returned when an I/O host function cannot locate a carrier context.
	ErrCarrierNotFound = errors.New("hostif: carrier not found")
)

// Policy defines capability requirements, invocation quotas, and data limits for a host function.
type Policy struct {
	// Capability specifies the security capability identifier required to invoke this function (e.g., CapIO).
	Capability string `json:"capability,omitempty" yaml:"capability,omitempty"`
	// MaxCallsPerExecution limits the number of times this function may be invoked during a single execution.
	// A value of 0 indicates no limit.
	MaxCallsPerExecution int `json:"max_calls_per_execution,omitempty" yaml:"max_calls_per_execution,omitempty"`
	// MaxDataBytes specifies the maximum buffer size in bytes permitted for memory transfer operations.
	// A value of 0 indicates no limit.
	MaxDataBytes int `json:"max_data_bytes,omitempty" yaml:"max_data_bytes,omitempty"`
	// RequiresGrant indicates whether invoking this function requires an explicit grant beyond baseline capability enablement.
	RequiresGrant bool `json:"requires_grant,omitempty" yaml:"requires_grant,omitempty"`
}

// registeredEntry pairs a registered host function with its policy and restriction wrapper.
type registeredEntry struct {
	raw     vm.HostFunc
	policy  Policy
	wrapped vm.HostFunc
}

// Registry manages registered vm.HostFunc functions, capability policies, and invocation limits.
type Registry struct {
	mu           sync.RWMutex
	capabilities map[string]bool
	grants       map[string]bool
	functions    map[string]registeredEntry
	order        []string
	carrier      *Carrier
	callCounts   map[string]int64
}

// NewRegistry constructs a new Registry initialized with the provided permitted capabilities.
func NewRegistry(capabilities []string) *Registry {
	capMap := make(map[string]bool, len(capabilities))
	for _, capability := range capabilities {
		if capability != "" {
			capMap[capability] = true
		}
	}

	return &Registry{
		capabilities: capMap,
		grants:       make(map[string]bool),
		functions:    make(map[string]registeredEntry),
		order:        make([]string, 0, 16),
		carrier:      NewCarrier(),
		callCounts:   make(map[string]int64),
	}
}

// Register adds a host function and its associated enforcement policy to the registry.
// The function is automatically wrapped with capability and quota checks.
func (r *Registry) Register(hf vm.HostFunc, policy Policy) error {
	if hf.Module == "" || hf.Name == "" || hf.Func == nil {
		return fmt.Errorf("%w: module, name, and implementation function are required", ErrInvalidHostFunc)
	}
	if policy.MaxCallsPerExecution < 0 || policy.MaxDataBytes < 0 {
		return fmt.Errorf("%w: limits cannot be negative", ErrInvalidPolicy)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	key := hf.Module + "." + hf.Name
	if _, exists := r.functions[key]; exists {
		return fmt.Errorf("%w: %s", ErrAlreadyRegistered, key)
	}

	wrapped := WrapHostFunc(hf, policy, r)

	r.functions[key] = registeredEntry{
		raw:     hf,
		policy:  policy,
		wrapped: wrapped,
	}
	r.order = append(r.order, key)
	return nil
}

// HostFuncs returns all registered host functions wrapped with capability and limit enforcement.
// Functions are returned in stable registration order.
func (r *Registry) HostFuncs() []vm.HostFunc {
	r.mu.RLock()
	defer r.mu.RUnlock()

	funcs := make([]vm.HostFunc, 0, len(r.order))
	for _, key := range r.order {
		funcs = append(funcs, r.functions[key].wrapped)
	}
	return funcs
}

// FilteredHostFuncs returns only host functions whose required capabilities are currently permitted.
func (r *Registry) FilteredHostFuncs() []vm.HostFunc {
	r.mu.RLock()
	defer r.mu.RUnlock()

	funcs := make([]vm.HostFunc, 0, len(r.order))
	for _, key := range r.order {
		entry := r.functions[key]
		if entry.policy.Capability == "" || r.capabilities[entry.policy.Capability] {
			funcs = append(funcs, entry.wrapped)
		}
	}
	return funcs
}

// Get retrieves a registered host function and its policy by module and name.
func (r *Registry) Get(module, name string) (vm.HostFunc, Policy, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	entry, ok := r.functions[module+"."+name]
	if !ok {
		return vm.HostFunc{}, Policy{}, false
	}
	return entry.wrapped, entry.policy, true
}

// HasCapability reports whether the specified capability is currently granted.
func (r *Registry) HasCapability(capability string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.capabilities[capability]
}

// Grant enables the specified capability in the registry.
func (r *Registry) Grant(capability string) {
	if capability == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.capabilities[capability] = true
}

// Revoke disables the specified capability in the registry.
func (r *Registry) Revoke(capability string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.capabilities, capability)
}

// Capabilities returns a sorted list of all active capabilities in the registry.
func (r *Registry) Capabilities() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]string, 0, len(r.capabilities))
	for capability := range r.capabilities {
		result = append(result, capability)
	}
	sort.Strings(result)
	return result
}

// GrantExplicit grants an explicit permission for a capability or function name.
func (r *Registry) GrantExplicit(target string) {
	if target == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.grants[target] = true
}

// RevokeGrant removes an explicit grant for a capability or function name.
func (r *Registry) RevokeGrant(target string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.grants, target)
}

// HasGrant reports whether target has an explicit grant in the registry.
func (r *Registry) HasGrant(target string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.grants[target]
}

// Carrier returns the default Carrier associated with this registry.
func (r *Registry) Carrier() *Carrier {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.carrier
}

// SetCarrier configures the default Carrier for this registry.
func (r *Registry) SetCarrier(carrier *Carrier) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if carrier == nil {
		r.carrier = NewCarrier()
	} else {
		r.carrier = carrier
	}
}

// ResetLimits clears all tracked invocation counts in the registry.
func (r *Registry) ResetLimits() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.callCounts = make(map[string]int64)
}

// incrementCallCount increments and returns the invocation count for a function key.
func (r *Registry) incrementCallCount(key string) int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.callCounts[key]++
	return r.callCounts[key]
}

// RegisterDefaults registers all standard host functions (I/O, Crypto, Time, Log) into the registry.
func (r *Registry) RegisterDefaults() error {
	if err := RegisterIO(r); err != nil {
		return err
	}
	if err := RegisterCrypto(r); err != nil {
		return err
	}
	if err := RegisterTime(r); err != nil {
		return err
	}
	if err := RegisterLog(r); err != nil {
		return err
	}
	return nil
}

// RegisterDefaults is a package-level helper to register all standard host functions into reg.
func RegisterDefaults(reg *Registry) error {
	if reg == nil {
		return errors.New("hostif: registry is nil")
	}
	return reg.RegisterDefaults()
}
