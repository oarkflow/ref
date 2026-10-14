package vm

import (
	"math"
)

// Stack represents the fixed-capacity execution value stack for bytecode evaluation.
type Stack struct {
	data     []uint64
	maxDepth int
}

// NewStack initializes an empty operand value stack with the specified maximum capacity.
func NewStack(maxDepth int) *Stack {
	if maxDepth <= 0 {
		maxDepth = 65536
	}
	initialCap := 1024
	if initialCap > maxDepth {
		initialCap = maxDepth
	}
	return &Stack{
		data:     make([]uint64, 0, initialCap),
		maxDepth: maxDepth,
	}
}

// Push appends a raw 64-bit value to the top of the stack, checking against max depth.
func (s *Stack) Push(v uint64) error {
	if len(s.data) >= s.maxDepth {
		return &Trap{
			Kind:    TrapStackOverflow,
			Message: "vm: stack overflow",
		}
	}
	s.data = append(s.data, v)
	return nil
}

// Pop removes and returns the top 64-bit value from the stack, checking for underflow.
func (s *Stack) Pop() (uint64, error) {
	if len(s.data) == 0 {
		return 0, &Trap{
			Kind:    TrapStackUnderflow,
			Message: "vm: stack underflow",
		}
	}
	topIndex := len(s.data) - 1
	val := s.data[topIndex]
	s.data = s.data[:topIndex]
	return val, nil
}

// Peek returns the top 64-bit value from the stack without removing it.
func (s *Stack) Peek() (uint64, error) {
	if len(s.data) == 0 {
		return 0, &Trap{
			Kind:    TrapStackUnderflow,
			Message: "vm: stack underflow",
		}
	}
	return s.data[len(s.data)-1], nil
}

// PushU32 pushes an unsigned 32-bit integer onto the stack.
func (s *Stack) PushU32(v uint32) error {
	return s.Push(uint64(v))
}

// PopU32 pops a value and converts it to an unsigned 32-bit integer.
func (s *Stack) PopU32() (uint32, error) {
	v, err := s.Pop()
	if err != nil {
		return 0, err
	}
	return uint32(v), nil
}

// PushI32 pushes a signed 32-bit integer onto the stack.
func (s *Stack) PushI32(v int32) error {
	return s.Push(uint64(uint32(v)))
}

// PopI32 pops a value and converts it to a signed 32-bit integer.
func (s *Stack) PopI32() (int32, error) {
	v, err := s.Pop()
	if err != nil {
		return 0, err
	}
	return int32(uint32(v)), nil
}

// PushU64 pushes an unsigned 64-bit integer onto the stack.
func (s *Stack) PushU64(v uint64) error {
	return s.Push(v)
}

// PopU64 pops an unsigned 64-bit integer from the stack.
func (s *Stack) PopU64() (uint64, error) {
	return s.Pop()
}

// PushI64 pushes a signed 64-bit integer onto the stack.
func (s *Stack) PushI64(v int64) error {
	return s.Push(uint64(v))
}

// PopI64 pops a value and converts it to a signed 64-bit integer.
func (s *Stack) PopI64() (int64, error) {
	v, err := s.Pop()
	if err != nil {
		return 0, err
	}
	return int64(v), nil
}

// PushF64 pushes a 64-bit IEEE 754 floating-point number onto the stack.
func (s *Stack) PushF64(v float64) error {
	return s.Push(math.Float64bits(v))
}

// PopF64 pops a value and converts it to a 64-bit IEEE 754 floating-point number.
func (s *Stack) PopF64() (float64, error) {
	bits, err := s.Pop()
	if err != nil {
		return 0, err
	}
	return math.Float64frombits(bits), nil
}

// PushF32 pushes a 32-bit IEEE 754 floating-point number onto the stack.
func (s *Stack) PushF32(v float32) error {
	return s.Push(uint64(math.Float32bits(v)))
}

// PopF32 pops a value and converts it to a 32-bit IEEE 754 floating-point number.
func (s *Stack) PopF32() (float32, error) {
	bits, err := s.Pop()
	if err != nil {
		return 0, err
	}
	return math.Float32frombits(uint32(bits)), nil
}

// Len returns the current number of elements on the stack.
func (s *Stack) Len() int {
	return len(s.data)
}

// Reset clears all values from the stack while preserving underlying capacity.
func (s *Stack) Reset() {
	s.data = s.data[:0]
}
