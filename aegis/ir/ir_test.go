package ir_test

import (
	"math"
	"strings"
	"testing"

	"github.com/oarkflow/ref/aegis/ir"
)

// helper to create a fully populated valid module for round-trip testing.
func createTestModule() *ir.Module {
	var buildID [32]byte
	for i := range buildID {
		buildID[i] = byte(i + 1)
	}

	return &ir.Module{
		Version: ir.Version,
		BuildID: buildID,
		Types: []ir.FuncType{
			{
				Params:  []ir.ValType{ir.I32, ir.I32},
				Results: []ir.ValType{ir.I32},
			},
			{
				Params:  []ir.ValType{ir.I64},
				Results: []ir.ValType{ir.I64, ir.F64},
			},
			{
				Params:  []ir.ValType{ir.Bytes},
				Results: []ir.ValType{ir.I32},
			},
		},
		Imports: []ir.Import{
			{
				Module: "env",
				Name:   "log_message",
				Type:   2,
			},
			{
				Module: "crypto",
				Name:   "sha256",
				Type:   2,
			},
		},
		Functions: []ir.Function{
			{
				Name:      "add_numbers",
				TypeIndex: 0,
				Locals:    []ir.ValType{ir.I32},
				Body: []ir.Instruction{
					{Op: ir.LocalGet, Operands: [3]uint64{0, 0, 0}},
					{Op: ir.LocalGet, Operands: [3]uint64{1, 0, 0}},
					{Op: ir.I32Add, Operands: [3]uint64{0, 0, 0}},
					{Op: ir.LocalSet, Operands: [3]uint64{2, 0, 0}},
					{Op: ir.LocalGet, Operands: [3]uint64{2, 0, 0}},
					{Op: ir.Return, Operands: [3]uint64{0, 0, 0}},
				},
				Attributes: ir.FuncAttr{
					Confidentiality: ir.ConfHigh,
					MustNotInline:   true,
					Diversify:       true,
				},
			},
			{
				Name:      "call_worker",
				TypeIndex: 1,
				Locals:    []ir.ValType{ir.F64},
				Body: []ir.Instruction{
					{Op: ir.I32Const, Operands: [3]uint64{42, 0, 0}},
					{Op: ir.I32Const, Operands: [3]uint64{100, 0, 0}},
					{Op: ir.Call, Operands: [3]uint64{0, 0, 0}},       // calls add_numbers (index 0)
					{Op: ir.CallImport, Operands: [3]uint64{0, 0, 0}}, // calls log_message
					{Op: ir.I64Const, Operands: [3]uint64{123456789, 0, 0}},
					{Op: ir.F64Const, Operands: [3]uint64{math.Float64bits(3.14159), 0, 0}},
					{Op: ir.Return, Operands: [3]uint64{0, 0, 0}},
				},
				Attributes: ir.FuncAttr{
					Confidentiality: ir.ConfCritical,
					MustNotInline:   false,
					Diversify:       true,
				},
			},
		},
		Exports: []ir.Export{
			{Name: "add", FuncIndex: 0},
			{Name: "worker", FuncIndex: 1},
			{Name: "ext_log", FuncIndex: 2}, // References import 0 (unified index)
		},
		Memory: ir.MemorySpec{
			InitialPages: 1,
			MaxPages:     4,
		},
		Data: []ir.DataSegment{
			{
				Offset: 0,
				Data:   []byte("Hello, Aegis IR!"),
			},
			{
				Offset: 128,
				Data:   []byte{0xDE, 0xAD, 0xBE, 0xEF},
			},
		},
		Metadata: ir.Metadata{
			CompilerVersion: "aegisc-0.1.0",
			TargetPlatform:  "aegis-vm-v1",
			BuildTime:       1728900000,
		},
	}
}

