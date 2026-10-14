package policy

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

// PlatformCapabilities describes what a target deployment platform can deliver.
type PlatformCapabilities struct {
	// Name identifies the platform (e.g., "linux-desktop", "android", "ios", "web").
	Name string `json:"name" yaml:"name"`
	// SupportsHardenedLocal indicates if the platform supports ASLR, memory safety, and stack protection.
	SupportsHardenedLocal bool `json:"supports_hardened_local" yaml:"supports_hardened_local"`
	// SupportsIsolatedLocal indicates if the platform has hardware enclave or confidential VM support.
	SupportsIsolatedLocal bool `json:"supports_isolated_local" yaml:"supports_isolated_local"`
	// SupportsRemoteConfidential indicates if the platform can connect to remote confidential enclaves.
	SupportsRemoteConfidential bool `json:"supports_remote_confidential" yaml:"supports_remote_confidential"`
	// SupportsAttestation indicates if the platform can produce or verify cryptographic hardware attestation tokens.
	SupportsAttestation bool `json:"supports_attestation" yaml:"supports_attestation"`
	// Limitations lists known security gaps, unmitigated attack vectors, or platform boundaries.
	Limitations []string `json:"limitations,omitempty" yaml:"limitations,omitempty"`
}

// SupportsMode reports whether the platform supports a specific execution mode directly.
func (caps PlatformCapabilities) SupportsMode(mode ExecutionMode) bool {
	switch mode {
	case ExecOrdinaryLocal:
		return true
	case ExecHardenedLocal:
		return caps.SupportsHardenedLocal
	case ExecIsolatedLocal:
		return caps.SupportsIsolatedLocal
	case ExecRemoteConfidential:
		return caps.SupportsRemoteConfidential
	case ExecConfidentialWhenAvailable:
		// Opportunistic mode can always execute by selecting the highest available tier.
		return true
	default:
		return false
	}
}

// PlatformReport describes policy compatibility with a specific target platform.
type PlatformReport struct {
	// Platform is the name of the evaluated platform.
	Platform string `json:"platform" yaml:"platform"`
	// Functions details the compatibility and assigned execution tier for each function.
	Functions []FuncPlatformReport `json:"functions" yaml:"functions"`
	// Errors records fatal platform incompatibilities that prevent execution.
	Errors []string `json:"errors,omitempty" yaml:"errors,omitempty"`
	// Warnings records security downgrades or advisory notices.
	Warnings []string `json:"warnings,omitempty" yaml:"warnings,omitempty"`
}

// Supported reports whether all functions in the policy can be executed on this platform
// (either directly, through acknowledged fallbacks, or opportunistic downgrades).
func (pr *PlatformReport) Supported() bool {
	return pr != nil && len(pr.Errors) == 0
}

// HasDowngrades reports whether any function was downgraded to a weaker execution mode.
func (pr *PlatformReport) HasDowngrades() bool {
	if pr == nil {
		return false
	}
	for _, fn := range pr.Functions {
		if fn.Downgraded {
			return true
		}
	}
	return false
}

// FunctionReport returns the report entry for a given function name.
func (pr *PlatformReport) FunctionReport(functionName string) (FuncPlatformReport, bool) {
	if pr == nil {
		return FuncPlatformReport{}, false
	}
	for _, fn := range pr.Functions {
		if fn.Function == functionName {
			return fn, true
		}
	}
	return FuncPlatformReport{}, false
}

// FuncPlatformReport describes the platform compatibility outcome for one function.
type FuncPlatformReport struct {
	// Function is the name of the function.
	Function string `json:"function" yaml:"function"`
	// RequestedExecution is the mode declared in the policy.
	RequestedExecution ExecutionMode `json:"requested_execution" yaml:"requested_execution"`
	// ActualExecution is the resolved execution mode on the evaluated platform.
	ActualExecution ExecutionMode `json:"actual_execution" yaml:"actual_execution"`
	// Supported is true if the function can run on the platform (including via fallback).
	Supported bool `json:"supported" yaml:"supported"`
	// Downgraded is true if ActualExecution is weaker than RequestedExecution.
	Downgraded bool `json:"downgraded" yaml:"downgraded"`
	// ResidualRisk describes the unmitigated security risks for the assigned execution mode.
	ResidualRisk string `json:"residual_risk" yaml:"residual_risk"`
}

