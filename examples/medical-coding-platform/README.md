# CLEAR Medical Coding & Billing Platform

> **Production-Ready Enterprise Healthcare Coding, Auditing, and Billing API**  
> Built with **REF (Runtime Execution Fabric)** and **BCL (Business Configuration Language)**.  
> Fully compliant and feature-complete with the Clear20 platform architecture.

---

## 1. Executive Summary & Architectural Overview

The **CLEAR Medical Coding Platform** is an enterprise-grade backend service designed for high-throughput clinical chart coding, quality assurance (QA) auditing, data entry (DE), and healthcare revenue cycle management (RCM). 

Rather than relying on procedural handler trees or brittle middleware chains, CLEAR is compiled into a **Dependency-Readiness Directed Acyclic Graph (DAG)** via **REF (Runtime Execution Fabric)**. All clinical business logic, master data lookup, validation rules, authentication policies, and state transitions are declared modularly using **BCL (Business Configuration Language)**.

### Key Capabilities
- **100% Clear20 Parity**: 96 comprehensive HTTP endpoints matching legacy `/web/client/...` and standard REST routes.
- **Sub-Millisecond DAG Execution**: Automatic parallelization of non-dependent tasks (e.g., authentication, permissions, tenant verification, and database lookups).
- **Dual-Credential Security Engine**: Native **Argon2id** password hashing with automatic transparent fallback to **Bcrypt** for legacy credential compatibility.
- **Enterprise RBAC & Multi-Tenancy**: Hierarchical role-based access control (SuperAdmin, Admin, Supervisor, Auditor, Coder, Data Entry) with fine-grained capability checks.
- **Clinical State Machine**: Strict lifecycle state transitions (`open` $\to$ `in_progress` $\to$ `suspended` $\leftrightarrow$ `released` $\to$ `coding_complete` $\to$ `qa_review` $\to$ `billed`).
- **Zero-Code Domain Modularity**: Complete domain logic articulated across 8 clean, isolated BCL directory packages.

---

## 2. Ref Core & Contrib Innovations

To support CLEAR and make these enterprise patterns accessible to any application built on Ref, generic components have been promoted directly into Ref Core (`platform`, `config`) and Contrib (`contrib/migrate`):

### A. Recursive BCL Document Compilation (`github.com/oarkflow/ref/platform`)
- **`platform.LoadDirRecursive(ctx, dir, opts)`**:
  Replaces flat directory scanning with a deterministic recursive file walker (`filepath.WalkDir`). Files are compiled in lexical order (`00_core` $\to$ `01_master` $\dots$ $\to$ `08_routes`), enabling true domain separation and nested BCL configurations without boilerplate.
- **`serve.Serve`**: Now natively leverages `platform.LoadDirRecursive` by default.

### B. Enterprise Config & Secrets Management (`github.com/oarkflow/ref/config`)
- **`config.LoadDotenv(path)`**: Standard zero-dependency `.env` loader with shell-style expansion, multi-line value support, and comment stripping.
- **`config.DotenvPath()`**: Automatically traverses directory ancestors to find `.env` regardless of working directory.
- **`config.LoadSecretFile(envKey, secretFilePath)`**: Ingests secrets mounted as files (Kubernetes Secrets, Docker Swarm Secrets, HashiCorp Vault files) and injects them into environment memory.
- **`config.ResolveDir(candidates...)`**: Iterates through candidate paths and resolves the first existing directory.
- **`config.Env(key, fallback)`**: Streamlined typed environment variable lookup.

### C. Reusable Migration & Seeding Engine (`github.com/oarkflow/ref/contrib/migrate`)
- **`contrib/migrate`**: A decoupled, production-ready database migration package wrapping `github.com/oarkflow/migrate`.
- **Automatic TTY Detection**: Intelligently determines interactive terminals (`os.ModeCharDevice`). Prompts operators (`[y/N]`) in interactive mode, or auto-applies when `AUTO_MIGRATE=true` or non-interactive flags are set.
- **SQLite Directory Provisioning**: Detects SQLite connection strings (`file:...` or `./path/app.db`) and automatically provisions the required parent directory structure with atomic permissions (`0755`).
- **CLI Utilities**: Exposes clean program interfaces: `NewManager`, `Pending`, `Apply`, `Seed`, and `EnsureMigrated`.