func assertModulesEqual(t *testing.T, expected, actual *ir.Module) {
	t.Helper()

	if actual.Version != expected.Version {
		t.Errorf("version mismatch: expected %d, got %d", expected.Version, actual.Version)
	}
	if actual.BuildID != expected.BuildID {
		t.Errorf("buildID mismatch: expected %x, got %x", expected.BuildID, actual.BuildID)
	}

	// Types
	if len(actual.Types) != len(expected.Types) {
		t.Fatalf("types count mismatch: expected %d, got %d", len(expected.Types), len(actual.Types))
	}
	for i := range expected.Types {
		expT, actT := expected.Types[i], actual.Types[i]
		if len(actT.Params) != len(expT.Params) {
			t.Errorf("type %d params len mismatch: expected %d, got %d", i, len(expT.Params), len(actT.Params))
		}
		for p := range expT.Params {
			if actT.Params[p] != expT.Params[p] {
				t.Errorf("type %d param %d mismatch: expected %v, got %v", i, p, expT.Params[p], actT.Params[p])
			}
		}
		if len(actT.Results) != len(expT.Results) {
			t.Errorf("type %d results len mismatch: expected %d, got %d", i, len(expT.Results), len(actT.Results))
		}
		for r := range expT.Results {
			if actT.Results[r] != expT.Results[r] {
				t.Errorf("type %d result %d mismatch: expected %v, got %v", i, r, expT.Results[r], actT.Results[r])
			}
		}
	}

	// Imports
	if len(actual.Imports) != len(expected.Imports) {
		t.Fatalf("imports count mismatch: expected %d, got %d", len(expected.Imports), len(actual.Imports))
	}
	for i := range expected.Imports {
		if actual.Imports[i] != expected.Imports[i] {
			t.Errorf("import %d mismatch: expected %+v, got %+v", i, expected.Imports[i], actual.Imports[i])
		}
	}

	// Functions
	if len(actual.Functions) != len(expected.Functions) {
		t.Fatalf("functions count mismatch: expected %d, got %d", len(expected.Functions), len(actual.Functions))
	}
	for i := range expected.Functions {
		expF, actF := expected.Functions[i], actual.Functions[i]
		if actF.Name != expF.Name {
			t.Errorf("func %d name mismatch: expected %q, got %q", i, expF.Name, actF.Name)
		}
		if actF.TypeIndex != expF.TypeIndex {
			t.Errorf("func %d type index mismatch: expected %d, got %d", i, expF.TypeIndex, actF.TypeIndex)
		}
		if actF.Attributes != expF.Attributes {
			t.Errorf("func %d attributes mismatch: expected %+v, got %+v", i, expF.Attributes, actF.Attributes)
		}
		if len(actF.Locals) != len(expF.Locals) {
			t.Fatalf("func %d locals count mismatch: expected %d, got %d", i, len(expF.Locals), len(actF.Locals))
		}
		for l := range expF.Locals {
			if actF.Locals[l] != expF.Locals[l] {
				t.Errorf("func %d local %d mismatch: expected %v, got %v", i, l, expF.Locals[l], actF.Locals[l])
			}
		}
		if len(actF.Body) != len(expF.Body) {
			t.Fatalf("func %d body len mismatch: expected %d, got %d", i, len(expF.Body), len(actF.Body))
		}
		for b := range expF.Body {
			if actF.Body[b] != expF.Body[b] {
				t.Errorf("func %d inst %d mismatch: expected %+v, got %+v", i, b, expF.Body[b], actF.Body[b])
			}
		}
	}

	// Exports
	if len(actual.Exports) != len(expected.Exports) {
		t.Fatalf("exports count mismatch: expected %d, got %d", len(expected.Exports), len(actual.Exports))
	}
	for i := range expected.Exports {
		if actual.Exports[i] != expected.Exports[i] {
			t.Errorf("export %d mismatch: expected %+v, got %+v", i, expected.Exports[i], actual.Exports[i])
		}
	}

	// Memory
	if actual.Memory != expected.Memory {
		t.Errorf("memory mismatch: expected %+v, got %+v", expected.Memory, actual.Memory)
	}

	// Data
	if len(actual.Data) != len(expected.Data) {
		t.Fatalf("data count mismatch: expected %d, got %d", len(expected.Data), len(actual.Data))
	}
	for i := range expected.Data {
		if actual.Data[i].Offset != expected.Data[i].Offset {
			t.Errorf("data %d offset mismatch: expected %d, got %d", i, expected.Data[i].Offset, actual.Data[i].Offset)
		}
		if string(actual.Data[i].Data) != string(expected.Data[i].Data) {
			t.Errorf("data %d content mismatch: expected %v, got %v", i, expected.Data[i].Data, actual.Data[i].Data)
		}
	}

	// Metadata
	if actual.Metadata != expected.Metadata {
		t.Errorf("metadata mismatch: expected %+v, got %+v", expected.Metadata, actual.Metadata)
	}
}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	orig := createTestModule()

	encoded, err := ir.Encode(orig)
	if err != nil {
		t.Fatalf("encode failed: %v", err)
	}

	decoded, err := ir.Decode(encoded)
	if err != nil {
		t.Fatalf("decode failed: %v", err)
	}

	assertModulesEqual(t, orig, decoded)

	// Validate the decoded module
	if err := ir.Validate(decoded); err != nil {
		t.Fatalf("decoded module failed validation: %v", err)
	}
}