var platformReportPool = sync.Pool{
	New: func() any {
		return &PlatformReport{
			Functions: make([]FuncPlatformReport, 0, 16),
			Errors:    make([]string, 0, 4),
			Warnings:  make([]string, 0, 4),
		}
	},
}

// CheckPlatform evaluates an Aegis Policy against the capabilities of a deployment platform.
//
// Compatibility rules:
//   - Each function's requested execution mode is checked against platform capabilities.
//   - If the requested mode is supported, it is adopted directly without downgrading.
//   - If not supported and Fallback is specified with FallbackAcknowledged=true, the fallback is used.
//   - If not supported and no valid fallback is acknowledged, an error is reported.
//   - For confidential_when_available, execution opportunistically selects the highest available confidential
//     mode (isolated local or remote confidential), and downgrades gracefully to hardened or ordinary local
//     if no confidential environment is available.
//   - ResidualRisk is always populated with an honest description of unmitigated exposure.
func CheckPlatform(p *Policy, caps PlatformCapabilities) *PlatformReport {
	report := &PlatformReport{
		Platform:  caps.Name,
		Functions: make([]FuncPlatformReport, 0),
		Errors:    make([]string, 0),
		Warnings:  make([]string, 0),
	}

	if p == nil {
		report.Errors = append(report.Errors, "policy: nil policy provided for platform check")
		return report
	}

	funcNames := make([]string, 0, len(p.Functions))
	for name := range p.Functions {
		funcNames = append(funcNames, name)
	}
	sort.Strings(funcNames)

	for _, name := range funcNames {
		fp := p.Functions[name]
		fnReport := evaluateFunctionPlatform(name, fp, caps, report)
		report.Functions = append(report.Functions, fnReport)
	}

	return report
}

