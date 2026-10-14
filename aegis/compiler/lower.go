package compiler

import (
	"fmt"
	"go/ast"
	"go/token"
	"math"
	"strconv"
	"strings"

	"github.com/oarkflow/ref/aegis/ir"
)

type blockKind int

const (
	kindBlock blockKind = iota
	kindLoop
	kindIf
)

// controlBlock records an active structured control block in the compilation context.
type controlBlock struct {
	kind        blockKind
	isLoopOuter bool
	postStmt    ast.Stmt
}

// localInfo records the allocation index and value type of a local variable.
type localInfo struct {
	index   uint32
	valType ir.ValType
}

// funcLowerer maintains state while translating a single Go function AST into Aegis IR bytecode.
type funcLowerer struct {
	compiler     *Compiler
	module       *ir.Module
	fdecl        *ast.FuncDecl
	funcIndex    uint32
	fset         *token.FileSet
	funcMap      map[string]uint32
	locals       map[string]localInfo
	localTypes   []ir.ValType
	paramCount   int
	scratchIdx   int
	hasScratch   bool
	controlStack []controlBlock
	body         []ir.Instruction
	warnings     []string
}

// lowerFunction lowers a Go AST function declaration into an Aegis IR Function.
func lowerFunction(
	compiler *Compiler,
	module *ir.Module,
	fdecl *ast.FuncDecl,
	funcIndex uint32,
	fset *token.FileSet,
	funcMap map[string]uint32,
) error {
	fl := &funcLowerer{
		compiler:     compiler,
		module:       module,
		fdecl:        fdecl,
		funcIndex:    funcIndex,
		fset:         fset,
		funcMap:      funcMap,
		locals:       make(map[string]localInfo),
		localTypes:   make([]ir.ValType, 0, 8),
		controlStack: make([]controlBlock, 0, 8),
		body:         make([]ir.Instruction, 0, 32),
	}

	// 1. Register function parameters as initial locals (0 .. paramCount-1)
	if fdecl.Type.Params != nil {
		for _, field := range fdecl.Type.Params.List {
			vt, err := resolveValType(field.Type)
			if err != nil {
				return err
			}
			for _, name := range field.Names {
				fl.locals[name.Name] = localInfo{
					index:   uint32(fl.paramCount),
					valType: vt,
				}
				fl.paramCount++
			}
		}
	}

	// 2. Lower body statements
	if fdecl.Body != nil {
		for _, stmt := range fdecl.Body.List {
			if err := fl.lowerStmt(stmt); err != nil {
				return err
			}
		}
	}

	// 3. Ensure function ends with OpReturn
	if len(fl.body) == 0 || fl.body[len(fl.body)-1].Op != ir.OpReturn {
		fnType := module.Types[module.Functions[funcIndex].TypeIndex]
		// Emit zero return values if return arity > 0
		for _, retType := range fnType.Results {
			switch retType {
			case ir.I64:
				fl.emit(ir.OpI64Const, 0)
			case ir.F64:
				fl.emit(ir.OpF64Const, 0)
			case ir.F32:
				fl.emit(ir.OpF32Const, 0)
			default:
				fl.emit(ir.OpI32Const, 0)
			}
		}
		fl.emit(ir.OpReturn, 0)
	}

	// 4. Update function definition
	module.Functions[funcIndex].Locals = fl.localTypes
	module.Functions[funcIndex].Body = fl.body

	return nil
}

// emit appends an instruction to the function body.
func (fl *funcLowerer) emit(op ir.Opcode, operands ...uint64) {
	var ops [3]uint64
	for i, opVal := range operands {
		if i < 3 {
			ops[i] = opVal
		}
	}
	fl.body = append(fl.body, ir.Instruction{
		Op:       op,
		Operands: ops,
	})
}

// allocLocal registers a new local variable slot and returns its index.
func (fl *funcLowerer) allocLocal(name string, vt ir.ValType) uint32 {
	idx := uint32(fl.paramCount + len(fl.localTypes))
	fl.localTypes = append(fl.localTypes, vt)
	fl.locals[name] = localInfo{
		index:   idx,
		valType: vt,
	}
	return idx
}

// getScratchLocal returns a reusable scratch local variable for discarding unused values.
func (fl *funcLowerer) getScratchLocal() uint32 {
	if !fl.hasScratch {
		fl.scratchIdx = fl.paramCount + len(fl.localTypes)
		fl.localTypes = append(fl.localTypes, ir.I32)
		fl.hasScratch = true
	}
	return uint32(fl.scratchIdx)
}

// pushControl pushes a control block onto the active control stack.
func (fl *funcLowerer) pushControl(kind blockKind, isLoopOuter bool, postStmt ast.Stmt) {
	fl.controlStack = append(fl.controlStack, controlBlock{
		kind:        kind,
		isLoopOuter: isLoopOuter,
		postStmt:    postStmt,
	})
}

// popControl removes the topmost control block from the control stack.
func (fl *funcLowerer) popControl() {
	if len(fl.controlStack) > 0 {
		fl.controlStack = fl.controlStack[:len(fl.controlStack)-1]
	}
}

// findBreakDepth finds the relative branch depth to the innermost loop's outer block.
func (fl *funcLowerer) findBreakDepth() int {
	for i := len(fl.controlStack) - 1; i >= 0; i-- {
		if fl.controlStack[i].isLoopOuter {
			return len(fl.controlStack) - 1 - i
		}
	}
	return -1
}