func TestEdgeCases(t *testing.T) {
	t.Run("EmptyModule", func(t *testing.T) {
		empty := &ir.Module{
			Version: ir.Version,
		}

		encoded, err := ir.Encode(empty)
		if err != nil {
			t.Fatalf("failed to encode empty module: %v", err)
		}

		decoded, err := ir.Decode(encoded)
		if err != nil {
			t.Fatalf("failed to decode empty module: %v", err)
		}

		if decoded.Version != ir.Version {
			t.Errorf("expected version %d, got %d", ir.Version, decoded.Version)
		}
		if len(decoded.Functions) != 0 || len(decoded.Types) != 0 || len(decoded.Imports) != 0 {
			t.Errorf("expected empty slices, got functions=%d types=%d imports=%d",
				len(decoded.Functions), len(decoded.Types), len(decoded.Imports))
		}

		// Validation on empty module succeeds
		if err := ir.Validate(decoded); err != nil {
			t.Errorf("validation of empty module should pass, got: %v", err)
		}
	})

	t.Run("ModuleWithOnlyImports", func(t *testing.T) {
		mod := &ir.Module{
			Version: ir.Version,
			Types: []ir.FuncType{
				{Params: []ir.ValType{ir.I32}, Results: []ir.ValType{ir.I32}},
			},
			Imports: []ir.Import{
				{Module: "env", Name: "syscall", Type: 0},
			},
		}

		encoded, err := ir.Encode(mod)
		if err != nil {
			t.Fatalf("encode failed: %v", err)
		}

		decoded, err := ir.Decode(encoded)
		if err != nil {
			t.Fatalf("decode failed: %v", err)
		}

		assertModulesEqual(t, mod, decoded)

		if err := ir.Validate(decoded); err != nil {
			t.Errorf("validation should pass, got: %v", err)
		}
	})

	t.Run("ModuleWithMaxOperands", func(t *testing.T) {
		maxOperands := [3]uint64{math.MaxUint64, math.MaxUint64, math.MaxUint64}
		mod := &ir.Module{
			Version: ir.Version,
			Types: []ir.FuncType{
				{Params: []ir.ValType{}, Results: []ir.ValType{}},
			},
			Functions: []ir.Function{
				{
					Name:      "all_operands",
					TypeIndex: 0,
					Body: []ir.Instruction{
						{Op: ir.Nop, Operands: maxOperands},
						{Op: ir.Return, Operands: maxOperands},
					},
				},
			},
		}

		encoded, err := ir.Encode(mod)
		if err != nil {
			t.Fatalf("encode failed: %v", err)
		}

		decoded, err := ir.Decode(encoded)
		if err != nil {
			t.Fatalf("decode failed: %v", err)
		}

		assertModulesEqual(t, mod, decoded)

		// Check operand preservation
		for i, inst := range decoded.Functions[0].Body {
			if inst.Operands != maxOperands {
				t.Errorf("instruction %d operands not preserved: got %+v", i, inst.Operands)
			}
		}

		// Text format handles all 3 operands cleanly
		text := ir.Text(decoded)
		if !strings.Contains(text, "18446744073709551615") {
			t.Errorf("text output should contain max uint64 operand values, got:\n%s", text)
		}
	})
}

