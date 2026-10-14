package compiler

import "github.com/oarkflow/ref/aegis/ir"

// Strip removes symbolic debug information, internal function names, and build metadata
// from the provided Aegis IR module.
//
// Module exports and function bodies are preserved to allow callers to invoke exported
// functions without leaking internal symbol names.
func Strip(m *ir.Module) {
	if m == nil {
		return
	}

	// Clear symbolic function names from internal functions
	for i := range m.Functions {
		m.Functions[i].Name = ""
	}

	// Reset build metadata
	m.Metadata = ir.Metadata{}
}
