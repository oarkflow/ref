package runtime

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strconv"
	"sync"
	"time"

	"github.com/oarkflow/ref/aegis/ir"
	"github.com/oarkflow/ref/aegis/pkg"
	"github.com/oarkflow/ref/aegis/vm"
)

var argSlicePool = sync.Pool{
	New: func() any {
		s := make([]uint64, 0, 8)
		return &s
	},
}

var bufferPool = sync.Pool{
	New: func() any {
		return new(bytes.Buffer)
	},
}

// Runtime manages the loading, authorization, and isolated VM execution of Aegis protected packages.
type Runtime struct {
	mu        sync.RWMutex
	config    Config
	reader    *pkg.Reader
	grants    GrantProvider
	hostFuncs []vm.HostFunc
	metrics   MetricsCollector
	closed    bool
}

// New constructs and initializes a new Aegis Runtime instance from the given configuration.
func New(config Config) (*Runtime, error) {
	if config.TrustedKeys == nil {
		return nil, errors.New("runtime: trusted keys must be provided")
	}
	if config.GrantProvider == nil {
		return nil, errors.New("runtime: grant provider must be provided")
	}

	// Apply default VM bounds if unconfigured
	if config.VM.MaxStackDepth <= 0 {
		config.VM.MaxStackDepth = 65536
	}
	if config.VM.MaxCallDepth <= 0 {
		config.VM.MaxCallDepth = 1024
	}
	if config.VM.MaxMemoryPages == 0 {
		config.VM.MaxMemoryPages = 256
	}

	reader := pkg.NewReader(config.TrustedKeys)

	metricsCollector := config.Metrics
	if metricsCollector == nil {
		metricsCollector = NewMetrics()
	}

	initialHostFuncs := make([]vm.HostFunc, len(config.HostFuncs))
	copy(initialHostFuncs, config.HostFuncs)

	return &Runtime{
		config:    config,
		reader:    reader,
		grants:    config.GrantProvider,
		hostFuncs: initialHostFuncs,
		metrics:   metricsCollector,
	}, nil
}

// RegisterHostFunc adds a host function available to all subsequently executed packages.
func (r *Runtime) RegisterHostFunc(hf vm.HostFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.hostFuncs = append(r.hostFuncs, hf)
}

// RegisterHostFuncs adds multiple host functions available to all subsequently executed packages.
func (r *Runtime) RegisterHostFuncs(hfs ...vm.HostFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.hostFuncs = append(r.hostFuncs, hfs...)
}

// Metrics returns the runtime's metrics collector.
func (r *Runtime) Metrics() MetricsCollector {
	return r.metrics
}

// Authorize requests an execution authorization grant from the configured GrantProvider,
// decrypts the package payload using the returned symmetric key, and stores the decrypted IR module.
func (r *Runtime) Authorize(ctx context.Context, lp *LoadedPackage, req GrantRequest) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	r.mu.RLock()
	closed := r.closed
	grants := r.grants
	reader := r.reader
	offline := r.config.Offline
	r.mu.RUnlock()

	if closed {
		return errors.New("runtime: runtime is closed")
	}
	if lp == nil {
		return errors.New("runtime: loaded package cannot be nil")
	}

	manifest := lp.Manifest()
	if manifest == nil {
		return errors.New("runtime: package manifest is missing")
	}

	// Populate grant request defaults from manifest if empty
	if req.PackageID == "" {
		req.PackageID = manifest.PackageID
	}
	if req.ApplicationID == "" {
		req.ApplicationID = manifest.ApplicationID
	}
	if req.BuildID == "" {
		req.BuildID = manifest.BuildID
	}
	if req.Version == "" {
		req.Version = manifest.Version
	}

	// 1. Evaluate offline caching policy
	lp.mutex.RLock()
	cachedGrant := lp.grant
	cachedModule := lp.module
	lp.mutex.RUnlock()

	now := time.Now()
	if cachedGrant != nil && cachedModule != nil && !cachedGrant.IsExpired(now) && offline.AllowCachedGrants {
		if offline.MaxCacheAge <= 0 || (cachedGrant.IssuedAt > 0 && now.Unix()-cachedGrant.IssuedAt <= offline.MaxCacheAge) {
			// Cached grant is valid and accepted
			return nil
		}
	}

	// 2. Check offline attestation policy
	if offline.RequireAttestation && len(req.Attestation) == 0 {
		return errors.New("runtime: attestation is required by offline policy")
	}

	// 3. Request authorization grant
	grant, err := grants.RequestGrant(ctx, req)
	if err != nil {
		return fmt.Errorf("runtime: failed to obtain grant: %w", err)
	}
	if grant == nil {
		return errors.New("runtime: nil grant returned by provider")
	}
	if len(grant.Key) != 32 {
		return fmt.Errorf("runtime: invalid grant key size (expected 32 bytes, got %d)", len(grant.Key))
	}
	if grant.IsExpired(now) {
		return errors.New("runtime: received expired grant")
	}

	// 4. Decrypt package payload using the grant key
	module, err := reader.Decrypt(lp.Package(), grant.Key)
	if err != nil {
		return fmt.Errorf("runtime: decryption failed: %w", err)
	}

	// 5. Store decrypted module and grant
	lp.mutex.Lock()
	lp.grant = grant
	lp.module = module
	lp.mutex.Unlock()

	return nil
}

