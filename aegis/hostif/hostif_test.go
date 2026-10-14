package hostif_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	"github.com/oarkflow/ref/aegis/hostif"
	"github.com/oarkflow/ref/aegis/ir"
	"github.com/oarkflow/ref/aegis/vm"
)

// mockLogger records log invocations for verification.
type mockLogger struct {
	entries []string
}

func (m *mockLogger) Log(level hostif.LogLevel, message string) {
	m.entries = append(m.entries, level.String()+": "+message)
}

func TestRegistryInitialization(t *testing.T) {
	reg := hostif.NewRegistry([]string{hostif.CapIO, hostif.CapTime})

	if !reg.HasCapability(hostif.CapIO) {
		t.Errorf("expected CapIO to be granted")
	}
	if !reg.HasCapability(hostif.CapTime) {
		t.Errorf("expected CapTime to be granted")
	}
	if reg.HasCapability(hostif.CapCrypto) {
		t.Errorf("CapCrypto should not be granted initially")
	}

	caps := reg.Capabilities()
	if len(caps) != 2 || caps[0] != hostif.CapIO || caps[1] != hostif.CapTime {
		t.Errorf("unexpected capabilities list: %v", caps)
	}

	// Dynamic grant and revoke
	reg.Grant(hostif.CapCrypto)
	if !reg.HasCapability(hostif.CapCrypto) {
		t.Errorf("expected CapCrypto to be granted after Grant")
	}

	reg.Revoke(hostif.CapTime)
	if reg.HasCapability(hostif.CapTime) {
		t.Errorf("CapTime should not be granted after Revoke")
	}

	// Validation on Register
	dummyFunc := vm.NewHostFunc("test", "dummy", func(ctx context.Context, v *vm.VM, args []uint64) ([]uint64, error) {
		return nil, nil
	})

	// Invalid function fields
	if err := reg.Register(vm.HostFunc{Name: "dummy", Func: dummyFunc.Func}, hostif.Policy{}); !errors.Is(err, hostif.ErrInvalidHostFunc) {
		t.Errorf("expected ErrInvalidHostFunc for empty module, got: %v", err)
	}
	if err := reg.Register(vm.HostFunc{Module: "test", Func: dummyFunc.Func}, hostif.Policy{}); !errors.Is(err, hostif.ErrInvalidHostFunc) {
		t.Errorf("expected ErrInvalidHostFunc for empty name, got: %v", err)
	}
	if err := reg.Register(vm.HostFunc{Module: "test", Name: "dummy"}, hostif.Policy{}); !errors.Is(err, hostif.ErrInvalidHostFunc) {
		t.Errorf("expected ErrInvalidHostFunc for nil func, got: %v", err)
	}

	// Invalid policy fields
	if err := reg.Register(dummyFunc, hostif.Policy{MaxCallsPerExecution: -1}); !errors.Is(err, hostif.ErrInvalidPolicy) {
		t.Errorf("expected ErrInvalidPolicy for negative max calls, got: %v", err)
	}
	if err := reg.Register(dummyFunc, hostif.Policy{MaxDataBytes: -5}); !errors.Is(err, hostif.ErrInvalidPolicy) {
		t.Errorf("expected ErrInvalidPolicy for negative max data bytes, got: %v", err)
	}

	// Successful registration
	if err := reg.Register(dummyFunc, hostif.Policy{Capability: hostif.CapIO}); err != nil {
		t.Fatalf("unexpected register error: %v", err)
	}

	// Duplicate registration error
	if err := reg.Register(dummyFunc, hostif.Policy{Capability: hostif.CapIO}); !errors.Is(err, hostif.ErrAlreadyRegistered) {
		t.Errorf("expected ErrAlreadyRegistered on duplicate register, got: %v", err)
	}

	// Get registered function
	retrieved, policy, ok := reg.Get("test", "dummy")
	if !ok || retrieved.Name != "dummy" || policy.Capability != hostif.CapIO {
		t.Errorf("failed to retrieve registered function correctly")
	}
}

