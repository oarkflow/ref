package intent

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sync"

	"github.com/oarkflow/ref/effect"
	"github.com/oarkflow/ref/execution"
	"github.com/oarkflow/ref/fact"
)

// Registry stores all registered intent definitions.
type Registry struct {
	mu      sync.RWMutex
	intents map[Name]*Definition
	sealed  bool
}

// NewRegistry creates a new intent registry.
func NewRegistry() *Registry {
	return &Registry{
		intents: make(map[Name]*Definition),
	}
}

// DecoderFunc deserializes raw invocation bytes into typed input.
type DecoderFunc[I any] func(raw []byte, contentType string) (I, error)

// Register registers a strongly typed Intent[I, O] into the registry.
func Register[I, O any](r *Registry, it Intent[I, O], customDecoder ...DecoderFunc[I]) error {
	if it == nil {
		return fmt.Errorf("ref: nil intent")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sealed {
		return fmt.Errorf("ref: intent registry is sealed")
	}

	name := it.Name()
	if _, exists := r.intents[name]; exists {
		return fmt.Errorf("ref: duplicate intent %q", name)
	}

	spec := it.Spec()
	seenRequirements := make(map[fact.DefinitionID]struct{}, len(spec.Requires))
	for _, key := range spec.Requires {
		if key.DefID == 0 {
			return fmt.Errorf("ref: intent %q has an invalid required fact", name)
		}
		if _, ok := seenRequirements[key.DefID]; ok {
			return fmt.Errorf("ref: intent %q repeats required fact %q", name, key.Name)
		}
		seenRequirements[key.DefID] = struct{}{}
	}

	var dec DecoderFunc[I]
	if len(customDecoder) > 0 && customDecoder[0] != nil {
		dec = customDecoder[0]
	} else {
		dec = defaultDecoder[I]()
	}

	inputKey := fact.NewKey[I](string(name) + ".input")

	def := &Definition{
		Name:     name,
		Spec:     spec,
		InputKey: inputKey.Any(),
		DecodeNode: func(nc *execution.NodeContext) error {
			raw := nc.Invocation().Input
			typedInput, err := dec(raw.RawBytes(), raw.ContentType())
			if err != nil {
				return Failure{
					Code:     "INVALID_INPUT",
					Category: CategoryInvalidInput,
					Message:  fmt.Sprintf("failed to decode intent input: %v", err),
					Cause:    err,
				}
			}
			execution.Publish(nc, inputKey, typedInput)
			return nil
		},
		Run: func(nc *execution.NodeContext) (any, effect.EffectPlan, OutcomeMeta, error) {
			typedInput, err := execution.Require(nc, inputKey)
			if err != nil {
				return nil, effect.EffectPlan{}, OutcomeMeta{}, err
			}

			outcome, err := it.Run(nc, typedInput)
			if err != nil {
				return nil, effect.EffectPlan{}, OutcomeMeta{}, err
			}

			return outcome.Value, outcome.Effects, outcome.Meta, nil
		},
	}

	r.intents[name] = def
	return nil
}

// RegisterDefinition registers a type-erased intent definition. It is intended
// for trusted compilers (for example a declarative application compiler) that
// build REF programs at runtime rather than from Go generic types.
func (r *Registry) RegisterDefinition(def *Definition) error {
	if def == nil {
		return fmt.Errorf("ref: nil intent definition")
	}
	if def.Name == "" {
		return fmt.Errorf("ref: intent name is required")
	}
	if def.Run == nil {
		return fmt.Errorf("ref: intent %q has no operation", def.Name)
	}
	seenRequirements := make(map[fact.DefinitionID]struct{}, len(def.Spec.Requires))
	for _, key := range def.Spec.Requires {
		if key.DefID == 0 {
			return fmt.Errorf("ref: intent %q has an invalid required fact", def.Name)
		}
		if _, ok := seenRequirements[key.DefID]; ok {
			return fmt.Errorf("ref: intent %q repeats required fact %q", def.Name, key.Name)
		}
		seenRequirements[key.DefID] = struct{}{}
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sealed {
		return fmt.Errorf("ref: intent registry is sealed")
	}
	if _, exists := r.intents[def.Name]; exists {
		return fmt.Errorf("ref: duplicate intent %q", def.Name)
	}
	copyDef := *def
	copyDef.Spec.Requires = append([]fact.AnyKey(nil), def.Spec.Requires...)
	r.intents[def.Name] = &copyDef
	return nil
}

func cloneDefinition(def *Definition) *Definition {
	if def == nil {
		return nil
	}
	copied := *def
	copied.Spec.Requires = append([]fact.AnyKey(nil), def.Spec.Requires...)
	return &copied
}

// Lookup retrieves an intent definition by name.
func (r *Registry) Lookup(name Name) (*Definition, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	def, ok := r.intents[name]
	return cloneDefinition(def), ok
}

// All returns all registered intent definitions.
func (r *Registry) All() map[Name]*Definition {
	r.mu.RLock()
	defer r.mu.RUnlock()
	res := make(map[Name]*Definition, len(r.intents))
	for k, v := range r.intents {
		res[k] = cloneDefinition(v)
	}
	return res
}

func (r *Registry) Freeze() {
	r.mu.Lock()
	r.sealed = true
	r.mu.Unlock()
}

func defaultDecoder[I any]() DecoderFunc[I] {
	var zero I
	valType := reflect.TypeOf(zero)

	return func(raw []byte, contentType string) (I, error) {
		// If I is []byte
		if valType == reflect.TypeOf([]byte(nil)) {
			var res any = raw
			return res.(I), nil
		}
		// If I is string
		if valType == reflect.TypeOf("") {
			var res any = string(raw)
			return res.(I), nil
		}
		// If empty input or no bytes
		if len(raw) == 0 {
			return zero, nil
		}

		var target I
		if err := json.Unmarshal(raw, &target); err != nil {
			return zero, err
		}
		return target, nil
	}
}
