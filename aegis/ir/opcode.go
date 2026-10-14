package ir

import "fmt"

// Opcode represents a single instruction operation code in Aegis IR.
type Opcode uint16

// Opcode definitions for Aegis IR.
const (
	// Control flow (0x00 - 0x0B)
	OpNop        Opcode = 0x00
	OpBlock      Opcode = 0x01
	OpLoop       Opcode = 0x02
	OpIf         Opcode = 0x03
	OpElse       Opcode = 0x04
	OpEnd        Opcode = 0x05
	OpBr         Opcode = 0x06
	OpBrIf       Opcode = 0x07
	OpBrTable    Opcode = 0x08
	OpReturn     Opcode = 0x09
	OpCall       Opcode = 0x0A
	OpCallImport Opcode = 0x0B

	// Stack operations (0x10 - 0x12)
	OpLocalGet Opcode = 0x10
	OpLocalSet Opcode = 0x11
	OpLocalTee Opcode = 0x12

	// Memory operations (0x20 - 0x29)
	OpLoad32     Opcode = 0x20
	OpLoad64     Opcode = 0x21
	OpStore32    Opcode = 0x22
	OpStore64    Opcode = 0x23
	OpLoadF32    Opcode = 0x24
	OpLoadF64    Opcode = 0x25
	OpStoreF32   Opcode = 0x26
	OpStoreF64   Opcode = 0x27
	OpMemorySize Opcode = 0x28
	OpMemoryGrow Opcode = 0x29

	// Constants (0x30 - 0x33)
	OpI32Const Opcode = 0x30
	OpI64Const Opcode = 0x31
	OpF32Const Opcode = 0x32
	OpF64Const Opcode = 0x33

	// i32 arithmetic (0x40 - 0x4C)
	OpI32Add  Opcode = 0x40
	OpI32Sub  Opcode = 0x41
	OpI32Mul  Opcode = 0x42
	OpI32DivS Opcode = 0x43
	OpI32DivU Opcode = 0x44
	OpI32RemS Opcode = 0x45
	OpI32RemU Opcode = 0x46
	OpI32And  Opcode = 0x47
	OpI32Or   Opcode = 0x48
	OpI32Xor  Opcode = 0x49
	OpI32Shl  Opcode = 0x4A
	OpI32ShrS Opcode = 0x4B
	OpI32ShrU Opcode = 0x4C

	// i32 comparison (0x50 - 0x5A)
	OpI32Eqz Opcode = 0x50
	OpI32Eq  Opcode = 0x51
	OpI32Ne  Opcode = 0x52
	OpI32LtS Opcode = 0x53
	OpI32LtU Opcode = 0x54
	OpI32GtS Opcode = 0x55
	OpI32GtU Opcode = 0x56
	OpI32LeS Opcode = 0x57
	OpI32LeU Opcode = 0x58
	OpI32GeS Opcode = 0x59
	OpI32GeU Opcode = 0x5A

	// i64 arithmetic (0x60 - 0x6C)
	OpI64Add  Opcode = 0x60
	OpI64Sub  Opcode = 0x61
	OpI64Mul  Opcode = 0x62
	OpI64DivS Opcode = 0x63
	OpI64DivU Opcode = 0x64
	OpI64RemS Opcode = 0x65
	OpI64RemU Opcode = 0x66
	OpI64And  Opcode = 0x67
	OpI64Or   Opcode = 0x68
	OpI64Xor  Opcode = 0x69
	OpI64Shl  Opcode = 0x6A
	OpI64ShrS Opcode = 0x6B
	OpI64ShrU Opcode = 0x6C

	// i64 comparison (0x70 - 0x7A)
	OpI64Eqz Opcode = 0x70
	OpI64Eq  Opcode = 0x71
	OpI64Ne  Opcode = 0x72
	OpI64LtS Opcode = 0x73
	OpI64LtU Opcode = 0x74
	OpI64GtS Opcode = 0x75
	OpI64GtU Opcode = 0x76
	OpI64LeS Opcode = 0x77
	OpI64LeU Opcode = 0x78
	OpI64GeS Opcode = 0x79
	OpI64GeU Opcode = 0x7A

	// f64 arithmetic (0x80 - 0x83)
	OpF64Add Opcode = 0x80
	OpF64Sub Opcode = 0x81
	OpF64Mul Opcode = 0x82
	OpF64Div Opcode = 0x83

	// f64 comparison (0x84 - 0x89)
	OpF64Eq Opcode = 0x84
	OpF64Ne Opcode = 0x85
	OpF64Lt Opcode = 0x86
	OpF64Gt Opcode = 0x87
	OpF64Le Opcode = 0x88
	OpF64Ge Opcode = 0x89

	// Conversions (0x90 - 0x95)
	OpI32WrapI64     Opcode = 0x90
	OpI64ExtendI32S  Opcode = 0x91
	OpI64ExtendI32U  Opcode = 0x92
	OpF64ConvertI32S Opcode = 0x93
	OpF64ConvertI64S Opcode = 0x94
	OpI32TruncF64S   Opcode = 0x95

	// Trap (0xFF)
	OpTrap Opcode = 0xFF
)

