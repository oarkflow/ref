package main

import (
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/oarkflow/ref/aegis/ir"
)

// runCompile handles the `aegis compile` CLI command.
func runCompile(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("compile", flag.ContinueOnError)
	flags.SetOutput(stderr)

	var (
		srcFlag   string
		entryFlag string
		outFlag   string
	)

	flags.StringVar(&srcFlag, "src", "", "Path to source Go file (.go)")
	flags.StringVar(&entryFlag, "entry", "", "Comma-separated list of entry function names to export")
	flags.StringVar(&outFlag, "out", "", "Path to output Aegis IR binary file (.air)")

	if err := flags.Parse(args); err != nil {
		return err
	}

	if srcFlag == "" {
		return errors.New("aegis: compile: source file (-src) is required")
	}

	if outFlag == "" {
		base := strings.TrimSuffix(srcFlag, filepath.Ext(srcFlag))
		outFlag = base + ".air"
	}

	var entries []string
	if entryFlag != "" {
		for _, e := range strings.Split(entryFlag, ",") {
			trimmed := strings.TrimSpace(e)
			if trimmed != "" {
				entries = append(entries, trimmed)
			}
		}
	}

	module, err := CompileGoFile(srcFlag, entries)
	if err != nil {
		return err
	}

	encoded, err := ir.Encode(module)
	if err != nil {
		return fmt.Errorf("aegis: compile: failed to encode IR module: %w", err)
	}

	outDir := filepath.Dir(outFlag)
	if outDir != "" && outDir != "." {
		if err := os.MkdirAll(outDir, 0755); err != nil {
			return fmt.Errorf("aegis: compile: failed to create output directory %q: %w", outDir, err)
		}
	}

	if err := os.WriteFile(outFlag, encoded, 0644); err != nil {
		return fmt.Errorf("aegis: compile: failed to write output file %q: %w", outFlag, err)
	}

	fmt.Fprintf(stdout, "Compiled %s -> %s (%d functions, %d exports, %d bytes)\n",
		srcFlag, outFlag, len(module.Functions), len(module.Exports), len(encoded))
	return nil
}

// CompileGoFile reads a Go source file and compiles its functions into an Aegis IR Module.
func CompileGoFile(srcPath string, entryFuncs []string) (*ir.Module, error) {
	data, err := os.ReadFile(srcPath)
	if err != nil {
		return nil, fmt.Errorf("aegis: compile: failed to read source file %q: %w", srcPath, err)
	}
	return CompileGoSource(data, entryFuncs)
}