func TestValidationErrors(t *testing.T) {
	tests := []struct {
		name          string
		module        *ir.Module
		expectedError string
	}{
		{
			name:          "NilModule",
			module:        nil,
			expectedError: "ir: module is nil",
		},
		{
			name: "InvalidVersion",
			module: &ir.Module{
				Version: 999,
			},
			expectedError: "ir: unsupported version 999, expected 1",
		},
		{
			name: "InvalidValTypeInParams",
			module: &ir.Module{
				Version: ir.Version,
				Types: []ir.FuncType{
					{Params: []ir.ValType{ir.ValType(99)}},
				},
			},
			expectedError: "invalid ValType 99",
		},
		{
			name: "InvalidValTypeInResults",
			module: &ir.Module{
				Version: ir.Version,
				Types: []ir.FuncType{
					{Results: []ir.ValType{ir.ValType(88)}},
				},
			},
			expectedError: "invalid ValType 88",
		},
		{
			name: "InvalidFunctionTypeIndex",
			module: &ir.Module{
				Version: ir.Version,
				Functions: []ir.Function{
					{
						Name:      "bad_type",
						TypeIndex: 5, // out of range
						Body:      []ir.Instruction{{Op: ir.Nop}},
					},
				},
			},
			expectedError: "references invalid type index 5 (type count: 0)",
		},
		{
			name: "InvalidImportTypeIndex",
			module: &ir.Module{
				Version: ir.Version,
				Imports: []ir.Import{
					{Module: "env", Name: "log", Type: 3},
				},
			},
			expectedError: "references invalid type index 3 (type count: 0)",
		},
		{
			name: "InvalidExportFuncIndex",
			module: &ir.Module{
				Version: ir.Version,
				Exports: []ir.Export{
					{Name: "missing", FuncIndex: 10},
				},
			},
			expectedError: "references invalid function index 10 (total available: 0)",
		},
		{
			name: "EmptyFunctionBody",
			module: &ir.Module{
				Version: ir.Version,
				Types: []ir.FuncType{
					{Params: nil, Results: nil},
				},
				Functions: []ir.Function{
					{
						Name:      "empty_func",
						TypeIndex: 0,
						Body:      nil, // empty!
					},
				},
			},
			expectedError: "empty body (must have at least one instruction)",
		},
		{
			name: "CallOutOfRange",
			module: &ir.Module{
				Version: ir.Version,
				Types: []ir.FuncType{
					{Params: nil, Results: nil},
				},
				Functions: []ir.Function{
					{
						Name:      "caller",
						TypeIndex: 0,
						Body: []ir.Instruction{
							{Op: ir.Call, Operands: [3]uint64{5, 0, 0}}, // only 1 function exists
						},
					},
				},
			},
			expectedError: "function index 5 out of bounds (function count: 1)",
		},
		{
			name: "CallImportOutOfRange",
			module: &ir.Module{
				Version: ir.Version,
				Types: []ir.FuncType{
					{Params: nil, Results: nil},
				},
				Functions: []ir.Function{
					{
						Name:      "caller",
						TypeIndex: 0,
						Body: []ir.Instruction{
							{Op: ir.CallImport, Operands: [3]uint64{2, 0, 0}}, // 0 imports
						},
					},
				},
			},
			expectedError: "import index 2 out of bounds (import count: 0)",
		},
		{
			name: "LocalGetOutOfRange",
			module: &ir.Module{
				Version: ir.Version,
				Types: []ir.FuncType{
					{Params: []ir.ValType{ir.I32}, Results: nil}, // 1 param
				},
				Functions: []ir.Function{
					{
						Name:      "local_user",
						TypeIndex: 0,
						Locals:    []ir.ValType{ir.I32}, // 1 local (total 2: index 0 and 1)
						Body: []ir.Instruction{
							{Op: ir.LocalGet, Operands: [3]uint64{5, 0, 0}}, // local index 5 out of range
						},
					},
				},
			},
			expectedError: "local index 5 out of bounds (params: 1, locals: 1)",
		},
		{
			name: "MemoryMaxPagesLessThanInitialPages",
			module: &ir.Module{
				Version: ir.Version,
				Memory: ir.MemorySpec{
					InitialPages: 10,
					MaxPages:     5,
				},
			},
			expectedError: "memory max pages (5) must be greater than or equal to initial pages (10)",
		},
		{
			name: "DataSegmentExceedsMemoryBounds",
			module: &ir.Module{
				Version: ir.Version,
				Memory: ir.MemorySpec{
					InitialPages: 1, // 65536 bytes
					MaxPages:     1,
				},
				Data: []ir.DataSegment{
					{
						Offset: 65530,
						Data:   []byte("1234567890"), // 65540 > 65536
					},
				},
			},
			expectedError: "data segment 0 (offset 65530, length 10) exceeds memory bounds (65536 bytes)",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ir.Validate(tc.module)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.expectedError)
			}
			if !strings.Contains(err.Error(), tc.expectedError) {
				t.Errorf("expected error containing %q, got:\n%s", tc.expectedError, err.Error())
			}
			var valErr *ir.ValidationError
			if _, ok := err.(*ir.ValidationError); !ok {
				t.Errorf("expected error type *ir.ValidationError, got %T", err)
			}
			_ = valErr
		})
	}
}