### D. Dual-Credential Authentication (`github.com/oarkflow/ref/platform`)
- **Dual Hash Verification**:
  Clinical platforms migrating from legacy backends often contain passwords hashed with Bcrypt (`$2a$`, `$2b$`, `$2y$`). Ref's authentication provider now verifies passwords using modern **Argon2id** by default, and automatically falls back to **Bcrypt** if Argon2id format does not match. This enables zero-downtime, transparent credential migration.

---

## 3. Directory & Domain Architecture

The CLEAR platform configuration is strictly segregated into logical subdirectories under `resources/config/`:

```text
examples/medical-coding-platform/
├── cmd/
│   ├── server/                     # Primary Ref server bootstrap & daemon
│   │   ├── main.go                 # Configuration loading, telemetry, listen
│   │   ├── migrate.go              # Delegates to contrib/migrate.EnsureMigrated
│   │   └── errors.go               # Standardized JSON error response handler
│   └── migrator/                   # Standalone CLI for schema and seed management
│       └── main.go                 # Commands: up, migrate, seed, db:seed
├── resources/
│   ├── config/                     # Modular BCL Business Logic
│   │   ├── 00_core/                # Database pool, sessions, auth policies
│   │   │   ├── 01_datasource.bcl   # SQLite / PostgreSQL connection pools
│   │   │   ├── 02_sessions.bcl     # Fast memory/cookie session manager
│   │   │   └── 03_security.bcl     # CORS, tenant isolation, rate limits
│   │   ├── 01_master/              # Master Catalogs & Metadata
│   │   │   ├── 01_shapes.bcl       # Facilities, workitems, chargemaster shapes
│   │   │   ├── 02_facilities.bcl   # Facility catalogs and queries
│   │   │   ├── 03_workitems.bcl    # Workitem types, settings, descriptions
│   │   │   ├── 04_providers.bcl    # Healthcare provider profiles and lookups
│   │   │   ├── 05_chargemaster.bcl # Charge code master rates and metadata
│   │   │   └── 06_cpt.bcl          # CPT-4 and ICD-10 medical terminology
│   │   ├── 02_encounters/          # Patient Demographics & Work Queues
│   │   │   ├── 01_shapes.bcl       # Encounters, patient demographics shapes
│   │   │   ├── 02_queues.bcl       # Open, in-progress, completed work queues
│   │   │   ├── 03_patient_header.bcl # Clinical patient banners and encounters
│   │   │   ├── 04_documentation.bcl  # Transcription and clinical note viewing
│   │   │   └── 05_encounter_ops.bcl  # Status counters, counts, metrics
│   │   ├── 03_coding/              # Clinical Coding Workflows
│   │   │   ├── 01_shapes.bcl       # Diagnosis (ICD-10) and Procedure (CPT) shapes
│   │   │   ├── 02_claim_chart.bcl  # Start-coding claim intent & concurrency lock
│   │   │   ├── 03_save_coding.bcl  # Coder documentation saving & validation
│   │   │   ├── 04_verify_coding.bcl # Rule verification & compliance checks
│   │   │   └── 05_pqrs.bcl         # PQRS quality measures and code mapping
│   │   ├── 04_qa/                  # Quality Assurance & Auditing
│   │   │   ├── 01_shapes.bcl       # Audit review shapes and error codes
│   │   │   ├── 02_qa_queues.bcl    # QA pending, in-progress, completed queues
│   │   │   ├── 03_qa_claim.bcl     # QA chart claim & supervisor assignment
│   │   │   └── 04_qa_save.bcl      # QA pass/fail grading, scoring, approval
│   │   ├── 05_data_entry/          # Charge Data Entry (DE) Operations
│   │   │   ├── 01_shapes.bcl       # Data entry charge transaction shapes
│   │   │   ├── 02_de_queues.bcl    # DE open and in-progress work queues
│   │   │   └── 03_de_save.bcl      # Charge posting and verification
│   │   ├── 06_suspends/            # Chart Suspension & Release Engine
│   │   │   ├── 01_shapes.bcl       # Suspend reason shapes and history logs
│   │   │   ├── 02_suspend_intents.bcl # Suspend chart with reason and audit trail
│   │   │   └── 03_release_intents.bcl # Supervisor release back to coder queue
│   │   ├── 07_admin/               # Administration, RBAC & Multitenancy
│   │   │   ├── 01_shapes.bcl       # Users, roles, permissions, companies shapes
│   │   │   ├── 02_auth_intents.bcl # Login, me, profile, password reset intents
│   │   │   ├── 03_admin_intents.bcl# User provisioning, role assign/revoke
│   │   │   └── 04_companies.bcl    # Multitenant company and client management
│   │   └── 08_routes/              # Complete HTTP Route Endpoints (96 Routes)
│   │       ├── 01_routes_auth.bcl  # Authentication and user endpoints
│   │       ├── 02_routes_facilities.bcl # Facility and client routes
│   │       ├── 03_routes_coding.bcl# Chart queues, claim, save, verify, PQRS
│   │       ├── 04_routes_qa.bcl    # QA audit queues, grading, and approval
│   │       ├── 05_routes_de.bcl    # Data entry queues and batch counts
│   │       ├── 06_routes_suspends.bcl # Suspension reason lists and release routes
│   │       ├── 07_routes_master.bcl# Providers, charge codes, CPTs, metadata
│   │       └── 08_routes_admin.bcl # User administration, roles, and companies
│   └── migrations/                 # DDL Schemas & Seed Data (oarkflow/migrate)
│       ├── 001_initial_schema.sql  # 29 Production tables (Clear20 compliant)
│       └── seeds/                  # Seed catalogs
│           └── 001_seed_data.sql   # Facilities, users, workitems, codes, encounters
├── .env.example                    # Template environment variables
├── go.mod                          # Go module definition
└── README.md                       # Comprehensive guide and documentation
```