func TestRegisterDefaults(t *testing.T) {
	reg := hostif.NewRegistry(hostif.StandardCapabilities)
	if err := hostif.RegisterDefaults(reg); err != nil {
		t.Fatalf("RegisterDefaults failed: %v", err)
	}

	expectedFuncs := []string{
		"host.read_input",
		"host.write_output",
		"host.hash_sha256",
		"host.random_bytes",
		"host.time_now_unix",
		"host.time_now_nanos",
		"host.log",
	}

	funcs := reg.HostFuncs()
	if len(funcs) != len(expectedFuncs) {
		t.Fatalf("expected %d host functions, got %d", len(expectedFuncs), len(funcs))
	}

	for _, expectedKey := range expectedFuncs {
		var mod, name string
		for i := 0; i < len(expectedKey); i++ {
			if expectedKey[i] == '.' {
				mod = expectedKey[:i]
				name = expectedKey[i+1:]
				break
			}
		}
		if _, _, ok := reg.Get(mod, name); !ok {
			t.Errorf("expected function %s to be registered", expectedKey)
		}
	}
}

func TestCapabilityFiltering(t *testing.T) {
	// Registry only permits IO
	reg := hostif.NewRegistry([]string{hostif.CapIO})

	ioCalled := false
	ioFunc := vm.NewHostFunc("test", "io_op", func(ctx context.Context, v *vm.VM, args []uint64) ([]uint64, error) {
		ioCalled = true
		return []uint64{1}, nil
	})

	cryptoCalled := false
	cryptoFunc := vm.NewHostFunc("test", "crypto_op", func(ctx context.Context, v *vm.VM, args []uint64) ([]uint64, error) {
		cryptoCalled = true
		return []uint64{2}, nil
	})

	if err := reg.Register(ioFunc, hostif.Policy{Capability: hostif.CapIO}); err != nil {
		t.Fatalf("failed to register ioFunc: %v", err)
	}
	if err := reg.Register(cryptoFunc, hostif.Policy{Capability: hostif.CapCrypto}); err != nil {
		t.Fatalf("failed to register cryptoFunc: %v", err)
	}

	// FilteredHostFuncs should only return the IO function
	filtered := reg.FilteredHostFuncs()
	if len(filtered) != 1 || filtered[0].Name != "io_op" {
		t.Errorf("expected FilteredHostFuncs to return only io_op, got: %v", filtered)
	}

	// HostFuncs returns both, with cryptoFunc wrapped to enforce capability
	allFuncs := reg.HostFuncs()
	if len(allFuncs) != 2 {
		t.Fatalf("expected 2 host funcs, got %d", len(allFuncs))
	}

	wrappedIO, _, _ := reg.Get("test", "io_op")
	wrappedCrypto, _, _ := reg.Get("test", "crypto_op")

	ctx := context.Background()

	// Calling IO operation should succeed
	res, err := wrappedIO.Func(ctx, nil, nil)
	if err != nil || len(res) != 1 || res[0] != 1 || !ioCalled {
		t.Fatalf("expected IO call to succeed, got res: %v, err: %v", res, err)
	}

	// Calling Crypto operation should fail due to lack of CapCrypto
	_, err = wrappedCrypto.Func(ctx, nil, nil)
	if !errors.Is(err, hostif.ErrCapabilityDenied) {
		t.Fatalf("expected ErrCapabilityDenied for crypto operation, got: %v", err)
	}
	if cryptoCalled {
		t.Errorf("crypto_op implementation should not have been called when capability is denied")
	}

	// Dynamically grant CapCrypto
	reg.Grant(hostif.CapCrypto)
	res, err = wrappedCrypto.Func(ctx, nil, nil)
	if err != nil || len(res) != 1 || res[0] != 2 || !cryptoCalled {
		t.Fatalf("expected Crypto call to succeed after Grant, got res: %v, err: %v", res, err)
	}

	// Revoke CapCrypto and verify it fails again
	reg.Revoke(hostif.CapCrypto)
	cryptoCalled = false
	_, err = wrappedCrypto.Func(ctx, nil, nil)
	if !errors.Is(err, hostif.ErrCapabilityDenied) {
		t.Fatalf("expected ErrCapabilityDenied after Revoke, got: %v", err)
	}

	// Context capability override
	ctxWithCrypto := hostif.WithCapabilities(ctx, hostif.CapCrypto)
	res, err = wrappedCrypto.Func(ctxWithCrypto, nil, nil)
	if err != nil || len(res) != 1 || res[0] != 2 {
		t.Fatalf("expected Crypto call with context capability override to succeed, got: %v", err)
	}
}

