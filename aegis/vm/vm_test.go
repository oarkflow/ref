package vm_test

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/oarkflow/ref/aegis/ir"
	"github.com/oarkflow/ref/aegis/vm"
)

type testFunc struct {
	name    string
	params  []ir.ValType
	results []ir.ValType
	locals  []ir.ValType
	body    []ir.Instruction
}

func testModule(funcs ...testFunc) *ir.Module {
	mod := &ir.Module{
		Version:   ir.Version,
		Memory:    ir.MemorySpec{InitialPages: 1, MaxPages: 4},
		Types:     make([]ir.FuncType, len(funcs)),
		Functions: make([]ir.Function, len(funcs)),
		Exports:   make([]ir.Export, len(funcs)),
	}

	for i, fn := range funcs {
		mod.Types[i] = ir.FuncType{
			Params:  fn.params,
			Results: fn.results,
		}
		mod.Functions[i] = ir.Function{
			Name:      fn.name,
			TypeIndex: uint32(i),
			Locals:    fn.locals,
			Body:      fn.body,
		}
		mod.Exports[i] = ir.Export{
			Name:      fn.name,
			FuncIndex: uint32(i),
		}
	}

	return mod
}

func TestArithmetic_I32(t *testing.T) {
	mod := testModule(
		testFunc{
			name:    "add",
			params:  []ir.ValType{ir.I32, ir.I32},
			results: []ir.ValType{ir.I32},
			body: []ir.Instruction{
				{Op: ir.LocalGet, Operands: [3]uint64{0, 0, 0}},
				{Op: ir.LocalGet, Operands: [3]uint64{1, 0, 0}},
				{Op: ir.I32Add, Operands: [3]uint64{0, 0, 0}},
				{Op: ir.Return, Operands: [3]uint64{0, 0, 0}},
			},
		},
		testFunc{
			name:    "sub_mul_bitwise",
			params:  []ir.ValType{ir.I32, ir.I32},
			results: []ir.ValType{ir.I32},
			body: []ir.Instruction{
				{Op: ir.LocalGet, Operands: [3]uint64{0, 0, 0}},
				{Op: ir.LocalGet, Operands: [3]uint64{1, 0, 0}},
				{Op: ir.I32Sub, Operands: [3]uint64{0, 0, 0}},
				{Op: ir.I32Const, Operands: [3]uint64{2, 0, 0}},
				{Op: ir.I32Mul, Operands: [3]uint64{0, 0, 0}},
				{Op: ir.Return, Operands: [3]uint64{0, 0, 0}},
			},
		},
	)

	v, err := vm.New(mod, vm.DefaultConfig())
	if err != nil {
		t.Fatalf("unexpected New error: %v", err)
	}

	ctx := context.Background()

	res, err := v.Call(ctx, "add", 15, 27)
	if err != nil {
		t.Fatalf("call 'add' error: %v", err)
	}
	if len(res) != 1 || res[0] != 42 {
		t.Errorf("expected 42, got %v", res)
	}

	res, err = v.Call(ctx, "sub_mul_bitwise", 30, 9)
	if err != nil {
		t.Fatalf("call 'sub_mul_bitwise' error: %v", err)
	}
	// (30 - 9) * 2 = 42
	if len(res) != 1 || res[0] != 42 {
		t.Errorf("expected 42, got %v", res)
	}
}

func TestArithmetic_I64(t *testing.T) {
	mod := testModule(
		testFunc{
			name:    "calc64",
			params:  []ir.ValType{ir.I64, ir.I64},
			results: []ir.ValType{ir.I64},
			body: []ir.Instruction{
				{Op: ir.LocalGet, Operands: [3]uint64{0, 0, 0}},
				{Op: ir.LocalGet, Operands: [3]uint64{1, 0, 0}},
				{Op: ir.I64Mul, Operands: [3]uint64{0, 0, 0}},
				{Op: ir.I64Const, Operands: [3]uint64{100, 0, 0}},
				{Op: ir.I64Add, Operands: [3]uint64{0, 0, 0}},
				{Op: ir.Return, Operands: [3]uint64{0, 0, 0}},
			},
		},
	)

	v, err := vm.New(mod, vm.DefaultConfig())
	if err != nil {
		t.Fatalf("unexpected New error: %v", err)
	}

	ctx := context.Background()
	res, err := v.Call(ctx, "calc64", 25, 4)
	if err != nil {
		t.Fatalf("call 'calc64' error: %v", err)
	}
	// 25 * 4 + 100 = 200
	if len(res) != 1 || res[0] != 200 {
		t.Errorf("expected 200, got %v", res)
	}
}

