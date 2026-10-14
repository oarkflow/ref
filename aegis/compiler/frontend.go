package compiler

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"

	"github.com/oarkflow/ref/aegis/ir"
)

// parseSource ingests source code from either a direct string or a filename path,
// ensuring a package declaration is present, and produces an AST representation.
func parseSource(fset *token.FileSet, src string) (*ast.File, error) {
	var code []byte
	var filename = "source.go"

	// If src points to an existing file, load it from filesystem
	if info, err := os.Stat(src); err == nil && !info.IsDir() {
		filename = filepath.Base(src)
		content, err := os.ReadFile(src)
		if err != nil {
			return nil, fmt.Errorf("compiler: failed to read source file %q: %w", src, err)
		}
		code = content
	} else {
		trimmed := strings.TrimSpace(src)
		if !strings.HasPrefix(trimmed, "package ") {
			// Auto-wrap bare functions / statements with a package header
			code = []byte("package main\n\n" + src)
		} else {
			code = []byte(src)
		}
	}

	file, err := parser.ParseFile(fset, filename, code, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("compiler: syntax error: %w", err)
	}

	return file, nil
}

// parsePackageDir parses all Go source files in a directory.
func parsePackageDir(fset *token.FileSet, dirPath string) ([]*ast.File, error) {
	pkgs, err := parser.ParseDir(fset, dirPath, nil, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("compiler: failed to parse package directory %q: %w", dirPath, err)
	}

	var files []*ast.File
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			files = append(files, file)
		}
	}

	if len(files) == 0 {
		return nil, fmt.Errorf("compiler: no Go source files found in %q", dirPath)
	}

	return files, nil
}

// validateAST traverses an AST file and verifies that it contains no unsupported
// constructs such as goroutines, channels, interfaces, complex pointers, or reflection.
func validateAST(fset *token.FileSet, file *ast.File) error {
	var validationErr error

	ast.Inspect(file, func(node ast.Node) bool {
		if validationErr != nil || node == nil {
			return false
		}

		pos := fset.Position(node.Pos())

		switch n := node.(type) {
		case *ast.GoStmt:
			validationErr = fmt.Errorf("compiler: goroutines (go statements) are not supported in Aegis IR (line %d)", pos.Line)
			return false

		case *ast.ChanType:
			validationErr = fmt.Errorf("compiler: channels are not supported in Aegis IR (line %d)", pos.Line)
			return false

		case *ast.SendStmt:
			validationErr = fmt.Errorf("compiler: channel send operations are not supported in Aegis IR (line %d)", pos.Line)
			return false

		case *ast.SelectStmt:
			validationErr = fmt.Errorf("compiler: select statements are not supported in Aegis IR (line %d)", pos.Line)
			return false

		case *ast.DeferStmt:
			validationErr = fmt.Errorf("compiler: defer statements are not supported in Aegis IR (line %d)", pos.Line)
			return false

		case *ast.InterfaceType:
			validationErr = fmt.Errorf("compiler: interfaces are not supported in Aegis IR (line %d)", pos.Line)
			return false

		case *ast.TypeAssertExpr:
			validationErr = fmt.Errorf("compiler: type assertions are not supported in Aegis IR (line %d)", pos.Line)
			return false

		case *ast.TypeSwitchStmt:
			validationErr = fmt.Errorf("compiler: type switches are not supported in Aegis IR (line %d)", pos.Line)
			return false

		case *ast.CommClause:
			validationErr = fmt.Errorf("compiler: channel communication clauses are not supported in Aegis IR (line %d)", pos.Line)
			return false

		case *ast.StarExpr:
			validationErr = fmt.Errorf("compiler: pointers are not supported in Aegis IR (line %d)", pos.Line)
			return false

		case *ast.UnaryExpr:
			if n.Op == token.ARROW {
				validationErr = fmt.Errorf("compiler: channel receive operations are not supported in Aegis IR (line %d)", pos.Line)
				return false
			}

		case *ast.ImportSpec:
			path := strings.Trim(n.Path.Value, `"`)
			if path == "reflect" || path == "unsafe" {
				validationErr = fmt.Errorf("compiler: package %q is not supported in Aegis IR (line %d)", path, pos.Line)
				return false
			}
		}

		return true
	})

	return validationErr
}

// resolveValType converts a Go AST type expression into its corresponding Aegis IR ValType.
func resolveValType(expr ast.Expr) (ir.ValType, error) {
	switch t := expr.(type) {
	case *ast.Ident:
		switch t.Name {
		case "int32", "uint32":
			return ir.I32, nil
		case "int64", "uint64":
			return ir.I64, nil
		case "int", "uint":
			return ir.I32, nil
		case "float32":
			return ir.F32, nil
		case "float64":
			return ir.F64, nil
		case "bool":
			return ir.I32, nil
		case "byte", "uint8", "int8":
			return ir.I32, nil
		case "int16", "uint16":
			return ir.I32, nil
		case "string":
			return ir.I32, nil // string represented as memory pointer offset
		default:
			return ir.TypeInvalid, fmt.Errorf("compiler: unsupported type identifier %q", t.Name)
		}

	case *ast.ArrayType:
		// Slices and arrays
		if ident, ok := t.Elt.(*ast.Ident); ok && (ident.Name == "byte" || ident.Name == "uint8") {
			return ir.Bytes, nil
		}
		return ir.TypeInvalid, fmt.Errorf("compiler: unsupported array or slice type %T", expr)

	default:
		return ir.TypeInvalid, fmt.Errorf("compiler: unsupported type expression %T", expr)
	}
}

// extractFuncType converts a function declaration's signature into an Aegis IR FuncType.
func extractFuncType(decl *ast.FuncDecl) (ir.FuncType, error) {
	var ft ir.FuncType

	// Parameters
	if decl.Type.Params != nil {
		for _, field := range decl.Type.Params.List {
			vt, err := resolveValType(field.Type)
			if err != nil {
				return ft, fmt.Errorf("compiler: function %s parameter type error: %w", decl.Name.Name, err)
			}
			if len(field.Names) == 0 {
				ft.Params = append(ft.Params, vt)
			} else {
				for range field.Names {
					ft.Params = append(ft.Params, vt)
				}
			}
		}
	}

	// Return results
	if decl.Type.Results != nil {
		for _, field := range decl.Type.Results.List {
			vt, err := resolveValType(field.Type)
			if err != nil {
				return ft, fmt.Errorf("compiler: function %s result type error: %w", decl.Name.Name, err)
			}
			if len(field.Names) == 0 {
				ft.Results = append(ft.Results, vt)
			} else {
				for range field.Names {
					ft.Results = append(ft.Results, vt)
				}
			}
		}
	}

	return ft, nil
}

// getOrAddFuncType finds an existing identical FuncType in the module or registers a new one.
func getOrAddFuncType(m *ir.Module, ft ir.FuncType) uint32 {
	for i, existing := range m.Types {
		if funcTypeEquals(existing, ft) {
			return uint32(i)
		}
	}
	m.Types = append(m.Types, ft)
	return uint32(len(m.Types) - 1)
}

// funcTypeEquals tests equality of parameter and result slices.
func funcTypeEquals(a, b ir.FuncType) bool {
	if len(a.Params) != len(b.Params) || len(a.Results) != len(b.Results) {
		return false
	}
	for i := range a.Params {
		if a.Params[i] != b.Params[i] {
			return false
		}
	}
	for i := range a.Results {
		if a.Results[i] != b.Results[i] {
			return false
		}
	}
	return true
}
