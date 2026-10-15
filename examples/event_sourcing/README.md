# Event Sourcing & CQRS Pattern

This example demonstrates how to build an **Event Sourcing** and **Command Query Responsibility Segregation (CQRS)** architecture in Go using REF's `eventbus` subsystem.

## Concepts Demonstrated

1. **Write Model (Commands):**
   - Business operations emit immutable domain events (`BalanceDeposited`, `BalanceWithdrawn`).
   - Events capture intent and state changes without mutating a relational view directly.
2. **Event Bus (`ref/eventbus`):**
   - Publishes domain events to subscribed handlers concurrently.
3. **Read Model (Projections / CQRS):**
   - Asynchronous projectors listen to events and update a dedicated `ReadModel` optimized for fast, query-side reads.
   - Decouples high-throughput command intake from low-latency query presentation.

## Running the Example

### Quick Start (Single Command)

```bash
# Run the demo
make

# Run the tests
make test
```

### Manual Run

```bash
go run ./examples/event_sourcing
```

### Expected Output

```text
=== EVENT SOURCING & CQRS DEMO ===

Executing Business Commands...
[EVENT BUS] Published Event: BalanceDeposited (Agg: user-99)
[EVENT BUS] Published Event: BalanceWithdrawn (Agg: user-99)
[PROJECTOR] Read Model Updated: User user-99 balance is now $100.00
[PROJECTOR] Read Model Updated: User user-99 balance is now $64.50

=== UI QUERY ===
User 99 Current Balance (from Read Model): $64.50
```
