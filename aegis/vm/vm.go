package vm

import (
	"context"
	"fmt"

	"github.com/oarkflow/ref/aegis/ir"
)

// funcControlMap holds precomputed control flow branch targets for an IR function.
type funcControlMap struct {
	endPC  map[uint32]uint32
	elsePC map[uint32]uint32
}

// VM represents an Aegis Virtual Machine execution engine instance.
type VM struct {
	module    *ir.Module
	memory    *Memory
	stack     *Stack
	callStack *CallStack
	locals    *Locals
	hostFuncs map[string]HostFunc // key: "module.name"
	fuel      int64
	fuelLimit bool
	config    Config
	funcMeta  map[uint32]*funcControlMap
}

// precomputeControlFlow analyzes a function's instructions to build jump tables for blocks, loops, and branches.
func precomputeControlFlow(fn *ir.Function) (*funcControlMap, error) {
	cm := &funcControlMap{
		endPC:  make(map[uint32]uint32),
		elsePC: make(map[uint32]uint32),
	}
	type blockTarget struct {
		op ir.Opcode
		pc uint32
	}
	var stack []blockTarget

	for pc := 0; pc < len(fn.Body); pc++ {
		inst := fn.Body[pc]
		switch inst.Op {
		case ir.OpBlock, ir.OpLoop, ir.OpIf:
			stack = append(stack, blockTarget{op: inst.Op, pc: uint32(pc)})
		case ir.OpElse:
			if len(stack) == 0 || stack[len(stack)-1].op != ir.OpIf {
				return nil, fmt.Errorf("vm: else without matching if at pc %d", pc)
			}
			top := stack[len(stack)-1]
			cm.elsePC[top.pc] = uint32(pc)
		case ir.OpEnd:
			if len(stack) > 0 {
				top := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				cm.endPC[top.pc] = uint32(pc)
				if top.op == ir.OpIf {
					if elsePC, hasElse := cm.elsePC[top.pc]; hasElse {
						cm.endPC[elsePC] = uint32(pc)
					}
				}
			}
		}
	}

	// For any unclosed blocks, default their endPC to the end of the instruction stream
	for len(stack) > 0 {
		top := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		cm.endPC[top.pc] = uint32(len(fn.Body))
		if top.op == ir.OpIf {
			if elsePC, hasElse := cm.elsePC[top.pc]; hasElse {
				cm.endPC[elsePC] = uint32(len(fn.Body))
			}
		}
	}

	return cm, nil
}

// New creates and initializes a new Aegis Virtual Machine executing the provided IR module.
func New(module *ir.Module, config Config, hostFuncs ...HostFunc) (*VM, error) {
	// 1. Validate module structure and semantics
	if err := ir.Validate(module); err != nil {
		return nil, fmt.Errorf("vm: module validation failed: %w", err)
	}

	// 2. Set default config bounds if zero or negative
	if config.MaxStackDepth <= 0 {
		config.MaxStackDepth = 65536
	}
	if config.MaxCallDepth <= 0 {
		config.MaxCallDepth = 1024
	}
	if config.MaxMemoryPages == 0 {
		config.MaxMemoryPages = 256
	}

	// 3. Initialize linear memory
	initialPages := module.Memory.InitialPages
	maxPages := module.Memory.MaxPages
	if maxPages == 0 || maxPages > config.MaxMemoryPages {
		maxPages = config.MaxMemoryPages
	}
	if maxPages < initialPages {
		maxPages = initialPages
	}

	memory, err := NewMemory(initialPages, maxPages)
	if err != nil {
		return nil, fmt.Errorf("vm: failed to initialize memory: %w", err)
	}

	// 4. Initialize static data segments
	for segIdx, seg := range module.Data {
		if err := memory.WriteBytes(seg.Offset, seg.Data); err != nil {
			return nil, fmt.Errorf("vm: failed to initialize data segment %d: %w", segIdx, err)
		}
	}

	// 5. Index and verify host functions against imports
	hfMap := make(map[string]HostFunc, len(hostFuncs))
	for _, hf := range hostFuncs {
		key := hf.Module + "." + hf.Name
		hfMap[key] = hf
	}

	for importIdx, imp := range module.Imports {
		key := imp.Module + "." + imp.Name
		if _, ok := hfMap[key]; !ok {
			return nil, &Trap{
				Kind:    TrapUnresolvedImport,
				Message: fmt.Sprintf("unresolved import: %s (import %d)", key, importIdx),
			}
		}
	}

	// 6. Precompute control flow maps for each internal function
	funcMeta := make(map[uint32]*funcControlMap, len(module.Functions))
	for funcIdx := range module.Functions {
		cm, err := precomputeControlFlow(&module.Functions[funcIdx])
		if err != nil {
			return nil, fmt.Errorf("vm: failed to analyze function %d control flow: %w", funcIdx, err)
		}
		funcMeta[uint32(funcIdx)] = cm
	}

	// 7. Instantiate VM
	vm := &VM{
		module:    module,
		memory:    memory,
		stack:     NewStack(config.MaxStackDepth),
		callStack: NewCallStack(config.MaxCallDepth),
		locals:    NewLocals(),
		hostFuncs: hfMap,
		fuel:      config.InitialFuel,
		fuelLimit: config.InitialFuel > 0,
		config:    config,
		funcMeta:  funcMeta,
	}

	return vm, nil
}