func TestArithmetic_F64(t *testing.T) {
	mod := testModule(
		testFunc{
			name:    "f64_math",
			params:  []ir.ValType{ir.F64, ir.F64},
			results: []ir.ValType{ir.F64},
			body: []ir.Instruction{
				{Op: ir.LocalGet, Operands: [3]uint64{0, 0, 0}},
				{Op: ir.LocalGet, Operands: [3]uint64{1, 0, 0}},
				{Op: ir.F64Add, Operands: [3]uint64{0, 0, 0}},
				{Op: ir.Return, Operands: [3]uint64{0, 0, 0}},
			},
		},
	)

	v, err := vm.New(mod, vm.DefaultConfig())
	if err != nil {
		t.Fatalf("unexpected New error: %v", err)
	}

	ctx := context.Background()
	argA := math.Float64bits(3.5)
	argB := math.Float64bits(2.5)
	res, err := v.Call(ctx, "f64_math", argA, argB)
	if err != nil {
		t.Fatalf("call 'f64_math' error: %v", err)
	}
	expected := math.Float64bits(6.0)
	if len(res) != 1 || res[0] != expected {
		t.Errorf("expected %f, got %f", 6.0, math.Float64frombits(res[0]))
	}
}

func TestConversions(t *testing.T) {
	mod := testModule(
		testFunc{
			name:    "convert",
			params:  []ir.ValType{ir.I32},
			results: []ir.ValType{ir.F64},
			body: []ir.Instruction{
				{Op: ir.LocalGet, Operands: [3]uint64{0, 0, 0}},
				{Op: ir.F64ConvertI32S, Operands: [3]uint64{0, 0, 0}},
				{Op: ir.Return, Operands: [3]uint64{0, 0, 0}},
			},
		},
	)

	v, err := vm.New(mod, vm.DefaultConfig())
	if err != nil {
		t.Fatalf("unexpected New error: %v", err)
	}

	ctx := context.Background()
	res, err := v.Call(ctx, "convert", 42)
	if err != nil {
		t.Fatalf("call 'convert' error: %v", err)
	}
	val := math.Float64frombits(res[0])
	if val != 42.0 {
		t.Errorf("expected 42.0, got %f", val)
	}
}

func TestMemory_StoreLoad(t *testing.T) {
	mod := testModule(
		testFunc{
			name:    "mem_roundtrip",
			params:  []ir.ValType{ir.I32, ir.I32},
			results: []ir.ValType{ir.I32},
			body: []ir.Instruction{
				// Address
				{Op: ir.LocalGet, Operands: [3]uint64{0, 0, 0}},
				// Value
				{Op: ir.LocalGet, Operands: [3]uint64{1, 0, 0}},
				// Store at Address + offset 4
				{Op: ir.Store32, Operands: [3]uint64{4, 0, 0}},
				// Load from Address + offset 4
				{Op: ir.LocalGet, Operands: [3]uint64{0, 0, 0}},
				{Op: ir.Load32, Operands: [3]uint64{4, 0, 0}},
				{Op: ir.Return, Operands: [3]uint64{0, 0, 0}},
			},
		},
	)

	v, err := vm.New(mod, vm.DefaultConfig())
	if err != nil {
		t.Fatalf("unexpected New error: %v", err)
	}

	ctx := context.Background()
	res, err := v.Call(ctx, "mem_roundtrip", 100, 99999)
	if err != nil {
		t.Fatalf("call 'mem_roundtrip' error: %v", err)
	}
	if len(res) != 1 || res[0] != 99999 {
		t.Errorf("expected 99999, got %v", res)
	}

	// Verify directly via VM ReadMemory
	bytes, err := v.ReadMemory(104, 4)
	if err != nil {
		t.Fatalf("ReadMemory error: %v", err)
	}
	if len(bytes) != 4 {
		t.Fatalf("expected 4 bytes, got %d", len(bytes))
	}
}