func TestValidationMultipleErrors(t *testing.T) {
	// A module with multiple errors simultaneously
	mod := &ir.Module{
		Version: 99,
		Memory: ir.MemorySpec{
			InitialPages: 5,
			MaxPages:     2,
		},
		Functions: []ir.Function{
			{
				Name:      "bad_func",
				TypeIndex: 10,
				Body:      nil,
			},
		},
	}

	err := ir.Validate(mod)
	if err == nil {
		t.Fatal("expected validation error, got nil")
	}

	valErr, ok := err.(*ir.ValidationError)
	if !ok {
		t.Fatalf("expected *ir.ValidationError, got %T", err)
	}

	if len(valErr.Errors) < 3 {
		t.Errorf("expected at least 3 aggregated errors, got %d:\n%s", len(valErr.Errors), valErr.Error())
	}
}

func TestOpcodeStringCoverage(t *testing.T) {
	opcodes := []struct {
		op       ir.Opcode
		expected string
		operandN int
	}{
		// Control flow
		{ir.OpNop, "nop", 0},
		{ir.OpBlock, "block", 0},
		{ir.OpLoop, "loop", 0},
		{ir.OpIf, "if", 0},
		{ir.OpElse, "else", 0},
		{ir.OpEnd, "end", 0},
		{ir.OpBr, "br", 1},
		{ir.OpBrIf, "br_if", 1},
		{ir.OpBrTable, "br_table", 1},
		{ir.OpReturn, "return", 0},
		{ir.OpCall, "call", 1},
		{ir.OpCallImport, "call_import", 1},

		// Stack
		{ir.OpLocalGet, "local.get", 1},
		{ir.OpLocalSet, "local.set", 1},
		{ir.OpLocalTee, "local.tee", 1},

		// Memory
		{ir.OpLoad32, "load32", 1},
		{ir.OpLoad64, "load64", 1},
		{ir.OpStore32, "store32", 1},
		{ir.OpStore64, "store64", 1},
		{ir.OpLoadF32, "load_f32", 1},
		{ir.OpLoadF64, "load_f64", 1},
		{ir.OpStoreF32, "store_f32", 1},
		{ir.OpStoreF64, "store_f64", 1},
		{ir.OpMemorySize, "memory.size", 0},
		{ir.OpMemoryGrow, "memory.grow", 0},

		// Constants
		{ir.OpI32Const, "i32.const", 1},
		{ir.OpI64Const, "i64.const", 1},
		{ir.OpF32Const, "f32.const", 1},
		{ir.OpF64Const, "f64.const", 1},

		// i32 arithmetic
		{ir.OpI32Add, "i32.add", 0},
		{ir.OpI32Sub, "i32.sub", 0},
		{ir.OpI32Mul, "i32.mul", 0},
		{ir.OpI32DivS, "i32.div_s", 0},
		{ir.OpI32DivU, "i32.div_u", 0},
		{ir.OpI32RemS, "i32.rem_s", 0},
		{ir.OpI32RemU, "i32.rem_u", 0},
		{ir.OpI32And, "i32.and", 0},
		{ir.OpI32Or, "i32.or", 0},
		{ir.OpI32Xor, "i32.xor", 0},
		{ir.OpI32Shl, "i32.shl", 0},
		{ir.OpI32ShrS, "i32.shr_s", 0},
		{ir.OpI32ShrU, "i32.shr_u", 0},

		// i32 comparison
		{ir.OpI32Eqz, "i32.eqz", 0},
		{ir.OpI32Eq, "i32.eq", 0},
		{ir.OpI32Ne, "i32.ne", 0},
		{ir.OpI32LtS, "i32.lt_s", 0},
		{ir.OpI32LtU, "i32.lt_u", 0},
		{ir.OpI32GtS, "i32.gt_s", 0},
		{ir.OpI32GtU, "i32.gt_u", 0},
		{ir.OpI32LeS, "i32.le_s", 0},
		{ir.OpI32LeU, "i32.le_u", 0},
		{ir.OpI32GeS, "i32.ge_s", 0},
		{ir.OpI32GeU, "i32.ge_u", 0},

		// i64 arithmetic
		{ir.OpI64Add, "i64.add", 0},
		{ir.OpI64Sub, "i64.sub", 0},
		{ir.OpI64Mul, "i64.mul", 0},
		{ir.OpI64DivS, "i64.div_s", 0},
		{ir.OpI64DivU, "i64.div_u", 0},
		{ir.OpI64RemS, "i64.rem_s", 0},
		{ir.OpI64RemU, "i64.rem_u", 0},
		{ir.OpI64And, "i64.and", 0},
		{ir.OpI64Or, "i64.or", 0},
		{ir.OpI64Xor, "i64.xor", 0},
		{ir.OpI64Shl, "i64.shl", 0},
		{ir.OpI64ShrS, "i64.shr_s", 0},
		{ir.OpI64ShrU, "i64.shr_u", 0},

		// i64 comparison
		{ir.OpI64Eqz, "i64.eqz", 0},
		{ir.OpI64Eq, "i64.eq", 0},
		{ir.OpI64Ne, "i64.ne", 0},
		{ir.OpI64LtS, "i64.lt_s", 0},
		{ir.OpI64LtU, "i64.lt_u", 0},
		{ir.OpI64GtS, "i64.gt_s", 0},
		{ir.OpI64GtU, "i64.gt_u", 0},
		{ir.OpI64LeS, "i64.le_s", 0},
		{ir.OpI64LeU, "i64.le_u", 0},
		{ir.OpI64GeS, "i64.ge_s", 0},
		{ir.OpI64GeU, "i64.ge_u", 0},

		// f64 arithmetic
		{ir.OpF64Add, "f64.add", 0},
		{ir.OpF64Sub, "f64.sub", 0},
		{ir.OpF64Mul, "f64.mul", 0},
		{ir.OpF64Div, "f64.div", 0},

		// f64 comparison
		{ir.OpF64Eq, "f64.eq", 0},
		{ir.OpF64Ne, "f64.ne", 0},
		{ir.OpF64Lt, "f64.lt", 0},
		{ir.OpF64Gt, "f64.gt", 0},
		{ir.OpF64Le, "f64.le", 0},
		{ir.OpF64Ge, "f64.ge", 0},

		// Conversions
		{ir.OpI32WrapI64, "i32.wrap_i64", 0},
		{ir.OpI64ExtendI32S, "i64.extend_i32_s", 0},
		{ir.OpI64ExtendI32U, "i64.extend_i32_u", 0},
		{ir.OpF64ConvertI32S, "f64.convert_i32_s", 0},
		{ir.OpF64ConvertI64S, "f64.convert_i64_s", 0},
		{ir.OpI32TruncF64S, "i32.trunc_f64_s", 0},

		// Trap
		{ir.OpTrap, "trap", 0},
	}

	for _, item := range opcodes {
		if str := item.op.String(); str != item.expected {
			t.Errorf("opcode 0x%02X string mismatch: expected %q, got %q", uint16(item.op), item.expected, str)
		}
		if n := item.op.OperandCount(); n != item.operandN {
			t.Errorf("opcode 0x%02X operand count mismatch: expected %d, got %d", uint16(item.op), item.operandN, n)
		}
	}

	// Unknown opcode
	unknown := ir.Opcode(0x1234)
	if !strings.Contains(unknown.String(), "opcode(0x1234)") {
		t.Errorf("unknown opcode string mismatch: got %q", unknown.String())
	}
	if unknown.OperandCount() != 0 {
		t.Errorf("unknown opcode operand count should be 0, got %d", unknown.OperandCount())
	}
}

