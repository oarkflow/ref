// Package transform provides program transformation and protection passes for Aegis IR modules.
package transform

import (
	cryptoRand "crypto/rand"
	"errors"
	"fmt"
	"math/rand"
	"time"

	"github.com/oarkflow/ref/aegis/ir"
)

// BuildDiversify implements a transformation pass that randomizes binary layout,
// shuffles internal function ordering, expands unused local registers, and assigns
// a fresh cryptographic BuildID to make each compilation binary-unique.
type BuildDiversify struct {
	seed int64
}

// NewBuildDiversify constructs a new BuildDiversify pass with an optional random seed.
func NewBuildDiversify(seed ...int64) *BuildDiversify {
	s := time.Now().UnixNano()
	if len(seed) > 0 && seed[0] != 0 {
		s = seed[0]
	}
	return &BuildDiversify{seed: s}
}

// NewDiversify is an alias constructor for NewBuildDiversify.
func NewDiversify(seed ...int64) *BuildDiversify {
	return NewBuildDiversify(seed...)
}

// Name returns the identifier of this transformation pass.
func (b *BuildDiversify) Name() string {
	return "BuildDiversify"
}

// Transform applies layout diversification: generates a random BuildID,
// shuffles function indices while updating OpCall and Export references,
// and injects unused dummy locals into stack frames.
func (b *BuildDiversify) Transform(m *ir.Module) (*ir.Module, Metrics, error) {
	if m == nil {
		return nil, Metrics{}, errors.New("transform: module is nil")
	}

	rng := rand.New(rand.NewSource(b.seed))
	metrics := Metrics{PassName: b.Name()}

	// 1. Assign fresh cryptographically secure 32-byte BuildID
	if _, err := cryptoRand.Read(m.BuildID[:]); err != nil {
		return nil, metrics, fmt.Errorf("transform: failed to generate cryptographic BuildID: %w", err)
	}

	numFuncs := len(m.Functions)
	if numFuncs == 0 {
		return m, metrics, nil
	}

	// 2. Randomize unused local registers in each function
	dummyTypes := []ir.ValType{ir.I32, ir.I64, ir.F32, ir.F64}
	for i := range m.Functions {
		fn := &m.Functions[i]
		fn.Attributes.Diversify = true

		// Append 1 to 2 randomized unused local variables
		extraLocals := rng.Intn(2) + 1
		for j := 0; j < extraLocals; j++ {
			fn.Locals = append(fn.Locals, dummyTypes[rng.Intn(len(dummyTypes))])
		}
		metrics.FunctionsModified++
	}

	// 3. Shuffle function layout if there are multiple functions
	if numFuncs > 1 {
		perm := rng.Perm(numFuncs)

		// Create reordered functions slice
		newFuncs := make([]ir.Function, numFuncs)
		for oldIdx, newIdx := range perm {
			newFuncs[newIdx] = m.Functions[oldIdx]
		}

		// Update OpCall target indices across all functions
		for i := range newFuncs {
			fn := &newFuncs[i]
			for j := range fn.Body {
				if fn.Body[j].Op == ir.OpCall {
					oldTarget := fn.Body[j].Operands[0]
					if oldTarget < uint64(numFuncs) {
						fn.Body[j].Operands[0] = uint64(perm[oldTarget])
					}
				}
			}
		}

		// Update Exports function indices (only internal function references)
		for i := range m.Exports {
			exp := &m.Exports[i]
			if exp.FuncIndex < uint32(numFuncs) {
				exp.FuncIndex = uint32(perm[exp.FuncIndex])
			}
		}

		m.Functions = newFuncs
	}

	return m, metrics, nil
}
