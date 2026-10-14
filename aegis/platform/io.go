package platform

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/oarkflow/ref/aegis/ir"
	"github.com/oarkflow/ref/aegis/vm"
)

// BuildIOHostFunctions constructs virtual machine host functions satisfying standard
// input acquisition, output emission, and diagnostic logging imports.
func BuildIOHostFunctions(
	module *ir.Module,
	inputs map[string][]byte,
	outputs map[string][]byte,
	outputsMu *sync.Mutex,
) []vm.HostFunc {
	if inputs == nil {
		inputs = make(map[string][]byte)
	}
	if outputsMu == nil {
		outputsMu = new(sync.Mutex)
	}

	registered := make(map[string]bool)
	var hostFuncs []vm.HostFunc

	// Satisfy module-declared imports
	if module != nil {
		for _, imp := range module.Imports {
			key := imp.Module + "." + imp.Name
			if registered[key] {
				continue
			}

			var returnArity int
			if int(imp.Type) < len(module.Types) {
				returnArity = len(module.Types[imp.Type].Results)
			}

			switch imp.Name {
			case "get_input_len", "input_len":
				hostFuncs = append(hostFuncs, createGetInputLenFunc(imp.Module, imp.Name, returnArity, inputs))
				registered[key] = true
			case "get_input", "read_input":
				hostFuncs = append(hostFuncs, createGetInputFunc(imp.Module, imp.Name, returnArity, inputs))
				registered[key] = true
			case "set_output", "write_output":
				hostFuncs = append(hostFuncs, createSetOutputFunc(imp.Module, imp.Name, returnArity, outputs, outputsMu))
				registered[key] = true
			case "log":
				hostFuncs = append(hostFuncs, createLogFunc(imp.Module, imp.Name, returnArity))
				registered[key] = true
			}
		}
	}

	// Always supply default fallbacks for "env" and "aegis" namespaces
	for _, ns := range []string{"env", "aegis"} {
		lenKey := ns + ".get_input_len"
		if !registered[lenKey] {
			hostFuncs = append(hostFuncs, createGetInputLenFunc(ns, "get_input_len", 1, inputs))
			registered[lenKey] = true
		}
		getKey := ns + ".get_input"
		if !registered[getKey] {
			hostFuncs = append(hostFuncs, createGetInputFunc(ns, "get_input", 1, inputs))
			registered[getKey] = true
		}
		setKey := ns + ".set_output"
		if !registered[setKey] {
			hostFuncs = append(hostFuncs, createSetOutputFunc(ns, "set_output", 0, outputs, outputsMu))
			registered[setKey] = true
		}
		logKey := ns + ".log"
		if !registered[logKey] {
			hostFuncs = append(hostFuncs, createLogFunc(ns, "log", 0))
			registered[logKey] = true
		}
	}

	return hostFuncs
}

func createGetInputLenFunc(
	modName, fnName string,
	returnArity int,
	inputs map[string][]byte,
) vm.HostFunc {
	return vm.NewHostFunc(modName, fnName, func(ctx context.Context, v *vm.VM, args []uint64) ([]uint64, error) {
		if len(args) < 2 {
			return nil, errors.New("platform: get_input_len requires 2 arguments: (key_ptr, key_len)")
		}
		keyBytes, err := v.ReadMemory(uint32(args[0]), uint32(args[1]))
		if err != nil {
			return nil, fmt.Errorf("platform: failed to read input key from memory: %w", err)
		}
		key := string(keyBytes)
		val, exists := inputs[key]
		var length uint64
		if exists {
			length = uint64(len(val))
		} else {
			length = math.MaxUint32
		}

		if returnArity > 0 {
			return []uint64{length}, nil
		}
		return nil, nil
	})
}

func createGetInputFunc(
	modName, fnName string,
	returnArity int,
	inputs map[string][]byte,
) vm.HostFunc {
	return vm.NewHostFunc(modName, fnName, func(ctx context.Context, v *vm.VM, args []uint64) ([]uint64, error) {
		if len(args) < 3 {
			return nil, errors.New("platform: get_input requires at least 3 arguments: (key_ptr, key_len, dst_ptr)")
		}
		keyBytes, err := v.ReadMemory(uint32(args[0]), uint32(args[1]))
		if err != nil {
			return nil, fmt.Errorf("platform: failed to read input key from memory: %w", err)
		}
		key := string(keyBytes)
		val, exists := inputs[key]
		if !exists {
			if returnArity > 0 {
				return []uint64{0}, nil
			}
			return nil, nil
		}

		toCopy := val
		if len(args) >= 4 {
			dstCap := int(args[3])
			if len(toCopy) > dstCap {
				toCopy = toCopy[:dstCap]
			}
		}

		if err := v.WriteMemory(uint32(args[2]), toCopy); err != nil {
			return nil, fmt.Errorf("platform: failed to write input into memory: %w", err)
		}

		if returnArity > 0 {
			return []uint64{uint64(len(toCopy))}, nil
		}
		return nil, nil
	})
}