func TestMemory_GrowAndSize(t *testing.T) {
	mod := testModule(
		testFunc{
			name:    "grow_mem",
			params:  []ir.ValType{ir.I32},
			results: []ir.ValType{ir.I32},
			body: []ir.Instruction{
				{Op: ir.MemorySize, Operands: [3]uint64{0, 0, 0}},
				{Op: ir.LocalGet, Operands: [3]uint64{0, 0, 0}},
				{Op: ir.MemoryGrow, Operands: [3]uint64{0, 0, 0}},
				// return memory.size after grow
				{Op: ir.MemorySize, Operands: [3]uint64{0, 0, 0}},
				{Op: ir.Return, Operands: [3]uint64{0, 0, 0}},
			},
		},
	)

	v, err := vm.New(mod, vm.DefaultConfig())
	if err != nil {
		t.Fatalf("unexpected New error: %v", err)
	}

	ctx := context.Background()
	res, err := v.Call(ctx, "grow_mem", 2)
	if err != nil {
		t.Fatalf("call 'grow_mem' error: %v", err)
	}
	// Initial 1 page + 2 pages grew = 3 pages
	if len(res) != 1 || res[0] != 3 {
		t.Errorf("expected 3 pages, got %v", res)
	}
}

func TestControlFlow_IfElse(t *testing.T) {
	mod := testModule(
		testFunc{
			name:    "branch_test",
			params:  []ir.ValType{ir.I32},
			results: []ir.ValType{ir.I32},
			body: []ir.Instruction{
				{Op: ir.LocalGet, Operands: [3]uint64{0, 0, 0}},
				{Op: ir.I32Eqz, Operands: [3]uint64{0, 0, 0}},
				{Op: ir.If, Operands: [3]uint64{0, 0, 0}},
				{Op: ir.I32Const, Operands: [3]uint64{100, 0, 0}},
				{Op: ir.Else, Operands: [3]uint64{0, 0, 0}},
				{Op: ir.I32Const, Operands: [3]uint64{200, 0, 0}},
				{Op: ir.End, Operands: [3]uint64{0, 0, 0}},
				{Op: ir.Return, Operands: [3]uint64{0, 0, 0}},
			},
		},
	)

	v, err := vm.New(mod, vm.DefaultConfig())
	if err != nil {
		t.Fatalf("unexpected New error: %v", err)
	}

	ctx := context.Background()

	// If 0: Eqz is 1 (true) -> 100
	res, err := v.Call(ctx, "branch_test", 0)
	if err != nil {
		t.Fatalf("call 'branch_test(0)' error: %v", err)
	}
	if len(res) != 1 || res[0] != 100 {
		t.Errorf("expected 100, got %v", res)
	}

	// If 5: Eqz is 0 (false) -> 200
	res, err = v.Call(ctx, "branch_test", 5)
	if err != nil {
		t.Fatalf("call 'branch_test(5)' error: %v", err)
	}
	if len(res) != 1 || res[0] != 200 {
		t.Errorf("expected 200, got %v", res)
	}
}

func TestControlFlow_Loop(t *testing.T) {
	// Function calculates sum from 1 to N:
	// sum = 0, i = N
	// Loop:
	//   if i == 0 break
	//   sum += i
	//   i--
	//   br 0 (repeat loop)
	// End
	mod := testModule(
		testFunc{
			name:    "sum_n",
			params:  []ir.ValType{ir.I32},
			results: []ir.ValType{ir.I32},
			locals:  []ir.ValType{ir.I32}, // local 1: sum
			body: []ir.Instruction{
				// sum = 0
				{Op: ir.I32Const, Operands: [3]uint64{0, 0, 0}},
				{Op: ir.LocalSet, Operands: [3]uint64{1, 0, 0}},

				// Block enclosing the loop so we can break out of it
				{Op: ir.Block, Operands: [3]uint64{0, 0, 0}},
				{Op: ir.Loop, Operands: [3]uint64{0, 0, 0}},
				// Check if i == 0 -> break out of Block (depth 1)
				{Op: ir.LocalGet, Operands: [3]uint64{0, 0, 0}},
				{Op: ir.I32Eqz, Operands: [3]uint64{0, 0, 0}},
				{Op: ir.BrIf, Operands: [3]uint64{1, 0, 0}}, // break Block

				// sum += i
				{Op: ir.LocalGet, Operands: [3]uint64{1, 0, 0}},
				{Op: ir.LocalGet, Operands: [3]uint64{0, 0, 0}},
				{Op: ir.I32Add, Operands: [3]uint64{0, 0, 0}},
				{Op: ir.LocalSet, Operands: [3]uint64{1, 0, 0}},

				// i--
				{Op: ir.LocalGet, Operands: [3]uint64{0, 0, 0}},
				{Op: ir.I32Const, Operands: [3]uint64{1, 0, 0}},
				{Op: ir.I32Sub, Operands: [3]uint64{0, 0, 0}},
				{Op: ir.LocalSet, Operands: [3]uint64{0, 0, 0}},

				// Repeat loop (target depth 0 is Loop)
				{Op: ir.Br, Operands: [3]uint64{0, 0, 0}},
				{Op: ir.End, Operands: [3]uint64{0, 0, 0}}, // end Loop
				{Op: ir.End, Operands: [3]uint64{0, 0, 0}}, // end Block

				// Return sum
				{Op: ir.LocalGet, Operands: [3]uint64{1, 0, 0}},
				{Op: ir.Return, Operands: [3]uint64{0, 0, 0}},
			},
		},
	)

	v, err := vm.New(mod, vm.DefaultConfig())
	if err != nil {
		t.Fatalf("unexpected New error: %v", err)
	}

	ctx := context.Background()
	// Sum 1 to 10 = 55
	res, err := v.Call(ctx, "sum_n", 10)
	if err != nil {
		t.Fatalf("call 'sum_n(10)' error: %v", err)
	}
	if len(res) != 1 || res[0] != 55 {
		t.Errorf("expected 55, got %v", res)
	}
}

