package ir

import (
	"fmt"
	"strings"
)

// PageSize is the size in bytes of a single Aegis IR linear memory page (64 KiB).
const PageSize uint32 = 65536

// ValidationError records one or more structural or semantic errors found in a Module.
type ValidationError struct {
	// Errors contains descriptive error messages for each issue detected.
	Errors []string
}

// Error formats the aggregated list of validation errors into a human-readable string.
func (e *ValidationError) Error() string {
	if len(e.Errors) == 0 {
		return "ir: validation failed"
	}
	if len(e.Errors) == 1 {
		return e.Errors[0]
	}
	return fmt.Sprintf("ir: validation failed with %d errors:\n  - %s", len(e.Errors), strings.Join(e.Errors, "\n  - "))
}

// isValidValType checks whether a ValType is a defined, recognized type.
func isValidValType(vt ValType) bool {
	return vt == I32 || vt == I64 || vt == F32 || vt == F64 || vt == Bytes
}

// Validate performs full structural and semantic validation on an Aegis IR module.
// It checks type signatures, index references, memory bounds, instruction constraints,
// and collects all failures into a *ValidationError. If the module is completely valid,
// it returns nil.
func Validate(m *Module) error {
	if m == nil {
		return &ValidationError{Errors: []string{"ir: module is nil"}}
	}

	var validationErrors []string

	// Check format version
	if m.Version != Version {
		validationErrors = append(validationErrors, fmt.Sprintf("ir: unsupported version %d, expected %d", m.Version, Version))
	}

	// Validate FuncTypes
	for typeIndex, funcType := range m.Types {
		for paramIndex, paramType := range funcType.Params {
			if !isValidValType(paramType) {
				validationErrors = append(validationErrors, fmt.Sprintf("ir: type %d param %d has invalid ValType %d", typeIndex, paramIndex, paramType))
			}
		}
		for resultIndex, resultType := range funcType.Results {
			if !isValidValType(resultType) {
				validationErrors = append(validationErrors, fmt.Sprintf("ir: type %d result %d has invalid ValType %d", typeIndex, resultIndex, resultType))
			}
		}
	}

	// Validate Import type references
	typeCount := uint32(len(m.Types))
	for importIndex, imp := range m.Imports {
		if imp.Type >= typeCount {
			validationErrors = append(validationErrors, fmt.Sprintf("ir: import %d (%s.%s) references invalid type index %d (type count: %d)",
				importIndex, imp.Module, imp.Name, imp.Type, typeCount))
		}
	}

	// Validate Export func references (can reference module functions and imports)
	totalCallable := uint32(len(m.Functions) + len(m.Imports))
	for exportIndex, exp := range m.Exports {
		if totalCallable == 0 || exp.FuncIndex >= totalCallable {
			validationErrors = append(validationErrors, fmt.Sprintf("ir: export %d (%s) references invalid function index %d (total available: %d)",
				exportIndex, exp.Name, exp.FuncIndex, totalCallable))
		}
	}

	// Validate Functions
	functionCount := uint32(len(m.Functions))
	importCount := uint32(len(m.Imports))

	for funcIndex, fn := range m.Functions {
		funcName := fn.Name
		if funcName == "" {
			funcName = fmt.Sprintf("func_%d", funcIndex)
		}

		// Function TypeIndex check
		if fn.TypeIndex >= typeCount {
			validationErrors = append(validationErrors, fmt.Sprintf("ir: function %d (%s) references invalid type index %d (type count: %d)",
				funcIndex, funcName, fn.TypeIndex, typeCount))
		}

		// Validate local types
		for localIndex, localType := range fn.Locals {
			if !isValidValType(localType) {
				validationErrors = append(validationErrors, fmt.Sprintf("ir: function %d (%s) local %d has invalid ValType %d",
					funcIndex, funcName, localIndex, localType))
			}
		}

		// Empty body check
		if len(fn.Body) == 0 {
			validationErrors = append(validationErrors, fmt.Sprintf("ir: function %d (%s) has empty body (must have at least one instruction)",
				funcIndex, funcName))
			continue
		}

		// Calculate total local variables (params + locals)
		var paramCount int
		if fn.TypeIndex < typeCount {
			paramCount = len(m.Types[fn.TypeIndex].Params)
		}
		totalLocals := uint64(paramCount + len(fn.Locals))

		// Validate instructions
		for instIndex, inst := range fn.Body {
			switch inst.Op {
			case OpCall:
				if inst.Operands[0] >= uint64(functionCount) {
					validationErrors = append(validationErrors, fmt.Sprintf("ir: function %d (%s) instruction %d (%s): function index %d out of bounds (function count: %d)",
						funcIndex, funcName, instIndex, inst.Op, inst.Operands[0], functionCount))
				}
			case OpCallImport:
				if inst.Operands[0] >= uint64(importCount) {
					validationErrors = append(validationErrors, fmt.Sprintf("ir: function %d (%s) instruction %d (%s): import index %d out of bounds (import count: %d)",
						funcIndex, funcName, instIndex, inst.Op, inst.Operands[0], importCount))
				}
			case OpLocalGet, OpLocalSet, OpLocalTee:
				if fn.TypeIndex < typeCount && inst.Operands[0] >= totalLocals {
					validationErrors = append(validationErrors, fmt.Sprintf("ir: function %d (%s) instruction %d (%s): local index %d out of bounds (params: %d, locals: %d)",
						funcIndex, funcName, instIndex, inst.Op, inst.Operands[0], paramCount, len(fn.Locals)))
				}
			}
		}
	}

	// Validate MemorySpec
	if m.Memory.MaxPages < m.Memory.InitialPages {
		validationErrors = append(validationErrors, fmt.Sprintf("ir: memory max pages (%d) must be greater than or equal to initial pages (%d)",
			m.Memory.MaxPages, m.Memory.InitialPages))
	}

	// Validate Data segments
	var maxMemBytes uint64
	if m.Memory.InitialPages > 0 {
		maxMemBytes = uint64(m.Memory.InitialPages) * uint64(PageSize)
	} else if m.Memory.MaxPages > 0 {
		maxMemBytes = uint64(m.Memory.MaxPages) * uint64(PageSize)
	}

	for segIndex, seg := range m.Data {
		segEnd := uint64(seg.Offset) + uint64(len(seg.Data))
		if maxMemBytes == 0 || segEnd > maxMemBytes {
			validationErrors = append(validationErrors, fmt.Sprintf("ir: data segment %d (offset %d, length %d) exceeds memory bounds (%d bytes)",
				segIndex, seg.Offset, len(seg.Data), maxMemBytes))
		}
	}

	if len(validationErrors) > 0 {
		return &ValidationError{Errors: validationErrors}
	}

	return nil
}
