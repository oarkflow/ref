package vm

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"

	"github.com/oarkflow/ref/aegis/ir"
)

var argSlicePool = sync.Pool{
	New: func() any {
		s := make([]uint64, 0, 8)
		return &s
	},
}

// enrichTrap wraps an error in a *Trap structure with contextual opcode and location information.
func (vm *VM) enrichTrap(err error, op ir.Opcode, funcIdx, pc uint32) error {
	var trap *Trap
	if errors.As(err, &trap) {
		if trap.FuncIdx == 0 && trap.PC == 0 && trap.Opcode == 0 {
			trap.FuncIdx = funcIdx
			trap.PC = pc
			trap.Opcode = op
		}
		return trap
	}
	return &Trap{
		Kind:    TrapTypeMismatch,
		FuncIdx: funcIdx,
		PC:      pc,
		Opcode:  op,
		Cause:   err,
		Message: err.Error(),
	}
}

// execute runs the VM interpretation loop until the call stack is empty or a trap occurs.
func (vm *VM) execute(ctx context.Context) error {
	var instructionCounter uint64

	for {
		// 1. Check context cancellation periodically (every 128 instructions and at entry)
		if instructionCounter&127 == 0 {
			if err := ctx.Err(); err != nil {
				frame := vm.callStack.Current()
				var currentFunc, currentPC uint32
				if frame != nil {
					currentFunc = frame.FuncIndex
					currentPC = frame.PC
				}
				return &Trap{
					Kind:    TrapCancelled,
					FuncIdx: currentFunc,
					PC:      currentPC,
					Cause:   err,
					Message: fmt.Sprintf("execution cancelled: %v", err),
				}
			}
		}

		frame := vm.callStack.Current()
		if frame == nil {
			return nil
		}

		funcIdx := frame.FuncIndex
		if int(funcIdx) >= len(vm.module.Functions) {
			return &Trap{
				Kind:    TrapInvalidFunction,
				FuncIdx: funcIdx,
				Message: fmt.Sprintf("invalid function index %d", funcIdx),
			}
		}

		fn := &vm.module.Functions[funcIdx]
		pc := frame.PC

		// Check if PC reached the end of the function body (implicit return)
		if int(pc) >= len(fn.Body) {
			poppedFrame, _ := vm.callStack.Pop()
			vm.locals.Release(poppedFrame.LocalsBase)
			if parent := vm.callStack.Current(); parent != nil {
				vm.locals.SetBase(parent.LocalsBase)
			} else {
				vm.locals.SetBase(0)
				return nil
			}
			continue
		}

		// 2. Consume fuel if bounded
		if vm.fuelLimit {
			if vm.fuel <= 0 {
				return &Trap{
					Kind:    TrapFuelExhausted,
					FuncIdx: funcIdx,
					PC:      pc,
					Opcode:  fn.Body[pc].Op,
					Message: "fuel exhausted",
				}
			}
			vm.fuel--
		}

		instructionCounter++

		// 3. Fetch instruction
		inst := fn.Body[pc]

		if vm.config.TraceExecution {
			fmt.Printf("[trace] func=%d pc=%d op=%s operands=%v stackLen=%d\n",
				funcIdx, pc, inst.Op, inst.Operands, vm.stack.Len())
		}

		// 4. Advance PC
		frame.PC++

		ctrlMap := vm.funcMeta[funcIdx]

		// 5. Opcode execution
		switch inst.Op {
		case ir.OpNop:
			// No operation

		case ir.OpBlock:
			endPC := ctrlMap.endPC[pc]
			frame.blockStack = append(frame.blockStack, blockEntry{
				kind:       ir.OpBlock,
				startPC:    pc,
				endPC:      endPC,
				stackDepth: vm.stack.Len(),
			})

		case ir.OpLoop:
			endPC := ctrlMap.endPC[pc]
			frame.blockStack = append(frame.blockStack, blockEntry{
				kind:       ir.OpLoop,
				startPC:    pc,
				endPC:      endPC,
				stackDepth: vm.stack.Len(),
			})

		case ir.OpIf:
			cond, err := vm.stack.PopU32()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			endPC := ctrlMap.endPC[pc]
			elsePC, hasElse := ctrlMap.elsePC[pc]

			if cond != 0 {
				frame.blockStack = append(frame.blockStack, blockEntry{
					kind:       ir.OpIf,
					startPC:    pc,
					endPC:      endPC,
					elsePC:     elsePC,
					stackDepth: vm.stack.Len(),
				})
			} else {
				if hasElse {
					frame.blockStack = append(frame.blockStack, blockEntry{
						kind:       ir.OpIf,
						startPC:    pc,
						endPC:      endPC,
						elsePC:     elsePC,
						stackDepth: vm.stack.Len(),
					})
					frame.PC = elsePC + 1
				} else {
					frame.PC = endPC + 1
				}
			}

		case ir.OpElse:
			if len(frame.blockStack) > 0 && frame.blockStack[len(frame.blockStack)-1].kind == ir.OpIf {
				entry := frame.blockStack[len(frame.blockStack)-1]
				frame.blockStack = frame.blockStack[:len(frame.blockStack)-1]
				frame.PC = entry.endPC + 1
			} else if endPC, ok := ctrlMap.endPC[pc]; ok {
				frame.PC = endPC + 1
			}

		case ir.OpEnd:
			if len(frame.blockStack) > 0 {
				frame.blockStack = frame.blockStack[:len(frame.blockStack)-1]
			}

		case ir.OpBr:
			depth := int(inst.Operands[0])
			if depth < 0 || depth >= len(frame.blockStack) {
				return &Trap{
					Kind:    TrapInvalidPC,
					FuncIdx: funcIdx,
					PC:      pc,
					Opcode:  inst.Op,
					Message: fmt.Sprintf("branch depth %d exceeds block stack depth %d", depth, len(frame.blockStack)),
				}
			}
			targetIdx := len(frame.blockStack) - 1 - depth
			target := frame.blockStack[targetIdx]

			if target.kind == ir.OpLoop {
				frame.blockStack = frame.blockStack[:targetIdx+1]
				frame.PC = target.startPC + 1
			} else {
				frame.blockStack = frame.blockStack[:targetIdx]
				frame.PC = target.endPC + 1
			}

		case ir.OpBrIf:
			cond, err := vm.stack.PopU32()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			if cond != 0 {
				depth := int(inst.Operands[0])
				if depth < 0 || depth >= len(frame.blockStack) {
					return &Trap{
						Kind:    TrapInvalidPC,
						FuncIdx: funcIdx,
						PC:      pc,
						Opcode:  inst.Op,
						Message: fmt.Sprintf("br_if depth %d exceeds block stack depth %d", depth, len(frame.blockStack)),
					}
				}
				targetIdx := len(frame.blockStack) - 1 - depth
				target := frame.blockStack[targetIdx]

				if target.kind == ir.OpLoop {
					frame.blockStack = frame.blockStack[:targetIdx+1]
					frame.PC = target.startPC + 1
				} else {
					frame.blockStack = frame.blockStack[:targetIdx]
					frame.PC = target.endPC + 1
				}
			}

		case ir.OpBrTable:
			index, err := vm.stack.PopU32()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			_ = index
			depth := int(inst.Operands[0])
			if depth < 0 || depth >= len(frame.blockStack) {
				return &Trap{
					Kind:    TrapInvalidPC,
					FuncIdx: funcIdx,
					PC:      pc,
					Opcode:  inst.Op,
					Message: fmt.Sprintf("br_table depth %d exceeds block stack depth %d", depth, len(frame.blockStack)),
				}
			}
			targetIdx := len(frame.blockStack) - 1 - depth
			target := frame.blockStack[targetIdx]
			if target.kind == ir.OpLoop {
				frame.blockStack = frame.blockStack[:targetIdx+1]
				frame.PC = target.startPC + 1
			} else {
				frame.blockStack = frame.blockStack[:targetIdx]
				frame.PC = target.endPC + 1
			}

		case ir.OpReturn:
			poppedFrame, _ := vm.callStack.Pop()
			vm.locals.Release(poppedFrame.LocalsBase)
			if parent := vm.callStack.Current(); parent != nil {
				vm.locals.SetBase(parent.LocalsBase)
			} else {
				vm.locals.SetBase(0)
				return nil
			}

		case ir.OpCall:
			calleeIdx := uint32(inst.Operands[0])
			if int(calleeIdx) >= len(vm.module.Functions) {
				return &Trap{
					Kind:    TrapInvalidFunction,
					FuncIdx: funcIdx,
					PC:      pc,
					Opcode:  inst.Op,
					Message: fmt.Sprintf("call target function %d out of bounds (%d functions)", calleeIdx, len(vm.module.Functions)),
				}
			}
			calleeFn := &vm.module.Functions[calleeIdx]
			if int(calleeFn.TypeIndex) >= len(vm.module.Types) {
				return &Trap{
					Kind:    TrapTypeMismatch,
					FuncIdx: funcIdx,
					PC:      pc,
					Opcode:  inst.Op,
					Message: fmt.Sprintf("function %d has invalid type index %d", calleeIdx, calleeFn.TypeIndex),
				}
			}
			calleeType := vm.module.Types[calleeFn.TypeIndex]

			paramPtr := argSlicePool.Get().(*[]uint64)
			paramVals := *paramPtr
			if cap(paramVals) < len(calleeType.Params) {
				paramVals = make([]uint64, len(calleeType.Params))
			} else {
				paramVals = paramVals[:len(calleeType.Params)]
			}

			for i := len(calleeType.Params) - 1; i >= 0; i-- {
				v, err := vm.stack.Pop()
				if err != nil {
					*paramPtr = paramVals[:0]
					argSlicePool.Put(paramPtr)
					return vm.enrichTrap(err, inst.Op, funcIdx, pc)
				}
				paramVals[i] = v
			}

			if vm.callStack.Depth() >= vm.config.MaxCallDepth {
				*paramPtr = paramVals[:0]
				argSlicePool.Put(paramPtr)
				return &Trap{
					Kind:    TrapCallStackOverflow,
					FuncIdx: funcIdx,
					PC:      pc,
					Opcode:  inst.Op,
					Message: fmt.Sprintf("call stack overflow reaching limit %d", vm.config.MaxCallDepth),
				}
			}

			totalLocals := len(calleeType.Params) + len(calleeFn.Locals)
			newBase := vm.locals.Allocate(totalLocals)
			for i, v := range paramVals {
				_ = vm.locals.SetAt(newBase+i, v)
			}
			*paramPtr = paramVals[:0]
			argSlicePool.Put(paramPtr)

			newFrame := Frame{
				FuncIndex:   calleeIdx,
				PC:          0,
				LocalsBase:  newBase,
				ReturnArity: len(calleeType.Results),
				blockStack:  make([]blockEntry, 0, 8),
			}
			if err := vm.callStack.Push(newFrame); err != nil {
				return err
			}

		case ir.OpCallImport:
			importIdx := uint32(inst.Operands[0])
			if int(importIdx) >= len(vm.module.Imports) {
				return &Trap{
					Kind:    TrapUnresolvedImport,
					FuncIdx: funcIdx,
					PC:      pc,
					Opcode:  inst.Op,
					Message: fmt.Sprintf("import index %d out of bounds (%d imports)", importIdx, len(vm.module.Imports)),
				}
			}
			imp := vm.module.Imports[importIdx]
			key := imp.Module + "." + imp.Name
			hf, ok := vm.hostFuncs[key]
			if !ok {
				return &Trap{
					Kind:    TrapUnresolvedImport,
					FuncIdx: funcIdx,
					PC:      pc,
					Opcode:  inst.Op,
					Message: fmt.Sprintf("unresolved import: %s", key),
				}
			}
			if int(imp.Type) >= len(vm.module.Types) {
				return &Trap{
					Kind:    TrapTypeMismatch,
					FuncIdx: funcIdx,
					PC:      pc,
					Opcode:  inst.Op,
					Message: fmt.Sprintf("import %s has invalid type index %d", key, imp.Type),
				}
			}
			impType := vm.module.Types[imp.Type]

			args := make([]uint64, len(impType.Params))
			for i := len(impType.Params) - 1; i >= 0; i-- {
				v, err := vm.stack.Pop()
				if err != nil {
					return vm.enrichTrap(err, inst.Op, funcIdx, pc)
				}
				args[i] = v
			}

			results, err := hf.Func(ctx, vm, args)
			if err != nil {
				return &Trap{
					Kind:    TrapHostError,
					FuncIdx: funcIdx,
					PC:      pc,
					Opcode:  inst.Op,
					Cause:   err,
					Message: fmt.Sprintf("host function %s failed: %v", key, err),
				}
			}
			for _, res := range results {
				if err := vm.stack.Push(res); err != nil {
					return vm.enrichTrap(err, inst.Op, funcIdx, pc)
				}
			}

		case ir.OpLocalGet:
			idx := uint32(inst.Operands[0])
			v, err := vm.locals.Get(idx)
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			if err := vm.stack.Push(v); err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}

		case ir.OpLocalSet:
			idx := uint32(inst.Operands[0])
			v, err := vm.stack.Pop()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			if err := vm.locals.Set(idx, v); err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}

		case ir.OpLocalTee:
			idx := uint32(inst.Operands[0])
			v, err := vm.stack.Peek()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			if err := vm.locals.Set(idx, v); err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}

		case ir.OpLoad32:
			base, err := vm.stack.PopU32()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			addr := base + uint32(inst.Operands[0])
			val, err := vm.memory.LoadU32(addr)
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			if err := vm.stack.PushU32(val); err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}

		case ir.OpLoad64:
			base, err := vm.stack.PopU32()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			addr := base + uint32(inst.Operands[0])
			val, err := vm.memory.LoadU64(addr)
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			if err := vm.stack.PushU64(val); err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}

		case ir.OpStore32:
			val, err := vm.stack.PopU32()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			base, err := vm.stack.PopU32()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			addr := base + uint32(inst.Operands[0])
			if err := vm.memory.StoreU32(addr, val); err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}

		case ir.OpStore64:
			val, err := vm.stack.PopU64()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			base, err := vm.stack.PopU32()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			addr := base + uint32(inst.Operands[0])
			if err := vm.memory.StoreU64(addr, val); err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}

		case ir.OpLoadF32:
			base, err := vm.stack.PopU32()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			addr := base + uint32(inst.Operands[0])
			val, err := vm.memory.LoadF32(addr)
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			if err := vm.stack.PushF32(val); err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}

		case ir.OpLoadF64:
			base, err := vm.stack.PopU32()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			addr := base + uint32(inst.Operands[0])
			val, err := vm.memory.LoadF64(addr)
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			if err := vm.stack.PushF64(val); err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}

		case ir.OpStoreF32:
			val, err := vm.stack.PopF32()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			base, err := vm.stack.PopU32()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			addr := base + uint32(inst.Operands[0])
			if err := vm.memory.StoreF32(addr, val); err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}

		case ir.OpStoreF64:
			val, err := vm.stack.PopF64()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			base, err := vm.stack.PopU32()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			addr := base + uint32(inst.Operands[0])
			if err := vm.memory.StoreF64(addr, val); err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}

		case ir.OpMemorySize:
			if err := vm.stack.PushU32(vm.memory.Size()); err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}

		case ir.OpMemoryGrow:
			delta, err := vm.stack.PopU32()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			oldSize, err := vm.memory.Grow(delta)
			if err != nil {
				if pushErr := vm.stack.PushU32(^uint32(0)); pushErr != nil {
					return pushErr
				}
			} else {
				if pushErr := vm.stack.PushU32(oldSize); pushErr != nil {
					return pushErr
				}
			}

		case ir.OpI32Const:
			if err := vm.stack.PushU32(uint32(inst.Operands[0])); err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}

		case ir.OpI64Const:
			if err := vm.stack.PushU64(inst.Operands[0]); err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}

		case ir.OpF32Const:
			if err := vm.stack.PushU32(uint32(inst.Operands[0])); err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}

		case ir.OpF64Const:
			if err := vm.stack.PushU64(inst.Operands[0]); err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}

		// i32 arithmetic
		case ir.OpI32Add:
			b, err := vm.stack.PopU32()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			a, err := vm.stack.PopU32()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			if err := vm.stack.PushU32(a + b); err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}

		case ir.OpI32Sub:
			b, err := vm.stack.PopU32()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			a, err := vm.stack.PopU32()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			if err := vm.stack.PushU32(a - b); err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}

		case ir.OpI32Mul:
			b, err := vm.stack.PopU32()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			a, err := vm.stack.PopU32()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			if err := vm.stack.PushU32(a * b); err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}

		case ir.OpI32DivS:
			b, err := vm.stack.PopI32()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			a, err := vm.stack.PopI32()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			if b == 0 {
				return &Trap{
					Kind:    TrapDivisionByZero,
					FuncIdx: funcIdx,
					PC:      pc,
					Opcode:  inst.Op,
					Message: "integer divide by zero",
				}
			}
			if a == math.MinInt32 && b == -1 {
				return &Trap{
					Kind:    TrapIntegerOverflow,
					FuncIdx: funcIdx,
					PC:      pc,
					Opcode:  inst.Op,
					Message: "integer overflow in i32.div_s",
				}
			}
			if err := vm.stack.PushI32(a / b); err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}

		case ir.OpI32DivU:
			b, err := vm.stack.PopU32()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			a, err := vm.stack.PopU32()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			if b == 0 {
				return &Trap{
					Kind:    TrapDivisionByZero,
					FuncIdx: funcIdx,
					PC:      pc,
					Opcode:  inst.Op,
					Message: "integer divide by zero",
				}
			}
			if err := vm.stack.PushU32(a / b); err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}

		case ir.OpI32RemS:
			b, err := vm.stack.PopI32()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			a, err := vm.stack.PopI32()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			if b == 0 {
				return &Trap{
					Kind:    TrapDivisionByZero,
					FuncIdx: funcIdx,
					PC:      pc,
					Opcode:  inst.Op,
					Message: "integer remainder by zero",
				}
			}
			var res int32
			if b != -1 {
				res = a % b
			}
			if err := vm.stack.PushI32(res); err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}

		case ir.OpI32RemU:
			b, err := vm.stack.PopU32()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			a, err := vm.stack.PopU32()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			if b == 0 {
				return &Trap{
					Kind:    TrapDivisionByZero,
					FuncIdx: funcIdx,
					PC:      pc,
					Opcode:  inst.Op,
					Message: "integer remainder by zero",
				}
			}
			if err := vm.stack.PushU32(a % b); err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}

		case ir.OpI32And:
			b, err := vm.stack.PopU32()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			a, err := vm.stack.PopU32()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			if err := vm.stack.PushU32(a & b); err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}

		case ir.OpI32Or:
			b, err := vm.stack.PopU32()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			a, err := vm.stack.PopU32()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			if err := vm.stack.PushU32(a | b); err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}

		case ir.OpI32Xor:
			b, err := vm.stack.PopU32()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			a, err := vm.stack.PopU32()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			if err := vm.stack.PushU32(a ^ b); err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}

		case ir.OpI32Shl:
			b, err := vm.stack.PopU32()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			a, err := vm.stack.PopU32()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			if err := vm.stack.PushU32(a << (b % 32)); err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}

		case ir.OpI32ShrS:
			b, err := vm.stack.PopU32()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			a, err := vm.stack.PopI32()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			if err := vm.stack.PushI32(a >> (b % 32)); err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}

		case ir.OpI32ShrU:
			b, err := vm.stack.PopU32()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			a, err := vm.stack.PopU32()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			if err := vm.stack.PushU32(a >> (b % 32)); err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}

		// i32 comparison
		case ir.OpI32Eqz:
			a, err := vm.stack.PopU32()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			var res uint32
			if a == 0 {
				res = 1
			}
			if err := vm.stack.PushU32(res); err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}

		case ir.OpI32Eq:
			b, err := vm.stack.PopU32()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			a, err := vm.stack.PopU32()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			var res uint32
			if a == b {
				res = 1
			}
			if err := vm.stack.PushU32(res); err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}

		case ir.OpI32Ne:
			b, err := vm.stack.PopU32()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			a, err := vm.stack.PopU32()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			var res uint32
			if a != b {
				res = 1
			}
			if err := vm.stack.PushU32(res); err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}

		case ir.OpI32LtS:
			b, err := vm.stack.PopI32()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			a, err := vm.stack.PopI32()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			var res uint32
			if a < b {
				res = 1
			}
			if err := vm.stack.PushU32(res); err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}

		case ir.OpI32LtU:
			b, err := vm.stack.PopU32()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			a, err := vm.stack.PopU32()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			var res uint32
			if a < b {
				res = 1
			}
			if err := vm.stack.PushU32(res); err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}

		case ir.OpI32GtS:
			b, err := vm.stack.PopI32()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			a, err := vm.stack.PopI32()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			var res uint32
			if a > b {
				res = 1
			}
			if err := vm.stack.PushU32(res); err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}

		case ir.OpI32GtU:
			b, err := vm.stack.PopU32()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			a, err := vm.stack.PopU32()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			var res uint32
			if a > b {
				res = 1
			}
			if err := vm.stack.PushU32(res); err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}

		case ir.OpI32LeS:
			b, err := vm.stack.PopI32()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			a, err := vm.stack.PopI32()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			var res uint32
			if a <= b {
				res = 1
			}
			if err := vm.stack.PushU32(res); err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}

		case ir.OpI32LeU:
			b, err := vm.stack.PopU32()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			a, err := vm.stack.PopU32()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			var res uint32
			if a <= b {
				res = 1
			}
			if err := vm.stack.PushU32(res); err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}

		case ir.OpI32GeS:
			b, err := vm.stack.PopI32()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			a, err := vm.stack.PopI32()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			var res uint32
			if a >= b {
				res = 1
			}
			if err := vm.stack.PushU32(res); err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}

		case ir.OpI32GeU:
			b, err := vm.stack.PopU32()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			a, err := vm.stack.PopU32()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			var res uint32
			if a >= b {
				res = 1
			}
			if err := vm.stack.PushU32(res); err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}

		// i64 arithmetic
		case ir.OpI64Add:
			b, err := vm.stack.PopU64()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			a, err := vm.stack.PopU64()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			if err := vm.stack.PushU64(a + b); err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}

		case ir.OpI64Sub:
			b, err := vm.stack.PopU64()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			a, err := vm.stack.PopU64()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			if err := vm.stack.PushU64(a - b); err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}

		case ir.OpI64Mul:
			b, err := vm.stack.PopU64()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			a, err := vm.stack.PopU64()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			if err := vm.stack.PushU64(a * b); err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}

		case ir.OpI64DivS:
			b, err := vm.stack.PopI64()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			a, err := vm.stack.PopI64()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			if b == 0 {
				return &Trap{
					Kind:    TrapDivisionByZero,
					FuncIdx: funcIdx,
					PC:      pc,
					Opcode:  inst.Op,
					Message: "integer divide by zero",
				}
			}
			if a == math.MinInt64 && b == -1 {
				return &Trap{
					Kind:    TrapIntegerOverflow,
					FuncIdx: funcIdx,
					PC:      pc,
					Opcode:  inst.Op,
					Message: "integer overflow in i64.div_s",
				}
			}
			if err := vm.stack.PushI64(a / b); err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}

		case ir.OpI64DivU:
			b, err := vm.stack.PopU64()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			a, err := vm.stack.PopU64()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			if b == 0 {
				return &Trap{
					Kind:    TrapDivisionByZero,
					FuncIdx: funcIdx,
					PC:      pc,
					Opcode:  inst.Op,
					Message: "integer divide by zero",
				}
			}
			if err := vm.stack.PushU64(a / b); err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}

		case ir.OpI64RemS:
			b, err := vm.stack.PopI64()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			a, err := vm.stack.PopI64()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			if b == 0 {
				return &Trap{
					Kind:    TrapDivisionByZero,
					FuncIdx: funcIdx,
					PC:      pc,
					Opcode:  inst.Op,
					Message: "integer remainder by zero",
				}
			}
			var res int64
			if b != -1 {
				res = a % b
			}
			if err := vm.stack.PushI64(res); err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}

		case ir.OpI64RemU:
			b, err := vm.stack.PopU64()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			a, err := vm.stack.PopU64()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			if b == 0 {
				return &Trap{
					Kind:    TrapDivisionByZero,
					FuncIdx: funcIdx,
					PC:      pc,
					Opcode:  inst.Op,
					Message: "integer remainder by zero",
				}
			}
			if err := vm.stack.PushU64(a % b); err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}

		case ir.OpI64And:
			b, err := vm.stack.PopU64()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			a, err := vm.stack.PopU64()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			if err := vm.stack.PushU64(a & b); err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}

		case ir.OpI64Or:
			b, err := vm.stack.PopU64()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			a, err := vm.stack.PopU64()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			if err := vm.stack.PushU64(a | b); err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}

		case ir.OpI64Xor:
			b, err := vm.stack.PopU64()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			a, err := vm.stack.PopU64()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			if err := vm.stack.PushU64(a ^ b); err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}

		case ir.OpI64Shl:
			b, err := vm.stack.PopU64()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			a, err := vm.stack.PopU64()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			if err := vm.stack.PushU64(a << (b % 64)); err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}

		case ir.OpI64ShrS:
			b, err := vm.stack.PopU64()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			a, err := vm.stack.PopI64()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			if err := vm.stack.PushI64(a >> (b % 64)); err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}

		case ir.OpI64ShrU:
			b, err := vm.stack.PopU64()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			a, err := vm.stack.PopU64()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			if err := vm.stack.PushU64(a >> (b % 64)); err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}

		// i64 comparison
		case ir.OpI64Eqz:
			a, err := vm.stack.PopU64()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			var res uint32
			if a == 0 {
				res = 1
			}
			if err := vm.stack.PushU32(res); err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}

		case ir.OpI64Eq:
			b, err := vm.stack.PopU64()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			a, err := vm.stack.PopU64()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			var res uint32
			if a == b {
				res = 1
			}
			if err := vm.stack.PushU32(res); err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}

		case ir.OpI64Ne:
			b, err := vm.stack.PopU64()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			a, err := vm.stack.PopU64()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			var res uint32
			if a != b {
				res = 1
			}
			if err := vm.stack.PushU32(res); err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}

		case ir.OpI64LtS:
			b, err := vm.stack.PopI64()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			a, err := vm.stack.PopI64()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			var res uint32
			if a < b {
				res = 1
			}
			if err := vm.stack.PushU32(res); err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}

		case ir.OpI64LtU:
			b, err := vm.stack.PopU64()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			a, err := vm.stack.PopU64()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			var res uint32
			if a < b {
				res = 1
			}
			if err := vm.stack.PushU32(res); err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}

		case ir.OpI64GtS:
			b, err := vm.stack.PopI64()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			a, err := vm.stack.PopI64()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			var res uint32
			if a > b {
				res = 1
			}
			if err := vm.stack.PushU32(res); err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}

		case ir.OpI64GtU:
			b, err := vm.stack.PopU64()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			a, err := vm.stack.PopU64()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			var res uint32
			if a > b {
				res = 1
			}
			if err := vm.stack.PushU32(res); err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}

		case ir.OpI64LeS:
			b, err := vm.stack.PopI64()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			a, err := vm.stack.PopI64()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			var res uint32
			if a <= b {
				res = 1
			}
			if err := vm.stack.PushU32(res); err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}

		case ir.OpI64LeU:
			b, err := vm.stack.PopU64()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			a, err := vm.stack.PopU64()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			var res uint32
			if a <= b {
				res = 1
			}
			if err := vm.stack.PushU32(res); err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}

		case ir.OpI64GeS:
			b, err := vm.stack.PopI64()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			a, err := vm.stack.PopI64()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			var res uint32
			if a >= b {
				res = 1
			}
			if err := vm.stack.PushU32(res); err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}

		case ir.OpI64GeU:
			b, err := vm.stack.PopU64()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			a, err := vm.stack.PopU64()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			var res uint32
			if a >= b {
				res = 1
			}
			if err := vm.stack.PushU32(res); err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}

		// f64 arithmetic
		case ir.OpF64Add:
			b, err := vm.stack.PopF64()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			a, err := vm.stack.PopF64()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			if err := vm.stack.PushF64(a + b); err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}

		case ir.OpF64Sub:
			b, err := vm.stack.PopF64()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			a, err := vm.stack.PopF64()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			if err := vm.stack.PushF64(a - b); err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}

		case ir.OpF64Mul:
			b, err := vm.stack.PopF64()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			a, err := vm.stack.PopF64()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			if err := vm.stack.PushF64(a * b); err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}

		case ir.OpF64Div:
			b, err := vm.stack.PopF64()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			a, err := vm.stack.PopF64()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			if err := vm.stack.PushF64(a / b); err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}

		// f64 comparison
		case ir.OpF64Eq:
			b, err := vm.stack.PopF64()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			a, err := vm.stack.PopF64()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			var res uint32
			if a == b {
				res = 1
			}
			if err := vm.stack.PushU32(res); err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}

		case ir.OpF64Ne:
			b, err := vm.stack.PopF64()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			a, err := vm.stack.PopF64()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			var res uint32
			if a != b {
				res = 1
			}
			if err := vm.stack.PushU32(res); err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}

		case ir.OpF64Lt:
			b, err := vm.stack.PopF64()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			a, err := vm.stack.PopF64()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			var res uint32
			if a < b {
				res = 1
			}
			if err := vm.stack.PushU32(res); err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}

		case ir.OpF64Gt:
			b, err := vm.stack.PopF64()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			a, err := vm.stack.PopF64()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			var res uint32
			if a > b {
				res = 1
			}
			if err := vm.stack.PushU32(res); err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}

		case ir.OpF64Le:
			b, err := vm.stack.PopF64()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			a, err := vm.stack.PopF64()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			var res uint32
			if a <= b {
				res = 1
			}
			if err := vm.stack.PushU32(res); err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}

		case ir.OpF64Ge:
			b, err := vm.stack.PopF64()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			a, err := vm.stack.PopF64()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			var res uint32
			if a >= b {
				res = 1
			}
			if err := vm.stack.PushU32(res); err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}

		// Conversions
		case ir.OpI32WrapI64:
			v, err := vm.stack.PopU64()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			if err := vm.stack.PushU32(uint32(v)); err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}

		case ir.OpI64ExtendI32S:
			v, err := vm.stack.PopI32()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			if err := vm.stack.PushI64(int64(v)); err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}

		case ir.OpI64ExtendI32U:
			v, err := vm.stack.PopU32()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			if err := vm.stack.PushU64(uint64(v)); err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}

		case ir.OpF64ConvertI32S:
			v, err := vm.stack.PopI32()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			if err := vm.stack.PushF64(float64(v)); err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}

		case ir.OpF64ConvertI64S:
			v, err := vm.stack.PopI64()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			if err := vm.stack.PushF64(float64(v)); err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}

		case ir.OpI32TruncF64S:
			v, err := vm.stack.PopF64()
			if err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}
			if math.IsNaN(v) || math.IsInf(v, 0) || v < float64(math.MinInt32) || v > float64(math.MaxInt32) {
				return &Trap{
					Kind:    TrapIntegerOverflow,
					FuncIdx: funcIdx,
					PC:      pc,
					Opcode:  inst.Op,
					Message: "float truncate to i32 overflow or invalid",
				}
			}
			if err := vm.stack.PushI32(int32(v)); err != nil {
				return vm.enrichTrap(err, inst.Op, funcIdx, pc)
			}

		case ir.OpTrap:
			return &Trap{
				Kind:    TrapExplicit,
				FuncIdx: funcIdx,
				PC:      pc,
				Opcode:  inst.Op,
				Message: "explicit trap instruction",
			}

		default:
			return &Trap{
				Kind:    TrapInvalidOpcode,
				FuncIdx: funcIdx,
				PC:      pc,
				Opcode:  inst.Op,
				Message: fmt.Sprintf("unsupported opcode: %s (0x%04X)", inst.Op, uint16(inst.Op)),
			}
		}
	}
}