func TestControlFlow_NestedBlocks(t *testing.T) {
	// Nested blocks with branch to outer
	mod := testModule(
		testFunc{
			name:    "nested_blocks",
			params:  []ir.ValType{},
			results: []ir.ValType{ir.I32},
			body: []ir.Instruction{
				{Op: ir.Block, Operands: [3]uint64{0, 0, 0}},
				{Op: ir.Block, Operands: [3]uint64{0, 0, 0}},
				// Break out of outer block (depth 1)
				{Op: ir.Br, Operands: [3]uint64{1, 0, 0}},
				// Skipped instruction
				{Op: ir.I32Const, Operands: [3]uint64{999, 0, 0}},
				{Op: ir.End, Operands: [3]uint64{0, 0, 0}},
				// Also skipped
				{Op: ir.I32Const, Operands: [3]uint64{888, 0, 0}},
				{Op: ir.End, Operands: [3]uint64{0, 0, 0}},
				// Target of Br 1
				{Op: ir.I32Const, Operands: [3]uint64{777, 0, 0}},
				{Op: ir.Return, Operands: [3]uint64{0, 0, 0}},
			},
		},
	)

	v, err := vm.New(mod, vm.DefaultConfig())
	if err != nil {
		t.Fatalf("unexpected New error: %v", err)
	}

	ctx := context.Background()
	res, err := v.Call(ctx, "nested_blocks")
	if err != nil {
		t.Fatalf("call 'nested_blocks' error: %v", err)
	}
	if len(res) != 1 || res[0] != 777 {
		t.Errorf("expected 777, got %v", res)
	}
}

func TestHostFunctions(t *testing.T) {
	mod := &ir.Module{
		Version: ir.Version,
		Types: []ir.FuncType{
			// Type 0: (i32, i32) -> (i32)
			{Params: []ir.ValType{ir.I32, ir.I32}, Results: []ir.ValType{ir.I32}},
			// Type 1: (i32) -> (i32)
			{Params: []ir.ValType{ir.I32}, Results: []ir.ValType{ir.I32}},
		},
		Imports: []ir.Import{
			{Module: "env", Name: "multiply_host", Type: 0},
		},
		Functions: []ir.Function{
			{
				Name:      "use_host",
				TypeIndex: 1,
				Body: []ir.Instruction{
					{Op: ir.LocalGet, Operands: [3]uint64{0, 0, 0}},
					{Op: ir.I32Const, Operands: [3]uint64{10, 0, 0}},
					{Op: ir.CallImport, Operands: [3]uint64{0, 0, 0}}, // calls multiply_host
					{Op: ir.Return, Operands: [3]uint64{0, 0, 0}},
				},
			},
		},
		Exports: []ir.Export{
			{Name: "use_host", FuncIndex: 0},
		},
	}

	var hostCalled bool
	hostFunc := vm.NewHostFunc("env", "multiply_host", func(ctx context.Context, v *vm.VM, args []uint64) ([]uint64, error) {
		hostCalled = true
		if len(args) != 2 {
			return nil, errors.New("expected 2 args")
		}
		result := args[0] * args[1]
		return []uint64{result}, nil
	})

	v, err := vm.New(mod, vm.DefaultConfig(), hostFunc)
	if err != nil {
		t.Fatalf("unexpected New error: %v", err)
	}

	ctx := context.Background()
	res, err := v.Call(ctx, "use_host", 7)
	if err != nil {
		t.Fatalf("call 'use_host' error: %v", err)
	}
	if !hostCalled {
		t.Error("host function was not invoked")
	}
	if len(res) != 1 || res[0] != 70 {
		t.Errorf("expected 70, got %v", res)
	}
}

