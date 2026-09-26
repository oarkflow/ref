# Money, the Nepali calendar and data residency

This page covers three things:
- exact currency arithmetic, from a built-in ISO 4217 registry;
- the Bikram Sambat (BS) calendar and fiscal years, Nepali and Gregorian;
- data residency: where a tenant's data may live, and where it may be sent.

The pure logic lives in its own packages, which Go code can use directly:
- `money`: the registry, amounts, rounding, conversion and allocation;
- `calendar/bs`: AD↔BS conversion, Nepal's fiscal year, and formatting;
- `calendar/fiscal`: Gregorian fiscal years.

The platform exposes them through BCL blocks, actions and expression functions.

## Money

### Currencies

Every generation starts from a built-in ISO 4217 registry. Each entry has a code, minor units and a symbol. It includes NPR, INR, USD, EUR, GBP, CNY, JPY (0 decimals), KWD, BHD and OMR (3 decimals), and about fifty more. NPR, INR, PKR, BDT and BTN group digits the South Asian way (`12,34,567.00`).

A document can add currencies, or change how a built-in one is written:

```bcl
currency "PTS" {
  minor_units 0          # required for a new code
  symbol "pts"
  name "Loyalty points"
  grouping "thousands"   # thousands (default), indian or none
}

currency "NPR" { symbol "रू" }   # re-symbol a built-in currency
```

A built-in currency's minor units can never change: every amount already stored in it would silently change value. Declaring `currency "JPY" { minor_units 2 }` stops the deployment.

Currencies that are not built in are also published to the process-wide registry, so the expression functions below can see them. Overrides of a built-in currency, such as NPR's symbol above, apply only to that generation's `money.*` actions and entity columns. That way, one application cannot change how another formats rupees.

### Arithmetic rules

- An amount is an `int64` count of minor units: paisa, cents or fils. Floating point is never used for arithmetic.
- Amounts come in as decimal strings or JSON numbers. A JSON number is read by its shortest decimal form, so `0.1` is exactly one tenth.
- Amounts go out as decimal strings with the currency's exact number of places: `"1250.50"`, `"1500"`, `"0.125"`.
- An amount with more decimal places than its currency allows is an error. It is never silently rounded. Use `money_round` when you do want rounding.
- Adding or subtracting mixed currencies is an error, and so is overflow.

Rounding modes:

