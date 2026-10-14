package runtime

import "time"

// ExecutionRequest specifies the execution parameters, target function, input payloads,
// and execution constraints for running a protected package.
type ExecutionRequest struct {
	// Function is the name of the exported function to invoke.
	Function string `json:"function"`

	// Inputs contains named byte buffers provided to the guest function via host I/O.
	Inputs map[string][]byte `json:"inputs,omitempty"`

	// Fuel specifies the maximum instruction budget for this execution (0 inherits runtime default).
	Fuel int64 `json:"fuel,omitempty"`

	// Args contains optional direct numeric arguments passed to the function.
	Args []uint64 `json:"args,omitempty"`
}

// ExecutionResult encapsulates the output data, resource usage, and audit metrics of an execution.
type ExecutionResult struct {
	// Outputs contains named byte buffers emitted by the guest function via host I/O.
	Outputs map[string][]byte `json:"outputs"`

	// FuelUsed is the total number of instructions consumed during execution.
	FuelUsed int64 `json:"fuel_used"`

	// Duration is the execution elapsed time in nanoseconds.
	Duration int64 `json:"duration"`

	// SessionID is the identifier of the authorization grant session used for execution.
	SessionID string `json:"session_id"`

	// Returns contains the raw return values from the invoked function.
	Returns []uint64 `json:"returns,omitempty"`
}

// ExecutionDuration returns Duration as a standard time.Duration.
func (res *ExecutionResult) ExecutionDuration() time.Duration {
	if res == nil {
		return 0
	}
	return time.Duration(res.Duration)
}
