package compiler_test

import (
	"context"
	"strings"
	"testing"

	"github.com/oarkflow/ref/aegis/compiler"
	"github.com/oarkflow/ref/aegis/ir"
	"github.com/oarkflow/ref/aegis/policy"
	"github.com/oarkflow/ref/aegis/vm"
)

// TestCompile_PricingCalculation tests compiling and executing a business pricing formula:
// calculatePrice(qty int32, unitPrice int32, discountPercent int32) int32
func TestCompile_PricingCalculation(t *testing.T) {
	src := `
package main

func calculatePrice(qty int32, unitPrice int32, discountPercent int32) int32 {
	subtotal := qty * unitPrice
	discount := (subtotal * discountPercent) / 100
	return subtotal - discount
}
`

	comp := compiler.New(compiler.Config{})
	res, err := comp.CompileSource(src, []string{"calculatePrice"})
	if err != nil {
		t.Fatalf("CompileSource failed: %v", err)
	}

	if err := ir.Validate(res.Module); err != nil {
		t.Fatalf("IR validation failed: %v", err)
	}

	if res.Report.FunctionsCompiled != 1 {
		t.Errorf("expected 1 function compiled, got %d", res.Report.FunctionsCompiled)
	}

	// Instantiate VM and verify execution
	v, err := vm.New(res.Module, vm.Config{})
	if err != nil {
		t.Fatalf("vm.New failed: %v", err)
	}

	ctx := context.Background()
	// qty: 10, unitPrice: 100, discountPercent: 20 -> 1000 - 200 = 800
	results, err := v.Call(ctx, "calculatePrice", 10, 100, 20)
	if err != nil {
		t.Fatalf("v.Call failed: %v", err)
	}
	if len(results) != 1 || results[0] != 800 {
		t.Fatalf("expected result 800, got %v", results)
	}

	// Test with 0 discount: 5 * 50 = 250
	results, err = v.Call(ctx, "calculatePrice", 5, 50, 0)
	if err != nil {
		t.Fatalf("v.Call failed: %v", err)
	}
	if len(results) != 1 || results[0] != 250 {
		t.Fatalf("expected result 250, got %v", results)
	}
}

// TestCompile_FactorialLoop tests a for loop with multiplication and mutation.
func TestCompile_FactorialLoop(t *testing.T) {
	src := `
package main

func Factorial(n int32) int32 {
	var result int32 = 1
	for i := int32(1); i <= n; i++ {
		result = result * i
	}
	return result
}
`

	comp := compiler.New(compiler.Config{
		SourceCode: src,
	})
	res, err := comp.Compile()
	if err != nil {
		t.Fatalf("Compile failed: %v", err)
	}

	if err := ir.Validate(res.Module); err != nil {
		t.Fatalf("IR validation failed: %v", err)
	}

	v, err := vm.New(res.Module, vm.Config{})
	if err != nil {
		t.Fatalf("vm.New failed: %v", err)
	}

	ctx := context.Background()
	// Factorial(5) = 120
	results, err := v.Call(ctx, "Factorial", 5)
	if err != nil {
		t.Fatalf("v.Call failed: %v", err)
	}
	if len(results) != 1 || results[0] != 120 {
		t.Fatalf("expected 120, got %v", results)
	}

	// Factorial(0) = 1
	results, err = v.Call(ctx, "Factorial", 0)
	if err != nil {
		t.Fatalf("v.Call failed: %v", err)
	}
	if len(results) != 1 || results[0] != 1 {
		t.Fatalf("expected 1, got %v", results)
	}
}

// TestCompile_FibonacciLoop tests for loop with break, multiple locals, and addition.
func TestCompile_FibonacciLoop(t *testing.T) {
	src := `
package main

func Fibonacci(n int32) int32 {
	if n <= 0 {
		return 0
	}
	if n == 1 {
		return 1
	}
	var a int32 = 0
	var b int32 = 1
	for i := int32(2); i <= n; i++ {
		c := a + b
		a = b
		b = c
	}
	return b
}
`

	comp := compiler.New(compiler.Config{
		SourceCode: src,
	})
	res, err := comp.Compile()
	if err != nil {
		t.Fatalf("Compile failed: %v", err)
	}

	v, err := vm.New(res.Module, vm.Config{})
	if err != nil {
		t.Fatalf("vm.New failed: %v", err)
	}

	ctx := context.Background()
	// Fibonacci(10) = 55
	results, err := v.Call(ctx, "Fibonacci", 10)
	if err != nil {
		t.Fatalf("v.Call failed: %v", err)
	}
	if len(results) != 1 || results[0] != 55 {
		t.Fatalf("expected 55, got %v", results)
	}

	// Fibonacci(1) = 1
	results, err = v.Call(ctx, "Fibonacci", 1)
	if err != nil {
		t.Fatalf("v.Call failed: %v", err)
	}
	if len(results) != 1 || results[0] != 1 {
		t.Fatalf("expected 1, got %v", results)
	}
}