// findContinueBlockAndDepth locates the innermost loop block and its relative depth.
func (fl *funcLowerer) findContinueBlockAndDepth() (*controlBlock, int) {
	for i := len(fl.controlStack) - 1; i >= 0; i-- {
		if fl.controlStack[i].kind == kindLoop {
			return &fl.controlStack[i], len(fl.controlStack) - 1 - i
		}
	}
	return nil, -1
}

// findContinueDepth finds the relative branch depth to the innermost loop.
func (fl *funcLowerer) findContinueDepth() int {
	_, depth := fl.findContinueBlockAndDepth()
	return depth
}

// lowerStmt translates an AST statement into Aegis IR bytecode.
func (fl *funcLowerer) lowerStmt(stmt ast.Stmt) error {
	if stmt == nil {
		return nil
	}

	switch s := stmt.(type) {
	case *ast.AssignStmt:
		return fl.lowerAssignStmt(s)

	case *ast.IncDecStmt:
		return fl.lowerIncDecStmt(s)

	case *ast.DeclStmt:
		return fl.lowerDeclStmt(s)

	case *ast.ReturnStmt:
		return fl.lowerReturnStmt(s)

	case *ast.IfStmt:
		return fl.lowerIfStmt(s)

	case *ast.ForStmt:
		return fl.lowerForStmt(s)

	case *ast.BranchStmt:
		return fl.lowerBranchStmt(s)

	case *ast.ExprStmt:
		vt, err := fl.lowerExpr(s.X, ir.TypeInvalid)
		if err != nil {
			return err
		}
		// If the expression pushed a result onto the stack, pop it into a scratch local
		if vt != ir.TypeInvalid {
			scratch := fl.getScratchLocal()
			fl.emit(ir.OpLocalSet, uint64(scratch))
		}
		return nil

	case *ast.BlockStmt:
		for _, child := range s.List {
			if err := fl.lowerStmt(child); err != nil {
				return err
			}
		}
		return nil

	case *ast.EmptyStmt:
		return nil

	default:
		pos := fl.fset.Position(stmt.Pos())
		return fmt.Errorf("compiler: unsupported statement %T (line %d)", stmt, pos.Line)
	}
}

// lowerAssignStmt translates assignment statements (:=, =, +=, -=, etc.).
func (fl *funcLowerer) lowerAssignStmt(stmt *ast.AssignStmt) error {
	switch stmt.Tok {
	case token.DEFINE: // :=
		for i, lhs := range stmt.Lhs {
			ident, ok := lhs.(*ast.Ident)
			if !ok {
				return fmt.Errorf("compiler: complex LHS in definition assignment not supported")
			}
			rhs := stmt.Rhs[i]
			vt := fl.deduceType(rhs, ir.I32)
			localIdx := fl.allocLocal(ident.Name, vt)
			if _, err := fl.lowerExpr(rhs, vt); err != nil {
				return err
			}
			fl.emit(ir.OpLocalSet, uint64(localIdx))
		}
		return nil

	case token.ASSIGN: // =
		for i, lhs := range stmt.Lhs {
			ident, ok := lhs.(*ast.Ident)
			if !ok {
				return fmt.Errorf("compiler: complex LHS in assignment not supported")
			}
			info, exists := fl.locals[ident.Name]
			if !exists {
				return fmt.Errorf("compiler: undefined variable %q (line %d)", ident.Name, fl.fset.Position(lhs.Pos()).Line)
			}
			rhs := stmt.Rhs[i]
			if _, err := fl.lowerExpr(rhs, info.valType); err != nil {
				return err
			}
			fl.emit(ir.OpLocalSet, uint64(info.index))
		}
		return nil

	default: // Compound assignment: +=, -=, *=, /=, %=, etc.
		if len(stmt.Lhs) != 1 || len(stmt.Rhs) != 1 {
			return fmt.Errorf("compiler: multiple compound assignment not supported")
		}
		ident, ok := stmt.Lhs[0].(*ast.Ident)
		if !ok {
			return fmt.Errorf("compiler: compound assignment to non-identifier not supported")
		}
		info, exists := fl.locals[ident.Name]
		if !exists {
			return fmt.Errorf("compiler: undefined variable %q (line %d)", ident.Name, fl.fset.Position(stmt.Lhs[0].Pos()).Line)
		}

		// 1. Push current variable value
		fl.emit(ir.OpLocalGet, uint64(info.index))

		// 2. Evaluate RHS
		if _, err := fl.lowerExpr(stmt.Rhs[0], info.valType); err != nil {
			return err
		}

		// 3. Emit binary operation
		binTok := binaryTokenForCompound(stmt.Tok)
		if err := fl.emitBinaryOp(binTok, info.valType); err != nil {
			return err
		}

		// 4. Store back to local
		fl.emit(ir.OpLocalSet, uint64(info.index))
		return nil
	}
}

