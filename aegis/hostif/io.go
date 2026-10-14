package hostif

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/oarkflow/ref/aegis/vm"
)

// contextKey provides unique context value keys within hostif.
type contextKey struct {
	name string
}

var (
	carrierKey      = &contextKey{name: "hostif.Carrier"}
	grantsKey       = &contextKey{name: "hostif.Grants"}
	capabilitiesKey = &contextKey{name: "hostif.Capabilities"}
)

// Carrier provides a thread-safe carrier for host-guest inputs, outputs, logs,
// execution time overrides, and invocation quotas.
type Carrier struct {
	mu             sync.RWMutex
	defaultInput   []byte
	namedInputs    map[string][]byte
	indexedInputs  [][]byte
	defaultOutput  bytes.Buffer
	namedOutputs   map[string][]byte
	indexedOutputs map[int][]byte
	logs           []string
	logger         Logger
	nowFunc        func() time.Time
	callCounts     map[string]int
	grants         map[string]bool
	lastHash       []byte
}

// NewCarrier constructs an initialized thread-safe Carrier.
func NewCarrier() *Carrier {
	return &Carrier{
		namedInputs:    make(map[string][]byte),
		indexedInputs:  make([][]byte, 0, 4),
		namedOutputs:   make(map[string][]byte),
		indexedOutputs: make(map[int][]byte),
		logs:           make([]string, 0, 16),
		callCounts:     make(map[string]int),
		grants:         make(map[string]bool),
	}
}

// SetInput sets the default raw input payload, copying the provided bytes.
func (c *Carrier) SetInput(data []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.defaultInput = append([]byte(nil), data...)
}

// Input returns a copy of the default raw input payload.
func (c *Carrier) Input() []byte {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return append([]byte(nil), c.defaultInput...)
}

// SetNamedInput assigns an input payload under the specified name.
func (c *Carrier) SetNamedInput(name string, data []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.namedInputs[name] = append([]byte(nil), data...)
}

// GetNamedInput retrieves an input payload by name.
func (c *Carrier) GetNamedInput(name string) ([]byte, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	val, ok := c.namedInputs[name]
	if !ok {
		return nil, false
	}
	return append([]byte(nil), val...), true
}

// SetIndexedInput stores an input payload at the specified 0-based index.
func (c *Carrier) SetIndexedInput(index int, data []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if index < 0 {
		return
	}
	if index >= len(c.indexedInputs) {
		extended := make([][]byte, index+1)
		copy(extended, c.indexedInputs)
		c.indexedInputs = extended
	}
	c.indexedInputs[index] = append([]byte(nil), data...)
}

// GetIndexedInput retrieves an input payload at the specified 0-based index.
func (c *Carrier) GetIndexedInput(index int) ([]byte, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if index < 0 || index >= len(c.indexedInputs) || c.indexedInputs[index] == nil {
		return nil, false
	}
	return append([]byte(nil), c.indexedInputs[index]...), true
}

// WriteOutput appends bytes to the default output buffer.
func (c *Carrier) WriteOutput(data []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.defaultOutput.Write(data)
}

// Output returns a copy of all accumulated default output bytes.
func (c *Carrier) Output() []byte {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return append([]byte(nil), c.defaultOutput.Bytes()...)
}

// ResetOutput clears the default output buffer.
func (c *Carrier) ResetOutput() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.defaultOutput.Reset()
}

// SetNamedOutput associates an output payload with a name.
func (c *Carrier) SetNamedOutput(name string, data []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.namedOutputs[name] = append([]byte(nil), data...)
}

// GetNamedOutput retrieves an output payload by name.
func (c *Carrier) GetNamedOutput(name string) ([]byte, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	val, ok := c.namedOutputs[name]
	if !ok {
		return nil, false
	}
	return append([]byte(nil), val...), true
}

// NamedOutputs returns a copy of all named outputs.
func (c *Carrier) NamedOutputs() map[string][]byte {
	c.mu.RLock()
	defer c.mu.RUnlock()
	res := make(map[string][]byte, len(c.namedOutputs))
	for k, v := range c.namedOutputs {
		res[k] = append([]byte(nil), v...)
	}
	return res
}

// SetIndexedOutput associates an output payload with an index.
func (c *Carrier) SetIndexedOutput(index int, data []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.indexedOutputs[index] = append([]byte(nil), data...)
}

// GetIndexedOutput retrieves an output payload by index.
func (c *Carrier) GetIndexedOutput(index int) ([]byte, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	val, ok := c.indexedOutputs[index]
	if !ok {
		return nil, false
	}
	return append([]byte(nil), val...), true
}

// IndexedOutputs returns a copy of all indexed outputs.
func (c *Carrier) IndexedOutputs() map[int][]byte {
	c.mu.RLock()
	defer c.mu.RUnlock()
	res := make(map[int][]byte, len(c.indexedOutputs))
	for k, v := range c.indexedOutputs {
		res[k] = append([]byte(nil), v...)
	}
	return res
}

// AppendLog appends a log entry to the carrier log buffer.
func (c *Carrier) AppendLog(entry string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.logs = append(c.logs, entry)
}