func TestRequiresGrant(t *testing.T) {
	reg := hostif.NewRegistry([]string{hostif.CapCrypto})

	called := false
	sensitiveFunc := vm.NewHostFunc("test", "sign_secret", func(ctx context.Context, v *vm.VM, args []uint64) ([]uint64, error) {
		called = true
		return []uint64{42}, nil
	})

	if err := reg.Register(sensitiveFunc, hostif.Policy{
		Capability:    hostif.CapCrypto,
		RequiresGrant: true,
	}); err != nil {
		t.Fatalf("failed to register sensitiveFunc: %v", err)
	}

	wrapped, _, _ := reg.Get("test", "sign_secret")
	ctx := context.Background()

	// Calling without grant must fail even though CapCrypto is granted
	_, err := wrapped.Func(ctx, nil, nil)
	if !errors.Is(err, hostif.ErrGrantRequired) {
		t.Fatalf("expected ErrGrantRequired, got: %v", err)
	}
	if called {
		t.Errorf("function should not have been called without grant")
	}

	// Explicit grant on registry
	reg.GrantExplicit("sign_secret")
	res, err := wrapped.Func(ctx, nil, nil)
	if err != nil || len(res) != 1 || res[0] != 42 || !called {
		t.Fatalf("expected call to succeed with explicit grant, got res: %v, err: %v", res, err)
	}

	// Revoke grant on registry
	reg.RevokeGrant("sign_secret")
	called = false
	_, err = wrapped.Func(ctx, nil, nil)
	if !errors.Is(err, hostif.ErrGrantRequired) {
		t.Fatalf("expected ErrGrantRequired after revoke grant, got: %v", err)
	}

	// Context-based grant
	ctxWithGrant := hostif.WithGrant(ctx, "test.sign_secret")
	res, err = wrapped.Func(ctxWithGrant, nil, nil)
	if err != nil || len(res) != 1 || res[0] != 42 {
		t.Fatalf("expected call with context grant to succeed, got: %v", err)
	}
}

func TestInvocationLimits(t *testing.T) {
	reg := hostif.NewRegistry([]string{hostif.CapIO})

	counter := 0
	limitedFunc := vm.NewHostFunc("test", "limited", func(ctx context.Context, v *vm.VM, args []uint64) ([]uint64, error) {
		counter++
		return []uint64{uint64(counter)}, nil
	})

	if err := reg.Register(limitedFunc, hostif.Policy{
		Capability:           hostif.CapIO,
		MaxCallsPerExecution: 3,
	}); err != nil {
		t.Fatalf("failed to register: %v", err)
	}

	wrapped, _, _ := reg.Get("test", "limited")
	ctx := context.Background()

	// Invocations 1, 2, 3 should succeed
	for i := 1; i <= 3; i++ {
		res, err := wrapped.Func(ctx, nil, nil)
		if err != nil || len(res) != 1 || res[0] != uint64(i) {
			t.Fatalf("call %d failed: res=%v, err=%v", i, res, err)
		}
	}

	// 4th invocation must exceed limit
	_, err := wrapped.Func(ctx, nil, nil)
	if !errors.Is(err, hostif.ErrLimitExceeded) {
		t.Fatalf("expected ErrLimitExceeded on 4th call, got: %v", err)
	}

	// Reset limits
	reg.ResetLimits()
	res, err := wrapped.Func(ctx, nil, nil)
	if err != nil || len(res) != 1 {
		t.Fatalf("call after ResetLimits failed: %v", err)
	}

	// Per-carrier context limit isolation
	carrierA := hostif.NewCarrier()
	carrierB := hostif.NewCarrier()
	ctxA := hostif.WithCarrier(ctx, carrierA)
	ctxB := hostif.WithCarrier(ctx, carrierB)

	for i := 0; i < 3; i++ {
		if _, err := wrapped.Func(ctxA, nil, nil); err != nil {
			t.Fatalf("ctxA call %d failed: %v", i+1, err)
		}
		if _, err := wrapped.Func(ctxB, nil, nil); err != nil {
			t.Fatalf("ctxB call %d failed: %v", i+1, err)
		}
	}

	// 4th call on ctxA should fail, but ctxB reset allows more
	if _, err := wrapped.Func(ctxA, nil, nil); !errors.Is(err, hostif.ErrLimitExceeded) {
		t.Errorf("expected ErrLimitExceeded for ctxA, got: %v", err)
	}

	carrierB.ResetCalls()
	if _, err := wrapped.Func(ctxB, nil, nil); err != nil {
		t.Errorf("expected ctxB to succeed after ResetCalls, got: %v", err)
	}
}

