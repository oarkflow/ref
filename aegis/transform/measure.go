// Package transform provides program transformation and protection passes for Aegis IR modules.
package transform

import (
	"fmt"
	"math"
	"strings"

	"github.com/oarkflow/ref/aegis/ir"
)

// FunctionMetrics summarizes complexity and information entropy metrics for a single function.
type FunctionMetrics struct {
	// Name is the symbolic name of the function.
	Name string
	// InstructionCount is the total number of bytecode instructions in the function body.
	InstructionCount int
	// CyclomaticComplexity is McCabe's cyclomatic complexity (1 + decision points).
	CyclomaticComplexity int
	// InstructionEntropy is the Shannon entropy of opcode distribution in bits.
	InstructionEntropy float64
}

// ModuleMetrics summarizes structural complexity and opcode entropy across an entire Aegis IR module.
type ModuleMetrics struct {
	// TotalFunctions is the number of internal functions in the module.
	TotalFunctions int
	// TotalInstructions is the aggregated instruction count across all functions.
	TotalInstructions int
	// CyclomaticComplexity is the sum of cyclomatic complexity across all functions.
	CyclomaticComplexity int
	// InstructionEntropy is the global Shannon entropy over all opcodes in the module.
	InstructionEntropy float64
	// Functions contains metrics broken down per function.
	Functions []FunctionMetrics
}

// String formats the module metrics into a human-readable summary string.
func (m ModuleMetrics) String() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "ModuleMetrics: funcs=%d instrs=%d complexity=%d entropy=%.3f bits",
		m.TotalFunctions, m.TotalInstructions, m.CyclomaticComplexity, m.InstructionEntropy)
	for _, fn := range m.Functions {
		fmt.Fprintf(&sb, "\n  - %s: instrs=%d, cc=%d, entropy=%.3f",
			fn.Name, fn.InstructionCount, fn.CyclomaticComplexity, fn.InstructionEntropy)
	}
	return sb.String()
}

// MeasureFunction computes cyclomatic complexity and instruction entropy for a single function.
func MeasureFunction(fn *ir.Function) FunctionMetrics {
	if fn == nil || len(fn.Body) == 0 {
		return FunctionMetrics{
			Name:                 funcName(fn),
			InstructionCount:     0,
			CyclomaticComplexity: 1,
			InstructionEntropy:   0.0,
		}
	}

	opcodeCounts := make(map[ir.Opcode]int)
	decisionPoints := 0

	for _, inst := range fn.Body {
		opcodeCounts[inst.Op]++
		switch inst.Op {
		case ir.OpIf, ir.OpBrIf, ir.OpBrTable, ir.OpLoop:
			decisionPoints++
		}
	}

	total := float64(len(fn.Body))
	entropy := 0.0
	for _, count := range opcodeCounts {
		if count > 0 {
			p := float64(count) / total
			entropy -= p * math.Log2(p)
		}
	}

	return FunctionMetrics{
		Name:                 funcName(fn),
		InstructionCount:     len(fn.Body),
		CyclomaticComplexity: 1 + decisionPoints,
		InstructionEntropy:   entropy,
	}
}

// Measure evaluates cyclomatic complexity and Shannon opcode entropy across all functions in the module.
func Measure(m *ir.Module) ModuleMetrics {
	if m == nil || len(m.Functions) == 0 {
		return ModuleMetrics{
			TotalFunctions:       0,
			TotalInstructions:    0,
			CyclomaticComplexity: 0,
			InstructionEntropy:   0.0,
			Functions:            nil,
		}
	}

	globalCounts := make(map[ir.Opcode]int)
	totalInstructions := 0
	totalComplexity := 0
	funcMetrics := make([]FunctionMetrics, len(m.Functions))

	for i := range m.Functions {
		fm := MeasureFunction(&m.Functions[i])
		funcMetrics[i] = fm
		totalComplexity += fm.CyclomaticComplexity
		totalInstructions += fm.InstructionCount

		for _, inst := range m.Functions[i].Body {
			globalCounts[inst.Op]++
		}
	}

	globalEntropy := 0.0
	if totalInstructions > 0 {
		total := float64(totalInstructions)
		for _, count := range globalCounts {
			if count > 0 {
				p := float64(count) / total
				globalEntropy -= p * math.Log2(p)
			}
		}
	}

	return ModuleMetrics{
		TotalFunctions:       len(m.Functions),
		TotalInstructions:    totalInstructions,
		CyclomaticComplexity: totalComplexity,
		InstructionEntropy:   globalEntropy,
		Functions:            funcMetrics,
	}
}

func funcName(fn *ir.Function) string {
	if fn == nil || fn.Name == "" {
		return "<anonymous>"
	}
	return fn.Name
}