// CompileGoSource parses Go source code and produces an Aegis IR Module.
func CompileGoSource(src []byte, entryFuncs []string) (*ir.Module, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "source.go", src, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("aegis: compile: syntax error: %w", err)
	}

	c := &compiler{
		fset:       fset,
		file:       file,
		types:      make([]ir.FuncType, 0),
		typeMap:    make(map[string]uint32),
		funcMap:    make(map[string]uint32),
		funcDecls:  make([]*ast.FuncDecl, 0),
		entries:    make(map[string]bool),
		allEntries: len(entryFuncs) == 0,
	}

	for _, e := range entryFuncs {
		c.entries[e] = true
	}

	// Pass 1: Index package-level functions
	for _, decl := range file.Decls {
		if fnDecl, ok := decl.(*ast.FuncDecl); ok && fnDecl.Recv == nil {
			fnName := fnDecl.Name.Name
			funcIndex := uint32(len(c.funcDecls))
			c.funcMap[fnName] = funcIndex
			c.funcDecls = append(c.funcDecls, fnDecl)
		}
	}

	// Validate requested entry functions exist
	if !c.allEntries {
		for _, e := range entryFuncs {
			if _, exists := c.funcMap[e]; !exists {
				return nil, fmt.Errorf("aegis: compile: entry function %q not found", e)
			}
		}
	}

	// Pass 2: Register function signatures in type table
	funcTypeIndices := make([]uint32, len(c.funcDecls))
	for i, fnDecl := range c.funcDecls {
		params := c.resolveParams(fnDecl.Type.Params)
		results := c.resolveResults(fnDecl.Type.Results)
		funcTypeIndices[i] = c.getOrAddType(params, results)
	}

	// Pass 3: Compile each function body
	compiledFunctions := make([]ir.Function, len(c.funcDecls))
	for i, fnDecl := range c.funcDecls {
		fn, err := c.compileFunction(fnDecl, funcTypeIndices[i], uint32(i))
		if err != nil {
			return nil, err
		}
		compiledFunctions[i] = fn
	}

	// Pass 4: Build exports
	exports := make([]ir.Export, 0)
	for i, fnDecl := range c.funcDecls {
		fnName := fnDecl.Name.Name
		if c.allEntries || c.entries[fnName] {
			exports = append(exports, ir.Export{
				Name:      fnName,
				FuncIndex: uint32(i),
			})
		}
	}

	var buildID [32]byte
	if _, err := io.ReadFull(rand.Reader, buildID[:]); err != nil {
		return nil, fmt.Errorf("aegis: compile: failed to generate build ID: %w", err)
	}

	module := &ir.Module{
		Version:   ir.Version,
		BuildID:   buildID,
		Types:     c.types,
		Functions: compiledFunctions,
		Exports:   exports,
		Memory: ir.MemorySpec{
			InitialPages: 1,
			MaxPages:     4,
		},
		Metadata: ir.Metadata{
			CompilerVersion: "aegisc-1.0.0",
			BuildTime:       time.Now().Unix(),
		},
	}

	if err := ir.Validate(module); err != nil {
		return nil, fmt.Errorf("aegis: compile: validation error: %w", err)
	}

	return module, nil
}

type compiler struct {
	fset       *token.FileSet
	file       *ast.File
	types      []ir.FuncType
	typeMap    map[string]uint32
	funcMap    map[string]uint32
	funcDecls  []*ast.FuncDecl
	entries    map[string]bool
	allEntries bool
}

func (c *compiler) resolveParams(fl *ast.FieldList) []ir.ValType {
	if fl == nil {
		return nil
	}
	var res []ir.ValType
	for _, field := range fl.List {
		vt := c.resolveType(field.Type)
		if len(field.Names) == 0 {
			res = append(res, vt)
		} else {
			for range field.Names {
				res = append(res, vt)
			}
		}
	}
	return res
}

func (c *compiler) resolveResults(fl *ast.FieldList) []ir.ValType {
	if fl == nil {
		return nil
	}
	var res []ir.ValType
	for _, field := range fl.List {
		vt := c.resolveType(field.Type)
		if len(field.Names) == 0 {
			res = append(res, vt)
		} else {
			for range field.Names {
				res = append(res, vt)
			}
		}
	}
	return res
}

func (c *compiler) resolveType(expr ast.Expr) ir.ValType {
	if expr == nil {
		return ir.I64
	}
	switch t := expr.(type) {
	case *ast.Ident:
		switch t.Name {
		case "int", "int64", "uint", "uint64", "uintptr":
			return ir.I64
		case "int32", "uint32", "rune", "int16", "uint16", "int8", "uint8", "byte", "bool":
			return ir.I32
		case "float64":
			return ir.F64
		case "float32":
			return ir.F32
		case "string":
			return ir.Bytes
		default:
			return ir.I64
		}
	case *ast.ArrayType:
		return ir.Bytes
	default:
		return ir.I64
	}
}

func (c *compiler) getOrAddType(params, results []ir.ValType) uint32 {
	var sb strings.Builder
	sb.WriteString("(")
	for i, p := range params {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString(p.String())
	}
	sb.WriteString(")->(")
	for i, r := range results {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString(r.String())
	}
	sb.WriteString(")")

	key := sb.String()
	if idx, exists := c.typeMap[key]; exists {
		return idx
	}

	idx := uint32(len(c.types))
	c.types = append(c.types, ir.FuncType{
		Params:  params,
		Results: results,
	})
	c.typeMap[key] = idx
	return idx
}

