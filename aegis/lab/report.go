package lab

import (
	"fmt"
	"strings"
	"sync"
)

var reportBuilderPool = sync.Pool{
	New: func() any {
		return new(strings.Builder)
	},
}

// Markdown formats the benchmark findings into an executive-ready, GitHub-flavored Markdown report.
func (r *RecoveryReport) Markdown() string {
	if r == nil {
		return "# Aegis Recovery Report\n\nNo report data available.\n"
	}

	sb := reportBuilderPool.Get().(*strings.Builder)
	sb.Reset()
	defer reportBuilderPool.Put(sb)

	benchName := r.BenchmarkName
	if benchName == "" {
		benchName = "Unnamed Benchmark"
	}

	fmt.Fprintf(sb, "# Aegis Reverse-Engineering Assessment: %s\n\n", benchName)

	// Status badge / alert
	if !r.InputOutputEquivalent {
		sb.WriteString("> [!CAUTION]\n> **Execution Divergence Detected:** The protected binary generated divergent output from the original baseline on one or more test cases.\n\n")
	} else if r.DifficultyScore == DifficultyHigh || r.DifficultyScore == DifficultyVeryHigh {
		fmt.Fprintf(sb, "> [!NOTE]\n> **Hardening Verified:** The protected binary achieved a **%s** reverse-engineering resistance score with zero semantic degradation.\n\n", r.DifficultyScore)
	}

	// 1. Executive Summary Table
	sb.WriteString("## Executive Summary\n\n")
	sb.WriteString("| Metric | Protected Result | Baseline Target | Assessment |\n")
	sb.WriteString("| :--- | :--- | :--- | :--- |\n")
	fmt.Fprintf(sb, "| **Difficulty Score** | `%s` | `Low` | Reverse-engineering resistance |\n", r.DifficultyScore)
	fmt.Fprintf(sb, "| **I/O Equivalence** | `%t` | `true` | VM behavioral parity |\n", r.InputOutputEquivalent)
	fmt.Fprintf(sb, "| **Constants Extracted** | `%d / %d` | `0` | Plaintext immediate leakage |\n", r.ConstantsExtracted, r.ConstantsTotal)
	fmt.Fprintf(sb, "| **Instruction Inflation** | `%.2fx` | `> 1.0x` | Binary size expansion ratio |\n", r.InstructionInflation)
	fmt.Fprintf(sb, "| **Entropy Increase** | `%+.4f bits` | `> 0.0 bits` | Opcode distribution dispersion |\n", r.EntropyIncrease)
	fmt.Fprintf(sb, "| **Boundaries Recovered** | `%.1f%%` | `< 50.0%%` | Named function recoverability |\n", r.FunctionBoundariesRecovered)
	fmt.Fprintf(sb, "| **Control Flow Recovered** | `%.1f%%` | `< 60.0%%` | CFG graph reconstruction similarity |\n\n", r.ControlFlowRecovered)

	// 2. Static Analysis Details
	if r.StaticDetails != nil {
		s := r.StaticDetails
		sb.WriteString("## Static Recovery Analysis\n\n")
		sb.WriteString("| Property | Original Baseline | Protected Binary | Delta |\n")
		sb.WriteString("| :--- | :--- | :--- | :--- |\n")
		fmt.Fprintf(sb, "| Instructions | %d | %d | %+.1f%% |\n",
			s.OriginalInstructions, s.ProtectedInstructions, (r.InstructionInflation-1.0)*100.0)
		fmt.Fprintf(sb, "| Named Functions | %d | %d | %d stripped |\n",
			s.OriginalNamedFunctions, s.ProtectedNamedFunctions, s.OriginalNamedFunctions-s.ProtectedNamedFunctions)
		fmt.Fprintf(sb, "| Basic Blocks | %d | %d | %+d blocks |\n",
			s.OriginalBasicBlocks, s.ProtectedBasicBlocks, s.ProtectedBasicBlocks-s.OriginalBasicBlocks)
		fmt.Fprintf(sb, "| CFG Edges | %d | %d | %+d edges |\n",
			s.OriginalEdges, s.ProtectedEdges, s.ProtectedEdges-s.OriginalEdges)
		fmt.Fprintf(sb, "| Shannon Entropy | %.4f bits | %.4f bits | %+.4f bits |\n\n",
			s.OriginalEntropy, s.ProtectedEntropy, s.EntropyIncrease)
	}

	// 3. Dynamic VM Evaluation Details
	if r.DynamicDetails != nil && len(r.DynamicDetails.TestCaseResults) > 0 {
		d := r.DynamicDetails
		sb.WriteString("## Dynamic VM Evaluation\n\n")
		fmt.Fprintf(sb, "**Total Trace Steps:** Original: `%d` | Protected: `%d` | Step Overhead: `%.2fx`\n\n",
			d.TotalOriginalSteps, d.TotalProtectedSteps, d.TraceOverhead)

		sb.WriteString("| Function | Args | Baseline Result | Protected Result | Parity |\n")
		sb.WriteString("| :--- | :--- | :--- | :--- | :--- |\n")
		for _, tc := range d.TestCaseResults {
			status := "PASS"
			if !tc.Equivalent {
				status = "FAIL"
			}
			origStr := formatUint64s(tc.OriginalResults)
			if tc.OriginalError != "" {
				origStr = "error: " + tc.OriginalError
			}
			protStr := formatUint64s(tc.ProtectedResults)
			if tc.ProtectedError != "" {
				protStr = "error: " + tc.ProtectedError
			}
			fmt.Fprintf(sb, "| `%s` | `%v` | `%s` | `%s` | **%s** |\n",
				tc.Function, tc.Args, origStr, protStr, status)
		}
		sb.WriteString("\n")
	}

	// 4. Security Engineering Recommendations
	sb.WriteString("## Security Engineering Recommendations\n\n")
	if len(r.Recommendations) == 0 {
		sb.WriteString("- No immediate hardening improvements required.\n")
	} else {
		for _, rec := range r.Recommendations {
			fmt.Fprintf(sb, "- %s\n", rec)
		}
	}
	sb.WriteString("\n")

	return sb.String()
}

// String returns a single-line or concise summary representation of the report.
func (r *RecoveryReport) String() string {
	if r == nil {
		return "RecoveryReport<nil>"
	}
	return fmt.Sprintf("RecoveryReport[%s]: Difficulty=%s, Parity=%t, Inflation=%.2fx, Constants=%d/%d, CFG=%.1f%%, Boundaries=%.1f%%",
		r.BenchmarkName, r.DifficultyScore, r.InputOutputEquivalent, r.InstructionInflation,
		r.ConstantsExtracted, r.ConstantsTotal, r.ControlFlowRecovered, r.FunctionBoundariesRecovered)
}

func formatUint64s(vals []uint64) string {
	if len(vals) == 0 {
		return "[]"
	}
	parts := make([]string, len(vals))
	for i, v := range vals {
		parts[i] = fmt.Sprintf("%d", v)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}