func TestFuelExhaustion(t *testing.T) {
	// Infinite loop
	mod := testModule(
		testFunc{
			name:    "infinite_loop",
			params:  []ir.ValType{},
			results: []ir.ValType{},
			body: []ir.Instruction{
				{Op: ir.Loop, Operands: [3]uint64{0, 0, 0}},
				{Op: ir.Nop, Operands: [3]uint64{0, 0, 0}},
				{Op: ir.Br, Operands: [3]uint64{0, 0, 0}}, // loop back
				{Op: ir.End, Operands: [3]uint64{0, 0, 0}},
			},
		},
	)

	cfg := vm.DefaultConfig()
	cfg.InitialFuel = 50

	v, err := vm.New(mod, cfg)
	if err != nil {
		t.Fatalf("unexpected New error: %v", err)
	}

	_, err = v.Call(context.Background(), "infinite_loop")
	if err == nil {
		t.Fatal("expected error due to fuel exhaustion, got nil")
	}

	var trap *vm.Trap
	if !errors.As(err, &trap) {
		t.Fatalf("expected *vm.Trap error, got %T: %v", err, err)
	}
	if trap.Kind != vm.TrapFuelExhausted {
		t.Errorf("expected TrapFuelExhausted, got %v", trap.Kind)
	}
}

func TestCallStackOverflow(t *testing.T) {
	// Deep recursive call
	mod := testModule(
		testFunc{
			name:    "recurse",
			params:  []ir.ValType{},
			results: []ir.ValType{},
			body: []ir.Instruction{
				{Op: ir.Call, Operands: [3]uint64{0, 0, 0}}, // calls itself
				{Op: ir.Return, Operands: [3]uint64{0, 0, 0}},
			},
		},
	)

	cfg := vm.DefaultConfig()
	cfg.MaxCallDepth = 16

	v, err := vm.New(mod, cfg)
	if err != nil {
		t.Fatalf("unexpected New error: %v", err)
	}

	_, err = v.Call(context.Background(), "recurse")
	if err == nil {
		t.Fatal("expected call stack overflow error, got nil")
	}

	var trap *vm.Trap
	if !errors.As(err, &trap) {
		t.Fatalf("expected *vm.Trap error, got %T: %v", err, err)
	}
	if trap.Kind != vm.TrapCallStackOverflow {
		t.Errorf("expected TrapCallStackOverflow, got %v", trap.Kind)
	}
}

func TestDivisionByZero(t *testing.T) {
	mod := testModule(
		testFunc{
			name:    "div_zero",
			params:  []ir.ValType{},
			results: []ir.ValType{ir.I32},
			body: []ir.Instruction{
				{Op: ir.I32Const, Operands: [3]uint64{10, 0, 0}},
				{Op: ir.I32Const, Operands: [3]uint64{0, 0, 0}},
				{Op: ir.I32DivS, Operands: [3]uint64{0, 0, 0}},
				{Op: ir.Return, Operands: [3]uint64{0, 0, 0}},
			},
		},
	)

	v, err := vm.New(mod, vm.DefaultConfig())
	if err != nil {
		t.Fatalf("unexpected New error: %v", err)
	}

	_, err = v.Call(context.Background(), "div_zero")
	if err == nil {
		t.Fatal("expected division by zero error, got nil")
	}

	var trap *vm.Trap
	if !errors.As(err, &trap) {
		t.Fatalf("expected *vm.Trap error, got %T: %v", err, err)
	}
	if trap.Kind != vm.TrapDivisionByZero {
		t.Errorf("expected TrapDivisionByZero, got %v", trap.Kind)
	}
}

