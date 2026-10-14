// Package compiler provides the Aegis Runtime Go source-to-IR bytecode compiler.
// It parses Go source code, validates compatibility against Aegis security constraints,
// lowers arithmetic, logical operations, control flow, and host interface calls into
// Aegis IR bytecode, and produces validated, runnable IR modules.
package compiler

import "github.com/oarkflow/ref/aegis/policy"

// Config defines the compilation options, source inputs, and security directives
// governing the generation of an Aegis IR module.
type Config struct {
	// PackagePath specifies the filesystem path to a Go package directory to compile.
	PackagePath string

	// SourceCode specifies direct Go source code (as a string) or the path to a .go file.
	SourceCode string

	// EntryPoints defines the function names to expose as module exports.
	// If omitted or empty, exported Go functions (or all functions) are exported by default.
	EntryPoints []string

	// Policy defines optional security tiers, per-function confidentiality levels,
	// and execution requirements to apply to the compiled module.
	Policy *policy.Policy

	// StripMetadata indicates whether to remove function names, local variable names,
	// and build metadata from the generated module.
	StripMetadata bool

	// Diversify indicates whether to randomize binary layout, local variable allocation,
	// and insert benign no-ops to resist binary analysis.
	Diversify bool

	// OptLevel defines the bytecode optimization level (0 = none, 1+ = basic optimizations).
	OptLevel int
}