type localInfo struct {
	index   uint64
	valType ir.ValType
}

type fnCompiler struct {
	parent       *compiler
	fnDecl       *ast.FuncDecl
	funcIdx      uint32
	fnType       ir.FuncType
	locals       map[string]localInfo
	extraLocals  []ir.ValType
	paramCount   int
	instructions []ir.Instruction
}

func (c *compiler) compileFunction(fnDecl *ast.FuncDecl, typeIdx uint32, funcIdx uint32) (ir.Function, error) {
	fnType := c.types[typeIdx]
	fc := &fnCompiler{
		parent:       c,
		fnDecl:       fnDecl,
		funcIdx:      funcIdx,
		fnType:       fnType,
		locals:       make(map[string]localInfo),
		extraLocals:  make([]ir.ValType, 0),
		instructions: make([]ir.Instruction, 0),
	}

	// Map parameter names to locals
	if fnDecl.Type.Params != nil {
		for _, field := range fnDecl.Type.Params.List {
			vt := c.resolveType(field.Type)
			if len(field.Names) == 0 {
				fc.paramCount++
			} else {
				for _, name := range field.Names {
					fc.locals[name.Name] = localInfo{
						index:   uint64(fc.paramCount),
						valType: vt,
					}
					fc.paramCount++
				}
			}
		}
	}

	// Compile body statements
	if fnDecl.Body != nil {
		for _, stmt := range fnDecl.Body.List {
			if err := fc.compileStmt(stmt); err != nil {
				return ir.Function{}, err
			}
		}
	}

	// Ensure function ends with OpReturn
	hasReturn := len(fc.instructions) > 0 && fc.instructions[len(fc.instructions)-1].Op == ir.OpReturn
	if !hasReturn {
		for range fnType.Results {
			fc.emit(ir.OpI64Const, 0)
		}
		fc.emit(ir.OpReturn)
	}

	return ir.Function{
		Name:      fnDecl.Name.Name,
		TypeIndex: typeIdx,
		Locals:    fc.extraLocals,
		Body:      fc.instructions,
		Attributes: ir.FuncAttr{
			Confidentiality: ir.ConfStandard,
		},
	}, nil
}

func (fc *fnCompiler) declareLocal(name string, vt ir.ValType) localInfo {
	if loc, exists := fc.locals[name]; exists {
		return loc
	}
	index := uint64(fc.paramCount + len(fc.extraLocals))
	fc.extraLocals = append(fc.extraLocals, vt)
	loc := localInfo{index: index, valType: vt}
	fc.locals[name] = loc
	return loc
}

func (fc *fnCompiler) emit(op ir.Opcode, operands ...uint64) {
	var ops [3]uint64
	for i, opVal := range operands {
		if i < 3 {
			ops[i] = opVal
		}
	}
	fc.instructions = append(fc.instructions, ir.Instruction{
		Op:       op,
		Operands: ops,
	})
}

