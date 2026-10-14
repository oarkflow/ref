package compiler

import (
	"fmt"

	"github.com/oarkflow/ref/aegis/ir"
)

// Report aggregates compilation statistics, metrics, and security attributes
// for a successfully generated Aegis IR module.
type Report struct {
	// FunctionsCompiled is the total count of functions compiled into the module.
	FunctionsCompiled int

	// FunctionsProtected is the count of functions assigned elevated confidentiality,
	// isolation, or diversification policies.
	FunctionsProtected int

	// InstructionCount is the total number of bytecode instructions emitted.
	InstructionCount int

	// DataSegmentBytes is the total size in bytes of all static linear data segments.
	DataSegmentBytes int

	// ImportsUsed lists all external host or environment function imports referenced.
	ImportsUsed []string

	// ExportsCreated lists all exported function entry points exposed by the module.
	ExportsCreated []string

	// BuildID is the hex-encoded 32-byte cryptographic identifier for this build.
	BuildID string
}

// String returns a human-readable summary of the compilation report.
func (r Report) String() string {
	return fmt.Sprintf("Report(compiled=%d, protected=%d, instructions=%d, dataBytes=%d, imports=%d, exports=%d, buildID=%s)",
		r.FunctionsCompiled, r.FunctionsProtected, r.InstructionCount, r.DataSegmentBytes, len(r.ImportsUsed), len(r.ExportsCreated), r.BuildID)
}

// CompileResult represents the output of a successful compilation run, containing
// the generated IR module, build metrics report, and non-fatal compilation warnings.
type CompileResult struct {
	// Module is the fully validated Aegis IR module ready for packaging or execution.
	Module *ir.Module

	// Report contains compilation statistics and metadata.
	Report Report

	// Warnings contains diagnostic messages for non-fatal issues encountered during compilation.
	Warnings []string
}
