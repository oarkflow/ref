// Package transform provides program transformation and protection passes for Aegis IR modules.
package transform

import (
	"errors"
	"math/rand"
	"time"

	"github.com/oarkflow/ref/aegis/ir"
)

// OpaquePredicates implements a transformation pass that injects conditional branches
// whose outcomes are mathematically deterministic (always true or always false), while appearing
// non-trivial and dynamic to static analyzers and decompilers.
type OpaquePredicates struct {
	seed int64
}

// NewOpaquePredicates creates a new OpaquePredicates pass with an optional random seed.
func NewOpaquePredicates(seed ...int64) *OpaquePredicates {
	s := time.Now().UnixNano()
	if len(seed) > 0 && seed[0] != 0 {
		s = seed[0]
	}
	return &OpaquePredicates{seed: s}
}

// Name returns the identifier of this transformation pass.
func (o *OpaquePredicates) Name() string {
	return "OpaquePredicates"
}

// Transform injects opaque predicate branch blocks at function entry points.
// Mathematical tautologies like (x * (x + 1)) % 2 == 0 are evaluated at runtime,
// directing control flow through genuine code while leaving bogus/dead branches
// that mimic legitimate calculations to deceive disassemblers.
func (o *OpaquePredicates) Transform(m *ir.Module) (*ir.Module, Metrics, error) {
	if m == nil {
		return nil, Metrics{}, errors.New("transform: module is nil")
	}

	rng := rand.New(rand.NewSource(o.seed))
	metrics := Metrics{PassName: o.Name()}

	typeCount := len(m.Types)

	for funcIdx := range m.Functions {
		fn := &m.Functions[funcIdx]
		if len(fn.Body) == 0 {
			continue
		}

		var paramCount int
		if int(fn.TypeIndex) < typeCount {
			paramCount = len(m.Types[fn.TypeIndex].Params)
		}

		// Allocate a dedicated local variable for bogus branch computations
		dummyLocal := uint32(paramCount + len(fn.Locals))
		fn.Locals = append(fn.Locals, ir.I32)

		// Choose a variable for tautology evaluation (prefer an i32 parameter if available)
		var evalLocal uint32 = dummyLocal
		if paramCount > 0 && int(fn.TypeIndex) < typeCount && m.Types[fn.TypeIndex].Params[0] == ir.I32 {
			evalLocal = 0
		}

		tautologyVariant := rng.Intn(3)
		var condInsts []ir.Instruction

		switch tautologyVariant {
		case 0:
			// (x * (x + 1)) % 2 == 0 is always true for any integer x
			condInsts = []ir.Instruction{
				{Op: ir.OpLocalGet, Operands: [3]uint64{uint64(evalLocal), 0, 0}},
				{Op: ir.OpLocalGet, Operands: [3]uint64{uint64(evalLocal), 0, 0}},
				{Op: ir.OpI32Const, Operands: [3]uint64{1, 0, 0}},
				{Op: ir.OpI32Add},
				{Op: ir.OpI32Mul},
				{Op: ir.OpI32Const, Operands: [3]uint64{2, 0, 0}},
				{Op: ir.OpI32RemU},
				{Op: ir.OpI32Eqz}, // 1 when remainder is 0 (always true)
			}
		case 1:
			// (x ^ x) == 0 is always true
			condInsts = []ir.Instruction{
				{Op: ir.OpLocalGet, Operands: [3]uint64{uint64(evalLocal), 0, 0}},
				{Op: ir.OpLocalGet, Operands: [3]uint64{uint64(evalLocal), 0, 0}},
				{Op: ir.OpI32Xor},
				{Op: ir.OpI32Eqz}, // 1 when x ^ x == 0 (always true)
			}
		default:
			// ((x | 1) & 1) == 1 is always true
			condInsts = []ir.Instruction{
				{Op: ir.OpLocalGet, Operands: [3]uint64{uint64(evalLocal), 0, 0}},
				{Op: ir.OpI32Const, Operands: [3]uint64{1, 0, 0}},
				{Op: ir.OpI32Or},
				{Op: ir.OpI32Const, Operands: [3]uint64{1, 0, 0}},
				{Op: ir.OpI32And}, // yields 1 (always true)
			}
		}

		// Realistic junk instructions mimicking business pricing / hashing logic
		junkInsts := []ir.Instruction{
			{Op: ir.OpI32Const, Operands: [3]uint64{0x1337, 0, 0}},
			{Op: ir.OpLocalGet, Operands: [3]uint64{uint64(evalLocal), 0, 0}},
			{Op: ir.OpI32Mul},
			{Op: ir.OpI32Const, Operands: [3]uint64{0x7F, 0, 0}},
			{Op: ir.OpI32Xor},
			{Op: ir.OpLocalSet, Operands: [3]uint64{uint64(dummyLocal), 0, 0}},
		}

		alwaysTrue := rng.Intn(2) == 0

		var opaqueBlock []ir.Instruction
		if alwaysTrue {
			// Condition evaluates to 1 -> Then (taken) branch runs benign OpNop, Else (dead) branch is skipped
			opaqueBlock = append(opaqueBlock, condInsts...)
			opaqueBlock = append(opaqueBlock, ir.Instruction{Op: ir.OpIf})
			opaqueBlock = append(opaqueBlock, ir.Instruction{Op: ir.OpNop})
			opaqueBlock = append(opaqueBlock, ir.Instruction{Op: ir.OpElse})
			opaqueBlock = append(opaqueBlock, junkInsts...)
			opaqueBlock = append(opaqueBlock, ir.Instruction{Op: ir.OpEnd})
		} else {
			// Invert condition: condition evaluates to 0 -> Then (dead) branch is skipped, Else (taken) branch runs OpNop
			opaqueBlock = append(opaqueBlock, condInsts...)
			opaqueBlock = append(opaqueBlock, ir.Instruction{Op: ir.OpI32Eqz}) // invert to 0
			opaqueBlock = append(opaqueBlock, ir.Instruction{Op: ir.OpIf})
			opaqueBlock = append(opaqueBlock, junkInsts...)
			opaqueBlock = append(opaqueBlock, ir.Instruction{Op: ir.OpElse})
			opaqueBlock = append(opaqueBlock, ir.Instruction{Op: ir.OpNop})
			opaqueBlock = append(opaqueBlock, ir.Instruction{Op: ir.OpEnd})
		}

		// Prepend opaque predicate block to function body
		newBody := make([]ir.Instruction, 0, len(opaqueBlock)+len(fn.Body))
		newBody = append(newBody, opaqueBlock...)
		newBody = append(newBody, fn.Body...)

		fn.Body = newBody
		metrics.InstructionsAdded += len(opaqueBlock)
		metrics.FunctionsModified++
	}

	return m, metrics, nil
}
