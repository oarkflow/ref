package vm

import "context"

// HostFunc represents an external Go function provided by the host environment to satisfy IR imports.
type HostFunc struct {
	// Module specifies the import namespace (e.g., "env", "system", "crypto").
	Module string
	// Name specifies the function name within the module.
	Name string
	// Func is the Go function executed when OpCallImport is dispatched.
	Func func(ctx context.Context, vm *VM, args []uint64) ([]uint64, error)
}

// NewHostFunc creates a HostFunc definition with the provided module, name, and implementation.
func NewHostFunc(module, name string, fn func(ctx context.Context, vm *VM, args []uint64) ([]uint64, error)) HostFunc {
	return HostFunc{
		Module: module,
		Name:   name,
		Func:   fn,
	}
}
