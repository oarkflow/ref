package execution

import (
	"context"
	"testing"
)

// A child context derived during an execution outlives the pooled
// execCancelCtx's reset; its propagation goroutine must be able to call Value
// and Deadline on the cleared parent without panicking.
func TestExecCancelCtxSafeAfterReset(t *testing.T) {
	var c execCancelCtx
	c.reset(context.WithValue(context.Background(), struct{}{}, "v"))
	if c.Value(struct{}{}) != "v" {
		t.Fatal("value not forwarded")
	}
	c.reset(nil)
	if c.Value(struct{}{}) != nil {
		t.Fatal("cleared context must report no values")
	}
	if _, ok := c.Deadline(); ok {
		t.Fatal("cleared context must report no deadline")
	}
	if c.Err() == nil {
		t.Fatal("cleared context must report canceled")
	}
}

// Once Done was handed out, a derived context may watch it after the
// execution ends, so the execution must not be recycled into the pool.
func TestExecCancelCtxObserved(t *testing.T) {
	var c execCancelCtx
	c.reset(context.Background())
	if c.observed() {
		t.Fatal("fresh context reported observed")
	}
	child, cancel := context.WithCancel(&c)
	defer cancel()
	if !c.observed() {
		t.Fatal("deriving a child did not mark the context observed")
	}
	c.cancelExecution()
	<-child.Done()
	if child.Err() == nil {
		t.Fatal("child not cancelled")
	}
}