// TestCompile_MultiFunctionCall tests calling an internal function from another function.
func TestCompile_MultiFunctionCall(t *testing.T) {
	src := `
package main

func square(x int32) int32 {
	return x * x
}

func SumOfSquares(a int32, b int32) int32 {
	return square(a) + square(b)
}
`

	comp := compiler.New(compiler.Config{
		SourceCode: src,
	})
	res, err := comp.Compile()
	if err != nil {
		t.Fatalf("Compile failed: %v", err)
	}

	if err := ir.Validate(res.Module); err != nil {
		t.Fatalf("IR validation failed: %v", err)
	}

	if res.Report.FunctionsCompiled != 2 {
		t.Fatalf("expected 2 functions compiled, got %d", res.Report.FunctionsCompiled)
	}

	v, err := vm.New(res.Module, vm.Config{})
	if err != nil {
		t.Fatalf("vm.New failed: %v", err)
	}

	ctx := context.Background()
	// 3^2 + 4^2 = 9 + 16 = 25
	results, err := v.Call(ctx, "SumOfSquares", 3, 4)
	if err != nil {
		t.Fatalf("v.Call failed: %v", err)
	}
	if len(results) != 1 || results[0] != 25 {
		t.Fatalf("expected 25, got %v", results)
	}
}

// TestCompile_HostCalls tests calling host interface functions (OpCallImport).
func TestCompile_HostCalls(t *testing.T) {
	src := `
package main

func ReadAndEcho(dest int32, maxLen int32) int32 {
	copied := host.read_input(dest, maxLen)
	host.write_output(dest, copied)
	return copied
}
`

	comp := compiler.New(compiler.Config{})
	res, err := comp.CompileSource(src, []string{"ReadAndEcho"})
	if err != nil {
		t.Fatalf("CompileSource failed: %v", err)
	}

	if len(res.Report.ImportsUsed) != 2 {
		t.Fatalf("expected 2 imports used, got %v", res.Report.ImportsUsed)
	}

	if err := ir.Validate(res.Module); err != nil {
		t.Fatalf("IR validation failed: %v", err)
	}

	// Register host mocks
	var writtenData []byte
	hostFuncs := []vm.HostFunc{
		{
			Module: "host",
			Name:   "read_input",
			Func: func(ctx context.Context, v *vm.VM, args []uint64) ([]uint64, error) {
				dest := uint32(args[0])
				payload := []byte("confidential-payload")
				if err := v.WriteMemory(dest, payload); err != nil {
					return nil, err
				}
				return []uint64{uint64(len(payload))}, nil
			},
		},
		{
			Module: "host",
			Name:   "write_output",
			Func: func(ctx context.Context, v *vm.VM, args []uint64) ([]uint64, error) {
				src := uint32(args[0])
				length := uint32(args[1])
				data, err := v.ReadMemory(src, length)
				if err != nil {
					return nil, err
				}
				writtenData = append([]byte(nil), data...)
				return []uint64{uint64(length)}, nil
			},
		},
	}

	v, err := vm.New(res.Module, vm.Config{}, hostFuncs...)
	if err != nil {
		t.Fatalf("vm.New failed: %v", err)
	}

	results, err := v.Call(context.Background(), "ReadAndEcho", 128, 512)
	if err != nil {
		t.Fatalf("v.Call failed: %v", err)
	}

	if len(results) != 1 || results[0] != uint64(len("confidential-payload")) {
		t.Fatalf("expected length %d, got %v", len("confidential-payload"), results)
	}

	if string(writtenData) != "confidential-payload" {
		t.Fatalf("expected written data %q, got %q", "confidential-payload", string(writtenData))
	}
}