// binaryTokenForCompound maps a compound assignment token to its corresponding binary operator token.
func binaryTokenForCompound(tok token.Token) token.Token {
	switch tok {
	case token.ADD_ASSIGN:
		return token.ADD
	case token.SUB_ASSIGN:
		return token.SUB
	case token.MUL_ASSIGN:
		return token.MUL
	case token.QUO_ASSIGN:
		return token.QUO
	case token.REM_ASSIGN:
		return token.REM
	case token.AND_ASSIGN:
		return token.AND
	case token.OR_ASSIGN:
		return token.OR
	case token.XOR_ASSIGN:
		return token.XOR
	case token.SHL_ASSIGN:
		return token.SHL
	case token.SHR_ASSIGN:
		return token.SHR
	default:
		return token.ILLEGAL
	}
}

// lowerIncDecStmt translates increment and decrement statements (x++, x--).
func (fl *funcLowerer) lowerIncDecStmt(stmt *ast.IncDecStmt) error {
	ident, ok := stmt.X.(*ast.Ident)
	if !ok {
		return fmt.Errorf("compiler: inc/dec on non-identifier not supported")
	}
	info, exists := fl.locals[ident.Name]
	if !exists {
		return fmt.Errorf("compiler: undefined variable %q", ident.Name)
	}

	fl.emit(ir.OpLocalGet, uint64(info.index))

	if info.valType == ir.I64 {
		fl.emit(ir.OpI64Const, 1)
		if stmt.Tok == token.INC {
			fl.emit(ir.OpI64Add, 0)
		} else {
			fl.emit(ir.OpI64Sub, 0)
		}
	} else {
		fl.emit(ir.OpI32Const, 1)
		if stmt.Tok == token.INC {
			fl.emit(ir.OpI32Add, 0)
		} else {
			fl.emit(ir.OpI32Sub, 0)
		}
	}

	fl.emit(ir.OpLocalSet, uint64(info.index))
	return nil
}

// lowerDeclStmt translates variable declaration statements (`var x int32 = ...`).
func (fl *funcLowerer) lowerDeclStmt(stmt *ast.DeclStmt) error {
	genDecl, ok := stmt.Decl.(*ast.GenDecl)
	if !ok || genDecl.Tok != token.VAR {
		return nil
	}

	for _, spec := range genDecl.Specs {
		valSpec, ok := spec.(*ast.ValueSpec)
		if !ok {
			continue
		}

		var specifiedType ir.ValType = ir.TypeInvalid
		if valSpec.Type != nil {
			vt, err := resolveValType(valSpec.Type)
			if err != nil {
				return err
			}
			specifiedType = vt
		}

		for i, name := range valSpec.Names {
			var rhs ast.Expr
			if i < len(valSpec.Values) {
				rhs = valSpec.Values[i]
			}

			vt := specifiedType
			if vt == ir.TypeInvalid && rhs != nil {
				vt = fl.deduceType(rhs, ir.I32)
			}
			if vt == ir.TypeInvalid {
				vt = ir.I32
			}

			localIdx := fl.allocLocal(name.Name, vt)

			if rhs != nil {
				if _, err := fl.lowerExpr(rhs, vt); err != nil {
					return err
				}
				fl.emit(ir.OpLocalSet, uint64(localIdx))
			} else {
				// Zero initialization
				switch vt {
				case ir.I64:
					fl.emit(ir.OpI64Const, 0)
				case ir.F64:
					fl.emit(ir.OpF64Const, 0)
				case ir.F32:
					fl.emit(ir.OpF32Const, 0)
				default:
					fl.emit(ir.OpI32Const, 0)
				}
				fl.emit(ir.OpLocalSet, uint64(localIdx))
			}
		}
	}

	return nil
}

// lowerReturnStmt translates return statements.
func (fl *funcLowerer) lowerReturnStmt(stmt *ast.ReturnStmt) error {
	fnType := fl.module.Types[fl.module.Functions[fl.funcIndex].TypeIndex]

	for i, resExpr := range stmt.Results {
		expectedType := ir.I32
		if i < len(fnType.Results) {
			expectedType = fnType.Results[i]
		}
		if _, err := fl.lowerExpr(resExpr, expectedType); err != nil {
			return err
		}
	}

	fl.emit(ir.OpReturn, 0)
	return nil
}

// lowerIfStmt translates if/else statements.
func (fl *funcLowerer) lowerIfStmt(stmt *ast.IfStmt) error {
	if stmt.Init != nil {
		if err := fl.lowerStmt(stmt.Init); err != nil {
			return err
		}
	}

	// Condition expression must evaluate to an i32 (0 or non-zero)
	if _, err := fl.lowerExpr(stmt.Cond, ir.I32); err != nil {
		return err
	}

	fl.emit(ir.OpIf, 0)
	fl.pushControl(kindIf, false, nil)

	for _, s := range stmt.Body.List {
		if err := fl.lowerStmt(s); err != nil {
			return err
		}
	}

	if stmt.Else != nil {
		fl.emit(ir.OpElse, 0)
		switch elseStmt := stmt.Else.(type) {
		case *ast.BlockStmt:
			for _, s := range elseStmt.List {
				if err := fl.lowerStmt(s); err != nil {
					return err
				}
			}
		case *ast.IfStmt:
			if err := fl.lowerIfStmt(elseStmt); err != nil {
				return err
			}
		default:
			return fmt.Errorf("compiler: unsupported else statement %T", stmt.Else)
		}
	}

	fl.popControl()
	fl.emit(ir.OpEnd, 0)
	return nil
}

