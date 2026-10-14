package hostif

import (
	"context"
	"errors"
	"fmt"

	"github.com/oarkflow/ref/aegis/vm"
)

// LogLevel represents message severity in guest diagnostic logs.
type LogLevel uint32

const (
	// LogLevelTrace represents fine-grained diagnostic trace events.
	LogLevelTrace LogLevel = 0
	// LogLevelDebug represents debugging messages.
	LogLevelDebug LogLevel = 1
	// LogLevelInfo represents informational messages.
	LogLevelInfo LogLevel = 2
	// LogLevelWarn represents warning conditions.
	LogLevelWarn LogLevel = 3
	// LogLevelError represents error conditions.
	LogLevelError LogLevel = 4
)

// String returns the human-readable severity name.
func (l LogLevel) String() string {
	switch l {
	case LogLevelTrace:
		return "TRACE"
	case LogLevelDebug:
		return "DEBUG"
	case LogLevelInfo:
		return "INFO"
	case LogLevelWarn:
		return "WARN"
	case LogLevelError:
		return "ERROR"
	default:
		return fmt.Sprintf("LEVEL(%d)", l)
	}
}

// Logger specifies the interface for external consumers of guest logs.
type Logger interface {
	Log(level LogLevel, message string)
}

// LogHostFunc constructs the "host.log" host function.
//
// Calling conventions supported:
//   - 2 args: log(msg_ptr, msg_len) -> logs at LogLevelInfo
//   - 3 args: log(level, msg_ptr, msg_len) -> logs with specified LogLevel
func LogHostFunc(fallbackCarrier *Carrier) vm.HostFunc {
	return vm.HostFunc{
		Module: "host",
		Name:   "log",
		Func: func(ctx context.Context, v *vm.VM, args []uint64) ([]uint64, error) {
			carrier, ok := CarrierFromContext(ctx)
			if !ok {
				carrier = fallbackCarrier
			}
			if carrier == nil {
				return nil, fmt.Errorf("%w: log requires a carrier in context or registry", ErrCarrierNotFound)
			}
			if v == nil {
				return nil, ErrNilVM
			}

			var msgPtr, msgLen uint32
			level := LogLevelInfo

			switch len(args) {
			case 2:
				msgPtr = uint32(args[0])
				msgLen = uint32(args[1])
			case 3:
				level = LogLevel(args[0])
				msgPtr = uint32(args[1])
				msgLen = uint32(args[2])
			default:
				return nil, fmt.Errorf("hostif: log requires 2 or 3 arguments, got %d", len(args))
			}

			if msgLen == 0 {
				return []uint64{0}, nil
			}

			msgBytes, err := v.ReadMemory(msgPtr, msgLen)
			if err != nil {
				return nil, fmt.Errorf("hostif: log failed to read memory: %w", err)
			}

			message := string(msgBytes)
			carrier.AppendLog(fmt.Sprintf("[%s] %s", level, message))

			carrier.mu.RLock()
			externalLogger := carrier.logger
			carrier.mu.RUnlock()
			if externalLogger != nil {
				externalLogger.Log(level, message)
			}

			return []uint64{uint64(msgLen)}, nil
		},
	}
}

// RegisterLog registers the standard "host.log" host function in reg.
func RegisterLog(reg *Registry, policies ...Policy) error {
	if reg == nil {
		return errors.New("hostif: registry is nil")
	}

	policy := Policy{Capability: CapLog}
	if len(policies) > 0 {
		policy = policies[0]
		if policy.Capability == "" {
			policy.Capability = CapLog
		}
	}

	return reg.Register(LogHostFunc(reg.Carrier()), policy)
}
