package pipeline

import "slices"

// withWorkDefaults gives every stage the workflow's suspend, resume and
// information roles unless it sets its own, and switches information requests
// on when any role may make them. def is copied before any change.
func withWorkDefaults(def *Definition) *Definition {
	d := *def
	if def.Information != nil && len(def.InfoRoles) == 0 {
		d.InfoRoles = def.Information.Roles
		def = &d
	}
	d.Stages = slices.Clone(def.Stages)
	for i := range d.Stages {
		st := &d.Stages[i]
		if len(st.SuspendRoles) == 0 {
			st.SuspendRoles = def.SuspendRoles
		}
		if len(st.ResumeRoles) == 0 {
			st.ResumeRoles = def.ResumeRoles
		}
		if len(st.InfoRoles) == 0 && st.Information != nil {
			st.InfoRoles = st.Information.Roles
		}
		if len(st.InfoRoles) == 0 {
			st.InfoRoles = d.InfoRoles
		}
		if len(st.AdminRoles) == 0 {
			st.AdminRoles = def.AdminRoles
		}
		// Whoever takes a stand on a stage may also hand it to someone else.
		for _, r := range st.AdminRoles {
			if !slices.Contains(st.AssignRoles, r) {
				st.AssignRoles = append(slices.Clone(st.AssignRoles), r)
			}
		}
		if len(st.InfoRoles) > 0 {
			d.AllowRFI = true
		}
	}
	return &d
}