// lowerForStmt translates standard for loops into structured OpBlock and OpLoop.
func (fl *funcLowerer) lowerForStmt(stmt *ast.ForStmt) error {
	if stmt.Init != nil {
		if err := fl.lowerStmt(stmt.Init); err != nil {
			return err
		}
	}

	// 1. Outer block for loop breakout (target of break)
	fl.emit(ir.OpBlock, 0)
	fl.pushControl(kindBlock, true, nil)

	// 2. Loop block for loop continuation (target of continue)
	fl.emit(ir.OpLoop, 0)
	fl.pushControl(kindLoop, false, stmt.Post)

	// 3. Condition check: if condition is false, break out to outer block
	if stmt.Cond != nil {
		if _, err := fl.lowerExpr(stmt.Cond, ir.I32); err != nil {
			return err
		}
		// Invert condition: if cond == 0, branch to outer block
		fl.emit(ir.OpI32Eqz, 0)
		breakDepth := fl.findBreakDepth()
		fl.emit(ir.OpBrIf, uint64(breakDepth))
	}

	// 4. Loop body statements
	for _, s := range stmt.Body.List {
		if err := fl.lowerStmt(s); err != nil {
			return err
		}
	}

	// 5. Post iteration step (e.g., i++)
	if stmt.Post != nil {
		if err := fl.lowerStmt(stmt.Post); err != nil {
			return err
		}
	}

	// 6. Branch back to start of loop (depth 0 relative to loop)
	continueDepth := fl.findContinueDepth()
	fl.emit(ir.OpBr, uint64(continueDepth))

	// 7. Close OpLoop and OpBlock
	fl.popControl()
	fl.emit(ir.OpEnd, 0)

	fl.popControl()
	fl.emit(ir.OpEnd, 0)

	return nil
}

// lowerBranchStmt translates break and continue statements.
func (fl *funcLowerer) lowerBranchStmt(stmt *ast.BranchStmt) error {
	switch stmt.Tok {
	case token.BREAK:
		depth := fl.findBreakDepth()
		if depth < 0 {
			pos := fl.fset.Position(stmt.Pos())
			return fmt.Errorf("compiler: break statement outside of loop (line %d)", pos.Line)
		}
		fl.emit(ir.OpBr, uint64(depth))
		return nil

	case token.CONTINUE:
		loopBlock, depth := fl.findContinueBlockAndDepth()
		if depth < 0 || loopBlock == nil {
			pos := fl.fset.Position(stmt.Pos())
			return fmt.Errorf("compiler: continue statement outside of loop (line %d)", pos.Line)
		}
		if loopBlock.postStmt != nil {
			if err := fl.lowerStmt(loopBlock.postStmt); err != nil {
				return err
			}
		}
		fl.emit(ir.OpBr, uint64(depth))
		return nil

	default:
		pos := fl.fset.Position(stmt.Pos())
		return fmt.Errorf("compiler: unsupported branch %s (line %d)", stmt.Tok, pos.Line)
	}
}

// lowerExpr translates an expression into instructions that place its value on the stack.
func (fl *funcLowerer) lowerExpr(expr ast.Expr, targetType ir.ValType) (ir.ValType, error) {
	if expr == nil {
		return ir.TypeInvalid, nil
	}

	switch e := expr.(type) {
	case *ast.BasicLit:
		return fl.lowerBasicLit(e, targetType)

	case *ast.Ident:
		return fl.lowerIdent(e, targetType)

	case *ast.ParenExpr:
		return fl.lowerExpr(e.X, targetType)

	case *ast.UnaryExpr:
		return fl.lowerUnaryExpr(e, targetType)

	case *ast.BinaryExpr:
		return fl.lowerBinaryExpr(e, targetType)

	case *ast.CallExpr:
		return fl.lowerCallExpr(e, targetType)

	default:
		pos := fl.fset.Position(expr.Pos())
		return ir.TypeInvalid, fmt.Errorf("compiler: unsupported expression %T (line %d)", expr, pos.Line)
	}
}

// lowerBasicLit translates literals (integers, floats, runes, strings).
func (fl *funcLowerer) lowerBasicLit(lit *ast.BasicLit, targetType ir.ValType) (ir.ValType, error) {
	switch lit.Kind {
	case token.INT:
		val, err := strconv.ParseInt(lit.Value, 0, 64)
		if err != nil {
			uval, uerr := strconv.ParseUint(lit.Value, 0, 64)
			if uerr != nil {
				return ir.TypeInvalid, fmt.Errorf("compiler: invalid integer literal %q: %w", lit.Value, err)
			}
			val = int64(uval)
		}

		if targetType == ir.I64 {
			fl.emit(ir.OpI64Const, uint64(val))
			return ir.I64, nil
		} else if targetType == ir.F64 {
			fl.emit(ir.OpF64Const, math.Float64bits(float64(val)))
			return ir.F64, nil
		} else if targetType == ir.F32 {
			fl.emit(ir.OpF32Const, uint64(math.Float32bits(float32(val))))
			return ir.F32, nil
		} else {
			fl.emit(ir.OpI32Const, uint64(uint32(val)))
			return ir.I32, nil
		}

	case token.FLOAT:
		val, err := strconv.ParseFloat(lit.Value, 64)
		if err != nil {
			return ir.TypeInvalid, fmt.Errorf("compiler: invalid float literal %q: %w", lit.Value, err)
		}
		if targetType == ir.F32 {
			fl.emit(ir.OpF32Const, uint64(math.Float32bits(float32(val))))
			return ir.F32, nil
		}
		fl.emit(ir.OpF64Const, math.Float64bits(val))
		return ir.F64, nil

	case token.CHAR:
		unquoted, _, _, err := strconv.UnquoteChar(lit.Value[1:len(lit.Value)-1], '\'')
		if err != nil {
			return ir.TypeInvalid, fmt.Errorf("compiler: invalid char literal %q: %w", lit.Value, err)
		}
		fl.emit(ir.OpI32Const, uint64(unquoted))
		return ir.I32, nil

	case token.STRING:
		s, err := strconv.Unquote(lit.Value)
		if err != nil {
			s = lit.Value
		}
		offset := fl.compiler.addDataString(fl.module, s)
		fl.emit(ir.OpI32Const, uint64(offset))
		return ir.I32, nil

	default:
		return ir.TypeInvalid, fmt.Errorf("compiler: unsupported literal kind %s", lit.Kind)
	}
}

