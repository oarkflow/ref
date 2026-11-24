package pipeline

import "slices"

// Field, form and group access: who sees and who may change what, and when a
// form or input is hidden or disabled by a condition.
//
// A hidden input or form is neither shown, validated nor accepted on save. A
// disabled one is shown but read-only and not accepted. Role limits are strict:
// they apply to everyone with a role to check, the applicant of a public stage
// included. Only outside parties holding a signed link are exempt, because the
// link itself is the grant and names exactly what they may fill.

// roleAllows reports whether the actor passes a role limit: no limit, an
// outside link holder, or one of the named roles.
func roleAllows(actor Actor, roles []string) bool {
	return len(roles) == 0 || actor.Link != nil || actor.HasAnyRole(roles)
}

// formShown reports whether a form's condition lets it appear.
func (e *Engine) formShown(cf *compiledForm, env map[string]any) bool {
	if fo, ok := fieldOverride(env, cf.Name); ok && isTrue(fo.Hidden) {
		return false
	}
	ok, err := e.cond(cf.VisibleIf, env)
	return err == nil && ok
}

// inputShown reports whether an input's own condition lets it appear.
func (e *Engine) inputShown(form string, in Input, env map[string]any) bool {
	// A workspace may hide an input, but never one the pipeline requires.
	if fo, ok := fieldOverride(env, form+"."+in.Name); ok && isTrue(fo.Hidden) && !in.Required {
		return false
	}
	ok, err := e.cond(in.VisibleIf, env)
	return err == nil && ok
}

// visibleTo reports whether the actor may see the form and input at all: the
// conditions hold and the actor passes the form's and the input's view roles.
func (e *Engine) visibleTo(cf *compiledForm, in Input, env map[string]any, actor Actor) bool {
	return e.formShown(cf, env) && e.inputShown(cf.Name, in, env) &&
		roleAllows(actor, cf.ViewRoles) && roleAllows(actor, in.ViewRoles)
}

// formVisibleTo is visibleTo for a whole form.
func (e *Engine) formVisibleTo(cf *compiledForm, env map[string]any, actor Actor) bool {
	return e.formShown(cf, env) && roleAllows(actor, cf.ViewRoles)
}

// disabledBy reports whether a disabled_if condition makes the input read-only
// (group, form or input level).
func (e *Engine) disabledBy(g *Group, cf *compiledForm, in Input, env map[string]any) bool {
	for _, key := range []string{cf.Name, cf.Name + "." + in.Name} {
		if fo, ok := fieldOverride(env, key); ok && isTrue(fo.Disabled) {
			return true
		}
	}
	for _, cond := range []string{g.DisabledIf, cf.DisabledIf, in.DisabledIf} {
		if cond == "" {
			continue
		}
		if ok, err := e.cond(cond, env); err == nil && ok {
			return true
		}
	}
	return false
}

// mayEditInput applies the edit roles of the group, the form and the input.
func mayEditInput(actor Actor, g *Group, cf *compiledForm, in Input) bool {
	return roleAllows(actor, g.EditRoles) && roleAllows(actor, cf.EditRoles) && roleAllows(actor, in.EditRoles)
}

// ruleFor finds the stage rule for an input: the exact "form.input" rule, else
// the form-wide "form" rule.
func (g *Group) ruleFor(form, input string) *FieldRule {
	var wide *FieldRule
	for i := range g.Fields {
		r := &g.Fields[i]
		if r.Path == form+"."+input {
			return r
		}
		if r.Path == form && wide == nil {
			wide = r
		}
	}
	return wide
}

// enablesFields reports whether a read-only group switches any field on.
func (g *Group) enablesFields() bool {
	for _, r := range g.Fields {
		if r.Mode == ModeEditable {
			return true
		}
	}
	return false
}

// fieldAccess resolves what a group's page does with one input for this actor:
// whether it is shown, its mode there (editable or readonly) and whether a rule
// disables it right now. The group's own mode is the starting point.
func (e *Engine) fieldAccess(g *Group, form, input string, env map[string]any, actor Actor) (visible bool, mode string, ruleDisabled bool) {
	mode = orDefault(g.Mode, ModeEditable)
	if mode == ModeHidden {
		return false, mode, false
	}
	if mode != ModeEditable {
		mode = ModeReadonly
	}
	r := g.ruleFor(form, input)
	if r == nil {
		return true, mode, false
	}
	if r.Mode != "" {
		mode = r.Mode
	}
	if mode == ModeHidden || !roleAllows(actor, r.Roles) {
		return false, mode, false
	}
	if ok, err := e.cond(r.VisibleIf, env); err != nil || !ok {
		return false, mode, false
	}
	if mode == ModeEditable && !roleAllows(actor, r.EditRoles) {
		mode = ModeReadonly
	}
	if mode != ModeEditable {
		mode = ModeReadonly
	}
	if r.DisabledIf != "" {
		if ok, err := e.cond(r.DisabledIf, env); err == nil && ok {
			ruleDisabled = true
		}
	}
	return true, mode, ruleDisabled
}

// stageRequired reports whether a stage rule makes the input required now.
func (e *Engine) stageRequired(st *Stage, form, input string, env map[string]any) bool {
	if st.Page == nil {
		return false
	}
	for i := range st.Page.Groups {
		if r := st.Page.Groups[i].ruleFor(form, input); r != nil && r.RequiredIf != "" {
			if ok, err := e.cond(r.RequiredIf, env); err == nil && ok {
				return true
			}
		}
	}
	return false
}

// stageHides reports whether any page group of the stage hides the input for
// everyone (a rule with mode hidden).
func stageHides(st *Stage, form, input string) bool {
	if st == nil || st.Page == nil {
		return false
	}
	shown := false
	for i := range st.Page.Groups {
		g := &st.Page.Groups[i]
		if !slices.Contains(g.Forms, form) {
			continue
		}
		if r := g.ruleFor(form, input); r != nil && r.Mode == ModeHidden {
			continue
		}
		shown = true
	}
	return !shown && len(st.Page.Groups) > 0 && stageHasForm(st, form)
}

func stageHasForm(st *Stage, form string) bool {
	for _, g := range st.Page.Groups {
		if slices.Contains(g.Forms, form) {
			return true
		}
	}
	return false
}