func TestDataByteLimits(t *testing.T) {
	reg := hostif.NewRegistry([]string{hostif.CapCrypto})

	hashFn := hostif.HashSHA256HostFunc()
	if err := reg.Register(hashFn, hostif.Policy{
		Capability:   hostif.CapCrypto,
		MaxDataBytes: 32,
	}); err != nil {
		t.Fatalf("failed to register hashFn: %v", err)
	}

	wrapped, _, _ := reg.Get("host", "hash_sha256")
	ctx := context.Background()

	// Calling with 32 bytes (within limit)
	err := hostif.CheckDataLimit(hostif.Policy{MaxDataBytes: 32}, 32)
	if err != nil {
		t.Errorf("expected 32 bytes to be allowed: %v", err)
	}

	// Calling with 33 bytes (exceeds limit)
	err = hostif.CheckDataLimit(hostif.Policy{MaxDataBytes: 32}, 33)
	if !errors.Is(err, hostif.ErrDataLimitExceeded) {
		t.Errorf("expected ErrDataLimitExceeded for 33 bytes, got: %v", err)
	}

	// Direct call through wrapper with 33 bytes length argument
	_, err = wrapped.Func(ctx, nil, []uint64{0, 33, 100})
	if !errors.Is(err, hostif.ErrDataLimitExceeded) {
		t.Fatalf("expected wrapper to enforce MaxDataBytes and return ErrDataLimitExceeded, got: %v", err)
	}
}

// testVM creates an initialized VM instance with 1 memory page for direct testing of memory reads/writes.
func testVM(t *testing.T) *vm.VM {
	t.Helper()
	mod := &ir.Module{
		Version: ir.Version,
		Memory:  ir.MemorySpec{InitialPages: 1, MaxPages: 2},
	}
	v, err := vm.New(mod, vm.DefaultConfig())
	if err != nil {
		t.Fatalf("failed to create test vm: %v", err)
	}
	return v
}

func TestMemoryReadWrite_IO(t *testing.T) {
	v := testVM(t)
	carrier := hostif.NewCarrier()
	ctx := hostif.WithCarrier(context.Background(), carrier)

	readFn := hostif.ReadInputHostFunc(carrier)
	writeFn := hostif.WriteOutputHostFunc(carrier)

	// 1. Default Input / Output
	inputPayload := []byte("hello aegis memory")
	carrier.SetInput(inputPayload)

	// Probe input length (max_len = 0)
	probeRes, err := readFn.Func(ctx, v, []uint64{0, 0})
	if err != nil || len(probeRes) != 1 || probeRes[0] != uint64(len(inputPayload)) {
		t.Fatalf("probe failed: res=%v, err=%v", probeRes, err)
	}

	// Read input into VM memory at offset 100
	readRes, err := readFn.Func(ctx, v, []uint64{100, uint64(len(inputPayload))})
	if err != nil || len(readRes) != 1 || readRes[0] != uint64(len(inputPayload)) {
		t.Fatalf("read_input failed: res=%v, err=%v", readRes, err)
	}

	// Verify VM linear memory contents
	memBytes, err := v.ReadMemory(100, uint32(len(inputPayload)))
	if err != nil || !bytes.Equal(memBytes, inputPayload) {
		t.Fatalf("memory content mismatch: got %q, want %q", memBytes, inputPayload)
	}

	// Write output from VM linear memory at offset 100 to carrier output
	writeRes, err := writeFn.Func(ctx, v, []uint64{100, uint64(len(inputPayload))})
	if err != nil || len(writeRes) != 1 || writeRes[0] != uint64(len(inputPayload)) {
		t.Fatalf("write_output failed: res=%v, err=%v", writeRes, err)
	}

	if !bytes.Equal(carrier.Output(), inputPayload) {
		t.Fatalf("carrier output mismatch: got %q, want %q", carrier.Output(), inputPayload)
	}

	// 2. Indexed Input / Output
	carrier.SetIndexedInput(3, []byte("indexed_data_payload"))
	indexedRes, err := readFn.Func(ctx, v, []uint64{3, 200, 100})
	if err != nil || len(indexedRes) != 1 || indexedRes[0] != uint64(len("indexed_data_payload")) {
		t.Fatalf("indexed read_input failed: %v", err)
	}

	// Write indexed output
	_, err = writeFn.Func(ctx, v, []uint64{5, 200, uint64(len("indexed_data_payload"))})
	if err != nil {
		t.Fatalf("indexed write_output failed: %v", err)
	}
	indexedOut, ok := carrier.GetIndexedOutput(5)
	if !ok || !bytes.Equal(indexedOut, []byte("indexed_data_payload")) {
		t.Fatalf("indexed output mismatch: got %q, want 'indexed_data_payload'", indexedOut)
	}

	// 3. Named Input / Output
	namedKey := "auth_token"
	namedVal := []byte("secret_token_12345")
	carrier.SetNamedInput(namedKey, namedVal)

	// Write key name to VM memory at offset 500
	if err := v.WriteMemory(500, []byte(namedKey)); err != nil {
		t.Fatalf("failed to write key to memory: %v", err)
	}

	// read_input(name_ptr=500, name_len=10, dest_ptr=600, max_len=100)
	namedReadRes, err := readFn.Func(ctx, v, []uint64{500, uint64(len(namedKey)), 600, 100})
	if err != nil || len(namedReadRes) != 1 || namedReadRes[0] != uint64(len(namedVal)) {
		t.Fatalf("named read_input failed: res=%v, err=%v", namedReadRes, err)
	}

	// write_output(name_ptr=500, name_len=10, src_ptr=600, len=len(namedVal))
	outKey := "result_token"
	if err := v.WriteMemory(700, []byte(outKey)); err != nil {
		t.Fatalf("failed to write outKey to memory: %v", err)
	}
	_, err = writeFn.Func(ctx, v, []uint64{700, uint64(len(outKey)), 600, uint64(len(namedVal))})
	if err != nil {
		t.Fatalf("named write_output failed: %v", err)
	}

	namedOut, ok := carrier.GetNamedOutput(outKey)
	if !ok || !bytes.Equal(namedOut, namedVal) {
		t.Fatalf("named output mismatch: got %q, want %q", namedOut, namedVal)
	}
}

