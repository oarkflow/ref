package compiler

import (
	"encoding/hex"
	"fmt"
	"go/ast"
	"go/token"
	"time"
	"unicode"

	"github.com/oarkflow/ref/aegis/ir"
	"github.com/oarkflow/ref/aegis/policy"
)

// Compiler coordinates Go source parsing, validation, bytecode generation,
// optimization, security policy enforcement, and IR module construction.
type Compiler struct {
	config Config
}

// New constructs a new Compiler instance with the provided configuration.
func New(config Config) *Compiler {
	return &Compiler{config: config}
}

// Compile executes the compilation process according to the compiler's configuration.
func (c *Compiler) Compile() (*CompileResult, error) {
	fset := token.NewFileSet()
	var astFiles []*ast.File

	// 1. Ingest and parse source code or package directory
	if c.config.SourceCode != "" {
		file, err := parseSource(fset, c.config.SourceCode)
		if err != nil {
			return nil, err
		}
		astFiles = append(astFiles, file)
	} else if c.config.PackagePath != "" {
		files, err := parsePackageDir(fset, c.config.PackagePath)
		if err != nil {
			return nil, err
		}
		astFiles = files
	} else {
		return nil, fmt.Errorf("compiler: no source code or package path specified")
	}

	// 2. Validate AST against Aegis security constraints
	for _, file := range astFiles {
		if err := validateAST(fset, file); err != nil {
			return nil, err
		}
	}

	// 3. Initialize IR module container
	mod := &ir.Module{
		Version: ir.Version,
		Memory: ir.MemorySpec{
			InitialPages: 1,
			MaxPages:     2,
		},
		Metadata: ir.Metadata{
			CompilerVersion: "aegis-compiler-v1.0.0",
			TargetPlatform:  "generic",
			BuildTime:       time.Now().Unix(),
		},
	}

	// 4. Pass 1: Collect all function declarations and register types
	var funcDecls []*ast.FuncDecl
	funcMap := make(map[string]uint32)

	for _, file := range astFiles {
		for _, decl := range file.Decls {
			fdecl, ok := decl.(*ast.FuncDecl)
			if !ok || fdecl.Body == nil {
				continue
			}

			fnName := fdecl.Name.Name
			if _, exists := funcMap[fnName]; exists {
				return nil, fmt.Errorf("compiler: duplicate function declaration %q", fnName)
			}

			ft, err := extractFuncType(fdecl)
			if err != nil {
				return nil, err
			}

			typeIndex := getOrAddFuncType(mod, ft)
			funcIdx := uint32(len(mod.Functions))
			funcMap[fnName] = funcIdx

			mod.Functions = append(mod.Functions, ir.Function{
				Name:      fnName,
				TypeIndex: typeIndex,
			})
			funcDecls = append(funcDecls, fdecl)
		}
	}

	if len(funcDecls) == 0 {
		return nil, fmt.Errorf("compiler: no compilable function declarations found")
	}

	// 5. Pass 2: Lower function bodies into Aegis IR bytecode
	var warnings []string
	for i, fdecl := range funcDecls {
		funcIdx := uint32(i)
		fn := &mod.Functions[funcIdx]

		// Apply security policy attributes if configured
		c.applyPolicyToFunction(fn)

		if err := lowerFunction(c, mod, fdecl, funcIdx, fset, funcMap); err != nil {
			return nil, fmt.Errorf("compiler: failed to compile function %q: %w", fdecl.Name.Name, err)
		}
	}

	// 6. Build Exports table
	if err := c.setupExports(mod, funcMap); err != nil {
		return nil, err
	}

	// 7. Apply optimizations if OptLevel > 0
	if c.config.OptLevel > 0 {
		optimize(mod, c.config.OptLevel)
	}

	// 8. Apply diversification
	if c.config.Diversify || (c.config.Policy != nil && c.config.Policy.Protection.Diversify) {
		if err := Diversify(mod); err != nil {
			return nil, err
		}
	} else {
		if err := GenerateBuildID(mod); err != nil {
			return nil, err
		}
	}

	// 9. Strip metadata if requested
	if c.config.StripMetadata || (c.config.Policy != nil && c.config.Policy.Protection.StripMetadata) {
		Strip(mod)
	}

	// 10. Validate the complete generated IR module
	if err := ir.Validate(mod); err != nil {
		return nil, fmt.Errorf("compiler: generated module validation failed: %w", err)
	}

	// 11. Generate compilation metrics and report
	report := c.buildReport(mod)

	return &CompileResult{
		Module:   mod,
		Report:   report,
		Warnings: warnings,
	}, nil
}

// CompileSource is a convenience method that compiles a source string with the given entry points.
func (c *Compiler) CompileSource(src string, entryPoints []string) (*CompileResult, error) {
	cfg := c.config
	cfg.SourceCode = src
	cfg.PackagePath = ""
	if len(entryPoints) > 0 {
		cfg.EntryPoints = entryPoints
	}
	comp := New(cfg)
	return comp.Compile()
}

