package vm

import (
	"fmt"
	"strings"

	"github.com/oarkflow/ref/aegis/ir"
)

// TrapKind identifies the category of a runtime trap, safety violation, or execution failure.
type TrapKind uint8

const (
	// TrapExplicit indicates execution of an explicit trap instruction (OpTrap).
	TrapExplicit TrapKind = iota
	// TrapStackOverflow indicates the value stack exceeded its maximum configured depth.
	TrapStackOverflow
	// TrapStackUnderflow indicates an attempt to pop from an empty value stack.
	TrapStackUnderflow
	// TrapCallStackOverflow indicates nested function calls exceeded the call stack limit.
	TrapCallStackOverflow
	// TrapMemoryOutOfBounds indicates memory access outside allocated linear memory pages.
	TrapMemoryOutOfBounds
	// TrapDivisionByZero indicates integer division or remainder with a zero divisor.
	TrapDivisionByZero
	// TrapIntegerOverflow indicates signed integer overflow during arithmetic operations.
	TrapIntegerOverflow
	// TrapFuelExhausted indicates instruction execution budget reached zero.
	TrapFuelExhausted
	// TrapCancelled indicates execution was cancelled via context cancellation.
	TrapCancelled
	// TrapInvalidPC indicates the program counter jumped outside valid code bounds.
	TrapInvalidPC
	// TrapInvalidOpcode indicates an unrecognized or malformed bytecode opcode.
	TrapInvalidOpcode
	// TrapTypeMismatch indicates mismatched operand count, signature, or value types.
	TrapTypeMismatch
	// TrapHostError indicates an imported host function returned an error.
	TrapHostError
	// TrapUnresolvedImport indicates an imported function was not provided to the VM.
	TrapUnresolvedImport
	// TrapInvalidFunction indicates a call or export referenced a non-existent function index.
	TrapInvalidFunction
)

// String returns the canonical human-readable name of the trap kind.
func (k TrapKind) String() string {
	switch k {
	case TrapExplicit:
		return "explicit trap"
	case TrapStackOverflow:
		return "stack overflow"
	case TrapStackUnderflow:
		return "stack underflow"
	case TrapCallStackOverflow:
		return "call stack overflow"
	case TrapMemoryOutOfBounds:
		return "memory out of bounds"
	case TrapDivisionByZero:
		return "division by zero"
	case TrapIntegerOverflow:
		return "integer overflow"
	case TrapFuelExhausted:
		return "fuel exhausted"
	case TrapCancelled:
		return "cancelled"
	case TrapInvalidPC:
		return "invalid program counter"
	case TrapInvalidOpcode:
		return "invalid opcode"
	case TrapTypeMismatch:
		return "type mismatch"
	case TrapHostError:
		return "host error"
	case TrapUnresolvedImport:
		return "unresolved import"
	case TrapInvalidFunction:
		return "invalid function"
	default:
		return fmt.Sprintf("trap(%d)", uint8(k))
	}
}

// Trap represents a runtime trap, safety assertion failure, or unrecoverable error during execution.
type Trap struct {
	// Kind specifies the category of trap.
	Kind TrapKind
	// Opcode is the IR opcode executing when the trap occurred.
	Opcode ir.Opcode
	// Addr is the memory address associated with memory access faults.
	Addr uint32
	// FuncIdx is the index of the function executing when the trap occurred.
	FuncIdx uint32
	// PC is the program counter (instruction body offset) where the trap occurred.
	PC uint32
	// Cause is the underlying root error, if any.
	Cause error
	// Message provides human-readable detail on the trap cause.
	Message string
}

// Error formats the trap into a descriptive error string prefixed with "vm:".
func (t *Trap) Error() string {
	if t.Message != "" {
		if strings.HasPrefix(t.Message, "vm:") {
			return t.Message
		}
		return fmt.Sprintf("vm: %s", t.Message)
	}
	if t.Cause != nil {
		return fmt.Sprintf("vm: trap: %s: %v", t.Kind.String(), t.Cause)
	}
	return fmt.Sprintf("vm: trap: %s", t.Kind.String())
}

// Unwrap returns the underlying causal error, implementing the Go errors.Unwrap interface.
func (t *Trap) Unwrap() error {
	return t.Cause
}
