package capability

import (
	"fmt"
	"sync"

	"github.com/oarkflow/ref/fact"
)

// Registry holds all registered capabilities.
type Registry struct {
	mu     sync.RWMutex
	byName map[string]*Registration
	byFact map[fact.DefinitionID]*Registration
	sealed bool
}

// NewRegistry initializes an empty capability registry.
func NewRegistry() *Registry {
	return &Registry{
		byName: make(map[string]*Registration),
		byFact: make(map[fact.DefinitionID]*Registration),
	}
}

// Register registers a capability.
func (r *Registry) Register(reg Registration) error {
	return r.RegisterAll([]Registration{reg})
}

func (r *Registry) RegisterAll(registrations []Registration) error {
	for _, reg := range registrations {
		if err := ValidateRegistration(reg); err != nil {
			return err
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sealed {
		return fmt.Errorf("ref: capability registry is sealed")
	}
	pendingNames := make(map[string]struct{}, len(registrations))
	pendingFacts := make(map[fact.DefinitionID]string, len(registrations))
	for _, reg := range registrations {
		if _, exists := r.byName[reg.Name]; exists {
			return fmt.Errorf("ref: duplicate capability %q", reg.Name)
		}
		if _, exists := pendingNames[reg.Name]; exists {
			return fmt.Errorf("ref: duplicate capability %q", reg.Name)
		}
		pendingNames[reg.Name] = struct{}{}
		for _, key := range reg.Provides {
			if existing, conflict := r.byFact[key.DefID]; conflict {
				return fmt.Errorf("ref: fact %q (def %d) already provided by capability %q", key.Name, key.DefID, existing.Name)
			}
			if existing, conflict := pendingFacts[key.DefID]; conflict {
				return fmt.Errorf("ref: fact %q (def %d) already provided by capability %q", key.Name, key.DefID, existing)
			}
			pendingFacts[key.DefID] = reg.Name
		}
	}
	for _, reg := range registrations {
		copied := cloneRegistration(&reg)
		r.byName[reg.Name] = copied
		for _, key := range reg.Provides {
			r.byFact[key.DefID] = copied
		}
	}
	return nil
}

func cloneRegistration(reg *Registration) *Registration {
	if reg == nil {
		return nil
	}
	copied := *reg
	copied.Requires = append([]fact.AnyKey(nil), reg.Requires...)
	copied.Provides = append([]fact.AnyKey(nil), reg.Provides...)
	if reg.Source != nil {
		source := *reg.Source
		copied.Source = &source
	}
	return &copied
}

// Lookup finds a capability by name.
func (r *Registry) Lookup(name string) (*Registration, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	reg, ok := r.byName[name]
	return cloneRegistration(reg), ok
}

// ProducerOf finds the capability that provides a given fact DefinitionID.
func (r *Registry) ProducerOf(id fact.DefinitionID) (*Registration, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	reg, ok := r.byFact[id]
	return cloneRegistration(reg), ok
}

// All returns a snapshot of all registered capabilities.
func (r *Registry) All() map[string]*Registration {
	r.mu.RLock()
	defer r.mu.RUnlock()
	res := make(map[string]*Registration, len(r.byName))
	for k, v := range r.byName {
		res[k] = cloneRegistration(v)
	}
	return res
}

func (r *Registry) Freeze() {
	r.mu.Lock()
	r.sealed = true
	r.mu.Unlock()
}