func (fc *fnCompiler) compileStmt(stmt ast.Stmt) error {
	switch s := stmt.(type) {
	case *ast.ReturnStmt:
		for _, res := range s.Results {
			if err := fc.compileExpr(res); err != nil {
				return err
			}
		}
		fc.emit(ir.OpReturn)
		return nil

	case *ast.AssignStmt:
		switch s.Tok {
		case token.DEFINE: // :=
			for i, lhs := range s.Lhs {
				ident, ok := lhs.(*ast.Ident)
				if !ok {
					return fmt.Errorf("aegis: compile: unsupported assignment target %T", lhs)
				}
				loc := fc.declareLocal(ident.Name, ir.I64)
				if i < len(s.Rhs) {
					if err := fc.compileExpr(s.Rhs[i]); err != nil {
						return err
					}
					fc.emit(ir.OpLocalSet, loc.index)
				}
			}
		case token.ASSIGN: // =
			for i, lhs := range s.Lhs {
				ident, ok := lhs.(*ast.Ident)
				if !ok {
					return fmt.Errorf("aegis: compile: unsupported assignment target %T", lhs)
				}
				loc, exists := fc.locals[ident.Name]
				if !exists {
					loc = fc.declareLocal(ident.Name, ir.I64)
				}
				if i < len(s.Rhs) {
					if err := fc.compileExpr(s.Rhs[i]); err != nil {
						return err
					}
					fc.emit(ir.OpLocalSet, loc.index)
				}
			}
		case token.ADD_ASSIGN, token.SUB_ASSIGN, token.MUL_ASSIGN, token.QUO_ASSIGN, token.REM_ASSIGN,
			token.AND_ASSIGN, token.OR_ASSIGN, token.XOR_ASSIGN, token.SHL_ASSIGN, token.SHR_ASSIGN:
			if len(s.Lhs) != 1 || len(s.Rhs) != 1 {
				return errors.New("aegis: compile: compound assignment requires 1 variable")
			}
			ident, ok := s.Lhs[0].(*ast.Ident)
			if !ok {
				return fmt.Errorf("aegis: compile: unsupported assignment target %T", s.Lhs[0])
			}
			loc, exists := fc.locals[ident.Name]
			if !exists {
				return fmt.Errorf("aegis: compile: undefined variable %q", ident.Name)
			}
			fc.emit(ir.OpLocalGet, loc.index)
			if err := fc.compileExpr(s.Rhs[0]); err != nil {
				return err
			}
			op, err := compoundOp(s.Tok)
			if err != nil {
				return err
			}
			fc.emit(op)
			fc.emit(ir.OpLocalSet, loc.index)
		}
		return nil

	case *ast.IncDecStmt:
		ident, ok := s.X.(*ast.Ident)
		if !ok {
			return fmt.Errorf("aegis: compile: unsupported inc/dec target %T", s.X)
		}
		loc, exists := fc.locals[ident.Name]
		if !exists {
			return fmt.Errorf("aegis: compile: undefined variable %q", ident.Name)
		}
		fc.emit(ir.OpLocalGet, loc.index)
		fc.emit(ir.OpI64Const, 1)
		if s.Tok == token.INC {
			fc.emit(ir.OpI64Add)
		} else {
			fc.emit(ir.OpI64Sub)
		}
		fc.emit(ir.OpLocalSet, loc.index)
		return nil

	case *ast.DeclStmt:
		genDecl, ok := s.Decl.(*ast.GenDecl)
		if !ok || genDecl.Tok != token.VAR {
			return nil
		}
		for _, spec := range genDecl.Specs {
			valSpec, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			vt := fc.parent.resolveType(valSpec.Type)
			for i, name := range valSpec.Names {
				loc := fc.declareLocal(name.Name, vt)
				if i < len(valSpec.Values) {
					if err := fc.compileExpr(valSpec.Values[i]); err != nil {
						return err
					}
					fc.emit(ir.OpLocalSet, loc.index)
				} else {
					fc.emit(ir.OpI64Const, 0)
					fc.emit(ir.OpLocalSet, loc.index)
				}
			}
		}
		return nil

	case *ast.IfStmt:
		if s.Init != nil {
			if err := fc.compileStmt(s.Init); err != nil {
				return err
			}
		}
		if err := fc.compileCondition(s.Cond); err != nil {
			return err
		}
		fc.emit(ir.OpIf)
		if s.Body != nil {
			for _, child := range s.Body.List {
				if err := fc.compileStmt(child); err != nil {
					return err
				}
			}
		}
		if s.Else != nil {
			fc.emit(ir.OpElse)
			if err := fc.compileStmt(s.Else); err != nil {
				return err
			}
		}
		fc.emit(ir.OpEnd)
		return nil

	case *ast.ForStmt:
		if s.Init != nil {
			if err := fc.compileStmt(s.Init); err != nil {
				return err
			}
		}
		fc.emit(ir.OpBlock) // target for break
		fc.emit(ir.OpLoop)  // target for loop continue
		if s.Cond != nil {
			if err := fc.compileCondition(s.Cond); err != nil {
				return err
			}
			fc.emit(ir.OpI32Eqz) // invert: branch out if false
			fc.emit(ir.OpBrIf, 1) // break to block end
		}
		if s.Body != nil {
			for _, child := range s.Body.List {
				if err := fc.compileStmt(child); err != nil {
					return err
				}
			}
		}
		if s.Post != nil {
			if err := fc.compileStmt(s.Post); err != nil {
				return err
			}
		}
		fc.emit(ir.OpBr, 0) // loop back to loop start
		fc.emit(ir.OpEnd)   // end loop
		fc.emit(ir.OpEnd)   // end block
		return nil

	case *ast.BranchStmt:
		switch s.Tok {
		case token.BREAK:
			fc.emit(ir.OpBr, 1)
		case token.CONTINUE:
			fc.emit(ir.OpBr, 0)
		default:
			return fmt.Errorf("aegis: compile: unsupported branch statement %s", s.Tok)
		}
		return nil

	case *ast.BlockStmt:
		for _, child := range s.List {
			if err := fc.compileStmt(child); err != nil {
				return err
			}
		}
		return nil

	case *ast.ExprStmt:
		return fc.compileExpr(s.X)

	default:
		return fmt.Errorf("aegis: compile: unsupported statement %T", stmt)
	}
}