// Execute validates authorization, verifies grant validity, instantiates the VM with I/O host functions,
// invokes the target function, and records execution metrics and audit events.
func (r *Runtime) Execute(ctx context.Context, lp *LoadedPackage, req ExecutionRequest) (*ExecutionResult, error) {
	startTime := time.Now()

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	r.mu.RLock()
	closed := r.closed
	vmDefaults := r.config.VM
	customHostFuncs := make([]vm.HostFunc, len(r.hostFuncs))
	copy(customHostFuncs, r.hostFuncs)
	r.mu.RUnlock()

	if closed {
		return nil, errors.New("runtime: runtime is closed")
	}
	if lp == nil {
		return nil, errors.New("runtime: loaded package cannot be nil")
	}

	manifest := lp.Manifest()
	var pkgID, appID, buildID string
	if manifest != nil {
		pkgID = manifest.PackageID
		appID = manifest.ApplicationID
		buildID = manifest.BuildID
	}

	// 1. Verify package is authorized and decrypted
	lp.mutex.RLock()
	module := lp.module
	grant := lp.grant
	lp.mutex.RUnlock()

	if module == nil || grant == nil {
		err := errors.New("runtime: package is not authorized or decrypted")
		r.recordMetric(ExecutionEvent{
			PackageID:     pkgID,
			ApplicationID: appID,
			BuildID:       buildID,
			Function:      req.Function,
			Duration:      time.Since(startTime),
			Error:         err,
			Timestamp:     startTime,
		})
		return nil, err
	}

	// 2. Verify grant expiration
	if grant.IsExpired(startTime) {
		err := errors.New("runtime: authorization grant has expired")
		r.recordMetric(ExecutionEvent{
			PackageID:     pkgID,
			ApplicationID: appID,
			BuildID:       buildID,
			Function:      req.Function,
			SessionID:     grant.SessionID,
			Duration:      time.Since(startTime),
			Error:         err,
			Timestamp:     startTime,
		})
		return nil, err
	}

	// 3. Locate exported function in module
	if req.Function == "" {
		err := errors.New("runtime: function name cannot be empty")
		r.recordMetric(ExecutionEvent{
			PackageID:     pkgID,
			ApplicationID: appID,
			BuildID:       buildID,
			SessionID:     grant.SessionID,
			Duration:      time.Since(startTime),
			Error:         err,
			Timestamp:     startTime,
		})
		return nil, err
	}

	var exportFound bool
	var targetExport ir.Export
	for _, exp := range module.Exports {
		if exp.Name == req.Function {
			targetExport = exp
			exportFound = true
			break
		}
	}
	if !exportFound {
		err := fmt.Errorf("runtime: exported function %q not found in module", req.Function)
		r.recordMetric(ExecutionEvent{
			PackageID:     pkgID,
			ApplicationID: appID,
			BuildID:       buildID,
			Function:      req.Function,
			SessionID:     grant.SessionID,
			Duration:      time.Since(startTime),
			Error:         err,
			Timestamp:     startTime,
		})
		return nil, err
	}

	// 4. Set up input/output bindings
	inputs := req.Inputs
	if inputs == nil {
		inputs = make(map[string][]byte)
	}
	outputs := make(map[string][]byte)
	var outputsMu sync.Mutex

	// 5. Build host functions connected to inputs and outputs
	allHostFuncs := r.buildHostFunctions(module, inputs, outputs, &outputsMu, customHostFuncs)

	// 6. Configure VM bounds and fuel
	vmConfig := vmDefaults
	if req.Fuel > 0 {
		vmConfig.InitialFuel = req.Fuel
	}

	// 7. Instantiate VM
	v, err := vm.New(module, vmConfig, allHostFuncs...)
	if err != nil {
		instErr := fmt.Errorf("runtime: failed to instantiate vm: %w", err)
		r.recordMetric(ExecutionEvent{
			PackageID:     pkgID,
			ApplicationID: appID,
			BuildID:       buildID,
			Function:      req.Function,
			SessionID:     grant.SessionID,
			Duration:      time.Since(startTime),
			Error:         instErr,
			Timestamp:     startTime,
		})
		return nil, instErr
	}
	defer func() {
		_ = v.Close()
	}()

	// 8. Prepare function call arguments
	callArgs := r.resolveCallArguments(module, targetExport, req, inputs)

	// 9. Execute function in VM
	execStart := time.Now()
	rawResults, callErr := v.Call(ctx, req.Function, callArgs...)
	duration := time.Since(execStart)

	// 10. Calculate fuel consumption
	var fuelUsed int64
	if vmConfig.InitialFuel > 0 {
		remainingFuel := v.RemainingFuel()
		fuelUsed = vmConfig.InitialFuel - remainingFuel
		if fuelUsed < 0 || fuelUsed > vmConfig.InitialFuel {
			fuelUsed = vmConfig.InitialFuel
		}
	}

	// 11. Populate outputs from raw results if not already set by host functions
	if callErr == nil && len(rawResults) > 0 {
		outputsMu.Lock()
		if _, exists := outputs["result"]; !exists {
			buf := make([]byte, 8)
			binary.LittleEndian.PutUint64(buf, rawResults[0])
			outputs["result"] = buf
		}
		for i, resVal := range rawResults {
			key := fmt.Sprintf("result_%d", i)
			if _, exists := outputs[key]; !exists {
				buf := make([]byte, 8)
				binary.LittleEndian.PutUint64(buf, resVal)
				outputs[key] = buf
			}
		}
		outputsMu.Unlock()
	}

	// 12. Record execution metric event
	r.recordMetric(ExecutionEvent{
		PackageID:     pkgID,
		ApplicationID: appID,
		BuildID:       buildID,
		Function:      req.Function,
		SessionID:     grant.SessionID,
		Duration:      duration,
		FuelUsed:      fuelUsed,
		Error:         callErr,
		Timestamp:     startTime,
	})

	if callErr != nil {
		return nil, fmt.Errorf("runtime: execution failed: %w", callErr)
	}

	outputsMu.Lock()
	copiedOutputs := make(map[string][]byte, len(outputs))
	for k, val := range outputs {
		vCopy := make([]byte, len(val))
		copy(vCopy, val)
		copiedOutputs[k] = vCopy
	}
	outputsMu.Unlock()

	return &ExecutionResult{
		Outputs:   copiedOutputs,
		FuelUsed:  fuelUsed,
		Duration:  duration.Nanoseconds(),
		SessionID: grant.SessionID,
		Returns:   rawResults,
	}, nil
}