func TestCryptoFunctions(t *testing.T) {
	v := testVM(t)
	carrier := hostif.NewCarrier()
	ctx := hostif.WithCarrier(context.Background(), carrier)

	hashFn := hostif.HashSHA256HostFunc()
	randFn := hostif.RandomBytesHostFunc()

	// 1. SHA-256 computation
	inputStr := "The quick brown fox jumps over the lazy dog"
	expectedDigest := sha256.Sum256([]byte(inputStr))

	if err := v.WriteMemory(100, []byte(inputStr)); err != nil {
		t.Fatalf("failed to write input to memory: %v", err)
	}

	// hash_sha256(src_ptr=100, src_len=len, dest_ptr=200)
	hashRes, err := hashFn.Func(ctx, v, []uint64{100, uint64(len(inputStr)), 200})
	if err != nil || len(hashRes) != 1 || hashRes[0] != 32 {
		t.Fatalf("hash_sha256 failed: res=%v, err=%v", hashRes, err)
	}

	digestBytes, err := v.ReadMemory(200, 32)
	if err != nil || !bytes.Equal(digestBytes, expectedDigest[:]) {
		t.Fatalf("SHA-256 digest mismatch:\ngot:  %x\nwant: %x", digestBytes, expectedDigest)
	}

	// 2. Cryptographically secure random bytes
	randRes, err := randFn.Func(ctx, v, []uint64{300, 32})
	if err != nil || len(randRes) != 1 || randRes[0] != 32 {
		t.Fatalf("random_bytes failed: res=%v, err=%v", randRes, err)
	}

	randBytes1, err := v.ReadMemory(300, 32)
	if err != nil {
		t.Fatalf("failed to read random bytes 1: %v", err)
	}

	// Ensure bytes are not all zeros
	allZero := true
	for _, b := range randBytes1 {
		if b != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		t.Errorf("random_bytes returned all zeros")
	}

	// Generate second chunk at offset 400 and ensure distinct
	_, err = randFn.Func(ctx, v, []uint64{400, 32})
	if err != nil {
		t.Fatalf("random_bytes 2 failed: %v", err)
	}
	randBytes2, err := v.ReadMemory(400, 32)
	if err != nil {
		t.Fatalf("failed to read random bytes 2: %v", err)
	}

	if bytes.Equal(randBytes1, randBytes2) {
		t.Errorf("two consecutive random_bytes calls produced identical bytes")
	}
}

