package lab

import (
	"encoding/binary"
	"fmt"
	"math"

	"github.com/oarkflow/ref/aegis/ir"
)

// StaticReport contains detailed findings from static reverse-engineering analysis.
type StaticReport struct {
	OriginalInstructions       int       `json:"original_instructions"`
	ProtectedInstructions      int       `json:"protected_instructions"`
	InstructionInflation       float64   `json:"instruction_inflation"`
	ConstantsTotal             int       `json:"constants_total"`
	ConstantsExtracted         int       `json:"constants_extracted"`
	OriginalEntropy            float64   `json:"original_entropy"`
	ProtectedEntropy           float64   `json:"protected_entropy"`
	EntropyIncrease            float64   `json:"entropy_increase"`
	OriginalNamedFunctions     int       `json:"original_named_functions"`
	ProtectedNamedFunctions    int       `json:"protected_named_functions"`
	FunctionBoundariesRecovered float64  `json:"function_boundaries_recovered"`
	OriginalBasicBlocks        int       `json:"original_basic_blocks"`
	ProtectedBasicBlocks       int       `json:"protected_basic_blocks"`
	OriginalEdges              int       `json:"original_edges"`
	ProtectedEdges             int       `json:"protected_edges"`
	ControlFlowRecovered       float64   `json:"control_flow_recovered"`
}

// BasicBlock represents a straight-line sequence of instructions with single entry and exit.
type BasicBlock struct {
	StartPC    int
	EndPC      int
	Successors []int
}

// AnalyzeStatic executes static reverse-engineering analysis comparing original and protected modules.
func AnalyzeStatic(orig, prot *ir.Module) (*StaticReport, error) {
	if orig == nil {
		return nil, fmt.Errorf("lab: original module cannot be nil")
	}
	if prot == nil {
		return nil, fmt.Errorf("lab: protected module cannot be nil")
	}

	// 1. Instruction count and inflation
	origInstCount := countInstructions(orig)
	protInstCount := countInstructions(prot)
	var inflation float64 = 1.0
	if origInstCount > 0 {
		inflation = float64(protInstCount) / float64(origInstCount)
	}

	// 2. Plaintext constant extraction
	origConstants := extractPlainConstants(orig)
	protConstants := extractPlainConstants(prot)

	constantsTotal := len(origConstants)
	constantsExtracted := countExtractedConstants(origConstants, protConstants)

	// 3. Shannon entropy of opcode distribution
	origEntropy := computeOpcodeEntropy(orig)
	protEntropy := computeOpcodeEntropy(prot)
	entropyIncrease := protEntropy - origEntropy

	// 4. Function boundaries recovery
	origNamed, protNamed, boundariesRecovered := analyzeFunctionBoundaries(orig, prot)

	// 5. Control-flow graph analysis
	origBlocks, origEdges := analyzeCFG(orig)
	protBlocks, protEdges := analyzeCFG(prot)
	cfgRecovered := computeCFGSimilarity(origBlocks, origEdges, protBlocks, protEdges)

	return &StaticReport{
		OriginalInstructions:       origInstCount,
		ProtectedInstructions:      protInstCount,
		InstructionInflation:       roundFloat(inflation, 4),
		ConstantsTotal:             constantsTotal,
		ConstantsExtracted:         constantsExtracted,
		OriginalEntropy:            roundFloat(origEntropy, 4),
		ProtectedEntropy:           roundFloat(protEntropy, 4),
		EntropyIncrease:            roundFloat(entropyIncrease, 4),
		OriginalNamedFunctions:     origNamed,
		ProtectedNamedFunctions:    protNamed,
		FunctionBoundariesRecovered: roundFloat(boundariesRecovered, 2),
		OriginalBasicBlocks:        origBlocks,
		ProtectedBasicBlocks:       protBlocks,
		OriginalEdges:              origEdges,
		ProtectedEdges:             protEdges,
		ControlFlowRecovered:       roundFloat(cfgRecovered, 2),
	}, nil
}

func countInstructions(m *ir.Module) int {
	total := 0
	for _, fn := range m.Functions {
		total += len(fn.Body)
	}
	return total
}

func extractPlainConstants(m *ir.Module) []uint64 {
	var constants []uint64

	// From instruction immediates
	for _, fn := range m.Functions {
		for _, inst := range fn.Body {
			switch inst.Op {
			case ir.OpI32Const:
				constants = append(constants, uint64(uint32(inst.Operands[0])))
			case ir.OpI64Const, ir.OpF32Const, ir.OpF64Const:
				constants = append(constants, inst.Operands[0])
			}
		}
	}

	// From static data segments
	for _, seg := range m.Data {
		for i := 0; i+8 <= len(seg.Data); i += 8 {
			val := binary.LittleEndian.Uint64(seg.Data[i : i+8])
			constants = append(constants, val)
		}
	}

	return constants
}

func countExtractedConstants(orig, prot []uint64) int {
	if len(orig) == 0 {
		return 0
	}

	// Build frequency multiset of protected constants
	protCounts := make(map[uint64]int, len(prot))
	for _, c := range prot {
		protCounts[c]++
	}

	extracted := 0
	for _, c := range orig {
		if count, found := protCounts[c]; found && count > 0 {
			extracted++
			protCounts[c]-- // consume one matching constant
		}
	}

	return extracted
}

