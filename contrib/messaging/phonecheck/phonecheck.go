// Package phonecheck registers phone.validate: validation and normalisation of
// phone numbers with github.com/oarkflow/phone (libphonenumber's metadata). A
// bulk send uses it to skip rows whose number cannot be dialled, with the reason.
//
//	node "numbers" {
//	  uses "phone.validate" kind pure requires [rows] provides [numbers]
//	  config { values_fact "phones" default_region "NP" allow_types ["MOBILE", "FIXED_LINE_OR_MOBILE"] }
//	}
//
// The result for one number is
//
//	{ input, valid, reason, e164, digits, national, country_code, region, type, carrier }
//
// reason is empty for a valid number, otherwise one of empty, unparsable,
// too_short, too_long, not_possible, not_valid or type_not_allowed.
package phonecheck

import (
	"fmt"
	"strings"
	"sync"

	"github.com/oarkflow/phone"
	"github.com/oarkflow/ref/platform"
)

var once sync.Once

// Register installs phone.validate.
func Register() {
	once.Do(func() {
		platform.RegisterActionDriver("phone.validate", platform.ActionFactoryFunc(build), platform.ActionInfo{
			Family:   "data",
			Summary:  "Validate and normalise phone numbers (github.com/oarkflow/phone). One number gives one result, a list gives a list in the same order",
			Provides: "{ input, valid, reason, e164, digits, national, country_code, region, type, carrier } or a list of them",
			Kind:     "pure",
			Config: []platform.ConfigField{
				{Name: "value_fact", Type: "fact", Summary: "A fact holding one number"},
				{Name: "values_fact", Type: "fact", Summary: "A fact holding a list of numbers"},
				{Name: "rows_fact", Type: "fact", Summary: "A fact holding a list of rows (objects). Each comes back with n, phone (the number as given), phone_e164, phone_region, phone_type, state (pending, or skipped when the number is not valid), reason, and fields (a copy of the original row)"},
				{Name: "phone_field_fact", Type: "fact", Summary: "With rows_fact: a fact holding the name of the column that has the number; default phone"},
				{Name: "default_region", Type: "string", Default: "NP", Summary: "Region (ISO code) for numbers written without a country code"},
				{Name: "region_fact", Type: "fact", Summary: "A fact holding the region, overriding default_region (a bulk send names its own)"},
				{Name: "allow_types", Type: "[]string", Summary: "Number types accepted (MOBILE, FIXED_LINE, FIXED_LINE_OR_MOBILE, VOIP, TOLL_FREE, PREMIUM_RATE, ...); empty accepts any valid number"},
			},
		})
	})
}

var typeNames = map[phone.PhoneNumberType]string{
	phone.FIXED_LINE: "FIXED_LINE", phone.MOBILE: "MOBILE", phone.FIXED_LINE_OR_MOBILE: "FIXED_LINE_OR_MOBILE",
	phone.TOLL_FREE: "TOLL_FREE", phone.PREMIUM_RATE: "PREMIUM_RATE", phone.SHARED_COST: "SHARED_COST",
	phone.VOIP: "VOIP", phone.PERSONAL_NUMBER: "PERSONAL_NUMBER", phone.PAGER: "PAGER", phone.UAN: "UAN",
	phone.VOICEMAIL: "VOICEMAIL", phone.UNKNOWN: "UNKNOWN",
}

// Check validates one number.
func Check(input, defaultRegion string, allow map[string]bool) map[string]any {
	out := map[string]any{"input": input, "valid": false, "reason": "", "e164": "", "digits": "", "national": "", "country_code": 0, "region": "", "type": "", "carrier": ""}
	text := strings.TrimSpace(input)
	if text == "" {
		out["reason"] = "empty"
		return out
	}
	num, err := phone.Parse(text, defaultRegion)
	if err != nil {
		out["reason"] = "unparsable"
		return out
	}
	out["country_code"] = int(num.GetCountryCode())
	out["region"] = phone.GetRegionCodeForNumber(num)
	switch phone.IsPossibleNumberWithReason(num) {
	case phone.IS_POSSIBLE, phone.IS_POSSIBLE_LOCAL_ONLY:
	case phone.TOO_SHORT:
		out["reason"] = "too_short"
		return out
	case phone.TOO_LONG:
		out["reason"] = "too_long"
		return out
	default:
		out["reason"] = "not_possible"
		return out
	}
	if !phone.IsValidNumber(num) {
		out["reason"] = "not_valid"
		return out
	}
	kind := typeNames[phone.GetNumberType(num)]
	out["type"] = kind
	if len(allow) > 0 && !allow[kind] {
		out["reason"] = "type_not_allowed"
		return out
	}
	e164 := phone.Format(num, phone.E164)
	out["valid"] = true
	out["e164"] = e164
	out["digits"] = strings.TrimPrefix(e164, "+")
	out["national"] = phone.Format(num, phone.NATIONAL)
	if carrier, err := phone.GetCarrierForNumber(num, "en"); err == nil {
		out["carrier"] = carrier
	}
	return out
}