// Aliases matching the specification names directly.
const (
	Nop        = OpNop
	Block      = OpBlock
	Loop       = OpLoop
	If         = OpIf
	Else       = OpElse
	End        = OpEnd
	Br         = OpBr
	BrIf       = OpBrIf
	BrTable    = OpBrTable
	Return     = OpReturn
	Call       = OpCall
	CallImport = OpCallImport

	LocalGet = OpLocalGet
	LocalSet = OpLocalSet
	LocalTee = OpLocalTee

	Load32     = OpLoad32
	Load64     = OpLoad64
	Store32    = OpStore32
	Store64    = OpStore64
	LoadF32    = OpLoadF32
	LoadF64    = OpLoadF64
	StoreF32   = OpStoreF32
	StoreF64   = OpStoreF64
	MemorySize = OpMemorySize
	MemoryGrow = OpMemoryGrow

	I32Const = OpI32Const
	I64Const = OpI64Const
	F32Const = OpF32Const
	F64Const = OpF64Const

	I32Add  = OpI32Add
	I32Sub  = OpI32Sub
	I32Mul  = OpI32Mul
	I32DivS = OpI32DivS
	I32DivU = OpI32DivU
	I32RemS = OpI32RemS
	I32RemU = OpI32RemU
	I32And  = OpI32And
	I32Or   = OpI32Or
	I32Xor  = OpI32Xor
	I32Shl  = OpI32Shl
	I32ShrS = OpI32ShrS
	I32ShrU = OpI32ShrU

	I32Eqz = OpI32Eqz
	I32Eq  = OpI32Eq
	I32Ne  = OpI32Ne
	I32LtS = OpI32LtS
	I32LtU = OpI32LtU
	I32GtS = OpI32GtS
	I32GtU = OpI32GtU
	I32LeS = OpI32LeS
	I32LeU = OpI32LeU
	I32GeS = OpI32GeS
	I32GeU = OpI32GeU

	I64Add  = OpI64Add
	I64Sub  = OpI64Sub
	I64Mul  = OpI64Mul
	I64DivS = OpI64DivS
	I64DivU = OpI64DivU
	I64RemS = OpI64RemS
	I64RemU = OpI64RemU
	I64And  = OpI64And
	I64Or   = OpI64Or
	I64Xor  = OpI64Xor
	I64Shl  = OpI64Shl
	I64ShrS = OpI64ShrS
	I64ShrU = OpI64ShrU

	I64Eqz = OpI64Eqz
	I64Eq  = OpI64Eq
	I64Ne  = OpI64Ne
	I64LtS = OpI64LtS
	I64LtU = OpI64LtU
	I64GtS = OpI64GtS
	I64GtU = OpI64GtU
	I64LeS = OpI64LeS
	I64LeU = OpI64LeU
	I64GeS = OpI64GeS
	I64GeU = OpI64GeU

	F64Add = OpF64Add
	F64Sub = OpF64Sub
	F64Mul = OpF64Mul
	F64Div = OpF64Div

	F64Eq = OpF64Eq
	F64Ne = OpF64Ne
	F64Lt = OpF64Lt
	F64Gt = OpF64Gt
	F64Le = OpF64Le
	F64Ge = OpF64Ge

	I32WrapI64     = OpI32WrapI64
	I64ExtendI32S  = OpI64ExtendI32S
	I64ExtendI32U  = OpI64ExtendI32U
	F64ConvertI32S = OpF64ConvertI32S
	F64ConvertI64S = OpF64ConvertI64S
	I32TruncF64S   = OpI32TruncF64S

	Trap = OpTrap
)

