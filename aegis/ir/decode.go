package ir

import (
	"encoding/binary"
	"errors"
	"fmt"
)

type binaryReader struct {
	data   []byte
	offset int
}

func newBinaryReader(data []byte) *binaryReader {
	return &binaryReader{data: data, offset: 0}
}

func (r *binaryReader) remaining() int {
	return len(r.data) - r.offset
}

func (r *binaryReader) readUint8() (uint8, error) {
	if r.offset+1 > len(r.data) {
		return 0, errors.New("ir: unexpected EOF reading uint8")
	}
	b := r.data[r.offset]
	r.offset++
	return b, nil
}

func (r *binaryReader) readUint16() (uint16, error) {
	if r.offset+2 > len(r.data) {
		return 0, errors.New("ir: unexpected EOF reading uint16")
	}
	v := binary.LittleEndian.Uint16(r.data[r.offset : r.offset+2])
	r.offset += 2
	return v, nil
}

func (r *binaryReader) readUint32() (uint32, error) {
	if r.offset+4 > len(r.data) {
		return 0, errors.New("ir: unexpected EOF reading uint32")
	}
	v := binary.LittleEndian.Uint32(r.data[r.offset : r.offset+4])
	r.offset += 4
	return v, nil
}

func (r *binaryReader) readUint64() (uint64, error) {
	if r.offset+8 > len(r.data) {
		return 0, errors.New("ir: unexpected EOF reading uint64")
	}
	v := binary.LittleEndian.Uint64(r.data[r.offset : r.offset+8])
	r.offset += 8
	return v, nil
}

func (r *binaryReader) readInt64() (int64, error) {
	u, err := r.readUint64()
	if err != nil {
		return 0, err
	}
	return int64(u), nil
}

func (r *binaryReader) readBytes(n int) ([]byte, error) {
	if n < 0 || r.offset+n > len(r.data) {
		return nil, errors.New("ir: unexpected EOF reading bytes")
	}
	b := r.data[r.offset : r.offset+n]
	r.offset += n
	return b, nil
}