func TestTimeFunctions(t *testing.T) {
	carrier := hostif.NewCarrier()
	ctx := hostif.WithCarrier(context.Background(), carrier)

	unixFn := hostif.TimeNowUnixHostFunc(carrier)
	nanosFn := hostif.TimeNowNanosHostFunc(carrier)

	// Real clock test
	beforeUnix := time.Now().Unix()
	resUnix, err := unixFn.Func(ctx, nil, nil)
	afterUnix := time.Now().Unix()

	if err != nil || len(resUnix) != 1 {
		t.Fatalf("time_now_unix failed: %v", err)
	}
	gotUnix := int64(resUnix[0])
	if gotUnix < beforeUnix || gotUnix > afterUnix {
		t.Errorf("got unix %d, expected between %d and %d", gotUnix, beforeUnix, afterUnix)
	}

	resNanos, err := nanosFn.Func(ctx, nil, nil)
	if err != nil || len(resNanos) != 1 || resNanos[0] == 0 {
		t.Fatalf("time_now_nanos failed: %v", err)
	}

	// Mock clock test
	mockTime := time.Date(2026, 10, 14, 12, 0, 0, 123456789, time.UTC)
	carrier.SetNowFunc(func() time.Time {
		return mockTime
	})

	resMockUnix, err := unixFn.Func(ctx, nil, nil)
	if err != nil || resMockUnix[0] != uint64(mockTime.Unix()) {
		t.Errorf("expected mock unix %d, got %v", mockTime.Unix(), resMockUnix)
	}

	resMockNanos, err := nanosFn.Func(ctx, nil, nil)
	if err != nil || resMockNanos[0] != uint64(mockTime.UnixNano()) {
		t.Errorf("expected mock nanos %d, got %v", mockTime.UnixNano(), resMockNanos)
	}
}

func TestLogFunction(t *testing.T) {
	v := testVM(t)
	carrier := hostif.NewCarrier()
	mockLog := &mockLogger{}
	carrier.SetLogger(mockLog)
	ctx := hostif.WithCarrier(context.Background(), carrier)

	logFn := hostif.LogHostFunc(carrier)

	msg := "Aegis runtime execution started"
	if err := v.WriteMemory(100, []byte(msg)); err != nil {
		t.Fatalf("failed to write message to memory: %v", err)
	}

	// 2 args: default INFO level
	res, err := logFn.Func(ctx, v, []uint64{100, uint64(len(msg))})
	if err != nil || len(res) != 1 || res[0] != uint64(len(msg)) {
		t.Fatalf("log failed: res=%v, err=%v", res, err)
	}

	// 3 args: explicit WARN level
	warnMsg := "Memory boundary threshold approached"
	if err := v.WriteMemory(200, []byte(warnMsg)); err != nil {
		t.Fatalf("failed to write warnMsg: %v", err)
	}
	res, err = logFn.Func(ctx, v, []uint64{uint64(hostif.LogLevelWarn), 200, uint64(len(warnMsg))})
	if err != nil || len(res) != 1 || res[0] != uint64(len(warnMsg)) {
		t.Fatalf("warn log failed: %v", err)
	}

	// Verify carrier trace buffer
	logs := carrier.Logs()
	if len(logs) != 2 {
		t.Fatalf("expected 2 logs in carrier, got: %v", logs)
	}
	if logs[0] != "[INFO] Aegis runtime execution started" {
		t.Errorf("unexpected log 0: %s", logs[0])
	}
	if logs[1] != "[WARN] Memory boundary threshold approached" {
		t.Errorf("unexpected log 1: %s", logs[1])
	}

	// Verify external logger callback
	if len(mockLog.entries) != 2 {
		t.Fatalf("expected 2 mock logger entries, got: %v", mockLog.entries)
	}
	if mockLog.entries[0] != "INFO: Aegis runtime execution started" {
		t.Errorf("unexpected mock entry 0: %s", mockLog.entries[0])
	}
	if mockLog.entries[1] != "WARN: Memory boundary threshold approached" {
		t.Errorf("unexpected mock entry 1: %s", mockLog.entries[1])
	}
}