| Mode | Behaviour |
|---|---|
| `half_even` (default) | Ties go to the even neighbour (banker's rounding), so rounding does not drift over many operations. |
| `half_up` | Ties go away from zero. |
| `half_down` | Ties go toward zero. |
| `down` | Truncates toward zero. |
| `up` | Rounds away from zero. |
| `floor` | Rounds toward negative infinity. |
| `ceiling` | Rounds toward positive infinity. |

### Allocation

Allocation splits a total so that the parts always sum exactly to the whole:
1. Each part gets the floor of its exact share.
2. The minor units left over go one each to the parts with the largest fractional remainders (the largest-remainder, or Hamilton, method). Ties go to the earlier part.

Ratios can be weights (`[1, 2, 3]`), percentages (`[50, 30, 20]`) or fractions (`["0.5", "0.25", "0.25"]`). A negative total, such as a refund, is split as its absolute value and then negated.

With a minimum unit, every part is a multiple of the unit, for example whole rupees or 0.05 cash steps. The part with the largest ratio also takes whatever is smaller than one unit.

```
money_allocate("NPR", "100", [1, 1, 1])          → ["33.34", "33.33", "33.33"]
money_allocate("NPR", "1000.03", [1, 1, 1], "1") → ["334.03", "333.00", "333.00"]
money_split("NPR", "10", 3, "0.05")              → ["3.35", "3.35", "3.30"]
```

### Money columns

In an entity, a decimal column can declare a currency, and its scale comes from the registry. The column kind defaults to `decimal`. A `scale` that disagrees with the currency is a compile error.

```bcl
entity "invoice" {
  database "db"
  column "amount" { currency "NPR"  min 0 }   # 2 places
  column "fare"   { currency "JPY" }          # 0 places: "1500.5" is a 422
  column "duty"   { currency "KWD" }          # 3 places
}
```

### Actions

```bcl
node "shares" {
  uses "money.allocate"
  requires [input]
  provides [shares]
  config {
    currency "NPR"
    amount "input.total"        # an expression
    ratios "input.weights"      # an expression or a literal list; or: parts 3
    unit "1"                    # optional minimum unit, in major units
  }
}

node "npr" {
  uses "money.convert"
  requires [input]
  provides [npr]
  config {
    from "USD"
    to "NPR"
    amount "input.amount"
    rate "input.rate"           # units of `to` per unit of `from`; "1/133.25" also works
    rounding "half_even"
  }
}
```

`from "USD" to "NPR"` may share a line (BCL v0.0.34 and later).

### Expression functions

The currency always comes first.

| Function | Result |
|---|---|
| `money_add("NPR", a, b, ...)` | a + b + …, e.g. `"100.30"` |
| `money_sub("NPR", a, b, ...)` | a − b − … |
| `money_sum("NPR", list)` | the sum of a list |
| `money_cmp("NPR", a, b)` | -1, 0 or 1 |
| `money_parse("INR", "₹ 1,23,456.5")` | `"123456.50"` (symbol, code and grouping are ignored) |
| `money_round("NPR", "2.345", "half_up")` | `"2.35"` |
| `money_format("NPR", a)` | `"Rs 12,34,567.50"`; a third argument of `"code"` gives `12,34,567.00 NPR` and `"plain"` gives the number only |
| `money_convert("USD", a, "NPR", rate, mode)` | the converted amount |
| `money_allocate("NPR", a, ratios[, unit])` | a list of parts |
| `money_split("NPR", a, n[, unit])` | n parts, as equal as possible |
| `money_minor("KWD", "1.5")` | `1500` |
| `money_from_minor("NPR", 12345)` | `"123.45"` |
| `currency_minor_units("JPY")` | `0` |
| `currency_symbol("GBP")` | `"£"` |

## Bikram Sambat and fiscal years

### Conversion and data

Bikram Sambat months are solar and 29 to 32 days long. Their lengths are published each year by the Nepal Panchang Nirnayak Samiti, so conversion is a table lookup. `calendar/bs` embeds the standard month-length table:
- it covers **BS 2000–2100**, which is AD 1943-04-14 to 2044-04-12;
- a date outside that range is an error, never a guess.

The table was assembled and verified as follows:
- **BS 2000–2090:** the reading shared by nepali-date-converter, bikram-sambat, nepali_datetime (PyPI) and ad-bs-converter. Where they disagree, the majority wins. All five sources agree on BS 2000–2083. The only exception is one sub-year difference in a single source for 2062, which the majority overrules.
- **BS 2091–2100:** only nepali_datetime's `calendar_bs.csv` goes this far, and the table follows it. This includes BS 2096, which that table gives as 364 days.
- **Tests:**
  - 1 Baisakh of every year from 2000 to 2100 is checked against dates produced by other libraries' own converters.
  - 1 Shrawan, the first day of the fiscal year, is checked for BS 2070–2084.
  - Anchors include 2000-01-01 BS = 1943-04-14 AD, 2081-01-01 BS = 2024-04-13 AD and 2082-04-01 BS = 2025-07-17 AD.
  - A day-by-day walk of the whole range checks that dates are contiguous, that round-trips are exact, and that weekdays agree.

**Years after the current one are projections.** The official panchang can still correct a month length when it is published. The table is data (`calendar/bs/data.go`): update it and re-run the tests.

### Nepal's fiscal year

Nepal's fiscal year starts on 1 Shrawan (month 4) and ends on the last day of Asar in the next BS year. It is written `2081/82`. The fiscal quarters are:

| Quarter | Months |
|---|---|
| Q1 | Shrawan–Ashwin |
| Q2 | Kartik–Poush |
| Q3 | Magh–Chaitra |
| Q4 | Baisakh–Asar |

### Date arguments

A date argument is a **Gregorian** date or instant:
- a string such as `"2024-04-13"`, or an RFC 3339 time such as the `now` variable;
- a Go `time.Time`;
- a Unix timestamp.

A bare date is read as that calendar day. An instant is read in Nepal time (UTC+05:45) by the BS functions, and in its own offset by the Gregorian ones.

BS 2000–2044 and AD 2000–2044 look alike, so the functions never guess which calendar a date is in. To use a BS date, convert it with `bs_to_ad` first.

### Calendar expression functions

| Function | Result |
|---|---|
| `bs_date(x)` | `"2081-04-01"` |
| `bs_date(x, layout)`, `bs_format(x, layout)` | formatted with ASCII digits |
| `bs_format_np(x, layout)` | formatted with Devanagari digits: `"१ साउन २०८१"` |
| `bs_year(x)`, `bs_month(x)`, `bs_day(x)` | integers |
| `bs_month_name(m)`, `bs_month_name_np(m)` | `"Shrawan"`, `"साउन"` |
| `bs_to_ad("2081-04-01")` | `"2024-07-16"` (ASCII or Devanagari digits) |
| `bs_days_in_month(2081, 1)` | `31` |
| `devanagari_digits(s)`, `ascii_digits(s)` | digit conversion |
| `fiscal_year(x)` | `"2081/82"` |
| `fiscal_quarter(x)` | 1–4 |
| `fiscal_year_start(x)`, `fiscal_year_end(x)` | AD dates. `x` can also be a label such as `"2081/82"`. |
| `gregorian_fiscal_year(x, 4)` | `"2024/25"`; `(x, 1)` gives `"2024"` and `(x, 10, "end")` gives `"FY2025"` |
| `gregorian_fiscal_quarter(x, m)` | 1–4 |
| `gregorian_fiscal_year_start(x, m)`, `gregorian_fiscal_year_end(x, m)` | `"2024-04-01"`, `"2025-03-31"` |

### Layout tokens

| Token | Meaning | Example |
|---|---|---|
| `YYYY` | year | 2081 |
| `YY` | two-digit year | 81 |
| `MMMM` | month name | Baisakh |
| `NNNN` | Nepali month name | वैशाख |
| `MM` | zero-padded month | 01 |
| `M` | month | 1 |
| `DD` | zero-padded day | 05 |
| `D` | day | 5 |
| `dddd` | weekday | Saturday |
| `WWWW` | Nepali weekday | शनिबार |

Any other character is copied as it is.

The romanised month names are Baisakh, Jestha, Asar, Shrawan, Bhadra, Ashwin, Kartik, Mangsir, Poush, Magh, Falgun and Chaitra.

For example, this node labels a record with its fiscal year:

```bcl
node "fy" {
  uses "expression"
  requires [input]
  provides [fy]
  config { expression "fiscal_year(input.posted_on)" }
}
```

## Data residency

### Regions and the residency block

A resource declares the region where its data lives. For an outbound service, that is where the data is sent. A resource without a region inherits one from the database (`config.database`) or store (`config.store`) it names.

```bcl
resource "db_eu" {
  kind "database.sql"
  region "eu-west-1"
  config { ... }
}

resource "cases" {
  kind "pipeline.cases"
  config { database "db_eu" }   # inherits eu-west-1
}

resource "crm_eu" {
  kind "service.http"
  region "eu-west-1"
  config { ... }
}

tenant "acme" { region "eu" }   # a zone name or a region

residency {
  claim "data_region"            # a principal claim naming the tenant's home, if the tenant block has none
  audit "db_eu"                  # a hash-chained audit table (default platform_audit) for refusals
  require_region false           # true: a bound tenant may not write to a resource with no region

  zone "eu" {
    regions ["eu-west-1", "eu-central-1"]
    hosts   ["*.eu.example.com"]  # outbound hosts that tenants homed here may reach
  }
  zone "us" { regions ["us-*"] }  # regions accept globs

  policy "customer-pii" {
    regions   ["eu"]              # zone names or region globs
    entities  ["customer"]
    pipelines ["kyc"]
    resources ["cases"]
    intents   ["crm.sync"]
    hosts     ["crm.eu.example.com"]
  }
}
```

### Compile-time checks

These checks run in both `Compile` and `Validate`. Each one stops the deployment:
- A covered entity's database, a covered pipeline's `pipeline.cases` resources, a covered resource, or a data-holding resource used by a covered intent is outside the policy's regions.
- Any of those declares no region, so its residency cannot be proven.
- A covered intent calls a `service.http` resource whose `allowed_hosts` include a host that the policy's `hosts` do not match.
- A tenant declares a `region`, but the document has no `residency` block to enforce it.

### Run-time checks

The tenant's home is the tenant block's `region`. If that is not set, it is the `claim` from the principal. The tenant block always wins, so a token cannot move a tenant out of its region.

For a tenant with a home:
- **Writes:** a write (an `effect` or `async_effect` node) to a resource in another region is refused. This includes an entity's create, update and delete.
- **Outbound calls:** any call to an outbound `service.*` resource in another region is refused.
- **Hosts:** the HTTP client checks the final host of every request, and of every redirect, against the zone's `hosts` and the covered intent's policy `hosts`. A refused call is never sent.

Reads are not restricted. A tenant with no home region is not constrained.

A refusal is `403 PERMISSION_DENIED` with a message such as `data residency: tenant region "eu" may not write to "db_us" in region "us-east-1"`. It also:
- records a deny decision in the plan;
- logs a structured `WARN` line (`audit=true`);
- appends a `residency.denied` entry to the audit table, when `audit` is set.

### Limitations

- A resource's region is what the document says. The platform does not probe where a database actually runs.
- Regions are checked per node. An action that writes somewhere other than its own resource is not seen. For example, a durable entity hook delivered later by the background dispatcher runs without the request's tenant.