func TestValTypeStringCoverage(t *testing.T) {
	valTypes := []struct {
		vt       ir.ValType
		expected string
	}{
		{ir.I32, "i32"},
		{ir.I64, "i64"},
		{ir.F32, "f32"},
		{ir.F64, "f64"},
		{ir.Bytes, "bytes"},
		{ir.ValType(99), "valtype(99)"},
	}

	for _, item := range valTypes {
		if str := item.vt.String(); str != item.expected {
			t.Errorf("ValType %d string mismatch: expected %q, got %q", uint8(item.vt), item.expected, str)
		}
	}
}

func TestConfLevelStringCoverage(t *testing.T) {
	levels := []struct {
		conf     ir.ConfLevel
		expected string
	}{
		{ir.ConfLow, "low"},
		{ir.ConfStandard, "standard"},
		{ir.ConfHigh, "high"},
		{ir.ConfCritical, "critical"},
		{ir.ConfLevel(42), "conflevel(42)"},
	}

	for _, item := range levels {
		if str := item.conf.String(); str != item.expected {
			t.Errorf("ConfLevel %d string mismatch: expected %q, got %q", uint8(item.conf), item.expected, str)
		}
	}
}

func TestTextOutput(t *testing.T) {
	t.Run("NilModule", func(t *testing.T) {
		text := ir.Text(nil)
		if text != "<nil module>\n" {
			t.Errorf("expected <nil module>\\n, got %q", text)
		}
	})

	t.Run("FullModuleDump", func(t *testing.T) {
		mod := createTestModule()
		text := ir.Text(mod)

		expectedSubstrings := []string{
			"module {",
			"version: 1",
			"compiler: \"aegisc-0.1.0\"",
			"type 0: func(i32, i32) -> (i32)",
			"import 0: \"env\".\"log_message\"",
			"memory: initial=1 pages, max=4 pages",
			"func 0 $add_numbers",
			"locals: [i32]",
			"local.get      0",
			"i32.add",
			"call           0 ($add_numbers)",
			"call_import    0 ($env.log_message)",
			"export 0: \"add\" -> func 0 ($add_numbers)",
			"export 2: \"ext_log\" -> func 2",
		}

		for _, sub := range expectedSubstrings {
			if !strings.Contains(text, sub) {
				t.Errorf("text output missing expected substring %q. Output was:\n%s", sub, text)
			}
		}
	})
}