func TestIntegration_VM_FullWorkflow(t *testing.T) {
	// Build an IR module that imports host functions and executes an end-to-end workflow:
	// 1. Reads input from host into VM memory at offset 100.
	// 2. Logs the input string.
	// 3. Computes the SHA-256 hash of the input and stores it at offset 200.
	// 4. Writes the SHA-256 hash from offset 200 to host output.
	// 5. Returns 0.

	mod := &ir.Module{
		Version: ir.Version,
		Memory:  ir.MemorySpec{InitialPages: 1, MaxPages: 2},
		Types: []ir.FuncType{
			// Type 0: (i32, i32) -> (i32) [read_input, write_output, log]
			{Params: []ir.ValType{ir.I32, ir.I32}, Results: []ir.ValType{ir.I32}},
			// Type 1: (i32, i32, i32) -> (i32) [hash_sha256]
			{Params: []ir.ValType{ir.I32, ir.I32, ir.I32}, Results: []ir.ValType{ir.I32}},
			// Type 2: () -> (i32) [main function]
			{Params: []ir.ValType{}, Results: []ir.ValType{ir.I32}},
		},
		Imports: []ir.Import{
			// Import 0: host.read_input
			{Module: "host", Name: "read_input", Type: 0},
			// Import 1: host.write_output
			{Module: "host", Name: "write_output", Type: 0},
			// Import 2: host.hash_sha256
			{Module: "host", Name: "hash_sha256", Type: 1},
			// Import 3: host.log
			{Module: "host", Name: "log", Type: 0},
		},
		Functions: []ir.Function{
			{
				Name:      "process_and_hash",
				TypeIndex: 2,
				Locals:    []ir.ValType{ir.I32, ir.I32}, // local 0: bytes_read, local 1: tmp
				Body: []ir.Instruction{
					// Step 1: read_input(dest_ptr=100, max_len=1024)
					{Op: ir.OpI32Const, Operands: [3]uint64{100, 0, 0}},
					{Op: ir.OpI32Const, Operands: [3]uint64{1024, 0, 0}},
					{Op: ir.OpCallImport, Operands: [3]uint64{0, 0, 0}}, // import 0: read_input
					{Op: ir.OpLocalSet, Operands: [3]uint64{0, 0, 0}},   // local 0 = bytes_read

					// Step 2: log(msg_ptr=100, msg_len=local 0)
					{Op: ir.OpI32Const, Operands: [3]uint64{100, 0, 0}},
					{Op: ir.OpLocalGet, Operands: [3]uint64{0, 0, 0}},
					{Op: ir.OpCallImport, Operands: [3]uint64{3, 0, 0}}, // import 3: log
					{Op: ir.OpLocalSet, Operands: [3]uint64{1, 0, 0}},   // consume result

					// Step 3: hash_sha256(src_ptr=100, src_len=local 0, dest_ptr=200)
					{Op: ir.OpI32Const, Operands: [3]uint64{100, 0, 0}},
					{Op: ir.OpLocalGet, Operands: [3]uint64{0, 0, 0}},
					{Op: ir.OpI32Const, Operands: [3]uint64{200, 0, 0}},
					{Op: ir.OpCallImport, Operands: [3]uint64{2, 0, 0}}, // import 2: hash_sha256
					{Op: ir.OpLocalSet, Operands: [3]uint64{1, 0, 0}},   // consume result

					// Step 4: write_output(src_ptr=200, len=32)
					{Op: ir.OpI32Const, Operands: [3]uint64{200, 0, 0}},
					{Op: ir.OpI32Const, Operands: [3]uint64{32, 0, 0}},
					{Op: ir.OpCallImport, Operands: [3]uint64{1, 0, 0}}, // import 1: write_output
					{Op: ir.OpLocalSet, Operands: [3]uint64{1, 0, 0}},   // consume result

					// Step 5: Return 0
					{Op: ir.OpI32Const, Operands: [3]uint64{0, 0, 0}},
					{Op: ir.OpReturn, Operands: [3]uint64{0, 0, 0}},
				},
			},
		},
		Exports: []ir.Export{
			{Name: "process_and_hash", FuncIndex: 0},
		},
	}

	reg := hostif.NewRegistry(hostif.StandardCapabilities)
	if err := hostif.RegisterDefaults(reg); err != nil {
		t.Fatalf("RegisterDefaults failed: %v", err)
	}

	vmInstance, err := vm.New(mod, vm.DefaultConfig(), reg.HostFuncs()...)
	if err != nil {
		t.Fatalf("vm.New failed: %v", err)
	}

	carrier := hostif.NewCarrier()
	payload := []byte("Aegis Secure Runtime Protected Execution")
	carrier.SetInput(payload)

	ctx := hostif.WithCarrier(context.Background(), carrier)
	results, err := vmInstance.Call(ctx, "process_and_hash")
	if err != nil {
		t.Fatalf("vm.Call failed: %v", err)
	}

	if len(results) != 1 || results[0] != 0 {
		t.Errorf("expected return value 0, got: %v", results)
	}

	// Verify output is SHA-256 of payload
	expectedDigest := sha256.Sum256(payload)
	actualDigest := carrier.Output()
	if !bytes.Equal(actualDigest, expectedDigest[:]) {
		t.Fatalf("output digest mismatch:\ngot:  %x\nwant: %x", actualDigest, expectedDigest)
	}

	// Verify log entry was recorded
	logs := carrier.Logs()
	if len(logs) != 1 || logs[0] != "[INFO] Aegis Secure Runtime Protected Execution" {
		t.Errorf("unexpected logs: %v", logs)
	}
}

