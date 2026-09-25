package platform

import (
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/oarkflow/ref/dos"
	"github.com/oarkflow/ref/intent"
)

// Date-of-service actions for activity-based applications: medical coding and
// billing (single- or multi-DOS encounters), field inspections, timesheets,
// home-care visits — anything recorded against the civil date(s) it happened
// on rather than a timestamp.
//
// A period fact is always published as
//
//	{ from "2026-03-01" to "2026-03-03" kind "multi" days 3 }
//
// so later nodes, SQL arguments and responses all see one shape whatever the
// client sent (a single "dos", a from/to pair, or a "from..to" string).

func registerDOSActions(r *Registry) {
	periodFields := []ConfigField{
		{Name: "period", Type: "string", Summary: "Fact path of an existing period (object with from/to, or a string); alternative to from/to/date"},
		{Name: "from", Type: "string", Default: "input.dos_from", Summary: "Fact path of the first date of service"},
		{Name: "to", Type: "string", Default: "input.dos_to", Summary: "Fact path of the last date of service; missing means a single DOS"},
		{Name: "date", Type: "string", Default: "input.dos", Summary: "Fact path of a single date of service, used when from is absent"},
	}
	mustAction(r, "dos.period", ActionFactoryFunc(buildDOSPeriod), ActionInfo{
		Family:  "dos",
		Summary: "Normalise a single or multi date of service into a period { from to kind days }",
		Config: append(periodFields,
			ConfigField{Name: "dates", Type: "bool", Default: "false", Summary: "Also publish every date in the period"},
			ConfigField{Name: "split_by_month", Type: "bool", Default: "false", Summary: "Also publish the period cut at month boundaries"}),
		Kind: "pure",
	})
	mustAction(r, "dos.validate", ActionFactoryFunc(buildDOSValidate), ActionInfo{
		Family:  "dos",
		Summary: "Check a period (and optionally its line items) against date-of-service rules",
		Config: append(periodFields,
			ConfigField{Name: "allow_multi", Type: "bool", Default: "true"},
			ConfigField{Name: "max_span_days", Type: "int", Summary: "Longest allowed multi-DOS period"},
			ConfigField{Name: "allow_future", Type: "bool", Default: "false"},
			ConfigField{Name: "max_age_days", Type: "int", Summary: "Timely-filing window: reject service older than this"},
			ConfigField{Name: "same_month", Type: "bool", Default: "false"},
			ConfigField{Name: "weekdays", Type: "[]string", Summary: "Billable weekdays, e.g. [mon tue wed thu fri]"},
			ConfigField{Name: "not_before", Type: "string", Summary: "Earliest allowed date (YYYY-MM-DD)"},
			ConfigField{Name: "timezone", Type: "string", Default: "UTC", Summary: "IANA zone that decides what 'today' is"},
			ConfigField{Name: "lines", Type: "string", Summary: "Fact path of line items; each must fall inside the period"},
			ConfigField{Name: "line_from", Type: "string", Default: "dos_from"},
			ConfigField{Name: "line_to", Type: "string", Default: "dos_to"},
			ConfigField{Name: "line_date", Type: "string", Default: "dos"},
			ConfigField{Name: "units_key", Type: "string", Default: "units"},
			ConfigField{Name: "fail", Type: "bool", Default: "true", Summary: "Fail the request (422 with details) on any violation; false only reports"}),
		Kind: "pure",
	})
	mustAction(r, "dos.expand", ActionFactoryFunc(buildDOSExpand), ActionInfo{
		Family:  "dos",
		Summary: "Explode line items into one row per date of service, ready for database.insert_many",
		Config: append(periodFields,
			ConfigField{Name: "lines", Type: "string", Summary: "Fact path of line items; without it the period itself is expanded into one row per date"},
			ConfigField{Name: "mode", Type: "string", Default: "distribute", Summary: "distribute (split total units across dates) | per_day (repeat units on every date)"},
			ConfigField{Name: "line_from", Type: "string", Default: "dos_from"},
			ConfigField{Name: "line_to", Type: "string", Default: "dos_to"},
			ConfigField{Name: "line_date", Type: "string", Default: "dos"},
			ConfigField{Name: "units_key", Type: "string", Default: "units"},
			ConfigField{Name: "output_date_key", Type: "string", Default: "dos"}),
		Kind: "pure",
	})
	mustAction(r, "dos.overlap", ActionFactoryFunc(buildDOSOverlap), ActionInfo{
		Family:  "dos",
		Summary: "Detect overlap between a period and existing records (duplicate-billing check)",
		Config: append(periodFields,
			ConfigField{Name: "existing", Type: "string", Required: true, Summary: "Fact path of existing rows, e.g. a database.query result"},
			ConfigField{Name: "existing_from", Type: "string", Default: "dos_from"},
			ConfigField{Name: "existing_to", Type: "string", Default: "dos_to"},
			ConfigField{Name: "id_key", Type: "string", Default: "id"},
			ConfigField{Name: "exclude_id", Type: "string", Summary: "Fact path of the record being edited, so it does not conflict with itself"},
			ConfigField{Name: "on_conflict", Type: "string", Default: "fail", Summary: "fail (409 with details) | report"}),
		Kind: "pure",
	})
}

