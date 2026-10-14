// Package transform provides program transformation and protection passes for Aegis IR modules.
package transform

import (
	"errors"
	"math/rand"
	"time"

	"github.com/oarkflow/ref/aegis/ir"
)

// InstructionSchedule implements an instruction scheduling and benign dead code insertion
// pass that interleaves harmless OpNop and non-interfering stack operations throughout
// function bytecode bodies.
type InstructionSchedule struct {
	seed int64
}

// NewInstructionSchedule constructs a new InstructionSchedule pass with an optional random seed.
func NewInstructionSchedule(seed ...int64) *InstructionSchedule {
	s := time.Now().UnixNano()
	if len(seed) > 0 && seed[0] != 0 {
		s = seed[0]
	}
	return &InstructionSchedule{seed: s}
}

// Name returns the identifier of this transformation pass.
func (s *InstructionSchedule) Name() string {
	return "InstructionSchedule"
}

// Transform scans function instructions and injects benign OpNop instructions
// and non-interfering local variable refreshes (local.get L -> local.set L)
// to disrupt pattern matching and signature detection.
func (s *InstructionSchedule) Transform(m *ir.Module) (*ir.Module, Metrics, error) {
	if m == nil {
		return nil, Metrics{}, errors.New("transform: module is nil")
	}

	rng := rand.New(rand.NewSource(s.seed))
	metrics := Metrics{PassName: s.Name()}

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
		totalLocals := paramCount + len(fn.Locals)

		newBody := make([]ir.Instruction, 0, len(fn.Body)+len(fn.Body)/2)
		inserted := 0

		// Insert a benign OpNop at function entry
		newBody = append(newBody, ir.Instruction{Op: ir.OpNop})
		inserted++

		for _, inst := range fn.Body {
			newBody = append(newBody, inst)

			// After instructions that leave the stack balanced (like local.set or nop),
			// periodically insert harmless operations with 30% probability
			if inst.Op == ir.OpLocalSet || inst.Op == ir.OpNop {
				if rng.Float64() < 0.35 {
					if totalLocals > 0 && rng.Intn(2) == 0 {
						// Non-interfering self-refresh: local.get L -> local.set L
						chosenLocal := uint64(rng.Intn(totalLocals))
						newBody = append(newBody,
							ir.Instruction{Op: ir.OpLocalGet, Operands: [3]uint64{chosenLocal, 0, 0}},
							ir.Instruction{Op: ir.OpLocalSet, Operands: [3]uint64{chosenLocal, 0, 0}},
						)
						inserted += 2
					} else {
						// Benign OpNop
						newBody = append(newBody, ir.Instruction{Op: ir.OpNop})
						inserted++
					}
				}
			}
		}

		fn.Body = newBody
		metrics.InstructionsAdded += inserted
		metrics.FunctionsModified++
	}

	return m, metrics, nil
}