func TestIntegration_VM_CapabilityAndInvocationLimits(t *testing.T) {
	// Test that when a capability is not granted or revoked, OpCallImport returns a trap
	mod := &ir.Module{
		Version: ir.Version,
		Memory:  ir.MemorySpec{InitialPages: 1, MaxPages: 2},
		Types: []ir.FuncType{
			{Params: []ir.ValType{ir.I32, ir.I32, ir.I32}, Results: []ir.ValType{ir.I32}},
			{Params: []ir.ValType{}, Results: []ir.ValType{ir.I32}},
		},
		Imports: []ir.Import{
			{Module: "host", Name: "hash_sha256", Type: 0},
		},
		Functions: []ir.Function{
			{
				Name:      "call_crypto",
				TypeIndex: 1,
				Body: []ir.Instruction{
					{Op: ir.OpI32Const, Operands: [3]uint64{0, 0, 0}},
					{Op: ir.OpI32Const, Operands: [3]uint64{10, 0, 0}},
					{Op: ir.OpI32Const, Operands: [3]uint64{100, 0, 0}},
					{Op: ir.OpCallImport, Operands: [3]uint64{0, 0, 0}},
					{Op: ir.OpReturn, Operands: [3]uint64{0, 0, 0}},
				},
			},
		},
		Exports: []ir.Export{
			{Name: "call_crypto", FuncIndex: 0},
		},
	}

	// Initialize registry WITHOUT CapCrypto (only CapIO)
	reg := hostif.NewRegistry([]string{hostif.CapIO})
	// Register crypto with max calls 1
	if err := reg.Register(hostif.HashSHA256HostFunc(), hostif.Policy{
		Capability:           hostif.CapCrypto,
		MaxCallsPerExecution: 1,
	}); err != nil {
		t.Fatalf("failed to register crypto: %v", err)
	}

	vmInstance, err := vm.New(mod, vm.DefaultConfig(), reg.HostFuncs()...)
	if err != nil {
		t.Fatalf("failed to create VM: %v", err)
	}

	ctx := context.Background()

	// 1. Should trap because CapCrypto is denied
	_, err = vmInstance.Call(ctx, "call_crypto")
	if err == nil {
		t.Fatalf("expected error due to missing CapCrypto, got nil")
	}
	var trap *vm.Trap
	if !errors.As(err, &trap) {
		t.Fatalf("expected vm.Trap, got: %T (%v)", err, err)
	}
	if trap.Kind != vm.TrapHostError {
		t.Errorf("expected TrapHostError, got: %v", trap.Kind)
	}
	if !errors.Is(trap.Cause, hostif.ErrCapabilityDenied) {
		t.Errorf("expected trap.Cause to wrap ErrCapabilityDenied, got: %v", trap.Cause)
	}

	// 2. Grant CapCrypto -> first call succeeds, second call traps with ErrLimitExceeded
	reg.Grant(hostif.CapCrypto)
	vmInstance.Reset()

	res, err := vmInstance.Call(ctx, "call_crypto")
	if err != nil {
		t.Fatalf("expected call_crypto to succeed after grant, got: %v", err)
	}
	if len(res) != 1 || res[0] != 32 {
		t.Errorf("expected 32 bytes written, got: %v", res)
	}

	// Second call in same execution context exceeds MaxCallsPerExecution (limit is 1)
	vmInstance.Reset()
	_, err = vmInstance.Call(ctx, "call_crypto")
	if err == nil {
		t.Fatalf("expected call_crypto to fail due to call limit, got nil")
	}
	if !errors.As(err, &trap) || !errors.Is(trap.Cause, hostif.ErrLimitExceeded) {
		t.Errorf("expected trap with ErrLimitExceeded, got: %v", err)
	}
}
