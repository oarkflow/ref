package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/oarkflow/ref/saga"
)

// TestRunCompensatesInReverse runs the checkout saga and checks that the
// failing third step rolls back the card charge and then the inventory
// deduction, and that the failed step itself is never compensated.
func TestRunCompensatesInReverse(t *testing.T) {
	var out bytes.Buffer
	err := run(context.Background(), &out)
	if !errors.Is(err, saga.ErrSagaFailed) {
		t.Fatalf("run error = %v, want saga.ErrSagaFailed", err)
	}
	if !strings.Contains(err.Error(), "Provision_Cloud_Server") {
		t.Fatalf("error does not name the failing step: %v", err)
	}
	var journal []string
	for _, line := range strings.Split(out.String(), "\n") {
		if strings.HasPrefix(line, "inventory: ") || strings.HasPrefix(line, "card: ") || strings.HasPrefix(line, "server: ") {
			journal = append(journal, line)
		}
	}
	want := []string{"inventory: deducted", "card: charged", "card: refunded", "inventory: restored"}
	if strings.Join(journal, "|") != strings.Join(want, "|") {
		t.Fatalf("journal = %q, want %q\n%s", journal, want, out.String())
	}
}