// resolveCallArguments prepares numerical arguments for calling an exported function.
func (r *Runtime) resolveCallArguments(
	module *ir.Module,
	targetExport ir.Export,
	req ExecutionRequest,
	inputs map[string][]byte,
) []uint64 {
	if len(req.Args) > 0 {
		return req.Args
	}

	if int(targetExport.FuncIndex) >= len(module.Functions) {
		return nil
	}

	fn := &module.Functions[targetExport.FuncIndex]
	if int(fn.TypeIndex) >= len(module.Types) {
		return nil
	}

	fnType := module.Types[fn.TypeIndex]
	paramCount := len(fnType.Params)
	if paramCount == 0 {
		return nil
	}

	ptr := argSlicePool.Get().(*[]uint64)
	args := *ptr
	if cap(args) < paramCount {
		args = make([]uint64, 0, paramCount)
	} else {
		args = args[:0]
	}

	for i := 0; i < paramCount; i++ {
		argKey := fmt.Sprintf("arg%d", i)
		if raw, ok := inputs[argKey]; ok {
			if len(raw) == 8 {
				args = append(args, binary.LittleEndian.Uint64(raw))
				continue
			} else if len(raw) == 4 {
				args = append(args, uint64(binary.LittleEndian.Uint32(raw)))
				continue
			} else if parsed, err := strconv.ParseUint(string(raw), 10, 64); err == nil {
				args = append(args, parsed)
				continue
			}
		}
		// Default zero parameter if omitted
		args = append(args, 0)
	}

	resultArgs := make([]uint64, len(args))
	copy(resultArgs, args)
	*ptr = args[:0]
	argSlicePool.Put(ptr)

	return resultArgs
}