// lowerIdent translates identifier access (local variables, booleans, constants).
func (fl *funcLowerer) lowerIdent(ident *ast.Ident, targetType ir.ValType) (ir.ValType, error) {
	switch ident.Name {
	case "true":
		fl.emit(ir.OpI32Const, 1)
		return ir.I32, nil
	case "false", "nil":
		fl.emit(ir.OpI32Const, 0)
		return ir.I32, nil
	}

	info, exists := fl.locals[ident.Name]
	if !exists {
		return ir.TypeInvalid, fmt.Errorf("compiler: undefined variable %q (line %d)", ident.Name, fl.fset.Position(ident.Pos()).Line)
	}

	fl.emit(ir.OpLocalGet, uint64(info.index))

	// Automatic type widening if targetType requires it
	if targetType == ir.I64 && info.valType == ir.I32 {
		fl.emit(ir.OpI64ExtendI32S, 0)
		return ir.I64, nil
	} else if targetType == ir.F64 && info.valType == ir.I32 {
		fl.emit(ir.OpF64ConvertI32S, 0)
		return ir.F64, nil
	} else if targetType == ir.F64 && info.valType == ir.I64 {
		fl.emit(ir.OpF64ConvertI64S, 0)
		return ir.F64, nil
	}

	return info.valType, nil
}

// lowerUnaryExpr translates unary operators (-, +, !, ^).
func (fl *funcLowerer) lowerUnaryExpr(expr *ast.UnaryExpr, targetType ir.ValType) (ir.ValType, error) {
	switch expr.Op {
	case token.SUB:
		// Check for integer literal optimization
		if lit, ok := expr.X.(*ast.BasicLit); ok && lit.Kind == token.INT {
			val, _ := strconv.ParseInt("-"+lit.Value, 0, 64)
			if targetType == ir.I64 {
				fl.emit(ir.OpI64Const, uint64(val))
				return ir.I64, nil
			}
			fl.emit(ir.OpI32Const, uint64(uint32(val)))
			return ir.I32, nil
		}
		if lit, ok := expr.X.(*ast.BasicLit); ok && lit.Kind == token.FLOAT {
			val, _ := strconv.ParseFloat("-"+lit.Value, 64)
			fl.emit(ir.OpF64Const, math.Float64bits(val))
			return ir.F64, nil
		}

		xtype := fl.deduceType(expr.X, targetType)
		switch xtype {
		case ir.I64:
			fl.emit(ir.OpI64Const, 0)
			if _, err := fl.lowerExpr(expr.X, ir.I64); err != nil {
				return ir.TypeInvalid, err
			}
			fl.emit(ir.OpI64Sub, 0)
			return ir.I64, nil
		case ir.F64:
			fl.emit(ir.OpF64Const, math.Float64bits(0.0))
			if _, err := fl.lowerExpr(expr.X, ir.F64); err != nil {
				return ir.TypeInvalid, err
			}
			fl.emit(ir.OpF64Sub, 0)
			return ir.F64, nil
		default:
			fl.emit(ir.OpI32Const, 0)
			if _, err := fl.lowerExpr(expr.X, ir.I32); err != nil {
				return ir.TypeInvalid, err
			}
			fl.emit(ir.OpI32Sub, 0)
			return ir.I32, nil
		}

	case token.ADD:
		return fl.lowerExpr(expr.X, targetType)

	case token.NOT:
		if _, err := fl.lowerExpr(expr.X, ir.I32); err != nil {
			return ir.TypeInvalid, err
		}
		fl.emit(ir.OpI32Eqz, 0)
		return ir.I32, nil

	case token.XOR:
		xtype := fl.deduceType(expr.X, targetType)
		if _, err := fl.lowerExpr(expr.X, xtype); err != nil {
			return ir.TypeInvalid, err
		}
		if xtype == ir.I64 {
			fl.emit(ir.OpI64Const, ^uint64(0))
			fl.emit(ir.OpI64Xor, 0)
			return ir.I64, nil
		}
		fl.emit(ir.OpI32Const, uint64(^uint32(0)))
		fl.emit(ir.OpI32Xor, 0)
		return ir.I32, nil

	default:
		pos := fl.fset.Position(expr.Pos())
		return ir.TypeInvalid, fmt.Errorf("compiler: unsupported unary operator %s (line %d)", expr.Op, pos.Line)
	}
}

