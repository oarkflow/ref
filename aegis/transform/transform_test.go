package transform_test

import (
	"context"
	"testing"

	"github.com/oarkflow/ref/aegis/compiler"
	"github.com/oarkflow/ref/aegis/ir"
	"github.com/oarkflow/ref/aegis/transform"
	"github.com/oarkflow/ref/aegis/vm"
)

const pricingSource = `
package main

func calculatePrice(qty int32, unitPrice int32, discountPercent int32) int32 {
	subtotal := qty * unitPrice
	discount := (subtotal * discountPercent) / 100
	return subtotal - discount
}
`

const factorialSource = `
package main

func Factorial(n int32) int32 {
	var result int32 = 1
	for i := int32(1); i <= n; i++ {
		result = result * i
	}
	return result
}
`

const fibonacciSource = `
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

const multiFuncSource = `
package main

func square(x int32) int32 {
	return x * x
}

func SumOfSquares(a int32, b int32) int32 {
	return square(a) + square(b)
}
`

// compileModule is a helper that compiles Go source code to an IR module.
func compileModule(t *testing.T, src string, exportNames ...string) *ir.Module {
	t.Helper()
	comp := compiler.New(compiler.Config{
		SourceCode: src,
	})
	var res *compiler.CompileResult
	var err error
	if len(exportNames) > 0 {
		res, err = comp.CompileSource(src, exportNames)
	} else {
		res, err = comp.Compile()
	}
	if err != nil {
		t.Fatalf("compiler error: %v", err)
	}
	if err := ir.Validate(res.Module); err != nil {
		t.Fatalf("compiled module failed initial validation: %v", err)
	}
	return res.Module
}

// runCall invokes an exported function on a VM instance.
func runCall(t *testing.T, m *ir.Module, funcName string, args ...uint64) []uint64 {
	t.Helper()
	v, err := vm.New(m, vm.Config{})
	if err != nil {
		t.Fatalf("vm.New failed: %v", err)
	}
	results, err := v.Call(context.Background(), funcName, args...)
	if err != nil {
		t.Fatalf("vm.Call %q failed: %v", funcName, err)
	}
	return results
}

func TestConstantEncoding(t *testing.T) {
	m := compileModule(t, pricingSource, "calculatePrice")

	// Baseline run
	baseRes := runCall(t, m, "calculatePrice", 10, 100, 20)
	if len(baseRes) != 1 || baseRes[0] != 800 {
		t.Fatalf("expected baseline 800, got %v", baseRes)
	}

	pass := transform.NewConstantEncoding(42)
	transformed, metrics, err := pass.Transform(m)
	if err != nil {
		t.Fatalf("ConstantEncoding failed: %v", err)
	}

	if err := ir.Validate(transformed); err != nil {
		t.Fatalf("validation failed after ConstantEncoding: %v", err)
	}

	if metrics.InstructionsAdded == 0 || metrics.InstructionsRemoved == 0 {
		t.Errorf("expected instructions added/removed, got metrics: %+v", metrics)
	}

	// Verify transformed module execution
	txRes := runCall(t, transformed, "calculatePrice", 10, 100, 20)
	if len(txRes) != 1 || txRes[0] != baseRes[0] {
		t.Fatalf("result mismatch: baseline=%v, transformed=%v", baseRes, txRes)
	}

	// Test another input
	txRes2 := runCall(t, transformed, "calculatePrice", 5, 50, 0)
	if len(txRes2) != 1 || txRes2[0] != 250 {
		t.Fatalf("expected 250, got %v", txRes2)
	}
}

func TestOpaquePredicates(t *testing.T) {
	m := compileModule(t, pricingSource, "calculatePrice")

	baseRes := runCall(t, m, "calculatePrice", 10, 100, 20)

	pass := transform.NewOpaquePredicates(12345)
	transformed, metrics, err := pass.Transform(m)
	if err != nil {
		t.Fatalf("OpaquePredicates failed: %v", err)
	}

	if err := ir.Validate(transformed); err != nil {
		t.Fatalf("validation failed after OpaquePredicates: %v", err)
	}

	if metrics.InstructionsAdded == 0 {
		t.Errorf("expected instructions added, got: %+v", metrics)
	}

	txRes := runCall(t, transformed, "calculatePrice", 10, 100, 20)
	if len(txRes) != 1 || txRes[0] != baseRes[0] {
		t.Fatalf("result mismatch: baseline=%v, transformed=%v", baseRes, txRes)
	}
}

func TestControlFlowFlatten_Pricing(t *testing.T) {
	m := compileModule(t, pricingSource, "calculatePrice")

	cases := []struct {
		qty      uint64
		price    uint64
		discount uint64
		expected uint64
	}{
		{10, 100, 20, 800},
		{5, 50, 0, 250},
		{100, 10, 50, 500},
	}

	pass := transform.NewControlFlowFlatten(99)
	transformed, metrics, err := pass.Transform(m)
	if err != nil {
		t.Fatalf("ControlFlowFlatten failed: %v", err)
	}

	if err := ir.Validate(transformed); err != nil {
		t.Fatalf("validation failed after ControlFlowFlatten: %v", err)
	}

	if metrics.FunctionsModified == 0 {
		t.Errorf("expected functions modified, got: %+v", metrics)
	}

	for _, tc := range cases {
		res := runCall(t, transformed, "calculatePrice", tc.qty, tc.price, tc.discount)
		if len(res) != 1 || res[0] != tc.expected {
			t.Errorf("calculatePrice(%d, %d, %d) expected %d, got %v",
				tc.qty, tc.price, tc.discount, tc.expected, res)
		}
	}
}

func TestControlFlowFlatten_Factorial(t *testing.T) {
	m := compileModule(t, factorialSource)

	cases := []struct {
		n        uint64
		expected uint64
	}{
		{0, 1},
		{1, 1},
		{2, 2},
		{3, 6},
		{4, 24},
		{5, 120},
		{6, 720},
	}

	pass := transform.NewControlFlowFlatten(777)
	transformed, metrics, err := pass.Transform(m)
	if err != nil {
		t.Fatalf("ControlFlowFlatten failed: %v", err)
	}

	if err := ir.Validate(transformed); err != nil {
		t.Fatalf("validation failed after ControlFlowFlatten: %v", err)
	}

	if metrics.FunctionsModified == 0 {
		t.Errorf("expected functions modified, got: %+v", metrics)
	}

	for _, tc := range cases {
		res := runCall(t, transformed, "Factorial", tc.n)
		if len(res) != 1 || res[0] != tc.expected {
			t.Errorf("Factorial(%d) expected %d, got %v", tc.n, tc.expected, res)
		}
	}
}

func TestControlFlowFlatten_Fibonacci(t *testing.T) {
	m := compileModule(t, fibonacciSource)

	cases := []struct {
		n        uint64
		expected uint64
	}{
		{0, 0},
		{1, 1},
		{2, 1},
		{3, 2},
		{4, 3},
		{5, 5},
		{6, 8},
		{10, 55},
	}

	pass := transform.NewControlFlowFlatten(888)
	transformed, metrics, err := pass.Transform(m)
	if err != nil {
		t.Fatalf("ControlFlowFlatten failed: %v", err)
	}

	if err := ir.Validate(transformed); err != nil {
		t.Fatalf("validation failed after ControlFlowFlatten: %v", err)
	}

	if metrics.FunctionsModified == 0 {
		t.Errorf("expected functions modified, got: %+v", metrics)
	}

	for _, tc := range cases {
		res := runCall(t, transformed, "Fibonacci", tc.n)
		if len(res) != 1 || res[0] != tc.expected {
			t.Errorf("Fibonacci(%d) expected %d, got %v", tc.n, tc.expected, res)
		}
	}
}

func TestBuildDiversify(t *testing.T) {
	m := compileModule(t, multiFuncSource)

	baseRes := runCall(t, m, "SumOfSquares", 3, 4)
	if len(baseRes) != 1 || baseRes[0] != 25 {
		t.Fatalf("expected baseline 25, got %v", baseRes)
	}

	pass := transform.NewBuildDiversify(555)
	transformed, metrics, err := pass.Transform(m)
	if err != nil {
		t.Fatalf("BuildDiversify failed: %v", err)
	}

	if err := ir.Validate(transformed); err != nil {
		t.Fatalf("validation failed after BuildDiversify: %v", err)
	}

	var zeroID [32]byte
	if transformed.BuildID == zeroID {
		t.Errorf("expected non-zero BuildID")
	}

	if metrics.FunctionsModified == 0 {
		t.Errorf("expected functions modified")
	}

	txRes := runCall(t, transformed, "SumOfSquares", 3, 4)
	if len(txRes) != 1 || txRes[0] != 25 {
		t.Fatalf("SumOfSquares(3, 4) expected 25, got %v", txRes)
	}
}

func TestInstructionSchedule(t *testing.T) {
	m := compileModule(t, pricingSource, "calculatePrice")

	baseRes := runCall(t, m, "calculatePrice", 10, 100, 20)

	pass := transform.NewInstructionSchedule(101)
	transformed, metrics, err := pass.Transform(m)
	if err != nil {
		t.Fatalf("InstructionSchedule failed: %v", err)
	}

	if err := ir.Validate(transformed); err != nil {
		t.Fatalf("validation failed after InstructionSchedule: %v", err)
	}

	if metrics.InstructionsAdded == 0 {
		t.Errorf("expected instructions added, got: %+v", metrics)
	}

	txRes := runCall(t, transformed, "calculatePrice", 10, 100, 20)
	if len(txRes) != 1 || txRes[0] != baseRes[0] {
		t.Fatalf("result mismatch: baseline=%v, transformed=%v", baseRes, txRes)
	}
}

func TestStandardPipeline_Pricing(t *testing.T) {
	m := compileModule(t, pricingSource, "calculatePrice")

	pipeline := transform.StandardPipeline()
	transformed, metricsList, err := pipeline.Apply(m)
	if err != nil {
		t.Fatalf("StandardPipeline failed: %v", err)
	}

	if len(metricsList) != 4 {
		t.Errorf("expected 4 pass metrics, got %d", len(metricsList))
	}

	if err := ir.Validate(transformed); err != nil {
		t.Fatalf("validation failed after StandardPipeline: %v", err)
	}

	res := runCall(t, transformed, "calculatePrice", 10, 100, 20)
	if len(res) != 1 || res[0] != 800 {
		t.Fatalf("calculatePrice(10, 100, 20) expected 800, got %v", res)
	}
}

func TestStandardPipeline_Factorial(t *testing.T) {
	m := compileModule(t, factorialSource)

	pipeline := transform.StandardPipeline()
	transformed, _, err := pipeline.Apply(m)
	if err != nil {
		t.Fatalf("StandardPipeline failed: %v", err)
	}

	if err := ir.Validate(transformed); err != nil {
		t.Fatalf("validation failed after StandardPipeline: %v", err)
	}

	cases := []struct {
		n        uint64
		expected uint64
	}{
		{0, 1},
		{1, 1},
		{5, 120},
		{6, 720},
	}

	for _, tc := range cases {
		res := runCall(t, transformed, "Factorial", tc.n)
		if len(res) != 1 || res[0] != tc.expected {
			t.Errorf("Factorial(%d) expected %d, got %v", tc.n, tc.expected, res)
		}
	}
}

func TestStandardPipeline_Fibonacci(t *testing.T) {
	m := compileModule(t, fibonacciSource)

	pipeline := transform.StandardPipeline()
	transformed, _, err := pipeline.Apply(m)
	if err != nil {
		t.Fatalf("StandardPipeline failed: %v", err)
	}

	if err := ir.Validate(transformed); err != nil {
		t.Fatalf("validation failed after StandardPipeline: %v", err)
	}

	cases := []struct {
		n        uint64
		expected uint64
	}{
		{0, 0},
		{1, 1},
		{5, 5},
		{10, 55},
	}

	for _, tc := range cases {
		res := runCall(t, transformed, "Fibonacci", tc.n)
		if len(res) != 1 || res[0] != tc.expected {
			t.Errorf("Fibonacci(%d) expected %d, got %v", tc.n, tc.expected, res)
		}
	}
}

func TestMeasure(t *testing.T) {
	m := compileModule(t, pricingSource, "calculatePrice")

	before := transform.Measure(m)
	if before.TotalFunctions != 1 || before.TotalInstructions == 0 {
		t.Fatalf("unexpected before metrics: %+v", before)
	}

	pipeline := transform.StandardPipeline()
	transformed, _, err := pipeline.Apply(m)
	if err != nil {
		t.Fatalf("StandardPipeline failed: %v", err)
	}

	after := transform.Measure(transformed)
	if after.TotalInstructions <= before.TotalInstructions {
		t.Errorf("expected instruction count to increase: before=%d, after=%d",
			before.TotalInstructions, after.TotalInstructions)
	}

	if after.CyclomaticComplexity <= before.CyclomaticComplexity {
		t.Errorf("expected cyclomatic complexity to increase: before=%d, after=%d",
			before.CyclomaticComplexity, after.CyclomaticComplexity)
	}

	if after.InstructionEntropy <= 0 {
		t.Errorf("expected positive instruction entropy, got %f", after.InstructionEntropy)
	}

	summary := after.String()
	if summary == "" {
		t.Errorf("expected non-empty summary string")
	}
}