// Logs returns a copy of all logged trace messages.
func (c *Carrier) Logs() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return append([]string(nil), c.logs...)
}

// ClearLogs clears the trace log buffer.
func (c *Carrier) ClearLogs() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.logs = c.logs[:0]
}

// SetLogger attaches an external Logger callback to the carrier.
func (c *Carrier) SetLogger(logger Logger) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.logger = logger
}

// SetNowFunc configures a custom time generator (useful for deterministic tests).
func (c *Carrier) SetNowFunc(fn func() time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nowFunc = fn
}

// Now returns the current time from nowFunc, or system clock if unset.
func (c *Carrier) Now() time.Time {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.nowFunc != nil {
		return c.nowFunc()
	}
	return time.Now()
}

// IncrementCall increments the invocation count for a function key and returns new count.
func (c *Carrier) IncrementCall(key string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.callCounts[key]++
	return c.callCounts[key]
}

// CallCount returns the invocation count recorded for a function key.
func (c *Carrier) CallCount(key string) int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.callCounts[key]
}

// ResetCalls clears all invocation counters recorded in this carrier.
func (c *Carrier) ResetCalls() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.callCounts = make(map[string]int)
}

// Grant adds an explicit grant for target in this carrier.
func (c *Carrier) Grant(target string) {
	if target == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.grants[target] = true
}

// RevokeGrant removes an explicit grant for target in this carrier.
func (c *Carrier) RevokeGrant(target string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.grants, target)
}

// HasGrant reports whether target has an explicit grant in this carrier.
func (c *Carrier) HasGrant(target string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.grants[target]
}

// SetLastHash stores the most recent cryptographic hash digest.
func (c *Carrier) SetLastHash(hash []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastHash = append([]byte(nil), hash...)
}

// LastHash retrieves the most recent cryptographic hash digest.
func (c *Carrier) LastHash() []byte {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return append([]byte(nil), c.lastHash...)
}

// Reset clears outputs, logs, and invocation counts, leaving inputs intact.
func (c *Carrier) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.defaultOutput.Reset()
	c.namedOutputs = make(map[string][]byte)
	c.indexedOutputs = make(map[int][]byte)
	c.logs = c.logs[:0]
	c.callCounts = make(map[string]int)
	c.lastHash = nil
}

// WithCarrier stores the carrier in ctx.
func WithCarrier(ctx context.Context, carrier *Carrier) context.Context {
	return context.WithValue(ctx, carrierKey, carrier)
}

// CarrierFromContext returns the carrier stored in ctx, if any.
func CarrierFromContext(ctx context.Context) (*Carrier, bool) {
	if ctx == nil {
		return nil, false
	}
	c, ok := ctx.Value(carrierKey).(*Carrier)
	return c, ok && c != nil
}

// WithGrant adds target to granted capabilities or functions in the execution context.
func WithGrant(ctx context.Context, target string) context.Context {
	if target == "" {
		return ctx
	}
	var current map[string]bool
	if existing, ok := ctx.Value(grantsKey).(map[string]bool); ok {
		current = make(map[string]bool, len(existing)+1)
		for k, v := range existing {
			current[k] = v
		}
	} else {
		current = make(map[string]bool, 1)
	}
	current[target] = true
	return context.WithValue(ctx, grantsKey, current)
}

// ContextHasGrant checks if target is granted in context or carrier.
func ContextHasGrant(ctx context.Context, target string) bool {
	if ctx == nil {
		return false
	}
	if carrier, ok := CarrierFromContext(ctx); ok && carrier.HasGrant(target) {
		return true
	}
	if m, ok := ctx.Value(grantsKey).(map[string]bool); ok {
		return m[target]
	}
	return false
}

// WithCapabilities sets an explicit capability allowlist on context.
func WithCapabilities(ctx context.Context, caps ...string) context.Context {
	allowlist := make(map[string]bool, len(caps))
	for _, c := range caps {
		if c != "" {
			allowlist[c] = true
		}
	}
	return context.WithValue(ctx, capabilitiesKey, allowlist)
}

// ContextHasCapability checks if cap is permitted in context allowlist.
// Returns (permitted, isRestricted). If isRestricted is false, context did not restrict capabilities.
func ContextHasCapability(ctx context.Context, capability string) (bool, bool) {
	if ctx == nil {
		return false, false
	}
	if m, ok := ctx.Value(capabilitiesKey).(map[string]bool); ok {
		return m[capability], true
	}
	return false, false
}

