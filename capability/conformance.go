package capability

import (
	"fmt"

	"github.com/oarkflow/ref/fact"
	"github.com/oarkflow/ref/graph"
)

func ValidateRegistration(reg Registration) error {
	if reg.Name == "" {
		return fmt.Errorf("ref: capability name is required")
	}
	if reg.Run == nil {
		return fmt.Errorf("ref: capability %q has no runner", reg.Name)
	}
	if reg.Kind > graph.StreamNode {
		return fmt.Errorf("ref: capability %q has invalid node kind %d", reg.Name, reg.Kind)
	}
	if reg.Speculation > graph.PostPolicySafe {
		return fmt.Errorf("ref: capability %q has invalid speculation class %d", reg.Name, reg.Speculation)
	}
	requires := make(map[fact.DefinitionID]struct{}, len(reg.Requires))
	for _, key := range reg.Requires {
		if key.DefID == 0 {
			return fmt.Errorf("ref: capability %q has an invalid required fact", reg.Name)
		}
		if _, ok := requires[key.DefID]; ok {
			return fmt.Errorf("ref: capability %q repeats required fact %q", reg.Name, key.Name)
		}
		requires[key.DefID] = struct{}{}
	}
	provides := make(map[fact.DefinitionID]struct{}, len(reg.Provides))
	for _, key := range reg.Provides {
		if key.DefID == 0 {
			return fmt.Errorf("ref: capability %q has an invalid provided fact", reg.Name)
		}
		if _, ok := provides[key.DefID]; ok {
			return fmt.Errorf("ref: capability %q repeats provided fact %q", reg.Name, key.Name)
		}
		if _, ok := requires[key.DefID]; ok {
			return fmt.Errorf("ref: capability %q both requires and provides fact %q", reg.Name, key.Name)
		}
		provides[key.DefID] = struct{}{}
	}
	return nil
}
