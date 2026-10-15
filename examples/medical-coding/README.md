# medical-coding

This example handles medical coding and billing for patient encounters. An encounter falls on a **single date of service** (an office visit) or spans **multiple dates** (an inpatient stay or a course of therapy), and is coded into claim lines, one per date.

- **Rules:**
  - timely filing (365 days)
  - no future dates
  - a period can span at most 60 days
  - every line must fall inside the encounter period
- **Rule failures:** all violations come back together in `error.details`.
- **Duplicate billing:** the same patient and provider cannot have two encounters covering the same date (409 `DOS_OVERLAP`, naming the existing encounter).
- **Lines:** a line without its own date covers every day of the stay, one line per date. A line with a `dos` lands on that date only.
- **Atomic save:** the encounter and its lines are saved in one transaction.
- **Tenants:** each billing client is a tenant (the `tenant_id` claim), and all reads and writes are isolated per tenant.

To run it:

### Quick Start (Single-Command Execution)

```sh
# Run directly with Aegis Platform Runtime
make

# Run securely inside an encrypted container via pure in-memory VFS (zero disk residue)
make secure
```

### Manual Run

```sh
export CODING_JWT_SECRET=$(openssl rand -hex 24)
go run ./examples/medical-coding &
CODER=$(go run ./examples/medical-coding -token '{"sub":"coder-1","tenant_id":"acme-billing"}')

curl -H "Authorization: Bearer $CODER" -H 'Content-Type: application/json' localhost:8096/api/encounters -d '{
  "patient_id": "P2", "provider_id": "DR1", "dos_from": "2026-09-01", "dos_to": "2026-09-04",
  "lines": [ { "cpt": "99232", "icd": "I50.9", "units": 1 },
             { "cpt": "93306", "icd": "I50.9", "dos": "2026-09-02" } ] }'
```

| Route | Purpose |
|---|---|
| `POST /api/encounters/preview` | Validate and expand without saving (reports, never fails) |
| `POST /api/encounters` | Code and save an encounter with its per-day lines |
| `GET /api/encounters` | The tenant's encounters |
| `GET /api/encounters/:id/lines` | Stored claim lines, by date |

### Aegis Zero-Disk In-Memory VFS Execution

When running with `make secure`:
1. Aegis packages the application directory into an AES-256-GCM encrypted, Ed25519-signed `.apkg` container.
2. At runtime, the container is validated against the developer's public key, decrypted strictly into memory, and loaded into an isolated in-memory Virtual Filesystem (`vfs.MemFS`).
3. No decrypted files touch the host disk or `/tmp` at any point during startup or request execution.
4. Upon process termination, memory buffers are cryptographically wiped (`0x00`).

The tests are in `platform/dos_e2e_test.go`, and the building blocks are described in [`docs/app-patterns.md`](../../docs/app-patterns.md).