func TestDecodeErrors(t *testing.T) {
	t.Run("DataTooShort", func(t *testing.T) {
		_, err := ir.Decode([]byte{1, 2, 3})
		if err == nil || !strings.Contains(err.Error(), "binary data too short") {
			t.Errorf("expected data too short error, got: %v", err)
		}
	})

	t.Run("InvalidMagic", func(t *testing.T) {
		buf := make([]byte, 40)
		copy(buf[:4], "WASM") // wrong magic
		_, err := ir.Decode(buf)
		if err == nil || !strings.Contains(err.Error(), "invalid magic header") {
			t.Errorf("expected invalid magic error, got: %v", err)
		}
	})

	t.Run("UnsupportedVersion", func(t *testing.T) {
		buf := make([]byte, 40)
		copy(buf[:4], ir.Magic)
		buf[4] = 99 // version 99
		_, err := ir.Decode(buf)
		if err == nil || !strings.Contains(err.Error(), "unsupported format version") {
			t.Errorf("expected unsupported version error, got: %v", err)
		}
	})

	t.Run("TruncatedSectionHeader", func(t *testing.T) {
		buf := make([]byte, 42) // 40 header + 2 bytes (needs at least 5 for section header)
		copy(buf[:4], ir.Magic)
		buf[4] = ir.Version
		_, err := ir.Decode(buf)
		if err == nil || !strings.Contains(err.Error(), "truncated section header") {
			t.Errorf("expected truncated section header error, got: %v", err)
		}
	})

	t.Run("TruncatedSectionPayload", func(t *testing.T) {
		buf := make([]byte, 45) // 40 header + 1 section ID + 4 length
		copy(buf[:4], ir.Magic)
		buf[4] = ir.Version
		buf[40] = ir.SectionType
		buf[41] = 100 // claims 100 bytes length, but only 0 available
		_, err := ir.Decode(buf)
		if err == nil || !strings.Contains(err.Error(), "truncated section") {
			t.Errorf("expected truncated section error, got: %v", err)
		}
	})

	t.Run("UnknownSectionID", func(t *testing.T) {
		buf := make([]byte, 45)
		copy(buf[:4], ir.Magic)
		buf[4] = ir.Version
		buf[40] = 0xFE // unknown section ID
		buf[41] = 0    // length 0
		_, err := ir.Decode(buf)
		if err == nil || !strings.Contains(err.Error(), "unknown section id") {
			t.Errorf("expected unknown section id error, got: %v", err)
		}
	})
}

