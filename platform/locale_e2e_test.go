package platform

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/oarkflow/ref/money"
)

const localeApp = `
name "locale"

currency "PTS" {
  minor_units 0
  symbol "pts"
  name "Loyalty points"
}

resource "db" {
  kind "database.sql"
  config {
    driver "sqlite"
    dsn env.required("LOCALE_DSN")
  }
}

entity "invoice" {
  database "db"
  allow_anonymous true
  column "customer" { kind text  required true }
  column "amount"   { currency "NPR"  min 0 }
  column "fare"     { currency "JPY" }
  column "duty"     { currency "KWD" }
  column "points"   { currency "PTS" }
}

intent "calendar.info" {
  response "info"
  node "bs"          { uses "expression"  requires [input]  provides [bs]          config { expression "bs_date(input.date)" } }
  node "long"        { uses "expression"  requires [input]  provides [long]        config { expression "bs_date(input.date, 'dddd, D MMMM YYYY')" } }
  node "nepali"      { uses "expression"  requires [input]  provides [nepali]      config { expression "bs_format_np(input.date, 'D NNNN YYYY')" } }
  node "fy"          { uses "expression"  requires [input]  provides [fy]          config { expression "fiscal_year(input.date)" } }
  node "quarter"     { uses "expression"  requires [input]  provides [quarter]     config { expression "fiscal_quarter(input.date)" } }
  node "fy_start"    { uses "expression"  requires [input]  provides [fy_start]    config { expression "fiscal_year_start(input.date)" } }
  node "fy_end"      { uses "expression"  requires [input]  provides [fy_end]      config { expression "fiscal_year_end(input.date)" } }
  node "ad"          { uses "expression"  requires [input]  provides [ad]          config { expression "bs_to_ad(input.bs)" } }
  node "india_fy"    { uses "expression"  requires [input]  provides [india_fy]    config { expression "gregorian_fiscal_year(input.date, 4)" } }
  node "us_fy"       { uses "expression"  requires [input]  provides [us_fy]       config { expression "gregorian_fiscal_year(input.date, 10, 'end')" } }
  node "us_fy_start" { uses "expression"  requires [input]  provides [us_fy_start] config { expression "gregorian_fiscal_year_start(input.date, 10)" } }
  node "info" {
    uses "collect"
    requires [bs, long, nepali, fy, quarter, fy_start, fy_end, ad, india_fy, us_fy, us_fy_start]
    provides [info]
  }
}

intent "money.math" {
  response "out"
  node "sum"       { uses "expression"  requires [input]  provides [sum]       config { expression "money_add('NPR', input.a, input.b, input.c)" } }
  node "diff"      { uses "expression"  requires [input]  provides [diff]      config { expression "money_sub('NPR', input.a, input.b)" } }
  node "formatted" { uses "expression"  requires [input]  provides [formatted] config { expression "money_format('NPR', input.big)" } }
  node "shares"    { uses "expression"  requires [input]  provides [shares]    config { expression "money_allocate('NPR', input.a, [50, 30, 20])" } }
  node "points"    { uses "expression"  requires [input]  provides [points]    config { expression "money_split('PTS', 100, 3)" } }
  node "usd"       { uses "expression"  requires [input]  provides [usd]       config { expression "money_convert('NPR', input.big, 'USD', '1/133.25', 'down')" } }
  node "split" {
    uses "money.allocate"
    requires [input]
    provides [split]
    config {
      currency "NPR"
      amount "input.bill"
      ratios "input.weights"
      unit "1"
    }
  }
  node "yen" {
    uses "money.convert"
    requires [input]
    provides [yen]
    config {
      from "USD"
      to "JPY"
      amount "input.usd"
      rate "input.rate"
      rounding "half_up"
    }
  }
  node "out" {
    uses "collect"
    requires [sum, diff, formatted, shares, points, usd, split, yen]
    provides [out]
  }
}

route "calendar" {
  method POST
  path "/calendar"
  intent "calendar.info"
}

route "money" {
  method POST
  path "/money"
  intent "money.math"
}
`

