package compiler

import (
	"crypto/rand"
	"fmt"

	"github.com/oarkflow/ref/aegis/ir"
)

// GenerateBuildID populates the module's 32-byte BuildID with cryptographically
// random bytes if it is currently unset (all zeros).
func GenerateBuildID(m *ir.Module) error {
	if m == nil {
		return nil
	}
	var zeroID [32]byte
	if m.BuildID == zeroID {
		if _, err := rand.Read(m.BuildID[:]); err != nil {
			return fmt.Errorf("compiler: failed to generate build ID: %w", err)
		}
	}
	return nil
}

// Diversify applies binary layout randomization and diversification transforms
// to the IR module. It generates a fresh random BuildID, inserts benign no-ops,
// expands frame local allocations, and marks function attributes with diversification flags.
func Diversify(m *ir.Module) error {
	if m == nil {
		return nil
	}

	// Always assign a fresh random 32-byte BuildID for diversified builds
	if _, err := rand.Read(m.BuildID[:]); err != nil {
		return fmt.Errorf("compiler: failed to generate build ID: %w", err)
	}

	for i := range m.Functions {
		fn := &m.Functions[i]
		fn.Attributes.Diversify = true

		// Add a benign local variable to diversify frame local memory layout
		fn.Locals = append(fn.Locals, ir.I32)

		// Insert a benign OpNop instruction at function entry
		if len(fn.Body) > 0 {
			diversifiedBody := make([]ir.Instruction, 0, len(fn.Body)+1)
			diversifiedBody = append(diversifiedBody, ir.Instruction{Op: ir.OpNop})
			diversifiedBody = append(diversifiedBody, fn.Body...)
			fn.Body = diversifiedBody
		}
	}

	return nil
}
