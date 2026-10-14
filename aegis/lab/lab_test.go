package lab

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/oarkflow/ref/aegis/ir"
	"github.com/oarkflow/ref/aegis/transform"
)

func createTestModules(t *testing.T) (*ir.Module, *ir.Module) {
	t.Helper()

	// Build an original IR module with multiple functions, constants, and control flow
	orig := &ir.Module{
		Version: ir.Version,
		Types: []ir.FuncType{
			{
				Params:  []ir.ValType{ir.I64, ir.I64},
				Results: []ir.ValType{ir.I64},
			},
			{
				Params:  []ir.ValType{ir.I64},
				Results: []ir.ValType{ir.I64},
			},
		},
		Functions: []ir.Function{
			{
				Name:      "calc",
				TypeIndex: 0,
				Body: []ir.Instruction{
					// (arg0 + arg1) * 7 + 100
					{Op: ir.OpLocalGet, Operands: [3]uint64{0, 0, 0}},
					{Op: ir.OpLocalGet, Operands: [3]uint64{1, 0, 0}},
					{Op: ir.OpI64Add},
					{Op: ir.OpI64Const, Operands: [3]uint64{7, 0, 0}},
					{Op: ir.OpI64Mul},
					{Op: ir.OpI64Const, Operands: [3]uint64{100, 0, 0}},
					{Op: ir.OpI64Add},
					{Op: ir.OpReturn},
				},
			},
			{
				Name:      "branchLogic",
				TypeIndex: 1,
				Body: []ir.Instruction{
					// if arg0 > 10 return 42 else return 99
					{Op: ir.OpLocalGet, Operands: [3]uint64{0, 0, 0}},
					{Op: ir.OpI64Const, Operands: [3]uint64{10, 0, 0}},
					{Op: ir.OpI64GtS},
					{Op: ir.OpIf},
					{Op: ir.OpI64Const, Operands: [3]uint64{42, 0, 0}},
					{Op: ir.OpReturn},
					{Op: ir.OpElse},
					{Op: ir.OpI64Const, Operands: [3]uint64{99, 0, 0}},
					{Op: ir.OpReturn},
					{Op: ir.OpEnd},
					{Op: ir.OpI64Const, Operands: [3]uint64{0, 0, 0}},
					{Op: ir.OpReturn},
				},
			},
			{
				Name:      "internalSecret",
				TypeIndex: 1,
				Body: []ir.Instruction{
					{Op: ir.OpI64Const, Operands: [3]uint64{1337, 0, 0}},
					{Op: ir.OpReturn},
				},
			},
		},
		Exports: []ir.Export{
			{Name: "calc", FuncIndex: 0},
			{Name: "branchLogic", FuncIndex: 1},
		},
	}

	pipeline := transform.StandardPipeline()
	protected, _, err := pipeline.Apply(orig)
	if err != nil {
		t.Fatalf("failed to apply StandardPipeline: %v", err)
	}

	return orig, protected
}

