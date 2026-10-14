package lab

import (
	"context"
	"fmt"

	"github.com/oarkflow/ref/aegis/ir"
	"github.com/oarkflow/ref/aegis/vm"
)

const defaultExecutionFuel = 10_000_000

// TestCaseResult records the execution results and metrics for a single dynamic test case.
type TestCaseResult struct {
	Function         string   `json:"function"`
	Args             []uint64 `json:"args"`
	Expected         []uint64 `json:"expected,omitempty"`
	OriginalResults  []uint64 `json:"original_results"`
	ProtectedResults []uint64 `json:"protected_results"`
	OriginalSteps    uint64   `json:"original_steps"`
	ProtectedSteps   uint64   `json:"protected_steps"`
	OriginalError    string   `json:"original_error,omitempty"`
	ProtectedError   string   `json:"protected_error,omitempty"`
	Equivalent       bool     `json:"equivalent"`
}

// DynamicReport summarizes dynamic VM execution and input/output equivalence verification.
type DynamicReport struct {
	InputOutputEquivalent bool             `json:"input_output_equivalent"`
	TotalOriginalSteps    uint64           `json:"total_original_steps"`
	TotalProtectedSteps   uint64           `json:"total_protected_steps"`
	TraceOverhead         float64          `json:"trace_overhead"`
	TestCaseResults       []TestCaseResult `json:"test_case_results"`
}

// AnalyzeDynamic executes original and protected modules on the Aegis VM across all test cases.
func AnalyzeDynamic(ctx context.Context, orig, prot *ir.Module, testCases []TestCase) (*DynamicReport, error) {
	if orig == nil {
		return nil, fmt.Errorf("lab: original module cannot be nil")
	}
	if prot == nil {
		return nil, fmt.Errorf("lab: protected module cannot be nil")
	}

	report := &DynamicReport{
		InputOutputEquivalent: true,
		TestCaseResults:       make([]TestCaseResult, 0, len(testCases)),
	}

	if len(testCases) == 0 {
		report.TraceOverhead = 1.0
		return report, nil
	}

	for _, tc := range testCases {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("lab: dynamic analysis cancelled: %w", err)
		}

		res := executeTestCase(ctx, orig, prot, tc)
		report.TestCaseResults = append(report.TestCaseResults, res)

		report.TotalOriginalSteps += res.OriginalSteps
		report.TotalProtectedSteps += res.ProtectedSteps

		if !res.Equivalent {
			report.InputOutputEquivalent = false
		}
	}

	if report.TotalOriginalSteps > 0 {
		report.TraceOverhead = roundFloat(float64(report.TotalProtectedSteps)/float64(report.TotalOriginalSteps), 4)
	} else {
		report.TraceOverhead = 1.0
	}

	return report, nil
}

func executeTestCase(ctx context.Context, orig, prot *ir.Module, tc TestCase) TestCaseResult {
	res := TestCaseResult{
		Function: tc.Function,
		Args:     tc.Args,
		Expected: tc.Expected,
	}

	// 1. Run Original on VM
	origCfg := vm.DefaultConfig()
	origCfg.InitialFuel = defaultExecutionFuel

	vmOrig, err := vm.New(orig, origCfg)
	if err != nil {
		res.OriginalError = fmt.Sprintf("vm.New failed: %v", err)
	} else {
		origOut, errCall := vmOrig.Call(ctx, tc.Function, tc.Args...)
		if errCall != nil {
			res.OriginalError = errCall.Error()
		} else {
			res.OriginalResults = origOut
		}
		res.OriginalSteps = uint64(defaultExecutionFuel - vmOrig.RemainingFuel())
		_ = vmOrig.Close()
	}

	// 2. Run Protected on VM
	protCfg := vm.DefaultConfig()
	protCfg.InitialFuel = defaultExecutionFuel

	vmProt, err := vm.New(prot, protCfg)
	if err != nil {
		res.ProtectedError = fmt.Sprintf("vm.New failed: %v", err)
	} else {
		protOut, errCall := vmProt.Call(ctx, tc.Function, tc.Args...)
		if errCall != nil {
			res.ProtectedError = errCall.Error()
		} else {
			res.ProtectedResults = protOut
		}
		res.ProtectedSteps = uint64(defaultExecutionFuel - vmProt.RemainingFuel())
		_ = vmProt.Close()
	}

	// 3. Evaluate Equivalence
	if res.OriginalError != "" || res.ProtectedError != "" {
		// If both errored in the same way, treat as equivalent
		if res.OriginalError != "" && res.ProtectedError != "" {
			res.Equivalent = true
		} else {
			res.Equivalent = false
		}
		return res
	}

	// Both succeeded: check output slice equality
	if !equalUint64Slices(res.OriginalResults, res.ProtectedResults) {
		res.Equivalent = false
		return res
	}

	// If expected results were specified, check that original results matched expectation
	if len(tc.Expected) > 0 && !equalUint64Slices(res.OriginalResults, tc.Expected) {
		res.Equivalent = false
		return res
	}

	res.Equivalent = true
	return res
}

func equalUint64Slices(a, b []uint64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
