// Package integrity provides runtime environment sanity and anti-tamper
// verification for the Aegis protected execution system.
//
// It performs defense-in-depth pre-flight and in-flight probes to ensure that
// guest IR modules and virtual machine state execute within an uncompromised,
// untraced, and unhooked host process.
package integrity

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// Standard severity classifications for integrity probe outcomes.
const (
	// SeverityInfo indicates an informational finding or clean check.
	SeverityInfo = "info"
	// SeverityLow indicates a minor environmental anomaly that does not immediately compromise security.
	SeverityLow = "low"
	// SeverityMedium indicates a moderate deviation requiring logging or heightened surveillance.
	SeverityMedium = "medium"
	// SeverityHigh indicates a serious anomaly, such as single-stepping or probe failures.
	SeverityHigh = "high"
	// SeverityCritical indicates an active compromise, such as an attached debugger or injected preload libraries.
	SeverityCritical = "critical"
)

// builderPool recycles strings.Builder allocations during report generation.
var builderPool = sync.Pool{
	New: func() any {
		return new(strings.Builder)
	},
}

// CheckResult represents the outcome of an individual integrity check.
type CheckResult struct {
	// Check is the unique name of the probe that produced this result.
	Check string `json:"check"`
	// Passed indicates whether the environment satisfied the integrity invariant.
	Passed bool `json:"passed"`
	// Evidence provides diagnostic detail or forensic data explaining the outcome.
	Evidence string `json:"evidence"`
	// Severity reflects the security impact if the check failed.
	Severity string `json:"severity"`
}

// Check defines the interface for an integrity or tamper-detection probe.
type Check interface {
	// Name returns a human-readable identifier for this integrity check.
	Name() string
	// Run executes the integrity probe within the supplied context.
	Run(ctx context.Context) CheckResult
}

// Checker coordinates the execution of multiple integrity checks and generates reports.
type Checker struct {
	mu     sync.RWMutex
	checks []Check
}

// NewChecker creates a new Checker instance configured with the specified checks.
// Nil checks are ignored.
func NewChecker(checks ...Check) *Checker {
	validChecks := make([]Check, 0, len(checks))
	for _, candidate := range checks {
		if candidate != nil {
			validChecks = append(validChecks, candidate)
		}
	}
	return &Checker{
		checks: validChecks,
	}
}

// DefaultChecker creates a Checker pre-populated with standard integrity checks:
// debugger detection, timing anomaly analysis, and dynamic environment verification.
func DefaultChecker() *Checker {
	return NewChecker(
		NewDebuggerCheck(),
		NewTimingCheck(),
		NewEnvironmentCheck(),
	)
}

// AddCheck appends an additional integrity check to the checker suite.
func (c *Checker) AddCheck(check Check) {
	if check == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.checks = append(c.checks, check)
}

// Checks returns a slice containing the currently registered integrity checks.
func (c *Checker) Checks() []Check {
	c.mu.RLock()
	defer c.mu.RUnlock()
	copied := make([]Check, len(c.checks))
	copy(copied, c.checks)
	return copied
}

// Run executes all registered checks in sequence, terminating early if the context is cancelled.
func (c *Checker) Run(ctx context.Context) []CheckResult {
	c.mu.RLock()
	registeredChecks := make([]Check, len(c.checks))
	copy(registeredChecks, c.checks)
	c.mu.RUnlock()

	results := make([]CheckResult, 0, len(registeredChecks))
	for _, probe := range registeredChecks {
		if err := ctx.Err(); err != nil {
			results = append(results, CheckResult{
				Check:    probe.Name(),
				Passed:   false,
				Evidence: fmt.Sprintf("integrity: check aborted due to context cancellation: %v", err),
				Severity: SeverityCritical,
			})
			break
		}
		result := probe.Run(ctx)
		results = append(results, result)
	}
	return results
}

// Summary aggregates check results, determining overall pass/fail status and
// returning a formatted textual report.
func (c *Checker) Summary(results []CheckResult) (passed bool, report string) {
	return Summarize(results)
}

// Summarize is a package-level helper that aggregates check results into an overall
// verdict and human-readable diagnostic report.
func Summarize(results []CheckResult) (passed bool, report string) {
	if len(results) == 0 {
		return true, "integrity: no checks executed"
	}

	builder, ok := builderPool.Get().(*strings.Builder)
	if !ok || builder == nil {
		builder = new(strings.Builder)
	} else {
		builder.Reset()
	}
	defer builderPool.Put(builder)

	passedCount := 0
	failedCount := 0
	allPassed := true

	for _, result := range results {
		if result.Passed {
			passedCount++
		} else {
			failedCount++
			allPassed = false
		}
	}

	status := "PASSED"
	if !allPassed {
		status = "FAILED"
	}

	fmt.Fprintf(builder, "integrity check summary: %s (%d/%d checks passed)\n", status, passedCount, len(results))
	for _, result := range results {
		marker := "PASS"
		if !result.Passed {
			marker = "FAIL"
		}
		if result.Severity != "" {
			fmt.Fprintf(builder, "  [%s] %s [%s]: %s\n", marker, result.Check, result.Severity, result.Evidence)
		} else {
			fmt.Fprintf(builder, "  [%s] %s: %s\n", marker, result.Check, result.Evidence)
		}
	}

	return allPassed, strings.TrimRight(builder.String(), "\n")
}
