// Package lab provides reverse-engineering resistance benchmarking, static recovery
// analysis, and dynamic VM execution evaluation for Aegis IR modules.
package lab

import (
	"github.com/oarkflow/ref/aegis/ir"
)

// Reverse-engineering difficulty classifications.
const (
	// DifficultyLow represents minimal or absent protection.
	DifficultyLow = "Low"
	// DifficultyMedium represents basic obfuscation or partial stripping.
	DifficultyMedium = "Medium"
	// DifficultyHigh represents thorough obfuscation with hidden constants and mangled CFG.
	DifficultyHigh = "High"
	// DifficultyVeryHigh represents comprehensive multi-layer protection resisting automated recovery.
	DifficultyVeryHigh = "Very High"
)

// TestCase defines an input argument set and expected return values for dynamic VM evaluation.
type TestCase struct {
	// Function is the name of the exported function to invoke.
	Function string `json:"function"`
	// Args contains the numerical arguments passed to the function.
	Args []uint64 `json:"args"`
	// Expected contains the expected return values (empty if unasserted).
	Expected []uint64 `json:"expected,omitempty"`
}

// RecoveryBenchmark specifies the benchmark suite comparing an unprotected original module
// against its protected counterpart.
type RecoveryBenchmark struct {
	// Name is a human-readable identifier for the benchmark target.
	Name string `json:"name"`
	// Original is the baseline, unprotected IR module.
	Original *ir.Module `json:"original"`
	// Protected is the hardened, transformed IR module.
	Protected *ir.Module `json:"protected"`
	// TestCases contains functional input/output verification scenarios.
	TestCases []TestCase `json:"test_cases,omitempty"`
}

// RecoveryReport consolidates static recovery findings, dynamic execution equivalence,
// and overall reverse-engineering resistance assessment.
type RecoveryReport struct {
	// BenchmarkName is the identifier of the benchmark run.
	BenchmarkName string `json:"benchmark_name"`
	// FunctionBoundariesRecovered is the percentage of named/discernible function boundaries.
	FunctionBoundariesRecovered float64 `json:"function_boundaries_recovered"`
	// ControlFlowRecovered is the measure of control flow graph reconstruction similarity (0.0 to 100.0).
	ControlFlowRecovered float64 `json:"control_flow_recovered"`
	// ConstantsExtracted is the number of original plain constants found in the protected binary.
	ConstantsExtracted int `json:"constants_extracted"`
	// ConstantsTotal is the total number of plain constants identified in the original module.
	ConstantsTotal int `json:"constants_total"`
	// InstructionInflation is the ratio of protected instructions to original instructions.
	InstructionInflation float64 `json:"instruction_inflation"`
	// EntropyIncrease is the difference in instruction distribution Shannon entropy (protected - original).
	EntropyIncrease float64 `json:"entropy_increase"`
	// InputOutputEquivalent indicates whether the protected binary produced identical behavior on the VM.
	InputOutputEquivalent bool `json:"input_output_equivalent"`
	// DifficultyScore categorizes the reverse-engineering difficulty ("Low", "Medium", "High", "Very High").
	DifficultyScore string `json:"difficulty_score"`
	// Recommendations contains actionable advice to improve hardening.
	Recommendations []string `json:"recommendations"`

	// StaticDetails holds fine-grained static analysis data.
	StaticDetails *StaticReport `json:"static_details,omitempty"`
	// DynamicDetails holds fine-grained VM test case execution data.
	DynamicDetails *DynamicReport `json:"dynamic_details,omitempty"`
}
