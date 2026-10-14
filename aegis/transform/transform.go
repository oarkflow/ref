// Package transform provides program transformation and protection passes for Aegis IR modules.
// It includes constant obfuscation, control flow restructuring, opaque predicates,
// symbol stripping, instruction scheduling, and binary layout diversification.
package transform

import (
	"errors"
	"fmt"

	"github.com/oarkflow/ref/aegis/ir"
)

// Pass defines the interface for an IR transformation or obfuscation pass.
type Pass interface {
	// Name returns a human-readable identifier for the pass.
	Name() string
	// Transform applies the pass to the provided module, returning the modified
	// module, performance metrics, or an error if the transformation failed.
	Transform(m *ir.Module) (*ir.Module, Metrics, error)
}

// Transformer is an alias/interface retained for basic module transformation without metrics.
type Transformer interface {
	Name() string
	Transform(module *ir.Module) error
}

// Metrics records operational statistics produced by a transformation pass.
type Metrics struct {
	// PassName identifies the transformation pass that generated these metrics.
	PassName string
	// FunctionsModified is the number of functions altered by the pass.
	FunctionsModified int
	// InstructionsAdded is the total number of bytecode instructions inserted.
	InstructionsAdded int
	// InstructionsRemoved is the total number of bytecode instructions replaced or removed.
	InstructionsRemoved int
	// DataSegmentsAdded is the number of new linear memory data segments added.
	DataSegmentsAdded int
}

// Pipeline maintains an ordered sequence of transformation passes applied to an Aegis IR module.
type Pipeline struct {
	passes []Pass
}

// NewPipeline creates a new transformation pipeline configured with the specified passes.
func NewPipeline(passes ...Pass) *Pipeline {
	copiedPasses := make([]Pass, len(passes))
	copy(copiedPasses, passes)
	return &Pipeline{
		passes: copiedPasses,
	}
}

// Passes returns a slice containing the ordered passes in this pipeline.
func (p *Pipeline) Passes() []Pass {
	result := make([]Pass, len(p.passes))
	copy(result, p.passes)
	return result
}

// Apply executes all passes in the pipeline sequentially against the input module.
// If any pass fails, the pipeline halts immediately and returns the error.
func (p *Pipeline) Apply(m *ir.Module) (*ir.Module, []Metrics, error) {
	if m == nil {
		return nil, nil, errors.New("transform: module is nil")
	}

	current := CloneModule(m)
	metricsList := make([]Metrics, 0, len(p.passes))

	for _, pass := range p.passes {
		next, metrics, err := pass.Transform(current)
		if err != nil {
			return nil, nil, fmt.Errorf("transform: pass %q failed: %w", pass.Name(), err)
		}
		if err := ir.Validate(next); err != nil {
			return nil, nil, fmt.Errorf("transform: pass %q produced invalid IR: %w", pass.Name(), err)
		}
		metricsList = append(metricsList, metrics)
		current = next
	}

	return current, metricsList, nil
}

// StandardPipeline constructs a default obfuscation pipeline containing ConstantEncoding,
// OpaquePredicates, ControlFlowFlatten, and BuildDiversify passes.
func StandardPipeline() *Pipeline {
	return NewPipeline(
		NewConstantEncoding(),
		NewOpaquePredicates(),
		NewControlFlowFlatten(),
		NewBuildDiversify(),
	)
}

// CloneModule creates a deep copy of an IR module so transformations do not mutate the original.
func CloneModule(m *ir.Module) *ir.Module {
	if m == nil {
		return nil
	}

	clone := &ir.Module{
		Version:  m.Version,
		BuildID:  m.BuildID,
		Memory:   m.Memory,
		Metadata: m.Metadata,
	}

	// Clone types
	if len(m.Types) > 0 {
		clone.Types = make([]ir.FuncType, len(m.Types))
		for i, ft := range m.Types {
			paramsCopy := make([]ir.ValType, len(ft.Params))
			copy(paramsCopy, ft.Params)
			resultsCopy := make([]ir.ValType, len(ft.Results))
			copy(resultsCopy, ft.Results)
			clone.Types[i] = ir.FuncType{
				Params:  paramsCopy,
				Results: resultsCopy,
			}
		}
	}

	// Clone imports
	if len(m.Imports) > 0 {
		clone.Imports = make([]ir.Import, len(m.Imports))
		copy(clone.Imports, m.Imports)
	}

	// Clone exports
	if len(m.Exports) > 0 {
		clone.Exports = make([]ir.Export, len(m.Exports))
		copy(clone.Exports, m.Exports)
	}

	// Clone data segments
	if len(m.Data) > 0 {
		clone.Data = make([]ir.DataSegment, len(m.Data))
		for i, seg := range m.Data {
			dataCopy := make([]byte, len(seg.Data))
			copy(dataCopy, seg.Data)
			clone.Data[i] = ir.DataSegment{
				Offset: seg.Offset,
				Data:   dataCopy,
			}
		}
	}

	// Clone functions
	if len(m.Functions) > 0 {
		clone.Functions = make([]ir.Function, len(m.Functions))
		for i, fn := range m.Functions {
			localsCopy := make([]ir.ValType, len(fn.Locals))
			copy(localsCopy, fn.Locals)

			bodyCopy := make([]ir.Instruction, len(fn.Body))
			copy(bodyCopy, fn.Body)

			clone.Functions[i] = ir.Function{
				Name:       fn.Name,
				TypeIndex:  fn.TypeIndex,
				Locals:     localsCopy,
				Body:       bodyCopy,
				Attributes: fn.Attributes,
			}
		}
	}

	return clone
}

// ValidateAfterTransform validates that a module remains semantically valid after transformations.
func ValidateAfterTransform(m *ir.Module) error {
	if err := ir.Validate(m); err != nil {
		return fmt.Errorf("transform: post-transformation validation failed: %w", err)
	}
	return nil
}
