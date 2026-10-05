# SMS gateway

A complete SMS sending application written **only in BCL, rules, SQL and SPL templates**. There is no Go code in this directory. Everything generic lives in REF; the messaging drivers (SMPP through [smppflow](https://github.com/oarkflow/smppflow), the embedded [oarkflow/broker](https://github.com/oarkflow/broker) queue) live in `contrib/messaging`.

What it does:

* **Channels.** SMPP (`np_telecom`), vendor REST/JSON APIs (`premium_np`, `in_vendor`, `global_fallback`) and a plain-text GET API (`smspasal`, see `send.smspasal` in `config/32_send.bcl`).
* **Providers.** Platform providers, providers for a country, providers assigned to a tenant or to a user (optionally per country). SMPP binds, vendor HTTP APIs, and anything you add.
* **Routing by parameters.** Sender, recipient number and country, message type, delivery quality, provider capability (unicode, delivery receipts, alphanumeric sender), cost, account assignment and provider health. The rules decide; Go decides nothing.
* **Pipeline.** prepare (account, number, text, validation, route, price) → accept (pay, store, queue) → deliver (claim, gate, send, classify, decide, settle) → receipt.
* **Delivery guarantees.** At-least-once delivery, **exactly-once payment**, configurable retry with backoff, failover down the route, a circuit breaker per provider and route, recovery of lost jobs.
* **Web console.** SPL pages and static assets.

## Run it and try the console

From the repository root, with no variables and no second process:

```sh
make run dir=./examples/smsgateway          # listens on :8080; add addr=127.0.0.1:9000 to change it
```

(`make run --dir=…` cannot work: GNU make reads `--dir` as its own `--directory` option. Use `dir=`.) `make run` runs `cmd/ref`, the generic runner, which has its own `go.mod` and carries every driver in the repository. `make build` writes `bin/ref`; then `bin/ref ./examples/smsgateway` does the same.

The configuration is all in `config/*.bcl`. The `sandbox` resource in `config/02_resources.bcl` starts stand-in carriers and vendors with the application (SMPP on :2775, vendor APIs on :9100, including an SMSPasal-style endpoint), so every provider works locally. State is kept in `examples/smsgateway/.data/` (SQLite, the broker log, sessions; git-ignored). Delete it to start clean.

**Console accounts** (development only; change them in `config/01_database.bcl` or with `PUT /v1/admin/users/{id}/password`):

| Sign in | Password | Role |
|---|---|---|
| `demo@example.com` | `demo-pass-123` | account: sends, campaigns |
| `operator@example.com` | `operator-pass-123` | operator: everything, plus providers, traffic and approvals |

The JSON API under `/v1` uses API keys instead. The development operator key is `dev-admin-key-change-me-0123` (resource `admin_auth` in `config/02_resources.bcl`).

**In the browser** (<http://127.0.0.1:8080/>). Every page except the sign-in page needs a session; without one the page's script sends you to `/login`.

1. **Sign in** at `/login`. The session is a signed, HttpOnly cookie.
2. **Send** (`/`): a number such as `+977 984 123 4567`, and either plain text or a template. Choosing a template shows one input per placeholder (pre-filled with defaults) and a live preview: the rendered text, characters, segments, encoding, message type and sender. *Explain* shows the route, rejected providers and price without sending; *Send* sends and shows your balance.
3. **Messages** (`/messages`): your messages and ledger. Reload to watch a message go from `queued` to `delivered`.
4. **Campaigns** (`/campaigns`): enter a name, recipients (one per line) and text. The campaign waits for approval; nothing is sent or charged until then.
5. **Operator** (as the operator; the header shows these links only to the operator): `/admin` overview, `/admin/providers`, `/admin/rules`, `/admin/accounts` (see [Operating it at runtime](#operating-it-at-runtime)). The overview: *Campaigns waiting for approval* has *approve* and *reject* buttons. Approving runs the campaign (every recipient goes through the normal pipeline); a refused number is recorded as failed and does not stop the rest. The page also lists providers (click *pause* / *resume*), traffic, and recent messages.

Things to try: a number in India (`+91 98765 43210`) as `acme_alice`; an OTP template to see the quality floor; pause `np_telecom` on the operator page and send as `demo` to watch the route change; send the same `reference` twice to see `duplicate`.

To send for real through SMSPasal, see [SMSPasal](#smspasal-live-provider).

## API

| Route | Who | |
|---|---|---|
| `POST /v1/messages` | account key | Send. `202` once the message is stored and paid for. A repeated `reference` returns the same message with `duplicate:true`; a reused reference with different content is `409`. |
| `POST /v1/route/explain` | account key | Dry run: how the number was read, the chain of providers, why each other provider was rejected, the price. Nothing is written. |
| `GET /v1/messages`, `/v1/messages/:id` | account key | Own messages, with every attempt. |
| `GET /v1/balance`, `/v1/ledger` | account key | Own money. |
| `POST /v1/webhooks/dlr/:provider` | `X-Webhook-Secret` | Vendor delivery receipts. |
| `GET/PUT /v1/admin/users…`, `POST …/key`, `POST …/topup`, `GET …/balance` | operator key | Accounts. A top-up is idempotent on its `reference`. |
| `GET /v1/admin/providers`, `PUT …/:id/state` | operator key | See and pause or resume a provider without a deploy. |
| `GET /v1/admin/messages`, `/stats`; `POST /v1/admin/optouts` | operator key | Reporting and opt-outs. |
| `POST /login`, `POST /logout`, `GET /ui/me` | none / session | Console sign-in with a session cookie. |
| `/ui/messages`, `/ui/route/explain`, `/ui/balance`, `/ui/ledger`, `/ui/campaigns…` | session (sender or admin) | The console's API: the same intents as `/v1`, behind the session. |
| `/ui/admin/providers`, `…/messages`, `…/stats`, `…/approvals…` | session (admin) | The operator's console API, including campaign approval, and the only place carriers are named. |
| `GET /login`, `/`, `/messages`, `/campaigns`, `/admin` | none | Console pages (SPL shells; they hold nothing private). |

A send is `{ to, text | template+vars+lang, from?, type?, reference?, dlr?, delay_seconds?, expires_in? }` (the `sms_send` shape in `config/03_schemas.bcl`).

## Where each thing is decided

| Question | Answer lives in |
|---|---|
| How a number is read (`+977…`, `00977…`, national), which country, valid length | `rules/numbering.bcl` |
| Is the message acceptable (status, opt-out, sender, text, countries, limits, price cap) | `rules/validate.bcl` |
| Which providers may carry it (capability, assignment, ownership, operator rules) | `rules/routing.bcl`, `route_eligibility` |
| Which of them first, and what the fall-backs are (tier, quality, delivery, failures, cost) | `rules/routing.bcl`, the `route_*` rankings |
| Which providers an operator named for this account, number, template or text (generated from the `routing_rules` table, so it needs no deploy) | `templates/rules/custom_routing.html` → `config/45_routing_rules.bcl` |
| What it costs | `rules/pricing.bcl` |
| Retry, fail over or fail; throttling; what each error means | `rules/delivery.bcl` |
| What a delivery receipt means, when to refund | `rules/receipts.bcl` |
| Which template and sender a template name implies | `rules/templates.bcl` |
| What a repeated reference means | `rules/idempotency.bcl` |
| What an account is told about a message (status, progress, why it failed) | `rules/status.bcl` |
| Message text | `templates/sms/*.html` (SPL) |
| Providers, their costs, assignments, retry settings | rows in the `providers` and `provider_costs` tables, seeded in `config/01_database.bcl` |
| Provider connections and secrets | `config/02_resources.bcl` |

To change behaviour you change a rule or a row. To change structure, see [Changing a flow](#changing-a-flow).

## Operating it at runtime

An operator changes how messages are routed, validated and priced **while the application runs**, from the console (all `/ui/admin/…`, role `admin`) with no deploy:

| Page | What you can do |
|---|---|
| **Providers** (`/admin/providers`) | Create or edit a provider: its channel, state, quality and delivery rate, countries, message types, the tenants and accounts it is assigned to (and whether it is assigned only to them), owner account, sender countries, number prefix pattern, capabilities (unicode, delivery receipts, alphanumeric sender, long messages), retry count and backoff. Set its price per segment per country (`*` for every other country). Pause or disable it. The next message uses the change. |
| **Routing rules** (`/admin/rules`, first tab) | Rules that choose the provider, added one by one, for any account or one account: **platform default**, **country default**, **user default**, **user and country**, **user and recipient**, **content based**, **user content based**, **size based**, or any combination. A rule has conditions (account, tenant, destination countries, number prefix or pattern, text words or pattern, message types, sender id pattern, segments) and an action: **use first** (the others stay as fall-backs), **use only**, or **never use**, for a provider. The page has a navigator (all rules, platform, per account, per country, per provider, switched off), collapsible groups and cards, search, expand/collapse all, a switch on every rule, and a form per rule with a plain-English summary. Per account: the Accounts page links to that account's rules. The rule with the highest priority that matches a provider decides for it; the console suggests a priority from how specific the rule is (an account counts for more than a country, a recipient more than a content word) and you can change it. |
| **Generated rules / Definitions** (`/admin/rules`, other tabs) | *Generated rules* shows, read only, the definition the routing rules produce. *Definitions* opens any decision table (numbering, validation, routing, pricing, delivery, receipts, templates, idempotency) in a code editor (Monaco: BCL highlighting, folding, line markers, loaded from cdn.jsdelivr.net, with a plain text box if it cannot load), **Check** marks the line of a problem, and **Check and save** publishes. A broken edit is refused and changes nothing. A saved edit governs the next message, is kept across restarts (table `rule_overrides`), and **Reset to file** undoes it. **Try a decision** evaluates one decision against sample facts and shows the rule that fired and the trace, without sending anything. |
| **Accounts** (`/admin/accounts`) | Create or edit an account: status, senders, countries, routing objective, daily limit, price cap, rate, plan. Top up (idempotent on the reference), set a sign-in email and password, issue an API key. The **routing objective** is one of `lowest_cost`, `highest_delivery` or `balanced`, or empty to let the rules decide (by message type); it is checked on the way in, because a word the rules do not know is not a preference, it is a silent fall-back to the default ranking. |

**Example routing rules (already there).** A fresh installation starts with eight rules to learn from and copy; the first three are on, the rest are switched off (switch one on to see it work):

| Rule | Does |
|---|---|
| Demo: my test number goes out through the live SMSPasal | For account `demo`, recipient `9856034616` **in any written form** (`+977 9856034616`, `009779856034616`, `977…`, `9856034616`, `09856034616`; the default country is Nepal, `+` or `00` means international), use `smspasal_real` (live). Works on an ordinary request: SMSPasal reports no delivery receipts, which costs a provider points but is not a reason to refuse a message — see *Delivery receipts* below. Needs `config/91_live_account.bcl`; without it the provider does not exist and the rule does nothing. |
| Demo: the login code template uses the sandbox carrier | For `demo`, messages sent with the `otp_login` template use `np_telecom` (a sandbox). |
| Never send promotional messages over the live SMSPasal | For everyone, message type `promotional` never uses `smspasal_real`. |
| ACME: payment receipts over the premium route | (off) tenant `acme` plus the `payment_received` template use `premium_np`. |
| Long messages (3 or more segments) over np_telecom | (off) a size rule. |
| India numbers over in_vendor | (off) a country rule that admits a provider assigned to one account. |
| Demo: invoice texts over np_telecom | (off) words in the text, for one account. |
| OTP texts only over np_telecom | (off) a rule that reserves the message for one route. |

Use **Try a route** at the top of `/admin/rules`: pick an account, type a recipient (any form) and a template or text, and it shows the chain of providers a message would follow, which are sandbox and which LIVE, which rule decided each, and why the others were left out, without sending anything. It is on `/admin` because it names carriers: the same question asked by an account (`POST /v1/route/explain`, and the check on the console's Send page) answers what it asked — the number, the price, whether a receipt would come — and not how the message would be carried.

**The table is the routing; the engine is a copy of it.** `rules/custom_routing.bcl` is a **placeholder** — it holds no rules and exists so the engine has a definition to load at start and for *Reset to file* to return to. The rules live in `routing_rules` and the definition is generated from them (`templates/rules/custom_routing.html`) and published with `rules.save`. `config/46_converge.bcl` re-renders and republishes on a 2s tick, fingerprinted in the `routing_published` table, so:

- a fresh database applies its own rules within a tick of starting, with nobody editing one;
- a replica that came up without the stored source converges too;
- editing a rule from the console still takes effect **in the request**, before the response, and is not left to the tick.

A checked-in copy of generated output would be a second source of truth that silently outranks the table — which is how a rule switched off in the console keeps routing traffic.

**Routing is two halves, in this order: FILTER, then RANK.** `rules.rank` runs the filter over every candidate, and only then scores whatever survived. A provider is therefore either eligible or it is not, and a score can only move a candidate up or down the chain — never out of it.

| | Decides | On what |
|---|---|---|
| **FILTER** (`route_eligibility`) | may this provider carry this message? | identity and state: capability, assignment, ownership, whether an operator's rule named it |
| **RANK** (`route_delivery` / `_cost` / `_balanced`) | which first, and what the fall-backs are | the tier the filter assigned, plus quality, delivery rate, observed failures, cost |

Nothing in the filter compares candidates to each other, so no provider is dropped for scoring badly. If a provider must be excluded, that is a fact about it — it is switched off, its circuit is open, it cannot encode the text, the sender's country does not allow it, or it is assigned to someone else — and the filter says so with a code.

`route_eligibility` reads as three bands, and the band a row is in says who may overrule it:

| Band | Rows | Who may overrule |
|---|---|---|
| 1000–970 | switched off, circuit open, cannot encode the text, sender's country not allowed | **nobody.** A message would be lost or unlawful. |
| 950 | an operator's rule names this provider | — it admits the provider (even one assigned to another account or owned by one) |
| 940–900 | no alphanumeric sender, no long messages, does not carry this type, OTP below quality 85 | **an operator's rule.** These are trades an operator can knowingly make. |
| 800–1 | the tiers: owned, assigned to the account, assigned to the tenant, bought for a number range, serves the country, catch-all | measured by the ranking, which turns the tier into a score |

**A reservation is a filter, not a score.** `mode: only`, and an account that owns its own provider, set `reserves`: the message belongs to that provider, so every other candidate is dropped *whatever it scored*. It is recorded even when the provider turns out to be unusable — the statement was about the message, not about the provider — and an unmet reservation then drops all of them, so the request is refused (`RESERVED_PROVIDER_UNAVAILABLE`, 503) rather than carried by a carrier the operator ruled out. This replaced an `exclusive` priority floor that dropped candidates by comparing priorities; making priority do the work of a filter meant the outcome depended on how the tiers happened to be numbered.

**How routing rules work.** The rules live in the `routing_rules` table. Every change checks the rule (the provider and account exist, the segment range is sane, every pattern is a valid regular expression, because the engine itself does not check patterns when it publishes), stores it, and regenerates `custom_routing`, which is published with `rules.save` (so it is checked by the engine, effective at once, and kept across restarts in `rule_overrides`). It is the FILTER half of routing, evaluated for every candidate provider (`rules.rank` with `eligibility_extra`), so its *allow* both marks the provider as granted (which the bands above then honour) and contributes the tier and priority that the ranking scores — the priority is applied there, not here; a *deny* removes the provider outright.

**"Use only" is kept, not merely preferred.** `mode: only` reserves the message for the provider it names, exactly as an account that owns its own provider does. If that provider cannot take the message — paused, circuit open, does not serve the destination — **no other provider is used**: the request is refused with `RESERVED_PROVIDER_UNAVAILABLE` (503) rather than quietly carried by a carrier the operator ruled out. `use` is the mode that leaves the others as fall-backs.

**The chain is scored, and the first entry is the provider used.** `rules.rank` scores every candidate the filter kept, once each — the tier's priority plus each metric's weight over its normalised range — and orders the chain by score, descending; `sms.submit` dispatches `chain[0]` and `sms.deliver` walks the rest of the chain on failure, in that order. Nothing re-orders the chain after it is built. Tier priorities are 10,000 apart, so the metrics order candidates *within* a tier, and a rule (200,000 + its own) always outranks them; `priced` is a deliberately tiny weight that settles an exact tie towards the provider whose cost is on file. A provider with **no price row is not free**: `price_known` is 0 and it is scored at the dearest price on file, so it cannot win a cost ranking by being missing from the price table.

**What is checked, and how rules are decided.** A rule's recipients are read by the phone library in the default region, so `9856034616`, `+977 9856034616` and `00977 9856034616` are the same rule, and a number that cannot be dialled is refused (no message could match it). A prefix may be written with `+` or `00`. Countries are upper-cased and must be two letters; message types are lower-cased words; content words are matched as plain text (`c++` and `a.b` mean exactly that) and may not contain `|`; a provider id is lowercase letters, digits and underscores, since the rules refer to it by name. When several rules name the same provider for a message, the highest priority wins, then the more specific rule (more conditions, a recipient counting most), then the lower id, so the outcome never depends on insertion order. Providers with equal scores keep the order of their ids. The generated `custom_routing` holds one decision per provider (`for_<id>`), so a message is checked only against the rules of each candidate; 5,000 rules route in about 4 ms. `contrib/messaging/e2e/routing_model_test.go` is a reference model of all of this, checked against the running router on random rule sets and messages, provider for provider, in order. [`examples/rules`](../rules) shows the same routing on its own, with its scenarios as tests.

**What an account is told, and what it is not.** An account sends a message and is told about its message. It is not told how the message is carried, and that is a deliberate line rather than a missing field.

Carriers are not an incidental detail of this gateway: they are its supplier list, its contracts and its failover order. An account that could read which carrier took a message could reconstruct all three, would be coupled to every change of them, and could route around the gateway's own policy. So `provider`, the chain, the per-carrier attempts and `sandbox` are left out of the sender-facing projections — omitted from the SQL rather than blanked afterwards, because a field that is never read cannot leak — and they are all still there for the operator, on `/ui/admin/*` and `/v1/admin/*`.

| | The account (`sender_auth`) | The operator (`admin_auth`) |
|---|---|---|
| Send | `{ id, status, duplicate, to, country, from, type, segments, encoding, price, currency, receipt }` | — |
| One message | the same fields plus `status`, `progress`, `terminal`, `detail`, `failure`, and `created/submitted/delivered_ms` | `message` (with `provider`, `plan`, `err_text`, `total_attempts`), `attempts[]` per carrier, `health[]` — `GET /v1/admin/messages/{id}` |
| A list | `id, to, from, type, segments, price, currency, receipt, status, created/submitted/delivered_ms` | `GET /v1/admin/messages` |
| Dry run | `ok, to, country, type, segments, encoding, price, currency, receipt` — or the reason it cannot be sent | the same, plus `route[]`, `rejected[]`, `objective`, `reserved_for`, `sandbox` — `POST /v1/admin/route/explain` |

The **status** is the account's own vocabulary, and `rules/status.bcl` is where it is defined:

| status | progress | terminal | meaning |
|---|---|---|---|
| `accepted` | 10 | no | we have it, it is waiting its turn |
| `sending` | 50 | no | going out now |
| `sent` | 80 | no | the network has it; a receipt is expected if one was asked for |
| `delivered` | 100 | yes | the handset got it |
| `failed` | 100 | yes | it will not be delivered, and the charge has been released |

`failure` says why in the account's words — `recipient_rejected` (no network can reach that number), `expired`, `no_route`, `not_delivered` — never the carrier's own error string, which is on `/admin`. `progress` and `terminal` are there so a client can draw a bar and know when to stop polling without knowing the words. The dry run's `receipt` answers "will I get a receipt for this" before anything is sent or charged.

**Delivery receipts are not a routing input.** `dlr` defaults to true, so routing never reads it: whether a message wants a receipt is a reporting preference, and letting it choose a carrier would mean two identical messages differing only in that field took different routes. A provider that cannot report one (`supports_dlr` 0, as for every HTTP carrier) is filtered and scored exactly like any other — which is why an SMSPasal route is reachable at all.

The promise is kept after routing instead, by `rules/validate.bcl`: a message that **asked** for a receipt (`dlr: true` in the request, as opposed to taking the default) is refused with `NO_DLR_PROVIDER` when the provider it would go out through cannot report one. That is a refusal, not a reroute — the carrier is not swapped for one that could, because a different carrier is a different route. A request that did not ask is simply sent, and `messages.want_dlr` records what will actually arrive (the request's `dlr` **and** the chosen provider's capability), so the receipts pipeline never waits for a receipt that cannot come. `POST /v1/route/explain` answers it before anything is sent, in `dlr`, `dlr_asked` and `receipt`.

To send only over carriers that can report, ask for one — and give the account a route where the provider that leads can.

**Provider credentials.** Each provider holds any number of accounts with its carrier or vendor (`provider_accounts`, edited under *Credentials* on the provider's page). The fields an account has depend on the channel (`channels.credential_fields`): a system id and password for SMPP, an API token for the JSON vendors, an API key, route id and campaign id for SMSPasal. Settings are stored as given; **secrets are sealed with AES-256-GCM before they are stored, are never returned by the API, and are replaced field by field** (leave a secret blank to keep it). The sealing key is the `key` of the `crypto.unseal` node in `config/30_deliver.bcl` and the `crypto.seal`/`crypto.unseal` nodes in `config/44_admin_manage.bcl`: `env("SMS_CREDENTIAL_KEY", "…dev key…")`. Set `SMS_CREDENTIAL_KEY` for anything real; accounts sealed with another key cannot be opened, so changing it means entering the secrets again. For every send one active account is chosen, the least recently used first, so load spreads over them. If the provider refuses an account's credentials, rules/delivery.bcl rests that account for five minutes and tries the provider's next account at once, without using up an attempt or failing over; with no account left it fails over to the next provider. If every account is resting or switched off, the provider is skipped. A provider with no accounts sends with whatever its channel resource holds. SMPP accounts bind separately (one session per account).

**Configuration is a form, never JSON.** Each channel declares the fields its providers have, and the console builds typed inputs from them: `channels.setting_fields` (provider-wide settings: an endpoint or API URL, a sender id to force) and `channels.credential_fields` (per account: public settings and secrets). Providers of different channels therefore get different forms: SMPP has system id and password; the JSON vendors have an endpoint and an API token; SMSPasal has an API URL, API key, route id and campaign id. A field is `{name, label, kind (text, url, number, bool, select), default, hint, required}`; a credential field also has `secret`. Settings are saved on their own (`PUT /ui/admin/providers/:id/settings`), so editing a provider's routing never changes them. The rule tester on `/admin/rules` is a form too: the facts a rule reads are found in its conditions and listed as inputs.

**Providers and channels.** A *provider* is a routing entity (a row in `providers`). Its *channel* is the technical connection it sends over: an SMPP bind or a vendor API declared in `config/02_resources.bcl`, listed in the `channels` table. Several providers can share a channel (two commercial routes over one bind, different countries and prices), so adding a provider, or an account-specific route, needs no deploy. A new channel (a new bind or vendor API) needs BCL: a resource, a worker in `config/31_workers.bcl`, a send intent and a case in `sms.deliver`, and a row in `channels` with its `credential_fields` and `setting_fields`. Delivery receipts are matched by channel.

**Runtime rules in the engine.** `rules.decide` and `rules.rank` look the definition up on every evaluation, so a published rule applies at once. The actions behind the Rules page are generic REF actions (`rules.catalog`, `rules.source`, `rules.save`, `rules.reset`, `rules.try`, [docs/generic-actions.md](../../docs/generic-actions.md)); the `rules.engine` resource persists edits with `overrides "db"`.

**Upgrading a development database.** Migrations are `CREATE TABLE IF NOT EXISTS`, so a database from an earlier version lacks newer columns. Delete `examples/smsgateway/.data/` after pulling schema changes.

## Auth, session and roles

`config/04_auth.bcl`: a `session.file` resource (signed cookie), `auth.session`, `authz.rbac` and two roles, `sender` and `admin` (which inherits `sender`). `POST /login` checks an argon2id password hash; routes under `/ui` name the roles they need (`authz { roles [...] }`), so an account reading `/ui/admin/stats` gets `403`. Not included: login rate limiting, CSRF tokens (the cookie is `SameSite=Lax`), password reset.

## Bulk campaigns and audiences

A campaign sends one message to many recipients, each with their own fields. It is a REF `process` (durable, on a `store.sql` resource) with an operator's approval in the middle (`config/50_campaigns.bcl`).

**Sources** (any one per campaign):
* **Numbers**: one phone number per line, written any way.
* **CSV**: pasted or uploaded, with a header line (comma, semicolon or tab); the console finds the columns and guesses the phone column (`phone`, `mobile`, `msisdn`, ...), which you can change. The CSV is parsed by the server, so `POST /ui/campaigns` accepts a `csv` string as well as the console does.
* **Rows**: JSON objects (`rows`, with `phone_field` naming the number's key).
* **A saved audience**: a list kept per account on `/audiences`, with its fields, checked once. A campaign copies it when it is created.

**Fields fill the message.** Every column of a row is a placeholder. A message template (`templates/sms/*.html`) uses `${amount}`; plain text uses `{{ amount }}` (click a column chip to insert it). The campaign can also carry default values (`vars`, or the template's placeholder boxes on the form); a row's own column wins over a default, and a placeholder with no value renders empty. The form shows, for each template placeholder, whether it comes from a column or is the same for everyone.

**Phone validation.** Every number is checked with [`github.com/oarkflow/phone`](https://github.com/oarkflow/phone) (libphonenumber's metadata) in the pipeline, with the country you choose for numbers written without a country code (default `NP`): a number that cannot be parsed, is too short or long, or is in no assigned range is **skipped, never sent and never charged**, and listed with the reason (`unparsable`, `too_short`, `too_long`, `not_possible`, `not_valid`, `type_not_allowed`); valid numbers are normalised to international form (`+9779841234567`). **What an invalid number means for the list is decided by a rule**, `rules/campaigns.bcl` (edit it on Admin > Rules > Definitions, and try it with the facts form): either the **whole list is discarded** (nothing stored or sent, a 422 that says why) or the **valid numbers are sent**, the invalid ones are skipped and counted, and the account is **notified**. As shipped, in order: a list with no valid number is discarded; so is one when the submitter asked for `on_invalid: discard`, when the campaign type is `otp` (all or nothing), when the account's plan is `strict`, or when **more than 20 percent** of the numbers are invalid (usually the wrong column or file); a submitter who asks for `on_invalid: skip` is allowed up to 50 percent; otherwise the valid numbers go ahead and the account is notified. The preview shows which way a list would go before anything is stored. The facts a rule reads are `campaign` (total, valid, skipped, invalid_percent, type, source, requested) and `user` (id, tenant, plan).

**Notifications.** The account is told when a campaign is accepted with invalid numbers skipped (how many, the first few numbers), when it has been sent (sent, failed, skipped), and when it is rejected: a bell with an unread count in the header and `/notifications` (`GET /ui/notifications`, `POST /ui/notifications/read` with `ids` or `everything`). To also tell people elsewhere (an e-mail, a webhook, an SMS), extend the intent that writes the row (`campaign.create_from_rows`, `campaign.create_from_audience`, `campaign.run`, `campaign.reject`; see docs/extending-intents.md).

The same check also guards every single send: `rules/validate.bcl` refuses a number that parses but cannot be dialled (`INVALID_PHONE`).

```
source ─► campaign.prepare_rows  (parse, normalise, phone.validate: pending or skipped)
POST /ui/campaigns/preview ─► counts, skipped rows with reasons, the first message (nothing stored)
POST /ui/campaigns ─► campaign.create ─ stores the campaign and its rows, starts the run
   run: approve (human task, role admin; the requester cannot approve their own) ─┬─ approve ─► campaign.run
                                                                                  └─ reject  ─► campaign.reject
campaign.run: for each PENDING row, sms.accept as the campaign's account, reference "<campaign>:<n>"
```

The approval task says how many rows are valid. Rows are sent as the campaign's account, through the normal pipeline (validation, routing rules, price, payment hold, queue), with the reference `<campaign>:<n>`, so a re-run never sends or charges twice. A refusal (no funds, opt-out, no provider can carry it) is recorded against the row with its reason and does not stop the rest. The campaign's page lists every row with its state (`pending`, `sent`, `failed`, `skipped`) and downloads a CSV report with the row's fields. Limits: 2000 rows per campaign or audience.

## The flows

```
POST /v1/messages ─► sms.submit
   sms.prepare  ─ account ─ number ─ content ─ message ─ valid ─ candidates ─ objective ─ route ─ rate ─ charge ─ limits ─ prepared
   reference_ok ─ one transaction: reserve funds (hold), insert message, write ledger ─ publish job to sms.dispatch.<provider>

sms.dispatch.<provider> ─► sms.deliver
   claim ─ job ─ gate ─ send.<provider> ─ class ─ decision ─ delay ─ settlement ─ settle.accept | retry | failover | fail | skip

sms.dlr ─► sms.receipt                      (from an SMPP bind, or POST /v1/webhooks/dlr/:provider)
   message ─ verdict ─ action ─ settlement ─ receipt.deliver | receipt.fail | receipt.ignore

every 30s ─► sms.recover ─ re-queue messages no job is working on
```

**Payment.** Funds are *held* when the message is accepted, in the same transaction that stores it. They are *captured* when a provider accepts the message and *released* if it can never be sent (or refunded if the handset later reports failure). A message is paid for at most once because every state change is a compare-and-set on the message's `dispatch_seq` and `state`; a duplicate or late job changes nothing.

**Delivery.** Each message carries its route (`plan`) and a `dispatch_seq`. A job names the sequence it was made for. A worker claims the message by `UPDATE … WHERE dispatch_seq = $n AND state = 'queued'`; whoever loses does nothing. If the process dies after storing a message but before queueing it, or while sending, `sms.recover` queues a fresh job with a higher sequence and the old job becomes harmless.

**Queues and consumers.** One queue and one consumer pool per provider (`config/31_workers.bcl`) so a slow provider cannot starve the others; `sms.dlr` has its own consumers.

## Changing a flow

Nodes are wired by facts (`requires` / `provides`), not by order. A new file in `config/` can add, replace or remove nodes of any intent with an `extend` block, without editing the files that define it. The full reference is [docs/extending-intents.md](../../docs/extending-intents.md).

### Recipes

**Add a validation rule.** Write the rule in `rules/`, then add one file:

```bcl
# config/90_blocklist.bcl
extend "blocklist" {
  intent "sms.prepare"
  node "blocklist" {
    uses "rules.decide" resource "policy" kind read requires [message] provides [blocklist]
    config { definition "blocklist" decision "blocklist" fail_on ["deny"] }
  }
  feed ["prepared"]      # nothing is accepted until the rule has passed
}
```

If the rule's table denies, the send fails with the row's `code`, `status` and `reason`. (`contrib/messaging/e2e/extend_test.go` does exactly this.)

**Add logging or metrics.** Same pattern with `trace.span`, `metric.emit` or `audit.record`; they observe and never fail the flow.

**Notify when a message fails or is delivered.** `extend` `settle.fail`, `settle.accept`, `receipt.fail` or `receipt.deliver` with a `notify.send` node and `feed ["settled"]` / `["applied"]`.

**Remove a step.** `extend "x" { intent "sms.prepare"  remove ["limits"] }`.

**Replace a step.** Give the extension a node of the same name and `replace ["name"]`.

**Add a provider.** (1) a `resource` in `config/02_resources.bcl`; (2) rows in `providers` and `provider_costs`; (3) a `worker` in `config/31_workers.bcl`; (4) a case in the `sent_result` switch of `sms.deliver` and a `send.<provider>` intent in `config/32_send.bcl`. Routing needs no change: the rules read the table. Items 3 and 4 can be an `extend` file too.

**Change routing.** Edit `rules/routing.bcl`: the `route_eligibility` table says which providers may carry a message and in which tier; the `route_objective` table chooses what the ranking optimises (delivery for an OTP, cost for promotions, a balance otherwise); the rankings score the survivors.

**Add a template.** Add `templates/sms/<name>.html` and a row in `rules/templates.bcl` (the file, type and sender a send uses). To show it in the console picker, add a `message_templates` row (`config/01_database.bcl`, or SQL on a running system): its label, description and `fields`, a JSON list of `{name,label,default,hint}`, one per `${placeholder}`. The console renders one input per field and previews the result with `POST /ui/templates/preview`, which uses the same rules decision as a real send.

### Extension points

Every intent can be extended. These are the facts to hook onto.

| Intent | Facts you can require | Good places to `feed` |
|---|---|---|
| `sms.prepare` | `input`, `principal`, `user`, `number`, `content`, `analysis`, `message` (the facts every rule reads), `candidates`, `route`, `charge` | `valid` (add a check), `route` (change candidates), `prepared` (last gate) |
| `sms.submit` | `prepared`, `existing`, `row` | `accepted` (before the money moves), `result` |
| `sms.deliver` | `job`, `gate`, `outbound`, `sent`, `class`, `decision`, `settlement` | `gate`, `settled` |
| `settle.accept` / `.fail` / `.retry` / `.failover` | `input` (the settlement: id, user_id, provider, code, error_text, attempt, delay_s, …), `applied` | `settled` |
| `sms.receipt` | `receipt`, `message`, `verdict`, `action`, `settlement` | `action`, `result` |
| `sms.recover` | `orphans` | `swept` |

## SMSPasal (live provider)

**A live provider is seeded for testing credentials.** `config/91_live_account.bcl` (git-ignored, because it holds a real API key) creates the provider `smspasal_real` (LIVE, API URL `https://sms.smspasal.com/smsapi/index.php`, sender id `SMSPASAL`), its account `primary` (your key, route 10259, campaign 9801), and the user `live` (`live@example.com` / `live-pass-123`, balance 1.000). The provider is assigned only to `live`, so no other account ever sends through it. The key is sealed with the development key that is in the repository, so treat the file as plain text: delete it, or remove the provider and account on `/admin/providers`, when you are done.

**Test credentials.** On a provider's page each account has a **test** link: enter a number and a text, and one message is sent through that account only, exactly as a real send would (the provider's settings, the account's credentials, the channel's send), but with no routing, payment or message record, and without touching the account's counters. The answer is the provider's own: accepted with its message id and whether it was a sandbox or a live send, or its refusal in its own words (for SMSPasal, for example, `ERR: INVALID API KEY`). Use it on `smspasal_real` with your own number to check the live key; the first message it sends is a real SMS. Every channel can be tested, SMPP binds included (`POST /ui/admin/providers/:id/accounts/:name/test`).

**The other providers are sandboxes.** Every channel points at the sandbox that starts with the application, so a message from `demo` goes to the stand-in SMPP carrier, is marked `delivered`, and never leaves this machine. The console says so: Explain and Send show "SANDBOX route", the Messages table shows `np_telecom (sandbox)`, and the Providers table has a Mode column (sandbox or LIVE).

To send through SMSPasal for real, on `/admin/providers` (as the operator), open **smspasal**:

1. **Settings**: set **API URL** to `https://sms.smspasal.com/smsapi/index.php`, and a **Sender id** your SMSPasal account has approved. Save settings.
2. **Credentials**: edit the `primary` account: **API key**, **Route id** and **Campaign id** from your SMSPasal account. Leave the key blank to keep the stored one.
3. **Sandbox**: untick it in the provider form (it is a label on the provider; it does not change where the provider sends).
4. **Who may use it**: in the provider form, put the account (for example `demo`) in *Accounts* and tick *Only for the tenants and accounts above*, or clear that box and give the provider a country so every account on that route uses it. Untick *Delivery receipts* (SMSPasal reports none here), save, then use Explain on the Send page as that account: smspasal should be first and Mode should say live. Send with `dlr` off.

No configuration file changes. (The only thing in BCL is the host list: `config/12_smspasal.bcl` allows calls to `sms.smspasal.com` and `127.0.0.1` and nothing else.)

The API answers HTTP 200 either way: `SMS-SHOOT-ID/<id>` is success, `ERR: …` is read by `rules/delivery.bcl` (invalid number: fail; key, sender, route or balance problem: rest the account and fail over). A message is marked delivered when SMSPasal accepts it; delivery reports from its DLR API are not wired in.

## Console and static files

`templates/layouts`, `templates/components`, `templates/pages` are SPL; `static/` is served at `/static` (`config/09_console.bcl`). SPL pitfalls: no apostrophes, quotes or `@directive` text inside an HTML comment; use a comparison, not a bare identifier, in `@if`.

## Tests

`contrib/messaging/e2e` boots this directory with an in-process SMSC and vendor API and covers: SMPP delivery and payment, exactly-once payment under repeated references and under a burst, insufficient funds, vendor provider with webhook receipts, failover when a provider is down, retry on the same provider, permanent failure releasing funds, routing by country and assignment, OTP quality floor, account isolation, restart durability, SPL templates and static files, and the `extend` mechanism.

```sh
cd contrib/messaging && go test ./...
```

## Limits

* One embedded broker node; scaling out means a networked broker resource, which REF's `queue` interface allows but this example does not ship.
* Tested on SQLite. The SQL is plain with `$n` placeholders, and uses `ON CONFLICT`, which PostgreSQL and SQLite share. MySQL needs the upserts rewritten.
* Delays use relative `delay_seconds`, not absolute times.
* The provider catalog is a table seeded on first start; manage it with SQL or add admin intents.
* A rate limit per account (`rate_per_second`) is stored and exposed to rules, but not enforced by a token bucket.
