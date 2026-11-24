package pipeline

import "strings"

// FieldRow says how one input behaves on one stage's page: whether it is
// editable, read-only or hidden there, who sees it, who may change it, and the
// conditions that apply. The rows are the workflow's field access matrix.
type FieldRow struct {
	Stage      string   `json:"stage"`
	StageTitle string   `json:"stage_title"`
	Actors     string   `json:"actors"`
	Group      string   `json:"group"`
	Form       string   `json:"form"`
	Field      string   `json:"field"`
	Label      string   `json:"label"`
	Mode       string   `json:"mode"`
	Sees       string   `json:"sees"`
	Edits      string   `json:"edits"`
	Notes      []string `json:"notes,omitempty"`
}

func firstRoles(lists ...[]string) string {
	for _, l := range lists {
		if len(l) > 0 {
			return strings.Join(l, ", ")
		}
	}
	return ""
}

// FieldMatrix lists every input of every stage page with its effective mode
// and access, so authors (and auditors) can see at a glance how forms, fields
// and groups change from stage to stage.
func (e *Engine) FieldMatrix() []FieldRow {
	var out []FieldRow
	for _, st := range e.C.Def.Stages {
		if st.Page == nil {
			continue
		}
		actors := "workflow users"
		switch {
		case st.External != nil:
			actors = "outside party"
		case st.Public && len(st.Roles) > 0:
			actors = "applicant, " + strings.Join(st.Roles, ", ")
		case st.Public:
			actors = "applicant"
		case len(st.Roles) > 0:
			actors = strings.Join(st.Roles, ", ")
		}
		for _, g := range st.Page.Groups {
			for _, form := range g.Forms {
				cf := e.C.forms[form]
				if cf == nil {
					continue
				}
				for _, in := range cf.inputs {
					r := g.ruleFor(form, in.Name)
					mode := orDefault(g.Mode, ModeEditable)
					if mode != ModeEditable && mode != ModeHidden {
						mode = ModeReadonly
					}
					if r != nil && r.Mode != "" {
						mode = r.Mode
					}
					if in.Compute != "" && mode == ModeEditable {
						mode = ModeReadonly
					}
					var rr, re []string
					if r != nil {
						rr, re = r.Roles, r.EditRoles
					}
					sees := firstRoles(rr, in.ViewRoles, cf.ViewRoles, g.Roles)
					if sees == "" {
						sees = "anyone who can open the stage"
					}
					edits := firstRoles(re, in.EditRoles, cf.EditRoles, g.EditRoles)
					if edits == "" {
						edits = actors
					}
					if mode == ModeHidden {
						sees, edits = "nobody", "nobody"
					} else if mode != ModeEditable {
						edits = "nobody (read-only)"
					}
					label := orDefault(in.Label, in.Name)
					var notes []string
					add := func(what, expr string) {
						if expr != "" {
							notes = append(notes, what+" "+expr)
						}
					}
					add("visible if", in.VisibleIf)
					add("disabled if", in.DisabledIf)
					if in.Required {
						notes = append(notes, "required")
					} else {
						add("required if", in.RequiredIf)
					}
					if r != nil {
						label = orDefault(r.Label, label)
						add("stage: visible if", r.VisibleIf)
						add("stage: disabled if", r.DisabledIf)
						add("stage: required if", r.RequiredIf)
					}
					out = append(out, FieldRow{Stage: st.Name, StageTitle: titleOr(st.Title, st.Name), Actors: actors, Group: titleOr(g.Title, g.Name),
						Form: form, Field: in.Name, Label: label, Mode: mode, Sees: sees, Edits: edits, Notes: notes})
				}
			}
		}
	}
	return out
}