// ReadInputHostFunc constructs the "host.read_input" host function.
//
// Calling conventions supported:
//   - 2 args: read_input(dest_ptr, max_len) -> reads default input
//   - 3 args: read_input(index, dest_ptr, max_len) -> reads indexed input
//   - 4 args: read_input(name_ptr, name_len, dest_ptr, max_len) -> reads named input
//
// In all cases, if max_len == 0, the function returns the available byte length without copying.
func ReadInputHostFunc(fallbackCarrier *Carrier) vm.HostFunc {
	return vm.HostFunc{
		Module: "host",
		Name:   "read_input",
		Func: func(ctx context.Context, v *vm.VM, args []uint64) ([]uint64, error) {
			carrier, ok := CarrierFromContext(ctx)
			if !ok {
				carrier = fallbackCarrier
			}
			if carrier == nil {
				return nil, fmt.Errorf("%w: read_input requires a carrier in context or registry", ErrCarrierNotFound)
			}
			if v == nil {
				return nil, ErrNilVM
			}

			var input []byte
			var destPtr, maxLen uint32

			switch len(args) {
			case 2:
				destPtr = uint32(args[0])
				maxLen = uint32(args[1])
				input = carrier.Input()

			case 3:
				index := int(args[0])
				destPtr = uint32(args[1])
				maxLen = uint32(args[2])
				var found bool
				input, found = carrier.GetIndexedInput(index)
				if !found {
					return []uint64{0}, nil
				}

			case 4:
				namePtr := uint32(args[0])
				nameLen := uint32(args[1])
				destPtr = uint32(args[2])
				maxLen = uint32(args[3])

				nameBytes, err := v.ReadMemory(namePtr, nameLen)
				if err != nil {
					return nil, fmt.Errorf("hostif: read_input failed to read input name: %w", err)
				}
				var found bool
				input, found = carrier.GetNamedInput(string(nameBytes))
				if !found {
					return []uint64{0}, nil
				}

			default:
				return nil, fmt.Errorf("hostif: read_input requires 2, 3, or 4 arguments, got %d", len(args))
			}

			// If maxLen is 0, caller is probing available input length
			if maxLen == 0 {
				return []uint64{uint64(len(input))}, nil
			}

			copyLen := len(input)
			if copyLen > int(maxLen) {
				copyLen = int(maxLen)
			}

			if copyLen > 0 {
				if err := v.WriteMemory(destPtr, input[:copyLen]); err != nil {
					return nil, fmt.Errorf("hostif: read_input failed to write memory: %w", err)
				}
			}

			return []uint64{uint64(copyLen)}, nil
		},
	}
}

// WriteOutputHostFunc constructs the "host.write_output" host function.
//
// Calling conventions supported:
//   - 2 args: write_output(src_ptr, len) -> writes to default output
//   - 3 args: write_output(index, src_ptr, len) -> writes to indexed output
//   - 4 args: write_output(name_ptr, name_len, src_ptr, len) -> writes to named output
func WriteOutputHostFunc(fallbackCarrier *Carrier) vm.HostFunc {
	return vm.HostFunc{
		Module: "host",
		Name:   "write_output",
		Func: func(ctx context.Context, v *vm.VM, args []uint64) ([]uint64, error) {
			carrier, ok := CarrierFromContext(ctx)
			if !ok {
				carrier = fallbackCarrier
			}
			if carrier == nil {
				return nil, fmt.Errorf("%w: write_output requires a carrier in context or registry", ErrCarrierNotFound)
			}
			if v == nil {
				return nil, ErrNilVM
			}

			switch len(args) {
			case 2:
				srcPtr := uint32(args[0])
				dataLen := uint32(args[1])
				data, err := v.ReadMemory(srcPtr, dataLen)
				if err != nil {
					return nil, fmt.Errorf("hostif: write_output failed to read linear memory: %w", err)
				}
				carrier.WriteOutput(data)
				return []uint64{uint64(dataLen)}, nil

			case 3:
				index := int(args[0])
				srcPtr := uint32(args[1])
				dataLen := uint32(args[2])
				data, err := v.ReadMemory(srcPtr, dataLen)
				if err != nil {
					return nil, fmt.Errorf("hostif: write_output failed to read linear memory: %w", err)
				}
				carrier.SetIndexedOutput(index, data)
				return []uint64{uint64(dataLen)}, nil

			case 4:
				namePtr := uint32(args[0])
				nameLen := uint32(args[1])
				srcPtr := uint32(args[2])
				dataLen := uint32(args[3])

				nameBytes, err := v.ReadMemory(namePtr, nameLen)
				if err != nil {
					return nil, fmt.Errorf("hostif: write_output failed to read output name: %w", err)
				}
				data, err := v.ReadMemory(srcPtr, dataLen)
				if err != nil {
					return nil, fmt.Errorf("hostif: write_output failed to read linear memory: %w", err)
				}
				carrier.SetNamedOutput(string(nameBytes), data)
				return []uint64{uint64(dataLen)}, nil

			default:
				return nil, fmt.Errorf("hostif: write_output requires 2, 3, or 4 arguments, got %d", len(args))
			}
		},
	}
}

// RegisterIO registers the standard I/O host functions (read_input, write_output) in reg.
func RegisterIO(reg *Registry, policies ...Policy) error {
	if reg == nil {
		return errors.New("hostif: registry is nil")
	}

	policy := Policy{Capability: CapIO}
	if len(policies) > 0 {
		policy = policies[0]
		if policy.Capability == "" {
			policy.Capability = CapIO
		}
	}

	if err := reg.Register(ReadInputHostFunc(reg.Carrier()), policy); err != nil {
		return err
	}
	if err := reg.Register(WriteOutputHostFunc(reg.Carrier()), policy); err != nil {
		return err
	}
	return nil
}
