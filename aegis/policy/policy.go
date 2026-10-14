// Package policy provides developer-facing protection policy configuration
// formats, validation rules, and platform compatibility checks for the
// Aegis Runtime.
//
// Developers author a policy document that declares the baseline protection
// tier for their application, as well as per-function confidentiality,
// execution mode, authorization, and fallback requirements. Aegis inspects
// the target platform capabilities against this policy to verify compatibility,
// enforce acknowledged downgrades, and describe residual security risks.
package policy

// Policy is the developer-authored protection configuration for an Aegis application.
type Policy struct {
	// Application identifies the application name and version.
	Application ApplicationPolicy `json:"application" yaml:"application"`
	// Protection sets global baseline security defaults.
	Protection ProtectionDefaults `json:"protection" yaml:"protection"`
	// Functions maps function names or identifiers to their protection policies.
	Functions map[string]FuncPolicy `json:"functions" yaml:"functions"`
}

// ApplicationPolicy identifies the application.
type ApplicationPolicy struct {
	// ID is the unique identifier of the application.
	ID string `json:"id" yaml:"id"`
	// Version is the semantic version string of the application.
	Version string `json:"version" yaml:"version"`
}

// ProtectionDefaults sets the baseline protection level.
type ProtectionDefaults struct {
	// Default sets the baseline protection tier.
	Default ProtectionLevel `json:"default" yaml:"default"`
	// StripMetadata indicates whether debugging and reflection metadata should be stripped.
	StripMetadata bool `json:"strip_metadata" yaml:"strip_metadata"`
	// Diversify indicates whether binary diversification and layout randomization should be applied.
	Diversify bool `json:"diversify" yaml:"diversify"`
}

// ProtectionLevel is a named security tier.
type ProtectionLevel string

const (
	// LevelStandard represents baseline application execution with standard operating system protections.
	LevelStandard ProtectionLevel = "standard"
	// LevelHardened represents execution with process-level memory safety, ASLR enforcement, and stack protection.
	LevelHardened ProtectionLevel = "hardened"
	// LevelIsolated represents hardware-isolated execution (TEE or confidential VM).
	LevelIsolated ProtectionLevel = "isolated"
)

// Valid reports whether the protection level is one of the recognized tiers.
func (p ProtectionLevel) Valid() bool {
	switch p {
	case LevelStandard, LevelHardened, LevelIsolated:
		return true
	default:
		return false
	}
}

// FuncPolicy describes protection requirements for one function.
type FuncPolicy struct {
	// Confidentiality is the required confidentiality tier for the function.
	Confidentiality Confidentiality `json:"confidentiality" yaml:"confidentiality"`
	// Execution is the target execution environment for the function.
	Execution ExecutionMode `json:"execution" yaml:"execution"`
	// Authorization defines the caller authorization requirements before invoking this function.
	Authorization Authorization `json:"authorization" yaml:"authorization"`
	// Fallback is an optional weaker execution mode to adopt if the target platform cannot provide Execution.
	Fallback ExecutionMode `json:"fallback,omitempty" yaml:"fallback,omitempty"`
	// FallbackAcknowledged must be true whenever Fallback is specified, confirming developer consent to degraded security.
	FallbackAcknowledged bool `json:"fallback_acknowledged,omitempty" yaml:"fallback_acknowledged,omitempty"`
}

// Confidentiality is the requested confidentiality level for a function.
type Confidentiality string

const (
	// ConfidentialityLow designates non-sensitive public or semi-public data and logic.
	ConfidentialityLow Confidentiality = "low"
	// ConfidentialityStandard designates business-standard internal operational data.
	ConfidentialityStandard Confidentiality = "standard"
	// ConfidentialityHigh designates sensitive customer data, PII, and financial records.
	ConfidentialityHigh Confidentiality = "high"
	// ConfidentialityCritical designates cryptographic root keys, master secrets, and core signing logic.
	ConfidentialityCritical Confidentiality = "critical"
)

// Valid reports whether the confidentiality level is recognized.
func (c Confidentiality) Valid() bool {
	switch c {
	case ConfidentialityLow, ConfidentialityStandard, ConfidentialityHigh, ConfidentialityCritical:
		return true
	default:
		return false
	}
}

// ExecutionMode is how a function should execute.
type ExecutionMode string

const (
	// ExecOrdinaryLocal runs the function as an ordinary local process without specialized isolation.
	ExecOrdinaryLocal ExecutionMode = "ordinary_local"
	// ExecHardenedLocal runs the function in a locally hardened process with memory protections.
	ExecHardenedLocal ExecutionMode = "hardened_local"
	// ExecIsolatedLocal runs the function inside a local hardware-isolated enclave (e.g., AMD SEV-SNP, Intel SGX).
	ExecIsolatedLocal ExecutionMode = "isolated_local"
	// ExecRemoteConfidential delegates function execution to a remote verified confidential enclave service.
	ExecRemoteConfidential ExecutionMode = "remote_confidential"
	// ExecConfidentialWhenAvailable runs opportunistically in a confidential enclave (local or remote) if supported,
	// or gracefully downgrades to local execution if confidential computing is unavailable.
	ExecConfidentialWhenAvailable ExecutionMode = "confidential_when_available"
)

// Valid reports whether the execution mode is one of the recognized modes.
func (e ExecutionMode) Valid() bool {
	switch e {
	case ExecOrdinaryLocal,
		ExecHardenedLocal,
		ExecIsolatedLocal,
		ExecRemoteConfidential,
		ExecConfidentialWhenAvailable:
		return true
	default:
		return false
	}
}

// Rank returns the relative isolation strength of the execution mode.
// ordinary_local (1) < hardened_local (2) < isolated_local (3) < remote_confidential (4).
// Returns 0 for invalid modes.
func (e ExecutionMode) Rank() int {
	switch e {
	case ExecOrdinaryLocal:
		return 1
	case ExecHardenedLocal:
		return 2
	case ExecIsolatedLocal:
		return 3
	case ExecRemoteConfidential:
		return 4
	case ExecConfidentialWhenAvailable:
		return 4
	default:
		return 0
	}
}

// Authorization is the authorization requirement.
type Authorization string

const (
	// AuthNone requires no caller authentication or authorization checks.
	AuthNone Authorization = "none"
	// AuthRequired requires valid session or role-based authorization.
	AuthRequired Authorization = "required"
	// AuthPerCall requires cryptographic per-call token authorization and fresh signature verification.
	AuthPerCall Authorization = "per_call"
)

// Valid reports whether the authorization requirement is recognized.
func (a Authorization) Valid() bool {
	switch a {
	case AuthNone, AuthRequired, AuthPerCall:
		return true
	default:
		return false
	}
}