// lowerBinaryExpr translates binary arithmetic, bitwise, comparison, and short-circuit logical operators.
func (fl *funcLowerer) lowerBinaryExpr(expr *ast.BinaryExpr, targetType ir.ValType) (ir.ValType, error) {
	// Logical short-circuiting: && and ||
	if expr.Op == token.LAND {
		scratch := fl.getScratchLocal()
		if _, err := fl.lowerExpr(expr.X, ir.I32); err != nil {
			return ir.TypeInvalid, err
		}
		fl.emit(ir.OpLocalSet, uint64(scratch))
		fl.emit(ir.OpLocalGet, uint64(scratch))
		fl.emit(ir.OpIf, 0)
		fl.pushControl(kindIf, false, nil)
		if _, err := fl.lowerExpr(expr.Y, ir.I32); err != nil {
			return ir.TypeInvalid, err
		}
		fl.emit(ir.OpLocalSet, uint64(scratch))
		fl.popControl()
		fl.emit(ir.OpEnd, 0)
		fl.emit(ir.OpLocalGet, uint64(scratch))
		return ir.I32, nil
	}

	if expr.Op == token.LOR {
		scratch := fl.getScratchLocal()
		if _, err := fl.lowerExpr(expr.X, ir.I32); err != nil {
			return ir.TypeInvalid, err
		}
		fl.emit(ir.OpLocalSet, uint64(scratch))
		fl.emit(ir.OpLocalGet, uint64(scratch))
		fl.emit(ir.OpI32Eqz, 0)
		fl.emit(ir.OpIf, 0)
		fl.pushControl(kindIf, false, nil)
		if _, err := fl.lowerExpr(expr.Y, ir.I32); err != nil {
			return ir.TypeInvalid, err
		}
		fl.emit(ir.OpLocalSet, uint64(scratch))
		fl.popControl()
		fl.emit(ir.OpEnd, 0)
		fl.emit(ir.OpLocalGet, uint64(scratch))
		return ir.I32, nil
	}

	// General binary operations
	opType := fl.determineCommonType(expr.X, expr.Y, targetType)

	if _, err := fl.lowerExpr(expr.X, opType); err != nil {
		return ir.TypeInvalid, err
	}
	if _, err := fl.lowerExpr(expr.Y, opType); err != nil {
		return ir.TypeInvalid, err
	}

	if err := fl.emitBinaryOp(expr.Op, opType); err != nil {
		return ir.TypeInvalid, err
	}

	// Comparison operators always result in i32 (boolean 0/1)
	if isComparisonOp(expr.Op) {
		return ir.I32, nil
	}

	return opType, nil
}

// isComparisonOp reports whether the token is a relational comparison operator.
func isComparisonOp(tok token.Token) bool {
	switch tok {
	case token.EQL, token.NEQ, token.LSS, token.LEQ, token.GTR, token.GEQ:
		return true
	default:
		return false
	}
}

// emitBinaryOp emits the Aegis IR opcode corresponding to an operator and operand type.
func (fl *funcLowerer) emitBinaryOp(tok token.Token, valType ir.ValType) error {
	switch valType {
	case ir.I64:
		switch tok {
		case token.ADD:
			fl.emit(ir.OpI64Add, 0)
		case token.SUB:
			fl.emit(ir.OpI64Sub, 0)
		case token.MUL:
			fl.emit(ir.OpI64Mul, 0)
		case token.QUO:
			fl.emit(ir.OpI64DivS, 0)
		case token.REM:
			fl.emit(ir.OpI64RemS, 0)
		case token.AND:
			fl.emit(ir.OpI64And, 0)
		case token.OR:
			fl.emit(ir.OpI64Or, 0)
		case token.XOR:
			fl.emit(ir.OpI64Xor, 0)
		case token.SHL:
			fl.emit(ir.OpI64Shl, 0)
		case token.SHR:
			fl.emit(ir.OpI64ShrS, 0)
		case token.EQL:
			fl.emit(ir.OpI64Eq, 0)
		case token.NEQ:
			fl.emit(ir.OpI64Ne, 0)
		case token.LSS:
			fl.emit(ir.OpI64LtS, 0)
		case token.LEQ:
			fl.emit(ir.OpI64LeS, 0)
		case token.GTR:
			fl.emit(ir.OpI64GtS, 0)
		case token.GEQ:
			fl.emit(ir.OpI64GeS, 0)
		default:
			return fmt.Errorf("compiler: unsupported i64 binary operator %s", tok)
		}

	case ir.F64:
		switch tok {
		case token.ADD:
			fl.emit(ir.OpF64Add, 0)
		case token.SUB:
			fl.emit(ir.OpF64Sub, 0)
		case token.MUL:
			fl.emit(ir.OpF64Mul, 0)
		case token.QUO:
			fl.emit(ir.OpF64Div, 0)
		case token.EQL:
			fl.emit(ir.OpF64Eq, 0)
		case token.NEQ:
			fl.emit(ir.OpF64Ne, 0)
		case token.LSS:
			fl.emit(ir.OpF64Lt, 0)
		case token.LEQ:
			fl.emit(ir.OpF64Le, 0)
		case token.GTR:
			fl.emit(ir.OpF64Gt, 0)
		case token.GEQ:
			fl.emit(ir.OpF64Ge, 0)
		default:
			return fmt.Errorf("compiler: unsupported f64 binary operator %s", tok)
		}

	default: // ir.I32
		switch tok {
		case token.ADD:
			fl.emit(ir.OpI32Add, 0)
		case token.SUB:
			fl.emit(ir.OpI32Sub, 0)
		case token.MUL:
			fl.emit(ir.OpI32Mul, 0)
		case token.QUO:
			fl.emit(ir.OpI32DivS, 0)
		case token.REM:
			fl.emit(ir.OpI32RemS, 0)
		case token.AND:
			fl.emit(ir.OpI32And, 0)
		case token.OR:
			fl.emit(ir.OpI32Or, 0)
		case token.XOR:
			fl.emit(ir.OpI32Xor, 0)
		case token.SHL:
			fl.emit(ir.OpI32Shl, 0)
		case token.SHR:
			fl.emit(ir.OpI32ShrS, 0)
		case token.EQL:
			fl.emit(ir.OpI32Eq, 0)
		case token.NEQ:
			fl.emit(ir.OpI32Ne, 0)
		case token.LSS:
			fl.emit(ir.OpI32LtS, 0)
		case token.LEQ:
			fl.emit(ir.OpI32LeS, 0)
		case token.GTR:
			fl.emit(ir.OpI32GtS, 0)
		case token.GEQ:
			fl.emit(ir.OpI32GeS, 0)
		default:
			return fmt.Errorf("compiler: unsupported i32 binary operator %s", tok)
		}
	}

	return nil
}

