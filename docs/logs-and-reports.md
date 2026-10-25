# Logs and reports

## Structured logs

`log.write` records an entry in a log resource:

```bcl
resource "log_recent" { kind "log.memory" config { capacity 5000 } }
resource "log_file"   { kind "log.file"   config { path ".data/app.log" max_bytes 16777216 } }
resource "logs"       { kind "log.tee"    config { sinks ["log_recent", "log_file"] } }

node "audit" {
  uses "log.write" resource "logs" kind effect requires [input] provides [logged]
  config { level "info" message "order placed" fields { order "{{ input.id }}" card "{{ input.card }}" } redact ["card"] }
}
```

| Sink | Notes |
|---|---|
| `log.memory` | ring buffer; queryable; lost on restart |
| `log.file` | JSON lines; rotated once at `max_bytes` (`path.1` keeps one previous generation) |
| `log.sql` | a table (`database`, `table`); shared by every replica |
| `log.tee` | writes to several, queries the first that can be queried |

Each entry carries `request_id`, `intent`, `principal` and `tenant` from the invocation, so one request's entries can be found together. Writing never fails the request (a broken sink makes the node publish `false`); `redact` replaces named fields before any sink sees them. `log.query` searches newest first by `min_level_fact`, `contains_fact`, `request_id_fact`, `since_fact`, and is limited to the caller's tenant unless `this_tenant false`: put it behind an admin-only route.

A host adds another sink (Loki, Elasticsearch, a vendor API) by registering a resource whose value implements `platform.LogSink`:
`Write(ctx, entry)` and `Query(ctx, query)` (return `platform.ErrLogQueryUnsupported` if write-only).

A log node that nothing depends on did not run in practice (the example's first version lost its entries that way), so make the node whose work you are recording `require` the log fact (`requires [..., logged]`), as `chat.send` does in the example.

## Reports

A report is three nodes: rows, then a file, then what to do with it.

```bcl
node "rows" { uses "database.query" resource "db" kind read provides [rows]
              config { statement "SELECT region, SUM(total) AS total FROM orders WHERE day >= $1 GROUP BY region" args [input.since] } }
node "file" { uses "report.export" requires [rows] provides [file]
              config { rows_fact "rows" format "xlsx" columns ["region:Region", "total:Revenue"] title "Revenue" filename "revenue" } }
```

`report.export` renders `csv`, `json`, `html` or `xlsx` (`format_fact` lets the caller choose). With `output "download"` (the default) the route sends the file; with `output "base64"` it publishes `{filename, content_type, content_base64, size, rows}` for `storage.put` (`encoding base64`), `notify.send` or a response.

- Rows may come from any node, including typed results such as `chat.history`; columns are `key` or `key:Heading`.
- **Formula injection:** CSV cells a spreadsheet would run (`=`, `@`, `+x`, `-x`) get a leading apostrophe; real numbers, including negatives, are left alone. XLSX text is stored as inert text. HTML is escaped.
- `max_rows` (default 100 000) bounds memory; an xlsx sheet holds 1 048 575 rows, beyond which export csv.
- The filename is sanitized, so a template or fact cannot escape the download name.

**Scheduled reports:** a `schedule` block starts an intent on a cron or interval; that intent runs the report and stores or sends it:

```bcl
schedule "weekly-revenue" { cron "0 6 * * 1" timezone "UTC" queue "jobs" intent "report.revenue" }
```