// String returns the mnemonic representation of the opcode.
func (op Opcode) String() string {
	switch op {
	case OpNop:
		return "nop"
	case OpBlock:
		return "block"
	case OpLoop:
		return "loop"
	case OpIf:
		return "if"
	case OpElse:
		return "else"
	case OpEnd:
		return "end"
	case OpBr:
		return "br"
	case OpBrIf:
		return "br_if"
	case OpBrTable:
		return "br_table"
	case OpReturn:
		return "return"
	case OpCall:
		return "call"
	case OpCallImport:
		return "call_import"

	case OpLocalGet:
		return "local.get"
	case OpLocalSet:
		return "local.set"
	case OpLocalTee:
		return "local.tee"

	case OpLoad32:
		return "load32"
	case OpLoad64:
		return "load64"
	case OpStore32:
		return "store32"
	case OpStore64:
		return "store64"
	case OpLoadF32:
		return "load_f32"
	case OpLoadF64:
		return "load_f64"
	case OpStoreF32:
		return "store_f32"
	case OpStoreF64:
		return "store_f64"
	case OpMemorySize:
		return "memory.size"
	case OpMemoryGrow:
		return "memory.grow"

	case OpI32Const:
		return "i32.const"
	case OpI64Const:
		return "i64.const"
	case OpF32Const:
		return "f32.const"
	case OpF64Const:
		return "f64.const"

	case OpI32Add:
		return "i32.add"
	case OpI32Sub:
		return "i32.sub"
	case OpI32Mul:
		return "i32.mul"
	case OpI32DivS:
		return "i32.div_s"
	case OpI32DivU:
		return "i32.div_u"
	case OpI32RemS:
		return "i32.rem_s"
	case OpI32RemU:
		return "i32.rem_u"
	case OpI32And:
		return "i32.and"
	case OpI32Or:
		return "i32.or"
	case OpI32Xor:
		return "i32.xor"
	case OpI32Shl:
		return "i32.shl"
	case OpI32ShrS:
		return "i32.shr_s"
	case OpI32ShrU:
		return "i32.shr_u"

	case OpI32Eqz:
		return "i32.eqz"
	case OpI32Eq:
		return "i32.eq"
	case OpI32Ne:
		return "i32.ne"
	case OpI32LtS:
		return "i32.lt_s"
	case OpI32LtU:
		return "i32.lt_u"
	case OpI32GtS:
		return "i32.gt_s"
	case OpI32GtU:
		return "i32.gt_u"
	case OpI32LeS:
		return "i32.le_s"
	case OpI32LeU:
		return "i32.le_u"
	case OpI32GeS:
		return "i32.ge_s"
	case OpI32GeU:
		return "i32.ge_u"

	case OpI64Add:
		return "i64.add"
	case OpI64Sub:
		return "i64.sub"
	case OpI64Mul:
		return "i64.mul"
	case OpI64DivS:
		return "i64.div_s"
	case OpI64DivU:
		return "i64.div_u"
	case OpI64RemS:
		return "i64.rem_s"
	case OpI64RemU:
		return "i64.rem_u"
	case OpI64And:
		return "i64.and"
	case OpI64Or:
		return "i64.or"
	case OpI64Xor:
		return "i64.xor"
	case OpI64Shl:
		return "i64.shl"
	case OpI64ShrS:
		return "i64.shr_s"
	case OpI64ShrU:
		return "i64.shr_u"

	case OpI64Eqz:
		return "i64.eqz"
	case OpI64Eq:
		return "i64.eq"
	case OpI64Ne:
		return "i64.ne"
	case OpI64LtS:
		return "i64.lt_s"
	case OpI64LtU:
		return "i64.lt_u"
	case OpI64GtS:
		return "i64.gt_s"
	case OpI64GtU:
		return "i64.gt_u"
	case OpI64LeS:
		return "i64.le_s"
	case OpI64LeU:
		return "i64.le_u"
	case OpI64GeS:
		return "i64.ge_s"
	case OpI64GeU:
		return "i64.ge_u"

	case OpF64Add:
		return "f64.add"
	case OpF64Sub:
		return "f64.sub"
	case OpF64Mul:
		return "f64.mul"
	case OpF64Div:
		return "f64.div"

	case OpF64Eq:
		return "f64.eq"
	case OpF64Ne:
		return "f64.ne"
	case OpF64Lt:
		return "f64.lt"
	case OpF64Gt:
		return "f64.gt"
	case OpF64Le:
		return "f64.le"
	case OpF64Ge:
		return "f64.ge"

	case OpI32WrapI64:
		return "i32.wrap_i64"
	case OpI64ExtendI32S:
		return "i64.extend_i32_s"
	case OpI64ExtendI32U:
		return "i64.extend_i32_u"
	case OpF64ConvertI32S:
		return "f64.convert_i32_s"
	case OpF64ConvertI64S:
		return "f64.convert_i64_s"
	case OpI32TruncF64S:
		return "i32.trunc_f64_s"

	case OpTrap:
		return "trap"

	default:
		return fmt.Sprintf("opcode(0x%02X)", uint16(op))
	}
}

// OperandCount returns the number of immediate operands expected by this opcode.
func (op Opcode) OperandCount() int {
	switch op {
	case OpBr, OpBrIf, OpBrTable, OpCall, OpCallImport:
		return 1
	case OpLocalGet, OpLocalSet, OpLocalTee:
		return 1
	case OpLoad32, OpLoad64, OpStore32, OpStore64,
		OpLoadF32, OpLoadF64, OpStoreF32, OpStoreF64:
		return 1
	case OpI32Const, OpI64Const, OpF32Const, OpF64Const:
		return 1
	default:
		return 0
	}
}
