package policy

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

// ValidationResult holds all discovered errors and warnings for a validated policy.
type ValidationResult struct {
	// Errors contains fatal rule violations that prevent safe policy enforcement.
	Errors []string `json:"errors,omitempty" yaml:"errors,omitempty"`
	// Warnings contains non-fatal security or configuration advisories.
	Warnings []string `json:"warnings,omitempty" yaml:"warnings,omitempty"`
}

var validationResultPool = sync.Pool{
	New: func() any {
		return &ValidationResult{
			Errors:   make([]string, 0, 4),
			Warnings: make([]string, 0, 4),
		}
	},
}

// AcquireValidationResult retrieves a zeroed ValidationResult from the pool.
func AcquireValidationResult() *ValidationResult {
	res := validationResultPool.Get().(*ValidationResult)
	res.Errors = res.Errors[:0]
	res.Warnings = res.Warnings[:0]
	return res
}

// ReleaseValidationResult returns a ValidationResult to the pool.
func ReleaseValidationResult(res *ValidationResult) {
	if res == nil {
		return
	}
	validationResultPool.Put(res)
}

// OK reports whether the policy is valid (i.e., contains zero errors).
func (v *ValidationResult) OK() bool {
	return v == nil || len(v.Errors) == 0
}

// Error concatenates all validation errors into a single string to satisfy the error interface.
// If there are no errors, it returns an empty string.
func (v *ValidationResult) Error() string {
	if v == nil || len(v.Errors) == 0 {
		return ""
	}
	return strings.Join(v.Errors, "; ")
}

// Err returns the ValidationResult as an error if it contains errors, or nil if there are none.
func (v *ValidationResult) Err() error {
	if v == nil || len(v.Errors) == 0 {
		return nil
	}
	return v
}

// Validate checks an Aegis policy against structural integrity and security constraints.
//
// Validation rules:
//   - Application.ID must be non-empty
//   - Application.Version must be non-empty
//   - Protection.Default must be a valid ProtectionLevel
//   - Each FuncPolicy must have valid Confidentiality, Execution, and Authorization values
//   - If a function has Fallback set, FallbackAcknowledged must be true
//   - Fallback must be a strictly weaker level than Execution (ordinary < hardened < isolated < remote)
//   - If Execution is isolated_local or remote_confidential and Confidentiality is low, a warning is added
func Validate(p *Policy) *ValidationResult {
	res := &ValidationResult{
		Errors:   make([]string, 0),
		Warnings: make([]string, 0),
	}

	if p == nil {
		res.Errors = append(res.Errors, "policy: policy cannot be nil")
		return res
	}

	// 1. Application.ID non-empty
	if strings.TrimSpace(p.Application.ID) == "" {
		res.Errors = append(res.Errors, "policy: application.id is required and cannot be empty")
	}

	// 2. Application.Version non-empty
	if strings.TrimSpace(p.Application.Version) == "" {
		res.Errors = append(res.Errors, "policy: application.version is required and cannot be empty")
	}

	// 3. Protection.Default valid
	if !p.Protection.Default.Valid() {
		res.Errors = append(res.Errors, fmt.Sprintf("policy: invalid protection.default %q", p.Protection.Default))
	}

	// Sort function names for deterministic output
	funcNames := make([]string, 0, len(p.Functions))
	for name := range p.Functions {
		funcNames = append(funcNames, name)
	}
	sort.Strings(funcNames)

	// 4. Function policies validation
	for _, name := range funcNames {
		fp := p.Functions[name]

		if !fp.Confidentiality.Valid() {
			res.Errors = append(res.Errors, fmt.Sprintf("policy: function %q has invalid confidentiality %q", name, fp.Confidentiality))
		}

		if !fp.Execution.Valid() {
			res.Errors = append(res.Errors, fmt.Sprintf("policy: function %q has invalid execution mode %q", name, fp.Execution))
		}

		if !fp.Authorization.Valid() {
			res.Errors = append(res.Errors, fmt.Sprintf("policy: function %q has invalid authorization %q", name, fp.Authorization))
		}

		// Fallback checks
		if fp.Fallback != "" {
			if !fp.Fallback.Valid() {
				res.Errors = append(res.Errors, fmt.Sprintf("policy: function %q has invalid fallback execution mode %q", name, fp.Fallback))
			} else if fp.Fallback == ExecConfidentialWhenAvailable {
				res.Errors = append(res.Errors, fmt.Sprintf("policy: function %q fallback cannot be %q", name, ExecConfidentialWhenAvailable))
			} else if fp.Execution.Valid() {
				// Fallback must be weaker level than Execution: ordinary < hardened < isolated < remote
				if fp.Fallback.Rank() >= fp.Execution.Rank() {
					res.Errors = append(res.Errors, fmt.Sprintf("policy: function %q fallback %q must be a weaker level than execution %q", name, fp.Fallback, fp.Execution))
				}
			}

			if !fp.FallbackAcknowledged {
				res.Errors = append(res.Errors, fmt.Sprintf("policy: function %q has fallback %q set but fallback_acknowledged is false", name, fp.Fallback))
			}
		}

		// Advisory warning: isolated_local or remote_confidential with low confidentiality
		if (fp.Execution == ExecIsolatedLocal || fp.Execution == ExecRemoteConfidential) && fp.Confidentiality == ConfidentialityLow {
			res.Warnings = append(res.Warnings, fmt.Sprintf("policy: function %q requests %s execution but confidentiality is low", name, fp.Execution))
		}
	}

	return res
}

// Validate runs policy validation and returns an error if any fatal rule violations occurred.
func (p *Policy) Validate() error {
	res := Validate(p)
	return res.Err()
}
