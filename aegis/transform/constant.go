// Package transform provides program transformation and protection passes for Aegis IR modules.
package transform

import (
	"errors"
	"math/rand"
	"time"

	"github.com/oarkflow/ref/aegis/ir"
)

// ConstantEncoding implements a transformation pass that obfuscates literal constants
// in Aegis IR function bodies by substituting single constant instructions with computed
// algebraic or bitwise sequences.
type ConstantEncoding struct {
	seed int64
}

// NewConstantEncoding creates a new ConstantEncoding pass. An optional random seed
// can be provided for deterministic compilation testing.
func NewConstantEncoding(seed ...int64) *ConstantEncoding {
	s := time.Now().UnixNano()
	if len(seed) > 0 && seed[0] != 0 {
		s = seed[0]
	}
	return &ConstantEncoding{seed: s}
}

// Name returns the identifier of this transformation pass.
func (c *ConstantEncoding) Name() string {
	return "ConstantEncoding"
}

// Transform scans all functions in the module and replaces literal i32 and i64 constants
// with equivalent computed expressions such as (C ^ K) ^ K or (C - K) + K.
func (c *ConstantEncoding) Transform(m *ir.Module) (*ir.Module, Metrics, error) {
	if m == nil {
		return nil, Metrics{}, errors.New("transform: module is nil")
	}

	rng := rand.New(rand.NewSource(c.seed))
	metrics := Metrics{PassName: c.Name()}

	for funcIdx := range m.Functions {
		fn := &m.Functions[funcIdx]
		if len(fn.Body) == 0 {
			continue
		}

		var (
			newBody  = make([]ir.Instruction, 0, len(fn.Body)*2)
			modified = false
		)

		for _, inst := range fn.Body {
			switch inst.Op {
			case ir.OpI32Const:
				origVal := uint32(inst.Operands[0])
				key := rng.Uint32() | 0x01
				mode := rng.Intn(3)

				switch mode {
				case 0:
					// (C ^ K) ^ K
					part1 := origVal ^ key
					newBody = append(newBody,
						ir.Instruction{Op: ir.OpI32Const, Operands: [3]uint64{uint64(part1), 0, 0}},
						ir.Instruction{Op: ir.OpI32Const, Operands: [3]uint64{uint64(key), 0, 0}},
						ir.Instruction{Op: ir.OpI32Xor},
					)
				case 1:
					// (C - K) + K
					part1 := origVal - key
					newBody = append(newBody,
						ir.Instruction{Op: ir.OpI32Const, Operands: [3]uint64{uint64(part1), 0, 0}},
						ir.Instruction{Op: ir.OpI32Const, Operands: [3]uint64{uint64(key), 0, 0}},
						ir.Instruction{Op: ir.OpI32Add},
					)
				default:
					// (C + K) - K
					part1 := origVal + key
					newBody = append(newBody,
						ir.Instruction{Op: ir.OpI32Const, Operands: [3]uint64{uint64(part1), 0, 0}},
						ir.Instruction{Op: ir.OpI32Const, Operands: [3]uint64{uint64(key), 0, 0}},
						ir.Instruction{Op: ir.OpI32Sub},
					)
				}

				metrics.InstructionsRemoved++
				metrics.InstructionsAdded += 3
				modified = true

			case ir.OpI64Const:
				origVal := inst.Operands[0]
				key := rng.Uint64() | 0x01
				mode := rng.Intn(3)

				switch mode {
				case 0:
					// (C ^ K) ^ K
					part1 := origVal ^ key
					newBody = append(newBody,
						ir.Instruction{Op: ir.OpI64Const, Operands: [3]uint64{part1, 0, 0}},
						ir.Instruction{Op: ir.OpI64Const, Operands: [3]uint64{key, 0, 0}},
						ir.Instruction{Op: ir.OpI64Xor},
					)
				case 1:
					// (C - K) + K
					part1 := origVal - key
					newBody = append(newBody,
						ir.Instruction{Op: ir.OpI64Const, Operands: [3]uint64{part1, 0, 0}},
						ir.Instruction{Op: ir.OpI64Const, Operands: [3]uint64{key, 0, 0}},
						ir.Instruction{Op: ir.OpI64Add},
					)
				default:
					// (C + K) - K
					part1 := origVal + key
					newBody = append(newBody,
						ir.Instruction{Op: ir.OpI64Const, Operands: [3]uint64{part1, 0, 0}},
						ir.Instruction{Op: ir.OpI64Const, Operands: [3]uint64{key, 0, 0}},
						ir.Instruction{Op: ir.OpI64Sub},
					)
				}

				metrics.InstructionsRemoved++
				metrics.InstructionsAdded += 3
				modified = true

			default:
				newBody = append(newBody, inst)
			}
		}

		if modified {
			fn.Body = newBody
			metrics.FunctionsModified++
		}
	}

	return m, metrics, nil
}
