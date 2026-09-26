package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/oarkflow/ref/saga"
)

func main() {
	// The saga failing and compensating is the demonstrated outcome, and
	// run has already reported it.
	_ = run(context.Background(), os.Stdout)
}

// run executes the checkout saga, writing the demo report to out. The
// third step always fails, so run returns the saga error after the first
// two steps have been compensated in reverse order.
func run(ctx context.Context, out io.Writer) error {
	fmt.Fprintln(out, "=== DISTRIBUTED SAGA TRANSACTION DEMO ===")
	orchestrator := saga.NewOrchestrator()

	// 1. First Step: Deduct Inventory
	orchestrator.AddStep(
		"Deduct_Inventory",
		func(ctx context.Context) error {
			time.Sleep(100 * time.Millisecond) // Simulate DB call
			// Logic: UPDATE inventory SET count = count - 1
			fmt.Fprintln(out, "inventory: deducted")
			return nil
		},
		func(ctx context.Context) error {
			time.Sleep(50 * time.Millisecond) // Simulate DB rollback
			// Rollback Logic: UPDATE inventory SET count = count + 1
			fmt.Fprintln(out, "inventory: restored")
			return nil
		},
	)

	// 2. Second Step: Charge Credit Card (External API)
	orchestrator.AddStep(
		"Charge_Credit_Card",
		func(ctx context.Context) error {
			time.Sleep(200 * time.Millisecond) // Simulate Stripe API call
			// Logic: Stripe charge successful
			fmt.Fprintln(out, "card: charged")
			return nil
		},
		func(ctx context.Context) error {
			time.Sleep(150 * time.Millisecond) // Simulate Stripe API refund
			// Rollback Logic: Stripe refund
			fmt.Fprintln(out, "card: refunded")
			return nil
		},
	)

	// 3. Third Step: Provision Cloud Server (Fails!)
	orchestrator.AddStep(
		"Provision_Cloud_Server",
		func(ctx context.Context) error {
			time.Sleep(300 * time.Millisecond) // Simulate AWS API call

			// DISASTER STRIKES! AWS is down.
			// This will trigger the Saga to automatically rollback Step 2 and Step 1.
			return errors.New("AWS API timeout (504 Gateway Time-out)")
		},
		func(ctx context.Context) error {
			// This won't run because the action failed.
			fmt.Fprintln(out, "server: deprovisioned")
			return nil
		},
	)

	// Execute the Saga Workflow
	fmt.Fprintln(out, "Starting Order Checkout Workflow...")
	err := orchestrator.Execute(ctx)

	fmt.Fprintln(out, "\n=== FINAL RESULT ===")
	if err != nil {
		fmt.Fprintf(out, "Workflow Failed Gracefully: %v\n", err)
		fmt.Fprintln(out, "Status: Eventual Consistency Maintained (No orphaned charges or missing inventory).")
	} else {
		fmt.Fprintln(out, "Workflow Succeeded.")
	}
	return err
}
