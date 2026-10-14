// Package ir defines the Aegis Intermediate Representation format, providing
// the core types, bytecode instructions, serialization, validation, and text
// representations for the Aegis protected execution runtime.
package ir

import "fmt"

// ValType represents the Aegis IR value types.
type ValType uint8

const (
	// TypeInvalid represents an uninitialized or invalid value type.
	TypeInvalid ValType = 0
	// I32 represents a 32-bit integer value type.
	I32 ValType = 1
	// I64 represents a 64-bit integer value type.
	I64 ValType = 2
	// F32 represents a 32-bit IEEE 754 floating-point value type.
	F32 ValType = 3
	// F64 represents a 64-bit IEEE 754 floating-point value type.
	F64 ValType = 4
	// Bytes represents an opaque byte sequence value type.
	Bytes ValType = 5
)

// String returns the human-readable representation of the value type.
func (t ValType) String() string {
	switch t {
	case I32:
		return "i32"
	case I64:
		return "i64"
	case F32:
		return "f32"
	case F64:
		return "f64"
	case Bytes:
		return "bytes"
	default:
		return fmt.Sprintf("valtype(%d)", uint8(t))
	}
}

// ConfLevel represents the confidentiality level assigned to a function,
// controlling runtime isolation, verification constraints, and data protections.
type ConfLevel uint8

const (
	// ConfLow indicates low confidentiality with minimal isolation requirements.
	ConfLow ConfLevel = 0
	// ConfStandard indicates default confidentiality for general business logic.
	ConfStandard ConfLevel = 1
	// ConfHigh indicates high confidentiality requiring sealed memory and strict boundaries.
	ConfHigh ConfLevel = 2
	// ConfCritical indicates critical confidentiality with full hardware/enclave sealing.
	ConfCritical ConfLevel = 3
)

// String returns the human-readable name of the confidentiality level.
func (c ConfLevel) String() string {
	switch c {
	case ConfLow:
		return "low"
	case ConfStandard:
		return "standard"
	case ConfHigh:
		return "high"
	case ConfCritical:
		return "critical"
	default:
		return fmt.Sprintf("conflevel(%d)", uint8(c))
	}
}

// FuncAttr specifies metadata attributes that govern execution security and code generation.
type FuncAttr struct {
	// Confidentiality defines the isolation requirements of the function.
	Confidentiality ConfLevel
	// MustNotInline prevents inlining of the function during compilation/optimization.
	MustNotInline bool
	// Diversify instructs code generators to apply binary diversity and obfuscation.
	Diversify bool
}

// Instruction represents a single executable bytecode operation in the Aegis IR.
type Instruction struct {
	// Op is the operation code.
	Op Opcode
	// Operands holds immediate values or indices required by the instruction.
	Operands [3]uint64
}

// FuncType describes a function signature with its parameter and result value types.
type FuncType struct {
	// Params lists the parameter types accepted by the function.
	Params []ValType
	// Results lists the result types produced by the function.
	Results []ValType
}

// Function represents a compiled function definition within an Aegis module.
type Function struct {
	// Name is the symbolic function name (empty in stripped release binaries).
	Name string
	// TypeIndex references the FuncType in Module.Types for this function.
	TypeIndex uint32
	// Locals defines the types of additional local variables allocated for this function.
	Locals []ValType
	// Body contains the sequence of instructions implementing the function logic.
	Body []Instruction
	// Attributes specifies confidentiality and execution directives.
	Attributes FuncAttr
}

// Import declares an external dependency imported by the module.
type Import struct {
	// Module is the namespace or external module name.
	Module string
	// Name is the symbol or export name imported from the module.
	Name string
	// Type references the FuncType index in Module.Types defining the import signature.
	Type uint32
}

// Export exposes an internal function or imported function to the caller.
type Export struct {
	// Name is the exported symbol name.
	Name string
	// FuncIndex identifies the function or import being exported.
	FuncIndex uint32
}

// MemorySpec defines linear memory limits in 64 KiB pages.
type MemorySpec struct {
	// InitialPages is the initial memory allocation size in pages.
	InitialPages uint32
	// MaxPages is the maximum memory growth limit in pages.
	MaxPages uint32
}

// DataSegment defines static linear memory initialization data.
type DataSegment struct {
	// Offset is the byte offset into linear memory where data should be written.
	Offset uint32
	// Data holds the raw bytes to initialize into linear memory.
	Data []byte
}

// Metadata records build, platform, and compiler information for the module.
type Metadata struct {
	// CompilerVersion is the identifier and version of the compiler producing the module.
	CompilerVersion string
	// TargetPlatform is the architecture and platform target.
	TargetPlatform string
	// BuildTime is the Unix epoch timestamp in seconds when the module was built.
	BuildTime int64
}

// Module is the top-level container for a complete Aegis Intermediate Representation program.
type Module struct {
	// Version is the Aegis IR format specification version.
	Version uint32
	// BuildID is the unique 32-byte cryptographic identifier for this build.
	BuildID [32]byte
	// Functions contains all internal function definitions.
	Functions []Function
	// Types holds the table of function signatures.
	Types []FuncType
	// Data contains linear memory initial data segments.
	Data []DataSegment
	// Imports lists all external symbols required by the module.
	Imports []Import
	// Exports lists symbols exposed to external callers.
	Exports []Export
	// Memory defines the linear memory specifications.
	Memory MemorySpec
	// Metadata contains optional compiler and build metadata.
	Metadata Metadata
}