func build(_ platform.BuildContext, spec platform.NodeSpec) (platform.Action, error) {
	if len(spec.Provides) != 1 {
		return nil, fmt.Errorf("node %q: phone.validate provides exactly one fact", spec.Name)
	}
	one, _ := spec.Config["value_fact"].(string)
	many, _ := spec.Config["values_fact"].(string)
	rowsFact, _ := spec.Config["rows_fact"].(string)
	fieldFact, _ := spec.Config["phone_field_fact"].(string)
	count := 0
	for _, f := range []string{one, many, rowsFact} {
		if f != "" {
			count++
		}
	}
	if count != 1 {
		return nil, fmt.Errorf("node %q: phone.validate needs exactly one of value_fact, values_fact and rows_fact", spec.Name)
	}
	region, _ := spec.Config["default_region"].(string)
	if region == "" {
		region = "NP"
	}
	allow := map[string]bool{}
	switch v := spec.Config["allow_types"].(type) {
	case []any:
		for _, t := range v {
			allow[strings.ToUpper(fmt.Sprint(t))] = true
		}
	case []string:
		for _, t := range v {
			allow[strings.ToUpper(t)] = true
		}
	}
	regionFact, _ := spec.Config["region_fact"].(string)
	provides := spec.Provides[0]
	return platform.ActionFunc(func(ctx *platform.ActionContext) (platform.ActionResult, error) {
		region := region
		if regionFact != "" {
			if v, ok := platform.ResolvePath(ctx.Inputs, regionFact); ok && strings.TrimSpace(fmt.Sprint(v)) != "" {
				region = strings.ToUpper(strings.TrimSpace(fmt.Sprint(v)))
			}
		}
		if rowsFact != "" {
			field := "phone"
			if fieldFact != "" {
				if v, ok := platform.ResolvePath(ctx.Inputs, fieldFact); ok && strings.TrimSpace(fmt.Sprint(v)) != "" {
					field = strings.TrimSpace(fmt.Sprint(v))
				}
			}
			v, _ := platform.ResolvePath(ctx.Inputs, rowsFact)
			list, _ := v.([]any)
			rows := make([]any, 0, len(list))
			for i, item := range list {
				original, _ := item.(map[string]any)
				row := make(map[string]any, len(original)+8)
				snapshot := make(map[string]any, len(original))
				for k, val := range original {
					row[k], snapshot[k] = val, val
				}
				given := strings.TrimSpace(fmt.Sprint(orEmpty(original[field])))
				res := Check(given, region, allow)
				row["n"], row["phone"], row["fields"] = i, given, snapshot
				row["phone_e164"], row["phone_region"], row["phone_type"] = res["e164"], res["region"], res["type"]
				row["reason"] = res["reason"]
				if res["valid"] == true {
					row["state"] = "pending"
				} else {
					row["state"] = "skipped"
				}
				rows = append(rows, row)
			}
			return platform.ActionResult{Outputs: map[string]any{provides: rows}}, nil
		}
		if one != "" {
			v, _ := platform.ResolvePath(ctx.Inputs, one)
			return platform.ActionResult{Outputs: map[string]any{provides: Check(fmt.Sprint(orEmpty(v)), region, allow)}}, nil
		}
		v, _ := platform.ResolvePath(ctx.Inputs, many)
		list, _ := v.([]any)
		results := make([]any, 0, len(list))
		for _, item := range list {
			results = append(results, Check(fmt.Sprint(orEmpty(item)), region, allow))
		}
		return platform.ActionResult{Outputs: map[string]any{provides: results}}, nil
	}), nil
}

func orEmpty(v any) any {
	if v == nil {
		return ""
	}
	return v
}