func computeOpcodeEntropy(m *ir.Module) float64 {
	counts := make(map[ir.Opcode]int)
	total := 0

	for _, fn := range m.Functions {
		for _, inst := range fn.Body {
			counts[inst.Op]++
			total++
		}
	}

	if total == 0 {
		return 0.0
	}

	var entropy float64
	for _, count := range counts {
		if count > 0 {
			prob := float64(count) / float64(total)
			entropy -= prob * math.Log2(prob)
		}
	}

	return entropy
}

func analyzeFunctionBoundaries(orig, prot *ir.Module) (origNamed, protNamed int, recovered float64) {
	origNames := make(map[string]bool)
	for _, fn := range orig.Functions {
		if fn.Name != "" {
			origNames[fn.Name] = true
			origNamed++
		}
	}

	matches := 0
	for _, fn := range prot.Functions {
		if fn.Name != "" {
			protNamed++
			if origNames[fn.Name] {
				matches++
			}
		}
	}

	if origNamed == 0 {
		return 0, protNamed, 100.0
	}

	recovered = (float64(matches) / float64(origNamed)) * 100.0
	return origNamed, protNamed, recovered
}

func analyzeCFG(m *ir.Module) (totalBlocks int, totalEdges int) {
	for funcIdx := range m.Functions {
		fn := &m.Functions[funcIdx]
		blocks, edges := buildFunctionCFG(fn)
		totalBlocks += blocks
		totalEdges += edges
	}
	return totalBlocks, totalEdges
}

func buildFunctionCFG(fn *ir.Function) (numBlocks int, numEdges int) {
	if len(fn.Body) == 0 {
		return 0, 0
	}

	// 1. Identify basic block leaders
	isLeader := make([]bool, len(fn.Body))
	isLeader[0] = true // entry instruction

	for pc, inst := range fn.Body {
		switch inst.Op {
		case ir.OpBr, ir.OpBrIf, ir.OpBrTable, ir.OpReturn, ir.OpTrap:
			if pc+1 < len(fn.Body) {
				isLeader[pc+1] = true
			}
		case ir.OpBlock, ir.OpLoop, ir.OpIf, ir.OpElse, ir.OpEnd:
			isLeader[pc] = true
			if pc+1 < len(fn.Body) {
				isLeader[pc+1] = true
			}
		}
	}

	// 2. Construct basic blocks
	type blockRange struct {
		start int
		end   int
	}
	var blocks []blockRange
	start := 0
	for pc := 1; pc < len(fn.Body); pc++ {
		if isLeader[pc] {
			blocks = append(blocks, blockRange{start: start, end: pc - 1})
			start = pc
		}
	}
	blocks = append(blocks, blockRange{start: start, end: len(fn.Body) - 1})

	// 3. Count edges between blocks
	edgesCount := 0
	for i, b := range blocks {
		lastInst := fn.Body[b.end]
		switch lastInst.Op {
		case ir.OpReturn, ir.OpTrap:
			// No outgoing edges within function
		case ir.OpBr:
			// Single unconditional branch edge
			edgesCount++
		case ir.OpBrIf:
			// Conditional branch: target + fallthrough
			edgesCount += 2
		case ir.OpIf:
			// Branch: then + else/end
			edgesCount += 2
		default:
			// Fallthrough to next block if not the last block
			if i+1 < len(blocks) {
				edgesCount++
			}
		}
	}

	return len(blocks), edgesCount
}

func computeCFGSimilarity(origBlocks, origEdges, protBlocks, protEdges int) float64 {
	if origBlocks == 0 && protBlocks == 0 {
		return 100.0
	}
	if origBlocks == 0 || protBlocks == 0 {
		return 0.0
	}

	// Block count ratio
	minBlocks := math.Min(float64(origBlocks), float64(protBlocks))
	maxBlocks := math.Max(float64(origBlocks), float64(protBlocks))
	blockRatio := minBlocks / maxBlocks

	// Edge count ratio
	var edgeRatio float64 = 1.0
	if origEdges > 0 || protEdges > 0 {
		minEdges := math.Min(float64(origEdges), float64(protEdges))
		maxEdges := math.Max(float64(origEdges), float64(protEdges))
		if maxEdges > 0 {
			edgeRatio = minEdges / maxEdges
		}
	}

	// Cyclomatic complexity comparison
	ccOrig := float64(origEdges - origBlocks + 2)
	if ccOrig < 1 {
		ccOrig = 1
	}
	ccProt := float64(protEdges - protBlocks + 2)
	if ccProt < 1 {
		ccProt = 1
	}
	ccRatio := math.Min(ccOrig, ccProt) / math.Max(ccOrig, ccProt)

	similarity := (blockRatio + edgeRatio + ccRatio) / 3.0
	return similarity * 100.0
}

func roundFloat(val float64, precision int) float64 {
	ratio := math.Pow(10, float64(precision))
	return math.Round(val*ratio) / ratio
}