func (fc *fnCompiler) compileCondition(expr ast.Expr) error {
	switch e := expr.(type) {
	case *ast.BinaryExpr:
		switch e.Op {
		case token.EQL, token.NEQ, token.LSS, token.LEQ, token.GTR, token.GEQ:
			return fc.compileBinaryExpr(e)
		}
	}
	if err := fc.compileExpr(expr); err != nil {
		return err
	}
	fc.emit(ir.OpI64Const, 0)
	fc.emit(ir.OpI64Ne)
	return nil
}

func (fc *fnCompiler) compileExpr(expr ast.Expr) error {
	switch e := expr.(type) {
	case *ast.BasicLit:
		switch e.Kind {
		case token.INT:
			v, err := strconv.ParseInt(e.Value, 0, 64)
			if err != nil {
				u, err2 := strconv.ParseUint(e.Value, 0, 64)
				if err2 != nil {
					return fmt.Errorf("aegis: compile: invalid integer literal %q: %w", e.Value, err)
				}
				v = int64(u)
			}
			fc.emit(ir.OpI64Const, uint64(v))
			return nil
		case token.FLOAT:
			v, err := strconv.ParseFloat(e.Value, 64)
			if err != nil {
				return fmt.Errorf("aegis: compile: invalid float literal %q: %w", e.Value, err)
			}
			fc.emit(ir.OpF64Const, math.Float64bits(v))
			return nil
		case token.CHAR:
			fc.emit(ir.OpI32Const, uint64(e.Value[1]))
			return nil
		default:
			return fmt.Errorf("aegis: compile: unsupported literal kind %s", e.Kind)
		}

	case *ast.Ident:
		switch e.Name {
		case "true":
			fc.emit(ir.OpI32Const, 1)
			return nil
		case "false":
			fc.emit(ir.OpI32Const, 0)
			return nil
		}
		loc, exists := fc.locals[e.Name]
		if !exists {
			return fmt.Errorf("aegis: compile: undefined variable %q", e.Name)
		}
		fc.emit(ir.OpLocalGet, loc.index)
		return nil

	case *ast.ParenExpr:
		return fc.compileExpr(e.X)

	case *ast.BinaryExpr:
		return fc.compileBinaryExpr(e)

	case *ast.UnaryExpr:
		switch e.Op {
		case token.SUB:
			fc.emit(ir.OpI64Const, 0)
			if err := fc.compileExpr(e.X); err != nil {
				return err
			}
			fc.emit(ir.OpI64Sub)
			return nil
		case token.ADD:
			return fc.compileExpr(e.X)
		case token.NOT:
			if err := fc.compileCondition(e.X); err != nil {
				return err
			}
			fc.emit(ir.OpI32Eqz)
			return nil
		case token.XOR:
			if err := fc.compileExpr(e.X); err != nil {
				return err
			}
			fc.emit(ir.OpI64Const, ^uint64(0))
			fc.emit(ir.OpI64Xor)
			return nil
		default:
			return fmt.Errorf("aegis: compile: unsupported unary operator %s", e.Op)
		}

	case *ast.CallExpr:
		ident, ok := e.Fun.(*ast.Ident)
		if !ok {
			return fmt.Errorf("aegis: compile: indirect call expression %T not supported", e.Fun)
		}

		// Built-in type casts
		switch ident.Name {
		case "int", "int64", "uint", "uint64", "int32", "uint32", "byte", "rune", "float64", "float32":
			if len(e.Args) != 1 {
				return errors.New("aegis: compile: type conversion expects exactly 1 argument")
			}
			return fc.compileExpr(e.Args[0])
		}

		calleeIdx, exists := fc.parent.funcMap[ident.Name]
		if !exists {
			return fmt.Errorf("aegis: compile: undefined function %q", ident.Name)
		}

		for _, arg := range e.Args {
			if err := fc.compileExpr(arg); err != nil {
				return err
			}
		}
		fc.emit(ir.OpCall, uint64(calleeIdx))
		return nil

	default:
		return fmt.Errorf("aegis: compile: unsupported expression %T", expr)
	}
}

