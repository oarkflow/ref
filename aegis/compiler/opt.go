package compiler

import "github.com/oarkflow/ref/aegis/ir"

// optimize applies bytecode optimizations to all functions in the module
// when OptLevel > 0.
func optimize(m *ir.Module, optLevel int) {
	if m == nil || optLevel <= 0 {
		return
	}

	for i := range m.Functions {
		optimizeFunction(&m.Functions[i], optLevel)
	}
}

// optimizeFunction applies peephole optimizations and constant folding to a single function.
func optimizeFunction(fn *ir.Function, optLevel int) {
	if len(fn.Body) == 0 {
		return
	}

	// 1. Peephole constant folding
	body := fn.Body
	changed := true
	for changed && len(body) >= 3 {
		changed = false
		newBody := make([]ir.Instruction, 0, len(body))
		for i := 0; i < len(body); i++ {
			if i+2 < len(body) && body[i].Op == ir.OpI32Const && body[i+1].Op == ir.OpI32Const {
				c1 := uint32(body[i].Operands[0])
				c2 := uint32(body[i+1].Operands[0])
				folded := false
				var res uint32

				switch body[i+2].Op {
				case ir.OpI32Add:
					res = c1 + c2
					folded = true
				case ir.OpI32Sub:
					res = c1 - c2
					folded = true
				case ir.OpI32Mul:
					res = c1 * c2
					folded = true
				case ir.OpI32And:
					res = c1 & c2
					folded = true
				case ir.OpI32Or:
					res = c1 | c2
					folded = true
				case ir.OpI32Xor:
					res = c1 ^ c2
					folded = true
				}

				if folded {
					newBody = append(newBody, ir.Instruction{
						Op:       ir.OpI32Const,
						Operands: [3]uint64{uint64(res), 0, 0},
					})
					i += 2
					changed = true
					continue
				}
			}
			newBody = append(newBody, body[i])
		}
		body = newBody
	}

	// 2. Dead code elimination after unconditional return
	// Remove instructions between OpReturn and the next OpEnd / OpElse
	if optLevel >= 1 && len(body) > 1 {
		cleaned := make([]ir.Instruction, 0, len(body))
		dead := false
		for _, inst := range body {
			if dead {
				if inst.Op == ir.OpEnd || inst.Op == ir.OpElse {
					dead = false
					cleaned = append(cleaned, inst)
				}
				// Skip dead instructions
				continue
			}

			cleaned = append(cleaned, inst)
			if inst.Op == ir.OpReturn {
				dead = true
			}
		}
		body = cleaned
	}

	fn.Body = body
}