// periodSource reads a period from facts according to the shared config keys.
type periodSource struct {
	period, from, to, date string
}

func newPeriodSource(config map[string]any) periodSource {
	return periodSource{
		period: configString(config, "period", ""),
		from:   configString(config, "from", "input.dos_from"),
		to:     configString(config, "to", "input.dos_to"),
		date:   configString(config, "date", "input.dos"),
	}
}

func (s periodSource) read(inputs map[string]any) (dos.Period, error) {
	if s.period != "" {
		raw, ok := resolvePath(inputs, s.period)
		if !ok || raw == nil {
			return dos.Period{}, invalidInput("a date-of-service period is required")
		}
		p, err := periodFromValue(raw, "from", "to", "dos")
		if err != nil {
			return dos.Period{}, invalidInput("%s", err.Error())
		}
		return p, nil
	}
	from, hasFrom := pathString(inputs, s.from)
	if !hasFrom {
		from, hasFrom = pathString(inputs, s.date)
	}
	if !hasFrom {
		return dos.Period{}, invalidInput("a date of service is required (%s or %s)", s.from, s.date)
	}
	to, _ := pathString(inputs, s.to)
	p, err := dos.ParsePeriod(from, to)
	if err != nil {
		return dos.Period{}, invalidInput("%s", err.Error())
	}
	return p, nil
}

func pathString(inputs map[string]any, path string) (string, bool) {
	if path == "" {
		return "", false
	}
	v, ok := resolvePath(inputs, path)
	if !ok || v == nil {
		return "", false
	}
	s := strings.TrimSpace(Stringify(v))
	return s, s != ""
}

// periodFromValue accepts {from,to}, {<fromKey>,<toKey>}, {<dateKey>},
// "a..b" or a single date string.
func periodFromValue(raw any, fromKey, toKey, dateKey string) (dos.Period, error) {
	switch v := raw.(type) {
	case string:
		if from, to, found := strings.Cut(v, ".."); found {
			return dos.ParsePeriod(from, to)
		}
		return dos.ParsePeriod(v, "")
	case map[string]any:
		from := firstString(v, fromKey, "from", dateKey)
		to := firstString(v, toKey, "to")
		if strings.TrimSpace(from) == "" {
			return dos.Period{}, fmt.Errorf("date of service is missing (%s)", fromKey)
		}
		return dos.ParsePeriod(from, to)
	default:
		return dos.Period{}, fmt.Errorf("unsupported date-of-service value %T", raw)
	}
}

func periodView(p dos.Period) map[string]any {
	return map[string]any{"from": p.From.String(), "to": p.To.String(), "kind": p.Kind(), "days": p.Days()}
}

func buildDOSPeriod(_ BuildContext, spec NodeSpec) (Action, error) {
	if err := exactlyOneOutput(spec); err != nil {
		return nil, err
	}
	source := newPeriodSource(spec.Config)
	withDates := configBool(spec.Config, "dates", false)
	split := configBool(spec.Config, "split_by_month", false)
	return ActionFunc(func(ctx *ActionContext) (ActionResult, error) {
		p, err := source.read(ctx.Inputs)
		if err != nil {
			return ActionResult{}, err
		}
		out := periodView(p)
		if withDates {
			dates := p.Dates()
			list := make([]any, len(dates))
			for i, d := range dates {
				list[i] = d.String()
			}
			out["dates"] = list
		}
		if split {
			var parts []any
			for _, part := range p.SplitByMonth() {
				parts = append(parts, periodView(part))
			}
			out["months"] = parts
		}
		return singleOutput(spec, out), nil
	}), nil
}

type lineKeys struct {
	from, to, date, units string
}

func newLineKeys(config map[string]any) lineKeys {
	return lineKeys{
		from:  configString(config, "line_from", "dos_from"),
		to:    configString(config, "line_to", "dos_to"),
		date:  configString(config, "line_date", "dos"),
		units: configString(config, "units_key", "units"),
	}
}