---

## 4. Security, Credentials & RBAC

### Dual Credential Verification Architecture
CLEAR supports seamless interoperability between modern cryptographic standards and legacy databases:
1. **Primary Algorithm (Argon2id)**:
   - Evaluated using standard format: `$argon2id$v=19$m=65536,t=3,p=4$<salt>$<hash>`.
   - Used for all new user registrations and password updates.
2. **Legacy Algorithm Fallback (Bcrypt)**:
   - When an incoming hash begins with `$2a$`, `$2b$`, or `$2y$`, the authentication engine safely attempts Bcrypt verification.
   - Allows existing Clear20 users to log in without administrative password resets.

### RBAC Matrix & Role Hierarchy

| Role | Role Code | Description | Key Capabilities |
|---|---|---|---|
| **Super Administrator** | `super_admin` | Global system operator | Full system access, company onboarding, database maintenance |
| **Administrator** | `admin` | Organization admin | User provisioning, role assign/revoke, facility setup |
| **Supervisor** | `supervisor` | Clinical team lead | Release suspended charts, reassign queues, review QA metrics |
| **QA Auditor** | `qa_auditor` | Compliance auditor | Claim QA charts, log error codes, score coding quality, approve |
| **Medical Coder** | `coder` | Certified medical coder | Claim open charts, document ICD-10 / CPT, record PQRS, suspend |
| **Data Entry** | `data_entry` | Billing specialist | Enter charges, post claims, verify encounter charge masters |
| **Viewer** | `viewer` | Read-only auditor | View patient headers, charts, and audit histories |

---

## 5. Clinical State Machine & Encounter Lifecycle

Charts progress through a deterministic, auditable state machine with optimistic locking:

