package ir

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"sync"
)

// Section ID constants for the Aegis IR binary format.
const (
	// SectionType contains function signature declarations.
	SectionType uint8 = 0x01
	// SectionImport contains external module symbol import declarations.
	SectionImport uint8 = 0x02
	// SectionFunction contains internal function definitions, locals, and bytecode bodies.
	SectionFunction uint8 = 0x03
	// SectionExport contains symbol export declarations.
	SectionExport uint8 = 0x04
	// SectionMemory contains linear memory limits and configuration.
	SectionMemory uint8 = 0x05
	// SectionData contains static linear memory initialization segments.
	SectionData uint8 = 0x06
	// SectionMetadata contains optional compiler, platform, and build timestamp metadata.
	SectionMetadata uint8 = 0x07
)

// Function attribute flag bitmasks.
const (
	// FuncFlagMustNotInline indicates that the function must never be inlined.
	FuncFlagMustNotInline uint8 = 1 << 0
	// FuncFlagDiversify indicates that the function should be compiled with binary diversification.
	FuncFlagDiversify uint8 = 1 << 1
)

// bufferPool recycles bytes.Buffer instances to eliminate hot-path allocations.
var bufferPool = sync.Pool{
	New: func() any {
		return new(bytes.Buffer)
	},
}

func getPooledBuffer() *bytes.Buffer {
	b := bufferPool.Get().(*bytes.Buffer)
	b.Reset()
	return b
}

func putPooledBuffer(b *bytes.Buffer) {
	if b.Cap() > 128*1024 {
		return
	}
	b.Reset()
	bufferPool.Put(b)
}

func writeUint8(buf *bytes.Buffer, v uint8) {
	buf.WriteByte(v)
}

func writeUint16(buf *bytes.Buffer, v uint16) {
	var scratch [2]byte
	binary.LittleEndian.PutUint16(scratch[:], v)
	buf.Write(scratch[:])
}

func writeUint32(buf *bytes.Buffer, v uint32) {
	var scratch [4]byte
	binary.LittleEndian.PutUint32(scratch[:], v)
	buf.Write(scratch[:])
}

func writeUint64(buf *bytes.Buffer, v uint64) {
	var scratch [8]byte
	binary.LittleEndian.PutUint64(scratch[:], v)
	buf.Write(scratch[:])
}

func writeInt64(buf *bytes.Buffer, v int64) {
	var scratch [8]byte
	binary.LittleEndian.PutUint64(scratch[:], uint64(v))
	buf.Write(scratch[:])
}

func writeString(buf *bytes.Buffer, s string) error {
	if len(s) > math.MaxUint16 {
		return fmt.Errorf("ir: string exceeds maximum length of 65535 bytes (len=%d)", len(s))
	}
	writeUint16(buf, uint16(len(s)))
	buf.WriteString(s)
	return nil
}

func writeSection(mainBuf *bytes.Buffer, sectionID uint8, secBuf *bytes.Buffer) {
	writeUint8(mainBuf, sectionID)
	writeUint32(mainBuf, uint32(secBuf.Len()))
	mainBuf.Write(secBuf.Bytes())
}