// TestCompile_RejectionOfUnsupportedConstructs verifies rejection of goroutines, channels, etc.
func TestCompile_RejectionOfUnsupportedConstructs(t *testing.T) {
	cases := []struct {
		name        string
		src         string
		errContains string
	}{
		{
			name: "goroutines",
			src: `
package main
func Bad() {
	go func() {}()
}
`,
			errContains: "goroutines (go statements) are not supported",
		},
		{
			name: "channel_type",
			src: `
package main
func Bad(ch chan int) {
}
`,
			errContains: "channels are not supported",
		},
		{
			name: "channel_send",
			src: `
package main
func Bad(ch any) {
	ch <- 1
}
`,
			errContains: "channel send operations are not supported",
		},
		{
			name: "select_statement",
			src: `
package main
func Bad() {
	select {}
}
`,
			errContains: "select statements are not supported",
		},
		{
			name: "defer_statement",
			src: `
package main
func Bad() {
	defer func() {}()
}
`,
			errContains: "defer statements are not supported",
		},
		{
			name: "interface_type",
			src: `
package main
type Reader interface {
	Read() int
}
func Bad(r Reader) {}
`,
			errContains: "interfaces are not supported",
		},
		{
			name: "type_assertion",
			src: `
package main
func Bad(x any) {
	_ = x.(int)
}
`,
			errContains: "type assertions are not supported",
		},
		{
			name: "pointer_type",
			src: `
package main
func Bad(p *int) {}
`,
			errContains: "pointers are not supported",
		},
		{
			name: "reflect_import",
			src: `
package main
import "reflect"
func Bad() {
	_ = reflect.TypeOf(1)
}
`,
			errContains: "package \"reflect\" is not supported",
		},
		{
			name: "unsafe_import",
			src: `
package main
import "unsafe"
func Bad() {
	_ = unsafe.Sizeof(1)
}
`,
			errContains: "package \"unsafe\" is not supported",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			comp := compiler.New(compiler.Config{SourceCode: tc.src})
			_, err := comp.Compile()
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.errContains)
			}
			if !strings.Contains(err.Error(), tc.errContains) {
				t.Fatalf("expected error containing %q, got %q", tc.errContains, err.Error())
			}
		})
	}
}

// TestCompile_PolicyEnforcement verifies applying security policies to functions.
func TestCompile_PolicyEnforcement(t *testing.T) {
	src := `
package main

func SecretFunc(x int32) int32 {
	return x ^ 42
}

func PublicFunc(x int32) int32 {
	return x + 1
}
`

	pol := &policy.Policy{
		Protection: policy.ProtectionDefaults{
			Default: policy.LevelStandard,
		},
		Functions: map[string]policy.FuncPolicy{
			"SecretFunc": {
				Confidentiality: policy.ConfidentialityCritical,
				Execution:       policy.ExecIsolatedLocal,
			},
		},
	}

	comp := compiler.New(compiler.Config{
		SourceCode: src,
		Policy:     pol,
	})

	res, err := comp.Compile()
	if err != nil {
		t.Fatalf("Compile failed: %v", err)
	}

	if res.Report.FunctionsProtected != 1 {
		t.Errorf("expected 1 function protected, got %d", res.Report.FunctionsProtected)
	}

	for _, fn := range res.Module.Functions {
		if fn.Name == "SecretFunc" {
			if fn.Attributes.Confidentiality != ir.ConfCritical {
				t.Errorf("expected ConfCritical for SecretFunc, got %v", fn.Attributes.Confidentiality)
			}
			if !fn.Attributes.MustNotInline {
				t.Errorf("expected MustNotInline for SecretFunc")
			}
		}
		if fn.Name == "PublicFunc" {
			if fn.Attributes.Confidentiality != ir.ConfStandard {
				t.Errorf("expected ConfStandard for PublicFunc, got %v", fn.Attributes.Confidentiality)
			}
		}
	}
}

// TestCompile_StripMetadata verifies function names and metadata removal.
func TestCompile_StripMetadata(t *testing.T) {
	src := `
package main

func Add(a int32, b int32) int32 {
	return a + b
}
`

	comp := compiler.New(compiler.Config{
		SourceCode:    src,
		StripMetadata: true,
	})

	res, err := comp.Compile()
	if err != nil {
		t.Fatalf("Compile failed: %v", err)
	}

	// Function internal name must be stripped
	for _, fn := range res.Module.Functions {
		if fn.Name != "" {
			t.Errorf("expected empty function name, got %q", fn.Name)
		}
	}

	// Metadata must be zeroed
	if res.Module.Metadata.CompilerVersion != "" {
		t.Errorf("expected empty metadata, got %+v", res.Module.Metadata)
	}

	// But export must still work
	v, err := vm.New(res.Module, vm.Config{})
	if err != nil {
		t.Fatalf("vm.New failed: %v", err)
	}

	results, err := v.Call(context.Background(), "Add", 15, 27)
	if err != nil {
		t.Fatalf("v.Call failed: %v", err)
	}
	if len(results) != 1 || results[0] != 42 {
		t.Fatalf("expected 42, got %v", results)
	}
}