func TestEncodeErrors(t *testing.T) {
	t.Run("NilModule", func(t *testing.T) {
		_, err := ir.Encode(nil)
		if err == nil || !strings.Contains(err.Error(), "cannot encode nil module") {
			t.Errorf("expected nil module error, got: %v", err)
		}
	})

	t.Run("OversizedImportString", func(t *testing.T) {
		mod := &ir.Module{
			Version: ir.Version,
			Imports: []ir.Import{
				{Module: strings.Repeat("A", 70000), Name: "test", Type: 0},
			},
		}
		_, err := ir.Encode(mod)
		if err == nil || !strings.Contains(err.Error(), "string exceeds maximum length") {
			t.Errorf("expected string exceeds length error, got: %v", err)
		}
	})
}

func TestValidationErrorFormatting(t *testing.T) {
	t.Run("ZeroErrors", func(t *testing.T) {
		ve := &ir.ValidationError{Errors: nil}
		if ve.Error() != "ir: validation failed" {
			t.Errorf("expected 'ir: validation failed', got %q", ve.Error())
		}
	})

	t.Run("SingleError", func(t *testing.T) {
		ve := &ir.ValidationError{Errors: []string{"ir: single failure"}}
		if ve.Error() != "ir: single failure" {
			t.Errorf("expected 'ir: single failure', got %q", ve.Error())
		}
	})

	t.Run("MultipleErrors", func(t *testing.T) {
		ve := &ir.ValidationError{Errors: []string{"ir: err 1", "ir: err 2"}}
		if !strings.Contains(ve.Error(), "with 2 errors") {
			t.Errorf("expected header with count, got %q", ve.Error())
		}
	})
}

