package lab

import (
	"context"
	"fmt"
)

// RunBenchmark executes a full comparative reverse-engineering resistance evaluation
// between an unprotected baseline IR module and a hardened target module.
func RunBenchmark(ctx context.Context, b *RecoveryBenchmark) (*RecoveryReport, error) {
	if b == nil {
		return nil, fmt.Errorf("lab: benchmark cannot be nil")
	}
	if b.Original == nil {
		return nil, fmt.Errorf("lab: original module cannot be nil")
	}
	if b.Protected == nil {
		return nil, fmt.Errorf("lab: protected module cannot be nil")
	}

	// Check cancellation before static analysis
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("lab: benchmark aborted: %w", err)
	}

	// 1. Static recovery analysis
	staticRep, err := AnalyzeStatic(b.Original, b.Protected)
	if err != nil {
		return nil, fmt.Errorf("lab: static analysis failed: %w", err)
	}

	// Check cancellation before dynamic evaluation
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("lab: benchmark aborted: %w", err)
	}

	// 2. Dynamic VM evaluation
	dynamicRep, err := AnalyzeDynamic(ctx, b.Original, b.Protected, b.TestCases)
	if err != nil {
		return nil, fmt.Errorf("lab: dynamic analysis failed: %w", err)
	}

	// 3. Compute reverse-engineering difficulty score
	difficulty := calculateDifficultyScore(staticRep, dynamicRep)

	// 4. Generate security recommendations
	recommendations := generateRecommendations(staticRep, dynamicRep)

	report := &RecoveryReport{
		BenchmarkName:               b.Name,
		FunctionBoundariesRecovered: staticRep.FunctionBoundariesRecovered,
		ControlFlowRecovered:        staticRep.ControlFlowRecovered,
		ConstantsExtracted:          staticRep.ConstantsExtracted,
		ConstantsTotal:              staticRep.ConstantsTotal,
		InstructionInflation:        staticRep.InstructionInflation,
		EntropyIncrease:             staticRep.EntropyIncrease,
		InputOutputEquivalent:       dynamicRep.InputOutputEquivalent,
		DifficultyScore:             difficulty,
		Recommendations:             recommendations,
		StaticDetails:               staticRep,
		DynamicDetails:              dynamicRep,
	}

	return report, nil
}

func calculateDifficultyScore(s *StaticReport, d *DynamicReport) string {
	var totalScore float64

	// 1. Function boundary resistance (0 to 20 pts)
	if s.OriginalNamedFunctions > 0 {
		boundaryResistance := 1.0 - (s.FunctionBoundariesRecovered / 100.0)
		if boundaryResistance < 0 {
			boundaryResistance = 0
		}
		totalScore += boundaryResistance * 20.0
	} else {
		totalScore += 20.0
	}

	// 2. CFG reconstruction resistance (0 to 25 pts)
	cfgResistance := 1.0 - (s.ControlFlowRecovered / 100.0)
	if cfgResistance < 0 {
		cfgResistance = 0
	}
	totalScore += cfgResistance * 25.0

	// 3. Constant hiding resistance (0 to 25 pts)
	if s.ConstantsTotal > 0 {
		constantResistance := 1.0 - (float64(s.ConstantsExtracted) / float64(s.ConstantsTotal))
		if constantResistance < 0 {
			constantResistance = 0
		}
		totalScore += constantResistance * 25.0
	} else {
		totalScore += 25.0
	}

	// 4. Instruction inflation resistance (0 to 15 pts)
	if s.InstructionInflation > 1.0 {
		infScore := s.InstructionInflation - 1.0
		if infScore > 1.0 {
			infScore = 1.0
		}
		totalScore += infScore * 15.0
	}

	// 5. Entropy increase resistance (0 to 15 pts)
	if s.EntropyIncrease > 0 {
		entScore := s.EntropyIncrease
		if entScore > 1.0 {
			entScore = 1.0
		}
		totalScore += entScore * 15.0
	}

	switch {
	case totalScore >= 70.0:
		return DifficultyVeryHigh
	case totalScore >= 45.0:
		return DifficultyHigh
	case totalScore >= 20.0:
		return DifficultyMedium
	default:
		return DifficultyLow
	}
}

func generateRecommendations(s *StaticReport, d *DynamicReport) []string {
	var recs []string

	if !d.InputOutputEquivalent {
		recs = append(recs, "CRITICAL: Protected module produced divergent outputs from original module on test cases; verify transformation correctness.")
	}

	if s.FunctionBoundariesRecovered > 0 {
		recs = append(recs, "Strip symbolic function names from internal functions to eliminate discernible function boundaries.")
	}

	if s.ConstantsExtracted > 0 {
		recs = append(recs, "Apply constant splitting or expression masking to conceal plaintext constants from static disassemblers.")
	}

	if s.ControlFlowRecovered > 60.0 {
		recs = append(recs, "Introduce control-flow flattening, opaque predicates, or bogus branch blocks to disrupt CFG reconstruction.")
	}

	if s.InstructionInflation < 1.3 {
		recs = append(recs, "Increase instruction padding and substitution passes to raise reverse-engineering complexity.")
	}

	if s.EntropyIncrease <= 0 {
		recs = append(recs, "Diversify instruction selection and opcode variety to increase binary distribution entropy.")
	}

	if len(recs) == 0 {
		recs = append(recs, "Binary hardening satisfies target anti-reverse-engineering thresholds; maintain periodic build diversification.")
	}

	return recs
}