func evaluateFunctionPlatform(name string, fp FuncPolicy, caps PlatformCapabilities, report *PlatformReport) FuncPlatformReport {
	req := fp.Execution

	// Case 1: confidential_when_available (opportunistic confidentiality)
	if req == ExecConfidentialWhenAvailable {
		if caps.SupportsIsolatedLocal {
			return FuncPlatformReport{
				Function:           name,
				RequestedExecution: req,
				ActualExecution:    ExecIsolatedLocal,
				Supported:          true,
				Downgraded:         false,
				ResidualRisk:       residualRiskForExecution(ExecIsolatedLocal, caps),
			}
		}
		if caps.SupportsRemoteConfidential {
			return FuncPlatformReport{
				Function:           name,
				RequestedExecution: req,
				ActualExecution:    ExecRemoteConfidential,
				Supported:          true,
				Downgraded:         false,
				ResidualRisk:       residualRiskForExecution(ExecRemoteConfidential, caps),
			}
		}

		// Downgrade gracefully to hardened local or ordinary local
		var actual ExecutionMode
		if caps.SupportsHardenedLocal {
			actual = ExecHardenedLocal
		} else {
			actual = ExecOrdinaryLocal
		}

		warningMsg := fmt.Sprintf("policy: function %q requested %s, downgraded to %s on platform %s", name, req, actual, caps.Name)
		report.Warnings = append(report.Warnings, warningMsg)

		return FuncPlatformReport{
			Function:           name,
			RequestedExecution: req,
			ActualExecution:    actual,
			Supported:          true,
			Downgraded:         true,
			ResidualRisk:       downgradedResidualRisk(req, actual, caps),
		}
	}

	// Case 2: Directly supported mode
	if caps.SupportsMode(req) {
		return FuncPlatformReport{
			Function:           name,
			RequestedExecution: req,
			ActualExecution:    req,
			Supported:          true,
			Downgraded:         false,
			ResidualRisk:       residualRiskForExecution(req, caps),
		}
	}

	// Case 3: Mode unsupported, check fallback
	if fp.Fallback != "" && fp.FallbackAcknowledged {
		if caps.SupportsMode(fp.Fallback) {
			warningMsg := fmt.Sprintf("policy: function %q requested %s (unsupported on %s); downgraded to acknowledged fallback %s", name, req, caps.Name, fp.Fallback)
			report.Warnings = append(report.Warnings, warningMsg)

			return FuncPlatformReport{
				Function:           name,
				RequestedExecution: req,
				ActualExecution:    fp.Fallback,
				Supported:          true,
				Downgraded:         true,
				ResidualRisk:       downgradedResidualRisk(req, fp.Fallback, caps),
			}
		}

		// Fallback itself is unsupported on platform
		errMsg := fmt.Sprintf("policy: function %q requested %s and fallback %s; neither is supported on platform %s", name, req, fp.Fallback, caps.Name)
		report.Errors = append(report.Errors, errMsg)

		return FuncPlatformReport{
			Function:           name,
			RequestedExecution: req,
			ActualExecution:    fp.Fallback,
			Supported:          false,
			Downgraded:         false,
			ResidualRisk:       fmt.Sprintf("Platform %s supports neither requested mode %s nor fallback mode %s.", caps.Name, req, fp.Fallback),
		}
	}

	// Case 4: Mode unsupported, fallback configured but unacknowledged
	if fp.Fallback != "" && !fp.FallbackAcknowledged {
		errMsg := fmt.Sprintf("policy: function %q requested %s which is unsupported on %s, and fallback %s is not acknowledged", name, req, caps.Name, fp.Fallback)
		report.Errors = append(report.Errors, errMsg)

		return FuncPlatformReport{
			Function:           name,
			RequestedExecution: req,
			ActualExecution:    req,
			Supported:          false,
			Downgraded:         false,
			ResidualRisk:       fmt.Sprintf("Execution mode %s is unsupported on %s and fallback %s is unacknowledged.", req, caps.Name, fp.Fallback),
		}
	}

	// Case 5: Mode unsupported, no fallback specified
	errMsg := fmt.Sprintf("policy: function %q requested %s which is unsupported on platform %s and no fallback is specified", name, req, caps.Name)
	report.Errors = append(report.Errors, errMsg)

	return FuncPlatformReport{
		Function:           name,
		RequestedExecution: req,
		ActualExecution:    req,
		Supported:          false,
		Downgraded:         false,
		ResidualRisk:       fmt.Sprintf("Execution mode %s is unsupported on platform %s with no fallback path available.", req, caps.Name),
	}
}

func residualRiskForExecution(mode ExecutionMode, caps PlatformCapabilities) string {
	var base string
	switch mode {
	case ExecOrdinaryLocal:
		base = "Process memory is accessible to host OS, root/admin users, and co-located debugging tools."
	case ExecHardenedLocal:
		base = "Protected by memory hardening and ASLR; privileged root users and kernel-level compromise remain unmitigated."
	case ExecIsolatedLocal:
		base = "Hardware enclave/TEE isolation protects memory from host; residual risks include CPU side-channel attacks and enclave host boundary flaws."
	case ExecRemoteConfidential:
		base = "Remote confidential enclave protects memory; residual risks include network transit latency, enclave attestation trust, and cloud operator microcode."
	default:
		base = "Unknown execution mode risk profile."
	}

	if len(caps.Limitations) > 0 {
		return fmt.Sprintf("%s Platform limitations: %s.", base, strings.Join(caps.Limitations, "; "))
	}
	return base
}

func downgradedResidualRisk(requested, actual ExecutionMode, caps PlatformCapabilities) string {
	actualRisk := residualRiskForExecution(actual, caps)
	return fmt.Sprintf("Security downgraded from requested %s to %s because platform %s lacks hardware or environment support. %s", requested, actual, caps.Name, actualRisk)
}