// readLines parses line items. A line with no date of its own inherits the
// encounter period.
func (k lineKeys) readLines(raw any, encounter dos.Period) ([]dos.Line, []map[string]any, error) {
	var items []map[string]any
	switch v := raw.(type) {
	case nil:
		return nil, nil, nil
	case []map[string]any:
		items = v
	case []any:
		for i, item := range v {
			m, ok := item.(map[string]any)
			if !ok {
				return nil, nil, invalidInput("line %d must be an object", i+1)
			}
			items = append(items, m)
		}
	default:
		return nil, nil, invalidInput("line items must be a list")
	}
	lines := make([]dos.Line, len(items))
	for i, item := range items {
		period := encounter
		if from := firstString(item, k.from, k.date); strings.TrimSpace(from) != "" {
			p, err := dos.ParsePeriod(from, firstString(item, k.to))
			if err != nil {
				return nil, nil, invalidInput("line %d: %s", i+1, err.Error())
			}
			period = p
		}
		units := 1.0
		if raw, ok := item[k.units]; ok && raw != nil {
			f, ok := ToFloat(raw)
			if !ok {
				return nil, nil, invalidInput("line %d: %s must be a number", i+1, k.units)
			}
			units = f
		}
		lines[i] = dos.Line{Period: period, Units: units, Data: item}
	}
	return lines, items, nil
}

var weekdayNames = map[string]time.Weekday{
	"sun": time.Sunday, "mon": time.Monday, "tue": time.Tuesday, "wed": time.Wednesday,
	"thu": time.Thursday, "fri": time.Friday, "sat": time.Saturday,
}

func buildDOSValidate(_ BuildContext, spec NodeSpec) (Action, error) {
	source := newPeriodSource(spec.Config)
	rules := dos.Rules{
		AllowMulti:  configBool(spec.Config, "allow_multi", true),
		AllowFuture: configBool(spec.Config, "allow_future", false),
		SameMonth:   configBool(spec.Config, "same_month", false),
	}
	var err error
	if rules.MaxSpanDays, err = configInt(spec.Config, "max_span_days", 0); err != nil {
		return nil, fmt.Errorf("node %q: %w", spec.Name, err)
	}
	if rules.MaxAgeDays, err = configInt(spec.Config, "max_age_days", 0); err != nil {
		return nil, fmt.Errorf("node %q: %w", spec.Name, err)
	}
	for _, name := range configStrings(spec.Config, "weekdays") {
		day, ok := weekdayNames[strings.ToLower(strings.TrimSpace(name))[:min(3, len(strings.TrimSpace(name)))]]
		if !ok {
			return nil, fmt.Errorf("node %q: unknown weekday %q", spec.Name, name)
		}
		rules.Weekdays = append(rules.Weekdays, day)
	}
	if raw := configString(spec.Config, "not_before", ""); raw != "" {
		if rules.NotBefore, err = dos.ParseDate(raw); err != nil {
			return nil, fmt.Errorf("node %q: not_before: %w", spec.Name, err)
		}
	}
	loc, err := time.LoadLocation(configString(spec.Config, "timezone", "UTC"))
	if err != nil {
		return nil, fmt.Errorf("node %q: timezone: %w", spec.Name, err)
	}
	linesPath := configString(spec.Config, "lines", "")
	keys := newLineKeys(spec.Config)
	fail := configBool(spec.Config, "fail", true)

	return ActionFunc(func(ctx *ActionContext) (ActionResult, error) {
		p, err := source.read(ctx.Inputs)
		if err != nil {
			return ActionResult{}, err
		}
		now := ctx.Now
		if now.IsZero() {
			now = time.Now()
		}
		today := dos.Today(now, loc)
		violations := rules.Validate(p, today)
		lineCount := 0
		if linesPath != "" {
			raw, _ := resolvePath(ctx.Inputs, linesPath)
			lines, _, err := keys.readLines(raw, p)
			if err != nil {
				return ActionResult{}, err
			}
			lineCount = len(lines)
			// Lines are held to the encounter period and positive units; the
			// period-level rules already ran on the encounter itself.
			lineRules := dos.Rules{AllowMulti: true, AllowFuture: true}
			violations = append(violations, lineRules.ValidateLines(p, lines, today)...)
		}
		details := make([]any, len(violations))
		for i, v := range violations {
			details[i] = map[string]any{"rule": v.Rule, "message": v.Message, "line": v.Line}
		}
		if fail && len(violations) > 0 {
			message := violations[0].Message
			if len(violations) > 1 {
				message = fmt.Sprintf("%s (and %d more)", message, len(violations)-1)
			}
			return ActionResult{}, intent.Failure{Code: "INVALID_DATE_OF_SERVICE", Category: intent.CategoryInvalidInput,
				Message: message, Meta: map[string]any{"details": details}}
		}
		out := periodView(p)
		out["valid"] = len(violations) == 0
		out["violations"] = details
		out["lines"] = lineCount
		return acknowledgement(spec, out), nil
	}), nil
}

