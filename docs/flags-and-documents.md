# Feature flags and PDF documents

## Feature flags

```bcl
flag_store "kv"                      # optional: a cache resource holding run-time overrides

flag "new_checkout" {
  description "Redesigned checkout"
  default false
  rule "staff" { roles ["staff"]  value true }
  rule "pilot" { tenants ["acme"]  rollout 25 }          # 25% of acme's users, sticky per user
  rule "big"   { condition "input.amount != nil and input.amount > 1000" }
}

flag "pricing" {                     # an experiment
  default "control"
  variant "control"      { weight 50 }
  variant "annual_first" { weight 50 }
}
```

### Evaluation

Rules are tried in order, and the first match decides. Within a rule, every criterion that is set must match:
- `roles`, `users`, `tenants`, `environments` (the document's `environment`);
- `condition`, an expression over the caller's facts.

`rollout` then admits that percentage of the matching callers. The choice is stable per user, or per tenant when the caller is anonymous, so a user doesn't flip between variants.

When no rule matches, weighted `variant`s split callers the same stable way; otherwise the flag has its `default`. `disabled true` forces the default for everyone.

### Where flags are used

- **In expressions:** `flags.<name>` is available everywhere, for example `flags.new_checkout ? 'new' : 'old'`.
- **In intents:**
  - `flag.evaluate` evaluates one flag. `require true` makes it fail with 404 while the flag is off; `explain true` also returns the reason, rule and variant.
  - `flag.all` returns every flag for the caller, so a client can configure itself.
- **On routes:** `route "beta" { … flag "new_checkout" }` answers 404 while the flag is off for that caller.
- **At run time:** `flag.set` overrides a flag for everyone. It takes `{flag, value}` to force a value, `{flag, off: true}` as a kill switch, or `{flag, clear: true}` to remove the override, with an optional `ttl` and `reason`, and is restricted by `roles`. With `flag_store`, overrides live in that cache resource and every replica reloads them every few seconds.

> **Expression caveat (bcl v0.0.32):** `>`, `>=`, `<` and `<=` treat a missing value as greater than any number. For example, `nil > 1000` is true. Guard optional values with `x != nil and x > 1000`.

## PDF documents

The dependency-free `document` package renders well-formed PDF 1.4:
- headings, wrapped paragraphs, label/value fields, tables and boxed notices;
- automatic page breaks, with a table's header repeated on each page;
- a footer with "Page n of N" on every page.

Text uses the standard Helvetica fonts, which cover Latin-1 only. Any other script, such as Devanagari, prints as `?` and would need an embedded font.

### `document.pdf`

`document.pdf` renders a layout whose text is `{{…}}` templates over the node's facts:

```bcl
node "pdf" {
  uses "document.pdf"
  requires [invoice]
  provides [pdf]
  config {
    title "Invoice {{invoice.number}}"
    subtitle "For {{invoice.customer}}"
    footer "Acme Ltd - VAT 123456"
    filename "invoice-{{invoice.number}}"
    blocks [
      { kind "fields", fields [ { label "Customer", value "{{invoice.customer}}" } ] },
      { kind "table", rows "invoice.lines", columns [ { label "Item", value "{{row.name}}" }, { label "Amount", value "{{row.amount}}" } ] },
      { kind "notice", text "Pay within 30 days" }
    ]
  }
}
```

By default the node publishes a download, which the route sends as `application/pdf` with a filename. With `output "base64"`, it publishes `{filename, content_type, content_base64, size, sha256}` instead, for storing with `storage.put` or attaching to an email.

### Certificate PDFs

`pipeline.certificate_pdf` renders an issued pipeline certificate as a PDF. It shows:
- the subject fields, labelled from the pipeline's inputs;
- the certificate number, case, issue date and expiry;
- the verification code, with an optional `verify_url`;
- the content fingerprint.

A certificate that no longer verifies is printed as **NOT VALID**, with the reason. The Passport example serves it at `GET /api/passport/cases/:id/certificates/:number/pdf`.

## Exact decimals

An entity column of `kind decimal` (optionally with `scale`, default 2) is exact:
- **Storage:** integer minor units.
- **Output:** strings such as `"1250.50"`.
- **Input:** a string or a number. More decimal places than the scale is a `422`, never silent rounding.
- **Filters and aggregates:** use the exact values; `sum`, `min` and `max` return exact strings. For example, `0.10 + 0.20` sums to exactly `"0.30"`.
