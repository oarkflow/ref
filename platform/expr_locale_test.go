package platform

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestLocaleExpressionFunctions(t *testing.T) {
	env := Env{
		"now":   "2024-07-15T20:00:00Z", // 01:45 on 16 July in Nepal: 1 Shrawan 2081
		"input": map[string]any{"a": "10.05", "b": 0.1, "list": []any{"1.10", 2, "3.333"}, "t": time.Date(2025, 4, 14, 0, 0, 0, 0, time.UTC)},
	}
	cases := map[string]any{
		`bs_date("1943-04-14")`:                             "2000-01-01",
		`bs_date("2024-04-13")`:                             "2081-01-01",
		`bs_date(now)`:                                      "2081-04-01",
		`bs_date(input.t)`:                                  "2082-01-01",
		`bs_date("2024-04-13", "YYYY/M/D")`:                 "2081/1/1",
		`bs_format("2024-04-13", "D MMMM YYYY, dddd")`:      "1 Baisakh 2081, Saturday",
		`bs_format_np("2024-04-13", "YYYY NNNN D WWWW")`:    "२०८१ वैशाख १ शनिबार",
		`bs_format("2024-04-13")`:                           "2081-01-01",
		`bs_year("2024-04-13")`:                             2081,
		`bs_month(now)`:                                     4,
		`bs_day("2024-04-27")`:                              15,
		`bs_month_name(4)`:                                  "Shrawan",
		`bs_month_name_np(12)`:                              "चैत",
		`bs_to_ad("2081-04-01")`:                            "2024-07-16",
		`bs_to_ad("२०८१-०१-०१")`:                            "2024-04-13",
		`bs_days_in_month(2081, 1)`:                         31,
		`devanagari_digits("FY 2081/82")`:                   "FY २०८१/८२",
		`ascii_digits("२०८१")`:                              "2081",
		`fiscal_year(now)`:                                  "2081/82",
		`fiscal_year("2024-07-15")`:                         "2080/81",
		`fiscal_quarter("2024-11-01")`:                      2,
		`fiscal_quarter("2025-02-01")`:                      3,
		`fiscal_year_start(now)`:                            "2024-07-16",
		`fiscal_year_end(now)`:                              "2025-07-16",
		`fiscal_year_start("2082/83")`:                      "2025-07-17",
		`fiscal_year_end("2081-82")`:                        "2025-07-16",
		`fiscal_year(1720000000)`:                           "2080/81",
		`gregorian_fiscal_year("2025-03-31", 4)`:            "2024/25",
		`gregorian_fiscal_year("2025-01-15", 1)`:            "2025",
		`gregorian_fiscal_year("2024-10-01", 10, "end")`:    "FY2025",
		`gregorian_fiscal_quarter("2024-10-01", 7)`:         2,
		`gregorian_fiscal_year_start(now, 4)`:               "2024-04-01",
		`gregorian_fiscal_year_end(now, 4)`:                 "2025-03-31",
		`money_add("NPR", input.a, input.b)`:                "10.15",
		`money_add("npr", "1")`:                             "1.00",
		`money_sub("USD", "1", "0.01", "0.99")`:             "0.00",
		`money_sum("KWD", input.list)`:                      "6.433",
		`money_cmp("NPR", "1.10", 1.1)`:                     0,
		`money_cmp("NPR", "1", "2")`:                        -1,
		`money_parse("INR", "₹ 1,23,456.5")`:                "123456.50",
		`money_round("NPR", "2.345")`:                       "2.34",
		`money_round("NPR", "2.345", "half_up")`:            "2.35",
		`money_round("JPY", 2.5)`:                           "2",
		`money_round("JPY", "-2.5", "up")`:                  "-3",
		`money_format("USD", "-1234.5")`:                    "-$1,234.50",
		`money_format("NPR", "1234567", "code")`:            "12,34,567.00 NPR",
		`money_format("NPR", "1234567", "plain")`:           "12,34,567.00",
		`money_convert("USD", "1", "NPR", "133.255")`:       "133.26",
		`money_convert("USD", "1", "NPR", 133.255, "down")`: "133.25",
		`money_minor("KWD", "1.5")`:                         int64(1500),
		`money_from_minor("NPR", 12345)`:                    "123.45",
		`money_from_minor("JPY", "-7")`:                     "-7",
		`currency_minor_units("BHD")`:                       3,
		`currency_symbol("GBP")`:                            "£",
	}
	for src, want := range cases {
		got, err := MustCompileExpr(src).Eval(env)
		if err != nil || fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("%s = %#v (%v), want %#v", src, got, err, want)
		}
	}
	lists := map[string]string{
		`money_allocate("NPR", "100", [1, 1, 1])`:           "[33.34 33.33 33.33]",
		`money_allocate("NPR", "-0.05", [1, 1])`:            "[-0.03 -0.02]",
		`money_allocate("NPR", "10.03", ["0.5", 0.5], "1")`: "[5.03 5.00]",
		`money_allocate("NPR", "1000", [70, 20, 10])`:       "[700.00 200.00 100.00]",
		`money_split("JPY", 1000, 3)`:                       "[334 333 333]",
		`money_split("NPR", "10", 3, "0.05")`:               "[3.35 3.35 3.30]",
	}
	for src, want := range lists {
		got, err := MustCompileExpr(src).Eval(env)
		if err != nil || fmt.Sprint(got) != want {
			t.Errorf("%s = %v (%v), want %s", src, got, err, want)
		}
	}
	errs := map[string]string{
		`bs_date("1900-01-01")`:                    "outside the supported range",
		`bs_date("yesterday")`:                     "not a date",
		`bs_date()`:                                "takes 1 to 2",
		`bs_month_name(13)`:                        "month must be 1-12",
		`bs_to_ad("2081-13-01")`:                   "invalid date",
		`bs_days_in_month(1990, 1)`:                "outside",
		`fiscal_year("2081/83")`:                   "not a fiscal year",
		`gregorian_fiscal_year(now, 13)`:           "start month must be 1-12",
		`gregorian_fiscal_year(now, 4, "middle")`:  "naming",
		`money_add("XXX", "1", "2")`:               `unknown currency "XXX"`,
		`money_add("NPR", "1.001")`:                "more decimal places",
		`money_add("NPR", "abc")`:                  "not a decimal amount",
		`money_add("NPR")`:                         "at least one amount",
		`money_sum("NPR", "x")`:                    "must be a list",
		`money_round("NPR", "1", "sideways")`:      "unknown rounding mode",
		`money_format("NPR", "1", "fancy")`:        "style",
		`money_convert("NPR", "1", "USD", "0")`:    "must be positive",
		`money_convert("NPR", "1", "USD", "x")`:    "rate",
		`money_allocate("NPR", "1", [0, 0])`:       "must be positive",
		`money_allocate("NPR", "1", "x")`:          "list of numbers",
		`money_allocate("NPR", "1", [1], "0.001")`: "unit",
		`money_split("NPR", "1", 0)`:               "parts must be",
		`money_from_minor("NPR", 1.5)`:             "whole number",
	}
	for src, want := range errs {
		_, err := MustCompileExpr(src).Eval(env)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", src, err, want)
		}
	}
}
