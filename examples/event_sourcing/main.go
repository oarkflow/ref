package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/oarkflow/ref/eventbus"
)

// ReadModel represents the UI-optimized projection of our data (CQRS)
type ReadModel struct {
	// mu guards UserBalances: the bus runs every projector in its own
	// goroutine, so projections of different events apply concurrently.
	mu           sync.Mutex
	UserBalances map[string]float64
}

// apply adds delta to the user's balance and returns the new balance.
func (m *ReadModel) apply(userID string, delta float64) float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.UserBalances[userID] += delta
	return m.UserBalances[userID]
}

// Balance returns the projected balance for userID.
func (m *ReadModel) Balance(userID string) float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.UserBalances[userID]
}

func main() {
	run(context.Background(), os.Stdout)
}

// run publishes the demo's events, waits for the asynchronous projectors to
// apply them, prints the UI query and returns the projected read model.
func run(ctx context.Context, out io.Writer) *ReadModel {
	fmt.Fprintln(out, "=== EVENT SOURCING & CQRS DEMO ===")

	bus := eventbus.NewBus()

	// The Read Model Database (optimized for fast UI queries)
	readModel := &ReadModel{
		UserBalances: make(map[string]float64),
	}
	// projected is released once per applied event so the read path waits
	// for the asynchronous projectors instead of guessing with a sleep.
	var projected sync.WaitGroup
	var outMu sync.Mutex

	// 1. Subscribe a Projector to update the Read Model asynchronously
	bus.Subscribe("BalanceDeposited", func(ctx context.Context, e eventbus.Event) error {
		defer projected.Done()
		userID := e.Aggregate
		amount := e.Payload["amount"].(float64)

		time.Sleep(50 * time.Millisecond) // Simulate DB write
		balance := readModel.apply(userID, amount)
		outMu.Lock()
		fmt.Fprintf(out, "[PROJECTOR] Read Model Updated: User %s balance is now $%.2f\n", userID, balance)
		outMu.Unlock()
		return nil
	})

	bus.Subscribe("BalanceWithdrawn", func(ctx context.Context, e eventbus.Event) error {
		defer projected.Done()
		userID := e.Aggregate
		amount := e.Payload["amount"].(float64)

		time.Sleep(50 * time.Millisecond) // Simulate DB write
		balance := readModel.apply(userID, -amount)
		outMu.Lock()
		fmt.Fprintf(out, "[PROJECTOR] Read Model Updated: User %s balance is now $%.2f\n", userID, balance)
		outMu.Unlock()
		return nil
	})

	// 2. The Business Command (The "Write" Path)
	// Instead of updating a row directly, we generate Immutable Events.
	fmt.Fprintln(out, "\nExecuting Business Commands...")
	events := []eventbus.Event{
		{
			ID:        "evt-1",
			Aggregate: "user-99",
			Type:      "BalanceDeposited",
			Payload:   map[string]any{"amount": 100.00},
			Timestamp: time.Now(),
		},
		{
			ID:        "evt-2",
			Aggregate: "user-99",
			Type:      "BalanceWithdrawn",
			Payload:   map[string]any{"amount": 35.50},
			Timestamp: time.Now(),
		},
	}

	// Publish the events to the event store/bus. Each event has exactly one
	// projector subscribed.
	projected.Add(len(events))
	bus.Publish(ctx, events...)

	// Wait for async projectors to finish
	projected.Wait()

	// 3. UI Query (The "Read" Path)
	fmt.Fprintln(out, "\n=== UI QUERY ===")
	fmt.Fprintf(out, "User 99 Current Balance (from Read Model): $%.2f\n", readModel.Balance("user-99"))
	return readModel
}
