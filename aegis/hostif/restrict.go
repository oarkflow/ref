package hostif

import (
	"context"
	"fmt"
	"sync/atomic"

	"github.com/oarkflow/ref/aegis/vm"
)

// WrapHostFunc creates a new vm.HostFunc that wraps the provided host function with
// capability filtering, grant validation, invocation quotas, and data boundary enforcement.
func WrapHostFunc(hf vm.HostFunc, policy Policy, reg *Registry) vm.HostFunc {
	var fallbackCallCounter atomic.Int64

	wrappedFunc := func(ctx context.Context, v *vm.VM, args []uint64) ([]uint64, error) {
		// 1. Verify capability requirements
		if policy.Capability != "" {
			permitted := false
			if hasCap, isRestricted := ContextHasCapability(ctx, policy.Capability); isRestricted {
				permitted = hasCap
			} else if reg != nil {
				permitted = reg.HasCapability(policy.Capability)
			}

			if !permitted {
				return nil, fmt.Errorf("%w: function %s.%s requires capability %q",
					ErrCapabilityDenied, hf.Module, hf.Name, policy.Capability)
			}
		}

		// 2. Verify explicit grant requirements
		if policy.RequiresGrant {
			granted := false
			key := hf.Module + "." + hf.Name

			if ContextHasGrant(ctx, policy.Capability) ||
				ContextHasGrant(ctx, key) ||
				ContextHasGrant(ctx, hf.Name) {
				granted = true
			} else if reg != nil {
				if reg.HasGrant(policy.Capability) ||
					reg.HasGrant(key) ||
					reg.HasGrant(hf.Name) {
					granted = true
				}
			}

			if !granted {
				return nil, fmt.Errorf("%w: function %s.%s requires an explicit grant for capability %q",
					ErrGrantRequired, hf.Module, hf.Name, policy.Capability)
			}
		}

		// 3. Enforce execution invocation quota
		if policy.MaxCallsPerExecution > 0 {
			key := hf.Module + "." + hf.Name
			var currentCalls int64

			if carrier, ok := CarrierFromContext(ctx); ok && carrier != nil {
				currentCalls = int64(carrier.IncrementCall(key))
			} else if reg != nil {
				currentCalls = reg.incrementCallCount(key)
			} else {
				currentCalls = fallbackCallCounter.Add(1)
			}

			if currentCalls > int64(policy.MaxCallsPerExecution) {
				return nil, fmt.Errorf("%w: function %s.%s exceeded execution limit of %d calls (attempt %d)",
					ErrLimitExceeded, hf.Module, hf.Name, policy.MaxCallsPerExecution, currentCalls)
			}
		}

		// 4. Enforce data transfer limit based on function signatures
		if policy.MaxDataBytes > 0 {
			if err := inspectAndEnforceDataLimit(hf.Name, policy.MaxDataBytes, args); err != nil {
				return nil, err
			}
		}

		// 5. Invoke underlying implementation
		return hf.Func(ctx, v, args)
	}

	return vm.HostFunc{
		Module: hf.Module,
		Name:   hf.Name,
		Func:   wrappedFunc,
	}
}

// inspectAndEnforceDataLimit inspects function arguments to detect buffer sizes and enforce MaxDataBytes.
func inspectAndEnforceDataLimit(funcName string, maxBytes int, args []uint64) error {
	var requestedBytes int

	switch funcName {
	case "read_input":
		// read_input(dest_ptr, max_len) or read_input(idx, dest_ptr, max_len) or read_input(name_ptr, name_len, dest_ptr, max_len)
		switch len(args) {
		case 2:
			requestedBytes = int(args[1])
		case 3:
			requestedBytes = int(args[2])
		case 4:
			requestedBytes = int(args[3])
		}
	case "write_output":
		// write_output(src_ptr, len) or write_output(idx, src_ptr, len) or write_output(name_ptr, name_len, src_ptr, len)
		switch len(args) {
		case 2:
			requestedBytes = int(args[1])
		case 3:
			requestedBytes = int(args[2])
		case 4:
			requestedBytes = int(args[3])
		}
	case "hash_sha256":
		// hash_sha256(src_ptr, src_len, ...)
		if len(args) >= 2 {
			requestedBytes = int(args[1])
		}
	case "random_bytes":
		// random_bytes(dest_ptr, length)
		if len(args) >= 2 {
			requestedBytes = int(args[1])
		}
	case "log":
		// log(msg_ptr, msg_len) or log(level, msg_ptr, msg_len)
		if len(args) == 2 {
			requestedBytes = int(args[1])
		} else if len(args) >= 3 {
			requestedBytes = int(args[2])
		}
	default:
		// For arbitrary custom host functions, if 2 or more arguments are passed and last is size
		if len(args) >= 2 {
			requestedBytes = int(args[len(args)-1])
		}
	}

	if requestedBytes > maxBytes {
		return fmt.Errorf("%w: requested %d bytes exceeds policy limit of %d bytes",
			ErrDataLimitExceeded, requestedBytes, maxBytes)
	}
	return nil
}

// CheckDataLimit is a utility helper to verify that a byte length does not exceed policy constraints.
func CheckDataLimit(policy Policy, length int) error {
	if policy.MaxDataBytes > 0 && length > policy.MaxDataBytes {
		return fmt.Errorf("%w: data size %d exceeds limit of %d bytes",
			ErrDataLimitExceeded, length, policy.MaxDataBytes)
	}
	return nil
}
