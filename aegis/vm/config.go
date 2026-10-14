package vm

// Config controls VM execution bounds, memory limits, and debug instrumentation.
type Config struct {
	// MaxStackDepth is the maximum number of entries allowed on the value stack (default: 65536).
	MaxStackDepth int
	// MaxCallDepth is the maximum number of call frames allowed on the call stack (default: 1024).
	MaxCallDepth int
	// MaxMemoryPages is the hard upper limit on linear memory pages (default: 256 = 16 MiB).
	MaxMemoryPages uint32
	// InitialFuel is the instruction execution budget before forced halt (0 = unlimited).
	InitialFuel int64
	// TraceExecution enables per-instruction execution logging for debugging.
	TraceExecution bool
}

// DefaultConfig returns the standard default configuration for Aegis VM execution.
func DefaultConfig() Config {
	return Config{
		MaxStackDepth:  65536,
		MaxCallDepth:   1024,
		MaxMemoryPages: 256,
		InitialFuel:    0,
		TraceExecution: false,
	}
}