// lowerCallExpr translates function calls, built-in type conversions, and host calls.
func (fl *funcLowerer) lowerCallExpr(expr *ast.CallExpr, targetType ir.ValType) (ir.ValType, error) {
	// 1. Type conversions
	if ident, ok := expr.Fun.(*ast.Ident); ok {
		switch ident.Name {
		case "int32", "uint32", "int", "uint", "bool":
			if len(expr.Args) == 1 {
				argType, err := fl.lowerExpr(expr.Args[0], ir.I32)
				if err != nil {
					return ir.TypeInvalid, err
				}
				if argType == ir.I64 {
					fl.emit(ir.OpI32WrapI64, 0)
				} else if argType == ir.F64 {
					fl.emit(ir.OpI32TruncF64S, 0)
				}
				return ir.I32, nil
			}

		case "int64", "uint64":
			if len(expr.Args) == 1 {
				argType, err := fl.lowerExpr(expr.Args[0], ir.I64)
				if err != nil {
					return ir.TypeInvalid, err
				}
				if argType == ir.I32 {
					if ident.Name == "uint64" {
						fl.emit(ir.OpI64ExtendI32U, 0)
					} else {
						fl.emit(ir.OpI64ExtendI32S, 0)
					}
				}
				return ir.I64, nil
			}

		case "float64":
			if len(expr.Args) == 1 {
				argType, err := fl.lowerExpr(expr.Args[0], ir.F64)
				if err != nil {
					return ir.TypeInvalid, err
				}
				if argType == ir.I32 {
					fl.emit(ir.OpF64ConvertI32S, 0)
				} else if argType == ir.I64 {
					fl.emit(ir.OpF64ConvertI64S, 0)
				}
				return ir.F64, nil
			}

		case "float32":
			if len(expr.Args) == 1 {
				return fl.lowerExpr(expr.Args[0], ir.F32)
			}
		}

		// 2. Call to internal function in same module
		if calleeIdx, exists := fl.funcMap[ident.Name]; exists {
			calleeFn := fl.module.Functions[calleeIdx]
			calleeType := fl.module.Types[calleeFn.TypeIndex]

			for i, arg := range expr.Args {
				expectedParam := ir.I32
				if i < len(calleeType.Params) {
					expectedParam = calleeType.Params[i]
				}
				if _, err := fl.lowerExpr(arg, expectedParam); err != nil {
					return ir.TypeInvalid, err
				}
			}

			fl.emit(ir.OpCall, uint64(calleeIdx))

			if len(calleeType.Results) > 0 {
				return calleeType.Results[0], nil
			}
			return ir.TypeInvalid, nil
		}
	}

	// 3. Host calls (e.g. host.read_input, host.write_output, read_input, etc.)
	var modName = "host"
	var fnName string

	if sel, ok := expr.Fun.(*ast.SelectorExpr); ok {
		if xIdent, ok := sel.X.(*ast.Ident); ok {
			modName = xIdent.Name
			fnName = sel.Sel.Name
		} else {
			return ir.TypeInvalid, fmt.Errorf("compiler: unsupported selector call target %T", sel.X)
		}
	} else if ident, ok := expr.Fun.(*ast.Ident); ok {
		fnName = ident.Name
	} else {
		return ir.TypeInvalid, fmt.Errorf("compiler: unsupported call expression target %T", expr.Fun)
	}

	return fl.lowerHostCall(modName, fnName, expr.Args, targetType)
}