// Encode serializes an Aegis IR module into binary bytes.
// It follows the canonical section ordering (Type, Import, Function, Export, Memory, Data, Metadata).
func Encode(m *Module) ([]byte, error) {
	if m == nil {
		return nil, errors.New("ir: cannot encode nil module")
	}

	mainBuf := getPooledBuffer()
	defer putPooledBuffer(mainBuf)

	// Magic: 4 bytes "AEGS"
	mainBuf.WriteString(Magic)

	// Version: 4 bytes uint32 little-endian
	writeUint32(mainBuf, m.Version)

	// BuildID: 32 bytes
	mainBuf.Write(m.BuildID[:])

	// Section 0x01: TYPE
	if len(m.Types) > 0 {
		secBuf := getPooledBuffer()
		writeUint32(secBuf, uint32(len(m.Types)))
		for _, ft := range m.Types {
			writeUint32(secBuf, uint32(len(ft.Params)))
			for _, paramType := range ft.Params {
				writeUint8(secBuf, uint8(paramType))
			}
			writeUint32(secBuf, uint32(len(ft.Results)))
			for _, resType := range ft.Results {
				writeUint8(secBuf, uint8(resType))
			}
		}
		writeSection(mainBuf, SectionType, secBuf)
		putPooledBuffer(secBuf)
	}

	// Section 0x02: IMPORT
	if len(m.Imports) > 0 {
		secBuf := getPooledBuffer()
		writeUint32(secBuf, uint32(len(m.Imports)))
		for _, imp := range m.Imports {
			if err := writeString(secBuf, imp.Module); err != nil {
				putPooledBuffer(secBuf)
				return nil, err
			}
			if err := writeString(secBuf, imp.Name); err != nil {
				putPooledBuffer(secBuf)
				return nil, err
			}
			writeUint32(secBuf, imp.Type)
		}
		writeSection(mainBuf, SectionImport, secBuf)
		putPooledBuffer(secBuf)
	}

	// Section 0x03: FUNCTION
	if len(m.Functions) > 0 {
		secBuf := getPooledBuffer()
		writeUint32(secBuf, uint32(len(m.Functions)))
		for _, fn := range m.Functions {
			writeUint32(secBuf, fn.TypeIndex)
			writeUint8(secBuf, uint8(fn.Attributes.Confidentiality))

			var flags uint8
			if fn.Attributes.MustNotInline {
				flags |= FuncFlagMustNotInline
			}
			if fn.Attributes.Diversify {
				flags |= FuncFlagDiversify
			}
			writeUint8(secBuf, flags)

			writeUint32(secBuf, uint32(len(fn.Locals)))
			for _, localType := range fn.Locals {
				writeUint8(secBuf, uint8(localType))
			}

			if err := writeString(secBuf, fn.Name); err != nil {
				putPooledBuffer(secBuf)
				return nil, err
			}

			writeUint32(secBuf, uint32(len(fn.Body)))
			for _, inst := range fn.Body {
				writeUint16(secBuf, uint16(inst.Op))
				writeUint64(secBuf, inst.Operands[0])
				writeUint64(secBuf, inst.Operands[1])
				writeUint64(secBuf, inst.Operands[2])
			}
		}
		writeSection(mainBuf, SectionFunction, secBuf)
		putPooledBuffer(secBuf)
	}

	// Section 0x04: EXPORT
	if len(m.Exports) > 0 {
		secBuf := getPooledBuffer()
		writeUint32(secBuf, uint32(len(m.Exports)))
		for _, exp := range m.Exports {
			if err := writeString(secBuf, exp.Name); err != nil {
				putPooledBuffer(secBuf)
				return nil, err
			}
			writeUint32(secBuf, exp.FuncIndex)
		}
		writeSection(mainBuf, SectionExport, secBuf)
		putPooledBuffer(secBuf)
	}

	// Section 0x05: MEMORY
	if m.Memory.InitialPages > 0 || m.Memory.MaxPages > 0 {
		secBuf := getPooledBuffer()
		writeUint32(secBuf, m.Memory.InitialPages)
		writeUint32(secBuf, m.Memory.MaxPages)
		writeSection(mainBuf, SectionMemory, secBuf)
		putPooledBuffer(secBuf)
	}

	// Section 0x06: DATA
	if len(m.Data) > 0 {
		secBuf := getPooledBuffer()
		writeUint32(secBuf, uint32(len(m.Data)))
		for _, seg := range m.Data {
			writeUint32(secBuf, seg.Offset)
			writeUint32(secBuf, uint32(len(seg.Data)))
			secBuf.Write(seg.Data)
		}
		writeSection(mainBuf, SectionData, secBuf)
		putPooledBuffer(secBuf)
	}

	// Section 0x07: METADATA (optional)
	if m.Metadata.CompilerVersion != "" || m.Metadata.TargetPlatform != "" || m.Metadata.BuildTime != 0 {
		secBuf := getPooledBuffer()
		if err := writeString(secBuf, m.Metadata.CompilerVersion); err != nil {
			putPooledBuffer(secBuf)
			return nil, err
		}
		if err := writeString(secBuf, m.Metadata.TargetPlatform); err != nil {
			putPooledBuffer(secBuf)
			return nil, err
		}
		writeInt64(secBuf, m.Metadata.BuildTime)
		writeSection(mainBuf, SectionMetadata, secBuf)
		putPooledBuffer(secBuf)
	}

	// Return a copy of the buffer bytes
	res := make([]byte, mainBuf.Len())
	copy(res, mainBuf.Bytes())
	return res, nil
}