// Call invokes an exported function by name with the given arguments, returning its results.
func (vm *VM) Call(ctx context.Context, name string, args ...uint64) ([]uint64, error) {
	// Find export entry
	var exportFound bool
	var export ir.Export
	for _, exp := range vm.module.Exports {
		if exp.Name == name {
			export = exp
			exportFound = true
			break
		}
	}
	if !exportFound {
		return nil, fmt.Errorf("vm: export %q not found", name)
	}

	// Check if the export refers to an import or internal function
	numFuncs := uint32(len(vm.module.Functions))
	if export.FuncIndex >= numFuncs {
		// Unified index pointing to an import
		importIdx := export.FuncIndex - numFuncs
		if int(importIdx) >= len(vm.module.Imports) {
			return nil, &Trap{
				Kind:    TrapInvalidFunction,
				Message: fmt.Sprintf("vm: export %q references invalid function index %d", name, export.FuncIndex),
			}
		}
		imp := vm.module.Imports[importIdx]
		key := imp.Module + "." + imp.Name
		hf, ok := vm.hostFuncs[key]
		if !ok {
			return nil, &Trap{
				Kind:    TrapUnresolvedImport,
				Message: fmt.Sprintf("vm: unresolved import: %s", key),
			}
		}
		if int(imp.Type) >= len(vm.module.Types) {
			return nil, &Trap{
				Kind:    TrapTypeMismatch,
				Message: fmt.Sprintf("vm: import %s has invalid type index %d", key, imp.Type),
			}
		}
		sig := vm.module.Types[imp.Type]
		if len(args) != len(sig.Params) {
			return nil, &Trap{
				Kind:    TrapTypeMismatch,
				Message: fmt.Sprintf("vm: parameter count mismatch for %s: expected %d, got %d", key, len(sig.Params), len(args)),
			}
		}
		return hf.Func(ctx, vm, args)
	}

	// Internal function dispatch
	fn := &vm.module.Functions[export.FuncIndex]
	if int(fn.TypeIndex) >= len(vm.module.Types) {
		return nil, &Trap{
			Kind:    TrapTypeMismatch,
			Message: fmt.Sprintf("vm: function %d has invalid type index %d", export.FuncIndex, fn.TypeIndex),
		}
	}
	fnType := vm.module.Types[fn.TypeIndex]

	// Validate argument count
	if len(args) != len(fnType.Params) {
		return nil, &Trap{
			Kind:    TrapTypeMismatch,
			Message: fmt.Sprintf("vm: parameter count mismatch for export %q: expected %d, got %d", name, len(fnType.Params), len(args)),
		}
	}

	// Allocate locals for parameters + declared local variables
	totalLocals := len(fnType.Params) + len(fn.Locals)
	base := vm.locals.Allocate(totalLocals)
	for i, arg := range args {
		if err := vm.locals.SetAt(base+i, arg); err != nil {
			return nil, err
		}
	}

	// Push initial activation frame
	initialFrame := Frame{
		FuncIndex:   export.FuncIndex,
		PC:          0,
		LocalsBase:  base,
		ReturnArity: len(fnType.Results),
		blockStack:  make([]blockEntry, 0, 8),
	}
	if err := vm.callStack.Push(initialFrame); err != nil {
		return nil, err
	}

	// Execute bytecode loop
	if err := vm.execute(ctx); err != nil {
		vm.callStack.Reset()
		vm.locals.Reset()
		return nil, err
	}

	// Pop results from value stack in reverse order
	results := make([]uint64, len(fnType.Results))
	for i := len(fnType.Results) - 1; i >= 0; i-- {
		val, err := vm.stack.Pop()
		if err != nil {
			return nil, err
		}
		results[i] = val
	}

	return results, nil
}

// RemainingFuel returns the number of instructions remaining before fuel exhaustion.
func (vm *VM) RemainingFuel() int64 {
	return vm.fuel
}

// ReadMemory copies a slice of bytes from linear memory at the specified offset.
func (vm *VM) ReadMemory(offset, length uint32) ([]byte, error) {
	return vm.memory.ReadBytes(offset, length)
}

// WriteMemory copies bytes directly into linear memory at the specified offset.
func (vm *VM) WriteMemory(offset uint32, data []byte) error {
	return vm.memory.WriteBytes(offset, data)
}

// Reset restores the VM to its initial state, clearing stacks and re-initializing memory.
func (vm *VM) Reset() {
	vm.stack.Reset()
	vm.callStack.Reset()
	vm.locals.Reset()
	vm.memory.Reset()

	// Re-apply static data segments
	for _, seg := range vm.module.Data {
		_ = vm.memory.WriteBytes(seg.Offset, seg.Data)
	}

	vm.fuel = vm.config.InitialFuel
	vm.fuelLimit = vm.config.InitialFuel > 0
}

// Close releases any resources held by the VM instance.
func (vm *VM) Close() error {
	vm.stack.Reset()
	vm.callStack.Reset()
	vm.locals.Reset()
	return nil
}
