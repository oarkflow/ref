package platform

import (
	"fmt"

	"github.com/oarkflow/ref/aegis/policy"
)

// Capabilities describes the architectural security properties, hardware isolation guarantees,
// and residual threat exposures for an Aegis execution backend.
type Capabilities struct {
	// ResistsStaticAnalysis indicates whether bytecode and internal symbols resist static inspection.
	ResistsStaticAnalysis bool `json:"resists_static_analysis"`

	// ResistsRuntimeInstrumentation indicates whether the backend detects or prevents dynamic hooking,
	// memory patching, and debugging instrumentation.
	ResistsRuntimeInstrumentation bool `json:"resists_runtime_instrumentation"`

	// ResistsPrivilegedAttacker indicates whether isolation holds against host root, administrator,
	// kernel, or hypervisor attackers.
	//
	// IMPORTANT: This MUST ONLY be set to true for verified hardware-backed confidential computing
	// environments (e.g. Intel SGX, AMD SEV-SNP, Arm CCA). Setting this to true for ordinary
	// desktop or mobile platforms is an integrity violation.
	ResistsPrivilegedAttacker bool `json:"resists_privileged_attacker"`

	// HasHardwareIsolation indicates whether guest code executes within a hardware-enforced
	// isolation boundary (CPU enclave or confidential VM).
	HasHardwareIsolation bool `json:"has_hardware_isolation"`

	// HasRemoteAttestation indicates whether the backend can generate cryptographically verifiable
	// remote attestation statements.
	HasRemoteAttestation bool `json:"has_remote_attestation"`

	// HasSecureKeyStorage indicates whether hardware-backed cryptographic storage (e.g., TPM, Secure Enclave, StrongBox)
	// is available for persistent keying.
	HasSecureKeyStorage bool `json:"has_secure_key_storage"`

	// HasMemoryEncryption indicates whether guest physical memory pages are transparently encrypted
	// by hardware memory controllers against bus snooping and physical DRAM probing.
	HasMemoryEncryption bool `json:"has_memory_encryption"`

	// Limitations enumerates platform boundaries, unmitigated attack surfaces, or environmental constraints.
	Limitations []string `json:"limitations,omitempty"`

	// ResidualRisks enumerates unmitigated security risks inherent to this execution environment.
	ResidualRisks []string `json:"residual_risks,omitempty"`
}

// ValidateAgainstPolicy checks whether the backend capabilities directly satisfy the requested
// execution mode.
//
// Rule 3 Enforcement: Modes requiring hardware isolation (such as isolated_local) are rejected
// unless the backend possesses true hardware isolation (HasHardwareIsolation=true and ResistsPrivilegedAttacker=true).
func (c Capabilities) ValidateAgainstPolicy(p policy.ExecutionMode) (bool, string) {
	switch p {
	case policy.ExecOrdinaryLocal:
		return true, ""

	case policy.ExecHardenedLocal:
		if !c.ResistsStaticAnalysis || !c.ResistsRuntimeInstrumentation {
			return false, "platform lacks static analysis or runtime instrumentation resistance required for hardened_local"
		}
		return true, ""

	case policy.ExecIsolatedLocal:
		if !c.HasHardwareIsolation || !c.ResistsPrivilegedAttacker {
			return false, "platform lacks hardware-backed isolation (ResistsPrivilegedAttacker) required for isolated_local"
		}
		return true, ""

	case policy.ExecRemoteConfidential:
		if !c.HasRemoteAttestation {
			return false, "platform lacks remote attestation capability required for remote_confidential"
		}
		return true, ""

	case policy.ExecConfidentialWhenAvailable:
		// Opportunistic mode can always execute on any platform, selecting the strongest available tier.
		return true, ""

	default:
		return false, fmt.Sprintf("platform: unrecognized execution mode %q", p)
	}
}

// ValidatePolicy evaluates an execution mode with an optional fallback and explicit developer acknowledgment.
//
// Under Rule 3, any security downgrade from an unsupported execution mode to a weaker mode is
// strictly rejected unless Fallback is non-empty AND fallbackAcknowledged is true.
func (c Capabilities) ValidatePolicy(req policy.ExecutionMode, fallback policy.ExecutionMode, fallbackAcknowledged bool) (bool, string) {
	// 1. Direct support check
	if ok, _ := c.ValidateAgainstPolicy(req); ok {
		return true, ""
	}

	// 2. Mode is unsupported; check fallback
	if fallback != "" && fallbackAcknowledged {
		if ok, _ := c.ValidateAgainstPolicy(fallback); ok {
			return true, fmt.Sprintf("policy: downgraded from %s to acknowledged fallback %s", req, fallback)
		}
		return false, fmt.Sprintf("platform supports neither requested mode %s nor fallback mode %s", req, fallback)
	}

	if fallback != "" && !fallbackAcknowledged {
		return false, fmt.Sprintf("platform: execution mode %s is unsupported, and fallback %s is not acknowledged (rule 3: unacknowledged downgrade rejected)", req, fallback)
	}

	return false, fmt.Sprintf("platform: execution mode %s is unsupported and no fallback is acknowledged (rule 3: downgrade rejected)", req)
}

// ValidateFuncPolicy evaluates a function's policy definition against backend capabilities,
// strictly enforcing Rule 3: downgrades to a weaker execution tier are rejected
// unless a valid fallback is specified and FallbackAcknowledged is true.
func (c Capabilities) ValidateFuncPolicy(fp policy.FuncPolicy) (bool, string) {
	return c.ValidatePolicy(fp.Execution, fp.Fallback, fp.FallbackAcknowledged)
}

// IsHonest validates that ResistsPrivilegedAttacker is never asserted without true hardware isolation
// and memory encryption.
func (c Capabilities) IsHonest() bool {
	if c.ResistsPrivilegedAttacker && (!c.HasHardwareIsolation || !c.HasMemoryEncryption) {
		return false
	}
	return true
}

// ToPolicyCapabilities maps Capabilities into policy.PlatformCapabilities for interoperability
// with policy.CheckPlatform.
func (c Capabilities) ToPolicyCapabilities(name string) policy.PlatformCapabilities {
	return policy.PlatformCapabilities{
		Name:                       name,
		SupportsHardenedLocal:      c.ResistsStaticAnalysis && c.ResistsRuntimeInstrumentation,
		SupportsIsolatedLocal:      c.HasHardwareIsolation && c.ResistsPrivilegedAttacker,
		SupportsRemoteConfidential: c.HasRemoteAttestation,
		SupportsAttestation:        c.HasRemoteAttestation,
		Limitations:                c.Limitations,
	}
}
