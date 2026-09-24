package domain

import (
	"fmt"
	"sync"

	"github.com/oarkflow/ref/capability"
	"github.com/oarkflow/ref/fact"
)

type Pack struct {
	mu           sync.RWMutex
	name         string
	version      string
	capabilities []capability.Registration
}

func NewPack(name, version string) (*Pack, error) {
	if name == "" {
		return nil, fmt.Errorf("domain: pack name is required")
	}
	if version == "" {
		return nil, fmt.Errorf("domain: pack version is required")
	}
	return &Pack{name: name, version: version}, nil
}

func (p *Pack) Name() string {
	if p == nil {
		return ""
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.name
}

func (p *Pack) Version() string {
	if p == nil {
		return ""
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.version
}

func (p *Pack) Add(registrations ...capability.Registration) error {
	if p == nil {
		return fmt.Errorf("domain: nil pack")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	pending := make(map[string]struct{}, len(registrations))
	for _, registration := range registrations {
		if err := capability.ValidateRegistration(registration); err != nil {
			return err
		}
		if _, exists := pending[registration.Name]; exists {
			return fmt.Errorf("domain: duplicate capability %q", registration.Name)
		}
		pending[registration.Name] = struct{}{}
		for _, existing := range p.capabilities {
			if existing.Name == registration.Name {
				return fmt.Errorf("domain: duplicate capability %q", registration.Name)
			}
		}
	}
	for _, registration := range registrations {
		p.capabilities = append(p.capabilities, cloneRegistration(registration))
	}
	return nil
}

func (p *Pack) Register(registry *capability.Registry) error {
	if p == nil || registry == nil {
		return fmt.Errorf("domain: pack and registry are required")
	}
	p.mu.RLock()
	registrations := make([]capability.Registration, len(p.capabilities))
	copy(registrations, p.capabilities)
	p.mu.RUnlock()
	return registry.RegisterAll(registrations)
}

func (p *Pack) Capabilities() []capability.Registration {
	if p == nil {
		return nil
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]capability.Registration, 0, len(p.capabilities))
	for _, registration := range p.capabilities {
		out = append(out, cloneRegistration(registration))
	}
	return out
}

func cloneRegistration(reg capability.Registration) capability.Registration {
	copied := reg
	copied.Requires = append([]fact.AnyKey(nil), reg.Requires...)
	copied.Provides = append([]fact.AnyKey(nil), reg.Provides...)
	if reg.Source != nil {
		source := *reg.Source
		copied.Source = &source
	}
	return copied
}

type Resource[T any] struct {
	ID       string
	TenantID string
	Version  uint64
	Value    T
}

func (r Resource[T]) Valid() bool {
	return r.ID != "" && r.TenantID != ""
}