func TestMemoryOutOfBounds(t *testing.T) {
	mod := testModule(
		testFunc{
			name:    "oob_load",
			params:  []ir.ValType{},
			results: []ir.ValType{ir.I32},
			body: []ir.Instruction{
				// Memory is 1 page = 65536 bytes
				// Try to load from 70000
				{Op: ir.I32Const, Operands: [3]uint64{70000, 0, 0}},
				{Op: ir.Load32, Operands: [3]uint64{0, 0, 0}},
				{Op: ir.Return, Operands: [3]uint64{0, 0, 0}},
			},
		},
	)

	v, err := vm.New(mod, vm.DefaultConfig())
	if err != nil {
		t.Fatalf("unexpected New error: %v", err)
	}

	_, err = v.Call(context.Background(), "oob_load")
	if err == nil {
		t.Fatal("expected out of bounds memory access error, got nil")
	}

	var trap *vm.Trap
	if !errors.As(err, &trap) {
		t.Fatalf("expected *vm.Trap error, got %T: %v", err, err)
	}
	if trap.Kind != vm.TrapMemoryOutOfBounds {
		t.Errorf("expected TrapMemoryOutOfBounds, got %v", trap.Kind)
	}
}

func TestContextCancellation(t *testing.T) {
	mod := testModule(
		testFunc{
			name:    "cancellable_loop",
			params:  []ir.ValType{},
			results: []ir.ValType{},
			body: []ir.Instruction{
				{Op: ir.Loop, Operands: [3]uint64{0, 0, 0}},
				{Op: ir.Nop, Operands: [3]uint64{0, 0, 0}},
				{Op: ir.Br, Operands: [3]uint64{0, 0, 0}},
				{Op: ir.End, Operands: [3]uint64{0, 0, 0}},
			},
		},
	)

	v, err := vm.New(mod, vm.DefaultConfig())
	if err != nil {
		t.Fatalf("unexpected New error: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	// Cancel after brief delay
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()

	_, err = v.Call(ctx, "cancellable_loop")
	if err == nil {
		t.Fatal("expected cancellation error, got nil")
	}

	var trap *vm.Trap
	if !errors.As(err, &trap) {
		t.Fatalf("expected *vm.Trap error, got %T: %v", err, err)
	}
	if trap.Kind != vm.TrapCancelled {
		t.Errorf("expected TrapCancelled, got %v", trap.Kind)
	}
}

func TestMultipleExports(t *testing.T) {
	mod := testModule(
		testFunc{
			name:    "func_a",
			params:  []ir.ValType{ir.I32},
			results: []ir.ValType{ir.I32},
			body: []ir.Instruction{
				{Op: ir.LocalGet, Operands: [3]uint64{0, 0, 0}},
				{Op: ir.I32Const, Operands: [3]uint64{10, 0, 0}},
				{Op: ir.I32Add, Operands: [3]uint64{0, 0, 0}},
				{Op: ir.Return, Operands: [3]uint64{0, 0, 0}},
			},
		},
		testFunc{
			name:    "func_b",
			params:  []ir.ValType{ir.I32},
			results: []ir.ValType{ir.I32},
			body: []ir.Instruction{
				{Op: ir.LocalGet, Operands: [3]uint64{0, 0, 0}},
				{Op: ir.I32Const, Operands: [3]uint64{10, 0, 0}},
				{Op: ir.I32Sub, Operands: [3]uint64{0, 0, 0}},
				{Op: ir.Return, Operands: [3]uint64{0, 0, 0}},
			},
		},
	)

	v, err := vm.New(mod, vm.DefaultConfig())
	if err != nil {
		t.Fatalf("unexpected New error: %v", err)
	}

	ctx := context.Background()

	resA, err := v.Call(ctx, "func_a", 50)
	if err != nil {
		t.Fatalf("call 'func_a' error: %v", err)
	}
	if len(resA) != 1 || resA[0] != 60 {
		t.Errorf("expected 60, got %v", resA)
	}

	resB, err := v.Call(ctx, "func_b", 50)
	if err != nil {
		t.Fatalf("call 'func_b' error: %v", err)
	}
	if len(resB) != 1 || resB[0] != 40 {
		t.Errorf("expected 40, got %v", resB)
	}
}

func TestLocals(t *testing.T) {
	mod := testModule(
		testFunc{
			name:    "locals_test",
			params:  []ir.ValType{ir.I32},
			results: []ir.ValType{ir.I32},
			locals:  []ir.ValType{ir.I32, ir.I32}, // local 1, local 2
			body: []ir.Instruction{
				// local 1 = arg0 * 2
				{Op: ir.LocalGet, Operands: [3]uint64{0, 0, 0}},
				{Op: ir.I32Const, Operands: [3]uint64{2, 0, 0}},
				{Op: ir.I32Mul, Operands: [3]uint64{0, 0, 0}},
				{Op: ir.LocalSet, Operands: [3]uint64{1, 0, 0}},

				// local 2 = local 1 + 5 (using LocalTee to copy to local 2 and keep on stack)
				{Op: ir.LocalGet, Operands: [3]uint64{1, 0, 0}},
				{Op: ir.I32Const, Operands: [3]uint64{5, 0, 0}},
				{Op: ir.I32Add, Operands: [3]uint64{0, 0, 0}},
				{Op: ir.LocalTee, Operands: [3]uint64{2, 0, 0}},

				// add local 2 again
				{Op: ir.LocalGet, Operands: [3]uint64{2, 0, 0}},
				{Op: ir.I32Add, Operands: [3]uint64{0, 0, 0}},
				{Op: ir.Return, Operands: [3]uint64{0, 0, 0}},
			},
		},
	)

	v, err := vm.New(mod, vm.DefaultConfig())
	if err != nil {
		t.Fatalf("unexpected New error: %v", err)
	}

	ctx := context.Background()
	// arg = 10 -> local1 = 20 -> local2 = 25 -> 25 + 25 = 50
	res, err := v.Call(ctx, "locals_test", 10)
	if err != nil {
		t.Fatalf("call 'locals_test' error: %v", err)
	}
	if len(res) != 1 || res[0] != 50 {
		t.Errorf("expected 50, got %v", res)
	}
}

func TestTrapExplicit(t *testing.T) {
	mod := testModule(
		testFunc{
			name:    "fail",
			params:  []ir.ValType{},
			results: []ir.ValType{},
			body: []ir.Instruction{
				{Op: ir.Trap, Operands: [3]uint64{0, 0, 0}},
			},
		},
	)

	v, err := vm.New(mod, vm.DefaultConfig())
	if err != nil {
		t.Fatalf("unexpected New error: %v", err)
	}

	_, err = v.Call(context.Background(), "fail")
	if err == nil {
		t.Fatal("expected trap error, got nil")
	}

	var trap *vm.Trap
	if !errors.As(err, &trap) {
		t.Fatalf("expected *vm.Trap error, got %T: %v", err, err)
	}
	if trap.Kind != vm.TrapExplicit {
		t.Errorf("expected TrapExplicit, got %v", trap.Kind)
	}
}

func TestVMReset(t *testing.T) {
	mod := testModule(
		testFunc{
			name:    "write_and_read",
			params:  []ir.ValType{ir.I32},
			results: []ir.ValType{ir.I32},
			body: []ir.Instruction{
				{Op: ir.I32Const, Operands: [3]uint64{0, 0, 0}},
				{Op: ir.LocalGet, Operands: [3]uint64{0, 0, 0}},
				{Op: ir.Store32, Operands: [3]uint64{0, 0, 0}},
				{Op: ir.I32Const, Operands: [3]uint64{0, 0, 0}},
				{Op: ir.Load32, Operands: [3]uint64{0, 0, 0}},
				{Op: ir.Return, Operands: [3]uint64{0, 0, 0}},
			},
		},
	)

	v, err := vm.New(mod, vm.DefaultConfig())
	if err != nil {
		t.Fatalf("unexpected New error: %v", err)
	}

	ctx := context.Background()
	res, err := v.Call(ctx, "write_and_read", 999)
	if err != nil {
		t.Fatalf("call error: %v", err)
	}
	if res[0] != 999 {
		t.Fatalf("expected 999, got %v", res[0])
	}

	// Reset VM
	v.Reset()

	// Memory at 0 should be 0 now
	mem, err := v.ReadMemory(0, 4)
	if err != nil {
		t.Fatalf("ReadMemory error: %v", err)
	}
	for _, b := range mem {
		if b != 0 {
			t.Errorf("expected zero byte after reset, got %d", b)
		}
	}
}
