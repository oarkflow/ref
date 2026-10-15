package pipeline

import (
	"fmt"
	"slices"
	"strings"
)

// Overrides are workspace settings applied on top of the base definition: a
// company, facility or work item tunes SLAs and narrows fields without
// changing the pipeline. They can tighten and tune; they can never grant
// access or remove a requirement the definition makes:
//
//   - a stage's due time and SLA duration, warning and calendar may change;
//   - an input may be made required, disabled or hidden (a form too), and
//     relabelled, but a base-required input cannot be hidden;
//   - roles, permissions and conditions are not overridable.
type Overrides struct {
	Stages map[string]StageOverride `json:"stages,omitempty"`
	// Fields is keyed "form.input", or "form" for a whole form.
	Fields map[string]FieldOverride `json:"fields,omitempty"`
}

// StageOverride tunes one stage's timing.
type StageOverride struct {
	Due        string `json:"due,omitempty"`
	Duration   string `json:"sla_duration,omitempty"`
	WarnBefore string `json:"sla_warn_before,omitempty"`
	Calendar   string `json:"sla_calendar,omitempty"`
}

// FieldOverride narrows one input or form.
type FieldOverride struct {
	Required *bool  `json:"required,omitempty"`
	Hidden   *bool  `json:"hidden,omitempty"`
	Disabled *bool  `json:"disabled,omitempty"`
	Label    string `json:"label,omitempty"`
}

// envOverrides is the environment key under which the case's overrides travel
// to the access helpers. It starts with an underscore so no expression an
// author writes can name it by accident.
const envOverrides = "_overrides"

func (e *Engine) overridesFor(c *Case) *Overrides {
	if e.Overrides == nil || c == nil {
		return nil
	}
	return e.Overrides(c)
}

func overridesIn(env map[string]any) *Overrides {
	ov, _ := env[envOverrides].(*Overrides)
	return ov
}

// fieldOverride returns the override for an input path, if any.
func fieldOverride(env map[string]any, key string) (FieldOverride, bool) {
	ov := overridesIn(env)
	if ov == nil {
		return FieldOverride{}, false
	}
	fo, ok := ov.Fields[key]
	return fo, ok
}

func isTrue(b *bool) bool { return b != nil && *b }

// stageFor is the stage as configured for this case's workspace: the base
// stage with its overrides applied to a copy.
func (e *Engine) stageFor(c *Case, st *Stage) *Stage {
	ov := e.overridesFor(c)
	if ov == nil {
		return st
	}
	so, ok := ov.Stages[st.Name]
	if !ok {
		return st
	}
	eff := *st
	if d, err := parseDuration(so.Due); err == nil && d > 0 {
		eff.Due = so.Due
	}
	if eff.SLA != nil {
		sla := *eff.SLA
		if sp, err := ParseSpan(so.Duration); err == nil && !sp.Zero() {
			sla.Duration = so.Duration
		}
		if sp, err := ParseSpan(so.WarnBefore); err == nil && !sp.Zero() {
			sla.WarnBefore = so.WarnBefore
		}
		if so.Calendar != "" && e.hasCalendar(so.Calendar) {
			sla.Calendar = so.Calendar
		}
		eff.SLA = &sla
	}
	return &eff
}

func (e *Engine) hasCalendar(name string) bool {
	return slices.ContainsFunc(e.C.Def.Calendars, func(cal Calendar) bool { return cal.Name == name })
}

// CheckOverrides lists what is wrong with a set of overrides against the
// pipeline: unknown stages, forms or inputs, unparseable durations, unknown
// calendars, and attempts to hide a base-required input. A host calls it
// before storing a workspace's settings.
func (c *Compiled) CheckOverrides(ov *Overrides) []string {
	if ov == nil {
		return nil
	}
	var problems []string
	for name, so := range ov.Stages {
		st, ok := c.Stage(name)
		if !ok {
			problems = append(problems, fmt.Sprintf("stage %q does not exist", name))
			continue
		}
		if so.Due != "" {
			if d, err := parseDuration(so.Due); err != nil || d <= 0 {
				problems = append(problems, fmt.Sprintf("stage %q due %q is not a duration", name, so.Due))
			}
		}
		for label, v := range map[string]string{"sla_duration": so.Duration, "sla_warn_before": so.WarnBefore} {
			if v == "" {
				continue
			}
			if sp, err := ParseSpan(v); err != nil || sp.Zero() {
				problems = append(problems, fmt.Sprintf("stage %q %s %q is not a duration", name, label, v))
			}
		}
		if (so.Duration != "" || so.WarnBefore != "" || so.Calendar != "") && st.SLA == nil {
			problems = append(problems, fmt.Sprintf("stage %q has no sla to tune", name))
		}
		if so.Calendar != "" && !slices.ContainsFunc(c.Def.Calendars, func(cal Calendar) bool { return cal.Name == so.Calendar }) {
			problems = append(problems, fmt.Sprintf("stage %q calendar %q is not defined", name, so.Calendar))
		}
	}
	for key, fo := range ov.Fields {
		form, input, hasInput := strings.Cut(key, ".")
		cf := c.forms[form]
		if cf == nil {
			problems = append(problems, fmt.Sprintf("form %q does not exist", form))
			continue
		}
		if !hasInput {
			continue
		}
		idx := slices.IndexFunc(cf.inputs, func(in Input) bool { return in.Name == input })
		if idx < 0 {
			problems = append(problems, fmt.Sprintf("input %q does not exist", key))
			continue
		}
		if isTrue(fo.Hidden) && cf.inputs[idx].Required {
			problems = append(problems, fmt.Sprintf("input %q is required by the pipeline and cannot be hidden", key))
		}
	}
	sort := func(s []string) []string { slices.Sort(s); return s }
	return sort(problems)
}
