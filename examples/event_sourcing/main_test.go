package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// TestRunProjectsEvents runs the demo and checks that both events reach the
// read model through the asynchronous projectors: 100.00 - 35.50 = 64.50.
func TestRunProjectsEvents(t *testing.T) {
	var out bytes.Buffer
	model := run(context.Background(), &out)
	if got := model.Balance("user-99"); got != 64.50 {
		t.Fatalf("projected balance = %.2f, want 64.50\n%s", got, out.String())
	}
	if got := strings.Count(out.String(), "[PROJECTOR] Read Model Updated"); got != 2 {
		t.Fatalf("projector updates = %d, want 2\n%s", got, out.String())
	}
	if !strings.Contains(out.String(), "User 99 Current Balance (from Read Model): $64.50") {
		t.Fatalf("UI query did not report the projected balance:\n%s", out.String())
	}
}
