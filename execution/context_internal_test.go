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