func createSetOutputFunc(
	modName, fnName string,
	returnArity int,
	outputs map[string][]byte,
	outputsMu *sync.Mutex,
) vm.HostFunc {
	return vm.NewHostFunc(modName, fnName, func(ctx context.Context, v *vm.VM, args []uint64) ([]uint64, error) {
		if len(args) < 4 {
			return nil, errors.New("platform: set_output requires 4 arguments: (key_ptr, key_len, val_ptr, val_len)")
		}
		keyBytes, err := v.ReadMemory(uint32(args[0]), uint32(args[1]))
		if err != nil {
			return nil, fmt.Errorf("platform: failed to read output key from memory: %w", err)
		}
		valBytes, err := v.ReadMemory(uint32(args[2]), uint32(args[3]))
		if err != nil {
			return nil, fmt.Errorf("platform: failed to read output value from memory: %w", err)
		}

		key := string(keyBytes)
		valCopy := make([]byte, len(valBytes))
		copy(valCopy, valBytes)

		outputsMu.Lock()
		outputs[key] = valCopy
		outputsMu.Unlock()

		if returnArity > 0 {
			return []uint64{0}, nil
		}
		return nil, nil
	})
}

func createLogFunc(
	modName, fnName string,
	returnArity int,
) vm.HostFunc {
	return vm.NewHostFunc(modName, fnName, func(ctx context.Context, v *vm.VM, args []uint64) ([]uint64, error) {
		if returnArity > 0 {
			return []uint64{0}, nil
		}
		return nil, nil
	})
}

// ExecuteModule executes an IR module function inside an isolated VM instance,
// measuring instruction fuel consumption, wall-clock duration, and output buffers.
func ExecuteModule(
	ctx context.Context,
	module *ir.Module,
	config BackendConfig,
	req ExecutionRequest,
) (*ExecutionResult, error) {
	if module == nil {
		return nil, ErrModuleNotLoaded
	}

	fuel := config.InitialFuel
	if req.Fuel > 0 {
		fuel = req.Fuel
	}
	maxPages := config.MaxMemoryPages
	if maxPages == 0 {
		maxPages = 256
	}

	vmConfig := vm.Config{
		MaxStackDepth:  65536,
		MaxCallDepth:   1024,
		MaxMemoryPages: maxPages,
		InitialFuel:    fuel,
	}

	outputs := make(map[string][]byte)
	var outputsMu sync.Mutex
	hostFuncs := BuildIOHostFunctions(module, req.Inputs, outputs, &outputsMu)

	v, err := vm.New(module, vmConfig, hostFuncs...)
	if err != nil {
		return nil, fmt.Errorf("platform: vm initialization failed: %w", err)
	}
	defer v.Close()

	start := time.Now()
	ret, callErr := v.Call(ctx, req.Function, req.Args...)
	duration := time.Since(start).Nanoseconds()

	var fuelUsed int64
	if fuel > 0 {
		remaining := v.RemainingFuel()
		fuelUsed = fuel - remaining
		if fuelUsed < 0 || fuelUsed > fuel {
			fuelUsed = fuel
		}
	}

	if callErr != nil {
		// Map VM traps to platform errors where appropriate
		var trap *vm.Trap
		if errors.As(callErr, &trap) {
			if trap.Kind == vm.TrapFuelExhausted {
				return nil, fmt.Errorf("%w: %v", ErrFuelExhausted, trap)
			}
		}
		if strings.Contains(callErr.Error(), "not found") {
			return nil, fmt.Errorf("%w: %s", ErrFunctionNotFound, req.Function)
		}
		return nil, fmt.Errorf("platform: execution failed: %w", callErr)
	}

	// Populate outputs from raw results if not already set by host functions
	outputsMu.Lock()
	if len(ret) > 0 {
		if _, exists := outputs["result"]; !exists {
			buf := make([]byte, 8)
			binary.LittleEndian.PutUint64(buf, ret[0])
			outputs["result"] = buf
		}
		for i, resVal := range ret {
			key := fmt.Sprintf("result_%d", i)
			if _, exists := outputs[key]; !exists {
				buf := make([]byte, 8)
				binary.LittleEndian.PutUint64(buf, resVal)
				outputs[key] = buf
			}
		}
	}
	outputsMu.Unlock()

	return &ExecutionResult{
		Returns:  ret,
		Outputs:  outputs,
		FuelUsed: fuelUsed,
		Duration: duration,
	}, nil
}