func TestRunBenchmarkProtected(t *testing.T) {
	orig, protected := createTestModules(t)

	benchmark := &RecoveryBenchmark{
		Name:      "ComputeAndBranchSuite",
		Original:  orig,
		Protected: protected,
		TestCases: []TestCase{
			{
				Function: "calc",
				Args:     []uint64{3, 4},
				Expected: []uint64{(3 + 4)*7 + 100}, // 149
			},
			{
				Function: "calc",
				Args:     []uint64{10, 20},
				Expected: []uint64{(10 + 20)*7 + 100}, // 310
			},
			{
				Function: "branchLogic",
				Args:     []uint64{15},
				Expected: []uint64{42},
			},
			{
				Function: "branchLogic",
				Args:     []uint64{5},
				Expected: []uint64{99},
			},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	report, err := RunBenchmark(ctx, benchmark)
	if err != nil {
		t.Fatalf("RunBenchmark failed: %v", err)
	}

	// 1. Verify ConstantsExtracted decreased
	if report.ConstantsTotal <= 0 {
		t.Errorf("expected ConstantsTotal > 0, got %d", report.ConstantsTotal)
	}
	if report.ConstantsExtracted >= report.ConstantsTotal {
		t.Errorf("expected ConstantsExtracted (%d) < ConstantsTotal (%d)",
			report.ConstantsExtracted, report.ConstantsTotal)
	}

	// 2. Verify InstructionInflation > 1.0
	if report.InstructionInflation <= 1.0 {
		t.Errorf("expected InstructionInflation > 1.0, got %.4f", report.InstructionInflation)
	}

	// 3. Verify I/O Equivalence is true
	if !report.InputOutputEquivalent {
		t.Errorf("expected InputOutputEquivalent to be true, got false")
	}

	// 4. Verify DifficultyScore is calculated ("High" or "Very High")
	if report.DifficultyScore != DifficultyHigh && report.DifficultyScore != DifficultyVeryHigh {
		t.Errorf("expected High or Very High difficulty score, got %q", report.DifficultyScore)
	}

	// 5. Verify CFG recovery metric decreased due to control flow flattening
	if report.ControlFlowRecovered >= 100.0 {
		t.Errorf("expected ControlFlowRecovered < 100.0, got %.1f", report.ControlFlowRecovered)
	}

	// 6. Verify Recommendations is non-empty
	if len(report.Recommendations) == 0 {
		t.Errorf("expected recommendations, got empty slice")
	}

	// 7. Verify Markdown and String generation
	md := report.Markdown()
	if !strings.Contains(md, "# Aegis Reverse-Engineering Assessment: ComputeAndBranchSuite") {
		t.Errorf("markdown missing title, got:\n%s", md)
	}
	if !strings.Contains(md, "Difficulty Score") || !strings.Contains(md, "Executive Summary") {
		t.Errorf("markdown missing key sections, got:\n%s", md)
	}

	str := report.String()
	if !strings.Contains(str, "ComputeAndBranchSuite") || !strings.Contains(str, report.DifficultyScore) {
		t.Errorf("string report invalid: %q", str)
	}
}

func TestRunBenchmarkUnprotected(t *testing.T) {
	orig, _ := createTestModules(t)

	benchmark := &RecoveryBenchmark{
		Name:      "UnprotectedComparison",
		Original:  orig,
		Protected: orig, // Compare against self
		TestCases: []TestCase{
			{
				Function: "calc",
				Args:     []uint64{2, 3},
				Expected: []uint64{(2 + 3)*7 + 100},
			},
		},
	}

	report, err := RunBenchmark(context.Background(), benchmark)
	if err != nil {
		t.Fatalf("RunBenchmark failed: %v", err)
	}

	if report.DifficultyScore != DifficultyLow {
		t.Errorf("expected DifficultyScore Low for unmodified module, got %q", report.DifficultyScore)
	}
	if report.InstructionInflation != 1.0 {
		t.Errorf("expected inflation 1.0, got %.2f", report.InstructionInflation)
	}
	if report.ConstantsExtracted != report.ConstantsTotal {
		t.Errorf("expected all constants extracted (%d), got %d", report.ConstantsTotal, report.ConstantsExtracted)
	}
	if report.FunctionBoundariesRecovered != 100.0 {
		t.Errorf("expected 100%% boundaries recovered, got %.1f", report.FunctionBoundariesRecovered)
	}
	if report.ControlFlowRecovered != 100.0 {
		t.Errorf("expected 100%% CFG recovered, got %.1f", report.ControlFlowRecovered)
	}
	if !report.InputOutputEquivalent {
		t.Errorf("expected InputOutputEquivalent to be true")
	}
}

func TestRunBenchmarkDivergence(t *testing.T) {
	orig, _ := createTestModules(t)

	// Create divergent protected module that returns incorrect value
	divergent := transform.CloneModule(orig)
	divergent.Functions[0].Body = []ir.Instruction{
		{Op: ir.OpI64Const, Operands: [3]uint64{999999, 0, 0}},
		{Op: ir.OpReturn},
	}

	benchmark := &RecoveryBenchmark{
		Name:      "DivergentModule",
		Original:  orig,
		Protected: divergent,
		TestCases: []TestCase{
			{
				Function: "calc",
				Args:     []uint64{1, 2},
				Expected: []uint64{(1 + 2)*7 + 100},
			},
		},
	}

	report, err := RunBenchmark(context.Background(), benchmark)
	if err != nil {
		t.Fatalf("RunBenchmark failed: %v", err)
	}

	if report.InputOutputEquivalent {
		t.Errorf("expected InputOutputEquivalent to be false on divergent module")
	}

	md := report.Markdown()
	if !strings.Contains(md, "CAUTION") && !strings.Contains(md, "Divergence") {
		t.Errorf("expected caution alert in markdown for divergence, got:\n%s", md)
	}
}

func TestRunBenchmarkValidationErrors(t *testing.T) {
	_, err := RunBenchmark(context.Background(), nil)
	if err == nil {
		t.Errorf("expected error for nil benchmark")
	}

	_, err = RunBenchmark(context.Background(), &RecoveryBenchmark{})
	if err == nil {
		t.Errorf("expected error for missing modules")
	}

	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	orig, prot := createTestModules(t)
	_, err = RunBenchmark(cancelledCtx, &RecoveryBenchmark{
		Original:  orig,
		Protected: prot,
	})
	if err == nil {
		t.Errorf("expected error for cancelled context")
	}
}

func TestRunBenchmarkStripped(t *testing.T) {
	orig, protected := createTestModules(t)

	// Strip function names from protected module
	for i := range protected.Functions {
		protected.Functions[i].Name = ""
	}

	benchmark := &RecoveryBenchmark{
		Name:      "StrippedModule",
		Original:  orig,
		Protected: protected,
		TestCases: []TestCase{
			{
				Function: "calc",
				Args:     []uint64{3, 4},
				Expected: []uint64{(3 + 4)*7 + 100},
			},
		},
	}

	report, err := RunBenchmark(context.Background(), benchmark)
	if err != nil {
		t.Fatalf("RunBenchmark failed: %v", err)
	}

	if report.FunctionBoundariesRecovered != 0.0 {
		t.Errorf("expected 0.0 boundaries recovered for stripped module, got %.1f", report.FunctionBoundariesRecovered)
	}
	if report.DifficultyScore != DifficultyVeryHigh && report.DifficultyScore != DifficultyHigh {
		t.Errorf("expected Very High or High difficulty score for stripped module, got %q", report.DifficultyScore)
	}
}

