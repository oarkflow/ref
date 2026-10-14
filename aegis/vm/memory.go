package vm

import (
	"encoding/binary"
	"fmt"
	"math"
)

// PageSize is the byte size of a single linear memory page (64 KiB).
const PageSize uint32 = 65536

// Memory represents a linear memory buffer managed in 64 KiB pages.
type Memory struct {
	data         []byte
	pages        uint32
	maxPages     uint32
	initialPages uint32
}

// NewMemory initializes linear memory with the specified initial and maximum page bounds.
func NewMemory(initial, max uint32) (*Memory, error) {
	if max < initial {
		return nil, fmt.Errorf("vm: memory max pages (%d) must be greater than or equal to initial pages (%d)", max, initial)
	}
	initialBytes := uint64(initial) * uint64(PageSize)
	if initialBytes > math.MaxInt {
		return nil, fmt.Errorf("vm: initial memory size exceeds addressable memory")
	}
	return &Memory{
		data:         make([]byte, initialBytes),
		pages:        initial,
		maxPages:     max,
		initialPages: initial,
	}, nil
}

// LoadU32 reads a little-endian 32-bit unsigned integer from the specified byte offset.
func (m *Memory) LoadU32(offset uint32) (uint32, error) {
	if uint64(offset)+4 > uint64(len(m.data)) {
		return 0, &Trap{
			Kind:    TrapMemoryOutOfBounds,
			Addr:    offset,
			Message: fmt.Sprintf("vm: load32 out of bounds at offset 0x%X (len: %d)", offset, len(m.data)),
		}
	}
	return binary.LittleEndian.Uint32(m.data[offset : offset+4]), nil
}

// LoadU64 reads a little-endian 64-bit unsigned integer from the specified byte offset.
func (m *Memory) LoadU64(offset uint32) (uint64, error) {
	if uint64(offset)+8 > uint64(len(m.data)) {
		return 0, &Trap{
			Kind:    TrapMemoryOutOfBounds,
			Addr:    offset,
			Message: fmt.Sprintf("vm: load64 out of bounds at offset 0x%X (len: %d)", offset, len(m.data)),
		}
	}
	return binary.LittleEndian.Uint64(m.data[offset : offset+8]), nil
}

// StoreU32 writes a little-endian 32-bit unsigned integer to the specified byte offset.
func (m *Memory) StoreU32(offset uint32, val uint32) error {
	if uint64(offset)+4 > uint64(len(m.data)) {
		return &Trap{
			Kind:    TrapMemoryOutOfBounds,
			Addr:    offset,
			Message: fmt.Sprintf("vm: store32 out of bounds at offset 0x%X (len: %d)", offset, len(m.data)),
		}
	}
	binary.LittleEndian.PutUint32(m.data[offset:offset+4], val)
	return nil
}

// StoreU64 writes a little-endian 64-bit unsigned integer to the specified byte offset.
func (m *Memory) StoreU64(offset uint32, val uint64) error {
	if uint64(offset)+8 > uint64(len(m.data)) {
		return &Trap{
			Kind:    TrapMemoryOutOfBounds,
			Addr:    offset,
			Message: fmt.Sprintf("vm: store64 out of bounds at offset 0x%X (len: %d)", offset, len(m.data)),
		}
	}
	binary.LittleEndian.PutUint64(m.data[offset:offset+8], val)
	return nil
}

// LoadF32 reads a 32-bit IEEE 754 float from the specified byte offset.
func (m *Memory) LoadF32(offset uint32) (float32, error) {
	val, err := m.LoadU32(offset)
	if err != nil {
		return 0, err
	}
	return math.Float32frombits(val), nil
}

// LoadF64 reads a 64-bit IEEE 754 float from the specified byte offset.
func (m *Memory) LoadF64(offset uint32) (float64, error) {
	val, err := m.LoadU64(offset)
	if err != nil {
		return 0, err
	}
	return math.Float64frombits(val), nil
}

// StoreF32 writes a 32-bit IEEE 754 float to the specified byte offset.
func (m *Memory) StoreF32(offset uint32, val float32) error {
	return m.StoreU32(offset, math.Float32bits(val))
}

// StoreF64 writes a 64-bit IEEE 754 float to the specified byte offset.
func (m *Memory) StoreF64(offset uint32, val float64) error {
	return m.StoreU64(offset, math.Float64bits(val))
}

// Size returns the current memory allocation size in 64 KiB pages.
func (m *Memory) Size() uint32 {
	return m.pages
}

// Grow increases memory by delta pages and returns the previous page count.
// Returns an error if the growth exceeds maxPages or system limits.
func (m *Memory) Grow(delta uint32) (uint32, error) {
	oldPages := m.pages
	if delta == 0 {
		return oldPages, nil
	}
	newPages := uint64(m.pages) + uint64(delta)
	if newPages > uint64(m.maxPages) {
		return oldPages, &Trap{
			Kind:    TrapMemoryOutOfBounds,
			Message: fmt.Sprintf("vm: memory growth by %d pages exceeds max pages limit %d", delta, m.maxPages),
		}
	}
	newByteLen := newPages * uint64(PageSize)
	if newByteLen > math.MaxInt {
		return oldPages, &Trap{
			Kind:    TrapMemoryOutOfBounds,
			Message: "vm: memory growth exceeds addressable memory limit",
		}
	}
	newData := make([]byte, newByteLen)
	copy(newData, m.data)
	m.data = newData
	m.pages = uint32(newPages)
	return oldPages, nil
}

// ReadBytes copies length bytes from linear memory starting at offset.
func (m *Memory) ReadBytes(offset, length uint32) ([]byte, error) {
	end := uint64(offset) + uint64(length)
	if end > uint64(len(m.data)) {
		return nil, &Trap{
			Kind:    TrapMemoryOutOfBounds,
			Addr:    offset,
			Message: fmt.Sprintf("vm: read bytes out of bounds at offset 0x%X (len: %d, mem: %d)", offset, length, len(m.data)),
		}
	}
	out := make([]byte, length)
	copy(out, m.data[offset:offset+length])
	return out, nil
}

// WriteBytes copies bytes into linear memory starting at offset.
func (m *Memory) WriteBytes(offset uint32, data []byte) error {
	end := uint64(offset) + uint64(len(data))
	if end > uint64(len(m.data)) {
		return &Trap{
			Kind:    TrapMemoryOutOfBounds,
			Addr:    offset,
			Message: fmt.Sprintf("vm: write bytes out of bounds at offset 0x%X (len: %d, mem: %d)", offset, len(data), len(m.data)),
		}
	}
	copy(m.data[offset:], data)
	return nil
}

// Reset re-initializes linear memory back to its initial allocation size, zeroing all bytes.
func (m *Memory) Reset() {
	initialBytes := int(m.initialPages) * int(PageSize)
	m.data = make([]byte, initialBytes)
	m.pages = m.initialPages
}