// lowerHostCall registers or finds an import and emits an OpCallImport instruction.
func (fl *funcLowerer) lowerHostCall(modName, fnName string, args []ast.Expr, targetType ir.ValType) (ir.ValType, error) {
	// Normalize common function names (e.g. ReadInput -> read_input)
	fnName = normalizeHostFuncName(fnName)

	// Determine import signature
	var paramTypes []ir.ValType
	for _, arg := range args {
		argType := fl.deduceType(arg, ir.I32)
		paramTypes = append(paramTypes, argType)
	}

	var resultTypes []ir.ValType
	retType := ir.I32
	switch fnName {
	case "time_now_unix", "time_now_nanos":
		retType = ir.I64
		resultTypes = []ir.ValType{ir.I64}
	default:
		if targetType != ir.TypeInvalid {
			retType = targetType
		}
		resultTypes = []ir.ValType{retType}
	}

	ft := ir.FuncType{
		Params:  paramTypes,
		Results: resultTypes,
	}
	typeIdx := getOrAddFuncType(fl.module, ft)

	// Find or add import
	importIdx := -1
	for i, imp := range fl.module.Imports {
		if imp.Module == modName && imp.Name == fnName && imp.Type == typeIdx {
			importIdx = i
			break
		}
	}
	if importIdx < 0 {
		fl.module.Imports = append(fl.module.Imports, ir.Import{
			Module: modName,
			Name:   fnName,
			Type:   typeIdx,
		})
		importIdx = len(fl.module.Imports) - 1
	}

	// Lower arguments left-to-right
	for i, arg := range args {
		expectedParam := ir.I32
		if i < len(paramTypes) {
			expectedParam = paramTypes[i]
		}
		if _, err := fl.lowerExpr(arg, expectedParam); err != nil {
			return ir.TypeInvalid, err
		}
	}

	fl.emit(ir.OpCallImport, uint64(importIdx))
	return retType, nil
}

// normalizeHostFuncName converts camelCase or PascalCase host names to snake_case.
func normalizeHostFuncName(name string) string {
	switch name {
	case "ReadInput":
		return "read_input"
	case "WriteOutput":
		return "write_output"
	case "HashSHA256":
		return "hash_sha256"
	case "RandomBytes":
		return "random_bytes"
	case "TimeNowUnix":
		return "time_now_unix"
	case "TimeNowNanos":
		return "time_now_nanos"
	default:
		return strings.ToLower(name)
	}
}

// deduceType peeks at an expression to infer its resulting Aegis IR ValType.
func (fl *funcLowerer) deduceType(expr ast.Expr, fallback ir.ValType) ir.ValType {
	if expr == nil {
		if fallback != ir.TypeInvalid {
			return fallback
		}
		return ir.I32
	}

	switch e := expr.(type) {
	case *ast.Ident:
		if info, ok := fl.locals[e.Name]; ok {
			return info.valType
		}
		if e.Name == "true" || e.Name == "false" {
			return ir.I32
		}
		if fallback != ir.TypeInvalid {
			return fallback
		}
		return ir.I32

	case *ast.BasicLit:
		switch e.Kind {
		case token.FLOAT:
			return ir.F64
		case token.INT:
			if fallback != ir.TypeInvalid {
				return fallback
			}
			return ir.I32
		default:
			return ir.I32
		}

	case *ast.BinaryExpr:
		if isComparisonOp(e.Op) || e.Op == token.LAND || e.Op == token.LOR {
			return ir.I32
		}
		return fl.determineCommonType(e.X, e.Y, fallback)

	case *ast.UnaryExpr:
		return fl.deduceType(e.X, fallback)

	case *ast.ParenExpr:
		return fl.deduceType(e.X, fallback)

	case *ast.CallExpr:
		if ident, ok := e.Fun.(*ast.Ident); ok {
			switch ident.Name {
			case "int64", "uint64":
				return ir.I64
			case "float64":
				return ir.F64
			case "float32":
				return ir.F32
			case "int32", "uint32", "int", "uint", "bool":
				return ir.I32
			default:
				if idx, ok := fl.funcMap[ident.Name]; ok {
					calleeFn := fl.module.Functions[idx]
					calleeType := fl.module.Types[calleeFn.TypeIndex]
					if len(calleeType.Results) > 0 {
						return calleeType.Results[0]
					}
				}
			}
		}
		if fallback != ir.TypeInvalid {
			return fallback
		}
		return ir.I32

	default:
		if fallback != ir.TypeInvalid {
			return fallback
		}
		return ir.I32
	}
}

// determineCommonType resolves the unified arithmetic type of two sub-expressions.
func (fl *funcLowerer) determineCommonType(x, y ast.Expr, targetType ir.ValType) ir.ValType {
	tx := fl.deduceType(x, ir.TypeInvalid)
	ty := fl.deduceType(y, ir.TypeInvalid)

	if tx != ir.TypeInvalid && ty != ir.TypeInvalid {
		if tx == ty {
			return tx
		}
		if tx == ir.F64 || ty == ir.F64 {
			return ir.F64
		}
		if tx == ir.I64 || ty == ir.I64 {
			return ir.I64
		}
		return ir.I32
	}

	if tx != ir.TypeInvalid {
		return tx
	}
	if ty != ir.TypeInvalid {
		return ty
	}
	if targetType != ir.TypeInvalid {
		return targetType
	}
	return ir.I32
}
