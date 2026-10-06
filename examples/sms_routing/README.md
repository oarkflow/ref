# Multi-Provider Parameter-Based SMS Routing & Pipeline Platform

This directory contains a complete, production-grade implementation of REF's parameter-based SMS routing platform. It includes multi-gateway topologies (SMPP direct carrier binds, HTTP aggregators, cloud providers), automated health-aware failover, dynamic BCL decision table rules, large multi-country datasets, and a manual runner CLI/server.

---

## Table of Contents
1. [Quickstart: How to Run Manually](#quickstart-how-to-run-manually)
2. [CLI Manual Runner Modes](#cli-manual-runner-modes)
   - [Mode 1: Start HTTP REST API Server](#mode-1-start-http-rest-api-server)
   - [Mode 2: Send Routed SMS via CLI](#mode-2-send-routed-sms-via-cli)
   - [Mode 3: Dry-Run Route Simulation](#mode-3-dry-run-route-simulation)
   - [Mode 4: Batch Dataset Execution](#mode-4-batch-dataset-execution)
   - [Mode 5: Outage & Failover Drill](#mode-5-outage--failover-drill)
   - [Mode 6: Inspect Gateways & Users](#mode-6-inspect-gateways--users)
3. [HTTP REST API Reference & Curl Examples](#http-rest-api-reference--curl-examples)
4. [Datasets Structure (CSV & JSON)](#datasets-structure-csv--json)
5. [Multi-Parameter Routing Rules Matrix](#multi-parameter-routing-rules-matrix)
6. [Automated Verification Tests](#automated-verification-tests)

---

## Quickstart: How to Run Manually

From the repository root `/Users/sujit/Sites/ref`, you can run the application directly using `go run`:

```bash
# 1. Start the HTTP API server
go run ./examples/sms_routing/main.go server -addr :8080

# 2. Or dispatch an SMS directly from the terminal
go run ./examples/sms_routing/main.go send \
  -to "+9779841234567" \
  -country "NP" \
  -mno "Nepal Telecom (NTC)" \
  -type "otp" \
  -tier "vip"

# 3. Or simulate route selection without sending
go run ./examples/sms_routing/main.go simulate \
  -country "IN" \
  -mno "Airtel" \
  -type "otp" \
  -tier "vip"
```

---

## CLI Manual Runner Modes

The manual runner is located at [`examples/sms_routing/main.go`](file:///Users/sujit/Sites/ref/examples/sms_routing/main.go). It provides 6 execution modes:

### Mode 1: Start HTTP REST API Server

Starts a live HTTP service loaded with the 18 SMS gateways, the `rules.engine` policy program, and the datasets:

```bash
go run ./examples/sms_routing/main.go server -addr :8080
```

Output:
```text
🚀 REF SMS Routing Platform HTTP Server listening on :8080
Interactive Endpoints Available:
  POST /api/sms/send         - Route and dispatch an SMS
  POST /api/sms/simulate     - Dry-run inspect provider scoring and decision
  GET  /api/sms/providers    - Catalog of all 18 gateways and coverage
  GET  /api/sms/users        - Large user dataset (47 profiles)
  GET  /api/sms/phone-numbers- Multi-format phone number dataset (42 records)
  GET  /api/sms/stats        - Real-time router throughput and failover stats
  POST /api/sms/dlr          - Ingest delivery receipt feedback
```

---

### Mode 2: Send Routed SMS via CLI

Dispatches an SMS through the parameter router directly from your terminal. The router evaluates the parameters against the BCL decision table and routes to the highest-ranking gateway.

#### Available Flags:
| Flag | Default | Description |
|---|---|---|
| `-to` | `+9779841234567` | Recipient phone number in any format |
| `-from` | `REF_GATEWAY` | Sender ID / Alphanumeric header |
| `-text` | `Your OTP is...` | Message body text |
| `-country` | `NP` | ISO 2-letter country code (`NP`, `IN`, `US`, `GB`, `DE`, `FR`, `AE`, `SG`, `BR`, etc.) |
| `-mno` | `Nepal Telecom (NTC)` | Carrier / Mobile Network Operator name |
| `-tier` | `vip` | Customer account tier: `vip`, `enterprise`, `business`, `starter` |
| `-type` | `otp` | Traffic purpose: `otp`, `critical_alert`, `transactional`, `marketing` |
| `-encoding` | `gsm7` | Encoding requirements: `gsm7` or `ucs2` (Unicode) |
| `-max-cost` | `0.0` | Maximum cost ceiling per SMS in USD (e.g. `0.008`) |
| `-failed` | `""` | Mark a primary provider as failed to test automatic failover |

#### Example 1: Nepal VIP OTP (Routes to NTC Direct SMPP)
```bash
go run ./examples/sms_routing/main.go send \
  -to "+9779841234567" \
  -country "NP" \
  -mno "Nepal Telecom (NTC)" \
  -type "otp" \
  -tier "vip" \
  -text "Your verification code is 449210."
```

Output:
```text
📨 Submitting Message via Parameter Router...
   Destination : +9779841234567
   Parameters  : country=NP, mno="Nepal Telecom (NTC)", tier=vip, type=otp

✅ SMS Dispatched Successfully!
   Selected Gateway   : carrier_ntc_smpp
   Provider Msg ID    : sim_carrier_ntc_smpp_1791221137274837000
   Attempts / Retries : 1
   Reported Latency   : 35 ms
   Segments           : 1
   Status             : DELIVERED / ACCEPTED
```

#### Example 2: India Low-Cost Bulk Marketing (Routes to Route Mobile Aggregator)
```bash
go run ./examples/sms_routing/main.go send \
  -to "+919876543210" \
  -country "IN" \
  -type "marketing" \
  -tier "starter" \
  -text "Mega Diwali Sale: 50% discount on all orders!"
```

#### Example 3: US 10DLC VIP Verification (Routes to Twilio US Direct)
```bash
go run ./examples/sms_routing/main.go send \
  -to "+14155552671" \
  -country "US" \
  -type "otp" \
  -tier "vip" \
  -text "Stripe security code: 829104"
```

---

### Mode 3: Dry-Run Route Simulation

Inspect the exact ranking chain, tier assignment, and cost score calculated by the decision engine without actually submitting messages:

```bash
go run ./examples/sms_routing/main.go simulate \
  -country "NP" \
  -mno "Nepal Telecom (NTC)" \
  -type "otp" \
  -tier "vip"
```

Output:
```text
🔍 Simulation Results for Parameters: map[country:NP customer_tier:vip mno:Nepal Telecom (NTC) provider_failed: traffic_type:otp]

RANK  PROVIDER ID         TIER    PRIORITY  COST/SMS  REASON
----  -----------         ----    --------  --------  ------
#1    carrier_ntc_smpp    Tier 1  10        $0.0120   🎯 Selected Primary Route
#2    jio_direct_smpp     Tier 1  10        $0.0048   1st Failover Backup
#3    airtel_direct_smpp  Tier 1  10        $0.0050   2nd Failover Backup
#4    twilio_us_direct    Tier 1  10        $0.0079   Catalog rank
#5    carrier_ncell_smpp  Tier 1  10        $0.0140   Catalog rank
```

---

### Mode 4: Batch Dataset Execution

Run batch dispatches across all 47 user accounts or all 42 phone numbers in the dataset to verify routing coverage:

```bash
# Execute batch over all 47 customer profiles
go run ./examples/sms_routing/main.go batch -dataset users
```

Output:
```text
📦 Running batch dispatch across 47 users in dataset...

USER ID  NAME                  COUNTRY  TYPE            TIER        SELECTED GATEWAY     STATUS
-------  ----                  -------  ----            ----        ----------------     ------
usr_001  Everest Bank Ltd      NP       otp             vip         carrier_ntc_smpp     OK
usr_002  Ncell Tech Portal     NP       critical_alert  vip         carrier_ntc_smpp     OK
usr_005  HDFC Digital Bank     IN       otp             vip         airtel_direct_smpp   OK
usr_006  Swiggy India          IN       transactional   enterprise  airtel_direct_smpp   OK
usr_010  Stripe Payments US    US       otp             vip         twilio_us_direct     OK
usr_011  Uber Technologies     US       transactional   enterprise  aws_sns_us           OK
usr_014  Silicon Valley Promo  US       marketing       business    bandwidth_us         OK
usr_018  Revolut UK            GB       otp             vip         bt_direct_smpp       OK
usr_022  N26 Bank Berlin       DE       otp             vip         messagebird_eu       OK
usr_027  Rakuten Group Tokyo   JP       otp             vip         singtel_apac_smpp    OK
usr_032  Careem Dubai          AE       transactional   enterprise  etisalat_mena_smpp   OK
usr_035  Grab Southeast Asia   SG       otp             vip         singtel_apac_smpp    OK
usr_038  Nubank Brasil         BR       otp             vip         zenvia_latam_http    OK
usr_041  Standard Bank SA      ZA       otp             vip         africastalking_http  OK
...

Batch Finished: 47 / 47 messages routed successfully.
```

---

### Mode 5: Outage & Failover Drill

Simulates a gateway outage (e.g. primary carrier link goes down) and verifies that the router automatically and transparently shifts traffic to the backup provider with zero caller errors:

```bash
go run ./examples/sms_routing/main.go failover -country "NP" -failed "carrier_ntc_smpp"
```

Output:
```text
⚡ Executing Outage & Automatic Failover Drill...
   Country Target  : NP
   Outage Provider : carrier_ntc_smpp

1. NORMAL OPERATION : Routed to "carrier_ntc_smpp"
2. UNDER OUTAGE     : Automatically failed over to "carrier_ncell_smpp" (zero caller error!)
```

---

### Mode 6: Inspect Gateways & Users

Display the live catalog of all 18 configured SMS gateways or the user profiles:

```bash
# View provider catalog with protocol, tier, cost, and coverage
go run ./examples/sms_routing/main.go providers

# View customer profiles with traffic types and cost caps
go run ./examples/sms_routing/main.go users
```

---

## HTTP REST API Reference & Curl Examples

When running in server mode (`go run ./examples/sms_routing/main.go server -addr :8080`), you can interact with the router via REST:

### 1. Send SMS (`POST /api/sms/send`)

#### Request:
```bash
curl -X POST http://localhost:8080/api/sms/send \
  -H "Content-Type: application/json" \
  -d '{
    "to": "+9779841234567",
    "from": "EVEREST_BANK",
    "text": "Your account 0123 has been credited with NPR 50,000. OTP: 981245",
    "country": "NP",
    "mno": "Nepal Telecom (NTC)",
    "customer_tier": "vip",
    "traffic_type": "otp"
  }'
```

#### Response:
```json
{
  "ok": true,
  "provider_message_id": "sim_carrier_ntc_smpp_1791221191730305000",
  "provider_id": "carrier_ntc_smpp",
  "segments": 1,
  "latency_ms": 35,
  "attempts": 1
}
```

---

### 2. Simulate Route Selection (`POST /api/sms/simulate`)

#### Request:
```bash
curl -X POST http://localhost:8080/api/sms/simulate \
  -H "Content-Type: application/json" \
  -d '{
    "country": "US",
    "traffic_type": "marketing",
    "customer_tier": "starter"
  }'
```

#### Response:
```json
{
  "selected_provider": "bandwidth_us",
  "candidate_chain": [
    { "rank": 1, "provider_id": "bandwidth_us", "tier": 2, "priority": 7, "cost_per_sms": 0.0055 },
    { "rank": 2, "provider_id": "jio_direct_smpp", "tier": 1, "priority": 10, "cost_per_sms": 0.0048 },
    { "rank": 3, "provider_id": "airtel_direct_smpp", "tier": 1, "priority": 10, "cost_per_sms": 0.0050 },
    { "rank": 4, "provider_id": "twilio_us_direct", "tier": 1, "priority": 10, "cost_per_sms": 0.0079 }
  ],
  "hints_evaluated": {
    "country": "US",
    "customer_tier": "starter",
    "traffic_type": "marketing"
  }
}
```

---

### 3. Gateway Catalog (`GET /api/sms/providers`)

```bash
curl http://localhost:8080/api/sms/providers
```

---

### 4. Router Metrics & Failover Stats (`GET /api/sms/stats`)

```bash
curl http://localhost:8080/api/sms/stats
```

#### Response:
```json
{
  "router": "gateway",
  "failovers_total": 0,
  "providers": {
    "carrier_ntc_smpp": {
      "total": 5,
      "success": 5,
      "failed": 0,
      "avg_latency_ms": 35,
      "priority": 10,
      "tier": 1,
      "enabled": true
    },
    "twilio_us_direct": {
      "total": 3,
      "success": 3,
      "failed": 0,
      "avg_latency_ms": 65,
      "priority": 10,
      "tier": 1,
      "enabled": true
    }
  }
}
```

---

### 5. Ingest Delivery Receipt Feedback (`POST /api/sms/dlr`)

Updates the rolling health tracker for a provider:

```bash
curl -X POST http://localhost:8080/api/sms/dlr \
  -H "Content-Type: application/json" \
  -d '{
    "provider_id": "carrier_ntc_smpp",
    "status": "DELIVRD",
    "latency_ms": 28
  }'
```

---

## Datasets Structure (CSV & JSON)

Located in [`examples/sms_routing/data/`](file:///Users/sujit/Sites/ref/examples/sms_routing/data/):

| Dataset | Records | Description |
|---|---|---|
| [`phone_numbers.json`](file:///Users/sujit/Sites/ref/examples/sms_routing/data/phone_numbers.json)<br>[`phone_numbers.csv`](file:///Users/sujit/Sites/ref/examples/sms_routing/data/phone_numbers.csv) | 42 | Multi-format phone numbers across 16 countries (`NP`, `IN`, `US`, `CA`, `GB`, `DE`, `FR`, `JP`, `AU`, `AE`, `SG`, `BR`, `ZA`, `NG`, `KE`, `KR`). Format detection is handled dynamically based on `country_code` (no static `format_type`). |
| [`providers.json`](file:///Users/sujit/Sites/ref/examples/sms_routing/data/providers.json)<br>[`providers.csv`](file:///Users/sujit/Sites/ref/examples/sms_routing/data/providers.csv) | 18 | 18 gateways covering SMPP carrier binds (`carrier_ntc_smpp`, `airtel_direct_smpp`, `bt_direct_smpp`, etc.) and HTTP aggregators (`twilio_us_direct`, `bandwidth_us`, `infobip_global_http`). |
| [`users.json`](file:///Users/sujit/Sites/ref/examples/sms_routing/data/users.json)<br>[`users.csv`](file:///Users/sujit/Sites/ref/examples/sms_routing/data/users.csv) | 47 | 47 enterprise and business accounts with customer tier, traffic type, encoding, max cost ceiling, and default sender IDs. |

---

## Multi-Parameter Routing Rules Matrix

Defined in [`examples/sms_routing/bcl/sms_routing_rules.bcl`](file:///Users/sujit/Sites/ref/examples/sms_routing/bcl/sms_routing_rules.bcl):

```mermaid
flowchart TD
    Req[Incoming Message + Parameter Hints] --> Filter{Budget & Health Check}
    Filter -->|Provider Exceeds Max Cost| Drop[Prune Provider]
    Filter -->|Success Rate < 80%| Degraded[Deprioritize / Skip]
    Filter -->|Healthy & Within Budget| Policy[BCL Decision Table Policy]

    Policy --> Failover{Primary Provider Failed?}
    Failover -->|Yes| FRoute[Priority 950-1000: Dynamic Backup Route]
    Failover -->|No| CountryRoute{Evaluate Country & MNO}

    CountryRoute -->|NP + NTC + OTP/VIP| NTC[carrier_ntc_smpp]
    CountryRoute -->|NP + Ncell + OTP/VIP| NCELL[carrier_ncell_smpp]
    CountryRoute -->|NP + Marketing| SPARROW[sparrow_sms_http]

    CountryRoute -->|IN + Airtel + OTP/VIP| AIRTEL[airtel_direct_smpp]
    CountryRoute -->|IN + Jio + OTP/VIP| JIO[jio_direct_smpp]
    CountryRoute -->|IN + Marketing| ROUTE_MOBILE[route_mobile_http]

    CountryRoute -->|US/CA + OTP/VIP| TWILIO[twilio_us_direct]
    CountryRoute -->|US + Marketing| BANDWIDTH[bandwidth_us]
    CountryRoute -->|US/CA + Transactional| AWS_SNS[aws_sns_us]

    CountryRoute -->|GB (UK) + VIP| BT[bt_direct_smpp]
    CountryRoute -->|DE / FR / EU| MSGBIRD[messagebird_eu]
    CountryRoute -->|AE (MENA)| ETISALAT[etisalat_mena_smpp]
    CountryRoute -->|BR (LATAM)| ZENVIA[zenvia_latam_http]
    CountryRoute -->|NG / KE / ZA (Africa)| AFRICASTALKING[africastalking_http]
    CountryRoute -->|SG / JP / AU (APAC)| SINGTEL[singtel_apac_smpp]
    CountryRoute -->|Other International| INFOBIP[infobip_global_http]
```

---

## Automated Verification Tests

To verify dataset integrity, all parameter combinations, and action pipeline execution:

```bash
# Run the complete matrix verification test suite
go test -v ./platform -run "TestDatasetIntegrity|TestSMSRoutingMatrix"

# Run all platform tests
go test ./platform
```