// TestCompile_Diversify verifies binary layout diversification and random build ID generation.
func TestCompile_Diversify(t *testing.T) {
	src := `
package main

func Compute(x int32) int32 {
	return (x * 3) + 7
}
`

	comp1 := compiler.New(compiler.Config{
		SourceCode: src,
		Diversify:  true,
	})
	res1, err := comp1.Compile()
	if err != nil {
		t.Fatalf("Compile 1 failed: %v", err)
	}

	comp2 := compiler.New(compiler.Config{
		SourceCode: src,
		Diversify:  true,
	})
	res2, err := comp2.Compile()
	if err != nil {
		t.Fatalf("Compile 2 failed: %v", err)
	}

	// Verify build IDs are non-zero and distinct
	if res1.Report.BuildID == "" || res2.Report.BuildID == "" {
		t.Errorf("expected non-empty build IDs")
	}
	if res1.Report.BuildID == res2.Report.BuildID {
		t.Errorf("expected diversified builds to have different build IDs")
	}

	// Both modules must still produce the correct execution result
	v1, err := vm.New(res1.Module, vm.Config{})
	if err != nil {
		t.Fatalf("vm.New 1 failed: %v", err)
	}
	v2, err := vm.New(res2.Module, vm.Config{})
	if err != nil {
		t.Fatalf("vm.New 2 failed: %v", err)
	}

	resVal1, err := v1.Call(context.Background(), "Compute", 10)
	if err != nil || len(resVal1) != 1 || resVal1[0] != 37 {
		t.Fatalf("v1 returned unexpected %v, err=%v", resVal1, err)
	}

	resVal2, err := v2.Call(context.Background(), "Compute", 10)
	if err != nil || len(resVal2) != 1 || resVal2[0] != 37 {
		t.Fatalf("v2 returned unexpected %v, err=%v", resVal2, err)
	}
}

// TestCompile_LogicalShortCircuit verifies short-circuiting in && and ||.
func TestCompile_LogicalShortCircuit(t *testing.T) {
	src := `
package main

func CheckAnd(a int32, b int32) int32 {
	if a > 0 && b > 0 {
		return 1
	}
	return 0
}

func CheckOr(a int32, b int32) int32 {
	if a > 0 || b > 0 {
		return 1
	}
	return 0
}
`

	comp := compiler.New(compiler.Config{SourceCode: src})
	res, err := comp.Compile()
	if err != nil {
		t.Fatalf("Compile failed: %v", err)
	}

	v, err := vm.New(res.Module, vm.Config{})
	if err != nil {
		t.Fatalf("vm.New failed: %v", err)
	}

	ctx := context.Background()

	// a=1, b=1 -> AND = 1
	r, _ := v.Call(ctx, "CheckAnd", 1, 1)
	if r[0] != 1 {
		t.Errorf("CheckAnd(1, 1) expected 1, got %d", r[0])
	}

	// a=0, b=1 -> AND = 0
	r, _ = v.Call(ctx, "CheckAnd", 0, 1)
	if r[0] != 0 {
		t.Errorf("CheckAnd(0, 1) expected 0, got %d", r[0])
	}

	// a=0, b=1 -> OR = 1
	r, _ = v.Call(ctx, "CheckOr", 0, 1)
	if r[0] != 1 {
		t.Errorf("CheckOr(0, 1) expected 1, got %d", r[0])
	}

	// a=0, b=0 -> OR = 0
	r, _ = v.Call(ctx, "CheckOr", 0, 0)
	if r[0] != 0 {
		t.Errorf("CheckOr(0, 0) expected 0, got %d", r[0])
	}
}

// TestCompile_BitwiseOperations tests &, |, ^, <<, >>.
func TestCompile_BitwiseOperations(t *testing.T) {
	src := `
package main

func Bitwise(a int32, b int32) int32 {
	andVal := a & b
	orVal := a | b
	xorVal := andVal ^ orVal
	shlVal := xorVal << 2
	shrVal := shlVal >> 1
	return shrVal
}
`

	comp := compiler.New(compiler.Config{SourceCode: src})
	res, err := comp.Compile()
	if err != nil {
		t.Fatalf("Compile failed: %v", err)
	}

	v, err := vm.New(res.Module, vm.Config{})
	if err != nil {
		t.Fatalf("vm.New failed: %v", err)
	}

	// a=0b1100 (12), b=0b1010 (10)
	// andVal = 0b1000 (8)
	// orVal = 0b1110 (14)
	// xorVal = 8 ^ 14 = 6
	// shlVal = 6 << 2 = 24
	// shrVal = 24 >> 1 = 12
	results, err := v.Call(context.Background(), "Bitwise", 12, 10)
	if err != nil {
		t.Fatalf("v.Call failed: %v", err)
	}
	if len(results) != 1 || results[0] != 12 {
		t.Fatalf("expected 12, got %v", results)
	}
}
