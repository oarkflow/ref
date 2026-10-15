# Distributed Saga Transaction Pattern

This example demonstrates how to orchestrate multi-step distributed transactions using REF's `ref/saga` orchestrator with automatic forward-execution and reverse compensation (rollback).

## Problem Context

In microservice architectures and distributed workflows, ACID transactions across multiple independent datastores or external APIs (e.g. Database + Stripe + AWS Cloud API) cannot be coordinated with 2-Phase Commit (2PC) without introducing severe availability and latency bottlenecks.

The **Saga Pattern** resolves this by executing a sequence of transactions:
1. Each step performs an isolated local transaction.
2. If any step fails (e.g. downstream network timeout or business validation error), the orchestrator triggers **compensating transactions** for all previously succeeded steps in reverse order.
3. Eventual consistency is maintained without locks across service boundaries.

## Running the Example

### Quick Start (Single Command)

```bash
# Run the saga transaction demo
make

# Run the unit tests
make test
```

### Manual Run

```bash
go run ./examples/saga_transaction
```

### Expected Output

```text
=== DISTRIBUTED SAGA TRANSACTION DEMO ===
Starting Order Checkout Workflow...
inventory: deducted
[SAGA] Step 'Deduct_Inventory' completed successfully.
card: charged
[SAGA] Step 'Charge_Credit_Card' completed successfully.
[SAGA] Step 'Provision_Cloud_Server' failed: AWS API timeout (504 Gateway Time-out). Initiating compensation (rollback)...
card: refunded
[SAGA] Successfully compensated step 'Charge_Credit_Card'
inventory: restored
[SAGA] Successfully compensated step 'Deduct_Inventory'

=== FINAL RESULT ===
Workflow Failed Gracefully: saga transaction failed and rolled back: failed at step 'Provision_Cloud_Server' (AWS API timeout (504 Gateway Time-out))
Status: Eventual Consistency Maintained (No orphaned charges or missing inventory).
```