```mermaid
stateDiagram-v2
    [*] --> open: Ingestion / Registration
    open --> in_progress: Claim Chart (start-coding)
    in_progress --> suspended: Suspend Chart (Needs Documentation / Provider Query)
    suspended --> open: Supervisor Release
    in_progress --> coding_complete: Save & Complete Coding
    coding_complete --> qa_review: Route to QA Sampling
    qa_review --> in_progress: QA Rejected (Feedback Returned)
    qa_review --> qa_approved: QA Approved
    coding_complete --> billed: Direct to Billing (No QA Sample)
    qa_approved --> billed: Final Billing Submission
    billed --> [*]
```

### Concurrency Protection & Audit Trail
- **Optimistic Locking**: When a coder claims a chart (`/coding/:workitem/start-coding`), the status transitions atomically to `in_progress` with `assigned_coder_id` bound to the authenticated user.
- **Audit Logging**: Every suspension and release creates an immutable record in `encounter_suspends` recording the `suspend_reason_id`, timestamp, user ID, and resolution comments.

---

## 6. Complete 96-Route Parity Matrix

CLEAR exposes **96 full endpoints**, supporting both modern clean paths and legacy `/web/client/...` URLs:

| Domain | Method | Clean Route | Legacy Dual Route (`/web/client/...`) | Role / Auth | Description |
|---|---|---|---|---|---|
| **Auth** | `GET` | `/login` | `/web/client/login` | Public | Login status check |
| **Auth** | `POST` | `/login` | `/web/client/login` | Public | Authenticate with email & password |
| **Auth** | `GET` | `/me` | `/web/client/me` | Authenticated | Current user profile & capabilities |
| **Auth** | `POST` | `/user/info` | `/web/client/user/info` | Authenticated | Detailed user profile information |
| **Auth** | `GET` | `/user/roles/:id` | `/web/client/user/roles/:id` | Authenticated | Assigned roles for specified user |
| **Master** | `GET` | `/facilities` | `/web/client/facilities` | Authenticated | List all active healthcare facilities |
| **Master** | `GET` | `/facility/:id/workitems` | `/web/client/facility/:id/workitems` | Authenticated | List workitems belonging to facility |
| **Master** | `GET` | `/coding/facilities/info` | `/web/client/coding/facilities/info` | Authenticated | Facility operational statistics |
| **Master** | `GET` | `/workitem` | `/web/client/workitem` | Authenticated | List all configured workitem types |
| **Master** | `GET` | `/workitem/types` | `/web/client/workitem/types` | Authenticated | Workitem categories (Inpatient, ER, etc.) |
| **Master** | `GET` | `/workitem/:id/settings` | `/web/client/workitem/:id/settings` | Authenticated | Workitem configuration settings |
| **Master** | `GET` | `/workitem/:id/description` | `/web/client/workitem/:id/description` | Authenticated | Workitem documentation & description |
| **Master** | `GET` | `/workitem/:id/workitems` | `/web/client/workitem/:id/workitems` | Authenticated | Company-scoped workitems |
| **Master** | `GET` | `/facility/:id/providers` | `/web/client/facility/:id/providers` | Authenticated | Providers practicing at facility |
| **Master** | `GET` | `/provider` | `/web/client/provider` | Authenticated | Provider directory |
| **Master** | `GET` | `/provider/:id/info` | `/web/client/provider/:id/info` | Authenticated | Provider NPI, taxonomy, credentials |
| **Master** | `GET` | `/charge-code` | `/web/client/charge-code` | Authenticated | Chargemaster rate schedules |
| **Master** | `GET` | `/cpt-code` | `/web/client/cpt-code` | Authenticated | CPT procedure codes directory |
| **Master** | `GET` | `/cpt-code/:id` | `/web/client/cpt-code/:id` | Authenticated | Specific CPT code details & RVU |
| **Master** | `GET` | `/cpts` | `/web/client/cpts` | Authenticated | CPT reference index |
| **Master** | `GET` | `/metadata/rules` | `/web/client/metadata/rules` | Authenticated | Clinical validation rules catalog |
| **Coding** | `GET` | `/coding/workitem/:id` | `/web/client/coding/workitem/:id` | Coder, Supervisor | Workitem dashboard summary |
| **Coding** | `GET` | `/coding/:workitem/open-list` | `/web/client/coding/:workitem/open-list` | Coder, Supervisor | Unassigned open clinical charts |
| **Coding** | `GET` | `/coding/:workitem/in-progress-list` | `/web/client/coding/:workitem/in-progress-list` | Coder, Supervisor | Charts currently claimed by coder |
| **Coding** | `GET` | `/coding/:workitem/completed-list` | `/web/client/coding/:workitem/completed-list` | Coder, Supervisor | Finished charts awaiting billing/QA |
| **Coding** | `GET` | `/coding/in-progress-count` | `/web/client/coding/in-progress-count` | Coder, Supervisor | Global in-progress chart count |
| **Coding** | `POST` | `/coding/:workitem/search` | `/web/client/coding/:workitem/search` | Coder, Supervisor | Query charts by MRN, date, patient |
| **Coding** | `POST` | `/coding/:workitem/start-coding` | `/web/client/coding/:workitem/start-coding` | Coder | Claim open chart into in-progress |
| **Coding** | `GET` | `/coding/:workitem/:id/patient-header` | `/web/client/coding/:workitem/:id/patient-header` | Coder, QA, Viewer | Demographic banner & encounter context |
| **Coding** | `GET` | `/coding/:workitem/:id/documentation` | `/web/client/coding/:workitem/:id/documentation` | Coder, QA, Viewer | Clinical notes & transcription text |
| **Coding** | `GET` | `/coding/:workitem/:id/summary` | `/web/client/coding/:workitem/:id/summary` | Coder, QA, Viewer | Historical encounter summary |
| **Coding** | `POST` | `/coding/:workitem/:id/save` | `/web/client/coding/:workitem/:id/save` | Coder | Save ICD-10 / CPT clinical codes |
| **Coding** | `POST` | `/coding/:workitem/:id/verify` | `/web/client/coding/:workitem/:id/verify` | Coder | Validate codes against NCCI edits |
| **Coding** | `POST` | `/coding/:workitem/:date/pqrs-measures` | `/web/client/coding/:workitem/:date/pqrs-measures` | Coder | Available PQRS quality measures |
| **Coding** | `POST` | `/coding/:workitem/:id/pqrs-codes` | `/web/client/coding/:workitem/:id/pqrs-codes` | Coder | PQRS codes mapped to encounter |
| **Coding** | `POST` | `/coding/:workitem/:id/pqrs-codes-by-id`| `/web/client/coding/:workitem/:id/pqrs-codes-by-id`| Coder | Query specific PQRS measure codes |
| **QA** | `GET` | `/coding/:workitem/qa-list` | `/web/client/coding/:workitem/qa-list` | Auditor, Supervisor | Queue of charts sampled for QA review |
| **QA** | `GET` | `/coding/:workitem/qa-in-progress-list` | `/web/client/coding/:workitem/qa-in-progress-list` | Auditor, Supervisor | Charts currently claimed by auditor |
| **QA** | `POST` | `/coding/:workitem/start-qa` | `/web/client/coding/:workitem/start-qa` | Auditor | Claim chart for quality audit |
| **QA** | `POST` | `/coding/:workitem/:id/save-qa` | `/web/client/coding/:workitem/:id/save-qa` | Auditor | Record audit findings & pass/fail score |
| **Data Entry**| `GET` | `/coding/:workitem/de-list` | `/web/client/coding/:workitem/de-list` | Data Entry | Charts ready for charge data entry |
| **Data Entry**| `GET` | `/coding/:workitem/de-in-progress-list`| `/web/client/coding/:workitem/de-in-progress-list`| Data Entry | Charts claimed for data entry |
| **Data Entry**| `GET` | `/coding/de-in-progress-count` | `/web/client/coding/de-in-progress-count` | Data Entry | Total data entry in-progress count |
| **Data Entry**| `POST` | `/coding/:workitem/start-de` | `/web/client/coding/:workitem/start-de` | Data Entry | Claim chart for data entry |
| **Data Entry**| `POST` | `/coding/:workitem/:id/save-de` | `/web/client/coding/:workitem/:id/save-de` | Data Entry | Save charge entry and mark billed |
| **Suspends** | `GET` | `/coding/suspend-reasons` | `/web/client/coding/suspend-reasons` | Authenticated | Catalog of standard suspension reasons |
| **Suspends** | `GET` | `/coding/:workitem/suspended-list` | `/web/client/coding/:workitem/suspended-list` | Coder, Supervisor | Queue of currently suspended charts |
| **Suspends** | `GET` | `/coding/:workitem/:id/suspends` | `/web/client/coding/:workitem/:id/suspends` | Coder, Supervisor | Suspension history & notes for encounter |
| **Suspends** | `POST` | `/coding/:workitem/:id/suspend` | `/web/client/coding/:workitem/:id/suspend` | Coder | Suspend chart with reason and notes |
| **Suspends** | `POST` | `/coding/:workitem/:id/release-suspend` | `/web/client/coding/:workitem/:id/release-suspend` | Supervisor | Release suspended chart back to queue |
| **Admin** | `GET` | `/users` | `/web/client/users` | Admin, SuperAdmin | Directory of all system users |
| **Admin** | `GET` | `/users/:id` | `/web/client/users/:id` | Admin, SuperAdmin | Detailed user account record |
| **Admin** | `GET` | `/users/:id/permissions` | `/web/client/users/:id/permissions` | Admin, SuperAdmin | Effective capabilities for user |
| **Admin** | `GET` | `/roles` | `/web/client/roles` | Admin, SuperAdmin | System role definitions |
| **Admin** | `POST` | `/user/assign-role` | `/web/client/user/assign-role` | Admin, SuperAdmin | Assign role to user |
| **Admin** | `POST` | `/user/revoke-role` | `/web/client/user/revoke-role` | Admin, SuperAdmin | Revoke role from user |
| **Admin** | `GET` | `/companies` | `/web/client/companies` | SuperAdmin | Multitenant client companies |
| **Admin** | `GET` | `/companies/:id` | `/web/client/companies/:id` | SuperAdmin | Company organization profile |
| **Admin** | `GET` | `/coders` | `/web/client/coders` | Supervisor, Admin | Directory of certified coders |
| **System** | `GET` | `/ping` | *(none)* | Public | Healthcheck and readiness probe |
| **System** | `GET` | `/background/service` | `/web/client/background/service` | Admin | Worker & background daemon status |
| **Docs** | `GET` | `/docs` | *(none)* | Public | Interactive Swagger UI 5.x API Explorer |
| **Docs** | `GET` | `/swagger` | *(none)* | Public | Swagger UI alias |
| **Docs** | `GET` | `/docs/openapi.json` | *(none)* | Public | Live OpenAPI 3.1 specification JSON |
| **Docs** | `GET` | `/openapi.json` | *(none)* | Public | Root OpenAPI 3.1 JSON specification |
| **Docs** | `GET` | `/swagger.json` | *(none)* | Public | Root Swagger JSON specification |