func writeApp(t *testing.T, src string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "app.bcl")
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLocaleEndToEnd(t *testing.T) {
	h := newAppHarness(t, writeApp(t, localeApp), map[string]string{
		"LOCALE_DSN": "file:" + filepath.Join(t.TempDir(), "l.db"),
	})

	t.Run("calendar", func(t *testing.T) {
		status, body := h.call("POST", "/calendar", "", map[string]any{"date": "2024-07-16", "bs": "2082-01-01"})
		if status != 200 {
			t.Fatalf("calendar: %d %v", status, body)
		}
		want := map[string]any{
			"bs": "2081-04-01", "long": "Tuesday, 1 Shrawan 2081", "nepali": "१ साउन २०८१",
			"fy": "2081/82", "quarter": float64(1), "fy_start": "2024-07-16", "fy_end": "2025-07-16",
			"ad": "2025-04-14", "india_fy": "2024/25", "us_fy": "FY2024", "us_fy_start": "2023-10-01",
		}
		for k, v := range want {
			if dig(body, k) != v {
				t.Fatalf("%s = %#v, want %#v (%v)", k, dig(body, k), v, body)
			}
		}
		// An instant is read in Nepal time: 20:00 UTC on 15 July is already 1 Shrawan.
		_, body = h.call("POST", "/calendar", "", map[string]any{"date": "2024-07-15T20:00:00Z", "bs": "2081-04-01"})
		if dig(body, "bs") != "2081-04-01" || dig(body, "fy") != "2081/82" || dig(body, "ad") != "2024-07-16" {
			t.Fatalf("instant: %v", body)
		}
		_, body = h.call("POST", "/calendar", "", map[string]any{"date": "2024-07-15", "bs": "2081-04-01"})
		if dig(body, "fy") != "2080/81" || dig(body, "quarter") != float64(4) {
			t.Fatalf("last day of FY 2080/81: %v", body)
		}
		if status, _ := h.call("POST", "/calendar", "", map[string]any{"date": "1900-01-01", "bs": "2081-01-01"}); status < 400 {
			t.Fatalf("out of range: %d", status)
		}
	})

	t.Run("money", func(t *testing.T) {
		status, body := h.call("POST", "/money", "", map[string]any{
			"a": "100", "b": 0.1, "c": "0.2", "big": "1234567.5",
			"bill": "1000.03", "weights": []any{1, 1, 1}, "usd": "19.99", "rate": "149.555",
		})
		if status != 200 {
			t.Fatalf("money: %d %v", status, body)
		}
		want := map[string]any{
			"sum": "100.30", "diff": "99.90", "formatted": "Rs 12,34,567.50",
			"usd": "9265.04", "yen": "2990",
		}
		for k, v := range want {
			if dig(body, k) != v {
				t.Fatalf("%s = %#v, want %#v (%v)", k, dig(body, k), v, body)
			}
		}
		if fmt.Sprint(dig(body, "shares")) != "[50.00 30.00 20.00]" ||
			fmt.Sprint(dig(body, "points")) != "[34 33 33]" ||
			fmt.Sprint(dig(body, "split")) != "[334.03 333.00 333.00]" {
			t.Fatalf("allocations: %v", body)
		}
		// Excess precision is refused, not rounded away.
		if status, _ := h.call("POST", "/money", "", map[string]any{
			"a": "1.001", "b": 0, "c": 0, "big": 1, "bill": 1, "weights": []any{1}, "usd": 1, "rate": 1,
		}); status < 400 {
			t.Fatalf("precision: %d", status)
		}
	})

	t.Run("currency columns", func(t *testing.T) {
		status, body := h.call("POST", "/api/invoices", "", map[string]any{
			"customer": "Sita", "amount": "1250.5", "fare": 1500, "duty": "0.125", "points": 40,
		})
		if status != 201 {
			t.Fatalf("create: %d %v", status, body)
		}
		if dig(body, "amount") != "1250.50" || dig(body, "fare") != "1500" || dig(body, "duty") != "0.125" || dig(body, "points") != "40" {
			t.Fatalf("scales: %v", body)
		}
		for field, value := range map[string]any{"fare": "1500.5", "amount": "1.001", "duty": "0.0001", "points": "1.5"} {
			rec := map[string]any{"customer": "X", field: value}
			if status, body := h.call("POST", "/api/invoices", "", rec); status != 422 {
				t.Fatalf("%s=%v: %d %v", field, value, status, body)
			}
		}
		_, body = h.call("GET", "/api/invoices?amount__gte=1000", "", nil)
		if dig(body, "total") != float64(1) {
			t.Fatalf("filter: %v", body)
		}
	})
}