func (fc *fnCompiler) compileBinaryExpr(e *ast.BinaryExpr) error {
	if err := fc.compileExpr(e.X); err != nil {
		return err
	}
	if err := fc.compileExpr(e.Y); err != nil {
		return err
	}

	switch e.Op {
	case token.ADD:
		fc.emit(ir.OpI64Add)
	case token.SUB:
		fc.emit(ir.OpI64Sub)
	case token.MUL:
		fc.emit(ir.OpI64Mul)
	case token.QUO:
		fc.emit(ir.OpI64DivS)
	case token.REM:
		fc.emit(ir.OpI64RemS)
	case token.AND:
		fc.emit(ir.OpI64And)
	case token.OR:
		fc.emit(ir.OpI64Or)
	case token.XOR:
		fc.emit(ir.OpI64Xor)
	case token.SHL:
		fc.emit(ir.OpI64Shl)
	case token.SHR:
		fc.emit(ir.OpI64ShrS)
	case token.EQL:
		fc.emit(ir.OpI64Eq)
	case token.NEQ:
		fc.emit(ir.OpI64Ne)
	case token.LSS:
		fc.emit(ir.OpI64LtS)
	case token.LEQ:
		fc.emit(ir.OpI64LeS)
	case token.GTR:
		fc.emit(ir.OpI64GtS)
	case token.GEQ:
		fc.emit(ir.OpI64GeS)
	case token.LAND:
		fc.emit(ir.OpI32And)
	case token.LOR:
		fc.emit(ir.OpI32Or)
	default:
		return fmt.Errorf("aegis: compile: unsupported binary operator %s", e.Op)
	}

	return nil
}

func compoundOp(tok token.Token) (ir.Opcode, error) {
	switch tok {
	case token.ADD_ASSIGN:
		return ir.OpI64Add, nil
	case token.SUB_ASSIGN:
		return ir.OpI64Sub, nil
	case token.MUL_ASSIGN:
		return ir.OpI64Mul, nil
	case token.QUO_ASSIGN:
		return ir.OpI64DivS, nil
	case token.REM_ASSIGN:
		return ir.OpI64RemS, nil
	case token.AND_ASSIGN:
		return ir.OpI64And, nil
	case token.OR_ASSIGN:
		return ir.OpI64Or, nil
	case token.XOR_ASSIGN:
		return ir.OpI64Xor, nil
	case token.SHL_ASSIGN:
		return ir.OpI64Shl, nil
	case token.SHR_ASSIGN:
		return ir.OpI64ShrS, nil
	default:
		return 0, fmt.Errorf("aegis: compile: unsupported compound operator %s", tok)
	}
}
