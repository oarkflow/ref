# ProcessGate pipeline migration

This standalone REF example contains 102 pipelines, 505 stages, and 696 forms
converted from the ProcessGate flow bundles. It needs no ProcessGate code or
service at runtime. The source bundles were found through the pipeline scripts
in `~/Sites/processgate/scripts`: `generate_government_pipeline_bundles.py`
produces the ministry flows, `assemble_pipeline_bundle.py` validates and joins
their split JSON, and `test_government_pipeline_flows.py` exercises them in
ProcessGate. The other scripts are capability, security, report, and operations
checks; they do not define additional pipeline bundles.

The generated ministry directory supplies 29 flows. The `docs/flows/bundles`
directory supplies 73 distinct additional flow templates (imports win over
exports when a slug occurs twice). `manifest.json` records each source path,
form and stage count, and behavior that requires manual configuration.

## Browser app

Open `http://localhost:8080/app/` after starting the example. It signs in,
lists the pipelines (`GET /api/pipelines`), starts cases, renders each stage's
forms with server-side validation errors, uploads files, saves drafts, runs
actions and review/approval nodes (per-field verdicts), shows the SLA panel,
work operations, notes and history, and has an Admin page to invite users and
run the SLA sweep. It is a no-build client in `static/`; it is not a form or
page *builder*.

## Run

From the repository root:

```sh
cd cmd/ref
go run . validate ../../examples/processgate-pipelines
go run . ../../examples/processgate-pipelines
```

The example reads `.env.example` for local settings. Register through
`POST /api/auth/register`, scan the returned `otpauth://` URI in any standard
authenticator app, confirm with `POST /api/auth/register/confirm`, and sign in
through `POST /api/auth/login` with the current code. An administrator can
invite staff through `POST /api/auth/invite`; invited users accept through
`POST /api/auth/invitations/accept`. Include the login token as a bearer token
for the case API. Pipeline names use snake case: for example
`forest_encroachment_restoration_action`. The generic routes are:

| Method | Path | Purpose |
| --- | --- | --- |
| GET | `/api/pipelines/:pipeline` | Describe forms and stages |
| POST | `/api/pipelines/:pipeline/cases` | Start a case |
| GET | `/api/pipelines/:pipeline/cases` | List authorized cases |
| GET | `/api/pipelines/:pipeline/cases/:id` | Read a case |
| GET | `/api/pipelines/:pipeline/cases/:id/stages/:stage` | Render a stage |
| PUT | `/api/pipelines/:pipeline/cases/:id/stages/:stage` | Save stage data |
| POST | `/api/pipelines/:pipeline/cases/:id/stages/:stage/actions/:action` | Submit or complete a stage |
| POST | `/api/pipelines/:pipeline/cases/:id/stages/:stage/nodes/:node/:verb` | Perform verification or approval work |
| POST | `/api/pipelines/:pipeline/cases/:id/stages/:stage/work/:op` | Claim, assign, or release stage work |
| GET | `/api/pipelines/:pipeline/cases/:id/stages/:stage/schedules` | List dated schedule instances |
| POST | `/api/pipelines/:pipeline/cases/:id/stages/:stage/schedules` | Add a schedule (`scheduled_date`, `label`) |
| POST | `/api/pipelines/:pipeline/cases/:id/stages/:stage/schedules/:schedule/submissions` | Submit form data for one date (`form_key`, `data`) |
| POST | `/api/pipelines/:pipeline/cases/:id/payments/verify` | Record a verified payment (`reference`, `amount` in minor units, `currency`) |
| GET | `/api/notifications` | Read delivered in-app notifications |
| POST | `/api/pipelines/:pipeline/cases/:id/request-information` | Staff request clarification (`reason`) |
| POST | `/api/pipelines/:pipeline/cases/:id/submit-information` | Applicant responds (`payload`) |
| POST | `/api/pipelines/:pipeline/cases/:id/stages/:stage/files/:path` | Upload a file field |
| GET | `/api/pipelines/:pipeline/cases/:id/files/:path` | Read an authorized file |

Run `python3 examples/processgate-pipelines/migrate.py` to regenerate from the
default ProcessGate checkout. Use `--source <path-to-docs/flows>` for another
checkout and `--output <directory>` for a separate output. The converter writes
the BCL configuration and manifest deterministically.

## Migration scope

Forms, supported field widgets, select values, field requirements, stage
order, stage roles, public entry flags, due time, stage form groups, and basic
review and approval nodes are mapped into REF. Multi-date stages keep dated
instances and separate form submissions. RFI pauses the active stage in
`requires_information` until the applicant submits a response. Rollback actions
return to an earlier stage. Payment flows block completion until a trusted verifier records
the settlement. Recognized notification events are delivered to a durable
in-app inbox. The generated example includes a user directory, authenticator
MFA registration, database, file storage, JWT authentication, and case routes.

This is a structural migration, not ProcessGate behavioral parity. The
`manifest.json` warnings identify each flow's outstanding work. In particular,
Source email verification is replaced by authenticator TOTP, and all API
routes for registered flows require sign-in. Flows whose source allows
anonymous entry return an access key at case start; send it as `X-Access-Key`
when reading or changing that case.
Verification scopes, approval assignment, some rollback
semantics, and domain-specific notification events need review. The six
payment bundles provide no gateway and a zero amount. A payment officer can
verify an assessed amount manually; set `PROCESSGATE_PAYMENT_VERIFY_INTENT` to
an intent that checks a provider and returns exactly
`{verified: true, reference, amount, currency}` to require provider verification
before recording payment. Implement and test that provider intent for your
chosen gateway before accepting online payments. Existing
ProcessGate cases, users, attachments, and status history are not imported.