*(Total: 96 clinical/admin routes + 5 Swagger UI & OpenAPI documentation endpoints).*

---

## 7. Interactive Swagger UI & OpenAPI 3.1 Engine

CLEAR includes **automatic, zero-boilerplate Swagger UI generation** directly integrated into the Ref runtime:

### Endpoints Available
- **Interactive Swagger UI**: [http://127.0.0.1:3000/docs](http://127.0.0.1:3000/docs) (or alias `/swagger`)
- **OpenAPI 3.1 Specification**: [http://127.0.0.1:3000/docs/openapi.json](http://127.0.0.1:3000/docs/openapi.json) (or `/openapi.json`, `/swagger.json`)

### Key Swagger UI Capabilities
1. **Zero-Configuration Mounting**: Automatically mounted at `/docs` whenever `p.Mount(app)` is executed.
2. **Smart Domain Grouping**: Automatically categorizes endpoints into clinical and operational tags (`Authentication`, `Medical Coding`, `Quality Assurance`, `Charge Data Entry`, `Chart Suspensions`, `Facilities`, `Workitems`, `Healthcare Providers`, `Chargemaster`, `CPT & Medical Terminology`, `Administration & RBAC`).
3. **Interactive "Try it out" & Authorization**:
   - Built-in **Authorize** modal supporting:
     - `cookieAuth`: Session cookie authentication (`clear_session_id`).
     - `bearerAuth`: JWT Bearer tokens (`Authorization: Bearer <token>`).
     - `apiKeyAuth`: API key header (`X-API-Key`).
4. **Instant Search & Deep-Linking**:
   - Integrated live filter bar to quickly locate endpoints by path or keyword.
   - Deep-linking (`deepLinking: true`) enables direct URL anchors to specific methods.
   - Accordion collapse (`docExpansion: "none"`) keeps the 192 route catalog fast and navigable.
5. **Schema Exploration**: All 22 clinical BCL shapes (`User`, `Encounter`, `DiagnosisCode`, `ProcedureCode`, etc.) are rendered with property types, constraints, and validation rules.

---

## 8. Database Engine & Schema (29 Tables)

The database schema reflects enterprise healthcare requirements, provisioned automatically via `contrib/migrate`:

1. `users`: User identity, credential hashes, status.
2. `roles`: Role definitions and hierarchies.
3. `user_roles`: User-to-role assignment map.
4. `permissions`: Granular action permissions.
5. `role_permissions`: Role-to-permission mapping.
6. `companies`: Multitenant company accounts.
7. `facilities`: Hospitals, clinics, emergency rooms.
8. `workitems`: Clinical queues and specialty classifications.
9. `workitem_settings`: Configuration flags and workflow rules per workitem.
10. `providers`: Attending and referring physicians.
11. `facility_providers`: Provider-to-facility credentialing.
12. `patients`: Master Patient Index (MPI), MRN, demographics.
13. `encounters`: Patient visits, admission dates, chart statuses.
14. `encounter_documentation`: Clinical notes, transcribed reports, EHR feeds.
15. `encounter_codes`: Documented ICD-10 and CPT codes for encounter.
16. `cpt_codes`: CPT-4 procedure code reference directory.
17. `icd10_codes`: ICD-10-CM diagnosis code reference catalog.
18. `chargemaster`: Hospital charge rates and department billing codes.
19. `suspend_reasons`: Standard suspension categories (Missing Op-Report, etc.).
20. `encounter_suspends`: Audit log of chart suspensions and supervisor notes.
21. `qa_reviews`: Audit quality reviews, sample scores, error categories.
22. `qa_errors`: Specific line-item errors logged during audit.
23. `data_entry_records`: Charge entry logs and billing export batches.
24. `pqrs_measures`: Clinical quality measure definitions.
25. `pqrs_codes`: Quality data codes associated with PQRS measures.
26. `encounter_pqrs`: Documented quality codes for encounter.
27. `validation_rules`: Automated NCCI and bundling rule definitions.
28. `audit_logs`: Immutable clinical compliance audit trail.
29. `background_services`: Worker heartbeat and scheduled job logs.

---

## 9. Developer Quickstart & Verification Guide

### Prerequisites
- **Go 1.24+**
- **SQLite 3** (Default for development / local testing) or **PostgreSQL 14+** (Production)
- **Python 3.10+** (For automated endpoint and lifecycle verification suites)

### Step 1: Environment Setup
Copy the sample environment file:
```sh
cp .env.example .env
```

Default `.env` configuration:
```env
APP_ENV=development
PORT=3000
DB_DRIVER=sqlite
DB_DSN=file:.data/clear/app.db?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)
SECRETS_DIR=./.data/secrets
AUTO_MIGRATE=true
```

### Step 2: Database Migration & Seeding
Using the compiled standalone migrator:
```sh
# Build the migrator utility
go build -o migrator ./cmd/migrator

# Apply schema migrations
./migrator migrate

# Seed baseline facilities, users, providers, and test charts
./migrator seed
```

### Step 3: Run the CLEAR Platform Server
```sh
# Build the platform server
go build -o server ./cmd/server

# Start the server daemon
./server
```

Output:
```text
INFO starting CLEAR medical coding platform env=development port=3000
INFO migration check: database schema up to date
INFO compiling BCL application document dir=resources/config
INFO mounting BCL routes
INFO Swagger UI documentation active docs=http://127.0.0.1:3000/docs swagger=http://127.0.0.1:3000/swagger openapi=http://127.0.0.1:3000/openapi.json
INFO server listening addr=:3000
+--------------------------------+
| Name   : fh                    |
| URL    : http://127.0.0.1:3000 |
| Routes : 214                   |
| Mode   : fast                  |
+--------------------------------+
```

### Step 4: Run Automated Verification Suites

Two comprehensive Python test suites validate complete functionality:

1. **Clinical State Machine & Dual-Credential Security**:
   ```sh
   python3 scripts/verify_lifecycle.py
   ```
   *Verifies:*
   - Argon2id login (`doctor@clear.io`)
   - Bcrypt legacy fallback login (`legacy_user@clear.io`)
   - Rejection of invalid credentials (401 Unauthorized)
   - Dynamic role assignment and revocation
   - Optimistic chart claim (`start-coding`)
   - Chart suspension with audit log and supervisor release
   - Clinical documentation save and completion
   - Workitem queue dashboard metrics

2. **101-Endpoint Verification Suite (96 Routes + 5 Docs Endpoints)**:
   ```sh
   python3 scripts/verify_all_endpoints.py
   ```
   *Verifies:*
   - All 96 clinical/admin routes (clean REST and legacy `/web/client/...`)
   - Interactive Swagger UI viewer (`GET /docs`, `GET /swagger`)
   - Live OpenAPI 3.1 specifications (`GET /docs/openapi.json`, `GET /openapi.json`, `GET /swagger.json`)

---

## 10. Default Seed Credentials

The test seed script automatically configures the following accounts:

| Email | Password | Role | Hash Format | Notes |
|---|---|---|---|---|
| `doctor@clear.io` | `DoctorSecret123!` | `admin`, `super_admin` | **Argon2id** | Primary administrative account |
| `legacy_user@clear.io` | `LegacySecret123!` | `coder` | **Bcrypt** | Demonstrates transparent Bcrypt fallback |
| `coder@clear.io` | `DoctorSecret123!` | `coder` | **Argon2id** | Certified medical coder account |
| `auditor@clear.io` | `DoctorSecret123!` | `qa_auditor` | **Argon2id** | Quality assurance auditor |
| `de@clear.io` | `DoctorSecret123!` | `data_entry` | **Argon2id** | Charge data entry account |

---

## 11. Production Deployment

### Docker / Containerized Environment
CLEAR compiles to a single, static binary with no external runtime dependencies:
```dockerfile
FROM golang:1.24-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o server ./cmd/server

FROM alpine:latest
WORKDIR /app
COPY --from=builder /app/server .
COPY resources/ resources/
EXPOSE 3000
ENTRYPOINT ["./server"]
```

### PostgreSQL Configuration
To connect to an enterprise PostgreSQL cluster:
1. Update `.env`:
   ```env
   DB_DRIVER=postgres
   DB_DSN=postgres://clear_user:secret@postgres.internal:5432/clear_db?sslmode=require
   ```
2. Uncomment the PostgreSQL driver in `cmd/server/main.go`:
   ```go
   _ "github.com/jackc/pgx/v5/stdlib"
   ```
3. Run `./migrator migrate` to provision PostgreSQL tables.
