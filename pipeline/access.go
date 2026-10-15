package pipeline

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