// buildHostFunctions constructs the set of host functions exposed to guest package execution.
func (r *Runtime) buildHostFunctions(
	module *ir.Module,
	inputs map[string][]byte,
	outputs map[string][]byte,
	outputsMu *sync.Mutex,
	customHostFuncs []vm.HostFunc,
) []vm.HostFunc {
	registered := make(map[string]bool)
	var result []vm.HostFunc

	// 1. Add all custom host functions
	for _, hf := range customHostFuncs {
		key := hf.Module + "." + hf.Name
		registered[key] = true
		result = append(result, hf)
	}

	// 2. Scan module imports and satisfy known I/O functions
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
			result = append(result, r.createGetInputLenFunc(imp.Module, imp.Name, returnArity, inputs))
			registered[key] = true
		case "get_input", "read_input":
			result = append(result, r.createGetInputFunc(imp.Module, imp.Name, returnArity, inputs))
			registered[key] = true
		case "set_output", "write_output":
			result = append(result, r.createSetOutputFunc(imp.Module, imp.Name, returnArity, outputs, outputsMu))
			registered[key] = true
		case "log":
			result = append(result, r.createLogFunc(imp.Module, imp.Name, returnArity))
			registered[key] = true
		}
	}

	// 3. Register standard fallback I/O functions for "env" and "aegis" namespaces
	standardNamespaces := []string{"env", "aegis"}
	for _, ns := range standardNamespaces {
		lenKey := ns + ".get_input_len"
		if !registered[lenKey] {
			result = append(result, r.createGetInputLenFunc(ns, "get_input_len", 1, inputs))
			registered[lenKey] = true
		}
		getKey := ns + ".get_input"
		if !registered[getKey] {
			result = append(result, r.createGetInputFunc(ns, "get_input", 1, inputs))
			registered[getKey] = true
		}
		setKey := ns + ".set_output"
		if !registered[setKey] {
			result = append(result, r.createSetOutputFunc(ns, "set_output", 0, outputs, outputsMu))
			registered[setKey] = true
		}
	}

	return result
}

func (r *Runtime) createGetInputLenFunc(
	modName, fnName string,
	returnArity int,
	inputs map[string][]byte,
) vm.HostFunc {
	return vm.NewHostFunc(modName, fnName, func(ctx context.Context, v *vm.VM, args []uint64) ([]uint64, error) {
		if len(args) < 2 {
			return nil, errors.New("runtime: get_input_len requires 2 arguments: (key_ptr, key_len)")
		}
		keyBytes, err := v.ReadMemory(uint32(args[0]), uint32(args[1]))
		if err != nil {
			return nil, fmt.Errorf("runtime: failed to read input key from memory: %w", err)
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

func (r *Runtime) createGetInputFunc(
	modName, fnName string,
	returnArity int,
	inputs map[string][]byte,
) vm.HostFunc {
	return vm.NewHostFunc(modName, fnName, func(ctx context.Context, v *vm.VM, args []uint64) ([]uint64, error) {
		if len(args) < 3 {
			return nil, errors.New("runtime: get_input requires at least 3 arguments: (key_ptr, key_len, dst_ptr)")
		}
		keyBytes, err := v.ReadMemory(uint32(args[0]), uint32(args[1]))
		if err != nil {
			return nil, fmt.Errorf("runtime: failed to read input key from memory: %w", err)
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
			return nil, fmt.Errorf("runtime: failed to write input into memory: %w", err)
		}

		if returnArity > 0 {
			return []uint64{uint64(len(toCopy))}, nil
		}
		return nil, nil
	})
}

func (r *Runtime) createSetOutputFunc(
	modName, fnName string,
	returnArity int,
	outputs map[string][]byte,
	outputsMu *sync.Mutex,
) vm.HostFunc {
	return vm.NewHostFunc(modName, fnName, func(ctx context.Context, v *vm.VM, args []uint64) ([]uint64, error) {
		if len(args) < 4 {
			return nil, errors.New("runtime: set_output requires 4 arguments: (key_ptr, key_len, val_ptr, val_len)")
		}
		keyBytes, err := v.ReadMemory(uint32(args[0]), uint32(args[1]))
		if err != nil {
			return nil, fmt.Errorf("runtime: failed to read output key from memory: %w", err)
		}
		valBytes, err := v.ReadMemory(uint32(args[2]), uint32(args[3]))
		if err != nil {
			return nil, fmt.Errorf("runtime: failed to read output value from memory: %w", err)
		}

		key := string(keyBytes)
		valCopy := make([]byte, len(valBytes))
		copy(valCopy, valBytes)

		outputsMu.Lock()
		outputs[key] = valCopy
		outputsMu.Unlock()

		if returnArity > 0 {
			return []uint64{uint64(len(valCopy))}, nil
		}
		return nil, nil
	})
}

func (r *Runtime) createLogFunc(modName, fnName string, returnArity int) vm.HostFunc {
	return vm.NewHostFunc(modName, fnName, func(ctx context.Context, v *vm.VM, args []uint64) ([]uint64, error) {
		// Log message host function: (msg_ptr, msg_len)
		if len(args) >= 2 {
			_, _ = v.ReadMemory(uint32(args[0]), uint32(args[1]))
		}
		if returnArity > 0 {
			return []uint64{0}, nil
		}
		return nil, nil
	})
}

func (r *Runtime) recordMetric(event ExecutionEvent) {
	if r.metrics != nil {
		r.metrics.RecordExecution(event)
	}
}

// Close terminates the runtime and releases all allocated resources.
func (r *Runtime) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.closed = true
	r.hostFuncs = nil
	return nil
}
