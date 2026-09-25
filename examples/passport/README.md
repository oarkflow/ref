# passport

This example is a public service pipeline for a **Passport Request**. It is built from a single `pipeline` block in [`app.bcl`](app.bcl) and served by the `pipeline.cases` resource. No application code is involved.

| Stage | Who | What happens |
|---|---|---|
| `application` | the applicant (public, anonymous or signed in) | Fills a **wizard** page. Its groups are: About you, Addresses (repeatable), Your request (a **tabbed** group), a **summary** review, and a Declaration. |
| `verification` | `officer` | Sees the submission **read-only** in accordion sections and adds stage-local *officer notes*. Two **review** nodes verify or flag every field. A **check** node and an **automated** watchlist screening (which runs an intent) run on entry. The officer can then accept, return for correction, or reject. |
| `biometrics` | `officer` | Photo, fingerprints and signature are each a **task** node with its own status. The stage is skipped for renewals and auto-advances once all nodes are done. |
| `approval` | `supervisor` | A **four-eyes** sign-off: the approver may not be the officer who reviewed the case, nor the applicant. Supervisors see sensitive fields unmasked. |
| `issuance` | automatic | A signed **certificate** is issued on entry, and the case completes. |

Returning a case for correction reopens the application stage. In that state, **only the flagged fields are editable**. Resubmitting sends the case straight back to verification: fields that were already verified stay verified, and only the flagged ones need review again.

Cases belong to a district of the `org.hierarchy`. This has two effects:
- The *collection office* options are reference data resolved per district.
- Officers only ever see cases in their own jurisdiction.

The example also exercises the work-management features (see [`docs/pipelines.md`](../../docs/pipelines.md#work-management)):

- **Routing:** verification is routed to the least-loaded officer covering the case's district; after a correction it returns to the same officer. Officers work only the cases they hold, while supervisors assign and officers delegate.
- **SLA:** two working days on the office calendar (Nepali working week and holidays), with a warning, re-routing on breach, and escalation to a senior officer.
- **Holds:** a case can be put on hold, which stops the SLA clock.
- **Notes:** internal notes for staff, public notes for the applicant too.
- **External link:** the ward office fills its recommendation through a signed, single-use link.
- **Computed fee and rule:** the fee is computed, and a cross-field rule restricts fast-track service to the Department of Passports.
- **Event hooks:** SLA, return, completion and link events record notifications after each change is saved.
- **Operations:** bulk operations, analytics, a scheduled sweep, legal hold and right-to-erasure.

## Run

```sh
export PASSPORT_JWT_SECRET=$(openssl rand -hex 24)
export PASSPORT_SIGNING_SECRET=$(openssl rand -hex 24)
go run ./examples/passport &          # UI at http://localhost:8096/

OFFICER=$(go run ./examples/passport -token '{"sub":"o1","roles":["officer"],"org_units":["ktm"]}')
SUPER=$(go run ./examples/passport -token '{"sub":"s1","roles":["supervisor"],"org_units":["bagmati"]}')

# An anonymous applicant starts a case and gets back an access key.
curl -s -XPOST localhost:8096/api/passport/cases -d '{"org_unit":"ktm"}' -H 'Content-Type: application/json'
```

## API

| Route | Purpose |
|---|---|
| `GET /api/passport` | The pipeline definition, for rendering a start page |
| `POST /api/passport/cases` `{org_unit, data?}` | Start a case. An anonymous caller gets an `access_key`, to send back as `X-Access-Key`. |
| `GET /api/passport/cases?scope=queue\|mine\|all` | A work queue (by role and jurisdiction), your own cases, or everything you may view |
| `GET /api/passport/cases/:id[/stages/:stage]` | The **view**: the page groups with their mode and layout, inputs with values (sensitive ones masked), flags, verdicts, nodes, and the actions you may take |
| `PUT /api/passport/cases/:id/stages/:stage` `{data}` | Save a draft. Only inputs editable for you are accepted, and every field is validated at once. |
| `POST /api/passport/cases/:id/stages/:stage/actions/:action` `{comment?, data?, revision?}` | Submit, accept, return, reject, approve or withdraw |
| `POST /api/passport/cases/:id/stages/:stage/nodes/:node/:verb` | Verify, complete, approve, reject, run, issue or waive |
| `GET /api/passport/cases/:id/history` | Stage states, history and certificates |
| `GET /api/passport/certificates/:number-or-code` | Public certificate verification |
| `POST /api/passport/cases/:id/stages/:stage/work/:op` | claim, release, assign `{to}`, delegate `{to}`, suspend `{reason, until}`, resume |
| `POST /api/passport/cases/:id/notes` | Add a note `{body, internal, parent_id}` |
| `POST /api/passport/cases/:id/stages/:stage/links` | Issue an external link `{party, scope, ttl}` |
| `GET/POST /api/passport/links/:token` | The outside party's page and submission |
| `POST /api/passport/cases/:id/hold/:op` | Place or release a legal hold (supervisor, dpo) |
| `POST /api/passport/erasure` | Right to erasure `{identifiers, mode, dry_run}` (dpo) |
| `GET /api/passport/analytics` | Process analytics (supervisor) |
| `POST /api/passport/bulk` | One operation on many cases |
| `POST /api/passport/sweep` | Apply SLA/escalation/retention now (also runs every minute) |

The full journey is exercised over HTTP in [`platform/passport_e2e_test.go`](../../platform/passport_e2e_test.go). The model is documented in [`docs/pipelines.md`](../../docs/pipelines.md).