// addDataString stores a static string literal into linear memory data segments.
func (c *Compiler) addDataString(m *ir.Module, s string) uint32 {
	data := []byte(s)
	var offset uint32

	if len(m.Data) > 0 {
		last := m.Data[len(m.Data)-1]
		offset = last.Offset + uint32(len(last.Data))
		// 4-byte alignment
		if rem := offset % 4; rem != 0 {
			offset += (4 - rem)
		}
	}

	m.Data = append(m.Data, ir.DataSegment{
		Offset: offset,
		Data:   data,
	})

	// Ensure memory pages accommodate the data segment
	minPages := (offset + uint32(len(data)) + ir.PageSize - 1) / ir.PageSize
	if minPages < 1 {
		minPages = 1
	}
	if m.Memory.InitialPages < minPages {
		m.Memory.InitialPages = minPages
	}
	if m.Memory.MaxPages < m.Memory.InitialPages {
		m.Memory.MaxPages = m.Memory.InitialPages + 1
	}

	return offset
}

// applyPolicyToFunction applies protection rules from Config.Policy to an IR function.
func (c *Compiler) applyPolicyToFunction(fn *ir.Function) {
	if c.config.Policy == nil {
		return
	}

	p := c.config.Policy

	// Baseline protection tier defaults
	switch p.Protection.Default {
	case policy.LevelIsolated:
		fn.Attributes.Confidentiality = ir.ConfCritical
		fn.Attributes.MustNotInline = true
	case policy.LevelHardened:
		fn.Attributes.Confidentiality = ir.ConfHigh
	default:
		fn.Attributes.Confidentiality = ir.ConfStandard
	}

	// Per-function policy override
	if fp, exists := p.Functions[fn.Name]; exists {
		switch fp.Confidentiality {
		case policy.ConfidentialityCritical:
			fn.Attributes.Confidentiality = ir.ConfCritical
		case policy.ConfidentialityHigh:
			fn.Attributes.Confidentiality = ir.ConfHigh
		case policy.ConfidentialityStandard:
			fn.Attributes.Confidentiality = ir.ConfStandard
		case policy.ConfidentialityLow:
			fn.Attributes.Confidentiality = ir.ConfLow
		}

		if fp.Execution == policy.ExecIsolatedLocal || fp.Execution == policy.ExecHardenedLocal || fp.Execution == policy.ExecRemoteConfidential {
			fn.Attributes.MustNotInline = true
		}
	}

	if p.Protection.Diversify || c.config.Diversify {
		fn.Attributes.Diversify = true
	}
}

// setupExports configures the Exports table based on Config.EntryPoints or Go export rules.
func (c *Compiler) setupExports(mod *ir.Module, funcMap map[string]uint32) error {
	exportedSet := make(map[string]bool)

	if len(c.config.EntryPoints) > 0 {
		for _, ep := range c.config.EntryPoints {
			funcIdx, exists := funcMap[ep]
			if !exists {
				return fmt.Errorf("compiler: entry point %q not found in compiled functions", ep)
			}
			if !exportedSet[ep] {
				mod.Exports = append(mod.Exports, ir.Export{
					Name:      ep,
					FuncIndex: funcIdx,
				})
				exportedSet[ep] = true
			}
		}
		return nil
	}

	// Automatic export strategy:
	// 1. If "main" exists, export it
	if funcIdx, exists := funcMap["main"]; exists {
		mod.Exports = append(mod.Exports, ir.Export{
			Name:      "main",
			FuncIndex: funcIdx,
		})
		exportedSet["main"] = true
	}

	// 2. Export all capitalized functions (standard Go export convention)
	for name, funcIdx := range funcMap {
		if len(name) > 0 && unicode.IsUpper(rune(name[0])) {
			if !exportedSet[name] {
				mod.Exports = append(mod.Exports, ir.Export{
					Name:      name,
					FuncIndex: funcIdx,
				})
				exportedSet[name] = true
			}
		}
	}

	// 3. If still nothing exported, export all internal functions
	if len(mod.Exports) == 0 {
		for name, funcIdx := range funcMap {
			if !exportedSet[name] {
				mod.Exports = append(mod.Exports, ir.Export{
					Name:      name,
					FuncIndex: funcIdx,
				})
				exportedSet[name] = true
			}
		}
	}

	return nil
}

// buildReport computes compilation metrics and aggregates details into a Report struct.
func (c *Compiler) buildReport(mod *ir.Module) Report {
	var totalInstructions int
	var protectedCount int

	for _, fn := range mod.Functions {
		totalInstructions += len(fn.Body)
		if fn.Attributes.Confidentiality > ir.ConfStandard || fn.Attributes.MustNotInline || fn.Attributes.Diversify {
			protectedCount++
		}
	}

	var dataBytes int
	for _, seg := range mod.Data {
		dataBytes += len(seg.Data)
	}

	imports := make([]string, 0, len(mod.Imports))
	for _, imp := range mod.Imports {
		imports = append(imports, imp.Module+"."+imp.Name)
	}

	exports := make([]string, 0, len(mod.Exports))
	for _, exp := range mod.Exports {
		exports = append(exports, exp.Name)
	}

	return Report{
		FunctionsCompiled:  len(mod.Functions),
		FunctionsProtected: protectedCount,
		InstructionCount:   totalInstructions,
		DataSegmentBytes:   dataBytes,
		ImportsUsed:        imports,
		ExportsCreated:     exports,
		BuildID:            hex.EncodeToString(mod.BuildID[:]),
	}
}