func TestCurrencyCompileErrors(t *testing.T) {
	base := `
name "c"
resource "db" {
  kind "database.sql"
  config { driver "sqlite" dsn "file::memory:" }
}
`
	cases := map[string]string{
		`currency "ZZZ" { symbol "z" }`:    "needs minor_units",
		`currency "JPY" { minor_units 2 }`: "cannot be redefined",
		`currency "ABC" { minor_units 1 }
currency "abc" { minor_units 1 }`: "declared twice",
		`entity "e" {
  database "db"
  column "x" { currency "XYZ" }
}`: `unknown currency "XYZ"`,
		`entity "e" {
  database "db"
  column "x" { currency "JPY"  scale 2 }
}`: "conflicts with JPY",
		`entity "e" {
  database "db"
  column "x" { currency "NPR"  kind text }
}`: "needs kind decimal",
	}
	for extra, want := range cases {
		_, err := Compile(context.Background(), []byte(base+extra), t.TempDir(), DefaultLoadOptions())
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s\n→ %v, want %q", extra, err, want)
		}
		report := Validate(context.Background(), []byte(base+extra), t.TempDir(), DefaultLoadOptions())
		if report.Valid || !strings.Contains(strings.Join(report.Errors, "\n"), want) {
			t.Fatalf("Validate(%s) = %v", extra, report.Errors)
		}
	}
}

func TestMoneyActionConfigErrors(t *testing.T) {
	base := "name \"c\"\nintent \"i\" {\n  response \"r\"\n  node \"r\" {\n    requires [input]\n    provides [r]\n"
	cases := map[string]string{
		`uses "money.allocate"
    config { currency "NPR" amount "input.a" }`: "exactly one of config.ratios and config.parts",
		`uses "money.allocate"
    config { currency "XXX" amount "input.a" parts 2 }`: `unknown currency "XXX"`,
		`uses "money.allocate"
    config { currency "NPR" amount "input.a" parts 2 unit "0.001" }`: "unit",
		`uses "money.convert"
    config {
      from "USD"
      to "NPR"
      amount "input.a"
      rate "1"
      rounding "sideways"
    }`: "unknown rounding mode",
	}
	for node, want := range cases {
		_, err := Compile(context.Background(), []byte(base+"    "+node+"\n  }\n}\n"), t.TempDir(), DefaultLoadOptions())
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s\n→ %v, want %q", node, err, want)
		}
	}
}

func TestCustomCurrencyPublishedToExpressions(t *testing.T) {
	src := "name \"c\"\ncurrency \"GEMZ\" {\n  minor_units 1\n  symbol \"g\"\n}\n"
	p, err := Compile(context.Background(), []byte(src), t.TempDir(), DefaultLoadOptions())
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if c, ok := money.Lookup("GEMZ"); !ok || c.Minor != 1 {
		t.Fatalf("GEMZ not published: %+v", c)
	}
	v, err := MustCompileExpr(`money_format("GEMZ", "12345.5")`).Eval(Env{})
	if err != nil || v != "g12,345.5" {
		t.Fatalf("format: %v %v", v, err)
	}
}