func buildDOSExpand(_ BuildContext, spec NodeSpec) (Action, error) {
	if err := exactlyOneOutput(spec); err != nil {
		return nil, err
	}
	source := newPeriodSource(spec.Config)
	linesPath := configString(spec.Config, "lines", "")
	mode := configString(spec.Config, "mode", dos.ExpandDistribute)
	if mode != dos.ExpandDistribute && mode != dos.ExpandPerDay {
		return nil, fmt.Errorf("node %q: mode must be %s or %s", spec.Name, dos.ExpandDistribute, dos.ExpandPerDay)
	}
	keys := newLineKeys(spec.Config)
	dateKey := configString(spec.Config, "output_date_key", "dos")

	return ActionFunc(func(ctx *ActionContext) (ActionResult, error) {
		p, err := source.read(ctx.Inputs)
		if err != nil {
			return ActionResult{}, err
		}
		var lines []dos.Line
		if linesPath != "" {
			raw, _ := resolvePath(ctx.Inputs, linesPath)
			if lines, _, err = keys.readLines(raw, p); err != nil {
				return ActionResult{}, err
			}
		} else {
			lines = []dos.Line{{Period: p, Units: float64(p.Days())}}
		}
		expanded, err := dos.Expand(lines, mode)
		if err != nil {
			return ActionResult{}, invalidInput("%s", err.Error())
		}
		rows := make([]any, 0, len(expanded))
		for _, day := range expanded {
			row := make(map[string]any, len(day.Data)+3)
			maps.Copy(row, day.Data)
			delete(row, keys.from)
			delete(row, keys.to)
			delete(row, keys.date)
			row[dateKey] = day.Date.String()
			row[keys.units] = day.Units
			row["line_no"] = day.Line + 1
			rows = append(rows, row)
		}
		return singleOutput(spec, rows), nil
	}), nil
}

func buildDOSOverlap(_ BuildContext, spec NodeSpec) (Action, error) {
	source := newPeriodSource(spec.Config)
	existingPath := configString(spec.Config, "existing", "")
	if existingPath == "" {
		return nil, fmt.Errorf("node %q: dos.overlap needs config.existing", spec.Name)
	}
	fromKey := configString(spec.Config, "existing_from", "dos_from")
	toKey := configString(spec.Config, "existing_to", "dos_to")
	idKey := configString(spec.Config, "id_key", "id")
	excludePath := configString(spec.Config, "exclude_id", "")
	onConflict := configString(spec.Config, "on_conflict", "fail")
	if onConflict != "fail" && onConflict != "report" {
		return nil, fmt.Errorf("node %q: on_conflict must be fail or report", spec.Name)
	}

	return ActionFunc(func(ctx *ActionContext) (ActionResult, error) {
		p, err := source.read(ctx.Inputs)
		if err != nil {
			return ActionResult{}, err
		}
		exclude, _ := pathString(ctx.Inputs, excludePath)
		raw, _ := resolvePath(ctx.Inputs, existingPath)
		var rows []map[string]any
		switch v := raw.(type) {
		case []map[string]any:
			rows = v
		case []any:
			for _, item := range v {
				if m, ok := item.(map[string]any); ok {
					rows = append(rows, m)
				}
			}
		case map[string]any:
			rows = []map[string]any{v}
		}
		var conflicts []any
		for _, row := range rows {
			if exclude != "" && Stringify(row[idKey]) == exclude {
				continue
			}
			existing, err := periodFromValue(row, fromKey, toKey, "dos")
			if err != nil {
				continue // rows without a usable period cannot conflict
			}
			if overlap, ok := p.Intersect(existing); ok {
				conflicts = append(conflicts, map[string]any{
					"id": row[idKey], "period": periodView(existing), "overlap": periodView(overlap),
				})
			}
		}
		if onConflict == "fail" && len(conflicts) > 0 {
			return ActionResult{}, intent.Failure{Code: "DOS_OVERLAP", Category: intent.CategoryConflict,
				Message: fmt.Sprintf("date of service %s overlaps %d existing record(s)", p, len(conflicts)),
				Meta:    map[string]any{"details": conflicts}}
		}
		if conflicts == nil {
			conflicts = []any{}
		}
		return acknowledgement(spec, map[string]any{"has_conflict": len(conflicts) > 0, "conflicts": conflicts, "period": periodView(p)}), nil
	}), nil
}