func (r *binaryReader) readString() (string, error) {
	strLen, err := r.readUint16()
	if err != nil {
		return "", err
	}
	b, err := r.readBytes(int(strLen))
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// Decode deserializes binary Aegis IR data into a Module struct, validating
// the file header, magic bytes, version, section structure, and boundary limits.
func Decode(data []byte) (*Module, error) {
	if len(data) < 40 {
		return nil, errors.New("ir: binary data too short (minimum 40 bytes required)")
	}

	// Magic: 4 bytes "AEGS"
	if string(data[:4]) != Magic {
		return nil, fmt.Errorf("ir: invalid magic header %q, expected %q", string(data[:4]), Magic)
	}

	// Version: 4 bytes uint32 little-endian
	version := binary.LittleEndian.Uint32(data[4:8])
	if version != Version {
		return nil, fmt.Errorf("ir: unsupported format version %d, expected %d", version, Version)
	}

	// BuildID: 32 bytes
	var buildID [32]byte
	copy(buildID[:], data[8:40])

	m := &Module{
		Version: version,
		BuildID: buildID,
	}

	offset := 40
	for offset < len(data) {
		if offset+5 > len(data) {
			return nil, errors.New("ir: truncated section header")
		}

		sectionID := data[offset]
		sectionLen := binary.LittleEndian.Uint32(data[offset+1 : offset+5])
		offset += 5

		if uint64(offset)+uint64(sectionLen) > uint64(len(data)) {
			return nil, fmt.Errorf("ir: truncated section 0x%02x: expected %d bytes, got %d",
				sectionID, sectionLen, len(data)-offset)
		}

		sectionData := data[offset : offset+int(sectionLen)]
		offset += int(sectionLen)

		reader := newBinaryReader(sectionData)

		switch sectionID {
		case SectionType:
			count, err := reader.readUint32()
			if err != nil {
				return nil, err
			}
			m.Types = make([]FuncType, count)
			for i := uint32(0); i < count; i++ {
				paramCount, err := reader.readUint32()
				if err != nil {
					return nil, err
				}
				params := make([]ValType, paramCount)
				for p := uint32(0); p < paramCount; p++ {
					pt, err := reader.readUint8()
					if err != nil {
						return nil, err
					}
					params[p] = ValType(pt)
				}
				resultCount, err := reader.readUint32()
				if err != nil {
					return nil, err
				}
				results := make([]ValType, resultCount)
				for rIdx := uint32(0); rIdx < resultCount; rIdx++ {
					rt, err := reader.readUint8()
					if err != nil {
						return nil, err
					}
					results[rIdx] = ValType(rt)
				}
				m.Types[i] = FuncType{
					Params:  params,
					Results: results,
				}
			}
			if reader.remaining() > 0 {
				return nil, errors.New("ir: unexpected trailing data in type section")
			}

		case SectionImport:
			count, err := reader.readUint32()
			if err != nil {
				return nil, err
			}
			m.Imports = make([]Import, count)
			for i := uint32(0); i < count; i++ {
				mod, err := reader.readString()
				if err != nil {
					return nil, err
				}
				name, err := reader.readString()
				if err != nil {
					return nil, err
				}
				typeIdx, err := reader.readUint32()
				if err != nil {
					return nil, err
				}
				m.Imports[i] = Import{
					Module: mod,
					Name:   name,
					Type:   typeIdx,
				}
			}
			if reader.remaining() > 0 {
				return nil, errors.New("ir: unexpected trailing data in import section")
			}

		case SectionFunction:
			count, err := reader.readUint32()
			if err != nil {
				return nil, err
			}
			m.Functions = make([]Function, count)
			for i := uint32(0); i < count; i++ {
				typeIndex, err := reader.readUint32()
				if err != nil {
					return nil, err
				}
				confByte, err := reader.readUint8()
				if err != nil {
					return nil, err
				}
				flagsByte, err := reader.readUint8()
				if err != nil {
					return nil, err
				}
				localsCount, err := reader.readUint32()
				if err != nil {
					return nil, err
				}
				locals := make([]ValType, localsCount)
				for l := uint32(0); l < localsCount; l++ {
					lt, err := reader.readUint8()
					if err != nil {
						return nil, err
					}
					locals[l] = ValType(lt)
				}
				name, err := reader.readString()
				if err != nil {
					return nil, err
				}
				bodyCount, err := reader.readUint32()
				if err != nil {
					return nil, err
				}
				body := make([]Instruction, bodyCount)
				for b := uint32(0); b < bodyCount; b++ {
					op, err := reader.readUint16()
					if err != nil {
						return nil, err
					}
					op0, err := reader.readUint64()
					if err != nil {
						return nil, err
					}
					op1, err := reader.readUint64()
					if err != nil {
						return nil, err
					}
					op2, err := reader.readUint64()
					if err != nil {
						return nil, err
					}
					body[b] = Instruction{
						Op:       Opcode(op),
						Operands: [3]uint64{op0, op1, op2},
					}
				}
				m.Functions[i] = Function{
					Name:      name,
					TypeIndex: typeIndex,
					Locals:    locals,
					Body:      body,
					Attributes: FuncAttr{
						Confidentiality: ConfLevel(confByte),
						MustNotInline:   (flagsByte & FuncFlagMustNotInline) != 0,
						Diversify:       (flagsByte & FuncFlagDiversify) != 0,
					},
				}
			}
			if reader.remaining() > 0 {
				return nil, errors.New("ir: unexpected trailing data in function section")
			}

		case SectionExport:
			count, err := reader.readUint32()
			if err != nil {
				return nil, err
			}
			m.Exports = make([]Export, count)
			for i := uint32(0); i < count; i++ {
				name, err := reader.readString()
				if err != nil {
					return nil, err
				}
				funcIdx, err := reader.readUint32()
				if err != nil {
					return nil, err
				}
				m.Exports[i] = Export{
					Name:      name,
					FuncIndex: funcIdx,
				}
			}
			if reader.remaining() > 0 {
				return nil, errors.New("ir: unexpected trailing data in export section")
			}

		case SectionMemory:
			initialPages, err := reader.readUint32()
			if err != nil {
				return nil, err
			}
			maxPages, err := reader.readUint32()
			if err != nil {
				return nil, err
			}
			m.Memory = MemorySpec{
				InitialPages: initialPages,
				MaxPages:     maxPages,
			}
			if reader.remaining() > 0 {
				return nil, errors.New("ir: unexpected trailing data in memory section")
			}

		case SectionData:
			count, err := reader.readUint32()
			if err != nil {
				return nil, err
			}
			m.Data = make([]DataSegment, count)
			for i := uint32(0); i < count; i++ {
				segOffset, err := reader.readUint32()
				if err != nil {
					return nil, err
				}
				dataLen, err := reader.readUint32()
				if err != nil {
					return nil, err
				}
				dataBytes, err := reader.readBytes(int(dataLen))
				if err != nil {
					return nil, err
				}
				cp := make([]byte, len(dataBytes))
				copy(cp, dataBytes)
				m.Data[i] = DataSegment{
					Offset: segOffset,
					Data:   cp,
				}
			}
			if reader.remaining() > 0 {
				return nil, errors.New("ir: unexpected trailing data in data section")
			}

		case SectionMetadata:
			compilerVersion, err := reader.readString()
			if err != nil {
				return nil, err
			}
			targetPlatform, err := reader.readString()
			if err != nil {
				return nil, err
			}
			buildTime, err := reader.readInt64()
			if err != nil {
				return nil, err
			}
			m.Metadata = Metadata{
				CompilerVersion: compilerVersion,
				TargetPlatform:  targetPlatform,
				BuildTime:       buildTime,
			}
			if reader.remaining() > 0 {
				return nil, errors.New("ir: unexpected trailing data in metadata section")
			}

		default:
			return nil, fmt.Errorf("ir: unknown section id 0x%02x", sectionID)
		}
	}

	return m, nil
}
