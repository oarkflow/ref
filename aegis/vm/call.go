package vm

import (
	"fmt"

	"github.com/oarkflow/ref/aegis/ir"
)

// blockEntry records runtime metadata for an active structured control block (block, loop, or if).
type blockEntry struct {
	kind       ir.Opcode
	startPC    uint32
	endPC      uint32
	elsePC     uint32
	stackDepth int
}

// Frame represents an active function activation record on the call stack.
type Frame struct {
	// FuncIndex is the index of the function executing within this frame.
	FuncIndex uint32
	// PC is the instruction pointer (index into the function body) currently being executed.
	PC uint32
	// LocalsBase is the starting offset of this frame's locals within the global Locals table.
	LocalsBase int
	// ReturnArity is the number of return values expected by the caller.
	ReturnArity int
	// blockStack tracks nested control flow blocks (Block, Loop, If) active in this frame.
	blockStack []blockEntry
}

// CallStack maintains the stack of active function invocation frames.
type CallStack struct {
	frames   []Frame
	maxDepth int
}

// NewCallStack allocates a CallStack bounded by maxDepth.
func NewCallStack(maxDepth int) *CallStack {
	if maxDepth <= 0 {
		maxDepth = 1024
	}
	initialCap := 64
	if initialCap > maxDepth {
		initialCap = maxDepth
	}
	return &CallStack{
		frames:   make([]Frame, 0, initialCap),
		maxDepth: maxDepth,
	}
}

// Push adds an activation frame to the call stack, returning an error if maxDepth is exceeded.
func (cs *CallStack) Push(frame Frame) error {
	if len(cs.frames) >= cs.maxDepth {
		return &Trap{
			Kind:    TrapCallStackOverflow,
			FuncIdx: frame.FuncIndex,
			PC:      frame.PC,
			Message: fmt.Sprintf("vm: call stack overflow (depth %d exceeds cap %d)", len(cs.frames)+1, cs.maxDepth),
		}
	}
	cs.frames = append(cs.frames, frame)
	return nil
}

// Pop removes and returns the top activation frame from the call stack.
func (cs *CallStack) Pop() (Frame, error) {
	if len(cs.frames) == 0 {
		return Frame{}, &Trap{
			Kind:    TrapStackUnderflow,
			Message: "vm: call stack underflow",
		}
	}
	lastIdx := len(cs.frames) - 1
	top := cs.frames[lastIdx]
	cs.frames = cs.frames[:lastIdx]
	return top, nil
}

// Current returns a pointer to the current active top frame, or nil if the call stack is empty.
func (cs *CallStack) Current() *Frame {
	if len(cs.frames) == 0 {
		return nil
	}
	return &cs.frames[len(cs.frames)-1]
}

// Depth returns the current number of active activation frames.
func (cs *CallStack) Depth() int {
	return len(cs.frames)
}

// Reset clears all frames from the call stack.
func (cs *CallStack) Reset() {
	cs.frames = cs.frames[:0]
}

// Locals manages the contiguous storage of function local variables and parameters.
type Locals struct {
	data []uint64
	base int
}

// NewLocals initializes an empty locals store.
func NewLocals() *Locals {
	return &Locals{
		data: make([]uint64, 0, 128),
		base: 0,
	}
}

// SetBase updates the active base index for local variable indexing.
func (l *Locals) SetBase(base int) {
	l.base = base
}

// Base returns the current frame base offset.
func (l *Locals) Base() int {
	return l.base
}

// Allocate grows the locals table by count slots and returns the previous base offset.
func (l *Locals) Allocate(count int) int {
	oldBase := len(l.data)
	if count > 0 {
		l.data = append(l.data, make([]uint64, count)...)
	}
	l.base = oldBase
	return oldBase
}

// Release frees locals back to the specified base offset.
func (l *Locals) Release(base int) {
	if base >= 0 && base <= len(l.data) {
		l.data = l.data[:base]
	}
	if l.base > base {
		l.base = base
	}
}

// Get reads the local variable at relative index from the current frame base.
func (l *Locals) Get(index uint32) (uint64, error) {
	absIdx := l.base + int(index)
	if absIdx < 0 || absIdx >= len(l.data) {
		return 0, &Trap{
			Kind:    TrapTypeMismatch,
			Message: fmt.Sprintf("vm: local index %d out of bounds (base %d, total %d)", index, l.base, len(l.data)),
		}
	}
	return l.data[absIdx], nil
}

// Set writes val to the local variable at relative index from the current frame base.
func (l *Locals) Set(index uint32, val uint64) error {
	absIdx := l.base + int(index)
	if absIdx < 0 || absIdx >= len(l.data) {
		return &Trap{
			Kind:    TrapTypeMismatch,
			Message: fmt.Sprintf("vm: local index %d out of bounds (base %d, total %d)", index, l.base, len(l.data)),
		}
	}
	l.data[absIdx] = val
	return nil
}

// SetAt sets the value at an absolute index in the storage slice.
func (l *Locals) SetAt(absIdx int, val uint64) error {
	if absIdx < 0 || absIdx >= len(l.data) {
		return &Trap{
			Kind:    TrapTypeMismatch,
			Message: fmt.Sprintf("vm: absolute local index %d out of bounds (total %d)", absIdx, len(l.data)),
		}
	}
	l.data[absIdx] = val
	return nil
}

// Reset clears all locals and resets base to 0.
func (l *Locals) Reset() {
	l.data = l.data[:0]
	l.base = 0
}
